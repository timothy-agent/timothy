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
	"strings"
	"sync"
	"time"
)

// GoogleConfig is the connectors.config shape for kind='google'. The
// client secret and the OAuth token bundle live in the secret store —
// config carries only their ref names.
type GoogleConfig struct {
	ClientID        string   `json:"client_id"`
	ClientSecretRef string   `json:"client_secret_ref"`
	Scopes          []string `json:"scopes"`
}

// tokenBundle is what the OAuth dance stores at the connector's
// credential_ref: JSON, envelope-encrypted by the secret store.
type tokenBundle struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

// SecretRW is the secret-store slice Google needs: read refs and
// write refreshed token bundles back.
type SecretRW interface {
	Resolve(ctx context.Context, refName string) (string, error)
	Set(ctx context.Context, refName, value string) error
}

const (
	googleAuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	//nolint:gosec // G101: the token ENDPOINT's URL, not a credential.
	googleTokenURL = "https://oauth2.googleapis.com/token"
	gmailBase      = "https://gmail.googleapis.com"
	calendarBase   = "https://www.googleapis.com/calendar/v3"

	// oauthStateTTL bounds how long a started OAuth dance may take.
	oauthStateTTL = 10 * time.Minute
	// refreshSkew renews an access token this long before its expiry.
	refreshSkew = 2 * time.Minute
)

// Google owns everything google-kind: the OAuth dance (start URL,
// callback exchange, refresh) and the connector builder that surfaces
// Gmail/Calendar tools. Endpoint fields default to Google's; tests
// point them at fakes.
type Google struct {
	Secrets   SecretRW
	Rows      rowSource // connector lookup for the OAuth dance
	Client    *http.Client
	PublicURL string // Timothy's public base URL, for the redirect URI
	Log       *slog.Logger

	AuthURL      string
	TokenURL     string
	GmailBase    string
	CalendarBase string
	// MarkItDownURL is the markitdown sidecar's base address (compose-
	// internal, e.g. http://markitdown:8000); empty disables HTML-only
	// body rendering and PDF attachment reading with a clear error
	// instead of a silent snippet fallback.
	MarkItDownURL string

	mu     sync.Mutex
	states map[string]oauthState
}

type oauthState struct {
	connectorID string
	expires     time.Time
}

// NewGoogle wires the google kind. publicURL is required for the
// OAuth redirect; empty disables StartAuth with a clear error.
func NewGoogle(secrets SecretRW, rows rowSource, publicURL string, log *slog.Logger) *Google {
	return &Google{
		Secrets: secrets, Rows: rows, Client: &http.Client{}, PublicURL: publicURL, Log: log,
		AuthURL: googleAuthURL, TokenURL: googleTokenURL,
		GmailBase: gmailBase, CalendarBase: calendarBase,
		states: map[string]oauthState{},
	}
}

// RedirectURI is where Google sends the browser back; the route is
// served unauthenticated by brain (the state token is the auth).
func (g *Google) RedirectURI() string {
	return strings.TrimRight(g.PublicURL, "/") + "/v1/connectors/oauth/callback"
}

// StartAuth begins the OAuth dance for one google connector and
// returns the URL to send the user's browser to.
func (g *Google) StartAuth(ctx context.Context, connectorID string) (string, error) {
	if g.PublicURL == "" {
		return "", fmt.Errorf("TIMOTHY_PUBLIC_URL is not set; the OAuth redirect needs Timothy's public address")
	}
	c, err := g.Rows.Get(ctx, connectorID)
	if err != nil {
		return "", err
	}
	cfg, err := googleConfig(c)
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
	g.mu.Lock()
	// Sweep expired states while we're here; the map stays tiny.
	for k, v := range g.states {
		if time.Now().After(v.expires) {
			delete(g.states, k)
		}
	}
	g.states[state] = oauthState{connectorID: connectorID, expires: time.Now().Add(oauthStateTTL)}
	g.mu.Unlock()

	q := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {g.RedirectURI()},
		"response_type": {"code"},
		"scope":         {strings.Join(cfg.Scopes, " ")},
		"state":         {state},
		// offline + consent: Google only issues a refresh token on a
		// consented offline grant — without these the connector dies
		// when the first access token expires.
		"access_type": {"offline"},
		"prompt":      {"consent"},
	}
	return g.AuthURL + "?" + q.Encode(), nil
}

// HandleCallback finishes the dance: validates state, exchanges the
// code, and stores the token bundle at the connector's credential_ref.
// Returns the connector name for the UI redirect.
func (g *Google) HandleCallback(ctx context.Context, state, code string) (string, error) {
	g.mu.Lock()
	st, ok := g.states[state]
	delete(g.states, state)
	g.mu.Unlock()
	if !ok || time.Now().After(st.expires) {
		return "", fmt.Errorf("unknown or expired oauth state; restart the connection from Settings")
	}

	c, err := g.Rows.Get(ctx, st.connectorID)
	if err != nil {
		return "", err
	}
	cfg, err := googleConfig(c)
	if err != nil {
		return "", err
	}
	bundle, err := g.exchange(ctx, cfg, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {g.RedirectURI()},
	})
	if err != nil {
		return "", fmt.Errorf("code exchange: %w", err)
	}
	if bundle.RefreshToken == "" {
		return "", fmt.Errorf("google returned no refresh token; remove Timothy's access at myaccount.google.com/permissions and reconnect")
	}
	if err := g.storeBundle(ctx, c.CredentialRef, bundle); err != nil {
		return "", err
	}
	return c.Name, nil
}

// exchange posts to the token endpoint with client credentials and
// decodes a token response into a bundle.
func (g *Google) exchange(ctx context.Context, cfg GoogleConfig, form url.Values) (tokenBundle, error) {
	secret, err := g.Secrets.Resolve(ctx, cfg.ClientSecretRef)
	if err != nil {
		return tokenBundle{}, fmt.Errorf("resolve client secret %q: %w", cfg.ClientSecretRef, err)
	}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", secret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenBundle{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.Client.Do(req)
	if err != nil {
		return tokenBundle{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return tokenBundle{}, fmt.Errorf("token endpoint status %d: %s", resp.StatusCode, snippet)
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

func (g *Google) storeBundle(ctx context.Context, ref string, b tokenBundle) error {
	//nolint:gosec // G117: serializing tokens INTO the encrypted secret store is this function's job.
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return g.Secrets.Set(ctx, ref, string(raw))
}

// token returns a live access token for the connector, refreshing and
// re-storing the bundle when it is about to expire.
func (g *Google) token(ctx context.Context, cfg GoogleConfig, ref string) (string, error) {
	raw, err := g.Secrets.Resolve(ctx, ref)
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

	fresh, err := g.exchange(ctx, cfg, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {b.RefreshToken},
	})
	if err != nil {
		return "", fmt.Errorf("token refresh: %w", err)
	}
	// Google omits the refresh token on refresh grants; keep the old one.
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = b.RefreshToken
	}
	if err := g.storeBundle(ctx, ref, fresh); err != nil {
		g.Log.Warn("refreshed google token not persisted; will refresh again next call", "error", err)
	}
	return fresh.AccessToken, nil
}

func googleConfig(c Connector) (GoogleConfig, error) {
	var cfg GoogleConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return cfg, fmt.Errorf("google %s: config: %w", c.Name, err)
	}
	if cfg.ClientID == "" || cfg.ClientSecretRef == "" {
		return cfg, fmt.Errorf("google %s: config.client_id and config.client_secret_ref are required", c.Name)
	}
	if len(cfg.Scopes) == 0 {
		return cfg, fmt.Errorf("google %s: config.scopes is required", c.Name)
	}
	return cfg, nil
}
