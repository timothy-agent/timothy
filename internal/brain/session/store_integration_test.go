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

// TestListCursorStableOnTiedTimestamps pins the composite-cursor SQL:
// rows sharing an updated_at page without duplicates or gaps because
// the id breaks the tie.
func TestListCursorStableOnTiedTimestamps(t *testing.T) {
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

	const marker = "cursor-tie-test"
	var ids []string
	for i := 0; i < 3; i++ {
		id, err := s.Create(ctx, marker)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		for _, id := range ids {
			_, _ = conn.Exec(cctx, "DELETE FROM session_events WHERE session_id = $1", id)
			_, _ = conn.Exec(cctx, "DELETE FROM sessions WHERE id = $1", id)
		}
	})
	// Force an exact timestamp collision.
	tied := time.Date(2020, 1, 2, 3, 4, 5, 123456000, time.UTC)
	if _, err := db.Exec(ctx,
		"UPDATE sessions SET updated_at = $1 WHERE title = $2", tied, marker); err != nil {
		t.Fatalf("tie timestamps: %v", err)
	}

	page, err := s.List(ctx, marker, time.Time{}, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var mine []Meta
	for _, m := range page {
		if m.Title == marker {
			mine = append(mine, m)
		}
	}
	if len(mine) != 3 {
		t.Fatalf("full page has %d marker rows, want 3", len(mine))
	}

	// Resume from the FIRST tied row: the remaining two must follow,
	// no duplicate of the cursor row, no skips.
	rest, err := s.List(ctx, marker, mine[0].UpdatedAt, mine[0].ID)
	if err != nil {
		t.Fatalf("List cursor: %v", err)
	}
	var restIDs []string
	for _, m := range rest {
		if m.Title == marker {
			restIDs = append(restIDs, m.ID)
		}
	}
	if len(restIDs) != 2 || restIDs[0] != mine[1].ID || restIDs[1] != mine[2].ID {
		t.Fatalf("cursor page = %v, want [%s %s]", restIDs, mine[1].ID, mine[2].ID)
	}
}

// TestListQueryMatchesTitleOnlySession pins the NULL guard in the
// search SQL: a session that has never emitted a user_message (its
// joined payload is NULL) must still be findable by title.
func TestListQueryMatchesTitleOnlySession(t *testing.T) {
	s, id := integrationStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	got, err := s.List(ctx, "integration test session", time.Time{}, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range got {
		if m.ID == id {
			return
		}
	}
	t.Fatalf("title-only session %s missing from query results", id)
}
