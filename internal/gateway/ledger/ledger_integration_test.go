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
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM cost_ledger WHERE provider = 'itest-provider'")
	})

	l := New(pool, log)
	cost := 0.001234

	// Success with usage and cost.
	l.Record(ctx, Entry{
		Provider: "itest-provider", Model: "m1", TaskCategory: "coding",
		SessionID: "sess-1",
		Usage:     &stream.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10},
		LatencyMS: 321, Status: "ok", CostUSD: &cost,
	})
	// Failure without usage: cost, tokens, session all NULL.
	l.Record(ctx, Entry{
		Provider: "itest-provider", Model: "m1", TaskCategory: "coding",
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
