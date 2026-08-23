package missions

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/fxrates"
)

// TestBudgetProjectorProjectSpendNoBudget confirms a mission with no
// BudgetAmount never even queries Spend — nothing to project.
func TestBudgetProjectorProjectSpendNoBudget(t *testing.T) {
	store := newFakeStore()
	store.spend["m1"] = MissionSpend{ByCurrency: map[string]float64{"USD": 5}}
	b := &budgetProjector{log: slog.Default()}

	spent, mixed, rateAsOf := b.projectSpend(context.Background(), store, Mission{ID: "m1"})
	if spent != 0 || mixed || rateAsOf != "" {
		t.Fatalf("projectSpend with no budget = (%v, %v, %q), want (0, false, \"\")", spent, mixed, rateAsOf)
	}
}

// TestBudgetProjectorProjectSpendSameCurrency confirms the common case:
// all spend already in the mission's own budget currency, no conversion
// needed.
func TestBudgetProjectorProjectSpendSameCurrency(t *testing.T) {
	store := newFakeStore()
	store.spend["m1"] = MissionSpend{ByCurrency: map[string]float64{"USD": 12.5}}
	budget := 100.0
	b := &budgetProjector{log: slog.Default()}

	spent, mixed, rateAsOf := b.projectSpend(context.Background(), store, Mission{ID: "m1", BudgetAmount: &budget, BudgetCurrency: "USD"})
	if spent != 12.5 || mixed || rateAsOf != "" {
		t.Fatalf("projectSpend = (%v, %v, %q), want (12.5, false, \"\")", spent, mixed, rateAsOf)
	}
}

// TestBudgetProjectorConvertOtherCurrencySpend covers
// convertOtherCurrencySpend directly (previously impossible without
// going through the whole Driver): a mixed-currency ledger converts
// cleanly when every needed rate is present, and degrades to mixed=true
// whenever a rate is missing, the rate source errors, or none is wired
// at all — the budget brake must pause rather than under-count.
func TestBudgetProjectorConvertOtherCurrencySpend(t *testing.T) {
	asOf := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	rates := map[string]fxrates.Rate{
		"EUR": {Value: 0.9, AsOf: asOf},
	}

	cases := []struct {
		name           string
		fxRates        fxRateSource
		usage          MissionSpend
		budgetCurrency string
		spent          float64
		wantSpent      float64
		wantMixed      bool
		wantRateAsOf   string
	}{
		{
			name:           "no other currencies",
			fxRates:        &fakeFXRates{rates: rates},
			usage:          MissionSpend{ByCurrency: map[string]float64{"USD": 5}},
			budgetCurrency: "USD",
			spent:          5,
			wantSpent:      5,
		},
		{
			name:           "converts cleanly",
			fxRates:        &fakeFXRates{rates: rates},
			usage:          MissionSpend{ByCurrency: map[string]float64{"USD": 5, "EUR": 9}},
			budgetCurrency: "USD",
			spent:          5,
			// 9 EUR / 0.9 = 10 USD
			wantSpent:    15,
			wantRateAsOf: "2026-01-15",
		},
		{
			name:           "missing rate for a currency present in the ledger",
			fxRates:        &fakeFXRates{rates: rates},
			usage:          MissionSpend{ByCurrency: map[string]float64{"USD": 5, "GBP": 3}},
			budgetCurrency: "USD",
			spent:          5,
			wantSpent:      5,
			wantMixed:      true,
		},
		{
			name:           "rate source errors",
			fxRates:        &fakeFXRates{err: context.DeadlineExceeded},
			usage:          MissionSpend{ByCurrency: map[string]float64{"USD": 5, "EUR": 9}},
			budgetCurrency: "USD",
			spent:          5,
			wantSpent:      5,
			wantMixed:      true,
		},
		{
			name:           "no rate source wired",
			fxRates:        nil,
			usage:          MissionSpend{ByCurrency: map[string]float64{"USD": 5, "EUR": 9}},
			budgetCurrency: "USD",
			spent:          5,
			wantSpent:      5,
			wantMixed:      true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &budgetProjector{fxRates: c.fxRates, log: slog.Default()}
			spent, mixed, rateAsOf := b.convertOtherCurrencySpend(context.Background(), c.usage, c.budgetCurrency, c.spent)
			if spent != c.wantSpent || mixed != c.wantMixed || rateAsOf != c.wantRateAsOf {
				t.Fatalf("convertOtherCurrencySpend = (%v, %v, %q), want (%v, %v, %q)",
					spent, mixed, rateAsOf, c.wantSpent, c.wantMixed, c.wantRateAsOf)
			}
		})
	}
}
