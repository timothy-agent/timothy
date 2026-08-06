//go:build integration

package missions

import (
	"context"
	"sync"
	"testing"
	"time"
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

	sched1 := NewScheduler(store.db, store, nil, nil, nil, store.log)
	sched2 := NewScheduler(store.db, store, nil, nil, nil, store.log)

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

	sched := NewScheduler(store.db, store, nil, nil, nil, store.log)
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
