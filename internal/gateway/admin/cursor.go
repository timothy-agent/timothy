package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

// cursorModelsItem mirrors the fields cursorFetchModels reads out of
// Cursor's response; aliases/parameters/variants are documented but
// unused here (AvailableModel normalizes only id and display name).
type cursorModelsItem struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// cursorAvailableModels resolves p's credential and fetches its Cursor
// model list (cached by modelList). The resolved key only ever reaches
// the outbound Authorization header, never logs or the response.
func (a *Admin) cursorAvailableModels(ctx context.Context, p Provider) ([]provider.AvailableModel, error) {
	key, err := a.secrets.Resolve(ctx, p.CredentialRef)
	if err != nil || key == "" {
		return nil, fmt.Errorf("credential %s unresolved", p.CredentialRef)
	}
	return cursorFetchModels(ctx, key)
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
