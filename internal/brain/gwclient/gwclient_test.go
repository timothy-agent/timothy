package gwclient

import (
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

	ch, err := c.Stream(t.Context(), StreamRequest{TaskCategory: "mini"})
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

	ch, err := c.Stream(t.Context(), StreamRequest{TaskCategory: "mini"})
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

	ch, err := c.Stream(t.Context(), StreamRequest{TaskCategory: "mini"})
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

func TestStreamRejectsGatewayErrorStatus(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no_route"}`, http.StatusBadGateway)
	})

	if _, err := c.Stream(t.Context(), StreamRequest{TaskCategory: "mini"}); err == nil {
		t.Fatal("Stream() = nil error for 502 gateway response")
	}
}
