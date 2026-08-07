//go:build integration

package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

// testMissionStore mirrors internal/brain/missions' own testStore
// helper — a real Postgres-backed *missions.Store, migrated, with
// itest-prefixed fixtures swept on cleanup.
func testMissionStore(t *testing.T) *missions.Store {
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
	store := missions.NewStore(pool, log)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		// Delete the missions plus any hidden sessions they provisioned,
		// which would otherwise linger as empty chats in the session list.
		_, _ = db.Exec(cctx, `WITH gone AS (
			DELETE FROM missions WHERE goal LIKE $1 || '%' RETURNING session_id
		), ids AS (SELECT session_id FROM gone WHERE session_id IS NOT NULL),
		g AS (DELETE FROM session_grants WHERE session_id IN (SELECT session_id FROM ids)),
		a AS (DELETE FROM tool_audit WHERE session_id IN (SELECT session_id FROM ids)),
		o AS (DELETE FROM tool_outputs WHERE session_id IN (SELECT session_id FROM ids)),
		e AS (DELETE FROM session_events WHERE session_id IN (SELECT session_id FROM ids))
		DELETE FROM sessions WHERE id IN (SELECT session_id FROM ids)`, "itest-api-mission ")
	})
	return store
}

// TestMissionsResumeWithAnswerReachesWorker confirms POST
// .../resume with {"answer": "..."} appends a progress note and a
// mission.answered event BEFORE signaling resume — the progress note is
// what the next worker session's packet actually renders (WorkPacket.Render
// in packet.go reads m.Progress verbatim, no packet changes needed for
// the answer to reach the worker).
func TestMissionsResumeWithAnswerReachesWorker(t *testing.T) {
	store := testMissionStore(t)
	ctx := context.Background()

	id, err := store.Create(ctx, missions.Mission{Goal: "itest-api-mission answer test", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Park it in waiting_for_input, same shape a real worker_blocked
	// transition leaves behind, so Signal(InputResume) has something
	// legal to resume from.
	if err := store.ApplyTransition(ctx, id, missions.Transition{
		Next: missions.StepState{Phase: missions.PhaseExecute, Status: missions.StatusWaitingForInput},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}

	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("POST", "/v1/missions/"+id+"/resume", strings.NewReader(`{"answer":"the deploy target is staging"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("resume with answer = %d, want 204", w.Code)
	}

	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != missions.StatusIdle {
		t.Fatalf("status after resume = %q, want idle", got.Status)
	}
	if len(got.Progress) != 1 || !strings.Contains(got.Progress[0].Note, "Answer to your question: the deploy target is staging") {
		t.Fatalf("progress = %+v, want one note containing the answer", got.Progress)
	}

	events, err := store.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawAnswered, sawResumed bool
	for _, e := range events {
		switch e.Kind {
		case "mission.answered":
			sawAnswered = true
			if !strings.Contains(string(e.Payload), "the deploy target is staging") {
				t.Fatalf("mission.answered payload = %s, want the answer", e.Payload)
			}
		case "mission.resumed":
			sawResumed = true
		}
		// The answer must be recorded before the resume — same
		// invariant the state machine's own event ordering already
		// relies on elsewhere.
		if e.Kind == "mission.resumed" && sawAnswered == false {
			t.Fatal("mission.resumed appeared before mission.answered")
		}
	}
	if !sawAnswered || !sawResumed {
		t.Fatalf("events = %+v, want both mission.answered and mission.resumed", events)
	}
}

// TestMissionsResumeWithoutAnswerLeavesProgressUntouched confirms the
// pause-case resume (no body) still works and writes no progress note —
// resume without an answer must stay legal.
func TestMissionsResumeWithoutAnswerLeavesProgressUntouched(t *testing.T) {
	store := testMissionStore(t)
	ctx := context.Background()

	id, err := store.Create(ctx, missions.Mission{Goal: "itest-api-mission no answer", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.ApplyTransition(ctx, id, missions.Transition{
		Next: missions.StepState{Phase: missions.PhaseExecute, Status: missions.StatusPaused, PauseReason: missions.PauseInfra},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}

	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("POST", "/v1/missions/"+id+"/resume", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("resume without a body = %d, want 204", w.Code)
	}

	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != missions.StatusIdle {
		t.Fatalf("status after resume = %q, want idle", got.Status)
	}
	if len(got.Progress) != 0 {
		t.Fatalf("progress = %+v, want none (no answer given)", got.Progress)
	}
}
