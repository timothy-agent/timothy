package missions

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// scriptedAgent is a fake agentStream that replays a fixed sequence of
// event batches, one batch per call to Start — call N of Start gets
// batch N. Lets a test script "first turn has no sentinel, recovery
// turn does" without a real gateway.
type scriptedAgent struct {
	batches  [][]stream.StreamEvent
	call     int
	requests []loop.Request
}

func (f *scriptedAgent) Start(ctx context.Context, req loop.Request) (<-chan stream.StreamEvent, error) {
	f.requests = append(f.requests, req)
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

func newTestRunnerWithParker(agent agentStream, parker parkNotifier) *nativeRunner {
	return &nativeRunner{agent: agent, parker: parker, log: slog.Default()}
}

// fakeParker records park/clear calls in order for assertion, keyed by
// mission id — a real parkNotifier for tests, not a mock of one.
type fakeParker struct {
	parked  []string // "missionID:tool:danger"
	cleared []string // missionID
}

func (f *fakeParker) OnPermissionParked(_ context.Context, missionID, _, tool, _, danger, _ string) {
	f.parked = append(f.parked, missionID+":"+tool+":"+danger)
}

func (f *fakeParker) OnPermissionCleared(_ context.Context, missionID string) {
	f.cleared = append(f.cleared, missionID)
}

func permissionRequestEvent(id, callID, tool, danger string) stream.StreamEvent {
	return stream.StreamEvent{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{
		ID: id, CallID: callID, Tool: tool, Danger: danger,
	}}
}

func toolResultEvent(id string) stream.StreamEvent {
	return stream.StreamEvent{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{ID: id}}
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
	v, state, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Goal: "goal", Diff: "diff content"}, nil)
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

// TestRunReviewPacketRendersArtifactsGoalAndEvidence covers the
// research-mission review gap: reviewers used to reject every round
// for "missing goal" / "missing artifact" because neither was in
// their context. The packet's goal, harness-read artifact contents,
// and the worker's evidence must all reach the reviewer — and no
// empty "Diff to review" section may appear when there is no diff.
func TestRunReviewPacketRendersArtifactsGoalAndEvidence(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("looks solid"), toolEndEvent(reviewVerdictToolName, `{"decision":"approve"}`)},
	}}
	r := newTestRunner(agent)
	packet := ReviewPacket{
		Goal:      "Summarize HTTP 429",
		UnitTitle: "write summary",
		Artifacts: map[string]string{"summary.md": "429 means Too Many Requests, per RFC 6585."},
		Evidence:  "Summary written to summary.md, sourced from RFC 6585.",
	}
	v, _, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, packet, nil)
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !v.Approved {
		t.Fatalf("RunReview verdict = %+v, want approved", v)
	}
	if len(agent.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(agent.requests))
	}
	msgs := agent.requests[0].Messages
	content := msgs[len(msgs)-1].Content
	for _, want := range []string{packet.Goal, "RFC 6585", packet.Evidence, "summary.md"} {
		if !strings.Contains(content, want) {
			t.Fatalf("reviewer message missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "Diff to review") {
		t.Fatalf("reviewer message must not claim there's a diff when there is none:\n%s", content)
	}
}

func TestRunReviewErrorsWhenVerdictMissing(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("I'm not sure about this one")},
		{textEvent("Still not calling it.")}, // recovery turn also missing
	}}
	r := newTestRunner(agent)
	_, _, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Diff: "diff"}, nil)
	if err == nil {
		t.Fatal("RunReview did not error when the reviewer never called review_verdict")
	}
	if agent.call != 2 {
		t.Fatalf("expected exactly two turns (original + one recovery, then give up), got %d", agent.call)
	}
}

// TestRunReviewRecoversWhenVerdictMissingThenPresent mirrors
// RunWorker's recovery ladder: a reviewer that doesn't call
// review_verdict on its first turn (e.g. it ran long on analysis) gets
// one recovery turn demanding the call before the round is treated as
// a failure.
func TestRunReviewRecoversWhenVerdictMissingThenPresent(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("Still analyzing the evidence in depth...")}, // no verdict
		{textEvent("Approved."), toolEndEvent(reviewVerdictToolName, `{"decision":"approve"}`)},
	}}
	r := newTestRunner(agent)
	v, _, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Diff: "diff"}, nil)
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !v.Approved {
		t.Fatalf("RunReview verdict after recovery = %+v, want approved", v)
	}
	if agent.call != 2 {
		t.Fatalf("expected exactly two turns (original + one recovery), got %d", agent.call)
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

// TestRunWorkerReportsPermissionParkAndClear is the M4.1 exit
// criterion: a tool call that parks on an interactive permission
// prompt must reach the mission row (via parkNotifier), not vanish
// into runTurn's event drain the way it silently did before this
// existed — that gap is what stranded a real mission for its full
// 10-minute timeout with the UI showing nothing.
func TestRunWorkerReportsPermissionParkAndClear(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		textEvent("about to run a risky command"),
		permissionRequestEvent("perm1", "call1", "shell", "destructive"),
		toolResultEvent("call1"),
		toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ran it"}`),
	}}}
	parker := &fakeParker{}
	r := newTestRunnerWithParker(agent, parker)
	v, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if v.Outcome != "done" {
		t.Fatalf("RunWorker verdict = %+v, want done (the turn continues normally after the park clears)", v)
	}
	if len(parker.parked) != 1 || parker.parked[0] != "m1:shell:destructive" {
		t.Fatalf("parker.parked = %v, want exactly one m1:shell:destructive", parker.parked)
	}
	if len(parker.cleared) != 1 || parker.cleared[0] != "m1" {
		t.Fatalf("parker.cleared = %v, want exactly one clear for m1", parker.cleared)
	}
}

// TestRunWorkerClearsPermissionParkOnStreamError covers the other exit
// path: if the turn errors out while still parked (e.g. the gateway
// connection drops), the mission row must not be left showing a
// pending_permission nothing will ever resolve.
func TestRunWorkerClearsPermissionParkOnStreamError(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		permissionRequestEvent("perm1", "call1", "shell", "moderate"),
		{Type: stream.EventError, Err: &stream.StreamError{Message: "connection lost"}},
	}}}
	parker := &fakeParker{}
	r := newTestRunnerWithParker(agent, parker)
	if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"}); err == nil {
		t.Fatal("RunWorker: expected the stream error to propagate")
	}
	if len(parker.parked) != 1 {
		t.Fatalf("parker.parked = %v, want exactly one park", parker.parked)
	}
	if len(parker.cleared) != 1 || parker.cleared[0] != "m1" {
		t.Fatalf("parker.cleared = %v, want exactly one clear for m1 even on error", parker.cleared)
	}
}

func doneEvent(model string) stream.StreamEvent {
	return stream.StreamEvent{Type: stream.EventDone, Meta: &stream.Meta{Model: model}}
}

// TestRunWorkerModelFloor: a turn served by a deny-listed fallback
// model must surface ErrModelFloor so the driver pauses the mission
// immediately instead of burning iterations on a model that cannot
// drive tool-using work.
func TestRunWorkerModelFloor(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("uh"), doneEvent("amazon.nova-lite-v1:0")},
	}}
	r := &nativeRunner{agent: agent, modelFloorDeny: []string{"nova-lite", "qwen2.5:7b"}, log: slog.Default()}
	_, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if !errors.Is(err, ErrModelFloor) {
		t.Fatalf("RunWorker err = %v, want ErrModelFloor", err)
	}
}

// TestRunWorkerModelFloorDisabledByDefault: no deny list, any model is
// fine — the sentinel outcome decides.
func TestRunWorkerModelFloorDisabledByDefault(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`), doneEvent("amazon.nova-lite-v1:0")},
	}}
	r := newTestRunner(agent)
	v, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if v.Outcome != "done" {
		t.Fatalf("verdict = %+v", v)
	}
}

// TestRunWorkerGetsMissionScopedShell: the worker turn must carry a
// turn-scoped shell tool rooted in the mission's own workspace — the
// root cause of the workspace-split failure family was workers running
// shell in the shared global root while verification looked in the
// per-mission directory.
func TestRunWorkerGetsMissionScopedShell(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Route: "default", Workspace: "/workspace/missions/m1"}
	if _, _, err := r.RunWorker(context.Background(), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	var names []string
	for _, tool := range agent.requests[0].ExtraTools {
		names = append(names, tool.Name)
	}
	if !slices.Contains(names, "shell") {
		t.Fatalf("worker ExtraTools = %v, want a mission-scoped shell", names)
	}
	if !slices.Contains(names, missionStatusToolName) {
		t.Fatalf("worker ExtraTools = %v, want the mission_status sentinel", names)
	}
}
