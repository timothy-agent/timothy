package missions

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
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
	f.events[id] = append(f.events[id], Event{MissionID: id, Seq: f.seq[id], Kind: kind})
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

func (r *scriptedRunner) RunReview(ctx context.Context, m Mission, diff string, gk *GatekeeperState) (ReviewVerdict, *GatekeeperState, error) {
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

func testDriver(store driverStore, runner Runner) *Driver {
	return NewDriver(store, runner, nil, nil, slog.Default())
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
