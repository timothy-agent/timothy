//go:build integration

package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func createSlackChannel(t *testing.T, s *Store, name string) string {
	t.Helper()
	id, err := s.Create(t.Context(), Channel{Name: name, Kind: KindSlack, CredentialRef: "SLACK_BOT", Config: Config{AppTokenRef: "SLACK_APP"}, Enabled: true})
	if err != nil {
		t.Fatalf("create slack channel: %v", err)
	}
	return id
}

func approveSender(t *testing.T, s *Store, channelID, userID string) {
	t.Helper()
	if _, _, err := s.EnsurePairing(t.Context(), channelID, userID, "Ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(t.Context(), channelID, userID); err != nil {
		t.Fatal(err)
	}
}

// startSlack runs the service against the fake and waits for the
// first socket.
func startSlack(t *testing.T, s *Store, f *fakeSlack, chatFn ChatFunc, deps MissionDeps) (stop func()) {
	t.Helper()
	svc := New(s, chatFn, deps, slackResolve, f.srv.Client(), discardLog())
	svc.SlackAPIBase = f.srv.URL + "/api"
	svc.editEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(ctx, nil)
	}()
	f.waitConns(t, 1)
	return func() {
		cancel()
		<-done
	}
}

// waitConns waits for n accepted sockets.
func (f *fakeSlack) waitConns(t *testing.T, n int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("socket connection %d", n), func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.conns >= n
	})
}

// push writes envelopes to the newest socket.
func (f *fakeSlack) push(t *testing.T, envelopes ...map[string]any) {
	t.Helper()
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		t.Fatal("no socket connected")
	}
	for _, env := range envelopes {
		data, _ := json.Marshal(env)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := conn.Write(ctx, websocket.MessageText, data)
		cancel()
		if err != nil {
			t.Fatalf("push: %v", err)
		}
	}
}

func (f *fakeSlack) acked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.acks...)
}

// waitAcked waits until every envelope id was acked.
func (f *fakeSlack) waitAcked(t *testing.T, ids ...string) {
	t.Helper()
	waitFor(t, "acks for "+strings.Join(ids, ","), func() bool {
		got := map[string]bool{}
		for _, a := range f.acked() {
			got[a] = true
		}
		for _, id := range ids {
			if !got[id] {
				return false
			}
		}
		return true
	})
}

func slackTexts(calls []slackCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.Body["text"].(string))
	}
	return out
}

func TestSlackStoreConfig(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	tag := runTag()
	id := createSlackChannel(t, s, tag+"slack")
	c, err := s.Get(ctx, id)
	if err != nil || c.Kind != KindSlack || c.Config.AppTokenRef != "SLACK_APP" || c.CredentialRef != "SLACK_BOT" {
		t.Fatalf("slack channel = %+v %v", c, err)
	}
	ref, dispatch := "OTHER_APP", true
	if err := s.Patch(ctx, id, Patch{AppTokenRef: &ref, Dispatch: &dispatch}); err != nil {
		t.Fatalf("patch app token: %v", err)
	}
	if c, _ = s.Get(ctx, id); c.Config.AppTokenRef != "OTHER_APP" || !c.Config.Dispatch {
		t.Fatalf("patched = %+v", c.Config)
	}
	bad := "xapp-1 pasted"
	if err := s.Patch(ctx, id, Patch{AppTokenRef: &bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pasted app token = %v", err)
	}
	tg := createChannel(t, s, tag+"tg")
	if err := s.Patch(ctx, tg, Patch{AppTokenRef: &ref}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("app token on telegram = %v", err)
	}

	f := newFakeSlack(t)
	svc := New(s, nil, MissionDeps{}, slackResolve, f.srv.Client(), discardLog())
	svc.SlackAPIBase = f.srv.URL + "/api"
	app := "SLACK_APP"
	if err := s.Patch(ctx, id, Patch{AppTokenRef: &app}); err != nil {
		t.Fatal(err)
	}
	name, err := svc.Test(ctx, id)
	if err != nil || name != "timothy" || len(f.callsOf("auth.test")) != 1 || f.openCount() != 1 || f.conns != 0 {
		t.Fatalf("Test = %q %v opens %d conns %d", name, err, f.openCount(), f.conns)
	}
	wrongApp := "SLACK_BOT"
	if err := s.Patch(ctx, id, Patch{AppTokenRef: &wrongApp}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Test(ctx, id); err == nil || !strings.Contains(err.Error(), "slack app token") || strings.Contains(err.Error(), fakeBotToken) {
		t.Fatalf("Test with wrong app token = %v", err)
	}
	if c, _ = s.Get(ctx, id); c.Config.BotUsername != "timothy" {
		t.Fatalf("bot username = %q", c.Config.BotUsername)
	}
}

// TestSlackUnpairedDMThenTurn: an unpaired DM gets one prompt and no
// model call, every envelope is acked; after approval a DM streams a
// reply through chat.postMessage then chat.update edits.
func TestSlackUnpairedDMThenTurn(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	id := createSlackChannel(t, s, runTag()+"dm")
	f := newFakeSlack(t)
	fc := &fakeChat{}
	stop := startSlack(t, s, f, fc.chat, MissionDeps{})
	defer stop()

	f.push(t, eventEnvelope("e1", dmEvent("U77", "hello", "10.1")), eventEnvelope("e2", dmEvent("U77", "anyone?", "10.2")))
	f.waitAcked(t, "e1", "e2")
	waitFor(t, "the pairing prompt", func() bool { return len(f.callsOf("chat.postMessage")) == 1 })
	time.Sleep(200 * time.Millisecond)
	posts := f.callsOf("chat.postMessage")
	if len(posts) != 1 || posts[0].Body["text"] != slackEscape(msgPairingPrompt) || posts[0].Body["channel"] != "DU77" || posts[0].Body["thread_ts"] != nil {
		t.Fatalf("unpaired replies = %v", slackTexts(posts))
	}
	if fc.count() != 0 {
		t.Fatalf("unpaired sender reached the model %d times", fc.count())
	}
	pairings, _ := s.ListPairings(ctx, id)
	if len(pairings) != 1 || pairings[0].ExternalUserID != "U77" || pairings[0].DisplayName != "Ada" || !looksLikeCode(pairings[0].Code) {
		t.Fatalf("pairing = %+v", pairings)
	}

	if err := s.Approve(ctx, id, "U77"); err != nil {
		t.Fatal(err)
	}
	f.push(t, eventEnvelope("e3", dmEvent("U77", "what's up", "10.3")))
	waitFor(t, "the final edit", func() bool {
		upd := slackTexts(f.callsOf("chat.update"))
		return len(upd) > 0 && upd[len(upd)-1] == "Hello there friend"
	})
	f.waitAcked(t, "e3")
	posts = f.callsOf("chat.postMessage")
	if len(posts) != 2 || posts[1].Body["text"] != msgThinking {
		t.Fatalf("posts = %v", slackTexts(posts))
	}
	for _, u := range f.callsOf("chat.update") {
		if u.Body["ts"] != posts[1].TS || u.Body["channel"] != "DU77" {
			t.Fatalf("edit went elsewhere: %v", u.Body)
		}
	}
	if fc.count() != 1 || fc.reqs[0].Message != "what's up" {
		t.Fatalf("chat requests = %+v", fc.reqs)
	}
	db, _ := pool.Get()
	var title, chatID, threadID string
	if err := db.QueryRow(ctx, `SELECT s.title, cc.external_chat_id, cc.external_thread_id FROM sessions s
		JOIN channel_conversations cc ON cc.session_id = s.id WHERE s.id = $1`, fc.reqs[0].SessionID).Scan(&title, &chatID, &threadID); err != nil {
		t.Fatal(err)
	}
	if title != "Slack: Ada" || chatID != "DU77" || threadID != "" {
		t.Fatalf("conversation = %q %q %q", title, chatID, threadID)
	}
}

// TestSlackChannelMentionThread: a mention (delivered as app_mention
// and message) makes one thread conversation with threaded replies; a
// later thread reply without a mention continues it; an unaddressed
// top-level message is ignored.
func TestSlackChannelMentionThread(t *testing.T) {
	s, _ := testStore(t)
	id := createSlackChannel(t, s, runTag()+"thread")
	approveSender(t, s, id, "U77")
	f := newFakeSlack(t)
	fc := &fakeChat{}
	stop := startSlack(t, s, f, fc.chat, MissionDeps{})
	defer stop()

	f.push(t,
		eventEnvelope("m1", channelEvent("app_mention", "U77", "<@UBOT> summarize", "20.1", "")),
		eventEnvelope("m2", channelEvent("message", "U77", "<@UBOT> summarize", "20.1", "")),
		eventEnvelope("m3", channelEvent("message", "U77", "lunch anyone?", "20.2", "")),
	)
	f.waitAcked(t, "m1", "m2", "m3")
	waitFor(t, "the first turn to finish", func() bool {
		upd := slackTexts(f.callsOf("chat.update"))
		return fc.count() == 1 && len(upd) > 0 && upd[len(upd)-1] == "Hello there friend"
	})
	if fc.reqs[0].Message != "summarize" {
		t.Fatalf("mention text = %q, want the mention stripped", fc.reqs[0].Message)
	}
	for _, p := range f.callsOf("chat.postMessage") {
		if p.Body["channel"] != "C1" || p.Body["thread_ts"] != "20.1" {
			t.Fatalf("reply not in the thread: %v", p.Body)
		}
	}
	conv, found, err := s.ConversationFor(t.Context(), id, "C1", "20.1")
	if err != nil || !found || conv.SessionID != fc.reqs[0].SessionID {
		t.Fatalf("thread conversation = %+v %v %v", conv, found, err)
	}

	f.push(t, eventEnvelope("m4", channelEvent("message", "U77", "and the rest?", "20.3", "20.1")),
		eventEnvelope("m5", channelEvent("message", "U77", "side chat", "30.2", "30.1")))
	waitFor(t, "the thread reply turn", func() bool { return fc.count() == 2 })
	f.waitAcked(t, "m4", "m5")
	time.Sleep(200 * time.Millisecond)
	if fc.count() != 2 || fc.reqs[1].Message != "and the rest?" || fc.reqs[1].SessionID != conv.SessionID {
		t.Fatalf("chat requests = %+v", fc.reqs)
	}
}

// TestSlackPressResolvesPermission: a chat turn parked on the real
// broker posts a buttons message in the thread; an unpaired press is
// refused; the paired press resolves the prompt and closes the
// buttons.
func TestSlackPressResolvesPermission(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	id := createSlackChannel(t, s, runTag()+"press")
	approveSender(t, s, id, "U77")
	broker := pgBroker(pool)
	decisions := make(chan string, 1)
	chatFn := func(_ context.Context, req chat.Request) (string, <-chan stream.StreamEvent, error) {
		out := make(chan stream.StreamEvent, 8)
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
	f := newFakeSlack(t)
	stop := startSlack(t, s, f, chatFn, MissionDeps{PendingPermission: broker.Get, ResolvePermission: broker.Resolve})
	defer stop()

	f.push(t, eventEnvelope("p1", channelEvent("app_mention", "U77", "<@UBOT> list my files", "40.1", "")))
	var msg slackCall
	var data []string
	waitFor(t, "the buttons message", func() bool {
		for _, c := range f.callsOf("chat.postMessage") {
			blocks, ok := c.Body["blocks"].([]any)
			if !ok || c.Body["text"] != "Timothy wants to run shell: list files" {
				continue
			}
			msg, data = c, nil
			for _, e := range blocks[1].(map[string]any)["elements"].([]any) {
				data = append(data, e.(map[string]any)["value"].(string))
			}
			return true
		}
		return false
	})
	if msg.Body["thread_ts"] != "40.1" || len(data) != 3 || !strings.HasSuffix(data[0], ":once") {
		t.Fatalf("buttons message = %v %v", msg.Body, data)
	}
	pid := strings.Split(data[0], ":")[1]

	f.push(t, pressEnvelope("p2", "U88", "C1", msg.TS, "40.1", data[0], "Timothy wants to run shell: list files"))
	f.waitAcked(t, "p2")
	time.Sleep(200 * time.Millisecond)
	if _, pending, err := broker.Get(ctx, pid); !pending || err != nil {
		t.Fatalf("prompt after the unpaired press: pending=%v err=%v", pending, err)
	}

	f.push(t, pressEnvelope("p3", "U77", "C1", msg.TS, "40.1", data[0], "Timothy wants to run shell: list files"))
	select {
	case d := <-decisions:
		if d != loop.DecideOnce {
			t.Fatalf("broker decision = %q, want once", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the paired press did not resolve the broker")
	}
	waitFor(t, "the buttons to close", func() bool {
		for _, c := range f.callsOf("chat.update") {
			if c.Body["ts"] == msg.TS && c.Body["text"] == "Allowed once" && len(c.Body["blocks"].([]any)) == 0 {
				return true
			}
		}
		return false
	})
	if _, pending, _ := broker.Get(ctx, pid); pending {
		t.Fatal("prompt still pending after the press")
	}
}

// TestSlackDisconnectRedialsAndDedups: a disconnect envelope leads to
// a fresh apps.connections.open and socket; a redelivered event makes
// one turn.
func TestSlackDisconnectRedialsAndDedups(t *testing.T) {
	s, _ := testStore(t)
	id := createSlackChannel(t, s, runTag()+"redial")
	approveSender(t, s, id, "U77")
	f := newFakeSlack(t)
	fc := &fakeChat{}
	stop := startSlack(t, s, f, fc.chat, MissionDeps{})
	defer stop()
	if f.openCount() != 1 {
		t.Fatalf("opens = %d", f.openCount())
	}

	f.push(t, map[string]any{"envelope_id": "d1", "type": "disconnect", "reason": "refresh_requested"})
	f.waitConns(t, 2)
	f.waitAcked(t, "d1")
	if f.openCount() != 2 {
		t.Fatalf("opens after disconnect = %d, want 2", f.openCount())
	}

	event := dmEvent("U77", "once please", "50.1")
	f.push(t, eventEnvelope("r1", event), eventEnvelope("r1", event), eventEnvelope("r2", event))
	f.waitAcked(t, "r1", "r2")
	waitFor(t, "the turn", func() bool { return fc.count() == 1 })
	time.Sleep(300 * time.Millisecond)
	if fc.count() != 1 {
		t.Fatalf("redelivered event made %d turns, want 1", fc.count())
	}
}
