package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeLog is an in-memory SessionLog.
type fakeLog struct {
	mu       sync.Mutex
	events   map[string][]session.Event
	titles   map[string]string
	category map[string]string
	createdN int
}

func newFakeLog() *fakeLog {
	return &fakeLog{events: map[string][]session.Event{}, titles: map[string]string{}, category: map[string]string{}}
}

func (f *fakeLog) Create(_ context.Context, title string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdN++
	id := "sess-created"
	f.events[id] = []session.Event{{SessionID: id, Seq: 1, Kind: session.KindSessionStarted, Payload: []byte(`{}`)}}
	return id, nil
}

func (f *fakeLog) Events(_ context.Context, id string) ([]session.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.events[id]; !ok {
		f.events[id] = []session.Event{{SessionID: id, Seq: 1, Kind: session.KindSessionStarted, Payload: []byte(`{}`)}}
	}
	return append([]session.Event(nil), f.events[id]...), nil
}

func (f *fakeLog) Append(_ context.Context, id, kind string, payload any) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	seq := int64(len(f.events[id]) + 1)
	f.events[id] = append(f.events[id], session.Event{SessionID: id, Seq: seq, Kind: kind, Payload: data})
	return seq, nil
}

func (f *fakeLog) SetTitleIfEmpty(_ context.Context, id, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.titles[id] == "" {
		f.titles[id] = title
	}
	return nil
}

func (f *fakeLog) SetLastCategory(_ context.Context, id, category string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.category[id] = category
	return nil
}

func (f *fakeLog) lastCategory(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.category[id]
}

func (f *fakeLog) kinds(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, ev := range f.events[id] {
		out = append(out, ev.Kind)
	}
	return out
}

// fakeGW streams canned events and records requests. blockCh, when
// set, delays the stream until closed (for cancellation tests).
type fakeGW struct {
	mu       sync.Mutex
	requests []gwclient.StreamRequest
	events   []stream.StreamEvent
	blockCh  chan struct{}
}

func (g *fakeGW) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	g.mu.Lock()
	g.requests = append(g.requests, req)
	block := g.blockCh
	events := append([]stream.StreamEvent(nil), g.events...)
	g.mu.Unlock()

	ch := make(chan stream.StreamEvent)
	go func() {
		defer close(ch)
		if block != nil {
			<-block
		}
		for _, ev := range events {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (g *fakeGW) lastRequest() gwclient.StreamRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.requests[len(g.requests)-1]
}

func okEvents(text string) []stream.StreamEvent {
	return []stream.StreamEvent{
		{Type: stream.EventChunk, Text: text},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod", LedgerID: "led"}},
	}
}

func newService(gw Gateway, log SessionLog) *Service {
	return New(gw, log, nil, nil, 60_000, "", discard())
}

func drain(t *testing.T, ch <-chan stream.StreamEvent) []stream.StreamEvent {
	t.Helper()
	var out []stream.StreamEvent
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatal("stream did not close")
		}
	}
}

// waitFor polls until cond or timeout — persistence runs after the
// client channel closes.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestChatPersistsTurnAsEvents(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("the answer")}
	s := newService(gw, log)

	id, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "the question", TaskCategory: "mini"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if id != "s1" {
		t.Fatalf("session id = %q", id)
	}
	drain(t, ch)

	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })
	kinds := log.kinds("s1")
	want := []string{session.KindSessionStarted, session.KindUserMessage, session.KindAssistantTurn}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	waitFor(t, func() bool { return log.lastCategory("s1") == "mini" })

	events, _ := log.Events(t.Context(), "s1")
	var turn session.AssistantTurn
	if err := json.Unmarshal(events[2].Payload, &turn); err != nil {
		t.Fatalf("decode turn: %v", err)
	}
	if turn.LLM.Message != "the answer" || turn.Provider != "prov" || turn.LedgerID != "led" {
		t.Fatalf("turn = %+v", turn)
	}
}

func TestChatHistoryComesFromProjection(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("first answer")}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "first question"})
	if err != nil {
		t.Fatalf("Chat 1: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })

	gw.mu.Lock()
	gw.events = okEvents("second answer")
	gw.mu.Unlock()
	_, ch, err = s.Chat(t.Context(), Request{SessionID: "s1", Message: "second question"})
	if err != nil {
		t.Fatalf("Chat 2: %v", err)
	}
	drain(t, ch)

	msgs := gw.lastRequest().Messages
	if len(msgs) != 3 {
		t.Fatalf("second request messages = %d, want 3: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "first question" || msgs[1].Content != "first answer" || msgs[2].Content != "second question" {
		t.Fatalf("projected history wrong: %+v", msgs)
	}
}

func TestChatWritesPendingStateOnCancel(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	block := make(chan struct{})
	gw := &fakeGW{blockCh: block, events: []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "partial answer that never finishes"},
		// no terminal: the connection "dies" after the chunk
	}}
	s := newService(gw, log)

	ctx, cancel := context.WithCancel(t.Context())
	_, ch, err := s.Chat(ctx, Request{SessionID: "s1", Message: "long question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	close(block) // let the chunk flow
	<-ch         // receive the chunk
	cancel()     // client disconnects mid-stream
	drain(t, ch)

	waitFor(t, func() bool {
		for _, k := range log.kinds("s1") {
			if k == session.KindPendingState {
				return true
			}
		}
		return false
	})

	// The next turn's projection must splice the partial in.
	events, _ := log.Events(t.Context(), "s1")
	msgs, err := session.LLMContext(events, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" || !strings.Contains(last.Content, "partial answer") {
		t.Fatalf("pending state not spliced: %+v", last)
	}
}

func TestChatAutoTitlesFirstExchange(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("hello there")}
	s := newService(gw, log)

	// Titles come from a second gateway call; the fake returns the
	// same canned events, whose chunk becomes the title.
	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	waitFor(t, func() bool {
		log.mu.Lock()
		defer log.mu.Unlock()
		return log.titles["s1"] != ""
	})
	log.mu.Lock()
	title := log.titles["s1"]
	log.mu.Unlock()
	if title != "hello there" {
		t.Fatalf("title = %q", title)
	}

	// Second exchange must NOT retitle.
	gw.mu.Lock()
	nCalls := len(gw.requests)
	gw.mu.Unlock()
	_, ch, _ = s.Chat(t.Context(), Request{SessionID: "s1", Message: "again"})
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) >= 5 })
	gw.mu.Lock()
	calls := len(gw.requests) - nCalls
	gw.mu.Unlock()
	if calls != 1 { // chat only, no title call
		t.Fatalf("second exchange made %d gateway calls, want 1", calls)
	}
}

func TestChatValidatesMessage(t *testing.T) {
	t.Parallel()
	s := newService(&fakeGW{}, newFakeLog())
	if _, _, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "   "}); err == nil {
		t.Fatal("blank message accepted")
	}
}

// orderCompactor records whether compaction ran before the provider
// call: the pre-send guarantee.
type orderCompactor struct {
	mu    sync.Mutex
	calls []string
}

func (c *orderCompactor) MaybeCompact(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "compact")
	return nil
}

func (c *orderCompactor) note(step string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, step)
}

// gwAfterCompact wraps fakeGW to record when the provider is hit.
type gwAfterCompact struct {
	*fakeGW
	order *orderCompactor
}

func (g *gwAfterCompact) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	g.order.note("stream")
	return g.fakeGW.Stream(ctx, req)
}

func TestChatCompactsBeforeSend(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	order := &orderCompactor{}
	gw := &gwAfterCompact{fakeGW: &fakeGW{events: okEvents("hi")}, order: order}
	svc := New(gw, log, nil, order, 60_000, "", discard())

	_, ch, err := svc.Chat(t.Context(), Request{Message: "hello"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	order.mu.Lock()
	defer order.mu.Unlock()
	if len(order.calls) < 2 || order.calls[0] != "compact" || order.calls[1] != "stream" {
		t.Fatalf("call order = %v, want compaction before the provider call", order.calls)
	}
}

// compactingLog is a Compactor that actually rewrites the log the way
// the real one does: it appends a compaction_applied event replacing
// everything before the newest user message.
type compactingLog struct {
	log *fakeLog
}

func (c *compactingLog) MaybeCompact(ctx context.Context, sessionID string) error {
	events, _ := c.log.Events(ctx, sessionID)
	// Replace everything before the just-appended user message.
	boundary := events[len(events)-1].Seq - 1
	_, err := c.log.Append(ctx, sessionID, session.KindCompactionApplied, session.CompactionApplied{
		Summary: "everything before was condensed", ReplacesThroughSeq: boundary,
	})
	return err
}

// TestChatSendsCompactedContext pins what actually goes on the wire
// when pre-send compaction fires: the provider sees the summary head
// plus the new user message, never the summarized-away content.
func TestChatSendsCompactedContext(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	ctx := context.Background()
	if _, err := log.Append(ctx, "s1", session.KindUserMessage, session.UserMessage{Text: "old question"}); err != nil {
		t.Fatal(err)
	}
	var turn session.AssistantTurn
	turn.LLM.Message = "old answer"
	if _, err := log.Append(ctx, "s1", session.KindAssistantTurn, turn); err != nil {
		t.Fatal(err)
	}

	gw := &fakeGW{events: okEvents("fresh reply")}
	svc := New(gw, log, nil, &compactingLog{log: log}, 60_000, "", discard())

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "new question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	msgs := gw.lastRequest().Messages
	if len(msgs) != 2 {
		t.Fatalf("wire messages = %+v, want [summary, new question]", msgs)
	}
	if !strings.Contains(msgs[0].Content, "everything before was condensed") {
		t.Fatalf("summary head missing from wire context: %+v", msgs[0])
	}
	if strings.Contains(fmt.Sprint(msgs), "old question") || strings.Contains(fmt.Sprint(msgs), "old answer") {
		t.Fatalf("summarized-away content leaked onto the wire: %+v", msgs)
	}
	if msgs[1].Content != "new question" {
		t.Fatalf("new user message not last on the wire: %+v", msgs[1])
	}
}

// TestFollowUpSeesCompletedTurnWhileDistillRuns pins turn durability:
// the assistant turn must be in the log the moment the client stream
// ends, even while distillation is still running — a fast follow-up
// projects the completed answer, never a phantom interruption.
func TestFollowUpSeesCompletedTurnWhileDistillRuns(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("the full answer")}
	distillStarted := make(chan struct{})
	distillRelease := make(chan struct{})
	var startedOnce sync.Once
	distill := func(ctx context.Context, sessionID, turnText string) *session.TurnMemory {
		startedOnce.Do(func() { close(distillStarted) })
		select {
		case <-distillRelease:
		case <-ctx.Done():
		}
		return &session.TurnMemory{KeyFindings: []string{"late residue"}}
	}
	defer close(distillRelease)
	svc := New(gw, log, distill, nil, 60_000, "", discard())

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "first question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	// Distill is now blocked mid-flight; the turn must already be durable.
	select {
	case <-distillStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("distill never started")
	}
	deadline := time.After(5 * time.Second)
	for {
		if k := log.kinds("s1"); len(k) > 0 && k[len(k)-1] == session.KindAssistantTurn {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("assistant_turn not durable while distill runs: %v", log.kinds("s1"))
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Follow-up inside the distill window: full answer on the wire, no
	// phantom interruption.
	_, ch2, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "second question"})
	if err != nil {
		t.Fatalf("Chat 2: %v", err)
	}
	drain(t, ch2)

	msgs := fmt.Sprint(gw.lastRequest().Messages)
	if !strings.Contains(msgs, "the full answer") {
		t.Fatalf("completed answer missing from follow-up context: %s", msgs)
	}
	if strings.Contains(msgs, "interrupted") {
		t.Fatalf("phantom interruption in follow-up context: %s", msgs)
	}
}
