package api

import (
	"context"
	"reflect"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/workflows"
)

// TestCreatePathGolden feeds one input through the API, automation and
// workflow builders and asserts ResolveDefaults resolves all three to
// the same mission once the caller-owned fields (automation run/workflow
// ids, name, lineage, forced approvals) are set aside (issue #816).
func TestCreatePathGolden(t *testing.T) {
	t.Parallel()
	deps := missions.ResolveDeps{
		Agent: func(_ context.Context, id string) (missions.AgentDefaults, bool) {
			return missions.AgentDefaults{Route: "agent-route", PromptOverlay: "overlay", Harness: "codex-cli"}, id == "a1"
		},
		RouteForRole:          func(context.Context, string) string { return "default" },
		RouteExists:           func(context.Context, string) bool { return true },
		CodingExecutorDefault: func(context.Context) string { return "claude-cli" },
		DefaultMaxIterations:  func(context.Context) int { return 6 },
	}
	const goal = "ship the shortener"
	for _, tc := range []struct {
		name  string
		kind  string
		agent string
		light bool
		// route/planRoute are the input's routes; reviewRoute is sent
		// only by the API and automations (a step has no review_route).
		route, planRoute, reviewRoute string
		wantReview                    string
	}{
		{"coding with agent", missions.KindCoding, "a1", false, "", "strong", "", "strong"},
		{"coding without agent", missions.KindCoding, "", false, "", "strong", "", "strong"},
		{"general light", missions.KindGeneral, "a1", true, "", "strong", "", "strong"},
		{"route without plan_route", missions.KindGeneral, "a1", false, "fast", "", "fast", "fast"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			apiReq := createMissionRequest{Goal: goal, Kind: tc.kind, AgentID: tc.agent, Route: tc.route, ReviewRoute: tc.reviewRoute, PlanRoute: tc.planRoute, Light: tc.light, DestinationIDs: []string{"d1"}}.createRequest("", nil)
			autoReq := missions.TemplateCreateRequest(missions.MissionTemplate{
				Goal: goal, Kind: tc.kind, Route: tc.route, ReviewRoute: tc.reviewRoute, PlanRoute: tc.planRoute, Light: tc.light, DestinationIDs: []string{"d1"},
			}, "nightly", tc.agent, []string{"d1"}, "r1")
			stepReq := workflows.StepCreateRequest(workflows.Step{Goal: goal, Kind: tc.kind, AgentID: tc.agent, Route: tc.route, PlanRoute: tc.planRoute, Light: tc.light, DestinationIDs: []string{"d1"}}, goal, "run-1", "build", "", "")

			// A follow-up's request is built from the API caller's own
			// resolved mission, kept unmasked as the parent: proves a
			// follow-up resolves to the same mission a fresh API create
			// with the same input would, once its own Destinations
			// (deliberately never carried) are set aside.
			parent, err := missions.ResolveDefaults(context.Background(), apiReq, deps)
			if err != nil {
				t.Fatalf("ResolveDefaults parent: %v", err)
			}
			fuReq := missions.FollowUpCreateRequest(parent, goal, nil)

			var got []missions.Mission
			wantOrigin := []string{missions.OriginAPI, missions.OriginAutomation, missions.OriginWorkflow, missions.OriginFollowup}
			for i, req := range []missions.CreateRequest{apiReq, autoReq, stepReq, fuReq} {
				m, err := missions.ResolveDefaults(context.Background(), req, deps)
				if err != nil {
					t.Fatalf("ResolveDefaults: %v", err)
				}
				// Origin, unattended and the unattended permission
				// timeout differ by caller by design (issue #817). A
				// follow-up is attended, same as the API.
				unattended := i == 1 || i == 2
				if m.OriginKind != wantOrigin[i] || m.Unattended != unattended || (m.PermissionTimeoutSeconds != nil) != unattended {
					t.Fatalf("caller %d: origin=%q unattended=%v timeout=%v, want %q %v", i, m.OriginKind, m.Unattended, m.PermissionTimeoutSeconds, wantOrigin[i], unattended)
				}
				if i == 3 && len(m.Destinations) != 0 {
					t.Fatalf("follow-up Destinations = %+v, want none: it's a per-mission choice", m.Destinations)
				}
				m.OriginKind, m.Unattended, m.PermissionTimeoutSeconds = "", false, nil
				m.Name, m.AutomationRunID, m.WorkflowRunID, m.WorkflowStep = "", "", "", ""
				m.AutoApprovePlan, m.AutoApproveTools = false, false
				m.Sources, m.ParentMissionID = nil, ""
				got = append(got, m)
			}
			if !reflect.DeepEqual(got[0], got[1]) {
				t.Fatalf("automation resolved differently:\napi  %+v\nauto %+v", got[0], got[1])
			}
			if !reflect.DeepEqual(got[0], got[2]) {
				t.Fatalf("workflow resolved differently:\napi  %+v\nstep %+v", got[0], got[2])
			}
			// The follow-up never carries Destinations, unlike the other
			// three callers here; set aside before the shape comparison.
			got[3].Destinations = got[0].Destinations
			if !reflect.DeepEqual(got[0], got[3]) {
				t.Fatalf("follow-up resolved differently:\napi %+v\nfu  %+v", got[0], got[3])
			}
			if got[0].ReviewRoute != tc.wantReview || got[0].MaxIterations != 6 || got[0].BudgetCurrency != "USD" {
				t.Fatalf("resolved = %+v, want review_route %q, max 6, USD", got[0], tc.wantReview)
			}
		})
	}
}
