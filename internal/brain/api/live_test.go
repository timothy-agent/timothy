package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// TestTranscriptTurnActiveReflectsInFlightTurn confirms GET
// /v1/sessions/{id} carries turn_active straight off chat.Service's
// broadcaster registry: false before any turn, true mid-turn, false
// again once the turn's terminal persist is durable.
func TestTranscriptTurnActiveReflectsInFlightTurn(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	a, dir, gw := testAPI(t, "tok", okEvents())
	gw.blockCh = block
	id, _ := dir.Create(t.Context(), "t")

	if active := transcriptTurnActive(t, a, id); active {
		t.Fatal("turn_active = true before any turn started")
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages", `{"message":"hello"}`)
	}()

	waitForTest(t, func() bool { return a.svc.TurnActive(id) })
	if active := transcriptTurnActive(t, a, id); !active {
		t.Fatal("turn_active = false while a turn is in flight")
	}

	close(block)
	select {
	case w := <-done:
		if w.Code != http.StatusOK {
			t.Fatalf("messages code = %d body=%s", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("POST /messages never returned")
	}

	waitForTest(t, func() bool { return !a.svc.TurnActive(id) })
	if active := transcriptTurnActive(t, a, id); active {
		t.Fatal("turn_active = true after the turn's terminal persist")
	}
	// The transcript itself must already show the completed turn by the
	// time turn_active reads false — no window where the flag is down
	// but a refetch would still see the stale state.
	w := doMux(a, http.MethodGet, "/v1/sessions/"+id, "")
	var resp struct {
		Items []session.TranscriptItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, item := range resp.Items {
		if item.Kind == "assistant" {
			found = true
		}
	}
	if !found {
		t.Fatal("transcript missing the completed assistant turn once turn_active went false")
	}
}

func transcriptTurnActive(t *testing.T, a *API, id string) bool {
	t.Helper()
	w := doMux(a, http.MethodGet, "/v1/sessions/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("transcript: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		TurnActive bool `json:"turn_active"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.TurnActive
}

func waitForTest(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// TestLiveEndpointNoActiveTurn confirms GET .../live answers 404
// no_active_turn when nothing is running — the status a client falls
// back to a normal transcript fetch on.
func TestLiveEndpointNoActiveTurn(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "t")

	w := doMux(a, http.MethodGet, "/v1/sessions/"+id+"/live", "")
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

// TestMessagesEndpointTurnInFlightReturns409 confirms POST
// /v1/sessions/{id}/messages maps chat.ErrTurnInFlight (D-042) to a
// 409 with code "turn_in_flight" — the same error-mapping pattern as
// the existing no_retryable_turn 409 in streamTurn — when a second
// send races an already in-flight turn on the same session.
func TestMessagesEndpointTurnInFlightReturns409(t *testing.T) {
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

	w := doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages", `{"message":"racing"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("racing POST /messages code = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "turn_in_flight" {
		t.Fatalf("error = %q, want turn_in_flight", body["error"])
	}

	close(block)
	select {
	case w := <-done:
		if w.Code != http.StatusOK {
			t.Fatalf("original POST /messages code = %d body=%s", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original POST /messages never returned")
	}
}

func TestLiveEndpointUnknownSession(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	w := doMux(a, http.MethodGet, "/v1/sessions/not-a-valid-id/live", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

func TestLiveEndpointRequiresAuth(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "t")

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+id+"/live", nil)
	w := httptest.NewRecorder()
	mux(a).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 without a bearer token", w.Code)
	}
}

// TestLiveEndpointReplaysThenFollowsLiveThenCloses is the end-to-end
// Tier-2 contract: attaching mid-turn gets the buffered prefix, then
// the rest of the turn as it happens, wire-identical to POST
// .../messages (same "data: {...}\n\n" frames, terminal meta last).
func TestLiveEndpointReplaysThenFollowsLiveThenCloses(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	a, dir, gw := testAPI(t, "tok", []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "hi "},
		{Type: stream.EventChunk, Text: "there"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod", LedgerID: "led-1"}},
	})
	gw.blockCh = block
	id, _ := dir.Create(t.Context(), "t")

	sendDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		sendDone <- doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages", `{"message":"hello"}`)
	}()
	waitForTest(t, func() bool { return a.svc.TurnActive(id) })

	liveReq := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+id+"/live", nil)
	liveReq.Header.Set("Authorization", "Bearer tok")
	liveRec := newSyncRecorder()
	liveDone := make(chan struct{})
	go func() {
		mux(a).ServeHTTP(liveRec, liveReq)
		close(liveDone)
	}()

	// Wait for the replayed prefix (the "hi " chunk the fake gateway
	// sends before blocking) to show up on the live connection.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(liveRec.body(), `"hi "`) {
		if time.Now().After(deadline) {
			t.Fatalf("live stream never showed the replayed prefix; body=%s", liveRec.body())
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(block) // let the turn finish

	select {
	case w := <-sendDone:
		if w.Code != http.StatusOK {
			t.Fatalf("messages code = %d body=%s", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("POST /messages never returned")
	}

	select {
	case <-liveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("GET /live never closed after the turn's terminal")
	}

	body := liveRec.body()
	if !strings.Contains(body, `"there"`) {
		t.Fatalf("live stream missing the post-attach chunk; body=%s", body)
	}
	if strings.Count(body, `"hi "`) != 1 {
		t.Fatalf("replayed chunk appeared %d times, want exactly once; body=%s", strings.Count(body, `"hi "`), body)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n\n")
	last := strings.TrimPrefix(lines[len(lines)-1], "data: ")
	var m meta
	if err := json.Unmarshal([]byte(last), &m); err != nil {
		t.Fatalf("terminal frame not meta: %v (%s)", err, last)
	}
	if m.Type != "meta" || m.SessionID != id || m.Provider != "prov" {
		t.Fatalf("meta = %+v", m)
	}
	if liveRec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", liveRec.Header().Get("Content-Type"))
	}
}

// TestLiveEndpointTwoSubscribersBothWork confirms N simultaneous
// attaches to the same in-flight turn each get their own full replay
// and live tail — two tabs opening the same session mid-turn.
func TestLiveEndpointTwoSubscribersBothWork(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	a, dir, gw := testAPI(t, "tok", []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "hi "},
		{Type: stream.EventChunk, Text: "there"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	})
	gw.blockCh = block
	id, _ := dir.Create(t.Context(), "t")

	sendDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		sendDone <- doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages", `{"message":"hello"}`)
	}()
	waitForTest(t, func() bool { return a.svc.TurnActive(id) })

	recs := make([]*syncRecorder, 2)
	dones := make([]chan struct{}, 2)
	for i := range recs {
		recs[i] = newSyncRecorder()
		dones[i] = make(chan struct{})
		req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+id+"/live", nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec, d := recs[i], dones[i]
		go func() {
			mux(a).ServeHTTP(rec, req)
			close(d)
		}()
	}

	for _, rec := range recs {
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(rec.body(), `"hi "`) {
			if time.Now().After(deadline) {
				t.Fatalf("a subscriber never saw the replayed prefix; body=%s", rec.body())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	close(block)
	select {
	case <-sendDone:
	case <-time.After(5 * time.Second):
		t.Fatal("POST /messages never returned")
	}
	for i, d := range dones {
		select {
		case <-d:
		case <-time.After(5 * time.Second):
			t.Fatalf("subscriber %d never closed", i)
		}
	}
	for i, rec := range recs {
		if !strings.Contains(rec.body(), `"there"`) {
			t.Fatalf("subscriber %d missing post-attach chunk; body=%s", i, rec.body())
		}
	}
}
