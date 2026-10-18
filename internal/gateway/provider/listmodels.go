package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// AvailableModel is one model reported by a provider's listing
// endpoint. Only the id is normalized; providers disagree on the rest.
// DisplayName is set only by drivers whose listing endpoint reports one
// (cursor-cli, anthropic, bedrock); empty for the rest.
//
// Prices (USD per million tokens) and ContextLength are set only from
// an openaicompat listing that states them in OpenRouter's shape
// (D-173); nil is unknown, never zero. They are never decoded straight from a reply, so another
// driver's envelope cannot fill them by accident.
type AvailableModel struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"display_name,omitempty"`
	InputPerMTok      *float64 `json:"-"`
	OutputPerMTok     *float64 `json:"-"`
	CacheReadPerMTok  *float64 `json:"-"`
	CacheWritePerMTok *float64 `json:"-"`
	ContextLength     *int64   `json:"-"`
}

// ModelLister is implemented by providers whose API can enumerate its
// models. Callers type-assert; a provider without it gets the catalog
// rows for its vendors instead (D-172).
type ModelLister interface {
	ListModels(ctx context.Context) ([]AvailableModel, error)
}

// listModels GETs url and decodes the OpenAI-style {"data": [{"id"}]}
// envelope, which the Anthropic models endpoint shares.
func listModels(ctx context.Context, client *http.Client, url string, header func(*http.Request)) ([]AvailableModel, error) {
	return fetchModels(ctx, client, url, header, false)
}

// fetchModels is listModels; priced also reads each entry's per-token
// pricing and context_length, in OpenRouter's shape, which other
// aggregators copy.
func fetchModels(ctx context.Context, client *http.Client, url string, header func(*http.Request), priced bool) ([]AvailableModel, error) {
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
		Data []modelEntry `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	if out.Data == nil {
		return nil, nil
	}
	models := make([]AvailableModel, len(out.Data))
	for i, e := range out.Data {
		models[i] = AvailableModel{ID: e.ID, DisplayName: e.DisplayName}
		if priced {
			e.applyPricing(&models[i])
		}
	}
	return models, nil
}

// modelEntry is one listing entry. Pricing and ContextLength stay raw
// so one malformed value nulls that field instead of failing the list.
type modelEntry struct {
	ID            string          `json:"id"`
	DisplayName   string          `json:"display_name"`
	ContextLength json.RawMessage `json:"context_length"`
	Pricing       struct {
		Prompt          json.RawMessage `json:"prompt"`
		Completion      json.RawMessage `json:"completion"`
		InputCacheRead  json.RawMessage `json:"input_cache_read"`
		InputCacheWrite json.RawMessage `json:"input_cache_write"`
	} `json:"pricing"`
}

func (e modelEntry) applyPricing(m *AvailableModel) {
	m.InputPerMTok = perMTok(e.Pricing.Prompt)
	m.OutputPerMTok = perMTok(e.Pricing.Completion)
	m.CacheReadPerMTok = perMTok(e.Pricing.InputCacheRead)
	m.CacheWritePerMTok = perMTok(e.Pricing.InputCacheWrite)
	var n float64
	if json.Unmarshal(e.ContextLength, &n) == nil && n >= 1 && n < math.MaxInt64 {
		v := int64(n)
		m.ContextLength = &v
	}
}

// perMTok converts a USD-per-token decimal string to per million
// tokens. Absent, non-string, unparseable, negative (OpenRouter's -1
// means "varies") or non-finite values are unknown: nil, never 0.
func perMTok(raw json.RawMessage) *float64 {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return nil
	}
	v *= 1e6
	return &v
}

// ListModels implements ModelLister via GET {base_url}/models. Each
// model also carries the prices and context length its entry states in
// OpenRouter's shape, on any host (D-173).
func (o *OpenAICompat) ListModels(ctx context.Context) ([]AvailableModel, error) {
	models, err := fetchModels(ctx, o.client, o.cfg.BaseURL+"/models", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
		for k, v := range o.cfg.Headers {
			req.Header.Set(k, v)
		}
	}, true)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: list models: %w", err)
	}
	return models, nil
}

// ListModels implements ModelLister via GET {base_url}/models with the
// chat call's auth and headers.
func (o *OpenAIResponses) ListModels(ctx context.Context) ([]AvailableModel, error) {
	models, err := listModels(ctx, o.client, o.cfg.BaseURL+"/models", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
		for k, v := range o.cfg.Headers {
			req.Header.Set(k, v)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("openai-responses: list models: %w", err)
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
