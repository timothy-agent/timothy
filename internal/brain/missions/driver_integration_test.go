//go:build integration

package missions

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"testing"
)

// TestDriverEndToEndCodingMission is M2's exit criterion: one mission
// driven fully through research -> plan -> execute -> review -> done
// against a real Postgres Store and a real git Workspace, with a
// SCRIPTED FAKE Runner standing in for the model (no live model call,
// no gateway dependency).
func TestDriverEndToEndCodingMission(t *testing.T) {
	requireGit(t)
	store := testStore(t)
	ctx := context.Background()

	repo := scratchRepo(t)
	wsRoot := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	workspace := NewWorkspace(wsRoot, nil, log)

	id, err := store.Create(ctx, Mission{Goal: marker + "e2e coding", Kind: "coding", Route: "default", ReviewRoute: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ws, wt, branch, base, err := workspace.Provision(ctx, id, marker+"e2e coding", "coding", repo)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := store.SetProvisioned(ctx, id, ws, wt, branch, base); err != nil {
		t.Fatalf("SetProvisioned: %v", err)
	}

	runner := &scriptedRunner{
		plans: []Spec{{Units: []PlanUnit{{Title: "add a file", VerifyCmd: "test -f new-file.txt"}}}},
		workerVerdicts: []WorkerVerdict{
			{Outcome: "done", Evidence: "created new-file.txt"},
		},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := NewDriver(store, runner, workspace, nil, nil, nil, log)

	// The scripted worker "does the work" by actually creating the file
	// the verify_cmd checks for — RunVerify runs for real against the
	// worktree, so the harness's own evidence must be genuine, not
	// merely claimed.
	if err := os.WriteFile(wt+"/new-file.txt", []byte("hello"), 0o644); err != nil {
		t.Fatalf("write new-file.txt: %v", err)
	}
	// Commit it so review's git diff has something real to see (a
	// worker would normally commit via CommitUnit itself; this test
	// pre-stages the file since the scripted runner doesn't).
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = wt
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	cmd = exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@localhost", "commit", "-m", "add file")
	cmd.Dir = wt
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}

	// research -> plan -> execute -> review -> done: drive to
	// completion (bounded, so a design bug can't hang the test suite).
	for i := 0; i < 10; i++ {
		cont, err := d.Advance(ctx, id)
		if err != nil {
			t.Fatalf("Advance[%d]: %v", i, err)
		}
		if !cont {
			break
		}
	}

	m, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Phase != PhaseDone || m.Status != StatusDone {
		t.Fatalf("mission after full drive = %+v, want done/done", m)
	}
	if len(m.Spec.Units) != 1 || !m.Spec.Units[0].Passes {
		t.Fatalf("mission spec after drive = %+v, want the unit verified", m.Spec)
	}

	events, err := store.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	kinds := map[string]bool{}
	for _, e := range events {
		kinds[e.Kind] = true
	}
	for _, want := range []string{"mission.provisioned", "mission.plan_created", "mission.phase_started", "mission.unit_verified", "mission.done"} {
		if !kinds[want] {
			t.Errorf("event log missing %q; got kinds %v", want, kinds)
		}
	}

	if err := workspace.Teardown(ctx, ws, wt, "coding"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
}
