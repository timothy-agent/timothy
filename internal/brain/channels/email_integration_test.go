//go:build integration

package channels

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// randomUUID is a connector id; the fake mailbox ignores it.
func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func createEmailChannel(t *testing.T, s *Store, name string, allow ...string) string {
	t.Helper()
	id, err := s.Create(t.Context(), Channel{Name: name, Kind: KindEmail, Enabled: true,
		Config: Config{ConnectorID: randomUUID(), FromAllow: allow}})
	if err != nil {
		t.Fatalf("create email channel: %v", err)
	}
	return id
}

// startEmail runs the service with f as every email channel's mailbox
// and a fast poll, and waits for the first-sight cursor.
func startEmail(t *testing.T, s *Store, channelID string, f *fakeMail, chatFn ChatFunc, deps MissionDeps,
	save func(context.Context, io.Reader) (attachments.Attachment, error)) (stop func()) {
	t.Helper()
	svc := New(s, chatFn, deps, nil, nil, discardLog())
	svc.mail.mailbox, svc.mail.save = f.open, save
	svc.mail.poll = func(context.Context) time.Duration { return 20 * time.Millisecond }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(ctx, nil)
	}()
	waitFor(t, "the first-sight cursor", func() bool {
		st, err := s.GetState(t.Context(), channelID)
		return err == nil && st.Cursor != ""
	})
	return func() {
		cancel()
		<-done
	}
}

// waitCursor waits until the channel's cursor is want.
func waitCursor(t *testing.T, s *Store, channelID, want string) {
	t.Helper()
	waitFor(t, "cursor "+want, func() bool {
		st, err := s.GetState(t.Context(), channelID)
		return err == nil && st.Cursor == want
	})
}

func (f *fakeMail) waitSent(t *testing.T, n int) []sentMail {
	t.Helper()
	waitFor(t, "sent mail", func() bool { return len(f.sentMails()) >= n })
	return f.sentMails()
}

func countRows(t *testing.T, pool *pgpool.Pool, table, channelID string) int {
	t.Helper()
	db, _ := pool.Get()
	var n int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE channel_id = $1`, channelID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestEmailOffAllowlistCreatesNothing is the regression gate: mail from
// a sender off the allowlist reaches no model, sends nothing and writes
// no pairing, inbound or conversation row; the cursor still passes it.
func TestEmailOffAllowlistCreatesNothing(t *testing.T) {
	s, pool := testStore(t)
	id := createEmailChannel(t, s, runTag()+"deny", "ada@x.com", "@team.org")
	f := newFakeMail(100)
	fc := &fakeChat{}
	stop := startEmail(t, s, id, f, fc.chat, MissionDeps{}, nil)
	defer stop()
	waitCursor(t, s, id, "100")

	f.add(connectors.MailMessage{MessageID: "s1@evil.com", FromAddress: "stranger@evil.com", Subject: "hi", Text: "hello"})
	f.add(connectors.MailMessage{MessageID: "s2@evil.com", FromAddress: "ada@x.com.evil.com", Subject: "hi", Text: "hello"})
	f.add(connectors.MailMessage{MessageID: "own@t", FromAddress: fakeMailbox, Subject: "Re: hi", Text: "loop"})
	waitCursor(t, s, id, "103")
	time.Sleep(100 * time.Millisecond)
	if fc.count() != 0 || len(f.sentMails()) != 0 {
		t.Fatalf("dropped mail reached the model %d times, sent %d", fc.count(), len(f.sentMails()))
	}
	for _, table := range []string{"channel_pairings", "channel_inbound", "channel_conversations"} {
		if n := countRows(t, pool, table, id); n != 0 {
			t.Fatalf("%s rows = %d, want 0", table, n)
		}
	}
}

// TestEmailAutomatedMailDropped: an autoresponse or list mail from an
// allowlisted sender, paired or not, gets no reply, turn or pairing; a
// normal mail after it still gets its turn.
func TestEmailAutomatedMailDropped(t *testing.T) {
	s, pool := testStore(t)
	id := createEmailChannel(t, s, runTag()+"auto", "@x.com")
	approveSender(t, s, id, "ada@x.com")
	f := newFakeMail(0)
	fc := &fakeChat{}
	stop := startEmail(t, s, id, f, fc.chat, MissionDeps{}, nil)
	defer stop()
	waitCursor(t, s, id, "0")

	f.add(connectors.MailMessage{MessageID: "v1@x.com", FromAddress: "ada@x.com", Subject: "Out of office", Text: "away", AutoSubmitted: "auto-replied"})
	f.add(connectors.MailMessage{MessageID: "v2@x.com", FromAddress: "bob@x.com", Subject: "Out of office", Text: "away", AutoSubmitted: "Auto-Replied; owner=bob"})
	f.add(connectors.MailMessage{MessageID: "l1@x.com", FromAddress: "list@x.com", Subject: "Digest", Text: "news", ListID: "<team.x.com>"})
	waitCursor(t, s, id, "3")
	time.Sleep(100 * time.Millisecond)
	if fc.count() != 0 || len(f.sentMails()) != 0 {
		t.Fatalf("automated mail reached the model %d times, sent %d", fc.count(), len(f.sentMails()))
	}
	if n := countRows(t, pool, "channel_pairings", id); n != 1 {
		t.Fatalf("channel_pairings rows = %d, want only the approved sender", n)
	}

	f.add(connectors.MailMessage{MessageID: "n1@x.com", FromAddress: "ada@x.com", Subject: "Plans", Text: "hello", AutoSubmitted: "no"})
	waitFor(t, "the turn", func() bool { return fc.count() == 1 && len(f.sentMails()) == 1 })
	if fc.reqs[0].Message != "hello" || f.sentMails()[0].InReplyTo != "n1@x.com" {
		t.Fatalf("turn = %q reply = %+v", fc.reqs[0].Message, f.sentMails()[0])
	}
}

// TestEmailPairingThenTurn: a new allowlisted sender gets one pairing
// prompt threaded under their mail and no model call; once approved,
// their reply runs one turn answered by one mail, no placeholder, with
// Re: subject and References.
func TestEmailPairingThenTurn(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	id := createEmailChannel(t, s, runTag()+"pair", "@x.com")
	f := newFakeMail(0)
	fc := &fakeChat{}
	stop := startEmail(t, s, id, f, fc.chat, MissionDeps{}, nil)
	defer stop()
	waitCursor(t, s, id, "0")

	f.add(connectors.MailMessage{MessageID: "m1@x.com", FromAddress: "Ada@X.com", FromName: "Ada", Subject: "Plans", Text: "hello"})
	sent := f.waitSent(t, 1)
	time.Sleep(100 * time.Millisecond)
	if len(f.sentMails()) != 1 || fc.count() != 0 {
		t.Fatalf("unpaired sender: sent %d, model calls %d", len(f.sentMails()), fc.count())
	}
	prompt := sent[0]
	if prompt.To != "ada@x.com" || prompt.InReplyTo != "m1@x.com" || prompt.Subject != "Re: Plans" || prompt.Body != msgPairingPrompt ||
		strings.Join(prompt.References, ",") != "m1@x.com" {
		t.Fatalf("pairing prompt = %+v", prompt)
	}
	pairings, _ := s.ListPairings(ctx, id)
	if len(pairings) != 1 || pairings[0].ExternalUserID != "ada@x.com" || pairings[0].Status != StatusPending || pairings[0].DisplayName != "Ada" {
		t.Fatalf("pairings = %+v", pairings)
	}

	if err := s.Approve(ctx, id, "ada@x.com"); err != nil {
		t.Fatal(err)
	}
	f.add(connectors.MailMessage{MessageID: "m2@x.com", FromAddress: "ada@x.com", FromName: "Ada", Subject: "Re: Plans",
		InReplyTo: prompt.ID, References: []string{"m1@x.com", prompt.ID}, Text: "what's up\n\nOn Mon, Timothy wrote:\n> " + msgPairingPrompt})
	sent = f.waitSent(t, 2)
	waitFor(t, "the turn", func() bool { return fc.count() == 1 })
	time.Sleep(150 * time.Millisecond)
	sent = f.sentMails()
	if len(sent) != 2 {
		t.Fatalf("sent %d mails, want the prompt and one reply: %+v", len(sent), sent)
	}
	reply := sent[1]
	if reply.Body != "Hello there friend" || reply.Subject != "Re: Plans" || reply.InReplyTo != "m2@x.com" || reply.To != "ada@x.com" ||
		strings.Join(reply.References, ",") != "m1@x.com,"+prompt.ID+",m2@x.com" {
		t.Fatalf("reply = %+v", reply)
	}
	if fc.reqs[0].Message != "what's up" {
		t.Fatalf("turn text = %q, want the quote stripped", fc.reqs[0].Message)
	}
	conv, found, err := s.ConversationFor(ctx, id, "m1@x.com", "")
	if err != nil || !found || conv.SessionID != fc.reqs[0].SessionID || conv.ExternalUserID != "ada@x.com" {
		t.Fatalf("conversation = %+v %v %v", conv, found, err)
	}
	db, _ := pool.Get()
	var raw []byte
	var title string
	if err := db.QueryRow(ctx, `SELECT cc.state, s.title FROM channel_conversations cc JOIN sessions s ON s.id = cc.session_id WHERE cc.id = $1`, conv.ID).Scan(&raw, &title); err != nil {
		t.Fatal(err)
	}
	var st convState
	_ = json.Unmarshal(raw, &st)
	if st.Subject != "Re: Plans" || st.LastMessageID != "m2@x.com" || st.LastOutgoingID != reply.ID || st.ReplyTo != "ada@x.com" || title != "Email: Ada" {
		t.Fatalf("state = %+v title %q", st, title)
	}
}

// TestEmailReplyPressesPermission: a turn parked on the real broker
// mails the prompt with the choices as text; a reply whose first line
// is "allow once" resolves the broker and the turn's reply follows.
func TestEmailReplyPressesPermission(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	id := createEmailChannel(t, s, runTag()+"perm", "ada@x.com")
	approveSender(t, s, id, "ada@x.com")
	broker := pgBroker(pool)
	decisions := make(chan string, 1)
	var mu sync.Mutex
	var turns []string
	chatFn := func(_ context.Context, req chat.Request) (string, <-chan stream.StreamEvent, error) {
		mu.Lock()
		turns = append(turns, req.Message)
		mu.Unlock()
		out := make(chan stream.StreamEvent, 8)
		if req.Message != "list my files" {
			out <- stream.StreamEvent{Type: stream.EventChunk, Text: "noted"}
			out <- stream.StreamEvent{Type: stream.EventDone}
			close(out)
			return req.SessionID, out, nil
		}
		go func() {
			defer close(out)
			pid, answer, err := broker.Create(context.Background(), loop.PendingPermission{SessionID: req.SessionID, Tool: "shell",
				Args: json.RawMessage(`{"command":"ls"}`), Danger: "safe", Rationale: "list files", OriginKind: loop.PermOriginChat})
			if err != nil {
				out <- stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{Message: err.Error()}}
				return
			}
			out <- stream.StreamEvent{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{ID: pid, Tool: "shell", Rationale: "list files"}}
			var d string
			select {
			case d = <-answer:
			case <-time.After(10 * time.Second):
				d = loop.DecideTimeout
			}
			decisions <- d
			out <- stream.StreamEvent{Type: stream.EventPermissionResolved, Resolved: &stream.PermissionResolvedEvent{ID: pid, Decision: d}}
			out <- stream.StreamEvent{Type: stream.EventChunk, Text: "listed"}
			out <- stream.StreamEvent{Type: stream.EventDone}
		}()
		return req.SessionID, out, nil
	}
	f := newFakeMail(0)
	stop := startEmail(t, s, id, f, chatFn, MissionDeps{PendingPermission: broker.Get, ResolvePermission: broker.Resolve}, nil)
	defer stop()
	waitCursor(t, s, id, "0")

	f.add(connectors.MailMessage{MessageID: "p1@x.com", FromAddress: "ada@x.com", Subject: "files", Text: "list my files"})
	ask := f.waitSent(t, 1)[0]
	if ask.Body != "Timothy wants to run shell: list files\n\nReply with one of: Allow once, Allow session, Deny" || ask.InReplyTo != "p1@x.com" {
		t.Fatalf("permission mail = %+v", ask)
	}
	f.add(connectors.MailMessage{MessageID: "p2@x.com", FromAddress: "ada@x.com", Subject: "Re: files", InReplyTo: ask.ID,
		References: []string{"p1@x.com", ask.ID}, Text: "maybe\n\nOn Mon, Timothy wrote:\n> Reply with one of"})
	f.add(connectors.MailMessage{MessageID: "p3@x.com", FromAddress: "ada@x.com", Subject: "Re: files", InReplyTo: ask.ID,
		References: []string{"p1@x.com", ask.ID}, Text: "allow ONCE\n\nOn Mon, Timothy wrote:\n> Reply with one of"})
	select {
	case d := <-decisions:
		if d != loop.DecideOnce {
			t.Fatalf("broker decision = %q, want once", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reply did not resolve the broker")
	}
	waitFor(t, "the turn's replies", func() bool {
		var bodies []string
		for _, m := range f.sentMails() {
			bodies = append(bodies, m.Body)
		}
		return strings.Contains(strings.Join(bodies, "|"), "listed|noted")
	})
	// The non-matching reply is an ordinary message, queued behind the
	// parked turn; the matching one never reaches the model.
	mu.Lock()
	got := strings.Join(turns, ",")
	mu.Unlock()
	if got != "list my files,maybe" {
		t.Fatalf("turns = %q", got)
	}
	conv, _, _ := s.ConversationFor(ctx, id, "p1@x.com", "")
	if _, ok, _ := s.TakeButton(ctx, conv.ID, ask.ID, "Allow once"); ok {
		t.Fatal("buttons still pressable after the answer")
	}
}

// TestEmailAttachmentReachesTheTurn: a stored attachment is saved to the
// attachments store and referenced on the chat request.
func TestEmailAttachmentReachesTheTurn(t *testing.T) {
	s, pool := testStore(t)
	id := createEmailChannel(t, s, runTag()+"att", "ada@x.com")
	approveSender(t, s, id, "ada@x.com")
	store := attachments.New(t.TempDir(), pool)
	f := newFakeMail(0)
	fc := &fakeChat{}
	stop := startEmail(t, s, id, f, fc.chat, MissionDeps{}, store.Save)
	defer stop()
	waitCursor(t, s, id, "0")

	body := []byte("meeting notes " + runTag())
	uid := f.add(connectors.MailMessage{MessageID: "a1@x.com", FromAddress: "ada@x.com", Subject: "notes", Text: "summarize",
		Attachments: []connectors.MailAttachment{{Filename: "notes.txt", ContentType: "text/plain", Size: len(body)}}})
	f.mu.Lock()
	f.files[fmt.Sprintf("%d/notes.txt", uid)] = body
	f.mu.Unlock()
	waitFor(t, "the turn", func() bool { return fc.count() == 1 })
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	req := fc.reqs[0]
	if len(req.Attachments) != 1 || req.Attachments[0].ID != want || req.Attachments[0].Name != "notes.txt" || req.Message != "summarize" {
		t.Fatalf("chat request = %+v", req)
	}
	if a, err := store.Get(t.Context(), want); err != nil || a.Mime != "text/plain" {
		t.Fatalf("stored attachment = %+v %v", a, err)
	}
}

// TestEmailCursorSurvivesRestart: the cursor advances past handled mail
// and a restarted service does not replay it; a repeated Message-ID is
// handled once.
func TestEmailCursorSurvivesRestart(t *testing.T) {
	s, _ := testStore(t)
	id := createEmailChannel(t, s, runTag()+"restart", "ada@x.com")
	approveSender(t, s, id, "ada@x.com")
	f := newFakeMail(5)
	fc := &fakeChat{}
	stop := startEmail(t, s, id, f, fc.chat, MissionDeps{}, nil)
	waitCursor(t, s, id, "5")
	f.add(connectors.MailMessage{MessageID: "r1@x.com", FromAddress: "ada@x.com", Subject: "one", Text: "first"})
	f.add(connectors.MailMessage{MessageID: "r1@x.com", FromAddress: "ada@x.com", Subject: "one", Text: "first"})
	waitCursor(t, s, id, "7")
	waitFor(t, "the turn", func() bool { return fc.count() == 1 && len(f.sentMails()) == 1 })
	stop()

	stop = startEmail(t, s, id, f, fc.chat, MissionDeps{}, nil)
	defer stop()
	calls := f.newerCalls()
	waitFor(t, "polls after the restart", func() bool { return f.newerCalls() >= calls+3 })
	if fc.count() != 1 {
		t.Fatalf("restart replayed mail: %d turns", fc.count())
	}
	f.add(connectors.MailMessage{MessageID: "r2@x.com", FromAddress: "ada@x.com", Subject: "two", Text: "second"})
	waitCursor(t, s, id, "8")
	waitFor(t, "the second turn", func() bool { return fc.count() == 2 })
	if fc.reqs[1].Message != "second" {
		t.Fatalf("second turn = %q", fc.reqs[1].Message)
	}
}

// TestEmailOutboundBudget: the 31st mail of an hour on one channel is
// refused and never reaches SMTP.
func TestEmailOutboundBudget(t *testing.T) {
	s, _ := testStore(t)
	id := createEmailChannel(t, s, runTag()+"budget", "ada@x.com")
	f := newFakeMail(0)
	c, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	a := newEmailAdapter(c, emailEnv{mailbox: f.open, store: s, log: discardLog()})
	a.budget = newHourlyBudget()
	a.remember("b1@x.com", mailThread{To: "ada@x.com", Subject: "loop", MessageID: "b1@x.com", References: []string{"b1@x.com"}})
	for i := range mailPerHour {
		if _, err := a.send(t.Context(), target{ChatID: "b1@x.com"}, "reply", nil, false); err != nil {
			t.Fatalf("mail %d: %v", i+1, err)
		}
	}
	if _, err := a.send(t.Context(), target{ChatID: "b1@x.com"}, "reply", nil, false); !errors.Is(err, errMailBudget) {
		t.Fatalf("mail 31 = %v, want errMailBudget", err)
	}
	if n := len(f.sentMails()); n != mailPerHour {
		t.Fatalf("sent %d mails, want %d", n, mailPerHour)
	}
	if _, err := a.send(t.Context(), target{ChatID: "unknown@x.com"}, "reply", nil, false); err == nil {
		t.Fatal("a mail with no thread was sent")
	}
}
