package gwclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func collect(t *testing.T, ch <-chan stream.StreamEvent) []stream.StreamEvent {
	t.Helper()
	var events []stream.StreamEvent
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-deadline:
			t.Fatalf("stream did not close; got %+v", events)
		}
	}
}

func terminals(events []stream.StreamEvent) int {
	n := 0
	for _, ev := range events {
		if ev.Type == stream.EventDone || ev.Type == stream.EventError {
			n++
		}
	}
	return n
}

func gatewayStub(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestStreamRelaysEvents(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"type\":\"chunk\",\"text\":\"hi\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"done\",\"meta\":{\"provider\":\"p\",\"model\":\"m\"}}\n\n")
	})

	ch, err := c.Stream(t.Context(), StreamRequest{Route: "mini"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if len(events) != 2 || events[0].Text != "hi" || events[1].Type != stream.EventDone {
		t.Fatalf("events = %+v", events)
	}
	if events[1].Meta == nil || events[1].Meta.Provider != "p" {
		t.Fatalf("meta lost in relay: %+v", events[1])
	}
}

func TestStreamNoSecondTerminalWhenConnectionDropsAfterDone(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"type\":\"done\"}\n\n")
		w.(http.Flusher).Flush()
		// Abort the connection uncleanly: the client sees a read error
		// AFTER the terminal already arrived.
		panic(http.ErrAbortHandler)
	})

	ch, err := c.Stream(t.Context(), StreamRequest{Route: "mini"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if n := terminals(events); n != 1 {
		t.Fatalf("terminals = %d, want exactly 1: %+v", n, events)
	}
	if events[len(events)-1].Type != stream.EventDone {
		t.Fatalf("last = %v, want done", events[len(events)-1].Type)
	}
}

func TestStreamEmitsTerminalWhenCutBeforeDone(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"type\":\"chunk\",\"text\":\"par\"}\n\n")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})

	ch, err := c.Stream(t.Context(), StreamRequest{Route: "mini"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if n := terminals(events); n != 1 {
		t.Fatalf("terminals = %d, want exactly 1: %+v", n, events)
	}
	last := events[len(events)-1]
	if last.Type != stream.EventError || last.Err.Code != "gateway_stream_cut" {
		t.Fatalf("last = %+v, want gateway_stream_cut error", last)
	}
}

// TestStreamEmitsTerminalWhenCutAfterContextDone pins the fix for a
// race where the caller's context (brain's turnCtx, ~30min ceiling)
// expires at almost the same instant as a genuine upstream failure:
// previously the synthetic gateway_stream_cut error was skipped
// entirely whenever ctx.Err() != nil at that point, so persistTurn
// (chat.go) received neither a failure nor any text and silently
// appended nothing — a real ~30min OCI turn vanished with zero
// session_events despite gateway having tried to report a real error.
func TestStreamEmitsTerminalWhenCutAfterContextDone(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"type\":\"chunk\",\"text\":\"par\"}\n\n")
		w.(http.Flusher).Flush()
		<-release
		panic(http.ErrAbortHandler)
	})

	ctx, cancel := context.WithCancel(t.Context())
	ch, err := c.Stream(ctx, StreamRequest{Route: "mini"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Drain the chunk, then cancel ctx before the read error happens —
	// reproducing turnCtx's deadline firing right as the upstream cuts.
	first := <-ch
	if first.Type != stream.EventChunk {
		t.Fatalf("first event = %+v, want chunk", first)
	}
	cancel()
	close(release)

	events := collect(t, ch)
	if n := terminals(events); n != 1 {
		t.Fatalf("terminals = %d, want exactly 1: %+v", n, events)
	}
	last := events[len(events)-1]
	if last.Type != stream.EventError || last.Err.Code != "gateway_stream_cut" {
		t.Fatalf("last = %+v, want gateway_stream_cut error even with ctx already done", last)
	}
}

func TestStreamRejectsGatewayErrorStatus(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no_route"}`, http.StatusBadGateway)
	})

	if _, err := c.Stream(t.Context(), StreamRequest{Route: "mini"}); err == nil {
		t.Fatal("Stream() = nil error for 502 gateway response")
	}
}

func TestModelWindows(t *testing.T) {
	t.Parallel()
	hits := 0
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/providers" {
			http.NotFound(w, r)
			return
		}
		hits++
		_, _ = fmt.Fprint(w, `{"providers":[
			{"name":"a","models":[{"id":"big","context_window":200000},{"id":"unsized"}]},
			{"name":"b","models":[{"id":"small","context_window":1000}]}
		]}`)
	})

	windows, err := c.ModelWindows(t.Context())
	if err != nil {
		t.Fatalf("ModelWindows: %v", err)
	}
	if windows["big"] != 200000 || windows["small"] != 1000 {
		t.Fatalf("windows = %+v", windows)
	}
	if _, ok := windows["unsized"]; ok {
		t.Fatal("model without a context window must be omitted")
	}

	// A second read inside the TTL serves from the memo.
	if _, err := c.ModelWindows(t.Context()); err != nil {
		t.Fatalf("ModelWindows (cached): %v", err)
	}
	if hits != 1 {
		t.Fatalf("gateway hits = %d, want 1 (TTL memo)", hits)
	}
}

func TestModelWindowsGatewayError(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"config_unavailable"}`, http.StatusServiceUnavailable)
	})

	if _, err := c.ModelWindows(t.Context()); err == nil {
		t.Fatal("ModelWindows() = nil error for 503 gateway response")
	}
}
