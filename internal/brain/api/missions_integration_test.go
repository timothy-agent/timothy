//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

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

// TestMissionsCreateCarriesPlanRoute confirms a create request's
// plan_route reaches the created mission row (both the create
// response and a subsequent store read) — mirrors
// TestMissionsCreateResponseCarriesDetectedEnvironment's round-trip
// shape for the new field.
func TestMissionsCreateCarriesPlanRoute(t *testing.T) {
	store := testMissionStore(t)

	driver := missions.NewDriver(store, errRunner{}, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

	body := `{"goal":"itest-api-mission plan route round trip","kind":"general","route":"mini","plan_route":"strong"}`
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
	if created.PlanRoute != "strong" {
		t.Fatalf("create response plan_route = %q, want %q", created.PlanRoute, "strong")
	}

	got, err := store.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PlanRoute != "strong" {
		t.Fatalf("stored plan_route = %q, want %q", got.PlanRoute, "strong")
	}
}

// TestMissionsCreateFollowUp covers the follow-up gate end to end: a
// parent that isn't terminal yet 409s, and a terminal parent's create
// succeeds with parent_mission_id/parent_context set to the parent's
// outcome digest — mirrors TestMissionsCreateCarriesPlanRoute's
// round-trip shape for the new fields.
func TestMissionsCreateFollowUp(t *testing.T) {
	store := testMissionStore(t)

	driver := missions.NewDriver(store, errRunner{}, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

	post := func(body string) (int, []byte) {
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}

	// A non-terminal parent 409s.
	parentID, err := store.Create(context.Background(), missions.Mission{
		Goal: "itest-api-mission follow-up parent (live)", Kind: "general",
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if code, body := post(`{"goal":"itest-api-mission follow-up child","kind":"general","parent_mission_id":"` + parentID + `"}`); code != http.StatusConflict {
		t.Fatalf("follow-up of a non-terminal parent: code=%d body=%s, want 409", code, body)
	}

	// Advance the parent to terminal, then the follow-up succeeds and
	// carries the parent's outcome digest.
	if err := store.ApplyTransition(context.Background(), parentID, missions.Transition{
		Next: missions.StepState{Phase: missions.PhaseDone, Status: missions.StatusDone},
	}); err != nil {
		t.Fatalf("apply transition to terminal: %v", err)
	}
	code, body := post(`{"goal":"itest-api-mission follow-up child","kind":"general","parent_mission_id":"` + parentID + `"}`)
	if code != http.StatusCreated {
		t.Fatalf("follow-up of a terminal parent: code=%d body=%s, want 201", code, body)
	}
	var created missions.Mission
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ParentMissionID != parentID {
		t.Fatalf("create response parent_mission_id = %q, want %q", created.ParentMissionID, parentID)
	}
	if !strings.Contains(created.ParentContext, "itest-api-mission follow-up parent (live)") {
		t.Fatalf("create response parent_context = %q, want it to contain the parent's goal", created.ParentContext)
	}
	if !strings.Contains(created.ParentContext, "terminal state: done") {
		t.Fatalf("create response parent_context = %q, want it to name the parent's terminal state", created.ParentContext)
	}

	got, err := store.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ParentMissionID != parentID || got.ParentContext != created.ParentContext {
		t.Fatalf("stored parent lineage = (%q, %q), want it to match the create response", got.ParentMissionID, got.ParentContext)
	}
}

// TestMissionsCreateFollowUpUnknownParent covers the 400 path with a
// LIVE store (TestMissionsCreateValidatesParentMission in
// missions_test.go covers the same shape against a degraded pool,
// which can't distinguish "unknown id" from "store unreachable").
func TestMissionsCreateFollowUpUnknownParent(t *testing.T) {
	store := testMissionStore(t)

	driver := missions.NewDriver(store, errRunner{}, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

	body := `{"goal":"itest-api-mission follow-up unknown parent","kind":"general","parent_mission_id":"00000000-0000-0000-0000-000000000000"}`
	req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "parent mission not found") {
		t.Fatalf("unknown parent_mission_id: code=%d body=%s, want 400 with a parent-not-found message", w.Code, w.Body.String())
	}
}

// itestPDFBytes is a minimal PDF header, enough for
// http.DetectContentType to sniff "application/pdf" the same way
// attachments.Store.Save's own tests do (attachments_integration_test.go).
var itestPDFBytes = []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<< /Type /Catalog >>\nendobj\n")

// TestMissionsCreateWithPDFAttachment covers the create-time
// attachment flow end to end against a live store, a real
// *attachments.Store, and a markitdown stub: the mission row carries
// the converted (and, in the oversized case, truncated) markdown, and
// every response (create, get, list) has it stripped.
func TestMissionsCreateWithPDFAttachment(t *testing.T) {
	store := testMissionStore(t)
	dsn := os.Getenv("DATABASE_URL")
	pool := pgpool.New(t.Context(), dsn, discard())
	if err := pool.WaitHealthy(t.Context()); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	attStore := attachments.New(t.TempDir(), pool)

	driver := missions.NewDriver(store, errRunner{}, nil, nil, nil, nil, nil, nil, discard())
	a := &API{token: "tok", log: discard()}

	post := func(m *http.ServeMux, body string) (int, []byte) {
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}

	att, err := attStore.Save(context.Background(), bytes.NewReader(itestPDFBytes))
	if err != nil {
		t.Fatalf("save attachment: %v", err)
	}

	t.Run("converts and snapshots markdown, stripped from responses", func(t *testing.T) {
		md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"markdown": "# converted spec\ndo the thing"})
		}))
		defer md.Close()

		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, attStore, md.URL, nil)

		code, body := post(m, `{"goal":"itest-api-mission with attachment","kind":"general","attachments":[{"id":"`+att.ID+`","name":"spec.pdf"}]}`)
		if code != http.StatusCreated {
			t.Fatalf("create with attachment: code=%d body=%s, want 201", code, body)
		}
		var created missions.Mission
		if err := json.Unmarshal(body, &created); err != nil {
			t.Fatalf("decode create response: %v", err)
		}
		if len(created.Attachments) != 1 || created.Attachments[0].Markdown != "" {
			t.Fatalf("create response attachments = %+v, want one entry with markdown stripped", created.Attachments)
		}

		stored, err := store.Get(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(stored.Attachments) != 1 || stored.Attachments[0].Markdown != "# converted spec\ndo the thing" {
			t.Fatalf("stored attachments = %+v, want the converted markdown snapshotted", stored.Attachments)
		}
		if stored.Attachments[0].Name != "spec.pdf" || stored.Attachments[0].Mime != "application/pdf" {
			t.Fatalf("stored attachment = %+v, want name/mime carried through", stored.Attachments[0])
		}

		getReq := httptest.NewRequest("GET", "/v1/missions/"+created.ID, nil)
		getReq.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, getReq)
		var got missions.Mission
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode get response: %v", err)
		}
		if got.Attachments[0].Markdown != "" {
			t.Fatalf("get response markdown = %q, want stripped", got.Attachments[0].Markdown)
		}

		listReq := httptest.NewRequest("GET", "/v1/missions", nil)
		listReq.Header.Set("Authorization", "Bearer tok")
		w = httptest.NewRecorder()
		m.ServeHTTP(w, listReq)
		var list struct {
			Missions []missions.Mission `json:"missions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatalf("decode list response: %v", err)
		}
		for _, lm := range list.Missions {
			for _, la := range lm.Attachments {
				if la.Markdown != "" {
					t.Fatalf("list response markdown = %q, want stripped for mission %s", la.Markdown, lm.ID)
				}
			}
		}
	})

	t.Run("markitdown response over the cap is truncated", func(t *testing.T) {
		huge := strings.Repeat("a", 128<<10+1024)
		md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"markdown": huge})
		}))
		defer md.Close()

		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, attStore, md.URL, nil)

		code, body := post(m, `{"goal":"itest-api-mission with huge attachment","kind":"general","attachments":[{"id":"`+att.ID+`"}]}`)
		if code != http.StatusCreated {
			t.Fatalf("create with attachment: code=%d body=%s, want 201", code, body)
		}
		var created missions.Mission
		if err := json.Unmarshal(body, &created); err != nil {
			t.Fatalf("decode create response: %v", err)
		}
		stored, err := store.Get(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(stored.Attachments[0].Markdown) >= len(huge) {
			t.Fatalf("stored markdown len = %d, want truncated below %d", len(stored.Attachments[0].Markdown), len(huge))
		}
		if !strings.Contains(stored.Attachments[0].Markdown, "truncated") {
			t.Fatalf("stored markdown = %q, want a truncation marker", stored.Attachments[0].Markdown)
		}
	})

	t.Run("markitdown 500 surfaces as internal_error", func(t *testing.T) {
		md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "conversion failed", http.StatusInternalServerError)
		}))
		defer md.Close()

		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, attStore, md.URL, nil)

		code, body := post(m, `{"goal":"itest-api-mission markitdown failure","kind":"general","attachments":[{"id":"`+att.ID+`"}]}`)
		if code != http.StatusInternalServerError {
			t.Fatalf("markitdown 500: code=%d body=%s, want 500", code, body)
		}
	})
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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nameMission, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nameMission, nil, nil, nil, "", nil)

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

// --- push/pr with a github-connection mission ---

// requireGitForAPI mirrors missions.requireGit (unexported to that
// package) — these tests exercise real git clones/pushes, same as
// worktree_test.go's coverage, just from the API layer down.
func requireGitForAPI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping")
	}
}

func gitRunAPI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // fixed test-fixture subcommands
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// gitHTTPBackendTLSServer stands up a real https:// git remote backed
// by git-http-backend (CGI) over an httptest TLS server — validateRemote
// (push.go) requires a genuine https:// scheme, so a local bare repo's
// plain filesystem path (the trick push_test.go/worktree_test.go use
// for rawPush, which bypasses validateRemote) can't stand in here; this
// gives Workspace.Push a real https origin to push against without any
// network access. Callers must set GIT_SSL_NO_VERIFY=1 in the pushing
// process's env (the server's cert is self-signed) — see
// requireGitSSLNoVerify.
func gitHTTPBackendTLSServer(t *testing.T, reposRoot string) *httptest.Server {
	t.Helper()
	handler := &cgi.Handler{
		Path: "/usr/lib/git-core/git-http-backend",
		Env:  []string{"GIT_HTTP_EXPORT_ALL=1", "GIT_PROJECT_ROOT=" + reposRoot},
		Dir:  reposRoot,
	}
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// requireGitSSLNoVerify sets GIT_SSL_NO_VERIFY=1 in the test process's
// own environment for the test's duration — rawPush/cloneRepo build
// cmd.Env from os.Environ(), so this propagates to every git subprocess
// they spawn, letting them trust gitHTTPBackendTLSServer's self-signed
// cert without touching production code (which never sets this itself).
func requireGitSSLNoVerify(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
}

// seedBareRepoWithMissionBranch builds a bare repo served over a real
// local https server (gitHTTPBackendTLSServer), then clones it into a
// mission worktree already checked out on branch — mirroring what
// Workspace.Provision would have produced for a real repo_url mission,
// without going through Provision itself (these tests fabricate the
// mission row directly via store.Create + SetProvisioned, so no
// connector token/identity resolution needs to run at provision time).
func seedBareRepoWithMissionBranch(t *testing.T) (repoURL, worktree, branch string) {
	t.Helper()
	requireGitSSLNoVerify(t)

	reposRoot := t.TempDir()
	bareName := "repo.git"
	bare := reposRoot + "/" + bareName
	gitRunAPI(t, reposRoot, "init", "-q", "--bare", "-b", "main", bareName)
	// git-http-backend's default receive-pack refuses non-fast-forward
	// pushes only; it's disabled from serving at all unless the repo
	// opts in (GIT_HTTP_EXPORT_ALL above covers the read side, but a
	// push additionally needs http.receivepack on).
	gitRunAPI(t, bare, "config", "http.receivepack", "true")

	srv := gitHTTPBackendTLSServer(t, reposRoot)
	repoURL = srv.URL + "/" + bareName

	seed := t.TempDir()
	gitRunAPI(t, seed, "init", "-q", "-b", "main")
	if err := os.WriteFile(seed+"/README.md", []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRunAPI(t, seed, "add", "README.md")
	gitRunAPI(t, seed, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "seed")
	gitRunAPI(t, seed, "remote", "add", "origin", repoURL)
	gitRunAPI(t, seed, "push", "-q", "origin", "main")

	worktree = t.TempDir() + "/wt"
	gitRunAPI(t, t.TempDir(), "clone", "-q", repoURL, worktree)
	branch = "mission/itest-pr-test"
	gitRunAPI(t, worktree, "checkout", "-q", "-b", branch)
	// A local change to push — an empty diff from origin/main would
	// still push fine, but a real file makes the test's intent obvious.
	if err := os.WriteFile(worktree+"/change.txt", []byte("mission work"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRunAPI(t, worktree, "add", "change.txt")
	gitRunAPI(t, worktree, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "mission work")
	return repoURL, worktree, branch
}

// fakeGitHubSource is a connectors.Source implementing the repoSource
// capability (GetRepo/CreatePR, ListRepos/CreateRepo unused here) by
// hand — used instead of connectors.GitHubBuilder + a real HTTP fake
// server, since githubAPIBase (the base URL GitHubBuilder's requests
// hit) is unexported to the connectors package and this test lives in
// api. GetRepo/CreatePR themselves are already covered against the
// real wire format in internal/brain/connectors/github_test.go; this
// fake only needs to prove the API layer calls through to them
// correctly.
type fakeGitHubSource struct {
	getRepoFn  func(ctx context.Context, owner, repo string) (connectors.GitHubRepo, error)
	createPRFn func(ctx context.Context, owner, repo, title, head, base, body string) (connectors.GitHubPR, error)
}

func (f *fakeGitHubSource) Tools() []*tools.Tool       { return nil }
func (f *fakeGitHubSource) Test(context.Context) error { return nil }
func (f *fakeGitHubSource) Close() error               { return nil }
func (f *fakeGitHubSource) ListRepos(context.Context) ([]connectors.GitHubRepo, error) {
	return nil, errors.New("not implemented in fakeGitHubSource")
}
func (f *fakeGitHubSource) CreateRepo(context.Context, string, bool) (connectors.GitHubRepo, error) {
	return connectors.GitHubRepo{}, errors.New("not implemented in fakeGitHubSource")
}
func (f *fakeGitHubSource) GetRepo(ctx context.Context, owner, repo string) (connectors.GitHubRepo, error) {
	return f.getRepoFn(ctx, owner, repo)
}
func (f *fakeGitHubSource) CreatePR(ctx context.Context, owner, repo, title, head, base, body string) (connectors.GitHubPR, error) {
	return f.createPRFn(ctx, owner, repo, title, head, base, body)
}

// testConnectorsManager builds a real *connectors.Manager backed by the
// same test Postgres pool, with the github builder returning src for
// every build — enough to exercise the API layer's connector-resolution
// and PR-flow wiring without touching real GitHub HTTP.
func testConnectorsManager(t *testing.T, src connectors.Source) *connectors.Manager {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	// context.Background(), not t.Context(): the pool's manage/watch
	// loop (pgpool.go) closes the underlying connection the moment its
	// context is Done, and t.Context() cancels at test end BEFORE
	// t.Cleanup callbacks run — a cleanup that still needs this pool
	// (createGitHubConnectorRow's own delete) would race the pool
	// tearing itself down. The pool has no other lifetime owner here, so
	// tying it to Background is safe: it's simply dropped (never
	// explicitly closed) once the test process is done with it.
	pool := pgpool.New(context.Background(), dsn, discard())
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	store := connectors.NewStore(pool, discard())
	resolve := func(context.Context, string) (string, error) { return "fake-pat-token", nil }
	mgr := connectors.NewManager(store, resolve, discard())
	mgr.RegisterBuilder("github", func(context.Context, connectors.Connector, connectors.Resolve) (connectors.Source, error) {
		return src, nil
	})
	return mgr
}

// createGitHubConnectorRow inserts a real github-kind connector row
// against the test Postgres, cleaned up on test end.
func createGitHubConnectorRow(t *testing.T, mgr *connectors.Manager) string {
	t.Helper()
	id, err := mgr.Store().Create(t.Context(), connectors.Connector{
		Name: "itest-api-pr-gh", Kind: "github", CredentialRef: "GH_PAT", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}
	t.Cleanup(func() {
		if err := mgr.Store().Delete(context.Background(), id); err != nil {
			t.Logf("cleanup: delete connector %s: %v", id, err)
		}
	})
	return id
}

// createGitHubConnectionMission fabricates a mission row with
// connector_id/repo_url set and SetProvisioned pointed at worktree —
// the shape Driver.ensureProvisioned would have produced for a real
// repo_url mission, without actually running provisioning (these tests
// only exercise push/pr, not clone/authorship, which worktree_test.go
// already covers).
func createGitHubConnectionMission(t *testing.T, store *missions.Store, connectorID, repoURL, worktree, branch string) string {
	t.Helper()
	id, err := store.Create(t.Context(), missions.Mission{
		Goal: "itest-api-mission pr flow", Kind: "coding", RepoURL: repoURL, ConnectorID: connectorID,
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	if err := store.SetProvisioned(t.Context(), id, worktree+"/..", worktree, branch, "deadbeef"); err != nil {
		t.Fatalf("SetProvisioned: %v", err)
	}
	return id
}

// TestMissionsPushResolvesConnectorToken proves POST .../push with no
// credential_ref in the body resolves the mission's connector's PAT
// instead — the connector-resolution path SetCloneTokenResolver's
// sibling wires at the push endpoint.
func TestMissionsPushResolvesConnectorToken(t *testing.T) {
	requireGitForAPI(t)
	store := testMissionStore(t)
	// push never builds a repoSource (only Store().Get for the
	// credential_ref) — an empty fakeGitHubSource is a safe stand-in;
	// getRepoFn/createPRFn would only be invoked if the endpoint
	// unexpectedly tried to build one.
	mgr := testConnectorsManager(t, &fakeGitHubSource{})
	connID := createGitHubConnectorRow(t, mgr)
	repoURL, worktree, branch := seedBareRepoWithMissionBranch(t)

	id := createGitHubConnectionMission(t, store, connID, repoURL, worktree, branch)
	workspace := missions.NewWorkspace(t.TempDir(), nil, discard())
	var resolvedRef string
	resolveSecret := func(_ context.Context, ref string) (string, error) {
		resolvedRef = ref
		return "dummy-token", nil
	}

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, nil, nil, nil, workspace, resolveSecret, nil, nil, nil, nil, nil, nil, mgr, nil, "", nil)

	req := httptest.NewRequest("POST", "/v1/missions/"+id+"/push", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("push with no credential_ref on a github-connection mission = %d, want 200: %s", w.Code, w.Body.String())
	}
	if resolvedRef != "GH_PAT" {
		t.Fatalf("resolveSecret ref = %q, want the connector's own credential_ref GH_PAT", resolvedRef)
	}
}

// TestMissionsPushConnectorTable covers the connector-resolution error
// paths: a missing connector_id, a disabled connector, and a
// non-github-kind connector each report a clear 400 rather than
// silently falling through to "credential_ref is required".
func TestMissionsPushConnectorTable(t *testing.T) {
	requireGitForAPI(t)
	store := testMissionStore(t)

	cases := []struct {
		name       string
		setupConns func(t *testing.T) (*connectors.Manager, string) // returns (mgr, connectorID)
	}{
		{
			name: "connector missing",
			setupConns: func(t *testing.T) (*connectors.Manager, string) {
				mgr := testConnectorsManager(t, &fakeGitHubSource{})
				return mgr, "00000000-0000-0000-0000-000000000000"
			},
		},
		{
			name: "connector disabled",
			setupConns: func(t *testing.T) (*connectors.Manager, string) {
				mgr := testConnectorsManager(t, &fakeGitHubSource{})
				id, err := mgr.Store().Create(t.Context(), connectors.Connector{
					Name: "itest-api-pr-disabled", Kind: "github", CredentialRef: "GH_PAT", Enabled: false,
				})
				if err != nil {
					t.Fatalf("create connector: %v", err)
				}
				t.Cleanup(func() { _ = mgr.Store().Delete(context.Background(), id) })
				return mgr, id
			},
		},
		{
			name: "connector not github kind",
			setupConns: func(t *testing.T) (*connectors.Manager, string) {
				mgr := testConnectorsManager(t, &fakeGitHubSource{})
				id, err := mgr.Store().Create(t.Context(), connectors.Connector{
					Name: "itest-api-pr-mcp", Kind: "mcp", CredentialRef: "", Enabled: true,
				})
				if err != nil {
					t.Fatalf("create connector: %v", err)
				}
				t.Cleanup(func() { _ = mgr.Store().Delete(context.Background(), id) })
				return mgr, id
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, connID := tc.setupConns(t)
			bare, worktree, branch := seedBareRepoWithMissionBranch(t)
			id := createGitHubConnectionMission(t, store, connID, bare, worktree, branch)
			workspace := missions.NewWorkspace(t.TempDir(), nil, discard())

			a := &API{token: "tok", log: discard()}
			m := mux(a)
			a.registerMissions(m.Handle, store, nil, nil, nil, workspace, nil, nil, nil, nil, nil, nil, nil, mgr, nil, "", nil)

			req := httptest.NewRequest("POST", "/v1/missions/"+id+"/push", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer tok")
			w := httptest.NewRecorder()
			m.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("push with %s = %d, want 400: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestMissionsPushExplicitCredentialRefOverridesConnector proves an
// explicit credential_ref in the body still wins even on a
// github-connection mission — the override path.
func TestMissionsPushExplicitCredentialRefOverridesConnector(t *testing.T) {
	requireGitForAPI(t)
	store := testMissionStore(t)
	// push never builds a repoSource on the explicit-ref path either.
	mgr := testConnectorsManager(t, &fakeGitHubSource{})
	connID := createGitHubConnectorRow(t, mgr)
	bare, worktree, branch := seedBareRepoWithMissionBranch(t)
	id := createGitHubConnectionMission(t, store, connID, bare, worktree, branch)
	workspace := missions.NewWorkspace(t.TempDir(), nil, discard())

	var gotRef string
	resolveSecret := func(_ context.Context, ref string) (string, error) {
		gotRef = ref
		return "dummy-token", nil
	}

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, nil, nil, nil, workspace, resolveSecret, nil, nil, nil, nil, nil, nil, mgr, nil, "", nil)

	req := httptest.NewRequest("POST", "/v1/missions/"+id+"/push", strings.NewReader(`{"credential_ref":"explicit-ref"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("push with explicit credential_ref = %d, want 200: %s", w.Code, w.Body.String())
	}
	if gotRef != "explicit-ref" {
		t.Fatalf("resolveSecret ref = %q, want the explicit override", gotRef)
	}
}

// TestMissionsPRHappyPath exercises POST .../pr end to end: push, then
// GetRepo (default_branch), then CreatePR, recording mission.pr_opened.
// GetRepo/CreatePR's own wire-format correctness (including the
// already-exists retry) is covered directly against a fake GitHub
// server in internal/brain/connectors/github_test.go; this test proves
// the API layer's push-then-lookup-then-create sequencing and event
// recording.
func TestMissionsPRHappyPath(t *testing.T) {
	requireGitForAPI(t)
	store := testMissionStore(t)
	var sawPRCreate bool
	mgr := testConnectorsManager(t, &fakeGitHubSource{
		getRepoFn: func(_ context.Context, owner, repo string) (connectors.GitHubRepo, error) {
			if owner != "octocat" || repo != "hello-world" {
				t.Fatalf("GetRepo(%q, %q), want octocat/hello-world", owner, repo)
			}
			return connectors.GitHubRepo{FullName: "octocat/hello-world", DefaultBranch: "main"}, nil
		},
		createPRFn: func(_ context.Context, owner, repo, title, head, base, body string) (connectors.GitHubPR, error) {
			sawPRCreate = true
			if base != "main" {
				t.Fatalf("CreatePR base = %q, want main (the repo's default branch)", base)
			}
			return connectors.GitHubPR{Number: 9, HTMLURL: "https://github.com/octocat/hello-world/pull/9", State: "open"}, nil
		},
	})
	connID := createGitHubConnectorRow(t, mgr)
	// worktree's real origin (from seedBareRepoWithMissionBranch) is the
	// local https test server — the actual push target. repo_url is set
	// to a realistic github.com shape instead: the pr endpoint parses
	// owner/repo from THAT field (mission.RepoURL), never from the
	// worktree's origin remote, exactly like a real github-connection
	// mission would.
	_, worktree, branch := seedBareRepoWithMissionBranch(t)
	id := createGitHubConnectionMission(t, store, connID, "https://github.com/octocat/hello-world.git", worktree, branch)

	workspace := missions.NewWorkspace(t.TempDir(), nil, discard())
	resolveSecret := func(context.Context, string) (string, error) { return "dummy-token", nil }

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, nil, nil, nil, workspace, resolveSecret, nil, nil, nil, nil, nil, nil, mgr, nil, "", nil)

	req := httptest.NewRequest("POST", "/v1/missions/"+id+"/pr", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pr = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !sawPRCreate {
		t.Fatal("PR create endpoint was never called")
	}
	var resp struct {
		URL    string `json:"url"`
		Number int    `json:"number"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Number != 9 {
		t.Fatalf("response = %+v, want number 9", resp)
	}

	events, err := store.Events(context.Background(), id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawEvent bool
	for _, e := range events {
		if e.Kind == "mission.pr_opened" {
			sawEvent = true
			var payload struct {
				URL    string `json:"url"`
				Number int    `json:"number"`
			}
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("decode mission.pr_opened payload: %v", err)
			}
			if payload.Number != 9 {
				t.Fatalf("mission.pr_opened payload = %+v, want number 9", payload)
			}
		}
	}
	if !sawEvent {
		t.Fatal("mission.pr_opened event was not recorded")
	}
}

// TestMissionsPRAlreadyExistsReturnsExisting proves the pr endpoint
// surfaces whatever CreatePR returns even when it internally resolved
// an "already exists" conflict to the existing PR (the retry logic
// itself is connectors.githubSource.CreatePR's — see
// TestManagerCreatePRAlreadyExists in the connectors package for that
// wire-level behavior) — the re-call/idempotent path a repeated "Push
// & open PR" click takes.
func TestMissionsPRAlreadyExistsReturnsExisting(t *testing.T) {
	requireGitForAPI(t)
	store := testMissionStore(t)
	mgr := testConnectorsManager(t, &fakeGitHubSource{
		getRepoFn: func(context.Context, string, string) (connectors.GitHubRepo, error) {
			return connectors.GitHubRepo{FullName: "octocat/hello-world", DefaultBranch: "main"}, nil
		},
		createPRFn: func(context.Context, string, string, string, string, string, string) (connectors.GitHubPR, error) {
			// Simulates CreatePR having already resolved a 422
			// already-exists conflict to the existing open PR.
			return connectors.GitHubPR{Number: 55, HTMLURL: "https://github.com/octocat/hello-world/pull/55", State: "open"}, nil
		},
	})
	connID := createGitHubConnectorRow(t, mgr)
	_, worktree, branch := seedBareRepoWithMissionBranch(t)
	id := createGitHubConnectionMission(t, store, connID, "https://github.com/octocat/hello-world.git", worktree, branch)

	workspace := missions.NewWorkspace(t.TempDir(), nil, discard())
	resolveSecret := func(context.Context, string) (string, error) { return "dummy-token", nil }

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, nil, nil, nil, workspace, resolveSecret, nil, nil, nil, nil, nil, nil, mgr, nil, "", nil)

	req := httptest.NewRequest("POST", "/v1/missions/"+id+"/pr", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pr (already exists) = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Number != 55 {
		t.Fatalf("response = %+v, want the existing PR number 55", resp)
	}
}

// TestMissionsPRRejectsNonGitHubConnectionMission proves the pr
// endpoint 400s a real mission row that has neither connector_id nor
// repo_url — the gate exercised against an actual store this time
// (TestPRRejectsNonGitHubConnectionMission in missions_test.go only
// proves the route reaches the store).
func TestMissionsPRRejectsNonGitHubConnectionMission(t *testing.T) {
	store := testMissionStore(t)
	id, err := store.Create(t.Context(), missions.Mission{Goal: "itest-api-mission non-github pr", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	a := &API{token: "tok", log: discard()}
	m := mux(a)
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)

	req := httptest.NewRequest("POST", "/v1/missions/"+id+"/pr", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("pr on a non-github-connection mission = %d, want 400: %s", w.Code, w.Body.String())
	}
}
