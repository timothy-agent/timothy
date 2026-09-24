package missions

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

func boolPtr(b bool) *bool { return &b }

// resolveFixture: "dead" has only unusable entries, everything else one
// usable entry.
func resolveFixture(_ context.Context, route, _ string) (*gwclient.ResolvedRoute, error) {
	if route == "dead" {
		return &gwclient.ResolvedRoute{Route: route, Entries: []gwclient.ResolvedRouteEntry{{Usable: false, SkipReason: "cooling down"}}}, nil
	}
	return &gwclient.ResolvedRoute{Route: route, Entries: []gwclient.ResolvedRouteEntry{{Usable: true}}}, nil
}

func TestResolveDefaultsHarnessPrecedence(t *testing.T) {
	t.Parallel()
	agent := func(harness string) AgentResolver {
		return func(context.Context, string) (AgentDefaults, bool) { return AgentDefaults{Harness: harness}, true }
	}
	settings := func(h string) func(context.Context) string { return func(context.Context) string { return h } }
	cases := []struct {
		name    string
		req     CreateRequest
		deps    ResolveDeps
		harness string
	}{
		{"explicit wins", CreateRequest{Kind: KindCoding, AgentID: "a", Harness: "pi"}, ResolveDeps{Agent: agent("codex-cli"), CodingExecutorDefault: settings("claude-cli")}, "pi"},
		{"agent over settings", CreateRequest{Kind: KindCoding, AgentID: "a"}, ResolveDeps{Agent: agent("codex-cli"), CodingExecutorDefault: settings("claude-cli")}, "codex-cli"},
		{"settings when agent has none", CreateRequest{Kind: KindCoding, AgentID: "a"}, ResolveDeps{Agent: agent(""), CodingExecutorDefault: settings("claude-cli")}, "claude-cli"},
		{"native when nothing set", CreateRequest{Kind: KindCoding, AgentID: "a"}, ResolveDeps{Agent: agent("")}, ""},
		{"explicit native falls through to agent", CreateRequest{Kind: KindCoding, AgentID: "a", Harness: "native"}, ResolveDeps{Agent: agent("codex-cli")}, "codex-cli"},
		{"settings native is off", CreateRequest{Kind: KindCoding}, ResolveDeps{CodingExecutorDefault: settings("native")}, ""},
		{"default agent harness ignored without agent id", CreateRequest{Kind: KindCoding}, ResolveDeps{Agent: agent("codex-cli")}, ""},
		{"general never delegates", CreateRequest{Kind: KindGeneral, AgentID: "a"}, ResolveDeps{Agent: agent("codex-cli"), CodingExecutorDefault: settings("claude-cli")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := ResolveDefaults(context.Background(), tc.req, tc.deps)
			if err != nil {
				t.Fatalf("ResolveDefaults: %v", err)
			}
			if m.Harness != tc.harness {
				t.Fatalf("Harness = %q, want %q", m.Harness, tc.harness)
			}
		})
	}
}

func TestResolveDefaultsRoutes(t *testing.T) {
	t.Parallel()
	roleRoute := func(context.Context, string) string { return "default" }
	codingExists := func(context.Context, string) bool { return true }
	agent := func(context.Context, string) (AgentDefaults, bool) {
		return AgentDefaults{Route: "agent-route", ReviewRoute: "agent-review", PromptOverlay: "overlay"}, true
	}
	cases := []struct {
		name                        string
		req                         CreateRequest
		deps                        ResolveDeps
		route, reviewRoute, overlay string
	}{
		{"explicit routes kept", CreateRequest{Kind: KindGeneral, Route: "r", ReviewRoute: "rr"}, ResolveDeps{Agent: agent, RouteForRole: roleRoute}, "r", "rr", "overlay"},
		{"agent fills empty routes", CreateRequest{Kind: KindGeneral}, ResolveDeps{Agent: agent, RouteForRole: roleRoute}, "agent-route", "agent-review", "overlay"},
		{"default role when no agent", CreateRequest{Kind: KindGeneral}, ResolveDeps{RouteForRole: roleRoute, RouteExists: codingExists}, "default", "default", ""},
		{"coding prefers coding route", CreateRequest{Kind: KindCoding}, ResolveDeps{RouteForRole: roleRoute, RouteExists: codingExists}, "coding", "default", ""},
		{"coding without coding route", CreateRequest{Kind: KindCoding}, ResolveDeps{RouteForRole: roleRoute}, "default", "default", ""},
		{"review falls back to plan_route", CreateRequest{Kind: KindGeneral, PlanRoute: "strong"}, ResolveDeps{RouteForRole: roleRoute}, "default", "strong", ""},
		{"explicit review beats plan_route", CreateRequest{Kind: KindGeneral, PlanRoute: "strong", ReviewRoute: "rr"}, ResolveDeps{RouteForRole: roleRoute}, "default", "rr", ""},
		{"no deps leaves routes empty", CreateRequest{Kind: KindGeneral}, ResolveDeps{}, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := ResolveDefaults(context.Background(), tc.req, tc.deps)
			if err != nil {
				t.Fatalf("ResolveDefaults: %v", err)
			}
			if m.Route != tc.route || m.ReviewRoute != tc.reviewRoute || m.PromptOverlay != tc.overlay {
				t.Fatalf("route=%q review=%q overlay=%q, want %q %q %q", m.Route, m.ReviewRoute, m.PromptOverlay, tc.route, tc.reviewRoute, tc.overlay)
			}
		})
	}
}

func TestResolveDefaultsScalarDefaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	m, err := ResolveDefaults(ctx, CreateRequest{Kind: KindGeneral}, ResolveDeps{})
	if err != nil {
		t.Fatalf("ResolveDefaults: %v", err)
	}
	if m.BudgetCurrency != "USD" || m.MaxIterations != fallbackMaxIterations || !m.AutoApproveTools || !m.AutoApprovePlan || m.Flow != FlowFull {
		t.Fatalf("zero request defaults = currency %q max %d tools %v plan %v flow %q", m.BudgetCurrency, m.MaxIterations, m.AutoApproveTools, m.AutoApprovePlan, m.Flow)
	}

	m, _ = ResolveDefaults(ctx, CreateRequest{Kind: KindGeneral}, ResolveDeps{DefaultMaxIterations: func(context.Context) int { return 7 }})
	if m.MaxIterations != 7 {
		t.Fatalf("MaxIterations = %d, want the configured 7", m.MaxIterations)
	}
	m, _ = ResolveDefaults(ctx, CreateRequest{Kind: KindGeneral}, ResolveDeps{DefaultMaxIterations: func(context.Context) int { return 0 }})
	if m.MaxIterations != fallbackMaxIterations {
		t.Fatalf("MaxIterations = %d, want fallback when the setting is unset", m.MaxIterations)
	}
	m, _ = ResolveDefaults(ctx, CreateRequest{Kind: KindGeneral, MaxIterations: 2}, ResolveDeps{DefaultMaxIterations: func(context.Context) int { return 7 }})
	if m.MaxIterations != 2 {
		t.Fatalf("MaxIterations = %d, want the explicit 2", m.MaxIterations)
	}

	m, _ = ResolveDefaults(ctx, CreateRequest{Kind: KindGeneral, BudgetCurrency: "EUR", AutoApproveTools: boolPtr(false), AutoApprovePlan: boolPtr(false)}, ResolveDeps{})
	if m.BudgetCurrency != "EUR" || m.AutoApproveTools || m.AutoApprovePlan {
		t.Fatalf("explicit values overwritten: currency %q tools %v plan %v", m.BudgetCurrency, m.AutoApproveTools, m.AutoApprovePlan)
	}

	m, _ = ResolveDefaults(ctx, CreateRequest{Kind: KindGeneral, Light: true}, ResolveDeps{})
	if m.Flow != FlowLight {
		t.Fatalf("light flow = %q, want light", m.Flow)
	}
	m, _ = ResolveDefaults(ctx, CreateRequest{Kind: KindGeneral, Light: true, Flow: string(FlowNoProve)}, ResolveDeps{})
	if m.Flow != FlowNoProve {
		t.Fatalf("explicit flow = %q, want no_prove kept for ValidateCreate to judge", m.Flow)
	}

	m, _ = ResolveDefaults(ctx, CreateRequest{Kind: KindCoding, ReviewHarness: "native"}, ResolveDeps{})
	if m.ReviewHarness != "" {
		t.Fatalf("ReviewHarness = %q, want native normalized to empty", m.ReviewHarness)
	}
}

func TestResolveDefaultsClassifiesOnlyWhenKindEmpty(t *testing.T) {
	t.Parallel()
	calls := 0
	deps := ResolveDeps{Classify: func(context.Context, string) (string, error) { calls++; return "coding", nil }}
	m, err := ResolveDefaults(context.Background(), CreateRequest{Goal: "g"}, deps)
	if err != nil {
		t.Fatalf("ResolveDefaults: %v", err)
	}
	if calls != 1 || m.Kind != KindCoding {
		t.Fatalf("empty kind: calls=%d kind=%q, want 1 call and coding", calls, m.Kind)
	}
	if _, err := ResolveDefaults(context.Background(), CreateRequest{Goal: "g", Kind: KindGeneral}, deps); err != nil {
		t.Fatalf("ResolveDefaults: %v", err)
	}
	if calls != 1 {
		t.Fatalf("classifier called %d times, want no call for an explicit kind", calls)
	}
	m, _ = ResolveDefaults(context.Background(), CreateRequest{Goal: "g"}, ResolveDeps{})
	if m.Kind != KindGeneral {
		t.Fatalf("nil classifier kind = %q, want general", m.Kind)
	}
	failing := ResolveDeps{Classify: func(context.Context, string) (string, error) { return "", errors.New("down") }}
	m, _ = ResolveDefaults(context.Background(), CreateRequest{Goal: "g"}, failing)
	if m.Kind != KindGeneral {
		t.Fatalf("failing classifier kind = %q, want general", m.Kind)
	}
}

func TestResolveDefaultsRouteGate(t *testing.T) {
	t.Parallel()
	deps := ResolveDeps{ResolveRoute: resolveFixture}
	_, err := ResolveDefaults(context.Background(), CreateRequest{Kind: KindGeneral, Route: "alive", ReviewRoute: "dead"}, deps)
	var unusable *RouteUnusableError
	if !errors.As(err, &unusable) || unusable.Route != "dead" || unusable.Reason != "cooling down" {
		t.Fatalf("err = %v, want RouteUnusableError for dead", err)
	}
	if err.Error() != `route "dead" has no usable provider: cooling down` {
		t.Fatalf("message = %q", err.Error())
	}
	// A light mission never runs review, so a dead review route passes.
	if _, err := ResolveDefaults(context.Background(), CreateRequest{Kind: KindGeneral, Light: true, Route: "alive", ReviewRoute: "dead"}, deps); err != nil {
		t.Fatalf("light with dead review route: %v", err)
	}
	// The gate sees the resolved route, not the request's empty one.
	roleDead := ResolveDeps{ResolveRoute: resolveFixture, RouteForRole: func(context.Context, string) string { return "dead" }}
	if _, err := ResolveDefaults(context.Background(), CreateRequest{Kind: KindGeneral}, roleDead); !errors.As(err, &unusable) {
		t.Fatalf("defaulted dead route: err = %v, want RouteUnusableError", err)
	}
}

// TestResolveDefaultsOrigin covers issue #817: origin_kind defaults,
// followup derivation, unattended derivation and override, and the
// 1800s permission timeout default for unattended missions only.
func TestResolveDefaultsOrigin(t *testing.T) {
	t.Parallel()
	seconds := func(n int) *int { return &n }
	cases := []struct {
		name           string
		req            CreateRequest
		wantOrigin     string
		wantUnattended bool
		wantTimeout    *int
	}{
		{"api default", CreateRequest{}, OriginAPI, false, nil},
		{"explicit api", CreateRequest{OriginKind: OriginAPI}, OriginAPI, false, nil},
		{"parent derives followup", CreateRequest{ParentMissionID: "p1"}, OriginFollowup, false, nil},
		{"explicit api with parent derives followup", CreateRequest{OriginKind: OriginAPI, ParentMissionID: "p1"}, OriginFollowup, false, nil},
		{"chat with parent stays chat", CreateRequest{OriginKind: OriginChat, ParentMissionID: "p1"}, OriginChat, false, nil},
		{"chat", CreateRequest{OriginKind: OriginChat}, OriginChat, false, nil},
		{"automation derives unattended", CreateRequest{OriginKind: OriginAutomation}, OriginAutomation, true, seconds(1800)},
		{"workflow derives unattended", CreateRequest{OriginKind: OriginWorkflow}, OriginWorkflow, true, seconds(1800)},
		{"workflow with parent stays workflow", CreateRequest{OriginKind: OriginWorkflow, ParentMissionID: "p1"}, OriginWorkflow, true, seconds(1800)},
		{"automation with parent stays automation", CreateRequest{OriginKind: OriginAutomation, ParentMissionID: "p1"}, OriginAutomation, true, seconds(1800)},
		{"api overridden unattended", CreateRequest{Unattended: boolPtr(true)}, OriginAPI, true, seconds(1800)},
		{"automation overridden attended", CreateRequest{OriginKind: OriginAutomation, Unattended: boolPtr(false)}, OriginAutomation, false, nil},
		{"unattended keeps explicit timeout", CreateRequest{OriginKind: OriginAutomation, PermissionTimeoutSeconds: seconds(60)}, OriginAutomation, true, seconds(60)},
		{"unattended keeps explicit zero timeout", CreateRequest{OriginKind: OriginWorkflow, PermissionTimeoutSeconds: seconds(0)}, OriginWorkflow, true, seconds(0)},
		{"attended keeps explicit timeout", CreateRequest{PermissionTimeoutSeconds: seconds(90)}, OriginAPI, false, seconds(90)},
		{"unknown origin passes through", CreateRequest{OriginKind: "cron"}, "cron", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.req.Kind = KindGeneral
			m, err := ResolveDefaults(context.Background(), tc.req, ResolveDeps{})
			if err != nil {
				t.Fatalf("ResolveDefaults: %v", err)
			}
			if m.OriginKind != tc.wantOrigin || m.Unattended != tc.wantUnattended {
				t.Fatalf("origin=%q unattended=%v, want %q %v", m.OriginKind, m.Unattended, tc.wantOrigin, tc.wantUnattended)
			}
			switch {
			case tc.wantTimeout == nil && m.PermissionTimeoutSeconds != nil:
				t.Fatalf("PermissionTimeoutSeconds = %d, want nil", *m.PermissionTimeoutSeconds)
			case tc.wantTimeout != nil && (m.PermissionTimeoutSeconds == nil || *m.PermissionTimeoutSeconds != *tc.wantTimeout):
				t.Fatalf("PermissionTimeoutSeconds = %v, want %d", m.PermissionTimeoutSeconds, *tc.wantTimeout)
			}
		})
	}
}

// TestResolveDefaultsChannelConversation: a chat create carries its
// channel conversation onto the mission; other creates leave it empty.
func TestResolveDefaultsChannelConversation(t *testing.T) {
	t.Parallel()
	m, err := ResolveDefaults(context.Background(), CreateRequest{Kind: KindGeneral, OriginKind: OriginChat, ChannelConversationID: "c1"}, ResolveDeps{})
	if err != nil || m.ChannelConversationID != "c1" || m.OriginKind != OriginChat || m.Unattended {
		t.Fatalf("chat create = %+v %v", m, err)
	}
	if m, _ := ResolveDefaults(context.Background(), CreateRequest{Kind: KindGeneral}, ResolveDeps{}); m.ChannelConversationID != "" {
		t.Fatalf("api create conversation = %q, want empty", m.ChannelConversationID)
	}
}

// TestResolveDefaultsOriginValidates: a resolved mission passes
// ValidateCreate for every known origin and fails for an unknown one.
func TestResolveDefaultsOriginValidates(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{"", OriginAPI, OriginAutomation, OriginWorkflow, OriginChat, OriginFollowup} {
		m, err := ResolveDefaults(context.Background(), CreateRequest{Kind: KindGeneral, Route: "default", OriginKind: origin}, ResolveDeps{})
		if err != nil {
			t.Fatalf("ResolveDefaults(%q): %v", origin, err)
		}
		if err := ValidateCreate(context.Background(), m, ValidateDeps{}); err != nil {
			t.Fatalf("ValidateCreate(origin %q): %v", origin, err)
		}
	}
	m, _ := ResolveDefaults(context.Background(), CreateRequest{Kind: KindGeneral, Route: "default", OriginKind: "cron"}, ResolveDeps{})
	if err := ValidateCreate(context.Background(), m, ValidateDeps{}); !errors.Is(err, ErrInvalidMission) {
		t.Fatalf("ValidateCreate(origin cron) = %v, want ErrInvalidMission", err)
	}
}

// TestNoLineageUnattendedInference guards issue #817: no non-test
// source in this package infers "nobody is watching" from
// AutomationRunID or WorkflowRunID; Mission.Unattended is the one rule.
func TestNoLineageUnattendedInference(t *testing.T) {
	t.Parallel()
	src := os.DirFS(".")
	files, err := fs.Glob(src, "*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	inference := regexp.MustCompile(`(\w+)\.(AutomationRunID|WorkflowRunID)\s*!=\s*""`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := fs.ReadFile(src, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, match := range inference.FindAllStringSubmatch(string(body), -1) {
			t.Errorf("%s: %q infers unattended from a lineage id, use Mission.Unattended", f, match[0])
		}
	}
}

// TestSingleMissionsInsert guards issue #816: store.Create is the only
// INSERT INTO missions in the package's non-test sources.
func TestSingleMissionsInsert(t *testing.T) {
	t.Parallel()
	src := os.DirFS(".")
	files, err := fs.Glob(src, "*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	insert := regexp.MustCompile(`(?i)INSERT\s+INTO\s+missions\b`)
	var hits []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := fs.ReadFile(src, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for range insert.FindAllIndex(body, -1) {
			hits = append(hits, f)
		}
	}
	if len(hits) != 1 || hits[0] != "store.go" {
		t.Fatalf("INSERT INTO missions found in %v, want exactly once in store.go", hits)
	}
}

// TestResolveDefaultsToolAllowlist pins issue #857: entries are trimmed
// and deduplicated, nil stays nil (unrestricted) and an empty entry
// survives for ValidateCreate to reject.
func TestResolveDefaultsToolAllowlist(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil stays unrestricted", nil, nil},
		{"trimmed and deduplicated", []string{" shell", "shell ", "read_note", "shell"}, []string{"shell", "read_note"}},
		{"blank entry kept for validation", []string{"shell", "  "}, []string{"shell", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := ResolveDefaults(context.Background(), CreateRequest{Kind: KindGeneral, ToolAllowlist: tc.in}, ResolveDeps{})
			if err != nil {
				t.Fatalf("ResolveDefaults: %v", err)
			}
			if !slices.Equal(m.ToolAllowlist, tc.want) || (tc.want == nil) != (m.ToolAllowlist == nil) {
				t.Fatalf("ToolAllowlist = %#v, want %#v", m.ToolAllowlist, tc.want)
			}
		})
	}
}
