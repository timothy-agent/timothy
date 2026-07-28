package missions

import (
	"context"
	"testing"
	"time"
)

func TestHubPublishReachesSubscriber(t *testing.T) {
	t.Parallel()
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := h.Subscribe(ctx)

	h.Publish(Signal{Kind: "mission", ID: "m1"})

	select {
	case sig := <-ch:
		if sig.Kind != "mission" || sig.ID != "m1" {
			t.Fatalf("got signal %+v, want {mission m1}", sig)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber never received the published signal")
	}
}

func TestHubSlowSubscriberDroppedNotBlocked(t *testing.T) {
	t.Parallel()
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := h.Subscribe(ctx)

	// Fill the buffer, then publish once more: Publish must return
	// immediately (never block on a full subscriber) and the extra
	// signal is simply dropped for that subscriber.
	done := make(chan struct{})
	go func() {
		for i := 0; i < subSignalBuf+5; i++ {
			h.Publish(Signal{Kind: "mission", ID: "m1"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a full subscriber instead of dropping the signal")
	}

	// Drain: at most subSignalBuf signals should be queued, not
	// subSignalBuf+5 — proving the overflow was dropped, not buffered.
	drained := 0
	for {
		select {
		case <-ch:
			drained++
		default:
			if drained > subSignalBuf {
				t.Fatalf("drained %d signals, want at most %d (buffer cap)", drained, subSignalBuf)
			}
			return
		}
	}
}

func TestHubCtxCancelUnsubscribes(t *testing.T) {
	t.Parallel()
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	ch := h.Subscribe(ctx)
	cancel()

	// The channel must close once ctx ends — ranging over it (as the
	// SSE handler does) exits instead of hanging forever.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel delivered a value instead of closing on ctx cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel never closed after ctx cancel")
	}

	// A publish after unsubscribe must not panic (send on closed
	// channel) and must not reach this subscriber.
	h.Publish(Signal{Kind: "mission", ID: "m2"})
}
