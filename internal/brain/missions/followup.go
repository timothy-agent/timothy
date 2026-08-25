package missions

import (
	"context"
	"fmt"
)

// CreateFollowUp spawns a new mission continuing a terminal parent —
// the driver-layer counterpart of api/missions.go's create handler's
// own ParentMissionID branch, reused by builtin.FollowupMission so a
// chat-triggered follow-up can never diverge in behavior from one
// created through the mission-create API.
func (d *Driver) CreateFollowUp(ctx context.Context, parentID, goal string) (string, error) {
	parent, err := d.store.Get(ctx, parentID)
	if err != nil {
		return "", fmt.Errorf("create follow-up: parent mission %s not found: %w", parentID, err)
	}
	if !parent.Phase.Terminal() {
		return "", fmt.Errorf("create follow-up: parent mission %s is not finished (phase %s)", parentID, parent.Phase)
	}
	events, err := d.store.Events(ctx, parentID)
	if err != nil {
		return "", fmt.Errorf("create follow-up: read parent events: %w", err)
	}
	parentContext := OutcomeDigest(parent, events, parent.Phase, parent.FailureReason)

	child := Mission{
		Goal: goal, Kind: parent.Kind, AgentID: parent.AgentID,
		Route: parent.Route, ReviewRoute: parent.ReviewRoute, PlanRoute: parent.PlanRoute,
		EscalationRoute: parent.EscalationRoute, MaxIterations: parent.MaxIterations,
		BudgetAmount: parent.BudgetAmount, BudgetCurrency: parent.BudgetCurrency,
		AutoApproveSafe: parent.AutoApproveSafe, PromptOverlay: parent.PromptOverlay,
		Knowledge: parent.Knowledge, Harness: parent.Harness, Environment: parent.Environment,
		RepoURL: parent.RepoURL, ConnectorID: parent.ConnectorID,
		BranchPattern: parent.BranchPattern, CommitStyle: parent.CommitStyle,
		Light: parent.Light, ParentMissionID: parent.ID, ParentContext: parentContext,
		// Deliberately NOT copied from parent: OnComplete (push consent is
		// a per-mission human choice), DestinationIDs (D-061, operator
		// addresses outputs per mission), Attachments (a follow-up's own
		// documents, not the parent's).
	}
	id, err := d.Create(ctx, child)
	if err != nil {
		return "", fmt.Errorf("create follow-up: %w", err)
	}
	// Fire-and-forget display name generation, same shape as
	// api/missions.go's create handler's own generateName — detached
	// from ctx so a caller winding down doesn't cancel it.
	if d.nameMission != nil {
		go d.backfillMissionName(context.Background(), id, goal) //nolint:gosec // G118: deliberate — the naming call must outlive whatever request/ctx triggered create
	}
	return id, nil
}
