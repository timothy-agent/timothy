package loop

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

	"github.com/prometheus/client_golang/prometheus"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func testToolCalls() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_tool_calls_total"}, []string{"tool", "outcome"})
}

// scriptedGateway plays one canned event script per Stream call and
// records every request it saw.
type scriptedGateway struct {
	mu       sync.Mutex
	scripts  [][]stream.StreamEvent
	requests []gwclient.StreamRequest
}

func (g *scriptedGateway) RouteForRole(_ context.Context, role string) (string, bool, error) {
	return role, true, nil
}

func (g *scriptedGateway) Stream(_ context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	g.mu.Lock()
	g.requests = append(g.requests, req)
	if len(g.scripts) == 0 {
		g.mu.Unlock()
		return nil, fmt.Errorf("scripted gateway exhausted")
	}
	script := g.scripts[0]
	g.scripts = g.scripts[1:]
	g.mu.Unlock()

	ch := make(chan stream.StreamEvent)
	go func() {
		defer close(ch)
		for _, ev := range script {
			ch <- ev
		}
	}()
	return ch, nil
}

func toolCallStep(pairs ...[2]string) []stream.StreamEvent {
	evs := []stream.StreamEvent{}
	for i, p := range pairs {
		id := fmt.Sprintf("call_%d", i+1)
		evs = append(evs,
			stream.StreamEvent{Type: stream.EventToolStart, ToolCall: &stream.ToolCallEvent{ID: id, Name: p[0]}},
			stream.StreamEvent{Type: stream.EventToolEnd, ToolCall: &stream.ToolCallEvent{
				ID: id, Name: p[0], Input: json.RawMessage(p[1]),
			}},
		)
	}
	evs = append(evs,
		stream.StreamEvent{Type: stream.EventUsage, Usage: &stream.Usage{InputTokens: 10, OutputTokens: 5}},
		stream.StreamEvent{Type: stream.EventDone, Meta: &stream.Meta{Provider: "fake", Model: "fake-1"}},
	)
	return evs
}

func finalStep(text string) []stream.StreamEvent {
	return []stream.StreamEvent{
		{Type: stream.EventChunk, Text: text},
		{Type: stream.EventUsage, Usage: &stream.Usage{InputTokens: 20, OutputTokens: 8}},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "fake", Model: "fake-1"}},
	}
}

// fixed test doubles

type allowAllPerms struct{ granted []string }

func (p *allowAllPerms) Resolve(_ context.Context, _, tool string, args json.RawMessage) (tools.Resolution, error) {
	return tools.Resolution{Decision: tools.DecisionAllow, Subject: tool}, nil
}
func (p *allowAllPerms) Grant(_ context.Context, _, tool, pattern string, _ time.Duration) error {
	p.granted = append(p.granted, tool+":"+pattern)
	return nil
}

type memOutputs struct {
	mu   sync.Mutex
	rows map[string]string
}

func (m *memOutputs) Put(_ context.Context, _, _, content string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string]string{}
	}
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(m.rows)+1)
	m.rows[id] = content
	return id, nil
}

type memAudit struct {
	mu      sync.Mutex
	entries []tools.AuditEntry
}

func (m *memAudit) Record(_ context.Context, e tools.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return nil
}

type memEvents struct {
	mu    sync.Mutex
	kinds []string
	seqs  int64
}

func (m *memEvents) Append(_ context.Context, _, kind string, _ any) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kinds = append(m.kinds, kind)
	m.seqs++
	return m.seqs, nil
}

type registryExec struct{ c *tools.Constrained }

func (r registryExec) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	return r.c.Execute(ctx, name, args)
}

func testAgent(t *testing.T, gw Gateway, extra ...*tools.Tool) (*Agent, *memAudit, *memEvents, *allowAllPerms) {
	t.Helper()
	reg := tools.NewRegistry()
	echo := &tools.Tool{
		Name:        "echo",
		Description: "echoes",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		Execute: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(args, &a)
			return "echo: " + a.Text, nil
		},
	}
	if err := reg.Register(echo); err != nil {
		t.Fatal(err)
	}
	for _, tool := range extra {
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	c, err := tools.NewConstrained(reg, testToolCalls())
	if err != nil {
		t.Fatal(err)
	}
	defs := make([]provider.ToolDef, 0, len(reg.List()))
	for _, tool := range reg.List() {
		defs = append(defs, provider.ToolDef{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	audit := &memAudit{}
	events := &memEvents{}
	perms := &allowAllPerms{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := NewAgent(gw, registryExec{c}, perms, &memOutputs{}, audit, events, NewPermBroker(), defs, logger)
	return a, audit, events, perms
}

func collect(t *testing.T, ch <-chan stream.StreamEvent) []stream.StreamEvent {
	t.Helper()
	var out []stream.StreamEvent
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("stream did not finish; got %d events", len(out))
		}
	}
}

func ofType(evs []stream.StreamEvent, typ stream.EventType) []stream.StreamEvent {
	var out []stream.StreamEvent
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func TestAgentToolCallThenAnswer(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"echo", `{"text":"hi"}`}),
		finalStep("the echo said hi"),
	}}
	a, audit, events, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding", Messages: []provider.Message{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if got := ofType(evs, stream.EventToolResult); len(got) != 1 || got[0].ToolResult.Status != "ok" {
		t.Fatalf("tool results = %+v", got)
	}
	if got := ofType(evs, stream.EventDone); len(got) != 1 {
		t.Fatalf("done events = %d, want exactly 1", len(got))
	}
	usage := ofType(evs, stream.EventUsage)
	if len(usage) != 1 || usage[0].Usage.InputTokens != 30 || usage[0].Usage.OutputTokens != 13 {
		t.Fatalf("usage = %+v, want summed 30/13", usage)
	}
	if len(audit.entries) != 1 || audit.entries[0].Status != "ok" {
		t.Fatalf("audit = %+v", audit.entries)
	}
	if len(events.kinds) != 1 || events.kinds[0] != "tool_execution" {
		t.Fatalf("session events = %v", events.kinds)
	}

	// Second request must carry the round-trip messages.
	second := gw.requests[1]
	var sawCall, sawResult bool
	for _, m := range second.Messages {
		if len(m.ToolCalls) > 0 {
			sawCall = true
		}
		if m.ToolResult != nil && strings.Contains(m.ToolResult.Content, "echo: hi") {
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("round-trip messages missing: %+v", second.Messages)
	}
	// D-020: the continuation after a clean tool step runs low effort.
	if second.Effort != "low" {
		t.Fatalf("continuation effort = %q, want low", second.Effort)
	}
	if gw.requests[0].Effort != "" {
		t.Fatalf("first step effort = %q, want normal", gw.requests[0].Effort)
	}
}

// TestAgentProviderStateEchoedAcrossSteps pins D-067: a driver's
// continuation state (e.g. the openai-responses driver's
// previous_response_id) riding a step's done Meta.ProviderState must be
// echoed back on the turn's NEXT step request, and a fresh turn must
// start with no state at all.
func TestAgentProviderStateEchoedAcrossSteps(t *testing.T) {
	t.Parallel()
	state := json.RawMessage(`{"driver":"openai-responses","previous_response_id":"resp_1"}`)
	toolStep := toolCallStep([2]string{"echo", `{"text":"hi"}`})
	toolStep[len(toolStep)-1].Meta = &stream.Meta{Provider: "fake", Model: "fake-1", ProviderState: state}

	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolStep,
		finalStep("the echo said hi"),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding", Messages: []provider.Message{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	if len(gw.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(gw.requests))
	}
	if len(gw.requests[0].ProviderState) != 0 {
		t.Fatalf("first step ProviderState = %s, want empty on a fresh turn", gw.requests[0].ProviderState)
	}
	if string(gw.requests[1].ProviderState) != string(state) {
		t.Fatalf("second step ProviderState = %s, want %s echoed from step 1's done Meta", gw.requests[1].ProviderState, state)
	}
}

// cancelThenCutGateway streams one chunk, waits until the caller's
// context is canceled, then closes the channel bare — no terminal.
// This is the exact shape of the turn deadline racing a provider cut.
type cancelThenCutGateway struct{ ctxDone <-chan struct{} }

func (g *cancelThenCutGateway) RouteForRole(_ context.Context, role string) (string, bool, error) {
	return role, true, nil
}

func (g *cancelThenCutGateway) Stream(_ context.Context, _ gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	ch := make(chan stream.StreamEvent)
	go func() {
		defer close(ch)
		ch <- stream.StreamEvent{Type: stream.EventChunk, Text: "par"}
		<-g.ctxDone
	}()
	return ch, nil
}

// TestAgentTerminalSurvivesContextCancel pins the emitFinal fix: when
// the turn context dies at the same instant the provider stream cuts
// with no terminal, the loop's synthesized incomplete event must still
// reach a live consumer — the old emit raced ctx.Done() and could drop
// it, leaving the relay with nothing to persist (a real ~30min turn
// once vanished with zero session_events this way).
func TestAgentTerminalSurvivesContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	gw := &cancelThenCutGateway{ctxDone: ctx.Done()}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(ctx, Request{SessionID: "s1", Route: "coding", Messages: []provider.Message{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}

	first := <-ch
	if first.Type != stream.EventChunk {
		t.Fatalf("first event = %+v, want chunk", first)
	}
	cancel()
	evs := collect(t, ch)

	inc := ofType(evs, stream.EventIncomplete)
	if len(inc) != 1 || !strings.Contains(inc[0].Text, "without a terminal event") {
		t.Fatalf("incomplete events = %+v, want exactly one no-terminal incomplete", inc)
	}
}

// TestAgentToolPanicBecomesErrorResult pins the recover in executeOne:
// a panicking tool must come back to the model as an error result
// (D-009), not crash the process — executeOne runs inside an errgroup
// worker, and an unrecovered panic there takes down every active turn
// and mission with it.
func TestAgentToolPanicBecomesErrorResult(t *testing.T) {
	t.Parallel()
	bomb := &tools.Tool{
		Name:        "bomb",
		Description: "panics",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			panic("boom")
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"bomb", `{}`}),
		finalStep("survived"),
	}}
	a, _, _, _ := testAgent(t, gw, bomb)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding", Messages: []provider.Message{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	results := ofType(evs, stream.EventToolResult)
	if len(results) != 1 || results[0].ToolResult.Status != "error" {
		t.Fatalf("tool results = %+v, want one error result", results)
	}
	if !strings.Contains(results[0].ToolResult.Digest, "internal error") {
		t.Fatalf("digest = %q, want panic folded into feedback", results[0].ToolResult.Digest)
	}
	if got := ofType(evs, stream.EventDone); len(got) != 1 {
		t.Fatalf("done events = %d, want the turn to continue and finish", len(got))
	}
}

func TestAgentParallelCallsRunConcurrently(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	running, peak := 0, 0
	slow := &tools.Tool{
		Name:        "slow",
		Description: "sleeps",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"additionalProperties":false}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			mu.Lock()
			running++
			if running > peak {
				peak = running
			}
			mu.Unlock()
			time.Sleep(150 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			return "done", nil
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep(
			[2]string{"slow", `{}`},
			[2]string{"slow", `{}`},
			[2]string{"slow", `{}`},
		),
		finalStep("all done"),
	}}
	a, _, _, _ := testAgent(t, gw, slow)

	start := time.Now()
	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)
	elapsed := time.Since(start)

	if peak < 2 {
		t.Fatalf("peak concurrency = %d, want >= 2", peak)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("3x150ms calls took %s — not concurrent", elapsed)
	}
}

func TestAgentStepCeilingForcesSynthesis(t *testing.T) {
	t.Parallel()
	// The model calls a tool on every step with distinct arguments each
	// time — genuine exploration, not a stuck retry loop — so this
	// isolates the step-count ceiling from the repeat guard, which
	// would otherwise cut the run short on its own.
	var scripts [][]stream.StreamEvent
	for i := range tools.DefaultMaxSteps {
		scripts = append(scripts, toolCallStep([2]string{"echo", fmt.Sprintf(`{"text":"call %d"}`, i)}))
	}
	scripts = append(scripts, finalStep("forced answer"))
	gw := &scriptedGateway{scripts: scripts}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if len(ofType(evs, stream.EventDone)) != 1 {
		t.Fatal("no clean terminal event")
	}
	last := gw.requests[len(gw.requests)-1]
	if len(last.Tools) != 0 {
		t.Fatalf("final step still offered %d tools — schemas must drop at the ceiling", len(last.Tools))
	}
	warn := gw.requests[len(gw.requests)-2]
	found := false
	for _, m := range warn.Messages {
		if strings.Contains(m.Content, "one tool step left") {
			found = true
		}
	}
	if !found {
		t.Fatal("no finalize warning injected at max_steps-1")
	}
}

// TestAgentForcesSynthesisOnRepeatedIdenticalCalls reproduces a live
// loop: a model retrying the exact same tool call (e.g. web_search
// hoping a later attempt "books" something it structurally cannot)
// must be cut off well before the full step ceiling, not burn all
// DefaultMaxSteps steps first.
func TestAgentForcesSynthesisOnRepeatedIdenticalCalls(t *testing.T) {
	t.Parallel()
	var scripts [][]stream.StreamEvent
	for range tools.DefaultMaxSteps {
		scripts = append(scripts, toolCallStep([2]string{"echo", `{"text":"book hotel in Nairobi"}`}))
	}
	scripts = append(scripts, finalStep("gave up retrying"))
	gw := &scriptedGateway{scripts: scripts}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if len(ofType(evs, stream.EventDone)) != 1 {
		t.Fatal("no clean terminal event")
	}
	// 3 identical calls trips the guard; the 4th request must have no
	// tool schemas, forced far short of the 16-step default ceiling.
	if len(gw.requests) >= tools.DefaultMaxSteps {
		t.Fatalf("loop ran %d requests before stopping — repeat guard did not cut it short", len(gw.requests))
	}
	last := gw.requests[len(gw.requests)-1]
	if len(last.Tools) != 0 {
		t.Fatalf("final step still offered %d tools — repeat guard must force synthesis", len(last.Tools))
	}
}

func TestAgentCeilingTerminatesEvenIfModelKeepsCalling(t *testing.T) {
	t.Parallel()
	// A model that emits a tool call on EVERY step, including the
	// forced-synthesis step where schemas are gone. The loop must
	// still stop — never execute past the ceiling, never spin.
	var scripts [][]stream.StreamEvent
	for i := 0; i < tools.DefaultMaxSteps+5; i++ {
		scripts = append(scripts, toolCallStep([2]string{"echo", `{"text":"again"}`}))
	}
	gw := &scriptedGateway{scripts: scripts}
	a, _, events, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if len(ofType(evs, stream.EventDone)) != 1 {
		t.Fatal("loop did not terminate with exactly one done")
	}
	// No execution on the forced-synthesis step: tool_execution
	// events must not exceed the pre-ceiling calls.
	if got := len(events.kinds); got > tools.DefaultMaxSteps {
		t.Fatalf("executed %d tools — ran past the ceiling", got)
	}
	// The gateway must not have been called more than maxSteps times.
	if len(gw.requests) > tools.DefaultMaxSteps {
		t.Fatalf("made %d gateway calls — exceeded the ceiling", len(gw.requests))
	}
}

func TestAgentFlushesUsageOnGatewayError(t *testing.T) {
	t.Parallel()
	withShortRetryBackoff(t)
	// First step succeeds with a tool call and usage; the second
	// Stream call errors synchronously (exhausted script) on every
	// attempt, exhausting the step retry budget. Accumulated usage
	// must still reach the client before the error.
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"echo", `{"text":"hi"}`}),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	usage := ofType(evs, stream.EventUsage)
	if len(usage) != 1 || usage[0].Usage.InputTokens != 10 {
		t.Fatalf("usage before error = %+v, want the first step's 10 input tokens", usage)
	}
	if len(ofType(evs, stream.EventError)) != 1 {
		t.Fatal("want exactly one error terminal")
	}
}

// TestAgentIncompleteThenDoneNotTreatedAsCleanFinish pins D-044: the
// gateway sends incomplete THEN done for a cut-off or zero-output
// stream. Before the fix, terminal was last-write-wins, so done
// clobbered incomplete and the step's abnormal-end guard never fired —
// the turn looked like a clean finish with an empty answer. The loop
// must instead follow the same surface-and-stop path an error step
// takes today: the client sees the incomplete terminal, never a done.
func TestAgentIncompleteThenDoneNotTreatedAsCleanFinish(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		{
			{Type: stream.EventUsage, Usage: &stream.Usage{InputTokens: 10, OutputTokens: 0}},
			{Type: stream.EventIncomplete, Text: "provider produced no output"},
			{Type: stream.EventDone, Meta: &stream.Meta{Provider: "fake", Model: "fake-1"}},
		},
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if len(ofType(evs, stream.EventDone)) != 0 {
		t.Fatalf("events = %+v, want no done event reaching the client", evs)
	}
	incomplete := ofType(evs, stream.EventIncomplete)
	if len(incomplete) != 1 || incomplete[0].Text != "provider produced no output" {
		t.Fatalf("incomplete events = %+v, want exactly one carrying the original text", incomplete)
	}
	if evs[len(evs)-1].Type != stream.EventIncomplete {
		t.Fatalf("last event = %v, want incomplete (the step's abnormal-end terminal)", evs[len(evs)-1].Type)
	}
}

func TestAgentPerToolOffloadThreshold(t *testing.T) {
	t.Parallel()
	// A 100-byte result: default threshold keeps it inline, a 50-byte
	// per-tool override offloads it.
	small := &tools.Tool{
		Name:        "chatty",
		Description: "small output",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			return strings.Repeat("x", 100), nil
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"chatty", `{}`}),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw, small)
	a.SetOffloadThreshold("chatty", 50)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	var content string
	for _, m := range gw.requests[1].Messages {
		if m.ToolResult != nil {
			content = m.ToolResult.Content
		}
	}
	if !strings.Contains(content, "retrieve_output") {
		t.Fatalf("100-byte result not offloaded under a 50-byte override: %q", content)
	}
}

func TestAgentOffloadsBigResults(t *testing.T) {
	t.Parallel()
	big := &tools.Tool{
		Name:        "dump",
		Description: "dumps",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			return strings.Repeat("log line\n", 5000), nil // ~45KB
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"dump", `{}`}),
		finalStep("summarized"),
	}}
	a, _, _, _ := testAgent(t, gw, big)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	second := gw.requests[1]
	var result *provider.ToolResult
	for _, m := range second.Messages {
		if m.ToolResult != nil {
			result = m.ToolResult
		}
	}
	if result == nil {
		t.Fatal("no tool result message")
	}
	if len(result.Content) > tools.DefaultOffloadThreshold {
		t.Fatalf("raw result entered the context: %d bytes", len(result.Content))
	}
	if !strings.Contains(result.Content, "retrieve_output") {
		t.Fatal("digest missing the retrieval ref")
	}
}

// TestAgentRetrieveOutputBetweenThresholdAndCap reproduces a live crash:
// retrieve_output's own result can land between the default offload
// threshold (8KB) and its inline cap (32KB) — bigger than the
// threshold but smaller than what the truncation branch tries to
// slice off. offloadIfBig must not slice past the content's length.
func TestAgentRetrieveOutputBetweenThresholdAndCap(t *testing.T) {
	t.Parallel()
	const size = 13081 // between 8KB and 32KB, the exact failing size seen live
	mid := &tools.Tool{
		Name:        "retrieve_output",
		Description: "stands in for the real tool",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			return strings.Repeat("x", size), nil
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"retrieve_output", `{}`}),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw, mid)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch) // must not panic

	var content string
	for _, m := range gw.requests[1].Messages {
		if m.ToolResult != nil {
			content = m.ToolResult.Content
		}
	}
	if len(content) != size {
		t.Fatalf("content length = %d, want the untouched %d-byte result (no truncation should apply)", len(content), size)
	}
}

// TestAgentRedactsLoadSkillDigest proves the skill pack's rule text
// never reaches the client — not in the live SSE tool_result event,
// not in what gets persisted for later replay. The model still gets
// the real content via the returned tool result.
func TestAgentRedactsLoadSkillDigest(t *testing.T) {
	t.Parallel()
	loadSkill := &tools.Tool{
		Name:        "load_skill",
		Description: "stands in for the real load_skill tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "# Skill: travel-planning\n\nSecret internal rule text.", nil
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"load_skill", `{"name":"travel-planning"}`}),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw, loadSkill)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding", Messages: []provider.Message{{Role: "user", Content: "load the travel skill"}}})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, ch)

	for _, ev := range events {
		if ev.Type == stream.EventToolResult && ev.ToolResult != nil {
			if strings.Contains(ev.ToolResult.Digest, "Secret internal rule text") {
				t.Fatalf("skill body leaked into the live tool_result digest: %q", ev.ToolResult.Digest)
			}
		}
	}

	// The model itself must still see the real content — the redaction
	// is client-facing only.
	second := gw.requests[1]
	var sawRealContent bool
	for _, m := range second.Messages {
		if m.ToolResult != nil && strings.Contains(m.ToolResult.Content, "Secret internal rule text") {
			sawRealContent = true
		}
	}
	if !sawRealContent {
		t.Fatal("model did not receive the real skill body")
	}
}

func TestAgentPermissionDenyBecomesFeedback(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"echo", `{"text":"hi"}`}),
		finalStep("adapted"),
	}}
	a, audit, _, _ := testAgent(t, gw)
	a.perms = denyPerms{}

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	results := ofType(evs, stream.EventToolResult)
	if len(results) != 1 || results[0].ToolResult.Status != "denied" {
		t.Fatalf("tool result = %+v, want denied", results)
	}
	if len(ofType(evs, stream.EventDone)) != 1 {
		t.Fatal("denied call must not end the turn")
	}
	second := gw.requests[1]
	var content string
	for _, m := range second.Messages {
		if m.ToolResult != nil {
			content = m.ToolResult.Content
		}
	}
	if !strings.Contains(content, "denied") {
		t.Fatalf("model feedback = %q", content)
	}
	// D-020: a denial keeps full effort.
	if second.Effort != "" {
		t.Fatalf("effort after denial = %q, want normal", second.Effort)
	}
	if audit.entries[0].Status != "denied" {
		t.Fatalf("audit status = %q", audit.entries[0].Status)
	}
}

type denyPerms struct{}

func (denyPerms) Resolve(_ context.Context, _, tool string, _ json.RawMessage) (tools.Resolution, error) {
	return tools.Resolution{Decision: tools.DecisionDeny, Subject: tool, Rationale: "policy guard: test"}, nil
}
func (denyPerms) Grant(context.Context, string, string, string, time.Duration) error { return nil }

func TestAgentInteractiveApprovalPaths(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, decision string) ([]stream.StreamEvent, *scriptedGateway, *allowAllPerms) {
		gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
			toolCallStep([2]string{"echo", `{"text":"hi"}`}),
			finalStep("after decision"),
		}}
		a, _, _, perms := testAgent(t, gw)
		ask := askPerms{}
		a.perms = &grantRecordingPerms{ask: ask, rec: perms}

		ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
		if err != nil {
			t.Fatal(err)
		}
		// Answer the prompt as soon as it appears.
		var evs []stream.StreamEvent
		deadline := time.After(10 * time.Second)
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return evs, gw, perms
				}
				evs = append(evs, ev)
				if ev.Type == stream.EventPermissionRequest {
					if !a.broker.Resolve(ev.Permission.ID, decision) {
						t.Fatal("broker did not know the prompt id")
					}
				}
			case <-deadline:
				t.Fatal("loop never finished")
			}
		}
	}

	t.Run("allow once executes", func(t *testing.T) {
		t.Parallel()
		evs, _, perms := run(t, DecideOnce)
		results := ofType(evs, stream.EventToolResult)
		if len(results) != 1 || results[0].ToolResult.Status != "ok" {
			t.Fatalf("results = %+v", results)
		}
		if len(perms.granted) != 0 {
			t.Fatalf("once must not record a grant: %v", perms.granted)
		}
	})

	t.Run("allow for session records a grant", func(t *testing.T) {
		t.Parallel()
		evs, _, perms := run(t, DecideSession)
		results := ofType(evs, stream.EventToolResult)
		if len(results) != 1 || results[0].ToolResult.Status != "ok" {
			t.Fatalf("results = %+v", results)
		}
		if len(perms.granted) != 1 {
			t.Fatalf("granted = %v, want one grant", perms.granted)
		}
	})

	t.Run("deny becomes feedback", func(t *testing.T) {
		t.Parallel()
		evs, gw, _ := run(t, DecideDeny)
		results := ofType(evs, stream.EventToolResult)
		if len(results) != 1 || results[0].ToolResult.Status != "denied" {
			t.Fatalf("results = %+v", results)
		}
		var content string
		for _, m := range gw.requests[1].Messages {
			if m.ToolResult != nil {
				content = m.ToolResult.Content
			}
		}
		if !strings.Contains(content, "user denied") {
			t.Fatalf("feedback = %q", content)
		}
	})
}

type askPerms struct{}

func (askPerms) Resolve(_ context.Context, _, tool string, _ json.RawMessage) (tools.Resolution, error) {
	return tools.Resolution{Decision: tools.DecisionAsk, Subject: tool, Rationale: "no standing grant"}, nil
}
func (askPerms) Grant(context.Context, string, string, string, time.Duration) error { return nil }

// grantRecordingPerms asks like askPerms but records grants on the
// shared recorder so tests can assert them.
type grantRecordingPerms struct {
	ask askPerms
	rec *allowAllPerms
}

func (g *grantRecordingPerms) Resolve(ctx context.Context, sid, tool string, args json.RawMessage) (tools.Resolution, error) {
	return g.ask.Resolve(ctx, sid, tool, args)
}
func (g *grantRecordingPerms) Grant(ctx context.Context, sid, tool, pattern string, ttl time.Duration) error {
	return g.rec.Grant(ctx, sid, tool, pattern, ttl)
}

// TestAgentSteeringInjectsMidTurn pins the Steering seam: a note
// returned by Request.Steering must land as a user message on the
// SECOND model call of a turn (never the first, which has nothing to
// steer), and steering must not fire again once the model stops
// calling tools.
func TestAgentSteeringInjectsMidTurn(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"echo", `{"text":"hi"}`}),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw)

	var calls int
	steering := func(context.Context) []string {
		calls++
		return []string{"operator says hurry up"}
	}

	ch, err := a.Start(t.Context(), Request{
		SessionID: "s1", Route: "coding",
		Messages: []provider.Message{{Role: "user", Content: "go"}},
		Steering: steering,
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	if len(gw.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(gw.requests))
	}
	if calls != 1 {
		t.Fatalf("steering called %d times, want exactly 1 (never on step 1)", calls)
	}
	var sawFirst bool
	for _, m := range gw.requests[0].Messages {
		if strings.Contains(m.Content, "operator says hurry up") {
			sawFirst = true
		}
	}
	if sawFirst {
		t.Fatal("steering note leaked into the first model call")
	}
	var sawSecond bool
	for _, m := range gw.requests[1].Messages {
		if m.Role == "user" && strings.Contains(m.Content, "operator says hurry up") {
			sawSecond = true
		}
	}
	if !sawSecond {
		t.Fatalf("steering note missing from second call: %+v", gw.requests[1].Messages)
	}
}

// TestAgentNilSteeringIsNoop pins that a nil Steering (every existing
// caller) never changes behavior: the loop must not even attempt to
// call it.
func TestAgentNilSteeringIsNoop(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"echo", `{"text":"hi"}`}),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding", Messages: []provider.Message{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	if len(ofType(evs, stream.EventDone)) != 1 {
		t.Fatal("must still finish cleanly with nil Steering")
	}
}

func TestAgentResearchCoercion(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		finalStep("answer with no retrieval"),
		toolCallStep([2]string{"echo", `{"text":"looked it up"}`}),
		finalStep("grounded answer"),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "research"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if len(gw.requests) != 3 {
		t.Fatalf("requests = %d, want 3 (answer, coerced retry, final)", len(gw.requests))
	}
	var coerced bool
	for _, m := range gw.requests[1].Messages {
		if strings.Contains(m.Content, "requires consulting your tools") {
			coerced = true
		}
	}
	if !coerced {
		t.Fatal("no coercion message injected")
	}
	if len(ofType(evs, stream.EventDone)) != 1 {
		t.Fatal("must still end with exactly one done")
	}
}

func TestSwapToolsChangesSurfaceForNewTurns(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"github_create_issue", `{"title":"bug"}`}),
		finalStep("filed"),
	}}
	a, _, _, _ := testAgent(t, gw)

	// Swap in a connector tool alongside the builtin echo.
	reg := tools.NewRegistry()
	connector := &tools.Tool{
		Name:        "github_create_issue",
		Description: "Create a GitHub issue",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "issue #7", nil
		},
	}
	if err := reg.Register(connector); err != nil {
		t.Fatal(err)
	}
	c, err := tools.NewConstrained(reg, testToolCalls())
	if err != nil {
		t.Fatal(err)
	}
	a.SwapTools(registryExec{c}, []provider.ToolDef{
		{Name: connector.Name, Description: connector.Description, InputSchema: connector.InputSchema},
	})

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding",
		Messages: []provider.Message{{Role: "user", Content: "file it"}}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	results := ofType(evs, stream.EventToolResult)
	if len(results) != 1 || results[0].ToolResult.Status != "ok" || results[0].ToolResult.Digest != "issue #7" {
		t.Fatalf("tool result = %+v, want ok issue #7", results)
	}
	// The gateway saw the swapped defs, not the original echo set.
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if len(gw.requests) == 0 || len(gw.requests[0].Tools) != 1 || gw.requests[0].Tools[0].Name != "github_create_issue" {
		t.Fatalf("first request tools = %+v, want only github_create_issue", gw.requests[0].Tools)
	}

	// Tools() reflects the same live swap — the surface a tools-
	// allowlist picker would list.
	got := a.Tools()
	if len(got) != 1 || got[0].Name != "github_create_issue" || got[0].Description != "Create a GitHub issue" {
		t.Fatalf("Tools() = %+v, want only github_create_issue", got)
	}
}

// TestRequestBuiltinsOnlySeesBaseToolsNotConnectorTools confirms a
// mission-driven turn (Request.BuiltinsOnly) resolves to the
// SetBaseTools snapshot instead of the shared registry SwapTools
// maintains — a connector tool (or a chat-only mission tool) present
// in the shared registry must never be offered to a builtins-only
// turn, closing the side-channel a mission worker could otherwise use
// to bypass the worktree/human-consented push pipeline.
func TestRequestBuiltinsOnlySeesBaseToolsNotConnectorTools(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw)

	// Base tools: just "echo" (testAgent's default).
	baseReg := tools.NewRegistry()
	if err := baseReg.Register(&tools.Tool{
		Name: "echo", Description: "echoes",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute:     func(context.Context, json.RawMessage) (string, error) { return "", nil },
	}); err != nil {
		t.Fatal(err)
	}
	baseConstrained, err := tools.NewConstrained(baseReg, testToolCalls())
	if err != nil {
		t.Fatal(err)
	}
	a.SetBaseTools(registryExec{baseConstrained}, []provider.ToolDef{{Name: "echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`)}})

	// Full/shared registry: adds a connector tool on top via SwapTools.
	fullReg := tools.NewRegistry()
	for _, tool := range []*tools.Tool{
		{Name: "echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`),
			Execute: func(context.Context, json.RawMessage) (string, error) { return "", nil }},
		{Name: "github_create_issue", Description: "creates an issue", InputSchema: json.RawMessage(`{"type":"object"}`),
			Execute: func(context.Context, json.RawMessage) (string, error) { return "", nil }},
	} {
		if err := fullReg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	fullConstrained, err := tools.NewConstrained(fullReg, testToolCalls())
	if err != nil {
		t.Fatal(err)
	}
	a.SwapTools(registryExec{fullConstrained}, []provider.ToolDef{
		{Name: "echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "github_create_issue", Description: "creates an issue", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})

	ch, err := a.Start(t.Context(), Request{
		SessionID: "s1", Route: "default", BuiltinsOnly: true,
		Messages: []provider.Message{{Role: "user", Content: "go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	for _, d := range gw.requests[0].Tools {
		if d.Name == "github_create_issue" {
			t.Fatalf("connector tool leaked into a BuiltinsOnly request's tool defs: %+v", gw.requests[0].Tools)
		}
	}
	if len(gw.requests[0].Tools) != 1 || gw.requests[0].Tools[0].Name != "echo" {
		t.Fatalf("BuiltinsOnly request tools = %+v, want only echo", gw.requests[0].Tools)
	}
}

// TestRequestWithoutBuiltinsOnlySeesFullSurfaceAfterSwap is
// TestSwapToolsChangesSurfaceForNewTurns's counterpart: a plain
// (unflagged, chat-shaped) turn keeps seeing the full shared registry
// — including connector tools — across a SwapTools/reload cycle, even
// after SetBaseTools has been called for the mission-only surface.
func TestRequestWithoutBuiltinsOnlySeesFullSurfaceAfterSwap(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw)

	baseReg := tools.NewRegistry()
	if err := baseReg.Register(&tools.Tool{
		Name: "echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (string, error) { return "", nil },
	}); err != nil {
		t.Fatal(err)
	}
	baseConstrained, err := tools.NewConstrained(baseReg, testToolCalls())
	if err != nil {
		t.Fatal(err)
	}
	a.SetBaseTools(registryExec{baseConstrained}, []provider.ToolDef{{Name: "echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`)}})

	fullReg := tools.NewRegistry()
	for _, tool := range []*tools.Tool{
		{Name: "echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`),
			Execute: func(context.Context, json.RawMessage) (string, error) { return "", nil }},
		{Name: "github_create_issue", Description: "creates an issue", InputSchema: json.RawMessage(`{"type":"object"}`),
			Execute: func(context.Context, json.RawMessage) (string, error) { return "", nil }},
	} {
		if err := fullReg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	fullConstrained, err := tools.NewConstrained(fullReg, testToolCalls())
	if err != nil {
		t.Fatal(err)
	}
	a.SwapTools(registryExec{fullConstrained}, []provider.ToolDef{
		{Name: "echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "github_create_issue", Description: "creates an issue", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})

	ch, err := a.Start(t.Context(), Request{
		SessionID: "s1", Route: "default",
		Messages: []provider.Message{{Role: "user", Content: "go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	var sawConnector bool
	for _, d := range gw.requests[0].Tools {
		if d.Name == "github_create_issue" {
			sawConnector = true
		}
	}
	if !sawConnector {
		t.Fatalf("connector tool missing from an unflagged request after SwapTools: %+v", gw.requests[0].Tools)
	}
}

// TestForceRouteSwitchesRouteAfterMatchingToolAndStaysSticky pins the
// sensitive-tool-output routing mechanism: a tool whose name ends with
// a registered suffix (connector tools are namespaced
// "<connector-name>_gmail_read", the connector name is user-chosen)
// pins every LATER step in the turn to the forced route, while the
// FIRST step (before the tool ran) still uses the turn's own route.
// It also confirms the forced route survives a SECOND matching tool
// call (stays sticky, doesn't reset).
func TestForceRouteSwitchesRouteAfterMatchingToolAndStaysSticky(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"personal_gmail_read", `{}`}),
		toolCallStep([2]string{"personal_gmail_read", `{}`}),
		finalStep("done"),
	}}
	sensitive := &tools.Tool{
		Name:        "personal_gmail_read",
		Description: "reads a fake email",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "email body", nil
		},
	}
	a, _, _, _ := testAgent(t, gw, sensitive)
	a.SetForceRoute("gmail_read", func(context.Context) string { return "local" })

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "default",
		Messages: []provider.Message{{Role: "user", Content: "check my email"}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	if len(gw.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(gw.requests))
	}
	if gw.requests[0].Route != "default" {
		t.Fatalf("step 1 route = %q, want default (before the sensitive tool ran)", gw.requests[0].Route)
	}
	if gw.requests[1].Route != "local" {
		t.Fatalf("step 2 route = %q, want local (forced after gmail_read ran)", gw.requests[1].Route)
	}
	if gw.requests[2].Route != "local" {
		t.Fatalf("step 3 route = %q, want local (sticky through a second matching call)", gw.requests[2].Route)
	}
}

// TestForceRouteIgnoresNonMatchingTools confirms an unrelated tool
// call never triggers the override — only a name ending in a
// registered suffix does.
func TestForceRouteIgnoresNonMatchingTools(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"echo", `{"text":"hi"}`}),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw)
	a.SetForceRoute("gmail_read", func(context.Context) string { return "local" })

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "default",
		Messages: []provider.Message{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	for i, req := range gw.requests {
		if req.Route != "default" {
			t.Fatalf("request %d route = %q, want default (echo never matches gmail_read)", i, req.Route)
		}
	}
}

// TestForceRouteByConnectorSwitchesRouteAfterMatchingTool pins the
// connector-level sensitivity counterpart to SetForceRoute: a whole
// connector flagged sensitive (its name is a PREFIX of every tool it
// serves, "<connector-name>_<tool-name>") pins the SAME turn that
// calls one of its tools, not just every turn after — the search/read
// tool's own results must never ride the turn's original route.
func TestForceRouteByConnectorSwitchesRouteAfterMatchingTool(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"gmail_search", `{}`}),
		finalStep("done"),
	}}
	search := &tools.Tool{
		Name:        "gmail_search",
		Description: "searches fake email",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "search results", nil
		},
	}
	a, _, _, _ := testAgent(t, gw, search)
	a.SetForceRouteByConnector(
		func(context.Context) []string { return []string{"gmail"} },
		func(context.Context) string { return "local" },
		nil,
	)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "default",
		Messages: []provider.Message{{Role: "user", Content: "search my email"}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	if len(gw.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(gw.requests))
	}
	if gw.requests[0].Route != "default" {
		t.Fatalf("step 1 route = %q, want default (before the sensitive connector's tool ran)", gw.requests[0].Route)
	}
	if gw.requests[1].Route != "local" {
		t.Fatalf("step 2 route = %q, want local (forced after gmail_search ran)", gw.requests[1].Route)
	}
}

// TestForceRouteByConnectorIgnoresUnlistedConnector confirms a tool
// from a connector NOT in the sensitive names list never triggers the
// override, and that a bare prefix match doesn't false-positive: a
// connector named "gmail" must not match a tool actually named
// "gmailbox_search" (no "_" boundary).
func TestForceRouteByConnectorIgnoresUnlistedConnector(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"gmailbox_search", `{}`}),
		finalStep("done"),
	}}
	tool := &tools.Tool{
		Name:        "gmailbox_search",
		Description: "unrelated tool that happens to start with 'gmail'",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "ok", nil
		},
	}
	a, _, _, _ := testAgent(t, gw, tool)
	a.SetForceRouteByConnector(
		func(context.Context) []string { return []string{"gmail"} },
		func(context.Context) string { return "local" },
		nil,
	)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "default",
		Messages: []provider.Message{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	for i, req := range gw.requests {
		if req.Route != "default" {
			t.Fatalf("request %d route = %q, want default (gmailbox_search must not prefix-match \"gmail\")", i, req.Route)
		}
	}
}

// TestForceRouteByConnectorMatchesUnifiedToolViaAccountConnector pins
// the unified-tool path: a call to a name carrying no connector prefix
// (e.g. mail_search) still pins the route once accountConnector
// resolves the call to a name in the sensitive list: the account the
// call actually routed to, not the tool's own name, decides the flip.
func TestForceRouteByConnectorMatchesUnifiedToolViaAccountConnector(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"mail_search", `{"account":"work-outlook"}`}),
		finalStep("done"),
	}}
	search := &tools.Tool{
		Name:        "mail_search",
		Description: "searches connected mail",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"account":{"type":"string"}},"additionalProperties":false}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "search results", nil
		},
	}
	a, _, _, _ := testAgent(t, gw, search)
	a.SetForceRouteByConnector(
		func(context.Context) []string { return []string{"work-outlook"} },
		func(context.Context) string { return "local" },
		func(_ context.Context, toolName string, args json.RawMessage) string {
			if toolName == "mail_search" && strings.Contains(string(args), "work-outlook") {
				return "work-outlook"
			}
			return ""
		},
	)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "default",
		Messages: []provider.Message{{Role: "user", Content: "search my email"}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	if len(gw.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(gw.requests))
	}
	if gw.requests[0].Route != "default" {
		t.Fatalf("step 1 route = %q, want default (before the sensitive account's tool ran)", gw.requests[0].Route)
	}
	if gw.requests[1].Route != "local" {
		t.Fatalf("step 2 route = %q, want local (forced after mail_search resolved to work-outlook)", gw.requests[1].Route)
	}
}

// TestForceRouteDropsModelHint closes the gateway-hint privacy hole: a
// ModelHint outranks Route in the gateway's Resolve order, so a hint
// surviving the flip would let a turn escape the forced route entirely
// after a sensitive tool ran. The hint must be dropped the moment the
// route is forced, not just the route switched.
func TestForceRouteDropsModelHint(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"personal_gmail_read", `{}`}),
		finalStep("done"),
	}}
	sensitive := &tools.Tool{
		Name:        "personal_gmail_read",
		Description: "reads a fake email",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "email body", nil
		},
	}
	a, _, _, _ := testAgent(t, gw, sensitive)
	a.SetForceRoute("gmail_read", func(context.Context) string { return "local" })

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "default", ModelHint: "glm-4.7-flash",
		Messages: []provider.Message{{Role: "user", Content: "check my email"}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	if len(gw.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(gw.requests))
	}
	if gw.requests[0].ModelHint != "glm-4.7-flash" {
		t.Fatalf("step 1 hint = %q, want glm-4.7-flash (before the sensitive tool ran)", gw.requests[0].ModelHint)
	}
	if gw.requests[1].Route != "local" {
		t.Fatalf("step 2 route = %q, want local (forced after gmail_read ran)", gw.requests[1].Route)
	}
	if gw.requests[1].ModelHint != "" {
		t.Fatalf("step 2 hint = %q, want empty — a surviving hint bypasses the forced route at the gateway", gw.requests[1].ModelHint)
	}
}

// TestForceRoutePinSurvivesStepRetry confirms the forced route and
// dropped hint hold even when a step's stream dies and is retried
// (D-038): the retry re-sends the same sreq built after the flip, so
// it must never revert to the turn's original route or hint.
func TestForceRoutePinSurvivesStepRetry(t *testing.T) {
	withShortRetryBackoff(t)
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"personal_gmail_read", `{}`}),
		retryableErrorStep(),
		finalStep("done"),
	}}
	sensitive := &tools.Tool{
		Name:        "personal_gmail_read",
		Description: "reads a fake email",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "email body", nil
		},
	}
	a, _, _, _ := testAgent(t, gw, sensitive)
	a.SetForceRoute("gmail_read", func(context.Context) string { return "local" })

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "default", ModelHint: "glm-4.7-flash",
		Messages: []provider.Message{{Role: "user", Content: "check my email"}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	// step 1 (unforced), step 2 attempt 1 (retryable error), step 2
	// attempt 2 (the retry) — three gw.Stream calls.
	if len(gw.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(gw.requests))
	}
	retried := gw.requests[2]
	if retried.Route != "local" {
		t.Fatalf("retried request route = %q, want local (pin must survive the step retry)", retried.Route)
	}
	if retried.ModelHint != "" {
		t.Fatalf("retried request hint = %q, want empty (dropped hint must survive the step retry)", retried.ModelHint)
	}
}

// TestRequestExtraToolsCallableAndExecutable reproduces the missions
// bug: a sentinel-style tool defined outside the shared agent registry
// must still be (a) offered to the model as callable and (b)
// executable, via Request.ExtraTools — without ever touching the
// shared base registry other turns read.
func TestRequestExtraToolsCallableAndExecutable(t *testing.T) {
	t.Parallel()
	var gotArgs json.RawMessage
	sentinel := &tools.Tool{
		Name:        "mission_status",
		Description: "reports turn outcome",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"outcome":{"type":"string"}},"required":["outcome"]}`),
		Execute: func(_ context.Context, args json.RawMessage) (string, error) {
			gotArgs = args
			return "status recorded", nil
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"mission_status", `{"outcome":"done"}`}),
		finalStep("finished"),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{
		SessionID:  "s1",
		Route:      "default",
		Messages:   []provider.Message{{Role: "user", Content: "go"}},
		ExtraTools: []*tools.Tool{sentinel},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if got := ofType(evs, stream.EventToolResult); len(got) != 1 || got[0].ToolResult.Status != "ok" {
		t.Fatalf("tool results = %+v, want one ok result for mission_status", got)
	}
	if string(gotArgs) != `{"outcome":"done"}` {
		t.Fatalf("sentinel Execute args = %s, want the model's call forwarded through", gotArgs)
	}

	// The first request sent to the gateway must have listed
	// mission_status as callable — this is the exact gap that let the
	// model silently never call it.
	var sawDef bool
	for _, d := range gw.requests[0].Tools {
		if d.Name == "mission_status" {
			sawDef = true
		}
	}
	if !sawDef {
		t.Fatalf("mission_status not offered in first request's tool defs: %+v", gw.requests[0].Tools)
	}
}

// TestRequestExtraToolsNotVisibleWithoutOptIn confirms a turn that
// doesn't set ExtraTools never sees another turn's extra tool — the
// isolation the missions package relies on to keep mission_status out
// of chat sessions.
func TestRequestExtraToolsNotVisibleWithoutOptIn(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		finalStep("done, no tools needed"),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "default",
		Messages: []provider.Message{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	for _, d := range gw.requests[0].Tools {
		if d.Name == "mission_status" {
			t.Fatalf("mission_status leaked into a plain request's tool defs: %+v", gw.requests[0].Tools)
		}
	}
}

// TestRequestExtraToolsOverrideBaseTool: an extra tool sharing a base
// tool's name must REPLACE it for the turn — one def offered to the
// model, and execution resolving to the turn-scoped implementation.
// This is how a mission's workspace-rooted shell shadows the global
// shell without touching the shared registry.
func TestRequestExtraToolsOverrideBaseTool(t *testing.T) {
	t.Parallel()
	executed := false
	override := &tools.Tool{
		Name:        "echo",
		Description: "turn-scoped echo",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			executed = true
			return "override ran", nil
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"echo", `{"text":"hi"}`}),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{
		SessionID:  "s1",
		Route:      "default",
		Messages:   []provider.Message{{Role: "user", Content: "go"}},
		ExtraTools: []*tools.Tool{override},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	count := 0
	for _, d := range gw.requests[0].Tools {
		if d.Name == "echo" {
			count++
			if d.Description != "turn-scoped echo" {
				t.Fatalf("echo def = %q, want the override's def, not the base one", d.Description)
			}
		}
	}
	if count != 1 {
		t.Fatalf("model saw %d echo defs, want exactly 1", count)
	}
	if !executed {
		t.Fatal("base echo executed instead of the turn-scoped override")
	}
}

// countingAskPerms behaves like askPerms (always DecisionAsk) but
// counts Resolve calls — tests use it to assert the permission chain
// was never reached at all (the unknown-tool precheck short-circuits
// before it).
type countingAskPerms struct {
	resolveCalls int
}

func (p *countingAskPerms) Resolve(_ context.Context, _, tool string, _ json.RawMessage) (tools.Resolution, error) {
	p.resolveCalls++
	return tools.Resolution{Decision: tools.DecisionAsk, Subject: tool, Rationale: "no standing grant"}, nil
}
func (p *countingAskPerms) Grant(context.Context, string, string, string, time.Duration) error { return nil }

// TestAgentUnknownToolRejectedBeforePermissionChain is D-039's first
// failure mode: a hallucinated tool name must never reach the
// permission chain (and so never park on askUser) — it's rejected with
// the same feedback tools.Constrained.Execute gives, just earlier.
func TestAgentUnknownToolRejectedBeforePermissionChain(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"does_not_exist", `{}`}),
		finalStep("adapted"),
	}}
	a, _, _, _ := testAgent(t, gw)
	perms := &countingAskPerms{}
	a.perms = perms

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	results := ofType(evs, stream.EventToolResult)
	if len(results) != 1 || results[0].ToolResult.Status != "error" {
		t.Fatalf("tool result = %+v, want one error result", results)
	}
	if !strings.Contains(results[0].ToolResult.Digest, `unknown tool "does_not_exist"`) {
		t.Fatalf("digest = %q, want the unknown-tool feedback", results[0].ToolResult.Digest)
	}
	if len(ofType(evs, stream.EventPermissionRequest)) != 0 {
		t.Fatal("unknown tool must never trigger a permission request")
	}
	if perms.resolveCalls != 0 {
		t.Fatalf("perms.Resolve called %d times, want 0 — unknown tool must short-circuit before it", perms.resolveCalls)
	}
}

// TestFilterDefs pins filterDefs' suffix-matching against an agent's
// ToolAllow list (D-036): connector tools register namespaced as
// "<connector-name>_<tool-name>" (connectors.Manager.Tools), so an
// allowlist entry authored before any connector name is known must
// still match at offer time. Mirrors tools.ToolMatches' semantics and
// its existing test expectations exactly — the grants layer and this
// layer must never disagree about what a config name refers to.
func TestFilterDefs(t *testing.T) {
	t.Parallel()
	defOf := func(name string) provider.ToolDef { return provider.ToolDef{Name: name} }

	tests := []struct {
		name  string
		defs  []string
		allow []string
		want  []string
	}{
		{
			name:  "empty allow keeps everything",
			defs:  []string{"web_search", "shell"},
			allow: nil,
			want:  []string{"web_search", "shell"},
		},
		{
			name:  "exact name still matches",
			defs:  []string{"shell"},
			allow: []string{"shell"},
			want:  []string{"shell"},
		},
		{
			name:  "gmail_search allows connector-namespaced gmail_gmail_search",
			defs:  []string{"gmail_gmail_search", "shell"},
			allow: []string{"gmail_search"},
			want:  []string{"gmail_gmail_search"},
		},
		{
			name:  "calendar_list_events allows google-calendar_calendar_list_events",
			defs:  []string{"google-calendar_calendar_list_events"},
			allow: []string{"calendar_list_events"},
			want:  []string{"google-calendar_calendar_list_events"},
		},
		{
			name:  "search matches web_search under the same suffix rule as matchGrant",
			defs:  []string{"web_search"},
			allow: []string{"search"},
			want:  []string{"web_search"},
		},
		{
			name:  "no underscore boundary rejected",
			defs:  []string{"notcalendar_list_events"},
			allow: []string{"calendar_list_events"},
			want:  nil,
		},
		{
			name:  "sandbox sentinel never suffix-matches",
			defs:  []string{"foo___sandbox__"},
			allow: []string{tools.SandboxGrantTool},
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var defs []provider.ToolDef
			for _, n := range tc.defs {
				defs = append(defs, defOf(n))
			}
			got := filterDefs(defs, tc.allow)
			var gotNames []string
			for _, d := range got {
				gotNames = append(gotNames, d.Name)
			}
			if len(gotNames) != len(tc.want) {
				t.Fatalf("filterDefs(%v, %v) = %v, want %v", tc.defs, tc.allow, gotNames, tc.want)
			}
			for i, n := range gotNames {
				if n != tc.want[i] {
					t.Fatalf("filterDefs(%v, %v) = %v, want %v", tc.defs, tc.allow, gotNames, tc.want)
				}
			}
		})
	}
}

// TestAgentToolAllowExcludedToolRejected pins the wiring between
// filterDefs and toolNames (run, agent.go ~line 214/241): toolNames
// must be built from the POST-ToolAllow-filter defs, not the raw
// agent tool surface. If a regression built toolNames from the
// unfiltered defs instead, a tool excluded by Request.ToolAllow would
// still resolve as "known" here and fall through to the permission
// chain — silently reopening the allowlist bypass that filterDefs
// exists to close, since resolveAndRun's unknown-tool precheck would
// no longer catch it.
func TestAgentToolAllowExcludedToolRejected(t *testing.T) {
	t.Parallel()
	var blockedRan bool
	allowed := &tools.Tool{
		Name:        "echo_allowed",
		Description: "allowed echo",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "allowed ran", nil
		},
	}
	blocked := &tools.Tool{
		Name:        "echo_blocked",
		Description: "blocked echo",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			blockedRan = true
			return "blocked ran", nil
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"echo_blocked", `{}`}),
		finalStep("adapted"),
	}}
	a, _, _, _ := testAgent(t, gw, allowed, blocked)
	perms := &countingAskPerms{}
	a.perms = perms

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding", ToolAllow: []string{"echo_allowed"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	results := ofType(evs, stream.EventToolResult)
	if len(results) != 1 || results[0].ToolResult.Status != "error" {
		t.Fatalf("tool result = %+v, want one error result", results)
	}
	if !strings.Contains(results[0].ToolResult.Digest, `unknown tool "echo_blocked"`) {
		t.Fatalf("digest = %q, want the unknown-tool feedback", results[0].ToolResult.Digest)
	}
	if blockedRan {
		t.Fatal("echo_blocked executed despite being excluded by ToolAllow")
	}
	if len(ofType(evs, stream.EventPermissionRequest)) != 0 {
		t.Fatal("tool excluded by ToolAllow must never trigger a permission request")
	}
	if perms.resolveCalls != 0 {
		t.Fatalf("perms.Resolve called %d times, want 0 — ToolAllow-excluded tool must short-circuit before it", perms.resolveCalls)
	}
}

// TestAgentForceToolCopiedToStreamRequest pins D-063: Request.ForceTool
// rides onto every outgoing gwclient.StreamRequest unchanged when the
// named tool is offered.
func TestAgentForceToolCopiedToStreamRequest(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{finalStep("done")}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding", ForceTool: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	if len(gw.requests) != 1 || gw.requests[0].ForceTool != "echo" {
		t.Fatalf("requests = %+v, want ForceTool=echo on the single request", gw.requests)
	}
}

// TestAgentForceToolClearedUnderForceSynthesis pins D-063: once the
// loop forces synthesis (tools.StepForceSynthesis drops Tools, no
// schemas offered), ForceTool must clear alongside — forcing a call
// with no tools offered is a wire error. Three identical echo calls in
// a row trip RepeatGuard's "stuck" path, which forces synthesis on the
// very next step without needing to hit the real step ceiling.
func TestAgentForceToolClearedUnderForceSynthesis(t *testing.T) {
	t.Parallel()
	repeat := toolCallStep([2]string{"echo", `{"text":"x"}`})
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		repeat, repeat, repeat, finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding", ForceTool: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	if len(gw.requests) != 4 {
		t.Fatalf("requests = %d, want 4 (3 repeats + forced synthesis)", len(gw.requests))
	}
	last := gw.requests[3]
	if last.ForceTool != "" {
		t.Fatalf("last request ForceTool = %q, want cleared under forced synthesis", last.ForceTool)
	}
	if len(last.Tools) != 0 {
		t.Fatalf("last request Tools = %+v, want none under forced synthesis", last.Tools)
	}
}

// TestAgentForceToolClearedWhenNotOffered pins D-063's defensive
// clause: forcing a tool absent from the turn's own filtered surface
// (ToolAllow excludes it here) is a wire error, so the loop drops it
// rather than send an invalid tool_choice.
func TestAgentForceToolClearedWhenNotOffered(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{finalStep("done")}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{
		SessionID: "s1", Route: "coding",
		ToolAllow: []string{"echo"},
		ForceTool: "not_offered",
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	if len(gw.requests) != 1 || gw.requests[0].ForceTool != "" {
		t.Fatalf("requests = %+v, want ForceTool cleared for an unoffered tool", gw.requests)
	}
}

// TestAgentUnattendedAskDeniesImmediately is D-039's second failure
// mode: an unattended (schedule-fired) turn hitting DecisionAsk must
// deny immediately with feedback naming the rationale, never call
// askUser (so no EventPermissionRequest, no 10-minute wait). The test
// itself finishing well under that timeout is part of the assertion.
func TestAgentUnattendedAskDeniesImmediately(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"echo", `{"text":"hi"}`}),
		finalStep("adapted"),
	}}
	a, _, _, _ := testAgent(t, gw)
	a.perms = askPerms{}

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding", Unattended: true})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	results := ofType(evs, stream.EventToolResult)
	if len(results) != 1 || results[0].ToolResult.Status != "denied" {
		t.Fatalf("tool result = %+v, want denied", results)
	}
	if len(ofType(evs, stream.EventPermissionRequest)) != 0 {
		t.Fatal("unattended turn must never emit a permission request")
	}
	var content string
	for _, m := range gw.requests[1].Messages {
		if m.ToolResult != nil {
			content = m.ToolResult.Content
		}
	}
	if !strings.Contains(content, "no standing grant") {
		t.Fatalf("feedback = %q, want it to contain the resolution's rationale", content)
	}
	if !strings.Contains(content, "unattended mission") {
		t.Fatalf("feedback = %q, want it to explain no human is available", content)
	}
}

// sessionGrantPerms mimics the real Permissions chain's behavior for
// the single-flight ask gate tests: Resolve returns Ask for a tool
// until a Grant has been recorded for it, after which it returns
// Allow — matching how a real session grant, once written, makes a
// later Resolve on the same (session, tool, subject) succeed without
// asking again.
type sessionGrantPerms struct {
	mu      sync.Mutex
	granted map[string]bool
}

func (p *sessionGrantPerms) Resolve(_ context.Context, sessionID, tool string, _ json.RawMessage) (tools.Resolution, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.granted[sessionID+"\x00"+tool] {
		return tools.Resolution{Decision: tools.DecisionAllow, Subject: tool}, nil
	}
	return tools.Resolution{Decision: tools.DecisionAsk, Subject: tool, Rationale: "no standing grant"}, nil
}

func (p *sessionGrantPerms) Grant(_ context.Context, sessionID, tool, _ string, _ time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.granted == nil {
		p.granted = map[string]bool{}
	}
	p.granted[sessionID+"\x00"+tool] = true
	return nil
}

// TestParallelSameToolAsksOnce reproduces the race this change fixes:
// a step firing 3 parallel calls to the same tool with the same
// arguments (same subject) must produce exactly one permission
// prompt. Answering that prompt with "session" records a grant that
// the other two calls — parked behind the gate — pick up on
// re-resolve, so they never prompt at all.
func TestParallelSameToolAsksOnce(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep(
			[2]string{"echo", `{"text":"hi"}`},
			[2]string{"echo", `{"text":"hi"}`},
			[2]string{"echo", `{"text":"hi"}`},
		),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw)
	a.perms = &sessionGrantPerms{}

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}

	var evs []stream.StreamEvent
	var prompts int
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				results := ofType(evs, stream.EventToolResult)
				if len(results) != 3 {
					t.Fatalf("tool results = %d, want 3", len(results))
				}
				for _, r := range results {
					if r.ToolResult.Status != "ok" {
						t.Fatalf("result = %+v, want ok", r.ToolResult)
					}
				}
				if prompts != 1 {
					t.Fatalf("permission prompts = %d, want exactly 1", prompts)
				}
				return
			}
			evs = append(evs, ev)
			if ev.Type == stream.EventPermissionRequest {
				prompts++
				if !a.broker.Resolve(ev.Permission.ID, DecideSession) {
					t.Fatal("broker did not know the prompt id")
				}
			}
		case <-deadline:
			t.Fatal("loop never finished")
		}
	}
}

// TestParallelSameToolOnceAnswerReAsks confirms "once" (not "session")
// answers do NOT suppress a sibling call's prompt: with only 2
// parallel same-tool calls, answering the first prompt with
// DecideOnce records no grant, so the second call — released from the
// gate after the first's execution — must still hit its own prompt.
func TestParallelSameToolOnceAnswerReAsks(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep(
			[2]string{"echo", `{"text":"hi"}`},
			[2]string{"echo", `{"text":"hi"}`},
		),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw)
	a.perms = &sessionGrantPerms{}

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}

	var evs []stream.StreamEvent
	var prompts int
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				results := ofType(evs, stream.EventToolResult)
				if len(results) != 2 {
					t.Fatalf("tool results = %d, want 2", len(results))
				}
				for _, r := range results {
					if r.ToolResult.Status != "ok" {
						t.Fatalf("result = %+v, want ok", r.ToolResult)
					}
				}
				if prompts != 2 {
					t.Fatalf("permission prompts = %d, want exactly 2 (once must not suppress the sibling's ask)", prompts)
				}
				return
			}
			evs = append(evs, ev)
			if ev.Type == stream.EventPermissionRequest {
				prompts++
				if !a.broker.Resolve(ev.Permission.ID, DecideOnce) {
					t.Fatal("broker did not know the prompt id")
				}
			}
		case <-deadline:
			t.Fatal("loop never finished")
		}
	}
}

// TestAgentAttendedAskStillParks is the control for the above: with
// Unattended left false (the default), DecisionAsk must still emit
// EventPermissionRequest and be answerable through the broker exactly
// as before D-039.
func TestAgentAttendedAskStillParks(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"echo", `{"text":"hi"}`}),
		finalStep("after decision"),
	}}
	a, _, _, _ := testAgent(t, gw)
	a.perms = askPerms{}

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	var evs []stream.StreamEvent
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				results := ofType(evs, stream.EventToolResult)
				if len(results) != 1 || results[0].ToolResult.Status != "ok" {
					t.Fatalf("results = %+v, want ok after DecideOnce", results)
				}
				return
			}
			evs = append(evs, ev)
			if ev.Type == stream.EventPermissionRequest {
				if !a.broker.Resolve(ev.Permission.ID, DecideOnce) {
					t.Fatal("broker did not know the prompt id")
				}
			}
		case <-deadline:
			t.Fatal("loop never finished")
		}
	}
}

// TestAskUserEmitsResolvedOnAnswer confirms askUser follows its
// permission_request with a permission_resolved event carrying the
// same ID and the decision the broker delivered — the persistence path
// (chat's relay) and any replay client learn the outcome from the
// stream itself, not just from the tool_result that happens to follow.
func TestAskUserEmitsResolvedOnAnswer(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"echo", `{"text":"hi"}`}),
		finalStep("after decision"),
	}}
	a, _, _, _ := testAgent(t, gw)
	a.perms = askPerms{}

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	var evs []stream.StreamEvent
	var requestID string
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				resolved := ofType(evs, stream.EventPermissionResolved)
				if len(resolved) != 1 {
					t.Fatalf("permission_resolved count = %d, want 1: %+v", len(resolved), resolved)
				}
				if resolved[0].Resolved.ID != requestID {
					t.Fatalf("resolved id = %q, want %q", resolved[0].Resolved.ID, requestID)
				}
				if resolved[0].Resolved.Decision != DecideOnce {
					t.Fatalf("resolved decision = %q, want %q", resolved[0].Resolved.Decision, DecideOnce)
				}
				return
			}
			evs = append(evs, ev)
			if ev.Type == stream.EventPermissionRequest {
				requestID = ev.Permission.ID
				if !a.broker.Resolve(ev.Permission.ID, DecideOnce) {
					t.Fatal("broker did not know the prompt id")
				}
			}
		case <-deadline:
			t.Fatal("loop never finished")
		}
	}
}

// TestWaitToolsReadyNilIsNoOp confirms the default (before main.go
// wires SetWaitToolsReady) behaves exactly as before D-043 — no hook,
// no wait.
func TestWaitToolsReadyNilIsNoOp(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{finalStep("hi")}}
	a, _, _, _ := testAgent(t, gw)

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)
}

// TestWaitToolsReadyRunsBeforeToolSnapshot proves the hook fires
// BEFORE the turn snapshots its tool surface: swapping tools inside
// the hook must be visible to the very first request the turn sends.
func TestWaitToolsReadyRunsBeforeToolSnapshot(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{finalStep("hi")}}
	a, _, _, _ := testAgent(t, gw)

	reg := tools.NewRegistry()
	swapped := &tools.Tool{
		Name:        "swapped_in",
		Description: "arrived via the readiness hook",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "ok", nil
		},
	}
	if err := reg.Register(swapped); err != nil {
		t.Fatal(err)
	}
	c, err := tools.NewConstrained(reg, testToolCalls())
	if err != nil {
		t.Fatal(err)
	}

	var hookCalled bool
	a.SetWaitToolsReady(func(context.Context) {
		hookCalled = true
		a.SwapTools(registryExec{c}, []provider.ToolDef{
			{Name: swapped.Name, Description: swapped.Description, InputSchema: swapped.InputSchema},
		})
	})

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	if !hookCalled {
		t.Fatal("waitToolsReady hook never ran")
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if len(gw.requests) == 0 || len(gw.requests[0].Tools) != 1 || gw.requests[0].Tools[0].Name != "swapped_in" {
		t.Fatalf("first request tools = %+v, want only swapped_in (hook must run before the snapshot)", gw.requests[0].Tools)
	}
}

// TestWaitToolsReadyTimeoutStillProceeds confirms a hook that never
// becomes ready (bounded by ctx, as main.go's WaitReady wiring does)
// still lets the turn through — it must never block the turn
// indefinitely.
func TestWaitToolsReadyTimeoutStillProceeds(t *testing.T) {
	t.Parallel()
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{finalStep("hi")}}
	a, _, _, _ := testAgent(t, gw)

	neverReady := make(chan struct{})
	a.SetWaitToolsReady(func(ctx context.Context) {
		// Mirrors main.go's real hook: bound the wait via context
		// rather than a real timer, so the test stays instant.
		wctx, cancel := context.WithTimeout(ctx, time.Nanosecond)
		defer cancel()
		select {
		case <-neverReady:
		case <-wctx.Done():
		}
	})

	ch, err := a.Start(t.Context(), Request{SessionID: "s1", Route: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	if len(ofType(evs, stream.EventDone)) != 1 {
		t.Fatalf("turn did not complete after wait timeout: %+v", evs)
	}
}

// TestAgentEndTurnToolsEndsAfterSentinel pins D-075: a tool named in
// EndTurnTools that executes cleanly ends the turn right there — no
// second model call to react to a result nobody asked for. Only one
// gateway request must fire, and the assistant+tool-result messages
// still land in the round-trip (session persistence must see them
// exactly as if the turn had continued).
func TestAgentEndTurnToolsEndsAfterSentinel(t *testing.T) {
	t.Parallel()
	sentinel := &tools.Tool{
		Name:        "mission_status",
		Description: "reports mission status",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"outcome":{"type":"string"}},"required":["outcome"],"additionalProperties":false}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "recorded", nil
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"mission_status", `{"outcome":"done"}`}),
	}}
	a, _, events, _ := testAgent(t, gw, sentinel)

	ch, err := a.Start(t.Context(), Request{
		SessionID:    "s1",
		Route:        "coding",
		Messages:     []provider.Message{{Role: "user", Content: "go"}},
		EndTurnTools: []string{"mission_status"},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if len(gw.requests) != 1 {
		t.Fatalf("gateway requests = %d, want exactly 1 (no continuation call)", len(gw.requests))
	}
	if got := ofType(evs, stream.EventDone); len(got) != 1 {
		t.Fatalf("done events = %d, want exactly 1", len(got))
	}
	usage := ofType(evs, stream.EventUsage)
	if len(usage) != 1 || usage[0].Usage.InputTokens != 10 || usage[0].Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want the sentinel step's own usage (10/5)", usage)
	}
	if len(events.kinds) != 1 || events.kinds[0] != "tool_execution" {
		t.Fatalf("session events = %v, want the sentinel's tool_execution persisted", events.kinds)
	}
}

// TestAgentEndTurnToolsErrorContinues pins the D-075 carve-out: an
// EndTurnTools call that comes back as an error/denial does NOT end
// the turn — the model must see the failure and get a chance to
// retry, exactly like today's behavior for every other tool.
func TestAgentEndTurnToolsErrorContinues(t *testing.T) {
	t.Parallel()
	sentinel := &tools.Tool{
		Name:        "mission_status",
		Description: "reports mission status",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			panic("bad args")
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"mission_status", `{}`}),
		finalStep("retried"),
	}}
	a, _, _, _ := testAgent(t, gw, sentinel)

	ch, err := a.Start(t.Context(), Request{
		SessionID:    "s1",
		Route:        "coding",
		Messages:     []provider.Message{{Role: "user", Content: "go"}},
		EndTurnTools: []string{"mission_status"},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if len(gw.requests) != 2 {
		t.Fatalf("gateway requests = %d, want 2 (error result must still get a continuation)", len(gw.requests))
	}
	if got := ofType(evs, stream.EventDone); len(got) != 1 {
		t.Fatalf("done events = %d, want exactly 1", len(got))
	}
}

