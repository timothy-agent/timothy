//go:build integration

package missions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// testScheduler fires through ResolveDefaults and a creator that
// validates and inserts like Driver.Create, minus provisioning.
func testScheduler(store *Store, destinationEnabled DestinationEnabled) *Scheduler {
	create := func(ctx context.Context, m Mission) (string, error) {
		if err := ValidateCreate(ctx, m, ValidateDeps{}); err != nil {
			return "", err
		}
		return store.Create(ctx, m)
	}
	deps := ResolveDeps{RouteForRole: func(context.Context, string) string { return "default" }, DefaultMaxIterations: store.DefaultMaxIterations}
	return NewScheduler(store.db, create, deps, nil, destinationEnabled, store.log)
}

func createTestSchedule(t *testing.T, store *Store, name, cronExpr string) string {
	t.Helper()
	db, err := store.db.Get()
	if err != nil {
		t.Fatalf("Get pool: %v", err)
	}
	var id string
	err = db.QueryRow(context.Background(), `INSERT INTO schedules (name, cron, mission_template)
		VALUES ($1, $2, $3) RETURNING id`, name, cronExpr, `{"goal":"`+marker+`scheduled run","kind":"general"}`).Scan(&id)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Exec(cctx, "DELETE FROM schedules WHERE id = $1", id)
	})
	return id
}

// TestSchedulerNoDoubleFireAcrossInstances simulates two service
// replicas racing the same tick against a shared Postgres: only one
// may fire per due boundary, enforced by the advisory lock.
func TestSchedulerNoDoubleFireAcrossInstances(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// A cron expression due right now: every minute, anchored a minute
	// in the past so dueDecision reports fire.
	id := createTestSchedule(t, store, marker+"race", "* * * * *")
	db, _ := store.db.Get()
	past := time.Now().Add(-2 * time.Minute)
	if _, err := db.Exec(ctx, "UPDATE schedules SET created_at = $2 WHERE id = $1", id, past); err != nil {
		t.Fatalf("backdate schedule: %v", err)
	}

	sched1 := testScheduler(store, nil)
	sched2 := testScheduler(store, nil)

	now := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = sched1.tick(ctx, now) }()
	go func() { defer wg.Done(); errs[1] = sched2.tick(ctx, now) }()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("tick[%d]: %v", i, err)
		}
	}

	var missionCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM missions WHERE schedule_id = $1`, id).Scan(&missionCount); err != nil {
		t.Fatalf("count missions: %v", err)
	}
	if missionCount != 1 {
		t.Fatalf("mission count after two racing ticks = %d, want exactly 1", missionCount)
	}
}

// TestSchedulerFireUsesScheduleNameDirectly confirms a scheduler-fired
// mission's name is the schedule's own name, set directly at insert
// time (TemplateCreateRequest), no LLM/gateway call involved, unlike a
// UI-created mission's async name generation.
func TestSchedulerFireUsesScheduleNameDirectly(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	id := createTestSchedule(t, store, marker+"named-schedule", "* * * * *")
	db, _ := store.db.Get()
	past := time.Now().Add(-2 * time.Minute)
	if _, err := db.Exec(ctx, "UPDATE schedules SET created_at = $2 WHERE id = $1", id, past); err != nil {
		t.Fatalf("backdate schedule: %v", err)
	}

	sched := testScheduler(store, nil)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var name string
	if err := db.QueryRow(ctx, `SELECT name FROM missions WHERE schedule_id = $1`, id).Scan(&name); err != nil {
		t.Fatalf("query fired mission's name: %v", err)
	}
	if name != marker+"named-schedule" {
		t.Fatalf("fired mission name = %q, want the schedule's own name %q", name, marker+"named-schedule")
	}
}

// TestSchedulerFireForcesAutoApprovePlanTrue confirms D-087 (issue
// #456): a scheduler-fired mission always gets auto_approve_plan=true.
// MissionTemplate has no field for it at all, so this is really
// confirming TemplateCreateRequest forces true rather
// than silently leaving the column at its Go zero-value false.
func TestSchedulerFireForcesAutoApprovePlanTrue(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	id := createTestSchedule(t, store, marker+"force-approve-plan", "* * * * *")
	db, _ := store.db.Get()
	past := time.Now().Add(-2 * time.Minute)
	if _, err := db.Exec(ctx, "UPDATE schedules SET created_at = $2 WHERE id = $1", id, past); err != nil {
		t.Fatalf("backdate schedule: %v", err)
	}

	sched := testScheduler(store, nil)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var autoApprovePlan bool
	if err := db.QueryRow(ctx, `SELECT auto_approve_plan FROM missions WHERE schedule_id = $1`, id).Scan(&autoApprovePlan); err != nil {
		t.Fatalf("query fired mission's auto_approve_plan: %v", err)
	}
	if !autoApprovePlan {
		t.Fatal("fired mission auto_approve_plan = false, want true (unattended missions have nobody to approve a plan)")
	}
}

// TestSchedulerFireCopiesReviewHarness covers issue #582: a template's
// review_harness lands on the fired mission verbatim.
func TestSchedulerFireCopiesReviewHarness(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	db, _ := store.db.Get()
	var id string
	if err := db.QueryRow(ctx, `INSERT INTO schedules (name, cron, mission_template, created_at)
		VALUES ($1, '* * * * *', $2, $3) RETURNING id`,
		marker+"review-harness", `{"goal":"`+marker+`review harness run","kind":"coding","review_harness":"pi"}`,
		time.Now().Add(-2*time.Minute)).Scan(&id); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Exec(cctx, "DELETE FROM schedules WHERE id = $1", id)
	})

	sched := testScheduler(store, nil)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var reviewHarness string
	if err := db.QueryRow(ctx, `SELECT review_harness FROM missions WHERE schedule_id = $1`, id).Scan(&reviewHarness); err != nil {
		t.Fatalf("query fired mission's review_harness: %v", err)
	}
	if reviewHarness != "pi" {
		t.Fatalf("fired mission review_harness = %q, want pi", reviewHarness)
	}
}

// TestSchedulerFireUsesTemplateNameOverSlug guards the fix for a real
// UI gap: a scheduled mission's display title used to show the raw
// schedule name (e.g. "inbox-digest-8h") instead of something
// presentable. mission_template.name, when set, must win over the
// schedule's own name.
func TestSchedulerFireUsesTemplateNameOverSlug(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	db, err := store.db.Get()
	if err != nil {
		t.Fatalf("Get pool: %v", err)
	}
	tmplJSON, err := json.Marshal(map[string]string{
		"goal": marker + "scheduled run",
		"kind": "general",
		"name": "Today's Meetings",
	})
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO schedules (name, cron, mission_template)
		VALUES ($1, $2, $3) RETURNING id`, marker+"titled-schedule", "* * * * *", tmplJSON).Scan(&id)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Exec(cctx, "DELETE FROM schedules WHERE id = $1", id)
	})
	past := time.Now().Add(-2 * time.Minute)
	if _, err := db.Exec(ctx, "UPDATE schedules SET created_at = $2 WHERE id = $1", id, past); err != nil {
		t.Fatalf("backdate schedule: %v", err)
	}

	sched := testScheduler(store, nil)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var name string
	if err := db.QueryRow(ctx, `SELECT name FROM missions WHERE schedule_id = $1`, id).Scan(&name); err != nil {
		t.Fatalf("query fired mission's name: %v", err)
	}
	if name != "Today's Meetings" {
		t.Fatalf("fired mission name = %q, want the template's display name %q", name, "Today's Meetings")
	}
}

// TestSchedulerFireCopiesTemplateAttachments confirms a fire
// copies a template's pre-converted attachments (issue #359) onto the
// fired mission's sources column, so a fire spends nothing to attach
// them.
func TestSchedulerFireCopiesTemplateAttachments(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	db, err := store.db.Get()
	if err != nil {
		t.Fatalf("Get pool: %v", err)
	}
	tmpl := map[string]any{
		"goal": marker + "scheduled run",
		"kind": "general",
		"attachments": []map[string]string{
			{"source": "pdf", "id": "att1", "mime": "application/pdf", "name": "spec.pdf", "markdown": "converted body"},
		},
	}
	tmplJSON, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO schedules (name, cron, mission_template)
		VALUES ($1, $2, $3) RETURNING id`, marker+"attach-schedule", "* * * * *", tmplJSON).Scan(&id)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Exec(cctx, "DELETE FROM schedules WHERE id = $1", id)
	})
	past := time.Now().Add(-2 * time.Minute)
	if _, err := db.Exec(ctx, "UPDATE schedules SET created_at = $2 WHERE id = $1", id, past); err != nil {
		t.Fatalf("backdate schedule: %v", err)
	}

	sched := testScheduler(store, nil)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var missionID string
	if err := db.QueryRow(ctx, `SELECT id FROM missions WHERE schedule_id = $1`, id).Scan(&missionID); err != nil {
		t.Fatalf("query fired mission id: %v", err)
	}
	m, err := store.Get(ctx, missionID)
	if err != nil {
		t.Fatalf("get fired mission: %v", err)
	}
	atts := m.Attachments()
	if len(atts) != 1 || atts[0].ID != "att1" || atts[0].Markdown != "converted body" {
		t.Fatalf("fired mission attachments = %+v, want the template's one pre-converted entry", atts)
	}
}

// TestSchedulerFireFiltersDestinationIDs confirms the fire-time
// re-check (filterDestinationIDs) drops ids that no longer resolve
// through destinationEnabled — missing/disabled destinations never
// fail the fire, they're just excluded from what lands on the new
// mission row.
func TestSchedulerFireFiltersDestinationIDs(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// The ids exercised here must parse as UUIDs even though no
	// destinations row backs them (the fake destinationEnabled below
	// stands in for the real lookup).
	const (
		kept            = "11111111-1111-1111-1111-111111111111"
		droppedDisabled = "22222222-2222-2222-2222-222222222222"
		droppedMissing  = "33333333-3333-3333-3333-333333333333"
	)
	scID, err := store.CreateSchedule(ctx, Schedule{
		Name: slugMarker + "-dest-filter", Cron: "* * * * *", Enabled: true,
		MissionTemplate: MissionTemplate{
			Goal: marker + "digest", Kind: "general",
			DestinationIDs: []string{kept, droppedDisabled, droppedMissing},
		},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	db, _ := store.db.Get()
	past := time.Now().Add(-2 * time.Minute)
	if _, err := db.Exec(ctx, "UPDATE schedules SET created_at = $2 WHERE id = $1", scID, past); err != nil {
		t.Fatalf("backdate schedule: %v", err)
	}

	destinationEnabled := func(_ context.Context, id string) (bool, error) {
		switch id {
		case kept:
			return true, nil
		case droppedDisabled:
			return false, nil // exists, disabled
		default:
			return false, nil // droppedMissing: unknown id
		}
	}
	sched := testScheduler(store, destinationEnabled)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var raw []byte
	if err := db.QueryRow(ctx, `SELECT destinations FROM missions WHERE schedule_id = $1`, scID).Scan(&raw); err != nil {
		t.Fatalf("query fired mission's destinations: %v", err)
	}
	var got []DestinationEntry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal destinations: %v", err)
	}
	if len(got) != 1 || got[0].DestinationID != kept {
		t.Fatalf("fired mission destinations = %+v, want one entry with destination_id %s", got, kept)
	}
}

// TestSchedulerLiveQueueDedup confirms a schedule whose prior mission
// is still active does not fire a second one, but last_run still
// advances so the next boundary computes correctly.
func TestSchedulerLiveQueueDedup(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	id := createTestSchedule(t, store, marker+"dedup", "* * * * *")
	db, _ := store.db.Get()
	past := time.Now().Add(-2 * time.Minute)
	if _, err := db.Exec(ctx, "UPDATE schedules SET created_at = $2 WHERE id = $1", id, past); err != nil {
		t.Fatalf("backdate schedule: %v", err)
	}

	// Pre-seed an ACTIVE mission already tied to this schedule.
	missionID, err := store.Create(ctx, Mission{Goal: marker + "already active", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := db.Exec(ctx, "UPDATE missions SET schedule_id = $2 WHERE id = $1", missionID, id); err != nil {
		t.Fatalf("attach schedule: %v", err)
	}

	sched := testScheduler(store, nil)
	now := time.Now()
	if err := sched.tick(ctx, now); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var missionCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM missions WHERE schedule_id = $1`, id).Scan(&missionCount); err != nil {
		t.Fatalf("count missions: %v", err)
	}
	if missionCount != 1 {
		t.Fatalf("mission count = %d, want still 1 (no new mission fired while one is active)", missionCount)
	}

	var lastRun *time.Time
	if err := db.QueryRow(ctx, `SELECT last_run FROM schedules WHERE id = $1`, id).Scan(&lastRun); err != nil {
		t.Fatalf("read last_run: %v", err)
	}
	if lastRun == nil {
		t.Fatal("last_run was not advanced despite the dedup skip")
	}
}

// scheduleFlags reads pending_fire/last_skipped_at/skip_reason for
// assertions below.
func scheduleFlags(t *testing.T, ctx context.Context, db *pgxpool.Pool, id string) (pendingFire bool, lastSkippedAt *time.Time, skipReason string) {
	t.Helper()
	if err := db.QueryRow(ctx, `SELECT pending_fire, last_skipped_at, skip_reason FROM schedules WHERE id = $1`, id).
		Scan(&pendingFire, &lastSkippedAt, &skipReason); err != nil {
		t.Fatalf("read schedule flags: %v", err)
	}
	return
}

// TestSchedulerDedupSkipSetsPendingFireAndRecordsReason confirms a live-
// queue dedup skip (a mission from this schedule still active) carries
// the missed fire forward via pending_fire, and records the skip.
func TestSchedulerDedupSkipSetsPendingFireAndRecordsReason(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	id := createTestSchedule(t, store, marker+"dedup-pending", "* * * * *")
	db, _ := store.db.Get()
	past := time.Now().Add(-2 * time.Minute)
	if _, err := db.Exec(ctx, "UPDATE schedules SET created_at = $2 WHERE id = $1", id, past); err != nil {
		t.Fatalf("backdate schedule: %v", err)
	}

	missionID, err := store.Create(ctx, Mission{Goal: marker + "active blocker", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := db.Exec(ctx, "UPDATE missions SET schedule_id = $2 WHERE id = $1", missionID, id); err != nil {
		t.Fatalf("attach schedule: %v", err)
	}

	sched := testScheduler(store, nil)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	pending, skippedAt, reason := scheduleFlags(t, ctx, db, id)
	if !pending {
		t.Fatal("pending_fire = false, want true after a dedup skip")
	}
	if skippedAt == nil || reason != "active_mission" {
		t.Fatalf("last_skipped_at=%v skip_reason=%q, want a timestamp and active_mission", skippedAt, reason)
	}
}

// TestSchedulerPendingFireResolvesOnceMissionClears confirms a schedule
// carrying pending_fire from an earlier dedup skip fires on the NEXT
// tick once the blocking mission is no longer active, and clears both
// pending_fire and the skip fields.
func TestSchedulerPendingFireResolvesOnceMissionClears(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	id := createTestSchedule(t, store, marker+"pending-resolves", "* * * * *")
	db, _ := store.db.Get()
	past := time.Now().Add(-2 * time.Minute)
	if _, err := db.Exec(ctx, "UPDATE schedules SET created_at = $2 WHERE id = $1", id, past); err != nil {
		t.Fatalf("backdate schedule: %v", err)
	}

	missionID, err := store.Create(ctx, Mission{Goal: marker + "temporarily active", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := db.Exec(ctx, "UPDATE missions SET schedule_id = $2 WHERE id = $1", missionID, id); err != nil {
		t.Fatalf("attach schedule: %v", err)
	}

	sched := testScheduler(store, nil)
	// First tick: due, but the mission above is active -> dedup skip,
	// pending_fire set.
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if pending, _, _ := scheduleFlags(t, ctx, db, id); !pending {
		t.Fatal("pending_fire not set after first tick's dedup skip")
	}

	// The blocking mission finishes.
	if _, err := db.Exec(ctx, "UPDATE missions SET phase = 'done' WHERE id = $1", missionID); err != nil {
		t.Fatalf("complete blocking mission: %v", err)
	}

	// Second tick: cron isn't newly due (last_run just advanced a moment
	// ago), but the pending fire must still resolve.
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	pending, skippedAt, reason := scheduleFlags(t, ctx, db, id)
	if pending {
		t.Fatal("pending_fire still true after the blocking mission cleared")
	}
	if skippedAt != nil || reason != "" {
		t.Fatalf("last_skipped_at=%v skip_reason=%q, want both cleared after the pending fire resolved", skippedAt, reason)
	}

	var missionCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM missions WHERE schedule_id = $1`, id).Scan(&missionCount); err != nil {
		t.Fatalf("count missions: %v", err)
	}
	if missionCount != 2 {
		t.Fatalf("mission count = %d, want 2 (the original active one plus the resolved pending fire)", missionCount)
	}
}

// TestSchedulerDueAndPendingFiresOnce confirms a schedule that is BOTH
// newly due (its cron boundary passed again) AND still carrying an
// earlier pending_fire only spawns one mission for the tick, not two.
func TestSchedulerDueAndPendingFiresOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	id := createTestSchedule(t, store, marker+"due-and-pending", "* * * * *")
	db, _ := store.db.Get()
	past := time.Now().Add(-2 * time.Minute)
	if _, err := db.Exec(ctx, `UPDATE schedules SET created_at = $2, pending_fire = true,
			last_skipped_at = $2, skip_reason = 'active_mission' WHERE id = $1`, id, past); err != nil {
		t.Fatalf("seed pending schedule: %v", err)
	}

	sched := testScheduler(store, nil)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var missionCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM missions WHERE schedule_id = $1`, id).Scan(&missionCount); err != nil {
		t.Fatalf("count missions: %v", err)
	}
	if missionCount != 1 {
		t.Fatalf("mission count = %d, want exactly 1 (due + pending fires once)", missionCount)
	}
	pending, skippedAt, reason := scheduleFlags(t, ctx, db, id)
	if pending || skippedAt != nil || reason != "" {
		t.Fatalf("pending_fire=%v last_skipped_at=%v skip_reason=%q, want all cleared after firing", pending, skippedAt, reason)
	}
}

// TestSchedulerBackfillSkipRecordsReason confirms a schedule whose due
// boundary is past misfireGrace still skips firing, but now records
// last_skipped_at/skip_reason="backfill_grace" instead of leaving no
// trace.
func TestSchedulerBackfillSkipRecordsReason(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	id := createTestSchedule(t, store, marker+"backfill", "0 9 * * *")
	db, _ := store.db.Get()
	// Anchor far enough in the past that "now" is well beyond
	// misfireGrace past the next boundary.
	past := time.Now().Add(-48 * time.Hour)
	if _, err := db.Exec(ctx, "UPDATE schedules SET created_at = $2 WHERE id = $1", id, past); err != nil {
		t.Fatalf("backdate schedule: %v", err)
	}

	sched := testScheduler(store, nil)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var missionCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM missions WHERE schedule_id = $1`, id).Scan(&missionCount); err != nil {
		t.Fatalf("count missions: %v", err)
	}
	if missionCount != 0 {
		t.Fatalf("mission count = %d, want 0 (backfill grace skip never fires)", missionCount)
	}
	pending, skippedAt, reason := scheduleFlags(t, ctx, db, id)
	if pending {
		t.Fatal("pending_fire = true, want false (a backfill skip is not a dedup skip)")
	}
	if skippedAt == nil || reason != "backfill_grace" {
		t.Fatalf("last_skipped_at=%v skip_reason=%q, want a timestamp and backfill_grace", skippedAt, reason)
	}
}

// createDueScheduleWithTemplate inserts a schedule due on this tick
// ("* * * * *" backdated two minutes) with a caller-supplied template.
func createDueScheduleWithTemplate(t *testing.T, ctx context.Context, db *pgxpool.Pool, name string, template map[string]any) string {
	t.Helper()
	templateJSON, err := json.Marshal(template)
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	var id string
	if err := db.QueryRow(ctx, `INSERT INTO schedules (name, cron, mission_template, created_at)
		VALUES ($1, '* * * * *', $2, now() - interval '2 minutes') RETURNING id`, name, templateJSON).Scan(&id); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return id
}

func countScheduleMissions(t *testing.T, ctx context.Context, db *pgxpool.Pool, scheduleID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM missions WHERE schedule_id = $1`, scheduleID).Scan(&n); err != nil {
		t.Fatalf("count missions: %v", err)
	}
	return n
}

// TestSchedulerSkipsScheduleWithDeletedAgent covers issue #815: the
// middle of three due schedules names a deleted agent; the other two
// fire and the middle one records skip_reason agent_missing.
func TestSchedulerSkipsScheduleWithDeletedAgent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	db, _ := store.db.Get()

	agentPrefix := "itest-mission-agent-"
	var liveAgent, deletedAgent string
	if err := db.QueryRow(ctx, `INSERT INTO agents (name) VALUES ($1) RETURNING id`, agentPrefix+"live").Scan(&liveAgent); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO agents (name) VALUES ($1) RETURNING id`, agentPrefix+"gone").Scan(&deletedAgent); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM agents WHERE id = $1`, deletedAgent); err != nil {
		t.Fatalf("delete agent: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		sweep(cctx, conn) // fired missions reference liveAgent
		_, _ = conn.Exec(cctx, `DELETE FROM agents WHERE name LIKE $1 || '%'`, agentPrefix)
	})

	goal := marker + "agent-missing run"
	first := createDueScheduleWithTemplate(t, ctx, db, marker+"agent-missing-1", map[string]any{"goal": goal, "kind": "general"})
	poisoned := createDueScheduleWithTemplate(t, ctx, db, marker+"agent-missing-2", map[string]any{"goal": goal, "kind": "general", "agent_id": deletedAgent})
	third := createDueScheduleWithTemplate(t, ctx, db, marker+"agent-missing-3", map[string]any{"goal": goal, "kind": "general", "agent_id": liveAgent})

	sched := testScheduler(store, nil)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if n := countScheduleMissions(t, ctx, db, first); n != 1 {
		t.Fatalf("first schedule missions = %d, want 1", n)
	}
	if n := countScheduleMissions(t, ctx, db, third); n != 1 {
		t.Fatalf("third schedule missions = %d, want 1", n)
	}
	if n := countScheduleMissions(t, ctx, db, poisoned); n != 0 {
		t.Fatalf("poisoned schedule missions = %d, want 0", n)
	}
	_, skippedAt, reason := scheduleFlags(t, ctx, db, poisoned)
	if skippedAt == nil || reason != "agent_missing" {
		t.Fatalf("poisoned last_skipped_at=%v skip_reason=%q, want a timestamp and agent_missing", skippedAt, reason)
	}
	for _, id := range []string{first, third} {
		if _, skippedAt, reason := scheduleFlags(t, ctx, db, id); skippedAt != nil || reason != "" {
			t.Fatalf("fired schedule %s last_skipped_at=%v skip_reason=%q, want cleared", id, skippedAt, reason)
		}
	}
}

// TestSchedulerRegression815FailedInsertDoesNotAbortTick reproduces
// issue #815 against real Postgres: a statement failing inside the tick
// transaction used to abort it (25P02) so no schedule fired. A test
// trigger makes the poisoned schedule's fire UPDATE raise in fireOne,
// so only fireEach's SAVEPOINT keeps the other two schedules firing.
func TestSchedulerRegression815FailedInsertDoesNotAbortTick(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	db, _ := store.db.Get()

	goal := marker + "regression-815 run"
	first := createDueScheduleWithTemplate(t, ctx, db, marker+"regression-815-1", map[string]any{"goal": goal, "kind": "general"})
	poisoned := createDueScheduleWithTemplate(t, ctx, db, marker+"regression-815-2", map[string]any{"goal": goal, "kind": "general"})
	third := createDueScheduleWithTemplate(t, ctx, db, marker+"regression-815-3", map[string]any{"goal": goal, "kind": "general"})

	// Fails only the successful-fire UPDATE (skip_reason cleared), so
	// markSkipped's fire_error write still lands after the rollback.
	dropPoison := func(ctx context.Context) {
		_, _ = db.Exec(ctx, `DROP TRIGGER IF EXISTS itest_poison_815 ON schedules`)
		_, _ = db.Exec(ctx, `DROP FUNCTION IF EXISTS itest_poison_815()`)
	}
	dropPoison(ctx)
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		dropPoison(cctx)
	})
	if _, err := db.Exec(ctx, `CREATE FUNCTION itest_poison_815() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'itest poison #815'; END $$`); err != nil {
		t.Fatalf("create poison function: %v", err)
	}
	if _, err := db.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER itest_poison_815 BEFORE UPDATE ON schedules FOR EACH ROW
		WHEN (OLD.id = '%s'::uuid AND NEW.skip_reason = '') EXECUTE FUNCTION itest_poison_815()`, poisoned)); err != nil {
		t.Fatalf("create poison trigger: %v", err)
	}

	sched := testScheduler(store, nil)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if n := countScheduleMissions(t, ctx, db, first); n != 1 {
		t.Fatalf("first schedule missions = %d, want 1", n)
	}
	if n := countScheduleMissions(t, ctx, db, third); n != 1 {
		t.Fatalf("third schedule missions = %d, want 1 (before #815 this was 0: 25P02 cascade)", n)
	}
	if n := countScheduleMissions(t, ctx, db, poisoned); n != 0 {
		t.Fatalf("poisoned schedule missions = %d, want 0", n)
	}
	_, skippedAt, reason := scheduleFlags(t, ctx, db, poisoned)
	if skippedAt == nil || reason != "fire_error" {
		t.Fatalf("poisoned last_skipped_at=%v skip_reason=%q, want a timestamp and fire_error", skippedAt, reason)
	}
}

// TestSchedulerRouteOutageRetriesFire covers issue #849: a due fire
// rejected by the route gate records fire_error with pending_fire set,
// and the next tick after the route recovers creates the mission.
func TestSchedulerRouteOutageRetriesFire(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	db, _ := store.db.Get()

	id := createDueScheduleWithTemplate(t, ctx, db, marker+"route-outage", map[string]any{"goal": marker + "route outage run", "kind": "general"})
	var down atomic.Bool
	down.Store(true)
	sched := testScheduler(store, nil)
	sched.resolve.ResolveRoute = func(_ context.Context, route, _ string) (*gwclient.ResolvedRoute, error) {
		return &gwclient.ResolvedRoute{Route: route, Entries: []gwclient.ResolvedRouteEntry{{Usable: !down.Load(), SkipReason: "cooling down"}}}, nil
	}

	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if n := countScheduleMissions(t, ctx, db, id); n != 0 {
		t.Fatalf("missions during outage = %d, want 0", n)
	}
	pending, skippedAt, reason := scheduleFlags(t, ctx, db, id)
	if !pending || skippedAt == nil || reason != "fire_error" {
		t.Fatalf("pending=%v last_skipped_at=%v skip_reason=%q, want pending set and fire_error", pending, skippedAt, reason)
	}

	down.Store(false)
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if n := countScheduleMissions(t, ctx, db, id); n != 1 {
		t.Fatalf("missions after recovery = %d, want 1", n)
	}
	pending, skippedAt, reason = scheduleFlags(t, ctx, db, id)
	if pending || skippedAt != nil || reason != "" {
		t.Fatalf("pending=%v last_skipped_at=%v skip_reason=%q, want all cleared after the retry fired", pending, skippedAt, reason)
	}
}

// TestSchedulerFireProvisionsMissionViaDriver covers issue #816: one
// tick fires through a real Driver.Create, so the mission is
// provisioned (hidden session, workspace) instead of a bare row, and
// still carries schedule_id and auto_approve_plan=true.
func TestSchedulerFireProvisionsMissionViaDriver(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	db, _ := store.db.Get()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	wsRoot := t.TempDir()
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, NewWorkspace(wsRoot, nil, log), nil, session.NewStore(store.db, log), tools.NewPermissions(store.db, wsRoot), nil, nil, log)
	d.SetValidateDeps(ValidateDeps{})
	deps := ResolveDeps{RouteForRole: func(context.Context, string) string { return "default" }}
	sched := NewScheduler(store.db, d.Create, deps, nil, nil, log)

	id := createDueScheduleWithTemplate(t, ctx, db, marker+"provisioned", map[string]any{"goal": marker + "provisioned run", "kind": "general", "light": true})
	if err := sched.tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var missionID string
	if err := db.QueryRow(ctx, `SELECT id FROM missions WHERE schedule_id = $1`, id).Scan(&missionID); err != nil {
		t.Fatalf("query fired mission: %v", err)
	}
	// Wait for the background Drive to settle so teardown doesn't race it.
	var m Mission
	deadline := time.Now().Add(15 * time.Second)
	for {
		var err error
		m, err = store.Get(ctx, missionID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if (m.Status != StatusIdle && m.Status != StatusWorking) || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if m.SessionID == "" || m.Workspace == "" {
		t.Fatalf("fired mission session=%q workspace=%q, want both provisioned by Driver.Create", m.SessionID, m.Workspace)
	}
	if m.ScheduleID != id || !m.AutoApprovePlan || m.Flow != FlowLight || m.Phase != PhaseBuild {
		t.Fatalf("fired mission schedule=%q auto_approve_plan=%v flow=%q phase=%q, want %q true light build", m.ScheduleID, m.AutoApprovePlan, m.Flow, m.Phase, id)
	}
	if _, skippedAt, reason := scheduleFlags(t, ctx, db, id); skippedAt != nil || reason != "" {
		t.Fatalf("skip fields = %v %q, want cleared after a successful fire", skippedAt, reason)
	}
}

// TestSchedulerRejectsLightCodingTemplateAtFire covers issue #816: a
// stored light+coding template (predating the save-time check) is
// skipped with fire_error and creates nothing.
func TestSchedulerRejectsLightCodingTemplateAtFire(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	db, _ := store.db.Get()

	id := createDueScheduleWithTemplate(t, ctx, db, marker+"light-coding", map[string]any{"goal": marker + "light coding run", "kind": "coding", "light": true})
	if err := testScheduler(store, nil).tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n := countScheduleMissions(t, ctx, db, id); n != 0 {
		t.Fatalf("missions = %d, want 0", n)
	}
	if _, skippedAt, reason := scheduleFlags(t, ctx, db, id); skippedAt == nil || reason != "fire_error" {
		t.Fatalf("last_skipped_at=%v skip_reason=%q, want a timestamp and fire_error", skippedAt, reason)
	}
}

// TestSchedulerFailedPendingFireKeepsPendingFlag confirms a pending
// fire whose create fails keeps pending_fire, the state a rolled-back
// savepoint left before issue #816 moved creation after commit.
func TestSchedulerFailedPendingFireKeepsPendingFlag(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	db, _ := store.db.Get()

	id := createDueScheduleWithTemplate(t, ctx, db, marker+"pending-fails", map[string]any{"goal": "", "kind": "general"})
	// Not due (last_run now), but carrying a pending fire.
	if _, err := db.Exec(ctx, `UPDATE schedules SET last_run = now(), pending_fire = true WHERE id = $1`, id); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	if err := testScheduler(store, nil).tick(ctx, time.Now()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	pending, skippedAt, reason := scheduleFlags(t, ctx, db, id)
	if !pending || skippedAt == nil || reason != "fire_error" {
		t.Fatalf("pending=%v last_skipped_at=%v skip_reason=%q, want pending kept and fire_error", pending, skippedAt, reason)
	}
}
