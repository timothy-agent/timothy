package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

func TestMissionsEndpointsUnmountedWhenStoreNil(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerMissions(m.Handle, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

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
		{"POST", "/v1/missions/abc/push"},
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
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil)

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
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("DELETE", "/v1/missions/abc", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("DELETE against a degraded store = %d, want 400 (reached the store, generic failure)", w.Code)
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
		a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, codingExecutorDefault, nil)
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

// TestMissionsClassifyEndpoint covers POST /v1/missions/classify: the
// happy path returning the classifier's verdict, and the empty-goal
// 400 — this endpoint has no store/driver dependency, so it can be
// tested end to end without Postgres.
func TestMissionsClassifyEndpoint(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	classify := func(context.Context, string) (string, error) { return "general", nil }
	m := mux(a)
	a.registerMissions(m.Handle, missions.NewStore(pgpool.New(context.Background(), "postgres://invalid/nope", discard()), discard()), nil, nil, nil, nil, nil, nil, classify, nil, nil)

	call := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/v1/missions/classify", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	if code, body := call(`{"goal":"write a report on Q3 sales"}`); code != http.StatusOK {
		t.Fatalf("classify with a goal = %d %s, want 200", code, body)
	} else if !strings.Contains(body, `"kind":"general"`) {
		t.Fatalf("classify body = %s, want kind general", body)
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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil)

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
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil)

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
	m := mux(a)
	a.registerMissions(m.Handle, store, driver, nil, nil, nil, nil, nil, nil, nil, nil)

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
