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
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

func TestPlainTextPrefersOverHTML(t *testing.T) {
	t.Parallel()
	parts := []gmailPart{
		{MimeType: "text/html", Body: gmailBody{Data: base64.URLEncoding.EncodeToString([]byte("<p>html</p>"))}},
		{MimeType: "text/plain", Body: gmailBody{Data: base64.URLEncoding.EncodeToString([]byte("plain"))}},
	}
	if got := plainText(parts); got != "plain" {
		t.Fatalf("plainText = %q, want %q", got, "plain")
	}
}

func TestHTMLPartFallsBackWhenNoPlainPart(t *testing.T) {
	t.Parallel()
	parts := []gmailPart{
		{MimeType: "text/html", Body: gmailBody{Data: base64.URLEncoding.EncodeToString([]byte("<p>only html</p>"))}},
	}
	if plainText(parts) != "" {
		t.Fatal("plainText should find nothing when there's no text/plain part")
	}
	if got := htmlPart(parts); string(got) != "<p>only html</p>" {
		t.Fatalf("htmlPart = %q, want the raw decoded HTML bytes", got)
	}
}

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
		if r.URL.Query().Get("q") == "from:nowhere.invalid" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"messages":[{"id":"m1"}]}`))
	})
	mux.HandleFunc("GET /gmail/v1/users/me/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		id := r.PathValue("id")
		switch id {
		case "m2": // HTML-only: a booking confirmation with no text/plain part.
			body := base64.URLEncoding.EncodeToString([]byte(
				`<html><body><p>Booking ref 817917166</p><p>Total: <b>EUR 214.50</b></p></body></html>`))
			// Fixed id: echoing the path value trips gosec's taint pass.
			_, _ = fmt.Fprintf(w, `{"id":"m2","snippet":"snip2","payload":{"headers":[
				{"name":"From","value":"noreply@kiwi.com"},{"name":"To","value":"b@y"},
				{"name":"Subject","value":"Your booking"},{"name":"Date","value":"today"}],
				"parts":[{"mimeType":"text/html","body":{"data":%q}}]}}`, body)
		case "m3": // has a PDF attachment.
			// Fixed id: echoing the path value trips gosec's taint pass.
			_, _ = fmt.Fprintf(w, `{"id":"m3","snippet":"snip3","payload":{"headers":[
				{"name":"From","value":"noreply@agoda.com"},{"name":"To","value":"b@y"},
				{"name":"Subject","value":"Your receipt"},{"name":"Date","value":"today"}],
				"parts":[
					{"mimeType":"text/plain","body":{"data":%q}},
					{"mimeType":"application/pdf","filename":"receipt.pdf","body":{"attachmentId":"att-1","size":41240}}
				]}}`, base64.URLEncoding.EncodeToString([]byte("see attached receipt")))
		default:
			body := base64.URLEncoding.EncodeToString([]byte("hello body"))
			// Fixed id: echoing the path value trips gosec's taint pass.
			_, _ = fmt.Fprintf(w, `{"id":"m1","snippet":"snip","payload":{"headers":[
				{"name":"From","value":"a@x"},{"name":"To","value":"b@y"},
				{"name":"Subject","value":"hi"},{"name":"Date","value":"today"}],
				"parts":[{"mimeType":"text/plain","body":{"data":%q}}]}}`, body)
		}
	})
	mux.HandleFunc("GET /gmail/v1/users/me/messages/{id}/attachments/{attachmentId}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.PathValue("attachmentId") != "att-1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, err := os.ReadFile("testdata/sample.pdf")
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		data := base64.URLEncoding.EncodeToString(raw)
		_, _ = fmt.Fprintf(w, `{"size":%d,"data":%q}`, len(raw), data)
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
	// Fakes the markitdown sidecar: echoes back a recognizable marker
	// plus the filename/mimetype headers it was called with, so tests
	// can assert gmail_read/gmail_read_attachment actually reached it
	// (rather than asserting real markitdown behavior — that's covered
	// by the sidecar's own build/run, not this Go test).
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
	g.MarkItDownURL = srv.URL
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
		{`["https://www.googleapis.com/auth/gmail.modify"]`, 4},
		{`["https://www.googleapis.com/auth/calendar"]`, 2},
		{bothScopes, 6},
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

// TestGmailSearchZeroResultsSuggestsBroadening pins a real search-
// coverage miss found in production: a from:-scoped query missed a
// real email (the sender's exact address differed from what was
// guessed), and the model gave up instead of retrying broader. A zero-
// result response must actively suggest broadening — not just state
// nothing matched — since the model won't always recall a long tool
// description many turns after reading it.
func TestGmailSearchZeroResultsSuggestsBroadening(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	src, _ := connectedSource(t, f)

	out, err := toolByName(t, src, "gmail_search").Execute(t.Context(),
		json.RawMessage(`{"query":"from:nowhere.invalid"}`))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "retry broader") {
		t.Fatalf("out = %q, want it to suggest broadening the search", out)
	}
}

// TestGmailReadFallsBackToMarkItDownForHTMLOnlyBody pins the fix for
// HTML-only mail (booking confirmations, receipts): gmail_read used to
// fall back to the truncated snippet whenever a message had no
// text/plain part. It must now hand the text/html part to markitdown
// instead — asserted here against the fake sidecar (real conversion
// behavior lives in the sidecar's own build/run, not this Go test).
func TestGmailReadFallsBackToMarkItDownForHTMLOnlyBody(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	src, _ := connectedSource(t, f)

	out, err := toolByName(t, src, "gmail_read").Execute(t.Context(), json.RawMessage(`{"id":"m2"}`))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "converted(filename=body.html, mimetype=text/html)") {
		t.Fatalf("read = %q, want it routed through markitdown", out)
	}
	if !strings.Contains(out, "Booking ref 817917166") {
		t.Fatalf("read = %q, want the original HTML content forwarded", out)
	}
	if strings.Contains(out, "snip2") {
		t.Fatalf("read = %q, want the converted body, not the snippet fallback", out)
	}
}

// TestGmailReadListsAttachmentsAndReadAttachmentUsesMarkItDown pins the
// fix for PDF-only receipts (a Kiwi/Agoda-style booking whose amount
// lives only in a PDF attachment): gmail_read must surface the
// attachment filename, and gmail_read_attachment must look up the real
// (long, opaque) Gmail attachment id itself — the model only ever
// supplies the short, copyable filename.
func TestGmailReadListsAttachmentsAndReadAttachmentUsesMarkItDown(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	src, _ := connectedSource(t, f)

	out, err := toolByName(t, src, "gmail_read").Execute(t.Context(), json.RawMessage(`{"id":"m3"}`))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "receipt.pdf") {
		t.Fatalf("read = %q, want it to list the attachment filename", out)
	}
	if strings.Contains(out, "att-1") {
		t.Fatalf("read = %q, want the raw attachment id kept out of what the model sees", out)
	}

	out, err = toolByName(t, src, "gmail_read_attachment").Execute(t.Context(),
		json.RawMessage(`{"message_id":"m3","filename":"receipt.pdf"}`))
	if err != nil {
		t.Fatalf("read_attachment: %v", err)
	}
	if !strings.Contains(out, "converted(filename=receipt.pdf") {
		t.Fatalf("read_attachment = %q, want it routed through markitdown with the given filename", out)
	}
}

func TestGmailReadAttachmentRejectsUnknownFilename(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	src, _ := connectedSource(t, f)

	_, err := toolByName(t, src, "gmail_read_attachment").Execute(t.Context(),
		json.RawMessage(`{"message_id":"m3","filename":"nope.pdf"}`))
	if err == nil {
		t.Fatal("expected an error for an unknown attachment filename")
	}
}

func TestFindAttachmentMatchesByFilename(t *testing.T) {
	t.Parallel()
	parts := []gmailPart{
		{Filename: "a.pdf", Body: gmailBody{AttachmentID: "id-a"}},
		{Filename: "b.docx", Body: gmailBody{AttachmentID: "id-b"}},
	}
	if id, ok := findAttachment(parts, "b.docx"); !ok || id != "id-b" {
		t.Fatalf("findAttachment(b.docx) = (%q, %v), want (id-b, true)", id, ok)
	}
	if _, ok := findAttachment(parts, "missing.pdf"); ok {
		t.Fatal("findAttachment should not match a filename that isn't present")
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
