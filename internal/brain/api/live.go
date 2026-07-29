package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// registerLive mounts GET /v1/sessions/{id}/live, Tier 2 of the
// live-reattach feature: a tab that opens a session mid-turn replays
// whatever the turn already emitted, then follows it live until the
// terminal, using the IDENTICAL wire format as POST .../messages
// (streamTurn below) so the web reuses the same SSE parser and
// applyEvent reducer — a reattached turn renders exactly like one this
// tab started itself.
func (a *API) registerLive(handle func(pattern string, h http.Handler)) {
	handle("GET /v1/sessions/{id}/live", a.auth(http.HandlerFunc(a.handleLive)))
}

// handleLive answers 404 "no_active_turn" when chat.Service has no
// turn in flight for this session — chosen over 204 because this is a
// GET whose resource (the live stream) simply doesn't exist right now,
// the same "nothing here" framing handleTranscript/handleRetry already
// use (not_found, no_retryable_turn), rather than a mutation's "done,
// nothing to report". A client seeing 404 falls back to its normal
// transcript fetch — turn_active having already gone false by the time
// this request lands is a benign race, not an error: the transcript it
// then fetches already reflects the finished turn (turnDone frees the
// broadcaster only after persistTurn's write is durable).
func (a *API) handleLive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validSessionID(id) {
		jsonError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	replay, live, ok := a.svc.Subscribe(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "no_active_turn", "no turn is currently running for this session")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer cannot flush")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	send := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// m accumulates the same terminal meta contract streamTurn emits —
	// scanning the replay AND the live tail identically, so a client
	// attaching mid-turn ends up with the same meta a client that was
	// there from the start would see.
	m := meta{Type: "meta", SessionID: id}
	fold := func(ev stream.StreamEvent) {
		if ev.Type == stream.EventUsage {
			m.Usage = ev.Usage
		}
		if ev.Meta != nil {
			m.Provider, m.Model, m.LedgerID = ev.Meta.Provider, ev.Meta.Model, ev.Meta.LedgerID
			m.DurationMs = ev.Meta.DurationMs
		}
	}

	for _, ev := range replay {
		fold(ev)
		send(ev)
	}
	ctx := r.Context()
	for {
		select {
		case ev, chOk := <-live:
			if !chOk {
				// Either the turn reached its terminal (persistTurn ran,
				// turnDone closed every subscriber) or this subscriber got
				// dropped for being too slow (turnBroadcaster.publish) — in
				// both cases nothing more is coming on this connection.
				// Sending the terminal meta unconditionally is safe: a
				// dropped-for-slowness client falls back to a transcript
				// refetch per the web's contract regardless of what this
				// meta says.
				send(m)
				return
			}
			fold(ev)
			send(ev)
		case <-ctx.Done():
			return
		}
	}
}
