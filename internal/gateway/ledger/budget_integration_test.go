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

func TestBudgetRoundTripAndStatus(t *testing.T) {
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
	// Sweep leftovers from a crashed run first.
	_, _ = db.Exec(ctx, "DELETE FROM cost_ledger WHERE provider = 'itest-budget'")

	s := NewBudgetStore(pool)

	// The day/month budget scopes are shared state (a dev database may
	// hold real limits): save and restore them. A defer, not t.Cleanup —
	// it runs while t.Context() and the pool are still alive.
	orig, err := s.Limits(ctx)
	if err != nil {
		t.Fatalf("limits (orig): %v", err)
	}
	defer func() {
		if err := s.Set(ctx, "day", orig.Day); err != nil {
			t.Errorf("restore day budget: %v", err)
		}
		if err := s.Set(ctx, "month", orig.Month); err != nil {
			t.Errorf("restore month budget: %v", err)
		}
		if _, err := db.Exec(ctx, "DELETE FROM cost_ledger WHERE provider = 'itest-budget'"); err != nil {
			t.Errorf("sweep itest-budget ledger rows: %v", err)
		}
	}()

	// Round-trip: set both, read back, clear one.
	day, month := &BudgetLimit{Amount: 0.9, Currency: "USD"}, &BudgetLimit{Amount: 1e9, Currency: "USD"}
	if err := s.Set(ctx, "day", day); err != nil {
		t.Fatalf("set day: %v", err)
	}
	if err := s.Set(ctx, "month", month); err != nil {
		t.Fatalf("set month: %v", err)
	}
	limits, err := s.Limits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits.Day == nil || limits.Day.Amount != day.Amount || limits.Month == nil || limits.Month.Amount != month.Amount {
		t.Fatalf("limits = %+v, want day=%v month=%v", limits, day, month)
	}

	// Upsert overwrites.
	day2 := &BudgetLimit{Amount: 0.8, Currency: "USD"}
	if err := s.Set(ctx, "day", day2); err != nil {
		t.Fatalf("set day again: %v", err)
	}

	// Status: spend today crosses the day limit but not the huge
	// month limit. Other tests' rows can only add spend, which keeps
	// both assertions stable.
	l := New(pool, log, nil)
	cost := 0.5
	for range 2 {
		l.Record(ctx, Entry{
			Provider: "itest-budget", Model: "m1", Route: "coding",
			Usage:     &stream.Usage{InputTokens: 1, OutputTokens: 1},
			LatencyMS: 1, Status: "ok", Cost: &cost,
		})
	}
	limits, err = s.Limits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	agg := NewAggregator(pool)
	status, err := agg.BudgetStatus(ctx, limits, time.Now())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Day.Spend < 1.0 || !status.Day.Over {
		t.Fatalf("day = %+v, want spend >= 1.0 and over", status.Day)
	}
	if status.Month.Over {
		t.Fatalf("month = %+v, want not over under limit %v", status.Month, month)
	}
	if status.Month.Limit == nil || status.Month.Limit.Amount != month.Amount {
		t.Fatalf("month limit = %v, want %v", status.Month.Limit, month)
	}

	// Clear: nil deletes the row.
	if err := s.Set(ctx, "day", nil); err != nil {
		t.Fatalf("clear day: %v", err)
	}
	limits, err = s.Limits(ctx)
	if err != nil {
		t.Fatalf("limits after clear: %v", err)
	}
	if limits.Day != nil {
		t.Fatalf("day limit after clear = %v, want nil", *limits.Day)
	}
	// No limit set: never over, spend still reported.
	status, err = agg.BudgetStatus(ctx, limits, time.Now())
	if err != nil {
		t.Fatalf("status after clear: %v", err)
	}
	if status.Day.Over || status.Day.Limit != nil {
		t.Fatalf("day after clear = %+v, want no limit and not over", status.Day)
	}
}

// TestBudgetStatusIgnoresOtherCurrencySpend confirms a budget in one
// currency never gets checked against spend recorded in another —
// comparing across currencies would need a guessed FX rate, which
// this codebase never does. The budget is denominated in XTS (the ISO
// 4217 code reserved for testing) rather than USD: the test runs
// against the live ledger, and a USD budget would count real same-day
// spend (canary missions, title calls) as contamination.
func TestBudgetStatusIgnoresOtherCurrencySpend(t *testing.T) {
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
	_, _ = db.Exec(ctx, "DELETE FROM cost_ledger WHERE provider = 'itest-budget-eur'")

	s := NewBudgetStore(pool)
	orig, err := s.Limits(ctx)
	if err != nil {
		t.Fatalf("limits (orig): %v", err)
	}
	defer func() {
		if err := s.Set(ctx, "day", orig.Day); err != nil {
			t.Errorf("restore day budget: %v", err)
		}
		if _, err := db.Exec(ctx, "DELETE FROM cost_ledger WHERE provider = 'itest-budget-eur'"); err != nil {
			t.Errorf("sweep itest-budget-eur ledger rows: %v", err)
		}
	}()

	// An XTS day budget with a huge headroom; all the spend below is EUR.
	if err := s.Set(ctx, "day", &BudgetLimit{Amount: 1e9, Currency: "XTS"}); err != nil {
		t.Fatalf("set day: %v", err)
	}

	l := New(pool, log, nil)
	cost := 500.0
	l.Record(ctx, Entry{
		Provider: "itest-budget-eur", Model: "m1", Route: "coding",
		Usage:     &stream.Usage{InputTokens: 1, OutputTokens: 1},
		LatencyMS: 1, Status: "ok", Cost: &cost, Currency: "EUR",
	})

	limits, err := s.Limits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	agg := NewAggregator(pool)
	status, err := agg.BudgetStatus(ctx, limits, time.Now())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Day.Currency != "XTS" || status.Day.Spend != 0 || status.Day.Over {
		t.Fatalf("day = %+v, want XTS currency, zero spend, not over (EUR spend must not count)", status.Day)
	}
}
