package gwclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

	// Delivery no longer races ctx.Done() (a coin flip there ate real
	// failures); with a live consumer the terminal must arrive on
	// every attempt, so a single one asserts it deterministically.
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

	first := <-ch
	if first.Type != stream.EventChunk {
		t.Fatalf("first event = %+v, want chunk", first)
	}
	cancel()
	close(release)

	ev, ok := <-ch
	if !ok || ev.Type != stream.EventError || ev.Err == nil || ev.Err.Code != "gateway_stream_cut" {
		t.Fatalf("event after cut = %+v (ok=%v), want gateway_stream_cut error", ev, ok)
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

func TestResolveRoute(t *testing.T) {
	t.Parallel()
	hits := 0
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/routes/coding/resolve" || r.URL.Query().Get("harness") != "claude-cli" {
			http.NotFound(w, r)
			return
		}
		hits++
		_, _ = fmt.Fprint(w, `{"route":"coding","entries":[
			{"provider_id":"p2","provider_name":"claude-sub","driver":"claude-cli","kind":"cli",
			 "model":"claude-sonnet-4","credential_ref":"subscription",
			 "base_url":"http://localhost:9999","usable":true}
		]}`)
	})

	resolved, err := c.ResolveRoute(t.Context(), "coding", "claude-cli")
	if err != nil {
		t.Fatalf("ResolveRoute: %v", err)
	}
	if resolved.Route != "coding" || len(resolved.Entries) != 1 {
		t.Fatalf("resolved = %+v", resolved)
	}
	exec := resolved.Entries[0]
	if exec.CredentialRef != "subscription" || exec.BaseURL != "http://localhost:9999" || !exec.Usable {
		t.Fatalf("executor entry = %+v", exec)
	}

	// A second read inside the TTL serves from the memo.
	if _, err := c.ResolveRoute(t.Context(), "coding", "claude-cli"); err != nil {
		t.Fatalf("ResolveRoute (cached): %v", err)
	}
	if hits != 1 {
		t.Fatalf("gateway hits = %d, want 1 (TTL memo)", hits)
	}
}

func TestResolveRouteCachesPerRouteNameAndHarness(t *testing.T) {
	t.Parallel()
	hits := map[string]int{}
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		route := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/routes/"), "/resolve")
		key := route + "\x00" + r.URL.Query().Get("harness")
		hits[key]++
		_, _ = fmt.Fprintf(w, `{"route":%q,"entries":[]}`, route) //nolint:gosec // G705: test stub echoing a fixed test path segment as JSON.
	})

	if _, err := c.ResolveRoute(t.Context(), "coding", ""); err != nil {
		t.Fatalf("ResolveRoute(coding, native): %v", err)
	}
	if _, err := c.ResolveRoute(t.Context(), "coding", "claude-cli"); err != nil {
		t.Fatalf("ResolveRoute(coding, claude-cli): %v", err)
	}
	if _, err := c.ResolveRoute(t.Context(), "mini", ""); err != nil {
		t.Fatalf("ResolveRoute(mini, native): %v", err)
	}
	if hits["coding\x00"] != 1 || hits["coding\x00claude-cli"] != 1 || hits["mini\x00"] != 1 {
		t.Fatalf("hits = %+v, want one each", hits)
	}
}

func TestResolveRouteNotFound(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	})

	if _, err := c.ResolveRoute(t.Context(), "no-such-route", ""); err == nil {
		t.Fatal("ResolveRoute() = nil error for 404 gateway response")
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

// TestResolveRouteConfigUnavailableWrapsErrGatewayUnavailable proves
// D-101 (issue #511): a 503 config_unavailable resolve response must be
// classified as ErrGatewayUnavailable so missions' delegated runner
// retries it instead of treating it as "no delegated route".
func TestResolveRouteConfigUnavailableWrapsErrGatewayUnavailable(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"config_unavailable","message":"routing configuration not loaded yet"}`, http.StatusServiceUnavailable)
	})

	_, err := c.ResolveRoute(t.Context(), "coding", "claude-cli")
	if err == nil {
		t.Fatal("ResolveRoute() = nil error for 503 config_unavailable")
	}
	if !errors.Is(err, ErrGatewayUnavailable) {
		t.Fatalf("ResolveRoute() error = %v, want it to wrap ErrGatewayUnavailable", err)
	}
}

// TestResolveRouteUnreachableWrapsErrGatewayUnavailable proves the same
// classification applies to a pure transport failure (gateway process
// not listening at all), not only an HTTP 503 response.
func TestResolveRouteUnreachableWrapsErrGatewayUnavailable(t *testing.T) {
	t.Parallel()
	c := New("http://127.0.0.1:1") // nothing listens here

	_, err := c.ResolveRoute(t.Context(), "coding", "claude-cli")
	if err == nil {
		t.Fatal("ResolveRoute() = nil error for an unreachable gateway")
	}
	if !errors.Is(err, ErrGatewayUnavailable) {
		t.Fatalf("ResolveRoute() error = %v, want it to wrap ErrGatewayUnavailable", err)
	}
}

// TestResolveRouteNotFoundDoesNotWrapErrGatewayUnavailable proves a
// genuinely definitive "no such route" answer (404) is never mistaken
// for the gateway being unavailable: only that distinction lets
// missions' delegated runner retry the transient case while still
// falling back to native on this one.
func TestResolveRouteNotFoundDoesNotWrapErrGatewayUnavailable(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	})

	_, err := c.ResolveRoute(t.Context(), "no-such-route", "")
	if err == nil {
		t.Fatal("ResolveRoute() = nil error for 404 gateway response")
	}
	if errors.Is(err, ErrGatewayUnavailable) {
		t.Fatalf("ResolveRoute() error = %v, want it NOT to wrap ErrGatewayUnavailable for a 404", err)
	}
}

func TestReadyReportsRoutingCheckStatus(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"degraded","checks":{"routing":{"status":"degraded","detail":"routing configuration not loaded yet"}}}`)
	})

	ready, detail, err := c.Ready(t.Context())
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if ready {
		t.Fatal("Ready() = true, want false for a degraded routing check")
	}
	if detail == "" {
		t.Fatal("Ready() detail is empty, want the routing check's detail")
	}
}

func TestReadyReportsOKWhenRoutingLoaded(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"ok","checks":{"postgres":{"status":"ok"},"routing":{"status":"ok"}}}`)
	})

	ready, _, err := c.Ready(t.Context())
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if !ready {
		t.Fatal("Ready() = false, want true once the routing check is ok")
	}
}

func TestReadyMissingRoutingCheckReportsNotReady(t *testing.T) {
	t.Parallel()
	c := gatewayStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"ok","checks":{"postgres":{"status":"ok"}}}`)
	})

	ready, _, err := c.Ready(t.Context())
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if ready {
		t.Fatal("Ready() = true, want false when an older gateway build reports no routing check at all")
	}
}
