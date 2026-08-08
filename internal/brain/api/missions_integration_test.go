//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
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

// errRunner is a missions.Runner that errors on every method — used
// in place of a nil runner wherever a test's create() call spawns
// Driver.Create's own background Drive goroutine (driver.go:219): a
// nil runner interface value panics the FIRST time Drive reaches a
// phase turn, which races unpredictably with (and can crash) an
// otherwise-unrelated test in the same process. A real error is what
// production would ever actually see from a broken runner.
type errRunner struct{}

func (errRunner) RunWorker(context.Context, missions.Mission, missions.WorkPacket) (missions.WorkerVerdict, string, error) {
	return missions.WorkerVerdict{}, "", errors.New("errRunner: not implemented")
}

func (errRunner) RunReview(context.Context, missions.Mission, missions.ReviewPacket, *missions.GatekeeperState) (missions.ReviewVerdict, *missions.GatekeeperState, error) {
	return missions.ReviewVerdict{}, nil, errors.New("errRunner: not implemented")
}

func (errRunner) PlanSession(context.Context, missions.Mission, string) (missions.Spec, error) {
	return missions.Spec{}, errors.New("errRunner: not implemented")
}

func (errRunner) ExploreSession(context.Context, missions.Mission) (string, error) {
	return "", errors.New("errRunner: not implemented")
}

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

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

// TestMissionsNoteAppendsEventAndProgressWithoutPhaseChange confirms
// POST .../note appends a mission.steered event and an "Operator
// note: ..." progress note, sanitized/truncated the same way resume's
// answer is, and leaves phase/status untouched — the note rides the
// next worker turn's own packet, no driver signal involved.
func TestMissionsNoteAppendsEventAndProgressWithoutPhaseChange(t *testing.T) {
	store := testMissionStore(t)
	ctx := context.Background()

	id, err := store.Create(ctx, missions.Mission{Goal: "itest-api-mission note test", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.ApplyTransition(ctx, id, missions.Transition{
		Next: missions.StepState{Phase: missions.PhaseExecute, Status: missions.StatusIdle},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}

	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("POST", "/v1/missions/"+id+"/note", strings.NewReader(`{"text":"focus on the staging config next"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("note = %d, want 200: %s", w.Code, w.Body.String())
	}

	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Phase != missions.PhaseExecute || got.Status != missions.StatusIdle {
		t.Fatalf("phase/status after note = %s/%s, want unchanged execute/idle", got.Phase, got.Status)
	}
	if len(got.Progress) != 1 || !strings.Contains(got.Progress[0].Note, "Operator note: focus on the staging config next") {
		t.Fatalf("progress = %+v, want one operator note", got.Progress)
	}

	events, err := store.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawSteered bool
	for _, e := range events {
		if e.Kind == "mission.steered" {
			sawSteered = true
			if !strings.Contains(string(e.Payload), "focus on the staging config next") {
				t.Fatalf("mission.steered payload = %s, want the note", e.Payload)
			}
		}
	}
	if !sawSteered {
		t.Fatalf("events = %+v, want mission.steered", events)
	}
}

// TestMissionsNoteUnknownMission confirms an unknown mission id 404s
// before any store write is attempted.
func TestMissionsNoteUnknownMission(t *testing.T) {
	store := testMissionStore(t)
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("POST", "/v1/missions/00000000-0000-0000-0000-000000000000/note", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("note on unknown mission = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// TestMissionsNoteEmptyTextRejected confirms an empty/missing text
// field 400s.
func TestMissionsNoteEmptyTextRejected(t *testing.T) {
	store := testMissionStore(t)
	ctx := context.Background()
	id, err := store.Create(ctx, missions.Mission{Goal: "itest-api-mission note empty test", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("POST", "/v1/missions/"+id+"/note", strings.NewReader(`{"text":""}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("note with empty text = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// TestMissionsNoteTerminalMissionRejected confirms a done/failed
// mission refuses the note with 409 already_finished rather than
// silently writing to a mission nothing will ever read again.
func TestMissionsNoteTerminalMissionRejected(t *testing.T) {
	store := testMissionStore(t)
	ctx := context.Background()
	id, err := store.Create(ctx, missions.Mission{Goal: "itest-api-mission note terminal test", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.ApplyTransition(ctx, id, missions.Transition{
		Next: missions.StepState{Phase: missions.PhaseDone, Status: missions.StatusIdle},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}

	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("POST", "/v1/missions/"+id+"/note", strings.NewReader(`{"text":"too late"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("note on terminal mission = %d, want 409: %s", w.Code, w.Body.String())
	}
}

// TestMissionsCreateResponseCarriesDetectedEnvironment confirms the
// POST /v1/missions response reflects the row create() actually
// persisted — the create() handler resolves an omitted coding
// mission's environment (D-05x, goal-keyword heuristic) BEFORE
// building the Mission it hands to Driver.Create, but the original bug
// was returning only {"id": id}: the detected value was stored (a
// later GET showed it) yet never reached the create response itself.
func TestMissionsCreateResponseCarriesDetectedEnvironment(t *testing.T) {
	store := testMissionStore(t)

	driver := missions.NewDriver(store, errRunner{}, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	body := `{"goal":"itest-api-mission write a Go CLI that parses logs","kind":"coding"}`
	req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", w.Code, w.Body.String())
	}

	var created missions.Mission
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Environment != "go" {
		t.Fatalf("create response environment = %q, want %q", created.Environment, "go")
	}

	got, err := store.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Environment != created.Environment {
		t.Fatalf("stored environment = %q, create response = %q, want equal", got.Environment, created.Environment)
	}
}

// TestMissionsCreateGeneratesNameAsync confirms a successful naming
// call lands on the row via SetNameIfEmpty without create having to
// wait for it — the create response itself carries no name yet (the
// call hasn't necessarily finished when create returns), but a
// subsequent GET picks it up once the background goroutine completes.
// Polls rather than gating on nameMission's own return, since the
// store write happens strictly after it — the same allowance
// chat_test.go's own waitFor makes for auto-title.
func TestMissionsCreateGeneratesNameAsync(t *testing.T) {
	store := testMissionStore(t)
	driver := missions.NewDriver(store, errRunner{}, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	nameMission := func(ctx context.Context, goal string) string {
		return "Parse Logs Utility"
	}
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nameMission, nil)

	body := `{"goal":"itest-api-mission write a Go CLI that parses logs","kind":"general"}`
	req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", w.Code, w.Body.String())
	}
	var created missions.Mission
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var got missions.Mission
	for time.Now().Before(deadline) {
		var err error
		got, err = store.Get(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Name != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Name != "Parse Logs Utility" {
		t.Fatalf("stored name = %q, want %q", got.Name, "Parse Logs Utility")
	}
}

// TestMissionsCreateNameFallsBackToEmptyOnGenerationFailure confirms a
// nameMission failure (returns "", matching chat.TitleOverGateway's
// own best-effort contract) leaves the mission's name empty rather
// than writing anything — the client is expected to fall back to a
// truncated goal.
func TestMissionsCreateNameFallsBackToEmptyOnGenerationFailure(t *testing.T) {
	store := testMissionStore(t)
	driver := missions.NewDriver(store, errRunner{}, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	done := make(chan struct{})
	nameMission := func(ctx context.Context, goal string) string {
		defer close(done)
		return "" // simulates a gateway/timeout failure, same as TitleOverGateway
	}
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nameMission, nil)

	body := `{"goal":"itest-api-mission a goal whose naming will fail","kind":"general"}`
	req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", w.Code, w.Body.String())
	}
	var created missions.Mission
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("nameMission was never called")
	}
	got, err := store.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "" {
		t.Fatalf("stored name = %q, want empty on generation failure", got.Name)
	}
}
