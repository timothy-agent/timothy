package api

import (
	"context"
	"reflect"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/workflows"
)

// TestCreatePathGolden feeds one input through the API, scheduler and
// workflow builders and asserts ResolveDefaults resolves all three to
// the same mission once the caller-owned fields (schedule/workflow
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
	}{
		{"coding with agent", missions.KindCoding, "a1", false},
		{"coding without agent", missions.KindCoding, "", false},
		{"general light", missions.KindGeneral, "a1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			apiReq := createMissionRequest{Goal: goal, Kind: tc.kind, AgentID: tc.agent, PlanRoute: "strong", Light: tc.light, DestinationIDs: []string{"d1"}}.createRequest("", nil)
			schedReq := missions.TemplateCreateRequest(missions.Schedule{ID: "s1", Name: "nightly", MissionTemplate: missions.MissionTemplate{
				Goal: goal, Kind: tc.kind, AgentID: tc.agent, PlanRoute: "strong", Light: tc.light, DestinationIDs: []string{"d1"},
			}}, []string{"d1"})
			stepReq := workflows.StepCreateRequest(workflows.Step{Goal: goal, Kind: tc.kind, AgentID: tc.agent, PlanRoute: "strong", Light: tc.light, DestinationIDs: []string{"d1"}}, goal, "run-1", "build", "", "")

			var got []missions.Mission
			for _, req := range []missions.CreateRequest{apiReq, schedReq, stepReq} {
				m, err := missions.ResolveDefaults(context.Background(), req, deps)
				if err != nil {
					t.Fatalf("ResolveDefaults: %v", err)
				}
				m.Name, m.ScheduleID, m.WorkflowRunID, m.WorkflowStep = "", "", "", ""
				m.AutoApprovePlan, m.AutoApproveTools = false, false
				m.Sources, m.ParentMissionID = nil, ""
				got = append(got, m)
			}
			if !reflect.DeepEqual(got[0], got[1]) {
				t.Fatalf("scheduler resolved differently:\napi   %+v\nsched %+v", got[0], got[1])
			}
			if !reflect.DeepEqual(got[0], got[2]) {
				t.Fatalf("workflow resolved differently:\napi  %+v\nstep %+v", got[0], got[2])
			}
			if got[0].ReviewRoute != "strong" || got[0].MaxIterations != 6 || got[0].BudgetCurrency != "USD" {
				t.Fatalf("resolved = %+v, want review_route from plan_route, max 6, USD", got[0])
			}
		})
	}
}
