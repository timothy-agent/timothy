package fxrates

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// tickInterval matches the source's own cadence: open.er-api.com
// publishes once a day, so polling faster buys nothing. No advisory
// lock (unlike scheduler.go's cross-instance dedup, which guards a
// side-effecting fire): Upsert's ON CONFLICT DO NOTHING makes two
// instances fetching the same day idempotent, so the simpler ticker-
// only loop is enough here.
const tickInterval = 6 * time.Hour

// Fetcher runs the daily USD-base rate fetch. want restricts stored
// currencies to the supported list (settings.allowedCurrencies) —
// passed in rather than imported, so this package has no dependency on
// brain/settings.
type Fetcher struct {
	store  *Store
	client *http.Client
	want   map[string]bool
	log    *slog.Logger
}

func NewFetcher(store *Store, want map[string]bool, log *slog.Logger) *Fetcher {
	return &Fetcher{
		store:  store,
		client: &http.Client{Timeout: FetchTimeout},
		want:   want,
		log:    log,
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
	fctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	rates, asOf, err := FetchLatest(fctx, f.client, BaseURL, f.want)
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
