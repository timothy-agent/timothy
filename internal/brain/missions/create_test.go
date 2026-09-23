package missions

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"regexp"
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

func TestTemplateCreateRequestCarriesScheduleFields(t *testing.T) {
	t.Parallel()
	sc := Schedule{ID: "s1", Name: "inbox-digest", MissionTemplate: MissionTemplate{Goal: "g", Kind: KindGeneral, AutoApproveTools: false}}
	req := TemplateCreateRequest(sc, []string{"d1"})
	if req.ScheduleID != "s1" || req.Name != "inbox-digest" || req.AutoApprovePlan == nil || !*req.AutoApprovePlan {
		t.Fatalf("req = %+v, want schedule id, schedule name, auto_approve_plan true", req)
	}
	if req.AutoApproveTools == nil || *req.AutoApproveTools {
		t.Fatal("AutoApproveTools must carry the template's explicit false")
	}
	if len(req.Destinations) != 1 || req.Destinations[0].DestinationID != "d1" {
		t.Fatalf("Destinations = %+v, want d1", req.Destinations)
	}
	sc.MissionTemplate.Name = "Today's Meetings"
	if got := TemplateCreateRequest(sc, nil).Name; got != "Today's Meetings" {
		t.Fatalf("Name = %q, want the template's own name", got)
	}
}

func TestValidateTemplate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		t       MissionTemplate
		wantErr string
	}{
		{"valid general", MissionTemplate{Goal: "g", Kind: KindGeneral}, ""},
		{"valid light general", MissionTemplate{Goal: "g", Kind: KindGeneral, Light: true}, ""},
		{"valid coding", MissionTemplate{Goal: "g", Kind: KindCoding}, ""},
		{"missing goal", MissionTemplate{Kind: KindGeneral}, "goal is required"},
		{"blank goal", MissionTemplate{Goal: "  ", Kind: KindGeneral}, "goal is required"},
		{"empty kind", MissionTemplate{Goal: "g"}, "kind must be"},
		{"bogus kind", MissionTemplate{Goal: "g", Kind: "bogus"}, "kind must be"},
		{"light coding", MissionTemplate{Goal: "g", Kind: KindCoding, Light: true}, "light is only valid for kind=general"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateTemplate(tc.t)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateTemplate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestScheduleFireRejectsLightCodingTemplate covers the fire-time half
// of the light+coding rule: a stored row that predates the save check
// fails before Driver.Create and maps to fire_error.
func TestScheduleFireRejectsLightCodingTemplate(t *testing.T) {
	t.Parallel()
	created := false
	s := &Scheduler{log: discardLog(), create: func(context.Context, Mission) (string, error) { created = true; return "m1", nil }}
	_, err := s.createFromSchedule(context.Background(), Schedule{ID: "s1", MissionTemplate: MissionTemplate{Goal: "g", Kind: KindCoding, Light: true}})
	if err == nil || !errors.Is(err, ErrInvalidMission) {
		t.Fatalf("err = %v, want ErrInvalidMission", err)
	}
	if created {
		t.Fatal("Driver.Create reached for an invalid template")
	}
	if got := skipReasonFor(err); got != "fire_error" {
		t.Fatalf("skip reason = %q, want fire_error", got)
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
