package router

import (
	"context"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

// D-173: aggregators (OpenRouter, Kilo and others) state prices in
// their own /models reply. Every openaicompat row is priced from its
// listing unless options.listing_prices is "off". The listing beats the
// LiteLLM catalog: it is what the provider charges, while the catalog
// is the upstream vendor's list price, which a reseller may not match.
// Order: prices_by_model, listing, catalog, else unknown. The Store
// relists such rows in the background and keeps the last good table
// across snapshot reloads.
const (
	livePriceInterval = 15 * time.Minute
	livePriceTimeout  = 15 * time.Second
)

// livePrices holds stated prices by provider name, then model id.
type livePrices struct {
	mu      sync.RWMutex
	prices  map[string]map[string]ModelPrices
	checked map[string]time.Time
}

func newLivePrices() *livePrices {
	return &livePrices{prices: map[string]map[string]ModelPrices{}, checked: map[string]time.Time{}}
}

// get is nil-safe: a snapshot built outside a Store has no live table.
func (l *livePrices) get(providerName, model string) (ModelPrices, bool) {
	if l == nil {
		return ModelPrices{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	p, ok := l.prices[providerName][model]
	return p, ok
}

// due reports whether providerName was last checked livePriceInterval
// or more before now, and marks it checked, so a failed relist also
// waits a full interval.
func (l *livePrices) due(providerName string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, ok := l.checked[providerName]; ok && now.Sub(last) < livePriceInterval {
		return false
	}
	l.checked[providerName] = now
	return true
}

// set replaces providerName's table with the models that state both an
// input and an output price. A missing cache price stays 0, as for a
// catalog row without one.
func (l *livePrices) set(providerName string, models []provider.AvailableModel) {
	table := make(map[string]ModelPrices, len(models))
	for _, m := range models {
		if m.InputPerMTok == nil || m.OutputPerMTok == nil {
			continue
		}
		p := ModelPrices{InputPerMTok: *m.InputPerMTok, OutputPerMTok: *m.OutputPerMTok}
		if m.CacheReadPerMTok != nil {
			p.CacheReadPerMTok = *m.CacheReadPerMTok
		}
		if m.CacheWritePerMTok != nil {
			p.CacheWritePerMTok = *m.CacheWritePerMTok
		}
		table[m.ID] = p
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prices[providerName] = table
}

// statesPrices reports whether row's listing prices are read (D-173).
// A listing without a pricing block just yields an empty table.
func statesPrices(row ProviderRow) bool {
	return row.Driver == "openaicompat" && !row.ListingPricesOff
}

// refreshLivePrices relists every enabled, healthy row whose listing
// states prices and that is due. A failed relist keeps the last table.
func (s *Store) refreshLivePrices(ctx context.Context, snap *Snapshot, now time.Time) {
	if snap == nil {
		return
	}
	for name, row := range snap.byName {
		if !row.Enabled || !snap.healthy[name] || !statesPrices(row) {
			continue
		}
		drv, ok := snap.registry.Get(name)
		if !ok {
			continue
		}
		lister, ok := drv.(provider.ModelLister)
		if !ok || !s.live.due(name, now) {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, livePriceTimeout)
		models, err := lister.ListModels(lctx)
		cancel()
		if err != nil {
			s.log.Warn("router: live price listing failed; keeping last prices", "provider", name, "error", err)
			continue
		}
		s.live.set(name, models)
	}
}
