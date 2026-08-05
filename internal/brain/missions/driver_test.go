package missions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStore is an in-memory driverStore for scripting Driver scenarios
// without a real Postgres pool.
type fakeStore struct {
	mu       sync.Mutex
	missions map[string]Mission
	events   map[string][]Event
	seq      map[string]int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{missions: map[string]Mission{}, events: map[string][]Event{}, seq: map[string]int64{}}
}

func (f *fakeStore) put(id string, m Mission) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.missions[id] = m
}

func (f *fakeStore) Create(ctx context.Context, m Mission) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.ID == "" {
		m.ID = fmt.Sprintf("fake-%d", len(f.missions)+1)
	}
	f.missions[m.ID] = m
	return m.ID, nil
}

func (f *fakeStore) SetProvisioned(ctx context.Context, id, workspace, worktree, branch, baseCommit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.Workspace, m.Worktree, m.Branch, m.BaseCommit = workspace, worktree, branch, baseCommit
	f.missions[id] = m
	return nil
}

// SetSession mirrors the real Store's WHERE session_id IS NULL guard —
// a second call for a mission that already has one is a no-op, not an
// overwrite.
func (f *fakeStore) SetSession(ctx context.Context, id, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	if m.SessionID == "" {
		m.SessionID = sessionID
		f.missions[id] = m
	}
	return nil
}

func (f *fakeStore) Get(ctx context.Context, id string) (Mission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.missions[id]
	if !ok {
		return Mission{}, ErrNotFound
	}
	return m, nil
}

func (f *fakeStore) ApplyTransition(ctx context.Context, id string, t Transition) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.Phase, m.Status, m.PauseReason = t.Next.Phase, t.Next.Status, t.Next.PauseReason
	m.Iteration, m.MaxIterations = t.Next.Iteration, t.Next.MaxIterations
	m.ConsecutiveFailures, m.LastGapFingerprint, m.StallCount = t.Next.ConsecutiveFailures, t.Next.LastGapFingerprint, t.Next.StallCount
	f.missions[id] = m
	for _, ev := range t.Events {
		f.seq[id]++
		f.events[id] = append(f.events[id], Event{MissionID: id, Seq: f.seq[id], Kind: ev.Kind})
	}
	return nil
}

func (f *fakeStore) AppendEvent(ctx context.Context, id, kind string, payload map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq[id]++
	raw, _ := json.Marshal(payload)
	f.events[id] = append(f.events[id], Event{MissionID: id, Seq: f.seq[id], Kind: kind, Payload: raw})
	return nil
}

func (f *fakeStore) SetSpec(ctx context.Context, id string, spec Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.Spec = spec
	f.missions[id] = m
	return nil
}

func (f *fakeStore) SetLastEvidence(ctx context.Context, id, evidence string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.LastEvidence = evidence
	f.missions[id] = m
	return nil
}

func (f *fakeStore) AppendProgress(ctx context.Context, id, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.Progress = append(m.Progress, ProgressNote{Note: note})
	f.missions[id] = m
	f.seq[id]++
	f.events[id] = append(f.events[id], Event{MissionID: id, Seq: f.seq[id], Kind: "mission.progress"})
	return nil
}

// scriptedRunner replays fixed responses for RunWorker/RunReview/
// PlanSession, one entry consumed per call — lets a test script an
// exact sequence of outcomes across several Advance calls. workerErr,
// if set, makes every RunWorker call fail with this error instead of
// returning a verdict — the driver maps a Runner-level failure
// (infra/gateway trouble) to InputWorkerFailed, distinct from the
// worker's OWN self-reported "retry" outcome (InputWorkerRetry), which
// does not count toward the backoff brake.
type scriptedRunner struct {
	workerVerdicts []WorkerVerdict
	workerIdx      int
	workerErr      error
	reviewVerdicts []ReviewVerdict
	reviewIdx      int
	plans          []Spec
	planIdx        int
}

func (r *scriptedRunner) RunWorker(ctx context.Context, m Mission, packet WorkPacket) (WorkerVerdict, string, error) {
	if r.workerErr != nil {
		return WorkerVerdict{}, "", r.workerErr
	}
	v := r.workerVerdicts[r.workerIdx]
	if r.workerIdx < len(r.workerVerdicts)-1 {
		r.workerIdx++
	}
	return v, "worker output", nil
}

func (r *scriptedRunner) RunReview(ctx context.Context, m Mission, packet ReviewPacket, gk *GatekeeperState) (ReviewVerdict, *GatekeeperState, error) {
	v := r.reviewVerdicts[r.reviewIdx]
	if r.reviewIdx < len(r.reviewVerdicts)-1 {
		r.reviewIdx++
	}
	return v, &GatekeeperState{}, nil
}

func (r *scriptedRunner) PlanSession(ctx context.Context, m Mission, researchNotes string) (Spec, error) {
	s := r.plans[r.planIdx]
	if r.planIdx < len(r.plans)-1 {
		r.planIdx++
	}
	return s, nil
}

// fakeSandboxExec runs command via /bin/sh -c directly in workdir —
// tests exercising verify_cmd need the real exit code/output a plan's
// check produces, not a mocked one, and don't care that it isn't
// actually containerized.
func fakeSandboxExec(ctx context.Context, missionID, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command) //nolint:gosec // test-only, command is test-authored
	cmd.Dir = workdir
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}

func testDriver(store driverStore, runner Runner) *Driver {
	return NewDriver(store, runner, nil, nil, nil, nil, fakeSandboxExec, nil, slog.Default())
}

// driveN calls Advance up to n times, stopping early if it returns
// false (mission parked/terminal).
func driveN(t *testing.T, d *Driver, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		cont, err := d.Advance(context.Background(), id)
		if err != nil {
			t.Fatalf("Advance[%d]: %v", i, err)
		}
		if !cont {
			return
		}
	}
}

func TestDriverHappyPathToDone(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "research", Phase: PhaseResearch, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit", VerifyCmd: ""}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)

	// research -> plan -> execute -> review -> done: 4 Advance calls.
	driveN(t, d, "m1", 4)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone || m.Status != StatusDone {
		t.Fatalf("mission after happy path = %+v, want done/done", m)
	}
	if m.LastEvidence != "did it" {
		t.Fatalf("mission.LastEvidence = %q, want the worker's evidence persisted for the reviewer", m.LastEvidence)
	}
}

func TestDriverBackoffPauseAfterThreeFailures(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "research", Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{workerErr: fmt.Errorf("gateway unavailable")}
	d := testDriver(store, runner)

	driveN(t, d, "m1", 3)

	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseBackoff {
		t.Fatalf("mission after 3 consecutive runner failures = %+v, want paused/backoff", m)
	}
}

// TestDriverWorkerRetryDoesNotCountTowardBackoff confirms the
// distinction the fix above documents: a worker's own self-reported
// "retry" outcome (it made an attempt, just didn't finish) never
// triggers the backoff brake, unlike a Runner-level failure.
func TestDriverWorkerRetryDoesNotCountTowardBackoff(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "research", Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "retry", Analysis: "still working on it"}}}
	d := testDriver(store, runner)

	driveN(t, d, "m1", 3)

	m, _ := store.Get(context.Background(), "m1")
	if m.Status == StatusPaused && m.PauseReason == PauseBackoff {
		t.Fatal("worker-reported retry incorrectly triggered the backoff brake")
	}
	if m.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures = %d after worker retries, want 0 (retry resets it)", m.ConsecutiveFailures)
	}
}

func TestDriverStallPauseOnIdenticalFindingsTwice(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "research", Phase: PhaseReview, Status: StatusWorking, MaxIterations: 8,
		Spec: Spec{Units: []PlanUnit{{Title: "u1"}}},
	})
	sameFindings := []Finding{{Title: "missing validation", File: "x.go"}}
	runner := &scriptedRunner{
		reviewVerdicts: []ReviewVerdict{{Approved: false, Findings: sameFindings}},
	}
	d := testDriver(store, runner)

	// review round 1: rework (stall=1) -> back to execute.
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance 1: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseExecute {
		t.Fatalf("after 1st rework, phase = %q, want execute", m.Phase)
	}
	// Manually push back to review for round 2 (driver would get there
	// via a worker DONE verdict; this test isolates the review-phase
	// stall logic specifically).
	m.Phase = PhaseReview
	store.put("m1", m)
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance 2: %v", err)
	}
	m, _ = store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseNoProgress {
		t.Fatalf("mission after 2 identical-fingerprint reworks = %+v, want paused/no_progress", m)
	}
}

func TestDriverBudgetPause(t *testing.T) {
	store := newFakeStore()
	budget := 1.0
	store.put("m1", Mission{ID: "m1", Kind: "research", Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, BudgetUSD: &budget})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done"}}}
	d := testDriver(store, runner)

	// toStepState currently reports SpentUSD=0 (ledger integration is
	// M3/M4), so budget pausing isn't reachable via Advance yet in
	// Phase 1 — this test documents that boundary explicitly rather
	// than asserting a pause that can't happen until spend tracking
	// lands. Confirm the mission instead completes normally.
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status == StatusPaused && m.PauseReason == PauseBudget {
		t.Fatal("budget pause fired despite SpentUSD always being 0 in Phase 1 — toStepState must have started reporting real spend; update this test")
	}
}

// TestDriverGatekeeperCleanupOnTerminal confirms the driver forgets a
// mission's in-progress reviewer session once the mission reaches a
// terminal phase — Advance() itself doesn't accept an external cancel
// input directly (that's Signal, wired in M4's API layer; the state
// machine's cancel precedence is already exercised in
// statemachine_test.go), so this drives a mission to done via review
// approval instead, which is the natural path that also needs cleanup.
func TestDriverGatekeeperCleanupOnTerminal(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "research", Phase: PhaseReview, Status: StatusWorking, MaxIterations: 8,
		Spec: Spec{Units: []PlanUnit{{Title: "only unit"}}},
	})
	d := testDriver(store, &scriptedRunner{reviewVerdicts: []ReviewVerdict{{Approved: true}}})
	d.gatekeepers["m1"] = &GatekeeperState{Messages: nil}

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %q, want done", m.Phase)
	}
	if _, stillPresent := d.gatekeepers["m1"]; stillPresent {
		t.Fatal("gatekeeper state was not cleaned up once the mission reached a terminal phase")
	}
}

// blockingRunner's RunWorker blocks on a channel until released,
// letting a test force two concurrent Drive calls to overlap on the
// same mission — reproducing the race where the work-slot sweep
// claims a mission that a still-running Drive loop already owns.
type blockingRunner struct {
	release  chan struct{}
	started  chan struct{}
	starts   int32
	verdict  WorkerVerdict
	reviewV  ReviewVerdict
	planSpec Spec
}

func (r *blockingRunner) RunWorker(ctx context.Context, m Mission, packet WorkPacket) (WorkerVerdict, string, error) {
	atomic.AddInt32(&r.starts, 1)
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-r.release
	return r.verdict, "worker output", nil
}

func (r *blockingRunner) RunReview(ctx context.Context, m Mission, packet ReviewPacket, gk *GatekeeperState) (ReviewVerdict, *GatekeeperState, error) {
	return r.reviewV, &GatekeeperState{}, nil
}

func (r *blockingRunner) PlanSession(ctx context.Context, m Mission, researchNotes string) (Spec, error) {
	return r.planSpec, nil
}

// TestDriverDriveIsSerializedPerMission reproduces a real bug: the
// work-slot sweep's ClaimWorkSlot can claim a mission that is still
// actively owned by an earlier Drive goroutine (Advance's own
// transitions pass through status='idle' transiently between steps),
// spawning a second concurrent Drive loop that races the first's
// read-then-write Advance calls. A second Drive call for a mission
// already being driven must no-op instead of starting a competing
// loop.
func TestDriverDriveIsSerializedPerMission(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "research", Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8,
		Spec: Spec{Units: []PlanUnit{{Title: "only unit"}}},
	})
	runner := &blockingRunner{
		release: make(chan struct{}),
		started: make(chan struct{}, 1),
		verdict: WorkerVerdict{Outcome: "done", Evidence: "did it"},
		reviewV: ReviewVerdict{Approved: true},
	}
	d := testDriver(store, runner)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = d.Drive(context.Background(), "m1")
	}()
	<-runner.started // first Drive is now blocked inside RunWorker
	go func() {
		defer wg.Done()
		_ = d.Drive(context.Background(), "m1") // must no-op, not race the first
	}()
	time.Sleep(20 * time.Millisecond) // give the second call a chance to (wrongly) start a competing Advance
	close(runner.release)
	wg.Wait()

	if got := atomic.LoadInt32(&runner.starts); got != 1 {
		t.Fatalf("RunWorker started %d times for one mission, want exactly 1 (second Drive must no-op while the first owns the mission)", got)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone || m.Status != StatusDone {
		t.Fatalf("mission after single-owner drive = %+v, want done/done", m)
	}
}

// TestDriverReviewApprovalContradictedByVerifyRoutesToRework
// reproduces a real bug: the reviewer approves a unit whose evidence
// doesn't hold up (e.g. a worker claiming a file exists when it never
// wrote one) — the harness's own verify_cmd then fails. This must NOT
// be treated as an infra fault (which just parks the mission for a
// human to blindly resume forever); it must route through rework,
// back to execute, so the worker gets another attempt with the actual
// failure recorded as a progress note.
func TestDriverReviewApprovalContradictedByVerifyRoutesToRework(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "research", Phase: PhaseReview, Status: StatusWorking, MaxIterations: 8,
		Spec: Spec{Units: []PlanUnit{{Title: "write summary.md", VerifyCmd: "test -f /nonexistent-verify-target"}}},
	})
	d := testDriver(store, &scriptedRunner{reviewVerdicts: []ReviewVerdict{{Approved: true}}})

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseExecute {
		t.Fatalf("mission phase = %q, want execute (rework path), not stuck on an infra pause", m.Phase)
	}
	if m.PauseReason == PauseInfra {
		t.Fatal("a failed verify_cmd after approval must not be classified as an infra fault")
	}
	if m.Iteration != 1 {
		t.Fatalf("iteration = %d, want 1 (rework costs an iteration like any other rework)", m.Iteration)
	}
	if len(m.Progress) == 0 {
		t.Fatal("expected a progress note explaining the verify failure to the next worker turn")
	}
	if !strings.Contains(m.Progress[len(m.Progress)-1].Note, "Verification failed") {
		t.Fatalf("progress note = %q, want it to explain the verify failure", m.Progress[len(m.Progress)-1].Note)
	}
}

func TestDriverBlockedParksForInput(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "research", Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "which env?"}}}
	d := testDriver(store, runner)

	cont, err := d.Advance(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if cont {
		t.Fatal("Advance reported it can continue on a mission that just parked waiting_for_input")
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusWaitingForInput {
		t.Fatalf("mission status = %q, want waiting_for_input", m.Status)
	}
}

// fakeSessionCreator hands out sequential ids without a real
// session.Store — Driver.Create only needs a non-empty string back.
type fakeSessionCreator struct{ n int }

func (f *fakeSessionCreator) Create(ctx context.Context, title string) (string, error) {
	f.n++
	return fmt.Sprintf("session-%d", f.n), nil
}

// fakeGranter records every Grant call for assertion — a real
// sessionGranter for tests, not a mock of one.
type fakeGranter struct {
	calls []struct{ sessionID, tool, pattern string }
}

func (f *fakeGranter) Grant(ctx context.Context, sessionID, tool, pattern string, ttl time.Duration) error {
	f.calls = append(f.calls, struct{ sessionID, tool, pattern string }{sessionID, tool, pattern})
	return nil
}

func TestDriverCreateGrantsShellAutoApproveWhenEnabled(t *testing.T) {
	store := newFakeStore()
	granter := &fakeGranter{}
	d := NewDriver(store, &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}, nil, nil, &fakeSessionCreator{}, granter, nil, nil, slog.Default())

	id, err := d.Create(context.Background(), Mission{Goal: "test", Kind: "research", Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, AutoApproveSafe: true}, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(granter.calls) != 1 {
		t.Fatalf("Grant calls = %d, want exactly 1", len(granter.calls))
	}
	call := granter.calls[0]
	if call.tool != "shell" || call.pattern != "*" {
		t.Fatalf("Grant call = %+v, want tool=shell pattern=*", call)
	}
	m, _ := store.Get(context.Background(), id)
	if call.sessionID != m.SessionID {
		t.Fatalf("Grant sessionID = %q, want the mission's own hidden session %q", call.sessionID, m.SessionID)
	}
}

func TestDriverCreateSkipsGrantWhenAutoApproveDisabled(t *testing.T) {
	store := newFakeStore()
	granter := &fakeGranter{}
	d := NewDriver(store, &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}, nil, nil, &fakeSessionCreator{}, granter, nil, nil, slog.Default())

	if _, err := d.Create(context.Background(), Mission{Goal: "test", Kind: "research", Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, AutoApproveSafe: false}, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(granter.calls) != 0 {
		t.Fatalf("Grant calls = %d, want 0 when AutoApproveSafe is false", len(granter.calls))
	}
}

// TestDriverCreateGrantsApprovalAllowlist confirms Create grants every
// tool in the resolved agent's ApprovalAllowlist — the fix that makes
// api/missions.go:28's stale "ApprovalAllowlist is resolved" comment
// actually true.
func TestDriverCreateGrantsApprovalAllowlist(t *testing.T) {
	store := newFakeStore()
	granter := &fakeGranter{}
	d := NewDriver(store, &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}, nil, nil, &fakeSessionCreator{}, granter, nil, nil, slog.Default())
	d.SetAgentResolver(func(ctx context.Context, agentID string) (AgentDefaults, bool) {
		if agentID != "briefing-agent" {
			return AgentDefaults{}, false
		}
		return AgentDefaults{ApprovalAllowlist: []string{"gmail_search", "gmail_send"}}, true
	})

	id, err := d.Create(context.Background(), Mission{
		Goal: "test", Kind: "research", AgentID: "briefing-agent",
		Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, AutoApproveSafe: false,
	}, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, _ := store.Get(context.Background(), id)

	gotTools := map[string]bool{}
	for _, call := range granter.calls {
		if call.sessionID != m.SessionID {
			t.Fatalf("Grant call sessionID = %q, want the mission's own hidden session %q", call.sessionID, m.SessionID)
		}
		gotTools[call.tool] = true
	}
	for _, want := range []string{"gmail_search", "gmail_send"} {
		if !gotTools[want] {
			t.Fatalf("Grant calls = %+v, want a grant for %q", granter.calls, want)
		}
	}
}

// TestDriverCreateSkipsAllowlistGrantWhenAgentUnresolved: an
// AgentID that doesn't resolve (deleted agent, or none set) grants
// nothing beyond whatever AutoApproveSafe itself produces — no
// allowlist to apply.
func TestDriverCreateSkipsAllowlistGrantWhenAgentUnresolved(t *testing.T) {
	store := newFakeStore()
	granter := &fakeGranter{}
	d := NewDriver(store, &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}, nil, nil, &fakeSessionCreator{}, granter, nil, nil, slog.Default())
	d.SetAgentResolver(func(ctx context.Context, agentID string) (AgentDefaults, bool) {
		return AgentDefaults{}, false
	})

	if _, err := d.Create(context.Background(), Mission{
		Goal: "test", Kind: "research", AutoApproveSafe: false,
		Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8,
	}, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(granter.calls) != 0 {
		t.Fatalf("Grant calls = %d, want 0 for an unresolved agent", len(granter.calls))
	}
}

// TestDriverAdvanceLazilyProvisionsBareMission reproduces the fix for
// the plan's defect #1: a mission inserted directly (bypassing
// Create — exactly what scheduler.go's createFromTemplate does) has no
// session and no workspace. The first Advance call must provision both
// before running the phase, or the worker gets no shell/write_file
// tools (runner.go's missionTools returns nil for an empty WorkRoot).
func TestDriverAdvanceLazilyProvisionsBareMission(t *testing.T) {
	store := newFakeStore()
	// A bare row, exactly as createFromTemplate leaves it: no
	// SessionID, no Workspace/Worktree.
	store.put("m1", Mission{
		ID: "m1", Goal: "scheduled run", Kind: "research",
		Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, AutoApproveSafe: true,
	})
	granter := &fakeGranter{}
	sessions := &fakeSessionCreator{}
	wsRoot := t.TempDir()
	workspace := NewWorkspace(wsRoot, nil, slog.Default())
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, workspace, nil, sessions, granter, nil, nil, slog.Default())

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	m, _ := store.Get(context.Background(), "m1")
	if m.SessionID == "" {
		t.Fatal("Advance did not provision a hidden session for a bare mission")
	}
	if m.Workspace == "" {
		t.Fatal("Advance did not provision a workspace for a bare mission")
	}
	found := false
	for _, call := range granter.calls {
		if call.tool == "shell" && call.pattern == "*" && call.sessionID == m.SessionID {
			found = true
		}
	}
	if !found {
		t.Fatal("Advance's lazy provisioning did not grant the auto-approve-safe shell allowance")
	}
}

// TestDriverAdvanceSkipsProvisioningWhenAlreadyProvisioned confirms
// ensureProvisioned is a no-op (no new session, no re-grant) once a
// mission already has both — the ordinary case for every mission
// created via Create, which must not re-provision on every single
// Advance call.
func TestDriverAdvanceSkipsProvisioningWhenAlreadyProvisioned(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "research", Phase: PhaseExecute, Status: StatusWorking,
		MaxIterations: 8, SessionID: "already-provisioned-session", Workspace: "/already/provisioned",
	})
	granter := &fakeGranter{}
	sessions := &fakeSessionCreator{}
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, nil, nil, sessions, granter, nil, nil, slog.Default())

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if len(granter.calls) != 0 {
		t.Fatalf("Grant calls = %d, want 0 for an already-provisioned mission", len(granter.calls))
	}
	if sessions.n != 0 {
		t.Fatalf("sessions.Create was called %d times, want 0 for an already-provisioned mission", sessions.n)
	}
}

// TestDriverSkipsReviewWhenHarnessChecksPass: a non-coding mission
// whose unit declares artifacts that pass the harness's own checks
// completes WITHOUT an LLM review round — the reviewer is the least
// reliable and most expensive link, and passing deterministic checks
// already establish the unit holds up. The scriptedRunner has no
// review verdicts scripted: any RunReview call would panic the test.
func TestDriverSkipsReviewWhenHarnessChecksPass(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "summary.md"), []byte("real content"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "research", Phase: PhaseExecute, Status: StatusWorking,
		MaxIterations: 8, Workspace: root,
		Spec: Spec{Units: []PlanUnit{{Title: "write summary", Artifacts: []string{"summary.md"}}}},
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "wrote it"}}}
	d := testDriver(store, runner)

	driveN(t, d, "m1", 2)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone || m.Status != StatusDone {
		t.Fatalf("mission = %+v, want done/done without a review round", m)
	}
}

// TestDriverArtifactCheckBlocksTautologicalDone: the worker claims
// done but never wrote the declared artifact — the harness check must
// send it back to execute (self-retry), never let the claim stand.
func TestDriverArtifactCheckBlocksTautologicalDone(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "research", Phase: PhaseExecute, Status: StatusWorking,
		MaxIterations: 8, Workspace: t.TempDir(),
		Spec: Spec{Units: []PlanUnit{{Title: "write summary", Artifacts: []string{"summary.md"}, VerifyCmd: "echo done"}}},
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "wrote it (no it didn't)"}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseExecute || m.Spec.Units[0].Passes {
		t.Fatalf("mission = phase %s passes %v, want back in execute with the unit NOT passed", m.Phase, m.Spec.Units[0].Passes)
	}
	if m.Iteration == 0 {
		t.Fatal("a failed artifact check must cost an iteration")
	}
}

// TestDriverCodingMissionsAlwaysReview: the skip gate must never apply
// to coding missions — a diff can be wrong in ways existence checks
// cannot see.
func TestDriverCodingMissionsAlwaysReview(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "coding", Phase: PhaseExecute, Status: StatusWorking,
		MaxIterations: 8, Workspace: root,
		Spec: Spec{Units: []PlanUnit{{Title: "write code", Artifacts: []string{"main.go"}}}},
	})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "wrote it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)

	driveN(t, d, "m1", 3)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission = %+v, want done via a real review round", m)
	}
	if m.Phase == PhaseDone && runner.reviewIdx == 0 && len(runner.reviewVerdicts) == 1 {
		// reviewIdx stays 0 when only one verdict exists and it was
		// consumed; assert the review actually ran via phase history:
		// a skipped review would have finished in 2 Advance calls with
		// no review phase. The phase_started events record it.
		found := false
		for _, ev := range store.events["m1"] {
			if ev.Kind == "mission.review_skipped" {
				found = true
			}
		}
		if found {
			t.Fatal("coding mission skipped review — the gate must not apply to coding kind")
		}
	}
	// An approval must leave a verdict event — otherwise a review that
	// ran is indistinguishable from one that never happened.
	approved := false
	for _, ev := range store.events["m1"] {
		if ev.Kind == "mission.review_verdict" && strings.Contains(string(ev.Payload), `"decision":"approved"`) {
			approved = true
		}
	}
	if !approved {
		t.Fatal("approved review left no mission.review_verdict event")
	}
}

// TestDriverEscalatesRepeatedRework: a second review rejection with
// the IDENTICAL gap fingerprint drops the resumed gatekeeper session,
// so any subsequent round starts with fresh eyes instead of the same
// session re-asserting its anchored verdict.
func TestDriverEscalatesRepeatedRework(t *testing.T) {
	findings := []Finding{{Title: "missing depth", File: "summary.md"}}
	fp := GapFingerprint(findings)
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "research", Phase: PhaseReview, Status: StatusWorking,
		MaxIterations: 8, Workspace: t.TempDir(), LastGapFingerprint: fp, StallCount: 1,
		Spec: Spec{Units: []PlanUnit{{Title: "write summary"}}},
	})
	runner := &scriptedRunner{reviewVerdicts: []ReviewVerdict{{Approved: false, Findings: findings}}}
	d := testDriver(store, runner)
	d.gatekeepers["m1"] = &GatekeeperState{}

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if _, ok := d.gatekeepers["m1"]; ok {
		t.Fatal("gatekeeper state survived a repeated identical rework — must be dropped for fresh-eyes review")
	}
}

// TestDriverModelFloorPausesImmediately: ErrModelFloor from the runner
// must pause the mission as infra on the FIRST turn — not accrue
// worker_failed rounds toward backoff.
func TestDriverModelFloorPausesImmediately(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "research", Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{workerErr: fmt.Errorf("%w: amazon.nova-lite-v1:0", ErrModelFloor)}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseInfra {
		t.Fatalf("mission after below-floor turn = %s/%s, want paused/infra immediately", m.Status, m.PauseReason)
	}
}
