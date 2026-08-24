package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

// withCursorTestServer points cursorModelsURL at srv for the duration
// of one test, restoring it afterward.
func withCursorTestServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := cursorModelsURL
	cursorModelsURL = srv.URL
	t.Cleanup(func() { cursorModelsURL = orig })
}

func TestCursorFetchModelsDecodesItems(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"composer-2.5","displayName":"Composer 2.5"},{"id":"sonic"}]}`))
	}))
	t.Cleanup(srv.Close)
	withCursorTestServer(t, srv)

	models, err := cursorFetchModels(t.Context(), "test-key")
	if err != nil {
		t.Fatalf("cursorFetchModels: %v", err)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth header = %q, want Bearer test-key", gotAuth)
	}
	want := []provider.AvailableModel{
		{ID: "composer-2.5", DisplayName: "Composer 2.5"},
		{ID: "sonic"},
	}
	if len(models) != len(want) || models[0] != want[0] || models[1] != want[1] {
		t.Fatalf("models = %+v, want %+v", models, want)
	}
}

func TestCursorFetchModelsUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	withCursorTestServer(t, srv)

	if _, err := cursorFetchModels(t.Context(), "bad-key"); err == nil {
		t.Fatal("upstream 401 accepted")
	}
}

func TestCursorFetchModelsNeverLeaksKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	withCursorTestServer(t, srv)

	_, err := cursorFetchModels(t.Context(), "super-secret-key")
	if err == nil {
		t.Fatal("upstream 500 accepted")
	}
	if strings.Contains(err.Error(), "super-secret-key") {
		t.Fatalf("error leaked key material: %v", err)
	}
}

func TestCursorModelsCacheServesWithinTTL(t *testing.T) {
	orig := cursorCache
	t.Cleanup(func() { cursorCache = orig })
	cursorCache = &cursorModelsCache{entries: map[string]cursorModelsCacheEntry{}}
	cursorCache.set("p1", []provider.AvailableModel{{ID: "cached"}})

	got, ok := cursorCache.get("p1")
	if !ok || len(got) != 1 || got[0].ID != "cached" {
		t.Fatalf("cache get = %+v, ok=%v, want cached hit", got, ok)
	}
}

func TestCursorModelsCacheExpires(t *testing.T) {
	orig := cursorCache
	t.Cleanup(func() { cursorCache = orig })
	cursorCache = &cursorModelsCache{entries: map[string]cursorModelsCacheEntry{
		"p1": {models: []provider.AvailableModel{{ID: "stale"}}, fetchedAt: time.Now().Add(-cursorModelsCacheTTL - time.Second)},
	}}
	if _, ok := cursorCache.get("p1"); ok {
		t.Fatal("cache served an expired entry")
	}
}
