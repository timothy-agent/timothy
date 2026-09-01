//go:build integration

package missions

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// TestDriverEndToEndCodingMission is M2's exit criterion: one mission
// driven fully through discover -> plan -> generate -> prove -> done
// against a real Postgres Store and a real git Workspace, with a
// SCRIPTED FAKE Runner standing in for the model (no live model call,
// no gateway dependency).
func TestDriverEndToEndCodingMission(t *testing.T) {
	requireGit(t)
	store := testStore(t)
	ctx := context.Background()

	wsRoot := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	workspace := NewWorkspace(wsRoot, nil, log)

	id, err := store.Create(ctx, Mission{Goal: marker + "e2e coding", Kind: "coding", Route: "default", ReviewRoute: "default", AutoApprovePlan: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ws, wt, branch, base, _, err := workspace.Provision(ctx, id, marker+"e2e coding", "coding", "", "", nil, "", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := store.SetProvisioned(ctx, id, ws, branch, base); err != nil {
		t.Fatalf("SetProvisioned: %v", err)
	}

	runner := &scriptedRunner{
		plans: []Plan{{Units: []PlanUnit{{Title: "add a file", VerifyCmd: "test -f new-file.txt"}}}},
		workerVerdicts: []WorkerVerdict{
			{Outcome: "done", Evidence: "created new-file.txt"},
		},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := NewDriver(store, runner, workspace, nil, nil, nil, fakeSandboxExec, nil, log)

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

	// discover -> plan -> generate -> prove -> done: drive to
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
	if len(m.Plan.Units) != 1 || !m.Plan.Units[0].Passes {
		t.Fatalf("mission spec after drive = %+v, want the unit verified", m.Plan)
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

// TestDriverLazilyProvisionsBareSchedulerStyleMission reproduces the
// plan's defect #1 against a real Postgres store: a mission inserted
// directly (bypassing Driver.Create — exactly what scheduler.go's
// createFromTemplate does) has no session, no workspace, no grants.
// One Advance call must provision all three before running the phase,
// or the worker gets no shell/write_file tools at all.
func TestDriverLazilyProvisionsBareSchedulerStyleMission(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	id, err := store.Create(ctx, Mission{
		Goal: marker + "lazy provisioning", Kind: "general", Route: "default", ReviewRoute: "default",
		AutoApproveTools: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Bare row, exactly as createFromTemplate leaves it: no session_id,
	// no workspace/worktree — confirmed before Advance runs.
	m, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.SessionID != "" || m.Workspace != "" {
		t.Fatalf("mission before Advance = %+v, want no session and no workspace", m)
	}

	wsRoot := t.TempDir()
	workspace := NewWorkspace(wsRoot, nil, log)
	sessions := session.NewStore(store.db, log)
	perms := tools.NewPermissions(store.db, wsRoot)
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, workspace, nil, sessions, perms, nil, nil, log)

	if _, err := d.Advance(ctx, id); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	m, err = store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after Advance: %v", err)
	}
	if m.SessionID == "" {
		t.Fatal("Advance did not provision a hidden session")
	}
	if m.Workspace == "" {
		t.Fatal("Advance did not provision a workspace")
	}

	db, err := store.db.Get()
	if err != nil {
		t.Fatalf("Get pool: %v", err)
	}
	var grantCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM session_grants WHERE session_id = $1 AND tool = 'shell'`,
		m.SessionID).Scan(&grantCount); err != nil {
		t.Fatalf("count session_grants: %v", err)
	}
	if grantCount == 0 {
		t.Fatal("Advance's lazy provisioning did not grant the auto-approve-safe shell allowance")
	}
}
