package connectors

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
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
	driveBase      = "https://www.googleapis.com/drive/v3"
	docsBase       = "https://docs.googleapis.com/v1"

	// driveReadonlyScope/documentsScope gate the Drive and Docs tool
	// sets in Builder — exact matches (see hasExactScope) since
	// drive.readonly and drive.file (Docs' scope) share a substring.
	driveReadonlyScope = "https://www.googleapis.com/auth/drive.readonly"
	documentsScope     = "https://www.googleapis.com/auth/documents"

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
	DriveBase    string
	DocsBase     string
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
		DriveBase: driveBase, DocsBase: docsBase,
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
		return tokenBundle{}, googleTokenError(resp)
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

// googleOAuthErrorBody is the token endpoint's error shape
// (https://www.rfc-editor.org/rfc/rfc6749#section-5.2).
type googleOAuthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// googleTokenError maps a non-200 token endpoint response to a human
// message, never surfacing the raw JSON body. invalid_grant (expired
// or revoked refresh token — Google's testing-mode apps hit this
// roughly weekly) gets the reconnect-oriented message; other errors
// keep the status and Google's error code, nothing more.
func googleTokenError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e googleOAuthErrorBody
	_ = json.Unmarshal(body, &e)
	if e.Error == "invalid_grant" {
		return fmt.Errorf("Google authorization expired or was revoked — reconnect to re-authorize. " +
			"(Testing-mode OAuth apps expire grants roughly weekly.)")
	}
	if e.Error != "" {
		return fmt.Errorf("Google authorization failed (status %d, error %q) — reconnect to re-authorize", resp.StatusCode, e.Error)
	}
	return fmt.Errorf("Google authorization failed (status %d)", resp.StatusCode)
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

// SendMail sends a plain-text email through connectorID's Gmail
// account — the same authed-token-plus-raw-MIME send gmailSend's tool
// uses (google_tools.go), reused directly here so destinations' email
// adapter and the chat tool can never diverge on how a message
// actually goes out. connectorID must name a google-kind connector
// with the gmail scope; callers (destinations' validation) check that
// before ever reaching here.
func (g *Google) SendMail(ctx context.Context, connectorID, to, subject, body string) error {
	c, err := g.Rows.Get(ctx, connectorID)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	cfg, err := googleConfig(c)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	if c.CredentialRef == "" {
		return fmt.Errorf("send mail: connector %s has no credential_ref", c.Name)
	}
	token, err := g.token(ctx, cfg, c.CredentialRef)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}

	var raw strings.Builder
	fmt.Fprintf(&raw, "To: %s\r\n", to)
	fmt.Fprintf(&raw, "Subject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", subject, body)
	payload := map[string]string{"raw": base64.URLEncoding.EncodeToString([]byte(raw.String()))}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.GmailBase+"/gmail/v1/users/me/messages/send", strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.Client.Do(req)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("send mail: gmail api status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// Attachment is one file to include on a SendMailWithAttachments call.
type Attachment struct {
	Name string
	Data []byte
}

// SendMailWithAttachments is SendMail plus a multipart/mixed body for
// destinations' artifact-file delivery — a separate method rather than
// an optional param on SendMail so the plain-text tool-call path
// (gmailSend) never builds MIME multipart machinery it doesn't need.
func (g *Google) SendMailWithAttachments(ctx context.Context, connectorID, to, subject, body string, attachments []Attachment) error {
	if len(attachments) == 0 {
		return g.SendMail(ctx, connectorID, to, subject, body)
	}
	c, err := g.Rows.Get(ctx, connectorID)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	cfg, err := googleConfig(c)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	if c.CredentialRef == "" {
		return fmt.Errorf("send mail: connector %s has no credential_ref", c.Name)
	}
	token, err := g.token(ctx, cfg, c.CredentialRef)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}

	var mime strings.Builder
	w := multipart.NewWriter(&mime)
	fmt.Fprintf(&mime, "To: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n", to, subject)
	fmt.Fprintf(&mime, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", w.Boundary())

	textPart, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=UTF-8"}})
	if err != nil {
		return fmt.Errorf("send mail: build text part: %w", err)
	}
	if _, err := textPart.Write([]byte(body)); err != nil {
		return fmt.Errorf("send mail: write text part: %w", err)
	}
	for _, a := range attachments {
		header := textproto.MIMEHeader{
			"Content-Type":              {"application/octet-stream"},
			"Content-Transfer-Encoding": {"base64"},
			"Content-Disposition":       {fmt.Sprintf(`attachment; filename=%q`, a.Name)},
		}
		part, err := w.CreatePart(header)
		if err != nil {
			return fmt.Errorf("send mail: build attachment part: %w", err)
		}
		if _, err := part.Write([]byte(base64.StdEncoding.EncodeToString(a.Data))); err != nil {
			return fmt.Errorf("send mail: write attachment part: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("send mail: close multipart: %w", err)
	}

	payload := map[string]string{"raw": base64.URLEncoding.EncodeToString([]byte(mime.String()))}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.GmailBase+"/gmail/v1/users/me/messages/send", strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.Client.Do(req)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("send mail: gmail api status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// SendMailHTML sends an HTML email — destinations' email adapter uses
// this for a mission's rendered markdown artifact content, so a
// digest reads as formatted prose in the inbox rather than a raw
// text/plain dump of markdown syntax. plainFallback is the same
// content as plain text, included as a multipart/alternative sibling
// part so a client with no HTML rendering (or a spam filter penalizing
// HTML-only mail) still shows something readable — never HTML-only.
// attachments wrap the alternative part in an outer multipart/mixed,
// same shape SendMailWithAttachments uses for its own single text part.
func (g *Google) SendMailHTML(ctx context.Context, connectorID, to, subject, plainFallback, htmlBody string, attachments []Attachment) error {
	c, err := g.Rows.Get(ctx, connectorID)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	cfg, err := googleConfig(c)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	if c.CredentialRef == "" {
		return fmt.Errorf("send mail: connector %s has no credential_ref", c.Name)
	}
	token, err := g.token(ctx, cfg, c.CredentialRef)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}

	var mime strings.Builder
	fmt.Fprintf(&mime, "To: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n", to, subject)

	if len(attachments) == 0 {
		altW := multipart.NewWriter(&mime)
		fmt.Fprintf(&mime, "Content-Type: multipart/alternative; boundary=%s\r\n\r\n", altW.Boundary())
		if err := writeAlternativeParts(altW, plainFallback, htmlBody); err != nil {
			return fmt.Errorf("send mail: %w", err)
		}
	} else {
		mixedW := multipart.NewWriter(&mime)
		fmt.Fprintf(&mime, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", mixedW.Boundary())
		altPart, err := mixedW.CreatePart(textproto.MIMEHeader{"Content-Type": {fmt.Sprintf("multipart/alternative; boundary=%s", multipartAltBoundary)}})
		if err != nil {
			return fmt.Errorf("send mail: build alternative part: %w", err)
		}
		altW := multipart.NewWriter(altPart)
		if err := altW.SetBoundary(multipartAltBoundary); err != nil {
			return fmt.Errorf("send mail: set boundary: %w", err)
		}
		if err := writeAlternativeParts(altW, plainFallback, htmlBody); err != nil {
			return fmt.Errorf("send mail: %w", err)
		}
		for _, a := range attachments {
			header := textproto.MIMEHeader{
				"Content-Type":              {"application/octet-stream"},
				"Content-Transfer-Encoding": {"base64"},
				"Content-Disposition":       {fmt.Sprintf(`attachment; filename=%q`, a.Name)},
			}
			part, err := mixedW.CreatePart(header)
			if err != nil {
				return fmt.Errorf("send mail: build attachment part: %w", err)
			}
			if _, err := part.Write([]byte(base64.StdEncoding.EncodeToString(a.Data))); err != nil {
				return fmt.Errorf("send mail: write attachment part: %w", err)
			}
		}
		if err := mixedW.Close(); err != nil {
			return fmt.Errorf("send mail: close multipart: %w", err)
		}
	}

	payload := map[string]string{"raw": base64.URLEncoding.EncodeToString([]byte(mime.String()))}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.GmailBase+"/gmail/v1/users/me/messages/send", strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.Client.Do(req)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("send mail: gmail api status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// multipartAltBoundary is a fixed boundary for the inner
// multipart/alternative part nested inside multipart/mixed —
// multipart.Writer.SetBoundary requires a value already conforming to
// RFC 2046 (1-70 chars from a fixed alphabet), so a fixed, sufficiently
// unlikely-to-collide string is simpler than generating one; the outer
// mixed boundary (auto-generated by CreatePart's own writer) is what
// actually has to be unique against the message body, and always is.
const multipartAltBoundary = "timothy-alt-boundary-7f3a9c"

// writeAlternativeParts writes the plain-text then HTML parts of a
// multipart/alternative body, in that order — RFC 2046 requires the
// simplest/most-compatible representation first, with the richest
// (HTML) last, so a client that only renders the last part it
// understands shows HTML when it can.
func writeAlternativeParts(w *multipart.Writer, plainFallback, htmlBody string) error {
	textPart, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=UTF-8"}})
	if err != nil {
		return fmt.Errorf("build text part: %w", err)
	}
	if _, err := textPart.Write([]byte(plainFallback)); err != nil {
		return fmt.Errorf("write text part: %w", err)
	}
	htmlPart, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/html; charset=UTF-8"}})
	if err != nil {
		return fmt.Errorf("build html part: %w", err)
	}
	if _, err := htmlPart.Write([]byte(htmlBody)); err != nil {
		return fmt.Errorf("write html part: %w", err)
	}
	return w.Close()
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
