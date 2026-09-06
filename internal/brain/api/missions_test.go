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
	a.registerMissions(m.Handle, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

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
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, codingExecutorDefault, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)
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
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, classify, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)
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

// TestMissionsCreateFlowNormalization covers create()'s flow/light
// mapping (D-090, issue #459): an omitted flow maps to light's value
// exactly as before this field existed, flow=light always implies
// light=true so every existing D-069 code path keeps keying off the
// light column, and an invalid flow/kind/light combination is
// rejected by ValidateCreate (wired via SetValidateDeps here so a
// validation rejection is distinguishable from the generic degraded-
// store 400, same pattern as TestMissionsCreateValidatesGitStrategy).
func TestMissionsCreateFlowNormalization(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	driver.SetValidateDeps(missions.ValidateDeps{})

	post := func(body string) (int, string) {
		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		b, _ := io.ReadAll(w.Result().Body)
		return w.Code, string(b)
	}

	// Omitted flow, light omitted: normalizes to "full", passes
	// validation, reaches the degraded store (generic 400, no "flow"
	// mention).
	if code, body := post(`{"goal":"g","kind":"general"}`); code != 400 || strings.Contains(body, "flow") {
		t.Fatalf("omitted flow/light: code=%d body=%q, want a generic 400 (passed validation)", code, body)
	}
	// Omitted flow, light=true: normalizes to flow="light", satisfying
	// its own light=true requirement, passes validation.
	if code, body := post(`{"goal":"g","kind":"general","light":true}`); code != 400 || strings.Contains(body, "flow") {
		t.Fatalf("omitted flow, light=true: code=%d body=%q, want a generic 400 (passed validation)", code, body)
	}
	// Explicit flow=light with light omitted: normalization sets
	// light=true to match, passes validation.
	if code, body := post(`{"goal":"g","kind":"general","flow":"light"}`); code != 400 || strings.Contains(body, "flow") {
		t.Fatalf("flow=light, light omitted: code=%d body=%q, want a generic 400 (passed validation)", code, body)
	}
	// Explicit flow=no_prove / discover_generate on kind=general both
	// pass validation.
	if code, body := post(`{"goal":"g","kind":"general","flow":"no_prove"}`); code != 400 || strings.Contains(body, "flow") {
		t.Fatalf("flow=no_prove on general: code=%d body=%q, want a generic 400 (passed validation)", code, body)
	}
	if code, body := post(`{"goal":"g","kind":"general","flow":"discover_generate"}`); code != 400 || strings.Contains(body, "flow") {
		t.Fatalf("flow=discover_generate on general: code=%d body=%q, want a generic 400 (passed validation)", code, body)
	}
	// Unknown flow value is rejected by ValidateCreate before Driver.Create
	// ever touches the degraded store.
	if code, body := post(`{"goal":"g","kind":"general","flow":"bogus"}`); code != 400 || !strings.Contains(body, "unknown flow") {
		t.Fatalf("flow=bogus: code=%d body=%q, want 400 with an unknown-flow message", code, body)
	}
	// kind=coding requesting a non-full flow is rejected: the flow
	// column must stay "full" for coding missions.
	if code, body := post(`{"goal":"g","kind":"coding","flow":"no_prove"}`); code != 400 || !strings.Contains(body, "flow must be") {
		t.Fatalf("flow=no_prove on coding: code=%d body=%q, want 400 with a flow-must-be-full message", code, body)
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
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, conns, nil, "", nil, nil, nil, nil, "", nil)
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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)
	req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(`{"goal":"g","kind":"coding","repo_url":"https://github.com/o/r","connector_id":"1"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("repo_url with no conns wired = %d, want 400", w.Code)
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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

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

// fakeWhisperServer stands in for the whisper sidecar, returning a
// fixed transcript for every /transcribe call.
func fakeWhisperServer(t *testing.T, transcript string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": transcript})
	}))
	t.Cleanup(srv.Close)
	return srv
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
			"vid1": {ID: "vid1", Mime: "video/mp4"},
			"txt1": {ID: "txt1", Mime: "text/plain"},
			"img1": {ID: "img1", Mime: "image/png"},
			"aud1": {ID: "aud1", Mime: "audio/mpeg"},
		},
		data: map[string][]byte{
			"doc1": []byte("%PDF-1.4"),
			"txt1": []byte("plain text notes"),
			"img1": []byte("fake image bytes"),
			"aud1": []byte("fake audio bytes"),
		},
	}
	md := fakeMarkitdownServer(t, "# converted")

	// post registers a fresh mux wired with the given attachment store,
	// markitdown URL, whisper URL, and caption func for each call:
	// registerMissions has no separate setter, so each variant needs its
	// own registration.
	post := func(t *testing.T, atts missionAttachmentStore, markitdownURL, whisperURL string, caption func(context.Context, string, []byte) string, body string) (int, string) {
		t.Helper()
		a, _, _ := testAPI(t, "tok", nil)
		m := mux(a)
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, atts, markitdownURL, nil, nil, nil, nil, whisperURL, caption)
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		b, _ := io.ReadAll(w.Result().Body)
		return w.Code, string(b)
	}

	t.Run("attachments not enabled without a store", func(t *testing.T) {
		code, body := post(t, nil, "", "", nil, `{"goal":"g","kind":"general","attachments":[{"id":"doc1"}]}`)
		if code != 400 || !strings.Contains(body, "attachments are not enabled") {
			t.Fatalf("code=%d body=%q, want 400 attachments-not-enabled", code, body)
		}
	})

	t.Run("too many attachments", func(t *testing.T) {
		ids := make([]string, maxMissionAttachments+1)
		for i := range ids {
			ids[i] = `{"id":"doc1"}`
		}
		code, body := post(t, fa, md.URL, "", nil, `{"goal":"g","kind":"general","attachments":[`+strings.Join(ids, ",")+`]}`)
		if code != 400 || !strings.Contains(body, "too many attachments") {
			t.Fatalf("code=%d body=%q, want 400 too-many-attachments", code, body)
		}
	})

	t.Run("empty markitdownURL rejects pdf attachments", func(t *testing.T) {
		code, body := post(t, fa, "", "", nil, `{"goal":"g","kind":"general","attachments":[{"id":"doc1"}]}`)
		if code != 400 || !strings.Contains(body, "markitdown sidecar") {
			t.Fatalf("code=%d body=%q, want 400 naming the missing sidecar", code, body)
		}
	})

	t.Run("unknown attachment id", func(t *testing.T) {
		code, body := post(t, fa, md.URL, "", nil, `{"goal":"g","kind":"general","attachments":[{"id":"missing"}]}`)
		if code != 400 || !strings.Contains(body, "not found") || !strings.Contains(body, "missing") {
			t.Fatalf("code=%d body=%q, want 400 attachment-not-found", code, body)
		}
	})

	t.Run("unsupported mime rejected", func(t *testing.T) {
		code, body := post(t, fa, md.URL, "", nil, `{"goal":"g","kind":"general","attachments":[{"id":"vid1"}]}`)
		if code != 400 || !strings.Contains(body, "unsupported attachment type") {
			t.Fatalf("code=%d body=%q, want 400 unsupported-mime", code, body)
		}
	})

	t.Run("image without a caption route is rejected", func(t *testing.T) {
		code, body := post(t, fa, md.URL, "", nil, `{"goal":"g","kind":"general","attachments":[{"id":"img1"}]}`)
		if code != 400 || !strings.Contains(body, "could not be described") {
			t.Fatalf("code=%d body=%q, want 400 image-not-described", code, body)
		}
	})

	t.Run("audio without whisper configured is rejected", func(t *testing.T) {
		code, body := post(t, fa, md.URL, "", nil, `{"goal":"g","kind":"general","attachments":[{"id":"aud1"}]}`)
		if code != 400 || !strings.Contains(body, "whisper sidecar") {
			t.Fatalf("code=%d body=%q, want 400 naming the missing whisper sidecar", code, body)
		}
	})

	// The remaining cases exercise the resolver past all its own 400
	// paths, so the fake pool has no live database, and create() itself
	// then fails past validation; asserting the failure is the
	// (unrelated) store error confirms the attachment cleared
	// validation.
	t.Run("text/plain attachment accepted with markitdown configured", func(t *testing.T) {
		code, body := post(t, fa, md.URL, "", nil, `{"goal":"g","kind":"general","attachments":[{"id":"txt1"}]}`)
		if code != 400 || !strings.Contains(body, "database unavailable") {
			t.Fatalf("code=%d body=%q, want a store error (validation passed)", code, body)
		}
	})

	t.Run("text-only attachment succeeds with no markitdownURL configured", func(t *testing.T) {
		code, body := post(t, fa, "", "", nil, `{"goal":"g","kind":"general","attachments":[{"id":"txt1"}]}`)
		if code != 400 || !strings.Contains(body, "database unavailable") {
			t.Fatalf("code=%d body=%q, want a store error (text attachments don't need the sidecar)", code, body)
		}
	})

	t.Run("image attachment captioned succeeds", func(t *testing.T) {
		caption := func(context.Context, string, []byte) string { return "a description" }
		code, body := post(t, fa, md.URL, "", caption, `{"goal":"g","kind":"general","attachments":[{"id":"img1"}]}`)
		if code != 400 || !strings.Contains(body, "database unavailable") {
			t.Fatalf("code=%d body=%q, want a store error (validation passed)", code, body)
		}
	})

	t.Run("audio attachment transcribed succeeds", func(t *testing.T) {
		wh := fakeWhisperServer(t, "hello world")
		code, body := post(t, fa, md.URL, wh.URL, nil, `{"goal":"g","kind":"general","attachments":[{"id":"aud1"}]}`)
		if code != 400 || !strings.Contains(body, "database unavailable") {
			t.Fatalf("code=%d body=%q, want a store error (validation passed)", code, body)
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
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)
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

// TestClassifyKind exercises classifyKind's deliverable-based parsing:
// the first recognised word in the reply wins, and nil classify, a
// classify error, or an unrecognised reply all fall back to "general"
// (see classifyKind's doc comment).
func TestClassifyKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		classify func(ctx context.Context, prompt string) (string, error)
		want     string
	}{
		{"nil classify defaults to general", nil, "general"},
		{
			"plain coding reply", func(context.Context, string) (string, error) {
				return "coding", nil
			}, "coding",
		},
		{
			"plain general reply", func(context.Context, string) (string, error) {
				return "general", nil
			}, "general",
		},
		{
			"topic is coding but deliverable is a book", func(context.Context, string) (string, error) {
				return "general (the goal is about coding but produces a book)", nil
			}, "general",
		},
		{
			"punctuation stripped before matching", func(context.Context, string) (string, error) {
				return "Coding.", nil
			}, "coding",
		},
		{
			"first recognised word wins when coding comes first", func(context.Context, string) (string, error) {
				return "coding, not general", nil
			}, "coding",
		},
		{
			"garbage reply defaults to general", func(context.Context, string) (string, error) {
				return "banana", nil
			}, "general",
		},
		{
			"empty reply defaults to general", func(context.Context, string) (string, error) {
				return "", nil
			}, "general",
		},
		{
			"classify error defaults to general", func(context.Context, string) (string, error) {
				return "", errors.New("gateway down")
			}, "general",
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

// TestClassifyKindPrompt pins the deliverable-vs-topic intent in the
// prompt sent to the classifier, so a future edit can't silently
// revert to topic-based classification.
func TestClassifyKindPrompt(t *testing.T) {
	t.Parallel()
	var gotPrompt string
	classify := func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "general", nil
	}
	classifyKind(context.Background(), classify, "some goal")
	if !strings.Contains(gotPrompt, "deliverable") && !strings.Contains(gotPrompt, "book") {
		t.Fatalf("classifyKind prompt = %q, want it to mention deliverable or the book counter-example", gotPrompt)
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
// classifyGoal uses: happy paths for both axes, the first recognised
// kind and light/full words winning in order even with extra fields,
// and every classify failure or unrecognised reply falling back to
// kind=general light=false, the same cheap-mistake bias classifyKind
// applies alone.
func TestClassifyKindAndLight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		classify  func(ctx context.Context, prompt string) (string, error)
		wantKind  string
		wantLight bool
	}{
		{"nil classify defaults to general/full", nil, "general", false},
		{
			"general + light", func(context.Context, string) (string, error) {
				return "general light", nil
			}, "general", true,
		},
		{
			"general + full with punctuation", func(context.Context, string) (string, error) {
				return "General, full.", nil
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
			"three word reply uses first recognised kind and light word", func(context.Context, string) (string, error) {
				return "general full extra", nil
			}, "general", false,
		},
		{
			"single word reply falls back", func(context.Context, string) (string, error) {
				return "general", nil
			}, "general", false,
		},
		{
			"garbage first word falls back", func(context.Context, string) (string, error) {
				return "banana light", nil
			}, "general", false,
		},
		{
			"garbage second word falls back", func(context.Context, string) (string, error) {
				return "general banana", nil
			}, "general", false,
		},
		{
			"empty reply falls back", func(context.Context, string) (string, error) {
				return "", nil
			}, "general", false,
		},
		{
			"classify error falls back", func(context.Context, string) (string, error) {
				return "", errors.New("gateway down")
			}, "general", false,
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

// TestClassifyHasPlan covers classifyHasPlan's shape heuristic (D-102,
// issue #496): a goal shaped like an explicit numbered/step plan (two
// or more distinct numbered/step lines) infers true; a plain goal, or
// one with only a single stray numbered mention, infers false.
func TestClassifyHasPlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		goal string
		want bool
	}{
		{"plain goal", "write a report on Q3 sales", false},
		{
			"numbered plan with periods",
			"Do the following:\n1. Create main.go\n2. Add a test\n3. Run go build",
			true,
		},
		{
			"numbered plan with parens",
			"Steps:\n1) set up the repo\n2) write the handler\n3) wire it up",
			true,
		},
		{
			"step-labeled plan",
			"Step 1: clone the repo\nStep 2: install dependencies\nStep 3: run the tests",
			true,
		},
		{
			"single stray numbered mention is not a plan",
			"Ship v1.0 of the library, see also RFC 2.0 for background",
			false,
		},
		{"empty goal", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyHasPlan(tc.goal); got != tc.want {
				t.Fatalf("classifyHasPlan(%q) = %v, want %v", tc.goal, got, tc.want)
			}
		})
	}
}

// TestMissionsClassifyEndpoint covers POST /v1/missions/classify: the
// happy path returning the classifier's verdict (kind, light, and
// has_plan), and the empty-goal 400; this endpoint has no store/driver
// dependency, so it can be tested end to end without Postgres.
func TestMissionsClassifyEndpoint(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	classify := func(ctx context.Context, prompt string) (string, error) {
		return "general light", nil
	}
	m := mux(a)
	a.registerMissions(m.Handle, missions.NewStore(pgpool.New(context.Background(), "postgres://invalid/nope", discard()), discard()), nil, nil, nil, nil, nil, nil, classify, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

	call := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/v1/missions/classify", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	if code, body := call(`{"goal":"write a report on Q3 sales"}`); code != http.StatusOK {
		t.Fatalf("classify with a goal = %d %s, want 200", code, body)
	} else if !strings.Contains(body, `"kind":"general"`) || !strings.Contains(body, `"light":true`) || !strings.Contains(body, `"has_plan":false`) {
		t.Fatalf("classify body = %s, want kind general, light true, has_plan false", body)
	}

	if code, body := call(`{"goal":"Do the following:\n1. write main.go\n2. add a test\n3. run go build"}`); code != http.StatusOK {
		t.Fatalf("classify with a plan-shaped goal = %d %s, want 200", code, body)
	} else if !strings.Contains(body, `"has_plan":true`) {
		t.Fatalf("classify body = %s, want has_plan true", body)
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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

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
// 400s: it reaches classifyKind (defaulting to "general" with no
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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

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

// TestMissionsCreateHasPlan covers has_plan's create-request parsing
// (D-102, issue #496): true and omitted (defaults to false) both pass
// straight through to the degraded driver/store (no ValidateCreate
// rejection -- has_plan has no shape rules of its own), mirroring
// TestMissionsCreateKindOptional's pattern of asserting on the response
// body rather than the status code alone.
func TestMissionsCreateHasPlan(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	pool := pgpool.New(context.Background(), "postgres://invalid/nope", discard())
	store := missions.NewStore(pool, discard())
	driver := missions.NewDriver(store, nil, nil, nil, nil, nil, nil, nil, discard())
	driver.SetValidateDeps(missions.ValidateDeps{})
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

	call := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	if code, body := call(`{"goal":"do something","kind":"general","has_plan":true}`); code != http.StatusBadRequest || strings.Contains(body, "has_plan") {
		t.Fatalf("create with has_plan=true = %d %s, want 400 from the degraded driver, not has_plan validation", code, body)
	}
	if code, body := call(`{"goal":"do something","kind":"general"}`); code != http.StatusBadRequest || strings.Contains(body, "has_plan") {
		t.Fatalf("create with has_plan omitted = %d %s, want 400 from the degraded driver, not has_plan validation", code, body)
	}
}

// TestCreateMissionRequestDecodesHasPlan pins the wire shape directly:
// has_plan decodes to true only when explicitly set, omitted/false
// both decode to false (Go's bool zero value), matching Light's own
// json:"light" (no omitempty on read) tag shape.
func TestCreateMissionRequestDecodesHasPlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"explicit true", `{"goal":"g","has_plan":true}`, true},
		{"explicit false", `{"goal":"g","has_plan":false}`, false},
		{"omitted defaults false", `{"goal":"g"}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var req createMissionRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if req.HasPlan != tc.want {
				t.Fatalf("HasPlan = %v, want %v", req.HasPlan, tc.want)
			}
		})
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
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

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
	wantOrder := []string{"discover", "plan", "generate", "prove", "escalate"}
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
// wins the base route and propagates unchanged to discover/plan (no
// plan_route override) and generate; prove carries the same route
// value but its own provenance label is "inherited-from-generate"
// (prove never itself set), not "explicit".
func TestExecutionPlanRouteSourceExplicit(t *testing.T) {
	t.Parallel()
	h := &missionAPI{
		log: discard(),
		resolveRoute: stubResolveRoute(map[string]*gwclient.ResolvedRoute{
			"mine": {Route: "mine", Entries: []gwclient.ResolvedRouteEntry{usableEntry("Anthropic", "claude-sonnet-5")}},
		}),
	}
	byPhase := getExecutionPlan(t, h, "kind=general&route=mine")

	for _, phase := range []string{"discover", "plan", "generate"} {
		p := byPhase[phase]
		if p.Route != "mine" || p.RouteSource != "explicit" {
			t.Fatalf("%s = route %q source %q, want mine/explicit", phase, p.Route, p.RouteSource)
		}
	}
	if p := byPhase["prove"]; p.Route != "mine" || p.RouteSource != "inherited-from-generate" {
		t.Fatalf("review = route %q source %q, want mine/inherited-from-generate", p.Route, p.RouteSource)
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
	if p := byPhase["generate"]; p.Route != "agent-route" || p.RouteSource != "agent" {
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
	if p := byPhase["generate"]; p.Route != "coding" || p.RouteSource != "named-coding" {
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
	if p := byPhase["generate"]; p.Route != "default" || p.RouteSource != "default-role" {
		t.Fatalf("execute = route %q source %q, want default/default-role", p.Route, p.RouteSource)
	}

	// kind=coding but no "coding" route exists (absent from the stub
	// map, so resolveRoute errors on it) must also fall back here.
	byPhaseCoding := getExecutionPlan(t, h, "kind=coding")
	if p := byPhaseCoding["generate"]; p.Route != "default" || p.RouteSource != "default-role" {
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
	p := byPhase["generate"]
	if p.Route != "" || p.RouteSource != "none" {
		t.Fatalf("execute = route %q source %q, want \"\"/none", p.Route, p.RouteSource)
	}
	if len(p.Entries) != 0 {
		t.Fatalf("execute entries = %+v, want empty (route never resolved)", p.Entries)
	}
}

// TestExecutionPlanOversightRoutes confirms plan_route, when set,
// covers discover/plan/prove (oversight phases) while generate stays on
// the base route, and review_route independently overrides prove
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

	// plan_route alone covers discover/plan/prove's route value,
	// generate keeps base. discover/plan carry plan_route's own
	// "explicit" provenance; prove's value matches but its provenance
	// is "inherited-from-plan" (prove never itself set plan_route).
	byPhase := getExecutionPlan(t, h, "kind=general&route=base&plan_route=strong")
	for _, phase := range []string{"discover", "plan"} {
		p := byPhase[phase]
		if p.Route != "strong" || p.RouteSource != "explicit" {
			t.Fatalf("%s = route %q source %q, want strong/explicit", phase, p.Route, p.RouteSource)
		}
	}
	if p := byPhase["prove"]; p.Route != "strong" || p.RouteSource != "inherited-from-plan" {
		t.Fatalf("review = route %q source %q, want strong/inherited-from-plan", p.Route, p.RouteSource)
	}
	if p := byPhase["generate"]; p.Route != "base" {
		t.Fatalf("execute = route %q, want base (unaffected by plan_route)", p.Route)
	}

	// review_route wins over plan_route for review alone.
	byPhase2 := getExecutionPlan(t, h, "kind=general&route=base&plan_route=strong&review_route=judge")
	if p := byPhase2["prove"]; p.Route != "judge" || p.RouteSource != "explicit" {
		t.Fatalf("review = route %q source %q, want judge/explicit", p.Route, p.RouteSource)
	}
	if p := byPhase2["plan"]; p.Route != "strong" {
		t.Fatalf("plan = route %q, want strong (review_route must not affect plan)", p.Route)
	}

	// review_route provenance without an explicit plan_route reports
	// inherited-from-generate, matching runner.go's reviewRoute falling
	// through oversightRoute straight to Route.
	byPhase3 := getExecutionPlan(t, h, "kind=general&route=base")
	if p := byPhase3["prove"]; p.Route != "base" || p.RouteSource != "inherited-from-generate" {
		t.Fatalf("review = route %q source %q, want base/inherited-from-generate", p.Route, p.RouteSource)
	}
}

// TestExecutionPlanHarnessAxis confirms kind=coding with a harness set
// resolves generate on the harness axis, and kind=general with the same
// harness set never delegates (D-072's canDelegate rule): generate
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
	if p := coding["generate"]; p.Axis != "harness" || p.Harness != "claude-cli" || p.HarnessSource != "explicit" {
		t.Fatalf("coding execute = axis %q harness %q source %q, want harness/claude-cli/explicit", p.Axis, p.Harness, p.HarnessSource)
	}

	general := getExecutionPlan(t, h, "kind=general&route=base&harness=claude-cli")
	if p := general["generate"]; p.Axis != "native" || p.Harness != "" {
		t.Fatalf("general execute = axis %q harness %q, want native/\"\" (harness ignored for non-coding)", p.Axis, p.Harness)
	}

	// "native" is the settings sentinel for off: normalizes to axis
	// native with no harness, same as create()'s own req.Harness == "native".
	nativeSentinel := getExecutionPlan(t, h, "kind=coding&route=base&harness=native")
	if p := nativeSentinel["generate"]; p.Axis != "native" || p.Harness != "" {
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
	if p := byPhase["generate"]; p.Harness != "opencode" || p.HarnessSource != "settings" {
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
	if p := byPhase["generate"]; p.Harness != "pi" || p.HarnessSource != "agent" {
		t.Fatalf("execute = harness %q source %q, want pi/agent", p.Harness, p.HarnessSource)
	}

	// An explicit ?harness= still wins over the agent's own harness.
	explicit := getExecutionPlan(t, h, "kind=coding&route=base&agent=coder&harness=claude-cli")
	if p := explicit["generate"]; p.Harness != "claude-cli" || p.HarnessSource != "explicit" {
		t.Fatalf("execute with explicit harness = harness %q source %q, want claude-cli/explicit", p.Harness, p.HarnessSource)
	}
}

// TestExecutionPlanLightSkipsOversightOnly confirms light=true skips
// discover/plan/prove with the fixed reason while generate is never
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
	for _, phase := range []string{"discover", "plan", "prove"} {
		p := byPhase[phase]
		if !p.Skipped || p.SkipReason != lightSkipReason {
			t.Fatalf("%s = skipped %v reason %q, want true/%q", phase, p.Skipped, p.SkipReason, lightSkipReason)
		}
	}
	if p := byPhase["generate"]; p.Skipped {
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
	entries := mixed["generate"].Entries
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
	for i, e := range noneUsable["generate"].Entries {
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
	entries := byPhase["generate"].Entries
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
	entries := byPhase["generate"].Entries
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
	reviewEntries := byPhase["prove"].Entries
	if !reviewEntries[1].Selected {
		t.Fatalf("review entries[1] (B/b) selected = false, want true (route_model inherited)")
	}

	// plan_route_model set without review_route_model: review_route_model
	// > plan_route_model, plan_route_model wins here.
	byPhase2 := getExecutionPlan(t, h, "kind=general&route=base&route_model="+url.QueryEscape("B/b")+"&plan_route_model="+url.QueryEscape("A/a"))
	reviewEntries2 := byPhase2["prove"].Entries
	if !reviewEntries2[0].Selected {
		t.Fatalf("review entries[0] (A/a) selected = false, want true (plan_route_model beats route_model)")
	}

	// review_route_model set: wins over both.
	byPhase3 := getExecutionPlan(t, h, "kind=general&route=base&route_model="+url.QueryEscape("B/b")+"&plan_route_model="+url.QueryEscape("A/a")+"&review_route_model="+url.QueryEscape("B/b"))
	reviewEntries3 := byPhase3["prove"].Entries
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
		if p.Phase != "generate" {
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

	execute := byPhase["generate"]
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
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

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
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, kbStore, noopIngester{}, nil, "", nil)

	req := httptest.NewRequest("POST", "/v1/missions/some-id/promote-kb", strings.NewReader(`{"collection_id":"c1"}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s, want 400 from the degraded store", w.Code, w.Body.String())
	}
}

// resolveRouteFixture fakes gwclient.ResolveRoute for the D-100 route
// gate tests: "dead" has only unusable entries, "empty" has no chain,
// "unknown" fails to resolve, everything else has one usable entry.
func resolveRouteFixture(_ context.Context, route, harness string) (*gwclient.ResolvedRoute, error) {
	switch route {
	case "dead":
		return &gwclient.ResolvedRoute{Route: route, Entries: []gwclient.ResolvedRouteEntry{
			{ProviderName: "zai", Model: "glm-4.7", Usable: false, SkipReason: "cooling down until 12:00"},
			{ProviderName: "openai", Model: "gpt-5", Usable: false, SkipReason: "disabled"},
		}}, nil
	case "empty":
		return &gwclient.ResolvedRoute{Route: route}, nil
	case "unknown":
		return nil, errors.New("gwclient: gateway http 404: no such route")
	case "harness-only-dead":
		if harness != "" {
			return &gwclient.ResolvedRoute{Route: route, Entries: []gwclient.ResolvedRouteEntry{{Usable: false, SkipReason: "no executor entry"}}}, nil
		}
	}
	return &gwclient.ResolvedRoute{Route: route, Entries: []gwclient.ResolvedRouteEntry{{ProviderName: "ok", Model: "m", Usable: true}}}, nil
}

// TestMissionsUnusableCreateRoute table-tests the create-time route
// gate (D-100, issue #536) across phase axes and flows.
func TestMissionsUnusableCreateRoute(t *testing.T) {
	t.Parallel()
	h := &missionAPI{log: discard(), resolveRoute: resolveRouteFixture}
	for _, tc := range []struct {
		name       string
		req        createMissionRequest
		flow       missions.Flow
		wantRoute  string
		wantReason string
	}{
		{name: "all usable", req: createMissionRequest{Route: "alive", ReviewRoute: "alive"}, flow: missions.FlowFull},
		{name: "dead worker route", req: createMissionRequest{Route: "dead", ReviewRoute: "alive"}, flow: missions.FlowFull, wantRoute: "dead", wantReason: "cooling down until 12:00"},
		{name: "dead review route", req: createMissionRequest{Route: "alive", ReviewRoute: "dead"}, flow: missions.FlowFull, wantRoute: "dead", wantReason: "cooling down until 12:00"},
		{name: "dead plan route", req: createMissionRequest{Route: "alive", PlanRoute: "dead", ReviewRoute: "alive"}, flow: missions.FlowFull, wantRoute: "dead", wantReason: "cooling down until 12:00"},
		{name: "empty chain never blocks", req: createMissionRequest{Route: "alive", PlanRoute: "empty", ReviewRoute: "alive"}, flow: missions.FlowFull},
		{name: "dead escalation route", req: createMissionRequest{Route: "alive", ReviewRoute: "alive", EscalationRoute: "dead"}, flow: missions.FlowFull, wantRoute: "dead", wantReason: "cooling down until 12:00"},
		{name: "light skips oversight and review", req: createMissionRequest{Route: "alive", PlanRoute: "dead", ReviewRoute: "dead"}, flow: missions.FlowLight},
		{name: "no_prove skips review only", req: createMissionRequest{Route: "alive", ReviewRoute: "dead"}, flow: missions.FlowNoProve},
		{name: "harness axis resolves separately", req: createMissionRequest{Route: "harness-only-dead", Harness: "claude-cli", ReviewRoute: "alive"}, flow: missions.FlowFull, wantRoute: "harness-only-dead", wantReason: "no executor entry"},
		{name: "resolve error never blocks", req: createMissionRequest{Route: "unknown", ReviewRoute: "unknown"}, flow: missions.FlowFull},
	} {
		route, reason, unusable := h.unusableCreateRoute(context.Background(), tc.req, tc.flow)
		if unusable != (tc.wantRoute != "") || route != tc.wantRoute || reason != tc.wantReason {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", tc.name, route, reason, unusable, tc.wantRoute, tc.wantReason, tc.wantRoute != "")
		}
	}
	if _, _, unusable := (&missionAPI{log: discard()}).unusableCreateRoute(context.Background(), createMissionRequest{Route: "dead"}, missions.FlowFull); unusable {
		t.Fatal("no gateway wiring must never block create")
	}
}

// TestMissionsCreateRejectsUnusableRoute confirms the create handler
// answers 400 route_unusable naming the route and first skip reason.
func TestMissionsCreateRejectsUnusableRoute(t *testing.T) {
	t.Parallel()
	h := &missionAPI{log: discard(), resolveRoute: resolveRouteFixture}
	req := httptest.NewRequest("POST", "/v1/missions", strings.NewReader(`{"goal":"g","kind":"general","route":"alive","review_route":"dead"}`))
	w := httptest.NewRecorder()
	h.create(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	var body struct{ Error, Message string }
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "route_unusable" || !strings.Contains(body.Message, `"dead"`) || !strings.Contains(body.Message, "cooling down until 12:00") {
		t.Fatalf("body = %+v, want route_unusable naming the route and skip reason", body)
	}
}

// TestMissionsRoutingValidation covers PATCH .../routing's request
// checks that need no store: body shape, gateway wiring, and the
// route's own resolution. The paused-only state gate lives in the
// driver (TestDriverChangeRouting) and the integration test.
func TestMissionsRoutingValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		h        *missionAPI
		body     string
		wantCode int
		wantErr  string
	}{
		{"malformed body", &missionAPI{log: discard(), resolveRoute: resolveRouteFixture}, `{`, 400, "bad_request"},
		{"missing review_route", &missionAPI{log: discard(), resolveRoute: resolveRouteFixture}, `{"review_route_model":"a/b"}`, 400, "bad_request"},
		{"no gateway wiring", &missionAPI{log: discard()}, `{"review_route":"alive"}`, 404, "not_found"},
		{"unknown route", &missionAPI{log: discard(), resolveRoute: resolveRouteFixture}, `{"review_route":"unknown"}`, 400, "unknown_route"},
		{"unusable route", &missionAPI{log: discard(), resolveRoute: resolveRouteFixture}, `{"review_route":"dead"}`, 400, "route_unusable"},
	} {
		req := httptest.NewRequest("PATCH", "/v1/missions/abc/routing", strings.NewReader(tc.body))
		req.SetPathValue("id", "abc")
		w := httptest.NewRecorder()
		tc.h.routing(w, req)
		var body struct{ Error, Message string }
		_ = json.NewDecoder(w.Body).Decode(&body)
		if w.Code != tc.wantCode || body.Error != tc.wantErr {
			t.Errorf("%s: got %d %q, want %d %q", tc.name, w.Code, body.Error, tc.wantCode, tc.wantErr)
		}
		if tc.wantErr == "route_unusable" && !strings.Contains(body.Message, "cooling down until 12:00") {
			t.Errorf("%s: message = %q, want the first skip reason", tc.name, body.Message)
		}
	}
}
