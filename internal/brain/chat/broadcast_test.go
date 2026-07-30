package chat

import (
	"errors"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func TestTurnBroadcasterReplayThenLiveExactlyOnce(t *testing.T) {
	t.Parallel()
	b := newTurnBroadcaster()

	// Two events land before anyone subscribes.
	b.publish(stream.StreamEvent{Type: stream.EventChunk, Text: "a"})
	b.publish(stream.StreamEvent{Type: stream.EventChunk, Text: "b"})

	replay, live := b.subscribe()
	if len(replay) != 2 || replay[0].Text != "a" || replay[1].Text != "b" {
		t.Fatalf("replay = %+v, want [a b]", replay)
	}

	// A third event, published AFTER subscribe, must arrive on live —
	// not duplicated into a second replay copy.
	b.publish(stream.StreamEvent{Type: stream.EventChunk, Text: "c"})

	select {
	case ev := <-live:
		if ev.Text != "c" {
			t.Fatalf("live event = %q, want %q", ev.Text, "c")
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber never received the live event")
	}

	// Nothing else should be queued — "a"/"b" arrived exactly once, via
	// replay, never re-delivered on the live channel.
	select {
	case ev, ok := <-live:
		t.Fatalf("unexpected extra event on live channel: %+v (ok=%v)", ev, ok)
	default:
	}
}

func TestTurnBroadcasterSlowSubscriberDroppedNotStalled(t *testing.T) {
	t.Parallel()
	b := newTurnBroadcaster()
	_, live := b.subscribe() // never drained — a permanently slow subscriber

	done := make(chan struct{})
	go func() {
		for i := 0; i < broadcastBuf+10; i++ {
			b.publish(stream.StreamEvent{Type: stream.EventChunk, Text: "x"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on a full subscriber instead of dropping it")
	}

	// The subscriber must have been dropped (channel closed) once its
	// buffer filled — publish never blocks the turn, but a subscriber
	// that can't keep up gets cut loose rather than silently lagging
	// forever.
	deadline := time.Now().Add(time.Second)
	for {
		select {
		case _, ok := <-live:
			if !ok {
				return // closed, as expected
			}
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("dropped subscriber's channel was never closed")
		}
	}
}

func TestTurnBroadcasterCloseAllClosesSubscribers(t *testing.T) {
	t.Parallel()
	b := newTurnBroadcaster()
	_, live1 := b.subscribe()
	_, live2 := b.subscribe()

	b.closeAll()

	for i, ch := range []<-chan stream.StreamEvent{live1, live2} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatalf("subscriber %d: channel delivered a value instead of closing", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: channel never closed", i)
		}
	}
}

func TestServiceTurnLifecycleActiveThenFreed(t *testing.T) {
	t.Parallel()
	s := &Service{}

	if s.TurnActive("s1") {
		t.Fatal("TurnActive true before any turn registered")
	}

	bc, err := s.turnBegin("s1")
	if err != nil {
		t.Fatalf("turnBegin: %v", err)
	}
	if !s.TurnActive("s1") {
		t.Fatal("TurnActive false immediately after turnBegin — presence must mean in-flight even with zero subscribers")
	}
	if _, _, ok := s.Subscribe("s1"); !ok {
		t.Fatal("Subscribe ok=false while a turn is registered")
	}
	_ = bc

	s.turnDone("s1")
	if s.TurnActive("s1") {
		t.Fatal("TurnActive true after turnDone freed the entry")
	}
	if _, _, ok := s.Subscribe("s1"); ok {
		t.Fatal("Subscribe ok=true after the turn was freed")
	}
}

// TestServiceTurnBeginExclusiveThenFreedAllowsNext confirms the new
// exclusivity contract (D-042): a second turnBegin call while an entry
// is live must fail with ErrTurnInFlight rather than evicting it (the
// old turnBroadcast's "always install fresh" behavior) — and once the
// live entry is freed via turnDone, a fresh turnBegin must succeed
// again, so a completed turn never permanently wedges the slot.
func TestServiceTurnBeginExclusiveThenFreedAllowsNext(t *testing.T) {
	t.Parallel()
	s := &Service{}
	first, err := s.turnBegin("s1")
	if err != nil {
		t.Fatalf("first turnBegin: %v", err)
	}
	if _, err := s.turnBegin("s1"); !errors.Is(err, ErrTurnInFlight) {
		t.Fatalf("second turnBegin while live: err = %v, want ErrTurnInFlight", err)
	}
	if !s.TurnActive("s1") {
		t.Fatal("TurnActive false while the first entry is still live")
	}
	_ = first

	s.turnDone("s1")
	second, err := s.turnBegin("s1")
	if err != nil {
		t.Fatalf("turnBegin after turnDone: %v", err)
	}
	if first == second {
		t.Fatal("turnBegin after turnDone returned the same broadcaster instance")
	}
	if !s.TurnActive("s1") {
		t.Fatal("TurnActive false after re-registering post-free")
	}
}
