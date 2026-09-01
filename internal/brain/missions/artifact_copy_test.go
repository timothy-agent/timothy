package missions

import (
	"context"
	"testing"
)

// TestDriverCopiesArtifactsBeforeDestinationDelivery covers copyArtifacts'
// ordering guarantee: the artifact-copy hook runs and its refs are
// persisted BEFORE destinations delivery sees the mission, so a
// webhook payload built from the same reloaded Mission can include
// them. Both now run synchronously within the result phase's step.
func TestDriverCopiesArtifactsBeforeDestinationDelivery(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true, Destinations: []DestinationEntry{{DestinationID: "d1"}}})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	wantRefs := []ArtifactRef{{ID: "att-1", Mime: "text/plain", Name: "out.md"}}
	d.SetArtifactCopy(func(ctx context.Context, m Mission) []ArtifactRef {
		return wantRefs
	})
	deliverRec := &recordingDeliver{}
	d.SetDestinationDeliver(deliverRec.fn())

	driveN(t, d, "m1", 5) // discover -> plan -> generate -> prove -> result -> done

	if got := deliverRec.count(); got != 1 {
		t.Fatalf("deliver calls = %d, want 1", got)
	}
	got := deliverRec.calls[0].mission.ArtifactRefs
	if len(got) != 1 || got[0] != wantRefs[0] {
		t.Fatalf("destination delivery saw ArtifactRefs = %+v, want %+v", got, wantRefs)
	}

	m, _ := store.Get(context.Background(), "m1")
	if len(m.ArtifactRefs) != 1 || m.ArtifactRefs[0] != wantRefs[0] {
		t.Fatalf("persisted mission ArtifactRefs = %+v, want %+v", m.ArtifactRefs, wantRefs)
	}
}

// TestDriverSkipsArtifactCopyOnFailed covers the same never-reaches-
// result guarantee: a mission that fails before its last unit is
// verified never enters the result phase, so the artifact-copy hook
// never runs.
func TestDriverSkipsArtifactCopyOnFailed(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 1})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "retry", Analysis: "nope"}},
	}
	d := testDriver(store, runner)
	called := false
	d.SetArtifactCopy(func(ctx context.Context, m Mission) []ArtifactRef {
		called = true
		return nil
	})

	driveN(t, d, "m1", 1)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseFailed {
		t.Fatalf("mission phase = %s, want failed", m.Phase)
	}
	if called {
		t.Fatal("artifact copy hook must not run when the mission never reaches result")
	}
}

// TestDriverSkipsArtifactCopyWhenNilHook confirms the nil-safe default
// never panics and leaves ArtifactRefs empty.
func TestDriverSkipsArtifactCopyWhenNilHook(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner) // no SetArtifactCopy call: d.artifactCopy stays nil

	driveN(t, d, "m1", 5)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %s, want done", m.Phase)
	}
	if len(m.ArtifactRefs) != 0 {
		t.Fatalf("ArtifactRefs = %+v, want none", m.ArtifactRefs)
	}
}
