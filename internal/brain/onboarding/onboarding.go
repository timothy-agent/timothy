// Package onboarding computes setup readiness from live probes and
// merges the operator's stored setup progress (issue #1057).
package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

// computeTimeout bounds one Compute call across every probe.
const computeTimeout = 4 * time.Second

// Readiness is what the UI gates on. Every field is derived live,
// never stored.
type Readiness struct {
	GatewayReady       bool `json:"gateway_ready"`
	ChatRoute          bool `json:"chat_route"`
	SummarizeRoute     bool `json:"summarize_route"`
	EmbeddingRoute     bool `json:"embedding_route"`
	VisionRoute        bool `json:"vision_route"`
	Sandbox            bool `json:"sandbox"`
	FirstChat          bool `json:"first_chat"`
	FirstMission       bool `json:"first_mission"`
	Connectors         int  `json:"connectors"`
	Channels           int  `json:"channels"`
	KBCollections      int  `json:"kb_collections"`
	Automations        int  `json:"automations"`
	AutomationsEnabled bool `json:"automations_enabled"`
	// MissionModelFloor is MISSION_MODEL_FLOOR's model-name substrings.
	MissionModelFloor []string `json:"mission_model_floor"`
}

// Probes are the live lookups Compute runs; a nil probe reads as false/0.
type Probes struct {
	// Invalidate drops the gateway client's memoized routing before the
	// route probes run, so a provider added seconds ago is visible.
	Invalidate          func()
	GatewayReady        func(ctx context.Context) (bool, string, error)
	RouteForRole        func(ctx context.Context, role string) (string, bool, error)
	ResolveRoute        func(ctx context.Context, name, harness string) (*gwclient.ResolvedRoute, error)
	SandboxHealth       func(ctx context.Context) error
	HasAssistantReply   func(ctx context.Context) (bool, error)
	HasSucceededMission func(ctx context.Context) (bool, error)
	CountConnectors     func(ctx context.Context) (int, error)
	CountChannels       func(ctx context.Context) (int, error)
	CountKBCollections  func(ctx context.Context) (int, error)
	CountAutomations    func(ctx context.Context) (int, error)
	AutomationsEnabled  func(ctx context.Context) bool
	MissionModelFloor   []string
}

// Compute runs every probe concurrently under computeTimeout. Probe
// errors read as false/0 and are logged at Debug, never returned. Route
// keys are false whenever the gateway is not ready.
func Compute(ctx context.Context, p Probes, log *slog.Logger) Readiness {
	ctx, cancel := context.WithTimeout(ctx, computeTimeout)
	defer cancel()
	if p.Invalidate != nil {
		p.Invalidate()
	}

	var (
		r  = Readiness{MissionModelFloor: p.MissionModelFloor}
		mu sync.Mutex
		wg sync.WaitGroup
	)
	run := func(fn func(context.Context) error, set func()) {
		wg.Go(func() {
			if err := fn(ctx); err != nil {
				log.Debug("onboarding: probe failed", "error", err)
				return
			}
			mu.Lock()
			set()
			mu.Unlock()
		})
	}
	boolProbe := func(name string, fn func(context.Context) (bool, error), dst *bool) {
		if fn == nil {
			return
		}
		var v bool
		run(func(ctx context.Context) error {
			var err error
			if v, err = fn(ctx); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			return nil
		}, func() { *dst = v })
	}
	countProbe := func(name string, fn func(context.Context) (int, error), dst *int) {
		if fn == nil {
			return
		}
		var n int
		run(func(ctx context.Context) error {
			var err error
			if n, err = fn(ctx); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			return nil
		}, func() { *dst = n })
	}

	if p.GatewayReady != nil {
		boolProbe("gateway", func(ctx context.Context) (bool, error) {
			ok, _, err := p.GatewayReady(ctx)
			return ok, err
		}, &r.GatewayReady)
	}
	for role, dst := range map[string]*bool{
		"default": &r.ChatRoute, "summarize": &r.SummarizeRoute,
		"embedding": &r.EmbeddingRoute, "vision": &r.VisionRoute,
	} {
		boolProbe("route "+role, p.routeUsable(role), dst)
	}
	if p.SandboxHealth != nil {
		boolProbe("sandbox", func(ctx context.Context) (bool, error) {
			return p.SandboxHealth(ctx) == nil, nil
		}, &r.Sandbox)
	}
	boolProbe("first chat", p.HasAssistantReply, &r.FirstChat)
	boolProbe("first mission", p.HasSucceededMission, &r.FirstMission)
	countProbe("connectors", p.CountConnectors, &r.Connectors)
	countProbe("channels", p.CountChannels, &r.Channels)
	countProbe("kb collections", p.CountKBCollections, &r.KBCollections)
	countProbe("automations", p.CountAutomations, &r.Automations)
	if p.AutomationsEnabled != nil {
		boolProbe("automations enabled", func(ctx context.Context) (bool, error) {
			return p.AutomationsEnabled(ctx), nil
		}, &r.AutomationsEnabled)
	}
	wg.Wait()
	// gwclient memoizes roles and resolves, so a downed gateway can
	// still answer route probes from cache.
	if !r.GatewayReady {
		r.ChatRoute, r.SummarizeRoute, r.EmbeddingRoute, r.VisionRoute = false, false, false, false
	}
	return r
}

// routeUsable reports whether role is bound to a route with at least
// one usable chat entry; nil when either gateway probe is missing.
func (p Probes) routeUsable(role string) func(context.Context) (bool, error) {
	if p.RouteForRole == nil || p.ResolveRoute == nil {
		return nil
	}
	return func(ctx context.Context) (bool, error) {
		name, ok, err := p.RouteForRole(ctx, role)
		if err != nil || !ok {
			return false, err
		}
		rr, err := p.ResolveRoute(ctx, name, "")
		if err != nil {
			return false, err
		}
		return slices.ContainsFunc(rr.Entries, func(e gwclient.ResolvedRouteEntry) bool { return e.Usable }), nil
	}
}

// Wizard values a patch may carry. WizardPending stores as "" so a
// restart clears the wizard state.
const (
	WizardPending = "pending"
	WizardSkipped = "skipped"
	WizardDone    = "done"
)

// Progress is the operator's stored setup state, also the PATCH body.
type Progress struct {
	// Wizard is "" (never run), "skipped" or "done".
	Wizard string `json:"wizard,omitempty"`
	// ChecklistDismissed is nil (no change) in a patch; stored as true/false.
	ChecklistDismissed *bool          `json:"checklist_dismissed,omitempty"`
	ToursSeen          map[string]int `json:"tours_seen,omitempty"`
	Visited            []string       `json:"visited,omitempty"`
}

// Validate rejects a patch with an unknown wizard state or a negative
// tour count.
func (p Progress) Validate() error {
	switch p.Wizard {
	case "", WizardPending, WizardSkipped, WizardDone:
	default:
		return fmt.Errorf("wizard must be %q, %q or %q", WizardPending, WizardSkipped, WizardDone)
	}
	for k, v := range p.ToursSeen {
		if v < 0 {
			return fmt.Errorf("tours_seen[%q] must not be negative", k)
		}
	}
	return nil
}

// Merge applies patch onto p: a set wizard or checklist_dismissed
// overwrites, tours_seen keeps the max per page and a 0 deletes the
// page, visited is a sorted set union. Call Validate on patch first.
func (p Progress) Merge(patch Progress) Progress {
	out := Progress{Wizard: p.Wizard, ChecklistDismissed: p.ChecklistDismissed}
	switch patch.Wizard {
	case "":
	case WizardPending:
		out.Wizard = ""
	default:
		out.Wizard = patch.Wizard
	}
	if patch.ChecklistDismissed != nil {
		v := *patch.ChecklistDismissed
		out.ChecklistDismissed = &v
	}
	tours := map[string]int{}
	for k, v := range p.ToursSeen {
		tours[k] = v
	}
	for k, v := range patch.ToursSeen {
		if v == 0 {
			delete(tours, k)
		} else if v > tours[k] {
			tours[k] = v
		}
	}
	if len(tours) > 0 {
		out.ToursSeen = tours
	}
	visited := slices.Concat(p.Visited, patch.Visited)
	slices.Sort(visited)
	out.Visited = slices.Compact(visited)
	if len(out.Visited) == 0 {
		out.Visited = nil
	}
	return out
}
