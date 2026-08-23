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
}

// missionSpawner is the narrow slice of *missions.Driver the engine
// needs to spawn a step's mission — an interface so engine_test.go can
// fake it without a real Driver. workflows importing missions.Mission
// directly (not vice versa) is what keeps this acyclic: missions never
// imports workflows, it only calls back into it through the
// missions.OnTerminal func type wired by cmd/brain/main.go.
type missionSpawner interface {
	Create(ctx context.Context, m missions.Mission) (string, error)
}

// missionEvents is the narrow slice of *missions.Store the engine needs
// to assemble a terminal mission's OutcomeDigest for the next step's
// goal interpolation — *missions.Store satisfies it (Driver has no
// Events passthrough of its own).
type missionEvents interface {
	Events(ctx context.Context, id string) ([]missions.Event, error)
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

	// routeForRole resolves a step's default route when it omits one
	// (see SetRouteForRole / spawnStep) — the same seam missions'
	// scheduler and api/missions.go's create handler resolve "default"
	// through, so a workflow step behaves like any other mission create
	// that doesn't name a route. nil-gated: unset leaves an empty
	// step.Route empty, which Driver.Create's ValidateCreate (D-071)
	// then rejects.
	routeForRole func(ctx context.Context, role string) string
}

func NewEngine(store engineStore, spawner missionSpawner, events missionEvents, log *slog.Logger) *Engine {
	return &Engine{store: store, spawner: spawner, events: events, log: log}
}

// SetRouteForRole wires the default-route resolver spawnStep uses for a
// step that omits its own route — a setter for the same reason
// missions.Driver's other optional deps are: cmd/brain/main.go builds
// the gateway route resolver alongside the Engine itself.
func (e *Engine) SetRouteForRole(fn func(ctx context.Context, role string) string) {
	e.routeForRole = fn
}

// StartRun creates a new run for workflowID and spawns the entry step's
// mission. runContext seeds {{context.KEY}} interpolation for every
// step in this run.
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
	if _, err := e.spawnStep(ctx, runID, def.Entry, def.Steps[def.Entry], runContext, "", ""); err != nil {
		e.pauseRun(ctx, runID, fmt.Sprintf("spawn entry step %q failed: %s", def.Entry, err.Error()))
		return runID, fmt.Errorf("workflows start run: spawn entry step: %w", err)
	}
	return runID, nil
}

// OnMissionTerminal reacts to mission m reaching a terminal phase: load
// its run, find the edge from its step matching the outcome, and either
// spawn the next step, end the run, pause it, or fail it on cap
// exceeded. m.WorkflowRunID must be non-empty — the driver's OnTerminal
// hook already filters for that before calling in. Every decision
// appends a run event; the engine never mutates the mission itself.
func (e *Engine) OnMissionTerminal(ctx context.Context, m missions.Mission) {
	run, err := e.store.GetRun(ctx, m.WorkflowRunID)
	if err != nil {
		e.log.Warn("workflows: terminal mission: load run failed", "run_id", m.WorkflowRunID, "mission_id", m.ID, "error", err)
		return
	}
	if run.Status != "running" {
		// A run already paused/done/failed/cancelled ignores a late
		// terminal event from a step mission that raced past it (e.g. a
		// second edge firing while the run was already being cancelled).
		return
	}
	wf, err := e.store.Get(ctx, run.WorkflowID)
	if err != nil {
		e.log.Warn("workflows: terminal mission: load workflow failed", "workflow_id", run.WorkflowID, "error", err)
		return
	}
	def, err := ParseDefinition(wf.Definition)
	if err != nil {
		e.log.Warn("workflows: terminal mission: parse definition failed", "workflow_id", run.WorkflowID, "error", err)
		return
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
		return
	}

	firings, err := e.store.CountEdgeFirings(ctx, m.WorkflowRunID, matched.From, matched.On, matched.To)
	if err != nil {
		e.log.Warn("workflows: terminal mission: count edge firings failed", "run_id", m.WorkflowRunID, "error", err)
		return
	}
	if firings >= matched.MaxIterations {
		e.failRun(ctx, m.WorkflowRunID, matched)
		return
	}

	if matched.To == endStep {
		e.endRun(ctx, m.WorkflowRunID, matched)
		return
	}

	step, ok := def.Steps[matched.To]
	if !ok {
		e.pauseRun(ctx, m.WorkflowRunID, fmt.Sprintf("edge targets unknown step %q", matched.To))
		return
	}
	events, err := e.events.Events(ctx, m.ID)
	if err != nil {
		e.pauseRun(ctx, m.WorkflowRunID, fmt.Sprintf("load mission %s events failed: %s", m.ID, err.Error()))
		return
	}
	outcome := missions.OutcomeDigest(m, events, m.Phase, m.FailureReason)
	if _, err := e.spawnStep(ctx, m.WorkflowRunID, matched.To, step, run.Context, outcome, m.ID); err != nil {
		e.pauseRun(ctx, m.WorkflowRunID, fmt.Sprintf("spawn step %q failed: %s", matched.To, err.Error()))
		return
	}
	if err := e.store.ApplyRunTransition(ctx, m.WorkflowRunID, RunTransition{
		Status: "running", CurrentStep: matched.To,
		Events: []RunTransitionEvent{{Kind: "edge.taken", Payload: map[string]any{
			"from": matched.From, "on": matched.On, "to": matched.To, "mission_id": m.ID,
		}}},
	}); err != nil {
		e.log.Warn("workflows: terminal mission: advance run failed", "run_id", m.WorkflowRunID, "error", err)
	}
}

// spawnStep creates the next mission for step, interpolating its goal
// from outcome + run context, and appends a run.warning event for any
// unknown {{context.KEY}} placeholder before creating the mission.
func (e *Engine) spawnStep(ctx context.Context, runID, stepName string, step Step, runContext map[string]string, outcome, parentMissionID string) (string, error) {
	goal, unknown := interpolate(step.Goal, outcome, runContext)
	for _, key := range unknown {
		if err := e.store.ApplyRunTransition(ctx, runID, RunTransition{
			Status: "running", CurrentStep: stepName,
			Events: []RunTransitionEvent{{Kind: "run.warning", Payload: map[string]any{
				"message": fmt.Sprintf("unknown placeholder {{context.%s}} rendered empty", key), "step": stepName,
			}}},
		}); err != nil {
			e.log.Warn("workflows: spawn step: record unknown placeholder warning failed", "run_id", runID, "key", key, "error", err)
		}
	}
	route := step.Route
	if route == "" && e.routeForRole != nil {
		route = e.routeForRole(ctx, "default")
	}
	return e.spawner.Create(ctx, missions.Mission{
		Goal: goal, Kind: step.Kind, Route: route, PlanRoute: step.PlanRoute, AgentID: step.AgentID,
		OnComplete: step.OnComplete, DestinationIDs: step.DestinationIDs,
		ParentMissionID: parentMissionID, ParentContext: outcome,
		WorkflowRunID: runID, WorkflowStep: stepName,
	})
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
