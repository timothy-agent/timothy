package missions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// TestDueDecisionNonUTCLocation proves cron evaluates against whatever
// location anchor/now carry, not the process's local time: "0 8 * * *"
// fires at 08:00 Europe/Amsterdam, which is 06:00 UTC in August (CEST,
// UTC+2), a caller (fireOne) that passed the raw UTC instants instead
// would see the boundary two hours late.
func TestDueDecisionNonUTCLocation(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	const dailyAt8 = "0 8 * * *"
	anchor := time.Date(2026, 8, 1, 8, 0, 0, 0, loc)

	// 06:00 UTC == 08:00 CEST: due.
	now := time.Date(2026, 8, 2, 6, 0, 0, 0, time.UTC).In(loc)
	got, err := dueDecision(dailyAt8, anchor, now, time.Hour)
	if err != nil {
		t.Fatalf("dueDecision: %v", err)
	}
	if got != decisionFire {
		t.Fatalf("dueDecision = %v, want decisionFire (08:00 CEST boundary)", got)
	}

	// 06:00 UTC == 07:00 CEST-equivalent wall clock one hour early: not yet due.
	early := time.Date(2026, 8, 2, 5, 0, 0, 0, time.UTC).In(loc)
	got, err = dueDecision(dailyAt8, anchor, early, time.Hour)
	if err != nil {
		t.Fatalf("dueDecision: %v", err)
	}
	if got != decisionSkip {
		t.Fatalf("dueDecision = %v, want decisionSkip (07:00 CEST, before boundary)", got)
	}
}

// TestTemplateCreateRequestResolvesDefaults runs a template through the
// scheduler's builder and ResolveDefaults, the path every fire takes.
func TestTemplateCreateRequestResolvesDefaults(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		template      MissionTemplate
		resolve       AgentResolver
		routeExists   func(context.Context, string) bool
		codingExec    func(context.Context) string
		wantRoute     string
		wantReview    string
		wantPlanRoute string
		wantOverlay   string
		wantHarness   string
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
			name:     "resolved agent's harness applies at fire time, ahead of the settings default",
			template: MissionTemplate{Goal: "g", Kind: "coding", AgentID: "coder"},
			resolve: func(ctx context.Context, agentID string) (AgentDefaults, bool) {
				return AgentDefaults{Route: "coding", Harness: "pi"}, true
			},
			codingExec:  func(context.Context) string { return "claude-cli" },
			wantRoute:   "coding",
			wantReview:  "default",
			wantHarness: "pi",
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
				return AgentDefaults{Route: "fast", ReviewRoute: "careful", PromptOverlay: "overlay text"}, true
			},
			wantRoute:   "fast",
			wantReview:  "careful",
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
			deps := ResolveDeps{Agent: tc.resolve, RouteForRole: routeForRole, RouteExists: routeExists, CodingExecutorDefault: tc.codingExec}
			got, err := ResolveDefaults(context.Background(), TemplateCreateRequest(Schedule{MissionTemplate: tc.template}, nil), deps)
			if err != nil {
				t.Fatalf("ResolveDefaults: %v", err)
			}
			overlay := got.PromptOverlay
			if got.Route != tc.wantRoute {
				t.Errorf("Route = %q, want %q", got.Route, tc.wantRoute)
			}
			if got.ReviewRoute != tc.wantReview {
				t.Errorf("ReviewRoute = %q, want %q", got.ReviewRoute, tc.wantReview)
			}
			if got.PlanRoute != tc.wantPlanRoute {
				t.Errorf("PlanRoute = %q, want %q", got.PlanRoute, tc.wantPlanRoute)
			}
			if overlay != tc.wantOverlay {
				t.Errorf("overlay = %q, want %q", overlay, tc.wantOverlay)
			}
			if got.Harness != tc.wantHarness {
				t.Errorf("Harness = %q, want %q", got.Harness, tc.wantHarness)
			}
		})
	}
}

// TestTemplateCreateRequestLightMapsToLightFlow confirms a light
// template (D-069) fires as flow=light.
func TestTemplateCreateRequestLightMapsToLightFlow(t *testing.T) {
	t.Parallel()
	deps := ResolveDeps{RouteForRole: func(context.Context, string) string { return "default" }}
	got, err := ResolveDefaults(context.Background(), TemplateCreateRequest(Schedule{MissionTemplate: MissionTemplate{Goal: "g", Kind: "general", Light: true}}, nil), deps)
	if err != nil {
		t.Fatalf("ResolveDefaults: %v", err)
	}
	if got.Flow != FlowLight {
		t.Fatalf("Flow = %q, want %q", got.Flow, FlowLight)
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

func TestSkipReasonFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"pre-check miss", fmt.Errorf("create mission: %w", fmt.Errorf("agent %q: %w", "a1", errAgentMissing)), "agent_missing"},
		{"agent_id foreign key violation", fmt.Errorf("create mission: %w", &pgconn.PgError{Code: "23503", ConstraintName: "missions_agent_id_fkey"}), "agent_missing"},
		{"other foreign key violation", &pgconn.PgError{Code: "23503", ConstraintName: "missions_schedule_id_fkey"}, "fire_error"},
		{"check violation", &pgconn.PgError{Code: "23514", ConstraintName: "missions_kind_check"}, "fire_error"},
		{"aborted transaction", &pgconn.PgError{Code: "25P02"}, "fire_error"},
		{"plain error", errors.New("parse cron"), "fire_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := skipReasonFor(tc.err); got != tc.want {
				t.Fatalf("skipReasonFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// abortingTx is a pgx.Tx fake that models Postgres's aborted
// transaction state: once a statement fails, every later statement
// returns 25P02 until ROLLBACK TO SAVEPOINT clears it.
type abortingTx struct {
	pgx.Tx
	aborted  bool
	failFor  map[string]error // INSERT arg -> error it fails with
	executed []string
}

func (f *abortingTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	sql = strings.Join(strings.Fields(sql), " ")
	if strings.HasPrefix(sql, "ROLLBACK TO SAVEPOINT") {
		f.aborted = false
		f.executed = append(f.executed, sql)
		return pgconn.CommandTag{}, nil
	}
	if f.aborted {
		return pgconn.CommandTag{}, &pgconn.PgError{Code: "25P02", Message: "current transaction is aborted"}
	}
	if strings.HasPrefix(sql, "INSERT") && len(args) > 0 {
		if err, ok := f.failFor[args[0].(string)]; ok {
			f.aborted = true
			return pgconn.CommandTag{}, err
		}
	}
	entry := sql
	if strings.HasPrefix(sql, "INSERT") {
		entry = fmt.Sprintf("INSERT %v", args[0])
	}
	if strings.HasPrefix(sql, "UPDATE schedules") {
		entry = fmt.Sprintf("SKIP %v %v", args[0], args[2])
	}
	f.executed = append(f.executed, entry)
	return pgconn.CommandTag{}, nil
}

// insertFire stands in for fireOne: one INSERT keyed by schedule id.
func insertFire(ctx context.Context, tx pgx.Tx, sc Schedule, _ time.Time) error {
	if _, err := tx.Exec(ctx, "INSERT INTO missions", sc.ID); err != nil {
		return fmt.Errorf("create mission: %w", err)
	}
	return nil
}

func filterExecuted(executed []string, prefixes ...string) []string {
	var out []string
	for _, e := range executed {
		for _, p := range prefixes {
			if strings.HasPrefix(e, p) {
				out = append(out, e)
			}
		}
	}
	return out
}

func threeSchedules() []Schedule {
	return []Schedule{{ID: "s1"}, {ID: "s2"}, {ID: "s3"}}
}

func TestFireEachSavepointControlFlow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		failFor   map[string]error
		wantFires []string
		wantSkips []string
	}{
		{
			name:      "all succeed",
			wantFires: []string{"INSERT s1", "INSERT s2", "INSERT s3"},
		},
		{
			name:      "middle agent missing",
			failFor:   map[string]error{"s2": &pgconn.PgError{Code: "23503", ConstraintName: "missions_agent_id_fkey"}},
			wantFires: []string{"INSERT s1", "INSERT s3"},
			wantSkips: []string{"SKIP s2 agent_missing"},
		},
		{
			name:      "first fails with another error",
			failFor:   map[string]error{"s1": &pgconn.PgError{Code: "23514", ConstraintName: "missions_kind_check"}},
			wantFires: []string{"INSERT s2", "INSERT s3"},
			wantSkips: []string{"SKIP s1 fire_error"},
		},
		{
			name: "all fail",
			failFor: map[string]error{
				"s1": &pgconn.PgError{Code: "23514"},
				"s2": &pgconn.PgError{Code: "23503", ConstraintName: "missions_agent_id_fkey"},
				"s3": &pgconn.PgError{Code: "23514"},
			},
			wantSkips: []string{"SKIP s1 fire_error", "SKIP s2 agent_missing", "SKIP s3 fire_error"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tx := &abortingTx{failFor: tc.failFor}
			s := &Scheduler{log: discardLog()}
			if err := s.fireEach(context.Background(), tx, threeSchedules(), time.Now(), insertFire); err != nil {
				t.Fatalf("fireEach: %v", err)
			}
			if got := filterExecuted(tx.executed, "INSERT"); !slices.Equal(got, tc.wantFires) {
				t.Fatalf("fires = %v, want %v", got, tc.wantFires)
			}
			if got := filterExecuted(tx.executed, "SKIP"); !slices.Equal(got, tc.wantSkips) {
				t.Fatalf("skips = %v, want %v", got, tc.wantSkips)
			}
			if got := len(filterExecuted(tx.executed, "SAVEPOINT")); got != 3 {
				t.Fatalf("savepoints = %d, want 3", got)
			}
			if got := len(filterExecuted(tx.executed, "RELEASE SAVEPOINT")); got != 3 {
				t.Fatalf("releases = %d, want 3", got)
			}
			if got := len(filterExecuted(tx.executed, "ROLLBACK TO SAVEPOINT")); got != len(tc.wantSkips) {
				t.Fatalf("rollbacks = %d, want %d", got, len(tc.wantSkips))
			}
		})
	}
}

// TestFireEachSecondOfThreeFailsLeavesTwoFiresOneSkip covers the
// missing-agent pre-check path: no DB error at all, just the sentinel.
func TestFireEachSecondOfThreeFailsLeavesTwoFiresOneSkip(t *testing.T) {
	t.Parallel()
	tx := &abortingTx{}
	fire := func(ctx context.Context, tx pgx.Tx, sc Schedule, now time.Time) error {
		if sc.ID == "s2" {
			return fmt.Errorf("create mission: %w", errAgentMissing)
		}
		return insertFire(ctx, tx, sc, now)
	}
	s := &Scheduler{log: discardLog()}
	if err := s.fireEach(context.Background(), tx, threeSchedules(), time.Now(), fire); err != nil {
		t.Fatalf("fireEach: %v", err)
	}
	if got := filterExecuted(tx.executed, "INSERT"); !slices.Equal(got, []string{"INSERT s1", "INSERT s3"}) {
		t.Fatalf("fires = %v, want [INSERT s1 INSERT s3]", got)
	}
	if got := filterExecuted(tx.executed, "SKIP"); !slices.Equal(got, []string{"SKIP s2 agent_missing"}) {
		t.Fatalf("skips = %v, want [SKIP s2 agent_missing]", got)
	}
}

// TestSchedulerRegression815PoisonedScheduleDoesNotCascade reproduces
// issue #815: before per-schedule savepoints, a failed INSERT for the
// second schedule left the shared transaction aborted, so the third
// schedule's INSERT and every skip update returned 25P02 and nothing
// fired. abortingTx models that abort state.
func TestSchedulerRegression815PoisonedScheduleDoesNotCascade(t *testing.T) {
	t.Parallel()
	poison := &pgconn.PgError{Code: "23503", ConstraintName: "missions_agent_id_fkey"}

	// Without savepoints (the pre-fix loop), the fake cascades.
	pre := &abortingTx{failFor: map[string]error{"s2": poison}}
	var preErrs int
	for _, sc := range threeSchedules() {
		if err := insertFire(context.Background(), pre, sc, time.Now()); err != nil {
			preErrs++
		}
	}
	if preErrs != 2 {
		t.Fatalf("pre-fix loop errors = %d, want 2 (s2 fails, s3 hits 25P02)", preErrs)
	}

	tx := &abortingTx{failFor: map[string]error{"s2": poison}}
	s := &Scheduler{log: discardLog()}
	if err := s.fireEach(context.Background(), tx, threeSchedules(), time.Now(), insertFire); err != nil {
		t.Fatalf("fireEach: %v", err)
	}
	if tx.aborted {
		t.Fatal("transaction left aborted after fireEach, commit would fail")
	}
	if got := filterExecuted(tx.executed, "INSERT"); !slices.Equal(got, []string{"INSERT s1", "INSERT s3"}) {
		t.Fatalf("fires = %v, want [INSERT s1 INSERT s3]", got)
	}
	if got := filterExecuted(tx.executed, "SKIP"); !slices.Equal(got, []string{"SKIP s2 agent_missing"}) {
		t.Fatalf("skips = %v, want [SKIP s2 agent_missing]", got)
	}
}
