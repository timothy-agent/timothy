package missions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	// LastSkippedAt/SkipReason record the most recent skip
	// ("backfill_grace", "active_mission", "agent_missing" or
	// "fire_error"), cleared on any successful
	// fire; nil/empty means the schedule's last due boundary fired
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
	// schedule's own name, which is free text but may still be a plain
	// identifier like "inbox-digest-8h"; this lets a schedule read
	// "Today's Meetings" in Telegram regardless.
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind"`
	AgentID     string `json:"agent_id"`
	Route       string `json:"route"`
	ReviewRoute string `json:"review_route"`
	// PlanRoute, when set, is the route discover/plan/replan/prove run
	// on instead of Route (see missions.Mission.PlanRoute). "" means
	// Route covers everything.
	PlanRoute      string   `json:"plan_route,omitempty"`
	MaxIterations  int      `json:"max_iterations"`
	BudgetAmount   *float64 `json:"budget_amount,omitempty"`
	BudgetCurrency string   `json:"budget_currency,omitempty"`
	// Harness selects the execution strategy for a coding mission's
	// worker turns (D-051); empty resolves at fire time through
	// ResolveDefaults, the same precedence a one-off create gets.
	Harness string `json:"harness,omitempty"`
	// ReviewHarness (issue #582) is copied onto the fired mission as-is:
	// the registered executor its review round runs as a read-only
	// delegated CLI, "" for the native reviewer. No settings default.
	ReviewHarness string `json:"review_harness,omitempty"`
	// Light missions (D-069) skip discover/plan/prove; only meaningful
	// on a kind=general template (rejected at schedule create/update for
	// kind=coding, api/schedules.go).
	Light bool `json:"light,omitempty"`
	// Environment selects the sandbox image key (D-05x) a coding
	// mission's container runs; empty is detected against the real
	// workspace once it exists (issue #495).
	Environment string `json:"environment,omitempty"`
	// AutoApproveTools defaults true for a scheduled mission, same as
	// api/missions.go's create handler — a mission fired unattended
	// needs the same standing shell approval a UI-created one gets by
	// default, or its very first shell call parks with nobody watching.
	AutoApproveTools bool `json:"auto_approve_tools"`
	// DestinationIDs names operator-created destinations (D-061) this
	// template's fired missions deliver their outcome digest to.
	// Validated at schedule create/patch time (same rule as mission
	// create, api/missions.go's validateDestinationIDs); re-filtered at
	// FIRE time by filterDestinationIDs since a destination can be
	// deleted or disabled between when the schedule was made and when
	// it next fires.
	DestinationIDs []string `json:"destination_ids,omitempty"`
	// Attachments are this template's documents/images/audio clips
	// (issue #359), resolved into markdown/caption/transcript at
	// schedule create/patch time (api/schedules.go) rather than on
	// every fire, so a fired mission spends nothing to attach them.
	// TemplateCreateRequest copies these straight onto the new mission's
	// sources.
	Attachments []SourceEntry `json:"attachments,omitempty"`
}

// AgentDefaults is the slice of an agents row a new mission borrows
// when its create request leaves the corresponding field empty
// (ResolveDefaults), and the provisioning-time grants the driver reads.
type AgentDefaults struct {
	// Name is the agent's display name, used to label a mission's
	// turns in the timeline (issue #473), resolved fresh here rather
	// than snapshotted on the mission, same freshness reasoning as the
	// rest of this struct.
	Name              string
	Route             string
	ReviewRoute       string
	PromptOverlay     string
	ApprovalAllowlist []string
	// Harness is the agent's own harness field, consulted by
	// ResolveDefaults/ResolveHarness ahead of
	// settings.coding_executor (mission.harness -> agent.harness ->
	// settings.coding_executor -> native). Empty means the agent
	// doesn't override.
	Harness string
}

// AgentResolver resolves an agent id to its defaults at FIRE time, not
// schedule-create time — an agent edited after the schedule was made
// (a new prompt overlay, a changed allowlist) must apply the moment it
// next fires, not freeze at whatever it was when the schedule was
// created. ok reports whether the id resolved to a real agent.
type AgentResolver func(ctx context.Context, agentID string) (AgentDefaults, bool)

// DestinationEnabled reports whether id names a real, enabled
// destinations row, the fire-time re-check filterDestinationIDs
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
	db *pgpool.Pool
	// create is Driver.Create: validate, insert, provision, Drive.
	create             func(ctx context.Context, m Mission) (string, error)
	resolve            ResolveDeps
	enabled            func(ctx context.Context) bool
	destinationEnabled DestinationEnabled
	log                *slog.Logger

	// location resolves the operator's configured timezone: cron
	// expressions are evaluated in this zone (schedule.Next reads the
	// wall-clock fields of the time it's given), UTC when unset or
	// unwired. This is a deliberate behavior change from the prior
	// always-UTC evaluation (see SetLocation).
	location func(ctx context.Context) *time.Location
}

// NewScheduler wires the scheduler. create is Driver.Create, the only
// way a fire makes a mission (issue #816); resolve is the same
// ResolveDeps every other create path uses; enabled is the
// scheduler_enabled feature switch (D-032) tick checks first, nil
// degrades open; destinationEnabled is the fire-time re-check of a
// template's DestinationIDs, nil drops every id.
func NewScheduler(db *pgpool.Pool, create func(ctx context.Context, m Mission) (string, error), resolve ResolveDeps, enabled func(ctx context.Context) bool, destinationEnabled DestinationEnabled, log *slog.Logger) *Scheduler {
	return &Scheduler{db: db, create: create, resolve: resolve, enabled: enabled, destinationEnabled: destinationEnabled, log: log}
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

	var fired []Schedule
	fire := func(ctx context.Context, tx pgx.Tx, sc Schedule, now time.Time) error {
		due, err := s.fireOne(ctx, tx, sc, now)
		if err == nil && due {
			fired = append(fired, sc)
		}
		return err
	}
	if err := s.fireEach(ctx, tx, schedules, now, fire); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("scheduler: commit: %w", err)
	}
	// Missions are created only after the bookkeeping commits, so a
	// fire is at most once: Driver.Create provisions and starts Drive,
	// which must see a committed row and must never run for a fire
	// whose last_run advance rolled back.
	for _, sc := range fired {
		s.fireMission(ctx, sc, now)
	}
	return nil
}

// errAgentMissing marks a template agent_id that names no agents row.
var errAgentMissing = errors.New("template agent does not exist")

// skipReasonFor maps a failed fire to the skip_reason recorded on its
// schedule: agent_missing for a missing agent (pre-check or the
// missions.agent_id foreign key), fire_error for anything else.
func skipReasonFor(err error) string {
	if errors.Is(err, errAgentMissing) {
		return "agent_missing"
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" && strings.Contains(pgErr.ConstraintName, "agent_id") {
		return "agent_missing"
	}
	return "fire_error"
}

// fireEach runs fire for every schedule inside its own savepoint (issue
// #815): a failure rolls back only that schedule's work and records it
// as skipped, so one bad row cannot abort the shared transaction and
// take every later schedule down with it (25P02).
func (s *Scheduler) fireEach(ctx context.Context, tx pgx.Tx, schedules []Schedule, now time.Time, fire func(context.Context, pgx.Tx, Schedule, time.Time) error) error {
	for _, sc := range schedules {
		if _, err := tx.Exec(ctx, `SAVEPOINT schedule_fire`); err != nil {
			return fmt.Errorf("scheduler: savepoint: %w", err)
		}
		if fireErr := fire(ctx, tx, sc, now); fireErr != nil {
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT schedule_fire`); err != nil {
				return fmt.Errorf("scheduler: rollback to savepoint: %w", err)
			}
			reason := skipReasonFor(fireErr)
			s.log.Warn("scheduler: schedule skipped, fire failed", "schedule_id", sc.ID, "name", sc.Name, "reason", reason, "error", fireErr)
			if err := s.markSkipped(ctx, tx, sc, now, reason); err != nil {
				s.log.Error("scheduler: recording skip failed", "schedule_id", sc.ID, "error", err)
				if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT schedule_fire`); err != nil {
					return fmt.Errorf("scheduler: rollback to savepoint: %w", err)
				}
			}
		}
		if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT schedule_fire`); err != nil {
			return fmt.Errorf("scheduler: release savepoint: %w", err)
		}
	}
	return nil
}

// fireOne evaluates one schedule inside tick's transaction and records
// its last_run/pending_fire/skip bookkeeping there. fire reports that
// the schedule fires this tick (due, or carrying a pending fire from an
// earlier dedup skip); tick creates its mission after commit.
func (s *Scheduler) fireOne(ctx context.Context, tx pgx.Tx, sc Schedule, now time.Time) (fire bool, err error) {
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
		return false, err
	}

	// Backfill skip: past misfireGrace, never fires for this boundary —
	// still advance last_run (so the NEXT boundary computes from now,
	// not the stale anchor) and record why, but pending_fire is
	// untouched (a stale pending fire from an EARLIER dedup skip must
	// still get its own chance below, not be silently dropped by this
	// unrelated late boundary).
	if dec == decisionBackfillSkip {
		if err := s.markSkipped(ctx, tx, sc, now, "backfill_grace"); err != nil {
			return false, err
		}
		return s.tryFirePending(ctx, tx, sc)
	}
	if dec == decisionSkip {
		return s.tryFirePending(ctx, tx, sc)
	}

	// dec == decisionFire: due this tick. Whether or not a pending fire
	// from an earlier tick is also carried, only ONE mission fires —
	// firing the current due boundary also satisfies whatever was
	// pending, so pending_fire clears here rather than firing twice.
	active, err := s.activeMissionExists(ctx, tx, sc.ID)
	if err != nil {
		return false, err
	}
	if active {
		// Live-queue dedup: a mission from THIS schedule still active
		// means firing again would pile up parallel runs of the same
		// job — skip firing, carry it forward as pending, but still
		// advance last_run so the next boundary computes from now.
		if _, err := tx.Exec(ctx, `UPDATE schedules SET
				last_run = $2, pending_fire = true, last_skipped_at = $2, skip_reason = $3, updated_at = now()
			WHERE id = $1`, sc.ID, now, "active_mission"); err != nil {
			return false, fmt.Errorf("advance last_run (dedup skip): %w", err)
		}
		s.log.Info("scheduler: skipped firing, a mission from this schedule is still active", "schedule_id", sc.ID)
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE schedules SET
			last_run = $2, pending_fire = false, last_skipped_at = NULL, skip_reason = '', updated_at = now()
		WHERE id = $1`, sc.ID, now); err != nil {
		return false, fmt.Errorf("advance last_run: %w", err)
	}
	return true, nil
}

// tryFirePending fires a carried-forward pending fire when no mission
// from this schedule is currently active — the other half of the dedup
// skip's carryover: the tick that skipped set pending_fire, and every
// SUBSEQUENT tick (not just the one right after) checks it here until
// the active mission finally clears. A no-op when pending_fire is
// false, or when a mission is still active.
func (s *Scheduler) tryFirePending(ctx context.Context, tx pgx.Tx, sc Schedule) (bool, error) {
	if !sc.PendingFire {
		return false, nil
	}
	active, err := s.activeMissionExists(ctx, tx, sc.ID)
	if err != nil {
		return false, err
	}
	if active {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE schedules SET
			pending_fire = false, last_skipped_at = NULL, skip_reason = '', updated_at = now()
		WHERE id = $1`, sc.ID); err != nil {
		return false, fmt.Errorf("clear pending_fire: %w", err)
	}
	return true, nil
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

// markSkipped records a skip's reason/time (backfill_grace, or a failed
// fire's agent_missing/fire_error); last_run still advances so the next
// boundary computes from now, not the stale anchor (same rule the
// pre-existing backfill-skip path always followed).
func (s *Scheduler) markSkipped(ctx context.Context, tx pgx.Tx, sc Schedule, now time.Time, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE schedules SET
			last_run = $2, last_skipped_at = $2, skip_reason = $3, updated_at = now()
		WHERE id = $1`, sc.ID, now, reason); err != nil {
		return fmt.Errorf("advance last_run (%s skip): %w", reason, err)
	}
	return nil
}

// fireMission creates sc's mission through the shared create path
// (ValidateTemplate, ResolveDefaults, Driver.Create) after tick's
// bookkeeping committed. A failure records agent_missing or fire_error
// on this schedule only, advancing last_run and restoring pending_fire
// to its value before the tick, the same end state a rolled-back fire
// used to leave.
func (s *Scheduler) fireMission(ctx context.Context, sc Schedule, now time.Time) {
	id, err := s.createFromSchedule(ctx, sc)
	if err == nil {
		s.log.Info("scheduler: fired", "schedule_id", sc.ID, "mission_id", id)
		return
	}
	reason := skipReasonFor(err)
	s.log.Warn("scheduler: schedule skipped, fire failed", "schedule_id", sc.ID, "name", sc.Name, "reason", reason, "error", err)
	db, dbErr := s.db.Get()
	if dbErr != nil {
		s.log.Error("scheduler: recording skip failed", "schedule_id", sc.ID, "error", dbErr)
		return
	}
	if _, err := db.Exec(ctx, `UPDATE schedules SET
			last_run = $2, last_skipped_at = $2, skip_reason = $3, pending_fire = $4, updated_at = now()
		WHERE id = $1`, sc.ID, now, reason, sc.PendingFire); err != nil {
		s.log.Error("scheduler: recording skip failed", "schedule_id", sc.ID, "error", err)
	}
}

// createFromSchedule builds sc's CreateRequest and hands it to
// Driver.Create. The template is re-validated here since a row can
// predate the save-time check.
func (s *Scheduler) createFromSchedule(ctx context.Context, sc Schedule) (string, error) {
	if err := ValidateTemplate(sc.MissionTemplate); err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidMission, err.Error())
	}
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("scheduler: %w", err)
	}
	if err := checkAgentExists(ctx, db, sc.MissionTemplate.AgentID); err != nil {
		return "", err
	}
	if s.create == nil {
		return "", errors.New("scheduler: no mission creator wired")
	}
	req := TemplateCreateRequest(sc, s.filterDestinationIDs(ctx, sc.MissionTemplate.DestinationIDs))
	m, err := ResolveDefaults(ctx, req, s.resolve)
	if err != nil {
		return "", fmt.Errorf("create mission: %w", err)
	}
	id, err := s.create(ctx, m)
	if err != nil {
		return "", fmt.Errorf("create mission: %w", err)
	}
	return id, nil
}

// TemplateCreateRequest maps a schedule's template onto a CreateRequest.
// destinationIDs are the template's ids after the fire-time re-check.
// The mission is named after the template, else the schedule, carries
// the schedule id, and always auto-approves its plan (D-087): nobody is
// watching an unattended mission.
func TemplateCreateRequest(sc Schedule, destinationIDs []string) CreateRequest {
	t := sc.MissionTemplate
	name := t.Name
	if name == "" {
		name = sc.Name
	}
	// The deliverer resolves each entry's kind by id at delivery time.
	var destinations []DestinationEntry
	for _, id := range destinationIDs {
		destinations = append(destinations, DestinationEntry{DestinationID: id})
	}
	autoApproveTools := t.AutoApproveTools
	autoApprovePlan := true
	return CreateRequest{
		Goal: t.Goal, Name: name, Kind: t.Kind, AgentID: t.AgentID,
		Route: t.Route, ReviewRoute: t.ReviewRoute, PlanRoute: t.PlanRoute,
		MaxIterations: t.MaxIterations, BudgetAmount: t.BudgetAmount, BudgetCurrency: t.BudgetCurrency,
		AutoApproveTools: &autoApproveTools, AutoApprovePlan: &autoApprovePlan,
		Harness: t.Harness, ReviewHarness: t.ReviewHarness, Environment: t.Environment,
		Light:        t.Light,
		Sources:      t.Attachments,
		Destinations: destinations,
		ScheduleID:   sc.ID,
	}
}

// ValidateTemplate rejects a template no fire could turn into a valid
// mission: an empty goal, a kind other than coding or general, or light
// on a non-general kind. The schedule API runs it at save time and the
// scheduler again at fire time.
func ValidateTemplate(t MissionTemplate) error {
	if strings.TrimSpace(t.Goal) == "" {
		return errors.New("mission_template.goal is required")
	}
	if t.Kind != KindCoding && t.Kind != KindGeneral {
		return errors.New(`mission_template.kind must be "coding" or "general"`)
	}
	if t.Light && t.Kind != KindGeneral {
		return errors.New("mission_template.light is only valid for kind=general")
	}
	return nil
}

// rowQuerier is the QueryRow slice shared by a pool and a transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// checkAgentExists returns errAgentMissing when agentID is set but
// names no agents row, so a deleted or malformed agent is reported as
// agent_missing instead of a generic insert failure.
func checkAgentExists(ctx context.Context, q rowQuerier, agentID string) error {
	if agentID == "" {
		return nil
	}
	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agents WHERE id::text = lower($1))`, agentID).Scan(&exists); err != nil {
		return fmt.Errorf("check agent: %w", err)
	}
	if !exists {
		return fmt.Errorf("agent %q: %w", agentID, errAgentMissing)
	}
	return nil
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
