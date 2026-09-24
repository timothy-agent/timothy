package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/automations"
	"github.com/SumonMSelim/timothy/internal/brain/channels"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// fakeDestinationLookup maps destination id to enabled.
type fakeDestinationLookup map[string]bool

func (f fakeDestinationLookup) EnabledByID(_ context.Context, id string) (bool, error) {
	ok, exists := f[id]
	return exists && ok, nil
}

type erroringDestinationLookup struct{}

func (erroringDestinationLookup) EnabledByID(context.Context, string) (bool, error) {
	return false, errors.New("lookup failed")
}

func assertErrorBody(t *testing.T, w *httptest.ResponseRecorder, wantCode, wantInMessage string) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body["error"] != wantCode {
		t.Fatalf("error = %q, want %q (message %q)", body["error"], wantCode, body["message"])
	}
	if !strings.Contains(body["message"], wantInMessage) {
		t.Fatalf("message = %q, want it to mention %q", body["message"], wantInMessage)
	}
}

func TestAutomationsEndpointsUnmountedWhenStoreNil(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerAutomations(m.Handle, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, req := range []struct{ method, path string }{
		{"GET", "/v1/automations"},
		{"POST", "/v1/automations"},
		{"GET", "/v1/automations/stats"},
		{"GET", "/v1/automations/templates"},
		{"GET", "/v1/automations/abc"},
		{"PATCH", "/v1/automations/abc"},
		{"DELETE", "/v1/automations/abc"},
		{"POST", "/v1/automations/abc/run"},
		{"GET", "/v1/automations/abc/runs"},
		{"GET", "/v1/automations/abc/notes"},
		{"PUT", "/v1/automations/abc/notes/n"},
		// Regression: the old surface is gone.
		{"GET", "/v1/schedules"},
	} {
		httpReq := httptest.NewRequest(req.method, req.path, nil)
		httpReq.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httpReq)
		if w.Code != 404 {
			t.Fatalf("%s %s with a nil store = %d, want 404", req.method, req.path, w.Code)
		}
	}
}

const testAgentID = "0b8f2f4e-6f1c-4b8a-9d2e-3c4b5a6d7e8f"

func createBody(t *testing.T, mutate func(map[string]any)) *bytes.Reader {
	t.Helper()
	body := map[string]any{
		"name":     "daily",
		"agent_id": testAgentID,
		"action":   map[string]any{"kind": "mission", "mission": map[string]any{"goal": "g", "kind": "general"}},
		"triggers": []any{map[string]any{"kind": "cron", "config": map[string]any{"expr": "0 9 * * *"}}},
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewReader(raw)
}

// TestAutomationCreateRejectsBeforeStore covers every create rejection
// that runs before the store is touched, so a nil store is fine.
func TestAutomationCreateRejectsBeforeStore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		mutate   func(map[string]any)
		wantCode string
		wantMsg  string
	}{
		{"unknown top-level field", func(b map[string]any) { b["cron"] = "0 9 * * *" }, "bad_request", "unknown field"},
		{"agent_id inside mission", func(b map[string]any) {
			b["action"] = map[string]any{"kind": "mission", "mission": map[string]any{"goal": "g", "kind": "general", "agent_id": testAgentID}}
		}, "bad_request", "unknown field"},
		{"non-uuid agent_id", func(b map[string]any) { b["agent_id"] = "researcher" }, "bad_request", "agent_id"},
		{"workflow action", func(b map[string]any) {
			b["action"] = map[string]any{"kind": "workflow", "workflow_id": testAgentID}
		}, "bad_request", "workflow actions are not available yet"},
		{"missing goal", func(b map[string]any) {
			b["action"] = map[string]any{"kind": "mission", "mission": map[string]any{"kind": "general"}}
		}, "bad_request", "goal is required"},
		{"light coding", func(b map[string]any) {
			b["action"] = map[string]any{"kind": "mission", "mission": map[string]any{"goal": "g", "kind": "coding", "light": true}}
		}, "bad_request", "light is only valid"},
		{"bad cron", func(b map[string]any) {
			b["triggers"] = []any{map[string]any{"kind": "cron", "config": map[string]any{"expr": "nope"}}}
		}, "bad_cron", "invalid cron expression"},
		{"no triggers", func(b map[string]any) { b["triggers"] = []any{} }, "bad_request", "at least one trigger"},
		{"webhook trigger without config", func(b map[string]any) {
			b["triggers"] = []any{map[string]any{"kind": "webhook"}}
		}, "bad_request", "webhook trigger config"},
		{"webhook trigger without credential_ref", func(b map[string]any) {
			b["triggers"] = []any{map[string]any{"kind": "webhook", "config": map[string]any{"scheme": "generic"}}}
		}, "bad_request", "credential_ref"},
		{"channel trigger without config", func(b map[string]any) {
			b["triggers"] = []any{map[string]any{"kind": "channel"}}
		}, "bad_request", "channel trigger config"},
		{"channel trigger without channels", func(b map[string]any) {
			b["triggers"] = []any{map[string]any{"kind": "channel", "config": map[string]any{"channel_id": testAgentID, "pattern": "^/run"}}}
		}, "bad_request", "channels are not enabled"},
		{"max_concurrent without parallel", func(b map[string]any) { b["max_concurrent"] = 2 }, "bad_request", "parallel"},
		{"unknown destination", func(b map[string]any) {
			b["action"] = map[string]any{"kind": "mission", "mission": map[string]any{"goal": "g", "kind": "general", "destination_ids": []string{"d1", "unknown"}}}
		}, "bad_request", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &automationAPI{destinations: fakeDestinationLookup{"d1": true}}
			w := httptest.NewRecorder()
			h.create(w, httptest.NewRequest("POST", "/v1/automations", createBody(t, tc.mutate)))
			if w.Code != 400 {
				t.Fatalf("create = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
			assertErrorBody(t, w, tc.wantCode, tc.wantMsg)
		})
	}
}

// TestAutomationPatchRejectsBeforeStore: a present action is validated
// before the store is read; a nil store is fine.
func TestAutomationPatchRejectsBeforeStore(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body, wantMsg string }{
		{"invalid action", `{"action":{"kind":"mission","mission":{"kind":"general"}}}`, "goal is required"},
		{"workflow action", `{"action":{"kind":"workflow"}}`, "not available yet"},
		{"unknown field", `{"cron":"0 9 * * *"}`, "unknown field"},
		{"bad expires_at", `{"expires_at":"tomorrow"}`, "expires_at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &automationAPI{}
			req := httptest.NewRequest("PATCH", "/v1/automations/abc", strings.NewReader(tc.body))
			req.SetPathValue("id", "abc")
			w := httptest.NewRecorder()
			h.patch(w, req)
			if w.Code != 400 {
				t.Fatalf("patch = %d, want 400", w.Code)
			}
			assertErrorBody(t, w, "bad_request", tc.wantMsg)
		})
	}
}

func TestDecodeExpiresAt(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	var req patchAutomationRequest
	for _, tc := range []struct {
		name      string
		body      string
		wantSet   bool
		wantClear bool
	}{
		{"omitted leaves unchanged", `{}`, false, false},
		{"null clears", `{"expires_at": null}`, true, true},
		{"timestamp sets", `{"expires_at": "2026-12-01T09:00:00Z"}`, true, false},
	} {
		req = patchAutomationRequest{}
		if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.name, err)
		}
		got, err := decodeExpiresAt(req.ExpiresAt)
		if err != nil {
			t.Fatalf("%s: decodeExpiresAt: %v", tc.name, err)
		}
		if (got != nil) != tc.wantSet {
			t.Fatalf("%s: set = %v, want %v", tc.name, got != nil, tc.wantSet)
		}
		if got == nil {
			continue
		}
		if (*got == nil) != tc.wantClear {
			t.Fatalf("%s: clear = %v, want %v", tc.name, *got == nil, tc.wantClear)
		}
		if !tc.wantClear && !(*got).Equal(ts) {
			t.Fatalf("%s: value = %v, want %v", tc.name, **got, ts)
		}
	}
}

func TestParseRunsLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"", 50, true}, {"1", 1, true}, {"200", 200, true}, {"0", 0, false}, {"201", 0, false}, {"x", 0, false},
	} {
		got, err := parseRunsLimit(tc.in)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Fatalf("parseRunsLimit(%q) = %d, %v; want %d ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestAutomationValidateDestinationIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if err := (&automationAPI{}).validateDestinationIDs(ctx, nil); err != nil {
		t.Fatalf("empty ids with destinations disabled = %v, want nil", err)
	}
	if err := (&automationAPI{}).validateDestinationIDs(ctx, []string{"d1"}); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("nil destinations = %v, want not enabled", err)
	}
	h := &automationAPI{destinations: fakeDestinationLookup{"d1": true, "d2": false}}
	if err := h.validateDestinationIDs(ctx, []string{"d1"}); err != nil {
		t.Fatalf("enabled id = %v, want nil", err)
	}
	err := h.validateDestinationIDs(ctx, []string{"d1", "d2", "d3"})
	var ve *automations.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "d2, d3") {
		t.Fatalf("disabled/unknown ids = %v, want a validation error naming d2, d3", err)
	}
	err = (&automationAPI{destinations: erroringDestinationLookup{}}).validateDestinationIDs(ctx, []string{"d1"})
	if err == nil || errors.As(err, &ve) {
		t.Fatalf("lookup failure = %v, want a non-validation error", err)
	}
}

// TestResolveTemplateAttachments covers reuse-vs-convert (issue #359).
func TestResolveTemplateAttachments(t *testing.T) {
	t.Parallel()
	t.Run("new id converted", func(t *testing.T) {
		t.Parallel()
		fa := &fakeMissionAttachments{byID: map[string]attachments.Attachment{"a1": {ID: "a1", Mime: "text/plain"}}, data: map[string][]byte{"a1": []byte("hello")}}
		h := &automationAPI{attachments: &attachmentResolver{store: fa}}
		out, err := h.resolveTemplateAttachments(t.Context(), []missions.SourceEntry{{ID: "a1", Name: "notes.txt"}}, nil)
		if err != nil || len(out) != 1 || out[0].Markdown != "hello" {
			t.Fatalf("out = %+v, err = %v, want one converted entry", out, err)
		}
	})
	t.Run("existing id with markdown reused", func(t *testing.T) {
		t.Parallel()
		h := &automationAPI{attachments: &attachmentResolver{store: &fakeMissionAttachments{}}}
		existing := []missions.SourceEntry{{Source: "pdf", ID: "a1", Mime: "text/plain", Markdown: "already converted"}}
		out, err := h.resolveTemplateAttachments(t.Context(), []missions.SourceEntry{{ID: "a1"}}, existing)
		if err != nil || len(out) != 1 || out[0].Markdown != "already converted" {
			t.Fatalf("out = %+v, err = %v, want the existing entry reused", out, err)
		}
	})
	t.Run("id missing from want is dropped", func(t *testing.T) {
		t.Parallel()
		h := &automationAPI{attachments: &attachmentResolver{store: &fakeMissionAttachments{}}}
		existing := []missions.SourceEntry{{Source: "pdf", ID: "a1", Markdown: "c1"}, {Source: "pdf", ID: "a2", Markdown: "c2"}}
		out, err := h.resolveTemplateAttachments(t.Context(), []missions.SourceEntry{{ID: "a2"}}, existing)
		if err != nil || len(out) != 1 || out[0].ID != "a2" {
			t.Fatalf("out = %+v, err = %v, want only a2", out, err)
		}
	})
	t.Run("cap exceeded", func(t *testing.T) {
		t.Parallel()
		h := &automationAPI{attachments: &attachmentResolver{store: &fakeMissionAttachments{}}}
		_, err := h.resolveTemplateAttachments(t.Context(), make([]missions.SourceEntry, maxMissionAttachments+1), nil)
		if err == nil || !strings.Contains(err.Error(), "too many attachments") {
			t.Fatalf("over cap = %v, want too-many-attachments", err)
		}
	})
}

func TestStripActionAttachmentMarkdown(t *testing.T) {
	t.Parallel()
	tmpl := &missions.MissionTemplate{Attachments: []missions.SourceEntry{{ID: "a1", Mime: "text/plain", Name: "notes.txt", Markdown: "secret body"}}}
	a := automations.Automation{Action: automations.Action{Kind: "mission", Mission: tmpl}}
	stripped := stripActionAttachmentMarkdown(a)
	if stripped.Action.Mission.Attachments[0].Markdown != "" || stripped.Action.Mission.Attachments[0].ID != "a1" {
		t.Fatalf("stripped = %+v, want markdown cleared and id kept", stripped.Action.Mission.Attachments[0])
	}
	if tmpl.Attachments[0].Markdown != "secret body" {
		t.Fatal("strip mutated the stored template")
	}
}

func TestAutomationTemplatesMissing(t *testing.T) {
	t.Parallel()
	kinds := func(k ...string) kindLister {
		return func(context.Context) ([]string, error) { return k, nil }
	}
	for _, tc := range []struct {
		name         string
		conns, dests kindLister
		want         map[string][]automations.Requirement
	}{
		{"nothing configured", nil, nil, map[string][]automations.Requirement{
			"daily-repo-digest":   {{Kind: "connector", Value: "github"}, {Kind: "destination", Value: "email"}},
			"pr-review-comment":   {{Kind: "connector", Value: "github"}},
			"weekly-kb-freshness": {},
			"inbox-triage":        {{Kind: "connector", Value: "mail"}},
			"coverage-watch":      {{Kind: "connector", Value: "github"}},
		}},
		{"github and imap only", kinds("github", "imap"), kinds(), map[string][]automations.Requirement{
			"daily-repo-digest":   {{Kind: "destination", Value: "email"}},
			"pr-review-comment":   {},
			"weekly-kb-freshness": {},
			"inbox-triage":        {},
			"coverage-watch":      {},
		}},
		{"all configured", kinds("github", "google"), kinds("email"), map[string][]automations.Requirement{
			"daily-repo-digest":   {},
			"pr-review-comment":   {},
			"weekly-kb-freshness": {},
			"inbox-triage":        {},
			"coverage-watch":      {},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &automationAPI{connectorKinds: tc.conns, destinationKinds: tc.dests}
			w := httptest.NewRecorder()
			h.templates(w, httptest.NewRequest("GET", "/v1/automations/templates", nil))
			if w.Code != 200 {
				t.Fatalf("templates = %d %s", w.Code, w.Body.String())
			}
			var body struct {
				Templates []struct {
					ID       string                    `json:"id"`
					Requires []automations.Requirement `json:"requires"`
					Missing  []automations.Requirement `json:"missing"`
				} `json:"templates"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(body.Templates) != len(tc.want) {
				t.Fatalf("got %d templates, want %d", len(body.Templates), len(tc.want))
			}
			for _, tpl := range body.Templates {
				want, ok := tc.want[tpl.ID]
				if !ok {
					t.Fatalf("unexpected template %q", tpl.ID)
				}
				if tpl.Missing == nil || tpl.Requires == nil {
					t.Fatalf("%s: requires/missing must encode as arrays, got %s", tpl.ID, w.Body.String())
				}
				if len(tpl.Missing) != len(want) {
					t.Fatalf("%s missing = %+v, want %+v", tpl.ID, tpl.Missing, want)
				}
				for i := range want {
					if tpl.Missing[i] != want[i] {
						t.Fatalf("%s missing = %+v, want %+v", tpl.ID, tpl.Missing, want)
					}
				}
			}
		})
	}
}

func TestAutomationTemplatesLookupError(t *testing.T) {
	t.Parallel()
	failing := func(context.Context) ([]string, error) { return nil, errors.New("db down") }
	for _, h := range []*automationAPI{{connectorKinds: failing}, {destinationKinds: failing}} {
		w := httptest.NewRecorder()
		h.templates(w, httptest.NewRequest("GET", "/v1/automations/templates", nil))
		if w.Code != 500 {
			t.Fatalf("templates with a failing lookup = %d, want 500", w.Code)
		}
		assertErrorBody(t, w, "automations_failed", "db down")
	}
}

func TestValidateTriggerConnectors(t *testing.T) {
	type row struct {
		kind    string
		enabled bool
	}
	rows := map[string]row{"gh": {"github", true}, "gh-off": {"github", false}, "bb": {"bitbucket", true}}
	lookup := connectorLookup(func(_ context.Context, id string) (string, bool, error) {
		switch id {
		case "boom":
			return "", false, errors.New("db down")
		}
		r, ok := rows[id]
		if !ok {
			return "", false, connectors.ErrNotFound
		}
		return r.kind, r.enabled, nil
	})
	trigger := func(id string) []automations.Trigger {
		return []automations.Trigger{{Kind: automations.TriggerConnectorEvent, Config: json.RawMessage(`{"connector_id":"` + id + `","repo":"o/r","events":["pr.opened"]}`)}}
	}
	h := &automationAPI{connectors: lookup}
	if err := h.validateTriggerConnectors(t.Context(), trigger("gh")); err != nil {
		t.Fatalf("enabled github: %v", err)
	}
	if err := h.validateTriggerConnectors(t.Context(), []automations.Trigger{{Kind: automations.TriggerManual}}); err != nil {
		t.Fatalf("no connector_event trigger: %v", err)
	}
	for _, id := range []string{"gh-off", "bb", "missing"} {
		var ve *automations.ValidationError
		if err := h.validateTriggerConnectors(t.Context(), trigger(id)); !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want a ValidationError", id, err)
		}
	}
	var ve *automations.ValidationError
	if err := h.validateTriggerConnectors(t.Context(), trigger("boom")); err == nil || errors.As(err, &ve) {
		t.Errorf("lookup failure: err = %v, want a plain error", err)
	}
	if err := (&automationAPI{}).validateTriggerConnectors(t.Context(), trigger("gh")); !errors.As(err, &ve) {
		t.Errorf("no lookup: err = %v, want a ValidationError", err)
	}
}

func TestValidateTriggerChannels(t *testing.T) {
	const on, off, gone, boom = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444"
	lookup := channelLookup(func(_ context.Context, id string) (bool, error) {
		switch id {
		case on:
			return true, nil
		case off:
			return false, nil
		case boom:
			return false, errors.New("db down")
		}
		return false, channels.ErrNotFound
	})
	trigger := func(id string) []automations.Trigger {
		return []automations.Trigger{{Kind: automations.TriggerChannel, Config: json.RawMessage(`{"channel_id":"` + id + `","pattern":"^/run"}`)}}
	}
	h := &automationAPI{channels: lookup}
	if err := h.validateTriggerConnectors(t.Context(), trigger(on)); err != nil {
		t.Fatalf("enabled channel: %v", err)
	}
	if err := (&automationAPI{}).validateTriggerConnectors(t.Context(), []automations.Trigger{{Kind: automations.TriggerManual}}); err != nil {
		t.Fatalf("no channel trigger: %v", err)
	}
	for _, id := range []string{off, gone} {
		var ve *automations.ValidationError
		if err := h.validateTriggerConnectors(t.Context(), trigger(id)); !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want a ValidationError", id, err)
		}
	}
	var ve *automations.ValidationError
	if err := h.validateTriggerConnectors(t.Context(), trigger(boom)); err == nil || errors.As(err, &ve) {
		t.Errorf("lookup failure: err = %v, want a plain error", err)
	}
	if err := (&automationAPI{}).validateTriggerConnectors(t.Context(), trigger(on)); !errors.As(err, &ve) {
		t.Errorf("no lookup: err = %v, want a ValidationError", err)
	}
}
