package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

// cursorModelsURL is Cursor's own model-listing endpoint. Fixed and
// hardcoded, never operator/user input, so this follows the same
// unguarded-client precedent as catalog.Fetch's LiteLLM URL rather than
// netguard (that guard is for user/model-supplied destinations).
// var, not const, so tests can point it at a fake server.
var cursorModelsURL = "https://api.cursor.com/v1/models"

const cursorModelsTimeout = 5 * time.Second

// cursorModelsCacheTTL bounds how long a provider's listing is cached
// in-memory: the UI calls this on every mount/toggle while editing a
// provider, and Cursor's catalog changes slowly enough that a 5 minute
// cache avoids hammering it without going stale during one edit session.
const cursorModelsCacheTTL = 5 * time.Minute

// cursorModelsCacheEntry is one provider id's cached listing.
type cursorModelsCacheEntry struct {
	models    []provider.AvailableModel
	fetchedAt time.Time
}

// cursorModelsCache is a small in-memory TTL cache keyed by provider id.
// mu guards the map itself, not entries (entries are replaced wholesale).
type cursorModelsCache struct {
	mu      sync.Mutex
	entries map[string]cursorModelsCacheEntry
}

var cursorCache = &cursorModelsCache{entries: map[string]cursorModelsCacheEntry{}}

func (c *cursorModelsCache) get(id string) ([]provider.AvailableModel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok || time.Since(e.fetchedAt) > cursorModelsCacheTTL {
		return nil, false
	}
	return e.models, true
}

func (c *cursorModelsCache) set(id string, models []provider.AvailableModel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[id] = cursorModelsCacheEntry{models: models, fetchedAt: time.Now()}
}

// cursorModelsItem mirrors the fields cursorFetchModels reads out of
// Cursor's response; aliases/parameters/variants are documented but
// unused here (AvailableModel normalizes only id and display name).
type cursorModelsItem struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// cursorAvailableModels resolves p's credential and returns its cached
// or freshly-fetched Cursor model list. The resolved key only ever
// reaches the outbound Authorization header, never logs or the response.
func (a *Admin) cursorAvailableModels(ctx context.Context, p Provider) ([]provider.AvailableModel, error) {
	if models, ok := cursorCache.get(p.ID); ok {
		return models, nil
	}

	key, err := a.secrets.Resolve(ctx, p.CredentialRef)
	if err != nil || key == "" {
		return nil, fmt.Errorf("credential %s unresolved", p.CredentialRef)
	}

	models, err := cursorFetchModels(ctx, key)
	if err != nil {
		return nil, err
	}
	cursorCache.set(p.ID, models)
	return models, nil
}

// cursorFetchModels calls Cursor's model-listing endpoint with key as a
// bearer token. Upstream errors are terse and never include key
// material.
func cursorFetchModels(ctx context.Context, key string) ([]provider.AvailableModel, error) {
	tctx, cancel := context.WithTimeout(ctx, cursorModelsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(tctx, http.MethodGet, cursorModelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cursor: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: cursorModelsTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cursor: list models: %w", ErrUpstream)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cursor: list models: upstream returned %d: %w", resp.StatusCode, ErrUpstream)
	}

	var out struct {
		Items []cursorModelsItem `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("cursor: decode models: %w", ErrUpstream)
	}

	models := make([]provider.AvailableModel, 0, len(out.Items))
	for _, item := range out.Items {
		models = append(models, provider.AvailableModel{ID: item.ID, DisplayName: item.DisplayName})
	}
	return models, nil
}
