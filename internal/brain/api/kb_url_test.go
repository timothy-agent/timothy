package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTitleFromURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"path segment without extension", "https://example.com/docs/getting-started.html", "getting-started"},
		{"trailing slash", "https://example.com/blog/rag-deep-dive/", "rag-deep-dive"},
		{"root path falls back to host", "https://example.com/", "example.com"},
		{"empty path falls back to host", "https://example.com", "example.com"},
		{"pdf name", "https://example.com/papers/attention.pdf", "attention"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatalf("parse %s: %v", tc.url, err)
			}
			if got := titleFromURL(u); got != tc.want {
				t.Fatalf("titleFromURL(%s) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestConvertFetched(t *testing.T) {
	t.Parallel()
	markitdown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"markdown":"# Converted"}`))
	}))
	t.Cleanup(markitdown.Close)

	u, _ := url.Parse("https://example.com/page")
	tests := []struct {
		name          string
		markitdownURL string
		contentType   string
		body          string
		want          string
		wantErr       string
	}{
		{"markdown passthrough", "", "text/markdown; charset=utf-8", "# Raw", "# Raw", ""},
		{"plain text passthrough", "", "text/plain", "hello", "hello", ""},
		{"html converts via markitdown", markitdown.URL, "text/html; charset=utf-8", "<h1>x</h1>", "# Converted", ""},
		{"pdf converts via markitdown", markitdown.URL, "application/pdf", "%PDF-", "# Converted", ""},
		{"html without markitdown errors", "", "text/html", "<h1>x</h1>", "", "markitdown sidecar"},
		{"unsupported type", "", "image/png", "\x89PNG", "", "unsupported content type"},
		{"missing content type sniffs html", markitdown.URL, "", "<!DOCTYPE html><html><body>x</body></html>", "# Converted", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &kbAPI{markitdownURL: tc.markitdownURL, markitdownHTTP: &http.Client{}}
			got, err := h.convertFetched(context.Background(), u, []byte(tc.body), tc.contentType)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("convertFetched: %v", err)
			}
			if got != tc.want {
				t.Fatalf("markdown = %q, want %q", got, tc.want)
			}
		})
	}
}
