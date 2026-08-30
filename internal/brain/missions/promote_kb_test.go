package missions

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingPromoteKB is a PromoteKB fake that records every call it
// receives, synchronized so tests can assert on it after the driver's
// dispatching goroutine has had a chance to run (fired via `go` from
// promoteToKB, same as DestinationDeliver).
type recordingPromoteKB struct {
	mu    sync.Mutex
	calls []struct {
		mission      Mission
		collectionID string
	}
}

func (r *recordingPromoteKB) fn() PromoteKB {
	return func(ctx context.Context, m Mission, collectionID string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, struct {
			mission      Mission
			collectionID string
		}{m, collectionID})
	}
}

func (r *recordingPromoteKB) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func waitForPromoteKBCalls(t *testing.T, r *recordingPromoteKB, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.count() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("promote kb calls = %d, want %d", r.count(), want)
}

func TestDriverPromotesToKBOnDone(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8, PromoteKBCollectionID: "c1"})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingPromoteKB{}
	d.SetPromoteKB(rec.fn())

	driveN(t, d, "m1", 4) // explore -> plan -> execute -> review -> done

	waitForPromoteKBCalls(t, rec, 1)
	if got := rec.calls[0].collectionID; got != "c1" {
		t.Fatalf("collection id = %q, want c1", got)
	}
}

func TestDriverSkipsPromoteKBOnFailed(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 1, PromoteKBCollectionID: "c1"})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "retry", Analysis: "nope"}},
	}
	d := testDriver(store, runner)
	rec := &recordingPromoteKB{}
	d.SetPromoteKB(rec.fn())

	// MaxIterations=1: the first retry immediately exhausts iterations
	// and the state machine fails the mission (stepWorkerRetry).
	driveN(t, d, "m1", 1)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseFailed {
		t.Fatalf("mission phase = %s, want failed", m.Phase)
	}
	time.Sleep(20 * time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("promote kb calls on a failed mission = %d, want 0 (artifacts of a failed mission are unverified)", got)
	}
}

func TestDriverSkipsPromoteKBWhenNoCollection(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingPromoteKB{}
	d.SetPromoteKB(rec.fn())

	driveN(t, d, "m1", 4)

	time.Sleep(20 * time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("promote kb calls for a mission with no promote_kb_collection_id = %d, want 0", got)
	}
}

func TestDriverSkipsPromoteKBWhenNilHook(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8, PromoteKBCollectionID: "c1"})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner) // no SetPromoteKB call: d.promoteKB stays nil

	// This must not panic: the whole point of the nil-safe field.
	driveN(t, d, "m1", 4)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %s, want done", m.Phase)
	}
}
