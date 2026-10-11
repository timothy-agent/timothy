package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
)

// oauthMCPServer is one TLS host serving an MCP endpoint behind OAuth
// and the authorization server that guards it.
type oauthMCPServer struct {
	srv *httptest.Server

	mu     sync.Mutex
	codes  map[string]url.Values
	issued map[string]bool
}

func newOAuthMCPServer(t *testing.T) *oauthMCPServer {
	t.Helper()
	s := &oauthMCPServer{codes: map[string]url.Values{}, issued: map[string]bool{}}
	mux := http.NewServeMux()
	writeJSONBody := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, http.StatusOK, map[string]any{
			"resource":              s.srv.URL + "/mcp",
			"authorization_servers": []string{s.srv.URL},
			"scopes_supported":      []string{"issues:read"},
		})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, http.StatusOK, map[string]any{
			"issuer":                           s.srv.URL,
			"authorization_endpoint":           s.srv.URL + "/authorize",
			"token_endpoint":                   s.srv.URL + "/token",
			"registration_endpoint":            s.srv.URL + "/register",
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, http.StatusCreated, map[string]string{"client_id": "dyn-client"})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		q := r.URL.Query()
		code := fmt.Sprintf("code-%d", len(s.codes)+1)
		s.codes[code] = q
		//nolint:gosec // G710: a fake authorization server redirecting to the client's redirect_uri is the flow under test.
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = r.ParseForm()
		q, ok := s.codes[r.PostForm.Get("code")]
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") ||
			r.PostForm.Get("resource") != s.srv.URL+"/mcp" || r.PostForm.Get("client_id") != "dyn-client" {
			writeJSONBody(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
		s.issued["at-1"] = true
		writeJSONBody(w, http.StatusOK, map[string]any{
			"access_token": "at-1", "token_type": "Bearer", "refresh_token": "rt-1", "expires_in": 3600, "scope": "issues:read",
		})
	})
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		live := s.issued[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		s.mu.Unlock()
		if !live {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.srv.URL+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"list_issues","description":"List issues"}]}}`, req.ID)
		default:
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, req.ID)
		}
	})
	s.srv = httptest.NewTLSServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// client trusts the server's certificate and never follows redirects,
// the way the browser hop is simulated below.
func (s *oauthMCPServer) client() *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(s.srv.Certificate())
	return &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// oauthRows is the connector store slice MCPAuth reads and patches.
type oauthRows struct {
	mu sync.Mutex
	c  connectors.Connector
}

func (r *oauthRows) List(context.Context) ([]connectors.Connector, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []connectors.Connector{r.c}, nil
}

func (r *oauthRows) Get(_ context.Context, id string) (connectors.Connector, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.c.ID {
		return connectors.Connector{}, connectors.ErrNotFound
	}
	return r.c, nil
}

func (r *oauthRows) Patch(_ context.Context, _ string, p connectors.Patch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.Config != nil {
		r.c.Config = *p.Config
	}
	return nil
}

type oauthSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *oauthSecrets) Resolve(_ context.Context, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[ref]
	if !ok {
		return "", fmt.Errorf("ref %s not found", ref)
	}
	return v, nil
}

func (s *oauthSecrets) Set(_ context.Context, ref, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[ref] = value
	return nil
}

// TestMCPOAuthCallbackEndToEnd runs the whole MCP login through the
// real callback handler: discovery from the 401, dynamic registration,
// consent, code exchange, and a Test that lists the server's tools.
func TestMCPOAuthCallbackEndToEnd(t *testing.T) {
	t.Parallel()
	server := newOAuthMCPServer(t)
	//nolint:gosec // G101: CredentialRef is a ref NAME, not a credential value.
	rows := &oauthRows{c: connectors.Connector{
		ID: "c1", Name: "linear", Kind: "mcp",
		Config:        json.RawMessage(`{"endpoint":"` + server.srv.URL + `/mcp","auth_mode":"oauth"}`),
		CredentialRef: "LINEAR_MCP_OAUTH",
	}}
	secrets := &oauthSecrets{m: map[string]string{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := connectors.NewMCPAuth(secrets, rows, "https://timothy.example", server.client(), log)
	h := &connectorAPI{mcp: auth, log: log}

	callback := func(rawQuery string) string {
		t.Helper()
		w := httptest.NewRecorder()
		h.oauthCallback(w, httptest.NewRequest(http.MethodGet, "/v1/connectors/oauth/callback?"+rawQuery, nil))
		if w.Code != http.StatusFound {
			t.Fatalf("callback status = %d", w.Code)
		}
		return w.Header().Get("Location")
	}

	if loc := callback("state=forged&code=x"); !strings.Contains(loc, "oauth_error=") {
		t.Fatalf("forged state redirect = %s", loc)
	}

	authURL, err := auth.StartAuth(t.Context(), "c1")
	if err != nil {
		t.Fatalf("StartAuth: %v", err)
	}
	resp, err := server.client().Get(authURL)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	_ = resp.Body.Close()
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || back.Path != "/v1/connectors/oauth/callback" {
		t.Fatalf("consent redirect = %s, %v", resp.Header.Get("Location"), err)
	}

	if loc := callback(back.RawQuery); loc != "/settings/connectors?oauth_connected=linear" {
		t.Fatalf("callback redirect = %s", loc)
	}
	// The state is single-use: replaying the callback fails.
	if loc := callback(back.RawQuery); !strings.Contains(loc, "oauth_error=") {
		t.Fatalf("replayed callback redirect = %s", loc)
	}

	c, _ := rows.Get(t.Context(), "c1")
	src, err := auth.Builder(connectors.MCPDeferral{})(t.Context(), c, secrets.Resolve)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := src.Test(t.Context()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if tools := src.Tools(); len(tools) != 1 || tools[0].Name != "list_issues" {
		t.Fatalf("tools = %v", tools)
	}
}

// TestOAuthCallbackProviderError keeps the provider's error redirect
// ahead of any engine dispatch.
func TestOAuthCallbackProviderError(t *testing.T) {
	t.Parallel()
	h := &connectorAPI{}
	w := httptest.NewRecorder()
	h.oauthCallback(w, httptest.NewRequest(http.MethodGet, "/v1/connectors/oauth/callback?error=access_denied&state=s", nil))
	if loc := w.Header().Get("Location"); loc != "/settings/connectors?oauth_error=access_denied" {
		t.Fatalf("redirect = %s", loc)
	}
}
