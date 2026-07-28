package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

func TestSchedulesEndpointsUnmountedWhenStoreNil(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerSchedules(m.Handle, nil)

	for _, req := range []struct{ method, path string }{
		{"GET", "/v1/schedules"},
		{"POST", "/v1/schedules"},
		{"PATCH", "/v1/schedules/abc"},
		{"DELETE", "/v1/schedules/abc"},
	} {
		httpReq := httptest.NewRequest(req.method, req.path, nil)
		httpReq.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httpReq)
		if w.Code != 404 {
			t.Fatalf("%s %s with a nil schedules store = %d, want 404 (unmounted)", req.method, req.path, w.Code)
		}
	}
}

// TestDecorateNextRun is a pure test of the GET-decoration helper: no
// store, no HTTP round trip, just the anchor/cron math.
func TestDecorateNextRun(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sc := missions.Schedule{Cron: "0 9 * * *", CreatedAt: created}
	view := decorate(sc)
	if view.NextRun == nil {
		t.Fatal("decorate did not compute a next_run")
	}
	want := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	if !view.NextRun.Equal(want) {
		t.Fatalf("NextRun = %v, want %v", *view.NextRun, want)
	}

	// last_run anchors ahead of created_at once the schedule has fired.
	lastRun := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	sc.LastRun = &lastRun
	view = decorate(sc)
	want = time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)
	if view.NextRun == nil || !view.NextRun.Equal(want) {
		t.Fatalf("NextRun with last_run set = %v, want %v", view.NextRun, want)
	}

	// An invalid cron (shouldn't happen past CreateSchedule's own
	// validation, but decorate must degrade gracefully) omits next_run
	// rather than panicking or reporting a fabricated zero time.
	sc = missions.Schedule{Cron: "garbage", CreatedAt: created}
	view = decorate(sc)
	if view.NextRun != nil {
		t.Fatalf("NextRun for invalid cron = %v, want nil", view.NextRun)
	}
}
