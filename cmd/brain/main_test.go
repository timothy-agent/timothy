package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// TestIntersectReadOnlyConnectorToolsMatchesAllowlist confirms the
// resolver's matching step: only tools whose name matches an allow
// entry (direct or connector-namespaced suffix, D-036) come through,
// and an empty allowlist (agent opted into nothing) yields none.
func TestIntersectReadOnlyConnectorToolsMatchesAllowlist(t *testing.T) {
	available := []*tools.Tool{
		{Name: "gmail_gmail_search", ReadOnly: true},
		{Name: "google-calendar_list_calendar_events", ReadOnly: true},
		{Name: "gmail_gmail_send"}, // not read-only; ReadOnlyTools would never hand this in, but the matcher must not care
	}

	got := intersectReadOnlyConnectorTools([]string{"gmail_search"}, available)
	var names []string
	for _, tl := range got {
		names = append(names, tl.Name)
	}
	if !slices.Equal(names, []string{"gmail_gmail_search"}) {
		t.Fatalf("intersect = %v, want [gmail_gmail_search]", names)
	}

	if got := intersectReadOnlyConnectorTools(nil, available); got != nil {
		t.Fatalf("empty allowlist = %v, want nil", got)
	}

	got = intersectReadOnlyConnectorTools([]string{"list_calendar_events", "gmail_search"}, available)
	names = nil
	for _, tl := range got {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	want := []string{"gmail_gmail_search", "google-calendar_list_calendar_events"}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("intersect = %v, want %v", names, want)
	}
}

// TestAdminProxyRewritesUsagePathFromWildcard pins D-116: the upstream
// path for the usage sub-tree is built from the matched {rest...}
// wildcard, never from a trim of the raw inbound path. ServeMux decodes
// percent-encoded segments into the wildcard, so "..%2f" reaches the
// rewrite as a literal "..", and path.Join collapses it inside
// /internal/admin/usage/ rather than letting it climb out of the
// prefix. Routing goes through a real ServeMux with the production
// pattern so PathValue is populated exactly as in the running server.
func TestAdminProxyRewritesUsagePathFromWildcard(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "plain sub-path", path: "/v1/admin/usage/summary", want: "/internal/admin/usage/summary"},
		{name: "nested sub-path", path: "/v1/admin/usage/series/daily", want: "/internal/admin/usage/series/daily"},
		{name: "encoded traversal stays inside the prefix", path: "/v1/admin/usage/..%2f..%2fproviders", want: "/internal/admin/usage/providers"},
		{name: "encoded traversal with a tail", path: "/v1/admin/usage/..%2fsecrets%2fx", want: "/internal/admin/usage/secrets/x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Path
			}))
			defer upstream.Close()

			proxy := adminProxy(upstream.URL, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if proxy == nil {
				t.Fatal("adminProxy returned nil")
			}
			mux := http.NewServeMux()
			mux.Handle("GET /v1/admin/usage/{rest...}", proxy)
			mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, tc.path, nil))

			if got != tc.want {
				t.Fatalf("upstream path = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "..") {
				t.Fatalf("upstream path %q still carries a traversal segment", got)
			}
			if !strings.HasPrefix(got, "/internal/admin/usage") {
				t.Fatalf("upstream path %q escaped the usage prefix", got)
			}
		})
	}
}

// TestAdminProxyRewritesNonWildcardRoutes confirms the wildcard branch
// left the other admin patterns alone: each is literal (or {id}-shaped)
// and still maps /v1/admin/... onto /internal/admin/... byte-for-byte.
func TestAdminProxyRewritesNonWildcardRoutes(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    string
	}{
		{pattern: "GET /v1/admin/providers", path: "/v1/admin/providers", want: "/internal/admin/providers"},
		{pattern: "GET /v1/admin/routes", path: "/v1/admin/routes", want: "/internal/admin/routes"},
		{pattern: "GET /v1/admin/secrets/{ref_name}", path: "/v1/admin/secrets/OPENAI_KEY", want: "/internal/admin/secrets/OPENAI_KEY"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			var got string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Path
			}))
			defer upstream.Close()

			proxy := adminProxy(upstream.URL, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			mux := http.NewServeMux()
			mux.Handle(tc.pattern, proxy)
			mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, tc.path, nil))

			if got != tc.want {
				t.Fatalf("upstream path = %q, want %q", got, tc.want)
			}
		})
	}
}
