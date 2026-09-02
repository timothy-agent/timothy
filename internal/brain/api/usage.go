package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/fxrates"
	"github.com/SumonMSelim/timothy/internal/brain/settings"
)

// DecorateUsageResponse rewrites a gateway usage-endpoint JSON response
// (proxied via httputil.ReverseProxy.ModifyResponse) in place, adding
// converted_amount/converted_currency/rate_as_of next to every
// {amount|cost, currency} entry it finds — gateway itself has no
// settings access (it's a separate, settings-unaware service) and no
// fx_rates reader, so decoration happens here in brain, the one place
// that already holds both the default_currency setting and the
// fxrates.Store. The gateway's own JSON shape and field names are left
// completely untouched; this only adds sibling fields.
//
// target is the resolved default_currency; rates is the USD-base table
// (nil-safe: a nil or fetch-failed table just means no row gets
// decorated, same as a genuinely absent rate — never a guess).
func DecorateUsageResponse(body []byte, target string, rates map[string]fxrates.Rate) []byte {
	var generic any
	if err := json.Unmarshal(body, &generic); err != nil {
		return body // not JSON (or a proxy error body) — pass through untouched
	}
	decorateAny(generic, target, rates)
	out, err := json.Marshal(generic)
	if err != nil {
		return body
	}
	return out
}

// decorateAny walks an arbitrary decoded JSON value looking for
// objects that carry BOTH a numeric money field ("cost" or "amount")
// and a "currency" string — Summary, SeriesPoint, GroupTotal,
// SessionUsage, and BudgetWindow's nested limit all share this exact
// shape, so one structural match covers every one of them. MissionUsage's
// cost_by_currency/unbilled_cost_by_currency are currency-keyed maps
// instead ({"USD": 1.5}, no per-entry currency field to match against)
// and get their own small special case in decorateObject.
func decorateAny(v any, target string, rates map[string]fxrates.Rate) {
	switch node := v.(type) {
	case map[string]any:
		decorateObject(node, target, rates)
		for _, child := range node {
			decorateAny(child, target, rates)
		}
	case []any:
		for _, child := range node {
			decorateAny(child, target, rates)
		}
	}
}

// costByCurrencyKey is MissionUsage's field name
// (internal/gateway/ledger.MissionUsage.CostByCurrency's json tag) —
// named explicitly since this shape (a currency-keyed map, not a
// {amount,currency} object) needs its own decoration path.
const costByCurrencyKey = "cost_by_currency"

// unbilledCostByCurrencyKey is MissionUsage's sibling field for spend
// billed through a subscription/oauth_token executor (D-051) — same
// currency-keyed map shape, decorated the same way.
const unbilledCostByCurrencyKey = "unbilled_cost_by_currency"

// decorateCostByCurrency adds a parallel "converted_"+convertedKey
// map next to the given currency-keyed cost map, converting every
// entry into target and summing what converts — entries with no
// usable rate are simply omitted from the converted map (never
// guessed), so a caller comparing len() against the original map can
// tell some entry didn't convert.
func decorateCostByCurrency(obj map[string]any, key string, target string, rates map[string]fxrates.Rate) {
	raw, ok := obj[key].(map[string]any)
	if !ok || len(raw) == 0 {
		return
	}
	converted := map[string]float64{}
	var asOf string
	for currency, v := range raw {
		amount, isNum := v.(float64)
		if !isNum {
			continue
		}
		c, rate, convertOK := fxrates.Convert(amount, currency, target, rates)
		if !convertOK {
			continue
		}
		converted[target] += c
		if !rate.AsOf.IsZero() {
			asOf = rate.AsOf.Format("2006-01-02")
		}
	}
	if len(converted) == 0 {
		return
	}
	obj["converted_"+key] = converted
	if asOf != "" {
		obj["rate_as_of"] = asOf
	}
}

func decorateObject(obj map[string]any, target string, rates map[string]fxrates.Rate) {
	if _, ok := obj[costByCurrencyKey]; ok {
		decorateCostByCurrency(obj, costByCurrencyKey, target, rates)
	}
	if _, ok := obj[unbilledCostByCurrencyKey]; ok {
		decorateCostByCurrency(obj, unbilledCostByCurrencyKey, target, rates)
	}
	currency, _ := obj["currency"].(string)
	if currency == "" || currency == target {
		return
	}
	amount, ok := moneyField(obj)
	if !ok {
		return
	}
	converted, rate, convertOK := fxrates.Convert(amount, currency, target, rates)
	if !convertOK {
		return
	}
	obj["converted_amount"] = converted
	obj["converted_currency"] = target
	if !rate.AsOf.IsZero() {
		obj["rate_as_of"] = rate.AsOf.Format("2006-01-02")
	}

	// unbilled_cost (Summary/SeriesPoint's metered-price-equivalent
	// scalar for subscription/oauth_token spend, D-051) shares the
	// object's own currency/target/rate — same conversion, its own
	// sibling field, so the analytics spend tile's annotation can show
	// the converted figure exactly like the billed amount does.
	if unbilledCost, isNum := obj["unbilled_cost"].(float64); isNum && unbilledCost != 0 {
		if converted, _, ok := fxrates.Convert(unbilledCost, currency, target, rates); ok {
			obj["converted_unbilled_cost"] = converted
		}
	}
}

// WithReviewTokenCeiling adds review_token_ceiling next to a mission
// usage object's review_input_tokens (D-097): the gateway aggregates
// the tokens, the ceiling is a brain setting, so the join happens
// here. Any other response passes through untouched.
func WithReviewTokenCeiling(body []byte, ceiling int64) []byte {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	if _, ok := obj["review_input_tokens"]; !ok {
		return body
	}
	obj["review_token_ceiling"] = ceiling
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// moneyField finds the row's monetary amount under whichever key this
// response shape uses ("cost" for aggregator rows, "amount" for a
// BudgetLimit) — both never coexist on the same object, so checking
// both by name is enough without knowing the caller's endpoint.
func moneyField(obj map[string]any) (amount float64, ok bool) {
	for _, k := range []string{"cost", "amount"} {
		if n, isNum := obj[k].(float64); isNum {
			return n, true
		}
	}
	return 0, false
}

// UsageDecorator resolves the default_currency setting and the latest
// stored fx rates once per intercepted response, then decorates it —
// the httputil.ReverseProxy.ModifyResponse hook main.go's adminProxy
// installs for the /v1/admin/usage/* sub-tree calls this directly.
type UsageDecorator struct {
	flags *settings.Store
	rates *fxrates.Store
}

func NewUsageDecorator(flags *settings.Store, rates *fxrates.Store) *UsageDecorator {
	return &UsageDecorator{flags: flags, rates: rates}
}

// Decorate rewrites resp.Body in place (and fixes up Content-Length),
// matching httputil.ReverseProxy.ModifyResponse's contract: return an
// error to make the proxy fall back to its ErrorHandler, or nil to let
// the (possibly rewritten) response continue through. A non-2xx or
// non-JSON response is passed through untouched — no error is ever
// surfaced from decoration failing; a degraded conversion must never
// take down a usage read that otherwise succeeded.
func (d *UsageDecorator) Decorate(resp *http.Response) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("usage decorator: read body: %w", err)
	}

	ctx := resp.Request.Context()
	target := d.flags.DefaultCurrency(ctx)
	rates, err := d.rates.LatestUSDRates(ctx)
	if err != nil {
		rates = nil // degrade to "nothing converts," never guess
	}
	decorated := DecorateUsageResponse(body, target, rates)
	decorated = WithReviewTokenCeiling(decorated, d.flags.ReviewTokenCeiling(ctx))

	resp.Body = io.NopCloser(bytes.NewReader(decorated))
	resp.ContentLength = int64(len(decorated))
	resp.Header.Set("Content-Length", fmt.Sprint(len(decorated)))
	return nil
}
