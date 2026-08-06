package ledger

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// UpdateSpendGauges refreshes the spend/budget gauges from the ledger.
// The budget gauge reports 0 for an unset window so the pair stays
// usable in alert expressions (spend >= budget and budget > 0).
func UpdateSpendGauges(ctx context.Context, agg *Aggregator, budgets *BudgetStore, spend, budget *prometheus.GaugeVec) error {
	limits, err := budgets.Limits(ctx)
	if err != nil {
		return err
	}
	status, err := agg.BudgetStatus(ctx, limits, time.Now())
	if err != nil {
		return err
	}
	set := func(window string, w BudgetWindow) {
		spend.WithLabelValues(window).Set(w.Spend)
		var limit float64
		if w.Limit != nil {
			limit = w.Limit.Amount
		}
		budget.WithLabelValues(window).Set(limit)
	}
	set("day", status.Day)
	set("month", status.Month)
	return nil
}

// RunSpendGauges updates the gauges immediately and then on every
// tick until ctx ends. Failures log and the next tick retries —
// metrics must never take serving down.
func RunSpendGauges(ctx context.Context, interval time.Duration, agg *Aggregator, budgets *BudgetStore, spend, budget *prometheus.GaugeVec, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := UpdateSpendGauges(ctx, agg, budgets, spend, budget); err != nil {
			log.Warn("spend gauge update failed", "error", err)
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}
