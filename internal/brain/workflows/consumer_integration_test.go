//go:build integration

package workflows

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// TestRegressionCrashAfterTerminalCommitLeftRunStuckRunning reproduces
// the stuck running run from before D-117: the step mission's terminal
// transition commits, then the process dies before anything reacts
// (no drain here). The run stays running on the old step. One drain
// afterwards advances it, and a redelivery of the same event spawns
// nothing more.
func TestRegressionCrashAfterTerminalCommitLeftRunStuckRunning(t *testing.T) {
	wfStore := testStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := wfStore.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		runs := `SELECT id FROM workflow_runs WHERE workflow_id IN (SELECT id FROM workflows WHERE name LIKE $1 || '%')`
		_, _ = conn.Exec(cctx, `DELETE FROM events WHERE source = 'mission' AND dedup_key IN (SELECT id::text FROM missions WHERE workflow_run_id IN (`+runs+`))`, marker)
		_, _ = conn.Exec(cctx, `DELETE FROM missions WHERE workflow_run_id IN (`+runs+`)`, marker)
		_, _ = conn.Exec(cctx, `DELETE FROM workflow_runs WHERE workflow_id IN (SELECT id FROM workflows WHERE name LIKE $1 || '%')`, marker)
		_, _ = conn.Exec(cctx, `DELETE FROM workflows WHERE name LIKE $1 || '%'`, marker)
	})

	def, _ := json.Marshal(Definition{
		Entry: "build",
		Steps: map[string]Step{
			"build":  {Goal: marker + "build", Kind: "general", Route: "default"},
			"review": {Goal: marker + "review {{outcome}}", Kind: "general", Route: "default"},
		},
		Edges: []Edge{
			{From: "build", On: "mission.done", To: "review", MaxIterations: 1},
			{From: "review", On: "mission.done", To: "end", MaxIterations: 1},
		},
	})
	wfID, err := wfStore.Create(ctx, marker+"crash-consume", def)
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	missionStore := missions.NewStore(wfStore.db, log)
	e := NewEngine(wfStore, storeSpawner{missionStore}, missionStore, log)
	runID, err := e.StartRun(ctx, wfID, nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var buildID string
	if err := db.QueryRow(ctx, `SELECT id FROM missions WHERE workflow_run_id = $1 AND workflow_step = 'build'`, runID).Scan(&buildID); err != nil {
		t.Fatalf("query build mission: %v", err)
	}

	// The terminal commit, then the "crash": no drainer runs.
	if err := missionStore.ApplyTransition(ctx, buildID, missions.Transition{
		Next:   missions.StepState{Phase: missions.PhaseDone, Status: missions.StatusDone, MaxIterations: 3},
		Events: []missions.EventDraft{{Kind: "mission.done", Payload: map[string]any{}}},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}
	run, err := wfStore.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "running" || run.CurrentStep != "build" {
		t.Fatalf("run after crash = %s/%s, want running/build", run.Status, run.CurrentStep)
	}

	// Restart: the drainer's boot tick consumes the pending event. Other
	// pending rows are settled so this drain sees only this run's event.
	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = now() WHERE processed_at IS NULL AND NOT (source = 'mission' AND dedup_key = $1)`, buildID); err != nil {
		t.Fatalf("settle other events: %v", err)
	}
	drainer := events.NewDrainer(events.NewStore(wfStore.db), []events.Consumer{e}, nil, log)
	if _, err := drainer.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	run, err = wfStore.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "running" || run.CurrentStep != "review" {
		t.Fatalf("run after drain = %s/%s, want running/review", run.Status, run.CurrentStep)
	}

	// Redelivery (a crash before the drain committed) spawns nothing more.
	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = NULL WHERE source = 'mission' AND dedup_key = $1`, buildID); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := drainer.Drain(ctx); err != nil {
		t.Fatalf("second drain: %v", err)
	}
	var reviews int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM missions WHERE workflow_run_id = $1 AND workflow_step = 'review'`, runID).Scan(&reviews); err != nil {
		t.Fatalf("count review missions: %v", err)
	}
	if reviews != 1 {
		t.Fatalf("review missions after redelivery = %d, want 1", reviews)
	}
	var lastError *string
	if err := db.QueryRow(ctx, `SELECT last_error FROM events WHERE source = 'mission' AND dedup_key = $1`, buildID).Scan(&lastError); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if lastError != nil {
		t.Fatalf("event last_error = %q, want none", *lastError)
	}
}
