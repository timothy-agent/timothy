//go:build integration

package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
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

	// Sweep leftovers from a crashed run (its cleanup never registered).
	_, _ = db.Exec(ctx, `DELETE FROM session_events WHERE session_id IN
		(SELECT id FROM sessions WHERE title IN ('integration test session', 'cursor-tie-test'))`)
	_, _ = db.Exec(ctx,
		"DELETE FROM sessions WHERE title IN ('integration test session', 'cursor-tie-test')")

	s := NewStore(pool, log)
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

func TestDeleteRemovesSessionScopedRecords(t *testing.T) {
	s, id := integrationStore(t)
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Populate every session-scoped table Delete must clear.
	if _, err := s.Append(ctx, id, KindUserMessage, UserMessage{Text: "doomed turn"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO session_grants (session_id, tool, pattern, expires) VALUES ($1, 'shell', '*', now() + interval '1 hour')`,
		`INSERT INTO tool_audit (session_id, tool, args_digest, status, duration_ms) VALUES ($1, 'shell', 'x', 'ok', 1)`,
		`INSERT INTO tool_outputs (session_id, tool, content) VALUES ($1, 'shell', 'out')`,
	} {
		if _, err := db.Exec(ctx, stmt, id); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM sessions WHERE id = $1`,
		`SELECT count(*) FROM session_events WHERE session_id = $1`,
		`SELECT count(*) FROM session_grants WHERE session_id = $1`,
		`SELECT count(*) FROM tool_audit WHERE session_id = $1`,
		`SELECT count(*) FROM tool_outputs WHERE session_id = $1`,
	} {
		var n int
		if err := db.QueryRow(ctx, q, id).Scan(&n); err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if n != 0 {
			t.Fatalf("%q = %d rows after delete, want 0", q, n)
		}
	}

	// Second delete: the session is gone.
	if err := s.Delete(ctx, id); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("re-delete err = %v, want not found", err)
	}
}

func TestDeleteRefusesMissionSession(t *testing.T) {
	s, id := integrationStore(t)
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	var missionID string
	if err := db.QueryRow(ctx, `INSERT INTO missions (goal, kind, session_id)
		VALUES ('itest-delete-guard', 'general', $1) RETURNING id`, id).Scan(&missionID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		_, _ = conn.Exec(ctx, `DELETE FROM mission_events WHERE mission_id = $1`, missionID)
		_, _ = conn.Exec(ctx, `DELETE FROM missions WHERE id = $1`, missionID)
	})

	if err := s.Delete(ctx, id); !errors.Is(err, ErrMissionReferenced) {
		t.Fatalf("Delete err = %v, want ErrMissionReferenced", err)
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE id = $1`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("session rows = %d (%v), want 1 — refusal must not delete", n, err)
	}
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
	s := NewStore(pool, log)

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

// TestListExcludesMissionSessions: mission bookkeeping sessions
// (missions.session_id) exist only so tool audit has a session FK.
// They are not chat and must never appear in the list — with or
// without a search query.
func TestListExcludesMissionSessions(t *testing.T) {
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
	s := NewStore(pool, log)

	const marker = "mission-session-list-test"
	chatID, err := s.Create(ctx, marker)
	if err != nil {
		t.Fatalf("Create chat: %v", err)
	}
	missionSessionID, err := s.Create(ctx, marker)
	if err != nil {
		t.Fatalf("Create mission session: %v", err)
	}
	var missionID string
	if err := db.QueryRow(ctx,
		"INSERT INTO missions (goal, kind, session_id) VALUES ($1, 'general', $2) RETURNING id",
		marker, missionSessionID).Scan(&missionID); err != nil {
		t.Fatalf("insert mission: %v", err)
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
		_, _ = conn.Exec(cctx, "DELETE FROM missions WHERE id = $1", missionID)
		for _, id := range []string{chatID, missionSessionID} {
			_, _ = conn.Exec(cctx, "DELETE FROM session_events WHERE session_id = $1", id)
			_, _ = conn.Exec(cctx, "DELETE FROM sessions WHERE id = $1", id)
		}
	})

	for _, query := range []string{"", marker} {
		got, err := s.List(ctx, query, time.Time{}, "")
		if err != nil {
			t.Fatalf("List(%q): %v", query, err)
		}
		var sawChat, sawMission bool
		for _, m := range got {
			switch m.ID {
			case chatID:
				sawChat = true
			case missionSessionID:
				sawMission = true
			}
		}
		if sawMission {
			t.Fatalf("List(%q) returned the mission bookkeeping session", query)
		}
		if !sawChat && query != "" {
			t.Fatalf("List(%q) lost the ordinary chat session", query)
		}
	}
}

// TestPendingPermissions pins the SQL: an unresolved permission_request
// in a scoped session is returned with its session's title; a request
// with a matching permission_resolved (same id) is excluded; and a
// session outside sessionIDs is excluded even with an unresolved
// request of its own — the "active turns only" scoping the API handler
// relies on chat.Service.ActiveSessions() for.
func TestPendingPermissions(t *testing.T) {
	s, id := integrationStore(t)
	ctx := t.Context()

	otherID, err := s.Create(ctx, "other session, not scoped in")
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		_, _ = conn.Exec(cctx, "DELETE FROM session_events WHERE session_id = $1", otherID)
		_, _ = conn.Exec(cctx, "DELETE FROM sessions WHERE id = $1", otherID)
	})

	// id: one resolved request (must be excluded) and one still
	// unresolved (must show up).
	if _, err := s.Append(ctx, id, KindPermissionRequest, PermissionRequest{
		ID: "resolved-1", CallID: "call-1", Tool: "shell", Rationale: "run tests",
	}); err != nil {
		t.Fatalf("append resolved request: %v", err)
	}
	if _, err := s.Append(ctx, id, KindPermissionResolved, PermissionResolved{
		ID: "resolved-1", Decision: "once",
	}); err != nil {
		t.Fatalf("append resolved: %v", err)
	}
	if _, err := s.Append(ctx, id, KindPermissionRequest, PermissionRequest{
		ID: "pending-1", CallID: "call-2", Tool: "gmail_search", Rationale: "read inbox",
	}); err != nil {
		t.Fatalf("append pending request: %v", err)
	}

	// otherID: unresolved too, but deliberately left out of sessionIDs.
	if _, err := s.Append(ctx, otherID, KindPermissionRequest, PermissionRequest{
		ID: "zombie-1", CallID: "call-3", Tool: "shell", Rationale: "stranded by a crash",
	}); err != nil {
		t.Fatalf("append other request: %v", err)
	}

	got, err := s.PendingPermissions(ctx, []string{id})
	if err != nil {
		t.Fatalf("PendingPermissions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("pending = %+v, want exactly one entry", got)
	}
	p := got[0]
	if p.SessionID != id || p.SessionTitle != "integration test session" ||
		p.Tool != "gmail_search" || p.Rationale != "read inbox" {
		t.Fatalf("pending[0] = %+v, want the still-unresolved gmail_search request", p)
	}

	// Empty scope: no query, no rows, ever.
	if got, err := s.PendingPermissions(ctx, nil); err != nil || got != nil {
		t.Fatalf("PendingPermissions(nil) = %v, %v; want nil, nil", got, err)
	}
}

// TestKnowledgeRoundTrip pins AddKnowledge's union semantics and
// SetKnowledge's outright replace, including clearing back to empty.
func TestKnowledgeRoundTrip(t *testing.T) {
	s, id := integrationStore(t)
	ctx := t.Context()

	got, err := s.Knowledge(ctx, id)
	if err != nil {
		t.Fatalf("Knowledge (initial): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("initial Knowledge = %v, want empty", got)
	}

	if err := s.AddKnowledge(ctx, id, []string{"a", "b"}); err != nil {
		t.Fatalf("AddKnowledge a,b: %v", err)
	}
	if err := s.AddKnowledge(ctx, id, []string{"b", "c"}); err != nil {
		t.Fatalf("AddKnowledge b,c: %v", err)
	}
	got, err = s.Knowledge(ctx, id)
	if err != nil {
		t.Fatalf("Knowledge (after adds): %v", err)
	}
	sort.Strings(got)
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("Knowledge after adds = %v, want [a b c]", got)
	}

	if err := s.SetKnowledge(ctx, id, []string{"x"}); err != nil {
		t.Fatalf("SetKnowledge: %v", err)
	}
	got, err = s.Knowledge(ctx, id)
	if err != nil {
		t.Fatalf("Knowledge (after set): %v", err)
	}
	if !slices.Equal(got, []string{"x"}) {
		t.Fatalf("Knowledge after SetKnowledge = %v, want [x]", got)
	}

	if err := s.SetKnowledge(ctx, id, nil); err != nil {
		t.Fatalf("SetKnowledge nil: %v", err)
	}
	got, err = s.Knowledge(ctx, id)
	if err != nil {
		t.Fatalf("Knowledge (after clear): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Knowledge after clear = %v, want empty", got)
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
