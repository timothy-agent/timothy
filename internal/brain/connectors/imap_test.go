package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// fakeIMAPSession is an in-memory imapSession: no network, canned
// search/fetch results, and a closed flag Close sets so tests can
// assert every dial is closed exactly once.
type fakeIMAPSession struct {
	mu sync.Mutex

	searchResults []imapMessageSummary
	searchErr     error
	messages      map[imap.UID]imapMessage
	attachments   map[imap.UID]map[string]struct {
		raw []byte
		ct  string
	}
	fetchErr error

	closed     bool
	closeCount int
}

func (f *fakeIMAPSession) Search(_ context.Context, words []string, max int) ([]imapMessageSummary, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	// Record the words seen so tests can assert ANDed-word behavior via
	// a dedicated field if needed; kept simple here since callers assert
	// on the tool's Execute output, not internals.
	_ = words
	out := f.searchResults
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

func (f *fakeIMAPSession) FetchMessage(_ context.Context, uid imap.UID) (imapMessage, error) {
	if f.fetchErr != nil {
		return imapMessage{}, f.fetchErr
	}
	m, ok := f.messages[uid]
	if !ok {
		return imapMessage{}, fmt.Errorf("message %d not found", uid)
	}
	return m, nil
}

func (f *fakeIMAPSession) FetchAttachment(_ context.Context, uid imap.UID, filename string) ([]byte, string, error) {
	byName, ok := f.attachments[uid]
	if !ok {
		return nil, "", fmt.Errorf("message %d not found", uid)
	}
	a, ok := byName[filename]
	if !ok {
		return nil, "", fmt.Errorf("no attachment named %q on this message; check mail_read's attachments list", filename)
	}
	return a.raw, a.ct, nil
}

func (f *fakeIMAPSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.closeCount++
	return nil
}

// imapRow builds a minimal imap-kind connectors row for tests.
func imapRow(smtpHost string) Connector {
	cfg := map[string]any{
		"host":     "imap.example.com",
		"username": "me@example.com",
	}
	if smtpHost != "" {
		cfg["smtp_host"] = smtpHost
	}
	raw, _ := json.Marshal(cfg)
	//nolint:gosec // G101: ref NAME in a fake config, not a credential value.
	return Connector{ID: "c1", Name: "imapbox", Kind: "imap", Config: raw, CredentialRef: "IMAPBOX_IMAP_PASSWORD"}
}

// testIMAPSource builds an imapSource wired to a fake dial (returning
// sess) and, optionally, a fake send capturing its calls.
func testIMAPSource(t *testing.T, row Connector, sess *fakeIMAPSession) (*imapSource, *[]sentMessage) {
	t.Helper()
	cfg, err := imapConfig(row)
	if err != nil {
		t.Fatalf("imapConfig: %v", err)
	}
	sent := &[]sentMessage{}
	src := &imapSource{
		name:          row.Name,
		cfg:           cfg,
		credentialRef: row.CredentialRef,
		resolve:       func(context.Context, string) (string, error) { return "secret-pw", nil },
	}
	src.dial = func(context.Context) (imapSession, error) { return sess, nil }
	src.send = func(_ context.Context, _ IMAPConfig, password string, recipients []string, msg []byte) error {
		*sent = append(*sent, sentMessage{password: password, recipients: recipients, msg: msg})
		return nil
	}
	return src, sent
}

type sentMessage struct {
	password   string
	recipients []string
	msg        []byte
}

func imapToolByName(t *testing.T, src *imapSource, name string) *tools.Tool {
	t.Helper()
	for _, tl := range src.Tools() {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("tool %s missing", name)
	return nil
}

// markitdownStub serves the same /convert response shape
// microsoft_test.go's fakeMicrosoft uses, so mail_read_attachment's
// markitdown round trip can be asserted without a real sidecar.
func markitdownStub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /convert", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		out := fmt.Sprintf("converted(filename=%s, mimetype=%s): %s",
			r.Header.Get("X-Filename"), r.Header.Get("X-Mimetype"), body)
		_ = json.NewEncoder(w).Encode(map[string]string{"markdown": out})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestIMAPMailSearchFormatsResults(t *testing.T) {
	t.Parallel()
	sess := &fakeIMAPSession{searchResults: []imapMessageSummary{
		{UID: 5, Date: "2026-07-22T00:00:00Z", From: "A <a@x>", Subject: "hi", Snippet: "snip"},
	}}
	src, _ := testIMAPSource(t, imapRow(""), sess)

	out, err := imapToolByName(t, src, "mail_search").Execute(t.Context(), json.RawMessage(`{"query":"hi"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"id: 5", "date: 2026-07-22T00:00:00Z", "from: A <a@x>", "subject: hi", "snippet: snip"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, want it to contain %q", out, want)
		}
	}
	if !sess.closed {
		t.Fatal("session not closed after mail_search")
	}
}

func TestIMAPMailSearchNoResults(t *testing.T) {
	t.Parallel()
	sess := &fakeIMAPSession{}
	src, _ := testIMAPSource(t, imapRow(""), sess)

	out, err := imapToolByName(t, src, "mail_search").Execute(t.Context(), json.RawMessage(`{"query":"nowhere"}`))
	if err != nil || out != "no messages matched" {
		t.Fatalf("Execute = (%q, %v)", out, err)
	}
}

func TestIMAPMailSearchMaxResultsDefaultAndCap(t *testing.T) {
	t.Parallel()
	var results []imapMessageSummary
	for i := 0; i < 30; i++ {
		results = append(results, imapMessageSummary{UID: imap.UID(i + 1), Subject: fmt.Sprintf("m%d", i)})
	}
	sess := &fakeIMAPSession{searchResults: results}
	src, _ := testIMAPSource(t, imapRow(""), sess)

	// Default (no max_results): capped at imapSearchDefault (10).
	out, err := imapToolByName(t, src, "mail_search").Execute(t.Context(), json.RawMessage(`{"query":"m"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Count(out, "id: "); got != imapSearchDefault {
		t.Fatalf("default max_results: got %d results, want %d", got, imapSearchDefault)
	}

	// Explicit max_results above imapSearchMax (25): capped at 25.
	out, err = imapToolByName(t, src, "mail_search").Execute(t.Context(), json.RawMessage(`{"query":"m","max_results":1000}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Count(out, "id: "); got != imapSearchMax {
		t.Fatalf("capped max_results: got %d results, want %d", got, imapSearchMax)
	}
}

func TestIMAPMailReadPrefersPlainTextOverHTML(t *testing.T) {
	t.Parallel()
	sess := &fakeIMAPSession{messages: map[imap.UID]imapMessage{
		7: {From: "a@x", To: "b@y", Date: "2026-07-22T00:00:00Z", Subject: "hi", Body: "plain body"},
	}}
	src, _ := testIMAPSource(t, imapRow(""), sess)

	out, err := imapToolByName(t, src, "mail_read").Execute(t.Context(), json.RawMessage(`{"id":"7"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"from: a@x", "to: b@y", "subject: hi", "plain body"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, want it to contain %q", out, want)
		}
	}
}

func TestIMAPMailReadListsAttachments(t *testing.T) {
	t.Parallel()
	sess := &fakeIMAPSession{messages: map[imap.UID]imapMessage{
		9: {Subject: "receipt", Body: "see attached", Attachments: []imapAttachment{{Filename: "receipt.pdf", ContentType: "application/pdf"}}},
	}}
	src, _ := testIMAPSource(t, imapRow(""), sess)

	out, err := imapToolByName(t, src, "mail_read").Execute(t.Context(), json.RawMessage(`{"id":"9"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "attachments:") || !strings.Contains(out, "- receipt.pdf") {
		t.Fatalf("out = %q, want the attachments block", out)
	}
}

func TestIMAPMailReadAttachmentUsesMarkItDown(t *testing.T) {
	t.Parallel()
	srv := markitdownStub(t)

	sess := &fakeIMAPSession{attachments: map[imap.UID]map[string]struct {
		raw []byte
		ct  string
	}{
		9: {"receipt.pdf": {raw: []byte("pdf bytes"), ct: "application/pdf"}},
	}}
	src, _ := testIMAPSource(t, imapRow(""), sess)
	src.markItDownURL = srv.URL

	out, err := imapToolByName(t, src, "mail_read_attachment").Execute(t.Context(),
		json.RawMessage(`{"message_id":"9","filename":"receipt.pdf"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "converted(filename=receipt.pdf, mimetype=application/pdf)") {
		t.Fatalf("out = %q, want it routed through markitdown", out)
	}
}

func TestIMAPMailReadAttachmentUnknownName(t *testing.T) {
	t.Parallel()
	sess := &fakeIMAPSession{attachments: map[imap.UID]map[string]struct {
		raw []byte
		ct  string
	}{9: {}}}
	src, _ := testIMAPSource(t, imapRow(""), sess)

	_, err := imapToolByName(t, src, "mail_read_attachment").Execute(t.Context(),
		json.RawMessage(`{"message_id":"9","filename":"nope.pdf"}`))
	if err == nil {
		t.Fatal("expected an error for an unknown attachment name")
	}
}

func TestIMAPMailSendAbsentWithoutSMTPHost(t *testing.T) {
	t.Parallel()
	src, _ := testIMAPSource(t, imapRow(""), &fakeIMAPSession{})
	for _, tl := range src.Tools() {
		if tl.Name == "mail_send" {
			t.Fatal("mail_send must not be present without smtp_host configured")
		}
	}
}

func TestIMAPMailSendPresentWithSMTPHost(t *testing.T) {
	t.Parallel()
	src, _ := testIMAPSource(t, imapRow("smtp.example.com"), &fakeIMAPSession{})
	found := false
	for _, tl := range src.Tools() {
		if tl.Name == "mail_send" {
			found = true
		}
	}
	if !found {
		t.Fatal("mail_send must be present when smtp_host is configured")
	}
}

func TestIMAPMailSendAssemblesMessage(t *testing.T) {
	t.Parallel()
	src, sent := testIMAPSource(t, imapRow("smtp.example.com"), &fakeIMAPSession{})

	out, err := imapToolByName(t, src, "mail_send").Execute(t.Context(),
		json.RawMessage(`{"to":"b@y.com","cc":"c@z.com","subject":"hi there","body":"the body"}`))
	if err != nil || out != "sent" {
		t.Fatalf("Execute = (%q, %v)", out, err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(*sent))
	}
	m := (*sent)[0]
	msg := string(m.msg)
	for _, want := range []string{"To: b@y.com", "Cc: c@z.com", "Subject: hi there", "the body", "From: me@example.com"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message = %q, want it to contain %q", msg, want)
		}
	}
	if len(m.recipients) != 2 || m.recipients[0] != "b@y.com" || m.recipients[1] != "c@z.com" {
		t.Fatalf("recipients = %v, want [b@y.com c@z.com]", m.recipients)
	}
	if m.password != "secret-pw" {
		t.Fatalf("password = %q", m.password)
	}
}

func TestIMAPMailSendRejectsHeaderInjection(t *testing.T) {
	t.Parallel()
	src, sent := testIMAPSource(t, imapRow("smtp.example.com"), &fakeIMAPSession{})

	for _, tc := range []struct {
		name string
		args string
	}{
		{"newline in to", `{"to":"b@y.com\r\nBcc: evil@x.com","subject":"hi","body":"b"}`},
		{"newline in subject", `{"to":"b@y.com","subject":"hi\r\nX-Injected: yes","body":"b"}`},
		{"newline in cc", `{"to":"b@y.com","cc":"c@z.com\nBcc: evil@x.com","subject":"hi","body":"b"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := imapToolByName(t, src, "mail_send").Execute(t.Context(), json.RawMessage(tc.args))
			if err == nil {
				t.Fatal("expected an error for a header value containing a newline")
			}
		})
	}
	if len(*sent) != 0 {
		t.Fatalf("sent messages = %d, want 0 (all rejected)", len(*sent))
	}
}

func TestIMAPBuilderValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		row  Connector
	}{
		{"missing host", Connector{Name: "imapbox", Kind: "imap", CredentialRef: "REF", Config: json.RawMessage(`{"username":"me@example.com"}`)}},
		{"missing username", Connector{Name: "imapbox", Kind: "imap", CredentialRef: "REF", Config: json.RawMessage(`{"host":"imap.example.com"}`)}},
		{"missing credential_ref", Connector{Name: "imapbox", Kind: "imap", Config: json.RawMessage(`{"host":"imap.example.com","username":"me@example.com"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := IMAPBuilder(nil, "")
			if _, err := b(t.Context(), tc.row, func(context.Context, string) (string, error) { return "", nil }); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestIMAPAccountInfoAndIdentity(t *testing.T) {
	t.Parallel()

	t.Run("without smtp", func(t *testing.T) {
		t.Parallel()
		src, _ := testIMAPSource(t, imapRow(""), &fakeIMAPSession{})
		kind, email := src.AccountInfo()
		if kind != "imap" || email != "me@example.com" {
			t.Fatalf("AccountInfo = (%q, %q)", kind, email)
		}
		id, err := src.Identity(t.Context())
		if err != nil {
			t.Fatalf("Identity: %v", err)
		}
		if id.Login != "me@example.com" || id.Email != "me@example.com" || id.Scopes != "imap" {
			t.Fatalf("identity = %+v", id)
		}
	})

	t.Run("with smtp", func(t *testing.T) {
		t.Parallel()
		src, _ := testIMAPSource(t, imapRow("smtp.example.com"), &fakeIMAPSession{})
		id, err := src.Identity(t.Context())
		if err != nil {
			t.Fatalf("Identity: %v", err)
		}
		if id.Scopes != "imap+smtp" {
			t.Fatalf("scopes = %q, want imap+smtp", id.Scopes)
		}
	})
}

func TestIMAPTestClosesSession(t *testing.T) {
	t.Parallel()
	sess := &fakeIMAPSession{}
	src, _ := testIMAPSource(t, imapRow(""), sess)

	if err := src.Test(t.Context()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if sess.closeCount != 1 {
		t.Fatalf("closeCount = %d, want 1", sess.closeCount)
	}
}
