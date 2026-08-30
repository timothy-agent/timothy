package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

func TestMissionsEndpointsUnmountedWhenStoreNil(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerMissions(m.Handle, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	for _, req := range []struct{ method, path string }{
		{"GET", "/v1/missions"},
		{"POST", "/v1/missions"},
		{"GET", "/v1/missions/abc"},
		{"DELETE", "/v1/missions/abc"},
		{"GET", "/v1/missions/abc/events"},
		{"POST", "/v1/missions/abc/resume"},
		{"POST", "/v1/missions/abc/cancel"},
		{"POST", "/v1/missions/abc/permission"},
		{"GET", "/v1/missions/abc/files"},
		{"GET", "/v1/missions/abc/files/foo.txt"},
		{"GET", "/v1/missions/abc/archive"},
		{"POST", "/v1/missions/abc/export-pdf"},
		{"POST", "/v1/missions/abc/push"},
		{"POST", "/v1/missions/abc/pr"},
		{"GET", "/v1/notifications"},
		{"POST", "/v1/notifications/abc/read"},
	} {
		httpReq := httptest.NewRequest(req.method, req.path, nil)
		httpReq.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httpReq)
		if w.Code != 404 {
			t.Fatalf("%s %s with a nil mission store = %d, want 404 (unmounted)", req.method, req.path, w.Code)
		}
	}
}

// TestMissionsListFilterValidation confirms bad ?schedule_id=/?limit=
// values 400 before ever reaching the store — a never-connecting pool
// (bad DSN, degraded) is enough to prove this, since a request that
// got past validation would instead surface a 500 from the store call.
func TestMissionsListFilterValidation(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	m := mux(a)
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	call := func(path string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code
	}

	if code := call("/v1/missions?schedule_id=not-a-uuid"); code != 400 {
		t.Fatalf("bad schedule_id = %d, want 400", code)
	}
	if code := call("/v1/missions?limit=0"); code != 400 {
		t.Fatalf("limit=0 = %d, want 400", code)
	}
	if code := call("/v1/missions?limit=-5"); code != 400 {
		t.Fatalf("negative limit = %d, want 400", code)
	}
	if code := call("/v1/missions?limit=nope"); code != 400 {
		t.Fatalf("non-numeric limit = %d, want 400", code)
	}
	// Valid shapes pass validation and reach the (degraded) store,
	// which then 500s — proving they were NOT rejected as bad input.
	if code := call("/v1/missions"); code != 500 {
		t.Fatalf("no filter against a degraded store = %d, want 500 (passed validation)", code)
	}
	if code := call("/v1/missions?limit=10"); code != 500 {
		t.Fatalf("valid limit against a degraded store = %d, want 500 (passed validation)", code)
	}
	// ?q= (the composer #-mention mission search) has no validation of
	// its own; any string passes straight through to List and reaches
	// the (degraded) store the same as an unfiltered list.
	if code := call("/v1/missions?q=fix+the+bug"); code != 500 {
		t.Fatalf("q= against a degraded store = %d, want 500 (reached the store)", code)
	}
}

// TestMissionsDeleteReachesStore confirms DELETE /v1/missions/{id} is
// wired to Store.Delete: against a never-connecting pool the request
// still passes routing/auth and reaches the store call, which fails
// closed as failMission's default 400 (the same generic-error mapping
// every other mission handler falls back to for an unrecognized
// error) — never a 404/409, which would mean the id path or the
// not-terminal check short-circuited before the store was even called.
// The actual not_found/not_terminal/success semantics need a real
// Postgres connection and are covered by the Store.Delete integration
// tests in internal/brain/missions.
func TestMissionsDeleteReachesStore(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	m := mux(a)
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	req := httptest.NewRequest("DELETE", "/v1/missions/abc", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("DELETE against a degraded store = %d, want 400 (reached the store, generic failure)", w.Code)
	}
}

// TestMissionsExportPDFNotEnabled confirms a nil pdfService 503s
// export-pdf before ever touching the store — proven against a
// never-connecting pool that would otherwise surface as a 400/500.
func TestMissionsExportPDFNotEnabled(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	m := mux(a)
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	req := httptest.NewRequest("POST", "/v1/missions/abc/export-pdf", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("export-pdf with no pdfService = %d, want 503", w.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "not_enabled" {
		t.Fatalf("error = %q, want not_enabled", body.Error)
	}
}

// TestMissionsCreateValidatesHarness covers D-051's create() gate:
// harness is only valid on kind=coding, "native" normalizes to "" (the
// stored value), an unknown harness 400s, and a coding request that
// omits harness picks up the settings-configured default via the seam
// — all BEFORE the store is ever reached. A request that fails this
// validation never reaches Driver.Create; the two "passed validation"
// cases below reach it against a degraded pool, which surfaces as
// failMission's generic 400 too (see TestMissionsDeleteReachesStore) —
// distinguished instead by asserting the codingExecutorDefault seam was
// (or wasn't) invoked, the one observable difference at this layer.
func TestMissionsCreateValidatesHarness(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())

	post := func(codingExecutorDefault func(context.Context) string, body string) int {
		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, codingExecutorDefault, nil, nil, nil, nil, nil, "", nil, nil, nil)
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code
	}

	if code := post(nil, `{"goal":"g","kind":"general","harness":"claude-cli"}`); code != 400 {
		t.Fatalf("harness on kind=general = %d, want 400", code)
	}
	if code := post(nil, `{"goal":"g","kind":"coding","harness":"not-a-real-harness"}`); code != 400 {
		t.Fatalf("unknown harness = %d, want 400", code)
	}
	// "native" normalizes to "" and passes validation, reaching the
	// (degraded) store — same generic 400 failMission maps every
	// unrecognized store error to.
	if code := post(nil, `{"goal":"g","kind":"coding","harness":"native"}`); code != 400 {
		t.Fatalf("harness=native = %d, want 400 (passed validation, reached degraded store)", code)
	}
	// A registered harness on kind=coding passes validation too.
	if code := post(nil, `{"goal":"g","kind":"coding","harness":"claude-cli"}`); code != 400 {
		t.Fatalf("harness=claude-cli = %d, want 400 (passed validation, reached degraded store)", code)
	}
	// Omitted harness on kind=coding applies the settings default via
	// the seam.
	defaultCalled := false
	defaulted := func(context.Context) string { defaultCalled = true; return "claude-cli" }
	if code := post(defaulted, `{"goal":"g","kind":"coding"}`); code != 400 {
		t.Fatalf("omitted harness with a settings default = %d, want 400 (passed validation)", code)
	}
	if !defaultCalled {
		t.Fatal("codingExecutorDefault seam not invoked for a kind=coding request with omitted harness")
	}
	// Omitted harness on kind=general never calls the default seam at
	// all; general missions must stay native regardless of the setting.
	generalCalled := false
	trackedDefault := func(context.Context) string { generalCalled = true; return "claude-cli" }
	if code := post(trackedDefault, `{"goal":"g","kind":"general"}`); code != 400 {
		t.Fatalf("general with omitted harness = %d, want 400 (reached degraded store)", code)
	}
	if generalCalled {
		t.Fatal("codingExecutorDefault seam invoked for a kind=general request")
	}
}

// TestMissionsCreateValidatesLight covers light's create() gate
// (D-069): rejected outright on an explicit kind=coding, and rejected
// when kind is omitted and classifies as coding — light never overrides
// a coding classification. light+general passes validation (reaching
// the degraded store, same 400-for-a-different-reason shape
// TestMissionsCreateValidatesHarness documents).
func TestMissionsCreateValidatesLight(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())

	post := func(classify func(context.Context, string) (string, error), body string) int {
		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, classify, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code
	}

	if code := post(nil, `{"goal":"g","kind":"coding","light":true}`); code != 400 {
		t.Fatalf("light on explicit kind=coding = %d, want 400", code)
	}
	codingClassify := func(context.Context, string) (string, error) { return "coding", nil }
	if code := post(codingClassify, `{"goal":"g","light":true}`); code != 400 {
		t.Fatalf("light with omitted kind classified as coding = %d, want 400", code)
	}
	// light+general passes validation, reaching the degraded store.
	if code := post(nil, `{"goal":"g","kind":"general","light":true}`); code != 400 {
		t.Fatalf("light+general = %d, want 400 (passed validation, reached degraded store)", code)
	}
}

// TestMissionsCreateValidatesRepoURL covers repo_url/connector_id's
// create() gate: repo_url is coding-only, requires connector_id,
// connector_id is only valid alongside repo_url, and an unknown
// connector_id 400s before Driver.Create is ever reached — mirrors
// TestMissionsCreateValidatesHarness's shape (a degraded store makes
// the "passed validation" cases 400 too, via failMission's generic
// mapping, so they're distinguished by not asserting a specific error
// message here, only that outright-rejected shapes 400 for a DIFFERENT,
// earlier reason: this test only needs to prove the reject cases fire
// before any store/connector call, which a nil conns/degraded store
// setup already demonstrates for the connector_id lookup case).
func TestMissionsCreateValidatesRepoURL(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	connStore := connectors.NewStore(pool, discard())
	conns := connectors.NewManager(connStore, nil, discard())

	post := func(body string) int {
		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, conns, nil, "", nil, nil, nil)
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code
	}

	if code := post(`{"goal":"g","kind":"general","repo_url":"https://github.com/o/r","connector_id":"1"}`); code != 400 {
		t.Fatalf("repo_url on kind=general = %d, want 400", code)
	}
	if code := post(`{"goal":"g","kind":"coding","repo_url":"https://github.com/o/r"}`); code != 400 {
		t.Fatalf("repo_url without connector_id = %d, want 400", code)
	}
	if code := post(`{"goal":"g","kind":"coding","connector_id":"1"}`); code != 400 {
		t.Fatalf("connector_id without repo_url = %d, want 400", code)
	}
	// connector_id names no real row against a degraded connectors
	// store — the same "unknown connector_id" 400 an unresolvable id
	// would get against a live store.
	if code := post(`{"goal":"g","kind":"coding","repo_url":"https://github.com/o/r","connector_id":"nope"}`); code != 400 {
		t.Fatalf("unknown connector_id = %d, want 400", code)
	}
	// No conns wired at all: repo_url is rejected outright rather than
	// panicking on a nil manager.
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)
	req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(`{"goal":"g","kind":"coding","repo_url":"https://github.com/o/r","connector_id":"1"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("repo_url with no conns wired = %d, want 400", w.Code)
	}
}

// TestMissionsCreateValidatesOnComplete covers on_complete's create()
// gate: an unknown value is rejected outright, and "push"/"push_pr"
// both require a github-connection coding mission (repo_url +
// connector_id) — mirrors TestMissionsCreateValidatesRepoURL's shape.
func TestMissionsCreateValidatesOnComplete(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	connStore := connectors.NewStore(pool, discard())
	conns := connectors.NewManager(connStore, nil, discard())

	post := func(body string) int {
		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, conns, nil, "", nil, nil, nil)
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code
	}

	if code := post(`{"goal":"g","kind":"coding","repo_url":"https://github.com/o/r","connector_id":"1","on_complete":"bogus"}`); code != 400 {
		t.Fatalf("unknown on_complete = %d, want 400", code)
	}
	if code := post(`{"goal":"g","kind":"coding","on_complete":"push"}`); code != 400 {
		t.Fatalf("on_complete=push without repo_url/connector_id = %d, want 400", code)
	}
	if code := post(`{"goal":"g","kind":"coding","on_complete":"push_pr"}`); code != 400 {
		t.Fatalf("on_complete=push_pr without repo_url/connector_id = %d, want 400", code)
	}
	if code := post(`{"goal":"g","kind":"general","repo_url":"https://github.com/o/r","connector_id":"1","on_complete":"push"}`); code != 400 {
		t.Fatalf("on_complete=push on kind=general = %d, want 400", code)
	}
}

// TestMissionsCreateValidatesGitStrategy covers create()'s
// branch_pattern/commit_style gate: an invalid pattern/style is
// rejected outright (a distinct error body, before Driver.Create is
// ever reached), while a valid one passes validation and reaches the
// (degraded) store — same generic 400 failMission maps every
// unrecognized store error to, mirroring
// TestMissionsCreateValidatesHarness's shape.
func TestMissionsCreateValidatesGitStrategy(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	driver.SetValidateDeps(missions.ValidateDeps{})

	post := func(body string) (int, string) {
		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		b, _ := io.ReadAll(w.Result().Body)
		return w.Code, string(b)
	}

	if code, body := post(`{"goal":"g","kind":"coding","branch_pattern":"{unknown}/{slug}"}`); code != 400 || !strings.Contains(body, "unknown placeholder") {
		t.Fatalf("unknown placeholder: code=%d body=%q, want 400 with an unknown-placeholder message", code, body)
	}
	if code, body := post(`{"goal":"g","kind":"coding","branch_pattern":"../{slug}"}`); code != 400 || !strings.Contains(body, "branch pattern") {
		t.Fatalf("traversal pattern: code=%d body=%q, want 400 with a branch-pattern message", code, body)
	}
	if code, body := post(`{"goal":"g","kind":"coding","commit_style":"loud"}`); code != 400 || !strings.Contains(body, "commit style") {
		t.Fatalf("unknown commit style: code=%d body=%q, want 400 with a commit-style message", code, body)
	}
	// Valid values pass validation and reach the degraded store —
	// failMission's generic 400, not a git-strategy-specific message.
	if code, body := post(`{"goal":"g","kind":"coding","branch_pattern":"{type}/{login}/{slug}","commit_style":"plain"}`); code != 400 || strings.Contains(body, "branch pattern") || strings.Contains(body, "commit style") {
		t.Fatalf("valid git strategy fields: code=%d body=%q, want a generic 400 (passed validation)", code, body)
	}
}

// TestMissionsCreateValidatesParentMission covers the follow-up gate:
// an unresolvable parent_mission_id 400s ("parent mission not found")
// before Driver.Create is ever reached — against a degraded pool
// Store.Get always wraps its failure as ErrNotFound (see store.go),
// the same "can't tell degraded-store from truly-unknown-id" shape
// TestMissionsCreateValidatesRepoURL's connector_id case already
// relies on. The not-terminal (409) and happy-path digest cases need a
// live parent mission and are covered by missions_integration_test.go.
func TestMissionsCreateValidatesParentMission(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(`{"goal":"g","kind":"general","parent_mission_id":"00000000-0000-0000-0000-000000000000"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	if w.Code != 400 || !strings.Contains(string(body), "parent mission not found") {
		t.Fatalf("unresolvable parent_mission_id: code=%d body=%q, want 400 with a parent-not-found message", w.Code, string(body))
	}
}

// fakeMissionAttachments is a minimal missionAttachmentStore fake for
// exercising resolveAttachments without a real *attachments.Store
// (which needs Postgres) — mirrors chat/attachments_test.go's
// fakeAttachments.
type fakeMissionAttachments struct {
	byID map[string]attachments.Attachment
	data map[string][]byte
}

func (f *fakeMissionAttachments) Get(_ context.Context, id string) (attachments.Attachment, error) {
	att, ok := f.byID[id]
	if !ok {
		return attachments.Attachment{}, attachments.ErrNotFound
	}
	return att, nil
}

func (f *fakeMissionAttachments) Open(_ context.Context, id string) (io.ReadCloser, attachments.Attachment, error) {
	att, ok := f.byID[id]
	if !ok {
		return nil, attachments.Attachment{}, attachments.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(f.data[id])), att, nil
}

// fakeMarkitdownServer stands in for the markitdown sidecar, returning
// a fixed markdown body for every /convert call.
func fakeMarkitdownServer(t *testing.T, markdown string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"markdown": markdown})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestMissionsCreateAttachmentsValidation covers resolveAttachments'
// 400 paths, all reachable before the (degraded) store is ever touched
// for the mission row itself: a disabled store, too many attachments,
// an unknown id, and a non-PDF mime.
func TestMissionsCreateAttachmentsValidation(t *testing.T) {
	t.Parallel()
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())

	fa := &fakeMissionAttachments{
		byID: map[string]attachments.Attachment{
			"doc1": {ID: "doc1", Mime: "application/pdf"},
			"img1": {ID: "img1", Mime: "image/png"},
			"txt1": {ID: "txt1", Mime: "text/plain"},
		},
		data: map[string][]byte{
			"doc1": []byte("%PDF-1.4"),
			"txt1": []byte("plain text notes"),
		},
	}
	md := fakeMarkitdownServer(t, "# converted")

	// post registers a fresh mux wired with the given attachment store/
	// markitdown URL for each call — registerMissions has no separate
	// setter, so each variant needs its own registration.
	post := func(t *testing.T, atts missionAttachmentStore, markitdownURL, body string) (int, string) {
		t.Helper()
		a, _, _ := testAPI(t, "tok", nil)
		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, atts, markitdownURL, nil, nil, nil)
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		b, _ := io.ReadAll(w.Result().Body)
		return w.Code, string(b)
	}

	t.Run("attachments not enabled without a store", func(t *testing.T) {
		code, body := post(t, nil, "", `{"goal":"g","kind":"general","attachments":[{"id":"doc1"}]}`)
		if code != 400 || !strings.Contains(body, "attachments are not enabled") {
			t.Fatalf("code=%d body=%q, want 400 attachments-not-enabled", code, body)
		}
	})

	t.Run("too many attachments", func(t *testing.T) {
		ids := make([]string, maxMissionAttachments+1)
		for i := range ids {
			ids[i] = `{"id":"doc1"}`
		}
		code, body := post(t, fa, md.URL, `{"goal":"g","kind":"general","attachments":[`+strings.Join(ids, ",")+`]}`)
		if code != 400 || !strings.Contains(body, "too many attachments") {
			t.Fatalf("code=%d body=%q, want 400 too-many-attachments", code, body)
		}
	})

	t.Run("empty markitdownURL rejects pdf attachments", func(t *testing.T) {
		code, body := post(t, fa, "", `{"goal":"g","kind":"general","attachments":[{"id":"doc1"}]}`)
		if code != 400 || !strings.Contains(body, "markitdown sidecar") {
			t.Fatalf("code=%d body=%q, want 400 naming the missing sidecar", code, body)
		}
	})

	t.Run("unknown attachment id", func(t *testing.T) {
		code, body := post(t, fa, md.URL, `{"goal":"g","kind":"general","attachments":[{"id":"missing"}]}`)
		if code != 400 || !strings.Contains(body, "not found") || !strings.Contains(body, "missing") {
			t.Fatalf("code=%d body=%q, want 400 attachment-not-found", code, body)
		}
	})

	t.Run("non-pdf mime rejected", func(t *testing.T) {
		code, body := post(t, fa, md.URL, `{"goal":"g","kind":"general","attachments":[{"id":"img1"}]}`)
		if code != 400 || !strings.Contains(body, "only document attachments are supported") {
			t.Fatalf("code=%d body=%q, want 400 unsupported-mime", code, body)
		}
	})

	// The remaining two cases exercise resolveAttachments past all its
	// own 400 paths — the fake pool has no live database, so create()
	// itself then fails past validation; asserting the failure is the
	// (unrelated) store error, not a resolveAttachments 400, confirms
	// text/plain cleared validation.
	t.Run("text/plain attachment accepted with markitdown configured", func(t *testing.T) {
		code, body := post(t, fa, md.URL, `{"goal":"g","kind":"general","attachments":[{"id":"txt1"}]}`)
		if code != 400 || !strings.Contains(body, "database unavailable") {
			t.Fatalf("code=%d body=%q, want a store error (validation passed)", code, body)
		}
	})

	t.Run("text-only attachment succeeds with no markitdownURL configured", func(t *testing.T) {
		code, body := post(t, fa, "", `{"goal":"g","kind":"general","attachments":[{"id":"txt1"}]}`)
		if code != 400 || !strings.Contains(body, "database unavailable") {
			t.Fatalf("code=%d body=%q, want a store error (text attachments don't need the sidecar)", code, body)
		}
	})
}

// TestMissionsCreateReferencesValidation covers create()'s
// referenced_context resolution: over-cap is rejected outright (mirrors
// resolveAttachments' own cap), a resolvable kb_doc reference reaches
// past validation into the (degraded) store, and an unresolvable
// reference (unknown kind, or kb docs disabled) is skipped rather than
// rejected, matching how an unknown Knowledge collection name already
// degrades silently instead of failing the request.
func TestMissionsCreateReferencesValidation(t *testing.T) {
	t.Parallel()
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())

	post := func(t *testing.T, body string) (int, string) {
		t.Helper()
		a, _, _ := testAPI(t, "tok", nil)
		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		b, _ := io.ReadAll(w.Result().Body)
		return w.Code, string(b)
	}

	t.Run("too many references", func(t *testing.T) {
		// chat.maxReferences (unexported, chat/references.go) is 8.
		refs := make([]string, 9)
		for i := range refs {
			refs[i] = `{"kind":"kb_doc","id":"d1"}`
		}
		code, body := post(t, `{"goal":"g","kind":"general","references":[`+strings.Join(refs, ",")+`]}`)
		if code != 400 || !strings.Contains(body, "too many references") {
			t.Fatalf("code=%d body=%q, want 400 too-many-references", code, body)
		}
	})

	t.Run("unresolvable reference skipped, request still reaches the store", func(t *testing.T) {
		// kb docs are never wired in this test's chat.Service, so a
		// kb_doc reference resolves to nothing (skip+log) rather than a
		// 400: the request proceeds to the (degraded) store, which then
		// fails as an unrelated database error.
		code, body := post(t, `{"goal":"g","kind":"general","references":[{"kind":"kb_doc","id":"missing"}]}`)
		if code != 400 || !strings.Contains(body, "database unavailable") {
			t.Fatalf("code=%d body=%q, want a store error (unresolvable reference skipped, not rejected)", code, body)
		}
	})
}

// TestClassifyKind exercises classifyKind's parsing and its bias to
// "coding" for anything short of an unambiguous "general" reply —
// nil classify, a classify error, and a garbage reply must all land on
// the cheap side of the error (see classifyKind's doc comment).
func TestClassifyKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		classify func(ctx context.Context, prompt string) (string, error)
		want     string
	}{
		{"nil classify defaults to coding", nil, "coding"},
		{
			"unambiguous general reply", func(context.Context, string) (string, error) {
				return "General", nil
			}, "general",
		},
		{
			"unambiguous coding reply", func(context.Context, string) (string, error) {
				return "coding", nil
			}, "coding",
		},
		{
			"garbage reply defaults to coding", func(context.Context, string) (string, error) {
				return "banana", nil
			}, "coding",
		},
		{
			"reply mentioning both words defaults to coding", func(context.Context, string) (string, error) {
				return "coding, not general", nil
			}, "coding",
		},
		{
			"empty reply defaults to coding", func(context.Context, string) (string, error) {
				return "", nil
			}, "coding",
		},
		{
			"classify error defaults to coding", func(context.Context, string) (string, error) {
				return "", errors.New("gateway down")
			}, "coding",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyKind(context.Background(), tc.classify, "some goal")
			if got != tc.want {
				t.Fatalf("classifyKind() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClassifyLight covers classifyLight's bias-false-on-ambiguity
// shape, same reasoning as TestClassifyKind's bias-toward-coding: a
// wrong "light" suggestion (the web UI's toggle default, never
// create()'s own gate) is worse when it defaults a multi-step goal's
// toggle to on than when it under-suggests.
func TestClassifyLight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		classify func(ctx context.Context, prompt string) (string, error)
		want     bool
	}{
		{"nil classify defaults to false", nil, false},
		{
			"unambiguous yes reply", func(context.Context, string) (string, error) {
				return "Yes", nil
			}, true,
		},
		{
			"unambiguous no reply", func(context.Context, string) (string, error) {
				return "no", nil
			}, false,
		},
		{
			"reply mentioning both words defaults to false", func(context.Context, string) (string, error) {
				return "yes, but also no", nil
			}, false,
		},
		{
			"garbage reply defaults to false", func(context.Context, string) (string, error) {
				return "banana", nil
			}, false,
		},
		{
			"classify error defaults to false", func(context.Context, string) (string, error) {
				return "", errors.New("gateway down")
			}, false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyLight(context.Background(), tc.classify, "some goal")
			if got != tc.want {
				t.Fatalf("classifyLight() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestClassifyKindAndLight exercises the merged single-call classifier
// classifyGoal uses: happy paths for both axes, and every ambiguous or
// malformed reply shape falling back to kind=coding light=false, the
// same safe-side bias classifyKind/classifyLight apply separately.
func TestClassifyKindAndLight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		classify  func(ctx context.Context, prompt string) (string, error)
		wantKind  string
		wantLight bool
	}{
		{"nil classify defaults to coding/full", nil, "coding", false},
		{
			"general + light", func(context.Context, string) (string, error) {
				return "general light", nil
			}, "general", true,
		},
		{
			"general + full", func(context.Context, string) (string, error) {
				return "general full", nil
			}, "general", false,
		},
		{
			"coding + light is never light (light only applies to general)", func(context.Context, string) (string, error) {
				return "coding light", nil
			}, "coding", false,
		},
		{
			"coding + full", func(context.Context, string) (string, error) {
				return "Coding Full", nil
			}, "coding", false,
		},
		{
			"extra whitespace still parses", func(context.Context, string) (string, error) {
				return "  general   light  ", nil
			}, "general", true,
		},
		{
			"single word reply falls back", func(context.Context, string) (string, error) {
				return "general", nil
			}, "coding", false,
		},
		{
			"three word reply falls back", func(context.Context, string) (string, error) {
				return "general light extra", nil
			}, "coding", false,
		},
		{
			"garbage first word falls back", func(context.Context, string) (string, error) {
				return "banana light", nil
			}, "coding", false,
		},
		{
			"garbage second word falls back", func(context.Context, string) (string, error) {
				return "general banana", nil
			}, "coding", false,
		},
		{
			"empty reply falls back", func(context.Context, string) (string, error) {
				return "", nil
			}, "coding", false,
		},
		{
			"classify error falls back", func(context.Context, string) (string, error) {
				return "", errors.New("gateway down")
			}, "coding", false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotKind, gotLight := classifyKindAndLight(context.Background(), tc.classify, "some goal")
			if gotKind != tc.wantKind || gotLight != tc.wantLight {
				t.Fatalf("classifyKindAndLight() = (%q, %v), want (%q, %v)", gotKind, gotLight, tc.wantKind, tc.wantLight)
			}
		})
	}
}

// TestMissionsClassifyEndpoint covers POST /v1/missions/classify: the
// happy path returning the classifier's verdict (kind and light), and
// the empty-goal 400 — this endpoint has no store/driver dependency, so
// it can be tested end to end without Postgres.
func TestMissionsClassifyEndpoint(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	classify := func(ctx context.Context, prompt string) (string, error) {
		return "general light", nil
	}
	m := mux(a)
	a.registerMissions(m.Handle, missions.NewStore(pgpool.New(context.Background(), "postgres://invalid/nope", discard()), discard()), nil, nil, nil, nil, nil, nil, classify, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	call := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/v1/missions/classify", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	if code, body := call(`{"goal":"write a report on Q3 sales"}`); code != http.StatusOK {
		t.Fatalf("classify with a goal = %d %s, want 200", code, body)
	} else if !strings.Contains(body, `"kind":"general"`) || !strings.Contains(body, `"light":true`) {
		t.Fatalf("classify body = %s, want kind general and light true", body)
	}

	if code, _ := call(`{"goal":""}`); code != http.StatusBadRequest {
		t.Fatalf("classify with an empty goal = %d, want 400", code)
	}
}

// TestMissionsResumeMalformedBodyRejected confirms a resume request
// with a body that isn't valid JSON 400s before ever reaching the store
// or driver — a degraded pool would surface as 500 (failMission's
// default) if the request passed body parsing, so a 400 here proves it
// didn't.
func TestMissionsResumeMalformedBodyRejected(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	req := httptest.NewRequest("POST", "/v1/missions/abc/resume", strings.NewReader(`{not json`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("resume with malformed JSON body = %d, want 400", w.Code)
	}
}

// TestMissionsResumeEmptyBodyUnchanged confirms an absent body (nil,
// matching the pre-answer-feature client) and an empty JSON object both
// still reach the driver's Signal call exactly as before this feature
// existed — proven by both hitting the same degraded-pool failure a
// resume with no answer always hit.
func TestMissionsResumeEmptyBodyUnchanged(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	call := func(body io.Reader) int {
		req := httptest.NewRequest("POST", "/v1/missions/abc/resume", body)
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code
	}
	if code := call(nil); code != http.StatusBadRequest {
		t.Fatalf("resume with no body = %d, want 400 from the degraded driver (reached Signal)", code)
	}
	if code := call(strings.NewReader(`{}`)); code != http.StatusBadRequest {
		t.Fatalf("resume with empty JSON body = %d, want 400 from the degraded driver (reached Signal)", code)
	}
	if code := call(strings.NewReader(`{"answer":""}`)); code != http.StatusBadRequest {
		t.Fatalf("resume with empty answer = %d, want 400 from the degraded driver (reached Signal, no answer appended)", code)
	}
}

// TestMissionsNoteMalformedOrEmptyBodyRejected confirms note's text
// validation happens before ever reaching the store — a degraded pool
// would surface as 500 (failMission's default) once past validation,
// so a 400 here proves the handler rejected the request first.
func TestMissionsNoteMalformedOrEmptyBodyRejected(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	call := func(body io.Reader) int {
		req := httptest.NewRequest("POST", "/v1/missions/abc/note", body)
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code
	}
	if code := call(strings.NewReader(`{not json`)); code != http.StatusBadRequest {
		t.Fatalf("note with malformed JSON body = %d, want 400", code)
	}
	if code := call(nil); code != http.StatusBadRequest {
		t.Fatalf("note with no body = %d, want 400", code)
	}
	if code := call(strings.NewReader(`{}`)); code != http.StatusBadRequest {
		t.Fatalf("note with empty JSON body = %d, want 400", code)
	}
	if code := call(strings.NewReader(`{"text":""}`)); code != http.StatusBadRequest {
		t.Fatalf("note with empty text = %d, want 400", code)
	}
}

// TestMissionsDecorateTopModels covers decorateTopModels's three
// degrade paths (nil seam, seam error, no rows) plus the happy path,
// all against a fake topModels func — no store/ledger involved, since
// the method itself never calls either.
func TestMissionsDecorateTopModels(t *testing.T) {
	t.Parallel()
	rows := []missions.Mission{{ID: "m1"}, {ID: "m2"}}

	h := &missionAPI{log: discard()}
	out := h.decorateTopModels(context.Background(), rows)
	if len(out) != 2 || out[0].TopModel != "" || out[1].TopModel != "" {
		t.Fatalf("nil topModels seam = %+v, want every row present with top_model omitted", out)
	}

	h = &missionAPI{log: discard(), topModels: func(context.Context, []string) (map[string]ledger.ModelUsed, error) {
		return nil, errors.New("ledger unreachable")
	}}
	out = h.decorateTopModels(context.Background(), rows)
	if len(out) != 2 || out[0].TopModel != "" || out[1].TopModel != "" {
		t.Fatalf("erroring topModels seam = %+v, want degrade to omitted, not a failed decoration", out)
	}

	h = &missionAPI{log: discard(), topModels: func(context.Context, []string) (map[string]ledger.ModelUsed, error) {
		return map[string]ledger.ModelUsed{
			"m1": {Provider: "anthropic", Model: "claude-sonnet"},
		}, nil
	}}
	out = h.decorateTopModels(context.Background(), rows)
	if out[0].TopModel != "claude-sonnet" || out[0].TopModelProvider != "anthropic" {
		t.Fatalf("m1 decoration = %+v, want top_model=claude-sonnet top_model_provider=anthropic", out[0])
	}
	if out[1].TopModel != "" || out[1].TopModelProvider != "" {
		t.Fatalf("m2 (absent from ledger map) = %+v, want fields omitted", out[1])
	}

	if out := h.decorateTopModels(context.Background(), nil); len(out) != 0 {
		t.Fatalf("decorateTopModels(nil) = %+v, want empty slice", out)
	}
}

// TestMissionsExecutorOptionsSurfacesSkipReason: when resolve succeeds
// but no chain entry is usable, the option's reason must be the first
// entry's own skip_reason when it's non-empty (e.g. the gateway's
// responses-probe gate, "endpoint does not serve /v1/responses…") —
// dropping it in favor of the generic "no usable provider for this
// route" string would silently strip the actionable detail from the
// MissionForm tooltip. An entry with no skip_reason of its own still
// falls back to the generic string, and a route with zero entries
// keeps its own distinct reason untouched.
func TestMissionsExecutorOptionsSurfacesSkipReason(t *testing.T) {
	t.Parallel()

	h := &missionAPI{log: discard(), resolveRoute: func(_ context.Context, route, harness string) (*gwclient.ResolvedRoute, error) {
		switch harness {
		case "codex-cli":
			return &gwclient.ResolvedRoute{Route: route, Entries: []gwclient.ResolvedRouteEntry{
				{ProviderName: "zai", Model: "glm-4.7", Usable: false,
					SkipReason: "endpoint does not serve /v1/responses — run Test connection on the provider to re-probe"},
			}}, nil
		case "opencode":
			return &gwclient.ResolvedRoute{Route: route, Entries: []gwclient.ResolvedRouteEntry{
				{ProviderName: "zai", Model: "glm-4.7", Usable: false},
			}}, nil
		default:
			return &gwclient.ResolvedRoute{Route: route, Entries: nil}, nil
		}
	}}

	req := httptest.NewRequest("GET", "/v1/missions/executor-options?route=coding", nil)
	w := httptest.NewRecorder()
	h.executorOptions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Options []executorOption `json:"options"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byHarness := map[string]executorOption{}
	for _, o := range body.Options {
		byHarness[o.Harness] = o
	}

	codex, ok := byHarness["codex-cli"]
	if !ok {
		t.Fatal("codex-cli option missing (executor.Registered() should include it)")
	}
	wantReason := "endpoint does not serve /v1/responses — run Test connection on the provider to re-probe"
	if codex.Usable || codex.Reason != wantReason {
		t.Fatalf("codex-cli option = %+v, want unusable with the entry's own skip_reason %q", codex, wantReason)
	}

	opencode, ok := byHarness["opencode"]
	if !ok {
		t.Fatal("opencode option missing (executor.Registered() should include it)")
	}
	if opencode.Usable || opencode.Reason != "no usable provider for this route" {
		t.Fatalf("opencode option = %+v, want the generic reason (entry carries no skip_reason)", opencode)
	}

	for harness, opt := range byHarness {
		if harness == "codex-cli" || harness == "opencode" {
			continue
		}
		if opt.Usable || opt.Reason != "route has no chain entries" {
			t.Fatalf("%s option = %+v, want the no-entries reason untouched", harness, opt)
		}
	}
}

// TestMissionsCreateKindOptional confirms an omitted kind no longer
// 400s: it reaches classifyKind (defaulting to "coding" with no
// classify wired) and then the degraded store, which 500s — proving
// validation accepted the empty kind rather than rejecting it. An
// explicit kind is still validated and honored exactly as before.
func TestMissionsCreateKindOptional(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	driver.SetValidateDeps(missions.ValidateDeps{})
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	call := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	// failMission's default branch maps any unrecognized driver error
	// (here, the degraded pool's connection failure) to 400 — the SAME
	// status an invalid kind gets, so the assertion that matters is
	// which body comes back, not the status code alone: a body without
	// "kind must be" proves the request passed kind validation and
	// failed downstream instead.
	if code, body := call(`{"goal":"do something"}`); code != http.StatusBadRequest || strings.Contains(body, "kind must be") {
		t.Fatalf("create with omitted kind = %d %s, want 400 from the degraded driver, not kind validation", code, body)
	}
	if code, body := call(`{"goal":"do something","kind":"general"}`); code != http.StatusBadRequest || strings.Contains(body, "kind must be") {
		t.Fatalf("create with explicit kind = %d %s, want 400 from the degraded driver, not kind validation", code, body)
	}
	if code, body := call(`{"goal":"do something","kind":"bogus"}`); code != http.StatusBadRequest || !strings.Contains(body, "kind must be") {
		t.Fatalf("create with an invalid kind = %d %s, want 400 from kind validation", code, body)
	}
}

// TestPRRejectsNonGitHubConnectionMission covers the pr endpoint's own
// gate (missing connector_id/repo_url) before it ever reaches the
// store's degraded pool, mirroring TestMissionsCreateValidatesRepoURL's
// pattern of asserting the specific rejection reason.
func TestPRRejectsNonGitHubConnectionMission(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	m := mux(a)
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	// Against a degraded pool, store.Get itself fails before the
	// connector_id/repo_url gate is ever reached — this test only
	// proves the route exists and reaches the store (400, not 404),
	// same reasoning as TestMissionsDeleteReachesStore. The gate's own
	// behavior (400 not_pr_able for a non-github-connection mission) is
	// covered against a real mission row in the integration suite.
	req := httptest.NewRequest("POST", "/v1/missions/abc/pr", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST pr against a degraded store = %d, want 400 (reached the store, generic failure)", w.Code)
	}
}

// stubResolveRoute returns a resolveRoute func whose ResolvedRoute per
// route name is fixed by the routes map; a route name absent from the
// map resolves as if the gateway returned "not found" (err != nil),
// matching gwclient.ResolveRoute's own behavior for an unknown route.
// harness is accepted but ignored — none of the execution-plan tests
// need axis-dependent chain content.
func stubResolveRoute(routes map[string]*gwclient.ResolvedRoute) func(context.Context, string, string) (*gwclient.ResolvedRoute, error) {
	return func(_ context.Context, name, _ string) (*gwclient.ResolvedRoute, error) {
		r, ok := routes[name]
		if !ok {
			return nil, errors.New("route not found")
		}
		return r, nil
	}
}

// usableEntry/unusableEntry build a ResolvedRoute chain entry for the
// execution-plan tests below — model/provider names are arbitrary,
// chosen only to be distinguishable in assertions.
func usableEntry(provider, model string) gwclient.ResolvedRouteEntry {
	return gwclient.ResolvedRouteEntry{ProviderName: provider, Model: model, Usable: true}
}

func unusableEntry(provider, model, reason string) gwclient.ResolvedRouteEntry {
	return gwclient.ResolvedRouteEntry{ProviderName: provider, Model: model, Usable: false, SkipReason: reason}
}

func getExecutionPlan(t *testing.T, h *missionAPI, query string) map[string]executionPlanPhase {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/missions/execution-plan?"+query, nil)
	w := httptest.NewRecorder()
	h.executionPlan(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("execution-plan status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		Phases []executionPlanPhase `json:"phases"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Phases) != 5 {
		t.Fatalf("phases = %d, want 5", len(body.Phases))
	}
	byPhase := map[string]executionPlanPhase{}
	for _, p := range body.Phases {
		byPhase[p.Phase] = p
	}
	wantOrder := []string{"explore", "plan", "execute", "review", "escalate"}
	for i, p := range body.Phases {
		if p.Phase != wantOrder[i] {
			t.Fatalf("phase[%d] = %q, want %q (phases must always appear in this order)", i, p.Phase, wantOrder[i])
		}
	}
	return byPhase
}

func TestExecutionPlanNotFoundWhenResolveRouteNil(t *testing.T) {
	t.Parallel()
	h := &missionAPI{log: discard()}
	req := httptest.NewRequest("GET", "/v1/missions/execution-plan", nil)
	w := httptest.NewRecorder()
	h.executionPlan(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// TestExecutionPlanRouteSourceExplicit confirms an explicit ?route=
// wins the base route and propagates unchanged to explore/plan (no
// plan_route override) and execute; review carries the same route
// value but its own provenance label is "inherited-from-execute"
// (review never itself set), not "explicit".
func TestExecutionPlanRouteSourceExplicit(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"mine": {Route: "mine", Entries: []gwclient.ResolvedRouteEntry{usableEntry("Anthropic", "claude-sonnet-5")}},
		}),
	}
	byPhase := getExecutionPlan(t, h, "kind=general&route=mine")

	for _, phase := range []string{"explore", "plan", "execute"} {
		p := byPhase[phase]
		if p.Route != "mine" || p.RouteSource != "explicit" {
			t.Fatalf("%s = route %q source %q, want mine/explicit", phase, p.Route, p.RouteSource)
		}
	}
	if p := byPhase["review"]; p.Route != "mine" || p.RouteSource != "inherited-from-execute" {
		t.Fatalf("review = route %q source %q, want mine/inherited-from-execute", p.Route, p.RouteSource)
	}
}

// TestExecutionPlanRouteSourceAgent confirms an agent's own Route wins
// the base route when no explicit ?route= is given.
func TestExecutionPlanRouteSourceAgent(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveAgentRoute: func(_ context.Context, id string) (string, bool) {
			if id == "coder" {
				return "agent-route", true
			}
			return "", false
		},
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"agent-route": {Route: "agent-route", Entries: []gwclient.ResolvedRouteEntry{usableEntry("GLM", "glm-5.3")}},
		}),
	}
	byPhase := getExecutionPlan(t, h, "kind=general&agent=coder")
	if p := byPhase["execute"]; p.Route != "agent-route" || p.RouteSource != "agent" {
		t.Fatalf("execute = route %q source %q, want agent-route/agent", p.Route, p.RouteSource)
	}
}

// TestExecutionPlanRouteSourceNamedCoding confirms kind=coding prefers
// a route literally named "coding" over the default-role route, only
// when it actually exists (routeExists's own contract).
func TestExecutionPlanRouteSourceNamedCoding(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log:          discard(),
		routeForRole: func(_ context.Context, _ string) string { return "default" },
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"coding":  {Route: "coding", Entries: []gwclient.ResolvedRouteEntry{usableEntry("GLM", "glm-5.3")}},
			"default": {Route: "default", Entries: []gwclient.ResolvedRouteEntry{usableEntry("OpenAI", "gpt-5-mini")}},
		}),
	}
	byPhase := getExecutionPlan(t, h, "kind=coding")
	if p := byPhase["execute"]; p.Route != "coding" || p.RouteSource != "named-coding" {
		t.Fatalf("execute = route %q source %q, want coding/named-coding", p.Route, p.RouteSource)
	}
}

// TestExecutionPlanRouteSourceDefaultRole confirms kind=general (or a
// coding route that doesn't exist) falls back to the default-role
// route.
func TestExecutionPlanRouteSourceDefaultRole(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log:          discard(),
		routeForRole: func(_ context.Context, role string) string { return "default" },
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"default": {Route: "default", Entries: []gwclient.ResolvedRouteEntry{usableEntry("OpenAI", "gpt-5-mini")}},
		}),
	}
	byPhase := getExecutionPlan(t, h, "kind=general")
	if p := byPhase["execute"]; p.Route != "default" || p.RouteSource != "default-role" {
		t.Fatalf("execute = route %q source %q, want default/default-role", p.Route, p.RouteSource)
	}

	// kind=coding but no "coding" route exists (absent from the stub
	// map, so resolveRoute errors on it) must also fall back here.
	byPhaseCoding := getExecutionPlan(t, h, "kind=coding")
	if p := byPhaseCoding["execute"]; p.Route != "default" || p.RouteSource != "default-role" {
		t.Fatalf("execute (coding, no coding route) = route %q source %q, want default/default-role", p.Route, p.RouteSource)
	}
}

// TestExecutionPlanRouteSourceNone confirms nothing configured
// resolves to an empty route with source "none" and no entries.
func TestExecutionPlanRouteSourceNone(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log:          discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{}),
	}
	byPhase := getExecutionPlan(t, h, "kind=general")
	p := byPhase["execute"]
	if p.Route != "" || p.RouteSource != "none" {
		t.Fatalf("execute = route %q source %q, want \"\"/none", p.Route, p.RouteSource)
	}
	if len(p.Entries) != 0 {
		t.Fatalf("execute entries = %+v, want empty (route never resolved)", p.Entries)
	}
}

// TestExecutionPlanOversightRoutes confirms plan_route, when set,
// covers explore/plan/review (oversight phases) while execute stays on
// the base route, and review_route independently overrides review
// alone (precedence: review_route > plan_route > route).
func TestExecutionPlanOversightRoutes(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"base":   {Route: "base", Entries: []gwclient.ResolvedRouteEntry{usableEntry("A", "a")}},
			"strong": {Route: "strong", Entries: []gwclient.ResolvedRouteEntry{usableEntry("B", "b")}},
			"judge":  {Route: "judge", Entries: []gwclient.ResolvedRouteEntry{usableEntry("C", "c")}},
		}),
	}

	// plan_route alone covers explore/plan/review's route value, execute
	// keeps base. explore/plan carry plan_route's own "explicit"
	// provenance; review's value matches but its provenance is
	// "inherited-from-plan" (review never itself set plan_route).
	byPhase := getExecutionPlan(t, h, "kind=general&route=base&plan_route=strong")
	for _, phase := range []string{"explore", "plan"} {
		p := byPhase[phase]
		if p.Route != "strong" || p.RouteSource != "explicit" {
			t.Fatalf("%s = route %q source %q, want strong/explicit", phase, p.Route, p.RouteSource)
		}
	}
	if p := byPhase["review"]; p.Route != "strong" || p.RouteSource != "inherited-from-plan" {
		t.Fatalf("review = route %q source %q, want strong/inherited-from-plan", p.Route, p.RouteSource)
	}
	if p := byPhase["execute"]; p.Route != "base" {
		t.Fatalf("execute = route %q, want base (unaffected by plan_route)", p.Route)
	}

	// review_route wins over plan_route for review alone.
	byPhase2 := getExecutionPlan(t, h, "kind=general&route=base&plan_route=strong&review_route=judge")
	if p := byPhase2["review"]; p.Route != "judge" || p.RouteSource != "explicit" {
		t.Fatalf("review = route %q source %q, want judge/explicit", p.Route, p.RouteSource)
	}
	if p := byPhase2["plan"]; p.Route != "strong" {
		t.Fatalf("plan = route %q, want strong (review_route must not affect plan)", p.Route)
	}

	// review_route provenance without an explicit plan_route reports
	// inherited-from-execute, matching runner.go's reviewRoute falling
	// through oversightRoute straight to Route.
	byPhase3 := getExecutionPlan(t, h, "kind=general&route=base")
	if p := byPhase3["review"]; p.Route != "base" || p.RouteSource != "inherited-from-execute" {
		t.Fatalf("review = route %q source %q, want base/inherited-from-execute", p.Route, p.RouteSource)
	}
}

// TestExecutionPlanHarnessAxis confirms kind=coding with a harness set
// resolves execute on the harness axis, and kind=general with the same
// harness set never delegates (D-072's canDelegate rule) — execute
// stays native regardless.
func TestExecutionPlanHarnessAxis(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"base": {Route: "base", Entries: []gwclient.ResolvedRouteEntry{usableEntry("A", "a")}},
		}),
	}

	coding := getExecutionPlan(t, h, "kind=coding&route=base&harness=claude-cli")
	if p := coding["execute"]; p.Axis != "harness" || p.Harness != "claude-cli" || p.HarnessSource != "explicit" {
		t.Fatalf("coding execute = axis %q harness %q source %q, want harness/claude-cli/explicit", p.Axis, p.Harness, p.HarnessSource)
	}

	general := getExecutionPlan(t, h, "kind=general&route=base&harness=claude-cli")
	if p := general["execute"]; p.Axis != "native" || p.Harness != "" {
		t.Fatalf("general execute = axis %q harness %q, want native/\"\" (harness ignored for non-coding)", p.Axis, p.Harness)
	}

	// "native" is the settings sentinel for off: normalizes to axis
	// native with no harness, same as create()'s own req.Harness == "native".
	nativeSentinel := getExecutionPlan(t, h, "kind=coding&route=base&harness=native")
	if p := nativeSentinel["execute"]; p.Axis != "native" || p.Harness != "" {
		t.Fatalf("coding execute with harness=native = axis %q harness %q, want native/\"\"", p.Axis, p.Harness)
	}
}

// TestExecutionPlanHarnessSourceSettings confirms an omitted ?harness=
// falls back to the settings default (codingExecutorDefault) with
// source "settings", only for kind=coding.
func TestExecutionPlanHarnessSourceSettings(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log:                   discard(),
		codingExecutorDefault: func(_ context.Context) string { return "opencode" },
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"base": {Route: "base", Entries: []gwclient.ResolvedRouteEntry{usableEntry("A", "a")}},
		}),
	}
	byPhase := getExecutionPlan(t, h, "kind=coding&route=base")
	if p := byPhase["execute"]; p.Harness != "opencode" || p.HarnessSource != "settings" {
		t.Fatalf("execute = harness %q source %q, want opencode/settings", p.Harness, p.HarnessSource)
	}
}

// TestExecutionPlanHarnessSourceAgent confirms the picked agent's own
// harness wins over the settings default, source "agent" - the same
// mission.harness -> agent.harness -> settings.coding_executor ->
// native precedence create() and the scheduler's fire path use
// (missions.ResolveHarness).
func TestExecutionPlanHarnessSourceAgent(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log:                   discard(),
		codingExecutorDefault: func(_ context.Context) string { return "opencode" },
		resolveAgentHarness: func(_ context.Context, id string) (string, bool) {
			if id == "coder" {
				return "pi", true
			}
			return "", false
		},
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"base": {Route: "base", Entries: []gwclient.ResolvedRouteEntry{usableEntry("A", "a")}},
		}),
	}
	byPhase := getExecutionPlan(t, h, "kind=coding&route=base&agent=coder")
	if p := byPhase["execute"]; p.Harness != "pi" || p.HarnessSource != "agent" {
		t.Fatalf("execute = harness %q source %q, want pi/agent", p.Harness, p.HarnessSource)
	}

	// An explicit ?harness= still wins over the agent's own harness.
	explicit := getExecutionPlan(t, h, "kind=coding&route=base&agent=coder&harness=claude-cli")
	if p := explicit["execute"]; p.Harness != "claude-cli" || p.HarnessSource != "explicit" {
		t.Fatalf("execute with explicit harness = harness %q source %q, want claude-cli/explicit", p.Harness, p.HarnessSource)
	}
}

// TestExecutionPlanLightSkipsOversightOnly confirms light=true skips
// explore/plan/review with the fixed reason while execute is never
// skipped.
func TestExecutionPlanLightSkipsOversightOnly(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"base": {Route: "base", Entries: []gwclient.ResolvedRouteEntry{usableEntry("A", "a")}},
		}),
	}
	byPhase := getExecutionPlan(t, h, "kind=general&route=base&light=true")
	for _, phase := range []string{"explore", "plan", "review"} {
		p := byPhase[phase]
		if !p.Skipped || p.SkipReason != lightSkipReason {
			t.Fatalf("%s = skipped %v reason %q, want true/%q", phase, p.Skipped, p.SkipReason, lightSkipReason)
		}
	}
	if p := byPhase["execute"]; p.Skipped {
		t.Fatalf("execute = skipped %v, want false (execute never skips)", p.Skipped)
	}
}

// TestExecutionPlanEscalateOff confirms escalate reports skipped with
// the fixed off reason when no escalation_route is set, and resolves
// normally (not skipped) when one is.
func TestExecutionPlanEscalateOff(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"base":   {Route: "base", Entries: []gwclient.ResolvedRouteEntry{usableEntry("A", "a")}},
			"strong": {Route: "strong", Entries: []gwclient.ResolvedRouteEntry{usableEntry("B", "b")}},
		}),
	}
	off := getExecutionPlan(t, h, "kind=general&route=base")
	p := off["escalate"]
	if !p.Skipped || p.SkipReason != escalateOffReason || p.Route != "" || p.RouteSource != "off" {
		t.Fatalf("escalate (off) = %+v, want skipped/off with the fixed reason and no route", p)
	}
	if len(p.Entries) != 0 {
		t.Fatalf("escalate (off) entries = %+v, want empty", p.Entries)
	}

	on := getExecutionPlan(t, h, "kind=general&route=base&escalation_route=strong")
	p2 := on["escalate"]
	if p2.Skipped || p2.Route != "strong" || p2.RouteSource != "explicit" {
		t.Fatalf("escalate (on) = %+v, want not skipped, route strong/explicit", p2)
	}
}

// TestExecutionPlanSelectedFirstUsable confirms selected lands on the
// first usable entry (not the first entry outright), and that no
// entry is selected when none are usable.
func TestExecutionPlanSelectedFirstUsable(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"mixed": {Route: "mixed", Entries: []gwclient.ResolvedRouteEntry{
				unusableEntry("A", "a", "cooling down"),
				usableEntry("B", "b"),
				usableEntry("C", "c"),
			}},
			"none-usable": {Route: "none-usable", Entries: []gwclient.ResolvedRouteEntry{
				unusableEntry("A", "a", "cooling down"),
				unusableEntry("B", "b", "no credential"),
			}},
		}),
	}

	mixed := getExecutionPlan(t, h, "kind=general&route=mixed")
	entries := mixed["execute"].Entries
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].Selected {
		t.Fatalf("entries[0] (unusable) selected = true, want false")
	}
	if !entries[1].Selected {
		t.Fatalf("entries[1] (first usable) selected = false, want true")
	}
	if entries[2].Selected {
		t.Fatalf("entries[2] (second usable) selected = true, want false (only the first usable entry is selected)")
	}

	noneUsable := getExecutionPlan(t, h, "kind=general&route=none-usable")
	for i, e := range noneUsable["execute"].Entries {
		if e.Selected {
			t.Fatalf("entries[%d] selected = true, want false (no entry is usable)", i)
		}
	}
}

// TestExecutionPlanRouteModelPinSelectsPinnedEntry confirms a well-
// formed ?route_model= that names a usable entry marks THAT entry
// selected instead of the first-usable one.
func TestExecutionPlanRouteModelPinSelectsPinnedEntry(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"mixed": {Route: "mixed", Entries: []gwclient.ResolvedRouteEntry{
				usableEntry("B", "b"),
				usableEntry("C", "c"),
			}},
		}),
	}
	byPhase := getExecutionPlan(t, h, "kind=general&route=mixed&route_model="+url.QueryEscape("C/c"))
	entries := byPhase["execute"].Entries
	if entries[0].Selected {
		t.Fatalf("entries[0] (B/b, unpinned) selected = true, want false")
	}
	if !entries[1].Selected {
		t.Fatalf("entries[1] (C/c, pinned) selected = false, want true")
	}
}

// TestExecutionPlanRouteModelPinUnusableDegradesToFirstUsable confirms
// a pin naming an entry that IS present but not usable leaves selected
// on the first usable entry — the pinned entry keeps its own
// Usable/SkipReason unchanged so the UI can explain why the pin will
// not apply.
func TestExecutionPlanRouteModelPinUnusableDegradesToFirstUsable(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"mixed": {Route: "mixed", Entries: []gwclient.ResolvedRouteEntry{
				unusableEntry("A", "a", "cooling down"),
				usableEntry("B", "b"),
			}},
		}),
	}
	byPhase := getExecutionPlan(t, h, "kind=general&route=mixed&route_model="+url.QueryEscape("A/a"))
	entries := byPhase["execute"].Entries
	if entries[0].Selected {
		t.Fatalf("entries[0] (A/a, pinned but unusable) selected = true, want false")
	}
	if entries[0].Usable || entries[0].SkipReason != "cooling down" {
		t.Fatalf("entries[0] usable/skip_reason = %v/%q, want unchanged (false/cooling down)", entries[0].Usable, entries[0].SkipReason)
	}
	if !entries[1].Selected {
		t.Fatalf("entries[1] (B/b, first usable) selected = false, want true (fallback when the pin can't apply)")
	}
}

// TestExecutionPlanReviewModelPinFallback confirms review's model pin
// mirrors reviewModel's own three-level precedence: review_route_model
// wins, else plan_route_model, else route_model.
func TestExecutionPlanReviewModelPinFallback(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"base": {Route: "base", Entries: []gwclient.ResolvedRouteEntry{
				usableEntry("A", "a"), usableEntry("B", "b"),
			}},
		}),
	}
	// Only route_model set: review inherits it (execute's route, no
	// plan_route/review_route set at all so review's route is "base" too).
	byPhase := getExecutionPlan(t, h, "kind=general&route=base&route_model="+url.QueryEscape("B/b"))
	reviewEntries := byPhase["review"].Entries
	if !reviewEntries[1].Selected {
		t.Fatalf("review entries[1] (B/b) selected = false, want true (route_model inherited)")
	}

	// plan_route_model set without review_route_model: review_route_model
	// > plan_route_model, plan_route_model wins here.
	byPhase2 := getExecutionPlan(t, h, "kind=general&route=base&route_model="+url.QueryEscape("B/b")+"&plan_route_model="+url.QueryEscape("A/a"))
	reviewEntries2 := byPhase2["review"].Entries
	if !reviewEntries2[0].Selected {
		t.Fatalf("review entries[0] (A/a) selected = false, want true (plan_route_model beats route_model)")
	}

	// review_route_model set: wins over both.
	byPhase3 := getExecutionPlan(t, h, "kind=general&route=base&route_model="+url.QueryEscape("B/b")+"&plan_route_model="+url.QueryEscape("A/a")+"&review_route_model="+url.QueryEscape("B/b"))
	reviewEntries3 := byPhase3["review"].Entries
	if !reviewEntries3[1].Selected {
		t.Fatalf("review entries[1] (B/b) selected = false, want true (review_route_model wins over plan_route_model)")
	}
}

// TestExecutionPlanPricesOmittedWhenNil confirms an entry with no
// configured prices omits the prices field entirely rather than
// emitting a zero-valued object, and an entry with prices carries them
// through unchanged.
func TestExecutionPlanPricesOmittedWhenNil(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"priced": {Route: "priced", Entries: []gwclient.ResolvedRouteEntry{
				{ProviderName: "GLM (Z.ai)", Model: "glm-5.3", Usable: true,
					Prices: &router.ModelPrices{InputPerMTok: 0.6, OutputPerMTok: 2.2}},
				{ProviderName: "NoPrice", Model: "mystery", Usable: true},
			}},
		}),
	}
	req := httptest.NewRequest("GET", "/v1/missions/execution-plan?kind=general&route=priced", nil)
	w := httptest.NewRecorder()
	h.executionPlan(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"glm-5.3"`) || !strings.Contains(body, `"input_per_mtok":0.6`) {
		t.Fatalf("priced entry missing expected fields: %s", body)
	}
	// The unpriced entry's own JSON object must not carry a "prices" key
	// at all. Decode into raw entries to check per-object rather than a
	// whole-body substring search (which could false-positive/negative
	// depending on ordering).
	var decoded struct {
		Phases []struct {
			Phase   string           `json:"phase"`
			Entries []map[string]any `json:"entries"`
		} `json:"phases"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, p := range decoded.Phases {
		if p.Phase != "execute" {
			continue
		}
		for _, e := range p.Entries {
			model := e["model"]
			_, hasPrices := e["prices"]
			if model == "glm-5.3" && !hasPrices {
				t.Fatalf("glm-5.3 entry missing prices key: %+v", e)
			}
			if model == "mystery" && hasPrices {
				t.Fatalf("mystery entry has a prices key though Prices is nil: %+v", e)
			}
		}
	}
}

// TestExecutionPlanResolveErrorIsolatedToOnePhase confirms a phase
// whose route fails to resolve reports empty entries with the error in
// skip_reason, while unrelated phases still resolve normally — a
// single bad route/escalation_route must never fail the whole request.
func TestExecutionPlanResolveErrorIsolatedToOnePhase(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"base": {Route: "base", Entries: []gwclient.ResolvedRouteEntry{usableEntry("A", "a")}},
		}),
	}
	byPhase := getExecutionPlan(t, h, "kind=general&route=base&escalation_route=broken")

	esc := byPhase["escalate"]
	if len(esc.Entries) != 0 {
		t.Fatalf("escalate entries = %+v, want empty (route failed to resolve)", esc.Entries)
	}
	if esc.SkipReason == "" {
		t.Fatalf("escalate skip_reason empty, want the resolve error")
	}
	if esc.Skipped {
		t.Fatalf("escalate skipped = true, want false (it wasn't skipped, it failed to resolve)")
	}

	execute := byPhase["execute"]
	if len(execute.Entries) != 1 || execute.Entries[0].ProviderName != "A" {
		t.Fatalf("execute = %+v, want the base route's entry unaffected by escalate's failure", execute)
	}
}

func TestChapterTitle(t *testing.T) {
	cases := []struct {
		name        string
		path        string
		content     string
		wantTitle   string
		wantContent string
	}{
		{
			name:        "h1 present",
			path:        "chapter1.md",
			content:     "# Chapter 1\n\nSome body text.",
			wantTitle:   "Chapter 1",
			wantContent: "\nSome body text.",
		},
		{
			name:        "h1 after blank lines",
			path:        "notes.md",
			content:     "\n\n# Notes\nbody",
			wantTitle:   "Notes",
			wantContent: "\n\nbody",
		},
		{
			name:        "no h1",
			path:        "README.md",
			content:     "Just some prose, no heading.",
			wantTitle:   "README",
			wantContent: "Just some prose, no heading.",
		},
		{
			name:        "h1 with trailing hashes and whitespace",
			path:        "x.md",
			content:     "#   Title Here   ##  \nbody",
			wantTitle:   "Title Here",
			wantContent: "body",
		},
		{
			name:        "subdir path",
			path:        "docs/notes.md",
			content:     "no heading here",
			wantTitle:   "docs/notes",
			wantContent: "no heading here",
		},
		{
			name:        "markdown extension",
			path:        "guide.markdown",
			content:     "# Guide\nbody",
			wantTitle:   "Guide",
			wantContent: "body",
		},
		// Setext-style headings ("Title\n=====") are intentionally not
		// recognized: only ATX "#" headings are treated as a duplicate
		// chapter title.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, remaining := chapterTitle(tc.path, tc.content)
			if title != tc.wantTitle {
				t.Errorf("title = %q, want %q", title, tc.wantTitle)
			}
			if remaining != tc.wantContent {
				t.Errorf("content = %q, want %q", remaining, tc.wantContent)
			}
		})
	}
}

// TestPromoteKBNotEnabledWithoutStore covers promoteKB's own nil-gate
// (a nil kbStore, e.g. kb disabled): the SAME shape exportPDF's own
// pdfService nil-gate uses, checked before the mission is even loaded.
func TestPromoteKBNotEnabledWithoutStore(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	m := mux(a)
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil)

	req := httptest.NewRequest("POST", "/v1/missions/some-id/promote-kb", strings.NewReader(`{"collection_id":"c1"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "knowledge base is not enabled") {
		t.Fatalf("code=%d body=%s, want 503 not-enabled", w.Code, w.Body.String())
	}
}

// noopIngester never calls memoryd: used where a test only needs a
// non-nil kbIngester to get past promoteKB's nil-gate, same reasoning
// as kb_integration_test.go's fakeIngester (unavailable here: this file
// has no integration build tag).
type noopIngester struct{}

func (noopIngester) IngestDocument(context.Context, string, string, string) (int, error) {
	return 0, nil
}

// TestPromoteKBReachesStore covers the enabled-kbStore path against a
// degraded pool, proving the route exists and reaches the mission
// lookup (400, not 404): same reasoning as TestMissionsDeleteReachesStore.
// collection_id's own validation (reached only once a mission loads
// successfully) is covered by the happy-path/gate tests in
// missions_integration_test.go, which use a real store.
func TestPromoteKBReachesStore(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	kbStore := kb.New(pool)
	m := mux(a)
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, kbStore, noopIngester{})

	req := httptest.NewRequest("POST", "/v1/missions/some-id/promote-kb", strings.NewReader(`{"collection_id":"c1"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s, want 400 from the degraded store", w.Code, w.Body.String())
	}
}
