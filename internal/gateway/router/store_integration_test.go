//go:build integration

package router

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

func integrationStore(t *testing.T) *Store {
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
	return NewStore(pool, resolveCredential, nil, log)
}

// resolveCredential is the store's credential resolver for integration
// tests: os.Getenv for this file's own fixtures (set via t.Setenv), and
// a valid bedrock static-keys JSON stub for anything else. A real "AWS
// Bedrock" provider row can exist in the shared dev/CI database with a
// credential_ref (e.g. "bedrock-static-keys") this package never sets
// as an env var; the bedrock driver JSON-parses the resolved credential
// at registry build time (D-048), so an empty/non-JSON fallback would
// fail Store.Load for every test in this file whenever that row exists.
func resolveCredential(ref string) string {
	if v := os.Getenv(ref); v != "" {
		return v
	}
	return `{"access_key_id":"itest","secret_access_key":"itest"}`
}

func TestStoreLoadsSeededConfig(t *testing.T) {
	s := integrationStore(t)

	if err := s.Load(t.Context()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	snap := s.Snapshot()
	if snap == nil {
		t.Fatal("nil snapshot after successful load")
	}

	// Providers are never seeded — only the 4 routes Timothy needs to
	// work, empty-chained and disabled (0036 drops the never-configured
	// research/local/coding seed rows on a fresh install). Disabled
	// routes don't enter the snapshot, so assert against the table
	// itself.
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, want := range []string{"default", "summarize", "embedding", "vision"} {
		var n int
		if err := db.QueryRow(t.Context(),
			"SELECT count(*) FROM routes WHERE name = $1", want).Scan(&n); err != nil {
			t.Fatalf("count route %s: %v", want, err)
		}
		if n != 1 {
			t.Fatalf("seeded route %q missing", want)
		}
	}

	// State-independent (the dev database's enabled flags are live
	// config, not fixture data): an unrouted category must resolve to
	// a structured error, never panic.
	if _, err := snap.Resolve("no-such-route", "", Sticky{}); err == nil {
		t.Fatal("Resolve succeeded for a category with no route")
	}
}

func TestStoreReloadReflectsSQLChanges(t *testing.T) {
	s := integrationStore(t)
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Fixture rows OWNED by this test: the dev database's real
	// providers and routes are live configuration and must never be
	// toggled by tests. Sweep at setup too — a run that dies between
	// insert and t.Cleanup registration leaks its fixtures.
	_, _ = db.Exec(t.Context(), "DELETE FROM routes WHERE name = 'itest-cat'")
	_, _ = db.Exec(t.Context(), "DELETE FROM providers WHERE name = 'itest-prov'")
	var providerID string
	if err := db.QueryRow(t.Context(), `INSERT INTO providers
		(name, kind, driver, base_url, default_model, credential_ref, enabled)
		VALUES ('itest-prov', 'api', 'openaicompat', 'https://itest.invalid/v1', 'itest-model', 'ITEST_KEY', true)
		RETURNING id`).Scan(&providerID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO routes (name, chain, enabled)
		VALUES ('itest-cat', jsonb_build_array(jsonb_build_object('provider_id', $1::uuid, 'model', 'itest-model')), true)`,
		providerID); err != nil {
		t.Fatalf("insert route: %v", err)
	}
	t.Cleanup(func() {
		// t.Context is canceled before cleanups run and the pool's
		// watcher may already have closed it — use an independent
		// connection so the cleanup cannot be lost.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, "DELETE FROM routes WHERE name = 'itest-cat'"); err != nil {
			t.Errorf("cleanup route: %v", err)
		}
		if _, err := conn.Exec(ctx, "DELETE FROM providers WHERE name = 'itest-prov'"); err != nil {
			t.Errorf("cleanup provider: %v", err)
		}
	})

	t.Setenv("ITEST_KEY", "test-key-value") // healthy via env lookup
	if err := s.Load(t.Context()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	attempts, err := s.Snapshot().Resolve("itest-cat", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve after insert: %v", err)
	}
	if len(attempts) != 1 || attempts[0].ProviderName != "itest-prov" || attempts[0].Model != "itest-model" {
		t.Fatalf("attempts = %+v, want itest-prov/itest-model", attempts)
	}
}

func TestStoreLoadsReasoningEffortFromOptions(t *testing.T) {
	s := integrationStore(t)
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	_, _ = db.Exec(t.Context(), "DELETE FROM providers WHERE name = 'itest-reasoning-prov'")
	if _, err := db.Exec(t.Context(), `INSERT INTO providers
		(name, kind, driver, base_url, default_model, credential_ref, options, enabled)
		VALUES ('itest-reasoning-prov', 'api', 'openaicompat', 'https://itest.invalid/v1', 'itest-model',
			'ITEST_REASONING_KEY', '{"reasoning_effort": "none"}'::jsonb, true)`); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, "DELETE FROM providers WHERE name = 'itest-reasoning-prov'"); err != nil {
			t.Errorf("cleanup provider: %v", err)
		}
	})

	t.Setenv("ITEST_REASONING_KEY", "test-key-value")
	if err := s.Load(t.Context()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	rows, _ := s.Snapshot().Providers()
	var found bool
	for _, row := range rows {
		if row.Name != "itest-reasoning-prov" {
			continue
		}
		found = true
		if row.ReasoningEffort != "none" {
			t.Fatalf("ReasoningEffort = %q, want %q", row.ReasoningEffort, "none")
		}
	}
	if !found {
		t.Fatal("itest-reasoning-prov not found in snapshot")
	}
}

func TestStoreLoadsRequestTimeoutFromOptions(t *testing.T) {
	s := integrationStore(t)
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	_, _ = db.Exec(t.Context(), "DELETE FROM providers WHERE name = 'itest-timeout-prov'")
	if _, err := db.Exec(t.Context(), `INSERT INTO providers
		(name, kind, driver, base_url, default_model, credential_ref, options, enabled)
		VALUES ('itest-timeout-prov', 'api', 'openaicompat', 'https://itest.invalid/v1', 'itest-model',
			'ITEST_TIMEOUT_KEY', '{"request_timeout": "20m"}'::jsonb, true)`); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, "DELETE FROM providers WHERE name = 'itest-timeout-prov'"); err != nil {
			t.Errorf("cleanup provider: %v", err)
		}
	})

	t.Setenv("ITEST_TIMEOUT_KEY", "test-key-value")
	if err := s.Load(t.Context()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	rows, _ := s.Snapshot().Providers()
	var found bool
	for _, row := range rows {
		if row.Name != "itest-timeout-prov" {
			continue
		}
		found = true
		if row.Timeout != 20*time.Minute {
			t.Fatalf("Timeout = %v, want 20m", row.Timeout)
		}
	}
	if !found {
		t.Fatal("itest-timeout-prov not found in snapshot")
	}
}

func TestStoreLoadFailsOnInvalidRequestTimeout(t *testing.T) {
	s := integrationStore(t)
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	_, _ = db.Exec(t.Context(), "DELETE FROM providers WHERE name = 'itest-bad-timeout-prov'")
	if _, err := db.Exec(t.Context(), `INSERT INTO providers
		(name, kind, driver, base_url, default_model, credential_ref, options, enabled)
		VALUES ('itest-bad-timeout-prov', 'api', 'openaicompat', 'https://itest.invalid/v1', 'itest-model',
			'ITEST_BAD_TIMEOUT_KEY', '{"request_timeout": "banana"}'::jsonb, true)`); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, "DELETE FROM providers WHERE name = 'itest-bad-timeout-prov'"); err != nil {
			t.Errorf("cleanup provider: %v", err)
		}
	})

	t.Setenv("ITEST_BAD_TIMEOUT_KEY", "test-key-value")
	if err := s.Load(t.Context()); err == nil {
		t.Fatal("Load succeeded with an invalid request_timeout, want error")
	}
}

// insertLedgerRow inserts one cost_ledger row for loadStats fixtures,
// timestamped now (inside the 60-minute window) so decay weight stays
// close to 1.
func insertLedgerRow(t *testing.T, tx pgx.Tx, provider, model, status, purpose string, latencyMS, outputTokens int) {
	t.Helper()
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO cost_ledger (provider, model, route, latency_ms, status, purpose, output_tokens)
		VALUES ($1, $2, 'itest-route', $3, $4, NULLIF($5, ''), $6)`,
		provider, model, latencyMS, status, purpose, outputTokens); err != nil {
		t.Fatalf("insert cost_ledger row: %v", err)
	}
}

// TestLoadStats covers the three correctness fixes: error rows must
// not inflate/deflate the latency or tps average (their weight now
// only counts in the ok-filtered numerator when it's also in the
// denominator), and purpose='executor' rows (whole CLI-harness runs
// booked under gateway provider/model pairs) are excluded entirely.
func TestLoadStats(t *testing.T) {
	s := integrationStore(t)
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	_, _ = db.Exec(t.Context(), "DELETE FROM cost_ledger WHERE provider = 'itest-stats-prov'")
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM cost_ledger WHERE provider = 'itest-stats-prov'")
	})

	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// ok row: 100ms/50 output tokens (tps=500). error row: 10000ms,
	// same output tokens — its latency/tps must not enter the average,
	// only its weight must count against uptime.
	insertLedgerRow(t, tx, "itest-stats-prov", "m1", "ok", "", 100, 50)
	insertLedgerRow(t, tx, "itest-stats-prov", "m1", "error", "", 10000, 50)
	// executor row: minutes-long CLI run under the same provider/model,
	// must be ignored entirely.
	insertLedgerRow(t, tx, "itest-stats-prov", "m1", "ok", "executor", 600000, 1)

	stats, err := loadStats(t.Context(), tx)
	if err != nil {
		t.Fatalf("loadStats: %v", err)
	}
	st, ok := stats["itest-stats-prov/m1"]
	if !ok {
		t.Fatalf("no stats for itest-stats-prov/m1: %+v", stats)
	}
	if st.LatencyMS < 95 || st.LatencyMS > 105 {
		t.Fatalf("LatencyMS = %v, want ~100 (error/executor rows must not inflate it)", st.LatencyMS)
	}
	if st.TokensPerS < 490 || st.TokensPerS > 510 {
		t.Fatalf("TokensPerS = %v, want ~500", st.TokensPerS)
	}
	if st.Uptime >= 1 {
		t.Fatalf("Uptime = %v, want < 1 (error row must still count against uptime)", st.Uptime)
	}
}
