package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

type openRouterAdmin struct{ created []gwclient.AdminProvider }

func (f *openRouterAdmin) ListProviders(context.Context) ([]gwclient.AdminProvider, error) {
	return nil, nil
}

func (f *openRouterAdmin) ListSecrets(context.Context) ([]gwclient.SecretRef, error) {
	return nil, nil
}

func (f *openRouterAdmin) CreateProvider(_ context.Context, p gwclient.AdminProvider) (string, error) {
	f.created = append(f.created, p)
	return "p-or", nil
}

// newOpenRouterAPI wires an engine against a fake keys endpoint that
// accepts any code.
func newOpenRouterAPI(t *testing.T) (*connectorAPI, *openRouterAdmin, *oauthSecrets) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"key":"sk-or-v1-x"}`))
	}))
	t.Cleanup(srv.Close)
	admin := &openRouterAdmin{}
	secrets := &oauthSecrets{m: map[string]string{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := connectors.NewOpenRouterAuth(secrets, admin, "https://timothy.example", srv.Client(), log)
	auth.AuthURL = srv.URL + "/auth"
	auth.KeysURL = srv.URL + "/keys"
	return &connectorAPI{openrouter: auth, log: log}, admin, secrets
}

func TestOpenRouterStartHandler(t *testing.T) {
	t.Parallel()
	h, _, _ := newOpenRouterAPI(t)

	w := httptest.NewRecorder()
	h.openRouterStart(w, httptest.NewRequest(http.MethodPost, "/v1/admin/providers/openrouter/oauth/start", strings.NewReader(`{"return_to":"wizard"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(out.URL)
	// The state rides inside callback_url, not as a top-level param.
	cb, cbErr := url.Parse(u.Query().Get("callback_url"))
	if err != nil || cbErr != nil || cb.Query().Get("state") == "" || u.Query().Get("code_challenge") == "" {
		t.Fatalf("url = %s, %v", out.URL, err)
	}

	w = httptest.NewRecorder()
	h.openRouterStart(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"return_to":"nowhere"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad return_to status = %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.openRouterStart(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`nope`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad body status = %d", w.Code)
	}
}

func TestOpenRouterCallbackLandings(t *testing.T) {
	t.Parallel()
	h, admin, secrets := newOpenRouterAPI(t)

	stateFor := func(returnTo string) string {
		t.Helper()
		authURL, err := h.openrouter.StartAuth(returnTo)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(authURL)
		cb, _ := url.Parse(u.Query().Get("callback_url"))
		return cb.Query().Get("state")
	}
	callback := func(rawQuery string) string {
		t.Helper()
		w := httptest.NewRecorder()
		h.oauthCallback(w, httptest.NewRequest(http.MethodGet, "/v1/connectors/oauth/callback?"+rawQuery, nil))
		if w.Code != http.StatusFound {
			t.Fatalf("callback status = %d", w.Code)
		}
		return w.Header().Get("Location")
	}

	// Wizard start resumes the wizard with the new row.
	loc := callback("state=" + stateFor("wizard") + "&code=c1")
	if !strings.HasPrefix(loc, "/welcome?") || !strings.Contains(loc, "provider_id=p-or") || !strings.Contains(loc, "oauth_connected=OpenRouter") {
		t.Fatalf("wizard landing = %s", loc)
	}
	if secrets.m["OPENROUTER_API_KEY"] != "sk-or-v1-x" || len(admin.created) != 1 {
		t.Fatalf("stored = %v, created = %d", secrets.m, len(admin.created))
	}

	// Settings start lands on the providers list.
	loc = callback("state=" + stateFor("settings") + "&code=c2")
	if !strings.HasPrefix(loc, "/settings/providers?") || !strings.Contains(loc, "oauth_connected=OpenRouter") {
		t.Fatalf("settings landing = %s", loc)
	}

	// A denial consumes the state and lands where the start came from.
	loc = callback("state=" + stateFor("wizard") + "&error=access_denied")
	if loc != "/welcome?oauth_error=access_denied" {
		t.Fatalf("denied landing = %s", loc)
	}

	// A forged state is nobody's: the shared default applies.
	if loc := callback("state=forged&code=x"); !strings.HasPrefix(loc, "/settings/connectors?oauth_error=") {
		t.Fatalf("forged landing = %s", loc)
	}
	// A missing code stores nothing and lands where the start came from.
	before := len(admin.created)
	loc = callback("state=" + stateFor("settings"))
	if !strings.HasPrefix(loc, "/settings/providers?oauth_error=") || len(admin.created) != before {
		t.Fatalf("missing code landing = %s, created = %d", loc, len(admin.created))
	}
	loc = callback("state=" + stateFor("wizard"))
	if !strings.HasPrefix(loc, "/welcome?oauth_error=") || len(admin.created) != before {
		t.Fatalf("wizard missing code landing = %s", loc)
	}
}

// TestOAuthCallbackNoOpenRouterEngine keeps the shared callback working
// when the OpenRouter engine is absent.
func TestOAuthCallbackNoOpenRouterEngine(t *testing.T) {
	t.Parallel()
	h := &connectorAPI{}
	w := httptest.NewRecorder()
	h.oauthCallback(w, httptest.NewRequest(http.MethodGet, "/v1/connectors/oauth/callback?state=s&code=c", nil))
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/settings/connectors?oauth_error=") {
		t.Fatalf("redirect = %s", loc)
	}
}
