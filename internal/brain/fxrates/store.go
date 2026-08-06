package fxrates

import (
	"context"
	"fmt"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// staleAfter bounds how old a stored rate may be and still be used —
// beyond this, callers (currency.go's live-lookup tool) fall back to a
// fresh fetch rather than convert against a rate that's gone stale.
// The budget brake and display conversion (Analytics, mission usage)
// intentionally do NOT apply this bound: D-013's sibling invariant
// there is "absent rate never guessed," and a week-old stored rate is
// still an honest, dated number — never worse than the alternative
// (no conversion shown at all).
const staleAfter = 7 * 24 * time.Hour

// Store reads and writes fx_rates. Append-only by day: Upsert is a
// no-op for a (base, quote, as_of) triple that already has a row —
// today's date always wins, retried fetches never overwrite anything.
type Store struct {
	db *pgpool.Pool
}

func NewStore(db *pgpool.Pool) *Store {
	return &Store{db: db}
}

// Upsert writes one day's USD-base rates. ON CONFLICT DO NOTHING: a
// retry after a partial failure, or a redundant tick, never rewrites
// an already-recorded day (append-only, D-013's sibling for rates).
func (s *Store) Upsert(ctx context.Context, asOf time.Time, rates map[string]float64) error {
	if len(rates) == 0 {
		return nil
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("fxrates: store: %w", err)
	}
	batch := make([][]any, 0, len(rates))
	for quote, rate := range rates {
		batch = append(batch, []any{"USD", quote, rate, asOf})
	}
	for _, row := range batch {
		if _, err := db.Exec(ctx, `INSERT INTO fx_rates (base, quote, rate, as_of) VALUES ($1, $2, $3, $4)
			ON CONFLICT (base, quote, as_of) DO NOTHING`, row...); err != nil {
			return fmt.Errorf("fxrates: store: %w", err)
		}
	}
	return nil
}

// Rate is one stored (or looked-up) exchange rate with its provenance
// date — every caller that converts a displayed amount carries this
// forward so the UI/event payload can say exactly which day's rate
// produced the number.
type Rate struct {
	Value float64
	AsOf  time.Time
}

// LatestUSDRates returns the most recent stored rate for every quote
// currency that has one, regardless of age — callers needing a
// freshness bound (currency.go) apply staleAfter themselves via
// LatestUSDRate. Used by the Convert helper's callers to build the
// USD-base table once per request instead of one query per pair.
func (s *Store) LatestUSDRates(ctx context.Context) (map[string]Rate, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("fxrates: latest rates: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT DISTINCT ON (quote) quote, rate, as_of
		FROM fx_rates WHERE base = 'USD' ORDER BY quote, as_of DESC`)
	if err != nil {
		return nil, fmt.Errorf("fxrates: latest rates: %w", err)
	}
	defer rows.Close()

	out := map[string]Rate{}
	for rows.Next() {
		var quote string
		var r Rate
		if err := rows.Scan(&quote, &r.Value, &r.AsOf); err != nil {
			return nil, fmt.Errorf("fxrates: latest rates: %w", err)
		}
		out[quote] = r
	}
	return out, rows.Err()
}

// LatestUSDRate returns one currency's most recent stored rate against
// USD, only if it is not older than staleAfter — currency.go's
// table-first lookup uses this directly: a stale or absent row means
// "fall back to a live fetch," never "guess."
func (s *Store) LatestUSDRate(ctx context.Context, quote string) (Rate, bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return Rate{}, false, fmt.Errorf("fxrates: latest rate: %w", err)
	}
	var r Rate
	err = db.QueryRow(ctx, `SELECT rate, as_of FROM fx_rates
		WHERE base = 'USD' AND quote = $1 ORDER BY as_of DESC LIMIT 1`, quote).Scan(&r.Value, &r.AsOf)
	if err != nil {
		return Rate{}, false, nil //nolint:nilerr // no row is "not found", not a real error
	}
	if time.Since(r.AsOf) > staleAfter {
		return Rate{}, false, nil
	}
	return r, true, nil
}
