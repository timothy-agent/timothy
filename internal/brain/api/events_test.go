package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// syncRecorder wraps httptest.ResponseRecorder with a mutex so a test
// can read the body from one goroutine while the handler under test
// (running in its own goroutine, as the events stream must) writes to
// it from another — plain ResponseRecorder isn't safe for that.
type syncRecorder struct {
	mu sync.Mutex
	*httptest.ResponseRecorder
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *syncRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(p)
}

func (r *syncRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(code)
}

func (r *syncRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *syncRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

var _ http.Flusher = (*syncRecorder)(nil)

func TestEventsEndpointUnmountedWhenHubNil(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := http.NewServeMux()
	a.registerEvents(m.Handle, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/events with a nil hub = %d, want 404 (unmounted)", rec.Code)
	}
}

// TestEventsEndpointStreamsSignals confirms a connected client sees
// the initial ready event, then a signal event for a hub publish that
// happens after it connected — the exact contract the web's
// subscribeEvents relies on to know when to refetch.
func TestEventsEndpointStreamsSignals(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	hub := missions.NewHub()
	m := http.NewServeMux()
	a.registerEvents(m.Handle, hub)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer tok")
	rec := newSyncRecorder()

	done := make(chan struct{})
	go func() {
		m.ServeHTTP(rec, req)
		close(done)
	}()

	// Give the handler a moment to subscribe before publishing — a
	// publish before Subscribe registers would simply miss this
	// subscriber (benign per the hub's own contract), so this waits on
	// the ready event as proof the subscription is live.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(rec.body(), "event: ready") {
		if time.Now().After(deadline) {
			t.Fatal("never saw the initial ready event")
		}
		time.Sleep(5 * time.Millisecond)
	}

	hub.Publish(missions.Signal{Kind: "mission", ID: "m1"})

	deadline = time.Now().Add(2 * time.Second)
	for !strings.Contains(rec.body(), "event: signal") {
		if time.Now().After(deadline) {
			t.Fatal("never saw the published signal")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never returned after context cancel")
	}

	body := rec.body()
	if !strings.Contains(body, `data: {"type":"signal","kind":"mission","id":"m1"}`) {
		t.Fatalf("body missing expected signal payload: %s", body)
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", rec.Header().Get("Content-Type"))
	}
}

func TestEventsEndpointRequiresAuth(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	hub := missions.NewHub()
	m := http.NewServeMux()
	a.registerEvents(m.Handle, hub)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 without a bearer token", rec.Code)
	}
}
