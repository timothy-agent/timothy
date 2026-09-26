package workflows

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// engineStore is the narrow slice of *Store the engine needs — kept as
// an interface so engine_test.go can fake it without a real Postgres
// pool, same reasoning as missions.driverStore.
type engineStore interface {
	Get(ctx context.Context, id string) (Workflow, error)
	CreateRun(ctx context.Context, workflowID, currentStep string, runContext map[string]string) (string, error)
	GetRun(ctx context.Context, id string) (Run, error)
	ApplyRunTransition(ctx context.Context, id string, t RunTransition) error
	CountEdgeFirings(ctx context.Context, runID, from, on, to string) (int, error)
	RunEvents(ctx context.Context, runID string) ([]RunEvent, error)
}

// missionSpawner is the narrow slice of *missions.Driver the engine
// needs to spawn a step's mission — an interface so engine_test.go can
// fake it without a real Driver. workflows importing missions.Mission
// directly (not vice versa) is what keeps this acyclic: missions never
// imports workflows, the engine hears about terminal missions as an
// events consumer (D-117).
type missionSpawner interface {
	Create(ctx context.Context, m missions.Mission) (string, error)
}

// missionEvents is the narrow slice of *missions.Store the engine needs
// to load a terminal mission, assemble its OutcomeDigest for the next
// step's goal interpolation, and find a step mission an earlier attempt
// already spawned; *missions.Store satisfies it.
type missionEvents interface {
	Get(ctx context.Context, id string) (missions.Mission, error)
	Events(ctx context.Context, id string) ([]missions.Event, error)
	WorkflowChild(ctx context.Context, runID, parentMissionID string) (string, error)
}

// Engine reacts to mission terminal events and drives workflow runs
// forward — the orchestration layer above missions (D-070). It never
// mutates mission state; it only reads terminal missions and creates
// new ones via spawner.
type Engine struct {
	store   engineStore
	spawner missionSpawner
	events  missionEvents
	log     *slog.Logger

	// resolve is the shared create-path lookups (missions.ResolveDefaults)
	// a step's mission resolves through, same as the API.
	// The zero value skips every lookup; an empty step route then stays
	// empty and ValidateCreate (D-071) rejects it.
	resolve missions.ResolveDeps
}

func NewEngine(store engineStore, spawner missionSpawner, events missionEvents, log *slog.Logger) *Engine {
	return &Engine{store: store, spawner: spawner, events: events, log: log}
}

// SetResolveDeps wires the create-path lookups spawnStep resolves a
// step's mission through (agent defaults, harness precedence, default
// and coding routes, the D-100 route gate).
func (e *Engine) SetResolveDeps(deps missions.ResolveDeps) {
	e.resolve = deps
}

// StartRun creates a new run for workflowID and spawns the entry step's
// mission. runContext seeds {{context.KEY}} interpolation for every
// step in this run. An entry mission already linked to the run is
// adopted, never created twice. Nothing re-drives StartRun for an
// existing run today, so this only keeps the entry spawn safe to retry.
func (e *Engine) StartRun(ctx context.Context, workflowID string, runContext map[string]string) (string, error) {
	wf, err := e.store.Get(ctx, workflowID)
	if err != nil {
		return "", fmt.Errorf("workflows start run: %w", err)
	}
	if !wf.Enabled {
		return "", fmt.Errorf("workflow %s is disabled", workflowID)
	}
	def, err := ParseDefinition(wf.Definition)
	if err != nil {
		return "", fmt.Errorf("workflows start run: %w", err)
	}
	runID, err := e.store.CreateRun(ctx, workflowID, def.Entry, runContext)
	if err != nil {
		return "", fmt.Errorf("workflows start run: %w", err)
	}
	entryID, err := e.events.WorkflowChild(ctx, runID, "")
	if err != nil {
		return runID, fmt.Errorf("workflows start run: look up entry mission: %w", err)
	}
	if entryID != "" {
		e.log.Info("workflows: adopting existing entry mission", "run_id", runID, "mission_id", entryID)
		return runID, nil
	}
	if _, err := e.spawnStep(ctx, runID, def.Entry, def.Entry, def.Steps[def.Entry], runContext, "", ""); err != nil {
		e.pauseRun(ctx, runID, fmt.Sprintf("spawn entry step %q failed: %s", def.Entry, err.Error()))
		return runID, fmt.Errorf("workflows start run: spawn entry step: %w", err)
	}
	return runID, nil
}

// OnMissionTerminal reacts to mission m reaching a terminal phase: load
// its run, find the edge from its step matching the outcome, and either
// spawn the next step, end the run, pause it, or fail it on cap
// exceeded. m.WorkflowRunID must be non-empty (Handle filters for
// that). Every decision appends a run event; the engine never mutates
// the mission itself. Idempotent for redelivery: a run no longer
// running, on another step, or that already took an edge for m is left
// alone. The next step's mission is adopt-or-create: a mission the run
// already spawned from m (a crash or failed record between spawn and
// edge.taken) is adopted, never created twice. Errors are load failures
// or a failed edge.taken record, both safe to retry.
func (e *Engine) OnMissionTerminal(ctx context.Context, m missions.Mission) error {
	run, err := e.store.GetRun(ctx, m.WorkflowRunID)
	if err != nil {
		return fmt.Errorf("workflows: terminal mission %s: load run %s: %w", m.ID, m.WorkflowRunID, err)
	}
	if run.Status != "running" {
		// A run already paused/done/failed/cancelled ignores a late
		// terminal event from a step mission that raced past it (e.g. a
		// second edge firing while the run was already being cancelled).
		return nil
	}
	if m.WorkflowStep != "" && m.WorkflowStep != run.CurrentStep {
		return nil
	}
	runEvents, err := e.store.RunEvents(ctx, m.WorkflowRunID)
	if err != nil {
		return fmt.Errorf("workflows: terminal mission %s: load run events: %w", m.ID, err)
	}
	if edgeTakenFor(runEvents, m.ID) {
		return nil
	}
	wf, err := e.store.Get(ctx, run.WorkflowID)
	if err != nil {
		return fmt.Errorf("workflows: terminal mission %s: load workflow %s: %w", m.ID, run.WorkflowID, err)
	}
	def, err := ParseDefinition(wf.Definition)
	if err != nil {
		return fmt.Errorf("workflows: terminal mission %s: parse definition of %s: %w", m.ID, run.WorkflowID, err)
	}

	on := "mission.done"
	if m.Phase == missions.PhaseFailed {
		on = "mission.failed"
	}

	// D-070: match the edge leaving the run's current step on this
	// mission's outcome kind — the sole decision point for where a run
	// goes next.
	var matched *Edge
	for i := range def.Edges {
		if def.Edges[i].From == run.CurrentStep && def.Edges[i].On == on {
			matched = &def.Edges[i]
			break
		}
	}
	if matched == nil {
		// mission.failed with no matching edge: pause per the plan's
		// failure semantics (slice 6 adds notifications). mission.done
		// with no matching edge is equally a dead end the author likely
		// forgot to wire — same pause treatment.
		e.pauseRun(ctx, m.WorkflowRunID, fmt.Sprintf("no matching edge for step %q on %s", run.CurrentStep, on))
		return nil
	}

	firings, err := e.store.CountEdgeFirings(ctx, m.WorkflowRunID, matched.From, matched.On, matched.To)
	if err != nil {
		return fmt.Errorf("workflows: terminal mission %s: count edge firings: %w", m.ID, err)
	}
	if firings >= matched.MaxIterations {
		e.failRun(ctx, m.WorkflowRunID, matched)
		return nil
	}

	if matched.To == endStep {
		e.endRun(ctx, m.WorkflowRunID, matched)
		return nil
	}

	step, ok := def.Steps[matched.To]
	if !ok {
		e.pauseRun(ctx, m.WorkflowRunID, fmt.Sprintf("edge targets unknown step %q", matched.To))
		return nil
	}
	childID, err := e.events.WorkflowChild(ctx, m.WorkflowRunID, m.ID)
	if err != nil {
		return fmt.Errorf("workflows: terminal mission %s: look up spawned mission: %w", m.ID, err)
	}
	if childID != "" {
		e.log.Info("workflows: adopting already spawned step mission", "run_id", m.WorkflowRunID, "mission_id", childID, "parent_mission_id", m.ID)
	} else {
		events, err := e.events.Events(ctx, m.ID)
		if err != nil {
			e.pauseRun(ctx, m.WorkflowRunID, fmt.Sprintf("load mission %s events failed: %s", m.ID, err.Error()))
			return nil
		}
		outcome := missions.OutcomeDigest(m, events, m.Phase, m.FailureReason)
		childID, err = e.spawnStep(ctx, m.WorkflowRunID, run.CurrentStep, matched.To, step, run.Context, outcome, m.ID)
		if err != nil {
			e.pauseRun(ctx, m.WorkflowRunID, fmt.Sprintf("spawn step %q failed: %s", matched.To, err.Error()))
			return nil
		}
	}
	if err := e.store.ApplyRunTransition(ctx, m.WorkflowRunID, RunTransition{
		Status: "running", CurrentStep: matched.To,
		Events: []RunTransitionEvent{{Kind: "edge.taken", Payload: map[string]any{
			"from": matched.From, "on": matched.On, "to": matched.To, "mission_id": m.ID, "spawned_mission_id": childID,
		}}},
	}); err != nil {
		// Returned so the drainer retries; the retry adopts childID.
		return fmt.Errorf("workflows: terminal mission %s: record edge to %q: %w", m.ID, matched.To, err)
	}
	return nil
}

// spawnStep creates the next mission for step, interpolating its goal
// from outcome + run context, and appends a run.warning event for any
// unknown {{context.KEY}} placeholder before creating the mission.
// currentStep is the run's step now; the warning keeps it so the run
// only advances after Create succeeds.
func (e *Engine) spawnStep(ctx context.Context, runID, currentStep, stepName string, step Step, runContext map[string]string, outcome, parentMissionID string) (string, error) {
	goal, unknown := interpolate(step.Goal, outcome, runContext)
	for _, key := range unknown {
		if err := e.store.ApplyRunTransition(ctx, runID, RunTransition{
			Status: "running", CurrentStep: currentStep,
			Events: []RunTransitionEvent{{Kind: "run.warning", Payload: map[string]any{
				"message": fmt.Sprintf("unknown placeholder {{context.%s}} rendered empty", key), "step": stepName,
			}}},
		}); err != nil {
			e.log.Warn("workflows: spawn step: record unknown placeholder warning failed", "run_id", runID, "key", key, "error", err)
		}
	}
	m, err := missions.ResolveDefaults(ctx, StepCreateRequest(step, goal, runID, stepName, outcome, parentMissionID), e.resolve)
	if err != nil {
		return "", err
	}
	return e.spawner.Create(ctx, m)
}

// StepCreateRequest maps a workflow step onto the shared
// missions.CreateRequest. goal is the interpolated step goal; outcome
// becomes the parent-lineage source (issue #481). The mission always
// auto-approves its plan (D-087) and keeps tool approval off, as
// workflow missions always have.
func StepCreateRequest(step Step, goal, runID, stepName, outcome, parentMissionID string) missions.CreateRequest {
	// A step wanting a push names a github destination row's id here.
	var destinations []missions.DestinationEntry
	for _, id := range step.DestinationIDs {
		destinations = append(destinations, missions.DestinationEntry{DestinationID: id})
	}
	autoApprovePlan, autoApproveTools := true, false
	// Prove stays on the step route unless plan_route covers it.
	reviewRoute := ""
	if step.PlanRoute == "" {
		reviewRoute = step.Route
	}
	return missions.CreateRequest{
		Goal: goal, Kind: step.Kind, Light: step.Light, Route: step.Route, ReviewRoute: reviewRoute, PlanRoute: step.PlanRoute, AgentID: step.AgentID,
		Destinations:    destinations,
		ParentMissionID: parentMissionID,
		Sources:         []missions.SourceEntry{{Source: missions.SourceKindMission, ID: missions.ParentLineageID, MissionID: parentMissionID, Digest: outcome}},
		WorkflowRunID:   runID, WorkflowStep: stepName,
		AutoApprovePlan: &autoApprovePlan, AutoApproveTools: &autoApproveTools,
		OriginKind: missions.OriginWorkflow,
	}
}

func (e *Engine) pauseRun(ctx context.Context, runID, reason string) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		e.log.Warn("workflows: pause run: reload failed", "run_id", runID, "error", err)
		return
	}
	if err := e.store.ApplyRunTransition(ctx, runID, RunTransition{
		Status: "paused", CurrentStep: run.CurrentStep,
		Events: []RunTransitionEvent{{Kind: "run.paused", Payload: map[string]any{"reason": reason}}},
	}); err != nil {
		e.log.Warn("workflows: pause run failed", "run_id", runID, "error", err)
	}
}

func (e *Engine) failRun(ctx context.Context, runID string, edge *Edge) {
	if err := e.store.ApplyRunTransition(ctx, runID, RunTransition{
		Status: "failed", CurrentStep: edge.From,
		Events: []RunTransitionEvent{{Kind: "run.cap_exceeded", Payload: map[string]any{
			"from": edge.From, "on": edge.On, "to": edge.To, "max_iterations": edge.MaxIterations,
		}}},
	}); err != nil {
		e.log.Warn("workflows: fail run (cap exceeded) failed", "run_id", runID, "error", err)
	}
}

func (e *Engine) endRun(ctx context.Context, runID string, edge *Edge) {
	if err := e.store.ApplyRunTransition(ctx, runID, RunTransition{
		Status: "done", CurrentStep: endStep,
		Events: []RunTransitionEvent{{Kind: "run.done", Payload: map[string]any{"from": edge.From, "on": edge.On}}},
	}); err != nil {
		e.log.Warn("workflows: end run failed", "run_id", runID, "error", err)
	}
}
