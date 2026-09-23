package missions

import (
	"context"
	"fmt"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

// CreateRequest is a mission create before defaults are applied, shared
// by the HTTP create handler, the scheduler and the workflow engine
// (issue #816). Empty fields mean "resolve the default";
// ResolveDefaults turns it into the Mission Driver.Create persists.
type CreateRequest struct {
	Goal    string
	Name    string
	Kind    string
	AgentID string

	Route            string
	ReviewRoute      string
	PlanRoute        string
	EscalationRoute  string
	RouteModel       string
	PlanRouteModel   string
	ReviewRouteModel string

	MaxIterations  int
	BudgetAmount   *float64
	BudgetCurrency string

	// AutoApproveTools/AutoApprovePlan default true when nil.
	AutoApproveTools *bool
	AutoApprovePlan  *bool

	Harness               string
	ReviewHarness         string
	Environment           string
	ExecutorSessionPolicy string
	HasPlan               bool

	// Flow "" maps to FlowLight when Light is set, else FlowFull.
	Light bool
	Flow  string

	PermissionTimeoutSeconds *int

	ParentMissionID string
	Sources         []SourceEntry
	Destinations    []DestinationEntry

	ScheduleID    string
	WorkflowRunID string
	WorkflowStep  string

	// OriginKind "" resolves to OriginAPI, or OriginFollowup when
	// ParentMissionID is set. Unattended nil derives from the origin.
	OriginKind string
	Unattended *bool
}

// ResolveDeps are the lookups ResolveDefaults needs. Every field is
// nil-safe: an unset dep skips that one resolution step.
type ResolveDeps struct {
	// Classify answers the kind prompt when a request omits kind.
	Classify func(ctx context.Context, prompt string) (string, error)
	// Agent resolves the request's agent; "" resolves the default agent.
	Agent AgentResolver
	// RouteForRole resolves a system role's route ("default").
	RouteForRole func(ctx context.Context, role string) string
	// RouteExists backs DefaultCodingRoute's "coding" route preference.
	RouteExists func(ctx context.Context, name string) bool
	// CodingExecutorDefault is settings.coding_executor.
	CodingExecutorDefault func(ctx context.Context) string
	// DefaultMaxIterations is the configured iteration ceiling.
	DefaultMaxIterations func(ctx context.Context) int
	// ResolveRoute backs the D-100 usable-route gate.
	ResolveRoute func(ctx context.Context, route, harness string) (*gwclient.ResolvedRoute, error)
}

// RouteUnusableError is the D-100 gate's rejection: route resolved to
// a chain whose every entry is unusable.
type RouteUnusableError struct {
	Route  string
	Reason string
}

func (e *RouteUnusableError) Error() string {
	return fmt.Sprintf("route %q has no usable provider: %s", e.Route, e.Reason)
}

// ResolveDefaults applies every create-time default to req, in the same
// order for every caller: kind classification (only when kind is
// empty), agent route/review_route/prompt overlay, harness precedence
// (ResolveHarness), default or coding route, review_route falling back
// to plan_route, auto-approve defaults, budget currency, flow, max
// iterations, origin/unattended and the unattended permission timeout,
// then the D-100 usable-route gate. ValidateCreate (run by
// Driver.Create) stays the shape check on the result.
func ResolveDefaults(ctx context.Context, req CreateRequest, deps ResolveDeps) (Mission, error) {
	kind := req.Kind
	if kind == "" {
		kind = ClassifyKind(ctx, deps.Classify, req.Goal)
	}
	harness := req.Harness
	if harness == "native" {
		harness = ""
	}
	reviewHarness := req.ReviewHarness
	if reviewHarness == "native" {
		reviewHarness = ""
	}

	route, reviewRoute := req.Route, req.ReviewRoute
	var promptOverlay, agentHarness string
	if deps.Agent != nil {
		if a, ok := deps.Agent(ctx, req.AgentID); ok {
			if route == "" {
				route = a.Route
			}
			if reviewRoute == "" {
				reviewRoute = a.ReviewRoute
			}
			promptOverlay = a.PromptOverlay
			// The default agent's harness never applies to a request
			// that names no agent.
			if req.AgentID != "" {
				agentHarness = a.Harness
			}
		}
	}
	harness, _ = ResolveHarness(ctx, kind, harness, agentHarness, deps.CodingExecutorDefault)

	defaultRoute := ""
	if deps.RouteForRole != nil {
		defaultRoute = deps.RouteForRole(ctx, "default")
	}
	if route == "" {
		if kind == KindCoding {
			route = DefaultCodingRoute(ctx, deps.RouteExists, defaultRoute)
		} else {
			route = defaultRoute
		}
	}
	if reviewRoute == "" {
		// An explicit plan_route covers review unless review_route was
		// set itself (runner precedence: review_route > plan_route > route).
		if req.PlanRoute != "" {
			reviewRoute = req.PlanRoute
		} else {
			reviewRoute = defaultRoute
		}
	}

	autoApproveTools := true
	if req.AutoApproveTools != nil {
		autoApproveTools = *req.AutoApproveTools
	}
	autoApprovePlan := true
	if req.AutoApprovePlan != nil {
		autoApprovePlan = *req.AutoApprovePlan
	}
	budgetCurrency := req.BudgetCurrency
	if budgetCurrency == "" {
		budgetCurrency = "USD"
	}
	// An explicit flow that contradicts light or kind is left for
	// ValidateCreate to reject.
	flow := Flow(req.Flow)
	if flow == "" {
		if req.Light {
			flow = FlowLight
		} else {
			flow = FlowFull
		}
	}
	maxIterations := req.MaxIterations
	if maxIterations <= 0 && deps.DefaultMaxIterations != nil {
		maxIterations = deps.DefaultMaxIterations(ctx)
	}
	if maxIterations <= 0 {
		maxIterations = fallbackMaxIterations
	}
	origin := req.OriginKind
	if origin == "" {
		origin = OriginAPI
	}
	if origin == OriginAPI && req.ParentMissionID != "" {
		origin = OriginFollowup
	}
	unattended := origin == OriginAutomation || origin == OriginWorkflow
	if req.Unattended != nil {
		unattended = *req.Unattended
	}
	permissionTimeout := req.PermissionTimeoutSeconds
	if permissionTimeout == nil && unattended {
		seconds := defaultPermissionTimeoutUnattended
		permissionTimeout = &seconds
	}

	m := Mission{
		Goal: req.Goal, Name: req.Name, Kind: kind, AgentID: req.AgentID,
		Route: route, ReviewRoute: reviewRoute, PlanRoute: req.PlanRoute, EscalationRoute: req.EscalationRoute,
		RouteModel: req.RouteModel, PlanRouteModel: req.PlanRouteModel, ReviewRouteModel: req.ReviewRouteModel,
		MaxIterations: maxIterations, BudgetAmount: req.BudgetAmount, BudgetCurrency: budgetCurrency,
		AutoApproveTools: autoApproveTools, AutoApprovePlan: autoApprovePlan, PromptOverlay: promptOverlay,
		Harness: harness, ReviewHarness: reviewHarness, Environment: req.Environment,
		ExecutorSessionPolicy:    req.ExecutorSessionPolicy,
		HasPlan:                  req.HasPlan,
		ParentMissionID:          req.ParentMissionID,
		Sources:                  req.Sources,
		Destinations:             req.Destinations,
		Flow:                     flow,
		PermissionTimeoutSeconds: permissionTimeout,
		ScheduleID:               req.ScheduleID,
		WorkflowRunID:            req.WorkflowRunID,
		WorkflowStep:             req.WorkflowStep,
		OriginKind:               origin,
		Unattended:               unattended,
	}
	// Route gate (D-100, issue #536): every phase axis this flow runs
	// must have a usable chain entry, else the first turn parks on the
	// gateway's no_route error.
	if r, reason, unusable := UnusableCreateRoute(ctx, deps.ResolveRoute, m); unusable {
		return Mission{}, &RouteUnusableError{Route: r, Reason: reason}
	}
	return m, nil
}

// ClassifyKind decides kind from goal by deliverable, not topic: a book
// or article about coding is general. Falls back to "general" on a nil
// classifier, a classify error, or an unrecognised reply, since general
// is the cheaper mistake.
func ClassifyKind(ctx context.Context, classify func(ctx context.Context, prompt string) (string, error), goal string) string {
	if classify == nil {
		return KindGeneral
	}
	prompt := "Decide how this mission's work happens, based on its deliverable, not its topic. Answer with exactly one word.\n" +
		"coding — the mission produces or changes code, scripts, or configuration files in a repository.\n" +
		"general — the output is a document, report, analysis, book, plan, or data gathering, even when its subject is programming, software, or coding (a book or article about coding is general).\n\n" +
		"Goal: " + goal
	reply, err := classify(ctx, prompt)
	if err != nil {
		return KindGeneral
	}
	for _, field := range strings.Fields(strings.ToLower(reply)) {
		switch strings.Trim(field, ".,") {
		case KindCoding:
			return KindCoding
		case KindGeneral:
			return KindGeneral
		}
	}
	return KindGeneral
}

// RouteUnusable resolves route on one axis (harness "" is the chat
// axis) and reports whether its chain has entries but none is usable,
// with the first skip reason. A resolve error, nil resolver or empty
// chain is never unusable: the gate blocks only on positive evidence.
func RouteUnusable(ctx context.Context, resolve func(ctx context.Context, route, harness string) (*gwclient.ResolvedRoute, error), route, harness string) (reason string, unusable bool) {
	if route == "" || resolve == nil {
		return "", false
	}
	resolved, err := resolve(ctx, route, harness)
	if err != nil || resolved == nil || len(resolved.Entries) == 0 {
		return "", false
	}
	reason = "no usable provider for this route"
	for _, e := range resolved.Entries {
		if e.Usable {
			return "", false
		}
		if reason == "no usable provider for this route" && e.SkipReason != "" {
			reason = e.SkipReason
		}
	}
	return reason, true
}

// UnusableCreateRoute walks the phase axes m's flow runs (D-100): build
// on the harness axis, discover/plan on the oversight route and prove
// on the review route unless the flow skips them, escalate when set.
// Returns the first route with zero usable entries and its reason.
func UnusableCreateRoute(ctx context.Context, resolve func(ctx context.Context, route, harness string) (*gwclient.ResolvedRoute, error), m Mission) (route, reason string, unusable bool) {
	type axis struct{ route, harness string }
	axes := []axis{{m.Route, m.Harness}}
	if m.Flow != FlowLight {
		oversight := m.PlanRoute
		if oversight == "" {
			oversight = m.Route
		}
		axes = append(axes, axis{oversight, ""})
	}
	if m.Flow == FlowFull || m.Flow == "" {
		axes = append(axes, axis{m.ReviewRoute, ""})
	}
	if m.EscalationRoute != "" {
		axes = append(axes, axis{m.EscalationRoute, ""})
	}
	seen := map[axis]bool{}
	for _, a := range axes {
		if a.route == "" || seen[a] {
			continue
		}
		seen[a] = true
		if reason, bad := RouteUnusable(ctx, resolve, a.route, a.harness); bad {
			return a.route, reason, true
		}
	}
	return "", "", false
}
