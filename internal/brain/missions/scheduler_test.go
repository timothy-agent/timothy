package missions

import (
	"context"
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
		name        string
		template    MissionTemplate
		resolve     AgentResolver
		wantRoute   string
		wantReview  string
		wantBudget  *float64
		wantOverlay string
	}{
		{
			name:       "nil resolver falls back to the default role's route",
			template:   MissionTemplate{Goal: "g", AgentID: "a1"},
			resolve:    nil,
			wantRoute:  "default",
			wantReview: "default",
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
			name:     "template's own non-empty fields are never overwritten",
			template: MissionTemplate{Goal: "g", AgentID: "briefing", Route: "explicit", ReviewRoute: "explicit-review"},
			resolve: func(ctx context.Context, agentID string) (AgentDefaults, bool) {
				return AgentDefaults{Route: "fast", ReviewRoute: "careful", PromptOverlay: "overlay text"}, true
			},
			wantRoute:   "explicit",
			wantReview:  "explicit-review",
			wantOverlay: "overlay text",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			routeForRole := func(context.Context, string) string { return "default" }
			got, overlay := resolveTemplateDefaults(context.Background(), tc.template, tc.resolve, routeForRole)
			if got.Route != tc.wantRoute {
				t.Errorf("Route = %q, want %q", got.Route, tc.wantRoute)
			}
			if got.ReviewRoute != tc.wantReview {
				t.Errorf("ReviewRoute = %q, want %q", got.ReviewRoute, tc.wantReview)
			}
			if (got.BudgetAmount == nil) != (tc.wantBudget == nil) {
				t.Errorf("BudgetAmount = %v, want %v", got.BudgetAmount, tc.wantBudget)
			} else if got.BudgetAmount != nil && *got.BudgetAmount != *tc.wantBudget {
				t.Errorf("BudgetAmount = %v, want %v", *got.BudgetAmount, *tc.wantBudget)
			}
			if overlay != tc.wantOverlay {
				t.Errorf("overlay = %q, want %q", overlay, tc.wantOverlay)
			}
		})
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
