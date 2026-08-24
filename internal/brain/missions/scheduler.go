package missions

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// schedulerTick is how often the scheduler evaluates every schedule.
const schedulerTick = time.Minute

// schedulerLockKey is this package's own advisory lock key ("MISS"),
// distinct from migrate.go's 0x54494D4F and store.go's
// workSlotLockKey ("TIMS") — schema migrations, work-slot claims, and
// schedule firing must never contend for the same lock.
const schedulerLockKey = 0x4D495353

// misfireGrace bounds how late a scheduled fire can run and still fire
// at all: beyond it, the tick skips silently and advances last_run
// anyway — at most one backfilled run after downtime, never a burst.
const misfireGrace = 1 * time.Hour

// Schedule is one schedules row.
type Schedule struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Cron            string          `json:"cron"`
	MissionTemplate MissionTemplate `json:"mission_template"`
	Enabled         bool            `json:"enabled"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
	LastRun         *time.Time      `json:"last_run,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	// PendingFire carries forward a fire that was due but skipped
	// because a mission from this schedule was still active (fireOne's
	// dedup) — cleared the moment it actually fires.
	PendingFire bool `json:"pending_fire"`
	// LastSkippedAt/SkipReason record the most recent skip (either
	// "backfill_grace" or "active_mission"), cleared on any successful
	// fire — nil/empty means the schedule's last due boundary fired
	// (or it has never been due).
	LastSkippedAt *time.Time `json:"last_skipped_at,omitempty"`
	SkipReason    string     `json:"skip_reason,omitempty"`
}

// MissionTemplate is applied verbatim as a new mission's initial
// columns each time its schedule fires.
type MissionTemplate struct {
	Goal string `json:"goal"`
	// Name, when set, becomes the created mission's display name
	// (Mission.Name — the UI/destination-delivery title) instead of the
	// schedule's own slug identifier. The schedule name stays a strict
	// lowercase slug for consistency with connectors/destinations/agents;
	// this lets a schedule read "Today's Meetings" in Telegram without
	// relaxing that.
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind"`
	AgentID     string `json:"agent_id"`
	Route       string `json:"route"`
	ReviewRoute string `json:"review_route"`
	// PlanRoute, when set, is the route explore/plan/replan/review run
	// on instead of Route (see missions.Mission.PlanRoute). "" means
	// Route covers everything.
	PlanRoute      string   `json:"plan_route,omitempty"`
	MaxIterations  int      `json:"max_iterations"`
	BudgetAmount   *float64 `json:"budget_amount,omitempty"`
	BudgetCurrency string   `json:"budget_currency,omitempty"`
	// Harness selects the execution strategy for a coding mission's
	// worker turns (D-051); empty applies the settings default at fire
	// time via resolveTemplateDefaults, same precedence as create()'s
	// own handling of an omitted request field.
	Harness string `json:"harness,omitempty"`
	// Light missions (D-069) skip explore/plan/review; only meaningful
	// on a kind=general template (rejected at schedule create/update for
	// kind=coding, api/schedules.go).
	Light bool `json:"light,omitempty"`
	// Environment selects the sandbox image key (D-05x) a coding
	// mission's container runs; empty auto-detects at fire time via
	// resolveTemplateDefaults (goal keyword only — no worktree exists
	// yet), same precedence as create()'s own handling.
	Environment string `json:"environment,omitempty"`
	// AutoApproveSafe defaults true for a scheduled mission, same as
	// api/missions.go's create handler — a mission fired unattended
	// needs the same standing shell approval a UI-created one gets by
	// default, or its very first shell call parks with nobody watching.
	AutoApproveSafe bool `json:"auto_approve_safe"`
	// DestinationIDs names operator-created destinations (D-061) this
	// template's fired missions deliver their outcome digest to.
	// Validated at schedule create/patch time (same rule as mission
	// create, api/missions.go's validateDestinationIDs); re-filtered at
	// FIRE time by resolveTemplateDefaults since a destination can be
	// deleted or disabled between when the schedule was made and when
	// it next fires.
	DestinationIDs []string `json:"destination_ids,omitempty"`
}

// AgentDefaults is the slice of an agents row a fired mission borrows
// when its template leaves the corresponding field empty — mirrors
// api/missions.go's create-handler resolution so a scheduler-fired
// mission gets the same defaults a UI-created one would.
type AgentDefaults struct {
	Route         string
	ReviewRoute   string
	PromptOverlay string
	// BudgetAmount is a plain fallback number: an agent-level default
	// carries no currency of its own, it always inherits whatever
	// currency the firing mission/request resolves to (request's
	// explicit budget_currency, or "USD" if that's also empty).
	BudgetAmount      *float64
	ApprovalAllowlist []string
	// Knowledge is the agent's kb_collections allowlist, snapshotted
	// onto the mission the same way PromptOverlay is (see
	// missions.Mission.Knowledge).
	Knowledge []string
}

// AgentResolver resolves an agent id to its defaults at FIRE time, not
// schedule-create time — an agent edited after the schedule was made
// (a new prompt overlay, a changed allowlist) must apply the moment it
// next fires, not freeze at whatever it was when the schedule was
// created. ok reports whether the id resolved to a real agent.
type AgentResolver func(ctx context.Context, agentID string) (AgentDefaults, bool)

// DestinationEnabled reports whether id names a real, enabled
// destinations row — the fire-time re-check resolveTemplateDefaults
// runs on a template's DestinationIDs, mirroring
// api/missions.go's validateDestinationIDs but tolerant instead of
// rejecting: a destination deleted or disabled since the schedule was
// created is dropped silently (with a log warning), never blocks the
// fire.
type DestinationEnabled func(ctx context.Context, id string) (bool, error)

// Scheduler ticks every schedulerTick, evaluating schedules rows
// against their cron expression (5-field, parsed via robfig/cron/v3's
// standard parser — parsing only, not its own goroutine scheduler: the
// tick+advisory-lock loop here matches the existing
// platform/migrate.go idiom rather than introducing a second
// scheduling paradigm).
type Scheduler struct {
	db                    *pgpool.Pool
	missions              *Store
	resolve               AgentResolver
	enabled               func(ctx context.Context) bool
	routeForRole          func(context.Context, string) string
	routeExists           func(context.Context, string) bool
	codingExecutorDefault func(context.Context) string
	destinationEnabled    DestinationEnabled
	log                   *slog.Logger

	// location resolves the operator's configured timezone: cron
	// expressions are evaluated in this zone (schedule.Next reads the
	// wall-clock fields of the time it's given), UTC when unset or
	// unwired. This is a deliberate behavior change from the prior
	// always-UTC evaluation (see SetLocation).
	location func(ctx context.Context) *time.Location
}

// NewScheduler wires the scheduler with the agent resolver
// createFromTemplate uses to fill in missing route/review_route/
// budget/prompt_overlay at fire time, the scheduler_enabled feature
// switch (D-032) that tick checks first, routeForRole (D-049) for
// the "default" system role's route when an agent's own route is also
// empty, routeExists for a coding template's route preferring the
// operator's "coding" route over "default" when it exists (see
// DefaultCodingRoute), codingExecutorDefault (D-051) for a coding
// template that omits harness, and destinationEnabled for the
// fire-time re-check of a template's DestinationIDs — nil-safe on all
// six: a nil resolve leaves an unresolved AgentID's fields at whatever
// the template already specified, a nil enabled defaults every tick to
// enabled (degrade open, since a config-read hiccup pausing every
// schedule silently would be worse than one that keeps firing), a
// nil/unbound routeForRole leaves the route "", a nil routeExists
// skips straight to the default route, a nil codingExecutorDefault
// leaves harness at whatever the template specified (native if
// empty), and a nil destinationEnabled leaves DestinationIDs
// unfiltered (destinations disabled entirely — nothing to check
// against).
func NewScheduler(db *pgpool.Pool, missions *Store, resolve AgentResolver, enabled func(ctx context.Context) bool, routeForRole func(context.Context, string) string, routeExists func(context.Context, string) bool, codingExecutorDefault func(context.Context) string, destinationEnabled DestinationEnabled, log *slog.Logger) *Scheduler {
	return &Scheduler{db: db, missions: missions, resolve: resolve, enabled: enabled, routeForRole: routeForRole, routeExists: routeExists, codingExecutorDefault: codingExecutorDefault, destinationEnabled: destinationEnabled, log: log}
}

// SetDestinationEnabled wires the fire-time destination re-check after
// construction — main.go builds the destinations store AFTER the
// scheduler (buildDestinations needs missionStore, built inside
// buildMissions), same late-wiring shape as Driver.SetDestinationDeliver.
func (s *Scheduler) SetDestinationEnabled(fn DestinationEnabled) {
	s.destinationEnabled = fn
}

// SetLocation wires the operator timezone accessor cron expressions
// are evaluated against, a setter for the same reason
// SetDestinationEnabled is.
func (s *Scheduler) SetLocation(loc func(ctx context.Context) *time.Location) {
	s.location = loc
}

// Run ticks forever until ctx is done. Double-fire protection across
// scaled instances: pg_try_advisory_xact_lock(schedulerLockKey) — a
// tick that doesn't get the lock skips silently, another instance
// already has it this minute.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(schedulerTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.tick(ctx, time.Now()); err != nil {
				s.log.Error("scheduler: tick failed", "error", err)
			}
		}
	}
}

// decision is the pure classification dueDecision produces for one
// schedule at a given moment.
type decision int

const (
	decisionSkip decision = iota
	decisionFire
	decisionBackfillSkip // due, but past misfireGrace: skip firing, still advance last_run
)

// dueDecision is the pure part of scheduling: given (cron expression,
// last run, now, grace), decide fire/skip/backfill-skip. Kept separate
// from tick's I/O so it's unit-testable without a Store. anchor is
// lastRun if set, else the schedule's created-at equivalent — callers
// pass whichever applies. The cron expression fires in whatever
// location anchor/now carry (schedule.Next reads their wall-clock
// fields): fireOne converts both into the operator's configured
// timezone before calling this, so cron fires in the operator
// timezone, UTC when unset.
func dueDecision(cronExpr string, anchor, now time.Time, grace time.Duration) (decision, error) {
	schedule, err := cron.ParseStandard(cronExpr)
	if err != nil {
		return decisionSkip, fmt.Errorf("parse cron %q: %w", cronExpr, err)
	}
	next := schedule.Next(anchor)
	if next.After(now) {
		return decisionSkip, nil
	}
	if now.Sub(next) > grace {
		return decisionBackfillSkip, nil
	}
	return decisionFire, nil
}

// tick evaluates every enabled, unexpired schedule once — but only
// when the scheduler_enabled feature switch is on: this is the toggle
// wire-up that was previously a no-op UI switch (see settings.KeyScheduler),
// checked before anything else in the tick so a disabled scheduler
// does no work at all, not even the advisory-lock attempt.
func (s *Scheduler) tick(ctx context.Context, now time.Time) error {
	if s.enabled != nil && !s.enabled(ctx) {
		return nil
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var acquired bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, int64(schedulerLockKey)).Scan(&acquired); err != nil {
		return fmt.Errorf("scheduler: advisory lock: %w", err)
	}
	if !acquired {
		return tx.Commit(ctx) // another instance already owns this tick
	}

	rows, err := tx.Query(ctx, `SELECT id, name, cron, mission_template, enabled, expires_at, last_run, created_at, updated_at, pending_fire
		FROM schedules WHERE enabled AND (expires_at IS NULL OR expires_at > now())`)
	if err != nil {
		return fmt.Errorf("scheduler: query schedules: %w", err)
	}
	var schedules []Schedule
	for rows.Next() {
		var sc Schedule
		var templateJSON []byte
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.Cron, &templateJSON, &sc.Enabled, &sc.ExpiresAt, &sc.LastRun, &sc.CreatedAt, &sc.UpdatedAt, &sc.PendingFire); err != nil {
			rows.Close()
			return fmt.Errorf("scheduler: scan schedule: %w", err)
		}
		_ = json.Unmarshal(templateJSON, &sc.MissionTemplate)
		schedules = append(schedules, sc)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("scheduler: schedules rows: %w", err)
	}

	for _, sc := range schedules {
		if err := s.fireOne(ctx, tx, sc, now); err != nil {
			s.log.Error("scheduler: schedule firing failed", "schedule_id", sc.ID, "name", sc.Name, "error", err)
		}
	}
	return tx.Commit(ctx)
}

// fireOne evaluates and, if due (or carrying a pending fire from an
// earlier dedup skip), fires one schedule at most once — inside the
// same transaction tick holds, so last_run/pending_fire/skip fields and
// any created mission commit atomically together.
func (s *Scheduler) fireOne(ctx context.Context, tx pgx.Tx, sc Schedule, now time.Time) error {
	// A never-run schedule anchors on its own creation, not "now" —
	// otherwise Next(now) is always in the future and it could never
	// fire on its very first eligible boundary.
	anchor := sc.CreatedAt
	if sc.LastRun != nil {
		anchor = *sc.LastRun
	}

	// Cron expressions are evaluated in the operator's configured
	// timezone (UTC when unset), not the container's local time: only
	// dueDecision's inputs change here, last_run/last_skipped_at below
	// still store the original instant.
	loc := time.UTC
	if s.location != nil {
		if l := s.location(ctx); l != nil {
			loc = l
		}
	}
	dec, err := dueDecision(sc.Cron, anchor.In(loc), now.In(loc), misfireGrace)
	if err != nil {
		return err
	}

	// Backfill skip: past misfireGrace, never fires for this boundary —
	// still advance last_run (so the NEXT boundary computes from now,
	// not the stale anchor) and record why, but pending_fire is
	// untouched (a stale pending fire from an EARLIER dedup skip must
	// still get its own chance below, not be silently dropped by this
	// unrelated late boundary).
	if dec == decisionBackfillSkip {
		if err := s.markSkipped(ctx, tx, sc, now, "backfill_grace"); err != nil {
			return err
		}
		return s.tryFirePending(ctx, tx, sc, now)
	}
	if dec == decisionSkip {
		return s.tryFirePending(ctx, tx, sc, now)
	}

	// dec == decisionFire: due this tick. Whether or not a pending fire
	// from an earlier tick is also carried, only ONE mission fires —
	// firing the current due boundary also satisfies whatever was
	// pending, so pending_fire clears here rather than firing twice.
	active, err := s.activeMissionExists(ctx, tx, sc.ID)
	if err != nil {
		return err
	}
	if active {
		// Live-queue dedup: a mission from THIS schedule still active
		// means firing again would pile up parallel runs of the same
		// job — skip firing, carry it forward as pending, but still
		// advance last_run so the next boundary computes from now.
		if _, err := tx.Exec(ctx, `UPDATE schedules SET
				last_run = $2, pending_fire = true, last_skipped_at = $2, skip_reason = $3, updated_at = now()
			WHERE id = $1`, sc.ID, now, "active_mission"); err != nil {
			return fmt.Errorf("advance last_run (dedup skip): %w", err)
		}
		s.log.Info("scheduler: skipped firing, a mission from this schedule is still active", "schedule_id", sc.ID)
		return nil
	}
	if err := s.createFromTemplate(ctx, tx, sc); err != nil {
		return fmt.Errorf("create mission: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE schedules SET
			last_run = $2, pending_fire = false, last_skipped_at = NULL, skip_reason = '', updated_at = now()
		WHERE id = $1`, sc.ID, now); err != nil {
		return fmt.Errorf("advance last_run: %w", err)
	}
	return nil
}

// tryFirePending fires a carried-forward pending fire when no mission
// from this schedule is currently active — the other half of the dedup
// skip's carryover: the tick that skipped set pending_fire, and every
// SUBSEQUENT tick (not just the one right after) checks it here until
// the active mission finally clears. A no-op when pending_fire is
// false, or when a mission is still active.
func (s *Scheduler) tryFirePending(ctx context.Context, tx pgx.Tx, sc Schedule, now time.Time) error {
	if !sc.PendingFire {
		return nil
	}
	active, err := s.activeMissionExists(ctx, tx, sc.ID)
	if err != nil {
		return err
	}
	if active {
		return nil
	}
	if err := s.createFromTemplate(ctx, tx, sc); err != nil {
		return fmt.Errorf("create mission (pending fire): %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE schedules SET
			pending_fire = false, last_skipped_at = NULL, skip_reason = '', updated_at = now()
		WHERE id = $1`, sc.ID); err != nil {
		return fmt.Errorf("clear pending_fire: %w", err)
	}
	return nil
}

// activeMissionExists reports whether a mission from schedule id is
// still in a non-terminal phase.
func (s *Scheduler) activeMissionExists(ctx context.Context, tx pgx.Tx, scheduleID string) (bool, error) {
	var activeCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM missions
		WHERE schedule_id = $1 AND phase NOT IN ('done', 'failed')`, scheduleID).Scan(&activeCount); err != nil {
		return false, fmt.Errorf("check active missions: %w", err)
	}
	return activeCount > 0, nil
}

// markSkipped records a backfill-grace skip's reason/time — last_run
// still advances so the next boundary computes from now, not the stale
// anchor (same rule the pre-existing backfill-skip path always followed).
func (s *Scheduler) markSkipped(ctx context.Context, tx pgx.Tx, sc Schedule, now time.Time, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE schedules SET
			last_run = $2, last_skipped_at = $2, skip_reason = $3, updated_at = now()
		WHERE id = $1`, sc.ID, now, reason); err != nil {
		return fmt.Errorf("advance last_run (backfill skip): %w", err)
	}
	return nil
}

// createFromTemplate inserts a new mission from the schedule's
// template, tagged with the schedule that spawned it. It resolves the
// template's agent_id AT FIRE TIME (mirroring api/missions.go's create
// handler): an empty route/review_route/budget/prompt_overlay is
// filled from the agent's current defaults, and an agent's own empty
// route (its "use the default chain" shorthand) still falls back to
// the "default" system role's route since the gateway requires a real
// route name. This inserted row bypasses Driver.Create entirely — it
// has no session, no workspace, no grants yet — so it is provisioned
// lazily the first time Advance/Drive touches it (see driver.go's
// ensureProvisioned).
// D-071: this inserts directly via SQL rather than Driver.Create, so
// ValidateCreate never runs against a schedule-fired mission — left as
// is for this slice. resolveTemplateDefaults (route/harness/environment)
// and filterDestinationIDs (destination_ids existence) already cover
// this insert's subset of ValidateCreate's checks; a schedule's own
// create/patch validation (schedules.go) covers the rest at schedule
// creation time.
func (s *Scheduler) createFromTemplate(ctx context.Context, tx pgx.Tx, sc Schedule) error {
	t, promptOverlay, knowledge := resolveTemplateDefaults(ctx, sc.MissionTemplate, s.resolve, s.routeForRole, s.routeExists, s.codingExecutorDefault)
	// destination_ids is NOT NULL; a nil slice binds as a NULL array
	// parameter, same fix as store.go's Create.
	destinationIDs := s.filterDestinationIDs(ctx, t.DestinationIDs)
	if destinationIDs == nil {
		destinationIDs = []string{}
	}
	spec, _ := json.Marshal(Spec{})
	budgetCurrency := t.BudgetCurrency
	if budgetCurrency == "" {
		budgetCurrency = "USD"
	}
	if knowledge == nil {
		knowledge = []string{}
	}
	knowledgeJSON, err := json.Marshal(knowledge)
	if err != nil {
		return fmt.Errorf("marshal knowledge: %w", err)
	}
	name := t.Name
	if name == "" {
		name = sc.Name
	}
	phase := initialPhase(t.Kind, t.Light)
	_, err = tx.Exec(ctx, `INSERT INTO missions
			(goal, name, kind, agent_id, max_iterations, budget_amount, budget_currency, route, review_route, plan_route, prompt_overlay, knowledge, auto_approve_safe, spec, schedule_id, harness, environment, destination_ids, light, phase)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`,
		t.Goal, name, t.Kind, t.AgentID, orDefault(t.MaxIterations, 3), t.BudgetAmount, budgetCurrency, t.Route, t.ReviewRoute, t.PlanRoute,
		promptOverlay, knowledgeJSON, t.AutoApproveSafe, spec, sc.ID, t.Harness, t.Environment, destinationIDs, t.Light, phase)
	return err
}

// filterDestinationIDs is the fire-time re-check on a template's
// DestinationIDs: a destination deleted or disabled since the
// schedule was created must not silently attach to the new mission
// row, but must also never fail the fire — it's dropped and logged
// instead. A nil destinationEnabled (destinations disabled) drops
// every id, since none of them can be verified to still be valid.
func (s *Scheduler) filterDestinationIDs(ctx context.Context, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	if s.destinationEnabled == nil {
		s.log.Warn("scheduler: dropping schedule destination_ids, destinations are not enabled", "destination_ids", ids)
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		ok, err := s.destinationEnabled(ctx, id)
		if err != nil {
			s.log.Warn("scheduler: dropping schedule destination id, lookup failed", "destination_id", id, "error", err)
			continue
		}
		if !ok {
			s.log.Warn("scheduler: dropping schedule destination id, unknown or disabled", "destination_id", id)
			continue
		}
		out = append(out, id)
	}
	return out
}

// resolveTemplateDefaults is the pure fire-time resolution step
// createFromTemplate performs, split out so it's unit-testable without
// a real pgx.Tx: an empty route/review_route/budget is filled from the
// resolved agent's current defaults (nil-safe — a nil resolve, or one
// that reports ok=false, leaves the template exactly as given except
// for the final default-role fallback), the resolved agent's prompt
// overlay is returned separately since MissionTemplate itself carries
// no such field, a coding template's still-empty route prefers the
// operator's "coding" route over "default" when it exists
// (DefaultCodingRoute), and a coding template that omits harness gets
// the settings default (D-051) — mirrors api/missions.go create()'s
// own precedence so a scheduler-fired mission inherits it too.
func resolveTemplateDefaults(ctx context.Context, t MissionTemplate, resolve AgentResolver, routeForRole func(context.Context, string) string, routeExists func(context.Context, string) bool, codingExecutorDefault func(context.Context) string) (MissionTemplate, string, []string) {
	promptOverlay := ""
	var knowledge []string
	if resolve != nil {
		if defaults, ok := resolve(ctx, t.AgentID); ok {
			if t.Route == "" {
				t.Route = defaults.Route
			}
			if t.ReviewRoute == "" {
				t.ReviewRoute = defaults.ReviewRoute
			}
			if t.BudgetAmount == nil {
				t.BudgetAmount = defaults.BudgetAmount
			}
			promptOverlay = defaults.PromptOverlay
			knowledge = defaults.Knowledge
		}
	}
	policy := policyFor(t.Kind, t.Light)
	defaultRoute := ""
	if routeForRole != nil {
		defaultRoute = routeForRole(ctx, "default")
	}
	if t.Route == "" {
		if t.Kind == KindCoding {
			t.Route = DefaultCodingRoute(ctx, routeExists, defaultRoute)
		} else {
			t.Route = defaultRoute
		}
	}
	if t.ReviewRoute == "" {
		// Same masking guard as the create handler: an explicit
		// plan_route covers review unless review_route was set itself.
		if t.PlanRoute != "" {
			t.ReviewRoute = t.PlanRoute
		} else {
			t.ReviewRoute = defaultRoute
		}
	}
	if policy.canDelegate && t.Harness == "" && codingExecutorDefault != nil {
		t.Harness = codingExecutorDefault(ctx)
	}
	// Environment (D-05x): no settings default (unlike Harness above) —
	// an omitted template auto-detects from the goal at fire time. No
	// worktree exists yet, so only the goal-keyword heuristic can fire;
	// repo-marker detection has nothing to check.
	if policy.needsWorktree && t.Environment == "" {
		t.Environment, _ = DetectEnvironment("", t.Goal)
	}
	return t, promptOverlay, knowledge
}
