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

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func staticBudget(n int) func(context.Context) int {
	return func(context.Context) int { return n }
}


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

func (f *fakeLog) SetLastRoute(_ context.Context, id, route, agent string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.category[id] = route
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
	return New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, discard())
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

	id, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "the question", Route: "mini"})
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

// TestChatSkillHintInjectsBodyDeterministically proves skill_hint does
// not depend on the model choosing to call load_skill: the pack body
// lands in the system prompt sent to the provider before any tokens
// stream, on the very first request.
func TestChatSkillHintInjectsBodyDeterministically(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("planned")}
	s := New(gw, newFakeLog(), nil, nil, staticBudget(60_000), []skills.Skill{
		{Name: "travel-planning", Description: "Use when planning a trip", Body: "Ask about dates, budget, destination."},
	}, nil, nil, discard())

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "Tokyo, 5 days", SkillHint: "travel-planning"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	// The first request is the chat turn itself; a first-exchange
	// autoTitle call may follow on its own goroutine, so index by
	// position rather than "last".
	gw.mu.Lock()
	sys := gw.requests[0].System
	gw.mu.Unlock()
	if !strings.Contains(sys, "travel-planning") || !strings.Contains(sys, "Ask about dates, budget, destination.") {
		t.Fatalf("system prompt missing the hinted skill body:\n%s", sys)
	}
}

// The runtime allowlist gates both the system-prompt skill index and
// skill_hint, per turn — no restart, no rebuild.
func TestChatSkillAllowlistGatesIndexAndHint(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	packs := []skills.Skill{
		{Name: "allowed-skill", Description: "Use when allowed", Body: "rules-a"},
		{Name: "blocked-skill", Description: "Use when blocked", Body: "rules-b"},
	}
	allow := func(_ context.Context, name string) bool { return name == "allowed-skill" }
	s := New(gw, newFakeLog(), nil, nil, staticBudget(60_000), packs, allow, nil, discard())

	if _, _, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi", SkillHint: "blocked-skill"}); err == nil {
		t.Fatal("skill_hint for a disallowed pack accepted")
	}

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	sys := chatRequest(t, gw).System
	if !strings.Contains(sys, "allowed-skill") || strings.Contains(sys, "blocked-skill") {
		t.Fatalf("skill index not gated by allowlist:\n%s", sys)
	}
}

func TestChatSkillHintRefusesUnknownSkill(t *testing.T) {
	t.Parallel()
	s := newService(&fakeGW{}, newFakeLog())
	_, _, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi", SkillHint: "no-such-skill"})
	if err == nil {
		t.Fatal("unknown skill_hint accepted")
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
	svc := New(gw, log, nil, order, staticBudget(60_000), nil, nil, nil, discard())

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
	svc := New(gw, log, nil, &compactingLog{log: log}, staticBudget(60_000), nil, nil, nil, discard())

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
	svc := New(gw, log, distill, nil, staticBudget(60_000), nil, nil, nil, discard())

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

func TestMemoryExtractGetsUserTextAndResidue(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("the answer")}
	distill := func(context.Context, string, string) *session.TurnMemory {
		return &session.TurnMemory{KeyFindings: []string{"user moved to Porto"}}
	}
	svc := New(gw, log, distill, nil, staticBudget(60_000), nil, nil, nil, discard())

	type call struct {
		sessionID string
		seq       int64
		text      string
	}
	got := make(chan call, 1)
	svc.SetMemoryExtract(func(_ context.Context, sessionID string, seq int64, text string) {
		got <- call{sessionID, seq, text}
	})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "I moved to Porto"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	select {
	case c := <-got:
		if c.sessionID != "s1" || c.seq == 0 {
			t.Fatalf("call = %+v", c)
		}
		if !strings.Contains(c.text, "I moved to Porto") {
			t.Fatalf("user text missing: %q", c.text)
		}
		if !strings.Contains(c.text, "user moved to Porto") {
			t.Fatalf("residue missing: %q", c.text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("memory extract never invoked")
	}
}

func TestMemoryExtractWithoutDistillSendsAssistantText(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("the answer")}
	svc := newService(gw, log) // no distiller

	got := make(chan string, 1)
	svc.SetMemoryExtract(func(_ context.Context, _ string, _ int64, text string) {
		got <- text
	})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	select {
	case text := <-got:
		if !strings.Contains(text, "question") || !strings.Contains(text, "the answer") {
			t.Fatalf("text = %q, want user + assistant", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("memory extract never invoked")
	}
}

func TestMemoryRetrieveInjectsIntoSystemTail(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("answer")}
	svc := newService(gw, log)
	block := "<memory source=\"timothy-memory\" trust=\"data\">\n- [semantic] user lives in Porto\n</memory>"
	svc.SetMemoryRetrieve(func(_ context.Context, sessionID, query string) string {
		if sessionID != "s1" || !strings.Contains(query, "where do I live") {
			t.Errorf("retrieve got sessionID=%s query=%q", sessionID, query)
		}
		return block
	})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "where do I live?"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := chatRequest(t, gw)
	if !strings.HasSuffix(sent.System, block) {
		t.Fatalf("memory block not at system tail:\n%s", sent.System)
	}
	// The stable prefix stays byte-identical (D-018).
	base := newService(gw, log)
	if !strings.HasPrefix(sent.System, assembleSystem(skills.Index(base.allowedPacks(t.Context(), agents.Agent{Memory: true})))) {
		t.Fatal("system prefix changed by memory injection")
	}
}

// chatRequest returns the turn's actual chat call (auto-title fires a
// second, purposeless mini request on first exchanges).
func chatRequest(t *testing.T, gw *fakeGW) gwclient.StreamRequest {
	t.Helper()
	gw.mu.Lock()
	defer gw.mu.Unlock()
	for _, r := range gw.requests {
		if r.Purpose == "chat" {
			return r
		}
	}
	t.Fatal("no chat request recorded")
	return gwclient.StreamRequest{}
}

func TestMemoryRetrieveEmptyLeavesSystemUntouched(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("answer")}
	svc := newService(gw, log)
	svc.SetMemoryRetrieve(func(context.Context, string, string) string { return "" })

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "hello"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	got := chatRequest(t, gw).System
	want := assembleSystem(skills.Index(svc.allowedPacks(t.Context(), agents.Agent{Memory: true})))
	if got != want {
		t.Fatalf("system modified on empty recall:\n%q\nvs\n%q", got, want)
	}
}

func TestMemoryExtractFiresOnTextlessTurn(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	// Provider quirk: turn completes with reasoning/tool traffic but
	// no text. The user's words still carry facts.
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	}}
	svc := newService(gw, log)
	got := make(chan string, 1)
	svc.SetMemoryExtract(func(_ context.Context, _ string, _ int64, text string) {
		got <- text
	})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "I moved to Porto in June 2026"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	select {
	case text := <-got:
		if !strings.Contains(text, "I moved to Porto") {
			t.Fatalf("text = %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("memory extract skipped on textless turn")
	}
}

// TestCollapseRepeatedTail covers the duplicate-answer collapse: a
// model that restates its whole reply after a late tool call must not
// leave two copies in the persisted turn.
func TestCollapseRepeatedTail(t *testing.T) {
	t.Parallel()
	answer := "The monthly bill would be $29.85 based on 30 million requests and 1,843,200 GB-seconds.\n\n## Sources\n1. [AWS Lambda Pricing](https://aws.amazon.com/lambda/pricing)"
	cases := []struct {
		name, in, want string
	}{
		{"exact double", answer + answer, answer},
		{"double with newline between", answer + "\n" + answer, answer},
		{"triple", answer + "\n" + answer + "\n" + answer, answer},
		{"prefix kept", "Let me check the pricing page.\n" + answer + "\n" + answer, "Let me check the pricing page.\n" + answer},
		{"no repeat unchanged", answer, answer},
		{"short repeats kept", "very good, very good, very good", "very good, very good, very good"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := collapseRepeatedTail(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The serving agent's profile shapes the whole turn: its route and
// name ride the gateway request, its tool allowlist travels for the
// loop, its overlay joins the system prompt, and memory off suppresses
// recall.
func TestAgentProfileShapesTurn(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("done")}
	log := newFakeLog()
	resolver := func(_ context.Context, name string) (agents.Agent, bool) {
		switch name {
		case "", "researcher":
			return agents.Agent{
				Name: "researcher", Route: "research",
				PromptOverlay: "Consult sources before answering.",
				Tools:         []string{"web_search"},
				Memory:        false,
			}, true
		default:
			return agents.Agent{}, false
		}
	}
	svc := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, resolver, discard())
	recalled := false
	svc.SetMemoryRetrieve(func(context.Context, string, string) string {
		recalled = true
		return "MEMORY BLOCK"
	})

	if _, _, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "hi", Agent: "nope"}); err == nil {
		t.Fatal("unknown agent accepted")
	}

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "hi", Agent: "researcher"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := chatRequest(t, gw)
	if sent.Route != "research" || sent.Agent != "researcher" {
		t.Fatalf("route/agent = %s/%s, want research/researcher", sent.Route, sent.Agent)
	}
	if len(sent.ToolAllow) != 1 || sent.ToolAllow[0] != "web_search" {
		t.Fatalf("tool allowlist = %v", sent.ToolAllow)
	}
	if !strings.Contains(sent.System, "Consult sources before answering.") {
		t.Fatalf("overlay missing from system prompt:\n%s", sent.System)
	}
	if recalled || strings.Contains(sent.System, "MEMORY BLOCK") {
		t.Fatal("memory recall ran for a memory-off agent")
	}
}
