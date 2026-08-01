package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestStopEndpointInvalidSessionID confirms a malformed id 404s the
// same way every other session-scoped route does (validSessionID),
// without ever reaching chat.Service.
func TestStopEndpointInvalidSessionID(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	w := doMux(a, http.MethodPost, "/v1/sessions/not-a-valid-id/stop", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

// TestStopEndpointNoActiveTurn confirms an idle (but real) session
// answers 404 no_active_turn — the same "nothing here" framing
// handleLive uses for the identical case.
func TestStopEndpointNoActiveTurn(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "t")

	w := doMux(a, http.MethodPost, "/v1/sessions/"+id+"/stop", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "no_active_turn" {
		t.Fatalf("error = %q, want no_active_turn", body["error"])
	}
}

// TestStopEndpointStopsActiveTurn confirms POST .../stop cancels a
// live turn: 204, TurnActive flips false without the fake gateway ever
// being unblocked (StopTurn's cancel is what winds it down, not the
// fake completing on its own), and the original POST /messages call
// still returns normally once the turn's abnormal end persists.
func TestStopEndpointStopsActiveTurn(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	a, dir, gw := testAPI(t, "tok", okEvents())
	gw.blockCh = block
	id, _ := dir.Create(t.Context(), "t")

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages", `{"message":"hello"}`)
	}()
	waitForTest(t, func() bool { return a.svc.TurnActive(id) })

	w := doMux(a, http.MethodPost, "/v1/sessions/"+id+"/stop", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("stop code = %d, want 204; body=%s", w.Code, w.Body.String())
	}

	waitForTest(t, func() bool { return !a.svc.TurnActive(id) })

	// The blocked send's own goroutine is still parked on <-block; the
	// turn ending is StopTurn's cancel winding down the loop, not the
	// fake gateway completing — closing block here just lets that now-
	// orphaned goroutine exit so the test doesn't leak it.
	close(block)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("original POST /messages never returned")
	}

	// Stopping again finds nothing live.
	w = doMux(a, http.MethodPost, "/v1/sessions/"+id+"/stop", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("second stop code = %d, want 404 (turn already over)", w.Code)
	}
}
