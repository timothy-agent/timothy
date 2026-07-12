package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	webSearchTimeout   = 15 * time.Second
	webSearchMaxBody   = 2 << 20 // bytes read off the wire
	webSearchMaxResult = 10
)

type webSearchArgs struct {
	Query string `json:"query"`
}

// searxResult is the subset of SearXNG's JSON response this tool uses.
type searxResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

// WebSearch queries the self-hosted SearXNG instance and returns the
// top results as title/URL/snippet. baseURL is the compose-internal
// address (e.g. http://searxng:8080) — never exposed to the model or
// the caller, so there is nothing here for a prompt to redirect.
func WebSearch(baseURL string) *tools.Tool {
	client := &http.Client{Timeout: webSearchTimeout}
	endpoint := strings.TrimRight(baseURL, "/") + "/search"

	return &tools.Tool{
		Name: "web_search",
		Description: `Searches the web and returns a list of matching pages.

Use when the user needs current information you don't have — news,
prices, recent releases, anything past your knowledge — or asks you to
"search the internet". Returns titles, URLs, and short snippets, not
full page text; call web_fetch on a promising URL to read further.

This is read-only lookup, never a transaction: it cannot book a
flight, reserve a hotel, purchase anything, or fill in a form. If the
user asks you to "book" or "buy" something, use this tool (and
web_fetch) to find options and prices, then tell the user what you
found and that they need to complete the booking themselves — do not
retry the search hoping for a different kind of result.

Arguments:
- query (string, required): the search query.

Example: {"query": "Amazon Bedrock Nova Lite pricing"} → a numbered
list of results with title, URL, and snippet.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "The search query"
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
			return runSearch(ctx, client, endpoint, args.Query)
		},
	}
}

func runSearch(ctx context.Context, client *http.Client, endpoint, query string) (string, error) {
	q := url.Values{"q": {query}, "format": {"json"}}
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
	if len(parsed.Results) == 0 {
		return "no results found", nil
	}

	var b strings.Builder
	n := len(parsed.Results)
	if n > webSearchMaxResult {
		n = webSearchMaxResult
	}
	for i := 0; i < n; i++ {
		r := parsed.Results[i]
		fmt.Fprintf(&b, "%d. %s\n%s\n%s\n\n", i+1, r.Title, r.URL, r.Content)
	}
	return strings.TrimSpace(b.String()), nil
}
