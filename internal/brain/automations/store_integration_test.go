//go:build integration

package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const marker = "itest-automation-"

// testStore returns a migrated store plus the pool, with every
// marker-named automation and marker-goal mission swept on cleanup.
func testStore(t *testing.T) (*Store, *pgpool.Pool) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	poolCtx, poolCancel := context.WithCancel(context.Background())
	t.Cleanup(poolCancel)
	pool := pgpool.New(poolCtx, dsn, log)
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
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = db.Exec(cctx, `DELETE FROM missions WHERE goal LIKE $1 || '%'`, marker)
		_, _ = db.Exec(cctx, `DELETE FROM automations WHERE name LIKE $1 || '%'`, marker)
	})
	return NewStore(pool), pool
}

// runTag makes every fixture name unique per run.
func runTag() string {
	return fmt.Sprintf("%s%d-", marker, time.Now().UnixNano())
}

func defaultAgentID(t *testing.T, pool *pgpool.Pool) string {
	t.Helper()
	db, _ := pool.Get()
	var id string
	if err := db.QueryRow(t.Context(), `SELECT id FROM agents WHERE is_default LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("default agent: %v", err)
	}
	return id
}

func fixture(name, agentID string) Automation {
	return Automation{
		Name: name, AgentID: agentID,
		Action:      Action{Kind: ActionMission, Mission: &missions.MissionTemplate{Goal: "g", Kind: missions.KindGeneral, Light: true}},
		Concurrency: ConcurrencySkip, MaxConcurrent: 1, MaxRunsPerHour: 6, Continuity: true, NotesEnabled: true, Enabled: true,
		Triggers: []Trigger{
			{Kind: TriggerCron, Config: json.RawMessage(`{"expr":"0 9 * * *"}`), Enabled: true},
			{Kind: TriggerManual, Enabled: true},
		},
	}
}

func TestStoreCRUD(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	tag := runTag()
	agent := defaultAgentID(t, pool)

	id, err := s.Create(ctx, fixture(tag+"crud", agent))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != tag+"crud" || len(got.Triggers) != 2 || got.Triggers[0].Kind != TriggerCron || string(got.Triggers[1].Config) != `{}` {
		t.Fatalf("Get = %+v", got)
	}
	st, err := s.AutomationStats(ctx, got, time.Now(), time.UTC)
	if err != nil || st.RunsTotal != 0 || st.LastRunAt != nil || st.NextRunAt == nil {
		t.Fatalf("stats = %+v, %v, want zero runs and a next run", st, err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, a := range list {
		if a.ID == id {
			found = len(a.Triggers) == 2
		}
	}
	if !found {
		t.Fatal("List missed the automation or its triggers")
	}

	// Replace triggers: keep the cron row (its state must survive), drop
	// manual, add a new cron.
	db, _ := pool.Get()
	cronID := got.Triggers[0].ID
	if _, err := db.Exec(ctx, `UPDATE automation_triggers SET state = '{"last_fired_at":"2026-09-01T07:00:00Z"}' WHERE id = $1`, cronID); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	name := tag + "renamed"
	exp := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	expPtr := &exp
	triggers := []Trigger{
		{ID: cronID, Kind: TriggerCron, Config: json.RawMessage(`{"expr":"0 10 * * *"}`), Enabled: true},
		{Kind: TriggerCron, Config: json.RawMessage(`{"expr":"30 18 * * 1-5"}`), Enabled: false},
	}
	if err := s.Patch(ctx, id, Patch{Name: &name, ExpiresAt: &expPtr, Triggers: &triggers}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	got, _ = s.Get(ctx, id)
	if got.Name != name || got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || len(got.Triggers) != 2 {
		t.Fatalf("after patch = %+v", got)
	}
	kept := got.Triggers[0]
	var keptCfg CronConfig
	_ = json.Unmarshal(kept.Config, &keptCfg)
	if kept.ID != cronID || keptCfg.Expr != "0 10 * * *" || !strings.Contains(string(kept.State), "last_fired_at") {
		t.Fatalf("kept trigger = %+v, want same id, new config, preserved state", kept)
	}
	if got.Triggers[1].ID == cronID || got.Triggers[1].Enabled {
		t.Fatalf("new trigger = %+v", got.Triggers[1])
	}

	// A kind change on a kept id resets its state.
	triggers = []Trigger{{ID: cronID, Kind: TriggerManual, Enabled: true}}
	if err := s.Patch(ctx, id, Patch{Triggers: &triggers}); err != nil {
		t.Fatalf("Patch kind: %v", err)
	}
	got, _ = s.Get(ctx, id)
	if len(got.Triggers) != 1 || got.Triggers[0].ID != cronID || string(got.Triggers[0].State) != `{}` {
		t.Fatalf("kind change = %+v, want same id and cleared state", got.Triggers)
	}

	var cleared *time.Time
	if err := s.Patch(ctx, id, Patch{ExpiresAt: &cleared}); err != nil {
		t.Fatalf("Patch clear: %v", err)
	}
	if got, _ = s.Get(ctx, id); got.ExpiresAt != nil {
		t.Fatalf("expires_at = %v, want cleared", got.ExpiresAt)
	}

	// Patch re-validates the resulting automation.
	bad := 0
	var ve *ValidationError
	if err := s.Patch(ctx, id, Patch{MaxRunsPerHour: &bad}); !errors.As(err, &ve) {
		t.Fatalf("invalid patch = %v, want ValidationError", err)
	}
	if err := s.Patch(ctx, "00000000-0000-4000-8000-000000000000", Patch{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("patch unknown = %v, want ErrNotFound", err)
	}

	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get malformed id = %v, want ErrNotFound", err)
	}
}

func TestStoreNameConflictCaseInsensitive(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	tag := runTag()
	agent := defaultAgentID(t, pool)
	if _, err := s.Create(ctx, fixture(tag+"Digest", agent)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Create(ctx, fixture("  "+strings.ToUpper(tag)+"DIGEST ", agent)); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("case-insensitive duplicate = %v, want ErrNameConflict", err)
	}
	other, err := s.Create(ctx, fixture(tag+"other", agent))
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}
	dup := tag + "digest"
	if err := s.Patch(ctx, other, Patch{Name: &dup}); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("rename onto existing = %v, want ErrNameConflict", err)
	}
	if _, err := s.Create(ctx, fixture(tag+"ghost-agent", "00000000-0000-4000-8000-000000000000")); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("unknown agent = %v, want ErrUnknownAgent", err)
	}
}

// TestStoreDeleteAfterRunKeepsMission: deleting an automation cascades
// its runs while the mission a run started keeps its row, unlinked.
func TestStoreDeleteAfterRunKeepsMission(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	tag := runTag()
	id, err := s.Create(ctx, fixture(tag+"fired", defaultAgentID(t, pool)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	a, _ := s.Get(ctx, id)
	runID, inserted, err := s.CreateRun(ctx, nil, Run{AutomationID: id, TriggerID: &a.Triggers[0].ID, DedupKey: tag + "boundary", Status: RunRunning})
	if err != nil || !inserted {
		t.Fatalf("CreateRun = %v, %v", inserted, err)
	}
	if _, again, err := s.CreateRun(ctx, nil, Run{AutomationID: id, DedupKey: tag + "boundary", Status: RunRunning}); err != nil || again {
		t.Fatalf("duplicate CreateRun inserted=%v err=%v, want a no-op", again, err)
	}
	ms := missions.NewStore(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	missionID, err := ms.Create(ctx, missions.Mission{Goal: marker + tag + "run mission", Kind: missions.KindGeneral, Route: "default",
		OriginKind: missions.OriginAutomation, Unattended: true, AutomationRunID: runID})
	if err != nil {
		t.Fatalf("mission Create: %v", err)
	}
	db, _ := pool.Get()
	if _, err := db.Exec(ctx, `UPDATE automation_runs SET mission_id = $2 WHERE id = $1`, runID, missionID); err != nil {
		t.Fatalf("link run: %v", err)
	}
	runs, err := s.ListRuns(ctx, id, 50)
	if err != nil || len(runs) != 1 || runs[0].MissionID != missionID || runs[0].TriggerID == nil {
		t.Fatalf("ListRuns = %+v, %v", runs, err)
	}

	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var runsLeft int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM automation_runs WHERE id = $1`, runID).Scan(&runsLeft)
	if runsLeft != 0 {
		t.Fatal("run survived its automation's delete")
	}
	m, err := ms.Get(ctx, missionID)
	if err != nil {
		t.Fatalf("mission after delete: %v", err)
	}
	if m.AutomationRunID != "" || m.OriginKind != missions.OriginAutomation {
		t.Fatalf("mission automation_run_id = %q origin = %q, want cleared and automation", m.AutomationRunID, m.OriginKind)
	}
}

func TestStoreNotes(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	tag := runTag()
	id, err := s.Create(ctx, fixture(tag+"notes", defaultAgentID(t, pool)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := range MaxNotes {
		if _, created, err := s.PutNote(ctx, id, fmt.Sprintf("note-%d", i), "v1", ""); err != nil || !created {
			t.Fatalf("PutNote %d: created=%v err=%v", i, created, err)
		}
	}
	if _, _, err := s.PutNote(ctx, id, "note-10", "v1", ""); !errors.Is(err, ErrNoteLimit) {
		t.Fatalf("11th note = %v, want ErrNoteLimit", err)
	}
	n, created, err := s.PutNote(ctx, id, "note-0", "v2", "")
	if err != nil || created || n.Content != "v2" {
		t.Fatalf("update at the cap = %+v created=%v err=%v, want an update", n, created, err)
	}
	var ve *ValidationError
	if _, _, err := s.PutNote(ctx, id, "note-0", strings.Repeat("a", MaxNoteBytes+1), ""); !errors.As(err, &ve) {
		t.Fatalf("4097 bytes = %v, want ValidationError", err)
	}
	if _, _, err := s.PutNote(ctx, id, "Bad Name", "x", ""); !errors.As(err, &ve) {
		t.Fatalf("bad name = %v, want ValidationError", err)
	}
	if _, _, err := s.PutNote(ctx, "00000000-0000-4000-8000-000000000000", "n", "x", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown automation = %v, want ErrNotFound", err)
	}
	notes, err := s.ListNotes(ctx, id)
	if err != nil || len(notes) != MaxNotes || notes[0].Name != "note-0" || notes[0].Content != "v2" {
		t.Fatalf("ListNotes = %d notes, %v", len(notes), err)
	}
	if err := s.DeleteNote(ctx, id, "note-3"); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	if _, err := s.GetNote(ctx, id, "note-3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetNote deleted = %v, want ErrNotFound", err)
	}
	if err := s.DeleteNote(ctx, id, "note-3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteNote twice = %v, want ErrNotFound", err)
	}
	if _, created, err := s.PutNote(ctx, id, "note-10", "fits again", ""); err != nil || !created {
		t.Fatalf("PutNote after delete = %v %v", created, err)
	}
}

// TestStoreStats seeds finished runs across days and checks the 7-day
// counts and the 14 local-day sparkline in Europe/Amsterdam as deltas,
// so rows other tests leave behind cannot skew them.
func TestStoreStats(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	tag := runTag()
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Now()
	before, err := s.Stats(ctx, now, ams)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	id, err := s.Create(ctx, fixture(tag+"stats", defaultAgentID(t, pool)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seeds := []struct {
		age    time.Duration
		status string
	}{
		{time.Minute, RunDone}, {2 * time.Minute, RunDone}, {3 * time.Minute, RunFailed},
		{3 * 24 * time.Hour, RunDone}, {10 * 24 * time.Hour, RunFailed}, {20 * 24 * time.Hour, RunDone},
		{time.Hour, RunSkipped},
	}
	wantDone7, wantFailed7 := 0, 0
	wantDay := map[string][2]int{}
	for i, sd := range seeds {
		finished := now.Add(-sd.age)
		if _, _, err := s.CreateRun(ctx, nil, Run{AutomationID: id, DedupKey: fmt.Sprintf("%sseed-%d", tag, i), Status: sd.status,
			CreatedAt: finished, FinishedAt: &finished}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		if sd.status == RunSkipped {
			continue
		}
		if sd.age < 7*24*time.Hour {
			if sd.status == RunDone {
				wantDone7++
			} else {
				wantFailed7++
			}
		}
		day := finished.In(ams).Format(time.DateOnly)
		c := wantDay[day]
		if sd.status == RunDone {
			c[0]++
		} else {
			c[1]++
		}
		wantDay[day] = c
	}
	after, err := s.Stats(ctx, now, ams)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if after.Total != before.Total+1 || after.Enabled != before.Enabled+1 {
		t.Fatalf("totals %d/%d -> %d/%d, want +1", before.Total, before.Enabled, after.Total, after.Enabled)
	}
	if after.Succeeded7d-before.Succeeded7d != wantDone7 || after.Failed7d-before.Failed7d != wantFailed7 {
		t.Fatalf("7d delta = %d/%d, want %d/%d", after.Succeeded7d-before.Succeeded7d, after.Failed7d-before.Failed7d, wantDone7, wantFailed7)
	}
	if len(after.Sparkline) != 14 {
		t.Fatalf("sparkline has %d buckets, want 14", len(after.Sparkline))
	}
	today := now.In(ams)
	if after.Sparkline[13].Day != today.Format(time.DateOnly) || after.Sparkline[0].Day != today.AddDate(0, 0, -13).Format(time.DateOnly) {
		t.Fatalf("sparkline spans %s..%s", after.Sparkline[0].Day, after.Sparkline[13].Day)
	}
	for i, d := range after.Sparkline {
		want := wantDay[d.Day]
		if d.Succeeded-before.Sparkline[i].Succeeded != want[0] || d.Failed-before.Sparkline[i].Failed != want[1] {
			t.Fatalf("day %s delta = %d/%d, want %d/%d", d.Day, d.Succeeded-before.Sparkline[i].Succeeded, d.Failed-before.Sparkline[i].Failed, want[0], want[1])
		}
	}

	a, _ := s.Get(ctx, id)
	st, err := s.AutomationStats(ctx, a, now, ams)
	if err != nil {
		t.Fatalf("AutomationStats: %v", err)
	}
	if st.RunsTotal != len(seeds) || st.Succeeded7d != wantDone7 || st.Failed7d != wantFailed7 || st.LastRunStatus != RunDone {
		t.Fatalf("automation stats = %+v", st)
	}
}

func TestStoreNameReferencingDestination(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	tag := runTag()
	dest := "5f0c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f"
	a := fixture(tag+"delivers", defaultAgentID(t, pool))
	a.Action.Mission.DestinationIDs = []string{dest}
	id, err := s.Create(ctx, a)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	name, ok, err := s.NameReferencingDestination(ctx, dest)
	if err != nil || !ok || name != tag+"delivers" {
		t.Fatalf("reference = %q %v %v", name, ok, err)
	}
	off := false
	if err := s.Patch(ctx, id, Patch{Enabled: &off}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, ok, err := s.NameReferencingDestination(ctx, dest); err != nil || ok {
		t.Fatalf("disabled reference = %v %v, want none", ok, err)
	}
}
