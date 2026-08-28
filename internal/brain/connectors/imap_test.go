package connectors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestIMAPMailReadAttachmentEmitsMedia(t *testing.T) {
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

	collector := tools.NewCollector(func(_ context.Context, r io.Reader) (string, string, error) {
		_, _ = io.ReadAll(r)
		return "att-1", "application/pdf", nil
	})
	ctx := tools.WithCollector(t.Context(), collector)
	if _, err := imapToolByName(t, src, "mail_read_attachment").Execute(ctx,
		json.RawMessage(`{"message_id":"9","filename":"receipt.pdf"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	refs := collector.Drain()
	if len(refs) != 1 || refs[0].ID != "att-1" || refs[0].Name != "receipt.pdf" {
		t.Fatalf("refs = %+v, want one media ref for receipt.pdf", refs)
	}
}

func TestIMAPMailReadAttachmentEmitFailureDoesNotFailTool(t *testing.T) {
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

	collector := tools.NewCollector(func(context.Context, io.Reader) (string, string, error) {
		return "", "", errors.New("save failed")
	})
	ctx := tools.WithCollector(t.Context(), collector)
	out, err := imapToolByName(t, src, "mail_read_attachment").Execute(ctx,
		json.RawMessage(`{"message_id":"9","filename":"receipt.pdf"}`))
	if err != nil {
		t.Fatalf("Execute: %v, want the markdown conversion to still succeed", err)
	}
	if !strings.Contains(out, "converted(filename=receipt.pdf, mimetype=application/pdf)") {
		t.Fatalf("out = %q, want it routed through markitdown despite media emit failure", out)
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
	for _, want := range []string{"To: <b@y.com>", "Cc: <c@z.com>", "Subject: hi there", "the body", "From: me@example.com"} {
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

func TestIMAPMailSendDisplayNameRecipient(t *testing.T) {
	t.Parallel()
	src, sent := testIMAPSource(t, imapRow("smtp.example.com"), &fakeIMAPSession{})

	out, err := imapToolByName(t, src, "mail_send").Execute(t.Context(),
		json.RawMessage(`{"to":"Bob Jones <b@y.com>","subject":"hi","body":"b"}`))
	if err != nil || out != "sent" {
		t.Fatalf("Execute = (%q, %v)", out, err)
	}
	m := (*sent)[0]
	if !strings.Contains(string(m.msg), `To: "Bob Jones" <b@y.com>`) {
		t.Fatalf("message = %q, want the header to keep the display name", m.msg)
	}
	// RCPT TO (the SMTP envelope) gets the bare address only.
	if len(m.recipients) != 1 || m.recipients[0] != "b@y.com" {
		t.Fatalf("recipients = %v, want [b@y.com]", m.recipients)
	}
}

func TestIMAPMailSendQuotedDisplayNameWithComma(t *testing.T) {
	t.Parallel()
	src, sent := testIMAPSource(t, imapRow("smtp.example.com"), &fakeIMAPSession{})

	out, err := imapToolByName(t, src, "mail_send").Execute(t.Context(),
		json.RawMessage(`{"to":"\"Jones, Bob\" <b@y.com>","subject":"hi","body":"b"}`))
	if err != nil || out != "sent" {
		t.Fatalf("Execute = (%q, %v)", out, err)
	}
	m := (*sent)[0]
	// The comma inside the quoted display name must not split it into
	// two recipients.
	if len(m.recipients) != 1 || m.recipients[0] != "b@y.com" {
		t.Fatalf("recipients = %v, want a single b@y.com (quoted comma not split)", m.recipients)
	}
}

func TestIMAPMailSendUnparseableAddressErrors(t *testing.T) {
	t.Parallel()
	src, sent := testIMAPSource(t, imapRow("smtp.example.com"), &fakeIMAPSession{})

	_, err := imapToolByName(t, src, "mail_send").Execute(t.Context(),
		json.RawMessage(`{"to":"not an address","subject":"hi","body":"b"}`))
	if err == nil {
		t.Fatal("expected an error for an unparseable to address")
	}
	if len(*sent) != 0 {
		t.Fatalf("sent messages = %d, want 0", len(*sent))
	}
}

func TestIMAPMailSendEncodesNonASCIISubject(t *testing.T) {
	t.Parallel()
	src, sent := testIMAPSource(t, imapRow("smtp.example.com"), &fakeIMAPSession{})

	out, err := imapToolByName(t, src, "mail_send").Execute(t.Context(),
		json.RawMessage(`{"to":"b@y.com","subject":"héllo","body":"b"}`))
	if err != nil || out != "sent" {
		t.Fatalf("Execute = (%q, %v)", out, err)
	}
	m := (*sent)[0]
	msg := string(m.msg)
	if strings.Contains(msg, "Subject: héllo") {
		t.Fatalf("message = %q, want the non-ASCII subject RFC 2047-encoded, not raw", msg)
	}
	if !strings.Contains(msg, "Subject: =?utf-8?") {
		t.Fatalf("message = %q, want an RFC 2047 encoded-word subject", msg)
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

// rfc822PlainText is a simple text/plain fixture.
const rfc822PlainText = "From: Alice <alice@x.com>\r\n" +
	"To: Bob <bob@y.com>\r\n" +
	"Date: Wed, 22 Jul 2026 10:00:00 +0000\r\n" +
	"Subject: hello\r\n" +
	"Content-Type: text/plain; charset=UTF-8\r\n" +
	"\r\n" +
	"plain body text\r\n"

// rfc822Alternative is multipart/alternative with both text/plain and
// text/html parts.
const rfc822Alternative = "From: Alice <alice@x.com>\r\n" +
	"To: Bob <bob@y.com>\r\n" +
	"Subject: alt\r\n" +
	"Content-Type: multipart/alternative; boundary=\"BOUND\"\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: text/plain; charset=UTF-8\r\n" +
	"\r\n" +
	"plain alt body\r\n" +
	"--BOUND\r\n" +
	"Content-Type: text/html; charset=UTF-8\r\n" +
	"\r\n" +
	"<p>html alt body</p>\r\n" +
	"--BOUND--\r\n"

// rfc822HTMLOnly has only a text/html part, no text/plain.
const rfc822HTMLOnly = "From: Alice <alice@x.com>\r\n" +
	"To: Bob <bob@y.com>\r\n" +
	"Subject: html only\r\n" +
	"Content-Type: text/html; charset=UTF-8\r\n" +
	"\r\n" +
	"<p>only html here</p>\r\n"

// rfc822WithAttachment is multipart/mixed: a text/plain body plus a
// PDF attachment.
const rfc822WithAttachment = "From: Alice <alice@x.com>\r\n" +
	"To: Bob <bob@y.com>\r\n" +
	"Subject: receipt\r\n" +
	"Content-Type: multipart/mixed; boundary=\"MIX\"\r\n" +
	"\r\n" +
	"--MIX\r\n" +
	"Content-Type: text/plain; charset=UTF-8\r\n" +
	"\r\n" +
	"see attached\r\n" +
	"--MIX\r\n" +
	"Content-Type: application/pdf\r\n" +
	"Content-Disposition: attachment; filename=\"receipt.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"cGRmIGJ5dGVz\r\n" +
	"--MIX--\r\n"

func TestParseIMAPMessageBytesPlainText(t *testing.T) {
	t.Parallel()
	msg, err := parseIMAPMessageBytes([]byte(rfc822PlainText))
	if err != nil {
		t.Fatalf("parseIMAPMessageBytes: %v", err)
	}
	if !strings.Contains(msg.From, "alice@x.com") {
		t.Fatalf("From = %q", msg.From)
	}
	if !strings.Contains(msg.To, "bob@y.com") {
		t.Fatalf("To = %q", msg.To)
	}
	if msg.Subject != "hello" {
		t.Fatalf("Subject = %q, want hello", msg.Subject)
	}
	if msg.Date == "" {
		t.Fatal("Date not parsed")
	}
	if strings.TrimSpace(msg.Body) != "plain body text" {
		t.Fatalf("Body = %q, want plain body text", msg.Body)
	}
	if len(msg.Attachments) != 0 {
		t.Fatalf("Attachments = %v, want none", msg.Attachments)
	}
}

func TestParseIMAPMessageBytesPrefersPlainOverHTML(t *testing.T) {
	t.Parallel()
	msg, err := parseIMAPMessageBytes([]byte(rfc822Alternative))
	if err != nil {
		t.Fatalf("parseIMAPMessageBytes: %v", err)
	}
	if strings.TrimSpace(msg.Body) != "plain alt body" {
		t.Fatalf("Body = %q, want the text/plain part", msg.Body)
	}
}

func TestParseIMAPMessageBytesFallsBackToHTML(t *testing.T) {
	t.Parallel()
	msg, err := parseIMAPMessageBytes([]byte(rfc822HTMLOnly))
	if err != nil {
		t.Fatalf("parseIMAPMessageBytes: %v", err)
	}
	if !strings.Contains(msg.Body, "only html here") {
		t.Fatalf("Body = %q, want the html part as fallback", msg.Body)
	}
}

func TestParseIMAPMessageBytesListsAttachments(t *testing.T) {
	t.Parallel()
	msg, err := parseIMAPMessageBytes([]byte(rfc822WithAttachment))
	if err != nil {
		t.Fatalf("parseIMAPMessageBytes: %v", err)
	}
	if strings.TrimSpace(msg.Body) != "see attached" {
		t.Fatalf("Body = %q, want see attached", msg.Body)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("Attachments = %v, want 1", msg.Attachments)
	}
	a := msg.Attachments[0]
	if a.Filename != "receipt.pdf" || a.ContentType != "application/pdf" {
		t.Fatalf("attachment = %+v, want receipt.pdf/application/pdf", a)
	}
}

func TestFindIMAPAttachmentFound(t *testing.T) {
	t.Parallel()
	raw, ct, err := findIMAPAttachment([]byte(rfc822WithAttachment), "receipt.pdf")
	if err != nil {
		t.Fatalf("findIMAPAttachment: %v", err)
	}
	if ct != "application/pdf" {
		t.Fatalf("content type = %q, want application/pdf", ct)
	}
	if string(raw) != "pdf bytes" {
		t.Fatalf("raw = %q, want %q", raw, "pdf bytes")
	}
}

func TestFindIMAPAttachmentMissing(t *testing.T) {
	t.Parallel()
	_, _, err := findIMAPAttachment([]byte(rfc822WithAttachment), "nope.pdf")
	if err == nil || !strings.Contains(err.Error(), "no attachment named") {
		t.Fatalf("err = %v, want a no attachment named error", err)
	}
}

func TestFormatEnvelopeAddresses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		addrs []imap.Address
		want  string
	}{
		{"with display name", []imap.Address{{Name: "Alice", Mailbox: "alice", Host: "x.com"}}, "Alice <alice@x.com>"},
		{"without display name", []imap.Address{{Mailbox: "bob", Host: "y.com"}}, "bob@y.com"},
		{"multiple addresses", []imap.Address{
			{Name: "Alice", Mailbox: "alice", Host: "x.com"},
			{Mailbox: "bob", Host: "y.com"},
		}, "Alice <alice@x.com>, bob@y.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := formatEnvelopeAddresses(tc.addrs); got != tc.want {
				t.Fatalf("formatEnvelopeAddresses = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJoinAddresses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		hdr  string
		want string
	}{
		{"with display name", "Alice <alice@x.com>", `"Alice" <alice@x.com>`},
		{"without display name", "bob@y.com", "<bob@y.com>"},
		{"multiple addresses", "Alice <alice@x.com>, bob@y.com", `"Alice" <alice@x.com>, <bob@y.com>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addrs, err := mail.ParseAddressList(tc.hdr)
			if err != nil {
				t.Fatalf("ParseAddressList(%q): %v", tc.hdr, err)
			}
			if got := joinAddresses(addrs); got != tc.want {
				t.Fatalf("joinAddresses = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIMAPConfigPortDefaults(t *testing.T) {
	t.Parallel()
	if got := (IMAPConfig{}).port(); got != imapDefaultPort {
		t.Fatalf("port() default = %d, want %d", got, imapDefaultPort)
	}
	if got := (IMAPConfig{Port: 143}).port(); got != 143 {
		t.Fatalf("port() explicit = %d, want 143", got)
	}
	if got := (IMAPConfig{}).smtpPort(); got != imapDefaultSMTPPort {
		t.Fatalf("smtpPort() default = %d, want %d", got, imapDefaultSMTPPort)
	}
	if got := (IMAPConfig{SMTPPort: 465}).smtpPort(); got != 465 {
		t.Fatalf("smtpPort() explicit = %d, want 465", got)
	}
}

func TestAddressHost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		addr string
		want string
	}{
		{"has at", "me@example.com", "example.com"},
		{"no at", "not-an-email", "localhost"},
		{"trailing at", "me@", "localhost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := addressHost(tc.addr); got != tc.want {
				t.Fatalf("addressHost(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

func TestIMAPConfigEmail(t *testing.T) {
	t.Parallel()
	if got := (IMAPConfig{Username: "me@x.com", AccountEmail: "other@x.com"}).email(); got != "other@x.com" {
		t.Fatalf("email() with account_email set = %q, want other@x.com", got)
	}
	if got := (IMAPConfig{Username: "me@x.com"}).email(); got != "me@x.com" {
		t.Fatalf("email() falls back to username = %q, want me@x.com", got)
	}
}

func TestIMAPConfigInvalidJSON(t *testing.T) {
	t.Parallel()
	row := Connector{Name: "imapbox", Kind: "imap", Config: json.RawMessage(`not json`)}
	if _, err := imapConfig(row); err == nil {
		t.Fatal("expected an error for invalid config JSON")
	}
}

func TestBuildSummary(t *testing.T) {
	t.Parallel()
	internalDate := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)

	t.Run("nil envelope falls back to internal date", func(t *testing.T) {
		t.Parallel()
		sum := buildSummary(5, internalDate, nil)
		if sum.UID != 5 || sum.Date != internalDate.Format(time.RFC3339) {
			t.Fatalf("summary = %+v", sum)
		}
	})

	t.Run("envelope with a real date overwrites internal date", func(t *testing.T) {
		t.Parallel()
		envDate := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
		env := &imap.Envelope{Date: envDate, Subject: "hi", From: []imap.Address{{Mailbox: "a", Host: "x.com"}}}
		sum := buildSummary(5, internalDate, env)
		if sum.Date != envDate.Format(time.RFC3339) {
			t.Fatalf("Date = %q, want the envelope date", sum.Date)
		}
		if sum.Subject != "hi" || sum.From != "a@x.com" {
			t.Fatalf("summary = %+v", sum)
		}
	})

	t.Run("envelope with a zero date does not clobber internal date", func(t *testing.T) {
		t.Parallel()
		env := &imap.Envelope{Subject: "no date header"}
		sum := buildSummary(5, internalDate, env)
		if sum.Date != internalDate.Format(time.RFC3339) {
			t.Fatalf("Date = %q, want internalDate kept (envelope Date is zero)", sum.Date)
		}
		if sum.Subject != "no date header" {
			t.Fatalf("Subject = %q", sum.Subject)
		}
	})
}

func TestFindSnippetTextPart(t *testing.T) {
	t.Parallel()

	t.Run("nil structure", func(t *testing.T) {
		t.Parallel()
		if _, _, ok := findSnippetTextPart(nil); ok {
			t.Fatal("expected no text part for a nil structure")
		}
	})

	t.Run("prefers text/plain over text/html", func(t *testing.T) {
		t.Parallel()
		structure := &imap.BodyStructureMultiPart{
			Subtype: "alternative",
			Children: []imap.BodyStructure{
				&imap.BodyStructureSinglePart{Type: "text", Subtype: "html", Encoding: "quoted-printable"},
				&imap.BodyStructureSinglePart{Type: "text", Subtype: "plain", Encoding: "base64"},
			},
		}
		part, path, ok := findSnippetTextPart(structure)
		if !ok || part.Subtype != "plain" {
			t.Fatalf("part = %+v, ok = %v, want text/plain", part, ok)
		}
		if len(path) == 0 {
			t.Fatalf("path = %v, want a non-empty part path", path)
		}
	})

	t.Run("falls back to text/html when no text/plain exists", func(t *testing.T) {
		t.Parallel()
		structure := &imap.BodyStructureMultiPart{
			Subtype: "mixed",
			Children: []imap.BodyStructure{
				&imap.BodyStructureSinglePart{Type: "text", Subtype: "html"},
				&imap.BodyStructureSinglePart{Type: "application", Subtype: "pdf"},
			},
		}
		part, _, ok := findSnippetTextPart(structure)
		if !ok || part.Subtype != "html" {
			t.Fatalf("part = %+v, ok = %v, want the text/html fallback", part, ok)
		}
	})

	t.Run("no text part at all", func(t *testing.T) {
		t.Parallel()
		structure := &imap.BodyStructureSinglePart{Type: "application", Subtype: "pdf"}
		if _, _, ok := findSnippetTextPart(structure); ok {
			t.Fatal("expected no text part for an attachment-only message")
		}
	})
}

func TestDecodeSnippetBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		raw      []byte
		encoding string
		want     string
	}{
		{"plain 7bit passes through", []byte("hello world"), "7bit", "hello world"},
		{"unspecified encoding passes through", []byte("hello world"), "", "hello world"},
		{"base64 decodes", []byte(base64.StdEncoding.EncodeToString([]byte("hello base64 body"))), "base64", "hello base64 body"},
		{"quoted-printable decodes", []byte("hello=20quoted=2Dprintable"), "quoted-printable", "hello quoted-printable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := decodeSnippetBytes(tc.raw, tc.encoding); got != tc.want {
				t.Fatalf("decodeSnippetBytes(%q, %q) = %q, want %q", tc.raw, tc.encoding, got, tc.want)
			}
		})
	}

	t.Run("base64 partial trimmed to full quantum before decoding", func(t *testing.T) {
		t.Parallel()
		// "hello base64 partial cut mid-message" base64-encoded, then cut
		// mid-quantum (not a multiple of 4 bytes) to simulate a partial
		// fetch landing inside a base64 group.
		full := base64.StdEncoding.EncodeToString([]byte("hello base64 partial cut mid-message"))
		cut := full[:len(full)-2] // drop a partial trailing quantum
		got := decodeSnippetBytes([]byte(cut), "base64")
		if !strings.HasPrefix("hello base64 partial cut mid-message", got) || got == "" {
			t.Fatalf("decodeSnippetBytes(partial base64) = %q, want a valid prefix of the original text", got)
		}
	})
}
