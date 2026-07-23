package missions

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// scriptedAgent is a fake agentStream that replays a fixed sequence of
// event batches, one batch per call to Start — call N of Start gets
// batch N. Lets a test script "first turn has no sentinel, recovery
// turn does" without a real gateway.
type scriptedAgent struct {
	batches [][]stream.StreamEvent
	call    int
}

func (f *scriptedAgent) Start(ctx context.Context, req loop.Request) (<-chan stream.StreamEvent, error) {
	i := f.call
	f.call++
	if i >= len(f.batches) {
		i = len(f.batches) - 1
	}
	ch := make(chan stream.StreamEvent, len(f.batches[i]))
	for _, ev := range f.batches[i] {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func textEvent(s string) stream.StreamEvent {
	return stream.StreamEvent{Type: stream.EventChunk, Text: s}
}

func toolEndEvent(name string, args string) stream.StreamEvent {
	return stream.StreamEvent{Type: stream.EventToolEnd, ToolCall: &stream.ToolCallEvent{
		Name: name, Input: json.RawMessage(args),
	}}
}

func newTestRunner(agent agentStream) *nativeRunner {
	return &nativeRunner{agent: agent, log: slog.Default()}
}

func TestRunWorkerSentinelPresent(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("did the thing"), toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"tests pass"}`)},
	}}
	r := newTestRunner(agent)
	v, text, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if v.Outcome != "done" || v.Evidence != "tests pass" {
		t.Fatalf("RunWorker verdict = %+v", v)
	}
	if text != "did the thing" {
		t.Fatalf("RunWorker text = %q", text)
	}
	if agent.call != 1 {
		t.Fatalf("expected exactly one turn when the sentinel is present, got %d", agent.call)
	}
}

func TestRunWorkerRecoversWhenSentinelMissingThenPresent(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("I made some progress")}, // no sentinel
		{textEvent("here it is"), toolEndEvent(missionStatusToolName, `{"outcome":"retry","analysis":"hit an error"}`)}, // recovery turn has it
	}}
	r := newTestRunner(agent)
	v, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if v.Outcome != "retry" || v.Analysis != "hit an error" {
		t.Fatalf("RunWorker verdict after recovery = %+v", v)
	}
	if agent.call != 2 {
		t.Fatalf("expected exactly two turns (original + one recovery), got %d", agent.call)
	}
}

func TestRunWorkerForcedRetryWhenSentinelNeverArrives(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("This looks ready for review.")}, // no sentinel, bail-shaped text
		{textEvent("Still nothing concrete.")},      // recovery also missing
	}}
	r := newTestRunner(agent)
	v, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if v.Outcome != "retry" {
		t.Fatalf("RunWorker verdict when sentinel never arrives = %+v, want forced retry", v)
	}
	if agent.call != 2 {
		t.Fatalf("expected exactly two turns (original + one recovery, then give up), got %d", agent.call)
	}
}

func TestRunWorkerIgnoresRepeatSentinelCalls(t *testing.T) {
	// A worker that calls mission_status twice in one turn: the runner
	// captures the LAST tool_end for that name in the stream loop — this
	// documents that behavior explicitly (loop.Agent itself is what
	// would reject a genuine repeat via the tool's closure-flag guard in
	// production; nativeRunner just reads what the stream reports).
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{
			toolEndEvent(missionStatusToolName, `{"outcome":"blocked","question":"which env?"}`),
			toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`),
		},
	}}
	r := newTestRunner(agent)
	v, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if v.Outcome != "done" {
		t.Fatalf("RunWorker verdict = %+v, want the last sentinel call's outcome", v)
	}
}

func TestRunReviewParsesVerdictAndCarriesGatekeeperState(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("looks fine"), toolEndEvent(reviewVerdictToolName, `{"decision":"approve"}`)},
	}}
	r := newTestRunner(agent)
	v, state, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, "diff content", nil)
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !v.Approved {
		t.Fatalf("RunReview verdict = %+v, want approved", v)
	}
	if state == nil || len(state.Messages) == 0 {
		t.Fatal("RunReview did not return gatekeeper state to resume from")
	}
}

func TestRunReviewErrorsWhenVerdictMissing(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("I'm not sure about this one")},
	}}
	r := newTestRunner(agent)
	_, _, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, "diff", nil)
	if err == nil {
		t.Fatal("RunReview did not error when the reviewer never called review_verdict")
	}
}

func TestPlanSessionParsesSpec(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent(`{"units":[{"title":"Add validation","verify_cmd":"go test ./...","passes":true}]}`)},
	}}
	r := newTestRunner(agent)
	spec, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "fix bug"}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if len(spec.Units) != 1 || spec.Units[0].Title != "Add validation" {
		t.Fatalf("PlanSession spec = %+v", spec)
	}
	if spec.Units[0].Passes {
		t.Fatal("PlanSession must never trust a planner-claimed passes=true — only RunVerify may set it")
	}
}

func TestPlanSessionRejectsEmptyPlan(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent(`{"units":[]}`)},
	}}
	r := newTestRunner(agent)
	if _, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default"}, ""); err == nil {
		t.Fatal("PlanSession accepted a plan with zero units")
	}
}

func TestPlanSessionRejectsMalformedJSON(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("I think the plan should have a few steps but I won't format it as JSON")},
	}}
	r := newTestRunner(agent)
	if _, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default"}, ""); err == nil {
		t.Fatal("PlanSession accepted non-JSON output")
	}
}
