package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeSlack is an httptest Slack Web API with the external upload
// flow; it records every call.
type fakeSlack struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	calls    []slackCall
	uploaded map[string][]byte
}

type slackCall struct {
	Method string
	Auth   string
	Form   map[string]string
	Body   map[string]any
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()
	f := &fakeSlack{t: t, uploaded: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlack) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/upload/") {
		data, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.uploaded[strings.TrimPrefix(r.URL.Path, "/upload/")] = data
		f.mu.Unlock()
		_, _ = io.WriteString(w, "OK")
		return
	}
	c := slackCall{Method: strings.TrimPrefix(r.URL.Path, "/"), Auth: r.Header.Get("Authorization")}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		_ = r.ParseForm()
		c.Form = map[string]string{}
		for k := range r.PostForm {
			c.Form[k] = r.PostForm.Get(k)
		}
	} else {
		_ = json.NewDecoder(r.Body).Decode(&c.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if c.Auth != "Bearer xoxb-secret" {
		_, _ = io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
		return
	}
	switch c.Method {
	case "chat.postMessage":
		if c.Body["channel"] == "C_GONE" {
			_, _ = io.WriteString(w, `{"ok":false,"error":"channel_not_found"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"ts":"1.1"}`)
	case "files.getUploadURLExternal":
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "upload_url": f.srv.URL + "/upload/F_" + c.Form["filename"], "file_id": "F_" + c.Form["filename"]})
	case "files.completeUploadExternal":
		_, _ = io.WriteString(w, `{"ok":true}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeSlack) callsOf(method string) []slackCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []slackCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// fakeTelegram records sendMessage bodies and sendDocument forms.
type fakeTelegram struct {
	srv       *httptest.Server
	mu        sync.Mutex
	messages  []map[string]any
	documents []map[string]string
	paths     []string
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.messages = append(f.messages, body)
		case strings.HasSuffix(r.URL.Path, "/sendDocument"):
			_ = r.ParseMultipartForm(1 << 20) //nolint:gosec // G120: test server, fixed small fixture body
			f.documents = append(f.documents, map[string]string{"chat_id": r.FormValue("chat_id"), "message_thread_id": r.FormValue("message_thread_id"), "caption": r.FormValue("caption")})
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

//nolint:gosec // G101: ref names, not credential values.
var testChannels = map[string]ChannelRef{
	"tg": {Kind: ChannelTelegram, CredentialRef: "TG_REF"},
	"sl": {Kind: ChannelSlack, CredentialRef: "SLACK_BOT"},
	"em": {Kind: ChannelEmail, ConnectorID: "imap-1"},
}

func lookupTestChannel(_ context.Context, id string) (ChannelRef, error) {
	c, ok := testChannels[id]
	if !ok {
		return ChannelRef{}, errors.New("channel not found")
	}
	return c, nil
}

func resolveTestToken(_ context.Context, ref string) (string, error) {
	switch ref {
	case "TG_REF":
		return "tg-secret", nil
	case "SLACK_BOT":
		return "xoxb-secret", nil
	}
	return "", errors.New("unknown ref")
}

type sentMail struct {
	ConnectorID, To, Subject, Body string
	Files                          []File
}

func testChannelAdapter(tg *fakeTelegram, sl *fakeSlack, mails *[]sentMail) *ChannelAdapter {
	a := &ChannelAdapter{Lookup: lookupTestChannel, ResolveToken: resolveTestToken,
		SendMail: func(_ context.Context, connectorID, to, subject, body string, files []File) error {
			*mails = append(*mails, sentMail{connectorID, to, subject, body, files})
			return nil
		}}
	if tg != nil {
		a.TelegramBase = tg.srv.URL
	}
	if sl != nil {
		a.SlackBase = sl.srv.URL
	}
	return a
}

func TestChannelAdapterTelegram(t *testing.T) {
	tg := newFakeTelegram(t)
	var mails []sentMail
	a := testChannelAdapter(tg, nil, &mails)
	p := Payload{Name: "inbox-digest", TextArtifacts: []TextArtifact{{Name: "digest.md", Content: "# Digest\n\n**important**"}}}
	if err := a.Deliver(t.Context(), json.RawMessage(`{"channel_id":"tg","chat_id":"-100","thread_id":"7"}`), "", p); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(tg.messages) != 1 || tg.messages[0]["chat_id"] != "-100" || tg.messages[0]["message_thread_id"] != "7" || tg.messages[0]["parse_mode"] != "MarkdownV2" {
		t.Fatalf("messages = %+v", tg.messages)
	}
	if text, _ := tg.messages[0]["text"].(string); !strings.Contains(text, "*Digest*") || !strings.Contains(text, "*important*") {
		t.Fatalf("text = %q", text)
	}
	if !strings.HasPrefix(tg.paths[0], "/bottg-secret/") {
		t.Fatalf("token not from the channel's credential_ref: %v", tg.paths)
	}
	if err := a.Deliver(t.Context(), json.RawMessage(`{"channel_id":"tg","chat_id":"-100","thread_id":"7"}`), "", Payload{Name: "n", Files: []File{{Name: "a.csv", Data: []byte("x")}}}); err != nil {
		t.Fatal(err)
	}
	if len(tg.documents) != 1 || tg.documents[0]["message_thread_id"] != "7" || tg.documents[0]["chat_id"] != "-100" {
		t.Fatalf("documents = %+v", tg.documents)
	}
}

func TestChannelAdapterErrors(t *testing.T) {
	var mails []sentMail
	a := testChannelAdapter(nil, nil, &mails)
	for name, config := range map[string]string{
		"bad config":      `not json`,
		"unknown channel": `{"channel_id":"nope","chat_id":"1"}`,
	} {
		if err := a.Deliver(t.Context(), json.RawMessage(config), "", Payload{Body: "hi"}); err == nil {
			t.Errorf("%s: Deliver = nil", name)
		}
	}
	noRef := &ChannelAdapter{Lookup: func(context.Context, string) (ChannelRef, error) {
		return ChannelRef{Kind: ChannelTelegram}, nil
	}, ResolveToken: resolveTestToken}
	if err := noRef.Deliver(t.Context(), json.RawMessage(`{"channel_id":"x","chat_id":"1"}`), "", Payload{}); err == nil {
		t.Error("a telegram channel without credential_ref delivered")
	}
	badRef := &ChannelAdapter{Lookup: func(context.Context, string) (ChannelRef, error) {
		return ChannelRef{Kind: ChannelSlack, CredentialRef: "OTHER"}, nil
	}, ResolveToken: resolveTestToken}
	if err := badRef.Deliver(t.Context(), json.RawMessage(`{"channel_id":"x","chat_id":"C1"}`), "", Payload{Body: "hi"}); err == nil {
		t.Error("an unresolvable bot token delivered")
	}
	if err := (&ChannelAdapter{}).Deliver(t.Context(), json.RawMessage(`{"channel_id":"tg"}`), "", Payload{}); err == nil {
		t.Error("an adapter without a lookup delivered")
	}
	noMail := &ChannelAdapter{Lookup: lookupTestChannel}
	if err := noMail.Deliver(t.Context(), json.RawMessage(`{"channel_id":"em","to":"a@x.com"}`), "", Payload{}); err == nil {
		t.Error("an email channel without SendMail delivered")
	}
}

func TestChannelAdapterSlack(t *testing.T) {
	sl := newFakeSlack(t)
	var mails []sentMail
	a := testChannelAdapter(nil, sl, &mails)
	cfg := json.RawMessage(`{"channel_id":"sl","chat_id":"C0123","thread_id":"1700.1"}`)
	p := Payload{Name: "weekly <report>", TextArtifacts: []TextArtifact{{Name: "r.md", Content: "# Summary\n\nSee **this** and [docs](https://x.example/a?b=1&c=2)."}}}
	if err := a.Deliver(t.Context(), cfg, "", p); err != nil {
		t.Fatalf("Deliver text: %v", err)
	}
	posts := sl.callsOf("chat.postMessage")
	if len(posts) != 1 || posts[0].Body["channel"] != "C0123" || posts[0].Body["thread_ts"] != "1700.1" {
		t.Fatalf("posts = %+v", posts)
	}
	text, _ := posts[0].Body["text"].(string)
	for _, want := range []string{"*weekly &lt;report&gt;*", "*Summary*", "See *this* and <https://x.example/a?b=1&amp;c=2|docs>."} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}

	files := Payload{Name: "export", Files: []File{{Name: "a.csv", Data: []byte("a,b")}, {Name: "b.pdf", Data: []byte("%PDF")}}}
	if err := a.Deliver(t.Context(), cfg, "", files); err != nil {
		t.Fatalf("Deliver files: %v", err)
	}
	gets := sl.callsOf("files.getUploadURLExternal")
	if len(gets) != 2 || gets[0].Form["filename"] != "a.csv" || gets[0].Form["length"] != "3" {
		t.Fatalf("upload url calls = %+v", gets)
	}
	if string(sl.uploaded["F_a.csv"]) != "a,b" || string(sl.uploaded["F_b.pdf"]) != "%PDF" {
		t.Fatalf("uploaded = %v", sl.uploaded)
	}
	done := sl.callsOf("files.completeUploadExternal")
	if len(done) != 2 || done[0].Body["channel_id"] != "C0123" || done[0].Body["thread_ts"] != "1700.1" || done[0].Body["initial_comment"] != "*export*" {
		t.Fatalf("complete calls = %+v", done)
	}
	if _, ok := done[1].Body["initial_comment"]; ok {
		t.Fatalf("the second file repeats the title: %+v", done[1].Body)
	}
	if len(sl.callsOf("chat.postMessage")) != 1 {
		t.Fatal("a files delivery also posted a message")
	}

	if err := a.Deliver(t.Context(), cfg, "", Payload{Body: "Mission complete: x", Links: []string{"https://t.example/missions/m1"}}); err != nil {
		t.Fatal(err)
	}
	if last := sl.callsOf("chat.postMessage"); len(last) != 2 || !strings.Contains(last[1].Body["text"].(string), "https://t.example/missions/m1") {
		t.Fatalf("completion post = %+v", last)
	}

	err := a.Deliver(t.Context(), json.RawMessage(`{"channel_id":"sl","chat_id":"C_GONE"}`), "", Payload{Body: "x"})
	if err == nil || !errors.Is(err, errMaybeDelivered) || strings.Contains(err.Error(), "xoxb-secret") {
		t.Fatalf("rejected post = %v, want a redacted maybe-delivered error", err)
	}
}

func TestChannelAdapterEmail(t *testing.T) {
	var mails []sentMail
	a := testChannelAdapter(nil, nil, &mails)
	p := Payload{Name: "digest", Body: "Mission complete: digest", Files: []File{{Name: "a.pdf", Data: []byte("%PDF")}},
		TextArtifacts: []TextArtifact{{Name: "d.md", Content: "the digest"}}}
	if err := a.Deliver(t.Context(), json.RawMessage(`{"channel_id":"em","to":"ops@example.com"}`), "", p); err != nil {
		t.Fatal(err)
	}
	if len(mails) != 1 {
		t.Fatalf("mails = %+v", mails)
	}
	m := mails[0]
	if m.ConnectorID != "imap-1" || m.To != "ops@example.com" || m.Subject != "Timothy mission: digest" ||
		!strings.HasPrefix(m.Body, "Mission complete: digest") || !strings.HasSuffix(m.Body, "the digest") || len(m.Files) != 1 {
		t.Fatalf("mail = %+v", m)
	}
	if err := a.Deliver(t.Context(), json.RawMessage(`{"channel_id":"em","to":"ops@example.com"}`), "", Payload{Subject: "Ad hoc", Body: "b"}); err != nil || mails[1].Subject != "Ad hoc" {
		t.Fatalf("ad hoc mail = %+v %v", mails, err)
	}
}

func TestChannelAdapterTest(t *testing.T) {
	var checked []string
	var mails []sentMail
	a := testChannelAdapter(nil, nil, &mails)
	a.Check = func(_ context.Context, id string) error {
		checked = append(checked, id)
		if id == "sl" {
			return errors.New("invalid_auth")
		}
		return nil
	}
	if err := a.Test(t.Context(), json.RawMessage(`{"channel_id":"tg","chat_id":"1"}`)); err != nil {
		t.Fatalf("telegram test: %v", err)
	}
	if err := a.Test(t.Context(), json.RawMessage(`{"channel_id":"sl","chat_id":"C1"}`)); err == nil {
		t.Fatal("slack test hid the connect error")
	}
	if err := a.Test(t.Context(), json.RawMessage(`{"channel_id":"em","to":"a@x.com"}`)); err != nil {
		t.Fatalf("email test: %v", err)
	}
	if err := a.Test(t.Context(), json.RawMessage(`{"channel_id":"nope"}`)); err == nil {
		t.Fatal("unknown channel passed the test")
	}
	if strings.Join(checked, ",") != "tg,sl" || len(mails) != 0 {
		t.Fatalf("checked = %v, mails = %d", checked, len(mails))
	}

	// Deliverer.Test routes a channel destination here and sends nothing.
	d := NewDeliverer(&fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "chan", Kind: "channel", Enabled: true, Config: json.RawMessage(`{"channel_id":"tg","chat_id":"1"}`)},
	}}, &fakeEventStore{}, nil, &WebhookAdapter{}, a, nil, nil, nil, discardLog())
	if err := d.Test(t.Context(), "d1"); err != nil || len(checked) != 3 {
		t.Fatalf("Deliverer.Test = %v, checked %v", err, checked)
	}
}

func TestSlackMrkdwn(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "hello", "hello"},
		{"bold", "a **b** c __d__", "a *b* c *d*"},
		{"heading", "# Title", "*Title*"},
		{"heading with bold", "## **Big** news ##", "*Big news*"},
		{"link", "see [docs](https://x.example/p)", "see <https://x.example/p|docs>"},
		{"escapes", "a < b & c > d", "a &lt; b &amp; c &gt; d"},
		{"inline code kept", "run `**x** [a](b)` now **y**", "run `**x** [a](b)` now *y*"},
		{"fence kept", "```\n# not a heading\n**x**\n```\n**y**", "```\n# not a heading\n**x**\n```\n*y*"},
		{"mention cannot ping", "<!channel> hi", "&lt;!channel&gt; hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SlackMrkdwn(tt.in); got != tt.want {
				t.Fatalf("SlackMrkdwn(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestChunkText(t *testing.T) {
	text := strings.Repeat("para one line.\n\n", 400)
	chunks := chunkText(text, slackMessageLimit)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	var n int
	for i, c := range chunks {
		if r := len([]rune(c)); r > slackMessageLimit {
			t.Fatalf("chunk %d has %d runes", i, r)
		}
		n += strings.Count(c, "para one line.")
	}
	if n != 400 {
		t.Fatalf("chunks hold %d paragraphs, want 400", n)
	}
	if got := chunkText("short", 10); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short = %v", got)
	}
	if got := chunkText(strings.Repeat("é", 25), 10); len(got) != 3 || got[0] != strings.Repeat("é", 10) {
		t.Fatalf("unbroken = %q", got)
	}
}
