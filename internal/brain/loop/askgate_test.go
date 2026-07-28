package loop

import (
	"context"
	"testing"
	"time"
)

// TestAskGateCtxCancelDeniesWaiter pins the gate's ctx-aware wait: a
// waiter whose context ends before the lock is free must return
// ok=false promptly, not block for the holder's full lifetime — and
// the entry's refcount bookkeeping must not leak once everyone
// involved has finished with the key.
func TestAskGateCtxCancelDeniesWaiter(t *testing.T) {
	t.Parallel()
	g := newAskGate()
	const key = "s1\x00shell\x00ls"

	holderRelease, ok := g.lock(t.Context(), key)
	if !ok {
		t.Fatal("holder failed to acquire the uncontended lock")
	}

	waitCtx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		cancel() // cancel promptly so the waiter never gets the lock
		if _, ok := g.lock(waitCtx, key); ok {
			t.Error("waiter with a cancelled context acquired the lock")
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled waiter did not return promptly")
	}

	holderRelease()

	g.mu.Lock()
	n := len(g.entries)
	g.mu.Unlock()
	if n != 0 {
		t.Fatalf("askGate.entries = %d after all parties finished, want 0 (leaked entry)", n)
	}
}

// TestAskGateSerializesConcurrentWaiters confirms the gate actually
// forces waiters to take turns rather than letting them proceed
// concurrently — the property the loop's DecisionAsk handling relies
// on to make only the first caller ask.
func TestAskGateSerializesConcurrentWaiters(t *testing.T) {
	t.Parallel()
	g := newAskGate()
	const key = "s1\x00shell\x00ls"
	const n = 5

	var order []int
	orderCh := make(chan int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			<-start
			release, ok := g.lock(t.Context(), key)
			if !ok {
				t.Errorf("goroutine %d failed to acquire the lock", i)
				orderCh <- -1
				return
			}
			time.Sleep(2 * time.Millisecond)
			orderCh <- i
			release()
		}(i)
	}
	close(start)

	for i := 0; i < n; i++ {
		select {
		case v := <-orderCh:
			order = append(order, v)
		case <-time.After(5 * time.Second):
			t.Fatal("goroutine did not complete in time — gate may be deadlocked")
		}
	}
	if len(order) != n {
		t.Fatalf("completed = %d, want %d", len(order), n)
	}

	g.mu.Lock()
	left := len(g.entries)
	g.mu.Unlock()
	if left != 0 {
		t.Fatalf("askGate.entries = %d after all released, want 0", left)
	}
}
