//go:build integration

package channels

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// missionFixtures is the real missions store and hub over the test
// database; missions it creates are deleted by goal prefix.
func missionFixtures(t *testing.T, pool *pgpool.Pool) (*missions.Store, *missions.Hub) {
	t.Helper()
	ms := missions.NewStore(pool, discardLog())
	hub := missions.NewHub()
	ms.SetHub(hub)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if db, err := pool.Get(); err == nil {
			_, _ = db.Exec(ctx, `DELETE FROM missions WHERE goal LIKE $1 || '%'`, marker)
		}
	})
	return ms, hub
}

// pairedConversation approves sender userID and creates the private
// conversation of chat userID.
func pairedConversation(t *testing.T, s *Store, channelID, userID string) Conversation {
	t.Helper()
	ctx := t.Context()
	if _, _, err := s.EnsurePairing(ctx, channelID, userID, "Ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, channelID, userID); err != nil {
		t.Fatal(err)
	}
	conv, err := s.CreateConversation(ctx, Conversation{ChannelID: channelID, ExternalChatID: userID, ExternalUserID: userID}, "Telegram: Ada")
	if err != nil {
		t.Fatal(err)
	}
	return conv
}

func runService(t *testing.T, svc *Service, f *fakeBot) (stop func()) {
	t.Helper()
	svc.APIBase = f.srv.URL
	svc.editEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(ctx, nil)
	}()
	return func() {
		cancel()
		<-done
	}
}

// callbackUpdate is a button press by userID on message messageID in
// the private chat chatID.
func callbackUpdate(updateID, userID, chatID, messageID int64, text, data string) map[string]any {
	return map[string]any{
		"update_id": updateID,
		"callback_query": map[string]any{
			"id":   "cq-" + time.Now().Format("150405.000000"),
			"from": map[string]any{"id": userID, "is_bot": false, "first_name": "Presser"},
			"message": map[string]any{
				"message_id": messageID,
				"chat":       map[string]any{"id": chatID, "type": "private"},
				"text":       text,
			},
			"data": data,
		},
	}
}

// replyUpdate is a private-chat text replying to the bot's message.
func replyUpdate(updateID, userID, replyTo int64, text string) map[string]any {
	u := privateUpdate(updateID, userID, text)
	u["message"].(map[string]any)["reply_to_message"] = map[string]any{
		"message_id": replyTo,
		"from":       map[string]any{"id": 999, "is_bot": true, "first_name": "Timothy"},
		"chat":       map[string]any{"id": userID, "type": "private"},
	}
	return u
}

// buttonsMessage returns the first sendMessage with a keyboard whose
// text starts with prefix.
func buttonsMessage(f *fakeBot, prefix string) (botCall, []string, bool) {
	for _, c := range f.callsOf("sendMessage") {
		text, _ := c.Body["text"].(string)
		markup, ok := c.Body["reply_markup"].(map[string]any)
		if !ok || !strings.HasPrefix(text, prefix) {
			continue
		}
		var data []string
		for _, row := range markup["inline_keyboard"].([]any) {
			for _, b := range row.([]any) {
				data = append(data, b.(map[string]any)["callback_data"].(string))
			}
		}
		return c, data, true
	}
	return botCall{}, nil, false
}

func answerTexts(f *fakeBot) []string {
	var out []string
	for _, c := range f.callsOf("answerCallbackQuery") {
		out = append(out, c.Body["text"].(string))
	}
	return out
}

func countSends(f *fakeBot, prefix string) int {
	n := 0
	for _, txt := range sendTexts(f) {
		if strings.HasPrefix(txt, prefix) {
			n++
		}
	}
	return n
}

func pgBroker(pool *pgpool.Pool) *loop.PermBroker {
	b := loop.NewPermBroker()
	b.SetStore(loop.NewPGPermStore(pool), nil, nil)
	return b
}

// TestChatTurnPermissionButtons: a chat turn parked on the real broker
// gets a buttons message; an unpaired presser is refused and the
// prompt stays pending; the paired sender's press resolves the broker
// and the buttons message reads the decision.
func TestChatTurnPermissionButtons(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	id := createChannel(t, s, runTag()+"perm")
	if _, _, err := s.EnsurePairing(ctx, id, "77", "Ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, id, "77"); err != nil {
		t.Fatal(err)
	}
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
	f := newFakeBot(t)
	svc := New(s, chatFn, MissionDeps{PendingPermission: broker.Get, ResolvePermission: broker.Resolve}, fakeResolve, f.srv.Client(), discardLog())
	stop := runService(t, svc, f)
	defer stop()

	f.script(privateUpdate(1, 77, "list my files"))
	var msg botCall
	var data []string
	waitFor(t, "the buttons message", func() bool {
		var ok bool
		msg, data, ok = buttonsMessage(f, "Timothy wants to run shell: list files")
		return ok
	})
	if len(data) != 3 || !strings.HasSuffix(data[0], ":once") {
		t.Fatalf("buttons = %v", data)
	}
	pid := strings.Split(data[0], ":")[1]

	f.script(callbackUpdate(2, 88, 77, msg.ResultID, "x", data[0]))
	waitFor(t, "the unpaired answer", func() bool { return len(answerTexts(f)) == 1 })
	if got := answerTexts(f)[0]; got != msgNotPaired {
		t.Fatalf("unpaired answer = %q", got)
	}
	if _, pending, err := broker.Get(ctx, pid); !pending || err != nil {
		t.Fatalf("prompt after the unpaired press: pending=%v err=%v", pending, err)
	}

	f.script(callbackUpdate(3, 77, 77, msg.ResultID, "x", data[0]))
	select {
	case d := <-decisions:
		if d != loop.DecideOnce {
			t.Fatalf("broker decision = %q, want once", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the paired press did not resolve the broker")
	}
	waitFor(t, "the buttons message edit", func() bool {
		for _, c := range f.callsOf("editMessageText") {
			if c.Body["message_id"].(float64) == float64(msg.ResultID) && c.Body["text"] == "Allowed once" && c.Body["reply_markup"] != nil {
				return true
			}
		}
		return false
	})
	if got := answerTexts(f); len(got) != 2 || got[1] != "Allowed once" {
		t.Fatalf("answers = %q", got)
	}
	if _, pending, _ := broker.Get(ctx, pid); pending {
		t.Fatal("prompt still pending after the press")
	}
}

// askRecorder records AnswerAskUser calls.
type askRecorder struct {
	mu    sync.Mutex
	calls [][2]string
}

func (a *askRecorder) answer(_ context.Context, id, answer string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, [2]string{id, answer})
	return nil
}

func (a *askRecorder) got() [][2]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][2]string(nil), a.calls...)
}

func missionDeps(ms *missions.Store, hub *missions.Hub, asks *askRecorder) MissionDeps {
	return MissionDeps{
		Get: ms.Get, ListParked: ms.ListParkedForChannels, Subscribe: hub.Subscribe, AnswerAskUser: asks.answer,
		Signal:     func(context.Context, string, missions.Input) error { return nil },
		DecidePlan: func(context.Context, string, missions.Input, string) error { return nil },
	}
}

// TestMissionAskWithOptionsPushAndPress: a parked channel mission gets
// one push with option buttons; pressing option 1 answers with that
// option; later reconcile passes do not push again.
func TestMissionAskWithOptionsPushAndPress(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	ms, hub := missionFixtures(t, pool)
	id := createChannel(t, s, runTag()+"ask")
	conv := pairedConversation(t, s, id, "77")
	mid, err := ms.Create(ctx, missions.Mission{Goal: marker + "ask options", Name: "Ask test", Kind: "general", Route: "default", ChannelConversationID: conv.ID})
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeBot(t)
	fc := &fakeChat{}
	asks := &askRecorder{}
	svc := New(s, fc.chat, missionDeps(ms, hub, asks), fakeResolve, f.srv.Client(), discardLog())
	svc.parkEvery = 100 * time.Millisecond
	stop := runService(t, svc, f)
	defer stop()

	if err := ms.SetPendingInput(ctx, mid, missions.PendingInput{Question: "Pick one", Kind: "mcq", Options: []string{"red", "blue"}, ProposedDefault: "red"}); err != nil {
		t.Fatal(err)
	}
	var msg botCall
	var data []string
	waitFor(t, "the ask push", func() bool {
		var ok bool
		msg, data, ok = buttonsMessage(f, "Mission Ask test asks: Pick one")
		return ok
	})
	if msg.Body["text"] != "Mission Ask test asks: Pick one\nProposed: red" || len(data) != 2 || data[1] != askCallback(mid, 1) || msg.Body["chat_id"].(float64) != 77 {
		t.Fatalf("push = %v buttons %v", msg.Body, data)
	}

	f.script(callbackUpdate(1, 77, 77, msg.ResultID, msg.Body["text"].(string), data[1]))
	waitFor(t, "the answer", func() bool { return len(asks.got()) == 1 })
	if got := asks.got()[0]; got != [2]string{mid, "blue"} {
		t.Fatalf("AnswerAskUser = %v, want blue", got)
	}
	waitFor(t, "the buttons to close", func() bool {
		for _, c := range f.callsOf("editMessageText") {
			if c.Body["message_id"].(float64) == float64(msg.ResultID) && strings.HasSuffix(c.Body["text"].(string), "Answered: blue") {
				return true
			}
		}
		return false
	})
	time.Sleep(500 * time.Millisecond)
	if n := countSends(f, "Mission Ask test asks"); n != 1 {
		t.Fatalf("ask pushed %d times, want once across reconcile passes", n)
	}
	if fc.count() != 0 {
		t.Fatalf("a button press reached the chat %d times", fc.count())
	}

	// A mission of another conversation is refused.
	other, err := ms.Create(ctx, missions.Mission{Goal: marker + "ask other", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatal(err)
	}
	f.script(callbackUpdate(2, 77, 77, msg.ResultID, "x", missionCallback(other, actResume)))
	waitFor(t, "the refusal", func() bool {
		a := answerTexts(f)
		return len(a) == 2 && a[1] == msgNotYours
	})
}

// TestMissionAskReplyTo: an ask without options says reply; the reply
// answers the mission once and never reaches the chat turn.
func TestMissionAskReplyTo(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	ms, hub := missionFixtures(t, pool)
	id := createChannel(t, s, runTag()+"reply")
	conv := pairedConversation(t, s, id, "77")
	mid, err := ms.Create(ctx, missions.Mission{Goal: marker + "ask reply", Name: "Reply test", Kind: "general", Route: "default", ChannelConversationID: conv.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.SetPendingInput(ctx, mid, missions.PendingInput{Question: "What name?", Kind: "open"}); err != nil {
		t.Fatal(err)
	}
	f := newFakeBot(t)
	fc := &fakeChat{}
	asks := &askRecorder{}
	svc := New(s, fc.chat, missionDeps(ms, hub, asks), fakeResolve, f.srv.Client(), discardLog())
	stop := runService(t, svc, f)
	defer stop()

	var pushID int64
	waitFor(t, "the ask push from the first reconcile", func() bool {
		for _, c := range f.callsOf("sendMessage") {
			if c.Body["text"] == "Mission Reply test asks: What name?\n\n"+msgReplyHint {
				pushID = c.ResultID
				return true
			}
		}
		return false
	})
	f.script(replyUpdate(1, 77, pushID, "call it Nova"))
	waitFor(t, "the reply answer", func() bool { return len(asks.got()) == 1 })
	if got := asks.got()[0]; got != [2]string{mid, "call it Nova"} {
		t.Fatalf("AnswerAskUser = %v", got)
	}
	waitFor(t, "the confirmation", func() bool { return countSends(f, msgSentToMission) == 1 })
	if fc.count() != 0 {
		t.Fatalf("the reply reached the chat %d times", fc.count())
	}
	// The ask is taken once: a second reply is an ordinary message.
	f.script(replyUpdate(2, 77, pushID, "and another thing"))
	waitFor(t, "the second reply to reach the chat", func() bool { return fc.count() == 1 })
	if len(asks.got()) != 1 {
		t.Fatalf("the ask answered twice: %v", asks.got())
	}
}

// TestOutcomesConsumer: done and failed events report to the
// conversation (failures too); 4xx is dropped, 5xx retried; missions
// without a conversation and disabled channels send nothing.
func TestOutcomesConsumer(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	ms, _ := missionFixtures(t, pool)
	id := createChannel(t, s, runTag()+"outcome")
	conv := pairedConversation(t, s, id, "77")
	mid, err := ms.Create(ctx, missions.Mission{Goal: marker + "outcome", Name: "Outcome test", Kind: "general", Route: "default", ChannelConversationID: conv.ID})
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := ms.Create(ctx, missions.Mission{Goal: marker + "outcome plain", Kind: "general", Route: "default"})
	f := newFakeBot(t)
	o := NewOutcomes(s, MissionDeps{Get: ms.Get, Events: ms.Events, WebBaseURL: func(context.Context) string { return "https://timothy.test" }},
		fakeResolve, f.srv.Client(), nil, discardLog())
	o.APIBase = f.srv.URL
	event := func(missionID, phase, reason string) events.Event {
		ev, err := events.MissionTerminal(events.MissionPayload{MissionID: missionID, Phase: phase, Reason: reason})
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}

	if err := o.Handle(ctx, nil, event(mid, "done", "")); err != nil {
		t.Fatalf("done: %v", err)
	}
	sends := sendTexts(f)
	if len(sends) != 1 || !strings.HasPrefix(sends[0], "Mission Outcome test is done\n\n") ||
		!strings.Contains(sends[0], "mission title: Outcome test") || !strings.HasSuffix(sends[0], "https://timothy.test/missions/"+mid) {
		t.Fatalf("done message = %q", sends)
	}
	if err := o.Handle(ctx, nil, event(mid, "failed", "max_iterations")); err != nil {
		t.Fatalf("failed: %v", err)
	}
	if sends = sendTexts(f); len(sends) != 2 || !strings.HasPrefix(sends[1], "Mission Outcome test failed: max_iterations") {
		t.Fatalf("failed message = %q", sends)
	}
	if err := o.Handle(ctx, nil, event(plain, "done", "")); err != nil || len(sendTexts(f)) != 2 {
		t.Fatalf("mission without a conversation: err=%v sends=%d", err, len(sendTexts(f)))
	}

	f.mu.Lock()
	f.sendStatus = 403
	f.mu.Unlock()
	if err := o.Handle(ctx, nil, event(mid, "done", "")); err != nil {
		t.Fatalf("a 403 must not retry: %v", err)
	}
	f.mu.Lock()
	f.sendStatus = 500
	f.mu.Unlock()
	if err := o.Handle(ctx, nil, event(mid, "done", "")); err == nil {
		t.Fatal("a 500 must return an error so the drainer retries")
	}
	f.mu.Lock()
	f.sendStatus = 0
	f.mu.Unlock()
	off := false
	if err := s.Patch(ctx, id, Patch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	before := len(f.callsOf("sendMessage"))
	if err := o.Handle(ctx, nil, event(mid, "done", "")); err != nil || len(f.callsOf("sendMessage")) != before {
		t.Fatalf("disabled channel: err=%v sends %d -> %d", err, before, len(f.callsOf("sendMessage")))
	}
}

// TestConversationAsksAndSessionLookup covers the store side of
// reply-to asks and the create_mission origin lookup.
func TestConversationAsksAndSessionLookup(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	id := createChannel(t, s, runTag()+"asks")
	conv := pairedConversation(t, s, id, "77")
	for i := int64(1); i <= maxAsks+2; i++ {
		if err := s.RememberAsk(ctx, conv.ID, strconv.FormatInt(i, 10), "m", AskUser); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, ok, _ := s.TakeAsk(ctx, conv.ID, "1"); ok {
		t.Fatal("the oldest ask survived the cap")
	}
	mid, kind, ok, err := s.TakeAsk(ctx, conv.ID, "5")
	if err != nil || !ok || mid != "m" || kind != AskUser {
		t.Fatalf("take = %q %q %v %v", mid, kind, ok, err)
	}
	if _, _, ok, _ := s.TakeAsk(ctx, conv.ID, "5"); ok {
		t.Fatal("an ask was taken twice")
	}
	got, err := s.ConversationByID(ctx, conv.ID)
	if err != nil || got.SessionID != conv.SessionID {
		t.Fatalf("ConversationByID = %+v %v", got, err)
	}
	if cid, err := s.ConversationIDForSession(ctx, conv.SessionID); err != nil || cid != conv.ID {
		t.Fatalf("ConversationIDForSession = %q %v", cid, err)
	}
	if cid, err := s.ConversationIDForSession(ctx, "00000000-0000-0000-0000-000000000000"); err != nil || cid != "" {
		t.Fatalf("unknown session = %q %v", cid, err)
	}
}
