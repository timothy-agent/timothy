package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// These tests pin the runStream orchestration contract directly with
// stub relays: timeout errors deliver on the parent context, retries
// surface before success, and the incomplete tail fires exactly when a
// relay ends unfinished.

func buildFor(url string) func(ctx context.Context) (*http.Request, error) {
	return func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	}
}

func TestRunStreamTimeoutErrorDeliversOnParentCtx(t *testing.T) {
	t.Parallel()
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	t.Cleanup(func() { close(blocked); srv.Close() })

	relayCalled := false
	ch := runStream(t.Context(), &http.Client{}, 100*time.Millisecond, maxRetries, buildFor(srv.URL),
		func(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (bool, error) {
			relayCalled = true
			return true, nil
		})
	events := collect(t, ch)

	if relayCalled {
		t.Fatal("relay ran despite request timeout")
	}
	errs := eventsOfType(events, stream.EventError)
	if len(errs) != 1 || errs[0].Err.Code != "timeout" {
		// The call ctx is expired here by definition; the event must
		// still arrive because terminal events gate on the parent ctx.
		t.Fatalf("want one timeout error event, got %+v", events)
	}
}

func TestRunStreamIncompleteTail(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	tests := []struct {
		name       string
		relayErr   error
		wantReason string
	}{
		{name: "clean early eof", relayErr: nil, wantReason: "stream ended before completion"},
		{name: "read error", relayErr: errors.New("boom"), wantReason: "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ch := runStream(t.Context(), &http.Client{}, time.Second, maxRetries, buildFor(srv.URL),
				func(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (bool, error) {
					return false, tt.relayErr
				})
			events := collect(t, ch)

			inc := eventsOfType(events, stream.EventIncomplete)
			if len(inc) != 1 || inc[0].Text != tt.wantReason {
				t.Fatalf("incomplete = %+v, want reason %q", inc, tt.wantReason)
			}
			if lastType(t, events) != stream.EventDone {
				t.Fatalf("last = %v, want done", lastType(t, events))
			}
		})
	}
}

func TestRunStreamFinishedRelayGetsNoTail(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	ch := runStream(t.Context(), &http.Client{}, time.Second, maxRetries, buildFor(srv.URL),
		func(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (bool, error) {
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventDone})
			return true, nil
		})
	events := collect(t, ch)

	if len(events) != 1 || events[0].Type != stream.EventDone {
		t.Fatalf("want exactly [done], got %+v", events)
	}
}

// TestRunStreamSurvivesSlowButActiveRelay pins the fix for a slow
// CPU-only backend (e.g. remote Ollama) being cut off mid-generation:
// the timeout bounds IDLE gaps, not the call's total wall-clock, so a
// relay that keeps emitting well past `timeout` must still finish
// cleanly as long as no single gap exceeds it.
func TestRunStreamSurvivesSlowButActiveRelay(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	const idle = 40 * time.Millisecond
	ch := runStream(t.Context(), &http.Client{}, idle, maxRetries, buildFor(srv.URL),
		func(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (bool, error) {
			for i := 0; i < 5; i++ {
				time.Sleep(idle / 2)
				if !emit(ctx, ch, stream.StreamEvent{Type: stream.EventChunk, Text: "tok"}) {
					return false, ctx.Err()
				}
			}
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventDone})
			return true, nil
		})
	events := collect(t, ch)

	chunks := eventsOfType(events, stream.EventChunk)
	if len(chunks) != 5 {
		t.Fatalf("want 5 chunks despite total runtime exceeding the idle timeout, got %+v", events)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last = %v, want done", lastType(t, events))
	}
}

// TestRunStreamCutsOffOnRealIdleGap confirms a genuine stall (no
// activity at all) still cancels the call — the watchdog must not
// become a way to hang forever.
func TestRunStreamCutsOffOnRealIdleGap(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	relayReturned := make(chan struct{})
	ch := runStream(t.Context(), &http.Client{}, 30*time.Millisecond, maxRetries, buildFor(srv.URL),
		func(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (bool, error) {
			defer close(relayReturned)
			<-ctx.Done()
			return false, ctx.Err()
		})
	events := collect(t, ch)

	select {
	case <-relayReturned:
	default:
		t.Fatal("relay never observed cancellation")
	}
	inc := eventsOfType(events, stream.EventIncomplete)
	if len(inc) != 1 {
		t.Fatalf("want one incomplete event on genuine stall, got %+v", events)
	}
}

func TestRunStreamRetryEventsPrecedeSuccess(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(srv.Close)

	ch := runStream(t.Context(), &http.Client{}, 5*time.Second, maxRetries, buildFor(srv.URL),
		func(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (bool, error) {
			b, _ := io.ReadAll(body)
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventChunk, Text: string(b)})
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventDone})
			return true, nil
		})
	events := collect(t, ch)

	if len(events) != 3 ||
		events[0].Type != stream.EventRetry || events[0].Retry.Attempt != 1 ||
		events[1].Type != stream.EventChunk || events[1].Text != "payload" ||
		events[2].Type != stream.EventDone {
		t.Fatalf("want [retry chunk done], got %+v", events)
	}
}
