package ledger

import (
	"context"
	"fmt"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// BudgetLimits are the configured USD spend limits; nil means no
// budget set for that window.
type BudgetLimits struct {
	Day   *float64 `json:"day"`
	Month *float64 `json:"month"`
}

// BudgetStore reads and writes the spend_budgets table.
type BudgetStore struct {
	db *pgpool.Pool
}

func NewBudgetStore(db *pgpool.Pool) *BudgetStore {
	return &BudgetStore{db: db}
}

// Limits returns the configured budgets. Missing rows come back nil.
func (s *BudgetStore) Limits(ctx context.Context) (BudgetLimits, error) {
	db, err := s.db.Get()
	if err != nil {
		return BudgetLimits{}, fmt.Errorf("budget limits: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT scope, limit_usd FROM spend_budgets`)
	if err != nil {
		return BudgetLimits{}, fmt.Errorf("budget limits: %w", err)
	}
	defer rows.Close()

	var limits BudgetLimits
	for rows.Next() {
		var (
			scope string
			limit float64
		)
		if err := rows.Scan(&scope, &limit); err != nil {
			return BudgetLimits{}, fmt.Errorf("budget limits: %w", err)
		}
		switch scope {
		case "day":
			limits.Day = &limit
		case "month":
			limits.Month = &limit
		}
	}
	return limits, rows.Err()
}

// Set upserts one window's limit; nil clears it.
func (s *BudgetStore) Set(ctx context.Context, scope string, limitUSD *float64) error {
	if scope != "day" && scope != "month" {
		return fmt.Errorf("unknown budget scope %q", scope)
	}
	if limitUSD != nil && *limitUSD <= 0 {
		return fmt.Errorf("budget limit for %s must be positive", scope)
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("budget set: %w", err)
	}
	if limitUSD == nil {
		_, err = db.Exec(ctx, `DELETE FROM spend_budgets WHERE scope = $1`, scope)
	} else {
		_, err = db.Exec(ctx, `INSERT INTO spend_budgets (scope, limit_usd) VALUES ($1, $2)
			ON CONFLICT (scope) DO UPDATE SET limit_usd = $2, updated_at = now()`, scope, *limitUSD)
	}
	if err != nil {
		return fmt.Errorf("budget set: %w", err)
	}
	return nil
}

// BudgetWindow is one window's budget position: what is configured,
// what was spent, and whether the limit is reached.
type BudgetWindow struct {
	LimitUSD *float64 `json:"limit_usd"`
	SpendUSD float64  `json:"spend_usd"`
	Over     bool     `json:"over"`
}

// BudgetStatus pairs both windows for the alert surface.
type BudgetStatus struct {
	Day   BudgetWindow `json:"day"`
	Month BudgetWindow `json:"month"`
}

// BudgetStatus computes spend against the given limits. Windows are
// UTC calendar boundaries: the dashboard's local-midnight tiles may
// differ slightly; the alert stays timezone-independent.
func (a *Aggregator) BudgetStatus(ctx context.Context, limits BudgetLimits, now time.Time) (BudgetStatus, error) {
	now = now.UTC()
	dayFrom := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	window := func(from time.Time, limit *float64) (BudgetWindow, error) {
		s, err := a.Summary(ctx, from, now)
		if err != nil {
			return BudgetWindow{}, err
		}
		return BudgetWindow{
			LimitUSD: limit,
			SpendUSD: s.CostUSD,
			Over:     limit != nil && s.CostUSD >= *limit,
		}, nil
	}

	day, err := window(dayFrom, limits.Day)
	if err != nil {
		return BudgetStatus{}, fmt.Errorf("budget status: %w", err)
	}
	month, err := window(monthFrom, limits.Month)
	if err != nil {
		return BudgetStatus{}, fmt.Errorf("budget status: %w", err)
	}
	return BudgetStatus{Day: day, Month: month}, nil
}
