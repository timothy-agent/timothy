package ledger

import (
	"context"
	"fmt"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// BudgetLimit pairs an amount with the currency it's denominated in —
// budgets are currency-scoped, so a limit is meaningless without one.
type BudgetLimit struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// BudgetLimits are the configured spend limits; nil means no budget
// set for that window.
type BudgetLimits struct {
	Day   *BudgetLimit `json:"day"`
	Month *BudgetLimit `json:"month"`
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
	rows, err := db.Query(ctx, `SELECT scope, limit_amount, currency FROM spend_budgets`)
	if err != nil {
		return BudgetLimits{}, fmt.Errorf("budget limits: %w", err)
	}
	defer rows.Close()

	var limits BudgetLimits
	for rows.Next() {
		var (
			scope string
			limit BudgetLimit
		)
		if err := rows.Scan(&scope, &limit.Amount, &limit.Currency); err != nil {
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

// Set upserts one window's limit; nil clears it. Blank currency
// defaults to USD (all current providers bill USD).
func (s *BudgetStore) Set(ctx context.Context, scope string, limit *BudgetLimit) error {
	if scope != "day" && scope != "month" {
		return fmt.Errorf("unknown budget scope %q", scope)
	}
	if limit != nil && limit.Amount <= 0 {
		return fmt.Errorf("budget limit for %s must be positive", scope)
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("budget set: %w", err)
	}
	if limit == nil {
		_, err = db.Exec(ctx, `DELETE FROM spend_budgets WHERE scope = $1`, scope)
	} else {
		currency := limit.Currency
		if currency == "" {
			currency = "USD"
		}
		_, err = db.Exec(ctx, `INSERT INTO spend_budgets (scope, limit_amount, currency) VALUES ($1, $2, $3)
			ON CONFLICT (scope) DO UPDATE SET limit_amount = $2, currency = $3, updated_at = now()`,
			scope, limit.Amount, currency)
	}
	if err != nil {
		return fmt.Errorf("budget set: %w", err)
	}
	return nil
}

// BudgetWindow is one window's budget position in a single currency:
// what is configured, what was spent, and whether the limit is
// reached. Spend is computed only from cost_ledger rows in this same
// currency — a budget in one currency is never checked against spend
// recorded in another.
type BudgetWindow struct {
	Currency string       `json:"currency"`
	Limit    *BudgetLimit `json:"limit"`
	Spend    float64      `json:"spend"`
	Over     bool         `json:"over"`
}

// BudgetStatus pairs both windows for the alert surface.
type BudgetStatus struct {
	Day   BudgetWindow `json:"day"`
	Month BudgetWindow `json:"month"`
}

// BudgetStatus computes spend against the given limits. Windows are
// UTC calendar boundaries: the dashboard's local-midnight tiles may
// differ slightly; the alert stays timezone-independent. A window
// with no limit set has no currency to scope spend to, so it reports
// zero spend rather than guessing a currency to sum.
func (a *Aggregator) BudgetStatus(ctx context.Context, limits BudgetLimits, now time.Time) (BudgetStatus, error) {
	now = now.UTC()
	dayFrom := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	window := func(from time.Time, limit *BudgetLimit) (BudgetWindow, error) {
		if limit == nil {
			return BudgetWindow{}, nil
		}
		summaries, err := a.SummaryByCurrency(ctx, from, now)
		if err != nil {
			return BudgetWindow{}, err
		}
		var spend float64
		for _, s := range summaries {
			if s.Currency == limit.Currency {
				spend = s.Cost
				break
			}
		}
		return BudgetWindow{
			Currency: limit.Currency,
			Limit:    limit,
			Spend:    spend,
			Over:     spend >= limit.Amount,
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
