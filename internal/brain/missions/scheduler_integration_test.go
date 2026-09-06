//go:build integration

package missions

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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

	sched1 := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)
	sched2 := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)

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
// time (createFromTemplate) — no LLM/gateway call involved, unlike a
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

	sched := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)
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
// confirming createFromTemplate's own INSERT hardcodes true rather
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

	sched := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)
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

	sched := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)
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
// UI gap: schedule names are strict lowercase slugs (shared validation
// with connectors/destinations/agents), so a scheduled mission's
// display title showed the raw slug (e.g. "inbox-digest-8h") instead
// of something presentable. mission_template.name, when set, must win
// over the schedule's own slug.
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

	sched := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)
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

// TestSchedulerFireCopiesTemplateAttachments confirms createFromTemplate
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

	sched := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)
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
	sched := NewScheduler(store.db, store, nil, nil, nil, nil, nil, destinationEnabled, store.log)
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

	sched := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)
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

	sched := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)
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

	sched := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)
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

	sched := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)
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

	sched := NewScheduler(store.db, store, nil, nil, nil, nil, nil, nil, store.log)
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
