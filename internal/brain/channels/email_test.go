package channels

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

func TestFromAllowed(t *testing.T) {
	allow, err := normalizeFromAllow([]string{" Ada@X.com", "@Example.org"})
	if err != nil {
		t.Fatal(err)
	}
	for from, want := range map[string]bool{
		"ada@x.com":           true,
		"bob@example.org":     true,
		"bob@sub.example.org": false,
		"bob@x.com":           false,
		"ada@x.co":            false,
		"evil@example.org.io": false,
		"example.org":         false,
	} {
		if got := fromAllowed(allow, from); got != want {
			t.Errorf("fromAllowed(%q) = %v, want %v", from, got, want)
		}
	}
}

func TestNormalizeFromAllow(t *testing.T) {
	got, err := normalizeFromAllow([]string{"A@B.co", "a@b.co", "", " @Mail.Example.COM "})
	if err != nil || strings.Join(got, ",") != "a@b.co,@mail.example.com" {
		t.Fatalf("normalize = %q %v", got, err)
	}
	for name, in := range map[string][]string{
		"empty":        nil,
		"blank":        {" "},
		"no at":        {"ada"},
		"display name": {"Ada <ada@x.com>"},
		"two ats":      {"a@b@c.com"},
		"bare domain":  {"@localhost"},
		"bad domain":   {"@-x.com"},
		"space":        {"a b@x.com"},
	} {
		if _, err := normalizeFromAllow(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	many := make([]string, maxFromAllow+1)
	for i := range many {
		many[i] = fmt.Sprintf("u%d@x.com", i)
	}
	if _, err := normalizeFromAllow(many); !errors.Is(err, ErrInvalid) {
		t.Fatalf("51 entries = %v", err)
	}
	if got, err := normalizeFromAllow(many[:maxFromAllow]); err != nil || len(got) != maxFromAllow {
		t.Fatalf("50 entries = %d %v", len(got), err)
	}
}

func TestValidateEmail(t *testing.T) {
	const conn = "11111111-1111-1111-1111-111111111111"
	ok := Channel{Name: "m", Kind: KindEmail, Config: Config{ConnectorID: conn, FromAllow: []string{"ADA@x.com"}}}
	if err := Validate(&ok); err != nil || ok.Config.FromAllow[0] != "ada@x.com" {
		t.Fatalf("valid email = %v %v", err, ok.Config.FromAllow)
	}
	for name, c := range map[string]Channel{
		"no connector":          {Name: "m", Kind: KindEmail, Config: Config{FromAllow: []string{"a@x.com"}}},
		"no allowlist":          {Name: "m", Kind: KindEmail, Config: Config{ConnectorID: conn}},
		"credential_ref":        {Name: "m", Kind: KindEmail, CredentialRef: "X", Config: Config{ConnectorID: conn, FromAllow: []string{"a@x.com"}}},
		"app token":             {Name: "m", Kind: KindEmail, Config: Config{ConnectorID: conn, FromAllow: []string{"a@x.com"}, AppTokenRef: "A"}},
		"allowlist on telegram": {Name: "t", Kind: KindTelegram, CredentialRef: "B", Config: Config{FromAllow: []string{"a@x.com"}}},
		"connector on slack":    {Name: "s", Kind: KindSlack, CredentialRef: "B", Config: Config{AppTokenRef: "A", ConnectorID: conn}},
	} {
		if err := Validate(&c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s = %v, want ErrInvalid", name, err)
		}
	}
}

func TestThreadRoot(t *testing.T) {
	for name, tc := range map[string]struct {
		m    connectors.MailMessage
		want string
	}{
		"references":  {connectors.MailMessage{MessageID: "c@x", InReplyTo: "b@x", References: []string{"a@x", "b@x"}}, "a@x"},
		"in-reply-to": {connectors.MailMessage{MessageID: "c@x", InReplyTo: "b@x"}, "b@x"},
		"own id":      {connectors.MailMessage{MessageID: "c@x"}, "c@x"},
		"no id":       {connectors.MailMessage{UID: 9}, "uid:9"},
	} {
		if got := threadRoot(tc.m); got != tc.want {
			t.Errorf("%s: threadRoot = %q, want %q", name, got, tc.want)
		}
	}
}

func TestMailInbound(t *testing.T) {
	in := mailInbound(connectors.MailMessage{UID: 3, MessageID: "m2@x", InReplyTo: "out1@t", References: []string{"m1@x", "out1@t"},
		FromAddress: "Ada@X.com", FromName: "Ada L", Subject: "Re: plans", Text: "yes please\r\n\r\nOn Mon, Ada wrote:\r\n> earlier"})
	want := inbound{DedupID: "email:m2@x", MessageID: "m2@x", UserID: "ada@x.com", DisplayName: "Ada L", ChatID: "m1@x",
		Private: true, Addressed: true, Text: "yes please", ReplyToID: "out1@t"}
	if in.DedupID != want.DedupID || in.MessageID != want.MessageID || in.UserID != want.UserID || in.DisplayName != want.DisplayName ||
		in.ChatID != want.ChatID || !in.Private || !in.Addressed || in.Text != want.Text || in.ReplyToID != want.ReplyToID || in.ThreadID != "" {
		t.Fatalf("inbound = %+v", in)
	}
	bare := mailInbound(connectors.MailMessage{UID: 4, FromAddress: "bob@x.com", Subject: " Lunch? "})
	if bare.Text != "Lunch?" || bare.DisplayName != "bob@x.com" || bare.MessageID != "uid:4" || bare.ChatID != "uid:4" {
		t.Fatalf("subject-only inbound = %+v", bare)
	}
}

func TestReplyText(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"top post":   {"Sounds good.\n\nOn Tue, Sep 22, 2026 at 10:00 AM Timothy <t@x.com> wrote:\n> Earlier text\n> more", "Sounds good."},
		"wrapped":    {"ok\n\nOn Tue, Sep 22, 2026 at 10:00 AM Timothy\n<t@x.com> wrote:\n> quoted", "ok"},
		"inline":     {"> question one\nanswer one\n> question two\nanswer two", "answer one\nanswer two"},
		"signature":  {"123456\n-- \nAda\nSent from my phone", "123456"},
		"outlook":    {"Allow once\r\n\r\n-----Original Message-----\r\nFrom: Timothy", "Allow once"},
		"plain":      {"  just text  ", "just text"},
		"on in text": {"On second thought, no.", "On second thought, no."},
	} {
		if got := replyText(tc.in); got != tc.want {
			t.Errorf("%s: replyText = %q, want %q", name, got, tc.want)
		}
	}
}

func TestReplySubject(t *testing.T) {
	for in, want := range map[string]string{"Plans": "Re: Plans", "Re: Plans": "Re: Plans", "RE: x": "RE: x", "": "Re: Timothy", " a ": "Re: a"} {
		if got := replySubject(in); got != want {
			t.Errorf("replySubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestButtonsTextAndReplyMatching(t *testing.T) {
	kb := permKeyboard(testPermID)
	if got := buttonsText(kb); got != "\n\nReply with one of: Allow once, Allow session, Deny" {
		t.Fatalf("buttonsText = %q", got)
	}
	var st convState
	st.rememberButtons("out1@t", kb)
	if _, ok := st.takeButton("out1@t", "maybe later\nAllow once"); ok {
		t.Fatal("a non-matching first line pressed a button")
	}
	if _, ok := st.takeButton("other@t", "Allow once"); ok {
		t.Fatal("a reply to another message pressed a button")
	}
	data, ok := st.takeButton("out1@t", "  allow SESSION \n\nOn Mon wrote:\n> x")
	if !ok || data != permCallback(testPermID, "session") {
		t.Fatalf("takeButton = %q %v", data, ok)
	}
	if _, ok := st.takeButton("out1@t", "Allow once"); ok {
		t.Fatal("buttons pressed twice")
	}
	for i := range maxAsks + 5 {
		st.rememberButtons(fmt.Sprintf("m%d", i), kb)
	}
	if len(st.Buttons) != maxAsks {
		t.Fatalf("remembered %d button messages, want %d", len(st.Buttons), maxAsks)
	}
	if _, ok := st.Buttons[fmt.Sprintf("m%d", maxAsks+4)]; !ok {
		t.Fatal("the newest button message was evicted")
	}
}

func TestHourlyBudget(t *testing.T) {
	b := newHourlyBudget()
	now := time.Now()
	for i := range mailPerHour {
		if !b.allow("c1", now) {
			t.Fatalf("mail %d refused", i+1)
		}
	}
	if b.allow("c1", now) {
		t.Fatal("mail 31 in the hour allowed")
	}
	if !b.allow("c2", now) {
		t.Fatal("another channel shares the budget")
	}
	if b.allow("c1", now.Add(time.Minute)) {
		t.Fatal("refilled too fast")
	}
	if !b.allow("c1", now.Add(3*time.Minute)) {
		t.Fatal("no refill after 3 minutes")
	}
}

func TestRedactAddress(t *testing.T) {
	err := redactAddress(errors.New("smtp rcpt to Ada@X.com: 550 no such user ada@x.com"), "ada@x.com")
	if strings.Contains(strings.ToLower(err.Error()), "ada@x.com") {
		t.Fatalf("address leaked: %v", err)
	}
	plain := errors.New("imap dial: timeout")
	if redactAddress(plain, "ada@x.com") != plain {
		t.Fatal("unrelated error rewritten")
	}
}

func emailTestAdapter(f *fakeMail, allow []string, save func(context.Context, io.Reader) (attachments.Attachment, error), log *slog.Logger) *emailAdapter {
	return newEmailAdapter(Channel{ID: "c1", Kind: KindEmail, Config: Config{ConnectorID: "x", FromAllow: allow}},
		emailEnv{mailbox: f.open, save: save, poll: func(context.Context) time.Duration { return time.Millisecond }, log: log})
}

func TestEmailReceiveFirstSight(t *testing.T) {
	f := newFakeMail(41)
	f.add(connectors.MailMessage{MessageID: "old@x", FromAddress: "ada@x.com", Text: "old"})
	a := emailTestAdapter(f, []string{"ada@x.com"}, nil, discardLog())
	items, next, err := a.receive(t.Context(), "")
	if err != nil || len(items) != 0 || next != "42" || f.newerCalls() != 0 {
		t.Fatalf("first sight = %v %q %v newer=%d, want no items, cursor 42", items, next, err, f.newerCalls())
	}
	items, next, err = a.receive(t.Context(), next)
	if err != nil || len(items) != 0 || next != "42" {
		t.Fatalf("after first sight = %v %q %v, want nothing replayed", items, next, err)
	}
	if len(f.sentMails()) != 0 {
		t.Fatal("receiving sent mail")
	}
	if me, err := a.connect(t.Context()); err != nil || me.Username != fakeMailbox {
		t.Fatalf("connect = %+v %v", me, err)
	}
}

func TestEmailReceiveFilters(t *testing.T) {
	var logs syncBuffer
	f := newFakeMail(10)
	f.add(connectors.MailMessage{MessageID: "own@t", FromAddress: "Timothy@Example.com", Text: "loop"})
	f.add(connectors.MailMessage{MessageID: "s1@evil", FromAddress: "stranger@evil.com", Text: "hi"})
	f.add(connectors.MailMessage{MessageID: "s2@evil", FromAddress: "Stranger@evil.com", Text: "hi again"})
	f.add(connectors.MailMessage{UID: 0})
	uid := f.add(connectors.MailMessage{MessageID: "m1@x", FromAddress: "Ada@X.com", FromName: "Ada", Subject: "Plans", Text: "hello",
		Attachments: []connectors.MailAttachment{{Filename: "a.txt", Size: 5}, {Filename: "huge.bin", Size: maxMailAttachmentBytes + 1}, {Filename: "bad.exe", Size: 3}}})
	f.files[fmt.Sprintf("%d/a.txt", uid)] = []byte("aaaaa")
	f.files[fmt.Sprintf("%d/bad.exe", uid)] = []byte("MZ!")
	var saved []string
	save := func(_ context.Context, r io.Reader) (attachments.Attachment, error) {
		raw, _ := io.ReadAll(r)
		if string(raw) == "MZ!" {
			return attachments.Attachment{}, attachments.ErrUnsupportedMIME
		}
		saved = append(saved, string(raw))
		return attachments.Attachment{ID: "att-" + string(raw)}, nil
	}
	a := emailTestAdapter(f, []string{"@x.com"}, save, slog.New(slog.NewTextHandler(&logs, nil)))
	items, next, err := a.receive(t.Context(), "10")
	if err != nil || next != "15" || len(items) != 1 {
		t.Fatalf("receive = %+v %q %v, want one item and cursor 15", items, next, err)
	}
	in := items[0]
	if in.UserID != "ada@x.com" || in.ChatID != "m1@x" || in.Text != "hello\n\nSkipped attachments: huge.bin, bad.exe" {
		t.Fatalf("item = %+v", in)
	}
	if len(in.Attachments) != 1 || in.Attachments[0] != (chat.AttachmentRef{ID: "att-aaaaa", Name: "a.txt"}) || len(saved) != 1 {
		t.Fatalf("attachments = %+v saved %v", in.Attachments, saved)
	}
	th, ok := a.thread("m1@x")
	if !ok || th.To != "ada@x.com" || th.Subject != "Plans" || th.MessageID != "m1@x" || strings.Join(th.References, ",") != "m1@x" {
		t.Fatalf("thread = %+v %v", th, ok)
	}
	out := logs.String()
	if strings.Count(out, "sender_hash") != 1 || strings.Contains(strings.ToLower(out), "stranger") || strings.Contains(out, "evil") {
		t.Fatalf("drop log = %s", out)
	}
}

func TestEmailReceiveAttachmentCap(t *testing.T) {
	f := newFakeMail(0)
	var atts []connectors.MailAttachment
	for i := range maxMailAttachments + 2 {
		atts = append(atts, connectors.MailAttachment{Filename: fmt.Sprintf("f%d.txt", i), Size: 1})
	}
	uid := f.add(connectors.MailMessage{MessageID: "m@x", FromAddress: "a@x.com", Text: "files", Attachments: atts})
	for _, att := range atts {
		f.files[fmt.Sprintf("%d/%s", uid, att.Filename)] = []byte(att.Filename)
	}
	save := func(_ context.Context, r io.Reader) (attachments.Attachment, error) {
		raw, _ := io.ReadAll(r)
		return attachments.Attachment{ID: string(raw)}, nil
	}
	a := emailTestAdapter(f, []string{"a@x.com"}, save, discardLog())
	items, _, err := a.receive(t.Context(), "0")
	if err != nil || len(items) != 1 || len(items[0].Attachments) != maxMailAttachments || !strings.HasSuffix(items[0].Text, "f8.txt, f9.txt") {
		t.Fatalf("items = %+v %v", items, err)
	}
}

func TestEmailReceiveBatchCap(t *testing.T) {
	f := newFakeMail(0)
	for i := range emailBatch + 5 {
		f.add(connectors.MailMessage{MessageID: fmt.Sprintf("m%d@x", i), FromAddress: "a@x.com", Text: "x"})
	}
	a := emailTestAdapter(f, []string{"a@x.com"}, nil, discardLog())
	items, next, err := a.receive(t.Context(), "0")
	if err != nil || len(items) != emailBatch || next != fmt.Sprint(emailBatch) {
		t.Fatalf("first poll = %d items, cursor %q %v", len(items), next, err)
	}
	items, next, _ = a.receive(t.Context(), next)
	if len(items) != 5 || next != fmt.Sprint(emailBatch+5) {
		t.Fatalf("second poll = %d items, cursor %q", len(items), next)
	}
}

func TestEmailReceiveWaitsForThePollInterval(t *testing.T) {
	f := newFakeMail(0)
	a := emailTestAdapter(f, []string{"a@x.com"}, nil, discardLog())
	a.env.poll = func(context.Context) time.Duration { return 80 * time.Millisecond }
	if _, _, err := a.receive(t.Context(), "0"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, _, err := a.receive(t.Context(), "0"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 70*time.Millisecond {
		t.Fatalf("second poll after %v, want the interval", d)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, cursor, err := a.receive(ctx, "7"); !errors.Is(err, context.Canceled) || cursor != "7" {
		t.Fatalf("cancelled wait = %q %v", cursor, err)
	}
}

// recAdapter records sends and edits; its caps are the test's.
type recAdapter struct {
	mu    sync.Mutex
	c     capabilities
	sends []recSend
	edits int
}

type recSend struct {
	text    string
	buttons [][]button
}

func (a *recAdapter) connect(context.Context) (identity, error)                { return identity{}, nil }
func (a *recAdapter) receive(context.Context, string) ([]inbound, string, error) { return nil, "", nil }
func (a *recAdapter) answerPress(context.Context, string, string) error         { return nil }
func (a *recAdapter) caps() capabilities                                         { return a.c }

func (a *recAdapter) send(_ context.Context, _ target, text string, buttons [][]button, _ bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sends = append(a.sends, recSend{text, buttons})
	return fmt.Sprintf("m%d", len(a.sends)), nil
}

func (a *recAdapter) edit(context.Context, target, string, string, [][]button) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.edits++
	return nil
}

func noEditRunner(ad *recAdapter, chatFn ChatFunc) *runner {
	s := &Service{log: discardLog(), now: time.Now, editEvery: time.Millisecond, chat: chatFn,
		store: NewStore(pgpool.New(context.Background(), "", discardLog()))}
	return newRunner(s, Channel{ID: "c1", Kind: KindEmail}, ad)
}

func TestTurnWithoutEditsSendsOnce(t *testing.T) {
	ad := &recAdapter{c: capabilities{MessageLimit: emailMessageLimit}}
	var req chat.Request
	chatFn := func(_ context.Context, r chat.Request) (string, <-chan stream.StreamEvent, error) {
		req = r
		out := make(chan stream.StreamEvent, 4)
		for _, d := range []string{"Hello", " there"} {
			out <- stream.StreamEvent{Type: stream.EventChunk, Text: d}
		}
		out <- stream.StreamEvent{Type: stream.EventDone}
		close(out)
		return r.SessionID, out, nil
	}
	r := noEditRunner(ad, chatFn)
	refs := []chat.AttachmentRef{{ID: "a1", Name: "n.txt"}}
	r.turn(t.Context(), turnJob{conv: Conversation{ID: "cv", SessionID: "s1"}, to: target{ChatID: "m1@x"}, text: "hi", attachments: refs})
	if len(ad.sends) != 1 || ad.sends[0].text != "Hello there" || ad.edits != 0 {
		t.Fatalf("sends = %+v edits = %d, want one final reply and no edits", ad.sends, ad.edits)
	}
	if len(req.Attachments) != 1 || req.Attachments[0] != refs[0] || req.Message != "hi" {
		t.Fatalf("chat request = %+v", req)
	}

	failing := &recAdapter{c: capabilities{MessageLimit: emailMessageLimit}}
	r = noEditRunner(failing, func(context.Context, chat.Request) (string, <-chan stream.StreamEvent, error) {
		return "", nil, errors.New("no model")
	})
	r.turn(t.Context(), turnJob{conv: Conversation{ID: "cv"}, to: target{ChatID: "m1@x"}, text: "hi"})
	if len(failing.sends) != 1 || failing.sends[0].text != "Something went wrong: no model" || failing.edits != 0 {
		t.Fatalf("failure sends = %+v edits = %d", failing.sends, failing.edits)
	}
}

func TestDrainWithoutButtonsRendersText(t *testing.T) {
	ad := &recAdapter{c: capabilities{MessageLimit: emailMessageLimit}}
	r := noEditRunner(ad, nil)
	r.svc.missions.ResolvePermission = func(context.Context, string, string) bool { return true }
	events := make(chan stream.StreamEvent, 4)
	events <- stream.StreamEvent{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{ID: testPermID, Tool: "shell", Rationale: "list files"}}
	events <- stream.StreamEvent{Type: stream.EventChunk, Text: "done"}
	events <- stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{Message: "boom"}}
	r.drain(t.Context(), turnJob{to: target{ChatID: "m1@x"}}, "", events, nil)
	if len(ad.sends) != 2 || ad.edits != 0 {
		t.Fatalf("sends = %+v edits = %d", ad.sends, ad.edits)
	}
	if ad.sends[0].text != "Timothy wants to run shell: list files\n\nReply with one of: Allow once, Allow session, Deny" || ad.sends[0].buttons != nil {
		t.Fatalf("buttons message = %+v", ad.sends[0])
	}
	if ad.sends[1].text != "Something went wrong: boom" {
		t.Fatalf("error message = %+v", ad.sends[1])
	}
}
