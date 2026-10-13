package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

type fakeProviderAdmin struct {
	mu       sync.Mutex
	rows     []gwclient.AdminProvider
	refs     []gwclient.SecretRef
	created  []gwclient.AdminProvider
	createFn func(gwclient.AdminProvider) (string, error)
}

func (f *fakeProviderAdmin) ListProviders(context.Context) ([]gwclient.AdminProvider, error) {
	return f.rows, nil
}

func (f *fakeProviderAdmin) ListSecrets(context.Context) ([]gwclient.SecretRef, error) {
	return f.refs, nil
}

func (f *fakeProviderAdmin) CreateProvider(_ context.Context, p gwclient.AdminProvider) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, p)
	if f.createFn != nil {
		return f.createFn(p)
	}
	return "p-new", nil
}

// fakeOpenRouter serves the keys endpoint: it accepts the one code it
// was told about when the verifier hashes to the recorded challenge.
type fakeOpenRouter struct {
	srv       *httptest.Server
	mu        sync.Mutex
	challenge string
	code      string
	status    int
	body      string
}

func newFakeOpenRouter(t *testing.T) *fakeOpenRouter {
	t.Helper()
	f := &fakeOpenRouter{code: "good-code", status: http.StatusOK, body: `{"key":"sk-or-v1-minted"}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/keys" {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		var in struct {
			Code     string `json:"code"`
			Verifier string `json:"code_verifier"`
			Method   string `json:"code_challenge_method"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &in)
		sum := sha256.Sum256([]byte(in.Verifier))
		f.mu.Lock()
		defer f.mu.Unlock()
		if in.Code != f.code || in.Method != "S256" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid code","code":400}}`))
			return
		}
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func testOpenRouter(t *testing.T, admin *fakeProviderAdmin) (*OpenRouterAuth, *fakeOpenRouter, *fakeSecrets) {
	t.Helper()
	f := newFakeOpenRouter(t)
	secrets := newFakeSecrets()
	a := NewOpenRouterAuth(secrets, admin, "https://timothy.example", f.srv.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.AuthURL = f.srv.URL + "/auth"
	a.KeysURL = f.srv.URL + "/api/v1/auth/keys"
	return a, f, secrets
}

// start runs StartAuth and records the challenge on the fake so the
// exchange can verify it.
func start(t *testing.T, a *OpenRouterAuth, f *fakeOpenRouter, returnTo string) url.Values {
	t.Helper()
	AuthURL, err := a.StartAuth(returnTo)
	if err != nil {
		t.Fatalf("StartAuth: %v", err)
	}
	u, err := url.Parse(AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.challenge = u.Query().Get("code_challenge")
	f.mu.Unlock()
	return u.Query()
}

func TestOpenRouterStartAndCallback(t *testing.T) {
	t.Parallel()
	admin := &fakeProviderAdmin{}
	a, f, secrets := testOpenRouter(t, admin)

	q := start(t, a, f, OpenRouterReturnWizard)
	if q.Get("callback_url") != "https://timothy.example/v1/connectors/oauth/callback" {
		t.Fatalf("callback_url = %s", q.Get("callback_url"))
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		t.Fatalf("auth params = %v", q)
	}
	if !a.HasState(q.Get("state")) || a.HasState("other") {
		t.Fatal("HasState wrong")
	}

	got, err := a.HandleCallback(t.Context(), q.Get("state"), "good-code")
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if got.ProviderID != "p-new" || got.Name != "OpenRouter" || got.ReturnTo != OpenRouterReturnWizard {
		t.Fatalf("connected = %+v", got)
	}
	if v, _ := secrets.Resolve(t.Context(), "OPENROUTER_API_KEY"); v != "sk-or-v1-minted" {
		t.Fatalf("stored key = %q", v)
	}
	if len(admin.created) != 1 {
		t.Fatalf("created = %d rows", len(admin.created))
	}
	row := admin.created[0]
	if row.Driver != "openaicompat" || row.Kind != "api" || row.BaseURL != "https://openrouter.ai/api/v1" ||
		row.CredentialRef != "OPENROUTER_API_KEY" || row.DefaultModel != OpenRouterDefaultModel || !row.Enabled {
		t.Fatalf("row = %+v", row)
	}
	if row.Headers["X-OpenRouter-Title"] != "Timothy" || row.Headers["HTTP-Referer"] == "" {
		t.Fatalf("attribution headers = %v", row.Headers)
	}
	// States are single-use.
	if _, err := a.HandleCallback(t.Context(), q.Get("state"), "good-code"); err == nil {
		t.Fatal("state replay accepted")
	}
	if a.HasState(q.Get("state")) {
		t.Fatal("consumed state still live")
	}
}

func TestOpenRouterNamesAvoidCollisions(t *testing.T) {
	t.Parallel()
	admin := &fakeProviderAdmin{
		rows: []gwclient.AdminProvider{
			//nolint:gosec // G101: CredentialRef is a ref NAME, not a credential value.
			{Name: "openrouter", CredentialRef: "OPENROUTER_API_KEY"},
			{Name: "OpenRouter 2", CredentialRef: "OTHER"},
		},
		refs: []gwclient.SecretRef{{RefName: "OPENROUTER_API_KEY_2"}},
	}
	a, f, secrets := testOpenRouter(t, admin)
	q := start(t, a, f, OpenRouterReturnSettings)
	got, err := a.HandleCallback(t.Context(), q.Get("state"), "good-code")
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if got.Name != "OpenRouter 3" || admin.created[0].CredentialRef != "OPENROUTER_API_KEY_3" {
		t.Fatalf("name = %s, ref = %s", got.Name, admin.created[0].CredentialRef)
	}
	if _, err := secrets.Resolve(t.Context(), "OPENROUTER_API_KEY"); err == nil {
		t.Fatal("existing ref overwritten")
	}
}

func TestOpenRouterExchangeFailureStoresNothing(t *testing.T) {
	t.Parallel()
	admin := &fakeProviderAdmin{}
	a, f, secrets := testOpenRouter(t, admin)
	q := start(t, a, f, OpenRouterReturnSettings)

	got, err := a.HandleCallback(t.Context(), q.Get("state"), "wrong-code")
	if err == nil || !strings.Contains(err.Error(), "Invalid code") {
		t.Fatalf("err = %v", err)
	}
	if got.ReturnTo != OpenRouterReturnSettings || got.ProviderID != "" {
		t.Fatalf("failed result = %+v", got)
	}
	if len(secrets.m) != 0 || len(admin.created) != 0 {
		t.Fatal("failed exchange left state behind")
	}
}

func TestOpenRouterEmptyKeyRejected(t *testing.T) {
	t.Parallel()
	admin := &fakeProviderAdmin{}
	a, f, secrets := testOpenRouter(t, admin)
	f.mu.Lock()
	f.body = `{"key":""}`
	f.mu.Unlock()
	q := start(t, a, f, OpenRouterReturnSettings)
	if _, err := a.HandleCallback(t.Context(), q.Get("state"), "good-code"); err == nil {
		t.Fatal("empty key accepted")
	}
	if len(secrets.m) != 0 {
		t.Fatal("empty key stored")
	}
}

func TestOpenRouterCreateFailureKeepsKeyStored(t *testing.T) {
	t.Parallel()
	admin := &fakeProviderAdmin{createFn: func(gwclient.AdminProvider) (string, error) {
		return "", context.DeadlineExceeded
	}}
	a, f, _ := testOpenRouter(t, admin)
	q := start(t, a, f, OpenRouterReturnSettings)
	if _, err := a.HandleCallback(t.Context(), q.Get("state"), "good-code"); err == nil {
		t.Fatal("create failure swallowed")
	}
}

func TestOpenRouterStartRequirements(t *testing.T) {
	t.Parallel()
	a, _, _ := testOpenRouter(t, &fakeProviderAdmin{})
	if _, err := a.StartAuth("elsewhere"); err == nil {
		t.Fatal("bad return_to accepted")
	}
	a.PublicURL = ""
	if _, err := a.StartAuth(OpenRouterReturnWizard); err == nil {
		t.Fatal("missing public URL accepted")
	}
	if _, err := a.HandleCallback(t.Context(), "bogus", "code"); err == nil {
		t.Fatal("unknown state accepted")
	}
	var nilAuth *OpenRouterAuth
	if nilAuth.HasState("x") {
		t.Fatal("nil engine claims state")
	}
}

func TestOpenRouterCancelAndExpiry(t *testing.T) {
	t.Parallel()
	a, f, _ := testOpenRouter(t, &fakeProviderAdmin{})
	q := start(t, a, f, OpenRouterReturnWizard)
	if to, ok := a.Cancel(q.Get("state")); !ok || to != OpenRouterReturnWizard {
		t.Fatalf("Cancel = %s, %v", to, ok)
	}
	if _, ok := a.Cancel(q.Get("state")); ok {
		t.Fatal("cancelled state still live")
	}

	q = start(t, a, f, OpenRouterReturnWizard)
	a.mu.Lock()
	st := a.states[q.Get("state")]
	st.expires = time.Now().Add(-time.Second)
	a.states[q.Get("state")] = st
	a.mu.Unlock()
	if _, err := a.HandleCallback(t.Context(), q.Get("state"), "good-code"); err == nil {
		t.Fatal("expired state accepted")
	}
}
