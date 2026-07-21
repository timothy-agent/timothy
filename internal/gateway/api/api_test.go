package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

type fakeSource struct {
	snap    *router.Snapshot
	loadErr error
}

func (f *fakeSource) Snapshot() *router.Snapshot   { return f.snap }
func (f *fakeSource) Load(_ context.Context) error { return f.loadErr }

type memRecorder struct {
	mu      sync.Mutex
	entries []ledger.Entry
}

func (m *memRecorder) Record(_ context.Context, e ledger.Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
}

func (m *memRecorder) all() []ledger.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ledger.Entry(nil), m.entries...)
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// oaiOK serves a minimal successful chat/completions SSE stream. The
// small sleep makes latency measurable for the ledger assertion.
func oaiOK(t *testing.T, text string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond)
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":  []map[string]any{{"index": 0, "embedding": []float32{0.1, 0.2}}, {"index": 1, "embedding": []float32{0.3, 0.4}}},
				"usage": map[string]int{"prompt_tokens": 7},
			})
			return
		}
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", text)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// oaiFail always answers 401 (permanent, no retry).
func oaiFail(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// snapshotFor builds a two-provider snapshot with a coding route
// chaining first→second and an embedding route on first.
func snapshotFor(t *testing.T, firstURL, secondURL string) *router.Snapshot {
	t.Helper()
	prices := &router.ModelPrices{InputPerMTok: 1, OutputPerMTok: 2}
	rows := []router.ProviderRow{
		{ID: "p1", Name: "one", Kind: "api", Driver: "openaicompat", BaseURL: firstURL,
			DefaultModel: "m1", Enabled: true,
			Models: []router.ModelInfo{{ID: "m1", Prices: prices}}},
		{ID: "p2", Name: "two", Kind: "api", Driver: "openaicompat", BaseURL: secondURL,
			DefaultModel: "m2", Enabled: true,
			Models: []router.ModelInfo{{ID: "m2"}}},
	}
	routes := []router.RouteRow{
		{Name: "coding", Chain: []router.ChainEntry{
			{ProviderID: "p1", Model: "m1"}, {ProviderID: "p2", Model: "m2"},
		}, Enabled: true},
		{Name: "embedding", Chain: []router.ChainEntry{
			{ProviderID: "p1", Model: "m1"},
		}, Enabled: true},
	}
	snap, err := router.BuildSnapshot(rows, routes, func(string) string { return "" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	return snap
}

func newAPI(snap *router.Snapshot) (*API, *memRecorder) {
	rec := &memRecorder{}
	return &API{store: &fakeSource{snap: snap}, ledger: rec, log: discard()}, rec
}

func postJSON(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func sseEvents(t *testing.T, body string) []stream.StreamEvent {
	t.Helper()
	var events []stream.StreamEvent
	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var ev stream.StreamEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatalf("bad SSE line %q: %v", line, err)
			}
			events = append(events, ev)
		}
	}
	return events
}

func TestStreamHappyPathWithLedger(t *testing.T) {
	t.Parallel()
	a, rec := newAPI(snapshotFor(t, oaiOK(t, "hello").URL, oaiFail(t).URL))

	w := postJSON(t, a.handleStream, `{"route":"coding","session_id":"s1","messages":[{"role":"user","content":"hi"}]}`)

	events := sseEvents(t, w.Body.String())
	var text string
	for _, ev := range events {
		if ev.Type == stream.EventChunk {
			text += ev.Text
		}
	}
	if text != "hello" {
		t.Fatalf("chunks = %q, events %+v", text, events)
	}
	if events[len(events)-1].Type != stream.EventDone {
		t.Fatalf("last event = %v", events[len(events)-1].Type)
	}

	entries := rec.all()
	if len(entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Status != "ok" || e.Provider != "one" || e.Model != "m1" || e.SessionID != "s1" {
		t.Fatalf("entry = %+v", e)
	}
	if e.Usage == nil || e.Usage.InputTokens != 10 || e.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", e.Usage)
	}
	// priced model: 10/1e6*1 + 5/1e6*2
	if e.CostUSD == nil || *e.CostUSD != 10.0/1e6+2*5.0/1e6 {
		t.Fatalf("cost = %v", e.CostUSD)
	}
	// The fake server sleeps before responding: a zero here means the
	// deferred latency stamp mutated a dead copy again.
	if e.LatencyMS <= 0 {
		t.Fatalf("latency_ms = %d, want > 0", e.LatencyMS)
	}
}

func TestStreamFailoverToSecondProvider(t *testing.T) {
	t.Parallel()
	a, rec := newAPI(snapshotFor(t, oaiFail(t).URL, oaiOK(t, "backup").URL))

	w := postJSON(t, a.handleStream, `{"route":"coding","messages":[{"role":"user","content":"hi"}]}`)

	events := sseEvents(t, w.Body.String())
	var text string
	for _, ev := range events {
		if ev.Type == stream.EventChunk {
			text += ev.Text
		}
		if ev.Type == stream.EventError {
			t.Fatalf("error event leaked to client during clean failover: %+v", ev)
		}
	}
	if text != "backup" {
		t.Fatalf("chunks = %q", text)
	}

	entries := rec.all()
	if len(entries) != 2 {
		t.Fatalf("ledger entries = %d, want 2 (failed + ok)", len(entries))
	}
	if entries[0].Status != "error" || entries[0].Provider != "one" {
		t.Fatalf("first entry = %+v", entries[0])
	}
	if entries[1].Status != "ok" || entries[1].Provider != "two" {
		t.Fatalf("second entry = %+v", entries[1])
	}
}

func TestStreamChainExhausted(t *testing.T) {
	t.Parallel()
	a, rec := newAPI(snapshotFor(t, oaiFail(t).URL, oaiFail(t).URL))

	w := postJSON(t, a.handleStream, `{"route":"coding","messages":[{"role":"user","content":"hi"}]}`)

	events := sseEvents(t, w.Body.String())
	if len(events) != 1 || events[0].Type != stream.EventError || events[0].Err.Code != "chain_exhausted" {
		t.Fatalf("events = %+v, want single chain_exhausted error", events)
	}
	if !strings.Contains(events[0].Err.Message, "one/m1") || !strings.Contains(events[0].Err.Message, "two/m2") {
		t.Fatalf("exhaustion message misses attempts: %s", events[0].Err.Message)
	}
	if len(rec.all()) != 2 {
		t.Fatalf("ledger entries = %d, want 2 failures", len(rec.all()))
	}
}

func TestStreamValidation(t *testing.T) {
	t.Parallel()
	a, _ := newAPI(snapshotFor(t, oaiFail(t).URL, oaiFail(t).URL))

	if w := postJSON(t, a.handleStream, `{"messages":[{"role":"user","content":"x"}]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("missing category: code = %d", w.Code)
	}
	if w := postJSON(t, a.handleStream, `{"route":"nope","messages":[{"role":"user","content":"x"}]}`); w.Code != http.StatusBadGateway {
		t.Fatalf("unknown category: code = %d", w.Code)
	}

	empty := &API{store: &fakeSource{}, ledger: &memRecorder{}, log: discard()}
	if w := postJSON(t, empty.handleStream, `{"route":"coding","messages":[{"role":"user","content":"x"}]}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no snapshot: code = %d", w.Code)
	}
}

func TestEmbedHappyPath(t *testing.T) {
	t.Parallel()
	a, rec := newAPI(snapshotFor(t, oaiOK(t, "x").URL, oaiFail(t).URL))

	w := postJSON(t, a.handleEmbed, `{"texts":["a","b"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Provider   string      `json:"provider"`
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Provider != "one" || len(out.Embeddings) != 2 || out.Embeddings[1][0] != 0.3 {
		t.Fatalf("out = %+v", out)
	}

	entries := rec.all()
	if len(entries) != 1 || entries[0].Status != "ok" || entries[0].Route != "embedding" {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Usage == nil || entries[0].Usage.InputTokens != 7 {
		t.Fatalf("usage = %+v", entries[0].Usage)
	}
}

func TestEmbedSkipsIncapableDriverAtResolve(t *testing.T) {
	t.Parallel()
	capable := oaiOK(t, "x")
	rows := []router.ProviderRow{
		// anthropic driver first in the chain: no embeddings capability,
		// must be skipped at resolve time with zero ledger rows.
		{ID: "p0", Name: "no-embed", Kind: "api", Driver: "anthropic",
			DefaultModel: "m0", Enabled: true},
		{ID: "p1", Name: "can-embed", Kind: "api", Driver: "openaicompat",
			BaseURL: capable.URL, DefaultModel: "m1", Enabled: true,
			Models: []router.ModelInfo{{ID: "m1"}}},
	}
	routes := []router.RouteRow{
		{Name: "embedding", Chain: []router.ChainEntry{
			{ProviderID: "p0", Model: "m0"}, {ProviderID: "p1", Model: "m1"},
		}, Enabled: true},
	}
	snap, err := router.BuildSnapshot(rows, routes, func(string) string { return "" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	a, rec := newAPI(snap)

	w := postJSON(t, a.handleEmbed, `{"texts":["a","b"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"provider":"can-embed"`) {
		t.Fatalf("wrong provider served: %s", w.Body.String())
	}
	entries := rec.all()
	if len(entries) != 1 || entries[0].Provider != "can-embed" {
		t.Fatalf("ledger = %+v, want single can-embed row (no wasted attempt)", entries)
	}
}

func TestProvidersListingNeverLeaksSecrets(t *testing.T) {
	t.Parallel()
	secret := "sk-super-secret-value"
	rows := []router.ProviderRow{{
		ID: "p1", Name: "one", Kind: "api", Driver: "openaicompat",
		BaseURL: "https://x.example/v1", DefaultModel: "m1",
		CredentialRef: "ONE_KEY", Enabled: true,
		Models: []router.ModelInfo{{ID: "m1"}},
	}}
	snap, err := router.BuildSnapshot(rows, nil, func(string) string { return secret })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	a, _ := newAPI(snap)

	req := httptest.NewRequest(http.MethodGet, "/v1/providers", nil)
	w := httptest.NewRecorder()
	a.handleProviders(w, req)

	body := w.Body.String()
	if strings.Contains(body, secret) {
		t.Fatal("resolved secret value leaked into providers listing")
	}
	if !strings.Contains(body, `"credential_ref":"ONE_KEY"`) {
		t.Fatalf("credential ref name missing: %s", body)
	}
	if !strings.Contains(body, `"healthy":true`) {
		t.Fatalf("health missing: %s", body)
	}
}

func TestReload(t *testing.T) {
	t.Parallel()
	a, _ := newAPI(nil)
	req := httptest.NewRequest(http.MethodPost, "/internal/reload", nil)

	w := httptest.NewRecorder()
	a.handleReload(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("reload ok: code = %d", w.Code)
	}

	a.store = &fakeSource{loadErr: errors.New("db down")}
	w = httptest.NewRecorder()
	a.handleReload(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("reload failure: code = %d", w.Code)
	}
}
