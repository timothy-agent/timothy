package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
)

// Model list sources (D-172).
const (
	modelSourceLive    = "live"
	modelSourceCatalog = "catalog"
)

// Price sources (D-173), in the order router.Snapshot.Prices resolves
// them: prices_by_model, the catalog, the provider's own listing.
const (
	priceSourceOperator = "operator"
	priceSourceCatalog  = "catalog"
	priceSourceListing  = "listing"
)

// ListedModel is one row of a provider's model list. Prices are per
// million tokens and, like the context window, null when no source
// states them (D-013); PriceSource names the source, empty when none.
type ListedModel struct {
	ID             string   `json:"id"`
	DisplayName    string   `json:"display_name,omitempty"`
	Source         string   `json:"source"`
	InputPerMTok   *float64 `json:"input_per_mtok"`
	OutputPerMTok  *float64 `json:"output_per_mtok"`
	MaxInputTokens *int64   `json:"max_input_tokens"`
	PriceSource    string   `json:"price_source,omitempty"`
}

// modelFetch calls a provider's own listing endpoint.
type modelFetch func(context.Context) ([]provider.AvailableModel, error)

// AvailableModels is the provider's one model list (D-172): the live
// listing when the driver can enumerate, else the catalog restricted to
// the row's candidate vendors. kind='cli' rows never build a chat driver
// (D-051); cursor-cli is the one cli driver with a listing endpoint.
func (a *Admin) AvailableModels(ctx context.Context, id string) ([]ListedModel, error) {
	p, err := a.get(ctx, id)
	if err != nil {
		return nil, err
	}
	fetch, err := a.modelFetcher(p)
	if err != nil {
		return nil, err
	}
	if fetch != nil {
		fetch = a.models.cached(p.ID, fetch)
	}
	return a.modelList(ctx, p, fetch)
}

// modelFetcher returns p's live listing call, nil when its driver
// cannot enumerate.
func (a *Admin) modelFetcher(p Provider) (modelFetch, error) {
	if p.Kind == "cli" {
		if p.Driver != "cursor-cli" {
			return nil, nil
		}
		return func(ctx context.Context) ([]provider.AvailableModel, error) {
			return a.cursorAvailableModels(ctx, p)
		}, nil
	}
	snap := a.store.Snapshot()
	if snap == nil {
		return nil, fmt.Errorf("routing configuration not loaded yet")
	}
	drv, ok := snap.Provider(p.Name)
	if !ok {
		return nil, fmt.Errorf("provider %s not in the serving snapshot; wait for the next reload", p.Name)
	}
	return listerFetch(drv), nil
}

// listerFetch returns drv's bounded listing call, nil when drv cannot
// enumerate.
func listerFetch(drv provider.Provider) modelFetch {
	lister, ok := drv.(provider.ModelLister)
	if !ok {
		return nil
	}
	return func(ctx context.Context) ([]provider.AvailableModel, error) {
		lctx, cancel := context.WithTimeout(ctx, testTimeout)
		defer cancel()
		return lister.ListModels(lctx)
	}
}

// validatedModels is an unsaved config's model list for Validate,
// listed uncached (the config has no id yet). model stands in for an
// unset default_model, which candidate vendor matching reads. nil when
// the listing fails: the probe already passed, so the list is advisory.
func (a *Admin) validatedModels(ctx context.Context, p Provider, model string, drv provider.Provider) []ListedModel {
	if p.DefaultModel == "" {
		p.DefaultModel = model
	}
	models, err := a.modelList(ctx, p, listerFetch(drv))
	if err != nil {
		a.log.Warn("validate model list failed", "provider", p.Name, "error", err)
		return nil
	}
	return models
}

// modelList builds p's list from fetch (nil: cannot list) and the
// catalog rows of p's candidate vendors. A listing error is the
// provider's own and passes through unchanged.
func (a *Admin) modelList(ctx context.Context, p Provider, fetch modelFetch) ([]ListedModel, error) {
	candidates := catalog.CandidateProvidersForRow(p.Kind, p.Driver, p.BaseURL, p.Options, p.Name, p.DefaultModel)
	pool, err := a.catalog.SearchProviders(ctx, "", candidates, -1)
	if err != nil {
		return nil, fmt.Errorf("admin model list: %w", err)
	}
	operator := operatorPrices(p.Options)
	if fetch == nil {
		return catalogModelList(pool, operator), nil
	}
	live, err := fetch(ctx)
	if err != nil {
		return nil, err
	}
	return liveModelList(cliproxyVendorModels(p, live), pool, operator, p.Options["listing_prices"] != "off"), nil
}

// operatorPrices decodes options.prices_by_model; a value that does not
// decode (rejected at write time anyway) contributes nothing.
func operatorPrices(opts map[string]string) map[string]router.ModelPrices {
	var prices map[string]router.ModelPrices
	if raw := opts["prices_by_model"]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &prices)
	}
	return prices
}

// applyOperatorPrice overrides row's price with prices_by_model's when
// the operator declared one for its id.
func applyOperatorPrice(row *ListedModel, operator map[string]router.ModelPrices) {
	if p, ok := operator[row.ID]; ok {
		row.InputPerMTok, row.OutputPerMTok, row.PriceSource = &p.InputPerMTok, &p.OutputPerMTok, priceSourceOperator
	}
}

// liveModelList joins live ids with their exact catalog match in pool.
// A match with a known non chat-like mode drops the id; an unmatched id
// or a match without a mode stays. Prices resolve in router.Snapshot.Prices
// order (D-173): prices_by_model, the listing's own price (when listing
// is on and it states both input and output), then the catalog. The
// catalog also fills a context window the listing leaves empty.
func liveModelList(live []provider.AvailableModel, pool []catalog.Model, operator map[string]router.ModelPrices, listing bool) []ListedModel {
	byKey := make(map[string]catalog.Model, len(pool))
	for _, m := range pool {
		if _, ok := byKey[m.ModelKey]; !ok {
			byKey[m.ModelKey] = m
		}
	}
	out := make([]ListedModel, 0, len(live))
	for _, m := range live {
		row := ListedModel{ID: m.ID, DisplayName: m.DisplayName, Source: modelSourceLive, MaxInputTokens: m.ContextLength}
		if listing && m.InputPerMTok != nil && m.OutputPerMTok != nil {
			row.InputPerMTok, row.OutputPerMTok, row.PriceSource = m.InputPerMTok, m.OutputPerMTok, priceSourceListing
		}
		if key := catalog.Match(m.ID, pool); key != "" {
			cm := byKey[key]
			if cm.Mode != "" && !catalog.ChatLike(cm.Mode) {
				continue
			}
			if row.PriceSource == "" && (cm.InputPerMTok != nil || cm.OutputPerMTok != nil) {
				row.InputPerMTok, row.OutputPerMTok, row.PriceSource = cm.InputPerMTok, cm.OutputPerMTok, priceSourceCatalog
			}
			if row.MaxInputTokens == nil {
				row.MaxInputTokens = cm.MaxInputTokens
			}
		}
		applyOperatorPrice(&row, operator)
		out = append(out, row)
	}
	return out
}

// catalogModelList is pool's chat-like rows, first row per id, with
// prices_by_model winning over the catalog's price.
func catalogModelList(pool []catalog.Model, operator map[string]router.ModelPrices) []ListedModel {
	out := make([]ListedModel, 0, len(pool))
	seen := make(map[string]bool, len(pool))
	for _, m := range pool {
		if !catalog.ChatLike(m.Mode) || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		row := ListedModel{
			ID:             m.ID,
			Source:         modelSourceCatalog,
			InputPerMTok:   m.InputPerMTok,
			OutputPerMTok:  m.OutputPerMTok,
			MaxInputTokens: m.MaxInputTokens,
		}
		if m.InputPerMTok != nil || m.OutputPerMTok != nil {
			row.PriceSource = priceSourceCatalog
		}
		applyOperatorPrice(&row, operator)
		out = append(out, row)
	}
	return out
}

// modelsCacheTTL bounds a cached live listing: pickers call the endpoint
// on every mount, and provider model lists change slowly.
const modelsCacheTTL = 5 * time.Minute

type modelsCacheEntry struct {
	models    []provider.AvailableModel
	fetchedAt time.Time
}

// modelsCache holds live listings keyed by provider id. Only successful
// fetches are cached.
type modelsCache struct {
	mu      sync.Mutex
	entries map[string]modelsCacheEntry
}

func newModelsCache() *modelsCache {
	return &modelsCache{entries: map[string]modelsCacheEntry{}}
}

func (c *modelsCache) get(id string) ([]provider.AvailableModel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok || time.Since(e.fetchedAt) > modelsCacheTTL {
		return nil, false
	}
	return e.models, true
}

func (c *modelsCache) set(id string, models []provider.AvailableModel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[id] = modelsCacheEntry{models: models, fetchedAt: time.Now()}
}

// cached wraps fetch so its listing is served from and stored under id.
func (c *modelsCache) cached(id string, fetch modelFetch) modelFetch {
	return func(ctx context.Context) ([]provider.AvailableModel, error) {
		return c.list(ctx, id, fetch)
	}
}

// list returns id's cached listing or calls fetch.
func (c *modelsCache) list(ctx context.Context, id string, fetch modelFetch) ([]provider.AvailableModel, error) {
	if models, ok := c.get(id); ok {
		return models, nil
	}
	models, err := fetch(ctx)
	if err != nil {
		return nil, err
	}
	c.set(id, models)
	return models, nil
}
