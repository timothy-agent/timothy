package missions

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingOnTerminal is an OnTerminal fake that records every mission
// id it's called with, synchronized so tests can assert on it after the
// driver's dispatching goroutine has had a chance to run (fired via
// `go` from fireOnTerminal, same as recordingDeliver above).
type recordingOnTerminal struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingOnTerminal) fn() OnTerminal {
	return func(ctx context.Context, m Mission) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, m.ID)
	}
}

func (r *recordingOnTerminal) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func waitForOnTerminalCalls(t *testing.T, r *recordingOnTerminal, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.count() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("onTerminal calls = %d, want %d", r.count(), want)
}

func TestDriverFiresOnTerminalForWorkflowMission(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8, WorkflowRunID: "run-1", WorkflowStep: "step-a"})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingOnTerminal{}
	d.SetOnTerminal(rec.fn())

	driveN(t, d, "m1", 4) // explore -> plan -> execute -> review -> done

	waitForOnTerminalCalls(t, rec, 1)
	if rec.calls[0] != "m1" {
		t.Fatalf("onTerminal called with mission %q, want m1", rec.calls[0])
	}
}

func TestDriverSkipsOnTerminalWithoutWorkflowRunID(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingOnTerminal{}
	d.SetOnTerminal(rec.fn())

	driveN(t, d, "m1", 4)

	time.Sleep(20 * time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("onTerminal calls for a mission with no workflow_run_id = %d, want 0", got)
	}
}

func TestDriverSkipsOnTerminalWhenNilHook(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8, WorkflowRunID: "run-1"})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner) // no SetOnTerminal call: d.onTerminal stays nil

	// This must not panic — the whole point of the nil-safe field.
	driveN(t, d, "m1", 4)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %s, want done", m.Phase)
	}
}
