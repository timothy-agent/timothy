package catalog

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestSyncReplacesEntries(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{
			"sample_spec": {"litellm_provider": "sample"},
			"anthropic/claude-a": {"litellm_provider": "anthropic", "mode": "chat", "input_cost_per_token": 0.000001}
		}`))
	}))
	defer srv.Close()
	s.url = srv.URL

	st, err := s.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if st.EntryCount != 1 {
		t.Fatalf("EntryCount = %d, want 1", st.EntryCount)
	}
	if st.Error != "" {
		t.Fatalf("Error = %q, want empty", st.Error)
	}
	models, err := s.Search(ctx, "", "", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(models) != 1 || models[0].ModelKey != "anthropic/claude-a" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].InputPerMTok == nil || *models[0].InputPerMTok != 1 {
		t.Fatalf("InputPerMTok = %v, want 1", models[0].InputPerMTok)
	}

	// A second sync with a different key set replaces the cache wholesale:
	// claude-a is gone, claude-b is the only entry.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v2"`)
		_, _ = w.Write([]byte(`{
			"anthropic/claude-b": {"litellm_provider": "anthropic", "mode": "chat"}
		}`))
	}))
	defer srv2.Close()
	s.url = srv2.URL

	if _, err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync (2): %v", err)
	}
	models, err = s.Search(ctx, "", "", 0)
	if err != nil {
		t.Fatalf("Search (2): %v", err)
	}
	if len(models) != 1 || models[0].ModelKey != "anthropic/claude-b" {
		t.Fatalf("models after replace = %+v, want only claude-b", models)
	}
}

func TestSyncNotModifiedIsNoOp(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == `"stable"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"stable"`)
		_, _ = w.Write([]byte(`{"anthropic/claude-a": {"litellm_provider": "anthropic"}}`))
	}))
	defer srv.Close()
	s.url = srv.URL

	first, err := s.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	second, err := s.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync (304): %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 requests made", hits)
	}
	if second.EntryCount != first.EntryCount {
		t.Fatalf("304 sync changed entry_count: %d -> %d", first.EntryCount, second.EntryCount)
	}
	if second.FetchedAt == nil || !second.FetchedAt.After(*first.FetchedAt) {
		t.Fatalf("304 sync should still bump fetched_at: first=%v second=%v", first.FetchedAt, second.FetchedAt)
	}
}

func TestSyncFailureKeepsOldEntriesAndSetsError(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"anthropic/claude-a": {"litellm_provider": "anthropic"}}`))
	}))
	s.url = srv.URL
	if _, err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	srv.Close() // now unreachable: next sync must fail without touching entries

	if _, err := s.Sync(ctx); err == nil {
		t.Fatal("expected an error syncing against a closed server")
	}
	models, err := s.Search(ctx, "", "", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %+v, want the old entry preserved", models)
	}
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Error == "" {
		t.Fatal("expected sync status to record the error")
	}
	if st.EntryCount != 1 {
		t.Fatalf("EntryCount = %d, want the old count preserved", st.EntryCount)
	}
}

func TestSearchFiltersAndLimits(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"anthropic/claude-3-5-sonnet": {"litellm_provider": "anthropic", "mode": "chat"},
			"anthropic/claude-3-opus": {"litellm_provider": "anthropic", "mode": "chat"},
			"openai/gpt-4o": {"litellm_provider": "openai", "mode": "chat"}
		}`))
	}))
	defer srv.Close()
	s.url = srv.URL
	if _, err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Case-insensitive substring on model_key.
	models, err := s.Search(ctx, "SONNET", "", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(models) != 1 || models[0].ModelKey != "anthropic/claude-3-5-sonnet" {
		t.Fatalf("models = %+v, want only the sonnet match", models)
	}

	// Provider filter.
	models, err = s.Search(ctx, "", "openai", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(models) != 1 || models[0].ModelKey != "openai/gpt-4o" {
		t.Fatalf("models = %+v, want only the openai entry", models)
	}

	// Limit.
	models, err = s.Search(ctx, "", "", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1 (limit)", len(models))
	}
}

func TestSearchOrdersPrefixBeforeSubstringThenAlphabetical(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	// zzz-gpt-4o has "gpt" only mid-key (substring); the other two
	// start with "gpt" (prefix). Prefix matches must come first, and
	// within each group results are alphabetical by model_key —
	// regardless of the source map's iteration order.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"openai/zzz-gpt-4o": {"litellm_provider": "openai", "mode": "chat"},
			"openai/gpt-4o-mini": {"litellm_provider": "openai", "mode": "chat"},
			"openai/gpt-4o": {"litellm_provider": "openai", "mode": "chat"}
		}`))
	}))
	defer srv.Close()
	s.url = srv.URL
	if _, err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	models, err := s.Search(ctx, "gpt", "", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := []string{"openai/gpt-4o", "openai/gpt-4o-mini", "openai/zzz-gpt-4o"}
	if len(models) != len(want) {
		t.Fatalf("models = %+v, want %d entries", models, len(want))
	}
	for i, k := range want {
		if models[i].ModelKey != k {
			t.Fatalf("models[%d] = %q, want %q (order: %+v)", i, models[i].ModelKey, k, models)
		}
	}
}

func TestSearchEmptyQueryOrdersAlphabetical(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"openai/gpt-5.6-luna": {"litellm_provider": "openai", "mode": "chat"},
			"openai/gpt-4o": {"litellm_provider": "openai", "mode": "chat"},
			"anthropic/claude-fable-5": {"litellm_provider": "anthropic", "mode": "chat"}
		}`))
	}))
	defer srv.Close()
	s.url = srv.URL
	if _, err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	models, err := s.Search(ctx, "", "", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := []string{"anthropic/claude-fable-5", "openai/gpt-4o", "openai/gpt-5.6-luna"}
	if len(models) != len(want) {
		t.Fatalf("models = %+v, want %d entries", models, len(want))
	}
	for i, k := range want {
		if models[i].ModelKey != k {
			t.Fatalf("models[%d] = %q, want %q (order: %+v)", i, models[i].ModelKey, k, models)
		}
	}
}

func TestSearchProvidersFiltersBySet(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"anthropic/claude-3-5-sonnet": {"litellm_provider": "anthropic", "mode": "chat"},
			"bedrock/claude-3-opus": {"litellm_provider": "bedrock", "mode": "chat"},
			"openai/gpt-4o": {"litellm_provider": "openai", "mode": "chat"}
		}`))
	}))
	defer srv.Close()
	s.url = srv.URL
	if _, err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Multi-provider set: only rows matching one of the set pass.
	models, err := s.SearchProviders(ctx, "", []string{"anthropic", "bedrock"}, 0)
	if err != nil {
		t.Fatalf("SearchProviders: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want the anthropic and bedrock rows only", models)
	}

	// Empty/nil set means no restriction.
	models, err = s.SearchProviders(ctx, "", nil, 0)
	if err != nil {
		t.Fatalf("SearchProviders: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("models = %+v, want all 3 rows with no provider restriction", models)
	}

	// q still filters on top of the provider set.
	models, err = s.SearchProviders(ctx, "opus", []string{"anthropic", "bedrock"}, 0)
	if err != nil {
		t.Fatalf("SearchProviders: %v", err)
	}
	if len(models) != 1 || models[0].ModelKey != "bedrock/claude-3-opus" {
		t.Fatalf("models = %+v, want only the opus match", models)
	}
}

func TestAllModelsReturnsWholeCatalog(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"anthropic/claude-3-5-sonnet": {"litellm_provider": "anthropic", "mode": "chat"},
			"openai/gpt-4o": {"litellm_provider": "openai", "mode": "chat"}
		}`))
	}))
	defer srv.Close()
	s.url = srv.URL
	if _, err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	models, err := s.AllModels(ctx)
	if err != nil {
		t.Fatalf("AllModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want both catalog rows", models)
	}
}

func TestSuggestMatchesThroughStore(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"anthropic/claude-3-5-sonnet-20241022": {"litellm_provider": "anthropic", "mode": "chat",
				"max_input_tokens": 200000, "input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015}
		}`))
	}))
	defer srv.Close()
	s.url = srv.URL
	if _, err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	sugs, err := s.Suggest(ctx, []string{"anthropic"}, []string{"claude-3-5-sonnet-20241022", "unknown-model"})
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(sugs) != 2 {
		t.Fatalf("suggestions len = %d, want 2", len(sugs))
	}
	matched := sugs[0]
	if matched.Match != "anthropic/claude-3-5-sonnet-20241022" {
		t.Fatalf("Match = %q, want the catalog key", matched.Match)
	}
	if matched.MaxInputTokens == nil || *matched.MaxInputTokens != 200000 {
		t.Fatalf("MaxInputTokens = %v, want 200000", matched.MaxInputTokens)
	}
	if matched.InputPerMTok == nil || *matched.InputPerMTok != 3 {
		t.Fatalf("InputPerMTok = %v, want 3", matched.InputPerMTok)
	}
	if sugs[1].Match != "" {
		t.Fatalf("unmatched model got a match: %q", sugs[1].Match)
	}
}

func TestStatusNeverSynced(t *testing.T) {
	s := testStore(t)
	st, err := s.Status(t.Context())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.FetchedAt != nil {
		t.Fatalf("FetchedAt = %v, want nil (never synced)", st.FetchedAt)
	}
	if st.EntryCount != 0 {
		t.Fatalf("EntryCount = %d, want 0", st.EntryCount)
	}
}
