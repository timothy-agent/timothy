// Package catalog syncs a local cache of known models + pricing from
// LiteLLM's maintained JSON. Suggest-only (D-013-adjacent): it never
// writes into providers.models itself, only backs admin suggestions
// the operator reviews and applies through the existing provider
// update path.
package catalog

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SourceURL is LiteLLM's maintained model price/context-window table.
const SourceURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

const (
	fetchTimeout = 60 * time.Second
	maxBodyBytes = 32 << 20 // 32MB guard against a runaway response
)

// sampleKey is LiteLLM's documentation entry, not a real model —
// always skipped.
const sampleKey = "sample_spec"

// rawEntry mirrors the source fields this catalog cares about; all
// optional, per LiteLLM's schema. Costs are USD per single token.
// Token maxes are float64 because the source mixes ints and floats
// (e.g. 4096.0); sample_spec even uses strings, which is why entries
// decode individually and tolerantly in parse.
type rawEntry struct {
	LitellmProvider             string   `json:"litellm_provider"`
	Mode                        string   `json:"mode"`
	MaxInputTokens              *float64 `json:"max_input_tokens"`
	MaxOutputTokens             *float64 `json:"max_output_tokens"`
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
	SupportsVision              *bool    `json:"supports_vision"`
}

// Entry is one parsed, converted catalog row. Price fields are per
// MILLION tokens (Timothy's convention) — nil means unknown, never
// guessed (D-013).
type Entry struct {
	ModelKey          string
	LitellmProvider   string
	Mode              string
	MaxInputTokens    *int64
	MaxOutputTokens   *int64
	InputPerMTok      *float64
	OutputPerMTok     *float64
	CacheReadPerMTok  *float64
	CacheWritePerMTok *float64
	SupportsVision    *bool
}

// FetchResult is one sync attempt's outcome. NotModified is true on a
// 304 (etag unchanged) — Entries is nil and the caller keeps existing
// rows as-is.
type FetchResult struct {
	Entries     []Entry
	ETag        string
	NotModified bool
}

// Fetch retrieves and parses the catalog source, sending etag as
// If-None-Match when set. A 304 response short-circuits to
// NotModified with no entries.
func Fetch(url, etag string) (FetchResult, error) {
	if url == "" {
		url = SourceURL
	}
	client := &http.Client{Timeout: fetchTimeout}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return FetchResult{}, fmt.Errorf("catalog fetch: %w", err)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return FetchResult{}, fmt.Errorf("catalog fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return FetchResult{NotModified: true, ETag: etag}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return FetchResult{}, fmt.Errorf("catalog fetch: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return FetchResult{}, fmt.Errorf("catalog fetch: %w", err)
	}
	if len(body) > maxBodyBytes {
		return FetchResult{}, fmt.Errorf("catalog fetch: response exceeds %d bytes", maxBodyBytes)
	}

	entries, err := parse(body)
	if err != nil {
		return FetchResult{}, fmt.Errorf("catalog fetch: %w", err)
	}
	return FetchResult{Entries: entries, ETag: resp.Header.Get("ETag")}, nil
}

// parse decodes the source JSON (a map of model_key -> entry) into
// Entries. Each entry decodes individually and tolerantly: sample_spec
// (documentation, string-typed values), entries with no
// litellm_provider, and entries whose fields don't decode are all
// skipped rather than failing the whole sync — the upstream file mixes
// hand-maintained shapes.
func parse(body []byte) ([]Entry, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	entries := make([]Entry, 0, len(raw))
	for key, msg := range raw {
		if key == sampleKey {
			continue
		}
		var re rawEntry
		if err := json.Unmarshal(msg, &re); err != nil || re.LitellmProvider == "" {
			continue
		}
		entries = append(entries, Entry{
			ModelKey:          key,
			LitellmProvider:   re.LitellmProvider,
			Mode:              re.Mode,
			MaxInputTokens:    toInt64(re.MaxInputTokens),
			MaxOutputTokens:   toInt64(re.MaxOutputTokens),
			InputPerMTok:      perMTok(re.InputCostPerToken),
			OutputPerMTok:     perMTok(re.OutputCostPerToken),
			CacheReadPerMTok:  perMTok(re.CacheReadInputTokenCost),
			CacheWritePerMTok: perMTok(re.CacheCreationInputTokenCost),
			SupportsVision:    re.SupportsVision,
		})
	}
	return entries, nil
}

// toInt64 truncates a source token max (int or float upstream) to
// int64; nil stays nil.
func toInt64(v *float64) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

// perMTok converts a per-single-token USD cost to per-million-tokens
// (Timothy's stored convention, router.ModelPrices). nil stays nil —
// a missing source field is unknown cost, never defaulted to 0.
func perMTok(perToken *float64) *float64 {
	if perToken == nil {
		return nil
	}
	v := *perToken * 1e6
	return &v
}
