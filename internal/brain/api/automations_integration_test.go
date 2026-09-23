//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/automations"
	"github.com/SumonMSelim/timothy/internal/brain/destinations"
	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

// automationHarness is the automations surface mounted over real
// stores, plus destinations and the missions list for cross-checks.
type automationHarness struct {
	t        *testing.T
	mux      *http.ServeMux
	pool     *pgpool.Pool
	missions *missions.Store
	tag      string
	agentID  string
	kicks    *atomic.Int32
}

func newAutomationHarness(t *testing.T) *automationHarness {
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
	ms := missions.NewStore(pool, log)
	tag := fmt.Sprintf("itest-api-automation-%d-", time.Now().UnixNano())
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Exec(cctx, `DELETE FROM missions WHERE goal LIKE $1 || '%'`, tag)
		_, _ = db.Exec(cctx, `DELETE FROM events WHERE source = 'manual' AND payload->>'automation_id' IN (SELECT id::text FROM automations WHERE name LIKE $1 || '%')`, tag)
		_, _ = db.Exec(cctx, `DELETE FROM automations WHERE name LIKE $1 || '%'`, tag)
		_, _ = db.Exec(cctx, `DELETE FROM destinations WHERE name LIKE $1 || '%'`, tag)
	})
	var agentID string
	if err := db.QueryRow(t.Context(), `SELECT id FROM agents WHERE is_default LIMIT 1`).Scan(&agentID); err != nil {
		t.Fatalf("default agent: %v", err)
	}
	store := automations.NewStore(pool)
	dest := destinations.NewStore(pool, nil, log)
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	kicks := &atomic.Int32{}
	a.registerAutomations(m.Handle, store, events.NewStore(pool), func() { kicks.Add(1) }, dest, &attachmentResolver{}, func(context.Context) *time.Location { return ams })
	a.registerDestinations(m.Handle, dest, ms, store, nil)
	mh := &missionAPI{store: ms}
	m.Handle("GET /v1/missions", a.auth(http.HandlerFunc(mh.list)))
	return &automationHarness{t: t, mux: m, pool: pool, missions: ms, tag: tag, agentID: agentID, kicks: kicks}
}

func (h *automationHarness) do(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, req)
	return w
}

func (h *automationHarness) decode(w *httptest.ResponseRecorder, v any) {
	h.t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		h.t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
}

func (h *automationHarness) create(name, extra string) string {
	h.t.Helper()
	body := fmt.Sprintf(`{"name": %q, "agent_id": %q,
		"action": {"kind": "mission", "mission": {"goal": "g", "kind": "general", "light": true}},
		"triggers": [{"kind": "cron", "config": {"expr": "0 9 * * *"}}, {"kind": "manual"}]%s}`, h.tag+name, h.agentID, extra)
	w := h.do("POST", "/v1/automations", body)
	if w.Code != 201 {
		h.t.Fatalf("create %s = %d %s", name, w.Code, w.Body.String())
	}
	var out struct{ ID string }
	h.decode(w, &out)
	return out.ID
}

type viewJSON struct {
	automations.Automation
	Stats automations.AutomationStats `json:"stats"`
}

func TestAutomationsAPILifecycle(t *testing.T) {
	h := newAutomationHarness(t)
	id := h.create("lifecycle", `, "description": "daily", "expires_at": "2030-01-01T00:00:00Z"`)

	w := h.do("GET", "/v1/automations/"+id, "")
	if w.Code != 200 {
		t.Fatalf("get = %d %s", w.Code, w.Body.String())
	}
	var v viewJSON
	h.decode(w, &v)
	if v.Name != h.tag+"lifecycle" || v.Concurrency != "skip" || v.MaxRunsPerHour != 6 || !v.Continuity || !v.NotesEnabled || !v.Enabled {
		t.Fatalf("defaults = %+v", v.Automation)
	}
	if len(v.Triggers) != 2 || v.Stats.RunsTotal != 0 || v.Stats.NextRunAt == nil || v.ExpiresAt == nil {
		t.Fatalf("view = %+v stats = %+v", v.Automation, v.Stats)
	}
	// next_run_at is 09:00 Amsterdam.
	if got := v.Stats.NextRunAt.In(mustLoc(t, "Europe/Amsterdam")); got.Hour() != 9 || got.Minute() != 0 {
		t.Fatalf("next_run_at = %v, want 09:00 Amsterdam", got)
	}

	w = h.do("GET", "/v1/automations", "")
	var list struct{ Automations []viewJSON }
	h.decode(w, &list)
	found := false
	for _, a := range list.Automations {
		found = found || a.ID == id
	}
	if w.Code != 200 || !found {
		t.Fatalf("list = %d, found = %v", w.Code, found)
	}

	cronID := v.Triggers[0].ID
	body := fmt.Sprintf(`{"name": %q, "triggers": [{"id": %q, "kind": "cron", "config": {"expr": "15 7 * * 1-5"}}]}`, h.tag+"renamed", cronID)
	w = h.do("PATCH", "/v1/automations/"+id, body)
	if w.Code != 200 {
		t.Fatalf("patch = %d %s", w.Code, w.Body.String())
	}
	h.decode(w, &v)
	if v.Name != h.tag+"renamed" || len(v.Triggers) != 1 || v.Triggers[0].ID != cronID {
		t.Fatalf("patched = %+v", v.Automation)
	}

	// Omitted expires_at leaves it; explicit null clears it.
	w = h.do("PATCH", "/v1/automations/"+id, `{"description": "still daily"}`)
	h.decode(w, &v)
	if v.ExpiresAt == nil || v.Description != "still daily" {
		t.Fatalf("omitted expires_at changed it: %+v", v.Automation)
	}
	w = h.do("PATCH", "/v1/automations/"+id, `{"expires_at": null}`)
	v = viewJSON{}
	h.decode(w, &v)
	if w.Code != 200 || v.ExpiresAt != nil {
		t.Fatalf("null expires_at = %d, %v, want cleared", w.Code, v.ExpiresAt)
	}

	w = h.do("PATCH", "/v1/automations/"+id, `{"triggers": [{"kind": "cron", "config": {"expr": "bad"}}]}`)
	if w.Code != 400 {
		t.Fatalf("bad cron patch = %d", w.Code)
	}
	assertErrorBody(t, w, "bad_cron", "invalid cron")

	w = h.do("GET", "/v1/automations/"+id+"/runs?limit=5", "")
	var runs struct{ Runs []automations.Run }
	h.decode(w, &runs)
	if w.Code != 200 || runs.Runs == nil || len(runs.Runs) != 0 {
		t.Fatalf("runs = %d %s", w.Code, w.Body.String())
	}
	if w = h.do("GET", "/v1/automations/"+id+"/runs?limit=500", ""); w.Code != 400 {
		t.Fatalf("runs limit 500 = %d, want 400", w.Code)
	}

	if w = h.do("DELETE", "/v1/automations/"+id, ""); w.Code != 204 {
		t.Fatalf("delete = %d", w.Code)
	}
	for _, path := range []string{"/v1/automations/" + id, "/v1/automations/" + id + "/runs", "/v1/automations/" + id + "/notes", "/v1/automations/not-a-uuid"} {
		if w = h.do("GET", path, ""); w.Code != 404 {
			t.Fatalf("GET %s after delete = %d, want 404", path, w.Code)
		}
		assertErrorBody(t, w, "not_found", "not found")
	}
	if w = h.do("DELETE", "/v1/automations/"+id, ""); w.Code != 404 {
		t.Fatalf("second delete = %d, want 404", w.Code)
	}
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

func TestAutomationsAPINameConflictAndAgent(t *testing.T) {
	h := newAutomationHarness(t)
	h.create("Conflict", "")
	body := fmt.Sprintf(`{"name": %q, "agent_id": %q, "action": {"kind": "mission", "mission": {"goal": "g", "kind": "general"}}, "triggers": [{"kind": "manual"}]}`,
		strings.ToUpper(h.tag)+"CONFLICT", h.agentID)
	w := h.do("POST", "/v1/automations", body)
	if w.Code != 409 {
		t.Fatalf("case-insensitive duplicate = %d, want 409", w.Code)
	}
	assertErrorBody(t, w, "name_conflict", "already exists")

	body = fmt.Sprintf(`{"name": %q, "agent_id": "00000000-0000-4000-8000-000000000000", "action": {"kind": "mission", "mission": {"goal": "g", "kind": "general"}}, "triggers": [{"kind": "manual"}]}`, h.tag+"ghost")
	w = h.do("POST", "/v1/automations", body)
	if w.Code != 400 {
		t.Fatalf("unknown agent = %d, want 400", w.Code)
	}
	assertErrorBody(t, w, "bad_request", "unknown agent_id")
}

func TestAutomationsAPINotes(t *testing.T) {
	h := newAutomationHarness(t)
	id := h.create("notes", "")
	for i := range automations.MaxNotes {
		w := h.do("PUT", fmt.Sprintf("/v1/automations/%s/notes/n-%d", id, i), `{"content": "v1"}`)
		if w.Code != 201 {
			t.Fatalf("put note %d = %d %s", i, w.Code, w.Body.String())
		}
	}
	w := h.do("PUT", "/v1/automations/"+id+"/notes/n-10", `{"content": "v1"}`)
	if w.Code != 409 {
		t.Fatalf("11th note = %d, want 409", w.Code)
	}
	assertErrorBody(t, w, "note_limit", "at most 10")
	w = h.do("PUT", "/v1/automations/"+id+"/notes/n-0", `{"content": "v2"}`)
	var n automations.Note
	h.decode(w, &n)
	if w.Code != 200 || n.Content != "v2" {
		t.Fatalf("second put = %d %+v, want 200 v2", w.Code, n)
	}
	big, _ := json.Marshal(map[string]string{"content": strings.Repeat("a", 4097)})
	if w = h.do("PUT", "/v1/automations/"+id+"/notes/n-0", string(big)); w.Code != 400 {
		t.Fatalf("4097 bytes = %d, want 400", w.Code)
	}
	if w = h.do("PUT", "/v1/automations/"+id+"/notes/Bad%20Name", `{"content": "x"}`); w.Code != 400 {
		t.Fatalf("bad name = %d, want 400", w.Code)
	}
	if w = h.do("PUT", "/v1/automations/"+id+"/notes/n-0", `{}`); w.Code != 400 {
		t.Fatalf("missing content = %d, want 400", w.Code)
	}
	w = h.do("GET", "/v1/automations/"+id+"/notes/n-0", "")
	h.decode(w, &n)
	if w.Code != 200 || n.Content != "v2" {
		t.Fatalf("get note = %d %+v", w.Code, n)
	}
	var notes struct{ Notes []automations.Note }
	w = h.do("GET", "/v1/automations/"+id+"/notes", "")
	h.decode(w, &notes)
	if len(notes.Notes) != automations.MaxNotes {
		t.Fatalf("notes = %d, want %d", len(notes.Notes), automations.MaxNotes)
	}
	if w = h.do("DELETE", "/v1/automations/"+id+"/notes/n-0", ""); w.Code != 204 {
		t.Fatalf("delete note = %d", w.Code)
	}
	if w = h.do("GET", "/v1/automations/"+id+"/notes/n-0", ""); w.Code != 404 {
		t.Fatalf("deleted note = %d, want 404", w.Code)
	}
}

func TestAutomationsAPIRunNow(t *testing.T) {
	h := newAutomationHarness(t)
	id := h.create("run-now", "")
	w := h.do("POST", "/v1/automations/"+id+"/run", "")
	if w.Code != 202 {
		t.Fatalf("run now = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		EventID int64 `json:"event_id"`
	}
	h.decode(w, &out)
	db, _ := h.pool.Get()
	var source, kind, automationID string
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) OVER (), source, kind, payload->>'automation_id' FROM events
		WHERE source = 'manual' AND payload->>'automation_id' = $1`, id).Scan(&count, &source, &kind, &automationID); err != nil {
		t.Fatalf("query event: %v", err)
	}
	if count != 1 || source != "manual" || kind != "run.now" || automationID != id || out.EventID == 0 {
		t.Fatalf("events = %d %s %s %s (id %d), want exactly one manual run.now", count, source, kind, automationID, out.EventID)
	}
	if got := h.kicks.Load(); got != 1 {
		t.Fatalf("drainer kicks after run now = %d, want 1", got)
	}

	if w = h.do("PATCH", "/v1/automations/"+id, `{"enabled": false}`); w.Code != 200 {
		t.Fatalf("disable = %d", w.Code)
	}
	w = h.do("POST", "/v1/automations/"+id+"/run", "")
	if w.Code != 409 {
		t.Fatalf("run now disabled = %d, want 409", w.Code)
	}
	assertErrorBody(t, w, "automation_disabled", "disabled")

	expired := h.create("expired", `, "expires_at": "2020-01-01T00:00:00Z"`)
	if w = h.do("POST", "/v1/automations/"+expired+"/run", ""); w.Code != 409 {
		t.Fatalf("run now expired = %d, want 409", w.Code)
	}
	if w = h.do("POST", "/v1/automations/00000000-0000-4000-8000-000000000000/run", ""); w.Code != 404 {
		t.Fatalf("run now unknown = %d, want 404", w.Code)
	}
	_ = db.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE source = 'manual' AND payload->>'automation_id' = $1`, id).Scan(&count)
	if count != 1 {
		t.Fatalf("events after refused run-now = %d, want 1", count)
	}
	if got := h.kicks.Load(); got != 1 {
		t.Fatalf("drainer kicks after refused run-now = %d, want 1", got)
	}
}

func TestAutomationsAPIDestinationGuard(t *testing.T) {
	h := newAutomationHarness(t)
	db, _ := h.pool.Get()
	var destID string
	if err := db.QueryRow(t.Context(), `INSERT INTO destinations (name, kind) VALUES ($1, 'webhook') RETURNING id`, h.tag+"dest").Scan(&destID); err != nil {
		t.Fatalf("insert destination: %v", err)
	}
	body := fmt.Sprintf(`{"name": %q, "agent_id": %q, "action": {"kind": "mission", "mission": {"goal": "g", "kind": "general", "destination_ids": [%q]}}, "triggers": [{"kind": "manual"}]}`,
		h.tag+"delivers", h.agentID, destID)
	w := h.do("POST", "/v1/automations", body)
	if w.Code != 201 {
		t.Fatalf("create with destination = %d %s", w.Code, w.Body.String())
	}
	var out struct{ ID string }
	h.decode(w, &out)

	w = h.do("DELETE", "/v1/admin/destinations/"+destID, "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), h.tag+"delivers") {
		t.Fatalf("delete referenced destination = %d %s, want 409 naming the automation", w.Code, w.Body.String())
	}
	if w = h.do("PATCH", "/v1/automations/"+out.ID, `{"enabled": false}`); w.Code != 200 {
		t.Fatalf("disable = %d", w.Code)
	}
	if w = h.do("DELETE", "/v1/admin/destinations/"+destID, ""); w.Code != 204 {
		t.Fatalf("delete after disable = %d %s, want 204", w.Code, w.Body.String())
	}

	body = fmt.Sprintf(`{"name": %q, "agent_id": %q, "action": {"kind": "mission", "mission": {"goal": "g", "kind": "general", "destination_ids": [%q]}}, "triggers": [{"kind": "manual"}]}`,
		h.tag+"gone", h.agentID, destID)
	if w = h.do("POST", "/v1/automations", body); w.Code != 400 {
		t.Fatalf("create with deleted destination = %d, want 400", w.Code)
	}
}

// TestAutomationsAPIMissionFilterAndStats: ?automation_id= lists the
// missions an automation's runs started, and stats has 14 buckets.
func TestAutomationsAPIMissionFilterAndStats(t *testing.T) {
	h := newAutomationHarness(t)
	id := h.create("filter", "")
	other := h.create("filter-other", "")
	store := automations.NewStore(h.pool)
	mission := func(automationID, goal string) string {
		finished := time.Now()
		runID, _, err := store.CreateRun(t.Context(), nil, automations.Run{AutomationID: automationID, DedupKey: goal, Status: automations.RunDone, FinishedAt: &finished})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		mid, err := h.missions.Create(t.Context(), missions.Mission{Goal: h.tag + goal, Kind: missions.KindGeneral, Route: "default",
			OriginKind: missions.OriginAutomation, Unattended: true, AutomationRunID: runID})
		if err != nil {
			t.Fatalf("mission Create: %v", err)
		}
		return mid
	}
	mine := mission(id, "mine")
	mission(other, "theirs")

	w := h.do("GET", "/v1/missions?automation_id="+id, "")
	var list struct{ Missions []missions.Mission }
	h.decode(w, &list)
	if w.Code != 200 || len(list.Missions) != 1 || list.Missions[0].ID != mine || list.Missions[0].AutomationRunID == "" {
		t.Fatalf("filtered missions = %d %s", w.Code, w.Body.String())
	}

	w = h.do("GET", "/v1/automations/stats", "")
	var st automations.Stats
	h.decode(w, &st)
	if w.Code != 200 || len(st.Sparkline) != 14 || st.Total < 2 || st.Succeeded7d < 2 {
		t.Fatalf("stats = %d %+v", w.Code, st)
	}
	if today := time.Now().In(mustLoc(t, "Europe/Amsterdam")).Format(time.DateOnly); st.Sparkline[13].Day != today {
		t.Fatalf("last sparkline day = %s, want %s", st.Sparkline[13].Day, today)
	}

	w = h.do("GET", "/v1/automations/"+id, "")
	var v viewJSON
	h.decode(w, &v)
	if v.Stats.RunsTotal != 1 || v.Stats.Succeeded7d != 1 || v.Stats.LastRunStatus != "done" {
		t.Fatalf("automation stats = %+v", v.Stats)
	}
}
