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
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

// marker prefixes every test workflow's name so sweep can find and
// remove them without touching unrelated rows — mirrors
// missions/store_integration_test.go's own marker.
const marker = "itest-workflow-"

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sweep(ctx, db)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Logf("teardown sweep skipped: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		sweep(cctx, conn)
	})
	return NewStore(pool, log)
}

// execer is the shared Exec surface between a pool connection and a
// one-shot pgx.Conn — mirrors missions/store_integration_test.go's own
// execer, lets sweep run identically at setup and teardown.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func sweep(ctx context.Context, db execer) {
	_, _ = db.Exec(ctx, `DELETE FROM workflow_runs WHERE workflow_id IN (SELECT id FROM workflows WHERE name LIKE $1 || '%')`, marker)
	_, _ = db.Exec(ctx, `DELETE FROM workflows WHERE name LIKE $1 || '%'`, marker)
}

func validDef() json.RawMessage {
	raw, _ := json.Marshal(Definition{
		Entry: "a",
		Steps: map[string]Step{"a": {Goal: "do a", Kind: "general"}},
		Edges: []Edge{{From: "a", On: "mission.done", To: "end", MaxIterations: 1}},
	})
	return raw
}

func TestStoreCreateGetList(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id, err := s.Create(ctx, marker+"one", validDef())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wf, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if wf.Name != marker+"one" || !wf.Enabled {
		t.Fatalf("wf = %+v", wf)
	}
	rows, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("List did not include the created workflow")
	}
}

func TestStoreCreateRejectsInvalidDefinition(t *testing.T) {
	s := testStore(t)
	if _, err := s.Create(context.Background(), marker+"bad", json.RawMessage(`{"entry":"x","steps":{}}`)); err == nil {
		t.Fatal("Create() = nil, want error for invalid definition")
	}
}

func TestStoreCreateRejectsDuplicateName(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	name := marker + "dup"
	if _, err := s.Create(ctx, name, validDef()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Create(ctx, name, validDef()); err == nil {
		t.Fatal("Create() = nil, want ErrDuplicate on second create with the same name")
	}
}

func TestRunEventsAppendOnlySeq(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	wfID, err := s.Create(ctx, marker+"seq", validDef())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	runID, err := s.CreateRun(ctx, wfID, "a", nil)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := s.AppendRunEvent(ctx, runID, "test.event", map[string]any{"i": i}); err != nil {
			t.Fatalf("AppendRunEvent(%d): %v", i, err)
		}
	}
	events, err := s.RunEvents(ctx, runID)
	if err != nil {
		t.Fatalf("RunEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("events[%d].Seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestApplyRunTransitionUpdatesStatusAndAppendsEvents(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	wfID, err := s.Create(ctx, marker+"transition", validDef())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	runID, err := s.CreateRun(ctx, wfID, "a", nil)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.ApplyRunTransition(ctx, runID, RunTransition{
		Status: "done", CurrentStep: "end",
		Events: []RunTransitionEvent{{Kind: "run.done", Payload: map[string]any{"from": "a"}}},
	}); err != nil {
		t.Fatalf("ApplyRunTransition: %v", err)
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "done" || run.CurrentStep != "end" {
		t.Fatalf("run = %+v", run)
	}
	events, err := s.RunEvents(ctx, runID)
	if err != nil {
		t.Fatalf("RunEvents: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "run.done" {
		t.Fatalf("events = %+v", events)
	}
}

func TestCountEdgeFirings(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	wfID, err := s.Create(ctx, marker+"firings", validDef())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	runID, err := s.CreateRun(ctx, wfID, "a", nil)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := s.AppendRunEvent(ctx, runID, "edge.taken", map[string]any{"from": "a", "on": "mission.done", "to": "end"}); err != nil {
			t.Fatalf("AppendRunEvent: %v", err)
		}
	}
	n, err := s.CountEdgeFirings(ctx, runID, "a", "mission.done", "end")
	if err != nil {
		t.Fatalf("CountEdgeFirings: %v", err)
	}
	if n != 2 {
		t.Fatalf("CountEdgeFirings = %d, want 2", n)
	}
	if n, _ := s.CountEdgeFirings(ctx, runID, "a", "mission.failed", "end"); n != 0 {
		t.Fatalf("CountEdgeFirings (different on) = %d, want 0", n)
	}
}

// TestStoreListRunsKeysetTies pages across ties on created_at: every
// run appears exactly once, ordered created_at DESC, id DESC (#1114).
func TestStoreListRunsKeysetTies(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	wfID, err := s.Create(ctx, marker+"paging", validDef())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	tie := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := range 5 {
		runID, err := s.CreateRun(ctx, wfID, "a", nil)
		if err != nil {
			t.Fatalf("CreateRun %d: %v", i, err)
		}
		at := tie
		if i == 0 {
			at = tie.Add(time.Hour)
		}
		if _, err := db.Exec(ctx, `UPDATE workflow_runs SET created_at = $2 WHERE id = $1`, runID, at); err != nil {
			t.Fatalf("set created_at: %v", err)
		}
	}
	all, err := s.ListRuns(ctx, wfID, time.Time{}, "", 50)
	if err != nil || len(all) != 5 {
		t.Fatalf("ListRuns all = %d runs, %v; want 5", len(all), err)
	}
	if !all[0].CreatedAt.After(tie) {
		t.Fatalf("first run created_at = %v, want the newest", all[0].CreatedAt)
	}
	for i := 2; i < len(all); i++ {
		if all[i-1].ID <= all[i].ID {
			t.Fatalf("tied runs not in id DESC order: %s then %s", all[i-1].ID, all[i].ID)
		}
	}

	var paged []string
	var before time.Time
	var beforeID string
	for range 4 {
		page, err := s.ListRuns(ctx, wfID, before, beforeID, 2)
		if err != nil {
			t.Fatalf("ListRuns page: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			paged = append(paged, r.ID)
		}
		last := page[len(page)-1]
		before, beforeID = last.CreatedAt, last.ID
	}
	if len(paged) != len(all) {
		t.Fatalf("paged %d runs, want %d: %v", len(paged), len(all), paged)
	}
	for i, r := range all {
		if paged[i] != r.ID {
			t.Fatalf("paged[%d] = %s, want %s", i, paged[i], r.ID)
		}
	}
}
