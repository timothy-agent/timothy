package connectors

import (
	"context"
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
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// fakeSecrets is an in-memory SecretRW.
type fakeSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{m: map[string]string{}} }

func (f *fakeSecrets) Resolve(_ context.Context, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[ref]
	if !ok {
		return "", fmt.Errorf("ref %s: not found", ref)
	}
	return v, nil
}

func (f *fakeSecrets) Set(_ context.Context, ref, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[ref] = value
	return nil
}

// fakeGoogle serves the token endpoint plus minimal Gmail/Calendar.
type fakeGoogle struct {
	mu         sync.Mutex
	tokenForms []url.Values
	gmailSent  []string // raw rfc822 payloads
	events     []map[string]any
	authTokens []string // Authorization headers seen on API calls
}

func (f *fakeGoogle) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		f.mu.Lock()
		f.tokenForms = append(f.tokenForms, form)
		f.mu.Unlock()
		if form.Get("client_secret") != "shh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		res := map[string]any{"access_token": "at-" + form.Get("grant_type"), "expires_in": 3600}
		if form.Get("grant_type") == "authorization_code" {
			res["refresh_token"] = "rt-1"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
	record := func(r *http.Request) {
		f.mu.Lock()
		f.authTokens = append(f.authTokens, r.Header.Get("Authorization"))
		f.mu.Unlock()
	}
	mux.HandleFunc("GET /gmail/v1/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte(`{"messages":[{"id":"m1"}]}`))
	})
	mux.HandleFunc("GET /gmail/v1/users/me/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		body := base64.URLEncoding.EncodeToString([]byte("hello body"))
		// Fixed id: echoing the path value trips gosec's taint pass.
		_, _ = fmt.Fprintf(w, `{"id":"m1","snippet":"snip","payload":{"headers":[
			{"name":"From","value":"a@x"},{"name":"To","value":"b@y"},
			{"name":"Subject","value":"hi"},{"name":"Date","value":"today"}],
			"parts":[{"mimeType":"text/plain","body":{"data":%q}}]}}`, body)
	})
	mux.HandleFunc("POST /gmail/v1/users/me/messages/send", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		var in struct {
			Raw string `json:"raw"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		raw, _ := base64.URLEncoding.DecodeString(in.Raw)
		f.mu.Lock()
		f.gmailSent = append(f.gmailSent, string(raw))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"sent-1"}`))
	})
	mux.HandleFunc("GET /calendars/primary/events", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte(`{"items":[{"summary":"standup","location":"zoom",
			"start":{"dateTime":"2026-07-22T09:00:00Z"},"end":{"dateTime":"2026-07-22T09:15:00Z"}}]}`))
	})
	mux.HandleFunc("POST /calendars/primary/events", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		var ev map[string]any
		_ = json.NewDecoder(r.Body).Decode(&ev)
		f.mu.Lock()
		f.events = append(f.events, ev)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"ev-1","htmlLink":"https://cal/ev-1"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const bothScopes = `["https://www.googleapis.com/auth/gmail.modify","https://www.googleapis.com/auth/calendar"]`

func googleRow(scopes string) Connector {
	return Connector{
		ID: "c1", Name: "personal", Kind: "google",
		Config: json.RawMessage(`{"client_id":"cid","client_secret_ref":"GOOGLE_SECRET","scopes":` + scopes + `}`),
		//nolint:gosec // G101: a ref NAME, not a credential value.
		CredentialRef: "PERSONAL_GOOGLE_OAUTH",
	}
}

func testGoogle(t *testing.T, f *fakeGoogle, row Connector) (*Google, *fakeSecrets) {
	t.Helper()
	srv := f.server(t)
	secrets := newFakeSecrets()
	_ = secrets.Set(t.Context(), "GOOGLE_SECRET", "shh")
	g := NewGoogle(secrets, fakeRows{rows: []Connector{row}}, "https://timothy.example", slog.New(slog.NewTextHandler(io.Discard, nil)))
	g.Client = srv.Client()
	g.TokenURL = srv.URL + "/token"
	g.GmailBase = srv.URL
	g.CalendarBase = srv.URL
	return g, secrets
}

func storedBundle(t *testing.T, secrets *fakeSecrets, ref string) tokenBundle {
	t.Helper()
	raw, err := secrets.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("bundle missing: %v", err)
	}
	var b tokenBundle
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("bundle malformed: %v", err)
	}
	return b
}

func TestOAuthStartAndCallback(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	g, secrets := testGoogle(t, f, googleRow(bothScopes))

	authURL, err := g.StartAuth(t.Context(), "c1")
	if err != nil {
		t.Fatalf("StartAuth: %v", err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != "cid" || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
		t.Fatalf("auth params = %v", q)
	}
	if q.Get("redirect_uri") != "https://timothy.example/v1/connectors/oauth/callback" {
		t.Fatalf("redirect_uri = %s", q.Get("redirect_uri"))
	}
	if !strings.Contains(q.Get("scope"), "gmail.modify") {
		t.Fatalf("scope = %s", q.Get("scope"))
	}

	name, err := g.HandleCallback(t.Context(), q.Get("state"), "the-code")
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if name != "personal" {
		t.Fatalf("name = %s", name)
	}
	b := storedBundle(t, secrets, "PERSONAL_GOOGLE_OAUTH")
	if b.AccessToken != "at-authorization_code" || b.RefreshToken != "rt-1" {
		t.Fatalf("bundle = %+v", b)
	}
	// The exchange carried the code and the resolved client secret.
	if got := f.tokenForms[0]; got.Get("code") != "the-code" || got.Get("client_secret") != "shh" {
		t.Fatalf("token form = %v", got)
	}
	// States are single-use.
	if _, err := g.HandleCallback(t.Context(), q.Get("state"), "again"); err == nil {
		t.Fatal("state replay accepted")
	}
}

func TestOAuthStartRequirements(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}

	noRef := googleRow(bothScopes)
	noRef.CredentialRef = ""
	g, _ := testGoogle(t, f, noRef)
	if _, err := g.StartAuth(t.Context(), "c1"); err == nil {
		t.Fatal("missing credential_ref accepted")
	}

	g2, _ := testGoogle(t, f, googleRow(bothScopes))
	g2.PublicURL = ""
	if _, err := g2.StartAuth(t.Context(), "c1"); err == nil {
		t.Fatal("missing public URL accepted")
	}
	if _, err := g2.HandleCallback(t.Context(), "bogus", "code"); err == nil {
		t.Fatal("unknown state accepted")
	}
}

func TestTokenRefreshPersistsAndKeepsRefreshToken(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	g, secrets := testGoogle(t, f, googleRow(bothScopes))

	//nolint:gosec // G117: fake token fixture.
	expired, _ := json.Marshal(tokenBundle{
		AccessToken: "stale", RefreshToken: "rt-old", Expiry: time.Now().Add(-time.Hour),
	})
	_ = secrets.Set(t.Context(), "PERSONAL_GOOGLE_OAUTH", string(expired))

	cfg, _ := googleConfig(googleRow(bothScopes))
	token, err := g.token(t.Context(), cfg, "PERSONAL_GOOGLE_OAUTH")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if token != "at-refresh_token" {
		t.Fatalf("token = %s", token)
	}
	b := storedBundle(t, secrets, "PERSONAL_GOOGLE_OAUTH")
	// Google omits refresh_token on refresh grants; the old one survives.
	if b.RefreshToken != "rt-old" || b.AccessToken != "at-refresh_token" {
		t.Fatalf("persisted bundle = %+v", b)
	}

	// A live bundle short-circuits: no extra token calls.
	calls := len(f.tokenForms)
	if _, err := g.token(t.Context(), cfg, "PERSONAL_GOOGLE_OAUTH"); err != nil {
		t.Fatal(err)
	}
	if len(f.tokenForms) != calls {
		t.Fatal("live token refreshed needlessly")
	}
}

func TestGoogleBuilderScopeGating(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}

	for _, tc := range []struct {
		scopes string
		want   int
	}{
		{`["https://www.googleapis.com/auth/gmail.modify"]`, 3},
		{`["https://www.googleapis.com/auth/calendar"]`, 2},
		{bothScopes, 5},
	} {
		row := googleRow(tc.scopes)
		g, _ := testGoogle(t, f, row)
		src, err := g.Builder()(t.Context(), row, nil)
		if err != nil {
			t.Fatalf("scopes %s: %v", tc.scopes, err)
		}
		if got := len(src.Tools()); got != tc.want {
			t.Fatalf("scopes %s: %d tools, want %d", tc.scopes, got, tc.want)
		}
	}

	row := googleRow(`["https://www.googleapis.com/auth/drive"]`)
	g, _ := testGoogle(t, f, row)
	if _, err := g.Builder()(t.Context(), row, nil); err == nil {
		t.Fatal("unknown scopes accepted")
	}
}

func connectedSource(t *testing.T, f *fakeGoogle) (Source, *fakeSecrets) {
	t.Helper()
	row := googleRow(bothScopes)
	g, secrets := testGoogle(t, f, row)
	//nolint:gosec // G117: fake token fixture.
	live, _ := json.Marshal(tokenBundle{
		AccessToken: "at-live", RefreshToken: "rt-1", Expiry: time.Now().Add(time.Hour),
	})
	_ = secrets.Set(t.Context(), "PERSONAL_GOOGLE_OAUTH", string(live))
	src, err := g.Builder()(t.Context(), row, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return src, secrets
}

func toolByName(t *testing.T, src Source, name string) *tools.Tool {
	t.Helper()
	for _, tl := range src.Tools() {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("tool %s missing", name)
	return nil
}

func TestGmailToolsRoundTrip(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	src, _ := connectedSource(t, f)

	out, err := toolByName(t, src, "gmail_search").Execute(t.Context(), json.RawMessage(`{"query":"is:unread"}`))
	if err != nil || !strings.Contains(out, "subject: hi") {
		t.Fatalf("search = (%q, %v)", out, err)
	}
	out, err = toolByName(t, src, "gmail_read").Execute(t.Context(), json.RawMessage(`{"id":"m1"}`))
	if err != nil || !strings.Contains(out, "hello body") {
		t.Fatalf("read = (%q, %v)", out, err)
	}
	out, err = toolByName(t, src, "gmail_send").Execute(t.Context(),
		json.RawMessage(`{"to":"b@y","subject":"re: hi","body":"on my way"}`))
	if err != nil || !strings.Contains(out, "sent-1") {
		t.Fatalf("send = (%q, %v)", out, err)
	}
	if len(f.gmailSent) != 1 || !strings.Contains(f.gmailSent[0], "Subject: re: hi") ||
		!strings.Contains(f.gmailSent[0], "on my way") {
		t.Fatalf("sent rfc822 = %q", f.gmailSent)
	}
	// Every call authenticated with the live access token.
	for _, h := range f.authTokens {
		if h != "Bearer at-live" {
			t.Fatalf("auth header = %q", h)
		}
	}
}

func TestCalendarToolsRoundTrip(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	src, _ := connectedSource(t, f)

	out, err := toolByName(t, src, "calendar_list_events").Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil || !strings.Contains(out, "standup") || !strings.Contains(out, "zoom") {
		t.Fatalf("list = (%q, %v)", out, err)
	}
	out, err = toolByName(t, src, "calendar_create_event").Execute(t.Context(), json.RawMessage(
		`{"summary":"1:1","start":"2026-07-23T10:00:00Z","end":"2026-07-23T10:30:00Z","attendees":["b@y"]}`))
	if err != nil || !strings.Contains(out, "https://cal/ev-1") {
		t.Fatalf("create = (%q, %v)", out, err)
	}
	if len(f.events) != 1 || f.events[0]["summary"] != "1:1" {
		t.Fatalf("created event = %+v", f.events)
	}
}

func TestGoogleSourceTestRefreshesToken(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	src, secrets := connectedSource(t, f)

	if err := src.Test(t.Context()); err != nil {
		t.Fatalf("Test with live bundle: %v", err)
	}
	// Break the bundle: Test must fail honestly.
	_ = secrets.Set(t.Context(), "PERSONAL_GOOGLE_OAUTH", "not json")
	if err := src.Test(t.Context()); err == nil {
		t.Fatal("corrupt bundle accepted")
	}
}
