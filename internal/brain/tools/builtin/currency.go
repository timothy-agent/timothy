package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	currencyTimeout = 15 * time.Second
	currencyMaxBody = 64 << 10
	currencyBaseURL = "https://api.frankfurter.app"
)

type currencyConvertArgs struct {
	Amount float64 `json:"amount"`
	From   string  `json:"from"`
	To     string  `json:"to"`
}

// currencyResponse is the subset of frankfurter.app's JSON response
// this tool uses (ECB reference rates, updated daily on banking days).
type currencyResponse struct {
	Amount float64            `json:"amount"`
	Base   string             `json:"base"`
	Date   string             `json:"date"`
	Rates  map[string]float64 `json:"rates"`
}

// ConvertCurrency converts an amount between currencies using daily ECB
// reference rates from frankfurter.app (no API key required).
func ConvertCurrency() *tools.Tool {
	return newCurrencyConverter(&http.Client{Timeout: currencyTimeout}, currencyBaseURL)
}

// newCurrencyConverter builds the tool against an injectable client and
// base URL so tests can point it at a stub server instead of the live
// exchange-rate service.
func newCurrencyConverter(client *http.Client, baseURL string) *tools.Tool {
	return &tools.Tool{
		Name: "currency_convert",
		Description: `Converts an amount from one currency to another using
daily reference exchange rates (ECB, updated on banking days).

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
			return convertCurrency(ctx, client, baseURL, args.Amount, from, to)
		},
	}
}

func convertCurrency(ctx context.Context, client *http.Client, baseURL string, amount float64, from, to string) (string, error) {
	q := url.Values{"amount": {fmt.Sprintf("%g", amount)}, "from": {from}, "to": {to}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(baseURL, "/")+"/latest?"+q.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchange rate lookup failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("unknown currency code (from=%s, to=%s): use ISO 4217 codes like EUR, USD, GBP", from, to)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("exchange rate service returned http %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, currencyMaxBody))
	if err != nil {
		return "", fmt.Errorf("read exchange rate response: %w", err)
	}
	var parsed currencyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse exchange rate response: %w", err)
	}
	converted, ok := parsed.Rates[to]
	if !ok {
		return "", fmt.Errorf("exchange rate service returned no rate for %s", to)
	}
	rate := 0.0
	if amount != 0 {
		rate = converted / amount
	}
	return fmt.Sprintf("%.2f %s = %.2f %s (rate %.5f, as of %s)",
		amount, from, converted, to, rate, parsed.Date), nil
}
