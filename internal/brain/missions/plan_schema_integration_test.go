//go:build integration

package missions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// TestPlanSessionUnknownFieldIsToolErrorAndTurnContinues covers issue
// #844 end to end over the real loop.Agent (no mocked runTurn): a
// submit_plan call with an unknown field is a tool error the model
// sees and can fix WITHIN the same turn (D-075: an errored end-turn
// tool never ends the turn), so a planner that resubmits a valid plan
// right after completes in one PlanSession call, no recovery turn.
func TestPlanSessionUnknownFieldIsToolErrorAndTurnContinues(t *testing.T) {
	bad := `{"units":[{"title":"Write it","artifacts":["out.md"],"check_cmd":"grep -q x out.md","criteria":["a","b"],"acceptance_criteria":["a","b"]}]}`
	valid := `{"units":[{"title":"Write it","artifacts":["out.md"],"check_cmd":"grep -q x out.md","criteria":["a","b"]}]}`
	gw := &originGateway{scripts: [][]stream.StreamEvent{
		originToolStep(planToolName, bad),
		originToolStep(planToolName, valid),
	}}
	exec := &gatedExec{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	agent := loop.NewAgent(gw, exec, askGatedPerms{}, originOutputs{}, originAudit{}, originEvents{}, loop.NewPermBroker(), nil, log)
	agent.SetBaseTools(exec, []provider.ToolDef{{Name: "gated", Description: "gated", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	r := &nativeRunner{agent: agent, log: log}

	plan, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Goal: "fix bug"}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if len(plan.Units) != 1 || plan.Units[0].Title != "Write it" {
		t.Fatalf("plan = %+v", plan)
	}
	if len(gw.requests) != 2 {
		t.Fatalf("gateway requests = %d, want 2 (one turn, no recovery)", len(gw.requests))
	}

	var feedback string
	var isError bool
	for _, msg := range gw.requests[1].Messages {
		if msg.ToolResult != nil {
			feedback = msg.ToolResult.Content
			isError = msg.ToolResult.IsError
		}
	}
	if !isError {
		t.Fatalf("second request's tool result IsError = false, want true")
	}
	// Content may be JSON-escaped by the fence/wrap layer, so match on
	// the field name and phrase, not the exact quoting.
	if !strings.Contains(feedback, "acceptance_criteria") {
		t.Fatalf("tool error feedback = %q, want it to name acceptance_criteria", feedback)
	}
	if !strings.Contains(feedback, "allowed unit fields") {
		t.Fatalf("tool error feedback = %q, want it to name the allowed unit fields", feedback)
	}
}
