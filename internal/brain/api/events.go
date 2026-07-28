package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// eventsHeartbeat keeps the SSE connection alive through proxies that
// kill an idle connection — a comment line carries no event, so it
// never triggers a client refetch.
const eventsHeartbeat = 30 * time.Second

// registerEvents mounts GET /v1/events, the push replacement for the
// web's old 5s mission/notification poll: nil hub (missions disabled,
// see cmd/brain/main.go's WORKSPACES gate) leaves it unmounted (404),
// matching every other nil-gated optional surface in this package.
func (a *API) registerEvents(handle func(pattern string, h http.Handler), hub *missions.Hub) {
	if hub == nil {
		return
	}
	h := &eventsAPI{hub: hub}
	handle("GET /v1/events", a.auth(http.HandlerFunc(h.stream)))
}

type eventsAPI struct {
	hub *missions.Hub
}

// stream relays hub signals to the client as SSE, same headers/flush
// discipline as streamTurn: an initial "ready" event lets the client
// know the stream is live (and refetch anything it might have missed
// while connecting), then one "signal" event per hub push, plus a
// heartbeat comment so idle connections survive proxies. Signals
// carry only kind/id — no payload — so the client always refetches
// the real state over the normal REST endpoints. The JSON payload
// also carries "type" (same convention as chat's terminal meta event)
// so a client parsing only "data:" lines — the web's createSSEParser
// never looks at the "event:" line — can still tell ready from signal.
func (h *eventsAPI) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer cannot flush")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	_, _ = fmt.Fprintf(w, "event: ready\ndata: {\"type\":\"ready\"}\n\n")
	flusher.Flush()

	ctx := r.Context()
	sigs := h.hub.Subscribe(ctx)
	ticker := time.NewTicker(eventsHeartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-sigs:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "event: signal\ndata: {\"type\":\"signal\",\"kind\":%q,\"id\":%q}\n\n", sig.Kind, sig.ID)
			flusher.Flush()
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
