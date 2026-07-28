package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

func TestMissionsEndpointsUnmountedWhenStoreNil(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerMissions(m.Handle, nil, nil, nil, nil, nil, nil)

	for _, req := range []struct{ method, path string }{
		{"GET", "/v1/missions"},
		{"POST", "/v1/missions"},
		{"GET", "/v1/missions/abc"},
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
	a.registerMissions(m.Handle, store, nil, nil, nil, nil, nil)

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
