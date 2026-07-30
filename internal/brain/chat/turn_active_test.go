package chat

import (
	"errors"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// TestChatTurnActiveDuringThenFalseAfter confirms TurnActive tracks a
// real Chat() turn's lifecycle end to end: true the instant the turn
// is registered (before the blocked gateway yields anything), false
// only once the turn's terminal persist has actually run.
func TestChatTurnActiveDuringThenFalseAfter(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	block := make(chan struct{})
	gw := &fakeGW{events: okEvents("the answer"), blockCh: block}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "the question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	waitFor(t, func() bool { return s.TurnActive("s1") })

	close(block) // let the stream complete
	drain(t, ch)

	waitFor(t, func() bool { return !s.TurnActive("s1") })
	// The transcript must already be durable by the time the flag
	// drops — no window where turn_active is false but a refetch would
	// still see the old (interrupted) state.
	waitFor(t, func() bool { return len(log.kinds("s1")) == 3 })
}

// TestChatRetryRegistersBroadcasterSameAsChat confirms Retry follows
// the identical lifecycle as Chat — both funnel through runTurn, but
// this pins that behavior explicitly since Retry is a separate public
// entry point.
func TestChatRetryRegistersBroadcasterSameAsChat(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("first")}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "the question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return !s.TurnActive("s1") })

	block := make(chan struct{})
	gw.mu.Lock()
	gw.events = okEvents("second")
	gw.blockCh = block
	gw.mu.Unlock()

	// Retry needs a dangling user_message with no completed turn after
	// it; force that shape directly rather than depending on internal
	// persistTurn timing.
	if _, err := log.Append(t.Context(), "s1", "user_message", map[string]string{"text": "retry me"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	_, retryCh, err := s.Retry(t.Context(), "s1")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	waitFor(t, func() bool { return s.TurnActive("s1") })
	if _, _, ok := s.Subscribe("s1"); !ok {
		t.Fatal("Subscribe ok=false during a Retry-started turn")
	}

	close(block)
	drain(t, retryCh)
	waitFor(t, func() bool { return !s.TurnActive("s1") })
}

// TestChatSubscribeMidTurnGetsBufferedPrefixThenLiveTail is the core
// Tier-2 contract: a subscriber attaching after some events already
// happened sees exactly the buffered prefix once, then the rest of the
// turn live, then the channel closes at the terminal — no gap, no
// duplication.
func TestChatSubscribeMidTurnGetsBufferedPrefixThenLiveTail(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "one"},
		{Type: stream.EventChunk, Text: "two"},
		{Type: stream.EventChunk, Text: "three"},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod"}},
	}}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// Drain the first event off the client-facing channel so the relay
	// has definitely published at least one event to the broadcaster
	// before this subscribes — a real "mid-turn" attach.
	first := <-ch
	if first.Text != "one" {
		t.Fatalf("first event = %+v, want text=one", first)
	}

	replay, live, ok := s.Subscribe("s1")
	if !ok {
		t.Fatal("Subscribe ok=false while turn in flight")
	}
	if len(replay) == 0 || replay[0].Text != "one" {
		t.Fatalf("replay = %+v, want to start with the already-published %q event", replay, "one")
	}
	// The relay races ahead of this test goroutine, so replay may have
	// captured more than just "one" by the time Subscribe's lock was
	// taken — that's fine (no gap), as long as whatever it captured
	// never repeats on live afterward. Record exactly the replay
	// contents so the live-side assertion below can check against it,
	// rather than assuming a fixed split point.
	inReplay := map[string]bool{}
	for _, ev := range replay {
		inReplay[ev.Text] = true
	}

	// Drain the rest of the client-facing stream so the turn completes.
	for range ch {
	}

	// The broadcaster's live channel must deliver every remaining event
	// exactly once (never one already in replay) and then close.
	var texts []string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, chOk := <-live:
			if !chOk {
				goto done
			}
			if ev.Text != "" {
				texts = append(texts, ev.Text)
			}
		case <-deadline:
			t.Fatal("live channel never closed")
		}
	}
done:
	seen := map[string]int{}
	for _, tx := range texts {
		seen[tx]++
	}
	for text, n := range seen {
		if inReplay[text] {
			t.Fatalf("live delivered %q again after it was already in replay", text)
		}
		if n != 1 {
			t.Fatalf("live delivered %q %d times, want exactly once", text, n)
		}
	}
	// Every event not captured in replay must show up exactly once live
	// — nothing silently dropped between replay and live.
	all := []string{"one", "two", "three"}
	for _, text := range all {
		if inReplay[text] {
			continue
		}
		if seen[text] != 1 {
			t.Fatalf("event %q missing from live (seen=%+v, inReplay=%+v)", text, seen, inReplay)
		}
	}
}

// TestChatSessionHubPublishesOnlyAfterTerminalPersist confirms the
// session signal fires after the turn's terminal event is durable in
// the log — mirroring missions' own publish-after-commit discipline —
// not merely after the stream's channel closes to the caller.
func TestChatSessionHubPublishesOnlyAfterTerminalPersist(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("the answer")}
	s := newService(gw, log)

	published := make(chan string, 1)
	s.SetSessionHub(func(sessionID string) { published <- sessionID })

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "the question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)

	select {
	case id := <-published:
		if id != "s1" {
			t.Fatalf("published session id = %q, want s1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session hub was never published to")
	}
	// By the time the publish fires, the assistant turn must already be
	// durable — otherwise a client reacting to the signal could refetch
	// and still see the stale (interrupted) state.
	kinds := log.kinds("s1")
	if len(kinds) == 0 || kinds[len(kinds)-1] != "assistant_turn" {
		t.Fatalf("kinds = %v, want the last one to be assistant_turn by the time the signal published", kinds)
	}
}

// TestChatSessionHubNilIsNoop confirms an unwired hub (nil
// publishSession, today's default and every other test's setup) never
// panics — the same nil-safe convention every other optional Service
// hook in this package already follows.
func TestChatSessionHubNilIsNoop(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("the answer")}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "the question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return !s.TurnActive("s1") })
}

// TestConcurrentChatOnSameSessionExactlyOneProceeds pins the D-042
// exclusivity guarantee: two Chat() calls racing on the same session
// must not both proceed — exactly one wins the slot and streams, the
// other gets ErrTurnInFlight and appends ZERO events to the log
// (neither its user_message nor anything else), since the loser must
// never touch the append-only log at all.
func TestConcurrentChatOnSameSessionExactlyOneProceeds(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	block := make(chan struct{})
	gw := &fakeGW{events: okEvents("the answer"), blockCh: block}
	s := newService(gw, log)

	// Start the first turn and wait for it to actually register before
	// racing the second — this pins "one winner" deterministically
	// rather than depending on goroutine scheduling to decide it.
	_, ch1, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "first"})
	if err != nil {
		t.Fatalf("Chat 1: %v", err)
	}
	waitFor(t, func() bool { return s.TurnActive("s1") })

	_, ch2, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "second"})
	if !errors.Is(err, ErrTurnInFlight) {
		t.Fatalf("Chat 2 (racing): err = %v, want ErrTurnInFlight", err)
	}
	if ch2 != nil {
		t.Fatal("Chat 2 (racing): channel should be nil on ErrTurnInFlight")
	}
	// Only session_started + the winner's user_message so far — the
	// loser must not have appended anything, not even its own
	// user_message.
	if kinds := log.kinds("s1"); len(kinds) != 2 {
		t.Fatalf("kinds after losing race = %v, want exactly 2 (session_started, winner's user_message)", kinds)
	}

	close(block)
	drain(t, ch1)
	waitFor(t, func() bool { return !s.TurnActive("s1") })
	// The winner's turn completes normally: session_started,
	// user_message, assistant_turn — still nothing from the loser.
	if kinds := log.kinds("s1"); len(kinds) != 3 {
		t.Fatalf("kinds after winner completes = %v, want exactly 3", kinds)
	}
}

// TestConcurrentChatAndRetrySameGuarantee confirms the exclusivity
// guard applies identically across Chat and Retry: a Retry racing an
// in-flight Chat-started turn on the same session must lose and touch
// nothing, same as two Chat calls racing each other.
func TestConcurrentChatAndRetrySameGuarantee(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	block := make(chan struct{})
	gw := &fakeGW{events: okEvents("the answer"), blockCh: block}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "first"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	waitFor(t, func() bool { return s.TurnActive("s1") })

	if _, retryCh, err := s.Retry(t.Context(), "s1"); !errors.Is(err, ErrTurnInFlight) {
		t.Fatalf("Retry racing an in-flight Chat turn: err = %v, want ErrTurnInFlight", err)
	} else if retryCh != nil {
		t.Fatal("Retry racing an in-flight Chat turn: channel should be nil on ErrTurnInFlight")
	}
	if kinds := log.kinds("s1"); len(kinds) != 2 {
		t.Fatalf("kinds after Retry lost the race = %v, want exactly 2", kinds)
	}

	close(block)
	drain(t, ch)
	waitFor(t, func() bool { return !s.TurnActive("s1") })
}

// TestTurnBeginAfterErrorDoesNotWedgeSession confirms turnDone still
// runs on an abnormal end (stream error, no EventDone): the cleanup-
// on-every-exit-path guarantee that keeps a bug in one turn from
// permanently 409-ing every later request against the same session.
func TestTurnBeginAfterErrorDoesNotWedgeSession(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: []stream.StreamEvent{
		{Type: stream.EventError, Err: &stream.StreamError{Code: "boom", Message: "boom"}},
	}}
	s := newService(gw, log)

	_, ch, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "first"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, ch)
	waitFor(t, func() bool { return !s.TurnActive("s1") })

	gw.mu.Lock()
	gw.events = okEvents("second answer")
	gw.mu.Unlock()

	_, ch2, err := s.Chat(t.Context(), Request{SessionID: "s1", Message: "second"})
	if err != nil {
		t.Fatalf("Chat after a prior errored turn: %v, want the slot free for a new turn", err)
	}
	drain(t, ch2)
	waitFor(t, func() bool { return !s.TurnActive("s1") })
}
