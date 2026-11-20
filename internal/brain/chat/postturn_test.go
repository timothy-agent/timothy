package chat

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// TestPostTurnWorkSerializedPerSession pins D-175 per-session FIFO
// ordering: a second turn on the same session must not start its
// post-turn work until the first turn's post-turn work finishes, while
// a different session runs independently.
func TestPostTurnWorkSerializedPerSession(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("answer")}

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	otherDone := make(chan struct{})

	var mu sync.Mutex
	var order []string
	var activeS1 atomic.Int32
	var maxActiveS1 atomic.Int32

	distill := func(ctx context.Context, sessionID, turnText, _ string) *session.TurnMemory {
		if sessionID == "s1" {
			cur := activeS1.Add(1)
			for {
				prev := maxActiveS1.Load()
				if cur <= prev || maxActiveS1.CompareAndSwap(prev, cur) {
					break
				}
			}
			defer activeS1.Add(-1)

			if strings.Contains(turnText, "first") {
				close(firstStarted)
				select {
				case <-firstRelease:
				case <-ctx.Done():
				}
			}
		}

		mu.Lock()
		order = append(order, sessionID+":"+turnText)
		mu.Unlock()

		if sessionID == "s2" {
			close(otherDone)
		}
		return &session.TurnMemory{KeyFindings: []string{sessionID}}
	}

	svc := New(gw, log, distill, nil, staticBudget(60_000), nil, nil, nil, nil, discard())
	t.Cleanup(func() { svc.Shutdown(context.Background()) })

	_, ch1, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "first"})
	if err != nil {
		t.Fatalf("Chat 1: %v", err)
	}
	drain(t, ch1)
	waitFor(t, func() bool { return !svc.TurnActive("s1") })

	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first distill never started")
	}

	// Second turn on s1 finishes streaming while first's post-turn is
	// still blocked; its post-turn job must wait in s1's queue.
	_, ch2, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "second"})
	if err != nil {
		t.Fatalf("Chat 2: %v", err)
	}
	drain(t, ch2)
	waitFor(t, func() bool { return !svc.TurnActive("s1") })

	// Meanwhile s2's post-turn work must run without waiting for s1.
	_, chOther, err := svc.Chat(t.Context(), Request{SessionID: "s2", Message: "other"})
	if err != nil {
		t.Fatalf("Chat s2: %v", err)
	}
	drain(t, chOther)
	select {
	case <-otherDone:
	case <-time.After(5 * time.Second):
		t.Fatal("s2 post-turn work blocked behind s1")
	}

	mu.Lock()
	for _, entry := range order {
		if strings.HasPrefix(entry, "s1:") {
			mu.Unlock()
			t.Fatalf("s1 post-turn work ran before first finished: %v", order)
		}
	}
	mu.Unlock()

	close(firstRelease)
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 3
	})

	if maxActiveS1.Load() != 1 {
		t.Fatalf("max concurrent post-turn jobs for s1 = %d, want 1", maxActiveS1.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	var s1Order []string
	for _, entry := range order {
		if strings.HasPrefix(entry, "s1:") {
			s1Order = append(s1Order, entry)
		}
	}
	if len(s1Order) != 2 || !strings.Contains(s1Order[0], "first") || !strings.Contains(s1Order[1], "second") {
		t.Fatalf("s1 post-turn order = %v, want [first, second]", s1Order)
	}
}

// blockingCompactor coordinates test compaction calls with channels.
type blockingCompactor struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	log     *fakeLog
}

func (c *blockingCompactor) MaybeCompact(ctx context.Context, sessionID string) error {
	c.mu.Lock()
	c.calls++
	call := c.calls
	started := c.started
	release := c.release
	c.mu.Unlock()

	if started != nil && call == 2 {
		// Call 1 is turn 1's pre-send pass; call 2 is turn 1's post-turn pass.
		close(started)
	}
	if release != nil && call == 2 {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if c.log != nil {
			events, _ := c.log.Events(ctx, sessionID)
			boundary := events[len(events)-1].Seq
			_, err := c.log.Append(ctx, sessionID, session.KindCompactionApplied, session.CompactionApplied{
				Summary: "compacted turn 1", ReplacesThroughSeq: boundary,
			})
			return err
		}
	}
	return nil
}

// TestPostTurnCompactionSkipsWhenNewTurnActive verifies that a
// delayed post-turn job does not run MaybeCompact while a follow-up turn
// is already active on the same session.
func TestPostTurnCompactionSkipsWhenNewTurnActive(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	gw := &fakeGW{events: okEvents("first answer")}

	distillStarted := make(chan struct{})
	distillRelease := make(chan struct{})
	distill := func(ctx context.Context, sessionID, turnText, _ string) *session.TurnMemory {
		if strings.Contains(turnText, "first") {
			close(distillStarted)
			select {
			case <-distillRelease:
			case <-ctx.Done():
			}
		}
		return nil
	}

	comp := &blockingCompactor{}
	svc := New(gw, log, distill, comp, staticBudget(60_000), nil, nil, nil, nil, discard())
	t.Cleanup(func() { svc.Shutdown(context.Background()) })

	_, ch1, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "first"})
	if err != nil {
		t.Fatalf("Chat 1: %v", err)
	}
	drain(t, ch1)
	waitFor(t, func() bool { return !svc.TurnActive("s1") })

	select {
	case <-distillStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("distill never started")
	}

	// Start turn 2 and hold its stream open so TurnActive("s1") stays true.
	block := make(chan struct{})
	gw.mu.Lock()
	gw.events = okEvents("second answer")
	gw.blockCh = block
	gw.mu.Unlock()

	_, ch2, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "second"})
	if err != nil {
		t.Fatalf("Chat 2: %v", err)
	}
	waitFor(t, func() bool { return svc.TurnActive("s1") })

	// Turn 1's pre-send + turn 2's pre-send = 2 calls so far.
	comp.mu.Lock()
	callsBefore := comp.calls
	comp.mu.Unlock()
	if callsBefore != 2 {
		t.Fatalf("compactor calls while turn 2 active = %d, want 2", callsBefore)
	}

	// Release turn 1's distill while turn 2 is still active; turn 1's
	// postCompact must skip MaybeCompact rather than touch turn 2.
	close(distillRelease)
	// Let turn 1's post-turn job drain by ending turn 2 only after
	// confirming no extra MaybeCompact ran during the active window.
	time.Sleep(50 * time.Millisecond)
	comp.mu.Lock()
	callsDuring := comp.calls
	comp.mu.Unlock()
	if callsDuring != 2 {
		t.Fatalf("post-turn compaction ran while turn 2 was active: calls = %d, want 2", callsDuring)
	}

	close(block)
	drain(t, ch2)
	waitFor(t, func() bool { return !svc.TurnActive("s1") })
	// Turn 2's own post-turn job compacts now that no turn is active.
	waitFor(t, func() bool {
		comp.mu.Lock()
		defer comp.mu.Unlock()
		return comp.calls == 3
	})
}

// TestNewTurnWaitsForRunningBackgroundCompaction verifies that when a
// post-turn compaction is already running, a follow-up Chat waits for it
// before appending its user_message and projects the compacted context.
func TestNewTurnWaitsForRunningBackgroundCompaction(t *testing.T) {
	t.Parallel()
	log := newFakeLog()
	log.titles["s1"] = "seeded"
	gw := &fakeGW{events: okEvents("first answer")}

	comp := &blockingCompactor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		log:     log,
	}
	svc := New(gw, log, nil, comp, staticBudget(60_000), nil, nil, nil, nil, discard())
	t.Cleanup(func() { svc.Shutdown(context.Background()) })

	_, ch1, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "first question"})
	if err != nil {
		t.Fatalf("Chat 1: %v", err)
	}
	drain(t, ch1)
	waitFor(t, func() bool { return !svc.TurnActive("s1") })

	select {
	case <-comp.started:
	case <-time.After(5 * time.Second):
		t.Fatal("background compaction never started")
	}

	gw.mu.Lock()
	gw.events = okEvents("second answer")
	gw.mu.Unlock()

	type chatRes struct {
		ch  <-chan stream.StreamEvent
		err error
	}
	resCh := make(chan chatRes, 1)
	go func() {
		_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "second question"})
		resCh <- chatRes{ch, err}
	}()

	// While background compaction is blocked, Chat 2 must not have
	// appended its user_message yet.
	time.Sleep(50 * time.Millisecond)
	for _, ev := range log.eventsSlice("s1") {
		if ev.Kind == session.KindUserMessage && strings.Contains(string(ev.Payload), "second question") {
			t.Fatal("Chat 2 appended user_message while background compaction was still running")
		}
	}

	close(comp.release)
	var res chatRes
	select {
	case res = <-resCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Chat 2 never returned after compaction released")
	}
	if res.err != nil {
		t.Fatalf("Chat 2: %v", res.err)
	}
	drain(t, res.ch)

	// Turn 2 must have seen the summary from turn 1's compaction and not
	// turn 1's raw text.
	msgs := gw.lastChatRequest().Messages
	if len(msgs) != 2 || !strings.Contains(msgs[0].Content, "compacted turn 1") || msgs[1].Content != "second question" {
		t.Fatalf("turn 2 wire messages = %+v, want [summary, second question]", msgs)
	}
}

func (f *fakeLog) eventsSlice(id string) []session.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]session.Event(nil), f.events[id]...)
}

// TestShutdownDrainsAndCancelsPostTurnWork covers both shutdown cases:
// queued work finishing inside the drain budget, and stalled work
// receiving cancellation when the drain deadline expires.
func TestShutdownDrainsAndCancelsPostTurnWork(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		drainBudget    time.Duration
		releaseBefore  bool
		wantCanceled   bool
		wantTurnMemory bool
	}{
		{
			name:           "drains in-flight work before returning",
			drainBudget:    5 * time.Second,
			releaseBefore:  true,
			wantCanceled:   false,
			wantTurnMemory: true,
		},
		{
			name:           "cancels stalled work when drain budget expires",
			drainBudget:    30 * time.Millisecond,
			releaseBefore:  false,
			wantCanceled:   true,
			wantTurnMemory: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log := newFakeLog()
			gw := &fakeGW{events: okEvents("answer")}

			started := make(chan struct{})
			release := make(chan struct{})
			canceled := make(chan bool, 1)
			var once sync.Once
			distill := func(ctx context.Context, _, _, _ string) *session.TurnMemory {
				once.Do(func() { close(started) })
				select {
				case <-release:
					canceled <- false
					return &session.TurnMemory{KeyFindings: []string{"saved"}}
				case <-ctx.Done():
					canceled <- true
					return nil
				}
			}

			svc := New(gw, log, distill, nil, staticBudget(60_000), nil, nil, nil, nil, discard())
			_, ch, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "hi"})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			drain(t, ch)
			waitFor(t, func() bool { return !svc.TurnActive("s1") })

			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("distill never started")
			}

			if tc.releaseBefore {
				go func() {
					time.Sleep(20 * time.Millisecond)
					close(release)
				}()
			}

			sctx, scancel := context.WithTimeout(context.Background(), tc.drainBudget)
			defer scancel()
			svc.Shutdown(sctx)

			select {
			case got := <-canceled:
				if got != tc.wantCanceled {
					t.Fatalf("canceled = %v, want %v", got, tc.wantCanceled)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("distill never exited")
			}

			hasTM := slices.Contains(log.kinds("s1"), session.KindTurnMemory)
			if hasTM != tc.wantTurnMemory {
				t.Fatalf("has turn_memory = %v, want %v", hasTM, tc.wantTurnMemory)
			}

			// Post-shutdown turns still complete and persist assistant_turn
			// without spawning leaked post-turn work.
			gw.mu.Lock()
			gw.events = okEvents("after shutdown")
			gw.mu.Unlock()
			_, ch2, err := svc.Chat(t.Context(), Request{SessionID: "s1", Message: "after"})
			if err != nil {
				t.Fatalf("Chat after shutdown: %v", err)
			}
			drain(t, ch2)
			waitFor(t, func() bool { return !svc.TurnActive("s1") })
		})
	}
}
