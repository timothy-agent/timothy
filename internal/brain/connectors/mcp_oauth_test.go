package connectors

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/netguard"
)

// fakeAS is an OAuth authorization server: RFC 8414 metadata, RFC 7591
// registration, an authorize endpoint that redirects with a code, and
// a token endpoint for both grants. base is its public URL.
type fakeAS struct {
	base string

	mu             sync.Mutex
	noRegistration bool
	noS256         bool
	issuer         string // overrides base as the advertised issuer
	regSecret      string // returned as client_secret on registration
	clientSecret   string // required on token calls when set
	expiresIn      int
	rotate         bool
	refreshStatus  int // non-zero: refresh grants fail with this status
	registrations  []map[string]any
	authorizeQs    []url.Values
	tokenForms     []url.Values
	basicAuth      []string
	codes          map[string]url.Values
	live           map[string]bool
	issued         int
}

func (a *fakeAS) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		issuer := a.base
		if a.issuer != "" {
			issuer = a.issuer
		}
		md := map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                a.base + "/authorize",
			"token_endpoint":                        a.base + "/token",
			"token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic", "client_secret_post"},
			"code_challenge_methods_supported":      []string{"S256"},
		}
		if a.noS256 {
			md["code_challenge_methods_supported"] = []string{"plain"}
		}
		if !a.noRegistration {
			md["registration_endpoint"] = a.base + "/register"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(md)
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.registrations = append(a.registrations, body)
		res := map[string]any{"client_id": fmt.Sprintf("dyn-%d", len(a.registrations))}
		if a.regSecret != "" {
			res["client_secret"] = a.regSecret
			res["token_endpoint_auth_method"] = "client_secret_post"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(res)
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		q := r.URL.Query()
		a.authorizeQs = append(a.authorizeQs, q)
		if a.codes == nil {
			a.codes = map[string]url.Values{}
		}
		code := fmt.Sprintf("code-%d", len(a.authorizeQs))
		a.codes[code] = q
		back := q.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
		//nolint:gosec // G710: a fake authorization server redirecting to the client's redirect_uri is the flow under test.
		http.Redirect(w, r, back, http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		_ = r.ParseForm()
		form := r.PostForm
		a.tokenForms = append(a.tokenForms, form)
		user, pass, hasBasic := r.BasicAuth()
		if hasBasic {
			a.basicAuth = append(a.basicAuth, user+":"+pass)
		}
		fail := func(status int, code string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error":%q,"error_description":"nope"}`, code)
		}
		if a.clientSecret != "" && pass != a.clientSecret && form.Get("client_secret") != a.clientSecret {
			fail(http.StatusUnauthorized, "invalid_client")
			return
		}
		res := map[string]any{"token_type": "Bearer", "scope": "read write"}
		switch form.Get("grant_type") {
		case "authorization_code":
			q, ok := a.codes[form.Get("code")]
			delete(a.codes, form.Get("code"))
			if !ok || pkceChallenge(form.Get("code_verifier")) != q.Get("code_challenge") ||
				form.Get("resource") != q.Get("resource") || form.Get("redirect_uri") != q.Get("redirect_uri") {
				fail(http.StatusBadRequest, "invalid_grant")
				return
			}
			res["refresh_token"] = "rt-1"
		case "refresh_token":
			if a.refreshStatus != 0 {
				fail(a.refreshStatus, "invalid_grant")
				return
			}
			if a.rotate {
				res["refresh_token"] = fmt.Sprintf("rt-%d", a.issued+2)
			}
		default:
			fail(http.StatusBadRequest, "unsupported_grant_type")
			return
		}
		a.issued++
		at := fmt.Sprintf("at-%d", a.issued)
		if a.live == nil {
			a.live = map[string]bool{}
		}
		a.live[at] = true
		res["access_token"] = at
		if a.expiresIn > 0 {
			res["expires_in"] = a.expiresIn
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
}

func (a *fakeAS) isLive(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.live[token]
}

func (a *fakeAS) revoke(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.live, token)
}

// grants counts token calls per grant type.
func (a *fakeAS) grants(grant string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, f := range a.tokenForms {
		if f.Get("grant_type") == grant {
			n++
		}
	}
	return n
}

// fakeOAuthMCP is an MCP server behind OAuth: 401 with a resource
// metadata challenge until the bearer is one the fakeAS issued, then
// the plain fakeMCP behavior.
type fakeOAuthMCP struct {
	as   *fakeAS
	base string

	mu          sync.Mutex
	inner       fakeMCP
	open        bool   // answer without auth
	header      string // overrides the WWW-Authenticate value; "-" sends none
	noPRM       bool
	prmBody     string // overrides the PRM document
	prmRedirect bool   // the well-known PRM answers 302
	movedHits   int
}

func (f *fakeOAuthMCP) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case f.prmRedirect:
			http.Redirect(w, r, f.base+"/moved-prm", http.StatusFound)
		case f.noPRM:
			http.NotFound(w, r)
		case f.prmBody != "":
			_, _ = io.WriteString(w, f.prmBody)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource":              f.base + "/mcp",
				"authorization_servers": []string{f.as.base},
				"scopes_supported":      []string{"read", "write"},
			})
		}
	})
	mux.HandleFunc("GET /moved-prm", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.movedHits++
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"authorization_servers":["`+f.as.base+`"]}`)
	})
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !f.open && !f.as.isLive(token) {
			switch f.header {
			case "":
				w.Header().Set("WWW-Authenticate",
					`Bearer error="invalid_token", resource_metadata="`+f.base+`/.well-known/oauth-protected-resource/mcp"`)
			case "-":
			default:
				w.Header().Set("WWW-Authenticate", f.header)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.inner.handler()(w, r)
	})
}

// newOAuthFakes starts the fake authorization server and MCP server
// over TLS, on one server when combined, and returns a client that
// trusts them.
func newOAuthFakes(t *testing.T, combined bool) (*fakeAS, *fakeOAuthMCP, *http.Client) {
	t.Helper()
	as := &fakeAS{expiresIn: 3600}
	m := &fakeOAuthMCP{as: as}
	var servers []*httptest.Server
	if combined {
		mux := http.NewServeMux()
		as.register(mux)
		m.register(mux)
		srv := httptest.NewTLSServer(mux)
		as.base, m.base = srv.URL, srv.URL
		servers = append(servers, srv)
	} else {
		asMux, mcpMux := http.NewServeMux(), http.NewServeMux()
		as.register(asMux)
		m.register(mcpMux)
		asSrv, mcpSrv := httptest.NewTLSServer(asMux), httptest.NewTLSServer(mcpMux)
		as.base, m.base = asSrv.URL, mcpSrv.URL
		servers = append(servers, asSrv, mcpSrv)
	}
	pool := x509.NewCertPool()
	for _, s := range servers {
		t.Cleanup(s.Close)
		pool.AddCert(s.Certificate())
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	return as, m, client
}

// patchRows is a rowSource that also patches, as the real Store does.
type patchRows struct {
	mu   sync.Mutex
	rows map[string]Connector
}

func newPatchRows(cs ...Connector) *patchRows {
	r := &patchRows{rows: map[string]Connector{}}
	for _, c := range cs {
		r.rows[c.ID] = c
	}
	return r
}

func (r *patchRows) List(context.Context) ([]Connector, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Connector, 0, len(r.rows))
	for _, c := range r.rows {
		out = append(out, c)
	}
	return out, nil
}

func (r *patchRows) Get(_ context.Context, id string) (Connector, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.rows[id]
	if !ok {
		return Connector{}, fmt.Errorf("connector %s: %w", id, ErrNotFound)
	}
	return c, nil
}

func (r *patchRows) Patch(_ context.Context, id string, p Patch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.rows[id]
	if !ok {
		return ErrNotFound
	}
	if p.Config != nil {
		c.Config = *p.Config
	}
	r.rows[id] = c
	return nil
}

func (r *patchRows) config(t *testing.T, id string) map[string]any {
	t.Helper()
	c, err := r.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(c.Config, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

//nolint:gosec // G101: a ref NAME, not a credential value.
const mcpOAuthRef = "LINEAR_MCP_OAUTH"

func oauthRow(endpoint, extra string) Connector {
	return Connector{
		ID: "m1", Name: "linear", Kind: "mcp",
		Config:        json.RawMessage(`{"endpoint":"` + endpoint + `","auth_mode":"oauth","headers":{"X-Team":"core"}` + extra + `}`),
		CredentialRef: mcpOAuthRef,
	}
}

func testMCPAuth(t *testing.T, client *http.Client, row Connector) (*MCPAuth, *patchRows, *fakeSecrets) {
	t.Helper()
	rows := newPatchRows(row)
	secrets := newFakeSecrets()
	auth := NewMCPAuth(secrets, rows, "https://timothy.example", client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return auth, rows, secrets
}

// connectMCP runs StartAuth, follows the consent redirect the way a
// browser would, and finishes with HandleCallback.
func connectMCP(t *testing.T, auth *MCPAuth) string {
	t.Helper()
	authURL, err := auth.StartAuth(t.Context(), "m1")
	if err != nil {
		t.Fatalf("StartAuth: %v", err)
	}
	resp, err := auth.client().Get(authURL)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	_ = resp.Body.Close()
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	name, err := auth.HandleCallback(t.Context(), back.Query().Get("state"), back.Query().Get("code"))
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	return name
}

func buildOAuthSource(t *testing.T, auth *MCPAuth, rows *patchRows) (Source, error) {
	t.Helper()
	c, err := rows.Get(t.Context(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	return auth.Builder(MCPDeferral{})(t.Context(), c, auth.Secrets.Resolve)
}

func TestParseBearerChallenge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		headers []string
		want    map[string]string
	}{
		{"resource metadata", []string{`Bearer resource_metadata="https://a.example/prm"`},
			map[string]string{"resource_metadata": "https://a.example/prm"}},
		{"several params", []string{`Bearer realm="mcp", error="invalid_token", resource_metadata="https://a.example/prm"`},
			map[string]string{"realm": "mcp", "error": "invalid_token", "resource_metadata": "https://a.example/prm"}},
		{"other scheme first in one value", []string{`Basic realm="x", Bearer scope="read write"`},
			map[string]string{"scope": "read write"}},
		{"bearer first then other scheme", []string{`Bearer scope="a", Basic realm="x"`},
			map[string]string{"scope": "a"}},
		{"separate header values", []string{`Basic realm="x"`, `Bearer resource_metadata="https://a.example/prm"`},
			map[string]string{"resource_metadata": "https://a.example/prm"}},
		{"case insensitive scheme and key", []string{`bearer Resource_Metadata="https://a.example/prm"`},
			map[string]string{"resource_metadata": "https://a.example/prm"}},
		{"escaped quote", []string{`Bearer realm="a\"b"`}, map[string]string{"realm": `a"b`}},
		{"token values and spaces around equals", []string{`Bearer error=invalid_token, scope = read`},
			map[string]string{"error": "invalid_token", "scope": "read"}},
		{"bare scheme", []string{`Bearer`}, map[string]string{}},
		{"no bearer", []string{`Basic realm="x"`}, map[string]string{}},
		{"none", nil, map[string]string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBearerChallenge(tc.headers)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckOAuthURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw, host string
		ok              bool
	}{
		{"https any host", "https://auth.example/x", "", true},
		{"https same host", "https://mcp.example/.well-known/x", "mcp.example", true},
		{"host compare ignores case and port", "https://MCP.example:8443/x", "mcp.example", true},
		{"http refused", "http://auth.example/x", "", false},
		{"foreign host refused", "https://evil.example/x", "mcp.example", false},
		{"no host refused", "https:///x", "", false},
		{"userinfo refused", "https://u:p@auth.example/x", "", false},
		{"other scheme refused", "javascript:alert(1)", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checkOAuthURL(tc.raw, tc.host)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && !errors.Is(err, errMCPUnsafeURL) {
				t.Fatalf("err = %v, want errMCPUnsafeURL", err)
			}
		})
	}
}

func TestDiscoveryURLs(t *testing.T) {
	t.Parallel()
	parse := func(s string) *url.URL {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"prm with path", prmURLs(parse("https://mcp.example/v1/mcp/")),
			[]string{"https://mcp.example/.well-known/oauth-protected-resource/v1/mcp", "https://mcp.example/.well-known/oauth-protected-resource"}},
		{"prm at root", prmURLs(parse("https://mcp.example")),
			[]string{"https://mcp.example/.well-known/oauth-protected-resource"}},
		{"as root issuer", asMetadataURLs(parse("https://auth.example/")),
			[]string{"https://auth.example/.well-known/oauth-authorization-server", "https://auth.example/.well-known/openid-configuration"}},
		{"as issuer with path", asMetadataURLs(parse("https://auth.example/tenant1")),
			[]string{
				"https://auth.example/.well-known/oauth-authorization-server/tenant1",
				"https://auth.example/.well-known/openid-configuration/tenant1",
				"https://auth.example/tenant1/.well-known/openid-configuration",
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if fmt.Sprint(tc.got) != fmt.Sprint(tc.want) {
				t.Fatalf("got %v, want %v", tc.got, tc.want)
			}
		})
	}
}

func TestResourceCovers(t *testing.T) {
	t.Parallel()
	endpoint, _ := url.Parse("https://mcp.example/v1/mcp")
	tests := []struct {
		resource string
		want     bool
	}{
		{"https://mcp.example", true},
		{"https://mcp.example/", true},
		{"https://mcp.example/v1", true},
		{"https://mcp.example/v1/mcp", true},
		{"https://MCP.example/v1/mcp/", true},
		{"https://mcp.example/v1/mc", false},
		{"https://mcp.example/other", false},
		{"https://evil.example/v1/mcp", false},
		{"http://mcp.example/v1/mcp", false},
		{"https://mcp.example:8443/v1/mcp", false},
	}
	for _, tc := range tests {
		t.Run(tc.resource, func(t *testing.T) {
			if got := resourceCovers(tc.resource, endpoint); got != tc.want {
				t.Fatalf("resourceCovers(%q) = %v, want %v", tc.resource, got, tc.want)
			}
		})
	}
}

func TestASMetadataValidate(t *testing.T) {
	t.Parallel()
	//nolint:gosec // G101: endpoint URLs, not credentials.
	good := mcpASMetadata{
		Issuer:                        "https://auth.example",
		AuthorizationEndpoint:         "https://auth.example/authorize",
		TokenEndpoint:                 "https://login.example/token",
		RegistrationEndpoint:          "https://auth.example/register",
		CodeChallengeMethodsSupported: []string{"plain", "S256"},
	}
	tests := []struct {
		name   string
		mutate func(*mcpASMetadata)
		issuer string
		want   error
	}{
		{"valid", func(*mcpASMetadata) {}, "https://auth.example", nil},
		{"trailing slash on issuer", func(*mcpASMetadata) {}, "https://auth.example/", nil},
		{"issuer mismatch", func(m *mcpASMetadata) { m.Issuer = "https://other.example" }, "https://auth.example", errMCPMetadata},
		{"no token endpoint", func(m *mcpASMetadata) { m.TokenEndpoint = "" }, "https://auth.example", errMCPMetadata},
		{"http token endpoint", func(m *mcpASMetadata) { m.TokenEndpoint = "http://auth.example/token" }, "https://auth.example", errMCPUnsafeURL},
		{"http authorization endpoint", func(m *mcpASMetadata) { m.AuthorizationEndpoint = "http://auth.example/a" }, "https://auth.example", errMCPUnsafeURL},
		{"http registration endpoint", func(m *mcpASMetadata) { m.RegistrationEndpoint = "http://auth.example/r" }, "https://auth.example", errMCPUnsafeURL},
		{"no S256", func(m *mcpASMetadata) { m.CodeChallengeMethodsSupported = []string{"plain"} }, "https://auth.example", errMCPMetadata},
		{"no methods listed", func(m *mcpASMetadata) { m.CodeChallengeMethodsSupported = nil }, "https://auth.example", errMCPMetadata},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			md := good
			md.CodeChallengeMethodsSupported = append([]string(nil), good.CodeChallengeMethodsSupported...)
			tc.mutate(&md)
			err := md.validate(tc.issuer)
			if tc.want == nil && err != nil {
				t.Fatalf("validate: %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("validate err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPKCE(t *testing.T) {
	t.Parallel()
	// RFC 7636 appendix B.
	if got := pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge = %s", got)
	}
	a, err := randomURLToken(32)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := randomURLToken(32)
	// 43 chars is RFC 7636's minimum verifier length.
	if len(a) != 43 || a == b || strings.ContainsAny(a, "+/=") {
		t.Fatalf("verifiers %q %q", a, b)
	}
}

func TestAuthorizeURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		endpoint string
		scopes   []string
		check    func(t *testing.T, q url.Values)
	}{
		{"all parameters", "https://auth.example/authorize", []string{"read", "write"}, func(t *testing.T, q url.Values) {
			want := map[string]string{
				"response_type": "code", "client_id": "cid", "redirect_uri": "https://t.example/cb",
				"state": "st", "code_challenge": "ch", "code_challenge_method": "S256",
				"resource": "https://mcp.example/mcp", "scope": "read write",
			}
			for k, v := range want {
				if q.Get(k) != v {
					t.Errorf("%s = %q, want %q", k, q.Get(k), v)
				}
			}
		}},
		{"no scopes omits scope", "https://auth.example/authorize", nil, func(t *testing.T, q url.Values) {
			if _, ok := q["scope"]; ok {
				t.Errorf("scope present: %v", q)
			}
		}},
		{"keeps existing query", "https://auth.example/authorize?tenant=t1", nil, func(t *testing.T, q url.Values) {
			if q.Get("tenant") != "t1" || q.Get("client_id") != "cid" {
				t.Errorf("query = %v", q)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mcpConfig{ClientID: "cid", Resource: "https://mcp.example/mcp", Scopes: tc.scopes}
			raw, err := authorizeURL(tc.endpoint, cfg, "https://t.example/cb", "st", "ch")
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(raw)
			tc.check(t, u.Query())
		})
	}
}

func TestMCPConfigAuthMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     mcpConfig
		want    string
		wantErr bool
	}{
		{"empty is token", mcpConfig{Endpoint: "http://internal:8080/mcp"}, mcpAuthToken, false},
		{"explicit token allows http", mcpConfig{Endpoint: "http://internal:8080/mcp", AuthMode: "token"}, mcpAuthToken, false},
		{"oauth https", mcpConfig{Endpoint: "https://mcp.example/mcp", AuthMode: "oauth"}, mcpAuthOAuth, false},
		{"oauth with pasted client", mcpConfig{Endpoint: "https://mcp.example/mcp", AuthMode: "oauth", ClientID: "c", ClientSecretRef: "R"}, mcpAuthOAuth, false},
		{"oauth over http refused", mcpConfig{Endpoint: "http://mcp.example/mcp", AuthMode: "oauth"}, "", true},
		{"oauth secret without client id", mcpConfig{Endpoint: "https://mcp.example/mcp", AuthMode: "oauth", ClientSecretRef: "R"}, "", true},
		{"unknown mode", mcpConfig{Endpoint: "https://mcp.example/mcp", AuthMode: "saml"}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.cfg.authMode()
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("authMode = %q, %v; want %q, err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestMCPRegistration(t *testing.T) {
	t.Parallel()
	body := mcpRegistrationRequest("https://t.example/v1/connectors/oauth/callback")
	raw, _ := json.Marshal(body)
	want := `{"client_name":"Timothy","grant_types":["authorization_code","refresh_token"],"redirect_uris":["https://t.example/v1/connectors/oauth/callback"],"response_types":["code"],"token_endpoint_auth_method":"none"}`
	if string(raw) != want {
		t.Fatalf("registration body = %s", raw)
	}

	tests := []struct {
		name    string
		status  int
		resp    string
		want    mcpRegistration
		wantErr string
	}{
		{"created public client", http.StatusCreated, `{"client_id":"c1"}`, mcpRegistration{ClientID: "c1"}, ""},
		{"ok confidential client", http.StatusOK, `{"client_id":"c2","client_secret":"s","token_endpoint_auth_method":"client_secret_basic"}`,
			mcpRegistration{ClientID: "c2", ClientSecret: "s", TokenEndpointAuthMethod: "client_secret_basic"}, ""},
		{"refused with error", http.StatusBadRequest, `{"error":"invalid_redirect_uri"}`, mcpRegistration{}, `invalid_redirect_uri`},
		{"refused bare", http.StatusForbidden, ``, mcpRegistration{}, "status 403"},
		{"no client id", http.StatusCreated, `{}`, mcpRegistration{}, "no client_id"},
		{"not json", http.StatusCreated, `<html>`, mcpRegistration{}, "decode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, _ = io.ReadAll(r.Body)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.resp)
			}))
			t.Cleanup(srv.Close)
			auth := NewMCPAuth(newFakeSecrets(), newPatchRows(), "https://t.example", srv.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			reg, err := auth.register(t.Context(), srv.URL)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || reg != tc.want {
				t.Fatalf("reg = %+v, %v", reg, err)
			}
			if string(got) != want {
				t.Fatalf("sent %s", got)
			}
		})
	}
}

func TestMCPExchangeExpiryAndClientAuth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		expiresIn  int
		method     string
		wantBasic  bool
		wantSecret bool
		wantExpiry bool
	}{
		{"public client with expiry", 3600, "none", false, false, true},
		{"no expires_in leaves expiry unset", 0, "none", false, false, false},
		{"basic auth", 60, "client_secret_basic", true, false, true},
		{"post auth", 60, "client_secret_post", false, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			as, _, client := newOAuthFakes(t, true)
			as.expiresIn = tc.expiresIn
			auth, _, secrets := testMCPAuth(t, client, oauthRow(as.base+"/mcp", ""))
			_ = secrets.Set(t.Context(), "CLIENT_SECRET", "s3cret")
			cfg := mcpConfig{
				ClientID: "cid", ClientSecretRef: "CLIENT_SECRET", TokenAuthMethod: tc.method,
				TokenEndpoint: as.base + "/token", Resource: as.base + "/mcp",
			}
			before := time.Now()
			b, scope, err := auth.exchange(t.Context(), cfg, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"rt"}})
			if err != nil {
				t.Fatalf("exchange: %v", err)
			}
			if scope != "read write" || b.AccessToken == "" {
				t.Fatalf("bundle = %+v scope = %q", b, scope)
			}
			if tc.wantExpiry {
				want := before.Add(time.Duration(tc.expiresIn) * time.Second)
				if b.Expiry.Before(want) || b.Expiry.After(want.Add(5*time.Second)) {
					t.Fatalf("expiry = %v, want about %v", b.Expiry, want)
				}
			} else if !b.Expiry.IsZero() {
				t.Fatalf("expiry = %v, want zero", b.Expiry)
			}
			form := as.tokenForms[0]
			if form.Get("resource") != as.base+"/mcp" {
				t.Fatalf("resource = %q", form.Get("resource"))
			}
			if gotBasic := len(as.basicAuth) == 1 && as.basicAuth[0] == "cid:s3cret"; gotBasic != tc.wantBasic {
				t.Fatalf("basic auth = %v", as.basicAuth)
			}
			if gotSecret := form.Get("client_secret") == "s3cret"; gotSecret != tc.wantSecret {
				t.Fatalf("form = %v", form)
			}
			if !tc.wantBasic && form.Get("client_id") != "cid" {
				t.Fatalf("client_id missing from form: %v", form)
			}
		})
	}
}

func TestMCPTokenRefresh(t *testing.T) {
	t.Parallel()
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Minute)
	tests := []struct {
		name          string
		stored        tokenBundle
		rejected      string
		rotate        bool
		refreshStatus int
		wantToken     string
		wantRefresh   string
		wantGrants    int
		wantErr       error
	}{
		{"live bundle untouched", tokenBundle{AccessToken: "live", RefreshToken: "rt-old", Expiry: future}, "", false, 0, "live", "rt-old", 0, nil},
		{"no expiry is live", tokenBundle{AccessToken: "live", RefreshToken: "rt-old"}, "", false, 0, "live", "rt-old", 0, nil},
		{"within skew refreshes", tokenBundle{AccessToken: "old", RefreshToken: "rt-old", Expiry: time.Now().Add(refreshSkew / 2)}, "", false, 0, "at-1", "rt-old", 1, nil},
		{"expired keeps refresh token", tokenBundle{AccessToken: "old", RefreshToken: "rt-old", Expiry: past}, "", false, 0, "at-1", "rt-old", 1, nil},
		{"rotated refresh token stored", tokenBundle{AccessToken: "old", RefreshToken: "rt-old", Expiry: past}, "", true, 0, "at-1", "rt-2", 1, nil},
		{"rejected live token refreshes", tokenBundle{AccessToken: "live", RefreshToken: "rt-old"}, "live", false, 0, "at-1", "rt-old", 1, nil},
		{"rejected older token reuses newer", tokenBundle{AccessToken: "newer", RefreshToken: "rt-old", Expiry: future}, "older", false, 0, "newer", "rt-old", 0, nil},
		{"no refresh token needs reconnect", tokenBundle{AccessToken: "old", Expiry: past}, "", false, 0, "", "", 0, errMCPReconnect},
		{"refresh rejected needs reconnect", tokenBundle{AccessToken: "old", RefreshToken: "rt-old", Expiry: past}, "", false, http.StatusBadRequest, "", "", 1, errMCPReconnect},
		{"invalid client needs reconnect", tokenBundle{AccessToken: "old", RefreshToken: "rt-old", Expiry: past}, "", false, http.StatusUnauthorized, "", "", 1, errMCPReconnect},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			as, _, client := newOAuthFakes(t, true)
			as.rotate, as.refreshStatus = tc.rotate, tc.refreshStatus
			auth, _, secrets := testMCPAuth(t, client, oauthRow(as.base+"/mcp", ""))
			if err := auth.storeBundle(t.Context(), mcpOAuthRef, tc.stored); err != nil {
				t.Fatal(err)
			}
			cfg := mcpConfig{ClientID: "cid", TokenAuthMethod: "none", TokenEndpoint: as.base + "/token", Resource: as.base + "/mcp"}
			got, err := auth.token(t.Context(), cfg, mcpOAuthRef, tc.rejected)
			if as.grants("refresh_token") != tc.wantGrants {
				t.Fatalf("refresh grants = %d, want %d", as.grants("refresh_token"), tc.wantGrants)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), "reconnect") {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.wantToken {
				t.Fatalf("token = %q, %v; want %q", got, err, tc.wantToken)
			}
			if b := storedBundle(t, secrets, mcpOAuthRef); b.AccessToken != tc.wantToken || b.RefreshToken != tc.wantRefresh {
				t.Fatalf("stored bundle = %+v", b)
			}
		})
	}
}

func TestMCPTokenNotConnected(t *testing.T) {
	t.Parallel()
	auth, _, _ := testMCPAuth(t, nil, oauthRow("https://mcp.example/mcp", ""))
	if _, err := auth.token(t.Context(), mcpConfig{}, mcpOAuthRef, ""); !errors.Is(err, errMCPNotConnected) {
		t.Fatalf("err = %v, want errMCPNotConnected", err)
	}
}

// TestMCPOAuthDynamicRegistration is the first acceptance criterion:
// 401 with resource metadata, discovery, registration, consent, and
// a Test that lists the server's tools.
func TestMCPOAuthDynamicRegistration(t *testing.T) {
	t.Parallel()
	as, m, client := newOAuthFakes(t, false)
	auth, rows, secrets := testMCPAuth(t, client, oauthRow(m.base+"/mcp", ""))

	if name := connectMCP(t, auth); name != "linear" {
		t.Fatalf("name = %s", name)
	}
	if len(as.registrations) != 1 {
		t.Fatalf("registrations = %d", len(as.registrations))
	}
	q := as.authorizeQs[0]
	if q.Get("client_id") != "dyn-1" || q.Get("resource") != m.base+"/mcp" || q.Get("scope") != "read write" ||
		q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != "https://timothy.example/v1/connectors/oauth/callback" {
		t.Fatalf("authorize query = %v", q)
	}
	b := storedBundle(t, secrets, mcpOAuthRef)
	if b.AccessToken != "at-1" || b.RefreshToken != "rt-1" || b.Expiry.IsZero() {
		t.Fatalf("bundle = %+v", b)
	}
	cfg := rows.config(t, "m1")
	if cfg["client_id"] != "dyn-1" || cfg["client_registered"] != true || cfg["auth_server"] != as.base ||
		cfg["token_endpoint"] != as.base+"/token" || cfg["token_auth_method"] != "none" ||
		cfg["resource"] != m.base+"/mcp" || fmt.Sprint(cfg["scopes"]) != "[read write]" {
		t.Fatalf("persisted config = %v", cfg)
	}
	if _, ok := cfg["client_secret_ref"]; ok {
		t.Fatalf("public client got a secret ref: %v", cfg)
	}
	// Keys the login does not own survive the patch.
	if cfg["endpoint"] != m.base+"/mcp" || fmt.Sprint(cfg["headers"]) != "map[X-Team:core]" || cfg["auth_mode"] != "oauth" {
		t.Fatalf("config lost keys: %v", cfg)
	}

	src, err := buildOAuthSource(t, auth, rows)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := src.Test(t.Context()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if len(src.Tools()) != 2 {
		t.Fatalf("tools = %d", len(src.Tools()))
	}

	// Reconnecting against the same issuer reuses the registered client.
	connectMCP(t, auth)
	if len(as.registrations) != 1 {
		t.Fatalf("reconnect registered again: %d", len(as.registrations))
	}
}

// TestMCPOAuthPastedClient is the second acceptance criterion: no
// dynamic registration, the operator's client id and secret are used.
func TestMCPOAuthPastedClient(t *testing.T) {
	t.Parallel()
	as, m, client := newOAuthFakes(t, false)
	as.noRegistration = true
	as.clientSecret = "shh"
	row := oauthRow(m.base+"/mcp", `,"client_id":"pasted","client_secret_ref":"LINEAR_CLIENT_SECRET"`)
	auth, rows, secrets := testMCPAuth(t, client, row)
	_ = secrets.Set(t.Context(), "LINEAR_CLIENT_SECRET", "shh")

	connectMCP(t, auth)
	if len(as.registrations) != 0 {
		t.Fatal("registered despite a pasted client")
	}
	if len(as.basicAuth) != 1 || as.basicAuth[0] != "pasted:shh" {
		t.Fatalf("basic auth = %v", as.basicAuth)
	}
	cfg := rows.config(t, "m1")
	if cfg["client_id"] != "pasted" || cfg["client_secret_ref"] != "LINEAR_CLIENT_SECRET" ||
		cfg["token_auth_method"] != "client_secret_basic" || cfg["client_registered"] != nil {
		t.Fatalf("persisted config = %v", cfg)
	}
	src, err := buildOAuthSource(t, auth, rows)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := src.Test(t.Context()); err != nil {
		t.Fatalf("Test: %v", err)
	}
}

func TestMCPOAuthNoRegistrationNoClient(t *testing.T) {
	t.Parallel()
	as, m, client := newOAuthFakes(t, false)
	as.noRegistration = true
	auth, _, _ := testMCPAuth(t, client, oauthRow(m.base+"/mcp", ""))
	_, err := auth.StartAuth(t.Context(), "m1")
	if !errors.Is(err, errMCPNoClient) || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestMCPOAuthRegisteredSecretStored(t *testing.T) {
	t.Parallel()
	as, m, client := newOAuthFakes(t, false)
	as.regSecret = "dyn-secret"
	as.clientSecret = "dyn-secret"
	auth, rows, secrets := testMCPAuth(t, client, oauthRow(m.base+"/mcp", ""))

	connectMCP(t, auth)
	if v, err := secrets.Resolve(t.Context(), mcpOAuthRef+"_CLIENT"); err != nil || v != "dyn-secret" {
		t.Fatalf("client secret = %q, %v", v, err)
	}
	cfg := rows.config(t, "m1")
	if cfg["client_secret_ref"] != mcpOAuthRef+"_CLIENT" || cfg["token_auth_method"] != "client_secret_post" {
		t.Fatalf("persisted config = %v", cfg)
	}
}

func TestMCPOAuthDiscoveryVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		combined bool
		tweak    func(as *fakeAS, m *fakeOAuthMCP)
		wantErr  error
		wantMsg  string
	}{
		{"well-known fallback without header", false, func(_ *fakeAS, m *fakeOAuthMCP) { m.header = "-" }, nil, ""},
		{"challenge scope wins", false, func(_ *fakeAS, m *fakeOAuthMCP) {
			m.header = `Bearer scope="admin"`
		}, nil, ""},
		{"origin issuer when no resource metadata", true, func(_ *fakeAS, m *fakeOAuthMCP) {
			m.header, m.noPRM = "-", true
		}, nil, ""},
		{"resource metadata on foreign host", false, func(_ *fakeAS, m *fakeOAuthMCP) {
			m.header = `Bearer resource_metadata="https://evil.example/prm"`
		}, errMCPUnsafeURL, "not on the endpoint host"},
		{"resource metadata over http", false, func(_ *fakeAS, m *fakeOAuthMCP) {
			m.header = `Bearer resource_metadata="` + strings.Replace(m.base, "https", "http", 1) + `/.well-known/oauth-protected-resource/mcp"`
		}, errMCPUnsafeURL, "https"},
		{"authorization server over http", false, func(as *fakeAS, m *fakeOAuthMCP) {
			m.prmBody = `{"authorization_servers":["` + strings.Replace(as.base, "https", "http", 1) + `"]}`
		}, errMCPUnsafeURL, "https"},
		{"resource not covering endpoint", false, func(as *fakeAS, m *fakeOAuthMCP) {
			m.prmBody = `{"resource":"https://other.example/mcp","authorization_servers":["` + as.base + `"]}`
		}, errMCPMetadata, "does not cover"},
		{"no authorization servers", false, func(_ *fakeAS, m *fakeOAuthMCP) { m.prmBody = `{}` }, errMCPMetadata, "no authorization_servers"},
		{"oversized metadata", false, func(_ *fakeAS, m *fakeOAuthMCP) {
			m.prmBody = `{"pad":"` + strings.Repeat("x", mcpMetadataMax) + `"}`
		}, errMCPMetadata, "exceeds"},
		{"issuer mismatch", false, func(as *fakeAS, _ *fakeOAuthMCP) { as.issuer = "https://other.example" }, errMCPMetadata, "does not match"},
		{"no PKCE S256", false, func(as *fakeAS, _ *fakeOAuthMCP) { as.noS256 = true }, errMCPMetadata, "S256"},
		{"no auth required", false, func(_ *fakeAS, m *fakeOAuthMCP) { m.open = true }, errMCPNoAuthNeeded, "bearer token mode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			as, m, client := newOAuthFakes(t, tc.combined)
			tc.tweak(as, m)
			auth, _, _ := testMCPAuth(t, client, oauthRow(m.base+"/mcp", ""))
			authURL, err := auth.StartAuth(t.Context(), "m1")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), tc.wantMsg) {
					t.Fatalf("err = %v, want %v containing %q", err, tc.wantErr, tc.wantMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("StartAuth: %v", err)
			}
			u, _ := url.Parse(authURL)
			if !strings.HasPrefix(authURL, as.base+"/authorize?") {
				t.Fatalf("auth URL = %s", authURL)
			}
			if tc.name == "challenge scope wins" && u.Query().Get("scope") != "admin" {
				t.Fatalf("scope = %q", u.Query().Get("scope"))
			}
		})
	}
}

func TestMCPOAuthDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	_, m, client := newOAuthFakes(t, false)
	m.header, m.prmRedirect = "-", true
	auth, _, _ := testMCPAuth(t, client, oauthRow(m.base+"/mcp", ""))
	if _, err := auth.StartAuth(t.Context(), "m1"); err == nil {
		t.Fatal("discovery succeeded through a redirect")
	}
	if m.movedHits != 0 {
		t.Fatalf("redirect followed %d times", m.movedHits)
	}
}

func TestMCPOAuthDialsThroughNetguard(t *testing.T) {
	t.Parallel()
	_, m, _ := newOAuthFakes(t, false)
	auth, _, _ := testMCPAuth(t, &http.Client{Transport: netguard.Guard{}.Transport()}, oauthRow(m.base+"/mcp", ""))
	_, err := auth.StartAuth(t.Context(), "m1")
	if err == nil || !strings.Contains(err.Error(), "blocked address") || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want a netguard refusal", err)
	}
}

func TestMCPOAuthStartRequirements(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		row       Connector
		publicURL string
		want      error
	}{
		{"no public url", oauthRow("https://mcp.example/mcp", ""), "", nil},
		{"token mode", Connector{ID: "m1", Name: "x", Kind: "mcp", Config: json.RawMessage(`{"endpoint":"https://mcp.example/mcp"}`), CredentialRef: "R"}, "https://t.example", ErrUnsupported},
		{"no credential ref", Connector{ID: "m1", Name: "x", Kind: "mcp", Config: json.RawMessage(`{"endpoint":"https://mcp.example/mcp","auth_mode":"oauth"}`)}, "https://t.example", ErrInvalid},
		{"bad auth mode", Connector{ID: "m1", Name: "x", Kind: "mcp", Config: json.RawMessage(`{"endpoint":"https://mcp.example/mcp","auth_mode":"x"}`), CredentialRef: "R"}, "https://t.example", ErrInvalid},
		{"unknown connector", Connector{ID: "other"}, "https://t.example", ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auth, _, _ := testMCPAuth(t, nil, tc.row)
			auth.PublicURL = tc.publicURL
			_, err := auth.StartAuth(t.Context(), "m1")
			if err == nil {
				t.Fatal("StartAuth succeeded")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestMCPOAuthStateSingleUseAndTTL(t *testing.T) {
	t.Parallel()
	_, m, client := newOAuthFakes(t, false)
	auth, _, _ := testMCPAuth(t, client, oauthRow(m.base+"/mcp", ""))

	stateOf := func() string {
		authURL, err := auth.StartAuth(t.Context(), "m1")
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(authURL)
		return u.Query().Get("state")
	}

	expired := stateOf()
	if !auth.HasState(expired) {
		t.Fatal("fresh state not live")
	}
	auth.mu.Lock()
	st := auth.states[expired]
	st.expires = time.Now().Add(-time.Second)
	auth.states[expired] = st
	auth.mu.Unlock()
	if auth.HasState(expired) {
		t.Fatal("expired state still live")
	}
	if _, err := auth.HandleCallback(t.Context(), expired, "code"); err == nil {
		t.Fatal("expired state accepted")
	}

	used := stateOf()
	// A bad code still consumes the state.
	if _, err := auth.HandleCallback(t.Context(), used, "bogus"); !errors.Is(err, errMCPReconnect) {
		t.Fatalf("bad code err = %v", err)
	}
	if auth.HasState(used) {
		t.Fatal("state survived its callback")
	}
	if auth.HasState("never-issued") {
		t.Fatal("unknown state live")
	}
}

func TestMCPOAuthBuilder(t *testing.T) {
	t.Parallel()
	connected := `,"client_id":"c","token_endpoint":"https://auth.example/token","auth_server":"https://auth.example"`
	tests := []struct {
		name    string
		builder func(a *MCPAuth) Builder
		row     Connector
		want    error
		wantMsg string
	}{
		{"not connected yet", func(a *MCPAuth) Builder { return a.Builder(MCPDeferral{}) },
			oauthRow("https://mcp.example/mcp", ""), errMCPNotConnected, ""},
		{"plain MCPBuilder refuses oauth", func(*MCPAuth) Builder { return MCPBuilder(nil, MCPDeferral{}) },
			oauthRow("https://mcp.example/mcp", connected), nil, "unavailable"},
		{"no credential ref", func(a *MCPAuth) Builder { return a.Builder(MCPDeferral{}) },
			Connector{ID: "m1", Name: "x", Kind: "mcp", Config: json.RawMessage(`{"endpoint":"https://mcp.example/mcp","auth_mode":"oauth"` + connected + `}`)}, nil, "credential_ref"},
		{"unknown auth mode", func(a *MCPAuth) Builder { return a.Builder(MCPDeferral{}) },
			Connector{ID: "m1", Name: "x", Kind: "mcp", Config: json.RawMessage(`{"endpoint":"https://mcp.example/mcp","auth_mode":"basic"}`)}, nil, "auth_mode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auth, _, secrets := testMCPAuth(t, nil, tc.row)
			_, err := tc.builder(auth)(t.Context(), tc.row, secrets.Resolve)
			if err == nil {
				t.Fatal("build succeeded")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantMsg)
			}
		})
	}
}

// TestMCPOAuthBuilderTokenModeUnchanged pins the bearer path behind
// MCPAuth.Builder: a token-mode row resolves credential_ref as before.
func TestMCPOAuthBuilderTokenModeUnchanged(t *testing.T) {
	t.Parallel()
	f := &fakeMCP{token: "tok-1"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	auth := NewMCPAuth(newFakeSecrets(), newPatchRows(), "", srv.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	src, err := auth.Builder(MCPDeferral{})(t.Context(), Connector{
		Name: "gh", Kind: "mcp", Config: json.RawMessage(`{"endpoint":"` + srv.URL + `"}`), CredentialRef: "GH",
	}, func(context.Context, string) (string, error) { return "tok-1", nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if f.gotAuth != "Bearer tok-1" || len(src.Tools()) != 2 {
		t.Fatalf("auth = %q tools = %d", f.gotAuth, len(src.Tools()))
	}
}

// TestMCPOAuthRefreshAtToolCall is the third acceptance criterion:
// an expired token is refreshed once and the call succeeds; a server
// 401 on a token the bundle dates as live forces one refresh too.
func TestMCPOAuthRefreshAtToolCall(t *testing.T) {
	t.Parallel()
	as, m, client := newOAuthFakes(t, false)
	auth, rows, secrets := testMCPAuth(t, client, oauthRow(m.base+"/mcp", ""))
	connectMCP(t, auth)
	src, err := buildOAuthSource(t, auth, rows)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	call := func() {
		t.Helper()
		out, err := toolByName(t, src, "create_issue").Execute(t.Context(), json.RawMessage(`{"title":"x"}`))
		if err != nil || out != "issue #42 created" {
			t.Fatalf("call = %q, %v", out, err)
		}
	}

	b := storedBundle(t, secrets, mcpOAuthRef)
	b.Expiry = time.Now().Add(-time.Minute)
	_ = auth.storeBundle(t.Context(), mcpOAuthRef, b)
	call()
	if n := as.grants("refresh_token"); n != 1 {
		t.Fatalf("refresh grants after expiry = %d, want 1", n)
	}

	as.revoke(storedBundle(t, secrets, mcpOAuthRef).AccessToken)
	call()
	if n := as.grants("refresh_token"); n != 2 {
		t.Fatalf("refresh grants after a 401 = %d, want 2", n)
	}
}

// TestMCPOAuthRefreshRejectedNeedsReconnect: a refresh rejection
// surfaces from Test as the reconnect error, and the identity line
// reports issuer and scopes once connected.
func TestMCPOAuthRefreshRejectedNeedsReconnect(t *testing.T) {
	t.Parallel()
	as, m, client := newOAuthFakes(t, false)
	auth, rows, secrets := testMCPAuth(t, client, oauthRow(m.base+"/mcp", ""))
	connectMCP(t, auth)

	mgr := testManager(rows)
	mgr.resolve = secrets.Resolve
	mgr.RegisterBuilder("mcp", auth.Builder(MCPDeferral{}))
	report, err := mgr.TestReport(t.Context(), "m1")
	if err != nil {
		t.Fatalf("TestReport: %v", err)
	}
	if report.Identity == nil || report.Identity.Login != as.base || report.Identity.Scopes != "read, write" {
		t.Fatalf("identity = %+v", report.Identity)
	}

	b := storedBundle(t, secrets, mcpOAuthRef)
	b.Expiry = time.Now().Add(-time.Minute)
	_ = auth.storeBundle(t.Context(), mcpOAuthRef, b)
	as.refreshStatus = http.StatusBadRequest
	_, err = mgr.TestReport(t.Context(), "m1")
	if !errors.Is(err, errMCPReconnect) || !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("err = %v, want reconnect", err)
	}
}

// TestTestReportTokenModeMCPHasNoIdentity pins that a bearer-token
// MCP connector's test still reports no identity.
func TestTestReportTokenModeMCPHasNoIdentity(t *testing.T) {
	t.Parallel()
	f := &fakeMCP{token: "tok-1"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	mgr := testManager(fakeRows{rows: []Connector{{
		ID: "c1", Name: "gh", Kind: "mcp", Config: json.RawMessage(`{"endpoint":"` + srv.URL + `"}`), CredentialRef: "GH",
	}}})
	mgr.resolve = func(context.Context, string) (string, error) { return "tok-1", nil }
	mgr.RegisterBuilder("mcp", MCPBuilder(srv.Client(), MCPDeferral{}))
	report, err := mgr.TestReport(t.Context(), "c1")
	if err != nil || report.Identity != nil {
		t.Fatalf("report = %+v, %v", report, err)
	}
}
