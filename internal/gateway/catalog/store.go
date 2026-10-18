package catalog

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store holds the model catalog in memory: it's a replaceable cache of
// external data (LiteLLM's synced JSON), not state worth persisting.
type Store struct {
	url string // override for tests; "" uses SourceURL
	log *slog.Logger

	mu         sync.RWMutex
	entries    []Entry
	etag       string
	fetchedAt  *time.Time
	entryCount int
	lastError  string
}

func New(log *slog.Logger) *Store {
	return &Store{log: log}
}

// NewWithURL is New with the source URL overridden — for tests in other
// packages (e.g. admin) that need Sync to hit an httptest source server
// instead of SourceURL.
func NewWithURL(log *slog.Logger, url string) *Store {
	return &Store{log: log, url: url}
}

// SyncStatus is one sync attempt's outcome.
type SyncStatus struct {
	FetchedAt  *time.Time `json:"fetched_at"`
	EntryCount int        `json:"entry_count"`
	Error      string     `json:"error"`
}

// Status returns the last sync attempt's outcome.
func (s *Store) Status(ctx context.Context) (SyncStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SyncStatus{FetchedAt: s.fetchedAt, EntryCount: s.entryCount, Error: s.lastError}, nil
}

// Sync fetches the source against the stored etag: on 304 it updates
// fetchedAt only (existing entries stay untouched); on success it
// swaps in the fetched entries+etag+count and clears lastError; on
// failure it records lastError and keeps the old entries in place.
func (s *Store) Sync(ctx context.Context) (SyncStatus, error) {
	s.mu.RLock()
	etag := s.etag
	s.mu.RUnlock()

	res, err := Fetch(s.url, etag)
	if err != nil {
		s.mu.Lock()
		s.lastError = err.Error()
		st := SyncStatus{FetchedAt: s.fetchedAt, EntryCount: s.entryCount, Error: s.lastError}
		s.mu.Unlock()
		return st, err
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetchedAt = &now
	if !res.NotModified {
		// Sorted once here so every query sees a stable base order —
		// the source is a Go map (parse's range), randomized per sync
		// otherwise.
		sort.Slice(res.Entries, func(i, j int) bool {
			return res.Entries[i].ModelKey < res.Entries[j].ModelKey
		})
		s.entries = res.Entries
		s.etag = res.ETag
		s.entryCount = len(res.Entries)
	}
	s.lastError = ""
	return SyncStatus{FetchedAt: s.fetchedAt, EntryCount: s.entryCount}, nil
}

// Model is one catalog entry, wire shape for admin queries. ID is the
// id a provider's API actually accepts: model_key with the entry's own
// litellm_provider prefix stripped (StripOwnPrefix) — LiteLLM
// namespaces model_key by provider, but that namespace isn't part of
// the id the provider itself understands. model_key stays alongside
// for reference/debug.
type Model struct {
	ID                string   `json:"id"`
	ModelKey          string   `json:"model_key"`
	LitellmProvider   string   `json:"litellm_provider"`
	Mode              string   `json:"mode"`
	MaxInputTokens    *int64   `json:"max_input_tokens,omitempty"`
	MaxOutputTokens   *int64   `json:"max_output_tokens,omitempty"`
	InputPerMTok      *float64 `json:"input_per_mtok,omitempty"`
	OutputPerMTok     *float64 `json:"output_per_mtok,omitempty"`
	CacheReadPerMTok  *float64 `json:"cache_read_per_mtok,omitempty"`
	CacheWritePerMTok *float64 `json:"cache_write_per_mtok,omitempty"`
	SupportsVision    *bool    `json:"supports_vision,omitempty"`
}

const defaultSearchLimit = 50
const maxSearchLimit = 200

// Search returns catalog entries matching q (case-insensitive substring
// on model_key) and litellmProvider (exact, empty means any). Results
// rank model_key-prefix matches before other substring matches, each
// group alphabetical by model_key (rankedMatches). limit defaults to
// 50, capped at 200.
func (s *Store) Search(ctx context.Context, q, litellmProvider string, limit int) ([]Model, error) {
	limit = clampSearchLimit(limit, false)
	qLower := strings.ToLower(q)

	s.mu.RLock()
	defer s.mu.RUnlock()
	return rankedMatches(s.entries, qLower, limit, false, func(e Entry) bool {
		return litellmProvider == "" || e.LitellmProvider == litellmProvider
	}), nil
}

// SearchProviders is Search's multi-provider sibling: q filters
// case-insensitive substring on model_key as usual, but the provider
// restriction is a set rather than a
// single exact value — the admin catalog-models endpoint's candidate
// litellm_provider(s) from a driver/base_url can be more than one
// (e.g. bedrock's "bedrock"/"bedrock_converse"). An empty set returns
// an empty list, never the whole catalog (D-171). limit < 0 returns
// every match (the provider listing's context-window source). An empty
// query lists chat-like modes before the rest, still alphabetical
// within each group, so a capped picker is not filled with embeddings
// and image models that sort first.
func (s *Store) SearchProviders(ctx context.Context, q string, litellmProviders []string, limit int) ([]Model, error) {
	if len(litellmProviders) == 0 {
		return []Model{}, nil
	}
	limit = clampSearchLimit(limit, true)
	qLower := strings.ToLower(q)
	want := make(map[string]bool, len(litellmProviders))
	for _, p := range litellmProviders {
		want[p] = true
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return rankedMatches(s.entries, qLower, limit, true, func(e Entry) bool {
		return want[e.LitellmProvider]
	}), nil
}

// clampSearchLimit applies the default and the max. allowUnlimited
// treats a negative limit as "every match" — Search itself never does,
// so a client cannot dump the catalog through the search endpoint.
func clampSearchLimit(limit int, allowUnlimited bool) int {
	if allowUnlimited && limit < 0 {
		return math.MaxInt
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	return limit
}

// rankedMatches filters s's entries (already sorted alphabetically by
// model_key at Sync time) by qLower (case-insensitive substring on
// model_key, empty matches all) and keep, then returns up to limit:
// model_key-prefix matches first, then remaining substring matches —
// each group in the alphabetical order entries already carry. Same
// deterministic order regardless of qLower's position in the key, so
// which `limit` rows a caller gets never depends on map iteration.
func rankedMatches(entries []Entry, qLower string, limit int, chatFirst bool, keep func(Entry) bool) []Model {
	prefix := []Model{}
	substring := []Model{}
	for _, e := range entries {
		if !keep(e) {
			continue
		}
		keyLower := strings.ToLower(e.ModelKey)
		switch {
		case qLower == "" || strings.HasPrefix(keyLower, qLower):
			prefix = append(prefix, toModel(e))
		case strings.Contains(keyLower, qLower):
			substring = append(substring, toModel(e))
		}
	}
	if chatFirst && qLower == "" {
		prefix = chatFirstModels(prefix)
	}
	out := append(prefix, substring...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ChatLike is a mode a person picks as a chat/default model. Other
// modes (embedding, image, audio, moderation) stay in the catalog but
// sort after these when a list is capped.
func ChatLike(mode string) bool {
	switch mode {
	case "chat", "completion", "responses":
		return true
	default:
		return false
	}
}

func chatFirstModels(models []Model) []Model {
	chat := make([]Model, 0, len(models))
	rest := make([]Model, 0)
	for _, m := range models {
		if ChatLike(m.Mode) {
			chat = append(chat, m)
		} else {
			rest = append(rest, m)
		}
	}
	return append(chat, rest...)
}

// byProvider returns every catalog entry for one of litellmProviders —
// the suggestion matcher's candidate pool when matching by segment.
func (s *Store) byProvider(ctx context.Context, litellmProviders []string) ([]Model, error) {
	if len(litellmProviders) == 0 {
		return nil, nil
	}
	want := make(map[string]bool, len(litellmProviders))
	for _, p := range litellmProviders {
		want[p] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Model
	for _, e := range s.entries {
		if want[e.LitellmProvider] {
			out = append(out, toModel(e))
		}
	}
	return out, nil
}

// AllModels returns the whole catalog, unpaginated — for callers that
// match against every entry regardless of provider (e.g. the admin
// catalog-prices endpoint, which ignores provider the same way the old
// static catalog file did).
func (s *Store) AllModels(ctx context.Context) ([]Model, error) {
	return s.allProviders(ctx)
}

// allProviders returns the whole catalog, unpaginated.
func (s *Store) allProviders(ctx context.Context) ([]Model, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Model, len(s.entries))
	for i, e := range s.entries {
		out[i] = toModel(e)
	}
	return out, nil
}

func toModel(e Entry) Model {
	return Model{
		ID:                StripOwnPrefix(e.ModelKey, e.LitellmProvider),
		ModelKey:          e.ModelKey,
		LitellmProvider:   e.LitellmProvider,
		Mode:              e.Mode,
		MaxInputTokens:    e.MaxInputTokens,
		MaxOutputTokens:   e.MaxOutputTokens,
		InputPerMTok:      e.InputPerMTok,
		OutputPerMTok:     e.OutputPerMTok,
		CacheReadPerMTok:  e.CacheReadPerMTok,
		CacheWritePerMTok: e.CacheWritePerMTok,
		SupportsVision:    e.SupportsVision,
	}
}
