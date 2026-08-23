package missions

// D-074: budgetProjector holds the budget/FX slice split out of Driver
// (convertOtherCurrencySpend, plus the budget-projection half of
// toStepState) — a pure extraction, no behavior change. Pure given a
// rate table, so it is directly unit-testable with a fake fxRateSource.

import (
	"context"
	"log/slog"

	"github.com/SumonMSelim/timothy/internal/brain/fxrates"
)

// budgetProjector projects a mission's ledger spend onto the state
// machine's budget-brake inputs (Spent, MixedCurrencySpend, RateAsOf),
// converting any spend recorded in a currency other than the mission's
// own budget currency via the stored USD-base rate table.
type budgetProjector struct {
	fxRates fxRateSource
	log     *slog.Logger
}

// projectSpend pulls a mission's actual ledger spend from store and
// folds cross-currency amounts into the mission's own budget currency —
// the budget-projection half of toStepState. A Spend query failure is
// treated the same as zero spend (best-effort, never blocks Advance/
// Signal over a bookkeeping read) — just logged.
func (b *budgetProjector) projectSpend(ctx context.Context, store driverStore, m Mission) (spent float64, mixed bool, rateAsOf string) {
	if m.BudgetAmount == nil {
		return 0, false, ""
	}
	usage, err := store.Spend(ctx, m.ID)
	if err != nil {
		b.log.Warn("driver: mission spend lookup failed", "mission_id", m.ID, "error", err)
		return 0, false, ""
	}
	spent = usage.ByCurrency[m.BudgetCurrency]
	return b.convertOtherCurrencySpend(ctx, usage, m.BudgetCurrency, spent)
}

// convertOtherCurrencySpend folds every currency in usage OTHER than
// budgetCurrency into spent, using the driver's stored fx rate table —
// same USD-base cross the display-conversion seam uses (D-013's spend
// sibling: never a guessed rate). mixed comes back true the moment ANY
// currency has no usable stored rate (missing pair, or stale beyond
// the store's own bound) — at that point the brake can no longer
// safely judge the mission's true spend, so it must pause rather than
// under-count. rateAsOf is the oldest date among whichever converted
// legs participated, "" when nothing needed converting.
func (b *budgetProjector) convertOtherCurrencySpend(ctx context.Context, usage MissionSpend, budgetCurrency string, spent float64) (newSpent float64, mixed bool, rateAsOf string) {
	others := make([]string, 0, len(usage.ByCurrency))
	for currency, amount := range usage.ByCurrency {
		if currency != budgetCurrency && amount > 0 {
			others = append(others, currency)
		}
	}
	if len(others) == 0 {
		return spent, false, ""
	}
	if b.fxRates == nil {
		return spent, true, "" // no rate source wired: same conservative pause as before
	}
	rates, err := b.fxRates.LatestUSDRates(ctx)
	if err != nil {
		b.log.Warn("driver: fx rate lookup failed; treating as unconvertible", "error", err)
		return spent, true, ""
	}
	var oldest fxrates.Rate
	for _, currency := range others {
		converted, rate, ok := fxrates.Convert(usage.ByCurrency[currency], currency, budgetCurrency, rates)
		if !ok {
			return spent, true, ""
		}
		spent += converted
		if oldest.AsOf.IsZero() || rate.AsOf.Before(oldest.AsOf) {
			oldest = rate
		}
	}
	if !oldest.AsOf.IsZero() {
		rateAsOf = oldest.AsOf.Format("2006-01-02")
	}
	return spent, false, rateAsOf
}
