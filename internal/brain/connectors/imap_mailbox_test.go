package connectors

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

const rfc822Threaded = "From: Ada Lovelace <Ada@X.com>\r\n" +
	"To: me@example.com\r\n" +
	"Subject: Re: plans\r\n" +
	"Message-ID: <m3@x.com>\r\n" +
	"In-Reply-To: <m2@timothy>\r\n" +
	"References: <m1@x.com> <m2@timothy>\r\n" +
	"Content-Type: multipart/mixed; boundary=\"MIX\"\r\n" +
	"\r\n" +
	"--MIX\r\n" +
	"Content-Type: text/plain; charset=UTF-8\r\n" +
	"\r\n" +
	"sounds good\r\n" +
	"--MIX\r\n" +
	"Content-Type: application/pdf\r\n" +
	"Content-Disposition: attachment; filename=\"plan.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"cGRmIGJ5dGVz\r\n" +
	"--MIX--\r\n"

func TestParseIMAPMessageBytesThreadHeaders(t *testing.T) {
	t.Parallel()
	m, err := parseIMAPMessageBytes([]byte(rfc822Threaded))
	if err != nil {
		t.Fatal(err)
	}
	if m.MessageID != "m3@x.com" || m.InReplyTo != "m2@timothy" || strings.Join(m.References, ",") != "m1@x.com,m2@timothy" {
		t.Fatalf("ids = %q %q %q", m.MessageID, m.InReplyTo, m.References)
	}
	if m.FromAddress != "Ada@X.com" || m.FromName != "Ada Lovelace" || m.BodyHTML {
		t.Fatalf("from = %q %q html=%v", m.FromAddress, m.FromName, m.BodyHTML)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Size != len("pdf bytes") {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	html, _ := parseIMAPMessageBytes([]byte(rfc822HTMLOnly))
	if !html.BodyHTML {
		t.Fatal("html-only body not marked")
	}
}

func testMailbox(t *testing.T, sess *fakeIMAPSession) (*IMAPMailbox, *[]sentMessage) {
	t.Helper()
	src, sent := testIMAPSource(t, imapRow("smtp.example.com"), sess)
	return &IMAPMailbox{src: src}, sent
}

func TestIMAPMailboxNewer(t *testing.T) {
	t.Parallel()
	sess := &fakeIMAPSession{
		uids: []imap.UID{3, 5, 7, 9},
		messages: map[imap.UID]imapMessage{
			5: {MessageID: "a@x", FromAddress: "a@x.com", Subject: "hi", Body: "<p>Hello</p><script>x()</script><p>there</p>", BodyHTML: true,
				Attachments: []imapAttachment{{Filename: "f.txt", ContentType: "text/plain", Size: 4}}},
		},
	}
	b, _ := testMailbox(t, sess)
	got, err := b.Newer(t.Context(), 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].UID != 5 || got[1].UID != 7 {
		t.Fatalf("Newer = %+v, want UIDs 5 and 7", got)
	}
	if got[0].Text != "Hello\nthere" || got[0].MessageID != "a@x" || len(got[0].Attachments) != 1 || got[0].Attachments[0].Size != 4 {
		t.Fatalf("message = %+v", got[0])
	}
	if got[1].FromAddress != "" || got[1].MessageID != "" {
		t.Fatalf("unfetchable message = %+v, want UID only", got[1])
	}
	if sess.closeCount != 1 {
		t.Fatalf("sessions closed = %d, want 1", sess.closeCount)
	}
	sess.latest = 42
	if uid, err := b.LatestUID(t.Context()); err != nil || uid != 42 {
		t.Fatalf("LatestUID = %d %v", uid, err)
	}
	if b.Address() != "me@example.com" {
		t.Fatalf("Address = %q", b.Address())
	}
}

func TestIMAPMailboxHTMLViaMarkitdown(t *testing.T) {
	t.Parallel()
	b, _ := testMailbox(t, &fakeIMAPSession{})
	b.src.markItDownURL = markitdownStub(t).URL
	if got := b.htmlText(t.Context(), "<p>x</p>"); !strings.HasPrefix(got, "converted(filename=body.html, mimetype=text/html)") {
		t.Fatalf("htmlText = %q", got)
	}
}

func TestStripHTML(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"<html><body><p>One</p><p>Two <b>bold</b></p></body></html>": "One\nTwo bold",
		"<style>p{}</style>text<br>more":                               "text\nmore",
		"plain":                                                        "plain",
	} {
		if got := stripHTML(in); got != want {
			t.Errorf("stripHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIMAPMailboxReplyHeaders(t *testing.T) {
	t.Parallel()
	b, sent := testMailbox(t, &fakeIMAPSession{})
	id, err := b.Reply(t.Context(), "ada@x.com", "Re: plans", "body text", "m3@x.com", []string{"m1@x.com", "bad id", "m3@x.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !validMsgID(id) || !strings.HasSuffix(id, ".timothy@example.com") {
		t.Fatalf("message id = %q", id)
	}
	if len(*sent) != 1 || strings.Join((*sent)[0].recipients, ",") != "ada@x.com" || (*sent)[0].password != "secret-pw" {
		t.Fatalf("sent = %+v", *sent)
	}
	msg := string((*sent)[0].msg)
	for _, h := range []string{
		"From: me@example.com\r\n", "To: <ada@x.com>\r\n", "Subject: Re: plans\r\n", "Message-Id: <" + id + ">\r\n",
		"In-Reply-To: <m3@x.com>\r\n", "References: <m1@x.com> <m3@x.com>\r\n", "Content-Type: text/plain; charset=UTF-8\r\n",
	} {
		if !strings.Contains(msg, h) {
			t.Errorf("message lacks %q:\n%s", h, msg)
		}
	}
	if !strings.HasSuffix(msg, "\r\n\r\nbody text") {
		t.Fatalf("body = %q", msg)
	}
	other, _ := b.Reply(t.Context(), "ada@x.com", "s", "b", "", nil)
	if other == id {
		t.Fatal("message ids repeat")
	}
	if strings.Contains(string((*sent)[1].msg), "In-Reply-To") || strings.Contains(string((*sent)[1].msg), "References") {
		t.Fatalf("unthreaded reply carries thread headers: %s", (*sent)[1].msg)
	}
}

func TestIMAPMailboxReplyRejectsHeaderInjection(t *testing.T) {
	t.Parallel()
	b, sent := testMailbox(t, &fakeIMAPSession{})
	for name, call := range map[string][4]string{
		"to":          {"a@x.com\r\nBcc: evil@x.com", "s", "", ""},
		"subject":     {"a@x.com", "s\r\nX-Injected: yes", "", ""},
		"in-reply-to": {"a@x.com", "s", "id@x\nBcc: evil@x.com", ""},
		"references":  {"a@x.com", "s", "", "id@x\r\nBcc: evil@x.com"},
		"two to":      {"a@x.com, b@x.com", "s", "", ""},
	} {
		if _, err := b.Reply(t.Context(), call[0], call[1], "body", call[2], []string{call[3]}); err == nil {
			t.Errorf("%s: injection accepted", name)
		}
	}
	if len(*sent) != 0 {
		t.Fatalf("sent %d messages, want 0", len(*sent))
	}
}

func TestManagerIMAPMailbox(t *testing.T) {
	t.Parallel()
	withSMTP, noSMTP := imapRow("smtp.example.com"), imapRow("")
	withSMTP.ID, withSMTP.Enabled = "1", true
	noSMTP.ID, noSMTP.Name, noSMTP.Enabled = "2", "nosmtp", true
	off := imapRow("smtp.example.com")
	off.ID, off.Name = "3", "off"
	m := testManager(fakeRows{rows: []Connector{withSMTP, noSMTP, off,
		{ID: "4", Name: "gh", Kind: "github", CredentialRef: "GH", Enabled: true, Config: json.RawMessage(`{}`)}}})
	m.RegisterBuilder("imap", IMAPBuilder(nil, ""))
	m.RegisterBuilder("github", GitHubBuilder(nil))
	b, err := m.IMAPMailbox(context.Background(), "1")
	if err != nil || b.Address() != "me@example.com" {
		t.Fatalf("enabled imap with smtp: %v %v", b, err)
	}
	for _, id := range []string{"2", "3", "4", "missing"} {
		if _, err := m.IMAPMailbox(context.Background(), id); err == nil {
			t.Errorf("connector %s: want an error", id)
		}
	}
}
