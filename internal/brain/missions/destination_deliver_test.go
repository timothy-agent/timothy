package missions

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingDeliver is a DestinationDeliver fake that records every call
// it receives, synchronized so tests can assert on it after the
// driver's dispatching goroutine has had a chance to run (Deliver is
// fired via `go` from deliverToDestinations, same as MemoryExtract).
type recordingDeliver struct {
	mu    sync.Mutex
	calls []struct {
		missionID string
		destIDs   []string
	}
}

func (r *recordingDeliver) fn() DestinationDeliver {
	return func(ctx context.Context, m Mission, destinationIDs []string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, struct {
			missionID string
			destIDs   []string
		}{m.ID, destinationIDs})
	}
}

func (r *recordingDeliver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func waitForDeliverCalls(t *testing.T, r *recordingDeliver, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.count() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("deliver calls = %d, want %d", r.count(), want)
}

func TestDriverDeliversToDestinationsOnDone(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8, DestinationIDs: []string{"d1", "d2"}})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingDeliver{}
	d.SetDestinationDeliver(rec.fn())

	driveN(t, d, "m1", 4) // explore -> plan -> execute -> review -> done

	waitForDeliverCalls(t, rec, 1)
	if got := rec.calls[0].destIDs; len(got) != 2 || got[0] != "d1" || got[1] != "d2" {
		t.Fatalf("destination ids = %v, want [d1 d2]", got)
	}
}

func TestDriverSkipsDeliveryOnFailed(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 1, DestinationIDs: []string{"d1"}})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "retry", Analysis: "nope"}},
	}
	d := testDriver(store, runner)
	rec := &recordingDeliver{}
	d.SetDestinationDeliver(rec.fn())

	// MaxIterations=1: the first retry immediately exhausts iterations
	// and the state machine fails the mission (stepWorkerRetry).
	driveN(t, d, "m1", 1)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseFailed {
		t.Fatalf("mission phase = %s, want failed", m.Phase)
	}
	time.Sleep(20 * time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("deliver calls on a failed mission = %d, want 0 (v1 never delivers on failure)", got)
	}
}

func TestDriverSkipsDeliveryWhenNoDestinations(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingDeliver{}
	d.SetDestinationDeliver(rec.fn())

	driveN(t, d, "m1", 4)

	time.Sleep(20 * time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("deliver calls for a mission with no destination_ids = %d, want 0", got)
	}
}

func TestDriverSkipsDeliveryWhenNilHook(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8, DestinationIDs: []string{"d1"}})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner) // no SetDestinationDeliver call: d.deliverDestinations stays nil

	// This must not panic — the whole point of the nil-safe field.
	driveN(t, d, "m1", 4)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %s, want done", m.Phase)
	}
}
