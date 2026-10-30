package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearchParsesResults(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("format = %q, want json", r.URL.Query().Get("format"))
		}
		if r.URL.Query().Get("q") != "golang" {
			t.Errorf("q = %q, want golang", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"The Go Programming Language","url":"https://go.dev/","content":"Go is an open source language."},
			{"title":"Go Playground","url":"https://go.dev/play/","content":"Run Go code online."}
		]}`))
	}))
	defer srv.Close()

	tool := WebSearch(srv.URL)
	args, _ := json.Marshal(map[string]string{"query": "golang"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"The Go Programming Language", "https://go.dev/", "Go Playground"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWebSearchEmptyResults(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	tool := WebSearch(srv.URL)
	args, _ := json.Marshal(map[string]string{"query": "asdkjaslkdjaslkd"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "no results found" {
		t.Fatalf("out = %q, want %q", out, "no results found")
	}
}

func TestWebSearchRejectsEmptyQuery(t *testing.T) {
	t.Parallel()
	tool := WebSearch("http://unused")
	args, _ := json.Marshal(map[string]string{"query": "   "})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected an error for a blank query")
	}
}

func TestWebSearchSurfacesBackendErrors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tool := WebSearch(srv.URL)
	args, _ := json.Marshal(map[string]string{"query": "golang"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want it to mention http 503", err)
	}
}

func TestWebSearchCapsResultCount(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString(`{"results":[`)
		for i := 0; i < 25; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"title":"r","url":"https://example.com","content":"c"}`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	}))
	defer srv.Close()

	tool := WebSearch(srv.URL)
	args, _ := json.Marshal(map[string]string{"query": "x"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Count(out, "https://example.com") != webSearchDefaultResult {
		t.Fatalf("result count = %d, want %d", strings.Count(out, "https://example.com"), webSearchDefaultResult)
	}
}

func TestWebSearchCountControlsRenderedResults(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString(`{"results":[`)
		for i := 0; i < 25; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"title":"r","url":"https://example.com","content":"c"}`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	}))
	defer srv.Close()

	tool := WebSearch(srv.URL)
	args, _ := json.Marshal(map[string]any{"query": "x", "count": 3})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Count(out, "https://example.com"); got != 3 {
		t.Fatalf("result count = %d, want 3", got)
	}
}

func TestWebSearchCountValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		count   int
		wantErr bool
	}{
		{"min valid", 1, false},
		{"max valid", 20, false},
		{"zero rejected", 0, true},
		{"negative rejected", -1, true},
		{"over max rejected", 21, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"results":[{"title":"r","url":"https://example.com","content":"c"}]}`))
			}))
			defer srv.Close()

			tool := WebSearch(srv.URL)
			args, _ := json.Marshal(map[string]any{"query": "x", "count": tt.count})
			_, err := tool.Execute(context.Background(), args)
			if tt.wantErr && err == nil {
				t.Fatal("expected an error for out-of-range count")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Execute: %v", err)
			}
		})
	}
}

func TestWebSearchTimeRangeAndCategoryParams(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("time_range"); got != "week" {
			t.Errorf("time_range = %q, want week", got)
		}
		if got := r.URL.Query().Get("categories"); got != "news" {
			t.Errorf("categories = %q, want news", got)
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	tool := WebSearch(srv.URL)
	args, _ := json.Marshal(map[string]string{"query": "x", "time_range": "week", "category": "news"})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestWebSearchOmitsOptionalParamsWhenAbsent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("time_range") {
			t.Errorf("time_range should be absent, got %q", r.URL.Query().Get("time_range"))
		}
		if r.URL.Query().Has("categories") {
			t.Errorf("categories should be absent, got %q", r.URL.Query().Get("categories"))
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	tool := WebSearch(srv.URL)
	args, _ := json.Marshal(map[string]string{"query": "x"})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestWebSearchRejectsInvalidTimeRange(t *testing.T) {
	t.Parallel()
	tool := WebSearch("http://unused")
	args, _ := json.Marshal(map[string]string{"query": "x", "time_range": "decade"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected an error for an invalid time_range")
	}
}

func TestWebSearchRejectsInvalidCategory(t *testing.T) {
	t.Parallel()
	tool := WebSearch("http://unused")
	args, _ := json.Marshal(map[string]string{"query": "x", "category": "sports"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected an error for an invalid category")
	}
}

func TestWebSearchTavilyParsesResults(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tvly-test-key" {
			t.Errorf("Authorization = %q, want Bearer tvly-test-key", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		var req tavilyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Query != "golang news" {
			t.Errorf("query = %q, want golang news", req.Query)
		}
		if req.MaxResults != 5 {
			t.Errorf("max_results = %d, want 5", req.MaxResults)
		}
		if req.Topic != "news" {
			t.Errorf("topic = %q, want news", req.Topic)
		}
		if req.TimeRange != "week" {
			t.Errorf("time_range = %q, want week", req.TimeRange)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Go 1.26 Released","url":"https://go.dev/blog/go1.26","content":"Go 1.26 is out."}
		]}`))
	}))
	defer srv.Close()

	tool := WebSearchWithConfig(WebSearchConfig{
		TavilyURL:    srv.URL,
		TavilyAPIKey: func(context.Context) string { return "tvly-test-key" },
		Provider:     func(context.Context) string { return "tavily" },
	})
	args, _ := json.Marshal(map[string]any{"query": "golang news", "count": 5, "category": "news", "time_range": "week"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"1. Go 1.26 Released", "https://go.dev/blog/go1.26", "Go 1.26 is out."} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWebSearchAutoRoutingPreference(t *testing.T) {
	t.Parallel()

	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"From Tavily","url":"https://tavily.com","content":"Tavily content"}]}`))
	}))
	defer tavilySrv.Close()

	searxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"From SearXNG","url":"https://searxng.org","content":"SearXNG content"}]}`))
	}))
	defer searxSrv.Close()

	// 1. Auto mode with Tavily key present -> uses Tavily.
	tool := WebSearchWithConfig(WebSearchConfig{
		SearxngURL:   searxSrv.URL,
		TavilyURL:    tavilySrv.URL,
		TavilyAPIKey: func(context.Context) string { return "tvly-key" },
	})
	args, _ := json.Marshal(map[string]string{"query": "test"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute with tavily key: %v", err)
	}
	if !strings.Contains(out, "From Tavily") {
		t.Fatalf("expected Tavily result in auto mode when key set, got:\n%s", out)
	}

	// 2. Auto mode with empty Tavily key -> falls back to SearXNG.
	toolFallback := WebSearchWithConfig(WebSearchConfig{
		SearxngURL:   searxSrv.URL,
		TavilyURL:    tavilySrv.URL,
		TavilyAPIKey: func(context.Context) string { return "" },
	})
	outFallback, err := toolFallback.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute fallback: %v", err)
	}
	if !strings.Contains(outFallback, "From SearXNG") {
		t.Fatalf("expected SearXNG result when tavily key empty, got:\n%s", outFallback)
	}

	// 3. Explicit Provider "searxng" uses SearXNG even with Tavily key set.
	toolExplicitSearx := WebSearchWithConfig(WebSearchConfig{
		SearxngURL:   searxSrv.URL,
		TavilyURL:    tavilySrv.URL,
		TavilyAPIKey: func(context.Context) string { return "tvly-key" },
		Provider:     func(context.Context) string { return "searxng" },
	})
	outExplicit, err := toolExplicitSearx.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute explicit searxng: %v", err)
	}
	if !strings.Contains(outExplicit, "From SearXNG") {
		t.Fatalf("expected SearXNG result when provider=searxng, got:\n%s", outExplicit)
	}

	// 4. Explicit Provider "tavily" fails when key is empty.
	toolTavilyNoKey := WebSearchWithConfig(WebSearchConfig{
		TavilyURL:    tavilySrv.URL,
		TavilyAPIKey: func(context.Context) string { return "" },
		Provider:     func(context.Context) string { return "tavily" },
	})
	_, err = toolTavilyNoKey.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "TAVILY_API_KEY is not configured") {
		t.Fatalf("err = %v, want TAVILY_API_KEY is not configured error", err)
	}

	// 5. Neither configured in auto mode fails.
	toolUnconfigured := WebSearchWithConfig(WebSearchConfig{})
	_, err = toolUnconfigured.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "web search is not configured") {
		t.Fatalf("err = %v, want web search is not configured error", err)
	}
}
