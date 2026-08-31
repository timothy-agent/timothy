package missions

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// recordingPromoteKB is a PromoteKB fake that records every call it
// receives: called SYNCHRONOUSLY from the result phase's step
// (D-086), no dispatch-goroutine wait needed. err, when set, is
// returned on every call.
type recordingPromoteKB struct {
	mu    sync.Mutex
	calls []struct {
		mission      Mission
		collectionID string
	}
	err error
}

func (r *recordingPromoteKB) fn() PromoteKB {
	return func(ctx context.Context, m Mission, collectionID string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, struct {
			mission      Mission
			collectionID string
		}{m, collectionID})
		return r.err
	}
}

func (r *recordingPromoteKB) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func TestDriverPromotesToKBOnDone(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, PromoteKBCollectionID: "c1"})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingPromoteKB{}
	d.SetPromoteKB(rec.fn())

	driveN(t, d, "m1", 5) // discover -> plan -> generate -> prove -> result -> done

	if got := rec.count(); got != 1 {
		t.Fatalf("promote kb calls = %d, want 1", got)
	}
	if got := rec.calls[0].collectionID; got != "c1" {
		t.Fatalf("collection id = %q, want c1", got)
	}
}

func TestDriverSkipsPromoteKBOnFailed(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 1, PromoteKBCollectionID: "c1"})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "retry", Analysis: "nope"}},
	}
	d := testDriver(store, runner)
	rec := &recordingPromoteKB{}
	d.SetPromoteKB(rec.fn())

	// MaxIterations=1: the first retry immediately exhausts iterations
	// and the state machine fails the mission (stepWorkerRetry) before
	// it ever reaches result.
	driveN(t, d, "m1", 1)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseFailed {
		t.Fatalf("mission phase = %s, want failed", m.Phase)
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("promote kb calls on a failed mission = %d, want 0 (a mission that fails never reaches result)", got)
	}
}

func TestDriverSkipsPromoteKBWhenNoCollection(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingPromoteKB{}
	d.SetPromoteKB(rec.fn())

	driveN(t, d, "m1", 5)

	if got := rec.count(); got != 0 {
		t.Fatalf("promote kb calls for a mission with no promote_kb_collection_id = %d, want 0", got)
	}
}

func TestDriverSkipsPromoteKBWhenNilHook(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, PromoteKBCollectionID: "c1"})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner) // no SetPromoteKB call: d.promoteKB stays nil

	// This must not panic: the whole point of the nil-safe field.
	driveN(t, d, "m1", 5)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %s, want done", m.Phase)
	}
}

// TestDriverParksInResultOnPromoteKBFailure covers D-086's core
// guarantee for kb promotion, mirroring
// TestDriverParksInResultOnDeliveryFailure: a promotion failure parks
// the mission IN result rather than being logged and lost.
func TestDriverParksInResultOnPromoteKBFailure(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, PromoteKBCollectionID: "c1"})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingPromoteKB{err: errors.New("kb store unavailable")}
	d.SetPromoteKB(rec.fn())

	driveN(t, d, "m1", 5)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseResult {
		t.Fatalf("mission phase = %s, want result (parked, work product not lost)", m.Phase)
	}
	if m.Status != StatusPaused || m.PauseReason != PauseInfra {
		t.Fatalf("mission status/pause_reason = %s/%s, want paused/infra", m.Status, m.PauseReason)
	}
}
