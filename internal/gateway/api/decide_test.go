package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// decideSnapshot builds a snapshot with a summarize role route on p1,
// plus an optional decide role route with the given chain.
func decideSnapshot(t *testing.T, firstURL, secondURL string, decideRoute *router.RouteRow) *router.Snapshot {
	t.Helper()
	rows := []router.ProviderRow{
		{ID: "p1", Name: "one", Kind: "api", Driver: "openaicompat", BaseURL: firstURL,
			DefaultModel: "m1", Enabled: true},
		{ID: "p2", Name: "two", Kind: "api", Driver: "openaicompat", BaseURL: secondURL,
			DefaultModel: "m2", Enabled: true},
	}
	routes := []router.RouteRow{
		{Name: "summarize", Role: "summarize", Chain: []router.ChainEntry{
			{ProviderID: "p1", Model: "m1"}, {ProviderID: "p2", Model: "m2"},
		}, Enabled: true},
	}
	if decideRoute != nil {
		routes = append(routes, *decideRoute)
	}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "" }, nil)
	return snap
}

func decodeDecide(t *testing.T, body []byte) decideResponse {
	t.Helper()
	var out decideResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return out
}

const choiceBody = `{"kind":"choice","question":"Which kind?","options":["coding","research"]}`

// TestDecideChoiceOverSummarizeFallback: no decide route at all, so
// the summarize role's chain answers through the llm fallback. The
// ledger row carries purpose decide, low effort, and the served route.
func TestDecideChoiceOverSummarizeFallback(t *testing.T) {
	t.Parallel()
	a, rec := newAPI(decideSnapshot(t, oaiOK(t, "research").URL, oaiFail(t).URL, nil))

	w := postJSON(t, a.handleDecide, choiceBody)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	out := decodeDecide(t, w.Body.Bytes())
	if out.Choice != "research" || out.Provider != "one" || out.Model != "m1" || out.LedgerID == "" {
		t.Fatalf("out = %+v", out)
	}
	if out.Score != nil {
		t.Fatalf("score = %v on a choice", *out.Score)
	}

	entries := rec.all()
	if len(entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Status != "ok" || e.Purpose != "decide" || e.Route != "summarize" || e.Effort != "low" || e.ID != out.LedgerID {
		t.Fatalf("entry = %+v", e)
	}
	if e.Usage == nil || e.Usage.InputTokens != 10 || e.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", e.Usage)
	}
	if e.Cost != nil {
		t.Fatalf("cost = %v, want nil for an unpriced model", *e.Cost)
	}
	if e.LatencyMS <= 0 {
		t.Fatalf("latency_ms = %d", e.LatencyMS)
	}
	if got := testutil.ToFloat64(a.providerCalls.WithLabelValues("one", "summarize", "ok")); got != 1 {
		t.Fatalf("provider_calls_total{one,summarize,ok} = %v, want 1", got)
	}
}

func TestDecideYesNoAndScore(t *testing.T) {
	t.Parallel()
	a, _ := newAPI(decideSnapshot(t, oaiOK(t, "No.").URL, oaiFail(t).URL, nil))
	w := postJSON(t, a.handleDecide, `{"kind":"yesno","question":"Is it done?"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("yesno code = %d body=%s", w.Code, w.Body.String())
	}
	if out := decodeDecide(t, w.Body.Bytes()); out.Choice != "no" {
		t.Fatalf("yesno out = %+v", out)
	}

	a, _ = newAPI(decideSnapshot(t, oaiOK(t, "0.8").URL, oaiFail(t).URL, nil))
	w = postJSON(t, a.handleDecide, `{"kind":"score","question":"How relevant?"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("score code = %d body=%s", w.Code, w.Body.String())
	}
	if out := decodeDecide(t, w.Body.Bytes()); out.Score == nil || *out.Score != 0.8 || out.Choice != "" {
		t.Fatalf("score out = %+v", out)
	}
}

// TestDecideRoleRoutePreferred: a chained decide route wins over the
// summarize fallback, and an explicit route wins over both.
func TestDecideRoleRoutePreferred(t *testing.T) {
	t.Parallel()
	decideRoute := &router.RouteRow{Name: "decide", Role: "decide", Capability: "decide",
		Chain: []router.ChainEntry{{ProviderID: "p2", Model: "m2"}}, Enabled: true}
	a, rec := newAPI(decideSnapshot(t, oaiOK(t, "coding").URL, oaiOK(t, "research").URL, decideRoute))

	w := postJSON(t, a.handleDecide, choiceBody)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if out := decodeDecide(t, w.Body.Bytes()); out.Choice != "research" || out.Provider != "two" {
		t.Fatalf("out = %+v, want the decide route's provider", out)
	}
	if e := rec.all()[0]; e.Route != "decide" {
		t.Fatalf("entry route = %q, want decide", e.Route)
	}

	w = postJSON(t, a.handleDecide, `{"route":"summarize","kind":"choice","question":"Which kind?","options":["coding","research"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("explicit route code = %d body=%s", w.Code, w.Body.String())
	}
	if out := decodeDecide(t, w.Body.Bytes()); out.Choice != "coding" || out.Provider != "one" {
		t.Fatalf("explicit route out = %+v", out)
	}
}

// TestDecideEmptyRoleRouteFallsBack: the seeded decide route exists
// but was never chained (fresh install before any bootstrap), so the
// summarize chain answers, byte-identical to the old classify path.
func TestDecideEmptyRoleRouteFallsBack(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		route router.RouteRow
	}{
		{"empty chain", router.RouteRow{Name: "decide", Role: "decide", Capability: "decide", Enabled: true}},
		{"disabled", router.RouteRow{Name: "decide", Role: "decide", Capability: "decide",
			Chain: []router.ChainEntry{{ProviderID: "p2", Model: "m2"}}, Enabled: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, rec := newAPI(decideSnapshot(t, oaiOK(t, "coding").URL, oaiFail(t).URL, &tc.route))
			w := postJSON(t, a.handleDecide, choiceBody)
			if w.Code != http.StatusOK {
				t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
			}
			if out := decodeDecide(t, w.Body.Bytes()); out.Choice != "coding" || out.Provider != "one" {
				t.Fatalf("out = %+v", out)
			}
			if e := rec.all()[0]; e.Route != "summarize" {
				t.Fatalf("entry route = %q, want summarize", e.Route)
			}
		})
	}
}

// TestDecideUnusableRoleRouteDoesNotFallBack: a chained decide route
// whose entries were tried and skipped (here an unbuildable driver)
// is a configuration to fix, so the call errors with the skip reason
// instead of silently answering from the summarize chain.
func TestDecideUnusableRoleRouteDoesNotFallBack(t *testing.T) {
	t.Parallel()
	rows := []router.ProviderRow{
		{ID: "p1", Name: "one", Kind: "api", Driver: "openaicompat", BaseURL: oaiOK(t, "coding").URL, DefaultModel: "m1", Enabled: true},
		{ID: "p2", Name: "broken", Kind: "api", Driver: "nonesuch", DefaultModel: "m2", Enabled: true},
	}
	routes := []router.RouteRow{
		{Name: "summarize", Role: "summarize", Chain: []router.ChainEntry{{ProviderID: "p1", Model: "m1"}}, Enabled: true},
		{Name: "decide", Role: "decide", Capability: "decide", Chain: []router.ChainEntry{{ProviderID: "p2", Model: "m2"}}, Enabled: true},
	}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "" }, nil)
	a, rec := newAPI(snap)

	w := postJSON(t, a.handleDecide, choiceBody)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "broken (unhealthy") {
		t.Fatalf("code = %d body=%s, want no_route naming the skipped entry", w.Code, w.Body.String())
	}
	if len(rec.all()) != 0 {
		t.Fatal("the summarize chain answered despite a configured decide route")
	}
}

// TestDecideMalformedFailsOverThenExhausts: a reply outside the option
// set advances the chain; when every entry misbehaves the call errors
// instead of guessing. Each attempt books one error row.
func TestDecideMalformedFailsOverThenExhausts(t *testing.T) {
	t.Parallel()
	a, rec := newAPI(decideSnapshot(t, oaiOK(t, "I would say coding").URL, oaiOK(t, "research").URL, nil))
	w := postJSON(t, a.handleDecide, choiceBody)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if out := decodeDecide(t, w.Body.Bytes()); out.Choice != "research" || out.Provider != "two" {
		t.Fatalf("out = %+v, want the second provider's answer", out)
	}
	entries := rec.all()
	if len(entries) != 2 || entries[0].Status != "error" || entries[0].ErrorCode != "malformed_reply" || entries[1].Status != "ok" {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Usage == nil {
		t.Fatal("malformed attempt lost its usage")
	}

	a, rec = newAPI(decideSnapshot(t, oaiOK(t, "maybe").URL, oaiFail(t).URL, nil))
	w = postJSON(t, a.handleDecide, choiceBody)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "chain_exhausted") {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "malformed_reply") || !strings.Contains(w.Body.String(), "provider_error") {
		t.Fatalf("body = %s, want both attempt codes", w.Body.String())
	}
	if entries = rec.all(); len(entries) != 2 || entries[1].ErrorCode != "provider_error" {
		t.Fatalf("entries = %+v", entries)
	}
}

// nativeDecider is a decision-model driver: CapDecide without chat.
type nativeDecider struct {
	got provider.DecideRequest
}

func (d *nativeDecider) Name() string        { return "laya" }
func (d *nativeDecider) Kind() provider.Kind { return provider.KindAPI }
func (d *nativeDecider) Capabilities() []provider.Capability {
	return []provider.Capability{provider.CapDecide}
}
func (d *nativeDecider) Stream(context.Context, provider.CompletionRequest) (<-chan stream.StreamEvent, error) {
	panic("a decision model never chats")
}
func (d *nativeDecider) Decide(_ context.Context, model string, req provider.DecideRequest) (provider.DecideResult, *stream.Usage, error) {
	d.got = req
	return provider.DecideResult{Choice: req.Options[0], Scores: map[string]float64{req.Options[0]: 0.9, req.Options[1]: 0.1}},
		&stream.Usage{InputTokens: 3}, nil
}

// TestDecideAttemptNativeDriver: an entry implementing
// provider.Decider is asked directly with the request as validated,
// and carries no low effort tag; a chat entry goes through the llm
// fallback and does.
func TestDecideAttemptNativeDriver(t *testing.T) {
	t.Parallel()
	native := &nativeDecider{}
	req := provider.DecideRequest{Kind: "yesno", Question: "Is it done?", Options: []string{"yes", "no"}}

	res, usage, effort, err := decideAttempt(t.Context(), router.Attempt{Provider: native, ProviderName: "laya", Model: "laya-1"}, req, true)
	if err != nil {
		t.Fatalf("decideAttempt: %v", err)
	}
	if res.Choice != "yes" || res.Scores["yes"] != 0.9 || effort != "" {
		t.Fatalf("res = %+v effort = %q", res, effort)
	}
	if usage == nil || usage.InputTokens != 3 {
		t.Fatalf("usage = %+v", usage)
	}
	if strings.Join(native.got.Options, ",") != "yes,no" {
		t.Fatalf("driver got options %v", native.got.Options)
	}

	snap := decideSnapshot(t, oaiOK(t, "yes").URL, oaiFail(t).URL, nil)
	attempts, err := snap.Resolve("summarize", "", router.Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res, _, effort, err = decideAttempt(t.Context(), attempts[0], req, false)
	if err != nil || res.Choice != "yes" || effort != "low" {
		t.Fatalf("fallback res = %+v effort = %q err = %v", res, effort, err)
	}
}

// TestDecidePurposeOverride: the caller's ledger tag wins over the
// "decide" default, and a decide purpose still books the route.
func TestDecidePurposeOverride(t *testing.T) {
	t.Parallel()
	a, rec := newAPI(decideSnapshot(t, oaiOK(t, "yes").URL, oaiFail(t).URL, nil))
	w := postJSON(t, a.handleDecide, `{"kind":"yesno","question":"Is it done?","purpose":"worker_bail","mission_id":"m-1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	e := rec.all()[0]
	if e.Purpose != "worker_bail" || e.MissionID != "m-1" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestDecideValidation(t *testing.T) {
	t.Parallel()
	a, rec := newAPI(decideSnapshot(t, oaiOK(t, "coding").URL, oaiFail(t).URL, nil))
	for _, body := range []string{
		`{"kind":"choice","question":"q","options":["one"]}`,
		`{"kind":"yesno","question":"q","options":["yes","no"]}`,
		`{"kind":"score","question":"q","options":["a","b"]}`,
		`{"kind":"rank","question":"q"}`,
		`{"kind":"yesno"}`,
		`not json`,
	} {
		if w := postJSON(t, a.handleDecide, body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d, want 400", body, w.Code)
		}
	}
	if len(rec.all()) != 0 {
		t.Fatal("a rejected request reached a provider")
	}
	if w := postJSON(t, a.handleDecide, `{"route":"nope","kind":"yesno","question":"q"}`); w.Code != http.StatusBadGateway {
		t.Fatalf("unknown route: code = %d", w.Code)
	}

	none, _ := newAPI(snapshotFor(t, oaiOK(t, "x").URL, oaiFail(t).URL))
	if w := postJSON(t, none.handleDecide, `{"kind":"yesno","question":"q"}`); w.Code != http.StatusBadGateway {
		t.Fatalf("no decide or summarize role: code = %d body=%s", w.Code, w.Body.String())
	}

	empty := &API{store: &fakeSource{}, ledger: &memRecorder{}, log: discard()}
	if w := postJSON(t, empty.handleDecide, `{"kind":"yesno","question":"q"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no snapshot: code = %d", w.Code)
	}
}

// TestRoleRoutesReportsDecide pins that brain can resolve the decide
// role the same way as the other system roles.
func TestRoleRoutesReportsDecide(t *testing.T) {
	t.Parallel()
	decideRoute := &router.RouteRow{Name: "my-decider", Role: "decide", Capability: "decide", Enabled: false}
	a, _ := newAPI(decideSnapshot(t, oaiFail(t).URL, oaiFail(t).URL, decideRoute))
	w := postJSON(t, a.handleRoleRoutes, "")
	var out struct {
		Roles map[string]string `json:"roles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Roles["decide"] != "my-decider" || out.Roles["summarize"] != "summarize" {
		t.Fatalf("roles = %v", out.Roles)
	}
}

// TestDecideClientCancelRecordsCancelled (D-135): a caller that hangs
// up mid-attempt books one cancelled row and the chain never advances.
func TestDecideClientCancelRecordsCancelled(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	a, rec := newAPI(decideSnapshot(t, oaiSlow(t, release, "coding").URL, oaiOK(t, "research").URL, nil))

	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(choiceBody)).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		a.handleDecide(w, req)
		close(done)
	}()
	cancel()
	<-done
	close(release)

	entries := rec.all()
	if len(entries) != 1 || entries[0].Status != "cancelled" || entries[0].ErrorCode != "client_cancelled" {
		t.Fatalf("entries = %+v, want one cancelled row", entries)
	}
}

func TestDecideLayaProviderNative(t *testing.T) {
	t.Parallel()
	layaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "convaiinnovations/laya-typed-decisions",
			"answers": {
				"q": {
					"type": "choice",
					"choice": "coding",
					"confidence": 0.95,
					"probabilities": {"coding": 0.95, "research": 0.05}
				}
			},
			"usage": {"input_tokens": 12}
		}`))
	}))
	defer layaSrv.Close()

	provRows := []router.ProviderRow{
		{ID: "p-laya", Name: "laya-svc", Kind: "api", Driver: "laya", BaseURL: layaSrv.URL, DefaultModel: "laya-english", Enabled: true},
	}
	routeRows := []router.RouteRow{
		{Name: "decide-route", Role: "decide", Capability: "decide", Chain: []router.ChainEntry{
			{ProviderID: "p-laya", Model: "convaiinnovations/laya-typed-decisions"},
		}, Enabled: true},
	}
	snap, err := router.BuildSnapshot(provRows, routeRows, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	a, rec := newAPI(snap)
	w := postJSON(t, a.handleDecide, choiceBody)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	out := decodeDecide(t, w.Body.Bytes())
	if out.Choice != "coding" || out.Provider != "laya-svc" {
		t.Fatalf("out = %+v", out)
	}
	if out.Scores["coding"] != 0.95 {
		t.Fatalf("out.Scores = %v, want 0.95 for coding", out.Scores)
	}

	entries := rec.all()
	if len(entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(entries))
	}
	if entries[0].Status != "ok" || entries[0].Effort != "" {
		t.Fatalf("ledger entry = %+v, want status=ok and empty effort for native decider", entries[0])
	}
}

func TestDecideLayaFailoverToLLM(t *testing.T) {
	t.Parallel()
	layaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"detail": "laya is down"}`))
	}))
	defer layaSrv.Close()

	chatSrv := oaiOK(t, "research")
	defer chatSrv.Close()

	provRows := []router.ProviderRow{
		{ID: "p-laya", Name: "laya-svc", Kind: "api", Driver: "laya", BaseURL: layaSrv.URL, DefaultModel: "laya-english", Enabled: true},
		{ID: "p-chat", Name: "chat-fallback", Kind: "api", Driver: "openaicompat", BaseURL: chatSrv.URL, DefaultModel: "chat-1", Enabled: true},
	}
	routeRows := []router.RouteRow{
		{Name: "decide-route", Role: "decide", Capability: "decide", Chain: []router.ChainEntry{
			{ProviderID: "p-laya", Model: "convaiinnovations/laya-typed-decisions"},
			{ProviderID: "p-chat", Model: "chat-1"},
		}, Enabled: true},
	}
	snap, err := router.BuildSnapshot(provRows, routeRows, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	a, rec := newAPI(snap)
	w := postJSON(t, a.handleDecide, choiceBody)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	out := decodeDecide(t, w.Body.Bytes())
	if out.Choice != "research" || out.Provider != "chat-fallback" {
		t.Fatalf("out = %+v, want fallback result", out)
	}

	entries := rec.all()
	if len(entries) != 2 {
		t.Fatalf("ledger entries = %d, want 2 (fail then ok)", len(entries))
	}
	if entries[0].Provider != "laya-svc" || entries[0].Status != "error" || entries[0].ErrorCode != "provider_error" {
		t.Fatalf("entry[0] = %+v, want laya error", entries[0])
	}
	if entries[1].Provider != "chat-fallback" || entries[1].Status != "ok" || entries[1].Effort != "low" {
		t.Fatalf("entry[1] = %+v, want chat fallback success with low effort", entries[1])
	}
}

func TestDecideLayaInvalidChoiceFailsOverToLLM(t *testing.T) {
	t.Parallel()
	layaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "convaiinnovations/laya-typed-decisions",
			"answers": {
				"q": {
					"type": "choice",
					"choice": "unallowed_option",
					"confidence": 0.99,
					"probabilities": {"unallowed_option": 0.99}
				}
			},
			"usage": {"input_tokens": 12}
		}`))
	}))
	defer layaSrv.Close()

	chatSrv := oaiOK(t, "research")
	defer chatSrv.Close()

	provRows := []router.ProviderRow{
		{ID: "p-laya", Name: "laya-svc", Kind: "api", Driver: "laya", BaseURL: layaSrv.URL, DefaultModel: "laya-english", Enabled: true},
		{ID: "p-chat", Name: "chat-fallback", Kind: "api", Driver: "openaicompat", BaseURL: chatSrv.URL, DefaultModel: "chat-1", Enabled: true},
	}
	routeRows := []router.RouteRow{
		{Name: "decide-route", Role: "decide", Capability: "decide", Chain: []router.ChainEntry{
			{ProviderID: "p-laya", Model: "convaiinnovations/laya-typed-decisions"},
			{ProviderID: "p-chat", Model: "chat-1"},
		}, Enabled: true},
	}
	snap, err := router.BuildSnapshot(provRows, routeRows, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	a, rec := newAPI(snap)
	w := postJSON(t, a.handleDecide, choiceBody)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	out := decodeDecide(t, w.Body.Bytes())
	if out.Choice != "research" || out.Provider != "chat-fallback" {
		t.Fatalf("out = %+v, want fallback result", out)
	}

	entries := rec.all()
	if len(entries) != 2 {
		t.Fatalf("ledger entries = %d, want 2 (fail then ok)", len(entries))
	}
	if entries[0].Provider != "laya-svc" || entries[0].Status != "error" {
		t.Fatalf("entry[0] = %+v, want laya error for invalid option", entries[0])
	}
	if entries[1].Provider != "chat-fallback" || entries[1].Status != "ok" {
		t.Fatalf("entry[1] = %+v, want fallback success", entries[1])
	}
}

func TestDecideJevNativeSuccessAndPricing(t *testing.T) {
	t.Parallel()
	jevSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jv_live_sec_123" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "jev-latest",
			"answers": {
				"q": {
					"type": "choice",
					"choice": "coding",
					"confidence": 0.98,
					"probabilities": {"coding": 0.98, "research": 0.02}
				}
			},
			"usage": {"input_tokens": 1000, "output_tokens": 0}
		}`))
	}))
	defer jevSrv.Close()

	provRows := []router.ProviderRow{
		{
			ID: "p-jev", Name: "jev-prod", Kind: "api", Driver: "jev",
			BaseURL: jevSrv.URL, DefaultModel: "jev-latest", CredentialRef: "JEV_KEY",
			PricesByModel: map[string]router.ModelPrices{
				"jev-latest": {InputPerMTok: 0.042, OutputPerMTok: 0, Currency: "USD"},
			},
			Enabled: true,
		},
	}
	routeRows := []router.RouteRow{
		{Name: "decide-route", Role: "decide", Capability: "decide", Chain: []router.ChainEntry{
			{ProviderID: "p-jev", Model: "jev-latest"},
		}, Enabled: true},
	}
	snap, err := router.BuildSnapshot(provRows, routeRows, func(ref string) string {
		if ref == "JEV_KEY" {
			return "jv_live_sec_123"
		}
		return ""
	}, nil)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	a, rec := newAPI(snap)
	w := postJSON(t, a.handleDecide, choiceBody)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	out := decodeDecide(t, w.Body.Bytes())
	if out.Choice != "coding" || out.Provider != "jev-prod" {
		t.Fatalf("out = %+v", out)
	}
	if out.Scores["coding"] != 0.98 {
		t.Fatalf("out.Scores = %v, want 0.98", out.Scores)
	}

	entries := rec.all()
	if len(entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Status != "ok" || e.Effort != "" {
		t.Fatalf("ledger entry = %+v, want status=ok and empty effort for native decider", e)
	}
	if e.Usage == nil || e.Usage.InputTokens != 1000 {
		t.Fatalf("e.Usage = %+v, want 1000 input tokens", e.Usage)
	}
	// 1000 tokens at $0.042/Mtok = $0.000042
	if e.Cost == nil || math.Abs(*e.Cost-0.000042) > 1e-8 {
		t.Fatalf("e.Cost = %v, want 0.000042", e.Cost)
	}
	if e.Currency != "USD" {
		t.Fatalf("e.Currency = %q, want USD", e.Currency)
	}
}

func TestDecideJev4xxFailsOverToLLM(t *testing.T) {
	t.Parallel()
	jevSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": "invalid_api_key", "message": "unauthorized"}`))
	}))
	defer jevSrv.Close()

	chatSrv := oaiOK(t, "research")
	defer chatSrv.Close()

	provRows := []router.ProviderRow{
		{ID: "p-jev", Name: "jev-prod", Kind: "api", Driver: "jev", BaseURL: jevSrv.URL, DefaultModel: "jev-latest", Enabled: true},
		{ID: "p-chat", Name: "chat-fallback", Kind: "api", Driver: "openaicompat", BaseURL: chatSrv.URL, DefaultModel: "chat-1", Enabled: true},
	}
	routeRows := []router.RouteRow{
		{Name: "decide-route", Role: "decide", Capability: "decide", Chain: []router.ChainEntry{
			{ProviderID: "p-jev", Model: "jev-latest"},
			{ProviderID: "p-chat", Model: "chat-1"},
		}, Enabled: true},
	}
	snap, err := router.BuildSnapshot(provRows, routeRows, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	a, rec := newAPI(snap)
	w := postJSON(t, a.handleDecide, choiceBody)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	out := decodeDecide(t, w.Body.Bytes())
	if out.Choice != "research" || out.Provider != "chat-fallback" {
		t.Fatalf("out = %+v, want chat-fallback result", out)
	}

	entries := rec.all()
	if len(entries) != 2 {
		t.Fatalf("ledger entries = %d, want 2 (fail then ok)", len(entries))
	}
	if entries[0].Provider != "jev-prod" || entries[0].Status != "error" || entries[0].ErrorCode != "provider_error" {
		t.Fatalf("entry[0] = %+v, want jev provider_error", entries[0])
	}
	// Unpriced provider has Cost == nil (NULL in DB)
	if entries[0].Cost != nil {
		t.Fatalf("unpriced entry[0].Cost = %v, want nil", entries[0].Cost)
	}
	if entries[1].Provider != "chat-fallback" || entries[1].Status != "ok" || entries[1].Effort != "low" {
		t.Fatalf("entry[1] = %+v, want chat fallback success with low effort", entries[1])
	}
}

