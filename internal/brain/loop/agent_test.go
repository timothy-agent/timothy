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

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// scriptedGateway plays one canned event script per Stream call and
// records every request it saw.
type scriptedGateway struct {
	mu       sync.Mutex
	scripts  [][]stream.StreamEvent
	requests []gwclient.StreamRequest
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
	c, err := tools.NewConstrained(reg)
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
	// First step succeeds with a tool call and usage; the second
	// Stream call errors synchronously (exhausted script). Accumulated
	// usage must still reach the client before the error.
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
	c, err := tools.NewConstrained(reg)
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
}
