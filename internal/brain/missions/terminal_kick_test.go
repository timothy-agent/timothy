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

// TestDriverAdvancePausedKicksEvents: a turn that parks the mission
// commits an actionable events row (issue #922), so the driver kicks
// the drainer instead of leaving the notification to its tick.
func TestDriverAdvancePausedKicksEvents(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseBuild, Status: StatusWorking, MaxIterations: 8, Plan: Plan{Units: []PlanUnit{{Title: "only unit"}}}})
	d := testDriver(store, &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}})
	kick := &countingKick{}
	d.SetEventsKick(kick.fn())

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused && m.Status != StatusWaitingForInput {
		t.Fatalf("status = %s, want an actionable park", m.Status)
	}
	if got := kick.n.Load(); got != 1 {
		t.Fatalf("kicks after park = %d, want 1", got)
	}
}

// TestDriverNonActionableTransitionDoesNotKick: a working -> working
// round commits no events row and kicks nothing.
func TestDriverNonActionableTransitionDoesNotKick(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true})
	d := testDriver(store, &scriptedRunner{plans: []Plan{{Units: []PlanUnit{{Title: "only unit"}}}}})
	kick := &countingKick{}
	d.SetEventsKick(kick.fn())

	driveN(t, d, "m1", 1)
	if got := kick.n.Load(); got != 0 {
		t.Fatalf("kicks after a working round = %d, want 0", got)
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
