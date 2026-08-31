package missions

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// askUserToolName is the operator-question tool a phase turn may call
// at most askBudget times per mission (D-088, issue #457).
const askUserToolName = "ask_user"

// askBudget caps how many ask_user calls one mission may spend,
// enforced by the harness (Execute below), never the prompt: a
// mission asking for every ambiguity would defeat #446's "assume and
// declare" default. Scheduler/workflow missions get budget 0 instead
// (see askBudgetFor): nobody is watching to answer.
const askBudget = 2

// askUserKinds enumerates ask_user's valid kind values.
var askUserKinds = map[string]bool{"mcq": true, "yes_no": true, "open": true}

// askUserArgs is ask_user's tool-call payload.
type askUserArgs struct {
	Question        string   `json:"question"`
	Kind            string   `json:"kind"`
	Options         []string `json:"options,omitempty"`
	ProposedDefault string   `json:"proposed_default"`
}

// askBudgetFor is the ask_user budget for one mission: 0 for a
// scheduler-fired or workflow-spawned mission (same signal
// nativeRunner already uses for Unattended: nobody is watching to
// answer a parked question), askBudget otherwise.
func askBudgetFor(m Mission) int {
	if m.ScheduleID != "" || m.WorkflowRunID != "" {
		return 0
	}
	return askBudget
}

// validateAskUser checks args against kind-specific rules, returning a
// message suitable for a plain tool-call error (never a park) when
// invalid.
func validateAskUser(a askUserArgs) error {
	if a.Question == "" {
		return fmt.Errorf("question is required")
	}
	if !askUserKinds[a.Kind] {
		return fmt.Errorf(`kind must be "mcq", "yes_no", or "open"`)
	}
	if a.ProposedDefault == "" {
		return fmt.Errorf("proposed_default is required")
	}
	switch a.Kind {
	case "mcq":
		if len(a.Options) < 2 || len(a.Options) > 6 {
			return fmt.Errorf("mcq requires 2-6 options")
		}
		found := false
		for _, opt := range a.Options {
			if opt == a.ProposedDefault {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("proposed_default must be one of options")
		}
	case "yes_no":
		if a.ProposedDefault != "yes" && a.ProposedDefault != "no" {
			return fmt.Errorf(`proposed_default must be "yes" or "no" for yes_no`)
		}
		if len(a.Options) > 0 {
			return fmt.Errorf("options must be empty for yes_no")
		}
	case "open":
		if len(a.Options) > 0 {
			return fmt.Errorf("options must be empty for open")
		}
	}
	return nil
}

// askUserPark records a park, notifies the inbox, and reports whether
// the park was recorded: the caller (Execute) ends the turn only when
// it was. Backed by *Store in production (parkAskUser below), faked in
// tests: kept separate from parkNotifier (runner.go) since ask_user's
// park is a different jsonb column with its own budget check, not the
// permission broker's.
type askUserParker interface {
	ParkAskUser(ctx context.Context, missionID string, input PendingInput) error
}

// AskUserTool defines the operator-question sentinel a phase turn may
// call: registered per-turn via loop.Request.ExtraTools AND
// loop.Request.EndTurnTools (D-075's end-turn pattern): successful
// execution parks the mission and ends the turn immediately, the same
// mechanism mission_status/review_verdict/submit_plan/explore_notes
// already use. missionID/phase/asksUsed are the turn's own values,
// closed over at construction (one tool built per turn, same as
// kbSearchTool); parker records the durable park.
func AskUserTool(missionID string, phase Phase, asksUsed int, budget int, parker askUserParker) *tools.Tool {
	return &tools.Tool{
		Name: askUserToolName,
		Description: fmt.Sprintf("Ask the operator one structured question when a genuinely blocking ambiguity needs a human decision, not for anything you can reasonably assume and declare instead. Ends your turn; the mission parks until answered or the timeout applies your proposed_default. Budget: %d per mission (%d used so far).", budget, asksUsed),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"question": {"type": "string", "description": "The exact question to ask."},
				"kind": {"type": "string", "enum": ["mcq", "yes_no", "open"], "description": "mcq: pick one of options. yes_no: yes or no. open: free text."},
				"options": {"type": "array", "items": {"type": "string"}, "description": "Required for mcq: 2-6 choices."},
				"proposed_default": {"type": "string", "description": "Required: what you'd choose if nobody answers in time. For mcq, must be one of options. For yes_no, \"yes\" or \"no\"."}
			},
			"required": ["question", "kind", "proposed_default"]
		}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			if asksUsed >= budget {
				return "", fmt.Errorf("ask_user budget exhausted (%d/%d used): proceed on your own best judgment instead", asksUsed, budget)
			}
			var a askUserArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid ask_user arguments: %w", err)
			}
			if err := validateAskUser(a); err != nil {
				return "", err
			}
			input := PendingInput{
				Question: a.Question, Kind: a.Kind, Options: a.Options,
				ProposedDefault: a.ProposedDefault, Phase: phase,
			}
			if err := parker.ParkAskUser(ctx, missionID, input); err != nil {
				return "", fmt.Errorf("recording the question failed: %w", err)
			}
			return "question recorded, mission parked awaiting the operator's answer", nil
		},
	}
}
