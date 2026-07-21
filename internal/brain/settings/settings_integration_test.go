//go:build integration

package settings

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

func testStore(t *testing.T) *Store {
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
	// Sweep at setup AND teardown: the teardown runs after t.Context()
	// is canceled (dead pool → silent failure), so the next run's setup
	// must clean leftovers itself.
	sweep := func(ctx context.Context) {
		_, _ = db.Exec(ctx, "DELETE FROM settings")
		_, _ = db.Exec(ctx, "DELETE FROM runtime_settings")
		_, _ = db.Exec(ctx, "DELETE FROM admin_audit WHERE entity = 'setting'")
	}
	sweep(ctx)
	t.Cleanup(func() { sweep(context.Background()) })
	return New(pool, log)
}

func TestSettingsDefaultsFlipAndAudit(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	// Absent rows read as enabled.
	if !s.Enabled(ctx, KeyTools) || !s.Enabled(ctx, KeyCompaction) {
		t.Fatal("defaults must be enabled")
	}

	if err := s.Set(ctx, KeyTools, false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if s.Enabled(ctx, KeyTools) {
		t.Fatal("flip did not take effect (cache must invalidate on write)")
	}
	if !s.Enabled(ctx, KeyCompaction) {
		t.Fatal("unrelated switch flipped")
	}

	if err := s.Set(ctx, "not_a_setting", true); err == nil {
		t.Fatal("unknown key must refuse")
	}

	// The flip left an audit row.
	pool := s.db
	db, _ := pool.Get()
	var n int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM admin_audit
		WHERE entity = 'setting' AND entity_id = $1`, KeyTools).Scan(&n); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if n == 0 {
		t.Fatal("no audit row for the settings flip")
	}
}

// Typed value settings: empty by default, validated on write, cache
// invalidated, helpers apply defaults and parse the allowlist.
func TestRuntimeValueSettings(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	if v := s.Value(ctx, ValueTokenBudget); v != "" {
		t.Fatalf("default budget value = %q, want empty", v)
	}
	if got := s.TokenBudget(ctx, 60_000); got != 60_000 {
		t.Fatalf("TokenBudget default = %d, want 60000", got)
	}
	if !s.SkillAllowed(ctx, "anything") {
		t.Fatal("empty allowlist must admit everything")
	}

	if err := s.SetValue(ctx, ValueTokenBudget, "not-a-number"); err == nil {
		t.Fatal("non-numeric budget accepted")
	}
	if err := s.SetValue(ctx, ValueTokenBudget, "-5"); err == nil {
		t.Fatal("negative budget accepted")
	}
	if err := s.SetValue(ctx, "nope", "x"); err == nil {
		t.Fatal("unknown value key accepted")
	}

	if err := s.SetValue(ctx, ValueTokenBudget, "120000"); err != nil {
		t.Fatalf("SetValue budget: %v", err)
	}
	if got := s.TokenBudget(ctx, 60_000); got != 120_000 {
		t.Fatalf("TokenBudget = %d, want 120000 (cache must invalidate on write)", got)
	}

	if err := s.SetValue(ctx, ValueSkillsAllowlist, "coding-task, research"); err != nil {
		t.Fatalf("SetValue allowlist: %v", err)
	}
	if !s.SkillAllowed(ctx, "coding-task") || !s.SkillAllowed(ctx, "research") {
		t.Fatal("listed packs must be allowed")
	}
	if s.SkillAllowed(ctx, "other") {
		t.Fatal("unlisted pack admitted")
	}

	// Empty clears back to default.
	if err := s.SetValue(ctx, ValueSkillsAllowlist, ""); err != nil {
		t.Fatalf("SetValue clear: %v", err)
	}
	if !s.SkillAllowed(ctx, "other") {
		t.Fatal("cleared allowlist must admit everything")
	}
}
