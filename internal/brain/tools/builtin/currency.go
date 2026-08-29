package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/fxrates"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	currencyTimeout = 15 * time.Second
)

type currencyConvertArgs struct {
	Amount float64 `json:"amount"`
	From   string  `json:"from"`
	To     string  `json:"to"`
}

// CurrencyLookup answers "what is from->to today" from the fx_rates
// table (internal/brain/fxrates.Store.LatestUSDRate, composed for a
// direct pair by CurrencyLookupFromStore below) — the primary path a
// live turn's convert_currency call takes. ok is false when the table
// has no usable rate for this pair (missing, or older than the store's
// own staleness bound), in which case the tool falls back to a live
// fetch rather than guess. asOf is formatted "2006-01-02".
type CurrencyLookup func(ctx context.Context, from, to string) (rate float64, asOf string, ok bool, err error)

// CurrencyLookupFromStore adapts fxrates.Store's USD-base rate table
// into the single-pair CurrencyLookup shape ConvertCurrency wants —
// the fx_rates table only stores USD-base rates, so any pair not
// involving USD is a cross computed via fxrates.Convert, same as
// display conversion (Analytics, mission usage) does.
func CurrencyLookupFromStore(store *fxrates.Store) CurrencyLookup {
	return func(ctx context.Context, from, to string) (float64, string, bool, error) {
		rates, err := store.LatestUSDRates(ctx)
		if err != nil {
			return 0, "", false, err
		}
		fresh := map[string]fxrates.Rate{}
		for code, r := range rates {
			if time.Since(r.AsOf) <= 7*24*time.Hour {
				fresh[code] = r
			}
		}
		rate, asOf, ok := fxrates.Convert(1, from, to, fresh)
		if !ok {
			return 0, "", false, nil
		}
		return rate, asOf.AsOf.Format("2006-01-02"), true, nil
	}
}

// ConvertCurrency converts an amount between currencies. lookup is
// consulted first (the fx_rates table this same package's daily
// fetcher — internal/brain/fxrates.Fetcher — maintains); nil, or a
// miss/stale result, falls back to a live fetch from open.er-api.com,
// the same source and response parser fxrates' daily fetcher uses (no
// second copy of the parsing logic). A nil lookup keeps the tool
// usable even with the rates table empty or the fxrates package
// unwired.
func ConvertCurrency(lookup CurrencyLookup) *tools.Tool {
	return newCurrencyConverter(lookup, &http.Client{Timeout: currencyTimeout}, fxrates.BaseURL)
}

// newCurrencyConverter builds the tool against an injectable
// lookup/client/base URL so tests can fake the table and point the
// fallback at a stub server instead of the live exchange-rate service.
func newCurrencyConverter(lookup CurrencyLookup, client *http.Client, baseURL string) *tools.Tool {
	return &tools.Tool{
		Name: "convert_currency",
		Description: `Converts an amount from one currency to another using
daily USD-base reference exchange rates (open.er-api.com, updated
daily; falls back to a live lookup when no stored rate is available).

Use for any cross-currency amount — "how much is €112 in USD",
comparing a foreign booking total against a home-currency budget, or
totaling a multi-currency trip. Never estimate an exchange rate from
memory — rates move and a stale guess can be off by a lot.

Arguments:
- amount (number, required): quantity to convert.
- from (string, required): source currency, ISO 4217 code (e.g. "EUR").
- to (string, required): target currency, ISO 4217 code (e.g. "USD").

Returns the converted amount, the rate used, and the rate's date.

Example: {"amount": 112, "from": "EUR", "to": "USD"} →
"112.00 EUR = 131.40 USD (rate 1.17325, as of 2026-07-21)"`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"amount": {
					"type": "number",
					"description": "Quantity to convert"
				},
				"from": {
					"type": "string",
					"description": "Source currency, ISO 4217 code (e.g. EUR)"
				},
				"to": {
					"type": "string",
					"description": "Target currency, ISO 4217 code (e.g. USD)"
				}
			},
			"required": ["amount", "from", "to"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args currencyConvertArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			from := strings.ToUpper(strings.TrimSpace(args.From))
			to := strings.ToUpper(strings.TrimSpace(args.To))
			if from == "" || to == "" {
				return "", fmt.Errorf("from and to must both be ISO 4217 currency codes")
			}
			if from == to {
				return fmt.Sprintf("%.2f %s = %.2f %s (same currency)", args.Amount, from, args.Amount, to), nil
			}
			if lookup != nil {
				if rate, asOf, ok, err := lookup(ctx, from, to); err == nil && ok {
					return fmt.Sprintf("%.2f %s = %.2f %s (rate %.5f, as of %s)",
						args.Amount, from, args.Amount*rate, to, rate, asOf), nil
				}
			}
			return convertCurrencyLive(ctx, client, baseURL, args.Amount, from, to)
		},
	}
}

// convertCurrencyLive is the fallback path: a live fetch from
// open.er-api.com when the fx_rates table has no usable rate for this
// pair (table empty, pair stale, or lookup itself unwired).
func convertCurrencyLive(ctx context.Context, client *http.Client, baseURL string, amount float64, from, to string) (string, error) {
	want := map[string]bool{from: true, to: true, "USD": true}
	rates, asOf, err := fxrates.FetchLatest(ctx, client, baseURL, want)
	if err != nil {
		return "", fmt.Errorf("exchange rate lookup failed: %w", err)
	}
	usdRates := make(map[string]fxrates.Rate, len(rates))
	for code, r := range rates {
		usdRates[code] = fxrates.Rate{Value: r, AsOf: asOf}
	}
	converted, rateInfo, ok := fxrates.Convert(amount, from, to, usdRates)
	if !ok {
		return "", fmt.Errorf("unknown currency code (from=%s, to=%s): use ISO 4217 codes like EUR, USD, GBP", from, to)
	}
	rate := 0.0
	if amount != 0 {
		rate = converted / amount
	}
	rateDate := asOf.Format("2006-01-02")
	if !rateInfo.AsOf.IsZero() {
		rateDate = rateInfo.AsOf.Format("2006-01-02")
	}
	return fmt.Sprintf("%.2f %s = %.2f %s (rate %.5f, as of %s)",
		amount, from, converted, to, rate, rateDate), nil
}
