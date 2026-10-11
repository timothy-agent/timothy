package connectors

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/netguard"
)

const (
	// mcpMetadataMax caps every OAuth response body read during
	// discovery, registration and token calls.
	mcpMetadataMax = 64 << 10
	// mcpOAuthCallTimeout bounds one discovery, registration or token
	// round trip.
	mcpOAuthCallTimeout = 20 * time.Second
	// mcpClientSecretSuffix names the ref a dynamically registered
	// client's secret is stored under: <credential_ref>_CLIENT.
	mcpClientSecretSuffix = "_CLIENT"
	// mcpDefaultScopes labels the identity line when the server
	// advertised no scopes and the login requested none.
	mcpDefaultScopes = "default scopes"
)

var (
	// errMCPUnsafeURL rejects a discovered URL that is not https or
	// sits on a host the endpoint did not vouch for.
	errMCPUnsafeURL = errors.New("unsafe oauth url")
	// errMCPMetadata is any malformed or missing discovery document.
	errMCPMetadata = errors.New("invalid oauth metadata")
	// errMCPNoAuthNeeded: the endpoint answered without credentials.
	errMCPNoAuthNeeded = errors.New("server answered without authorization; use bearer token mode")
	// errMCPNoClient: no registration endpoint and no pasted client id.
	errMCPNoClient = errors.New("authorization server has no dynamic client registration; paste a client id and secret")
	// errMCPNotConnected: oauth mode before the first completed login.
	errMCPNotConnected = errors.New("not connected yet; press Connect to sign in")
	// errMCPReconnect: the authorization server rejected a code or
	// refresh token, or there is no refresh token to use.
	errMCPReconnect = errors.New("authorization expired or was revoked; reconnect to re-authorize")
)

// MCPAuth runs the MCP authorization flow (2025-06-18 revision) for
// kind='mcp' connectors in oauth auth mode. D-151: discovery
// (protected-resource then authorization-server metadata), dynamic
// client registration when no client id is configured, PKCE S256 with
// the RFC 8707 resource parameter, and refresh before expiry. Tokens
// live at the connector's credential_ref as a tokenBundle, like
// google's; discovered values are written into config at callback so
// refresh never rediscovers. Every call goes through Client (netguard
// in production) and follows no redirects.
type MCPAuth struct {
	Secrets   SecretRW
	Rows      rowSource
	Client    *http.Client
	PublicURL string
	Log       *slog.Logger

	mu        sync.Mutex
	states    map[string]mcpAuthState
	refreshes refreshLocks
}

// mcpAuthState is one started login: the PKCE verifier and the config
// the callback persists once the code exchange succeeds.
type mcpAuthState struct {
	connectorID string
	verifier    string
	cfg         mcpConfig
	expires     time.Time
}

// NewMCPAuth wires the engine. A nil client dials through netguard
// with no allowlist. publicURL empty disables StartAuth.
func NewMCPAuth(secrets SecretRW, rows rowSource, publicURL string, client *http.Client, log *slog.Logger) *MCPAuth {
	if client == nil {
		client = &http.Client{Transport: netguard.Guard{}.Transport()}
	}
	return &MCPAuth{
		Secrets: secrets, Rows: rows, Client: client, PublicURL: publicURL, Log: log,
		states: map[string]mcpAuthState{},
	}
}

// Builder is MCPBuilder over a.Client that also serves oauth mode.
func (a *MCPAuth) Builder(deferral MCPDeferral) Builder {
	return mcpBuilder(a.Client, deferral, a)
}

// RedirectURI is the callback route shared with google and microsoft.
func (a *MCPAuth) RedirectURI() string {
	return strings.TrimRight(a.PublicURL, "/") + "/v1/connectors/oauth/callback"
}

// bind gives an oauth-mode source its token source and identity.
func (a *MCPAuth) bind(src *mcpSource, ref string) error {
	if a == nil {
		return fmt.Errorf("oauth auth mode is unavailable in this build")
	}
	if ref == "" {
		return fmt.Errorf("oauth auth mode needs a credential_ref to store tokens under")
	}
	cfg := src.cfg
	if cfg.TokenEndpoint == "" || cfg.ClientID == "" {
		return errMCPNotConnected
	}
	src.auth = func(ctx context.Context, rejected string) (string, error) {
		return a.token(ctx, cfg, ref, rejected)
	}
	scopes := strings.Join(cfg.Scopes, ", ")
	if scopes == "" {
		scopes = mcpDefaultScopes
	}
	src.identity = &GitHubIdentity{Login: cfg.AuthServer, Scopes: scopes}
	return nil
}

// StartAuth discovers the server's authorization server, registers a
// client when needed, and returns the consent URL. Discovery refusals
// wrap ErrUnsupported so the operator sees the reason.
func (a *MCPAuth) StartAuth(ctx context.Context, connectorID string) (string, error) {
	if a.PublicURL == "" {
		return "", fmt.Errorf("TIMOTHY_PUBLIC_URL is not set; the OAuth redirect needs Timothy's public address")
	}
	c, err := a.Rows.Get(ctx, connectorID)
	if err != nil {
		return "", err
	}
	var cfg mcpConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return "", fmt.Errorf("mcp %s: config: %w: %w", c.Name, err, ErrInvalid)
	}
	mode, err := cfg.authMode()
	if err != nil {
		return "", fmt.Errorf("mcp %s: %w: %w", c.Name, err, ErrInvalid)
	}
	if mode != mcpAuthOAuth {
		return "", fmt.Errorf("mcp %s uses bearer token auth; set auth mode to oauth to sign in: %w", c.Name, ErrUnsupported)
	}
	if c.CredentialRef == "" {
		return "", fmt.Errorf("connector %s has no credential_ref to store the OAuth tokens under: %w", c.Name, ErrInvalid)
	}

	d, err := a.discover(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("mcp %s: oauth login refused: %w: %w", c.Name, err, ErrUnsupported)
	}
	next, err := a.ensureClient(ctx, c.CredentialRef, cfg, d)
	if err != nil {
		return "", fmt.Errorf("mcp %s: oauth login refused: %w: %w", c.Name, err, ErrUnsupported)
	}
	next.AuthServer = d.issuer
	next.TokenEndpoint = d.as.TokenEndpoint
	next.Resource = d.resource
	next.Scopes = d.scopes

	verifier, err := randomURLToken(32)
	if err != nil {
		return "", fmt.Errorf("pkce verifier: %w", err)
	}
	state, err := randomURLToken(24)
	if err != nil {
		return "", fmt.Errorf("oauth state: %w", err)
	}
	a.mu.Lock()
	for k, v := range a.states {
		if time.Now().After(v.expires) {
			delete(a.states, k)
		}
	}
	a.states[state] = mcpAuthState{connectorID: connectorID, verifier: verifier, cfg: next, expires: time.Now().Add(oauthStateTTL)}
	a.mu.Unlock()

	return authorizeURL(d.as.AuthorizationEndpoint, next, a.RedirectURI(), state, pkceChallenge(verifier))
}

// authorizeURL composes the consent URL, keeping any query the
// authorization endpoint already carries.
func authorizeURL(endpoint string, cfg mcpConfig, redirectURI, state, challenge string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("authorization_endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("resource", cfg.Resource)
	if len(cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// HasState reports whether state is a live login this engine started,
// without consuming it, so the shared callback route can dispatch.
func (a *MCPAuth) HasState(state string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.states[state]
	return ok && time.Now().Before(st.expires)
}

// HandleCallback exchanges the code, stores the token bundle at
// credential_ref, and persists the discovered config. Returns the
// connector name for the UI redirect.
func (a *MCPAuth) HandleCallback(ctx context.Context, state, code string) (string, error) {
	a.mu.Lock()
	st, ok := a.states[state]
	delete(a.states, state)
	a.mu.Unlock()
	if !ok || time.Now().After(st.expires) {
		return "", fmt.Errorf("unknown or expired oauth state; restart the connection from Settings")
	}
	c, err := a.Rows.Get(ctx, st.connectorID)
	if err != nil {
		return "", err
	}
	bundle, scope, err := a.exchange(ctx, st.cfg, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {a.RedirectURI()},
		"code_verifier": {st.verifier},
	})
	if err != nil {
		return "", fmt.Errorf("code exchange: %w", err)
	}
	if err := a.storeBundle(ctx, c.CredentialRef, bundle); err != nil {
		return "", err
	}
	cfg := st.cfg
	if scope != "" {
		cfg.Scopes = strings.Fields(scope)
	}
	if err := a.persistConfig(ctx, c, cfg); err != nil {
		return "", err
	}
	return c.Name, nil
}

// persistConfig writes the oauth fields into the row's config,
// leaving every other key (endpoint, headers) as stored.
func (a *MCPAuth) persistConfig(ctx context.Context, c Connector, cfg mcpConfig) error {
	patcher, ok := a.Rows.(rowPatcher)
	if !ok {
		return fmt.Errorf("connector store cannot persist the oauth config")
	}
	m := map[string]json.RawMessage{}
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, &m); err != nil {
			return fmt.Errorf("mcp %s: config: %w", c.Name, err)
		}
	}
	fields := map[string]any{
		"client_id":         cfg.ClientID,
		"client_secret_ref": cfg.ClientSecretRef,
		"client_registered": cfg.ClientRegistered,
		"auth_server":       cfg.AuthServer,
		"token_endpoint":    cfg.TokenEndpoint,
		"token_auth_method": cfg.TokenAuthMethod,
		"scopes":            cfg.Scopes,
		"resource":          cfg.Resource,
	}
	for k, v := range fields {
		if reflect.ValueOf(v).IsZero() {
			delete(m, k)
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("mcp %s: config %s: %w", c.Name, k, err)
		}
		m[k] = raw
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("mcp %s: config: %w", c.Name, err)
	}
	msg := json.RawMessage(raw)
	if err := patcher.Patch(ctx, c.ID, Patch{Config: &msg}); err != nil {
		return fmt.Errorf("mcp %s: persist oauth config: %w", c.Name, err)
	}
	return nil
}

// token returns a live access token, refreshing when the bundle is
// within refreshSkew of expiry or when rejected names the token the
// server just refused. A bundle without expiry is used until refused.
// Held under ref's refresh lock end to end (D-112).
func (a *MCPAuth) token(ctx context.Context, cfg mcpConfig, ref, rejected string) (string, error) {
	defer a.refreshes.lock(ref).Unlock()
	raw, err := a.Secrets.Resolve(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("%w (no tokens at %q): %w", errMCPNotConnected, ref, err)
	}
	var b tokenBundle
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return "", fmt.Errorf("stored tokens at %q are not a bundle: %w", ref, err)
	}
	live := b.Expiry.IsZero() || time.Now().Before(b.Expiry.Add(-refreshSkew))
	if live && (rejected == "" || rejected != b.AccessToken) {
		return b.AccessToken, nil
	}
	if b.RefreshToken == "" {
		return "", fmt.Errorf("token refresh: no refresh token: %w", errMCPReconnect)
	}
	fresh, _, err := a.exchange(ctx, cfg, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {b.RefreshToken},
	})
	if err != nil {
		return "", fmt.Errorf("token refresh: %w", err)
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = b.RefreshToken
	}
	if err := a.storeBundle(ctx, ref, fresh); err != nil {
		a.Log.Warn("refreshed mcp token not persisted; will refresh again next call", "error", err)
	}
	return fresh.AccessToken, nil
}

func (a *MCPAuth) storeBundle(ctx context.Context, ref string, b tokenBundle) error {
	//nolint:gosec // G117: serializing tokens INTO the encrypted secret store is this function's job.
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return a.Secrets.Set(ctx, ref, string(raw))
}

// exchange posts form to the token endpoint with the configured
// client authentication and the resource indicator. Returns the
// bundle and the granted scope, if the server reported one.
func (a *MCPAuth) exchange(ctx context.Context, cfg mcpConfig, form url.Values) (tokenBundle, string, error) {
	form.Set("resource", cfg.Resource)
	var basicSecret string
	switch cfg.TokenAuthMethod {
	case "client_secret_basic", "client_secret_post":
		secret, err := a.Secrets.Resolve(ctx, cfg.ClientSecretRef)
		if err != nil {
			return tokenBundle{}, "", fmt.Errorf("resolve client secret %q: %w", cfg.ClientSecretRef, err)
		}
		if cfg.TokenAuthMethod == "client_secret_post" {
			form.Set("client_id", cfg.ClientID)
			form.Set("client_secret", secret)
		} else {
			basicSecret = secret
		}
	default:
		form.Set("client_id", cfg.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenBundle{}, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basicSecret != "" {
		// RFC 6749 section 2.3.1: both halves form-encoded first.
		req.SetBasicAuth(url.QueryEscape(cfg.ClientID), url.QueryEscape(basicSecret))
	}
	resp, body, err := a.do(req)
	if err != nil {
		return tokenBundle{}, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return tokenBundle{}, "", mcpTokenError(resp.StatusCode, body)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return tokenBundle{}, "", fmt.Errorf("decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return tokenBundle{}, "", fmt.Errorf("token response has no access_token")
	}
	if out.TokenType != "" && !strings.EqualFold(out.TokenType, "bearer") {
		return tokenBundle{}, "", fmt.Errorf("token response type %q is not bearer", out.TokenType)
	}
	b := tokenBundle{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken}
	if out.ExpiresIn > 0 {
		b.Expiry = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return b, out.Scope, nil
}

// mcpTokenError maps a token endpoint failure to a short message. A
// 400 or 401 (RFC 6749 section 5.2) means the grant or client is no
// longer good: reconnect. Anything else may be transient.
func mcpTokenError(status int, body []byte) error {
	var e googleOAuthErrorBody
	_ = json.Unmarshal(body, &e)
	if status == http.StatusBadRequest || status == http.StatusUnauthorized {
		if e.Error != "" {
			return fmt.Errorf("%w (status %d, error %q)", errMCPReconnect, status, e.Error)
		}
		return fmt.Errorf("%w (status %d)", errMCPReconnect, status)
	}
	return fmt.Errorf("token endpoint status %d", status)
}

// mcpDiscovery is what StartAuth learned about the server.
type mcpDiscovery struct {
	issuer   string
	resource string
	scopes   []string
	as       mcpASMetadata
}

// mcpPRM is RFC 9728 protected-resource metadata, the fields used.
type mcpPRM struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// mcpASMetadata is RFC 8414 authorization-server metadata, the fields
// used.
type mcpASMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// discover runs protected-resource then authorization-server
// discovery for cfg.Endpoint, enforcing the scheme and host guards.
func (a *MCPAuth) discover(ctx context.Context, cfg mcpConfig) (mcpDiscovery, error) {
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return mcpDiscovery{}, fmt.Errorf("config.endpoint: %w", err)
	}
	challenge, err := a.challenge(ctx, cfg)
	if err != nil {
		return mcpDiscovery{}, err
	}

	var prm *mcpPRM
	if raw := challenge["resource_metadata"]; raw != "" {
		u, err := checkOAuthURL(raw, endpoint.Hostname())
		if err != nil {
			return mcpDiscovery{}, fmt.Errorf("resource_metadata: %w", err)
		}
		var doc mcpPRM
		if err := a.getJSON(ctx, u.String(), &doc); err != nil {
			return mcpDiscovery{}, err
		}
		prm = &doc
	} else {
		for _, candidate := range prmURLs(endpoint) {
			var doc mcpPRM
			if err := a.getJSON(ctx, candidate, &doc); err == nil {
				prm = &doc
				break
			}
		}
	}

	d := mcpDiscovery{resource: canonicalResource(endpoint)}
	if prm != nil {
		if prm.Resource != "" {
			if !resourceCovers(prm.Resource, endpoint) {
				return mcpDiscovery{}, fmt.Errorf("%w: protected resource %q does not cover endpoint %q", errMCPMetadata, prm.Resource, cfg.Endpoint)
			}
			d.resource = prm.Resource
		}
		if len(prm.AuthorizationServers) == 0 {
			return mcpDiscovery{}, fmt.Errorf("%w: protected resource metadata lists no authorization_servers", errMCPMetadata)
		}
		d.issuer = prm.AuthorizationServers[0]
		d.scopes = prm.ScopesSupported
	} else {
		// Servers predating protected-resource metadata serve
		// authorization-server metadata on their own origin.
		d.issuer = endpoint.Scheme + "://" + endpoint.Host
	}
	if s := challenge["scope"]; s != "" {
		d.scopes = strings.Fields(s)
	}

	issuer, err := checkOAuthURL(d.issuer, "")
	if err != nil {
		return mcpDiscovery{}, fmt.Errorf("authorization server: %w", err)
	}
	d.as, err = a.fetchASMetadata(ctx, issuer)
	if err != nil {
		return mcpDiscovery{}, err
	}
	return d, nil
}

// challenge sends an unauthenticated initialize and returns the 401's
// Bearer challenge parameters. Any other non-2xx answer yields none,
// leaving discovery to the well-known fallbacks.
func (a *MCPAuth) challenge(ctx context.Context, cfg mcpConfig) (map[string]string, error) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "timothy", "version": "1"},
		},
	})
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, mcpOAuthCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range cfg.Headers {
		if !strings.EqualFold(k, "Authorization") {
			req.Header.Set(k, v)
		}
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe endpoint: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil, errMCPNoAuthNeeded
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return map[string]string{}, nil
	}
	return parseBearerChallenge(resp.Header.Values("WWW-Authenticate")), nil
}

// fetchASMetadata tries the RFC 8414 and OpenID discovery locations
// for issuer and validates the first document found.
func (a *MCPAuth) fetchASMetadata(ctx context.Context, issuer *url.URL) (mcpASMetadata, error) {
	var lastErr error
	for _, candidate := range asMetadataURLs(issuer) {
		var md mcpASMetadata
		if err := a.getJSON(ctx, candidate, &md); err != nil {
			lastErr = err
			continue
		}
		if err := md.validate(issuer.String()); err != nil {
			return mcpASMetadata{}, err
		}
		return md, nil
	}
	return mcpASMetadata{}, fmt.Errorf("no authorization server metadata for %s: %w", issuer, lastErr)
}

// validate checks RFC 8414 section 3.3's issuer match, the endpoint
// guards, and PKCE S256 support.
func (md mcpASMetadata) validate(issuer string) error {
	if strings.TrimRight(md.Issuer, "/") != strings.TrimRight(issuer, "/") {
		return fmt.Errorf("%w: issuer %q does not match %q", errMCPMetadata, md.Issuer, issuer)
	}
	if md.AuthorizationEndpoint == "" || md.TokenEndpoint == "" {
		return fmt.Errorf("%w: authorization_endpoint and token_endpoint are required", errMCPMetadata)
	}
	for name, raw := range map[string]string{
		"authorization_endpoint": md.AuthorizationEndpoint,
		"token_endpoint":         md.TokenEndpoint,
		"registration_endpoint":  md.RegistrationEndpoint,
	} {
		if raw == "" {
			continue
		}
		if _, err := checkOAuthURL(raw, ""); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if !slices.Contains(md.CodeChallengeMethodsSupported, "S256") {
		return fmt.Errorf("%w: authorization server does not support PKCE S256", errMCPMetadata)
	}
	return nil
}

// ensureClient returns cfg with a usable client id and token auth
// method: the operator's pasted client, a client registered earlier
// with the same issuer, or a fresh RFC 7591 registration.
func (a *MCPAuth) ensureClient(ctx context.Context, ref string, cfg mcpConfig, d mcpDiscovery) (mcpConfig, error) {
	switch {
	case cfg.ClientID != "" && !cfg.ClientRegistered:
		cfg.TokenAuthMethod = "none"
		if cfg.ClientSecretRef != "" {
			cfg.TokenAuthMethod = secretAuthMethod(d.as.TokenEndpointAuthMethodsSupported)
		}
		return cfg, nil
	case cfg.ClientID != "" && cfg.ClientRegistered && cfg.AuthServer == d.issuer:
		return cfg, nil
	}
	if d.as.RegistrationEndpoint == "" {
		return cfg, errMCPNoClient
	}
	reg, err := a.register(ctx, d.as.RegistrationEndpoint)
	if err != nil {
		return cfg, err
	}
	cfg.ClientID = reg.ClientID
	cfg.ClientRegistered = true
	cfg.ClientSecretRef = ""
	cfg.TokenAuthMethod = "none"
	if reg.ClientSecret != "" {
		secretRef := ref + mcpClientSecretSuffix
		if err := a.Secrets.Set(ctx, secretRef, reg.ClientSecret); err != nil {
			return cfg, fmt.Errorf("store registered client secret: %w", err)
		}
		cfg.ClientSecretRef = secretRef
		cfg.TokenAuthMethod = reg.TokenEndpointAuthMethod
		if cfg.TokenAuthMethod != "client_secret_post" {
			cfg.TokenAuthMethod = "client_secret_basic"
		}
	}
	return cfg, nil
}

// secretAuthMethod picks how a confidential client authenticates:
// basic unless the server lists only post (RFC 8414 defaults to basic).
func secretAuthMethod(supported []string) string {
	if slices.Contains(supported, "client_secret_post") && !slices.Contains(supported, "client_secret_basic") {
		return "client_secret_post"
	}
	return "client_secret_basic"
}

// mcpRegistration is an RFC 7591 registration response, the fields
// used.
type mcpRegistration struct {
	ClientID                string `json:"client_id"`
	ClientSecret            string `json:"client_secret"`
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
}

// register posts a public-client registration for the redirect URI.
func (a *MCPAuth) register(ctx context.Context, endpoint string) (mcpRegistration, error) {
	body, err := json.Marshal(mcpRegistrationRequest(a.RedirectURI()))
	if err != nil {
		return mcpRegistration{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return mcpRegistration{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, raw, err := a.do(req)
	if err != nil {
		return mcpRegistration{}, fmt.Errorf("client registration: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var e googleOAuthErrorBody
		_ = json.Unmarshal(raw, &e)
		if e.Error != "" {
			return mcpRegistration{}, fmt.Errorf("client registration refused (status %d, error %q)", resp.StatusCode, e.Error)
		}
		return mcpRegistration{}, fmt.Errorf("client registration refused (status %d)", resp.StatusCode)
	}
	var reg mcpRegistration
	if err := json.Unmarshal(raw, &reg); err != nil {
		return mcpRegistration{}, fmt.Errorf("decode client registration: %w", err)
	}
	if reg.ClientID == "" {
		return mcpRegistration{}, fmt.Errorf("%w: client registration returned no client_id", errMCPMetadata)
	}
	return reg, nil
}

// mcpRegistrationRequest is the RFC 7591 body: a public client using
// the authorization code grant with refresh.
func mcpRegistrationRequest(redirectURI string) map[string]any {
	return map[string]any{
		"client_name":                "Timothy",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
}

// getJSON fetches one metadata document into out. Any non-200 answer
// is an error, so callers walking fallbacks can move on.
func (a *MCPAuth) getJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	resp, body, err := a.do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s answered status %d", errMCPMetadata, rawURL, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: %s: %w", errMCPMetadata, rawURL, err)
	}
	return nil
}

// do sends req without following redirects and reads at most
// mcpMetadataMax of the body.
func (a *MCPAuth) do(req *http.Request) (*http.Response, []byte, error) {
	ctx, cancel := context.WithTimeout(req.Context(), mcpOAuthCallTimeout)
	defer cancel()
	resp, err := a.client().Do(req.WithContext(ctx))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, mcpMetadataMax+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > mcpMetadataMax {
		return nil, nil, fmt.Errorf("%w: response from %s exceeds %d bytes", errMCPMetadata, req.URL.Redacted(), mcpMetadataMax)
	}
	return resp, body, nil
}

// client is a.Client with redirects disabled: a discovery or token
// call that redirects is answered by the 3xx itself.
func (a *MCPAuth) client() *http.Client {
	c := *a.Client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// checkOAuthURL requires https, a host, and no userinfo; with host
// set, the URL must also sit on that host.
func checkOAuthURL(raw, host string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", errMCPUnsafeURL, raw, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("%w: %q must be an https URL", errMCPUnsafeURL, raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: %q must not carry credentials", errMCPUnsafeURL, raw)
	}
	if host != "" && !strings.EqualFold(u.Hostname(), host) {
		return nil, fmt.Errorf("%w: %q is not on the endpoint host %q", errMCPUnsafeURL, raw, host)
	}
	return u, nil
}

// prmURLs lists RFC 9728 well-known locations for endpoint: the
// path-inserted form first, then the origin root.
func prmURLs(endpoint *url.URL) []string {
	origin := endpoint.Scheme + "://" + endpoint.Host
	root := origin + "/.well-known/oauth-protected-resource"
	if p := strings.TrimRight(endpoint.Path, "/"); p != "" {
		return []string{root + p, root}
	}
	return []string{root}
}

// asMetadataURLs lists RFC 8414 then OpenID discovery locations for
// issuer, path-inserted when the issuer has a path.
func asMetadataURLs(issuer *url.URL) []string {
	origin := issuer.Scheme + "://" + issuer.Host
	p := strings.TrimRight(issuer.Path, "/")
	if p == "" {
		return []string{
			origin + "/.well-known/oauth-authorization-server",
			origin + "/.well-known/openid-configuration",
		}
	}
	return []string{
		origin + "/.well-known/oauth-authorization-server" + p,
		origin + "/.well-known/openid-configuration" + p,
		origin + p + "/.well-known/openid-configuration",
	}
}

// canonicalResource is the endpoint URL as an RFC 8707 resource: no
// fragment.
func canonicalResource(endpoint *url.URL) string {
	u := *endpoint
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// resourceCovers reports whether a protected-resource identifier
// names endpoint: same scheme and host, and a path prefix of it on a
// segment boundary.
func resourceCovers(resource string, endpoint *url.URL) bool {
	r, err := url.Parse(resource)
	if err != nil || r.Scheme != endpoint.Scheme || !strings.EqualFold(r.Host, endpoint.Host) {
		return false
	}
	rp := strings.TrimRight(r.Path, "/")
	ep := strings.TrimRight(endpoint.Path, "/")
	return rp == "" || ep == rp || strings.HasPrefix(ep, rp+"/")
}

// parseBearerChallenge returns the Bearer challenge's auth-params
// (RFC 7235 section 4.1), keys lowercased, across every
// WWW-Authenticate value; other schemes' params are skipped.
func parseBearerChallenge(headers []string) map[string]string {
	out := map[string]string{}
	for _, h := range headers {
		bearer := false
		rest := h
		for {
			rest = strings.TrimLeft(rest, " \t,")
			if rest == "" {
				break
			}
			i := strings.IndexAny(rest, " \t,=")
			if i < 0 {
				break
			}
			tok := rest[:i]
			after := strings.TrimLeft(rest[i:], " \t")
			if !strings.HasPrefix(after, "=") {
				bearer = strings.EqualFold(tok, "bearer")
				rest = rest[i:]
				continue
			}
			var val string
			val, rest = readParamValue(strings.TrimLeft(after[1:], " \t"))
			if bearer && tok != "" {
				out[strings.ToLower(tok)] = val
			}
		}
	}
	return out
}

// readParamValue reads one quoted-string or token value and returns
// it with the unread remainder.
func readParamValue(s string) (string, string) {
	if !strings.HasPrefix(s, `"`) {
		i := strings.IndexAny(s, " \t,")
		if i < 0 {
			return s, ""
		}
		return s[:i], s[i:]
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '"':
			return b.String(), s[i+1:]
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), ""
}

// randomURLToken returns n random bytes, base64url without padding.
func randomURLToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// pkceChallenge is the RFC 7636 S256 challenge for verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
