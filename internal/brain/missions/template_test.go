package missions

import (
	"context"
	"strings"
	"testing"
)

func TestTemplateCreateRequestResolvesAutomation(t *testing.T) {
	t.Parallel()
	req := TemplateCreateRequest(MissionTemplate{Goal: "g", Kind: KindGeneral}, "digest", "a1", nil, "r1")
	if req.OriginKind != OriginAutomation || req.AutomationRunID != "r1" || req.AgentID != "a1" {
		t.Fatalf("req = %+v, want automation origin, run r1, agent a1", req)
	}
	m, err := ResolveDefaults(context.Background(), req, ResolveDeps{})
	if err != nil {
		t.Fatalf("ResolveDefaults: %v", err)
	}
	if m.OriginKind != OriginAutomation || !m.Unattended || m.AutomationRunID != "r1" || m.PermissionTimeoutSeconds == nil || *m.PermissionTimeoutSeconds != 1800 {
		t.Fatalf("origin=%q unattended=%v run=%q timeout=%v, want automation true r1 1800", m.OriginKind, m.Unattended, m.AutomationRunID, m.PermissionTimeoutSeconds)
	}
}

func TestTemplateCreateRequestCarriesTemplateFields(t *testing.T) {
	t.Parallel()
	tmpl := MissionTemplate{Goal: "g", Kind: KindGeneral, AutoApproveTools: false}
	req := TemplateCreateRequest(tmpl, "inbox-digest", "a1", []string{"d1"}, "r1")
	if req.Name != "inbox-digest" || req.AutoApprovePlan == nil || !*req.AutoApprovePlan {
		t.Fatalf("req = %+v, want automation name, auto_approve_plan true", req)
	}
	if req.AutoApproveTools == nil || *req.AutoApproveTools {
		t.Fatal("AutoApproveTools must carry the template's explicit false")
	}
	if len(req.Destinations) != 1 || req.Destinations[0].DestinationID != "d1" {
		t.Fatalf("Destinations = %+v, want d1", req.Destinations)
	}
	tmpl.Name = "Today's Meetings"
	if got := TemplateCreateRequest(tmpl, "inbox-digest", "a1", nil, "").Name; got != "Today's Meetings" {
		t.Fatalf("Name = %q, want the template's own name", got)
	}
}

func TestTemplateCreateRequestLightMapsToLightFlow(t *testing.T) {
	t.Parallel()
	deps := ResolveDeps{RouteForRole: func(context.Context, string) string { return "default" }}
	got, err := ResolveDefaults(context.Background(), TemplateCreateRequest(MissionTemplate{Goal: "g", Kind: "general", Light: true}, "", "", nil, ""), deps)
	if err != nil {
		t.Fatalf("ResolveDefaults: %v", err)
	}
	if got.Flow != FlowLight {
		t.Fatalf("Flow = %q, want %q", got.Flow, FlowLight)
	}
}

// TestTemplateCreateRequestResolvesDefaults runs a template through
// TemplateCreateRequest and ResolveDefaults, the path every run takes.
func TestTemplateCreateRequestResolvesDefaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		template      MissionTemplate
		agentID       string
		resolve       AgentResolver
		routeExists   func(context.Context, string) bool
		codingExec    func(context.Context) string
		wantRoute     string
		wantReview    string
		wantPlanRoute string
		wantOverlay   string
		wantHarness   string
	}{
		{name: "nil resolver falls back to the default role's route", template: MissionTemplate{Goal: "g"}, agentID: "a1", wantRoute: "default", wantReview: "default"},
		{name: "coding template with no harness applies the settings default", template: MissionTemplate{Goal: "g", Kind: "coding"}, agentID: "a1",
			codingExec: func(context.Context) string { return "claude-cli" }, wantRoute: "default", wantReview: "default", wantHarness: "claude-cli"},
		{name: "coding template's own harness is never overwritten", template: MissionTemplate{Goal: "g", Kind: "coding", Harness: "claude-cli"}, agentID: "a1",
			codingExec: func(context.Context) string { return "" }, wantRoute: "default", wantReview: "default", wantHarness: "claude-cli"},
		{name: "general template never applies the coding executor default", template: MissionTemplate{Goal: "g", Kind: "general"}, agentID: "a1",
			codingExec: func(context.Context) string { return "claude-cli" }, wantRoute: "default", wantReview: "default"},
		{name: "resolved agent's harness applies ahead of the settings default", template: MissionTemplate{Goal: "g", Kind: "coding"}, agentID: "coder",
			resolve: func(context.Context, string) (AgentDefaults, bool) {
				return AgentDefaults{Route: "coding", Harness: "pi"}, true
			},
			codingExec: func(context.Context) string { return "claude-cli" }, wantRoute: "coding", wantReview: "default", wantHarness: "pi"},
		{name: "unresolved agent id falls back to the default role's route", template: MissionTemplate{Goal: "g"}, agentID: "missing",
			resolve:   func(context.Context, string) (AgentDefaults, bool) { return AgentDefaults{}, false },
			wantRoute: "default", wantReview: "default"},
		{name: "empty template fields fill from resolved agent", template: MissionTemplate{Goal: "g"}, agentID: "briefing",
			resolve: func(context.Context, string) (AgentDefaults, bool) {
				return AgentDefaults{Route: "fast", ReviewRoute: "careful", PromptOverlay: "overlay text"}, true
			},
			wantRoute: "fast", wantReview: "careful", wantOverlay: "overlay text"},
		{name: "template's own non-empty fields are never overwritten", template: MissionTemplate{Goal: "g", Route: "explicit", ReviewRoute: "explicit-review"}, agentID: "briefing",
			resolve: func(context.Context, string) (AgentDefaults, bool) {
				return AgentDefaults{Route: "fast", ReviewRoute: "careful", PromptOverlay: "overlay text"}, true
			},
			wantRoute: "explicit", wantReview: "explicit-review", wantOverlay: "overlay text"},
		{name: "coding template with no route prefers the coding route when it exists", template: MissionTemplate{Goal: "g", Kind: "coding"}, agentID: "a1",
			routeExists: func(context.Context, string) bool { return true }, wantRoute: "coding", wantReview: "default"},
		{name: "coding template with no route falls back to default when coding route is absent", template: MissionTemplate{Goal: "g", Kind: "coding"}, agentID: "a1",
			routeExists: func(context.Context, string) bool { return false }, wantRoute: "default", wantReview: "default"},
		{name: "coding template's own route is never overwritten by the coding preference", template: MissionTemplate{Goal: "g", Kind: "coding", Route: "explicit"}, agentID: "a1",
			routeExists: func(context.Context, string) bool { return true }, wantRoute: "explicit", wantReview: "default"},
		{name: "plan_route passes through untouched", template: MissionTemplate{Goal: "g", PlanRoute: "strong"}, agentID: "briefing",
			resolve: func(context.Context, string) (AgentDefaults, bool) {
				return AgentDefaults{Route: "fast", ReviewRoute: "careful"}, true
			},
			wantRoute: "fast", wantReview: "careful", wantPlanRoute: "strong"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			routeExists := tc.routeExists
			if tc.template.Kind != "coding" {
				routeExists = func(context.Context, string) bool {
					t.Fatal("routeExists must not be called for a non-coding template")
					return false
				}
			}
			deps := ResolveDeps{Agent: tc.resolve, RouteForRole: func(context.Context, string) string { return "default" }, RouteExists: routeExists, CodingExecutorDefault: tc.codingExec}
			got, err := ResolveDefaults(context.Background(), TemplateCreateRequest(tc.template, "", tc.agentID, nil, ""), deps)
			if err != nil {
				t.Fatalf("ResolveDefaults: %v", err)
			}
			if got.Route != tc.wantRoute || got.ReviewRoute != tc.wantReview || got.PlanRoute != tc.wantPlanRoute {
				t.Errorf("routes = %q/%q/%q, want %q/%q/%q", got.Route, got.ReviewRoute, got.PlanRoute, tc.wantRoute, tc.wantReview, tc.wantPlanRoute)
			}
			if got.PromptOverlay != tc.wantOverlay {
				t.Errorf("overlay = %q, want %q", got.PromptOverlay, tc.wantOverlay)
			}
			if got.Harness != tc.wantHarness {
				t.Errorf("Harness = %q, want %q", got.Harness, tc.wantHarness)
			}
		})
	}
}

func TestValidateTemplate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		t       MissionTemplate
		wantErr string
	}{
		{"valid general", MissionTemplate{Goal: "g", Kind: KindGeneral}, ""},
		{"valid light general", MissionTemplate{Goal: "g", Kind: KindGeneral, Light: true}, ""},
		{"valid coding", MissionTemplate{Goal: "g", Kind: KindCoding}, ""},
		{"missing goal", MissionTemplate{Kind: KindGeneral}, "goal is required"},
		{"blank goal", MissionTemplate{Goal: "  ", Kind: KindGeneral}, "goal is required"},
		{"empty kind", MissionTemplate{Goal: "g"}, "kind must be"},
		{"bogus kind", MissionTemplate{Goal: "g", Kind: "bogus"}, "kind must be"},
		{"light coding", MissionTemplate{Goal: "g", Kind: KindCoding, Light: true}, "light is only valid for kind=general"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateTemplate(tc.t)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateTemplate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}
