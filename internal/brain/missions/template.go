package missions

import (
	"context"
	"errors"
	"strings"
)

// MissionTemplate is an automation's mission action: the initial
// columns of every mission its runs start. The automation owns the
// agent, so the template carries no agent id.
type MissionTemplate struct {
	Goal string `json:"goal"`
	// Name, when set, becomes the started mission's display name
	// instead of the automation's own name.
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind"`
	Route       string `json:"route"`
	ReviewRoute string `json:"review_route"`
	// PlanRoute, when set, is the route discover/plan/replan/prove run
	// on instead of Route. "" means Route covers everything.
	PlanRoute      string   `json:"plan_route,omitempty"`
	MaxIterations  int      `json:"max_iterations"`
	BudgetAmount   *float64 `json:"budget_amount,omitempty"`
	BudgetCurrency string   `json:"budget_currency,omitempty"`
	// Harness selects a coding mission's worker harness (D-051); empty
	// resolves through ResolveDefaults.
	Harness string `json:"harness,omitempty"`
	// ReviewHarness (issue #582) is copied onto the mission as-is.
	ReviewHarness string `json:"review_harness,omitempty"`
	// Light missions (D-069) skip discover/plan/prove; kind=general only.
	Light bool `json:"light,omitempty"`
	// Environment selects a coding mission's sandbox image key.
	Environment string `json:"environment,omitempty"`
	// AutoApproveTools is copied onto the mission; unattended runs
	// need the standing shell approval a UI-created mission gets.
	AutoApproveTools bool `json:"auto_approve_tools"`
	// DestinationIDs names operator-created destinations (D-061) the
	// mission delivers its outcome digest to.
	DestinationIDs []string `json:"destination_ids,omitempty"`
	// Attachments are converted once at automation create/patch time
	// (issue #359) and copied onto every mission's sources.
	Attachments []SourceEntry `json:"attachments,omitempty"`
}

// AgentDefaults is the slice of an agents row a new mission borrows
// when its create request leaves the corresponding field empty
// (ResolveDefaults), and the provisioning-time grants the driver reads.
type AgentDefaults struct {
	// Name labels a mission's turns in the timeline (issue #473).
	Name              string
	Route             string
	ReviewRoute       string
	PromptOverlay     string
	ApprovalAllowlist []string
	// Harness is the agent's own harness field; empty means the agent
	// does not override.
	Harness string
	// Tools is the agent's tool allowlist, the ceiling for a trigger's
	// tool_allowlist (issue #857).
	Tools []string
}

// AgentResolver resolves an agent id to its current defaults; ok
// reports whether the id resolved to a real agent.
type AgentResolver func(ctx context.Context, agentID string) (AgentDefaults, bool)

// TemplateCreateRequest maps an automation's mission template onto a
// CreateRequest. The mission is named after the template, else name,
// runs as agentID, links automationRunID and always auto-approves its
// plan (D-087): nobody is watching an automation run.
func TemplateCreateRequest(t MissionTemplate, name, agentID string, destinationIDs []string, automationRunID string) CreateRequest {
	if t.Name != "" {
		name = t.Name
	}
	var destinations []DestinationEntry
	for _, id := range destinationIDs {
		destinations = append(destinations, DestinationEntry{DestinationID: id})
	}
	autoApproveTools := t.AutoApproveTools
	autoApprovePlan := true
	return CreateRequest{
		Goal: t.Goal, Name: name, Kind: t.Kind, AgentID: agentID,
		Route: t.Route, ReviewRoute: t.ReviewRoute, PlanRoute: t.PlanRoute,
		MaxIterations: t.MaxIterations, BudgetAmount: t.BudgetAmount, BudgetCurrency: t.BudgetCurrency,
		AutoApproveTools: &autoApproveTools, AutoApprovePlan: &autoApprovePlan,
		Harness: t.Harness, ReviewHarness: t.ReviewHarness, Environment: t.Environment,
		Light:           t.Light,
		Sources:         t.Attachments,
		Destinations:    destinations,
		AutomationRunID: automationRunID,
		OriginKind:      OriginAutomation,
	}
}

// ValidateTemplate rejects a template no run could turn into a valid
// mission: an empty goal, a kind other than coding or general, or light
// on a non-general kind.
func ValidateTemplate(t MissionTemplate) error {
	if strings.TrimSpace(t.Goal) == "" {
		return errors.New("mission.goal is required")
	}
	if t.Kind != KindCoding && t.Kind != KindGeneral {
		return errors.New(`mission.kind must be "coding" or "general"`)
	}
	if t.Light && t.Kind != KindGeneral {
		return errors.New("mission.light is only valid for kind=general")
	}
	return nil
}
