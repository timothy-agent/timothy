package connectors

import (
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

// fakeMicrosoft serves the token endpoint plus minimal Graph mail/
// calendar routes.
type fakeMicrosoft struct {
	mu         sync.Mutex
	tokenForms []url.Values
	mailSent   []map[string]any
	authTokens []string
}

func (f *fakeMicrosoft) server(t *testing.T) *httptest.Server {
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
		} else if form.Get("refresh_token") != "no-rotate" {
			// Microsoft rotates the refresh token on every refresh.
			res["refresh_token"] = "rt-rotated"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
	record := func(r *http.Request) {
		f.mu.Lock()
		f.authTokens = append(f.authTokens, r.Header.Get("Authorization"))
		f.mu.Unlock()
	}
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte(`{"displayName":"Ada Lovelace","mail":"ada@example.com","userPrincipalName":"ada@example.onmicrosoft.com"}`))
	})
	mux.HandleFunc("GET /me/messages", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.URL.Query().Get("$search") == `"nowhere"` {
			_, _ = w.Write([]byte(`{"value":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"id":"m1","subject":"hi","from":{"emailAddress":{"name":"A","address":"a@x"}},"receivedDateTime":"2026-07-22T00:00:00Z","bodyPreview":"snip"}]}`))
	})
	mux.HandleFunc("GET /me/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if got := r.Header.Get("Prefer"); got != `outlook.body-content-type="text"` {
			t.Fatalf("mail_read missing Prefer text header: %q", got)
		}
		id := r.PathValue("id")
		switch id {
		case "m2":
			_, _ = w.Write([]byte(`{"id":"m2","subject":"receipt","from":{"emailAddress":{"address":"noreply@x"}},
				"toRecipients":[{"emailAddress":{"address":"b@y"}}],"receivedDateTime":"today",
				"body":{"contentType":"text","content":"see attached receipt"},"hasAttachments":true}`))
		default:
			_, _ = w.Write([]byte(`{"id":"m1","subject":"hi","from":{"emailAddress":{"address":"a@x"}},
				"toRecipients":[{"emailAddress":{"address":"b@y"}}],"receivedDateTime":"today",
				"body":{"contentType":"text","content":"hello body"},"hasAttachments":false}`))
		}
	})
	mux.HandleFunc("GET /me/messages/{id}/attachments", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.PathValue("id") != "m2" {
			_, _ = w.Write([]byte(`{"value":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"id":"att-1","name":"receipt.pdf","contentType":"application/pdf"}]}`))
	})
	mux.HandleFunc("GET /me/messages/{id}/attachments/{attachmentId}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.PathValue("id") != "m2" || r.PathValue("attachmentId") != "att-1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data := base64.StdEncoding.EncodeToString([]byte("pdf bytes"))
		_, _ = fmt.Fprintf(w, `{"id":"att-1","name":"receipt.pdf","contentType":"application/pdf","contentBytes":%q}`, data)
	})
	mux.HandleFunc("POST /me/sendMail", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.mailSent = append(f.mailSent, in)
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /me/calendarView", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte(`{"value":[{"subject":"standup","location":{"displayName":"zoom"},
			"organizer":{"emailAddress":{"name":"Boss"}},
			"start":{"dateTime":"2026-07-22T09:00:00Z"},"end":{"dateTime":"2026-07-22T09:15:00Z"}}]}`))
	})
	mux.HandleFunc("POST /convert", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		body, _ := io.ReadAll(r.Body)
		out := fmt.Sprintf("converted(filename=%s, mimetype=%s): %s",
			r.Header.Get("X-Filename"), r.Header.Get("X-Mimetype"), body)
		_ = json.NewEncoder(w).Encode(map[string]string{"markdown": out})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const microsoftMailScopes = `["Mail.Read","Mail.Send"]`
const microsoftAllScopes = `["Mail.Read","Mail.Send","Calendars.Read","offline_access"]`

func microsoftRow(scopes string) Connector {
	//nolint:gosec // G101: ref NAMES in a fake config, not credential values.
	return Connector{
		ID: "c1", Name: "outlook", Kind: "microsoft",
		Config:        json.RawMessage(`{"client_id":"cid","client_secret_ref":"MSFT_SECRET","scopes":` + scopes + `}`),
		CredentialRef: "OUTLOOK_MSFT_OAUTH",
	}
}

func testMicrosoft(t *testing.T, f *fakeMicrosoft, row Connector) (*Microsoft, *fakeSecrets) {
	t.Helper()
	srv := f.server(t)
	secrets := newFakeSecrets()
	_ = secrets.Set(t.Context(), "MSFT_SECRET", "shh")
	m := NewMicrosoft(secrets, fakeRows{rows: []Connector{row}}, "https://timothy.example", slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Client = srv.Client()
	m.TokenURL = srv.URL + "/token"
	m.GraphBase = srv.URL
	m.MarkItDownURL = srv.URL
	return m, secrets
}

func TestMicrosoftStartAuthAppendsOfflineAccess(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	m, _ := testMicrosoft(t, f, microsoftRow(microsoftMailScopes))

	authURL, err := m.StartAuth(t.Context(), "c1")
	if err != nil {
		t.Fatalf("StartAuth: %v", err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != "cid" {
		t.Fatalf("auth params = %v", q)
	}
	if q.Get("redirect_uri") != "https://timothy.example/v1/connectors/oauth/callback" {
		t.Fatalf("redirect_uri = %s", q.Get("redirect_uri"))
	}
	scope := q.Get("scope")
	if !strings.Contains(scope, "Mail.Read") || !strings.Contains(scope, "offline_access") {
		t.Fatalf("scope = %s, want offline_access auto-appended", scope)
	}
}

func TestMicrosoftStartAuthDoesNotDuplicateOfflineAccess(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	m, _ := testMicrosoft(t, f, microsoftRow(microsoftAllScopes))

	authURL, err := m.StartAuth(t.Context(), "c1")
	if err != nil {
		t.Fatalf("StartAuth: %v", err)
	}
	u, _ := url.Parse(authURL)
	scope := u.Query().Get("scope")
	if strings.Count(scope, "offline_access") != 1 {
		t.Fatalf("scope = %q, want offline_access exactly once", scope)
	}
}

func TestMicrosoftOAuthStartAndCallback(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	m, secrets := testMicrosoft(t, f, microsoftRow(microsoftMailScopes))

	authURL, err := m.StartAuth(t.Context(), "c1")
	if err != nil {
		t.Fatalf("StartAuth: %v", err)
	}
	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")

	if !m.HasState(state) {
		t.Fatal("HasState should report the freshly started state as live")
	}

	name, err := m.HandleCallback(t.Context(), state, "the-code")
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if name != "outlook" {
		t.Fatalf("name = %s", name)
	}
	if m.HasState(state) {
		t.Fatal("state must be consumed after HandleCallback")
	}
	b := storedBundle(t, secrets, "OUTLOOK_MSFT_OAUTH")
	if b.AccessToken != "at-authorization_code" || b.RefreshToken != "rt-1" {
		t.Fatalf("bundle = %+v", b)
	}
	if got := f.tokenForms[0]; got.Get("code") != "the-code" || got.Get("client_secret") != "shh" {
		t.Fatalf("token form = %v", got)
	}

	// States are single-use.
	if _, err := m.HandleCallback(t.Context(), state, "again"); err == nil {
		t.Fatal("state replay accepted")
	}
}

func TestMicrosoftRefreshRotatesRefreshToken(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	m, secrets := testMicrosoft(t, f, microsoftRow(microsoftMailScopes))

	//nolint:gosec // G117: fake token fixture.
	expired, _ := json.Marshal(tokenBundle{
		AccessToken: "stale", RefreshToken: "rt-old", Expiry: time.Now().Add(-time.Hour),
	})
	_ = secrets.Set(t.Context(), "OUTLOOK_MSFT_OAUTH", string(expired))

	cfg, _ := microsoftConfig(microsoftRow(microsoftMailScopes))
	token, err := m.token(t.Context(), cfg, "OUTLOOK_MSFT_OAUTH")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if token != "at-refresh_token" {
		t.Fatalf("token = %s", token)
	}
	b := storedBundle(t, secrets, "OUTLOOK_MSFT_OAUTH")
	// Microsoft rotates the refresh token on every refresh: the new one
	// (from the fake's response) must replace the old.
	if b.RefreshToken != "rt-rotated" {
		t.Fatalf("persisted refresh token = %q, want the rotated one", b.RefreshToken)
	}
	if b.AccessToken != "at-refresh_token" {
		t.Fatalf("persisted access token = %q", b.AccessToken)
	}
	// The refresh call carried scope too.
	last := f.tokenForms[len(f.tokenForms)-1]
	if last.Get("scope") == "" {
		t.Fatal("refresh form missing scope")
	}
}

func TestMicrosoftRefreshKeepsOldTokenWhenResponseOmitsIt(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	m, secrets := testMicrosoft(t, f, microsoftRow(microsoftMailScopes))

	//nolint:gosec // G117: fake token fixture.
	expired, _ := json.Marshal(tokenBundle{
		AccessToken: "stale", RefreshToken: "no-rotate", Expiry: time.Now().Add(-time.Hour),
	})
	_ = secrets.Set(t.Context(), "OUTLOOK_MSFT_OAUTH", string(expired))

	cfg, _ := microsoftConfig(microsoftRow(microsoftMailScopes))
	if _, err := m.token(t.Context(), cfg, "OUTLOOK_MSFT_OAUTH"); err != nil {
		t.Fatalf("token: %v", err)
	}
	b := storedBundle(t, secrets, "OUTLOOK_MSFT_OAUTH")
	if b.RefreshToken != "no-rotate" {
		t.Fatalf("refresh token = %q, want the old one kept when the response omits a new one", b.RefreshToken)
	}
}

func TestMicrosoftBuilderScopeGating(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}

	for _, tc := range []struct {
		scopes string
		want   int
	}{
		{`["Mail.Read"]`, 3},
		{`["Mail.Send"]`, 1},
		{`["Calendars.Read"]`, 1},
		{microsoftAllScopes, 5},
	} {
		row := microsoftRow(tc.scopes)
		m, _ := testMicrosoft(t, f, row)
		src, err := m.Builder()(t.Context(), row, nil)
		if err != nil {
			t.Fatalf("scopes %s: %v", tc.scopes, err)
		}
		if got := len(src.Tools()); got != tc.want {
			t.Fatalf("scopes %s: %d tools, want %d", tc.scopes, got, tc.want)
		}
	}

	row := microsoftRow(`["Files.Read"]`)
	m, _ := testMicrosoft(t, f, row)
	if _, err := m.Builder()(t.Context(), row, nil); err == nil {
		t.Fatal("unknown scopes accepted")
	}
}

// TestMicrosoftNoSendScopeMeansNoSendTool pins the scope-gating matrix
// entry the task calls out explicitly: Mail.Read alone must never
// surface mail_send.
func TestMicrosoftNoSendScopeMeansNoSendTool(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	row := microsoftRow(`["Mail.Read"]`)
	m, _ := testMicrosoft(t, f, row)
	src, err := m.Builder()(t.Context(), row, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range src.Tools() {
		if tl.Name == "mail_send" {
			t.Fatal("mail_send must not be present without Mail.Send scope")
		}
	}
}

func connectedMicrosoftSource(t *testing.T, f *fakeMicrosoft) (Source, *fakeSecrets) {
	t.Helper()
	row := microsoftRow(microsoftAllScopes)
	m, secrets := testMicrosoft(t, f, row)
	//nolint:gosec // G117: fake token fixture.
	live, _ := json.Marshal(tokenBundle{
		AccessToken: "at-live", RefreshToken: "rt-1", Expiry: time.Now().Add(time.Hour),
	})
	_ = secrets.Set(t.Context(), "OUTLOOK_MSFT_OAUTH", string(live))
	src, err := m.Builder()(t.Context(), row, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return src, secrets
}

func microsoftToolByName(t *testing.T, src Source, name string) *tools.Tool {
	t.Helper()
	for _, tl := range src.Tools() {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("tool %s missing", name)
	return nil
}

func TestOutlookMailToolsRoundTrip(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	src, _ := connectedMicrosoftSource(t, f)

	out, err := microsoftToolByName(t, src, "mail_search").Execute(t.Context(), json.RawMessage(`{"query":"hi"}`))
	if err != nil || !strings.Contains(out, "subject: hi") {
		t.Fatalf("search = (%q, %v)", out, err)
	}
	out, err = microsoftToolByName(t, src, "mail_search").Execute(t.Context(), json.RawMessage(`{"query":"nowhere"}`))
	if err != nil || !strings.Contains(out, "no messages matched") {
		t.Fatalf("empty search = (%q, %v)", out, err)
	}

	out, err = microsoftToolByName(t, src, "mail_read").Execute(t.Context(), json.RawMessage(`{"id":"m1"}`))
	if err != nil || !strings.Contains(out, "hello body") {
		t.Fatalf("read = (%q, %v)", out, err)
	}

	out, err = microsoftToolByName(t, src, "mail_send").Execute(t.Context(),
		json.RawMessage(`{"to":"b@y","subject":"re: hi","body":"on my way"}`))
	if err != nil || out != "sent" {
		t.Fatalf("send = (%q, %v)", out, err)
	}
	if len(f.mailSent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(f.mailSent))
	}
	msg, _ := f.mailSent[0]["message"].(map[string]any)
	if msg["subject"] != "re: hi" {
		t.Fatalf("sent message = %+v", f.mailSent[0])
	}

	for _, h := range f.authTokens {
		if h != "Bearer at-live" {
			t.Fatalf("auth header = %q", h)
		}
	}
}

func TestOutlookMailReadListsAttachmentsAndReadAttachmentUsesMarkItDown(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	src, _ := connectedMicrosoftSource(t, f)

	out, err := microsoftToolByName(t, src, "mail_read").Execute(t.Context(), json.RawMessage(`{"id":"m2"}`))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "receipt.pdf") {
		t.Fatalf("read = %q, want it to list the attachment name", out)
	}

	out, err = microsoftToolByName(t, src, "mail_read_attachment").Execute(t.Context(),
		json.RawMessage(`{"message_id":"m2","filename":"receipt.pdf"}`))
	if err != nil {
		t.Fatalf("read_attachment: %v", err)
	}
	if !strings.Contains(out, "converted(filename=receipt.pdf, mimetype=application/pdf)") {
		t.Fatalf("read_attachment = %q, want it routed through markitdown", out)
	}
}

func TestOutlookMailReadAttachmentRejectsUnknownName(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	src, _ := connectedMicrosoftSource(t, f)

	_, err := microsoftToolByName(t, src, "mail_read_attachment").Execute(t.Context(),
		json.RawMessage(`{"message_id":"m2","filename":"nope.pdf"}`))
	if err == nil {
		t.Fatal("expected an error for an unknown attachment name")
	}
}

func TestOutlookCalendarListEvents(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	src, _ := connectedMicrosoftSource(t, f)

	out, err := microsoftToolByName(t, src, "calendar_list_events").Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil || !strings.Contains(out, "standup") || !strings.Contains(out, "zoom") ||
		!strings.Contains(out, "Boss") {
		t.Fatalf("list = (%q, %v)", out, err)
	}
}

func TestMicrosoftSourceIdentity(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	src, _ := connectedMicrosoftSource(t, f)

	idr, ok := src.(identifier)
	if !ok {
		t.Fatal("microsoftSource must implement identifier")
	}
	id, err := idr.Identity(t.Context())
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if id.Name != "Ada Lovelace" || id.Email != "ada@example.com" || id.Login != "ada@example.onmicrosoft.com" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestMicrosoftSourceTestRefreshesToken(t *testing.T) {
	t.Parallel()
	f := &fakeMicrosoft{}
	src, secrets := connectedMicrosoftSource(t, f)

	if err := src.Test(t.Context()); err != nil {
		t.Fatalf("Test with live bundle: %v", err)
	}
	_ = secrets.Set(t.Context(), "OUTLOOK_MSFT_OAUTH", "not json")
	if err := src.Test(t.Context()); err == nil {
		t.Fatal("corrupt bundle accepted")
	}
}

func TestMicrosoftTokenErrorMapping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "invalid_grant expired refresh token",
			status: http.StatusBadRequest,
			body:   `{"error":"invalid_grant","error_description":"AADSTS700084: expired"}`,
			want:   "Microsoft authorization expired or was revoked — reconnect to re-authorize",
		},
		{
			name:   "other oauth error keeps status and code",
			status: http.StatusBadRequest,
			body:   `{"error":"invalid_client","error_description":"bad client"}`,
			want:   `Microsoft authorization failed (status 400, error "invalid_client") — reconnect to re-authorize`,
		},
		{
			name:   "generic 500 with no parseable error",
			status: http.StatusInternalServerError,
			body:   `<html>Internal Server Error</html>`,
			want:   "Microsoft authorization failed (status 500)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}
			err := microsoftTokenError(resp)
			if err.Error() != tc.want {
				t.Fatalf("microsoftTokenError = %q, want %q", err.Error(), tc.want)
			}
			if strings.Contains(err.Error(), "error_description") {
				t.Fatalf("microsoftTokenError leaked raw JSON: %q", err.Error())
			}
		})
	}
}

func TestMicrosoftAPIErrorMapping(t *testing.T) {
	t.Parallel()
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"bad token"}}`))}
	err := microsoftAPIError(resp)
	if err.Error() != "Microsoft authorization expired or was revoked — reconnect to re-authorize" {
		t.Fatalf("microsoftAPIError = %q", err.Error())
	}

	resp2 := &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"not found"}}`))}
	err2 := microsoftAPIError(resp2)
	if !strings.Contains(err2.Error(), "404") {
		t.Fatalf("microsoftAPIError = %q, want status 404 kept", err2.Error())
	}
}
