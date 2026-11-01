package fxrates

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// tickInterval matches the source's own cadence: open.er-api.com
// publishes once a day, so polling faster buys nothing. No advisory
// lock: Upsert's ON CONFLICT DO NOTHING makes two
// instances fetching the same day idempotent, so the simpler ticker-
// only loop is enough here.
const tickInterval = 6 * time.Hour

// Fetcher runs the daily USD-base rate fetch. want restricts stored
// currencies to the supported list (settings.allowedCurrencies) —
// passed in rather than imported, so this package has no dependency on
// brain/settings.
//
// fallbackKey returns the operator's FallbackKeyRef value, or "" when
// none is stored; only then is the fallback source ever contacted.
type Fetcher struct {
	store       *Store
	client      *http.Client
	want        map[string]bool
	fallbackKey func(context.Context) string
	log         *slog.Logger

	primaryURL  string
	fallbackURL string
}

func NewFetcher(store *Store, want map[string]bool, fallbackKey func(context.Context) string, log *slog.Logger) *Fetcher {
	return &Fetcher{
		store:       store,
		client:      &http.Client{Timeout: FetchTimeout},
		want:        want,
		fallbackKey: fallbackKey,
		log:         log,
		primaryURL:  BaseURL,
		fallbackURL: FallbackURL,
	}
}

// Run fetches immediately, then on every tick until ctx ends. A fetch
// failure logs and retries next tick — stale rates are acceptable
// (D-design: absent rate degrades display, never a guess), so a
// transient outage must never crash or block the caller.
func (f *Fetcher) Run(ctx context.Context) {
	f.tick(ctx)
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.tick(ctx)
		}
	}
}

func (f *Fetcher) tick(ctx context.Context) {
	rates, asOf, err := f.fetch(ctx)
	if err != nil {
		f.log.Warn("fxrates: fetch failed; will retry next tick", "error", err)
		return
	}
	if err := f.store.Upsert(ctx, asOf, rates); err != nil {
		f.log.Warn("fxrates: store failed", "error", err)
		return
	}
	f.log.Info("fxrates: rates updated", "currencies", len(rates), "as_of", asOf.Format("2006-01-02"))
}

// fetch tries the keyless primary, then the fallback only when the
// primary failed and a fallback key is stored. Each attempt gets its
// own FetchTimeout.
func (f *Fetcher) fetch(ctx context.Context) (map[string]float64, time.Time, error) {
	pctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	rates, asOf, err := FetchLatest(pctx, f.client, f.primaryURL, f.want)
	cancel()
	if err == nil {
		return rates, asOf, nil
	}
	key := ""
	if f.fallbackKey != nil {
		key = strings.TrimSpace(f.fallbackKey(ctx))
	}
	if key == "" {
		return nil, time.Time{}, err
	}
	f.log.Warn("fxrates: primary fetch failed; trying fallback source", "error", err)
	fctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	rates, asOf, ferr := FetchFallback(fctx, f.client, f.fallbackURL, key, f.want)
	if ferr != nil {
		return nil, time.Time{}, errors.Join(err, ferr)
	}
	return rates, asOf, nil
}
