package missions

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

// blockingPlanRunner holds every planning turn until its ctx is done,
// standing in for a model call that never returns on its own.
type blockingPlanRunner struct {
	*scriptedRunner
	calls   atomic.Int32
	entered chan struct{}
}

func (b *blockingPlanRunner) PlanSession(ctx context.Context, m Mission, notes string) (Plan, error) {
	if b.calls.Add(1) == 1 {
		close(b.entered)
	}
	<-ctx.Done()
	return Plan{}, ctx.Err()
}

// TestDriverCancelAbortsRunningTurn pins D-135: a cancel signal
// cancels the in-flight turn's ctx, the runner is never called again,
// nothing is recorded after the cancel, and the mission ends cancelled.
func TestDriverCancelAbortsRunningTurn(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusWorking, MaxIterations: 8, SessionID: "s1", Workspace: t.TempDir()})
	runner := &blockingPlanRunner{scriptedRunner: &scriptedRunner{}, entered: make(chan struct{})}
	d := testDriver(store, runner)

	driveErr := make(chan error, 1)
	go func() { driveErr <- d.Drive(context.Background(), "m1") }()
	<-runner.entered

	if err := d.Signal(context.Background(), "m1", InputCancel); err != nil {
		t.Fatalf("Signal cancel: %v", err)
	}
	evs, _ := store.Events(context.Background(), "m1")
	atCancel := len(evs)

	select {
	case err := <-driveErr:
		if err != nil {
			t.Fatalf("Drive after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not unblock the running turn")
	}

	if got := runner.calls.Load(); got != 1 {
		t.Fatalf("runner calls = %d, want 1 (no call after cancel)", got)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseFailed {
		t.Fatalf("phase = %s, want failed", m.Phase)
	}
	evs, _ = store.Events(context.Background(), "m1")
	if len(evs) != atCancel {
		t.Fatalf("events after cancel = %+v, want none", evs[atCancel:])
	}
	last := evs[len(evs)-1]
	var payload struct{ Reason string }
	_ = json.Unmarshal(last.Payload, &payload)
	if last.Kind != "mission.failed" || payload.Reason != "cancelled" {
		t.Fatalf("last event = %s %s, want mission.failed reason cancelled", last.Kind, last.Payload)
	}
}

// TestDriverCancelWithoutRunningTurn: a cancel with no turn in flight
// still applies, and a later Advance does nothing.
func TestDriverCancelWithoutRunningTurn(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusIdle, MaxIterations: 8})
	runner := &blockingPlanRunner{scriptedRunner: &scriptedRunner{}, entered: make(chan struct{})}
	d := testDriver(store, runner)

	if err := d.Signal(context.Background(), "m1", InputCancel); err != nil {
		t.Fatalf("Signal cancel: %v", err)
	}
	if cont, err := d.Advance(context.Background(), "m1"); err != nil || cont {
		t.Fatalf("Advance after cancel = %v, %v; want false, nil", cont, err)
	}
	if got := runner.calls.Load(); got != 0 {
		t.Fatalf("runner calls = %d, want 0", got)
	}
}
