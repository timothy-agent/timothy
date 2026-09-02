package missions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// scriptedAgent is a fake agentStream that replays a fixed sequence of
// event batches, one batch per call to Start: call N of Start gets
// batch N. Lets a test script "first turn has no sentinel, recovery
// turn does" without a real gateway. The real loop.Agent guarantees
// exactly one terminal event before close, so batches that don't end
// on one get an EventDone appended: a bare close is the abnormal
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
// mission id: a real parkNotifier for tests, not a mock of one.
type fakeParker struct {
	parked    []string // "missionID:tool:danger"
	cleared   []string // missionID
	denied    []string // "missionID:tool:digest"
	toolCalls []toolCallRecord
}

// toolCallRecord captures one OnToolCall invocation for assertion:
// order in the slice IS call order, since runTurn appends synchronously
// off its single event-consuming loop.
type toolCallRecord struct {
	missionID, phase, tool, digest, status string
	durationMs                             int64
	kbHits                                 []KBHitTrace
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

func (f *fakeParker) OnToolCall(_ context.Context, missionID, phase, tool, digest, status string, durationMs int64, kbHits []KBHitTrace) {
	f.toolCalls = append(f.toolCalls, toolCallRecord{missionID, phase, tool, digest, status, durationMs, kbHits})
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

// finishedToolResultEvent builds a fully-populated tool_result event:
// the shape mission.tool_call's OnToolCall reads from (name, status,
// args, duration).
func finishedToolResultEvent(id, name, status, args string, durationMs int64) stream.StreamEvent {
	return stream.StreamEvent{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{
		ID: id, Name: name, Status: status, Args: json.RawMessage(args), DurationMs: durationMs,
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
	// D-075: the worker turn must tell loop.Agent its sentinel ends the
	// turn, so a clean mission_status call never triggers a pointless
	// continuation call. ask_user (D-088) always rides along in
	// EndTurnTools too, whether or not the turn actually offers it.
	wantEndTurnTools := []string{missionStatusToolName, askUserToolName}
	if len(agent.requests) != 1 || !slices.Equal(agent.requests[0].EndTurnTools, wantEndTurnTools) {
		t.Fatalf("EndTurnTools = %+v, want %+v", agent.requests[0].EndTurnTools, wantEndTurnTools)
	}
}

// TestRunWorkerFinalMessageExcludesPriorToolRoundNarration guards D-069's
// light-mission delivery: a worker session with several internal
// tool-calling rounds within ONE loop.Agent turn (draft narration, a
// shell call, more narration, then the sentinel) must expose only the
// text written since the last non-sentinel tool call as FinalMessage:
// the deliverable, not the whole session's chatter. The full text
// return (RunWorker's second value) stays unchanged: everything, in
// order, for progress-note/log purposes.
func TestRunWorkerFinalMessageExcludesPriorToolRoundNarration(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		textEvent("draft1"),
		toolEndEvent("shell", `{"command":"ls"}`),
		textEvent("final answer"),
		toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"tests pass"}`),
	}}}
	r := newTestRunner(agent)
	v, text, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if v.FinalMessage != "final answer" {
		t.Fatalf("FinalMessage = %q, want only the text written after the last non-sentinel tool call", v.FinalMessage)
	}
	if text != "draft1final answer" {
		t.Fatalf("text = %q, want the full unchanged multi-round transcript", text)
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
	// text extraction: that ladder order is unchanged by this fix.
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
	// captures the LAST tool_end for that name in the stream loop: this
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

func TestRunReviewParsesVerdict(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("looks fine"), toolEndEvent(reviewVerdictToolName, `{"decision":"approve","resolved":["F1"]}`)},
	}}
	r := newTestRunner(agent)
	v, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Goal: "goal", Diff: "diff content"})
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !v.Approved || len(v.Resolved) != 1 || v.Resolved[0] != "F1" {
		t.Fatalf("RunReview verdict = %+v, want approved with resolved [F1]", v)
	}
	// D-092: every round is a cold session, exactly one user message
	// carrying the packet, never a prior round's transcript.
	if msgs := agent.requests[0].Messages; len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("reviewer request messages = %+v, want exactly one user message", msgs)
	}
}

// TestRunReviewPacketListsOpenFindings pins the D-092 reviewer packet
// section: prior rounds' open findings reach the reviewer by id, with
// severity and file, under a heading telling it to mark resolved ids;
// resolved findings and an empty ledger render no section at all.
func TestRunReviewPacketListsOpenFindings(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(reviewVerdictToolName, `{"decision":"approve"}`)},
	}}
	r := newTestRunner(agent)
	packet := ReviewPacket{Goal: "goal", OpenFindings: []Finding{
		{ID: "F1", Title: "missing validation", File: "x.go", Severity: SeverityBlocking, Status: FindingOpen},
		{ID: "F2", Title: "typo in banner", Severity: SeverityMinor, Status: FindingOpen},
		{ID: "F3", Title: "already fixed", Status: FindingResolved},
	}}
	if _, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, packet); err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	content := agent.requests[0].Messages[0].Content
	for _, want := range []string{
		"Open findings from prior rounds (mark resolved ids in your verdict):",
		"- F1 [blocking] (unit 1) x.go: missing validation",
		"- F2 [minor] (unit 1) typo in banner",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("reviewer message missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "F3") {
		t.Fatalf("reviewer message lists a resolved finding:\n%s", content)
	}
	if strings.Contains(renderReviewContent(ReviewPacket{Goal: "goal"}), "Open findings") {
		t.Fatal("empty ledger must render no findings section")
	}
}

// TestRenderReviewContentFindingsOnly pins the D-096 re-review packet
// shape: the open findings with detail and evidence, the finding files,
// the scope-creep list, the delta diff and the affected units' harness
// state render; goal, plan, whole-change stat, listing, progress and the
// worker's report never do.
func TestRenderReviewContentFindingsOnly(t *testing.T) {
	content := renderReviewContent(ReviewPacket{
		FindingsOnly: true,
		Units:        []PlanUnit{{Title: "write code", HarnessPassed: true, VerifyCheck: "verify_cmd", VerifyExcerpt: "ok\n"}},
		OpenFindings: []Finding{{ID: "F1", Unit: 0, Title: "missing validation", File: "src/main.go", Detail: "no input check", Evidence: "+func main()", Severity: SeverityBlocking, Status: FindingOpen}},
		Files:        map[string]string{"src/main.go": "package main\n"},
		ScopeCreep:   []string{"docs/notes.md"},
		Diff:         "diff --git a/src/main.go b/src/main.go\n+validated\n",
		// Fields a findings-only packet never carries; set here to prove
		// the renderer does not leak them.
		Goal: "the goal", Plan: Plan{Units: []PlanUnit{{Title: "write code"}}}, DiffStat: "1 file changed",
		Listing: "src/main.go (13 bytes)", Evidence: "worker says done", Progress: []ProgressNote{{Note: "steer"}},
	})
	for _, want := range []string{
		"Re-review of open findings only.",
		"Affected units (harness state):",
		"### write code [harness-verified]",
		"Harness verify_cmd check: passed",
		"- F1 [blocking] (unit 1) src/main.go: missing validation",
		"  detail: no input check",
		"  evidence: +func main()",
		"Files named by the findings (read from disk by the harness):",
		"--- src/main.go ---\npackage main",
		"Changed outside unit scope (judge whether these changes belong; the harness opened no finding for them):\n- docs/notes.md",
		"Diff since the last review (restricted to the finding files and the affected units' scope):",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("findings-only content missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "acceptance criteria") {
		t.Fatalf("findings-only content must not ask for criteria judgement:\n%s", content)
	}
}

// TestRenderReviewContentFullOmitsFindingsOnlySections confirms the
// full round renders no re-review header and no scope-creep or files
// section when the packet carries none.
func TestRenderReviewContentFullOmitsFindingsOnlySections(t *testing.T) {
	content := renderReviewContent(ReviewPacket{Goal: "goal", Diff: "diff --git a/x b/x\n"})
	for _, banned := range []string{"Re-review", "Changed outside unit scope", "Files named by the findings", "Diff since the last review"} {
		if strings.Contains(content, banned) {
			t.Fatalf("full-round content carries %q:\n%s", banned, content)
		}
	}
	if !strings.Contains(content, "Diff to review (restricted to the reviewed units' scope):") {
		t.Fatalf("full-round diff heading missing:\n%s", content)
	}
}

// TestRunReviewRequestCarriesLoopCaps pins D-093: the reviewer's
// loop.Request caps its tool loop at reviewMaxSteps and every result at
// reviewToolResultCap, while a worker turn leaves both at zero (agent
// defaults).
func TestRunReviewRequestCarriesLoopCaps(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(reviewVerdictToolName, `{"decision":"approve"}`)},
	}}
	r := newTestRunner(agent)
	if _, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Goal: "goal"}); err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if req := agent.requests[0]; req.MaxSteps != 4 || req.ToolResultCap != 4096 {
		t.Fatalf("reviewer request MaxSteps=%d ToolResultCap=%d, want 4 and 4096", req.MaxSteps, req.ToolResultCap)
	}

	worker := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`)},
	}}
	r = newTestRunner(worker)
	if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "goal"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if req := worker.requests[0]; req.MaxSteps != 0 || req.ToolResultCap != 0 {
		t.Fatalf("worker request MaxSteps=%d ToolResultCap=%d, want agent defaults (0, 0)", req.MaxSteps, req.ToolResultCap)
	}
}

// TestRunReviewRoutePrecedence pins review's route precedence at the
// request level: ReviewRoute > PlanRoute > Route.
func TestRunReviewRoutePrecedence(t *testing.T) {
	tests := []struct {
		name string
		m    Mission
		want string
	}{
		{"route alone", Mission{ID: "m1", Route: "mini"}, "mini"},
		{"plan_route beats route", Mission{ID: "m1", Route: "mini", PlanRoute: "strong"}, "strong"},
		{"review_route beats plan_route and route", Mission{ID: "m1", Route: "mini", PlanRoute: "strong", ReviewRoute: "reviewer"}, "reviewer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := &scriptedAgent{batches: [][]stream.StreamEvent{
				{toolEndEvent(reviewVerdictToolName, `{"decision":"approve"}`)},
			}}
			r := newTestRunner(agent)
			if _, err := r.RunReview(context.Background(), tc.m, ReviewPacket{Goal: "goal"}); err != nil {
				t.Fatalf("RunReview: %v", err)
			}
			if got := agent.requests[0].Route; got != tc.want {
				t.Fatalf("reviewer request route = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRunReviewPacketRendersArtifactsGoalAndEvidence covers the
// research-mission review gap: reviewers used to reject every round
// for "missing goal" / "missing artifact" because neither was in
// their context. The packet's goal, harness-read artifact contents,
// and the worker's evidence must all reach the reviewer: and no
// empty "Diff to review" section may appear when there is no diff.
func TestRunReviewPacketRendersArtifactsGoalAndEvidence(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("looks solid"), toolEndEvent(reviewVerdictToolName, `{"decision":"approve"}`)},
	}}
	r := newTestRunner(agent)
	packet := ReviewPacket{
		Goal:      "Summarize HTTP 429",
		Units:     []PlanUnit{{Title: "write summary"}},
		Artifacts: map[string]string{"summary.md": "429 means Too Many Requests, per RFC 6585."},
		Evidence:  "Summary written to summary.md, sourced from RFC 6585.",
	}
	v, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, packet)
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
	_, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Diff: "diff"})
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
	v, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Diff: "diff"})
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
// reviewer failure: prose review, never a review_verdict tool call:
// the runner must recover the verdict from a text-form sentinel in the
// recovery turn instead of erroring the round out.
func TestRunReviewFallsBackToTextSentinel(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("Looking at this closely, I think it holds up.")},   // no tool call
		{textEvent(`Confirmed. <review_verdict decision="approve"/>`)}, // recovery: still text-only
	}}
	r := newTestRunner(agent)
	v, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Diff: "diff"})
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !v.Approved {
		t.Fatalf("RunReview verdict via text fallback = %+v, want approved", v)
	}
}

// TestRunReviewFallsBackToTokenJSONTextSentinel mirrors the XML case
// for the token+JSON text form, on a rework decision: findings are
// empty in the text form (acceptable: GapFingerprint of empty findings
// is empty, which the state machine already tolerates).
func TestRunReviewFallsBackToTokenJSONTextSentinel(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("still analyzing")},
		{textEvent("review_verdict\n{\"decision\": \"rework\"}")},
	}}
	r := newTestRunner(agent)
	v, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Diff: "diff"})
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
		{toolEndEvent(planToolName, `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./...","passes":true}]}`)},
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

// TestPlanSessionParsesAssumptions covers issue #446: assumptions is
// optional, so a plan omitting it, sending an empty array, or sending
// populated entries must all parse cleanly with the expected result.
func TestPlanSessionParsesAssumptions(t *testing.T) {
	tests := []struct {
		name string
		json string
		want []PlanAssumption
	}{
		{
			name: "omitted",
			json: `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./..."}]}`,
			want: nil,
		},
		{
			name: "empty",
			json: `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./..."}],"assumptions":[]}`,
			want: nil,
		},
		{
			name: "populated",
			json: `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./..."}],"assumptions":[{"assumption":"no language version was specified","default":"Python 3.12"}]}`,
			want: []PlanAssumption{{Assumption: "no language version was specified", Default: "Python 3.12"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := parsePlan(tt.json)
			if err != nil {
				t.Fatalf("parsePlan: %v", err)
			}
			if len(plan.Assumptions) != len(tt.want) {
				t.Fatalf("Assumptions = %+v, want %+v", plan.Assumptions, tt.want)
			}
			for i := range tt.want {
				if plan.Assumptions[i] != tt.want[i] {
					t.Fatalf("Assumptions[%d] = %+v, want %+v", i, plan.Assumptions[i], tt.want[i])
				}
			}
		})
	}
}

// TestPlanSessionPromptsLengthAwareSplitting pins that the planner is
// told to split a unit whose own deliverable demands a long
// continuous generation (many chapters/sections/files) along its
// natural boundaries, regardless of the goal's subject matter: a
// long single-turn stream truncating mid-generation is what a
// too-few-units plan risks (a real 12-chapter book goal collapsed
// into one unit and truncated on a long model stream, twice).
func TestPlanSessionPromptsLengthAwareSplitting(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./...","passes":true}]}`)},
	}}
	r := newTestRunner(agent)
	if _, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "fix bug"}, ""); err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	system := agent.requests[0].System
	if !strings.Contains(system, "split that unit along its own natural boundaries") {
		t.Fatalf("planner system prompt missing length-aware splitting guidance: %s", system)
	}
}

// TestPlanSessionForcesPlanTool pins D-063: when submit_plan is the
// planning turn's sole tool, the request forces it.
func TestPlanSessionForcesPlanTool(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./...","passes":true}]}`)},
	}}
	r := newTestRunner(agent)
	if _, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "fix bug"}, ""); err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if got := agent.requests[0].ForceTool; got != planToolName {
		t.Fatalf("planner ForceTool = %q, want %s", got, planToolName)
	}
}

// TestPlanSessionNoForceToolWithKB pins the D-063 carve-out: a
// KB-attached mission also offers search_kb/read_kb on the planning
// turn, so forcing submit_plan would make consulting them impossible.
func TestPlanSessionNoForceToolWithKB(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./...","passes":true}]}`)},
	}}
	r := newTestRunner(agent)
	r.kbSearch = func(ctx context.Context, query string, collections []string, mode string, k int) ([]builtin.KBSearchHit, error) {
		return nil, nil
	}
	m := Mission{ID: "m1", Route: "default", Goal: "fix bug"}
	if _, err := r.PlanSession(context.Background(), m, ""); err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if got := agent.requests[0].ForceTool; got != "" {
		t.Fatalf("planner ForceTool = %q, want empty with KB tools offered", got)
	}
}

// TestPlanSessionUsesPlanRoute confirms a mission's plan phase runs on
// PlanRoute when set, instead of Route.
func TestPlanSessionUsesPlanRoute(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./...","passes":true}]}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Route: "mini", PlanRoute: "strong", Goal: "fix bug"}
	if _, err := r.PlanSession(context.Background(), m, ""); err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if got := agent.requests[0].Route; got != "strong" {
		t.Fatalf("planner request route = %q, want plan_route", got)
	}
}

// TestPlanSessionIncludesParentContext confirms a follow-up mission's
// prior outcome digest reaches the planner's user prompt.
func TestPlanSessionIncludesParentContext(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./...","passes":true}]}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{
		ID: "m1", Route: "default", Goal: "fix bug", ParentMissionID: "parent",
		Sources: []SourceEntry{{Source: SourceKindMission, ID: ParentLineageID, MissionID: "parent", Digest: "prior mission fixed the signup bug"}},
	}
	if _, err := r.PlanSession(context.Background(), m, ""); err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	msgs := agent.requests[0].Messages
	content := msgs[len(msgs)-1].Content
	if !strings.Contains(content, "Previous mission outcome:") || !strings.Contains(content, "prior mission fixed the signup bug") {
		t.Fatalf("planner message missing parent context:\n%s", content)
	}
}

// TestPlanSessionIncludesReferencedContext confirms a mission's picked
// composer #-mention references reach the planner's user prompt,
// additive to (not instead of) ParentContext.
func TestPlanSessionIncludesReferencedContext(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./...","passes":true}]}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{
		ID: "m1", Route: "default", Goal: "fix bug", ParentMissionID: "parent",
		Sources: []SourceEntry{
			{Source: SourceKindMission, ID: ParentLineageID, MissionID: "parent", Digest: "prior mission fixed the signup bug"},
			{Source: SourceKindKB, DocID: "doc1", Name: "runbook", Digest: "kb doc: the login flow uses OAuth"},
		},
	}
	if _, err := r.PlanSession(context.Background(), m, ""); err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	msgs := agent.requests[0].Messages
	content := msgs[len(msgs)-1].Content
	if !strings.Contains(content, "Previous mission outcome:") {
		t.Fatalf("planner message missing parent context:\n%s", content)
	}
	if !strings.Contains(content, "Referenced context:") || !strings.Contains(content, "kb doc: the login flow uses OAuth") {
		t.Fatalf("planner message missing referenced context:\n%s", content)
	}
}

// TestPlanSessionIncludesAttachments confirms a create-time PDF
// attachment's markdown reaches the planner's user prompt.
func TestPlanSessionIncludesAttachments(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./...","passes":true}]}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Route: "default", Goal: "fix bug", Sources: []SourceEntry{
		{Source: SourceKindPDF, ID: "att1", Name: "spec.pdf", Markdown: "the spec says fix it this way"},
	}}
	if _, err := r.PlanSession(context.Background(), m, ""); err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	msgs := agent.requests[0].Messages
	content := msgs[len(msgs)-1].Content
	if !strings.Contains(content, "Attached document spec.pdf:") || !strings.Contains(content, "the spec says fix it this way") {
		t.Fatalf("planner message missing attachment:\n%s", content)
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

// TestPlanSessionParsesInfeasible pins D-077: infeasible=true with a
// reason parses to Plan.Infeasible, no units needed.
func TestPlanSessionParsesInfeasible(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"infeasible":true,"reason":"goal forbids the only possible action"}`)},
	}}
	r := newTestRunner(agent)
	spec, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "impossible goal"}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if !spec.Infeasible {
		t.Fatal("PlanSession spec.Infeasible = false, want true")
	}
	if spec.InfeasibleReason != "goal forbids the only possible action" {
		t.Fatalf("PlanSession spec.InfeasibleReason = %q", spec.InfeasibleReason)
	}
	if len(spec.Units) != 0 {
		t.Fatalf("PlanSession spec.Units = %+v, want empty on an infeasible plan", spec.Units)
	}
}

// TestPlanSessionRejectsInfeasibleWithoutReason confirms infeasible=true
// with no reason is rejected, same one-recovery-turn ladder as any
// other invalid plan.
func TestPlanSessionRejectsInfeasibleWithoutReason(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"infeasible":true}`)},
		{toolEndEvent(planToolName, `{"infeasible":true}`)},
	}}
	r := newTestRunner(agent)
	if _, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default"}, ""); err == nil {
		t.Fatal("PlanSession accepted infeasible=true with no reason")
	}
}

// TestPlanSessionRejectsCommandSubstitution guards parsePlan's
// determinism rule: verify_cmd runs harness-side via RunVerify,
// outside the permission chain (D-050's sandbox relaxation for a
// worker/reviewer shell CALL does not apply here): a planner-authored
// verify_cmd containing $(...) is rejected because command
// substitution undermines a reproducible content check, not because of
// any permission concern. The plan should be rejected here, at
// submission, with feedback the planner can act on immediately.
func TestPlanSessionRejectsCommandSubstitution(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Check output","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"test \"$(cat out.md)\" = ok"}]}`)},
		{toolEndEvent(planToolName, `{"units":[{"title":"Check output","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"test \"$(cat out.md)\" = ok"}]}`)},
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
		{toolEndEvent(planToolName, "{\"units\":[{\"title\":\"Check output\",\"artifacts\":[\"out.md\"],\"verify_cmd\":\"test `cat out.md` = ok\"}]}")},
		{toolEndEvent(planToolName, "{\"units\":[{\"title\":\"Check output\",\"artifacts\":[\"out.md\"],\"verify_cmd\":\"test `cat out.md` = ok\"}]}")},
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
		{toolEndEvent(planToolName, `{"units":[{"title":"Check output","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"test \"$(cat out.md)\" = ok"}]}`)},
		{toolEndEvent(planToolName, `{"units":[{"title":"Check output","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"grep -qi ok out.md"}]}`)},
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
// guard doesn't false-positive on real verification commands: a
// content-checking verify_cmd (the existing tautology guard's whole
// point: never a bare echo) and a legitimate input redirect (<, which
// must stay legal: only substitution is banned) both parse cleanly
// in a single turn, no recovery needed.
func TestPlanSessionAcceptsLegitimateVerifyCmd(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[`+
			`{"title":"Check content","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"grep -qi 'foo' out.md"},`+
			`{"title":"Check line count","artifacts":["x"],"criteria":["c1","c2"],"verify_cmd":"test -f x && wc -l < x"}`+
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

// TestPlanSessionRejectsUnitWithNoArtifacts guards D-068: a unit that
// declares zero artifacts leaves the harness nothing to check, which
// is exactly what let amazon.nova-2-lite's empty-contract unit
// "succeed" on a verify_cmd that always exits 0. Recovery with an
// artifact added must succeed.
func TestPlanSessionRejectsUnitWithNoArtifacts(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Integrate Gmail API","verify_cmd":"grep -qi ok out.md"}]}`)},
		{toolEndEvent(planToolName, `{"units":[{"title":"Integrate Gmail API","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"grep -qi ok out.md"}]}`)},
	}}
	r := newTestRunner(agent)
	spec, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "fix bug"}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if len(spec.Units) != 1 || len(spec.Units[0].Artifacts) != 1 {
		t.Fatalf("PlanSession spec = %+v", spec)
	}
	if agent.call != 2 {
		t.Fatalf("expected exactly two turns (original + one recovery), got %d", agent.call)
	}
	recoverReq := agent.requests[1]
	last := recoverReq.Messages[len(recoverReq.Messages)-1]
	if !strings.Contains(last.Content, "Integrate Gmail API") || !strings.Contains(last.Content, "artifact") {
		t.Fatalf("recovery message = %q, want it to name the unit and mention artifacts", last.Content)
	}
}

// TestPlanSessionRejectsNoOpVerifyCmd guards D-068's deny-set on the
// verify_cmd's first shell word: echo/true/:/printf always exit 0
// regardless of content, which is exactly the shape of the real
// amazon.nova-2-lite incident (verify_cmd `echo 'Gmail API integration
// required for actual execution'`, always passing, proving nothing).
// A command that merely CONTAINS one of those words: as an argument,
// or after &&: must still be accepted; the deny-set only looks at
// the first token, by design (see the scoping comment in parsePlan).
func TestPlanSessionRejectsNoOpVerifyCmd(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"echo", `echo 'Gmail API integration required for actual execution'`},
		{"true", `true`},
		{"colon", `:`},
		{"printf", `printf 'done\n'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := fmt.Sprintf(`{"units":[{"title":"Ship it","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":%q}]}`, tc.cmd)
			agent := &scriptedAgent{batches: [][]stream.StreamEvent{
				{toolEndEvent(planToolName, plan)},
				{toolEndEvent(planToolName, plan)},
			}}
			r := newTestRunner(agent)
			_, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default"}, "")
			if err == nil {
				t.Fatalf("PlanSession accepted a no-op verify_cmd %q", tc.cmd)
			}
			if !strings.Contains(err.Error(), "CONTENT") {
				t.Fatalf("PlanSession error = %q, want it to demand a content check", err.Error())
			}
		})
	}
}

// TestPlanSessionAcceptsVerifyCmdContainingEchoWord confirms the
// no-op deny-set only inspects the verify_cmd's FIRST shell word: a
// real content check that happens to contain the word "echo" as an
// argument, or a command containing "echo" later after &&, must pass.
func TestPlanSessionAcceptsVerifyCmdContainingEchoWord(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[`+
			`{"title":"Check literal word","artifacts":["file.md"],"criteria":["c1","c2"],"verify_cmd":"grep -q echo file.md"},`+
			`{"title":"Check then echo","artifacts":["report.md"],"criteria":["c1","c2"],"verify_cmd":"test -s report.md && grep -q x report.md"}`+
			`]}`)},
	}}
	r := newTestRunner(agent)
	spec, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default"}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if len(spec.Units) != 2 {
		t.Fatalf("PlanSession spec = %+v", spec)
	}
	if agent.call != 1 {
		t.Fatalf("expected exactly one turn (no recovery needed), got %d", agent.call)
	}
}

// TestPlanSessionRejectsUnterminatedQuote guards D-068's POSIX shell
// syntax gate: verify_cmd is run through the REAL /bin/sh -n (per this
// repo's real-shell-tests convention: a fake shell would forgive
// exactly the quoting bugs this check exists to catch). This mirrors
// the real amazon.nova-lite incident, whose unterminated quote burned
// execute iterations before failing at verify time, well after the
// plan was accepted.
func TestPlanSessionRejectsUnterminatedQuote(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this machine")
	}
	bad := `grep -q "unterminated report.md`
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, fmt.Sprintf(`{"units":[{"title":"Ship it","artifacts":["report.md"],"criteria":["c1","c2"],"verify_cmd":%q}]}`, bad))},
		{toolEndEvent(planToolName, fmt.Sprintf(`{"units":[{"title":"Ship it","artifacts":["report.md"],"criteria":["c1","c2"],"verify_cmd":%q}]}`, bad))},
	}}
	r := newTestRunner(agent)
	_, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default"}, "")
	if err == nil {
		t.Fatal("PlanSession accepted a verify_cmd with an unterminated quote")
	}
	if !strings.Contains(err.Error(), "does not parse as POSIX shell") {
		t.Fatalf("PlanSession error = %q, want it to name the shell parse failure", err.Error())
	}
}

// TestPlanSessionAcceptsValidQuoting confirms the /bin/sh -n gate
// doesn't false-positive on real, properly quoted verify_cmds.
func TestPlanSessionAcceptsValidQuoting(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this machine")
	}
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Ship it","artifacts":["report.md"],"criteria":["c1","c2"],"verify_cmd":"grep -qi 'retry-after' report.md"}]}`)},
	}}
	r := newTestRunner(agent)
	spec, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default"}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if len(spec.Units) != 1 {
		t.Fatalf("PlanSession spec = %+v", spec)
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
// injected message names what was wrong: not a byte-identical retry
// of the original prompt, which previously produced the same failure
// every time against a nondeterministic model.
func TestPlanSessionRecoversWithFeedback(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("here's my plan in prose, no tool call")},
		{toolEndEvent(planToolName, `{"units":[{"title":"Fix it","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"grep -qi ok out.md"}]}`)},
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
// existed: that gap is what stranded a real mission for its full
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
// had been delivered so far: the direct signal that distinguishes
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
// second call was still genuinely blocked awaiting a decision: the
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
		// after resultsDelivered==1 (right after call1, prematurely):
		// this asserts the clear only happens once BOTH results (2) have
		// actually been delivered.
		t.Fatalf("clear fired after %v results delivered, want exactly one clear after both (2) results", parker.resultsDeliveredAtClear)
	}
}

// countingResultAgent wraps scriptedAgent's fixed batch with a live
// counter the test parker reads at clear-time: the pre-buffered
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

func doneEventMeta(provider, model string) stream.StreamEvent {
	return stream.StreamEvent{Type: stream.EventDone, Meta: &stream.Meta{Provider: provider, Model: model}}
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
// fine: the sentinel outcome decides.
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

// TestRunTurnCarriesServedProviderAndModel covers issue #507: runTurn
// must surface the last stream meta event's provider/model on
// turnResult, the entry that actually served the turn after any
// failover.
func TestRunTurnCarriesServedProviderAndModel(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`), doneEventMeta("OpenAI Responses", "gpt-5.3-codex")},
	}}
	r := newTestRunner(agent)
	res, err := r.runTurn(context.Background(), loop.Request{MissionID: "m1"}, missionStatusToolName, PhaseGenerate)
	if err != nil {
		t.Fatalf("runTurn: %v", err)
	}
	if res.provider != "OpenAI Responses" || res.model != "gpt-5.3-codex" {
		t.Fatalf("turnResult provider/model = %q/%q, want OpenAI Responses/gpt-5.3-codex", res.provider, res.model)
	}
}

// TestRunTurnOmitsProviderModelWhenNeverServed covers issue #507: a
// turn that errors before any provider answered must never guess a
// provider/model.
func TestRunTurnOmitsProviderModelWhenNeverServed(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{{Type: stream.EventError, Err: &stream.StreamError{Message: "boom"}}},
	}}
	r := newTestRunner(agent)
	res, err := r.runTurn(context.Background(), loop.Request{MissionID: "m1"}, missionStatusToolName, PhaseGenerate)
	if err == nil {
		t.Fatal("runTurn: expected an error")
	}
	if res.provider != "" || res.model != "" {
		t.Fatalf("turnResult provider/model = %q/%q, want both empty", res.provider, res.model)
	}
}

// TestRunWorkerGetsMissionScopedShell: the worker turn must carry a
// turn-scoped shell tool rooted in the mission's own workspace: the
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

// TestOversightRoute pins discover/plan/replan's route resolution:
// PlanRoute wins when set, Route otherwise: exact current behavior
// when PlanRoute is empty.
func TestOversightRoute(t *testing.T) {
	tests := []struct {
		name string
		m    Mission
		want string
	}{
		{"empty plan_route falls back to route", Mission{Route: "mini"}, "mini"},
		{"plan_route wins when set", Mission{Route: "mini", PlanRoute: "strong"}, "strong"},
		{"review_route never consulted", Mission{Route: "mini", ReviewRoute: "reviewer-only"}, "mini"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := oversightRoute(tc.m); got != tc.want {
				t.Fatalf("oversightRoute = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReviewRoute pins review's route precedence: ReviewRoute (the
// existing, already-shipped review-only override) beats PlanRoute,
// which beats Route.
func TestReviewRoute(t *testing.T) {
	tests := []struct {
		name string
		m    Mission
		want string
	}{
		{"no overrides falls back to route", Mission{Route: "mini"}, "mini"},
		{"plan_route wins over route", Mission{Route: "mini", PlanRoute: "strong"}, "strong"},
		{"review_route wins over plan_route", Mission{Route: "mini", PlanRoute: "strong", ReviewRoute: "reviewer"}, "reviewer"},
		{"review_route wins over route alone", Mission{Route: "mini", ReviewRoute: "reviewer"}, "reviewer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewRoute(tc.m); got != tc.want {
				t.Fatalf("reviewRoute = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPhaseRoute pins the mission.turn event's route field (issue
// #473) to whichever route helper the phase's own request builder
// actually uses, and to "" for result (no LLM turn runs there).
func TestPhaseRoute(t *testing.T) {
	tests := []struct {
		name string
		m    Mission
		want string
	}{
		{"discover uses oversight route", Mission{Phase: PhaseDiscover, Route: "mini", PlanRoute: "strong"}, "strong"},
		{"plan uses oversight route", Mission{Phase: PhasePlan, Route: "mini", PlanRoute: "strong"}, "strong"},
		{"generate uses worker route", Mission{Phase: PhaseGenerate, Route: "mini", EscalationRoute: "coding", ConsecutiveFailures: 1}, "coding"},
		{"prove uses review route", Mission{Phase: PhaseProve, Route: "mini", ReviewRoute: "reviewer"}, "reviewer"},
		{"result runs no LLM turn", Mission{Phase: PhaseResult, Route: "mini"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := phaseRoute(tc.m); got != tc.want {
				t.Fatalf("phaseRoute = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWorkerModel pins route_model's precedence: it backs execute
// exactly like Route, but must NOT carry over once workerRoute has
// swapped to EscalationRoute: it names an entry in the base route's
// chain, not the escalation route's.
func TestWorkerModel(t *testing.T) {
	tests := []struct {
		name string
		m    Mission
		want string
	}{
		{"no pin set", Mission{Route: "mini"}, ""},
		{"pin applies on the base route", Mission{Route: "mini", RouteModel: "OpenAI/gpt-5-mini"}, "OpenAI/gpt-5-mini"},
		{"pin cleared once escalated on failure", Mission{Route: "mini", RouteModel: "OpenAI/gpt-5-mini", EscalationRoute: "coding", ConsecutiveFailures: 1}, ""},
		{"pin cleared once escalated on stall", Mission{Route: "mini", RouteModel: "OpenAI/gpt-5-mini", EscalationRoute: "coding", StallCount: 1}, ""},
		{"pin survives when escalation route is configured but not triggered", Mission{Route: "mini", RouteModel: "OpenAI/gpt-5-mini", EscalationRoute: "coding"}, "OpenAI/gpt-5-mini"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := workerModel(tc.m); got != tc.want {
				t.Fatalf("workerModel = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOversightModel pins discover/plan's model-pin precedence: mirrors
// oversightRoute exactly (plan_route_model wins when set, else
// route_model).
func TestOversightModel(t *testing.T) {
	tests := []struct {
		name string
		m    Mission
		want string
	}{
		{"empty plan_route_model falls back to route_model", Mission{RouteModel: "GLM/glm-5.3"}, "GLM/glm-5.3"},
		{"plan_route_model wins when set", Mission{RouteModel: "GLM/glm-5.3", PlanRouteModel: "Anthropic/claude-sonnet-5"}, "Anthropic/claude-sonnet-5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := oversightModel(tc.m); got != tc.want {
				t.Fatalf("oversightModel = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReviewModel pins review's three-level model precedence:
// review_route_model > plan_route_model > route_model, tracking
// reviewRoute's own route precedence exactly.
func TestReviewModel(t *testing.T) {
	tests := []struct {
		name string
		m    Mission
		want string
	}{
		{"no overrides falls back to route_model", Mission{RouteModel: "GLM/glm-5.3"}, "GLM/glm-5.3"},
		{"plan_route_model wins over route_model", Mission{RouteModel: "GLM/glm-5.3", PlanRouteModel: "Anthropic/claude-sonnet-5"}, "Anthropic/claude-sonnet-5"},
		{"review_route_model wins over plan_route_model", Mission{RouteModel: "GLM/glm-5.3", PlanRouteModel: "Anthropic/claude-sonnet-5", ReviewRouteModel: "OpenAI/gpt-5-mini"}, "OpenAI/gpt-5-mini"},
		{"review_route_model wins over route_model alone", Mission{RouteModel: "GLM/glm-5.3", ReviewRouteModel: "OpenAI/gpt-5-mini"}, "OpenAI/gpt-5-mini"},
		{"only plan pin set: a mission with only plan_route_model must not leak an unpinned review model", Mission{PlanRouteModel: "Anthropic/claude-sonnet-5"}, "Anthropic/claude-sonnet-5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewModel(tc.m); got != tc.want {
				t.Fatalf("reviewModel = %q, want %q", got, tc.want)
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
	if got := agent.requests[0].ModelHint; got != "" {
		t.Fatalf("worker request model hint = %q, want empty once escalated (pin names the base route's chain, not the escalation route's)", got)
	}
}

// TestRunWorkerAppliesRouteModelPin confirms RouteModel reaches the
// loop request as ModelHint on a clean (non-escalated) run.
func TestRunWorkerAppliesRouteModelPin(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Route: "mini", RouteModel: "OpenAI/gpt-5-mini", Workspace: "/workspace/missions/m1"}
	if _, _, err := r.RunWorker(context.Background(), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if got := agent.requests[0].ModelHint; got != "OpenAI/gpt-5-mini" {
		t.Fatalf("worker request model hint = %q, want the route_model pin", got)
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

// TestRunWorkerIncludesConnectorReadsWhenResolverSet confirms RunWorker
// layers the connector reads resolver's tools into ExtraTools:
// scheduled general missions (daily inbox digest) need gmail/calendar
// reads despite BuiltinsOnly.
func TestRunWorkerIncludesConnectorReadsWhenResolverSet(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`)},
	}}
	r := newTestRunner(agent)
	var gotAgentID string
	r.connectorReads = func(ctx context.Context, agentID string) []*tools.Tool {
		gotAgentID = agentID
		return []*tools.Tool{{Name: "gmail_gmail_search", ReadOnly: true}}
	}
	m := Mission{ID: "m1", AgentID: "a1", Route: "default", Workspace: "/workspace/missions/m1"}
	if _, _, err := r.RunWorker(context.Background(), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if gotAgentID != "a1" {
		t.Fatalf("connectorReads called with agentID %q, want a1", gotAgentID)
	}
	var names []string
	for _, tool := range agent.requests[0].ExtraTools {
		names = append(names, tool.Name)
	}
	if !slices.Contains(names, "gmail_gmail_search") {
		t.Fatalf("worker ExtraTools = %v, want gmail_gmail_search", names)
	}
}

// TestRunWorkerOmitsConnectorReadsWhenResolverUnset confirms unset
// connectorReads (today's default) never adds connector tools:
// nil-safe, same as before this existed.
func TestRunWorkerOmitsConnectorReadsWhenResolverUnset(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Route: "default", Workspace: "/workspace/missions/m1"}
	if _, _, err := r.RunWorker(context.Background(), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	for _, tool := range agent.requests[0].ExtraTools {
		if strings.Contains(tool.Name, "gmail") || strings.Contains(tool.Name, "calendar") {
			t.Fatalf("worker ExtraTools = %v, want no connector tools", tool.Name)
		}
	}
}

// TestDiscoverSessionIncludesConnectorReadsWhenResolverSet mirrors
// TestRunWorkerIncludesConnectorReadsWhenResolverSet for the discover
// phase: scheduled missions may need connector reads before planning
// too.
func TestDiscoverSessionIncludesConnectorReadsWhenResolverSet(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(discoverNotesToolName, `{"findings":"ok"}`)},
	}}
	r := newTestRunner(agent)
	r.connectorReads = func(ctx context.Context, agentID string) []*tools.Tool {
		return []*tools.Tool{{Name: "google-calendar_calendar_list_events", ReadOnly: true}}
	}
	m := Mission{ID: "m1", AgentID: "a1", Route: "default"}
	if _, _, _, err := r.DiscoverSession(context.Background(), m); err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	var names []string
	for _, tool := range agent.requests[0].ExtraTools {
		names = append(names, tool.Name)
	}
	if !slices.Contains(names, "google-calendar_calendar_list_events") {
		t.Fatalf("discover ExtraTools = %v, want google-calendar_calendar_list_events", names)
	}
}

// shellExtraTool pulls the "shell" tool out of a mission's ExtraTools
// list: helper for tests exercising missionTools' Runner wiring
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
	r.sandbox = func(ctx context.Context, missionID, environment, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
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
	r.sandbox = func(ctx context.Context, missionID, environment, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
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
// caps its own: a runaway sandboxed command must not balloon memory
// or context just because the local exec path isn't the one running.
func TestMissionToolsSandboxCapsOutput(t *testing.T) {
	r := newTestRunner(&scriptedAgent{})
	over := strings.Repeat("x", shellOutputCap+1024)
	r.sandbox = func(ctx context.Context, missionID, environment, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
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
// the mission via parkNotifier as a mission.permission_denied event:
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

// TestRunWorkerEmitsToolCallTraceInOrder covers issue #369's acceptance
// criterion 1: every finished tool call in a worker turn reaches the
// mission via parkNotifier as a mission.tool_call trace entry, in call
// order, tagged with the generate phase and the correct outcome
// classification (ok/denied/error).
func TestRunWorkerEmitsToolCallTraceInOrder(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		finishedToolResultEvent("call1", "search_kb", "ok", `{"query":"first"}`, 12),
		finishedToolResultEvent("call2", "shell", "denied", `{"command":"rm -rf /"}`, 3),
		finishedToolResultEvent("call3", "write_file", "error", `{"path":"x"}`, 40),
		toolEndEvent(missionStatusToolName, `{"outcome":"retry","analysis":"blocked"}`),
	}}}
	parker := &fakeParker{}
	r := newTestRunnerWithParker(agent, parker)
	if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	want := []toolCallRecord{
		{"m1", "generate", "search_kb", `{"query":"first"}`, "ok", 12, nil},
		{"m1", "generate", "shell", `{"command":"rm -rf /"}`, "denied", 3, nil},
		{"m1", "generate", "write_file", `{"path":"x"}`, "error", 40, nil},
	}
	if len(parker.toolCalls) != len(want) {
		t.Fatalf("toolCalls = %+v, want %d entries", parker.toolCalls, len(want))
	}
	for i, got := range parker.toolCalls {
		if !toolCallRecordsEqual(got, want[i]) {
			t.Fatalf("toolCalls[%d] = %+v, want %+v", i, got, want[i])
		}
	}
}

// toolCallRecordsEqual compares two toolCallRecord values: kbHits is a
// slice (not comparable with ==), so this stands in for a plain struct
// equality check.
func toolCallRecordsEqual(a, b toolCallRecord) bool {
	return a.missionID == b.missionID && a.phase == b.phase && a.tool == b.tool &&
		a.digest == b.digest && a.status == b.status && a.durationMs == b.durationMs &&
		slices.Equal(a.kbHits, b.kbHits)
}

// TestToolCallDigestCapsLargeArgs is the digest-capping table test
// (issue #369's cap requirement): args past toolCallDigestCap bytes are
// truncated with an ellipsis, never carried whole into the event.
func TestToolCallDigestCapsLargeArgs(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{"empty", "", ""},
		{"short", `{"a":1}`, `{"a":1}`},
		{"exactly at cap", strings.Repeat("a", toolCallDigestCap), strings.Repeat("a", toolCallDigestCap)},
		{"over cap", strings.Repeat("a", toolCallDigestCap+500), strings.Repeat("a", toolCallDigestCap) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolCallDigest(json.RawMessage(tt.args))
			if got != tt.want {
				t.Fatalf("toolCallDigest(%d bytes) len=%d, want len=%d", len(tt.args), len(got), len(tt.want))
			}
		})
	}
}

// TestKBSearchHitTraceCapsHitCount covers issue #413's bounding
// requirement: kbSearchHitTrace never records more than kbSearchHitCap
// hits, even given a malformed or unexpectedly long digest.
func TestKBSearchHitTraceCapsHitCount(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= kbSearchHitCap+5; i++ {
		fmt.Fprintf(&b, "%d. Doc %d\nSource: kb://doc-%d (score 0.5)\ncontent\n\n", i, i, i)
	}
	got := kbSearchHitTrace(b.String())
	if len(got) != kbSearchHitCap {
		t.Fatalf("len(kbSearchHitTrace(...)) = %d, want %d", len(got), kbSearchHitCap)
	}
}

// TestRunTurnBareCloseIsError pins the missions half of D-044: a loop
// channel that closes with no terminal event at all (every producer
// lost it to the turn deadline racing a stream cut) must surface as an
// infra error: previously it returned a clean empty verdict, which
// the caller read as a missing sentinel and burned a recovery re-run
// plus a forced retry for one silent infra failure.
// TestDiscoverSessionSentinelPresent mirrors TestRunWorkerSentinelPresent:
// an discover_notes call on the first turn is trusted directly, no
// recovery needed.
func TestDiscoverSessionSentinelPresent(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("looked around"), toolEndEvent(discoverNotesToolName, `{"findings":"no prior implementation; goal is self-contained"}`)},
	}}
	r := newTestRunner(agent)
	notes, _, _, err := r.DiscoverSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "test"})
	if err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	if notes != "no prior implementation; goal is self-contained" {
		t.Fatalf("DiscoverSession notes = %q", notes)
	}
	if agent.call != 1 {
		t.Fatalf("expected exactly one turn when the sentinel is present, got %d", agent.call)
	}
}

// TestDiscoverSessionUsesPlanRoute confirms discover runs on PlanRoute
// when set, instead of Route.
func TestDiscoverSessionUsesPlanRoute(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(discoverNotesToolName, `{"findings":"no prior implementation"}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Route: "mini", PlanRoute: "strong", Goal: "test"}
	if _, _, _, err := r.DiscoverSession(context.Background(), m); err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	if got := agent.requests[0].Route; got != "strong" {
		t.Fatalf("discoverer request route = %q, want plan_route", got)
	}
}

// TestDiscoverSessionIncludesParentContext confirms a follow-up
// mission's prior outcome digest reaches the discoverer's user prompt.
func TestDiscoverSessionIncludesParentContext(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(discoverNotesToolName, `{"findings":"no prior implementation"}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{
		ID: "m1", Route: "default", Goal: "test", ParentMissionID: "parent",
		Sources: []SourceEntry{{Source: SourceKindMission, ID: ParentLineageID, MissionID: "parent", Digest: "prior mission fixed the signup bug"}},
	}
	if _, _, _, err := r.DiscoverSession(context.Background(), m); err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	msgs := agent.requests[0].Messages
	content := msgs[len(msgs)-1].Content
	if !strings.Contains(content, "Previous mission outcome:") || !strings.Contains(content, "prior mission fixed the signup bug") {
		t.Fatalf("discoverer message missing parent context:\n%s", content)
	}
}

// TestDiscoverSessionIncludesReferencedContext confirms a mission's
// picked composer #-mention references reach the discoverer's user
// prompt, additive to (not instead of) ParentContext.
func TestDiscoverSessionIncludesReferencedContext(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(discoverNotesToolName, `{"findings":"no prior implementation"}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{
		ID: "m1", Route: "default", Goal: "test", ParentMissionID: "parent",
		Sources: []SourceEntry{
			{Source: SourceKindMission, ID: ParentLineageID, MissionID: "parent", Digest: "prior mission fixed the signup bug"},
			{Source: SourceKindKB, DocID: "doc1", Name: "runbook", Digest: "kb doc: the login flow uses OAuth"},
		},
	}
	if _, _, _, err := r.DiscoverSession(context.Background(), m); err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	msgs := agent.requests[0].Messages
	content := msgs[len(msgs)-1].Content
	if !strings.Contains(content, "Previous mission outcome:") {
		t.Fatalf("discoverer message missing parent context:\n%s", content)
	}
	if !strings.Contains(content, "Referenced context:") || !strings.Contains(content, "kb doc: the login flow uses OAuth") {
		t.Fatalf("discoverer message missing referenced context:\n%s", content)
	}
}

// TestDiscoverSessionIncludesAttachments confirms a create-time PDF
// attachment's markdown reaches the discoverer's user prompt.
func TestDiscoverSessionIncludesAttachments(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(discoverNotesToolName, `{"findings":"no prior implementation"}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Route: "default", Goal: "test", Sources: []SourceEntry{
		{Source: SourceKindPDF, ID: "att1", Name: "spec.pdf", Markdown: "the spec says fix it this way"},
	}}
	if _, _, _, err := r.DiscoverSession(context.Background(), m); err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	msgs := agent.requests[0].Messages
	content := msgs[len(msgs)-1].Content
	if !strings.Contains(content, "Attached document spec.pdf:") || !strings.Contains(content, "the spec says fix it this way") {
		t.Fatalf("discoverer message missing attachment:\n%s", content)
	}
}

// TestDiscoverSessionRecoversWhenSentinelMissingThenPresent mirrors
// fakeEnvironmentSink records SetEnvironment calls.
type fakeEnvironmentSink struct {
	calls []string
}

func (f *fakeEnvironmentSink) SetEnvironment(ctx context.Context, id, environment, marker string) error {
	f.calls = append(f.calls, id+":"+environment+":"+marker)
	return nil
}

// TestDiscoverSessionReportsEnvironment covers issue #495: a coding
// mission with no environment yet gets the discover turn's registered
// value written through the sink; base and unknown keys never do.
func TestDiscoverSessionReportsEnvironment(t *testing.T) {
	cases := []struct {
		name      string
		mission   Mission
		args      string
		wantCalls []string
	}{
		{"registered key on an undecided coding mission", Mission{ID: "m1", Kind: KindCoding, Route: "default", Goal: "build a vite app"},
			`{"findings":"fresh repo","environment":"node"}`, []string{"m1:node:discover"}},
		{"base is not a detection", Mission{ID: "m1", Kind: KindCoding, Route: "default", Goal: "docs"},
			`{"findings":"docs only","environment":"base"}`, nil},
		{"unknown key ignored", Mission{ID: "m1", Kind: KindCoding, Route: "default", Goal: "rust cli"},
			`{"findings":"cargo project","environment":"rust"}`, nil},
		{"already decided by markers", Mission{ID: "m1", Kind: KindCoding, Environment: "go", Route: "default", Goal: "x"},
			`{"findings":"go module","environment":"node"}`, nil},
		{"general missions never set one", Mission{ID: "m1", Kind: "general", Route: "default", Goal: "x"},
			`{"findings":"n/a","environment":"node"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := &scriptedAgent{batches: [][]stream.StreamEvent{
				{toolEndEvent(discoverNotesToolName, tc.args)},
			}}
			r := newTestRunner(agent)
			sink := &fakeEnvironmentSink{}
			r.SetEnvironmentSink(sink)
			if _, _, _, err := r.DiscoverSession(context.Background(), tc.mission); err != nil {
				t.Fatalf("DiscoverSession: %v", err)
			}
			if strings.Join(sink.calls, ",") != strings.Join(tc.wantCalls, ",") {
				t.Fatalf("SetEnvironment calls = %v, want %v", sink.calls, tc.wantCalls)
			}
		})
	}
}

// TestDiscoverSessionPrefixesUnsupportedStack confirms a stack the
// sandbox has no image for lands at the top of the findings with the
// bootstrap instruction the planner needs (issue #495).
func TestDiscoverSessionPrefixesUnsupportedStack(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(discoverNotesToolName, `{"findings":"Cargo.toml at the root","stack":"Rust CLI"}`)},
	}}
	r := newTestRunner(agent)
	notes, _, _, err := r.DiscoverSession(context.Background(), Mission{ID: "m1", Kind: KindCoding, Route: "default", Goal: "test"})
	if err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	if !strings.HasPrefix(notes, "Stack: Rust CLI. The sandbox has no preinstalled toolchain") {
		t.Fatalf("notes = %q, want the stack note first", notes)
	}
	if !strings.HasSuffix(notes, "Cargo.toml at the root") {
		t.Fatalf("notes = %q, want the findings kept after the stack note", notes)
	}
}

// RunWorker's recovery ladder: a missing sentinel on the first turn
// gets one recovery re-run before the sentinel is trusted.
func TestDiscoverSessionRecoversWhenSentinelMissingThenPresent(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("still discovering")}, // no sentinel
		{textEvent("done discovering"), toolEndEvent(discoverNotesToolName, `{"findings":"found a reusable config loader"}`)},
	}}
	r := newTestRunner(agent)
	notes, _, _, err := r.DiscoverSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "test"})
	if err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	if notes != "found a reusable config loader" {
		t.Fatalf("DiscoverSession notes = %q", notes)
	}
	if agent.call != 2 {
		t.Fatalf("expected exactly two turns (original + one recovery), got %d", agent.call)
	}
}

// TestDiscoverSessionFallsBackToTextSentinel covers a discover turn
// that expresses discover_notes as text (XML-ish tag), never as a tool
// call: same fallback RunWorker/RunReview already rely on.
func TestDiscoverSessionFallsBackToTextSentinel(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("no tool call here")},
		{textEvent(`Done. <discover_notes findings="the goal needs no exploration; it is self-contained"/>`)},
	}}
	r := newTestRunner(agent)
	notes, _, _, err := r.DiscoverSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "test"})
	if err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	if notes != "the goal needs no exploration; it is self-contained" {
		t.Fatalf("DiscoverSession notes via text fallback = %q", notes)
	}
}

// TestDiscoverSessionFallsBackToRawText is the advisory-phase contract:
// when NEITHER turn produces an discover_notes call in any form, the raw
// turn text becomes the notes: never an error, since discover findings
// are advisory input to the planner, not a gate on mission progress.
func TestDiscoverSessionFallsBackToRawText(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{textEvent("Looked at the workspace, nothing notable.")},
		{textEvent("Confirmed, nothing else to add.")},
	}}
	r := newTestRunner(agent)
	notes, _, _, err := r.DiscoverSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "test"})
	if err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	want := "Looked at the workspace, nothing notable.\nConfirmed, nothing else to add."
	if notes != want {
		t.Fatalf("DiscoverSession fallback notes = %q, want %q", notes, want)
	}
	if agent.call != 2 {
		t.Fatalf("expected exactly two turns (original + one recovery), got %d", agent.call)
	}
}

// TestDiscoverSessionStreamErrorPropagates confirms an infra-level
// stream error (not a missing sentinel) is a real error, distinct from
// the advisory "no sentinel, use raw text" fallback path.
func TestDiscoverSessionStreamErrorPropagates(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		{Type: stream.EventError, Err: &stream.StreamError{Message: "connection lost"}},
	}}}
	r := newTestRunner(agent)
	if _, _, _, err := r.DiscoverSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "test"}); err == nil {
		t.Fatal("DiscoverSession: expected the stream error to propagate")
	}
}

// TestDiscoverSessionGetsShellButNotWriteFile confirms the discover
// turn's ExtraTools include a mission-scoped shell (for read-only
// exploration) but never write_file: the generate phase does the
// actual work, discover must not create or modify files.
func TestDiscoverSessionGetsShellButNotWriteFile(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(discoverNotesToolName, `{"findings":"nothing notable"}`)},
	}}
	r := newTestRunner(agent)
	m := Mission{ID: "m1", Route: "default", Goal: "test", Workspace: "/workspace/missions/m1"}
	if _, _, _, err := r.DiscoverSession(context.Background(), m); err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	var names []string
	for _, tool := range agent.requests[0].ExtraTools {
		names = append(names, tool.Name)
	}
	if !slices.Contains(names, "shell") {
		t.Fatalf("discover ExtraTools = %v, want a mission-scoped shell", names)
	}
	if slices.Contains(names, "write_file") {
		t.Fatalf("discover ExtraTools = %v, must not include write_file", names)
	}
	if !slices.Contains(names, discoverNotesToolName) {
		t.Fatalf("discover ExtraTools = %v, want the discover_notes sentinel", names)
	}
	if agent.requests[0].ToolAllow != nil {
		t.Fatalf("discover ToolAllow = %v, want nil so base tools (search_web/fetch_url) stay available", agent.requests[0].ToolAllow)
	}
}

// TestDiscoverSessionKBNudge pins issue #367: the discoverer's system
// prompt nudges it to search_kb before declaring a goal self-contained,
// exactly when search_kb is offered (kbSearch wired); no backend means
// no mention of a tool the model doesn't have.
func TestDiscoverSessionKBNudge(t *testing.T) {
	notesBatch := [][]stream.StreamEvent{
		{toolEndEvent(discoverNotesToolName, `{"findings":"nothing notable"}`)},
	}

	t.Run("no backend wired", func(t *testing.T) {
		agent := &scriptedAgent{batches: notesBatch}
		r := newTestRunner(agent)
		if _, _, _, err := r.DiscoverSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "test"}); err != nil {
			t.Fatalf("DiscoverSession: %v", err)
		}
		if strings.Contains(agent.requests[0].System, "search_kb") {
			t.Fatalf("discover system prompt mentions search_kb with no backend wired: %s", agent.requests[0].System)
		}
	})

	t.Run("backend wired", func(t *testing.T) {
		agent := &scriptedAgent{batches: notesBatch}
		r := newTestRunner(agent)
		r.kbSearch = func(ctx context.Context, query string, boostCollections []string, mode string, k int) ([]builtin.KBSearchHit, error) {
			return nil, nil
		}
		if _, _, _, err := r.DiscoverSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "test"}); err != nil {
			t.Fatalf("DiscoverSession: %v", err)
		}
		system := agent.requests[0].System
		if !strings.Contains(system, "search_kb") {
			t.Fatalf("discover system prompt missing search_kb nudge with backend wired: %s", system)
		}
	})
}

func TestRunTurnBareCloseIsError(t *testing.T) {
	agent := &scriptedAgent{rawClose: true, batches: [][]stream.StreamEvent{{
		textEvent("partial work"),
	}}}
	r := newTestRunner(agent)
	res, err := r.runTurn(context.Background(), loop.Request{MissionID: "m1"}, missionStatusToolName, PhaseGenerate)
	if err == nil || !strings.Contains(err.Error(), "without a terminal event") {
		t.Fatalf("err = %v, want no-terminal error", err)
	}
	if res.text != "partial work" {
		t.Fatalf("text = %q, want the partial preserved", res.text)
	}
}

// TestRunTurnIncompleteIsError: a cut-off stream (EventIncomplete
// terminal) is an infra failure, not a short clean answer: it must
// not flow into sentinel parsing as if the worker finished.
func TestRunTurnIncompleteIsError(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		textEvent("partial"),
		{Type: stream.EventIncomplete, Text: "stream ended without a terminal event"},
	}}}
	r := newTestRunner(agent)
	if _, err := r.runTurn(context.Background(), loop.Request{MissionID: "m1"}, missionStatusToolName, PhaseGenerate); err == nil || !strings.Contains(err.Error(), "incomplete stream") {
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
	if _, err := r.runTurn(context.Background(), loop.Request{MissionID: "m1"}, missionStatusToolName, PhaseGenerate); err == nil || !strings.Contains(err.Error(), "provider stream error") {
		t.Fatalf("err = %v, want generic provider stream error", err)
	}
}

// hangingAgent is a fake agentStream whose Start never sends and never
// closes its channel on its own: it only closes once ctx is
// cancelled, modeling a stream that emits no chunk, no terminal, and
// no error (the observed production hang: a worker turn silently wedged
// for 35+ minutes with driveTimeBound, at 4h, nowhere near catching it).
type hangingAgent struct{}

func (hangingAgent) Start(ctx context.Context, req loop.Request) (<-chan stream.StreamEvent, error) {
	ch := make(chan stream.StreamEvent)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// TestRunTurnTimesOutOnHungStream: runTurn must not block forever on a
// stream that never emits anything: turnTimeout bounds it, and the
// resulting bare channel close falls into the same "stream ended
// without a terminal event" retryable-failure path as a real stream
// cut, rather than hanging the driver's Drive goroutine.
func TestRunTurnTimesOutOnHungStream(t *testing.T) {
	old := turnTimeout
	turnTimeout = 50 * time.Millisecond
	defer func() { turnTimeout = old }()

	r := newTestRunner(hangingAgent{})
	done := make(chan struct{})
	var err error
	go func() {
		_, err = r.runTurn(context.Background(), loop.Request{MissionID: "m1"}, missionStatusToolName, PhaseGenerate)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runTurn did not return after turnTimeout elapsed")
	}
	if err == nil || !strings.Contains(err.Error(), "without a terminal event") {
		t.Fatalf("err = %v, want no-terminal error", err)
	}
}

// TestMissionRunnerRequestsAreBuiltinsOnly guards the security fix:
// every loop.Request the native runner builds: worker, discoverer,
// reviewer, planner: must set BuiltinsOnly, so a mission turn's base
// tool surface never includes connector tools (e.g. a write-capable
// GitHub MCP token) or the chat-only mission list/get/push tools. A
// missed phase here would silently reopen the side-channel the fix
// closes.
func TestMissionRunnerRequestsAreBuiltinsOnly(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`)},
	}}
	r := newTestRunner(agent)
	if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if !agent.requests[len(agent.requests)-1].BuiltinsOnly {
		t.Fatal("RunWorker's request must set BuiltinsOnly")
	}

	agent = &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(discoverNotesToolName, `{"findings":"none"}`)},
	}}
	r = newTestRunner(agent)
	if _, _, _, err := r.DiscoverSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "test"}); err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	if !agent.requests[len(agent.requests)-1].BuiltinsOnly {
		t.Fatal("DiscoverSession's request must set BuiltinsOnly")
	}

	agent = &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(reviewVerdictToolName, `{"decision":"approve"}`)},
	}}
	r = newTestRunner(agent)
	if _, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Goal: "goal"}); err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !agent.requests[len(agent.requests)-1].BuiltinsOnly {
		t.Fatal("RunReview's request must set BuiltinsOnly")
	}

	agent = &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"do it","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"grep -qi ok out.md"}]}`)},
	}}
	r = newTestRunner(agent)
	if _, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "fix bug"}, ""); err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if !agent.requests[len(agent.requests)-1].BuiltinsOnly {
		t.Fatal("PlanSession's request must set BuiltinsOnly")
	}
}

// okToolResultEvent is toolResultEvent's ok-status counterpart with a
// name and result text. Content is what runner.go's trace parsing
// (kb_hits, search_web URLs) reads (issue #418); Digest mirrors it
// here since the loop only diverges the two when Digest is truncated,
// which this fixed-text helper never triggers.
func okToolResultEvent(id, name, content string) stream.StreamEvent {
	return stream.StreamEvent{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{
		ID: id, Name: name, Status: "ok", Digest: content, Content: content,
	}}
}

func TestWebFetchArgURL(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"valid url arg", `{"url":"https://example.com/docs"}`, []string{"https://example.com/docs"}},
		{"empty url", `{"url":""}`, nil},
		{"malformed json", `not json`, nil},
		{"missing url field", `{}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := webFetchArgURL(json.RawMessage(tc.input))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("webFetchArgURL(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestWebSearchResultURLs(t *testing.T) {
	cases := []struct {
		name   string
		digest string
		want   []string
	}{
		{
			name:   "single result",
			digest: "1. Example Docs\nhttps://example.com/docs\nsnippet text",
			want:   []string{"https://example.com/docs"},
		},
		{
			name:   "multiple results blank-line separated",
			digest: "1. First\nhttps://a.example/x\nsnippet one\n\n2. Second\nhttps://b.example/y\nsnippet two",
			want:   []string{"https://a.example/x", "https://b.example/y"},
		},
		{
			name:   "no results found sentinel yields nothing",
			digest: "no results found",
			want:   nil,
		},
		{
			name:   "truncated tail with no url line yields nothing extra",
			digest: "1. Only",
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := webSearchResultURLs(tc.digest)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("webSearchResultURLs(%q) = %v, want %v", tc.digest, got, tc.want)
			}
		})
	}
}

// TestKBSearchHitTrace covers issue #413: search_kb's rendered digest
// (kbsearch.go's formatKBHits) parses back into document ids, titles,
// and fused scores for the mission.tool_call trace.
func TestKBSearchHitTrace(t *testing.T) {
	cases := []struct {
		name   string
		digest string
		want   []KBHitTrace
	}{
		{
			name:   "no matching passages found sentinel yields an explicit empty result",
			digest: "no matching passages found",
			want:   []KBHitTrace{},
		},
		{
			name: "single hit",
			digest: "1. Runbook — Runbook > Deploy (runbook.md)\n" +
				"Source: kb://doc-abc-123 (score 0.8123)\n" +
				"Run make deploy.",
			want: []KBHitTrace{{DocumentID: "doc-abc-123", DocumentTitle: "Runbook", Score: 0.8123}},
		},
		{
			name: "multiple hits, one with no breadcrumb or source ref",
			digest: "1. Runbook — Runbook > Deploy (runbook.md)\n" +
				"Source: kb://doc-1 (score 0.9)\n" +
				"Run make deploy.\n\n" +
				"2. FAQ\n" +
				"Source: kb://doc-2 (score 0.4)\n" +
				"Ask ops.",
			want: []KBHitTrace{
				{DocumentID: "doc-1", DocumentTitle: "Runbook", Score: 0.9},
				{DocumentID: "doc-2", DocumentTitle: "FAQ", Score: 0.4},
			},
		},
		{
			name:   "hit with no document id has no source line to match",
			digest: "1. FAQ\nAsk ops.",
			want:   nil,
		},
		{
			name:   "malformed digest yields nothing",
			digest: "not a search_kb result at all",
			want:   nil,
		},
		{
			name:   "empty digest yields nothing",
			digest: "",
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := kbSearchHitTrace(tc.digest)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("kbSearchHitTrace(%q) = %+v, want %+v", tc.digest, got, tc.want)
			}
		})
	}
}

// TestKBSearchHitTraceSurvivesOversizedFirstHit covers issue #418: the
// mission.tool_call digest caps at 2000 chars (toolCallDigestCap), so a
// first hit whose own chunk content alone exceeds that would push every
// later hit past a capped string before it's ever parsed. Since
// kbSearchHitTrace now runs against the tool result's full untruncated
// content (runner.go passes ev.ToolResult.Content, not .Digest), all 10
// hits survive regardless of how large the first hit's content is.
func TestKBSearchHitTraceSurvivesOversizedFirstHit(t *testing.T) {
	oversized := strings.Repeat("x", toolCallDigestCap+500)
	var b strings.Builder
	want := make([]KBHitTrace, 0, kbSearchHitCap)
	for i := 1; i <= kbSearchHitCap; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		title := fmt.Sprintf("Doc %d", i)
		content := "short content"
		if i == 1 {
			content = oversized
		}
		fmt.Fprintf(&b, "%d. %s\nSource: kb://%s (score %.4f)\n%s\n\n", i, title, docID, float64(i)/10, content)
		want = append(want, KBHitTrace{DocumentID: docID, DocumentTitle: title, Score: float64(i) / 10})
	}
	full := strings.TrimSpace(b.String())
	if len(full) <= toolCallDigestCap {
		t.Fatalf("test fixture too small: %d chars, want > %d", len(full), toolCallDigestCap)
	}
	got := kbSearchHitTrace(full)
	if !slices.Equal(got, want) {
		t.Fatalf("kbSearchHitTrace on %d-char content = %+v, want %d hits", len(full), got, len(want))
	}
}

// TestRunWorkerEmitsKBSearchHitsInTrace is the end-to-end wiring check
// for issue #413: a worker turn's search_kb call reaches the
// mission.tool_call trace with the returned document ids/titles/scores,
// while an unrelated tool call's kbHits stays nil.
func TestRunWorkerEmitsKBSearchHitsInTrace(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		toolEndEvent("search_kb", `{"query":"deploy"}`),
		okToolResultEvent("call-1", "search_kb",
			"1. Runbook — Runbook > Deploy (runbook.md)\nSource: kb://doc-abc-123 (score 0.8123)\nRun make deploy."),
		toolEndEvent("shell", `{"command":"ls"}`),
		finishedToolResultEvent("call-2", "shell", "ok", `{"command":"ls"}`, 5),
		toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"deployed"}`),
	}}}
	parker := &fakeParker{}
	r := newTestRunnerWithParker(agent, parker)
	if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if len(parker.toolCalls) != 2 {
		t.Fatalf("toolCalls = %+v, want 2 entries", parker.toolCalls)
	}
	kbCall := parker.toolCalls[0]
	want := []KBHitTrace{{DocumentID: "doc-abc-123", DocumentTitle: "Runbook", Score: 0.8123}}
	if kbCall.tool != "search_kb" || !slices.Equal(kbCall.kbHits, want) {
		t.Fatalf("search_kb toolCall = %+v, want kbHits %+v", kbCall, want)
	}
	if shellCall := parker.toolCalls[1]; shellCall.kbHits != nil {
		t.Fatalf("shell toolCall.kbHits = %+v, want nil", shellCall.kbHits)
	}
}

// TestRunWorkerEmitsAllKBHitsPastOversizedFirstHit covers issue #418
// end-to-end: a search_kb result whose first hit's chunk content alone
// exceeds the mission.tool_call digest cap (toolCallDigestCap) must
// still land every hit (up to kbSearchHitCap) in the trace, because
// runner.go now parses the tool result's full content rather than its
// capped digest.
func TestRunWorkerEmitsAllKBHitsPastOversizedFirstHit(t *testing.T) {
	oversized := strings.Repeat("x", toolCallDigestCap+500)
	var b strings.Builder
	want := make([]KBHitTrace, 0, kbSearchHitCap)
	for i := 1; i <= kbSearchHitCap; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		title := fmt.Sprintf("Doc %d", i)
		content := "short content"
		if i == 1 {
			content = oversized
		}
		fmt.Fprintf(&b, "%d. %s\nSource: kb://%s (score %.4f)\n%s\n\n", i, title, docID, float64(i)/10, content)
		want = append(want, KBHitTrace{DocumentID: docID, DocumentTitle: title, Score: float64(i) / 10})
	}
	full := strings.TrimSpace(b.String())
	if len(full) <= toolCallDigestCap {
		t.Fatalf("test fixture too small: %d chars, want > %d", len(full), toolCallDigestCap)
	}

	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		toolEndEvent("search_kb", `{"query":"deploy"}`),
		okToolResultEvent("call-1", "search_kb", full),
		toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"deployed"}`),
	}}}
	parker := &fakeParker{}
	r := newTestRunnerWithParker(agent, parker)
	if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if len(parker.toolCalls) != 1 {
		t.Fatalf("toolCalls = %+v, want 1 entry", parker.toolCalls)
	}
	if got := parker.toolCalls[0].kbHits; !slices.Equal(got, want) {
		t.Fatalf("search_kb toolCall.kbHits = %+v (%d), want %d hits", got, len(got), len(want))
	}
}

// TestRunWorkerEmitsEmptyKBSearchHitsExplicitly covers issue #413's
// empty-result acceptance criterion: a search_kb call with no hits
// still records a non-nil empty kbHits slice, distinguishing "searched,
// found nothing" from "not a search_kb call".
func TestRunWorkerEmitsEmptyKBSearchHitsExplicitly(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{
		toolEndEvent("search_kb", `{"query":"nothing"}`),
		okToolResultEvent("call-1", "search_kb", "no matching passages found"),
		toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"searched, found nothing"}`),
	}}}
	parker := &fakeParker{}
	r := newTestRunnerWithParker(agent, parker)
	if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if len(parker.toolCalls) != 1 {
		t.Fatalf("toolCalls = %+v, want 1 entry", parker.toolCalls)
	}
	if kbHits := parker.toolCalls[0].kbHits; kbHits == nil || len(kbHits) != 0 {
		t.Fatalf("kbHits = %+v, want a non-nil empty slice", kbHits)
	}
}

// TestRunWorkerCollectsSeenURLsFromWebFetchAndWebSearch is the
// end-to-end wiring check (D-059): a worker turn that calls fetch_url
// and search_web must surface both URLs on the returned verdict, so
// the driver's citations check has real harness evidence to compare
// against: never the model's own claim.
func TestRunWorkerCollectsSeenURLsFromWebFetchAndWebSearch(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{
			toolEndEvent("fetch_url", `{"url":"https://example.com/fetched"}`),
			okToolResultEvent("call-1", "fetch_url", "fetched content"),
			toolEndEvent("search_web", `{"query":"golang release notes"}`),
			okToolResultEvent("call-2", "search_web", "1. Release notes\nhttps://example.com/searched\nsnippet"),
			toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"researched"}`),
		},
	}}
	r := newTestRunner(agent)
	v, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	want := []string{"https://example.com/fetched", "https://example.com/searched"}
	if !slices.Equal(v.SeenURLs, want) {
		t.Fatalf("RunWorker verdict SeenURLs = %v, want %v", v.SeenURLs, want)
	}
}

// TestKBSearchToolRecordsRefsInSink covers the execution-time harvest
// (the digest is capped/offloaded past digestCeiling, so citation
// evidence must be recorded when the tool RUNS, never parsed from the
// rendered result): executing the worker's search_kb tool must land
// every returned document's kb:// ref in the sink, and a nil sink
// (discover/plan turns) must stay safe.
func TestKBSearchToolRecordsRefsInSink(t *testing.T) {
	r := &nativeRunner{log: slog.Default(), kbSearch: func(ctx context.Context, query string, collections []string, mode string, k int) ([]builtin.KBSearchHit, error) {
		return []builtin.KBSearchHit{
			{DocumentID: "aaaaaaaa-0000-0000-0000-000000000001", DocumentTitle: "Runbook", Content: "content"},
			{DocumentID: "bbbbbbbb-0000-0000-0000-000000000002", DocumentTitle: "Guide", Content: "more"},
		}, nil
	}}
	m := Mission{ID: "m1"}

	sink := &kbRefSink{}
	tool := r.kbSearchTool(m, sink)
	if tool == nil {
		t.Fatal("kbSearchTool = nil with backend present")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"deploy runbook"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := []string{"kb://aaaaaaaa-0000-0000-0000-000000000001", "kb://bbbbbbbb-0000-0000-0000-000000000002"}
	if got := sink.all(); !slices.Equal(got, want) {
		t.Fatalf("sink refs = %v, want %v", got, want)
	}

	if _, err := r.kbSearchTool(m, nil).Execute(context.Background(), json.RawMessage(`{"query":"deploy runbook"}`)); err != nil {
		t.Fatalf("Execute with nil sink: %v", err)
	}
}

// kbReadTool is gated only on a backend being wired (D-078, issue
// #368): read_kb is never scoped by anything mission-specific, so it's
// offered on every mission once a backend exists.
func TestKBReadToolGating(t *testing.T) {
	read := func(ctx context.Context, documentID string) (builtin.KBDocument, error) {
		return builtin.KBDocument{Title: "Runbook", Markdown: "content"}, nil
	}
	if tool := (&nativeRunner{log: slog.Default()}).kbReadTool(Mission{}); tool != nil {
		t.Fatal("kbReadTool offered without a backend")
	}
	tool := (&nativeRunner{log: slog.Default(), kbRead: read}).kbReadTool(Mission{})
	if tool == nil {
		t.Fatal("kbReadTool = nil with backend wired")
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"ref":"kb://doc-1"}`))
	if err != nil || !strings.Contains(out, "Runbook") {
		t.Fatalf("Execute = %q, %v", out, err)
	}
}

// TestRunWorkerSurfacesKBSinkRefsOnVerdict is the end-to-end wiring
// check: refs recorded by the sink during the worker turn must reach
// the verdict's SeenURLs alongside the digest-harvested web URLs.
func TestRunWorkerSurfacesKBSinkRefsOnVerdict(t *testing.T) {
	agent := &kbExecutingAgent{scriptedAgent: scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"researched"}`)},
	}}}
	r := newTestRunner(agent)
	r.kbSearch = func(ctx context.Context, query string, collections []string, mode string, k int) ([]builtin.KBSearchHit, error) {
		return []builtin.KBSearchHit{{DocumentID: "aaaaaaaa-0000-0000-0000-000000000001", DocumentTitle: "Runbook", Content: "content"}}, nil
	}
	v, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	want := []string{"kb://aaaaaaaa-0000-0000-0000-000000000001"}
	if !slices.Equal(v.SeenURLs, want) {
		t.Fatalf("RunWorker verdict SeenURLs = %v, want %v", v.SeenURLs, want)
	}
}

// kbExecutingAgent stands in for the loop actually running the offered
// search_kb tool mid-turn: Start executes it for real (so the tool's
// closure records into the worker's sink) before replaying the
// scripted events.
type kbExecutingAgent struct {
	scriptedAgent
}

func (a *kbExecutingAgent) Start(ctx context.Context, req loop.Request) (<-chan stream.StreamEvent, error) {
	for _, tool := range req.ExtraTools {
		if tool.Name == "search_kb" {
			if _, err := tool.Execute(ctx, json.RawMessage(`{"query":"q"}`)); err != nil {
				return nil, err
			}
		}
	}
	return a.scriptedAgent.Start(ctx, req)
}

// TestRunWorkerOffersKBSearch covers kbSearchTool's gate after issue
// #368: only a wired backend controls whether search_kb is offered.
func TestRunWorkerOffersKBSearch(t *testing.T) {
	hasKBSearch := func(req loop.Request) bool {
		for _, tool := range req.ExtraTools {
			if tool.Name == "search_kb" {
				return true
			}
		}
		return false
	}
	doneBatch := [][]stream.StreamEvent{{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`)}}

	t.Run("no backend wired", func(t *testing.T) {
		agent := &scriptedAgent{batches: doneBatch}
		r := newTestRunner(agent)
		if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"}); err != nil {
			t.Fatalf("RunWorker: %v", err)
		}
		if hasKBSearch(agent.requests[0]) {
			t.Fatal("search_kb offered with no backend wired")
		}
	})

	t.Run("backend wired", func(t *testing.T) {
		agent := &scriptedAgent{batches: doneBatch}
		r := newTestRunner(agent)
		r.kbSearch = func(ctx context.Context, query string, boostCollections []string, mode string, k int) ([]builtin.KBSearchHit, error) {
			return nil, nil
		}
		if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"}); err != nil {
			t.Fatalf("RunWorker: %v", err)
		}
		if !hasKBSearch(agent.requests[0]) {
			t.Fatal("search_kb not offered despite backend wired, whole-KB default (issue #368)")
		}
	})
}

// fakeProgressReader is a ProgressReader backed by an in-memory slice,
// mutable between polls so tests can simulate a note posted mid-turn.
type fakeProgressReader struct {
	notes []ProgressNote
}

func (f *fakeProgressReader) Progress(_ context.Context, _ string) ([]ProgressNote, error) {
	return f.notes, nil
}

// TestSteeringForSkipsNotesSeededInPacket pins the watermark's starting
// point: operator notes already rendered into the worker's seed packet
// (WorkPacket.Progress) must never be redelivered as mid-turn steering.
func TestSteeringForSkipsNotesSeededInPacket(t *testing.T) {
	seeded := []ProgressNote{{Note: "Operator note: already seen"}}
	reader := &fakeProgressReader{notes: seeded}
	r := &nativeRunner{log: slog.Default(), progressReader: reader}

	steer := r.steeringFor("m1", seeded)
	if steer == nil {
		t.Fatal("steeringFor returned nil with a progressReader wired")
	}
	if got := steer(context.Background()); got != nil {
		t.Fatalf("steering = %v, want nil (note already in the seed packet)", got)
	}
}

// TestSteeringForDeliversNoteExactlyOnce pins the core contract: a note
// appended to the store mid-turn is delivered on the next poll, and
// never again on a later poll once the watermark has advanced.
func TestSteeringForDeliversNoteExactlyOnce(t *testing.T) {
	reader := &fakeProgressReader{}
	r := &nativeRunner{log: slog.Default(), progressReader: reader}
	steer := r.steeringFor("m1", nil)

	if got := steer(context.Background()); got != nil {
		t.Fatalf("steering before any note = %v, want nil", got)
	}

	reader.notes = append(reader.notes, ProgressNote{Note: "Operator note: hurry up"})
	got := steer(context.Background())
	if len(got) != 1 || got[0] != "Operator steering note (mid-run): hurry up" {
		t.Fatalf("steering after note posted = %v, want one formatted note", got)
	}

	if got := steer(context.Background()); got != nil {
		t.Fatalf("steering re-delivered the same note: %v", got)
	}

	reader.notes = append(reader.notes, ProgressNote{Note: "not an operator note"})
	if got := steer(context.Background()); got != nil {
		t.Fatalf("steering delivered a non-operator progress note: %v", got)
	}
}

// TestSteeringForNilWithoutProgressReader pins that RunWorker's Steering
// wiring degrades to a no-op when no ProgressReader was ever set
// (SetProgressReader unwired): the pre-existing behavior everywhere
// this feature isn't configured.
func TestSteeringForNilWithoutProgressReader(t *testing.T) {
	r := &nativeRunner{log: slog.Default()}
	if steer := r.steeringFor("m1", nil); steer != nil {
		t.Fatal("steeringFor should return nil with no progressReader wired")
	}
}

// TestExecEnvironmentNoteIncludesTimezoneSteer pins that the date line
// carries the timezone-presentation instruction right after the date,
// for both an operator location and the nil (UTC) default: a global
// harness instruction instead of per-prompt boilerplate.
func TestExecEnvironmentNoteIncludesTimezoneSteer(t *testing.T) {
	want := "Present all dates and times in this timezone unless the goal asks otherwise."

	t.Run("nil location (UTC)", func(t *testing.T) {
		note := execEnvironmentNote(nil)
		if !strings.Contains(note, want) {
			t.Fatalf("timezone steer missing:\n%s\nwant substring:\n%s", note, want)
		}
		if i, j := strings.Index(note, "Today is"), strings.Index(note, want); i == -1 || j <= i {
			t.Fatalf("timezone steer must follow the date: %s", note)
		}
	})

	t.Run("operator location", func(t *testing.T) {
		loc, err := time.LoadLocation("Europe/Amsterdam")
		if err != nil {
			t.Fatalf("load location: %v", err)
		}
		note := execEnvironmentNote(loc)
		if !strings.Contains(note, want) {
			t.Fatalf("timezone steer missing:\n%s\nwant substring:\n%s", note, want)
		}
	})
}

// TestRunWorkerWiresSteeringFromProgressReader confirms RunWorker's
// loop.Request actually carries a non-nil Steering func end-to-end when
// a ProgressReader is wired, and that the request has none when it
// isn't: the seam other packages (loop.Agent) rely on.
func TestRunWorkerWiresSteeringFromProgressReader(t *testing.T) {
	doneBatch := [][]stream.StreamEvent{{toolEndEvent(missionStatusToolName, `{"outcome":"done","evidence":"ok"}`)}}

	t.Run("wired", func(t *testing.T) {
		agent := &scriptedAgent{batches: doneBatch}
		r := newTestRunner(agent)
		r.SetProgressReader(&fakeProgressReader{})
		if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"}); err != nil {
			t.Fatalf("RunWorker: %v", err)
		}
		if agent.requests[0].Steering == nil {
			t.Fatal("Steering not wired on the worker request despite a ProgressReader")
		}
	})

	t.Run("unwired", func(t *testing.T) {
		agent := &scriptedAgent{batches: doneBatch}
		r := newTestRunner(agent)
		if _, _, err := r.RunWorker(context.Background(), Mission{ID: "m1", Route: "default"}, WorkPacket{Goal: "test"}); err != nil {
			t.Fatalf("RunWorker: %v", err)
		}
		if agent.requests[0].Steering != nil {
			t.Fatal("Steering wired despite no ProgressReader")
		}
	})
}

// TestDiscoverSessionWiresSteeringFromProgressReader mirrors
// TestRunWorkerWiresSteeringFromProgressReader for the discover phase
// (D-089, issue #458): a ProgressReader must produce a non-nil Steering
// func on the discover turn's loop.Request, same seam RunWorker uses.
func TestDiscoverSessionWiresSteeringFromProgressReader(t *testing.T) {
	batch := [][]stream.StreamEvent{
		{toolEndEvent(discoverNotesToolName, `{"findings":"no prior implementation"}`)},
	}

	t.Run("wired", func(t *testing.T) {
		agent := &scriptedAgent{batches: batch}
		r := newTestRunner(agent)
		r.SetProgressReader(&fakeProgressReader{})
		if _, _, _, err := r.DiscoverSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "test"}); err != nil {
			t.Fatalf("DiscoverSession: %v", err)
		}
		if agent.requests[0].Steering == nil {
			t.Fatal("Steering not wired on the discover request despite a ProgressReader")
		}
	})

	t.Run("unwired", func(t *testing.T) {
		agent := &scriptedAgent{batches: batch}
		r := newTestRunner(agent)
		if _, _, _, err := r.DiscoverSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "test"}); err != nil {
			t.Fatalf("DiscoverSession: %v", err)
		}
		if agent.requests[0].Steering != nil {
			t.Fatal("Steering wired despite no ProgressReader")
		}
	})
}

// TestPlanSessionWiresSteeringFromProgressReader mirrors
// TestRunWorkerWiresSteeringFromProgressReader for the plan phase
// (D-089, issue #458).
func TestPlanSessionWiresSteeringFromProgressReader(t *testing.T) {
	batch := [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Add validation","artifacts":["out.md"],"criteria":["c1","c2"],"verify_cmd":"go test ./...","passes":true}]}`)},
	}

	t.Run("wired", func(t *testing.T) {
		agent := &scriptedAgent{batches: batch}
		r := newTestRunner(agent)
		r.SetProgressReader(&fakeProgressReader{})
		if _, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "fix bug"}, ""); err != nil {
			t.Fatalf("PlanSession: %v", err)
		}
		if agent.requests[0].Steering == nil {
			t.Fatal("Steering not wired on the plan request despite a ProgressReader")
		}
	})

	t.Run("unwired", func(t *testing.T) {
		agent := &scriptedAgent{batches: batch}
		r := newTestRunner(agent)
		if _, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "fix bug"}, ""); err != nil {
			t.Fatalf("PlanSession: %v", err)
		}
		if agent.requests[0].Steering != nil {
			t.Fatal("Steering wired despite no ProgressReader")
		}
	})
}

// TestRunReviewWiresSteeringFromProgressReader mirrors
// TestRunWorkerWiresSteeringFromProgressReader for the prove phase
// (D-089, issue #458).
func TestRunReviewWiresSteeringFromProgressReader(t *testing.T) {
	batch := [][]stream.StreamEvent{
		{toolEndEvent(reviewVerdictToolName, `{"decision":"approve"}`)},
	}

	t.Run("wired", func(t *testing.T) {
		agent := &scriptedAgent{batches: batch}
		r := newTestRunner(agent)
		r.SetProgressReader(&fakeProgressReader{})
		if _, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Goal: "goal"}); err != nil {
			t.Fatalf("RunReview: %v", err)
		}
		if agent.requests[0].Steering == nil {
			t.Fatal("Steering not wired on the review request despite a ProgressReader")
		}
	})

	t.Run("unwired", func(t *testing.T) {
		agent := &scriptedAgent{batches: batch}
		r := newTestRunner(agent)
		if _, err := r.RunReview(context.Background(), Mission{ID: "m1", ReviewRoute: "default"}, ReviewPacket{Goal: "goal"}); err != nil {
			t.Fatalf("RunReview: %v", err)
		}
		if agent.requests[0].Steering != nil {
			t.Fatal("Steering wired despite no ProgressReader")
		}
	})
}

// TestParsePlanCriteriaAndScope is the D-095 plan validation table:
// 2 to 6 criteria per unit or plan_invalid naming the unit; blank
// entries trimmed; scope defaults to the artifact directories (the
// file itself at the workspace root) unless the planner sets it.
func TestParsePlanCriteriaAndScope(t *testing.T) {
	unit := func(extra string) string {
		return `{"units":[{"title":"Write it","artifacts":["src/a.go","report.md","src/b.go"],"verify_cmd":"grep -q x report.md"` + extra + `}]}`
	}
	tests := []struct {
		name      string
		json      string
		wantErr   string
		wantCrit  []string
		wantScope []string
	}{
		{"missing criteria", unit(``), "plan_invalid: unit \"Write it\"", nil, nil},
		{"one criterion", unit(`,"criteria":["only one"]`), "plan_invalid", nil, nil},
		{"seven criteria", unit(`,"criteria":["1","2","3","4","5","6","7"]`), "plan_invalid", nil, nil},
		{"blank entries do not count", unit(`,"criteria":["real"," ",""]`), "plan_invalid", nil, nil},
		{"two criteria, default scope", unit(`,"criteria":[" a ","b"]`), "", []string{"a", "b"}, []string{"src", "report.md"}},
		{"explicit scope kept", unit(`,"criteria":["a","b"],"scope":["src/","docs"]`), "", []string{"a", "b"}, []string{"src/", "docs"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := parsePlan(tc.json)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parsePlan err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePlan: %v", err)
			}
			u := plan.Units[0]
			if strings.Join(u.Criteria, "|") != strings.Join(tc.wantCrit, "|") {
				t.Fatalf("Criteria = %v, want %v", u.Criteria, tc.wantCrit)
			}
			if strings.Join(u.Scope, "|") != strings.Join(tc.wantScope, "|") {
				t.Fatalf("Scope = %v, want %v", u.Scope, tc.wantScope)
			}
		})
	}
}

// TestPlanSessionRetriesWithCriteriaError pins the D-095 retry: a plan
// without criteria buys one recovery turn whose message names the
// plan_invalid error, and a corrected plan on that turn is accepted.
func TestPlanSessionRetriesWithCriteriaError(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, `{"units":[{"title":"Write it","artifacts":["out.md"],"verify_cmd":"grep -q x out.md"}]}`)},
		{toolEndEvent(planToolName, `{"units":[{"title":"Write it","artifacts":["out.md"],"criteria":["a","b"],"verify_cmd":"grep -q x out.md"}]}`)},
	}}
	r := newTestRunner(agent)
	plan, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "g"}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if agent.call != 2 || len(plan.Units[0].Criteria) != 2 {
		t.Fatalf("calls = %d, plan = %+v; want one recovery turn and the corrected plan", agent.call, plan)
	}
	msgs := agent.requests[1].Messages
	last := msgs[len(msgs)-1].Content
	if !strings.Contains(last, "plan_invalid: unit \"Write it\"") {
		t.Fatalf("recovery message = %q, want the plan_invalid error naming the unit", last)
	}
}

// TestRenderReviewContentCriteriaReplaceGoal pins the D-095 reviewer
// packet: units render with criteria, harness status and verify
// excerpt, the whole-change stat precedes the scoped diff, and the goal
// is absent unless the packet sets it (legacy plans).
func TestRenderReviewContentCriteriaReplaceGoal(t *testing.T) {
	p := ReviewPacket{
		Units: []PlanUnit{{
			Title: "Write summary", Criteria: []string{"summary.md names RFC 6585", "under 200 words"},
			HarnessPassed: true, VerifyCheck: "verify_cmd", VerifyExcerpt: "grep ok\n",
		}},
		DiffStat: " summary.md | 3 +++\n 1 file changed",
		Diff:     "diff --git a/summary.md b/summary.md\n+429",
	}
	got := renderReviewContent(p)
	for _, want := range []string{
		"Units under review (judge each against its acceptance criteria):",
		"### Write summary [harness-verified]",
		"- summary.md names RFC 6585",
		"- under 200 words",
		"Harness verify_cmd check: passed",
		"Verify output:\ngrep ok\n",
		"Changed files (whole change):\n summary.md | 3 +++",
		"Diff to review (restricted to the reviewed units' scope):\ndiff --git",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("review content missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Mission goal") {
		t.Fatalf("review content carries the goal despite criteria:\n%s", got)
	}
	if statIdx, diffIdx := strings.Index(got, "Changed files"), strings.Index(got, "Diff to review"); statIdx > diffIdx {
		t.Fatal("diff stat must precede the scoped diff")
	}
	legacy := renderReviewContent(ReviewPacket{Goal: "the goal", Units: []PlanUnit{{Title: "u"}}})
	if !strings.Contains(legacy, "Mission goal: the goal") {
		t.Fatalf("legacy packet must render the goal:\n%s", legacy)
	}
}
