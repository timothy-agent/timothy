//go:build integration

package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// buildReviewWorkflow creates a build -> review -> end workflow named
// marker+name and registers cleanup of its runs, missions and events.
func buildReviewWorkflow(t *testing.T, wfStore *Store, name string) string {
	t.Helper()
	ctx := context.Background()
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
	wfID, err := wfStore.Create(ctx, marker+name, def)
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	return wfID
}

// finishBuildStep finds runID's build mission and commits its terminal
// transition, which inserts the mission.done inbox event. Other pending
// events are settled so the next drain sees only this one.
func finishBuildStep(t *testing.T, db *pgxpool.Pool, missionStore *missions.Store, runID string) string {
	t.Helper()
	ctx := context.Background()
	var buildID string
	if err := db.QueryRow(ctx, `SELECT id FROM missions WHERE workflow_run_id = $1 AND workflow_step = 'build'`, runID).Scan(&buildID); err != nil {
		t.Fatalf("query build mission: %v", err)
	}
	if err := missionStore.ApplyTransition(ctx, buildID, missions.Transition{
		Next:   missions.StepState{Phase: missions.PhaseDone, Status: missions.StatusDone, MaxIterations: 3},
		Events: []missions.EventDraft{{Kind: "mission.done", Payload: map[string]any{}}},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = now() WHERE processed_at IS NULL AND NOT (source = 'mission' AND dedup_key = $1)`, buildID); err != nil {
		t.Fatalf("settle other events: %v", err)
	}
	return buildID
}

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
	wfID := buildReviewWorkflow(t, wfStore, "crash-consume")
	missionStore := missions.NewStore(wfStore.db, log)
	e := NewEngine(wfStore, storeSpawner{missionStore}, missionStore, log)
	runID, err := e.StartRun(ctx, wfID, nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// The terminal commit, then the "crash": no drainer runs.
	buildID := finishBuildStep(t, db, missionStore, runID)
	run, err := wfStore.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "running" || run.CurrentStep != "build" {
		t.Fatalf("run after crash = %s/%s, want running/build", run.Status, run.CurrentStep)
	}

	// Restart: the drainer's boot tick consumes the pending event.
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

// failEdgeOnce fails the first edge.taken record, standing in for a
// crash or DB error after the step mission was spawned.
type failEdgeOnce struct {
	*Store
	failed atomic.Bool
}

func (s *failEdgeOnce) ApplyRunTransition(ctx context.Context, id string, t RunTransition) error {
	if len(t.Events) > 0 && t.Events[0].Kind == "edge.taken" && s.failed.CompareAndSwap(false, true) {
		return errors.New("injected edge.taken record failure")
	}
	return s.Store.ApplyRunTransition(ctx, id, t)
}

// TestRegressionSpawnCommittedThenRecordFailed covers issue #842: the
// review mission commits but edge.taken fails. The event stays pending
// with the error, the run stays on build, and the retry adopts the
// review mission instead of spawning a second one.
func TestRegressionSpawnCommittedThenRecordFailed(t *testing.T) {
	wfStore := testStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := wfStore.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wfID := buildReviewWorkflow(t, wfStore, "record-fail")
	missionStore := missions.NewStore(wfStore.db, log)
	e := NewEngine(&failEdgeOnce{Store: wfStore}, storeSpawner{missionStore}, missionStore, log)
	runID, err := e.StartRun(ctx, wfID, nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	buildID := finishBuildStep(t, db, missionStore, runID)
	drainer := events.NewDrainer(events.NewStore(wfStore.db), []events.Consumer{e}, nil, log)

	reviewMissions := func() (int, string) {
		t.Helper()
		var n int
		var id string
		if err := db.QueryRow(ctx, `SELECT count(*), COALESCE(min(id::text), '') FROM missions WHERE workflow_run_id = $1 AND workflow_step = 'review'`, runID).Scan(&n, &id); err != nil {
			t.Fatalf("count review missions: %v", err)
		}
		return n, id
	}

	n, err := drainer.Drain(ctx)
	if err != nil {
		t.Fatalf("first drain: %v", err)
	}
	if n == 0 {
		t.Fatal("first drain handled no events (another drainer holds the lock?)")
	}
	var attempts int
	var lastError *string
	var processed bool
	if err := db.QueryRow(ctx, `SELECT attempts, last_error, processed_at IS NOT NULL FROM events WHERE source = 'mission' AND dedup_key = $1`, buildID).Scan(&attempts, &lastError, &processed); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if attempts != 1 || lastError == nil || processed {
		t.Fatalf("event after failed record: attempts=%d last_error=%v processed=%v, want 1, set, false", attempts, lastError, processed)
	}
	if reviews, _ := reviewMissions(); reviews != 1 {
		t.Fatalf("review missions after first drain = %d, want 1", reviews)
	}
	run, err := wfStore.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "running" || run.CurrentStep != "build" {
		t.Fatalf("run after failed record = %s/%s, want running/build", run.Status, run.CurrentStep)
	}

	if _, err := drainer.Drain(ctx); err != nil {
		t.Fatalf("second drain: %v", err)
	}
	reviews, reviewID := reviewMissions()
	if reviews != 1 {
		t.Fatalf("review missions after retry = %d, want 1", reviews)
	}
	run, err = wfStore.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "running" || run.CurrentStep != "review" {
		t.Fatalf("run after retry = %s/%s, want running/review", run.Status, run.CurrentStep)
	}
	runEvents, err := wfStore.RunEvents(ctx, runID)
	if err != nil {
		t.Fatalf("RunEvents: %v", err)
	}
	var edges []map[string]any
	for _, ev := range runEvents {
		if ev.Kind != "edge.taken" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode edge.taken: %v", err)
		}
		edges = append(edges, p)
	}
	if len(edges) != 1 || edges[0]["mission_id"] != buildID || edges[0]["spawned_mission_id"] != reviewID {
		t.Fatalf("edge.taken payloads = %v, want one with mission_id %s spawned_mission_id %s", edges, buildID, reviewID)
	}
}

// onlyWorkflow scopes RunsWithoutMissions to one workflow so a recovery
// sweep in a test never touches other runs in the shared database.
type onlyWorkflow struct {
	*Store
	workflowID string
}

func (s onlyWorkflow) RunsWithoutMissions(ctx context.Context, cutoff time.Time) ([]Run, error) {
	runs, err := s.Store.RunsWithoutMissions(ctx, cutoff)
	var out []Run
	for _, r := range runs {
		if r.WorkflowID == s.workflowID {
			out = append(out, r)
		}
	}
	return out, err
}

// TestRegressionCrashBetweenCreateRunAndEntrySpawn covers issue #931: a
// run created without its entry mission (crash after CreateRun) gets
// exactly one entry mission from the boot recovery, a second sweep
// spawns nothing, and a run that already has its entry, a paused run,
// and a run newer than the cutoff are left alone.
func TestRegressionCrashBetweenCreateRunAndEntrySpawn(t *testing.T) {
	wfStore := testStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := wfStore.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wfID := buildReviewWorkflow(t, wfStore, "recover-entry")
	missionStore := missions.NewStore(wfStore.db, log)
	e := NewEngine(onlyWorkflow{Store: wfStore, workflowID: wfID}, storeSpawner{missionStore}, missionStore, log)

	// The crash: the run row commits, the entry spawn never happens.
	orphan, err := wfStore.CreateRun(ctx, wfID, "build", map[string]string{"K": "v"})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	withEntry, err := e.StartRun(ctx, wfID, nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	paused, err := wfStore.CreateRun(ctx, wfID, "build", nil)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := wfStore.ApplyRunTransition(ctx, paused, RunTransition{Status: "paused", CurrentStep: "build"}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	var cutoff time.Time
	if err := db.QueryRow(ctx, `SELECT now()`).Scan(&cutoff); err != nil {
		t.Fatalf("now: %v", err)
	}
	newer, err := wfStore.CreateRun(ctx, wfID, "build", nil)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	runs, err := wfStore.RunsWithoutMissions(ctx, cutoff)
	if err != nil {
		t.Fatalf("RunsWithoutMissions: %v", err)
	}
	var ours []string
	for _, r := range runs {
		if r.WorkflowID == wfID {
			ours = append(ours, r.ID)
		}
	}
	if len(ours) != 1 || ours[0] != orphan {
		t.Fatalf("RunsWithoutMissions for this workflow = %v, want only %s", ours, orphan)
	}

	entryMissions := func(runID string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM missions WHERE workflow_run_id = $1 AND workflow_step = 'build' AND parent_mission_id IS NULL`, runID).Scan(&n); err != nil {
			t.Fatalf("count entry missions: %v", err)
		}
		return n
	}
	for attempt := 1; attempt <= 2; attempt++ {
		n, err := e.RecoverRunsWithoutEntry(ctx, cutoff)
		if err != nil {
			t.Fatalf("sweep %d: RecoverRunsWithoutEntry: %v", attempt, err)
		}
		if want := map[int]int{1: 1, 2: 0}[attempt]; n != want {
			t.Fatalf("sweep %d recovered %d, want %d", attempt, n, want)
		}
		if got := entryMissions(orphan); got != 1 {
			t.Fatalf("sweep %d: orphan entry missions = %d, want 1", attempt, got)
		}
	}
	if got := entryMissions(withEntry); got != 1 {
		t.Fatalf("run with entry has %d entry missions, want 1", got)
	}
	for _, id := range []string{paused, newer} {
		if got := entryMissions(id); got != 0 {
			t.Fatalf("run %s has %d entry missions, want 0", id, got)
		}
	}
	run, err := wfStore.GetRun(ctx, orphan)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "running" || run.CurrentStep != "build" {
		t.Fatalf("recovered run = %s/%s, want running/build", run.Status, run.CurrentStep)
	}
}

// failPauseOnce fails the first run.paused transition.
type failPauseOnce struct {
	*Store
	failed atomic.Bool
}

func (s *failPauseOnce) ApplyRunTransition(ctx context.Context, id string, t RunTransition) error {
	if len(t.Events) > 0 && t.Events[0].Kind == "run.paused" && s.failed.CompareAndSwap(false, true) {
		return errors.New("injected run.paused failure")
	}
	return s.Store.ApplyRunTransition(ctx, id, t)
}

// TestRegressionPauseFailureRetriedByDrainer covers issue #931: a failed
// pause transition keeps the event pending with its error, and the
// drainer's retry pauses the run once.
func TestRegressionPauseFailureRetriedByDrainer(t *testing.T) {
	wfStore := testStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := wfStore.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wfID := buildReviewWorkflow(t, wfStore, "pause-fail")
	missionStore := missions.NewStore(wfStore.db, log)
	e := NewEngine(&failPauseOnce{Store: wfStore}, storeSpawner{missionStore}, missionStore, log)
	runID, err := e.StartRun(ctx, wfID, nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var buildID string
	if err := db.QueryRow(ctx, `SELECT id FROM missions WHERE workflow_run_id = $1 AND workflow_step = 'build'`, runID).Scan(&buildID); err != nil {
		t.Fatalf("query build mission: %v", err)
	}
	// build has no mission.failed edge, so the run pauses.
	if err := missionStore.ApplyTransition(ctx, buildID, missions.Transition{
		Next:   missions.StepState{Phase: missions.PhaseFailed, Status: missions.StatusError, MaxIterations: 3},
		Events: []missions.EventDraft{{Kind: "mission.failed", Payload: map[string]any{}}},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = now() WHERE processed_at IS NULL AND NOT (source = 'mission' AND dedup_key = $1)`, buildID); err != nil {
		t.Fatalf("settle other events: %v", err)
	}
	drainer := events.NewDrainer(events.NewStore(wfStore.db), []events.Consumer{e}, nil, log)

	n, err := drainer.Drain(ctx)
	if err != nil {
		t.Fatalf("first drain: %v", err)
	}
	if n == 0 {
		t.Fatal("first drain handled no events (another drainer holds the lock?)")
	}
	var attempts int
	var lastError *string
	var processed bool
	if err := db.QueryRow(ctx, `SELECT attempts, last_error, processed_at IS NOT NULL FROM events WHERE source = 'mission' AND dedup_key = $1`, buildID).Scan(&attempts, &lastError, &processed); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if attempts != 1 || lastError == nil || processed {
		t.Fatalf("event after failed pause: attempts=%d last_error=%v processed=%v, want 1, set, false", attempts, lastError, processed)
	}
	if run, _ := wfStore.GetRun(ctx, runID); run.Status != "running" {
		t.Fatalf("run after failed pause = %s, want running", run.Status)
	}

	if _, err := drainer.Drain(ctx); err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT processed_at IS NOT NULL FROM events WHERE source = 'mission' AND dedup_key = $1`, buildID).Scan(&processed); err != nil {
		t.Fatalf("read event: %v", err)
	}
	run, err := wfStore.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	runEvents, err := wfStore.RunEvents(ctx, runID)
	if err != nil {
		t.Fatalf("RunEvents: %v", err)
	}
	pauses := 0
	for _, ev := range runEvents {
		if ev.Kind == "run.paused" {
			pauses++
		}
	}
	if !processed || run.Status != "paused" || pauses != 1 {
		t.Fatalf("after retry: processed=%v status=%s run.paused=%d, want true, paused, 1", processed, run.Status, pauses)
	}
}
