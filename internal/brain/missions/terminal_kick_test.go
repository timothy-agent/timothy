package missions

import (
	"context"
	"sync/atomic"
	"testing"
)

// countingKick records how often the driver kicked the events drainer.
type countingKick struct{ n atomic.Int32 }

func (c *countingKick) fn() func() { return func() { c.n.Add(1) } }

func TestDriverKicksEventsOnTerminalTransition(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true, WorkflowRunID: "run-1", WorkflowStep: "step-a"})
	runner := &scriptedRunner{
		plans:          []Plan{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	kick := &countingKick{}
	d.SetEventsKick(kick.fn())

	driveN(t, d, "m1", 4) // discover -> plan -> build -> prove -> result
	if got := kick.n.Load(); got != 0 {
		t.Fatalf("kicks before the terminal transition = %d, want 0", got)
	}
	driveN(t, d, "m1", 1) // result -> done
	if got := kick.n.Load(); got != 1 {
		t.Fatalf("kicks after done = %d, want 1", got)
	}
}

func TestDriverKicksEventsOnCancel(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseBuild, Status: StatusPaused, MaxIterations: 8})
	d := testDriver(store, &scriptedRunner{})
	kick := &countingKick{}
	d.SetEventsKick(kick.fn())

	if err := d.Signal(context.Background(), "m1", InputCancel); err != nil {
		t.Fatalf("Signal cancel: %v", err)
	}
	if got := kick.n.Load(); got != 1 {
		t.Fatalf("kicks after cancel = %d, want 1", got)
	}
}

func TestDriverTerminalWithoutKickDoesNotPanic(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true, WorkflowRunID: "run-1"})
	runner := &scriptedRunner{
		plans:          []Plan{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner) // no SetEventsKick: d.kickEvents stays nil

	driveN(t, d, "m1", 5)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %s, want done", m.Phase)
	}
}
