//go:build integration

package settings

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
	// The settings table is shared state (a dev database holds the
	// user's real switches): snapshot it, run the test against a clean
	// slate, and restore the snapshot afterwards. The teardown runs
	// after t.Context() is canceled and the pool may be closed, so it
	// uses an independent connection.
	type row struct {
		key   string
		value []byte
	}
	var saved []row
	rows, err := db.Query(ctx, "SELECT key, value FROM settings")
	if err != nil {
		t.Fatalf("snapshot settings: %v", err)
	}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.key, &r.value); err != nil {
			t.Fatalf("scan setting: %v", err)
		}
		saved = append(saved, r)
	}
	rows.Close()
	_, _ = db.Exec(ctx, "DELETE FROM settings")
	_, _ = db.Exec(ctx, "DELETE FROM admin_audit WHERE entity = 'setting'")
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		if _, err := conn.Exec(cctx, "DELETE FROM settings"); err != nil {
			t.Errorf("cleanup settings: %v", err)
		}
		if _, err := conn.Exec(cctx, "DELETE FROM admin_audit WHERE entity = 'setting'"); err != nil {
			t.Errorf("cleanup setting audit: %v", err)
		}
		for _, r := range saved {
			if _, err := conn.Exec(cctx,
				"INSERT INTO settings (key, value) VALUES ($1, $2)", r.key, r.value); err != nil {
				t.Errorf("restore setting %s: %v", r.key, err)
			}
		}
	})
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

	if err := s.SetValue(ctx, ValueSkillsAllowlist, "coding, research"); err != nil {
		t.Fatalf("SetValue allowlist: %v", err)
	}
	if !s.SkillAllowed(ctx, "coding") || !s.SkillAllowed(ctx, "research") {
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

// TestDefaultCurrencySetting exercises the allowed-list validation on
// ValueDefaultCurrency: a valid code (any case) is accepted and
// normalized to uppercase, an unknown code is rejected, and clearing
// back to empty falls through DefaultCurrency's "USD" fallback.
func TestDefaultCurrencySetting(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	if got := s.DefaultCurrency(ctx); got != "USD" {
		t.Fatalf("DefaultCurrency default = %q, want USD", got)
	}

	if err := s.SetValue(ctx, ValueDefaultCurrency, "XYZ"); err == nil {
		t.Fatal("unknown currency code accepted")
	}

	if err := s.SetValue(ctx, ValueDefaultCurrency, "eur"); err != nil {
		t.Fatalf("SetValue lowercase valid code: %v", err)
	}
	if got := s.DefaultCurrency(ctx); got != "EUR" {
		t.Fatalf("DefaultCurrency after set = %q, want EUR (normalized to uppercase)", got)
	}

	if err := s.SetValue(ctx, ValueDefaultCurrency, ""); err != nil {
		t.Fatalf("SetValue clear: %v", err)
	}
	if got := s.DefaultCurrency(ctx); got != "USD" {
		t.Fatalf("DefaultCurrency after clear = %q, want USD", got)
	}
}

// TestCodingExecutorSetting covers D-051: an unknown harness is
// rejected, "native" normalizes to "" on write, a registered harness
// (claude-cli) round-trips, and the default is "" (native).
func TestCodingExecutorSetting(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	if got := s.CodingExecutor(ctx); got != "" {
		t.Fatalf("CodingExecutor default = %q, want empty (native)", got)
	}

	if err := s.SetValue(ctx, ValueCodingExecutor, "codex-cli-unregistered"); err == nil {
		t.Fatal("unknown harness accepted")
	}

	if err := s.SetValue(ctx, ValueCodingExecutor, "claude-cli"); err != nil {
		t.Fatalf("SetValue claude-cli: %v", err)
	}
	if got := s.CodingExecutor(ctx); got != "claude-cli" {
		t.Fatalf("CodingExecutor after set = %q, want claude-cli", got)
	}

	if err := s.SetValue(ctx, ValueCodingExecutor, "native"); err != nil {
		t.Fatalf("SetValue native: %v", err)
	}
	if got := s.CodingExecutor(ctx); got != "" {
		t.Fatalf("CodingExecutor after native = %q, want empty (native normalizes to \"\")", got)
	}
}

func TestGitStrategySettings(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	if got := s.GitBranchPattern(ctx); got != "" {
		t.Fatalf("GitBranchPattern default = %q, want empty (built-in default)", got)
	}
	if got := s.GitCommitStyle(ctx); got != "" {
		t.Fatalf("GitCommitStyle default = %q, want empty (built-in default)", got)
	}

	if err := s.SetValue(ctx, ValueGitBranchPattern, "{type}/{login}/{slug}"); err != nil {
		t.Fatalf("SetValue branch pattern: %v", err)
	}
	if got := s.GitBranchPattern(ctx); got != "{type}/{login}/{slug}" {
		t.Fatalf("GitBranchPattern after set = %q", got)
	}
	if err := s.SetValue(ctx, ValueGitBranchPattern, "{unknown}/{slug}"); err == nil {
		t.Fatal("unknown placeholder accepted")
	}
	if err := s.SetValue(ctx, ValueGitBranchPattern, "../{slug}"); err == nil {
		t.Fatal("traversal pattern accepted")
	}

	if err := s.SetValue(ctx, ValueGitCommitStyle, "plain"); err != nil {
		t.Fatalf("SetValue commit style: %v", err)
	}
	if got := s.GitCommitStyle(ctx); got != "plain" {
		t.Fatalf("GitCommitStyle after set = %q, want plain", got)
	}
	if err := s.SetValue(ctx, ValueGitCommitStyle, "loud"); err == nil {
		t.Fatal("unknown commit style accepted")
	}
}

// TestTimezoneSetting covers Location's fallback to UTC (unset, and a
// stale/invalid stored value) and its use of the stored IANA name once
// validated on write.
func TestTimezoneSetting(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	if got := s.Location(ctx); got != time.UTC {
		t.Fatalf("Location default = %v, want time.UTC", got)
	}

	if err := s.SetValue(ctx, ValueTimezone, "Not/AZone"); err == nil {
		t.Fatal("unknown IANA timezone accepted")
	}

	if err := s.SetValue(ctx, ValueTimezone, "Europe/Amsterdam"); err != nil {
		t.Fatalf("SetValue Europe/Amsterdam: %v", err)
	}
	loc := s.Location(ctx)
	if loc == nil || loc.String() != "Europe/Amsterdam" {
		t.Fatalf("Location after set = %v, want Europe/Amsterdam", loc)
	}

	if err := s.SetValue(ctx, ValueTimezone, ""); err != nil {
		t.Fatalf("SetValue clear: %v", err)
	}
	if got := s.Location(ctx); got != time.UTC {
		t.Fatalf("Location after clear = %v, want time.UTC", got)
	}
}

// TestPermissionTimeoutSecondsSetting covers issue #445's global setting:
// absent/unset (or explicitly 0) means disabled, matching the original
// park-forever behavior of a deployment that never opts in; a positive
// value round-trips, and a negative or non-numeric value is rejected.
func TestPermissionTimeoutSecondsSetting(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	if got := s.PermissionTimeoutSeconds(ctx); got != 0 {
		t.Fatalf("PermissionTimeoutSeconds default = %d, want 0 (disabled)", got)
	}

	if err := s.SetValue(ctx, ValuePermissionTimeoutSeconds, "-5"); err == nil {
		t.Fatal("negative timeout accepted")
	}
	if err := s.SetValue(ctx, ValuePermissionTimeoutSeconds, "not-a-number"); err == nil {
		t.Fatal("non-numeric timeout accepted")
	}

	if err := s.SetValue(ctx, ValuePermissionTimeoutSeconds, "600"); err != nil {
		t.Fatalf("SetValue 600: %v", err)
	}
	if got := s.PermissionTimeoutSeconds(ctx); got != 600 {
		t.Fatalf("PermissionTimeoutSeconds after set = %d, want 600 (cache must invalidate on write)", got)
	}

	if err := s.SetValue(ctx, ValuePermissionTimeoutSeconds, "0"); err != nil {
		t.Fatalf("SetValue 0: %v", err)
	}
	if got := s.PermissionTimeoutSeconds(ctx); got != 0 {
		t.Fatalf("PermissionTimeoutSeconds after explicit 0 = %d, want 0 (disabled)", got)
	}

	if err := s.SetValue(ctx, ValuePermissionTimeoutSeconds, ""); err != nil {
		t.Fatalf("SetValue clear: %v", err)
	}
	if got := s.PermissionTimeoutSeconds(ctx); got != 0 {
		t.Fatalf("PermissionTimeoutSeconds after clear = %d, want 0", got)
	}
}
