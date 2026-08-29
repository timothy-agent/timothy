// Package fxrates fetches and stores daily USD-base reference exchange
// rates for DISPLAY conversion — turning a ledger row's honest billing
// currency into the user's chosen default_currency for the Analytics
// and mission-usage views (D-013's cost-honesty invariant is otherwise
// unchanged: nothing here ever rewrites what a ledger row says a
// provider actually billed).
//
// Source: open.er-api.com, not frankfurter.app (the source
// internal/brain/tools/builtin/currency.go's live convert_currency tool
// used before this package existed, now sharing this same source and
// parser). frankfurter.app publishes ECB reference rates, which do not
// cover several currencies in settings.allowedCurrencies (BDT, AED,
// SAR, TWD, VND) — open.er-api.com's USD-base table covers the full
// list with one free, keyless request.
package fxrates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// FetchTimeout bounds one call to the rate provider.
	FetchTimeout = 15 * time.Second
	maxBody      = 256 << 10
	// BaseURL is open.er-api.com's USD-base latest-rates endpoint — no
	// API key, one request returns every quote currency at once.
	BaseURL = "https://open.er-api.com/v6/latest/USD"
)

// apiResponse is the subset of open.er-api.com's JSON response this
// package uses. Verified shape (2026-07, USD base):
//
//	{
//	  "result": "success",
//	  "base_code": "USD",
//	  "time_last_update_utc": "Mon, 21 Jul 2026 00:02:31 +0000",
//	  "rates": {"EUR": 0.856, "GBP": 0.734, ...}
//	}
//
// A non-"success" result (rate limited, provider outage) carries an
// "error-type" field instead of rates — treated as a fetch failure.
type apiResponse struct {
	Result  string             `json:"result"`
	Base    string             `json:"base_code"`
	Updated string             `json:"time_last_update_utc"`
	Rates   map[string]float64 `json:"rates"`
}

// FetchLatest fetches the current USD-base rate table via client
// (caller supplies timeout/transport) from baseURL, returning only the
// quote currencies in `want` — the fetcher's caller (fetch.go) passes
// settings.allowedCurrencies so no unsupported code is ever stored.
// asOf is today's UTC date: open.er-api.com updates once daily, and the
// exact source timestamp format is not worth parsing when the fetcher
// itself already runs on a daily tick.
func FetchLatest(ctx context.Context, client *http.Client, baseURL string, want map[string]bool) (rates map[string]float64, asOf time.Time, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("fxrates: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("fxrates: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, time.Time{}, fmt.Errorf("fxrates: http %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("fxrates: read response: %w", err)
	}
	return ParseLatest(body, want)
}

// ParseLatest decodes an open.er-api.com /latest/USD response body,
// split out from FetchLatest so both the daily fetcher and
// currency.go's live-fallback path share one parser instead of two
// copies of the same shape.
func ParseLatest(body []byte, want map[string]bool) (rates map[string]float64, asOf time.Time, err error) {
	var parsed apiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, time.Time{}, fmt.Errorf("fxrates: parse response: %w", err)
	}
	if parsed.Result != "success" {
		return nil, time.Time{}, fmt.Errorf("fxrates: provider reported result %q", parsed.Result)
	}
	if len(parsed.Rates) == 0 {
		return nil, time.Time{}, fmt.Errorf("fxrates: response carried no rates")
	}
	out := make(map[string]float64, len(want))
	for code := range want {
		if code == "USD" {
			continue // base currency: rate to itself is always 1, never stored
		}
		if r, ok := parsed.Rates[code]; ok && r > 0 {
			out[code] = r
		}
	}
	return out, time.Now().UTC().Truncate(24 * time.Hour), nil
}
