package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/fxrates"
)

func TestDecorateUsageResponse(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	// fxrates.Convert takes a USD-base table (quote -> units per 1 USD):
	// 1 USD = 0.86 EUR.
	rates := map[string]fxrates.Rate{"EUR": {Value: 0.86, AsOf: asOf}}

	tests := []struct {
		name   string
		body   string
		target string
		rates  map[string]fxrates.Rate
		want   map[string]any // expected top-level decorated fields, checked via decoded round-trip
	}{
		{
			name:   "summary row gets a converted_amount",
			body:   `{"summaries":[{"currency":"USD","cost":100}]}`,
			target: "EUR",
			rates:  rates,
		},
		{
			name:   "row already in target currency is left untouched",
			body:   `{"summaries":[{"currency":"EUR","cost":50}]}`,
			target: "EUR",
			rates:  rates,
		},
		{
			name:   "missing rate leaves the row undecorated",
			body:   `{"summaries":[{"currency":"GBP","cost":10}]}`,
			target: "EUR",
			rates:  rates,
		},
		{
			name:   "non-JSON body passes through untouched",
			body:   `not json`,
			target: "EUR",
			rates:  rates,
		},
		{
			name:   "budget limit's amount field also decorates",
			body:   `{"day":{"currency":"USD","limit":{"amount":20,"currency":"USD"},"spend":5}}`,
			target: "EUR",
			rates:  rates,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := DecorateUsageResponse([]byte(tt.body), tt.target, tt.rates)
			switch tt.name {
			case "non-JSON body passes through untouched":
				if string(out) != tt.body {
					t.Fatalf("out = %q, want unchanged %q", out, tt.body)
				}
				return
			}
			var decoded map[string]any
			if err := json.Unmarshal(out, &decoded); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			switch tt.name {
			case "summary row gets a converted_amount":
				row := decoded["summaries"].([]any)[0].(map[string]any)
				if row["converted_currency"] != "EUR" {
					t.Fatalf("row = %+v, want converted_currency EUR", row)
				}
				if got := row["converted_amount"].(float64); got < 85 || got > 87 {
					t.Fatalf("converted_amount = %v, want ~86", got)
				}
				if row["rate_as_of"] != "2026-07-20" {
					t.Fatalf("rate_as_of = %v, want 2026-07-20", row["rate_as_of"])
				}
			case "row already in target currency is left untouched":
				row := decoded["summaries"].([]any)[0].(map[string]any)
				if _, ok := row["converted_amount"]; ok {
					t.Fatalf("row = %+v, want no converted_amount when currency == target", row)
				}
			case "missing rate leaves the row undecorated":
				row := decoded["summaries"].([]any)[0].(map[string]any)
				if _, ok := row["converted_amount"]; ok {
					t.Fatalf("row = %+v, want no converted_amount when no rate exists", row)
				}
			case "budget limit's amount field also decorates":
				limit := decoded["day"].(map[string]any)["limit"].(map[string]any)
				if limit["converted_currency"] != "EUR" {
					t.Fatalf("limit = %+v, want converted_currency EUR", limit)
				}
			}
		})
	}
}

func TestDecorateUsageResponseMissionUsageCostByCurrency(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	rates := map[string]fxrates.Rate{"EUR": {Value: 0.86, AsOf: asOf}}
	// cost_by_currency is a plain map[string]float64, not a
	// {amount,currency} object — decorateCostByCurrency's special case
	// adds a parallel converted_cost_by_currency map next to it, and
	// the original is left byte-for-byte unchanged (cost honesty).
	body := `{"mission_id":"m1","cost_by_currency":{"USD":10}}`
	out := DecorateUsageResponse([]byte(body), "EUR", rates)
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cbc := decoded["cost_by_currency"].(map[string]any)
	if cbc["USD"].(float64) != 10 {
		t.Fatalf("cost_by_currency = %+v, want USD unchanged at 10", cbc)
	}
	converted, ok := decoded["converted_cost_by_currency"].(map[string]any)
	if !ok {
		t.Fatalf("decoded = %+v, want a converted_cost_by_currency map", decoded)
	}
	if got := converted["EUR"].(float64); got < 8 || got > 9 {
		t.Fatalf("converted_cost_by_currency[EUR] = %v, want ~8.6", got)
	}
	if decoded["rate_as_of"] != "2026-07-20" {
		t.Fatalf("rate_as_of = %v, want 2026-07-20", decoded["rate_as_of"])
	}
}

func TestDecorateUsageResponseMissionUsageUnbilledCostByCurrency(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	rates := map[string]fxrates.Rate{"EUR": {Value: 0.86, AsOf: asOf}}
	// unbilled_cost_by_currency gets the same decoration as
	// cost_by_currency, under its own converted_ key — both left
	// byte-for-byte unchanged, only a parallel converted map is added.
	body := `{"mission_id":"m1","cost_by_currency":{"USD":10},"unbilled_cost_by_currency":{"USD":5}}`
	out := DecorateUsageResponse([]byte(body), "EUR", rates)
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["unbilled_cost_by_currency"].(map[string]any)["USD"].(float64) != 5 {
		t.Fatalf("unbilled_cost_by_currency = %+v, want USD unchanged at 5", decoded["unbilled_cost_by_currency"])
	}
	converted, ok := decoded["converted_unbilled_cost_by_currency"].(map[string]any)
	if !ok {
		t.Fatalf("decoded = %+v, want a converted_unbilled_cost_by_currency map", decoded)
	}
	if got := converted["EUR"].(float64); got < 4 || got > 5 {
		t.Fatalf("converted_unbilled_cost_by_currency[EUR] = %v, want ~4.3", got)
	}
	if decoded["converted_cost_by_currency"].(map[string]any)["EUR"].(float64) < 8 {
		t.Fatalf("converted_cost_by_currency should still be decorated too: %+v", decoded)
	}
}

func TestDecorateUsageResponseSummaryUnbilledCost(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	rates := map[string]fxrates.Rate{"EUR": {Value: 0.86, AsOf: asOf}}
	// Summary/SeriesPoint's unbilled_cost scalar shares the row's own
	// currency/cost — gets a converted_unbilled_cost sibling the same
	// way cost gets converted_amount, so the analytics spend tile's
	// unbilled annotation can show the converted figure like the
	// billed amount does.
	body := `{"summaries":[{"currency":"USD","cost":100,"unbilled_cost":10}]}`
	out := DecorateUsageResponse([]byte(body), "EUR", rates)
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	row := decoded["summaries"].([]any)[0].(map[string]any)
	if row["unbilled_cost"].(float64) != 10 {
		t.Fatalf("unbilled_cost = %+v, want unchanged at 10", row["unbilled_cost"])
	}
	got, ok := row["converted_unbilled_cost"].(float64)
	if !ok {
		t.Fatalf("row = %+v, want a converted_unbilled_cost field", row)
	}
	if got < 8 || got > 9 {
		t.Fatalf("converted_unbilled_cost = %v, want ~8.6", got)
	}
}

func TestDecorateUsageResponseSummaryUnbilledCostZeroOmitted(t *testing.T) {
	t.Parallel()
	rates := map[string]fxrates.Rate{"EUR": {Value: 0.86}}
	// Zero unbilled_cost (the common case: no subscription/oauth_token
	// spend in range) must never grow a converted_unbilled_cost field —
	// same convention as decorateCostByCurrency omitting empty maps.
	body := `{"summaries":[{"currency":"USD","cost":100,"unbilled_cost":0}]}`
	out := DecorateUsageResponse([]byte(body), "EUR", rates)
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	row := decoded["summaries"].([]any)[0].(map[string]any)
	if _, ok := row["converted_unbilled_cost"]; ok {
		t.Fatalf("row = %+v, want no converted_unbilled_cost for a zero unbilled_cost", row)
	}
}

func TestDecorateUsageResponseSummaryUnbilledCostMissingRateOmitted(t *testing.T) {
	t.Parallel()
	rates := map[string]fxrates.Rate{} // no stored rates at all
	// No rate for the row's currency: converted_amount is already
	// omitted by the existing logic, and converted_unbilled_cost must
	// follow the same "never guess" rule (D-013) rather than falling
	// back to some other figure.
	body := `{"summaries":[{"currency":"GBP","cost":100,"unbilled_cost":10}]}`
	out := DecorateUsageResponse([]byte(body), "EUR", rates)
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	row := decoded["summaries"].([]any)[0].(map[string]any)
	if _, ok := row["converted_unbilled_cost"]; ok {
		t.Fatalf("row = %+v, want no converted_unbilled_cost when no rate exists", row)
	}
}

func TestDecorateUsageResponseMissionUsageUnconvertibleCurrencyOmitted(t *testing.T) {
	t.Parallel()
	rates := map[string]fxrates.Rate{} // no stored rates at all
	body := `{"mission_id":"m1","cost_by_currency":{"GBP":5}}`
	out := DecorateUsageResponse([]byte(body), "EUR", rates)
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := decoded["converted_cost_by_currency"]; ok {
		t.Fatalf("decoded = %+v, want no converted_cost_by_currency when nothing converts", decoded)
	}
}

// TestWithReviewTokenCeiling pins D-097: a mission usage body gains
// review_token_ceiling next to its review_input_tokens; every other
// body passes through byte for byte.
func TestWithReviewTokenCeiling(t *testing.T) {
	t.Parallel()
	out := WithReviewTokenCeiling([]byte(`{"mission_id":"m1","review_input_tokens":42}`), 1_500_000)
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["review_token_ceiling"] != float64(1_500_000) || decoded["review_input_tokens"] != float64(42) {
		t.Fatalf("decoded = %+v, want review_token_ceiling 1500000 beside review_input_tokens 42", decoded)
	}
	for _, body := range []string{`{"summaries":[{"currency":"USD","cost":1}]}`, `not json`, `[1,2]`} {
		if got := WithReviewTokenCeiling([]byte(body), 5); string(got) != body {
			t.Fatalf("WithReviewTokenCeiling(%q) = %q, want unchanged", body, got)
		}
	}
}
