package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	webSearchTimeout       = 15 * time.Second
	webSearchMaxBody       = 2 << 20 // bytes read off the wire
	webSearchDefaultResult = 10
	webSearchMaxResult     = 20
	defaultTavilyEndpoint  = "https://api.tavily.com/search"
)

var (
	webSearchTimeRanges = map[string]bool{"day": true, "week": true, "month": true, "year": true}
	webSearchCategories = map[string]bool{"general": true, "news": true, "science": true, "it": true}
)

type webSearchArgs struct {
	Query     string `json:"query"`
	TimeRange string `json:"time_range"`
	Category  string `json:"category"`
	Count     *int   `json:"count"`
}

// searchResponse is the subset of SearXNG and Tavily JSON responses this tool uses.
type searchResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

type searxResponse = searchResponse
type tavilyResponse = searchResponse

type tavilyRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
	Topic      string `json:"topic,omitempty"`
	TimeRange  string `json:"time_range,omitempty"`
}

// WebSearchConfig configures web search backends.
type WebSearchConfig struct {
	SearxngURL   string
	TavilyURL    string // defaults to "https://api.tavily.com/search" when empty
	TavilyAPIKey func(ctx context.Context) string
	Provider     func(ctx context.Context) string // "", "tavily", or "searxng"
}

// WebSearch queries the self-hosted SearXNG instance and returns the
// top results as title/URL/snippet. baseURL is the compose-internal
// address (e.g. http://searxng:8080) — never exposed to the model or
// the caller, so there is nothing here for a prompt to redirect.
func WebSearch(baseURL string) *tools.Tool {
	return WebSearchWithConfig(WebSearchConfig{SearxngURL: baseURL})
}

// WebSearchWithConfig returns a search_web tool supporting SearXNG and Tavily.
func WebSearchWithConfig(cfg WebSearchConfig) *tools.Tool {
	client := &http.Client{Timeout: webSearchTimeout}
	tavilyEndpoint := cfg.TavilyURL
	if tavilyEndpoint == "" {
		tavilyEndpoint = defaultTavilyEndpoint
	}
	searxEndpoint := ""
	if cfg.SearxngURL != "" {
		searxEndpoint = strings.TrimRight(cfg.SearxngURL, "/") + "/search"
	}

	return &tools.Tool{
		Name: "search_web",
		Description: `Searches the web and returns a list of matching pages.

Use when the user needs current information you don't have — news,
prices, recent releases, anything past your knowledge — or asks you to
"search the internet". Returns titles, URLs, and short snippets, not
full page text; call fetch_url on a promising URL to read further.

This is read-only lookup, never a transaction: it cannot book a
flight, reserve a hotel, purchase anything, or fill in a form. If the
user asks you to "book" or "buy" something, use this tool (and
fetch_url) to find options and prices, then tell the user what you
found and that they need to complete the booking themselves — do not
retry the search hoping for a different kind of result.

Do not use this to read a URL you already have; use fetch_url for
that. Search finds pages, fetch_url reads one.

Arguments:
- query (string, required): the search query.
- time_range (string, optional): "day", "week", "month", or "year" —
  restricts results to that recency. Example: {"query": "AI news",
  "time_range": "week"}.
- category (string, optional): "general" (default), "news",
  "science", or "it" — narrows results to that domain. Example:
  {"query": "quantum computing breakthrough", "category": "science"}.
- count (integer, optional): how many results to return, 1-20,
  default 10. Example: {"query": "golang 1.26 release notes",
  "count": 5}.

Example: {"query": "Amazon Bedrock Nova Lite pricing"} → a numbered
list of results with title, URL, and snippet.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "The search query"
				},
				"time_range": {
					"type": "string",
					"enum": ["day", "week", "month", "year"],
					"description": "Restrict results to this recency window."
				},
				"category": {
					"type": "string",
					"enum": ["general", "news", "science", "it"],
					"description": "Narrow results to this domain; defaults to general."
				},
				"count": {
					"type": "integer",
					"minimum": 1,
					"maximum": 20,
					"description": "Number of results to return; defaults to 10."
				}
			},
			"required": ["query"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args webSearchArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.Query) == "" {
				return "", fmt.Errorf("query must not be empty")
			}
			if args.TimeRange != "" && !webSearchTimeRanges[args.TimeRange] {
				return "", fmt.Errorf("time_range must be one of day, week, month, year, got %q", args.TimeRange)
			}
			if args.Category != "" && !webSearchCategories[args.Category] {
				return "", fmt.Errorf("category must be one of general, news, science, it, got %q", args.Category)
			}
			count := webSearchDefaultResult
			if args.Count != nil {
				count = *args.Count
				if count < 1 || count > webSearchMaxResult {
					return "", fmt.Errorf("count must be between 1 and %d, got %d", webSearchMaxResult, count)
				}
			}

			provider := ""
			if cfg.Provider != nil {
				provider = strings.ToLower(strings.TrimSpace(cfg.Provider(ctx)))
			}
			tavilyKey := ""
			if cfg.TavilyAPIKey != nil {
				tavilyKey = strings.TrimSpace(cfg.TavilyAPIKey(ctx))
			}

			switch provider {
			case "tavily":
				if tavilyKey == "" {
					return "", errors.New("tavily web search selected, but TAVILY_API_KEY is not configured")
				}
				return runTavilySearch(ctx, client, tavilyEndpoint, tavilyKey, args.Query, args.TimeRange, args.Category, count)
			case "searxng":
				if cfg.SearxngURL == "" {
					return "", errors.New("searxng web search selected, but SEARXNG_URL is not configured")
				}
				return runSearch(ctx, client, searxEndpoint, args.Query, args.TimeRange, args.Category, count)
			case "":
				if tavilyKey != "" {
					return runTavilySearch(ctx, client, tavilyEndpoint, tavilyKey, args.Query, args.TimeRange, args.Category, count)
				}
				if cfg.SearxngURL != "" {
					return runSearch(ctx, client, searxEndpoint, args.Query, args.TimeRange, args.Category, count)
				}
				return "", errors.New("web search is not configured: set TAVILY_API_KEY or SEARXNG_URL")
			default:
				return "", fmt.Errorf("unknown web search provider %q", provider)
			}
		},
	}
}

func runTavilySearch(ctx context.Context, client *http.Client, endpoint, apiKey, query, timeRange, category string, count int) (string, error) {
	topic := "general"
	if category == "news" {
		topic = "news"
	}
	reqBody := tavilyRequest{
		Query:      query,
		MaxResults: count,
		Topic:      topic,
		TimeRange:  timeRange,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("build tavily request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("search backend returned http %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, webSearchMaxBody))
	if err != nil {
		return "", fmt.Errorf("read search response: %w", err)
	}

	var parsed tavilyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse search response: %w", err)
	}
	return formatSearchResults(parsed.Results, count), nil
}

func runSearch(ctx context.Context, client *http.Client, endpoint, query, timeRange, category string, count int) (string, error) {
	q := url.Values{"q": {query}, "format": {"json"}}
	if timeRange != "" {
		q.Set("time_range", timeRange)
	}
	if category != "" {
		q.Set("categories", category)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("search backend returned http %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, webSearchMaxBody))
	if err != nil {
		return "", fmt.Errorf("read search response: %w", err)
	}

	var parsed searxResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse search response: %w", err)
	}
	return formatSearchResults(parsed.Results, count), nil
}

func formatSearchResults(results []struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}, count int) string {
	if len(results) == 0 {
		return "no results found"
	}

	var b strings.Builder
	n := len(results)
	if n > count {
		n = count
	}
	for i := 0; i < n; i++ {
		r := results[i]
		fmt.Fprintf(&b, "%d. %s\n%s\n%s\n\n", i+1, r.Title, r.URL, r.Content)
	}
	return strings.TrimSpace(b.String())
}
