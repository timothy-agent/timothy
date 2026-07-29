package chat

import (
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

	bc := s.turnBroadcast("s1")
	if !s.TurnActive("s1") {
		t.Fatal("TurnActive false immediately after turnBroadcast — presence must mean in-flight even with zero subscribers")
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

// TestServiceTurnBroadcastReplacesStaleEntry confirms a fresh
// turnBroadcast call always wins the slot: a new turn must never find
// itself unable to register because turn_active is stuck true from an
// entry whose free lost a race.
func TestServiceTurnBroadcastReplacesStaleEntry(t *testing.T) {
	t.Parallel()
	s := &Service{}
	first := s.turnBroadcast("s1")
	second := s.turnBroadcast("s1")
	if first == second {
		t.Fatal("second turnBroadcast returned the same broadcaster instance")
	}
	if !s.TurnActive("s1") {
		t.Fatal("TurnActive false after re-registering")
	}
}
