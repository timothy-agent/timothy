package connectors

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// MicrosoftConfig is the connectors.config shape for kind='microsoft'.
// The client secret and the OAuth token bundle live in the secret
// store — config carries only their ref names.
type MicrosoftConfig struct {
	ClientID        string   `json:"client_id"`
	ClientSecretRef string   `json:"client_secret_ref"`
	Scopes          []string `json:"scopes"`
}

const (
	microsoftAuthURL = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
	//nolint:gosec // G101: the token ENDPOINT's URL, not a credential.
	microsoftTokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	graphBase         = "https://graph.microsoft.com/v1.0"

	// mailReadScope/mailSendScope/calendarsReadScope gate the mail and
	// calendar tool sets in Builder — exact matches, mirroring Google's
	// driveReadonlyScope/documentsScope.
	mailReadScope      = "Mail.Read"
	mailSendScope      = "Mail.Send"
	calendarsReadScope = "Calendars.Read"
	// offlineAccessScope must be granted or Microsoft issues no refresh
	// token — StartAuth appends it automatically so the operator never
	// has to know to add it.
	offlineAccessScope = "offline_access"
)

// Microsoft owns everything microsoft-kind: the OAuth dance (start URL,
// callback exchange, refresh) and the connector builder that surfaces
// Outlook mail/calendar tools. Endpoint fields default to Microsoft's;
// tests point them at fakes.
type Microsoft struct {
	Secrets   SecretRW
	Rows      rowSource
	Client    *http.Client
	PublicURL string
	Log       *slog.Logger

	AuthURL   string
	TokenURL  string
	GraphBase string
	// MarkItDownURL is the markitdown sidecar's base address; empty
	// disables attachment reading with a clear error.
	MarkItDownURL string

	mu     sync.Mutex
	states map[string]oauthState
}

// NewMicrosoft wires the microsoft kind. publicURL is required for the
// OAuth redirect; empty disables StartAuth with a clear error.
func NewMicrosoft(secrets SecretRW, rows rowSource, publicURL string, log *slog.Logger) *Microsoft {
	return &Microsoft{
		Secrets: secrets, Rows: rows, Client: &http.Client{}, PublicURL: publicURL, Log: log,
		AuthURL: microsoftAuthURL, TokenURL: microsoftTokenURL, GraphBase: graphBase,
		states: map[string]oauthState{},
	}
}

// RedirectURI is where Microsoft sends the browser back; the route is
// served unauthenticated by brain (the state token is the auth).
func (m *Microsoft) RedirectURI() string {
	return strings.TrimRight(m.PublicURL, "/") + "/v1/connectors/oauth/callback"
}

// StartAuth begins the OAuth dance for one microsoft connector and
// returns the URL to send the user's browser to.
func (m *Microsoft) StartAuth(ctx context.Context, connectorID string) (string, error) {
	if m.PublicURL == "" {
		return "", fmt.Errorf("TIMOTHY_PUBLIC_URL is not set; the OAuth redirect needs Timothy's public address")
	}
	c, err := m.Rows.Get(ctx, connectorID)
	if err != nil {
		return "", err
	}
	cfg, err := microsoftConfig(c)
	if err != nil {
		return "", err
	}
	if c.CredentialRef == "" {
		return "", fmt.Errorf("connector %s has no credential_ref to store the OAuth tokens under", c.Name)
	}

	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("oauth state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(buf)
	m.mu.Lock()
	for k, v := range m.states {
		if time.Now().After(v.expires) {
			delete(m.states, k)
		}
	}
	m.states[state] = oauthState{connectorID: connectorID, expires: time.Now().Add(oauthStateTTL)}
	m.mu.Unlock()

	// offline_access must be requested or Microsoft never issues a
	// refresh token — appended here so the operator never has to know.
	scopes := cfg.Scopes
	if !slices.Contains(scopes, offlineAccessScope) {
		scopes = append(append([]string{}, scopes...), offlineAccessScope)
	}
	q := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {m.RedirectURI()},
		"response_type": {"code"},
		"response_mode": {"query"},
		"scope":         {strings.Join(scopes, " ")},
		"state":         {state},
	}
	return m.AuthURL + "?" + q.Encode(), nil
}

// HasState reports whether state is a live (unconsumed, unexpired)
// OAuth state this Microsoft instance started — mirrors Google's
// HasState, letting the shared callback route pick which engine's
// HandleCallback to call without consuming the state itself.
func (m *Microsoft) HasState(state string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[state]
	return ok && time.Now().Before(st.expires)
}

// HandleCallback finishes the dance: validates state, exchanges the
// code, and stores the token bundle at the connector's credential_ref.
// Returns the connector name for the UI redirect.
func (m *Microsoft) HandleCallback(ctx context.Context, state, code string) (string, error) {
	m.mu.Lock()
	st, ok := m.states[state]
	delete(m.states, state)
	m.mu.Unlock()
	if !ok || time.Now().After(st.expires) {
		return "", fmt.Errorf("unknown or expired oauth state; restart the connection from Settings")
	}

	c, err := m.Rows.Get(ctx, st.connectorID)
	if err != nil {
		return "", err
	}
	cfg, err := microsoftConfig(c)
	if err != nil {
		return "", err
	}
	bundle, err := m.exchange(ctx, cfg, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {m.RedirectURI()},
	})
	if err != nil {
		return "", fmt.Errorf("code exchange: %w", err)
	}
	if bundle.RefreshToken == "" {
		return "", fmt.Errorf("microsoft returned no refresh token; remove Timothy's access at account.live.com/consent/Manage or myapps.microsoft.com and reconnect")
	}
	if err := m.storeBundle(ctx, c.CredentialRef, bundle); err != nil {
		return "", err
	}
	return c.Name, nil
}

// exchange posts to the token endpoint with client credentials and
// decodes a token response into a bundle.
func (m *Microsoft) exchange(ctx context.Context, cfg MicrosoftConfig, form url.Values) (tokenBundle, error) {
	secret, err := m.Secrets.Resolve(ctx, cfg.ClientSecretRef)
	if err != nil {
		return tokenBundle{}, fmt.Errorf("resolve client secret %q: %w", cfg.ClientSecretRef, err)
	}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", secret)
	// The token endpoint wants scope on every grant, refresh included.
	scopes := cfg.Scopes
	if !slices.Contains(scopes, offlineAccessScope) {
		scopes = append(append([]string{}, scopes...), offlineAccessScope)
	}
	form.Set("scope", strings.Join(scopes, " "))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenBundle{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.Client.Do(req)
	if err != nil {
		return tokenBundle{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return tokenBundle{}, microsoftTokenError(resp)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return tokenBundle{}, fmt.Errorf("decode token response: %w", err)
	}
	return tokenBundle{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
	}, nil
}

// microsoftOAuthErrorBody is the token endpoint's error shape
// (https://www.rfc-editor.org/rfc/rfc6749#section-5.2).
type microsoftOAuthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// microsoftTokenError maps a non-200 token endpoint response to a human
// message, never surfacing the raw JSON body. invalid_grant (expired
// or revoked refresh token) gets the reconnect-oriented message; other
// errors keep the status and Microsoft's error code, nothing more.
func microsoftTokenError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e microsoftOAuthErrorBody
	_ = json.Unmarshal(body, &e)
	if e.Error == "invalid_grant" {
		return fmt.Errorf("Microsoft authorization expired or was revoked — reconnect to re-authorize")
	}
	if e.Error != "" {
		return fmt.Errorf("Microsoft authorization failed (status %d, error %q) — reconnect to re-authorize", resp.StatusCode, e.Error)
	}
	return fmt.Errorf("Microsoft authorization failed (status %d)", resp.StatusCode)
}

func (m *Microsoft) storeBundle(ctx context.Context, ref string, b tokenBundle) error {
	//nolint:gosec // G117: serializing tokens INTO the encrypted secret store is this function's job.
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return m.Secrets.Set(ctx, ref, string(raw))
}

// token returns a live access token for the connector, refreshing and
// re-storing the bundle when it is about to expire. Microsoft rotates
// the refresh token on every refresh — the new one always replaces the
// old, falling back to the old only when the response omits it.
func (m *Microsoft) token(ctx context.Context, cfg MicrosoftConfig, ref string) (string, error) {
	raw, err := m.Secrets.Resolve(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("connector is not connected yet (no tokens at %q): %w", ref, err)
	}
	var b tokenBundle
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return "", fmt.Errorf("stored tokens at %q are not a bundle: %w", ref, err)
	}
	if time.Now().Before(b.Expiry.Add(-refreshSkew)) {
		return b.AccessToken, nil
	}

	fresh, err := m.exchange(ctx, cfg, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {b.RefreshToken},
	})
	if err != nil {
		return "", fmt.Errorf("token refresh: %w", err)
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = b.RefreshToken
	}
	if err := m.storeBundle(ctx, ref, fresh); err != nil {
		m.Log.Warn("refreshed microsoft token not persisted; will refresh again next call", "error", err)
	}
	return fresh.AccessToken, nil
}

func microsoftConfig(c Connector) (MicrosoftConfig, error) {
	var cfg MicrosoftConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return cfg, fmt.Errorf("microsoft %s: config: %w", c.Name, err)
	}
	if cfg.ClientID == "" || cfg.ClientSecretRef == "" {
		return cfg, fmt.Errorf("microsoft %s: config.client_id and config.client_secret_ref are required", c.Name)
	}
	if len(cfg.Scopes) == 0 {
		return cfg, fmt.Errorf("microsoft %s: config.scopes is required", c.Name)
	}
	return cfg, nil
}
