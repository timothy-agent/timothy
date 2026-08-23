// Package workflows implements the orchestration layer above missions
// (D-070): a workflow chains missions together via edges keyed on
// mission terminal outcomes. Missions stay atoms — this package only
// reads terminal missions and spawns new ones, never reaches inside a
// running mission. See docs/2026-08-14-agentic-workflows-plan.md.
package workflows

import (
	"encoding/json"
	"fmt"
)

// maxIterations is the HARD Go-side ceiling on any edge's max_iterations
// — a workflow definition can set a lower cap but never raise it past
// this, regardless of what the stored jsonb says (safety invariant in
// code, never data).
const maxIterations = 10

// endStep is the reserved step name a terminal edge targets to end the
// run successfully — not a real step, so it (and "done") can never
// collide with an author-defined step name.
const endStep = "end"

// Definition is a workflow's data: steps keyed by name, edges between
// them, and the entry step a new run starts on.
type Definition struct {
	Entry string          `json:"entry"`
	Steps map[string]Step `json:"steps"`
	Edges []Edge          `json:"edges"`
}

// Step is a mission template subset: enough to spawn a follow-up
// mission for this step. Goal may contain {{context.KEY}} and
// {{outcome}} placeholders, interpolated at spawn time (see
// interpolate.go).
type Step struct {
	Goal           string   `json:"goal"`
	Kind           string   `json:"kind"` // coding | general
	Route          string   `json:"route,omitempty"`
	PlanRoute      string   `json:"plan_route,omitempty"`
	AgentID        string   `json:"agent_id,omitempty"`
	OnComplete     string   `json:"on_complete,omitempty"`
	DestinationIDs []string `json:"destination_ids,omitempty"`
}

// Edge is one transition: when a step's mission ends with outcome On
// ("mission.done" or "mission.failed" in slice 1), go to step To ("end"
// terminates the run). MaxIterations caps how many times this exact
// edge may fire within one run — clamped to maxIterations regardless of
// what's stored (see Validate).
type Edge struct {
	From          string `json:"from"`
	On            string `json:"on"`
	To            string `json:"to"`
	MaxIterations int    `json:"max_iterations"`
}

// validOn is the slice-1 event-source allowlist — Go code, not data,
// same reasoning as the plan's "event-source allowlist are Go code"
// invariant.
var validOn = map[string]bool{
	"mission.done":   true,
	"mission.failed": true,
}

// Validate checks a definition's structural integrity and clamps every
// edge's MaxIterations to the hard ceiling. Mutates d.Edges in place
// (the clamp), so callers must persist the validated definition, not
// the one passed in.
func (d *Definition) Validate() error {
	if d.Entry == "" {
		return fmt.Errorf("entry is required")
	}
	if _, ok := d.Steps[d.Entry]; !ok {
		return fmt.Errorf("entry %q does not name a step", d.Entry)
	}
	for name := range d.Steps {
		if name == endStep || name == "done" {
			return fmt.Errorf("step name %q is reserved", name)
		}
		switch d.Steps[name].Kind {
		case "coding", "general":
		default:
			return fmt.Errorf("step %q: kind must be \"coding\" or \"general\"", name)
		}
		switch d.Steps[name].OnComplete {
		case "", "push", "push_pr":
		default:
			return fmt.Errorf("step %q: on_complete must be \"\", \"push\", or \"push_pr\"", name)
		}
	}
	for i := range d.Edges {
		e := &d.Edges[i]
		if _, ok := d.Steps[e.From]; !ok {
			return fmt.Errorf("edge %d: from %q does not name a step", i, e.From)
		}
		if !validOn[e.On] {
			return fmt.Errorf("edge %d: on %q is not a supported event", i, e.On)
		}
		if e.To != endStep {
			if _, ok := d.Steps[e.To]; !ok {
				return fmt.Errorf("edge %d: to %q does not name a step or %q", i, e.To, endStep)
			}
		}
		if e.MaxIterations <= 0 || e.MaxIterations > maxIterations {
			e.MaxIterations = maxIterations
		}
	}
	return nil
}

// ParseDefinition unmarshals and validates a stored definition —
// Store.Create/Update's shared entry point, so no unvalidated
// definition ever reaches the database.
func ParseDefinition(raw json.RawMessage) (Definition, error) {
	var d Definition
	if err := json.Unmarshal(raw, &d); err != nil {
		return Definition{}, fmt.Errorf("parse definition: %w", err)
	}
	if err := d.Validate(); err != nil {
		return Definition{}, fmt.Errorf("invalid definition: %w", err)
	}
	return d, nil
}
