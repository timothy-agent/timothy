package missions

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// recordingDeliver is a DestinationDeliver fake that records every call
// it receives: called SYNCHRONOUSLY from the result phase's step
// (D-086), so no dispatch-goroutine wait is needed. err, when set, is
// returned on every call (scripts a delivery failure without a real
// Deliverer).
type recordingDeliver struct {
	mu    sync.Mutex
	calls []struct {
		missionID string
		destIDs   []string
		mission   Mission
	}
	err error
}

func (r *recordingDeliver) fn() DestinationDeliver {
	return func(ctx context.Context, m Mission, destinationIDs []string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, struct {
			missionID string
			destIDs   []string
			mission   Mission
		}{m.ID, destinationIDs, m})
		return r.err
	}
}

func (r *recordingDeliver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func TestDriverDeliversToDestinationsOnDone(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, DestinationIDs: []string{"d1", "d2"}})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingDeliver{}
	d.SetDestinationDeliver(rec.fn())

	driveN(t, d, "m1", 5) // discover -> plan -> generate -> prove -> result -> done

	if got := rec.count(); got != 1 {
		t.Fatalf("deliver calls = %d, want 1", got)
	}
	if got := rec.calls[0].destIDs; len(got) != 2 || got[0] != "d1" || got[1] != "d2" {
		t.Fatalf("destination ids = %v, want [d1 d2]", got)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %s, want done", m.Phase)
	}
}

func TestDriverSkipsDeliveryOnFailed(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 1, DestinationIDs: []string{"d1"}})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "retry", Analysis: "nope"}},
	}
	d := testDriver(store, runner)
	rec := &recordingDeliver{}
	d.SetDestinationDeliver(rec.fn())

	// MaxIterations=1: the first retry immediately exhausts iterations
	// and the state machine fails the mission (stepWorkerRetry) before
	// it ever reaches result.
	driveN(t, d, "m1", 1)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseFailed {
		t.Fatalf("mission phase = %s, want failed", m.Phase)
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("deliver calls on a failed mission = %d, want 0 (a mission that fails never reaches result)", got)
	}
}

func TestDriverSkipsDeliveryWhenNoDestinations(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingDeliver{}
	d.SetDestinationDeliver(rec.fn())

	driveN(t, d, "m1", 5)

	if got := rec.count(); got != 0 {
		t.Fatalf("deliver calls for a mission with no destination_ids = %d, want 0", got)
	}
}

// TestDriverBackfillsNameBeforeDestinationDelivery covers D-086's
// ordering guarantee: runResult backfills a missing name and reloads
// the mission BEFORE handing it to the destinations hook, so a
// mission that reaches result with no name still delivers with the
// backfilled one rather than empty.
func TestDriverBackfillsNameBeforeDestinationDelivery(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, DestinationIDs: []string{"d1"}})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	nameRec := &recordingNameMission{name: "Backfilled Name"}
	d.SetNameMission(nameRec.fn())
	deliverRec := &recordingDeliver{}
	d.SetDestinationDeliver(deliverRec.fn())

	driveN(t, d, "m1", 5) // discover -> plan -> generate -> prove -> result -> done

	if got := deliverRec.count(); got != 1 {
		t.Fatalf("deliver calls = %d, want 1", got)
	}
	if got := deliverRec.calls[0].mission.Name; got != "Backfilled Name" {
		t.Fatalf("destination delivery saw mission name = %q, want %q (backfilled before delivery)", got, "Backfilled Name")
	}
}

func TestDriverSkipsDeliveryWhenNilHook(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, DestinationIDs: []string{"d1"}})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner) // no SetDestinationDeliver call: d.deliverDestinations stays nil

	// This must not panic — the whole point of the nil-safe field.
	driveN(t, d, "m1", 5)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %s, want done", m.Phase)
	}
}

// TestDriverParksInResultOnDeliveryFailure covers D-086's core
// guarantee: a delivery failure at result parks the mission IN result
// (not failed) with a visible pause reason, instead of the old
// behavior of logging the failure and losing it on the terminal
// transition.
func TestDriverParksInResultOnDeliveryFailure(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, DestinationIDs: []string{"d1"}})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingDeliver{err: errors.New("destination unreachable")}
	d.SetDestinationDeliver(rec.fn())

	driveN(t, d, "m1", 5) // discover -> plan -> generate -> prove -> result(parked)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseResult {
		t.Fatalf("mission phase = %s, want result (parked, work product not lost)", m.Phase)
	}
	if m.Status != StatusPaused || m.PauseReason != PauseInfra {
		t.Fatalf("mission status/pause_reason = %s/%s, want paused/infra", m.Status, m.PauseReason)
	}
}

// TestDriverResultRetryOnlyRedeliversFailedDestination covers the
// result step's idempotency contract: a first result round fails one
// destination and parks; resuming re-runs the step and only the
// previously-failed destination is retried (a destination already
// recorded delivered is never re-sent).
func TestDriverResultRetryOnlyRedeliversFailedDestination(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, DestinationIDs: []string{"d1", "d2"}})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)

	fake := &idempotentFakeDeliverer{failOnce: map[string]bool{"d2": true}}
	d.SetDestinationDeliver(fake.deliver)

	driveN(t, d, "m1", 5) // ... -> result parks: d1 delivered, d2 failed

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseResult || m.Status != StatusPaused {
		t.Fatalf("mission phase/status = %s/%s, want result/paused after first round", m.Phase, m.Status)
	}
	if got := fake.callsFor("d1"); got != 1 {
		t.Fatalf("d1 delivery attempts after first round = %d, want 1", got)
	}
	if got := fake.callsFor("d2"); got != 1 {
		t.Fatalf("d2 delivery attempts after first round = %d, want 1", got)
	}

	// Resume: the second round must retry only d2.
	if err := d.Signal(context.Background(), "m1", InputResume); err != nil {
		t.Fatalf("Signal(resume): %v", err)
	}
	driveN(t, d, "m1", 1)

	m, _ = store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %s, want done after the retry delivers d2", m.Phase)
	}
	if got := fake.callsFor("d1"); got != 1 {
		t.Fatalf("d1 delivery attempts after retry = %d, want 1 (never re-sent once delivered)", got)
	}
	if got := fake.callsFor("d2"); got != 2 {
		t.Fatalf("d2 delivery attempts after retry = %d, want 2 (retried once)", got)
	}
}

// idempotentFakeDeliverer models destinations.Deliverer's own
// alreadyDelivered contract closely enough to test result-step retry
// behavior without a real Postgres-backed events store: a destination
// once delivered is tracked as such and never re-sent; one that
// failed stays retryable.
type idempotentFakeDeliverer struct {
	mu        sync.Mutex
	failOnce  map[string]bool // destination id -> fail exactly the next attempt
	delivered map[string]bool
	attempts  map[string]int
}

func (f *idempotentFakeDeliverer) callsFor(destID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts[destID]
}

func (f *idempotentFakeDeliverer) deliver(ctx context.Context, m Mission, destinationIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attempts == nil {
		f.attempts = map[string]int{}
	}
	if f.delivered == nil {
		f.delivered = map[string]bool{}
	}

	var failed []string
	for _, id := range destinationIDs {
		if f.delivered[id] {
			continue // delivered on a prior attempt: never re-sent
		}
		f.attempts[id]++
		if f.failOnce[id] {
			f.failOnce[id] = false // only the next attempt fails
			failed = append(failed, id)
			continue
		}
		f.delivered[id] = true
	}
	if len(failed) > 0 {
		return errors.New("delivery failed for: " + failed[0])
	}
	return nil
}
