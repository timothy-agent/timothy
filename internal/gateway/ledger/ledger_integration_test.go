//go:build integration

package ledger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

func TestRecordWritesRows(t *testing.T) {
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
	// Sweep at setup (crashed-run leftovers) and via defer — it runs
	// while t.Context() and the pool are still alive, unlike t.Cleanup.
	_, _ = db.Exec(ctx, "DELETE FROM cost_ledger WHERE provider = 'itest-provider'")
	defer func() {
		if _, err := db.Exec(ctx, "DELETE FROM cost_ledger WHERE provider = 'itest-provider'"); err != nil {
			t.Errorf("sweep itest-provider ledger rows: %v", err)
		}
	}()

	l := New(pool, log)
	cost := 0.001234

	// Success with usage and cost.
	l.Record(ctx, Entry{
		Provider: "itest-provider", Model: "m1", Route: "coding",
		SessionID: "sess-1",
		Usage:     &stream.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10},
		LatencyMS: 321, Status: "ok", CostUSD: &cost,
	})
	// Failure without usage: cost, tokens, session all NULL.
	l.Record(ctx, Entry{
		Provider: "itest-provider", Model: "m1", Route: "coding",
		LatencyMS: 45, Status: "error", ErrorCode: "timeout",
	})

	var okCount, nullUsage int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM cost_ledger
		WHERE provider = 'itest-provider' AND status = 'ok'
		AND input_tokens = 100 AND output_tokens = 50 AND cache_read_tokens = 10
		AND session_id = 'sess-1' AND cost_usd = 0.001234`).Scan(&okCount); err != nil {
		t.Fatalf("query ok row: %v", err)
	}
	if okCount != 1 {
		t.Fatalf("ok rows = %d, want 1", okCount)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM cost_ledger
		WHERE provider = 'itest-provider' AND status = 'error' AND error_code = 'timeout'
		AND input_tokens IS NULL AND cost_usd IS NULL AND session_id IS NULL`).Scan(&nullUsage); err != nil {
		t.Fatalf("query error row: %v", err)
	}
	if nullUsage != 1 {
		t.Fatalf("error rows = %d, want 1", nullUsage)
	}
}

// LastSuccess must ignore a sticky row older than the route's own
// updated_at: an operator's deliberate chain reorder (which bumps
// routes.updated_at) has to take effect on the very next request
// instead of a stale sticky pick overriding it.
func TestLastSuccessIgnoresStaleRouteEdit(t *testing.T) {
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
	const routeName = "itest-sticky-route"
	_, _ = db.Exec(ctx, "DELETE FROM cost_ledger WHERE provider = 'itest-sticky-provider'")
	_, _ = db.Exec(ctx, "DELETE FROM routes WHERE name = $1", routeName)
	defer func() {
		if _, err := db.Exec(ctx, "DELETE FROM cost_ledger WHERE provider = 'itest-sticky-provider'"); err != nil {
			t.Errorf("sweep itest-sticky-provider ledger rows: %v", err)
		}
		if _, err := db.Exec(ctx, "DELETE FROM routes WHERE name = $1", routeName); err != nil {
			t.Errorf("sweep %s route: %v", routeName, err)
		}
	}()

	if _, err := db.Exec(ctx, `INSERT INTO routes (name, chain, enabled, updated_at)
		VALUES ($1, '[]', true, now())`, routeName); err != nil {
		t.Fatalf("insert route: %v", err)
	}

	l := New(pool, log)
	l.Record(ctx, Entry{
		Provider: "itest-sticky-provider", Model: "m1", Route: routeName,
		SessionID: "sess-sticky", LatencyMS: 10, Status: "ok",
	})

	// Before any route edit, the fresh success row wins.
	provider, model, ok := l.LastSuccess(ctx, "sess-sticky", routeName)
	if !ok || provider != "itest-sticky-provider" || model != "m1" {
		t.Fatalf("LastSuccess before edit = %q/%q/%v, want itest-sticky-provider/m1/true", provider, model, ok)
	}

	// Simulate an operator's chain reorder: bump routes.updated_at past
	// the ledger row's ts.
	if _, err := db.Exec(ctx, `UPDATE routes SET updated_at = now() + interval '1 second' WHERE name = $1`, routeName); err != nil {
		t.Fatalf("bump route updated_at: %v", err)
	}

	if _, _, ok := l.LastSuccess(ctx, "sess-sticky", routeName); ok {
		t.Fatal("LastSuccess after route edit = ok, want false (stale sticky row invalidated)")
	}
}
