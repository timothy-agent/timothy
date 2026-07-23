//go:build integration

package missions

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const marker = "itest-mission "

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
	sweep := func(ctx context.Context) {
		_, _ = db.Exec(ctx, "DELETE FROM missions WHERE goal LIKE $1 || '%'", marker)
	}
	sweep(ctx)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Logf("teardown sweep skipped: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		_, _ = conn.Exec(cctx, "DELETE FROM missions WHERE goal LIKE $1 || '%'", marker)
	})
	return NewStore(pool, log)
}

func TestMissionCRUD(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "crud", Kind: "research", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Phase != PhaseResearch || m.Status != StatusIdle || m.MaxIterations != 8 {
		t.Fatalf("Get = %+v, want default research/idle/8", m)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, lm := range list {
		if lm.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("List did not include the created mission")
	}

	if _, err := s.Get(ctx, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("Get of a nonexistent id succeeded")
	}
}

func TestApplyTransitionAndEvents(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "transition", Kind: "research"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	next := StepState{Phase: PhasePlan, Status: StatusIdle, MaxIterations: 8}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   next,
		Events: []EventDraft{{Kind: "mission.phase_started", Payload: map[string]any{"phase": "plan"}}},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}

	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Phase != PhasePlan {
		t.Fatalf("Phase after transition = %q, want plan", m.Phase)
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "mission.phase_started" || events[0].Seq != 1 {
		t.Fatalf("Events = %+v, want one mission.phase_started at seq 1", events)
	}
}

// TestAppendEventSeqMonotonic drives concurrent appends to the SAME
// mission and asserts seq is a gap-free 1..N sequence — the FOR UPDATE
// lock on the mission row must serialize these, not merely avoid a
// crash.
func TestAppendEventSeqMonotonic(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "seq", Kind: "research"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.AppendEvent(ctx, id, "mission.recovery", map[string]any{"i": i})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("AppendEvent[%d]: %v", i, err)
		}
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != n {
		t.Fatalf("len(events) = %d, want %d", len(events), n)
	}
	seen := map[int64]bool{}
	for _, e := range events {
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
	for i := int64(1); i <= n; i++ {
		if !seen[i] {
			t.Fatalf("seq %d missing: gap in the sequence", i)
		}
	}
}

func TestSetProvisionedBranchCollision(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id1, err := s.Create(ctx, Mission{Goal: marker + "provision-1", Kind: "coding"})
	if err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	id2, err := s.Create(ctx, Mission{Goal: marker + "provision-2", Kind: "coding"})
	if err != nil {
		t.Fatalf("Create 2: %v", err)
	}

	if err := s.SetProvisioned(ctx, id1, "/ws", "/ws/wt1", "mission/shared-branch", "abc123"); err != nil {
		t.Fatalf("SetProvisioned 1: %v", err)
	}
	// Same workspace+branch, different mission, still active: refused.
	err = s.SetProvisioned(ctx, id2, "/ws", "/ws/wt2", "mission/shared-branch", "abc123")
	if err == nil {
		t.Fatal("SetProvisioned allowed a branch collision with an active mission")
	}

	// A different branch on the same workspace is fine.
	if err := s.SetProvisioned(ctx, id2, "/ws", "/ws/wt2", "mission/other-branch", "abc123"); err != nil {
		t.Fatalf("SetProvisioned distinct branch: %v", err)
	}

	// Once mission 1 is terminal, its branch is free for reuse.
	if err := s.ApplyTransition(ctx, id1, Transition{Next: StepState{Phase: PhaseDone, Status: StatusDone}}); err != nil {
		t.Fatalf("ApplyTransition terminal: %v", err)
	}
	id3, err := s.Create(ctx, Mission{Goal: marker + "provision-3", Kind: "coding"})
	if err != nil {
		t.Fatalf("Create 3: %v", err)
	}
	if err := s.SetProvisioned(ctx, id3, "/ws", "/ws/wt3", "mission/shared-branch", "def456"); err != nil {
		t.Fatalf("SetProvisioned after mission 1 terminal: %v", err)
	}
}

// TestClaimWorkSlotConcurrencyCap fires more concurrent claimants than
// the slot cap and asserts the number actually flipped to working
// never exceeds max — the advisory lock must make the
// count-then-update atomic, not just individually safe.
func TestClaimWorkSlotConcurrencyCap(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	const total, max = 10, 3
	ids := make([]string, total)
	for i := range ids {
		id, err := s.Create(ctx, Mission{Goal: marker + "slot", Kind: "research"})
		if err != nil {
			t.Fatalf("Create[%d]: %v", i, err)
		}
		ids[i] = id
	}

	var wg sync.WaitGroup
	claimed := make(chan string, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, ok, err := s.ClaimWorkSlot(ctx, max)
			if err != nil {
				t.Errorf("ClaimWorkSlot: %v", err)
				return
			}
			if ok {
				claimed <- id
			}
		}()
	}
	wg.Wait()
	close(claimed)

	var got []string
	for id := range claimed {
		got = append(got, id)
	}
	if len(got) != max {
		t.Fatalf("claimed %d slots, want exactly %d (cap)", len(got), max)
	}

	db, _ := s.db.Get()
	var working int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM missions WHERE status = 'working' AND goal LIKE $1 || '%'`, marker).Scan(&working); err != nil {
		t.Fatalf("count working: %v", err)
	}
	if working != max {
		t.Fatalf("working count = %d, want %d", working, max)
	}
}

func TestReconcileTerminalIdempotency(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "reconcile", Kind: "research"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	donePayload := map[string]any{"units": 3}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseDone, Status: StatusDone},
		Events: []EventDraft{{Kind: "mission.done", Payload: donePayload}},
	}); err != nil {
		t.Fatalf("ApplyTransition done: %v", err)
	}

	// A duplicate of the SAME outcome is a no-op: no mission.reconciled
	// event, mission stays done.
	if err := s.ReconcileTerminal(ctx, id, PhaseDone, donePayload); err != nil {
		t.Fatalf("ReconcileTerminal duplicate: %v", err)
	}
	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, e := range events {
		if e.Kind == "mission.reconciled" {
			t.Fatal("duplicate terminal wrote a mission.reconciled event")
		}
	}

	// A CONTRADICTORY second terminal (failed after done) writes
	// mission.reconciled naming the canonical (first-by-seq) outcome,
	// and does not flip the mission's persisted phase.
	if err := s.ReconcileTerminal(ctx, id, PhaseFailed, map[string]any{"reason": "race"}); err != nil {
		t.Fatalf("ReconcileTerminal contradiction: %v", err)
	}
	events, err = s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	reconciled := false
	for _, e := range events {
		if e.Kind == "mission.reconciled" {
			reconciled = true
		}
	}
	if !reconciled {
		t.Fatal("contradictory terminal did not write mission.reconciled")
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Phase != PhaseDone {
		t.Fatalf("phase after reconciliation = %q, want done (canonical) unchanged", m.Phase)
	}
}

func TestRecoverWorking(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	idleID, err := s.Create(ctx, Mission{Goal: marker + "recover-idle", Kind: "research"})
	if err != nil {
		t.Fatalf("Create idle: %v", err)
	}
	workingID, err := s.Create(ctx, Mission{Goal: marker + "recover-working", Kind: "research"})
	if err != nil {
		t.Fatalf("Create working: %v", err)
	}
	if err := s.ApplyTransition(ctx, workingID, Transition{Next: StepState{Phase: PhaseExecute, Status: StatusWorking}}); err != nil {
		t.Fatalf("ApplyTransition working: %v", err)
	}

	working, err := s.RecoverWorking(ctx)
	if err != nil {
		t.Fatalf("RecoverWorking: %v", err)
	}
	byID := map[string]bool{}
	for _, m := range working {
		byID[m.ID] = true
	}
	if !byID[workingID] {
		t.Fatal("RecoverWorking did not return the working mission")
	}
	if byID[idleID] {
		t.Fatal("RecoverWorking incorrectly returned an idle mission")
	}
}
