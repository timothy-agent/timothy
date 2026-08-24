package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// AvailableModel is one model reported by a provider's listing
// endpoint. Only the id is normalized; providers disagree on the rest.
// DisplayName is set only by drivers whose listing endpoint reports one
// (cursor-cli); empty for the rest.
type AvailableModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

// ModelLister is implemented by providers whose API can enumerate its
// models. Callers type-assert; a provider without it (bedrock) falls
// back to manual model entry in the UI.
type ModelLister interface {
	ListModels(ctx context.Context) ([]AvailableModel, error)
}

// listModels GETs url and decodes the OpenAI-style {"data": [{"id"}]}
// envelope, which the Anthropic models endpoint shares.
func listModels(ctx context.Context, client *http.Client, url string, header func(*http.Request)) ([]AvailableModel, error) {
	resp, err := doWithRetry(ctx, client, maxRetries, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		header(req)
		return req, nil
	}, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Data []AvailableModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	return out.Data, nil
}

// ListModels implements ModelLister via GET {base_url}/models.
func (o *OpenAICompat) ListModels(ctx context.Context) ([]AvailableModel, error) {
	models, err := listModels(ctx, o.client, o.cfg.BaseURL+"/models", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
		for k, v := range o.cfg.Headers {
			req.Header.Set(k, v)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("openaicompat: list models: %w", err)
	}
	return models, nil
}

// ListModels implements ModelLister via GET {base_url}/v1/models.
func (a *Anthropic) ListModels(ctx context.Context) ([]AvailableModel, error) {
	models, err := listModels(ctx, a.client, a.cfg.BaseURL+"/v1/models", func(req *http.Request) {
		req.Header.Set("X-Api-Key", a.cfg.APIKey)
		req.Header.Set("Anthropic-Version", anthropicVersion)
		for k, v := range a.cfg.Headers {
			req.Header.Set(k, v)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: list models: %w", err)
	}
	return models, nil
}
