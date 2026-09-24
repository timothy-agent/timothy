package channels

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunRetriesWhenChannelsCannotBeListed(t *testing.T) {
	var logs syncBuffer
	store := NewStore(pgpool.New(context.Background(), "", nil))
	svc := New(store, nil, fakeResolve, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	svc.retryEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(ctx, nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(logs.String(), "channels: list failed") < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if n := strings.Count(logs.String(), "channels: list failed"); n < 3 {
		t.Fatalf("list failures logged %d times, want retries every retryEvery", n)
	}
	if strings.Contains(logs.String(), "no channels enabled") {
		t.Fatal("a failed list must not claim no channels are enabled")
	}
}

func TestReconcileSkipsTheStoreWhenSwitchedOff(t *testing.T) {
	var logs syncBuffer
	store := NewStore(pgpool.New(context.Background(), "", nil))
	svc := New(store, nil, fakeResolve, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	off := func(context.Context) bool { return false }
	if !svc.reconcile(context.Background(), off) {
		t.Fatal("reconcile with the switch off touched the store")
	}
	svc.reconcile(context.Background(), off)
	if n := strings.Count(logs.String(), "channels: no channels enabled"); n != 1 {
		t.Fatalf("idle line logged %d times, want once", n)
	}
}

func TestStoreChangeKicksReload(t *testing.T) {
	store := NewStore(pgpool.New(context.Background(), "", nil))
	svc := New(store, nil, fakeResolve, nil, discardLog())
	store.onChange(context.Background())
	store.onChange(context.Background())
	select {
	case <-svc.reload:
	default:
		t.Fatal("a store write did not signal a reload")
	}
	select {
	case <-svc.reload:
		t.Fatal("reload signals must coalesce")
	default:
	}
}
