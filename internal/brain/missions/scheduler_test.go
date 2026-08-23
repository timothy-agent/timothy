package missions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"
)

func TestDueDecision(t *testing.T) {
	// "0 9 * * *" = every day at 09:00.
	const dailyAt9 = "0 9 * * *"

	cases := []struct {
		name   string
		cron   string
		anchor time.Time
		now    time.Time
		grace  time.Duration
		want   decision
	}{
		{
			name:   "on-time fire: now is exactly at the next boundary",
			cron:   dailyAt9,
			anchor: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
			grace:  time.Hour,
			want:   decisionFire,
		},
		{
			name:   "not yet due",
			cron:   dailyAt9,
			anchor: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC),
			grace:  time.Hour,
			want:   decisionSkip,
		},
		{
			name:   "misfire within grace still fires",
			cron:   dailyAt9,
			anchor: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC), // 30 min late
			grace:  time.Hour,
			want:   decisionFire,
		},
		{
			name:   "misfire beyond grace skips but caller still advances last_run",
			cron:   dailyAt9,
			anchor: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC), // over a day late
			grace:  time.Hour,
			want:   decisionBackfillSkip,
		},
		{
			name:   "never-run schedule fires on its first eligible boundary",
			cron:   dailyAt9,
			anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), // e.g. schedule created at midnight
			now:    time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
			grace:  time.Hour,
			want:   decisionFire,
		},
		{
			name:   "invalid cron expression errors",
			cron:   "not a cron expression",
			anchor: time.Now(),
			now:    time.Now(),
			grace:  time.Hour,
			want:   decisionSkip,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dueDecision(tc.cron, tc.anchor, tc.now, tc.grace)
			if tc.name == "invalid cron expression errors" {
				if err == nil {
					t.Fatal("dueDecision accepted an invalid cron expression")
				}
				return
			}
			if err != nil {
				t.Fatalf("dueDecision: %v", err)
			}
			if got != tc.want {
				t.Fatalf("dueDecision = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveTemplateDefaults(t *testing.T) {
	t.Parallel()
	budget := 5.0

	cases := []struct {
		name          string
		template      MissionTemplate
		resolve       AgentResolver
		routeExists   func(context.Context, string) bool
		codingExec    func(context.Context) string
		wantRoute     string
		wantReview    string
		wantPlanRoute string
		wantBudget    *float64
		wantOverlay   string
		wantHarness   string
		wantKnowledge []string
	}{
		{
			name:       "nil resolver falls back to the default role's route",
			template:   MissionTemplate{Goal: "g", AgentID: "a1"},
			resolve:    nil,
			wantRoute:  "default",
			wantReview: "default",
		},
		{
			name:        "coding template with no harness applies the settings default",
			template:    MissionTemplate{Goal: "g", Kind: "coding", AgentID: "a1"},
			resolve:     nil,
			codingExec:  func(context.Context) string { return "claude-cli" },
			wantRoute:   "default",
			wantReview:  "default",
			wantHarness: "claude-cli",
		},
		{
			name:        "coding template's own harness is never overwritten",
			template:    MissionTemplate{Goal: "g", Kind: "coding", AgentID: "a1", Harness: "claude-cli"},
			resolve:     nil,
			codingExec:  func(context.Context) string { return "" },
			wantRoute:   "default",
			wantReview:  "default",
			wantHarness: "claude-cli",
		},
		{
			name:        "general template never applies the coding executor default",
			template:    MissionTemplate{Goal: "g", Kind: "general", AgentID: "a1"},
			resolve:     nil,
			codingExec:  func(context.Context) string { return "claude-cli" },
			wantRoute:   "default",
			wantReview:  "default",
			wantHarness: "",
		},
		{
			name:     "unresolved agent id falls back to the default role's route",
			template: MissionTemplate{Goal: "g", AgentID: "missing"},
			resolve: func(ctx context.Context, agentID string) (AgentDefaults, bool) {
				return AgentDefaults{}, false
			},
			wantRoute:  "default",
			wantReview: "default",
		},
		{
			name:     "empty template fields fill from resolved agent",
			template: MissionTemplate{Goal: "g", AgentID: "briefing"},
			resolve: func(ctx context.Context, agentID string) (AgentDefaults, bool) {
				return AgentDefaults{Route: "fast", ReviewRoute: "careful", BudgetAmount: &budget, PromptOverlay: "overlay text"}, true
			},
			wantRoute:   "fast",
			wantReview:  "careful",
			wantBudget:  &budget,
			wantOverlay: "overlay text",
		},
		{
			name:     "resolved agent's knowledge collections are returned",
			template: MissionTemplate{Goal: "g", AgentID: "briefing"},
			resolve: func(ctx context.Context, agentID string) (AgentDefaults, bool) {
				return AgentDefaults{Route: "fast", ReviewRoute: "careful", Knowledge: []string{"docs", "runbooks"}}, true
			},
			wantRoute:     "fast",
			wantReview:    "careful",
			wantKnowledge: []string{"docs", "runbooks"},
		},
		{
			name:     "unresolved agent id yields no knowledge",
			template: MissionTemplate{Goal: "g", AgentID: "missing"},
			resolve: func(ctx context.Context, agentID string) (AgentDefaults, bool) {
				return AgentDefaults{}, false
			},
			wantRoute:     "default",
			wantReview:    "default",
			wantKnowledge: nil,
		},
		{
			name:     "template's own non-empty fields are never overwritten",
			template: MissionTemplate{Goal: "g", AgentID: "briefing", Route: "explicit", ReviewRoute: "explicit-review"},
			resolve: func(ctx context.Context, agentID string) (AgentDefaults, bool) {
				return AgentDefaults{Route: "fast", ReviewRoute: "careful", PromptOverlay: "overlay text"}, true
			},
			wantRoute:   "explicit",
			wantReview:  "explicit-review",
			wantOverlay: "overlay text",
		},
		{
			name:        "coding template with no route prefers the coding route when it exists",
			template:    MissionTemplate{Goal: "g", Kind: "coding", AgentID: "a1"},
			resolve:     nil,
			routeExists: func(context.Context, string) bool { return true },
			wantRoute:   "coding",
			wantReview:  "default",
		},
		{
			name:        "coding template with no route falls back to default when coding route is absent",
			template:    MissionTemplate{Goal: "g", Kind: "coding", AgentID: "a1"},
			resolve:     nil,
			routeExists: func(context.Context, string) bool { return false },
			wantRoute:   "default",
			wantReview:  "default",
		},
		{
			name:        "coding template's own route is never overwritten by the coding preference",
			template:    MissionTemplate{Goal: "g", Kind: "coding", AgentID: "a1", Route: "explicit"},
			resolve:     nil,
			routeExists: func(context.Context, string) bool { return true },
			wantRoute:   "explicit",
			wantReview:  "default",
		},
		{
			// No agent-level plan_route equivalent exists: a template's
			// plan_route passes through completely untouched, never
			// defaulted from the resolved agent or the default role.
			name:     "plan_route passes through untouched, no agent-level default",
			template: MissionTemplate{Goal: "g", AgentID: "briefing", PlanRoute: "strong"},
			resolve: func(ctx context.Context, agentID string) (AgentDefaults, bool) {
				return AgentDefaults{Route: "fast", ReviewRoute: "careful"}, true
			},
			wantRoute:     "fast",
			wantReview:    "careful",
			wantPlanRoute: "strong",
		},
		{
			name:          "empty template plan_route stays empty",
			template:      MissionTemplate{Goal: "g", AgentID: "a1"},
			resolve:       nil,
			wantRoute:     "default",
			wantReview:    "default",
			wantPlanRoute: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			routeForRole := func(context.Context, string) string { return "default" }
			routeExists := tc.routeExists
			if tc.template.Kind != "coding" {
				// A general template must never even consult the coding
				// preference — proven by a routeExists that panics if
				// called, not just by asserting the resulting route.
				routeExists = func(context.Context, string) bool {
					t.Fatal("routeExists must not be called for a non-coding template")
					return false
				}
			}
			got, overlay, knowledge := resolveTemplateDefaults(context.Background(), tc.template, tc.resolve, routeForRole, routeExists, tc.codingExec)
			if got.Route != tc.wantRoute {
				t.Errorf("Route = %q, want %q", got.Route, tc.wantRoute)
			}
			if got.ReviewRoute != tc.wantReview {
				t.Errorf("ReviewRoute = %q, want %q", got.ReviewRoute, tc.wantReview)
			}
			if got.PlanRoute != tc.wantPlanRoute {
				t.Errorf("PlanRoute = %q, want %q", got.PlanRoute, tc.wantPlanRoute)
			}
			if (got.BudgetAmount == nil) != (tc.wantBudget == nil) {
				t.Errorf("BudgetAmount = %v, want %v", got.BudgetAmount, tc.wantBudget)
			} else if got.BudgetAmount != nil && *got.BudgetAmount != *tc.wantBudget {
				t.Errorf("BudgetAmount = %v, want %v", *got.BudgetAmount, *tc.wantBudget)
			}
			if overlay != tc.wantOverlay {
				t.Errorf("overlay = %q, want %q", overlay, tc.wantOverlay)
			}
			if got.Harness != tc.wantHarness {
				t.Errorf("Harness = %q, want %q", got.Harness, tc.wantHarness)
			}
			if !slices.Equal(knowledge, tc.wantKnowledge) {
				t.Errorf("knowledge = %v, want %v", knowledge, tc.wantKnowledge)
			}
		})
	}
}

// TestResolveTemplateDefaultsPassesLightThrough confirms light (D-069)
// is untouched by fire-time resolution — it has no agent-level default
// and no coding-only precedence the way harness/environment do.
func TestResolveTemplateDefaultsPassesLightThrough(t *testing.T) {
	t.Parallel()
	routeForRole := func(context.Context, string) string { return "default" }
	got, _, _ := resolveTemplateDefaults(context.Background(), MissionTemplate{Goal: "g", Kind: "general", Light: true}, nil, routeForRole, nil, nil)
	if !got.Light {
		t.Fatal("Light = false, want true (passed through unchanged)")
	}
}

func TestTickSkipsAllWorkWhenDisabled(t *testing.T) {
	t.Parallel()
	called := false
	s := &Scheduler{enabled: func(ctx context.Context) bool {
		called = true
		return false
	}}
	// db is nil: if tick proceeded past the enabled check it would
	// panic dereferencing s.db.Get, not merely fail — the disabled
	// short-circuit must return before any of that.
	if err := s.tick(context.Background(), time.Now()); err != nil {
		t.Fatalf("tick with scheduler disabled: %v", err)
	}
	if !called {
		t.Fatal("tick never consulted the enabled func")
	}
}

func TestTickNilEnabledDegradesOpen(t *testing.T) {
	t.Parallel()
	// A nil enabled func must NOT short-circuit tick (degrade open) —
	// this only checks the guard itself doesn't panic or skip; db is
	// nil, so a real proceed would panic on s.db.Get, proving the nil
	// check alone can't be mistaken for "disabled".
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("tick with nil enabled and nil db: want it to proceed past the guard and panic on the nil db, proving degrade-open")
		}
	}()
	s := &Scheduler{enabled: nil}
	_ = s.tick(context.Background(), time.Now())
}

// TestFilterDestinationIDs covers the fire-time re-check on a
// template's DestinationIDs: dropped ids never fail the fire, they're
// just excluded from what actually lands on the fired mission's row.
func TestFilterDestinationIDs(t *testing.T) {
	t.Parallel()

	t.Run("empty input returns nil without consulting the lookup", func(t *testing.T) {
		t.Parallel()
		s := &Scheduler{log: discardLog()}
		if got := s.filterDestinationIDs(context.Background(), nil); got != nil {
			t.Fatalf("filterDestinationIDs(nil) = %v, want nil", got)
		}
	})

	t.Run("nil destinationEnabled drops every id (destinations disabled)", func(t *testing.T) {
		t.Parallel()
		s := &Scheduler{log: discardLog()}
		got := s.filterDestinationIDs(context.Background(), []string{"d1", "d2"})
		if len(got) != 0 {
			t.Fatalf("filterDestinationIDs with nil destinationEnabled = %v, want empty", got)
		}
	})

	t.Run("drops disabled and missing ids, passes valid ones", func(t *testing.T) {
		t.Parallel()
		lookup := map[string]bool{"d1": true, "d2": false} // d3 absent entirely
		s := &Scheduler{
			log: discardLog(),
			destinationEnabled: func(_ context.Context, id string) (bool, error) {
				ok, exists := lookup[id]
				return exists && ok, nil
			},
		}
		got := s.filterDestinationIDs(context.Background(), []string{"d1", "d2", "d3"})
		if !slices.Equal(got, []string{"d1"}) {
			t.Fatalf("filterDestinationIDs = %v, want [d1]", got)
		}
	})

	t.Run("a lookup error drops that id without failing the fire", func(t *testing.T) {
		t.Parallel()
		s := &Scheduler{
			log: discardLog(),
			destinationEnabled: func(_ context.Context, id string) (bool, error) {
				if id == "d1" {
					return false, errors.New("db unreachable")
				}
				return true, nil
			},
		}
		got := s.filterDestinationIDs(context.Background(), []string{"d1", "d2"})
		if !slices.Equal(got, []string{"d2"}) {
			t.Fatalf("filterDestinationIDs = %v, want [d2]", got)
		}
	})
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
