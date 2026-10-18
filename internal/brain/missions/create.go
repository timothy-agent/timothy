package missions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

// CreateRequest is a mission create before defaults are applied, shared
// by the HTTP create handler, automation runs and the workflow engine
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
	ExecutorSessionPolicy string
	HasPlan               bool

	// Flow "" maps to FlowLight when Light is set, else FlowFull.
	Light bool
	Flow  string

	PermissionTimeoutSeconds *int

	ParentMissionID string
	Sources         []SourceEntry
	Destinations    []DestinationEntry

	AutomationRunID string
	WorkflowRunID   string
	WorkflowStep    string

	// ToolAllowlist nil means unrestricted; entries are trimmed and
	// deduplicated.
	ToolAllowlist []string

	// OriginKind "" resolves to OriginAPI, or OriginFollowup when
	// ParentMissionID is set. Unattended nil derives from the origin.
	OriginKind string
	Unattended *bool

	// ChannelConversationID is the channel conversation of the chat
	// session that created the mission, if any.
	ChannelConversationID string
}

// ResolveDeps are the lookups ResolveDefaults needs. Every field is
// nil-safe: an unset dep skips that one resolution step.
type ResolveDeps struct {
	// Decide asks the decide role for kind when a request omits it.
	Decide Decide
	// Classify answers the kind prompt when Decide is nil or unbound.
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
		kind = ClassifyKind(ctx, deps.Decide, deps.Classify, req.Goal)
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
		Harness: harness, ReviewHarness: reviewHarness,
		ExecutorSessionPolicy:    req.ExecutorSessionPolicy,
		HasPlan:                  req.HasPlan,
		ParentMissionID:          req.ParentMissionID,
		Sources:                  req.Sources,
		Destinations:             req.Destinations,
		Flow:                     flow,
		PermissionTimeoutSeconds: permissionTimeout,
		AutomationRunID:          req.AutomationRunID,
		ToolAllowlist:            normalizeToolAllowlist(req.ToolAllowlist),
		WorkflowRunID:            req.WorkflowRunID,
		WorkflowStep:             req.WorkflowStep,
		OriginKind:               origin,
		Unattended:               unattended,
		ChannelConversationID:    req.ChannelConversationID,
	}
	// Route gate (D-100, issue #536): every phase axis this flow runs
	// must have a usable chain entry, else the first turn parks on the
	// gateway's no_route error.
	if r, reason, unusable := UnusableCreateRoute(ctx, deps.ResolveRoute, m); unusable {
		return Mission{}, &RouteUnusableError{Route: r, Reason: reason}
	}
	return m, nil
}

// Decide answers one decide-role question with the picked option id.
// It returns ErrDecideUnbound when no decide role is bound, which sends
// the caller to its classify path.
type Decide func(ctx context.Context, kind, question, state string, options []gwclient.DecideOption) (string, error)

// ErrDecideUnbound marks a Decide call skipped because no decide role
// is bound.
var ErrDecideUnbound = errors.New("missions: decide role unbound")

// Kind and light on the decide role (D-174): the goal is the state,
// each kind an "id: description" option.
const (
	KindQuestion  = "Classify this mission goal based on its deliverable, not its topic."
	LightQuestion = "Is this goal a single-pass task deliverable in one response: a read, summary, lookup, or short write-up with no multi-step plan or file artifacts?"
)

// KindOptions are the decide options for a mission's kind.
func KindOptions() []gwclient.DecideOption {
	return []gwclient.DecideOption{
		{ID: KindCoding, Description: "produces or changes code, scripts, or configuration files in a repository"},
		{ID: KindGeneral, Description: "output is a document, report, analysis, book, plan, or data gathering, even when its subject is programming, software, or coding (a book or article about coding is general)"},
	}
}

// ClassifyKind decides kind for a create request that omits it, on the
// same path as ClassifyKindAndLight so create and the preview endpoint
// never disagree. Light is never asked.
func ClassifyKind(ctx context.Context, decide Decide, classify func(ctx context.Context, prompt string) (string, error), goal string) string {
	kind, _ := classifyGoal(ctx, decide, classify, goal, false)
	return kind
}

// ClassifyKindAndLight decides a goal's kind by deliverable, not topic,
// and for a general goal whether it is light (one response, no plan or
// artifacts). With a decide role bound (D-174), kind is a choice and
// light a yesno, both with the goal as state. Unbound (nil decide or
// ErrDecideUnbound), one classify prompt answers both axes. Any failure
// or unknown answer gives general and light false: general is the
// cheaper mistake, and light is only suggested when confident.
func ClassifyKindAndLight(ctx context.Context, decide Decide, classify func(ctx context.Context, prompt string) (string, error), goal string) (kind string, light bool) {
	return classifyGoal(ctx, decide, classify, goal, true)
}

func classifyGoal(ctx context.Context, decide Decide, classify func(ctx context.Context, prompt string) (string, error), goal string, askLight bool) (string, bool) {
	if decide == nil {
		return classifyKindAndLight(ctx, classify, goal)
	}
	choice, err := decide(ctx, "choice", KindQuestion, goal, KindOptions())
	if errors.Is(err, ErrDecideUnbound) {
		return classifyKindAndLight(ctx, classify, goal)
	}
	if err != nil || (choice != KindCoding && choice != KindGeneral) {
		return KindGeneral, false
	}
	if choice == KindCoding || !askLight {
		return choice, false
	}
	answer, err := decide(ctx, "yesno", LightQuestion, goal, nil)
	return KindGeneral, err == nil && answer == "yes"
}

// classifyKindAndLight answers both axes in one classify call. Parsing
// takes the first recognised kind word and the first recognised
// light/full word, in order, from the reply's fields (punctuation
// stripped).
func classifyKindAndLight(ctx context.Context, classify func(ctx context.Context, prompt string) (string, error), goal string) (kind string, light bool) {
	if classify == nil {
		return "general", false
	}
	prompt := "Classify this mission goal along two independent axes, based on its deliverable, not its topic.\n" +
		"Axis 1 — coding or general: coding means the mission produces or changes code, scripts, or configuration files in a repository; general means the output is a document, report, analysis, book, plan, or data gathering, even when its subject is programming, software, or coding (a book or article about coding is general).\n" +
		"Axis 2 — light or full: light means the goal is a single-pass task deliverable in one response (a read, summary, lookup, or short write-up with no multi-step plan or file artifacts); full means it needs a plan and artifacts.\n" +
		"Answer with exactly two words separated by a space: first coding or general, then light or full.\n\n" +
		"Goal: " + goal
	reply, err := classify(ctx, prompt)
	if err != nil {
		return "general", false
	}
	fields := strings.Fields(strings.ToLower(reply))
	kind = ""
	kindFound, lightFound := false, false
	for _, field := range fields {
		clean := strings.Trim(field, ".,")
		if !kindFound {
			switch clean {
			case "coding":
				kind = "coding"
				kindFound = true
				continue
			case "general":
				kind = "general"
				kindFound = true
				continue
			}
		}
		if kindFound && !lightFound {
			switch clean {
			case "light":
				light = true
				lightFound = true
			case "full":
				light = false
				lightFound = true
			}
		}
	}
	if !kindFound {
		return "general", false
	}
	if !lightFound {
		light = false
	}
	return kind, kind == "general" && light
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

// normalizeToolAllowlist trims and deduplicates entries; nil stays nil.
// An empty entry is kept for ValidateCreate to reject.
func normalizeToolAllowlist(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, name := range in {
		name = strings.TrimSpace(name)
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}
