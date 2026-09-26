//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/automations"
	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const hookSecret = "itest-hook-signing-key"

// hookHarness is the /hooks route and the automations surface over
// the real stores behind an httptest server, plus the drainer running
// the automations dispatcher.
type hookHarness struct {
	t       *testing.T
	pool    *pgpool.Pool
	store   *automations.Store
	drainer *events.Drainer
	srv     *httptest.Server
	tag     string
	agentID string
}

func newHookHarness(t *testing.T) *hookHarness {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	poolCtx, poolCancel := context.WithCancel(context.Background())
	t.Cleanup(poolCancel)
	pool := pgpool.New(poolCtx, dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	h := &hookHarness{t: t, pool: pool, store: automations.NewStore(pool), tag: fmt.Sprintf("itest-api-hooks-%d-", time.Now().UnixNano())}
	if err := db.QueryRow(t.Context(), `SELECT id FROM agents WHERE is_default LIMIT 1`).Scan(&h.agentID); err != nil {
		t.Fatalf("default agent: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Exec(cctx, `DELETE FROM events WHERE source = 'webhook' AND payload->>'automation_id' IN (SELECT id::text FROM automations WHERE name LIKE $1 || '%')`, h.tag)
		_, _ = db.Exec(cctx, `DELETE FROM notifications WHERE mission_id IS NULL AND message LIKE $1 || '%'`, h.tag)
		_, _ = db.Exec(cctx, `DELETE FROM automations WHERE name LIKE $1 || '%'`, h.tag)
	})
	ev := events.NewStore(pool)
	h.drainer = events.NewDrainer(ev, []events.Consumer{automations.NewDispatcher(nil, log)}, nil, log)
	notifier := missions.NewNotifier(pool, "", nil, log)
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerHooks(m.Handle, h.store, ev, nil, func(_ context.Context, name string) (string, error) {
		if name != "ITEST_HOOK_KEY" {
			return "", fmt.Errorf("unknown secret %q", name)
		}
		return hookSecret, nil
	}, notifier.NotifyOperator)
	a.registerAutomations(m.Handle, h.store, ev, nil, nil, &attachmentResolver{}, nil, nil, nil, nil, nil)
	h.srv = httptest.NewServer(m)
	t.Cleanup(h.srv.Close)
	return h
}

// create inserts an automation with one webhook trigger and returns
// the automation and trigger ids.
func (h *hookHarness) create(name, config string) (automationID, triggerID string) {
	h.t.Helper()
	id, err := h.store.Create(h.t.Context(), automations.Automation{
		Name: h.tag + name, AgentID: h.agentID,
		Action:      automations.Action{Kind: automations.ActionMission, Mission: &missions.MissionTemplate{Goal: "g {{event.body.note}}", Kind: missions.KindGeneral, Light: true}},
		Concurrency: automations.ConcurrencyParallel, MaxConcurrent: 3, MaxRunsPerHour: 60, Enabled: true,
		Triggers: []automations.Trigger{{Kind: automations.TriggerWebhook, Config: json.RawMessage(config), CredentialRef: "ITEST_HOOK_KEY", Enabled: true}},
	})
	if err != nil {
		h.t.Fatalf("create automation: %v", err)
	}
	a, err := h.store.Get(h.t.Context(), id)
	if err != nil {
		h.t.Fatalf("get automation: %v", err)
	}
	return id, a.Triggers[0].ID
}

// post sends body to the trigger with the given headers.
func (h *hookHarness) post(triggerID, body string, header http.Header) (int, map[string]any) {
	h.t.Helper()
	req, _ := http.NewRequestWithContext(h.t.Context(), "POST", h.srv.URL+"/hooks/"+triggerID, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatalf("response %d is not JSON: %s", resp.StatusCode, raw)
	}
	return resp.StatusCode, out
}

func githubHeaders(body, delivery string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-GitHub-Event", "pull_request")
	h.Set("X-GitHub-Delivery", delivery)
	h.Set("X-Hub-Signature-256", "sha256="+hmacHex(hookSecret, body))
	return h
}

func (h *hookHarness) count(sql string, args ...any) int {
	h.t.Helper()
	db, _ := h.pool.Get()
	var n int
	if err := db.QueryRow(h.t.Context(), sql, args...).Scan(&n); err != nil {
		h.t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func (h *hookHarness) webhookEvents(triggerID string) int {
	return h.count(`SELECT count(*) FROM events WHERE source = 'webhook' AND payload->>'trigger_id' = $1`, triggerID)
}

func (h *hookHarness) drain() {
	h.t.Helper()
	for range 10 {
		n, err := h.drainer.Drain(h.t.Context())
		if err != nil {
			h.t.Fatalf("Drain: %v", err)
		}
		if n == 0 {
			return
		}
	}
}

func TestHookGitHubDelivery(t *testing.T) {
	h := newHookHarness(t)
	automationID, triggerID := h.create("gh", `{"scheme":"github","filters":[{"path":"$.action","equals":"opened"}]}`)
	body := `{"action":"opened","note":"` + h.tag + `hello","pull_request":{"number":7}}`
	delivery := h.tag + "delivery-1"

	code, out := h.post(triggerID, body, githubHeaders(body, delivery))
	if code != http.StatusAccepted || out["status"] != "accepted" || out["event_id"] == nil {
		t.Fatalf("valid delivery = %d %v, want 202 accepted with event_id", code, out)
	}
	if n := h.webhookEvents(triggerID); n != 1 {
		t.Fatalf("events rows = %d, want 1", n)
	}
	h.drain()
	runs, err := h.store.ListRuns(t.Context(), automationID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v, %v, want 1", runs, err)
	}
	r := runs[0]
	if r.Status != automations.RunStarting || r.DedupKey != triggerID+"|"+delivery || r.TriggerID == nil || *r.TriggerID != triggerID {
		t.Fatalf("run = %+v, want starting with dedup trigger|delivery", r)
	}
	var runEvent struct {
		Kind     string            `json:"kind"`
		Source   string            `json:"source"`
		Scheme   string            `json:"scheme"`
		Delivery string            `json:"delivery"`
		Headers  map[string]string `json:"headers"`
		Body     map[string]any    `json:"body"`
	}
	if err := json.Unmarshal(r.Event, &runEvent); err != nil {
		t.Fatalf("run event: %v", err)
	}
	if runEvent.Kind != "webhook.received" || runEvent.Source != "webhook" || runEvent.Scheme != "github" || runEvent.Delivery != delivery ||
		runEvent.Headers["x-github-event"] != "pull_request" || runEvent.Body["note"] != h.tag+"hello" {
		t.Fatalf("run event = %+v", runEvent)
	}
	var lastDelivery string
	db, _ := h.pool.Get()
	_ = db.QueryRow(t.Context(), `SELECT coalesce(state->>'last_delivery_at', '') FROM automation_triggers WHERE id = $1`, triggerID).Scan(&lastDelivery)
	if lastDelivery == "" {
		t.Fatal("state.last_delivery_at not stamped")
	}

	// The same delivery id again: 202 duplicate, still one row and run.
	code, out = h.post(triggerID, body, githubHeaders(body, delivery))
	if code != http.StatusAccepted || out["status"] != "duplicate" {
		t.Fatalf("duplicate delivery = %d %v, want 202 duplicate", code, out)
	}
	h.drain()
	if n := h.webhookEvents(triggerID); n != 1 {
		t.Fatalf("events rows after duplicate = %d, want 1", n)
	}
	if runs, _ := h.store.ListRuns(t.Context(), automationID, 10); len(runs) != 1 {
		t.Fatalf("runs after duplicate = %d, want 1", len(runs))
	}

	// A filter miss is acknowledged and writes nothing.
	closed := `{"action":"closed"}`
	code, out = h.post(triggerID, closed, githubHeaders(closed, h.tag+"delivery-2"))
	if code != http.StatusAccepted || out["status"] != "ignored" {
		t.Fatalf("filter miss = %d %v, want 202 ignored", code, out)
	}
	if n := h.webhookEvents(triggerID); n != 1 {
		t.Fatalf("events rows after filter miss = %d, want 1", n)
	}

	// Oversized body: 413 before any signature check.
	big := `{"a":"` + strings.Repeat("x", hookBodyLimit) + `"}`
	code, out = h.post(triggerID, big, githubHeaders(big, h.tag+"delivery-big"))
	if code != http.StatusRequestEntityTooLarge || out["error"] != "payload_too_large" {
		t.Fatalf("oversized body = %d %v, want 413", code, out)
	}
}

// TestHookForgedSignature: a forged delivery answers 401 without echo,
// writes no event and never starts a run.
func TestHookForgedSignature(t *testing.T) {
	h := newHookHarness(t)
	automationID, triggerID := h.create("forged", `{"scheme":"github"}`)
	body := `{"action":"opened","secret":"echo-me"}`
	header := githubHeaders(body, h.tag+"forged-1")
	header.Set("X-Hub-Signature-256", "sha256="+hmacHex("wrong-key", body))
	code, out := h.post(triggerID, body, header)
	if code != http.StatusUnauthorized || len(out) != 1 || out["error"] != "unauthorized" {
		t.Fatalf("forged = %d %v, want 401 {error: unauthorized} only", code, out)
	}
	header.Del("X-Hub-Signature-256")
	if code, _ := h.post(triggerID, body, header); code != http.StatusUnauthorized {
		t.Fatalf("unsigned = %d, want 401", code)
	}
	h.drain()
	if n := h.webhookEvents(triggerID); n != 0 {
		t.Fatalf("events rows after forged deliveries = %d, want 0", n)
	}
	if runs, _ := h.store.ListRuns(t.Context(), automationID, 10); len(runs) != 0 {
		t.Fatalf("runs after forged deliveries = %d, want 0", len(runs))
	}
	if n := h.count(`SELECT jsonb_array_length(state->'auth_failures') FROM automation_triggers WHERE id = $1`, triggerID); n != 2 {
		t.Fatalf("auth_failures = %d, want 2", n)
	}
	// Unknown trigger and bearer token do not change anything: still 404.
	header.Set("Authorization", "Bearer tok")
	if code, out := h.post("0b8f2f4e-6f1c-4b8a-9d2e-3c4b5a6d7e8f", body, header); code != http.StatusNotFound || len(out) != 1 || out["error"] != "not_found" {
		t.Fatalf("unknown trigger = %d %v, want 404 not_found", code, out)
	}
	// Bearer-authenticated routes on the same mux still demand the token.
	resp, err := h.srv.Client().Get(h.srv.URL + "/v1/automations")
	if err != nil {
		t.Fatalf("GET /v1/automations: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /v1/automations without a token = %d, want 401", resp.StatusCode)
	}
}

func TestHookGenericScheme(t *testing.T) {
	h := newHookHarness(t)
	_, triggerID := h.create("generic", `{"scheme":"generic"}`)
	body := `{"note":"hi"}`
	sign := func(ts time.Time) http.Header {
		stamp := strconv.FormatInt(ts.Unix(), 10)
		header := http.Header{}
		header.Set("X-Timothy-Timestamp", stamp)
		header.Set("X-Timothy-Signature", hmacHex(hookSecret, stamp+"."+body))
		header.Set("X-Timothy-Delivery", h.tag+"generic-"+stamp)
		return header
	}
	now := time.Now()
	if code, out := h.post(triggerID, body, sign(now)); code != http.StatusAccepted || out["status"] != "accepted" {
		t.Fatalf("generic delivery = %d %v, want 202", code, out)
	}
	if code, _ := h.post(triggerID, body, sign(now.Add(-10*time.Minute))); code != http.StatusUnauthorized {
		t.Fatalf("stale timestamp = %d, want 401", code)
	}
	// No delivery header: the body hash deduplicates.
	unkeyed := sign(now)
	unkeyed.Del("X-Timothy-Delivery")
	if code, out := h.post(triggerID, body, unkeyed); code != http.StatusAccepted || out["status"] != "accepted" {
		t.Fatalf("hash-keyed delivery = %d %v, want accepted", code, out)
	}
	if code, out := h.post(triggerID, body, unkeyed); code != http.StatusAccepted || out["status"] != "duplicate" {
		t.Fatalf("same body again = %d %v, want duplicate", code, out)
	}
}

// TestHookRateLimit: 30 requests a minute per trigger, the 31st is 429.
func TestHookRateLimit(t *testing.T) {
	h := newHookHarness(t)
	_, triggerID := h.create("rate", `{"scheme":"github","filters":[{"path":"$.action","equals":"never"}]}`)
	body := `{"action":"opened"}`
	for i := range hookBurst {
		code, out := h.post(triggerID, body, githubHeaders(body, fmt.Sprintf("%srate-%d", h.tag, i)))
		if code != http.StatusAccepted || out["status"] != "ignored" {
			t.Fatalf("request %d = %d %v, want 202 ignored", i+1, code, out)
		}
	}
	code, out := h.post(triggerID, body, githubHeaders(body, h.tag+"rate-31"))
	if code != http.StatusTooManyRequests || out["error"] != "rate_limited" {
		t.Fatalf("request 31 = %d %v, want 429", code, out)
	}
}

// TestHookAuthFailuresDisable: 20 forged deliveries in 10 minutes
// disable the trigger, notify the operator with no mission, and the
// trigger then answers 404 even to a valid signature. Re-enabling it
// through PATCH clears the reason.
func TestHookAuthFailuresDisable(t *testing.T) {
	h := newHookHarness(t)
	automationID, triggerID := h.create("guard", `{"scheme":"github"}`)
	body := `{"action":"opened"}`
	forged := githubHeaders(body, h.tag+"guard")
	forged.Set("X-Hub-Signature-256", "sha256="+hmacHex("wrong-key", body))
	for i := range automations.HookAuthFailureLimit {
		if code, _ := h.post(triggerID, body, forged); code != http.StatusUnauthorized {
			t.Fatalf("forged request %d = %d, want 401", i+1, code)
		}
	}
	a, err := h.store.Get(t.Context(), automationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var state struct {
		DisabledReason string   `json:"disabled_reason"`
		AuthFailures   []string `json:"auth_failures"`
	}
	_ = json.Unmarshal(a.Triggers[0].State, &state)
	if a.Triggers[0].Enabled || state.DisabledReason != "auth_failures" || len(state.AuthFailures) != 20 {
		t.Fatalf("trigger after 20 failures: enabled %v state %s", a.Triggers[0].Enabled, a.Triggers[0].State)
	}
	if !a.Enabled {
		t.Fatal("the automation itself was disabled, want only the trigger")
	}
	if n := h.count(`SELECT count(*) FROM notifications WHERE mission_id IS NULL AND kind = 'automation_trigger_disabled' AND message LIKE $1 || '%'`, h.tag+"guard"); n != 1 {
		t.Fatalf("operator notifications = %d, want 1", n)
	}
	valid := githubHeaders(body, h.tag+"guard-valid")
	if code, out := h.post(triggerID, body, valid); code != http.StatusNotFound || out["error"] != "not_found" {
		t.Fatalf("disabled trigger = %d %v, want 404", code, out)
	}

	triggers := []automations.Trigger{{ID: triggerID, Kind: automations.TriggerWebhook, Config: a.Triggers[0].Config, CredentialRef: "ITEST_HOOK_KEY", Enabled: true}}
	if err := h.store.Patch(t.Context(), automationID, automations.Patch{Triggers: &triggers}); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	a, _ = h.store.Get(t.Context(), automationID)
	state.DisabledReason, state.AuthFailures = "", nil
	_ = json.Unmarshal(a.Triggers[0].State, &state)
	if !a.Triggers[0].Enabled || state.DisabledReason != "" || len(state.AuthFailures) != 0 {
		t.Fatalf("re-enabled trigger: enabled %v state %s, want reason and failures cleared", a.Triggers[0].Enabled, a.Triggers[0].State)
	}
	if code, out := h.post(triggerID, body, valid); code != http.StatusAccepted || out["status"] != "accepted" {
		t.Fatalf("re-enabled trigger = %d %v, want 202", code, out)
	}
}

// TestHookDisabledAutomation: a disabled automation hides its trigger.
func TestHookDisabledAutomation(t *testing.T) {
	h := newHookHarness(t)
	automationID, triggerID := h.create("off", `{"scheme":"github"}`)
	off := false
	if err := h.store.Patch(t.Context(), automationID, automations.Patch{Enabled: &off}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	body := `{"action":"opened"}`
	if code, out := h.post(triggerID, body, githubHeaders(body, h.tag+"off")); code != http.StatusNotFound || out["error"] != "not_found" {
		t.Fatalf("disabled automation = %d %v, want 404", code, out)
	}
}

// TestSecretsDirectoryGuardsWebhookTriggerRef runs the credentials
// directory over the real automations store: a webhook trigger's
// credential_ref lists its automation and cannot be deleted.
func TestSecretsDirectoryGuardsWebhookTriggerRef(t *testing.T) {
	h := newHookHarness(t)
	h.create("secret-ref", `{"scheme":"generic"}`)
	gw := &fakeGatewaySecrets{refs: []gwclient.SecretRef{{RefName: "ITEST_HOOK_KEY"}}}
	a, _, _ := testAPI(t, "tok", nil)
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, nil, nil, h.store)

	want := referenceInfo{Kind: "automation", Name: h.tag + "secret-ref", Role: "credential"}
	found := false
	for _, ref := range listSecrets(t, m)["ITEST_HOOK_KEY"] {
		if ref == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("ITEST_HOOK_KEY referents missing %+v", want)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/secrets/ITEST_HOOK_KEY", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), h.tag+"secret-ref") {
		t.Fatalf("delete = %d %s, want 409 naming the automation", w.Code, w.Body)
	}
	if gw.deletedRef != "" {
		t.Fatalf("gateway DeleteSecret called with %q, want never called", gw.deletedRef)
	}
}
