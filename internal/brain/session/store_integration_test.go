//go:build integration

package session

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

func integrationStore(t *testing.T) (*Store, string) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	s := NewStore(pool)
	id, err := s.Create(ctx, "integration test session")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		_, _ = conn.Exec(ctx, "DELETE FROM session_events WHERE session_id = $1", id)
		_, _ = conn.Exec(ctx, "DELETE FROM cost_ledger WHERE session_id = $1", id)
		if _, err := conn.Exec(ctx, "DELETE FROM sessions WHERE id = $1", id); err != nil {
			t.Errorf("cleanup session: %v", err)
		}
	})
	return s, id
}

func TestAppendAssignsGaplessOrderedSeqs(t *testing.T) {
	s, id := integrationStore(t)

	// Concurrent appenders to ONE session must serialize on the row
	// lock: every seq unique, no gaps, no lost writes.
	const writers, each = 4, 5
	var wg sync.WaitGroup
	errs := make([]error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_, err := s.Append(context.Background(), id, KindUserMessage, UserMessage{Text: "m"})
				errs[w*each+i] = err
			}
		}(w)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	events, err := s.Events(t.Context(), id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	// session_started took seq 1; then writers*each appended events.
	if len(events) != writers*each+1 {
		t.Fatalf("events = %d, want %d", len(events), writers*each+1)
	}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("seq gap: events[%d].Seq = %d", i, ev.Seq)
		}
	}
}

func TestEventsRoundTripProjection(t *testing.T) {
	s, id := integrationStore(t)

	if _, err := s.Append(t.Context(), id, KindUserMessage, UserMessage{Text: "hello"}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	var turn AssistantTurn
	turn.LLM.Message = "hi"
	turn.UI.Blocks = []UIBlock{{Type: "text", Text: "hi"}}
	if _, err := s.Append(t.Context(), id, KindAssistantTurn, turn); err != nil {
		t.Fatalf("append turn: %v", err)
	}

	events, err := s.Events(t.Context(), id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	msgs, err := LLMContext(events, 0)
	if err != nil {
		t.Fatalf("LLMContext: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Content != "hello" || msgs[1].Content != "hi" {
		t.Fatalf("round-trip projection = %+v", msgs)
	}
}
