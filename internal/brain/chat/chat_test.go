package chat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func staticBudget(n int) func(context.Context) int {
	return func(context.Context) int { return n }
}

// fakeLog is an in-memory SessionLog.
type fakeLog struct {
	mu        sync.Mutex
	events    map[string][]session.Event
	titles    map[string]string
	category  map[string]string
	knowledge map[string][]string
	createdN  int
	// knowledgeErr, when set, makes Knowledge fail instead of reading
	// the map: for the session-knowledge-lookup-failure fallback test.
	knowledgeErr error
	// missions marks a session as mission bookkeeping, mirroring the
	// session store's Meta.Mission.
	missions map[string]bool
}

func newFakeLog() *fakeLog {
	return &fakeLog{events: map[string][]session.Event{}, titles: map[string]string{}, category: map[string]string{}, knowledge: map[string][]string{}, missions: map[string]bool{}}
}

func (f *fakeLog) Create(_ context.Context, title string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdN++
	id := "sess-created"
	f.events[id] = []session.Event{{SessionID: id, Seq: 1, Kind: session.KindSessionStarted, Payload: []byte(`{}`)}}
	return id, nil
}

func (f *fakeLog) Get(_ context.Context, id string) (session.Meta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return session.Meta{ID: id, Title: f.titles[id], Mission: f.missions[id]}, nil
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

func (f *fakeLog) Knowledge(_ context.Context, id string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.knowledgeErr != nil {
		return nil, f.knowledgeErr
	}
	return append([]string(nil), f.knowledge[id]...), nil
}

func (f *fakeLog) AddKnowledge(_ context.Context, id string, names []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, name := range names {
		if !slices.Contains(f.knowledge[id], name) {
			f.knowledge[id] = append(f.knowledge[id], name)
		}
	}
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

func (g *fakeGW) RouteForRole(_ context.Context, role string) (string, bool, error) {
	return role, true, nil
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

// lastChatRequest skips side-calls (title, distill, extract) that race
// the assertion after drain: the async auto-title on a fresh session
// can land after the chat request and make lastRequest nondeterministic.
func (g *fakeGW) lastChatRequest() gwclient.StreamRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := len(g.requests) - 1; i >= 0; i-- {
		if g.requests[i].Purpose == "chat" {
			return g.requests[i]
		}
	}
	return gwclient.StreamRequest{}
}

// erroringGW resolves any role but fails every Stream call: for
// pinning a caller's best-effort behavior on a gateway-unreachable
// error, distinct from fakeGW's always-succeeds default.
type erroringGW struct{}

func (erroringGW) RouteForRole(_ context.Context, role string) (string, bool, error) {
	return role, true, nil
}

func (erroringGW) Stream(context.Context, gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	return nil, errors.New("gateway unreachable")
}

func okEvents(text string) []stream.StreamEvent {
	return []stream.StreamEvent{
		{Type: stream.EventChunk, Text: text},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod", LedgerID: "led"}},
	}
}

func newService(gw Gateway, log SessionLog) *Service {
	return New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, nil, discard())
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

// waitFor polls until cond or timeout: persistence runs after the
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

// TestChatSegmentsTextAcrossToolActivity confirms text chunks from
// consecutive agent-loop steps, separated by tool activity, persist as
// distinct UI blocks joined by "\n\n" rather than fusing byte-for-byte
// ("...area.I'm searching for...").
func TestChatSegmentsTextAcrossToolActivity(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "exact nature area."},
		{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{ID: "call_1", Name: "search_web", Status: "ok"}},
		{Type: stream.EventChunk, Text: "I'm searching for"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod", LedgerID: "led"}},
	}}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "the question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	events := drain(t, ch)

	var streamed strings.Builder
	for _, ev := range events {
		if ev.Type == stream.EventChunk {
			streamed.WriteString(ev.Text)
		}
	}
	if got := streamed.String(); got != "exact nature area.\n\nI'm searching for" {
		t.Fatalf("streamed text = %q", got)
	}

	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })
	stored, _ := log.Events(t.Context(), "s1")
	var turn session.AssistantTurn
	if err := json.Unmarshal(stored[2].Payload, &turn); err != nil {
		t.Fatalf("decode turn: %v", err)
	}
	if turn.LLM.Message != "exact nature area.\n\nI'm searching for" {
		t.Fatalf("turn.LLM.Message = %q", turn.LLM.Message)
	}
	var textBlocks []string
	for _, b := range turn.UI.Blocks {
		if b.Type == "text" {
			textBlocks = append(textBlocks, b.Text)
		}
	}
	want := []string{"exact nature area.", "I'm searching for"}
	if !slices.Equal(textBlocks, want) {
		t.Fatalf("text blocks = %v, want %v", textBlocks, want)
	}
}

// TestChatPersistsTurnCost confirms the persisted assistant_turn
// carries the gateway's cost attribution (D-013: cost is priced or
// absent, never guessed) exactly as the done-frame meta reported it.
func TestChatPersistsTurnCost(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	cost := 0.0042
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "the answer"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod", LedgerID: "led", Cost: &cost, Currency: "USD"}},
	}}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "the question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })
	events, _ := log.Events(t.Context(), "s1")
	var turn session.AssistantTurn
	if err := json.Unmarshal(events[2].Payload, &turn); err != nil {
		t.Fatalf("decode turn: %v", err)
	}
	if turn.Cost == nil || *turn.Cost != cost || turn.Currency != "USD" {
		t.Fatalf("turn cost/currency = %v/%q, want %v/USD", turn.Cost, turn.Currency, cost)
	}
}

// TestChatPersistsTurnCostAbsentWhenUnpriced confirms an unpriced
// model's turn persists with no cost field at all (omitempty), never a
// guessed 0: the replay must be able to distinguish "unpriced" from
// "free".
func TestChatPersistsTurnCostAbsentWhenUnpriced(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("the answer")}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "the question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })
	events, _ := log.Events(t.Context(), "s1")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(events[2].Payload, &raw); err != nil {
		t.Fatalf("decode turn: %v", err)
	}
	if _, ok := raw["cost"]; ok {
		t.Fatalf("payload has cost key, want omitted: %s", raw["cost"])
	}
	if _, ok := raw["currency"]; ok {
		t.Fatalf("payload has currency key, want omitted: %s", raw["currency"])
	}
}

// TestChatPersistsTurnDuration confirms the persisted assistant_turn
// carries a wall-clock DurationMs covering the turn: a stand-in
// upstream delay proves it's measuring real elapsed time, not just
// echoing a zero default.
func TestChatPersistsTurnDuration(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	block := make(chan struct{})
	gw := &fakeGW{events: okEvents("the answer"), blockCh: block}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "the question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	close(block)
	drain(t, ch)

	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })
	events, _ := log.Events(t.Context(), "s1")
	var turn session.AssistantTurn
	if err := json.Unmarshal(events[2].Payload, &turn); err != nil {
		t.Fatalf("decode turn: %v", err)
	}
	if turn.DurationMs < 50 {
		t.Fatalf("turn.DurationMs = %d, want >= 50ms", turn.DurationMs)
	}
}

// TestChatPersistsPermissionAsks confirms the relay appends
// permission_request and permission_resolved as their own durable
// session events the moment they cross the stream: not batched into
// persistTurn at turn end, since a parked ask is exactly the case
// where the turn hasn't ended yet. A replay of the session must then
// carry the resolved pair, and UITranscript must drop it once
// answered (only a still-pending ask belongs in replay).
func TestChatPersistsPermissionAsks(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{
			ID: "perm-1", CallID: "call-1", Tool: "shell", Args: "{}",
			Danger: "destructive", Rationale: "runs a shell command",
		}},
		{Type: stream.EventPermissionResolved, Resolved: &stream.PermissionResolvedEvent{
			ID: "perm-1", Decision: "once",
		}},
		{Type: stream.EventChunk, Text: "done"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod", LedgerID: "led"}},
	}}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "run a command"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	waitFor(t, func() bool { return len(log.kinds("s1")) == 5 })
	kinds := log.kinds("s1")
	want := []string{
		session.KindSessionStarted, session.KindUserMessage,
		session.KindPermissionRequest, session.KindPermissionResolved,
		session.KindAssistantTurn,
	}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}

	events, _ := log.Events(t.Context(), "s1")
	var req session.PermissionRequest
	if err := json.Unmarshal(events[2].Payload, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.ID != "perm-1" || req.Tool != "shell" || req.Danger != "destructive" {
		t.Fatalf("request = %+v", req)
	}
	var res session.PermissionResolved
	if err := json.Unmarshal(events[3].Payload, &res); err != nil {
		t.Fatalf("decode resolved: %v", err)
	}
	if res.ID != "perm-1" || res.Decision != "once" {
		t.Fatalf("resolved = %+v", res)
	}

	// A resolved ask must not appear in a replay: only a still-pending
	// prompt belongs in the UI transcript.
	items, err := session.UITranscript(events)
	if err != nil {
		t.Fatalf("UITranscript: %v", err)
	}
	for _, it := range items {
		if it.Kind == "permission" {
			t.Fatalf("resolved ask still in replay: %+v", it)
		}
	}
}

// TestChatReplayCarriesUnresolvedPermissionAsk confirms a session
// whose turn is still parked on an ask exposes it in the UI transcript
// projection: the whole point of persisting the request at all.
func TestChatReplayCarriesUnresolvedPermissionAsk(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	block := make(chan struct{})
	gw := &fakeGW{blockCh: block, events: []stream.StreamEvent{
		{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{
			ID: "perm-1", CallID: "call-1", Tool: "shell", Args: "{}",
			Danger: "safe", Rationale: "runs a shell command",
		}},
		// No resolution, no terminal: the turn is still parked.
	}}
	s := newService(gw, log)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, ch, err := s.Chat(ctx, Request{SessionID: "s1", Message: "run a command"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	close(block)
	<-ch // receive the permission_request

	waitFor(t, func() bool {
		for _, k := range log.kinds("s1") {
			if k == session.KindPermissionRequest {
				return true
			}
		}
		return false
	})

	events, _ := log.Events(t.Context(), "s1")
	items, err := session.UITranscript(events)
	if err != nil {
		t.Fatalf("UITranscript: %v", err)
	}
	var found bool
	for _, it := range items {
		if it.Kind == "permission" {
			found = true
			if it.Permission == nil || it.Permission.ID != "perm-1" {
				t.Fatalf("permission item = %+v", it)
			}
		}
	}
	if !found {
		t.Fatal("unresolved ask missing from replay")
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
	// The client-facing stream closing does not happen-after turnDone
	// (see relay's drainAndPersist): wait for the slot to actually free
	// before a second turn on the same session, or it races turnBegin's
	// new exclusivity and spuriously hits ErrTurnInFlight (D-042).
	waitFor(t, func() bool { return !s.TurnActive("s1") })

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

// A session whose first exchange fails outright (chain exhausted, a
// dropped stream) never reaches autoTitle: persistTurn returns before
// it on !sawDone, so the title stays empty. Keying needsTitle on the
// title staying empty, rather than on turn history, means titling
// keeps being retried across failed attempts until one lands.
func TestChatRetriesAutoTitleUntilATurnCompletes(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{{Type: stream.EventError, Err: &stream.StreamError{Code: "boom", Message: "boom"}}}}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "first try"})
	if err != nil {
		t.Fatalf("Chat 1: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 }) // session_started, user_message, turn_failed
	// The slot only frees once persistTurn returns and turnDone runs,
	// which is not synchronous with drain returning (D-042): even on
	// an errored turn, this must still happen so the session isn't
	// permanently wedged.
	waitFor(t, func() bool { return !s.TurnActive("s1") })

	log.mu.Lock()
	title := log.titles["s1"]
	log.mu.Unlock()
	if title != "" {
		t.Fatalf("title = %q after a turn that never completed, want empty", title)
	}

	gw.mu.Lock()
	gw.events = okEvents("second answer")
	gw.mu.Unlock()
	_, ch, err = s.Chat(t.Context(), Request{SessionID: "s1", Message: "second try"})
	if err != nil {
		t.Fatalf("Chat 2: %v", err)
	}
	drain(t, ch)

	waitFor(t, func() bool {
		log.mu.Lock()
		defer log.mu.Unlock()
		return log.titles["s1"] != ""
	})
	log.mu.Lock()
	title = log.titles["s1"]
	log.mu.Unlock()
	if title != "second answer" {
		t.Fatalf("title = %q, want the second (first-completed) exchange's text", title)
	}
}

// A session can carry a completed turn yet still have an empty title:
// the title call itself timed out (issue #552). needsTitle must key on
// the empty title, not on turn history, so this session isn't stuck
// untitled forever.
func TestChatTitlesUntitledSessionWithCompletedTurns(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("hello there")}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat 1: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool {
		log.mu.Lock()
		defer log.mu.Unlock()
		return log.titles["s1"] != ""
	})

	// Simulate the title call having timed out: the turn completed but
	// no title landed.
	log.mu.Lock()
	log.titles["s1"] = ""
	log.mu.Unlock()

	gw.mu.Lock()
	gw.events = okEvents("second answer")
	gw.mu.Unlock()
	_, ch, err = s.Chat(t.Context(), Request{SessionID: "s1", Message: "again"})
	if err != nil {
		t.Fatalf("Chat 2: %v", err)
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
	if title != "second answer" {
		t.Fatalf("title = %q, want a retitle after the earlier title call timed out", title)
	}
}

// Mission bookkeeping sessions are not chat: they must never get a
// title call.
func TestChatSkipsTitleForMissionSession(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	log.missions["s1"] = true
	gw := &fakeGW{events: okEvents("hello there")}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) >= 3 }) // session_started, user_message, assistant_turn

	gw.mu.Lock()
	calls := len(gw.requests)
	gw.mu.Unlock()
	if calls != 1 { // chat only, no title call
		t.Fatalf("mission session made %d gateway calls, want 1", calls)
	}
	log.mu.Lock()
	title := log.titles["s1"]
	log.mu.Unlock()
	if title != "" {
		t.Fatalf("title = %q, want empty for a mission bookkeeping session", title)
	}
}

// TestTerminalErrorPersistsTurnFailed pins D-044: a turn that ends on
// a terminal EventError with no partial text must not vanish silently
// : relay used to drop the error after streaming it to the client,
// leaving nothing durable. persistTurn now appends a KindTurnFailed
// event carrying the error's code and message.
func TestTerminalErrorPersistsTurnFailed(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventError, Err: &stream.StreamError{Code: "chain_exhausted", Message: "every provider failed"}},
	}}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hello"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })

	want := []string{session.KindSessionStarted, session.KindUserMessage, session.KindTurnFailed}
	if kinds := log.kinds("s1"); strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}

	events, _ := log.Events(t.Context(), "s1")
	var failed session.TurnFailed
	if err := json.Unmarshal(events[2].Payload, &failed); err != nil {
		t.Fatalf("decode turn_failed: %v", err)
	}
	if failed.Code != "chain_exhausted" || failed.Message != "every provider failed" {
		t.Fatalf("turn_failed = %+v", failed)
	}
}

// TestEmptyResponsePersistsTurnFailed pins D-044's other half: a turn
// that reaches a clean EventDone with no text, no reasoning, and no
// tool executions is not a success either: persistTurn must not write
// a blank assistant_turn, and must instead persist evidence the model
// answered with nothing at all.
func TestEmptyResponsePersistsTurnFailed(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	}}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hello"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })

	want := []string{session.KindSessionStarted, session.KindUserMessage, session.KindTurnFailed}
	if kinds := log.kinds("s1"); strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v (no blank assistant_turn)", kinds, want)
	}

	events, _ := log.Events(t.Context(), "s1")
	var failed session.TurnFailed
	if err := json.Unmarshal(events[2].Payload, &failed); err != nil {
		t.Fatalf("decode turn_failed: %v", err)
	}
	if failed.Code != "empty_response" {
		t.Fatalf("turn_failed.Code = %q, want empty_response", failed.Code)
	}
}

// TestBareStreamClosePersistsTurnAborted pins the last backstop in the
// terminal-delivery chain: when the upstream channel closes with no
// events at all: every producer's terminal lost to the turn deadline
// racing a stream cut: persistTurn must synthesize a turn_failed
// rather than append nothing (a real ~30min turn once vanished with
// zero session_events this way).
func TestBareStreamClosePersistsTurnAborted(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{} // no events: channel closes bare
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hello"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })

	want := []string{session.KindSessionStarted, session.KindUserMessage, session.KindTurnFailed}
	if kinds := log.kinds("s1"); strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}

	events, _ := log.Events(t.Context(), "s1")
	var failed session.TurnFailed
	if err := json.Unmarshal(events[2].Payload, &failed); err != nil {
		t.Fatalf("decode turn_failed: %v", err)
	}
	if failed.Code != "turn_aborted" {
		t.Fatalf("turn_failed.Code = %q, want turn_aborted", failed.Code)
	}
}

// TestSetTurnTimeout pins the ceiling override's floor: anything at or
// below the loop's 10m permission park timeout is rejected (a parked
// ask could never be answered in time), and a valid value replaces the
// compiled default.
func TestSetTurnTimeout(t *testing.T) {
	t.Parallel()
	s := newService(&fakeGW{}, newFakeLog())
	if err := s.SetTurnTimeout(10 * time.Minute); err == nil {
		t.Fatal("10m accepted; want rejection at or below the permission park timeout")
	}
	if err := s.SetTurnTimeout(60 * time.Minute); err != nil {
		t.Fatalf("SetTurnTimeout(60m): %v", err)
	}
	if s.turnTimeout != 60*time.Minute {
		t.Fatalf("turnTimeout = %s, want 60m", s.turnTimeout)
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
	resolver := func(context.Context, string) (agents.Agent, bool) {
		return agents.Agent{Memory: true, Skills: []string{"travel-planning"}}, true
	}
	s := New(gw, newFakeLog(), nil, nil, staticBudget(60_000), []skills.Skill{
		{Name: "travel-planning", Description: "Use when planning a trip", Body: "Ask about dates, budget, destination."},
	}, nil, nil, resolver, discard())

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

// extraToolNames returns the ExtraTools names on req, for asserting
// which turn-only tools (search_kb, read_kb) were offered.
func extraToolNames(req gwclient.StreamRequest) []string {
	var names []string
	for _, t := range req.ExtraTools {
		names = append(names, t.Name)
	}
	return names
}

// TestKBToolsOfferedFromSessionKnowledgeAlone pins the union contract:
// an agent with an empty Knowledge list still gets search_kb/read_kb
// offered when the session itself has pinned collections.
func TestKBToolsOfferedFromSessionKnowledgeAlone(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	log := newFakeLog()
	log.knowledge["s1"] = []string{"docs"}
	resolver := func(context.Context, string) (agents.Agent, bool) {
		return agents.Agent{Memory: true}, true // empty Knowledge
	}
	s := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())
	s.SetKBSearch(func(context.Context, string, []string, string, int) ([]builtin.KBSearchHit, error) {
		return nil, nil
	})
	s.SetKBRead(func(context.Context, string) (builtin.KBDocument, error) {
		return builtin.KBDocument{}, nil
	})

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	names := extraToolNames(chatRequest(t, gw))
	if !slices.Contains(names, "search_kb") || !slices.Contains(names, "read_kb") {
		t.Fatalf("extra tools = %v, want search_kb and read_kb from session knowledge alone", names)
	}
}

// TestKBToolsOfferedWithNoKnowledgeAtAll pins issue #368's default:
// an agent with no Knowledge configured and no session pins still gets
// search_kb/read_kb offered, once a backend is wired: whole-KB search
// is available by default, not gated on any collection being named.
func TestKBToolsOfferedWithNoKnowledgeAtAll(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	log := newFakeLog()
	resolver := func(context.Context, string) (agents.Agent, bool) {
		return agents.Agent{Memory: true}, true // empty Knowledge, no session pins
	}
	s := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())
	s.SetKBSearch(func(context.Context, string, []string, string, int) ([]builtin.KBSearchHit, error) {
		return nil, nil
	})
	s.SetKBRead(func(context.Context, string) (builtin.KBDocument, error) {
		return builtin.KBDocument{}, nil
	})

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	names := extraToolNames(chatRequest(t, gw))
	if !slices.Contains(names, "search_kb") || !slices.Contains(names, "read_kb") {
		t.Fatalf("extra tools = %v, want search_kb and read_kb offered with no knowledge configured", names)
	}
}

// TestPinnedKnowledgePromptBlock pins the system-prompt signal: a
// session with pinned collections and a wired kb backend gets a
// "Pinned knowledge" block naming them.
func TestPinnedKnowledgePromptBlock(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	log := newFakeLog()
	log.knowledge["s1"] = []string{"docs"}
	s := newService(gw, log)
	s.SetKBSearch(func(context.Context, string, []string, string, int) ([]builtin.KBSearchHit, error) {
		return nil, nil
	})

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sys := chatRequest(t, gw).System
	if !strings.Contains(sys, "Pinned knowledge") || !strings.Contains(sys, "docs") {
		t.Fatalf("system prompt missing pinned knowledge block:\n%s", sys)
	}
}

// TestPinnedKnowledgeNoPromptWithoutBackend pins the guard: a pinned
// collection with no kb backend wired must not promise a tool that
// doesn't exist.
func TestPinnedKnowledgeNoPromptWithoutBackend(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	log := newFakeLog()
	log.knowledge["s1"] = []string{"docs"}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sys := chatRequest(t, gw).System
	if strings.Contains(sys, "Pinned knowledge") {
		t.Fatalf("system prompt has pinned knowledge block with no kb backend:\n%s", sys)
	}
}

// TestKBToolsUnionDedupesAgentAndSessionCollections pins the union
// itself: the agent's own Knowledge and the session's pinned list
// combine without duplicates, and both feed the bound search call as a
// boost (never a filter: issue #368).
func TestKBToolsUnionDedupesAgentAndSessionCollections(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	log := newFakeLog()
	log.knowledge["s1"] = []string{"a", "b"}
	resolver := func(context.Context, string) (agents.Agent, bool) {
		return agents.Agent{Memory: true, Knowledge: []string{"a"}}, true
	}
	s := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())

	var gotBoost []string
	s.SetKBSearch(func(_ context.Context, _ string, boost []string, _ string, _ int) ([]builtin.KBSearchHit, error) {
		gotBoost = boost
		return nil, nil
	})

	tool := s.kbSearchTool([]string{"a", "b"})
	if tool == nil {
		t.Fatal("kbSearchTool returned nil with a backend wired")
	}
	if _, err := tool.Execute(t.Context(), []byte(`{"query":"x"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sort.Strings(gotBoost)
	if !slices.Equal(gotBoost, []string{"a", "b"}) {
		t.Fatalf("bound boost collections = %v, want [a b]", gotBoost)
	}
}

// TestKBToolsFallBackOnSessionKnowledgeLookupFailure pins the
// best-effort contract: s.log.Knowledge erroring must not kill the
// turn. search_kb still gets offered from the agent's own Knowledge
// list, but the "Pinned knowledge" block is skipped since skErr != nil.
func TestKBToolsFallBackOnSessionKnowledgeLookupFailure(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	log := newFakeLog()
	log.knowledgeErr = errors.New("session knowledge lookup boom")
	resolver := func(context.Context, string) (agents.Agent, bool) {
		return agents.Agent{Memory: true, Knowledge: []string{"docs"}}, true
	}
	s := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())
	s.SetKBSearch(func(context.Context, string, []string, string, int) ([]builtin.KBSearchHit, error) {
		return nil, nil
	})

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	req := chatRequest(t, gw)
	names := extraToolNames(req)
	if !slices.Contains(names, "search_kb") {
		t.Fatalf("extra tools = %v, want search_kb from agent knowledge alone", names)
	}
	if strings.Contains(req.System, "Pinned knowledge") {
		t.Fatalf("system prompt has pinned knowledge block despite session knowledge lookup failure:\n%s", req.System)
	}
}

// TestChatPersistsMentionedKnowledge pins Chat()'s persistence side:
// req.Knowledge (composer # mentions) gets unioned into the session's
// stored knowledge before the turn runs.
func TestChatPersistsMentionedKnowledge(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	log := newFakeLog()
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi", Knowledge: []string{"runbooks"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	got, err := log.Knowledge(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Knowledge: %v", err)
	}
	if !slices.Equal(got, []string{"runbooks"}) {
		t.Fatalf("session knowledge = %v, want [runbooks]", got)
	}
}

// The runtime allowlist gates both the system-prompt skill index and
// skill_hint, per turn: no restart, no rebuild.
func TestChatSkillAllowlistGatesIndexAndHint(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	packs := []skills.Skill{
		{Name: "allowed-skill", Description: "Use when allowed", Body: "rules-a"},
		{Name: "blocked-skill", Description: "Use when blocked", Body: "rules-b"},
	}
	allow := func(_ context.Context, name string) bool { return name == "allowed-skill" }
	resolver := func(context.Context, string) (agents.Agent, bool) {
		return agents.Agent{Memory: true, Skills: []string{"allowed-skill", "blocked-skill"}}, true
	}
	s := New(gw, newFakeLog(), nil, nil, staticBudget(60_000), packs, allow, nil, resolver, discard())

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

// An agent with an empty skills allowlist gets no packs at all: the
// flipped semantics (empty = none, opt-in only), unlike tools' own
// empty = none via resolveToolAllow's exemptions below.
func TestChatEmptySkillsAllowlistDeniesEveryPack(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	packs := []skills.Skill{
		{Name: "some-skill", Description: "d", Body: "rules"},
	}
	resolver := func(context.Context, string) (agents.Agent, bool) {
		return agents.Agent{Memory: true}, true // Skills left empty
	}
	s := New(gw, newFakeLog(), nil, nil, staticBudget(60_000), packs, nil, nil, resolver, discard())

	// skill_hint naming a pack the (empty) allowlist doesn't list must
	// be refused, same as an unknown skill: a denied hint is never
	// force-loaded around the allowlist.
	if _, _, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi", SkillHint: "some-skill"}); err == nil {
		t.Fatal("skill_hint accepted for an agent with an empty skills allowlist")
	}

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	sys := chatRequest(t, gw).System
	if strings.Contains(sys, "some-skill") {
		t.Fatalf("empty skills allowlist still surfaced a pack in the index:\n%s", sys)
	}
}

// A non-empty skills allowlist admits only the packs it names: the
// unflipped half of the same rule.
func TestChatNonEmptySkillsAllowlistAdmitsOnlyListedPacks(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	packs := []skills.Skill{
		{Name: "listed-skill", Description: "d", Body: "rules-a"},
		{Name: "unlisted-skill", Description: "d", Body: "rules-b"},
	}
	resolver := func(context.Context, string) (agents.Agent, bool) {
		return agents.Agent{Memory: true, Skills: []string{"listed-skill"}}, true
	}
	s := New(gw, newFakeLog(), nil, nil, staticBudget(60_000), packs, nil, nil, resolver, discard())

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	sys := chatRequest(t, gw).System
	if !strings.Contains(sys, "listed-skill") || strings.Contains(sys, "unlisted-skill") {
		t.Fatalf("skills allowlist did not admit only the listed pack:\n%s", sys)
	}
}

// Tools keep the same "empty = none, opt-in only" flip as skills, but
// resolveToolAllow carves out retrieve_output unconditionally (D-019:
// how the model reads back its own offloaded results) and load_skill
// only when the agent's skills allowlist is non-empty (nothing to
// load otherwise).
func TestResolveToolAllowEmptyGrantsOnlyRetrieveOutput(t *testing.T) {
	t.Parallel()
	got := resolveToolAllow(agents.Agent{})
	want := []string{retrieveOutputTool}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("resolveToolAllow(empty) = %v, want %v", got, want)
	}
}

func TestResolveToolAllowEmptyToolsWithSkillsAlsoGrantsLoadSkill(t *testing.T) {
	t.Parallel()
	got := resolveToolAllow(agents.Agent{Skills: []string{"some-skill"}})
	want := map[string]bool{retrieveOutputTool: true, loadSkillTool: true}
	if len(got) != len(want) {
		t.Fatalf("resolveToolAllow = %v, want exactly %v", got, want)
	}
	for _, n := range got {
		if !want[n] {
			t.Fatalf("resolveToolAllow = %v, unexpected entry %q", got, n)
		}
	}
}

func TestResolveToolAllowNonEmptyToolsGainsInfraExemptions(t *testing.T) {
	t.Parallel()
	profile := agents.Agent{Tools: []string{"search_web", "get_current_time"}, Skills: []string{"some-skill"}}
	got := resolveToolAllow(profile)
	want := []string{"search_web", "get_current_time", "retrieve_output", "load_skill"}
	if !slices.Equal(got, want) {
		t.Fatalf("resolveToolAllow(non-empty) = %v, want %v", got, want)
	}
	if len(profile.Tools) != 2 {
		t.Fatalf("profile.Tools mutated: %v", profile.Tools)
	}
}

func TestResolveToolAllowNeverDuplicatesExemptions(t *testing.T) {
	t.Parallel()
	profile := agents.Agent{Tools: []string{"retrieve_output", "load_skill"}, Skills: []string{"some-skill"}}
	got := resolveToolAllow(profile)
	want := []string{"retrieve_output", "load_skill"}
	if !slices.Equal(got, want) {
		t.Fatalf("resolveToolAllow(already listed) = %v, want %v", got, want)
	}
}

// End-to-end: an agent with no tools configured still offers
// retrieve_output to the loop (the exemption survives the actual
// Chat call, not just the helper).
func TestChatEmptyToolsAllowlistStillOffersRetrieveOutput(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("ok")}
	resolver := func(context.Context, string) (agents.Agent, bool) {
		return agents.Agent{Memory: true}, true // Tools left empty
	}
	s := New(gw, newFakeLog(), nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	allow := chatRequest(t, gw).ToolAllow
	if len(allow) != 1 || allow[0] != retrieveOutputTool {
		t.Fatalf("ToolAllow = %v, want only retrieve_output", allow)
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
	svc := New(gw, log, nil, order, staticBudget(60_000), nil, nil, nil, nil, discard())

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
	// A session with a completed turn is already titled; otherwise the
	// async title call races lastRequest below.
	log.titles["s1"] = "seeded"

	gw := &fakeGW{events: okEvents("fresh reply")}
	svc := New(gw, log, nil, &compactingLog{log: log}, staticBudget(60_000), nil, nil, nil, nil, discard())

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
// ends, even while distillation is still running. It also pins the
// D-042 exclusivity boundary this same window now enforces: distill
// runs inside persistTurn, BEFORE turnDone frees the slot (see
// relay's drainAndPersist), so a follow-up landing in that window is
// correctly rejected with ErrTurnInFlight rather than allowed to
// interleave: turn "done" means the slot is free, not merely that the
// client's stream closed. (Before D-042, this test allowed that
// follow-up to proceed immediately; that was the old
// always-install-fresh turnBroadcast semantics this design replaces.)
// Once distill releases and the slot frees, the follow-up proceeds and
// sees the completed answer, never a phantom interruption.
func TestFollowUpSeesCompletedTurnWhileDistillRuns(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("the full answer")}
	distillStarted := make(chan struct{})
	distillRelease := make(chan struct{})
	var startedOnce sync.Once
	distill := func(ctx context.Context, sessionID, turnText, _ string) *session.TurnMemory {
		startedOnce.Do(func() { close(distillStarted) })
		select {
		case <-distillRelease:
		case <-ctx.Done():
		}
		return &session.TurnMemory{KeyFindings: []string{"late residue"}}
	}
	svc := New(gw, log, distill, nil, staticBudget(60_000), nil, nil, nil, nil, discard())

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

	// A follow-up inside the distill window must NOT be allowed to
	// interleave: the slot is still held (turnDone hasn't run yet).
	if _, _, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "second question"}); !errors.Is(err, ErrTurnInFlight) {
		t.Fatalf("Chat during distill window: err = %v, want ErrTurnInFlight", err)
	}
	if got := log.kinds("s1"); len(got) != 3 {
		t.Fatalf("rejected follow-up appended events: kinds = %v, want no change (still 3)", got)
	}

	close(distillRelease)
	waitFor(t, func() bool { return !svc.TurnActive("s1") })

	// Now that the slot is free, the follow-up proceeds and sees the
	// completed answer, never a phantom interruption.
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
	distill := func(context.Context, string, string, string) *session.TurnMemory {
		return &session.TurnMemory{KeyFindings: []string{"user moved to Porto"}}
	}
	svc := New(gw, log, distill, nil, staticBudget(60_000), nil, nil, nil, nil, discard())

	type call struct {
		sessionID string
		seq       int64
		text      string
	}
	got := make(chan call, 1)
	svc.SetMemoryExtract(func(_ context.Context, sessionID string, seq int64, text, _ string) {
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
	svc.SetMemoryExtract(func(_ context.Context, _ string, _ int64, text, _ string) {
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
	// The stable prefix (identity + skills index) stays byte-identical
	// (D-018); only the per-turn tail (date line, then memory) varies.
	base := newService(gw, log)
	stablePrefix := systemPrompt + "\n\n" + skills.Index(base.allowedPacks(t.Context(), agents.Agent{Memory: true}))
	if !strings.HasPrefix(sent.System, stablePrefix) {
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
	want := assembleSystem(skills.Index(svc.allowedPacks(t.Context(), agents.Agent{Memory: true})), time.Now(), nil)
	if got != want {
		t.Fatalf("system modified on empty recall:\n%q\nvs\n%q", got, want)
	}
}

func TestMemoryExtractFiresOnTextlessTurn(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	// Provider quirk: turn completes with reasoning/tool traffic but
	// no text. The user's words still carry facts. A tool execution
	// keeps this a valid non-empty turn (D-044's empty-response guard
	// keys on text+reasoning+tools all being empty).
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{ID: "c1", Name: "time", Status: "ok"}},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	}}
	svc := newService(gw, log)
	got := make(chan string, 1)
	svc.SetMemoryExtract(func(_ context.Context, _ string, _ int64, text, _ string) {
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

// The "auto" sentinel resolves through SetAutoDispatch's candidates
// and classify BEFORE the normal agent lookup: the resolved name (not
// "auto" itself) is what reaches the resolver, the gateway request,
// and persistence.
func TestChatAutoDispatchesAgent(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("done")}
	log := newFakeLog()
	resolver := func(_ context.Context, name string) (agents.Agent, bool) {
		switch name {
		case "", "general":
			return agents.Agent{Name: "general", Route: "default"}, true
		case "researcher":
			return agents.Agent{Name: "researcher", Route: "research"}, true
		default:
			return agents.Agent{}, false
		}
	}
	svc := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())
	candidates := []agents.Agent{
		{Name: "general", Description: "everyday tasks"},
		{Name: "researcher", Description: "consults sources"},
	}
	svc.SetAutoDispatch(
		func(context.Context) []agents.Agent { return candidates },
		func(context.Context, string) (string, error) { return "2", nil }, // picks researcher
	)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "what's the RFC say?", Agent: autoAgentName})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := chatRequest(t, gw)
	if sent.Agent != "researcher" || sent.Route != "research" {
		t.Fatalf("agent/route = %s/%s, want researcher/research (auto-dispatched)", sent.Agent, sent.Route)
	}

	events, err := log.Events(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var msg session.UserMessage
	for _, ev := range events {
		if ev.Kind == session.KindUserMessage {
			_ = json.Unmarshal(ev.Payload, &msg)
		}
	}
	if msg.Agent != "researcher" {
		t.Fatalf("persisted user message agent = %q, want researcher (never the raw auto sentinel)", msg.Agent)
	}
}

// Without SetAutoDispatch wired, the "auto" sentinel falls back to the
// default agent: dispatch is an ergonomics layer, never a hard gate
// on serving a session; an unwired server must not turn every "Auto"
// composer choice into a hard error.
func TestChatAutoWithoutDispatchWiredFallsBackToDefault(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("done")}
	log := newFakeLog()
	resolver := func(_ context.Context, name string) (agents.Agent, bool) {
		if name == "" {
			return agents.Agent{Name: "general", Route: "default"}, true
		}
		return agents.Agent{}, false
	}
	svc := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "hi", Agent: autoAgentName})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := chatRequest(t, gw)
	if sent.Agent != "general" {
		t.Fatalf("agent = %q, want general (fallback default)", sent.Agent)
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
				Tools:         []string{"search_web"},
				Memory:        false,
			}, true
		default:
			return agents.Agent{}, false
		}
	}
	svc := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())
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
	if !slices.Equal(sent.ToolAllow, []string{"search_web", "retrieve_output"}) {
		t.Fatalf("tool allowlist = %v, want authored list plus retrieve_output", sent.ToolAllow)
	}
	if !strings.Contains(sent.System, "Consult sources before answering.") {
		t.Fatalf("overlay missing from system prompt:\n%s", sent.System)
	}
	if recalled || strings.Contains(sent.System, "MEMORY BLOCK") {
		t.Fatal("memory recall ran for a memory-off agent")
	}
}

// A failed attempt (no EventDone) leaves the user_message durable with
// a turn_failed event after it (D-044): persistTurn only appends
// assistant_turn on sawDone. Retry must reuse that message, not append
// a second one; turn_failed doesn't block retry (lastUserMessage only
// stops at a completed assistant_turn).
func TestRetryReusesLastUserMessageWithoutDuplicating(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{{Type: stream.EventError, Err: &stream.StreamError{Code: "boom", Message: "boom"}}}}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "the question", Route: "mini"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })
	want := []string{session.KindSessionStarted, session.KindUserMessage, session.KindTurnFailed}
	if kinds := log.kinds("s1"); strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds after failed attempt = %v, want a dangling user_message plus turn_failed", kinds)
	}
	// The slot only frees once turnDone runs, not synchronously with
	// drain returning (D-042): an errored turn must still free it so
	// Retry below isn't spuriously rejected.
	waitFor(t, func() bool { return !s.TurnActive("s1") })

	gw.mu.Lock()
	gw.events = okEvents("the answer")
	gw.mu.Unlock()

	_, ch, err = s.Retry(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s1")) == 4 })

	kinds := log.kinds("s1")
	want = []string{session.KindSessionStarted, session.KindUserMessage, session.KindTurnFailed, session.KindAssistantTurn}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds after retry = %v, want %v (no duplicated user_message)", kinds, want)
	}

	sent := chatRequest(t, gw)
	if sent.Route != "mini" {
		t.Fatalf("retried route = %q, want mini (the original message's persisted route)", sent.Route)
	}

	events, _ := log.Events(t.Context(), "s1")
	var turn session.AssistantTurn
	if err := json.Unmarshal(events[3].Payload, &turn); err != nil {
		t.Fatalf("decode turn: %v", err)
	}
	if turn.LLM.Message != "the answer" {
		t.Fatalf("turn = %+v", turn)
	}
}

// Retry on a session whose last event isn't a dangling user_message:
// no messages at all, or a turn that already completed: has nothing
// to retry and must reject rather than silently no-op or duplicate.
func TestRetryRejectsWhenNothingToRetry(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	s := newService(&fakeGW{}, log)

	if _, _, err := s.Retry(t.Context(), "s1"); !errors.Is(err, ErrNoRetryableTurn) {
		t.Fatalf("Retry on empty session: err = %v, want ErrNoRetryableTurn", err)
	}

	gw := &fakeGW{events: okEvents("answer")}
	s2 := newService(gw, log)
	_, ch, err := s2.Chat(t.Context(), Request{SessionID: "s2", Message: "q", Route: "mini"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return len(log.kinds("s2")) == 3 })

	if _, _, err := s2.Retry(t.Context(), "s2"); !errors.Is(err, ErrNoRetryableTurn) {
		t.Fatalf("Retry on a completed turn: err = %v, want ErrNoRetryableTurn", err)
	}
}

// A brain restart mid-turn can leave tool_execution/permission events
// (and no pending_state, if the crash landed before a flush) after the
// dangling user_message: the turn never reached persistTurn at all.
// Retry must still treat that message as retryable: none of those
// trailing kinds are turn-terminal, and LLMContext already excludes
// tool_execution/permission_request/permission_resolved from the
// model-facing projection, so the retried turn sees exactly the
// original user message with nothing appended: a clean restart.
func TestRetrySurvivesTrailingToolAndPermissionEvents(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	if _, err := log.Append(t.Context(), "s1", session.KindUserMessage, session.UserMessage{
		Text: "the question", Route: "mini", Agent: "default",
	}); err != nil {
		t.Fatalf("seed user_message: %v", err)
	}
	if _, err := log.Append(t.Context(), "s1", session.KindToolExecution, session.ToolExecution{
		CallID: "c1", Name: "shell", Status: "ok",
	}); err != nil {
		t.Fatalf("seed tool_execution: %v", err)
	}
	if _, err := log.Append(t.Context(), "s1", session.KindPermissionRequest, session.PermissionRequest{
		ID: "p1", CallID: "c2", Tool: "shell",
	}); err != nil {
		t.Fatalf("seed permission_request: %v", err)
	}
	if _, err := log.Append(t.Context(), "s1", session.KindPermissionResolved, session.PermissionResolved{
		ID: "p1", Decision: "once",
	}); err != nil {
		t.Fatalf("seed permission_resolved: %v", err)
	}
	if _, err := log.Append(t.Context(), "s1", session.KindToolExecution, session.ToolExecution{
		CallID: "c2", Name: "shell", Status: "ok",
	}); err != nil {
		t.Fatalf("seed second tool_execution: %v", err)
	}

	gw := &fakeGW{events: okEvents("the answer")}
	s := newService(gw, log)

	_, ch, err := s.Retry(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool {
		kinds := log.kinds("s1")
		return len(kinds) > 0 && kinds[len(kinds)-1] == session.KindAssistantTurn
	})

	kinds := log.kinds("s1")
	want := []string{
		session.KindUserMessage, session.KindToolExecution, session.KindPermissionRequest,
		session.KindPermissionResolved, session.KindToolExecution, session.KindAssistantTurn,
	}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds after retry = %v, want %v (no duplicated user_message)", kinds, want)
	}

	// The context handed to the gateway must be exactly the original
	// user message: tool_execution and permission events never enter
	// LLMContext, and there is no pending_state here to splice in as an
	// interrupted assistant message. A clean restart, not a replay of
	// the dead attempt's partial state.
	sent := chatRequest(t, gw)
	if len(sent.Messages) != 1 || sent.Messages[0].Role != "user" || sent.Messages[0].Content != "the question" {
		t.Fatalf("retried context = %+v, want exactly [{user the question}]", sent.Messages)
	}
	if sent.Route != "mini" {
		t.Fatalf("retried route = %q, want mini (the original message's persisted route)", sent.Route)
	}
}

// A trailing pending_state (a periodic checkpoint the dead attempt
// flushed before dying) is the one trailing kind LLMContext does splice
// back in: as an interrupted assistant message: so the retried turn
// picks up where the checkpoint left off instead of starting blind.
func TestRetryReplaysTrailingPendingStateAsInterrupted(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	if _, err := log.Append(t.Context(), "s1", session.KindUserMessage, session.UserMessage{
		Text: "the question", Route: "mini", Agent: "default",
	}); err != nil {
		t.Fatalf("seed user_message: %v", err)
	}
	if _, err := log.Append(t.Context(), "s1", session.KindPendingState, session.PendingState{
		Partial: "partial answer so far",
	}); err != nil {
		t.Fatalf("seed pending_state: %v", err)
	}

	gw := &fakeGW{events: okEvents("the rest of the answer")}
	s := newService(gw, log)

	_, ch, err := s.Retry(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool {
		kinds := log.kinds("s1")
		return len(kinds) > 0 && kinds[len(kinds)-1] == session.KindAssistantTurn
	})

	sent := chatRequest(t, gw)
	if len(sent.Messages) != 2 {
		t.Fatalf("retried context = %+v, want [user, interrupted-assistant]", sent.Messages)
	}
	if sent.Messages[0].Role != "user" || sent.Messages[0].Content != "the question" {
		t.Fatalf("message[0] = %+v", sent.Messages[0])
	}
	if sent.Messages[1].Role != "assistant" || !strings.Contains(sent.Messages[1].Content, "partial answer so far") ||
		!strings.Contains(sent.Messages[1].Content, "interrupted") {
		t.Fatalf("message[1] = %+v, want the spliced interrupted partial", sent.Messages[1])
	}
}

// TestAutoTitleUsesSummarizeRoleWithLowEffort pins the title request
// shape: the summarize role (fast derived-text chain, same as mission
// titles) with low reasoning effort, so a reasoning model first in the
// chain does not spend the whole deadline thinking.
func TestAutoTitleUsesSummarizeRoleWithLowEffort(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("a good title")}
	s := newService(gw, log)

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

	gw.mu.Lock()
	defer gw.mu.Unlock()
	for _, r := range gw.requests {
		if r.Purpose == "title" {
			if r.Route != "summarize" {
				t.Fatalf("auto-title route = %q, want %q", r.Route, "summarize")
			}
			if r.Effort != "low" {
				t.Fatalf("auto-title effort = %q, want low", r.Effort)
			}
			return
		}
	}
	t.Fatal("no title request recorded")
}

// TestTitleOverGatewayUsesSummarizeRoleAndTrimsQuotes mirrors
// TestClassifyOverGatewayResolvesSummarizeRole for the standalone
// TitleOverGateway constructor (missions naming reuses this instead of
// autoTitle directly, since a mission has no session/reply at create
// time): titles are summarize-class work (D-049), same quote/
// whitespace trimming.
func TestTitleOverGatewayUsesSummarizeRoleAndTrimsQuotes(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents(`"Parse Logs Utility"`)}
	name := TitleOverGateway(gw, discard())(context.Background(), "write a Go CLI that parses logs")
	if name != "Parse Logs Utility" {
		t.Fatalf("name = %q, want quotes trimmed to Parse Logs Utility", name)
	}

	gw.mu.Lock()
	defer gw.mu.Unlock()
	if len(gw.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(gw.requests))
	}
	if gw.requests[0].Route != "summarize" {
		t.Fatalf("route = %q, want summarize", gw.requests[0].Route)
	}
	if gw.requests[0].Purpose != "title" {
		t.Fatalf("purpose = %q, want title", gw.requests[0].Purpose)
	}
	if gw.requests[0].Effort != "low" {
		t.Fatalf("effort = %q, want low", gw.requests[0].Effort)
	}
}

// TestTitleOverGatewayFallsBackToDefaultRoleWhenSummarizeUnbound
// confirms an unbound "summarize" role falls back to "default" rather
// than giving up: a fresh install may not have seeded a summarize
// role yet.
func TestTitleOverGatewayFallsBackToDefaultRoleWhenSummarizeUnbound(t *testing.T) {
	t.Parallel()
	gw := &roleGW{
		fakeGW: fakeGW{events: okEvents("Parse Logs Utility")},
		routeForRole: func(_ context.Context, role string) (string, bool, error) {
			if role == "summarize" {
				return "", false, nil
			}
			return role, true, nil
		},
	}
	name := TitleOverGateway(gw, discard())(context.Background(), "write a Go CLI that parses logs")
	if name != "Parse Logs Utility" {
		t.Fatalf("name = %q, want Parse Logs Utility", name)
	}
	if got := gw.lastRequest().Route; got != "default" {
		t.Fatalf("route = %q, want default (fallback from unbound summarize)", got)
	}
}

// TestTitleOverGatewayEmptyOnGatewayError confirms the best-effort
// contract: any Stream error returns "" rather than propagating, so a
// caller (missions' async naming) never has to distinguish failure
// modes: same as autoTitle's own logged-and-dropped path.
func TestTitleOverGatewayEmptyOnGatewayError(t *testing.T) {
	t.Parallel()
	gw := &erroringGW{}
	name := TitleOverGateway(gw, discard())(context.Background(), "a goal")
	if name != "" {
		t.Fatalf("name = %q, want empty on gateway error", name)
	}
}

// TestCaptionImageOverGatewayUsesVisionRoleAndSendsImage confirms the
// route preference and that the caller's image bytes reach the request
// as a base64 ImageData part rather than raw bytes.
func TestCaptionImageOverGatewayUsesVisionRoleAndSendsImage(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("A bar chart showing quarterly revenue.")}
	caption := CaptionImageOverGateway(gw, discard())(context.Background(), "image/png", []byte("fake-bytes"))
	if caption != "A bar chart showing quarterly revenue." {
		t.Fatalf("caption = %q, want the streamed text", caption)
	}

	gw.mu.Lock()
	defer gw.mu.Unlock()
	if len(gw.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(gw.requests))
	}
	req := gw.requests[0]
	if req.Route != "vision" {
		t.Fatalf("route = %q, want vision", req.Route)
	}
	if req.Purpose != "kb_caption" {
		t.Fatalf("purpose = %q, want kb_caption", req.Purpose)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].Images) != 1 {
		t.Fatalf("messages = %+v, want one message carrying one image", req.Messages)
	}
	img := req.Messages[0].Images[0]
	if img.MediaType != "image/png" {
		t.Fatalf("media type = %q, want image/png", img.MediaType)
	}
	if img.Data != base64.StdEncoding.EncodeToString([]byte("fake-bytes")) {
		t.Fatalf("image data = %q, want base64 of the input bytes", img.Data)
	}
}

// TestCaptionImageOverGatewayFallsBackToDefaultRoleWhenVisionUnbound
// mirrors TitleOverGateway's fallback: a fresh install may not have
// bound the vision role yet.
func TestCaptionImageOverGatewayFallsBackToDefaultRoleWhenVisionUnbound(t *testing.T) {
	t.Parallel()
	gw := &roleGW{
		fakeGW: fakeGW{events: okEvents("A screenshot of a terminal.")},
		routeForRole: func(_ context.Context, role string) (string, bool, error) {
			if role == "vision" {
				return "", false, nil
			}
			return role, true, nil
		},
	}
	caption := CaptionImageOverGateway(gw, discard())(context.Background(), "image/jpeg", []byte("x"))
	if caption != "A screenshot of a terminal." {
		t.Fatalf("caption = %q, want the streamed text", caption)
	}
	if got := gw.lastRequest().Route; got != "default" {
		t.Fatalf("route = %q, want default (fallback from unbound vision)", got)
	}
}

// TestCaptionImageOverGatewayEmptyOnGatewayError confirms the
// best-effort contract: a Stream error returns "" so KB ingest keeps
// the original link/image untouched rather than failing.
func TestCaptionImageOverGatewayEmptyOnGatewayError(t *testing.T) {
	t.Parallel()
	gw := &erroringGW{}
	caption := CaptionImageOverGateway(gw, discard())(context.Background(), "image/png", []byte("x"))
	if caption != "" {
		t.Fatalf("caption = %q, want empty on gateway error", caption)
	}
}

// TestCaptionImageOverGatewayEmptyOnEmptyReply confirms an empty stream
// (no chunks) also degrades to "" rather than a blank caption string
// being treated as real content by a caller.
func TestCaptionImageOverGatewayEmptyOnEmptyReply(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: nil}
	caption := CaptionImageOverGateway(gw, discard())(context.Background(), "image/png", []byte("x"))
	if caption != "" {
		t.Fatalf("caption = %q, want empty on empty stream", caption)
	}
}

// TestClassifyCollectionOverGatewayMatchesExistingID confirms a reply
// that is exactly one listed collection's id resolves to ExistingID.
func TestClassifyCollectionOverGatewayMatchesExistingID(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("col-2")}
	choice := ClassifyCollectionOverGateway(gw, discard())(context.Background(), "Q3 Invoice", "some invoice text",
		[]kb.Collection{{ID: "col-1", Name: "Recipes"}, {ID: "col-2", Name: "Finance"}})
	if choice.ExistingID != "col-2" || choice.NewName != "" {
		t.Fatalf("choice = %+v, want ExistingID=col-2", choice)
	}

	gw.mu.Lock()
	defer gw.mu.Unlock()
	if gw.requests[0].Route != "summarize" {
		t.Fatalf("route = %q, want summarize", gw.requests[0].Route)
	}
	if gw.requests[0].Purpose != "kb_classify" {
		t.Fatalf("purpose = %q, want kb_classify", gw.requests[0].Purpose)
	}
}

// TestClassifyCollectionOverGatewayProposesNewCollection confirms a
// "NEW: name | description" reply parses into NewName/NewDesc.
func TestClassifyCollectionOverGatewayProposesNewCollection(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("NEW: Home Repairs | Notes about fixing things around the house")}
	choice := ClassifyCollectionOverGateway(gw, discard())(context.Background(), "Fixing the sink", "plumbing notes",
		[]kb.Collection{{ID: "col-1", Name: "Recipes"}})
	if choice.ExistingID != "" || choice.NewName != "Home Repairs" || choice.NewDesc != "Notes about fixing things around the house" {
		t.Fatalf("choice = %+v, want new collection Home Repairs", choice)
	}
}

// TestClassifyCollectionOverGatewayListsDocCounts confirms the
// collection list sent to the model includes each collection's
// document count, so the model can judge how established a collection
// already is.
func TestClassifyCollectionOverGatewayListsDocCounts(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("col-1")}
	ClassifyCollectionOverGateway(gw, discard())(context.Background(), "Q3 Invoice", "some invoice text",
		[]kb.Collection{{ID: "col-1", Name: "Finance", Description: "money stuff", DocCount: 7}})

	gw.mu.Lock()
	defer gw.mu.Unlock()
	user := fmt.Sprint(gw.requests[0].Messages)
	if !strings.Contains(user, "col-1: Finance (7 docs) - money stuff") {
		t.Fatalf("user message missing doc count in the collection list:\n%s", user)
	}
}

// TestClassifyCollectionOverGatewayFallsBackToUnsortedOnGatewayError
// confirms the best-effort contract: any Stream error resolves to the
// Unsorted fallback rather than propagating.
func TestClassifyCollectionOverGatewayFallsBackToUnsortedOnGatewayError(t *testing.T) {
	t.Parallel()
	gw := &erroringGW{}
	choice := ClassifyCollectionOverGateway(gw, discard())(context.Background(), "title", "text", nil)
	if choice.NewName != unsortedCollectionName || choice.ExistingID != "" {
		t.Fatalf("choice = %+v, want Unsorted fallback", choice)
	}
}

// TestClassifyCollectionOverGatewayFallsBackOnMalformedReply confirms a
// reply that neither matches a known id nor starts with "NEW:" falls
// back to Unsorted instead of being trusted as a collection id.
func TestClassifyCollectionOverGatewayFallsBackOnMalformedReply(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("not-a-real-id")}
	choice := ClassifyCollectionOverGateway(gw, discard())(context.Background(), "title", "text",
		[]kb.Collection{{ID: "col-1", Name: "Recipes"}})
	if choice.NewName != unsortedCollectionName || choice.ExistingID != "" {
		t.Fatalf("choice = %+v, want Unsorted fallback", choice)
	}
}

// TestValidTitle table-tests each rejection class and a set of
// accepted titles, including a 6-word and a unicode one.
func TestValidTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"too many words", "one two three four five six seven eight nine", false},
		{"too long", strings.Repeat("a", 61), false},
		{"newline", "Parse Logs\nUtility", false},
		{"backtick", "Parse `Logs` Utility", false},
		{"chatter i'll", "I'll create a log parser", false},
		{"chatter i will", "I will build this now", false},
		{"chatter i can", "I can do that for you", false},
		{"chatter i would", "I would suggest a CLI", false},
		{"chatter sure", "Sure, here's a title", false},
		{"chatter okay", "Okay let's name this", false},
		{"chatter ok", "Ok here is a name", false},
		{"chatter let me", "Let me name this for you", false},
		{"chatter here is", "Here is your title", false},
		{"chatter here's", "Here's a good name", false},
		{"chatter certainly", "Certainly, a title follows", false},
		{"chatter of course", "Of course, here's a name", false},
		{"chatter case-insensitive", "SURE thing, a title", false},
		{"bare chatter word", "Ok", false},
		{"normal short", "Parse Logs Utility", true},
		{"six words", "Build A Go Log Parser Tool", true},
		{"unicode", "Résumé Parser Für Verträge", true},
		{"cjk within rune limit", strings.Repeat("日", 21), true},
		{"over 60 runes multibyte", strings.Repeat("日", 61), false},
		{"prefix inside word ok", "Okta Integration Setup", true},
		{"prefix inside word sure", "Surefire Deploy Checklist", true},
		{"prefix inside word here", "Heredoc Quoting Fix", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := validTitle(tc.in); got != tc.want {
				t.Fatalf("validTitle(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// titleScriptedGW returns one canned reply per Stream call, in
// order: for proving TitleOverGateway's one-retry contract, which
// fakeGW's always-identical-events shape can't exercise.
type titleScriptedGW struct {
	fakeGW
	replies []string
}

func (g *titleScriptedGW) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	g.mu.Lock()
	i := len(g.requests)
	g.requests = append(g.requests, req)
	reply := g.replies[min(i, len(g.replies)-1)]
	g.mu.Unlock()
	return okEventsChan(reply), nil
}

func okEventsChan(text string) <-chan stream.StreamEvent {
	ch := make(chan stream.StreamEvent, len(okEvents(text)))
	for _, ev := range okEvents(text) {
		ch <- ev
	}
	close(ch)
	return ch
}

// TestTitleOverGatewayRetriesOnceOnChatterThenSucceeds confirms a
// first reply that answers/comments on the request instead of naming
// it is rejected and retried once with the same params, returning the
// second reply once it's valid.
func TestTitleOverGatewayRetriesOnceOnChatterThenSucceeds(t *testing.T) {
	t.Parallel()
	gw := &titleScriptedGW{replies: []string{"I'll create a log parser for you", "Parse Logs Utility"}}
	name := TitleOverGateway(gw, discard())(context.Background(), "write a Go CLI that parses logs")
	if name != "Parse Logs Utility" {
		t.Fatalf("name = %q, want Parse Logs Utility", name)
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if len(gw.requests) != 2 {
		t.Fatalf("requests = %d, want 2 (one retry)", len(gw.requests))
	}
	if gw.requests[0].Route != gw.requests[1].Route || gw.requests[0].Purpose != gw.requests[1].Purpose {
		t.Fatalf("retry params changed: %+v vs %+v", gw.requests[0], gw.requests[1])
	}
}

// TestTitleOverGatewayEmptyWhenBothAttemptsInvalid confirms two
// consecutive chatter replies exhaust the retry and return "" rather
// than a chatter string.
func TestTitleOverGatewayEmptyWhenBothAttemptsInvalid(t *testing.T) {
	t.Parallel()
	gw := &titleScriptedGW{replies: []string{"I'll create a log parser", "Sure, here's a title for that"}}
	name := TitleOverGateway(gw, discard())(context.Background(), "write a Go CLI that parses logs")
	if name != "" {
		t.Fatalf("name = %q, want empty after both attempts invalid", name)
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if len(gw.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(gw.requests))
	}
}

// TestTitleOverGatewayTakesFirstLine confirms a multi-line reply (a
// model adding commentary after the title despite the prompt) is
// still accepted by taking only its first line, rather than rejected
// by validTitle's newline guard.
func TestTitleOverGatewayTakesFirstLine(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("Fix Login Bug\n\nLet me know if you need anything")}
	name := TitleOverGateway(gw, discard())(context.Background(), "fix the login bug")
	if name != "Fix Login Bug" {
		t.Fatalf("name = %q, want Fix Login Bug", name)
	}
}

// TestTitleOverGatewayLogsStreamErrorEvent confirms a stream that ends
// in EventError with no usable chunk text logs the error's code and
// message rather than failing silently.
func TestTitleOverGatewayLogsStreamErrorEvent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventError, Err: &stream.StreamError{Code: "timeout", Message: "upstream timed out"}},
	}}
	name := TitleOverGateway(gw, log)(context.Background(), "a goal")
	if name != "" {
		t.Fatalf("name = %q, want empty on stream error event", name)
	}
	if !strings.Contains(buf.String(), "title: stream error event") || !strings.Contains(buf.String(), "upstream timed out") {
		t.Fatalf("log missing stream error event details, got: %s", buf.String())
	}
}

// TestTitleOverGatewayLogsInvalidTwice confirms exhausting both
// attempts on invalid titles logs the rejection.
func TestTitleOverGatewayLogsInvalidTwice(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	gw := &titleScriptedGW{replies: []string{"I'll create a log parser", "Sure, here's a title for that"}}
	name := TitleOverGateway(gw, log)(context.Background(), "write a Go CLI that parses logs")
	if name != "" {
		t.Fatalf("name = %q, want empty after both attempts invalid", name)
	}
	if !strings.Contains(buf.String(), "title: rejected by validTitle") {
		t.Fatalf("log missing rejection, got: %s", buf.String())
	}
}

// TestMemoryExtractUsesSensitiveRouteWhenTurnRanSensitiveTool pins the
// side-call route pin: a turn that executed a tool matching the wired
// SensitiveTools sends its extraction on the sensitive route instead of
// memoryd's own default, mirroring the in-turn SetForceRoute pin.
func TestMemoryExtractUsesSensitiveRouteWhenTurnRanSensitiveTool(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{ID: "c1", Name: "personal_gmail_read", Status: "ok"}},
		{Type: stream.EventChunk, Text: "the answer"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	}}
	svc := newService(gw, log)
	svc.SetSensitiveTools(&session.SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	got := make(chan string, 1)
	svc.SetMemoryExtract(func(_ context.Context, _ string, _ int64, _ string, route string) {
		got <- route
	})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "summarize my inbox"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	select {
	case route := <-got:
		if route != "local" {
			t.Fatalf("route = %q, want local (sensitive tool ran this turn)", route)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("memory extract never invoked")
	}
}

// TestMemoryExtractUsesEmptyRouteWhenTurnDidNotRunSensitiveTool proves
// the pin is per-turn, not global: a turn with no matching tool call
// leaves the route empty (memoryd's own default) even with
// SensitiveTools wired.
func TestMemoryExtractUsesEmptyRouteWhenTurnDidNotRunSensitiveTool(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{ID: "c1", Name: "shell", Status: "ok"}},
		{Type: stream.EventChunk, Text: "the answer"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	}}
	svc := newService(gw, log)
	svc.SetSensitiveTools(&session.SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	got := make(chan string, 1)
	svc.SetMemoryExtract(func(_ context.Context, _ string, _ int64, _ string, route string) {
		got <- route
	})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "run a shell command"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	select {
	case route := <-got:
		if route != "" {
			t.Fatalf("route = %q, want empty (no sensitive tool ran)", route)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("memory extract never invoked")
	}
}

// fakeGranter is an in-memory Granter that records every Grant call:
// stands in for tools.Permissions in chat-level tests, which never
// touch a real session_grants table.
type fakeGranter struct {
	mu    sync.Mutex
	calls []grantCall
}

type grantCall struct {
	sessionID, tool, pattern string
}

func (g *fakeGranter) Grant(_ context.Context, sessionID, tool, pattern string, _ time.Duration) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, grantCall{sessionID, tool, pattern})
	return nil
}

func (g *fakeGranter) grantedTools(sessionID string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, c := range g.calls {
		if c.sessionID == sessionID {
			out = append(out, c.tool)
		}
	}
	return out
}

// TestChatSeedsApprovalAllowlistAsStandingGrant proves the mechanism
// this feature adds: a turn served by an agent with a non-empty
// ApprovalAllowlist grants every listed tool for the session before
// the turn runs: the same session_grants row missions/driver.go's
// grantSessionDefaults writes, so tools.Permissions.Resolve's
// matchGrant (D-036 suffix rule) allows the connector-namespaced call
// without an ask on the very first turn.
func TestChatSeedsApprovalAllowlistAsStandingGrant(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("done")}
	log := newFakeLog()
	resolver := func(_ context.Context, name string) (agents.Agent, bool) {
		return agents.Agent{ID: "agent-1", Name: "scheduler", Memory: true,
			ApprovalAllowlist: []string{"list_calendar_events"}}, true
	}
	svc := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())
	granter := &fakeGranter{}
	svc.SetApprovalGrants(granter)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "what's on my calendar", Agent: "scheduler"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	got := granter.grantedTools("s1")
	if len(got) != 1 || got[0] != "list_calendar_events" {
		t.Fatalf("granted tools = %v, want [list_calendar_events]", got)
	}
}

// TestChatWithoutAllowlistGrantsNothing proves the seeder only ever
// widens consent the agent's own config already lists: an agent with
// no ApprovalAllowlist gets no grants, so its tools keep asking exactly
// like before this feature existed.
func TestChatWithoutAllowlistGrantsNothing(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("done")}
	log := newFakeLog()
	resolver := func(_ context.Context, name string) (agents.Agent, bool) {
		return agents.Agent{ID: "agent-2", Name: "plain", Memory: true}, true
	}
	svc := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())
	granter := &fakeGranter{}
	svc.SetApprovalGrants(granter)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "hi", Agent: "plain"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	if got := granter.grantedTools("s1"); len(got) != 0 {
		t.Fatalf("granted tools = %v, want none (agent has no allowlist)", got)
	}
}

// TestChatSeedsApprovalAllowlistOnceIdempotent proves a long-lived
// session doesn't re-INSERT the same grant row on every turn: a second
// turn served by the SAME agent in the SAME session must not call
// Grant again.
func TestChatSeedsApprovalAllowlistOnceIdempotent(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("done")}
	log := newFakeLog()
	resolver := func(_ context.Context, name string) (agents.Agent, bool) {
		return agents.Agent{ID: "agent-1", Name: "scheduler", Memory: true,
			ApprovalAllowlist: []string{"list_calendar_events"}}, true
	}
	svc := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())
	granter := &fakeGranter{}
	svc.SetApprovalGrants(granter)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "first", Agent: "scheduler"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	// See TestChatHistoryComesFromProjection: the slot only frees after
	// drain returns, not synchronously with it (D-042).
	waitFor(t, func() bool { return !svc.TurnActive("s1") })

	_, ch, err = svc.Chat(t.Context(), Request{SessionID: "s1", Message: "second", Agent: "scheduler"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	if got := granter.grantedTools("s1"); len(got) != 1 {
		t.Fatalf("granted tools after two turns = %v, want exactly one grant call", got)
	}
}

// TestChatAgentSwitchGrantsNewAllowlist proves the "once per
// session+agent" key (not "once per session") handles a mid-session
// agent switch: a second turn in the same session served by a
// DIFFERENT agent grants that agent's own list too.
func TestChatAgentSwitchGrantsNewAllowlist(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("done")}
	log := newFakeLog()
	resolver := func(_ context.Context, name string) (agents.Agent, bool) {
		switch name {
		case "scheduler":
			return agents.Agent{ID: "agent-1", Name: "scheduler", Memory: true,
				ApprovalAllowlist: []string{"list_calendar_events"}}, true
		case "mailer":
			return agents.Agent{ID: "agent-2", Name: "mailer", Memory: true,
				ApprovalAllowlist: []string{"gmail_search"}}, true
		default:
			return agents.Agent{}, false
		}
	}
	svc := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())
	granter := &fakeGranter{}
	svc.SetApprovalGrants(granter)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "first", Agent: "scheduler"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	// See TestChatHistoryComesFromProjection: the slot only frees after
	// drain returns, not synchronously with it (D-042).
	waitFor(t, func() bool { return !svc.TurnActive("s1") })

	_, ch, err = svc.Chat(t.Context(), Request{SessionID: "s1", Message: "second", Agent: "mailer"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	got := granter.grantedTools("s1")
	want := []string{"list_calendar_events", "gmail_search"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("granted tools = %v, want %v (both agents' allowlists)", got, want)
	}
}

// TestDistillUsesSensitiveRouteWhenTurnRanSensitiveTool mirrors the
// memory-extract pin above: a turn that executed a sensitive tool must
// keep its distillation on the same pinned route, not the cheap
// "summarize" default: the turn text can quote raw sensitive output.
func TestDistillUsesSensitiveRouteWhenTurnRanSensitiveTool(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{ID: "c1", Name: "personal_gmail_read", Status: "ok"}},
		{Type: stream.EventChunk, Text: "the answer"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	}}
	got := make(chan string, 1)
	distill := func(_ context.Context, _, _, route string) *session.TurnMemory {
		got <- route
		return nil
	}
	svc := New(gw, log, distill, nil, staticBudget(60_000), nil, nil, nil, nil, discard())
	svc.SetSensitiveTools(&session.SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "summarize my inbox"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	select {
	case route := <-got:
		if route != "local" {
			t.Fatalf("route = %q, want local (sensitive tool ran this turn)", route)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("distill never invoked")
	}
}

// TestDistillUsesEmptyRouteWhenTurnDidNotRunSensitiveTool proves the
// pin is per-turn: a turn with no matching tool call leaves the route
// empty (loop.DistillTurn's own "summarize" default) even with
// SensitiveTools wired.
func TestDistillUsesEmptyRouteWhenTurnDidNotRunSensitiveTool(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{ID: "c1", Name: "shell", Status: "ok"}},
		{Type: stream.EventChunk, Text: "the answer"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	}}
	got := make(chan string, 1)
	distill := func(_ context.Context, _, _, route string) *session.TurnMemory {
		got <- route
		return nil
	}
	svc := New(gw, log, distill, nil, staticBudget(60_000), nil, nil, nil, nil, discard())
	svc.SetSensitiveTools(&session.SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "run a shell command"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	select {
	case route := <-got:
		if route != "" {
			t.Fatalf("route = %q, want empty (no sensitive tool ran)", route)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("distill never invoked")
	}
}

// seedSensitiveSession pre-populates a session's log with a completed
// turn that ran a sensitive tool: standing in for "a prior turn in
// this session already executed gmail_read", the trigger for the
// whole-session route pin under test below.
func seedSensitiveSession(t *testing.T, log *fakeLog, sessionID string) {
	t.Helper()
	if _, err := log.Append(t.Context(), sessionID, session.KindToolExecution, session.ToolExecution{
		CallID: "c0", Name: "personal_gmail_read", Status: "ok",
	}); err != nil {
		t.Fatalf("seed tool_execution: %v", err)
	}
	if _, err := log.Append(t.Context(), sessionID, session.KindAssistantTurn, session.AssistantTurn{}); err != nil {
		t.Fatalf("seed assistant_turn: %v", err)
	}
	// Already titled, like any session with a completed turn: keeps the
	// async title call out of the gateway requests these tests inspect.
	log.titles[sessionID] = "seeded"
}

// TestChatPinsSessionSensitiveRouteOnNextTurn is the feature under
// test: once a PRIOR turn in the session executed a sensitive tool, the
// NEXT Chat() call: which itself runs no sensitive tool: must still
// be served on the sensitive route, with the route picker ignored and
// the model hint dropped (a hint outranks the route at the gateway,
// same reason the in-turn SetForceRoute pin drops it).
func TestChatPinsSessionSensitiveRouteOnNextTurn(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	seedSensitiveSession(t, log, "s1")
	gw := &fakeGW{events: okEvents("the answer")}
	svc := newService(gw, log)
	svc.SetSensitiveTools(&session.SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "another question", Route: "mini", ModelHint: "big-model"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := gw.lastChatRequest()
	if sent.Route != "local" {
		t.Fatalf("route = %q, want local (session pinned by a prior sensitive tool call)", sent.Route)
	}
	if sent.ModelHint != "" {
		t.Fatalf("model hint = %q, want empty (dropped alongside the route pin)", sent.ModelHint)
	}
}

// TestChatSessionPinNoopWhenSensitiveRouteUnset proves the pin is
// active only when sensitive_tool_route is actually configured: a nil
// Route func (the feature off) leaves a sensitive session's next turn
// on ordinary routing, matching today's behavior everywhere.
func TestChatSessionPinNoopWhenSensitiveRouteUnset(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	seedSensitiveSession(t, log, "s1")
	gw := &fakeGW{events: okEvents("the answer")}
	svc := newService(gw, log)
	svc.SetSensitiveTools(&session.SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "" }})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "another question", Route: "mini", ModelHint: "big-model"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := gw.lastChatRequest()
	if sent.Route != "mini" {
		t.Fatalf("route = %q, want mini (sensitive route unset, picker honored)", sent.Route)
	}
	if sent.ModelHint != "big-model" {
		t.Fatalf("model hint = %q, want big-model (unset route means no pin)", sent.ModelHint)
	}
}

// TestChatFreshSessionRoutesNormally proves the pin only fires once a
// sensitive tool has actually run: a session with no prior turns at all
// routes exactly as requested.
func TestChatFreshSessionRoutesNormally(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("the answer")}
	svc := newService(gw, log)
	svc.SetSensitiveTools(&session.SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "a question", Route: "mini", ModelHint: "big-model"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := gw.lastChatRequest()
	if sent.Route != "mini" {
		t.Fatalf("route = %q, want mini (fresh session, no sensitive tool ran yet)", sent.Route)
	}
	if sent.ModelHint != "big-model" {
		t.Fatalf("model hint = %q, want big-model (no pin on a fresh session)", sent.ModelHint)
	}
}

// TestRetryPinsSessionSensitiveRoute mirrors the Chat pin for Retry: a
// session already pinned by a prior sensitive tool call re-runs its
// last turn on the sensitive route even though Retry reuses the
// original request's own route/model_hint verbatim.
func TestRetryPinsSessionSensitiveRoute(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	seedSensitiveSession(t, log, "s1")
	if _, err := log.Append(t.Context(), "s1", session.KindUserMessage, session.UserMessage{
		Text: "retry me", Route: "mini", Agent: "default", ModelHint: "big-model",
	}); err != nil {
		t.Fatalf("seed user_message: %v", err)
	}
	gw := &fakeGW{events: okEvents("the answer")}
	svc := newService(gw, log)
	svc.SetSensitiveTools(&session.SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	_, ch, err := svc.Retry(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	drain(t, ch)

	sent := gw.lastChatRequest()
	if sent.Route != "local" {
		t.Fatalf("route = %q, want local (session pinned by a prior sensitive tool call)", sent.Route)
	}
	if sent.ModelHint != "" {
		t.Fatalf("model hint = %q, want empty (dropped alongside the route pin)", sent.ModelHint)
	}
}

// TestPersistTurnPinsSideCallsWhenSessionPreviouslySensitive proves
// persistTurn's side-call route resolution (distill, memory extract)
// treats "session pinned by an earlier turn" the same as "this turn
// itself ran a sensitive tool": the context this turn saw still carries
// the earlier turn's sensitive content even though THIS turn's own
// tool calls are unrelated.
func TestPersistTurnPinsSideCallsWhenSessionPreviouslySensitive(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	seedSensitiveSession(t, log, "s1")
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{ID: "c1", Name: "shell", Status: "ok"}},
		{Type: stream.EventChunk, Text: "the answer"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	}}
	distillRoute := make(chan string, 1)
	distill := func(_ context.Context, _, _, route string) *session.TurnMemory {
		distillRoute <- route
		return nil
	}
	svc := New(gw, log, distill, nil, staticBudget(60_000), nil, nil, nil, nil, discard())
	svc.SetSensitiveTools(&session.SensitiveTools{ConnectorNames: func(context.Context) []string { return []string{"personal"} }, Route: func(context.Context) string { return "local" }})

	extractRoute := make(chan string, 1)
	svc.SetMemoryExtract(func(_ context.Context, _ string, _ int64, _ string, route string) {
		extractRoute <- route
	})

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "run a shell command, unrelated to email"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	select {
	case route := <-distillRoute:
		if route != "local" {
			t.Fatalf("distill route = %q, want local (session pinned by an earlier turn)", route)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("distill never invoked")
	}
	select {
	case route := <-extractRoute:
		if route != "local" {
			t.Fatalf("memory extract route = %q, want local (session pinned by an earlier turn)", route)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("memory extract never invoked")
	}
}

// slowGW is a Gateway whose stream keeps producing on its own schedule,
// gated ONLY by the context passed to Stream: unlike fakeGW, its sends
// aren't wrapped in a second select that could race the caller's read.
// Used to prove that once runTurn hands gw.Stream the detached turnCtx
// (not the request's ctx), the upstream genuinely survives a request-
// side cancel: production's real gwclient.Stream is bound to whatever
// ctx runTurn passes it, so this models that binding faithfully instead
// of asserting it indirectly. delay applies uniformly before every
// event unless delays is set, giving a per-event delay instead (used
// when the test needs the first event immediate but later ones held
// off far past the point the test acts).
type slowGW struct {
	events []stream.StreamEvent
	delay  time.Duration
	delays []time.Duration
}

func (g *slowGW) RouteForRole(_ context.Context, role string) (string, bool, error) {
	return role, true, nil
}

func (g *slowGW) Stream(ctx context.Context, _ gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	ch := make(chan stream.StreamEvent)
	go func() {
		defer close(ch)
		for i, ev := range g.events {
			d := g.delay
			if i < len(g.delays) {
				d = g.delays[i]
			}
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// TestChatSurvivesClientDisconnect is the kill-test for the fix this
// package exists to ship: cancelling the REQUEST context (a browser
// nav/reload) must not cancel the turn. runTurn hands gw.Stream the
// detached turnCtx, so slowGW here is bound to that, not to the
// request ctx passed into Chat: the disconnect must only stop
// forwarding to the client channel, never the upstream itself.
func TestChatSurvivesClientDisconnect(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &slowGW{delay: 20 * time.Millisecond, events: []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "first "},
		{Type: stream.EventChunk, Text: "second "},
		{Type: stream.EventChunk, Text: "third"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	}}
	s := newService(gw, log)

	reqCtx, cancel := context.WithCancel(t.Context())
	_, ch, err := s.Chat(reqCtx, Request{SessionID: "s1", Message: "long question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// A /live subscriber attached independently of the request that
	// started the turn: it must still see every event, including the
	// ones the disconnecting client never gets.
	replay, live, ok := s.Subscribe("s1")
	if !ok {
		t.Fatal("Subscribe: no turn in flight")
	}
	if len(replay) != 0 {
		t.Fatalf("replay before any event = %v, want empty", replay)
	}

	<-ch     // receive the first forwarded chunk
	cancel() // client disconnects — browser nav/reload

	// out must close promptly: the client-facing channel does not wait
	// for the whole turn to finish once reqCtx is done.
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				break loop
			}
		case <-deadline:
			t.Fatal("out did not close promptly after client disconnect")
		}
	}

	// The broadcaster (and any /live subscriber) keeps seeing events
	// after the original client disconnected, until the turn's own
	// terminal.
	var got []stream.StreamEvent
	for ev := range live {
		got = append(got, ev)
	}
	var text strings.Builder
	sawDone := false
	for _, ev := range got {
		if ev.Type == stream.EventChunk {
			text.WriteString(ev.Text)
		}
		if ev.Type == stream.EventDone {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatalf("broadcaster never saw the terminal done event: %+v", got)
	}
	if text.String() != "first second third" {
		t.Fatalf("broadcaster text = %q, want full upstream text", text.String())
	}

	// The turn must persist as a completed assistant_turn: never
	// turn_failed: despite the client having vanished mid-stream. A
	// pending_state checkpoint (flushPending, on the disconnect branch
	// itself) may also land before it; that's an existing, harmless
	// checkpoint superseded by the real completed turn right after.
	waitFor(t, func() bool { return !s.TurnActive("s1") })
	kinds := log.kinds("s1")
	if kinds[len(kinds)-1] != session.KindAssistantTurn {
		t.Fatalf("kinds = %v, want assistant_turn last (turn must complete normally, not fail)", kinds)
	}
	for _, k := range kinds {
		if k == session.KindTurnFailed {
			t.Fatalf("kinds = %v, must not contain turn_failed", kinds)
		}
	}
	events, _ := log.Events(t.Context(), "s1")
	var turn session.AssistantTurn
	if err := json.Unmarshal(events[len(events)-1].Payload, &turn); err != nil {
		t.Fatalf("decode turn: %v", err)
	}
	if turn.LLM.Message != "first second third" {
		t.Fatalf("persisted turn text = %q, want full upstream text", turn.LLM.Message)
	}
}

// TestStopTurnCancelsMidStream: StopTurn must let a caller (a "stop"
// button in the UI) cancel a session's in-flight turn server-side. The
// upstream (bound to turnCtx) winds down once cancelled, and the
// existing abnormal-end machinery in persistTurn takes over: partial
// text becomes a pending_state (the same shape a real client disconnect
// with no terminal produces).
func TestStopTurnCancelsMidStream(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	// slowGW (not fakeGW): the upstream must still be live-but-idle
	// after the first chunk, so the test can call StopTurn
	// deterministically mid-stream rather than racing the upstream's
	// own natural close: fakeGW's producer goroutine exits (closing
	// the channel) right after its last canned event, which can beat
	// StopTurn to the punch when there is no terminal event to hold it
	// open. The second event is delayed well past StopTurn's cancel.
	gw := &slowGW{
		events: []stream.StreamEvent{
			{Type: stream.EventChunk, Text: "partial answer"},
			{Type: stream.EventChunk, Text: " that stalls here"},
		},
		delays: []time.Duration{0, 2 * time.Second},
	}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "long question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !s.TurnActive("s1") {
		t.Fatal("TurnActive = false right after Chat, want true")
	}

	<-ch // receive the first chunk, so the turn has partial text before stopping

	if !s.StopTurn("s1") {
		t.Fatal("StopTurn = false, want true (a turn is live)")
	}
	drain(t, ch)

	waitFor(t, func() bool { return !s.TurnActive("s1") })

	found := false
	for _, k := range log.kinds("s1") {
		if k == session.KindPendingState {
			found = true
		}
	}
	if !found {
		t.Fatalf("kinds = %v, want a pending_state (partial text, abnormal end)", log.kinds("s1"))
	}

	// Stopping again finds nothing live.
	if s.StopTurn("s1") {
		t.Fatal("StopTurn on an already-finished session = true, want false")
	}
}

// TestStopTurnNoActiveTurn confirms StopTurn is a plain false, not a
// panic or error, when the session never had a turn running.
func TestStopTurnNoActiveTurn(t *testing.T) {
	t.Parallel()
	s := newService(&fakeGW{}, newFakeLog())
	if s.StopTurn("never-started") {
		t.Fatal("StopTurn on an idle session = true, want false")
	}
}

// TestTurnTimeoutCeilingEndsAbandonedTurn confirms the detached turn
// context actually ceils the turn's lifetime: with turnTimeout set very
// small, an upstream that outlives it must be cut off and the turn
// persisted with whatever partial evidence exists, rather than running
// forever. Production always runs the real 30-minute const; s.turnTimeout
// exists as a settable field precisely so this deadline path can be
// exercised without an unreasonably slow test.
func TestTurnTimeoutCeilingEndsAbandonedTurn(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &slowGW{delay: 500 * time.Millisecond, events: []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "partial"},
		{Type: stream.EventChunk, Text: " more"},
		{Type: stream.EventChunk, Text: " than the ceiling allows"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	}}
	s := newService(gw, log)
	s.turnTimeout = 30 * time.Millisecond // far shorter than any event delay above

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "long question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	waitFor(t, func() bool { return !s.TurnActive("s1") })
	kinds := log.kinds("s1")
	// The ceiling fires before any chunk (500ms delay vs 30ms ceiling),
	// so there is no partial text worth keeping: the turn ends with no
	// assistant_turn, same shape a real abandoned/stuck upstream leaves.
	for _, k := range kinds {
		if k == session.KindAssistantTurn {
			t.Fatalf("kinds = %v, want no assistant_turn (ceiling cut the turn short)", kinds)
		}
	}
}

// roleGW is a fakeGW whose RouteForRole is caller-controlled, for
// testing ClassifyOverGateway's role resolution directly (fakeGW's
// default RouteForRole always reports ok=true, which can't exercise
// the unbound-role path).
type roleGW struct {
	fakeGW
	routeForRole func(context.Context, string) (string, bool, error)
}

func (g *roleGW) RouteForRole(ctx context.Context, role string) (string, bool, error) {
	return g.routeForRole(ctx, role)
}

// ClassifyOverGateway resolves the classification call's route by the
// "summarize" role (D-049) rather than any hardcoded route name, and
// streams on whatever route that role currently resolves to.
func TestClassifyOverGatewayResolvesSummarizeRole(t *testing.T) {
	t.Parallel()
	gw := &roleGW{
		fakeGW: fakeGW{events: okEvents("2")},
		routeForRole: func(_ context.Context, role string) (string, bool, error) {
			if role != "summarize" {
				t.Fatalf("role = %q, want summarize", role)
			}
			return "cheap-cloud", true, nil
		},
	}
	classify := ClassifyOverGateway(gw)
	reply, err := classify(t.Context(), "pick one")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if reply != "2" {
		t.Fatalf("reply = %q, want 2", reply)
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if len(gw.requests) != 1 {
		t.Fatalf("Stream called %d times, want 1", len(gw.requests))
	}
	if got := gw.requests[0].Route; got != "cheap-cloud" {
		t.Fatalf("route = %q, want cheap-cloud (resolved via summarize role)", got)
	}
}

// When the "summarize" role is unbound, ClassifyOverGateway returns an
// error WITHOUT calling Stream at all: agents.Dispatch already treats
// any classify error as "fall back to the default agent", so this just
// disables auto-dispatch rather than breaking the turn or firing an
// avoidable request.
func TestClassifyOverGatewaySkipsStreamWhenRoleUnbound(t *testing.T) {
	t.Parallel()
	gw := &roleGW{
		fakeGW: fakeGW{events: okEvents("2")},
		routeForRole: func(context.Context, string) (string, bool, error) {
			return "", false, nil
		},
	}
	classify := ClassifyOverGateway(gw)
	if _, err := classify(t.Context(), "pick one"); err == nil {
		t.Fatal("classify: want error when summarize role is unbound")
	}
	gw.mu.Lock()
	n := len(gw.requests)
	gw.mu.Unlock()
	if n != 0 {
		t.Fatalf("Stream called %d times, want 0 (no dispatch call when role unbound)", n)
	}
}

// Dispatch (agents package) already falls back to the default agent on
// any classify error, so an unbound summarize role surfaces here as a
// dispatch fallback, never a broken turn: this is the same contract
// TestChatAutoWithoutDispatchWiredFallsBackToDefault exercises for a
// nil classify, extended to a wired-but-erroring one.
func TestChatAutoDispatchFallsBackWhenClassifyErrors(t *testing.T) {
	t.Parallel()
	gw := &fakeGW{events: okEvents("done")}
	log := newFakeLog()
	resolver := func(_ context.Context, name string) (agents.Agent, bool) {
		switch name {
		case "", "general":
			return agents.Agent{Name: "general", Route: "default"}, true
		case "researcher":
			return agents.Agent{Name: "researcher", Route: "research"}, true
		default:
			return agents.Agent{}, false
		}
	}
	svc := New(gw, log, nil, nil, staticBudget(60_000), nil, nil, nil, resolver, discard())
	candidates := []agents.Agent{
		{Name: "general", Description: "everyday tasks"},
		{Name: "researcher", Description: "consults sources"},
	}
	svc.SetAutoDispatch(
		func(context.Context) []agents.Agent { return candidates },
		func(context.Context, string) (string, error) { return "", errors.New("summarize role unbound") },
	)

	_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "what's the RFC say?", Agent: autoAgentName})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	sent := chatRequest(t, gw)
	if sent.Agent != "general" || sent.Route != "default" {
		t.Fatalf("agent/route = %s/%s, want general/default (dispatch fallback on classify error)", sent.Agent, sent.Route)
	}
}
