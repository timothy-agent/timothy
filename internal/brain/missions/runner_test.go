package missions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// scriptedAgent is a fake agentStream that replays a fixed sequence of
// event batches, one batch per call to Start — call N of Start gets
// batch N. Lets a test script "first turn has no sentinel, recovery
// turn does" without a real gateway. The real loop.Agent guarantees
// exactly one terminal event before close, so batches that don't end
// on one get an EventDone appended — a bare close is the abnormal
// shape runTurn now rejects, and only rawClose tests script it.
type scriptedAgent struct {
	batches  [][]stream.StreamEvent
	rawClose bool // suppress the auto-appended terminal (terminal-loss tests)
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
	batch := f.batches[i]
	if !f.rawClose && !endsTerminal(batch) {
		batch = append(append([]stream.StreamEvent(nil), batch...), stream.StreamEvent{Type: stream.EventDone})
	}
	ch := make(chan stream.StreamEvent, len(batch))
	for _, ev := range batch {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func endsTerminal(batch []stream.StreamEvent) bool {
	if len(batch) == 0 {
		return false
	}
	switch batch[len(batch)-1].Type {
	case stream.EventDone, stream.EventError, stream.EventIncomplete:
		return true
	}
	return false
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
	denied  []string // "missionID:tool:digest"
}

func (f *fakeParker) OnPermissionParked(_ context.Context, missionID, _, tool, _, danger, _ string) {
	f.parked = append(f.parked, missionID+":"+tool+":"+danger)
}

func (f *fakeParker) OnPermissionCleared(_ context.Context, missionID string) {
	f.cleared = append(f.cleared, missionID)
}

func (f *fakeParker) OnPermissionDenied(_ context.Context, missionID, tool, digest string) {
	f.denied = append(f.denied, missionID+":"+tool+":"+digest)
}

func permissionRequestEvent(id, callID, tool, danger string) stream.StreamEvent {
	return stream.StreamEvent{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{
		ID: id, CallID: callID, Tool: tool, Danger: danger,
	}}
}

func toolResultEvent(id string) stream.StreamEvent {
	return stream.StreamEvent{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{ID: id}}
}

func deniedToolResultEvent(id, name, digest string) stream.StreamEvent {
	return stream.StreamEvent{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{
		ID: id, Name: name, Status: "denied", Digest: digest,
	}}
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
	if !v.Forced {
		t.Fatalf("RunWorker verdict when sentinel never arrives = %+v, want Forced=true", v)
	}
	if agent.call != 2 {
		t.Fatalf("expected exactly two turns (original + one recovery, then give up), got %d", agent.call)
	}
}

// TestRunWorkerFallsBackToXMLTextSentinel covers the observed GLM-5.2
// failure: the worker never calls mission_status as a tool, but ends
// its turn with the XML-ish self-closing tag form. The runner must
// recover the verdict from text instead of forcing a retry.
func TestRunWorkerFallsBackToXMLTextSentinel(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent(`All files have been created. <mission_status outcome="done" evidence="All files have been created and tests pass."/>`)},
	}}
	r := newTestRunner(agent)
	v, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if v.Outcome != "done" || v.Evidence != "All files have been created and tests pass." {
		t.Fatalf("RunWorker verdict via XML text fallback = %+v", v)
	}
	if v.Forced {
		t.Fatal("a successfully-recovered text-form sentinel must not be marked Forced")
	}
	// The XML form arrived on the FIRST turn (no tool call at all), so the
	// runner still needs its one recovery re-run before falling back to
	// text extraction — that ladder order is unchanged by this fix.
	if agent.call != 2 {
		t.Fatalf("expected two turns (original + one recovery) before the text fallback kicks in, got %d", agent.call)
	}
}

// TestRunWorkerFallsBackToTokenJSONTextSentinel covers the observed
// qwen3:30b failure: a bare "mission_status" line followed by a JSON
// object, never a tool call.
func TestRunWorkerFallsBackToTokenJSONTextSentinel(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("no sentinel here")},
		{textEvent("mission_status\n{\"outcome\": \"retry\", \"analysis\": \"hit an error, will retry\"}")},
	}}
	r := newTestRunner(agent)
	v, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if v.Outcome != "retry" || v.Analysis != "hit an error, will retry" {
		t.Fatalf("RunWorker verdict via token+JSON text fallback = %+v", v)
	}
	if v.Forced {
		t.Fatal("a successfully-recovered text-form sentinel must not be marked Forced")
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

// TestRunReviewFallsBackToTextSentinel covers the observed qwen3:30b
// reviewer failure: prose review, never a review_verdict tool call —
// the runner must recover the verdict from a text-form sentinel in the
// recovery turn instead of erroring the round out.
func TestRunReviewFallsBackToTextSentinel(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("Looking at this closely, I think it holds up.")}, // no tool call
		{textEvent(`Confirmed. <review_verdict decision="approve"/>`)}, // recovery: still text-only
	}}
	r := newTestRunner(agent)
	v, _, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Diff: "diff"}, nil)
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !v.Approved {
		t.Fatalf("RunReview verdict via text fallback = %+v, want approved", v)
	}
}

// TestRunReviewFallsBackToTokenJSONTextSentinel mirrors the XML case
// for the token+JSON text form, on a rework decision — findings are
// empty in the text form (acceptable: GapFingerprint of empty findings
// is empty, which the state machine already tolerates).
func TestRunReviewFallsBackToTokenJSONTextSentinel(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("still analyzing")},
		{textEvent("review_verdict\n{\"decision\": \"rework\"}")},
	}}
	r := newTestRunner(agent)
	v, _, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Diff: "diff"}, nil)
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if v.Approved {
		t.Fatalf("RunReview verdict via token+JSON text fallback = %+v, want rework", v)
	}
	if len(v.Findings) != 0 {
		t.Fatalf("RunReview findings = %+v, want empty (text form carries no structured findings)", v.Findings)
	}
}

func TestPlanSessionParsesSpec(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Add validation","verify_cmd":"go test ./...","passes":true}]}`)},
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
	if agent.call != 1 {
		t.Fatalf("expected exactly one turn (no recovery needed), got %d", agent.call)
	}
	// The planner must not act: its only tool is submit_plan, so the
	// base surface (shell, write_file, ...) is filtered out entirely.
	if allow := agent.requests[0].ToolAllow; len(allow) != 1 || allow[0] != planToolName {
		t.Fatalf("planner ToolAllow = %v, want [%s]", allow, planToolName)
	}
}

func TestPlanSessionRejectsEmptyPlan(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[]}`)},
		{toolEndEvent(planToolName, `{"units":[]}`)},
	}}
	r := newTestRunner(agent)
	if _, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default"}, ""); err == nil {
		t.Fatal("PlanSession accepted a plan with zero units")
	}
}

// TestPlanSessionRejectsCommandSubstitution guards the fix for a real
// canary flake: a planner-authored verify_cmd containing $(...) parked
// the mission when the worker's own shell classifier correctly denied
// it as opaque (D-039) — the plan should be rejected here, at
// submission, with feedback the planner can act on immediately.
func TestPlanSessionRejectsCommandSubstitution(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Check output","verify_cmd":"test \"$(cat out.md)\" = ok"}]}`)},
		{toolEndEvent(planToolName, `{"units":[{"title":"Check output","verify_cmd":"test \"$(cat out.md)\" = ok"}]}`)},
	}}
	r := newTestRunner(agent)
	_, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default"}, "")
	if err == nil {
		t.Fatal("PlanSession accepted a verify_cmd containing command substitution")
	}
	if !strings.Contains(err.Error(), "command substitution") {
		t.Fatalf("PlanSession error = %q, want it to name command substitution", err.Error())
	}
}

// TestPlanSessionRejectsBackticks mirrors the $(...) case for the
// other opaque-form spelling of command substitution.
func TestPlanSessionRejectsBackticks(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, "{\"units\":[{\"title\":\"Check output\",\"verify_cmd\":\"test `cat out.md` = ok\"}]}")},
		{toolEndEvent(planToolName, "{\"units\":[{\"title\":\"Check output\",\"verify_cmd\":\"test `cat out.md` = ok\"}]}")},
	}}
	r := newTestRunner(agent)
	_, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default"}, "")
	if err == nil {
		t.Fatal("PlanSession accepted a verify_cmd containing backticks")
	}
	if !strings.Contains(err.Error(), "command substitution") {
		t.Fatalf("PlanSession error = %q, want it to name command substitution", err.Error())
	}
}

// TestPlanSessionRecoversFromCommandSubstitutionFeedback confirms the
// substitution rejection feeds the same one-recovery-turn ladder as
// the empty-plan/malformed-JSON cases: the planner gets told exactly
// what was wrong and a corrected plan on the next turn is accepted.
func TestPlanSessionRecoversFromCommandSubstitutionFeedback(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Check output","verify_cmd":"test \"$(cat out.md)\" = ok"}]}`)},
		{toolEndEvent(planToolName, `{"units":[{"title":"Check output","verify_cmd":"grep -qi ok out.md"}]}`)},
	}}
	r := newTestRunner(agent)
	spec, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "fix bug"}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if len(spec.Units) != 1 || spec.Units[0].VerifyCmd != "grep -qi ok out.md" {
		t.Fatalf("PlanSession spec = %+v", spec)
	}
	if agent.call != 2 {
		t.Fatalf("expected exactly two turns (original + one recovery), got %d", agent.call)
	}
}

// TestPlanSessionAcceptsLegitimateVerifyCmd confirms the substitution
// guard doesn't false-positive on real verification commands — a
// content-checking verify_cmd (the existing tautology guard's whole
// point: never a bare echo) and a legitimate input redirect (<, which
// must stay legal — only substitution is banned) both parse cleanly
// in a single turn, no recovery needed.
func TestPlanSessionAcceptsLegitimateVerifyCmd(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[`+
			`{"title":"Check content","verify_cmd":"grep -qi 'foo' out.md"},`+
			`{"title":"Check line count","verify_cmd":"test -f x && wc -l < x"}`+
			`]}`)},
	}}
	r := newTestRunner(agent)
	spec, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "fix bug"}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if len(spec.Units) != 2 {
		t.Fatalf("PlanSession spec = %+v", spec)
	}
	if spec.Units[0].VerifyCmd != "grep -qi 'foo' out.md" || spec.Units[1].VerifyCmd != "test -f x && wc -l < x" {
		t.Fatalf("PlanSession spec units = %+v", spec.Units)
	}
	if agent.call != 1 {
		t.Fatalf("expected exactly one turn (no recovery needed), got %d", agent.call)
	}
}

func TestPlanSessionRejectsMalformedJSON(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("I think the plan should have a few steps but I won't format it as JSON")},
		{textEvent("still no tool call")},
	}}
	r := newTestRunner(agent)
	if _, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default"}, ""); err == nil {
		t.Fatal("PlanSession accepted non-JSON output")
	}
	if agent.call != 2 {
		t.Fatalf("expected exactly two turns (original + one recovery), got %d", agent.call)
	}
}

// TestPlanSessionRecoversWithFeedback confirms a missing/invalid
// submit_plan call on the first turn gets ONE recovery re-run whose
// injected message names what was wrong — not a byte-identical retry
// of the original prompt, which previously produced the same failure
// every time against a nondeterministic model.
func TestPlanSessionRecoversWithFeedback(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("here's my plan in prose, no tool call")},
		{toolEndEvent(planToolName, `{"units":[{"title":"Fix it","verify_cmd":"true"}]}`)},
	}}
	r := newTestRunner(agent)
	spec, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "fix bug"}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if len(spec.Units) != 1 || spec.Units[0].Title != "Fix it" {
		t.Fatalf("PlanSession spec = %+v", spec)
	}
	if agent.call != 2 {
		t.Fatalf("expected exactly two turns (original + one recovery), got %d", agent.call)
	}
	recoverReq := agent.requests[1]
	last := recoverReq.Messages[len(recoverReq.Messages)-1]
	if !strings.Contains(last.Content, "did not call submit_plan") {
		t.Fatalf("recovery message = %q, want it to name the failure", last.Content)
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

// sequencingParker records, at the moment each OnPermissionCleared
// fires, how many distinct calls had parked vs how many tool results
// had been delivered so far — the direct signal that distinguishes
// "cleared after the right call" from "cleared after the first call
// resolved regardless of which one" (a plain clear-count is identical
// in both the buggy and fixed behavior for two concurrent parks).
type sequencingParker struct {
	fakeParker
	resultsDeliveredAtClear []int
	resultsDelivered        atomic.Int32
}

func (f *sequencingParker) OnPermissionCleared(ctx context.Context, missionID string) {
	f.fakeParker.OnPermissionCleared(ctx, missionID)
	f.resultsDeliveredAtClear = append(f.resultsDeliveredAtClear, int(f.resultsDelivered.Load()))
}

// TestRunWorkerConcurrentParksClearOnlyWhenAllResolve is the
// regression for a real incident: a turn issuing two concurrent
// destructive tool calls, both parked. Before this fix, runTurn's
// single shared "parked" bool cleared the mission's pending-permission
// state as soon as the FIRST call's result arrived, even while the
// second call was still genuinely blocked awaiting a decision — the
// UI/API then showed nothing pending while a destructive command sat
// unresolved for up to the full 10-minute timeout.
func TestRunWorkerConcurrentParksClearOnlyWhenAllResolve(t *testing.T) {
	parker := &sequencingParker{}
	agent := &countingResultAgent{
		scriptedAgent: scriptedAgent{batches: [][]stream.StreamEvent{{
			textEvent("running two risky tool calls"),
			permissionRequestEvent("perm1", "call1", "shell", "destructive"),
			permissionRequestEvent("perm2", "call2", "shell", "destructive"),
			toolResultEvent("call1"), // first call resolves — must NOT clear yet
			toolResultEvent("call2"), // second (last) call resolves — NOW it clears
			toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ran both"}`),
		}}},
		parker: parker,
	}
	r := newTestRunnerWithParker(agent, parker)
	v, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if v.Outcome != "done" {
		t.Fatalf("RunWorker verdict = %+v, want done", v)
	}
	if len(parker.parked) != 2 {
		t.Fatalf("parker.parked = %v, want two parks (both concurrent calls)", parker.parked)
	}
	if len(parker.resultsDeliveredAtClear) != 1 || parker.resultsDeliveredAtClear[0] != 2 {
		// The bug this guards: the buggy single-bool version clears
		// after resultsDelivered==1 (right after call1, prematurely) —
		// this asserts the clear only happens once BOTH results (2) have
		// actually been delivered.
		t.Fatalf("clear fired after %v results delivered, want exactly one clear after both (2) results", parker.resultsDeliveredAtClear)
	}
}

// countingResultAgent wraps scriptedAgent's fixed batch with a live
// counter the test parker reads at clear-time — the pre-buffered
// channel scriptedAgent uses can't otherwise expose "how far the
// consumer has gotten" to a callback fired mid-drain.
type countingResultAgent struct {
	scriptedAgent
	parker *sequencingParker
}

func (f *countingResultAgent) Start(ctx context.Context, req loop.Request) (<-chan stream.StreamEvent, error) {
	raw, err := f.scriptedAgent.Start(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan stream.StreamEvent)
	go func() {
		defer close(out)
		for ev := range raw {
			if ev.Type == stream.EventToolResult {
				f.parker.resultsDelivered.Add(1)
			}
			out <- ev
		}
	}()
	return out, nil
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
func TestWorkerRoute(t *testing.T) {
	tests := []struct {
		name string
		m    Mission
		want string
	}{
		{"no escalation route configured", Mission{Route: "mini", ConsecutiveFailures: 2, StallCount: 1}, "mini"},
		{"clean run stays on its route", Mission{Route: "mini", EscalationRoute: "coding"}, "mini"},
		{"worker failure escalates", Mission{Route: "mini", EscalationRoute: "coding", ConsecutiveFailures: 1}, "coding"},
		{"review rework escalates", Mission{Route: "mini", EscalationRoute: "coding", StallCount: 1}, "coding"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := workerRoute(tc.m); got != tc.want {
				t.Fatalf("workerRoute = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunWorkerUsesEscalatedRoute(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Route: "mini", EscalationRoute: "coding",
		ConsecutiveFailures: 1, Workspace: "/workspace/missions/m1"}
	if _, _, err := r.RunWorker(context.Background(), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if got := agent.requests[0].Route; got != "coding" {
		t.Fatalf("worker request route = %q, want the escalation route", got)
	}
}

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

// shellExtraTool pulls the "shell" tool out of a mission's ExtraTools
// list — helper for tests exercising missionTools' Runner wiring
// directly, without going through a full RunWorker turn.
func shellExtraTool(t *testing.T, extras []*tools.Tool) *tools.Tool {
	t.Helper()
	for _, tool := range extras {
		if tool.Name == "shell" {
			return tool
		}
	}
	t.Fatal("no shell tool in ExtraTools")
	return nil
}

// TestMissionToolsSandboxRoutesShell confirms that with r.sandbox set,
// missionTools wires a shell whose Runner calls the sandbox backend
// (never local exec) with the mission id and mission's own work root,
// and formats a non-zero exit code the same way builtin.Shell's local
// path does ("(exit status N)" appended, no error).
func TestMissionToolsSandboxRoutesShell(t *testing.T) {
	dir := t.TempDir()
	var gotMissionID, gotWorkdir, gotCommand string
	r := newTestRunner(&scriptedAgent{})
	r.sandbox = func(ctx context.Context, missionID, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		gotMissionID, gotWorkdir, gotCommand = missionID, workdir, command
		_, _ = out.Write([]byte("sandboxed output"))
		return 3, nil
	}
	extraTools := r.missionTools(Mission{ID: "m1", Workspace: dir})
	shell := shellExtraTool(t, extraTools)
	args, _ := json.Marshal(map[string]string{"command": "false"})
	out, err := shell.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotMissionID != "m1" || gotWorkdir != dir || gotCommand != "false" {
		t.Fatalf("sandbox called with (%q,%q,%q), want (m1,%s,false)", gotMissionID, gotWorkdir, gotCommand, dir)
	}
	if !strings.HasPrefix(out, "sandboxed output") || !strings.Contains(out, "(exit status 3)") {
		t.Fatalf("output = %q, want sandbox output plus exit-status suffix", out)
	}
}

// TestMissionToolsSandboxTimeoutPropagatesAsError confirms a timeout
// from the sandbox backend surfaces as an error from the shell tool,
// matching builtin.Shell's local-path contract (a timeout is an error,
// a non-zero exit is not).
func TestMissionToolsSandboxTimeoutPropagatesAsError(t *testing.T) {
	r := newTestRunner(&scriptedAgent{})
	r.sandbox = func(ctx context.Context, missionID, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		return 124, errors.New("command timed out after 30s")
	}
	extraTools := r.missionTools(Mission{ID: "m1", Workspace: t.TempDir()})
	shell := shellExtraTool(t, extraTools)
	args, _ := json.Marshal(map[string]string{"command": "sleep 100"})
	if _, err := shell.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Execute err = %v, want a timeout error", err)
	}
}

// TestMissionToolsSandboxCapsOutput confirms cappedStringWriter bounds
// the sandbox Runner's output the same way builtin.Shell's local path
// caps its own — a runaway sandboxed command must not balloon memory
// or context just because the local exec path isn't the one running.
func TestMissionToolsSandboxCapsOutput(t *testing.T) {
	r := newTestRunner(&scriptedAgent{})
	over := strings.Repeat("x", shellOutputCap+1024)
	r.sandbox = func(ctx context.Context, missionID, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		_, _ = out.Write([]byte(over))
		return 0, nil
	}
	extraTools := r.missionTools(Mission{ID: "m1", Workspace: t.TempDir()})
	shell := shellExtraTool(t, extraTools)
	args, _ := json.Marshal(map[string]string{"command": "yes"})
	out, err := shell.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "[output capped]") {
		t.Fatalf("output not marked as capped: %q", out[:min(200, len(out))])
	}
	if len(out) > shellOutputCap+len("\n[output capped]") {
		t.Fatalf("output length %d exceeds cap plus marker", len(out))
	}
}

// TestRunWorkerUnattendedFollowsScheduleID is the D-039 wiring check:
// a schedule-fired mission (ScheduleID set) has nobody watching its
// turns, so the loop.Request it hands to the agent must say so.
func TestRunWorkerUnattendedFollowsScheduleID(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Route: "default", ScheduleID: "sched-1"}
	if _, _, err := r.RunWorker(context.Background(), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if !agent.requests[0].Unattended {
		t.Fatal("worker request Unattended = false, want true for a schedule-fired mission")
	}
}

// TestRunWorkerAttendedWithoutScheduleID is the other half: a
// UI-created mission (no ScheduleID) keeps the park-and-answer flow.
func TestRunWorkerAttendedWithoutScheduleID(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Route: "default"}
	if _, _, err := r.RunWorker(context.Background(), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if agent.requests[0].Unattended {
		t.Fatal("worker request Unattended = true, want false without a ScheduleID")
	}
}

// TestRunWorkerReportsPermissionDenied confirms a denied tool result
// (the D-039 automatic unattended denial, or any other denial) reaches
// the mission via parkNotifier as a mission.permission_denied event —
// without this, an unattended turn's fast-fail denials would be
// invisible to anything watching the mission (no park event is ever
// emitted for them, unlike the ask-and-timeout path).
func TestRunWorkerReportsPermissionDenied(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		textEvent("tried a risky call"),
		deniedToolResultEvent("call1", "shell", "permission denied automatically (unattended mission): ..."),
		toolEndEvent(missionStatusToolName, `{"outcome":"retry","analysis":"denied"}`),
	}}}
	parker := &fakeParker{}
	r := newTestRunnerWithParker(agent, parker)
	if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if len(parker.denied) != 1 || !strings.HasPrefix(parker.denied[0], "m1:shell:") {
		t.Fatalf("parker.denied = %v, want exactly one m1:shell:... entry", parker.denied)
	}
}

// TestRunTurnBareCloseIsError pins the missions half of D-044: a loop
// channel that closes with no terminal event at all (every producer
// lost it to the turn deadline racing a stream cut) must surface as an
// infra error — previously it returned a clean empty verdict, which
// the caller read as a missing sentinel and burned a recovery re-run
// plus a forced retry for one silent infra failure.
func TestRunTurnBareCloseIsError(t *testing.T) {
	agent := &scriptedAgent{rawClose: true, batches: [][]stream.StreamEvent{{
		textEvent("partial work"),
	}}}
	r := newTestRunner(agent)
	text, _, err := r.runTurn(context.Background(), loop.Request{MissionID: "m1"}, missionStatusToolName)
	if err == nil || !strings.Contains(err.Error(), "without a terminal event") {
		t.Fatalf("err = %v, want no-terminal error", err)
	}
	if text != "partial work" {
		t.Fatalf("text = %q, want the partial preserved", text)
	}
}

// TestRunTurnIncompleteIsError: a cut-off stream (EventIncomplete
// terminal) is an infra failure, not a short clean answer — it must
// not flow into sentinel parsing as if the worker finished.
func TestRunTurnIncompleteIsError(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		textEvent("partial"),
		{Type: stream.EventIncomplete, Text: "stream ended without a terminal event"},
	}}}
	r := newTestRunner(agent)
	if _, _, err := r.runTurn(context.Background(), loop.Request{MissionID: "m1"}, missionStatusToolName); err == nil || !strings.Contains(err.Error(), "incomplete stream") {
		t.Fatalf("err = %v, want incomplete-stream error", err)
	}
}

// TestRunTurnNilErrErrorEvent: an EventError carrying a nil Err must
// not panic the runner (chat's noteFailure guards this; the runner
// dereferenced ev.Err.Message unconditionally).
func TestRunTurnNilErrErrorEvent(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		{Type: stream.EventError},
	}}}
	r := newTestRunner(agent)
	if _, _, err := r.runTurn(context.Background(), loop.Request{MissionID: "m1"}, missionStatusToolName); err == nil || !strings.Contains(err.Error(), "provider stream error") {
		t.Fatalf("err = %v, want generic provider stream error", err)
	}
}
