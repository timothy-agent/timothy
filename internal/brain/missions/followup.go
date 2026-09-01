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
	parentSource := SourceEntry{Source: SourceKindMission, ID: ParentLineageID, MissionID: parent.ID, Digest: OutcomeDigest(parent, events, parent.Phase, parent.FailureReason)}
	var sources []SourceEntry
	sources = append(sources, parentSource)
	if github, ok := parent.GitHubSource(); ok {
		sources = append(sources, github)
	}

	child := Mission{
		Goal: goal, Kind: parent.Kind, AgentID: parent.AgentID,
		Route: parent.Route, ReviewRoute: parent.ReviewRoute, PlanRoute: parent.PlanRoute,
		EscalationRoute: parent.EscalationRoute, MaxIterations: parent.MaxIterations,
		// Pins name an entry inside a route, so they carry over with the
		// routes above; dropping them would silently demote a follow-up
		// to the chain's first usable entry.
		RouteModel: parent.RouteModel, PlanRouteModel: parent.PlanRouteModel,
		ReviewRouteModel: parent.ReviewRouteModel,
		BudgetAmount:     parent.BudgetAmount, BudgetCurrency: parent.BudgetCurrency,
		AutoApproveSafe: parent.AutoApproveSafe, AutoApprovePlan: parent.AutoApprovePlan, PromptOverlay: parent.PromptOverlay,
		Knowledge: parent.Knowledge, Harness: parent.Harness, Environment: parent.Environment,
		Flow: parent.Flow, ParentMissionID: parent.ID, Sources: sources,
		// Deliberately NOT copied from parent: Destinations (push consent,
		// destination_ids, and kb promotion are per-mission human choices,
		// D-061, operator addresses outputs per mission), pdf sources (a
		// follow-up's own documents, not the parent's).
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
