//go:build integration

package tools

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

func integrationOutputs(t *testing.T) *Outputs {
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
	// Sweep at setup too — a run that dies before t.Cleanup registers
	// leaks its fixtures.
	sweepOutputsFixtures(ctx, t, db)
	return NewOutputs(pool)
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func sweepOutputsFixtures(ctx context.Context, t *testing.T, db execer) {
	t.Helper()
	_, _ = db.Exec(ctx, `DELETE FROM tool_outputs
		WHERE session_id IN (SELECT id FROM sessions WHERE title = 'outputs test')`)
	_, _ = db.Exec(ctx, `DELETE FROM session_grants
		WHERE session_id IN (SELECT id FROM sessions WHERE title = 'outputs test')`)
	_, _ = db.Exec(ctx, "DELETE FROM sessions WHERE title = 'outputs test'")
}

func newSessionID(t *testing.T, o *Outputs) string {
	t.Helper()
	db, err := o.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var id string
	if err := db.QueryRow(t.Context(),
		"INSERT INTO sessions (title) VALUES ('outputs test') RETURNING id",
	).Scan(&id); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() {
		// t.Context is canceled before cleanups run and the pool may
		// already be closed — use an independent connection.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		sweepOutputsFixtures(ctx, t, conn)
	})
	return id
}

func TestOutputsPutGetRoundTrip(t *testing.T) {
	o := integrationOutputs(t)
	sid := newSessionID(t, o)

	content := strings.Repeat("log line\n", 1000)
	id, err := o.Put(t.Context(), sid, "shell", content)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := o.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Tool != "shell" || got.Content != content {
		t.Fatalf("round trip mismatch: tool=%q len=%d", got.Tool, len(got.Content))
	}
}

func TestOutputsGetUnknownRef(t *testing.T) {
	o := integrationOutputs(t)

	for _, ref := range []string{
		"9be4c1d2-04a7-47a1-a1a9-3f6d2c9f1e10", // valid shape, absent
		"not-a-uuid",                           // invalid shape must not hit pg errors
		"",
	} {
		if _, err := o.Get(t.Context(), ref); !errors.Is(err, ErrOutputNotFound) {
			t.Fatalf("Get(%q) err = %v, want ErrOutputNotFound", ref, err)
		}
	}
}

func TestOutputsGC(t *testing.T) {
	o := integrationOutputs(t)
	sid := newSessionID(t, o)

	id, err := o.Put(t.Context(), sid, "shell", "expiring output")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Backdate the row past the retention window instead of sleeping.
	db, err := o.db.Get()
	if err != nil {
		t.Fatalf("Get pool: %v", err)
	}
	if _, err := db.Exec(t.Context(),
		"UPDATE tool_outputs SET created_at = now() - interval '8 days' WHERE id = $1", id,
	); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	removed, err := o.GC(t.Context(), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed < 1 {
		t.Fatalf("GC removed %d rows, want at least 1", removed)
	}
	if _, err := o.Get(t.Context(), id); !errors.Is(err, ErrOutputNotFound) {
		t.Fatalf("expired ref still readable: %v", err)
	}
}
