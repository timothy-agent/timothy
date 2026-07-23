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
	ID              string
	Name            string
	Cron            string
	MissionTemplate MissionTemplate
	Enabled         bool
	ExpiresAt       *time.Time
	LastRun         *time.Time
	CreatedAt       time.Time
}

// MissionTemplate is applied verbatim as a new mission's initial
// columns each time its schedule fires.
type MissionTemplate struct {
	Goal          string   `json:"goal"`
	Kind          string   `json:"kind"`
	AgentID       string   `json:"agent_id"`
	Route         string   `json:"route"`
	ReviewRoute   string   `json:"review_route"`
	MaxIterations int      `json:"max_iterations"`
	BudgetUSD     *float64 `json:"budget_usd,omitempty"`
}

// Scheduler ticks every schedulerTick, evaluating schedules rows
// against their cron expression (5-field, parsed via robfig/cron/v3's
// standard parser — parsing only, not its own goroutine scheduler: the
// tick+advisory-lock loop here matches the existing
// platform/migrate.go idiom rather than introducing a second
// scheduling paradigm).
type Scheduler struct {
	db       *pgpool.Pool
	missions *Store
	log      *slog.Logger
}

func NewScheduler(db *pgpool.Pool, missions *Store, log *slog.Logger) *Scheduler {
	return &Scheduler{db: db, missions: missions, log: log}
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
// pass whichever applies.
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

// tick evaluates every enabled, unexpired schedule once.
func (s *Scheduler) tick(ctx context.Context, now time.Time) error {
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

	rows, err := tx.Query(ctx, `SELECT id, name, cron, mission_template, enabled, expires_at, last_run, created_at
		FROM schedules WHERE enabled AND (expires_at IS NULL OR expires_at > now())`)
	if err != nil {
		return fmt.Errorf("scheduler: query schedules: %w", err)
	}
	var schedules []Schedule
	for rows.Next() {
		var sc Schedule
		var templateJSON []byte
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.Cron, &templateJSON, &sc.Enabled, &sc.ExpiresAt, &sc.LastRun, &sc.CreatedAt); err != nil {
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

// fireOne evaluates and, if due, fires one schedule — inside the same
// transaction tick holds, so last_run and any created mission commit
// atomically together.
func (s *Scheduler) fireOne(ctx context.Context, tx pgx.Tx, sc Schedule, now time.Time) error {
	// A never-run schedule anchors on its own creation, not "now" —
	// otherwise Next(now) is always in the future and it could never
	// fire on its very first eligible boundary.
	anchor := sc.CreatedAt
	if sc.LastRun != nil {
		anchor = *sc.LastRun
	}

	dec, err := dueDecision(sc.Cron, anchor, now, misfireGrace)
	if err != nil {
		return err
	}
	if dec == decisionSkip {
		return nil
	}

	// Live-queue dedup: a mission from THIS schedule still active means
	// firing again would pile up parallel runs of the same job — skip
	// firing, but still advance last_run so the next boundary computes
	// from now, not from the stale anchor.
	if dec == decisionFire {
		var activeCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM missions
			WHERE schedule_id = $1 AND phase NOT IN ('done', 'failed')`, sc.ID).Scan(&activeCount); err != nil {
			return fmt.Errorf("check active missions: %w", err)
		}
		if activeCount == 0 {
			if err := s.createFromTemplate(ctx, tx, sc); err != nil {
				return fmt.Errorf("create mission: %w", err)
			}
		} else {
			s.log.Info("scheduler: skipped firing, a mission from this schedule is still active", "schedule_id", sc.ID)
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE schedules SET last_run = $2, updated_at = now() WHERE id = $1`, sc.ID, now); err != nil {
		return fmt.Errorf("advance last_run: %w", err)
	}
	return nil
}

// createFromTemplate inserts a new mission from the schedule's
// template, tagged with the schedule that spawned it.
func (s *Scheduler) createFromTemplate(ctx context.Context, tx pgx.Tx, sc Schedule) error {
	t := sc.MissionTemplate
	spec, _ := json.Marshal(Spec{})
	_, err := tx.Exec(ctx, `INSERT INTO missions
			(goal, kind, agent_id, max_iterations, budget_usd, route, review_route, spec, schedule_id)
		VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8, $9)`,
		t.Goal, t.Kind, t.AgentID, orDefault(t.MaxIterations, 8), t.BudgetUSD, t.Route, t.ReviewRoute, spec, sc.ID)
	return err
}
