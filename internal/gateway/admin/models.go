package admin

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

// Model list sources (D-172).
const (
	modelSourceLive    = "live"
	modelSourceCatalog = "catalog"
)

// ListedModel is one row of a provider's model list. Prices are per
// million tokens and, like the context window, null when no exact
// catalog match exists (D-013).
type ListedModel struct {
	ID             string   `json:"id"`
	DisplayName    string   `json:"display_name,omitempty"`
	Source         string   `json:"source"`
	InputPerMTok   *float64 `json:"input_per_mtok"`
	OutputPerMTok  *float64 `json:"output_per_mtok"`
	MaxInputTokens *int64   `json:"max_input_tokens"`
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
	lister, ok := drv.(provider.ModelLister)
	if !ok {
		return nil, nil
	}
	return func(ctx context.Context) ([]provider.AvailableModel, error) {
		lctx, cancel := context.WithTimeout(ctx, testTimeout)
		defer cancel()
		return lister.ListModels(lctx)
	}, nil
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
	if fetch == nil {
		return catalogModelList(pool), nil
	}
	live, err := a.models.list(ctx, p.ID, fetch)
	if err != nil {
		return nil, err
	}
	return liveModelList(cliproxyVendorModels(p, live), pool), nil
}

// liveModelList joins live ids with their exact catalog match in pool.
// A match with a known non chat-like mode drops the id; an unmatched id
// or a match without a mode stays.
func liveModelList(live []provider.AvailableModel, pool []catalog.Model) []ListedModel {
	byKey := make(map[string]catalog.Model, len(pool))
	for _, m := range pool {
		if _, ok := byKey[m.ModelKey]; !ok {
			byKey[m.ModelKey] = m
		}
	}
	out := make([]ListedModel, 0, len(live))
	for _, m := range live {
		row := ListedModel{ID: m.ID, DisplayName: m.DisplayName, Source: modelSourceLive}
		if key := catalog.Match(m.ID, pool); key != "" {
			cm := byKey[key]
			if cm.Mode != "" && !catalog.ChatLike(cm.Mode) {
				continue
			}
			row.InputPerMTok, row.OutputPerMTok, row.MaxInputTokens = cm.InputPerMTok, cm.OutputPerMTok, cm.MaxInputTokens
		}
		out = append(out, row)
	}
	return out
}

// catalogModelList is pool's chat-like rows, first row per id.
func catalogModelList(pool []catalog.Model) []ListedModel {
	out := make([]ListedModel, 0, len(pool))
	seen := make(map[string]bool, len(pool))
	for _, m := range pool {
		if !catalog.ChatLike(m.Mode) || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, ListedModel{
			ID:             m.ID,
			Source:         modelSourceCatalog,
			InputPerMTok:   m.InputPerMTok,
			OutputPerMTok:  m.OutputPerMTok,
			MaxInputTokens: m.MaxInputTokens,
		})
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
