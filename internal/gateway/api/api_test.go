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

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/metrics"
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

// oaiSlow blocks until release is closed before writing anything —
// standing in for a local model still cold-loading or mid-prefill.
func oaiSlow(t *testing.T, release <-chan struct{}, text string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
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

// oaiEmptyDone streams usage then [DONE] with zero content deltas — a
// provider stream that terminates cleanly but produced no output
// (D-044).
func oaiEmptyDone(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":0}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// silentCutProvider's Stream returns a channel that closes having
// delivered zero events — no chunk, no done, no error — standing in
// for the upstream-cut race where runStream's own terminal emit lost
// against the parent context (httpx.go's emit silently no-ops when
// its ctx is done), so the provider layer never surfaces why.
type silentCutProvider struct{ name string }

func (p silentCutProvider) Name() string                    { return p.name }
func (p silentCutProvider) Kind() provider.Kind              { return provider.KindAPI }
func (p silentCutProvider) Capabilities() []provider.Capability { return nil }
func (p silentCutProvider) Stream(context.Context, provider.CompletionRequest) (<-chan stream.StreamEvent, error) {
	ch := make(chan stream.StreamEvent)
	close(ch)
	return ch, nil
}

// midStreamCutProvider streams one real chunk, then closes silently —
// the same race as silentCutProvider, but after content already
// reached the client, so failover is no longer an option (D-044's
// "errors after content are relayed honestly" contract) and the
// client must instead get an explicit terminal error frame.
type midStreamCutProvider struct{ name string }

func (p midStreamCutProvider) Name() string                       { return p.name }
func (p midStreamCutProvider) Kind() provider.Kind                 { return provider.KindAPI }
func (p midStreamCutProvider) Capabilities() []provider.Capability { return nil }
func (p midStreamCutProvider) Stream(context.Context, provider.CompletionRequest) (<-chan stream.StreamEvent, error) {
	ch := make(chan stream.StreamEvent, 1)
	ch <- stream.StreamEvent{Type: stream.EventChunk, Text: "hel"}
	close(ch)
	return ch, nil
}

// fakeCatalog is a minimal in-memory catalogLookup for router tests: it
// returns its whole seeded pool regardless of q/litellmProviders, since
// tests only care about catalog.Match's downstream lookup by id.
type fakeCatalog struct{ models []catalog.Model }

func (f fakeCatalog) SearchProviders(_ context.Context, _ string, _ []string, _ int) ([]catalog.Model, error) {
	return f.models, nil
}

func f64(v float64) *float64 { return &v }
func fbool(v bool) *bool     { return &v }

// snapshotFor builds a two-provider snapshot with a coding route
// chaining first→second and an embedding route on first.
func snapshotFor(t *testing.T, firstURL, secondURL string) *router.Snapshot {
	t.Helper()
	rows := []router.ProviderRow{
		{ID: "p1", Name: "one", Kind: "api", Driver: "openaicompat", BaseURL: firstURL,
			DefaultModel: "m1", Enabled: true},
		{ID: "p2", Name: "two", Kind: "api", Driver: "openaicompat", BaseURL: secondURL,
			DefaultModel: "m2", Enabled: true},
	}
	routes := []router.RouteRow{
		{Name: "coding", Chain: []router.ChainEntry{
			{ProviderID: "p1", Model: "m1"}, {ProviderID: "p2", Model: "m2"},
		}, Enabled: true},
		{Name: "embedding", Capability: "embeddings", Role: "embedding", Chain: []router.ChainEntry{
			{ProviderID: "p1", Model: "m1"},
		}, Enabled: true},
	}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "" }, nil)
	return snap
}

func newAPI(snap *router.Snapshot) (*API, *memRecorder) {
	rec := &memRecorder{}
	a := &API{store: &fakeSource{snap: snap}, ledger: rec, log: discard()}
	a.providerCalls = metrics.New().NewCounterVec("provider_calls_total", "test", "provider", "route", "status")
	return a, rec
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

// pricedSnapshotFor is snapshotFor with a real catalog price seeded for
// p1's model, isolated from snapshotFor's shared embedding-route fixture
// so seeding a catalog entry for "m1" never gates its embeddings-capable
// chain entry (mode-restricted catalog presence is stricter than the old
// undeclared-capability permissive default).
func pricedSnapshotFor(t *testing.T, firstURL, secondURL string) *router.Snapshot {
	t.Helper()
	rows := []router.ProviderRow{
		{ID: "p1", Name: "one", Kind: "api", Driver: "openaicompat", BaseURL: firstURL,
			DefaultModel: "m1", Enabled: true},
		{ID: "p2", Name: "two", Kind: "api", Driver: "openaicompat", BaseURL: secondURL,
			DefaultModel: "m2", Enabled: true},
	}
	routes := []router.RouteRow{
		{Name: "coding", Chain: []router.ChainEntry{
			{ProviderID: "p1", Model: "m1"}, {ProviderID: "p2", Model: "m2"},
		}, Enabled: true},
	}
	cat := fakeCatalog{models: []catalog.Model{
		{ID: "m1", ModelKey: "m1", Mode: "chat", InputPerMTok: f64(1), OutputPerMTok: f64(2)},
	}}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "" }, cat)
	return snap
}

func TestStreamHappyPathWithLedger(t *testing.T) {
	t.Parallel()
	a, rec := newAPI(pricedSnapshotFor(t, oaiOK(t, "hello").URL, oaiFail(t).URL))

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
	done := events[len(events)-1]
	if done.Type != stream.EventDone {
		t.Fatalf("last event = %v", done.Type)
	}
	// Cost must ride the terminal event's Meta, not just the ledger row
	// written after streamAttempt returns: the client has no other way
	// to see the price of a turn it just watched happen.
	if done.Meta == nil || done.Meta.Cost == nil || *done.Meta.Cost != 10.0/1e6+2*5.0/1e6 {
		t.Fatalf("done.Meta.Cost = %+v, want the priced cost", done.Meta)
	}
	if done.Meta.Currency != "USD" {
		t.Fatalf("done.Meta.Currency = %q, want USD", done.Meta.Currency)
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
	if e.Cost == nil || *e.Cost != 10.0/1e6+2*5.0/1e6 {
		t.Fatalf("cost = %v", e.Cost)
	}
	// The fake server sleeps before responding: a zero here means the
	// deferred latency stamp mutated a dead copy again.
	if e.LatencyMS <= 0 {
		t.Fatalf("latency_ms = %d, want > 0", e.LatencyMS)
	}
	if got := testutil.ToFloat64(a.providerCalls.WithLabelValues("one", "coding", "ok")); got != 1 {
		t.Fatalf("provider_calls_total{one,coding,ok} = %v, want 1", got)
	}
}

// TestStreamEmptyOutputBookedIncomplete pins D-044: a provider stream
// that terminates cleanly with zero content deltas ([DONE] only) must
// not be booked as a success. res.streamed is set only by content
// events (chunk/reasoning/tool) — never by EventUsage — so the ledger
// status flips to "incomplete" even though usage/cost are still known
// (a real, reported zero, not the unknown-so-NULL case D-013 covers).
// The client sees an EventIncomplete before the terminal done, the same
// shape a cut-off stream already uses.
func TestStreamEmptyOutputBookedIncomplete(t *testing.T) {
	t.Parallel()
	a, rec := newAPI(snapshotFor(t, oaiEmptyDone(t).URL, oaiFail(t).URL))

	w := postJSON(t, a.handleStream, `{"route":"coding","session_id":"s1","messages":[{"role":"user","content":"hi"}]}`)

	events := sseEvents(t, w.Body.String())
	if len(events) < 2 {
		t.Fatalf("events = %+v, want at least incomplete+done", events)
	}
	last := events[len(events)-1]
	if last.Type != stream.EventDone {
		t.Fatalf("last event = %v, want done", last.Type)
	}
	prev := events[len(events)-2]
	if prev.Type != stream.EventIncomplete {
		t.Fatalf("event before done = %v, want incomplete", prev.Type)
	}

	entries := rec.all()
	if len(entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Status != "incomplete" {
		t.Fatalf("status = %q, want incomplete", e.Status)
	}
	if e.Usage == nil || e.Usage.OutputTokens != 0 {
		t.Fatalf("usage = %+v, want reported zero output tokens", e.Usage)
	}
}

// TestStreamHeadersFlushBeforeFirstProviderEvent pins the fix for the
// gateway sitting silent past the caller's response-header timeout
// while a slow-to-first-token provider (e.g. a cold-loading local
// model) is still working: headers must reach the client as soon as
// the handler starts, not on the first send().
func TestStreamHeadersFlushBeforeFirstProviderEvent(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	a, _ := newAPI(snapshotFor(t, oaiSlow(t, release, "hello").URL, oaiFail(t).URL))

	srv := httptest.NewServer(http.HandlerFunc(a.handleStream))
	t.Cleanup(srv.Close)

	// Shorter than any deliberate delay in this test: if headers only
	// went out on the first send(), this would time out while the
	// provider is still blocked on release.
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 500 * time.Millisecond}}

	resp, err := client.Post(srv.URL, "application/json", strings.NewReader(
		`{"route":"coding","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("post: %v (headers did not arrive before the provider produced anything)", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 before any provider event", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// Now let the provider finish and confirm the stream still drains
	// normally end to end.
	close(release)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	events := sseEvents(t, string(body))
	if len(events) == 0 || events[len(events)-1].Type != stream.EventDone {
		t.Fatalf("events = %+v, want trailing done", events)
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
	if got := testutil.ToFloat64(a.providerCalls.WithLabelValues("one", "coding", "error")); got != 1 {
		t.Fatalf("provider_calls_total{one,coding,error} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(a.providerCalls.WithLabelValues("two", "coding", "ok")); got != 1 {
		t.Fatalf("provider_calls_total{two,coding,ok} = %v, want 1", got)
	}
}

func TestStreamChainExhausted(t *testing.T) {
	t.Parallel()
	a, rec := newAPI(snapshotFor(t, oaiFail(t).URL, oaiFail(t).URL))

	w := postJSON(t, a.handleStream, `{"route":"coding","messages":[{"role":"user","content":"hi"}]}`)

	events := sseEvents(t, w.Body.String())
	if len(events) != 2 || events[0].Type != stream.EventFailover || events[1].Type != stream.EventError || events[1].Err.Code != "chain_exhausted" {
		t.Fatalf("events = %+v, want [failover, chain_exhausted error]", events)
	}
	if events[0].Failover.FromProvider != "one" || events[0].Failover.ToProvider != "two" {
		t.Fatalf("failover event = %+v, want from one to two", events[0].Failover)
	}
	if !strings.Contains(events[1].Err.Message, "http_401") {
		t.Fatalf("exhaustion message missing error codes: %s", events[1].Err.Message)
	}
	if len(rec.all()) != 2 {
		t.Fatalf("ledger entries = %d, want 2 failures", len(rec.all()))
	}
}

// TestStreamAttemptSilentCloseIsAFailure pins the fix for a provider
// channel closing with zero events (no chunk, no done, no error)
// instead of any well-formed terminal — previously streamAttempt's
// range loop simply never ran its body, leaving res all zero values
// and nothing sent to the client, indistinguishable from a legitimate
// empty completion. It must now report a real failure so the chain
// can fail over (or the client at least gets a reason).
func TestStreamAttemptSilentCloseIsAFailure(t *testing.T) {
	t.Parallel()
	att := router.Attempt{Provider: silentCutProvider{name: "one"}, ProviderName: "one", Model: "m1"}
	var sent []stream.StreamEvent
	res := streamAttempt(t.Context(), att, provider.CompletionRequest{}, ledger.Entry{}, nil, func(ev stream.StreamEvent) {
		sent = append(sent, ev)
	})

	if !res.failed {
		t.Fatalf("res.failed = false, want true for a silently closed channel")
	}
	if res.entry.ErrorCode != "stream_cut" {
		t.Fatalf("entry.ErrorCode = %q, want stream_cut", res.entry.ErrorCode)
	}
	if len(sent) != 0 {
		t.Fatalf("sent = %+v, want nothing relayed to the client for a pre-content failure", sent)
	}
	if !res.failedOver() {
		t.Fatalf("failedOver() = false, want true so the chain can advance")
	}
}

// TestStreamAttemptMidStreamCutSendsErrorEvent pins the fix for the
// same silent-close race when it happens AFTER content already
// reached the client: failover is not an option once streamed, so the
// only way the client (and brain's D-044 persistTurn) learns anything
// went wrong is an explicit EventError frame. Before this fix,
// streamAttempt only mutated its own attemptResult and returned —
// the client saw a clean channel close with the chunk it already had
// and nothing else, so brain persisted neither an assistant message
// nor a failure event.
func TestStreamAttemptMidStreamCutSendsErrorEvent(t *testing.T) {
	t.Parallel()
	att := router.Attempt{Provider: midStreamCutProvider{name: "one"}, ProviderName: "one", Model: "m1"}
	var sent []stream.StreamEvent
	res := streamAttempt(t.Context(), att, provider.CompletionRequest{}, ledger.Entry{}, nil, func(ev stream.StreamEvent) {
		sent = append(sent, ev)
	})

	if !res.failed {
		t.Fatalf("res.failed = false, want true for a mid-stream cut")
	}
	if res.entry.ErrorCode != "stream_cut" {
		t.Fatalf("entry.ErrorCode = %q, want stream_cut", res.entry.ErrorCode)
	}
	if res.failedOver() {
		t.Fatalf("failedOver() = true, want false: content already streamed, no failover once sent")
	}
	if len(sent) != 2 {
		t.Fatalf("sent = %+v, want [chunk, error]", sent)
	}
	if sent[0].Type != stream.EventChunk {
		t.Fatalf("sent[0].Type = %v, want EventChunk", sent[0].Type)
	}
	if sent[1].Type != stream.EventError || sent[1].Err == nil || sent[1].Err.Code != "stream_cut" {
		t.Fatalf("sent[1] = %+v, want an EventError with code stream_cut", sent[1])
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

// visionMessages is a one-turn request carrying an image, forcing
// requiredVisionCapability to demand CapVision at Resolve.
const visionMessages = `[{"role":"user","content":"look","images":[{"media_type":"image/png","data":"AAAA"}]}]`

// TestStreamVisionRouteMissingFallsBackToDefault is the D-046 gateway
// safety net under test: "vision" was never created on this install (no
// route row at all), so Resolve's NoRouteError carries zero Skipped
// entries — nothing was even tried. handleStream retries on "default",
// which chains a vision-capable model, and the image turn still serves.
func TestStreamVisionRouteMissingFallsBackToDefault(t *testing.T) {
	t.Parallel()
	srv := oaiOK(t, "described")
	rows := []router.ProviderRow{
		{ID: "p1", Name: "one", Kind: "api", Driver: "openaicompat", BaseURL: srv.URL,
			DefaultModel: "m1", Enabled: true},
	}
	routes := []router.RouteRow{
		{Name: "default", Role: "default", Chain: []router.ChainEntry{{ProviderID: "p1", Model: "m1"}}, Enabled: true},
		// no "vision" route row at all.
	}
	cat := fakeCatalog{models: []catalog.Model{
		{ID: "m1", ModelKey: "m1", Mode: "chat", SupportsVision: fbool(true)},
	}}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "" }, cat)
	a, rec := newAPI(snap)

	w := postJSON(t, a.handleStream, fmt.Sprintf(`{"route":"vision","messages":%s}`, visionMessages))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	events := sseEvents(t, w.Body.String())
	var text string
	for _, ev := range events {
		if ev.Type == stream.EventChunk {
			text += ev.Text
		}
		if ev.Type == stream.EventError {
			t.Fatalf("error event leaked to client: %+v", ev)
		}
	}
	if text != "described" {
		t.Fatalf("chunks = %q", text)
	}
	entries := rec.all()
	if len(entries) != 1 || entries[0].Route != "default" {
		t.Fatalf("ledger = %+v, want single entry booked under the fallback route", entries)
	}
}

// TestStreamVisionRouteDisabledFallsBackToDefault covers the "route
// exists but disabled" flavor of the same missing/empty class: disabled
// routes never enter the snapshot's routes map (BuildSnapshot only
// loads Enabled rows), so Resolve produces the identical empty-Skipped
// NoRouteError as a route that was never created.
func TestStreamVisionRouteDisabledFallsBackToDefault(t *testing.T) {
	t.Parallel()
	srv := oaiOK(t, "described")
	rows := []router.ProviderRow{
		{ID: "p1", Name: "one", Kind: "api", Driver: "openaicompat", BaseURL: srv.URL,
			DefaultModel: "m1", Enabled: true},
	}
	routes := []router.RouteRow{
		{Name: "default", Role: "default", Chain: []router.ChainEntry{{ProviderID: "p1", Model: "m1"}}, Enabled: true},
		{Name: "vision", Role: "vision", Capability: "vision", Chain: []router.ChainEntry{{ProviderID: "p1", Model: "m1"}}, Enabled: false},
	}
	cat := fakeCatalog{models: []catalog.Model{
		{ID: "m1", ModelKey: "m1", Mode: "chat", SupportsVision: fbool(true)},
	}}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "" }, cat)
	a, rec := newAPI(snap)

	w := postJSON(t, a.handleStream, fmt.Sprintf(`{"route":"vision","messages":%s}`, visionMessages))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	entries := rec.all()
	if len(entries) != 1 || entries[0].Route != "default" {
		t.Fatalf("ledger = %+v, want single entry booked under the fallback route", entries)
	}
}

// TestStreamVisionRoutePresentNoFallback confirms a real, enabled,
// vision-capable "vision" route is served directly — no fallback fires,
// and the ledger books the request under "vision" itself, not "default".
func TestStreamVisionRoutePresentNoFallback(t *testing.T) {
	t.Parallel()
	visionSrv := oaiOK(t, "from-vision-route")
	defaultSrv := oaiOK(t, "from-default-route")
	rows := []router.ProviderRow{
		{ID: "p1", Name: "vprov", Kind: "api", Driver: "openaicompat", BaseURL: visionSrv.URL,
			DefaultModel: "vm", Enabled: true},
		{ID: "p2", Name: "dprov", Kind: "api", Driver: "openaicompat", BaseURL: defaultSrv.URL,
			DefaultModel: "dm", Enabled: true},
	}
	routes := []router.RouteRow{
		{Name: "vision", Role: "vision", Capability: "vision", Chain: []router.ChainEntry{{ProviderID: "p1", Model: "vm"}}, Enabled: true},
		{Name: "default", Role: "default", Chain: []router.ChainEntry{{ProviderID: "p2", Model: "dm"}}, Enabled: true},
	}
	cat := fakeCatalog{models: []catalog.Model{
		{ID: "vm", ModelKey: "vm", Mode: "chat", SupportsVision: fbool(true)},
		{ID: "dm", ModelKey: "dm", Mode: "chat", SupportsVision: fbool(true)},
	}}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "" }, cat)
	a, rec := newAPI(snap)

	w := postJSON(t, a.handleStream, fmt.Sprintf(`{"route":"vision","messages":%s}`, visionMessages))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	events := sseEvents(t, w.Body.String())
	var text string
	for _, ev := range events {
		if ev.Type == stream.EventChunk {
			text += ev.Text
		}
	}
	if text != "from-vision-route" {
		t.Fatalf("chunks = %q, want served by the real vision route (no fallback)", text)
	}
	entries := rec.all()
	if len(entries) != 1 || entries[0].Route != "vision" {
		t.Fatalf("ledger = %+v, want single entry booked under vision", entries)
	}
}

// TestStreamVisionCapabilityExhaustedNoFallback confirms the fallback
// does NOT fire when "vision" exists and is enabled but every chain
// entry lacks the vision capability: Resolve's NoRouteError carries
// non-empty Skipped ("lacks vision capability") in that case, the
// signal that something was tried and rejected — falling back here
// would silently serve an image turn on a model that can't see it.
func TestStreamVisionCapabilityExhaustedNoFallback(t *testing.T) {
	t.Parallel()
	rows := []router.ProviderRow{
		{ID: "p1", Name: "textonly", Kind: "api", Driver: "openaicompat", BaseURL: oaiOK(t, "x").URL,
			DefaultModel: "m1", Enabled: true},
	}
	routes := []router.RouteRow{
		{Name: "vision", Role: "vision", Capability: "vision", Chain: []router.ChainEntry{{ProviderID: "p1", Model: "m1"}}, Enabled: true},
	}
	cat := fakeCatalog{models: []catalog.Model{
		{ID: "m1", ModelKey: "m1", Mode: "chat"}, // no vision
	}}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "" }, cat)
	a, rec := newAPI(snap)

	w := postJSON(t, a.handleStream, fmt.Sprintf(`{"route":"vision","messages":%s}`, visionMessages))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 (capability-exhausted must still error, no fallback)", w.Code)
	}
	if len(rec.all()) != 0 {
		t.Fatalf("ledger entries = %+v, want none (Resolve failed before any attempt)", rec.all())
	}
}

// TestStreamNonVisionUnknownRouteStillErrors confirms the fallback is
// vision-only: a request for any other unknown/misconfigured route
// keeps erroring exactly as before (TestStreamValidation's "nope" case,
// duplicated here to pin it explicitly against this feature).
func TestStreamNonVisionUnknownRouteStillErrors(t *testing.T) {
	t.Parallel()
	a, _ := newAPI(snapshotFor(t, oaiFail(t).URL, oaiFail(t).URL))

	w := postJSON(t, a.handleStream, `{"route":"nope","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 (non-vision unknown route, no fallback)", w.Code)
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
			BaseURL: capable.URL, DefaultModel: "m1", Enabled: true},
	}
	routes := []router.RouteRow{
		{Name: "embedding", Capability: "embeddings", Role: "embedding", Chain: []router.ChainEntry{
			{ProviderID: "p0", Model: "m0"}, {ProviderID: "p1", Model: "m1"},
		}, Enabled: true},
	}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "" }, nil)
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
	}}
	snap, _ := router.BuildSnapshot(rows, nil, func(string) string { return secret }, nil)
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

// resolveSnapshot builds a "coding" route mixing a plain chat entry
// with a subscription-auth kind='cli' row (D-051's shape — harness is
// no longer a chain field; the executor axis is selected by the
// resolve endpoint's ?harness= query param instead).
func resolveSnapshot(t *testing.T) *router.Snapshot {
	t.Helper()
	rows := []router.ProviderRow{
		{ID: "p1", Name: "anthropic", Kind: "api", Driver: "anthropic",
			BaseURL: "https://api.anthropic.com", DefaultModel: "sonnet",
			CredentialRef: "A_KEY", Enabled: true},
		{ID: "p2", Name: "claude-sub", Kind: "cli", Driver: "claude-cli",
			CredentialRef: "subscription", Enabled: true, AnthropicBaseURL: "http://localhost:9999"},
	}
	routes := []router.RouteRow{
		{Name: "coding", Enabled: true, Chain: []router.ChainEntry{
			{ProviderID: "p1", Model: "sonnet"},
			{ProviderID: "p2", Model: "claude-sonnet-4"},
		}},
	}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "sk" }, nil)
	return snap
}

func resolveReq(name, harness string) *http.Request {
	url := "/v1/routes/" + name + "/resolve"
	if harness != "" {
		url += "?harness=" + harness
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.SetPathValue("name", name)
	return req
}

func TestResolveRouteChatAxisDefault(t *testing.T) {
	t.Parallel()
	a, _ := newAPI(resolveSnapshot(t))

	w := httptest.NewRecorder()
	a.handleResolveRoute(w, resolveReq("coding", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", w.Code, w.Body.String())
	}

	var out struct {
		Route   string              `json:"route"`
		Entries []resolveRouteEntry `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Route != "coding" || len(out.Entries) != 2 {
		t.Fatalf("out = %+v", out)
	}
	api := out.Entries[0]
	if !api.Usable || api.SkipReason != "" {
		t.Fatalf("api entry = %+v", api)
	}
	// kind='cli' row has no chat driver built for it at all.
	if out.Entries[1].Usable {
		t.Fatalf("cli-kind entry usable on chat axis: %+v", out.Entries[1])
	}
	if strings.Contains(w.Body.String(), `"sk"`) {
		t.Fatalf("resolved secret leaked into resolve response: %s", w.Body.String())
	}
}

func TestResolveRouteExecutorAxis(t *testing.T) {
	t.Parallel()
	a, _ := newAPI(resolveSnapshot(t))

	w := httptest.NewRecorder()
	a.handleResolveRoute(w, resolveReq("coding", "claude-cli"))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", w.Code, w.Body.String())
	}

	var out struct {
		Route   string              `json:"route"`
		Entries []resolveRouteEntry `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Entries) != 2 {
		t.Fatalf("out = %+v", out)
	}
	exec := out.Entries[1]
	if !exec.Usable || exec.SkipReason != "" {
		t.Fatalf("executor entry = %+v", exec)
	}
	if exec.CredentialRef != "subscription" {
		t.Fatalf("credential_ref = %q, want name only", exec.CredentialRef)
	}
	// A kind='cli' row never gets the anthropic_base_url override
	// (bugfix): it spawns its own CLI against the vendor's default
	// endpoint under subscription/oauth credentials, and BuildInvocation
	// rejects any BaseURL under those auth modes.
	if exec.BaseURL != "" {
		t.Fatalf("base_url = %q, want empty for a kind='cli' row", exec.BaseURL)
	}
}

func TestResolveRouteHarnessOnly(t *testing.T) {
	t.Parallel()
	rows := []router.ProviderRow{
		{ID: "p2", Name: "claude-sub", Kind: "cli", Driver: "claude-cli",
			CredentialRef: "subscription", Enabled: true, AnthropicBaseURL: "http://localhost:9999"},
	}
	routes := []router.RouteRow{
		{Name: "coding-exec", Enabled: true, Chain: []router.ChainEntry{
			{ProviderID: "p2", Model: "claude-sonnet-4"},
		}},
	}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "sk" }, nil)
	a, _ := newAPI(snap)

	w := httptest.NewRecorder()
	a.handleResolveRoute(w, resolveReq("coding-exec", "claude-cli"))
	var out struct {
		Entries []resolveRouteEntry `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Entries) != 1 || !out.Entries[0].Usable {
		t.Fatalf("entries = %+v", out.Entries)
	}
}

func TestResolveRouteUnknownHarnessParamRejected(t *testing.T) {
	t.Parallel()
	a, _ := newAPI(resolveSnapshot(t))

	w := httptest.NewRecorder()
	a.handleResolveRoute(w, resolveReq("coding", "nonexistent-cli"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 for unknown harness param", w.Code)
	}
}

func TestResolveRouteWireIncompatibleProvider(t *testing.T) {
	t.Parallel()
	rows := []router.ProviderRow{
		{ID: "p2", Name: "grok-sub", Kind: "api", Driver: "openaicompat",
			CredentialRef: "subscription", Enabled: true},
	}
	routes := []router.RouteRow{
		{Name: "coding-exec", Enabled: true, Chain: []router.ChainEntry{
			{ProviderID: "p2", Model: "m"},
		}},
	}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "sk" }, nil)
	a, _ := newAPI(snap)

	w := httptest.NewRecorder()
	a.handleResolveRoute(w, resolveReq("coding-exec", "claude-cli"))
	var out struct {
		Entries []resolveRouteEntry `json:"entries"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Entries) != 1 || out.Entries[0].Usable || !strings.Contains(out.Entries[0].SkipReason, "wire-incompatible") {
		t.Fatalf("entries = %+v", out.Entries)
	}
}

func TestResolveRouteNotFound(t *testing.T) {
	t.Parallel()
	a, _ := newAPI(resolveSnapshot(t))

	w := httptest.NewRecorder()
	a.handleResolveRoute(w, resolveReq("no-such-route", ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

func TestResolveRouteConfigUnavailable(t *testing.T) {
	t.Parallel()
	a, _ := newAPI(nil)

	w := httptest.NewRecorder()
	a.handleResolveRoute(w, resolveReq("coding", ""))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", w.Code)
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
