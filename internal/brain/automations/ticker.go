package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

const (
	// tickEvery is how often the ticker evaluates cron triggers.
	tickEvery = time.Minute
	// misfireGrace is how late a boundary may still fire: one missed
	// boundary backfills within it, older ones are skipped.
	misfireGrace = time.Hour
	// maxWalk caps the boundaries one walk pass enumerates.
	maxWalk = 1000
	// tickLockKey is the cron ticker's advisory lock key ("MISS").
	tickLockKey = 0x4D495353
)

// Ticker turns due cron boundaries into cron.due events.
type Ticker struct {
	store    *Store
	events   *events.Store
	enabled  func(ctx context.Context) bool
	location func(ctx context.Context) *time.Location
	kick     func()
	log      *slog.Logger
}

// NewTicker wires the ticker. enabled is the automations switch (nil
// runs always), location the operator timezone (nil is UTC) and kick
// the drainer's Kick (nil skips).
func NewTicker(store *Store, ev *events.Store, enabled func(ctx context.Context) bool, location func(ctx context.Context) *time.Location, kick func(), log *slog.Logger) *Ticker {
	return &Ticker{store: store, events: ev, enabled: enabled, location: location, kick: kick, log: log}
}

// Run ticks every minute until ctx is done.
func (t *Ticker) Run(ctx context.Context) {
	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := t.Tick(ctx, time.Now()); err != nil && ctx.Err() == nil {
				t.log.Error("automations: tick failed", "error", err)
			}
		}
	}
}

// walk is one trigger's due boundaries at a tick.
type walk struct {
	// Newest is the newest due boundary; zero when none is due.
	Newest time.Time
	// Fire reports that Newest is within misfireGrace.
	Fire bool
	// Skipped counts due boundaries that do not fire; a lower bound
	// once a pass hits maxWalk.
	Skipped int
}

// walkDue collects the boundaries of s after anchor up to now. When a
// pass hits maxWalk, the walk jumps to the grace window, since older
// boundaries only skip.
func walkDue(s cron.Schedule, anchor, now time.Time) walk {
	var w walk
	from := anchor
	for pass := 0; pass < 2; pass++ {
		next := s.Next(from)
		i := 0
		for ; i < maxWalk && !next.IsZero() && !next.After(now); i++ {
			if !w.Newest.IsZero() {
				w.Skipped++
			}
			w.Newest = next
			next = s.Next(next)
		}
		jump := now.Add(-misfireGrace - time.Second)
		if i < maxWalk || next.IsZero() || next.After(now) || !jump.After(w.Newest) {
			break
		}
		from = jump.In(anchor.Location())
	}
	if !w.Newest.IsZero() {
		if now.Sub(w.Newest) <= misfireGrace {
			w.Fire = true
		} else {
			w.Skipped++
		}
	}
	return w
}

// cronTrigger is one enabled cron trigger of an enabled, unexpired
// automation.
type cronTrigger struct {
	id, automationID    string
	config, state       []byte
	automationCreatedAt time.Time
}

// Tick evaluates every enabled cron trigger at now under the ticker's
// advisory lock, writes one event for the newest due boundary within
// grace and advances each trigger's state in the same transaction.
func (t *Ticker) Tick(ctx context.Context, now time.Time) error {
	if t.enabled != nil && !t.enabled(ctx) {
		return nil
	}
	loc := time.UTC
	if t.location != nil {
		if l := t.location(ctx); l != nil {
			loc = l
		}
	}
	db, err := t.store.db.Get()
	if err != nil {
		return fmt.Errorf("automations tick: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("automations tick: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var acquired bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, int64(tickLockKey)).Scan(&acquired); err != nil {
		return fmt.Errorf("automations tick: advisory lock: %w", err)
	}
	if !acquired {
		return tx.Commit(ctx)
	}
	fired, err := t.tickTriggers(ctx, tx, now, loc)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("automations tick: commit: %w", err)
	}
	if fired > 0 && t.kick != nil {
		t.kick()
	}
	return nil
}

// tickTriggers runs every enabled cron trigger through tx at now and
// reports how many fired. The caller holds the ticker lock.
func (t *Ticker) tickTriggers(ctx context.Context, tx pgx.Tx, now time.Time, loc *time.Location) (int, error) {
	rows, err := tx.Query(ctx, `SELECT t.id, t.automation_id, t.config, t.state, a.created_at
		FROM automation_triggers t JOIN automations a ON a.id = t.automation_id
		WHERE t.kind = 'cron' AND t.enabled AND a.enabled AND (a.expires_at IS NULL OR a.expires_at > $1)
		ORDER BY t.created_at, t.id`, now)
	if err != nil {
		return 0, fmt.Errorf("automations tick: query triggers: %w", err)
	}
	var triggers []cronTrigger
	for rows.Next() {
		var c cronTrigger
		if err := rows.Scan(&c.id, &c.automationID, &c.config, &c.state, &c.automationCreatedAt); err != nil {
			rows.Close()
			return 0, fmt.Errorf("automations tick: scan trigger: %w", err)
		}
		triggers = append(triggers, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("automations tick: triggers: %w", err)
	}
	fired := 0
	for _, c := range triggers {
		ok, err := t.tickTrigger(ctx, tx, c, now, loc)
		if err != nil {
			return 0, err
		}
		if ok {
			fired++
		}
	}
	return fired, nil
}

// tickTrigger walks one trigger's boundaries, inserts its event and
// merges its new state. fired reports that an event was written.
func (t *Ticker) tickTrigger(ctx context.Context, tx pgx.Tx, c cronTrigger, now time.Time, loc *time.Location) (fired bool, err error) {
	var cfg CronConfig
	_ = json.Unmarshal(c.config, &cfg)
	schedule, err := cron.ParseStandard(cfg.Expr)
	if err != nil {
		t.log.Warn("automations: cron trigger does not parse, skipping", "trigger_id", c.id, "automation_id", c.automationID, "error", err)
		return false, nil
	}
	var st struct {
		LastFiredAt *time.Time `json:"last_fired_at"`
	}
	_ = json.Unmarshal(c.state, &st)
	anchor := c.automationCreatedAt
	if st.LastFiredAt != nil {
		anchor = *st.LastFiredAt
	}
	w := walkDue(schedule, anchor.In(loc), now)
	if w.Newest.IsZero() {
		return false, nil
	}
	patch := map[string]string{"last_fired_at": w.Newest.UTC().Format(time.RFC3339)}
	if w.Skipped > 0 {
		patch["last_skipped_at"] = now.UTC().Format(time.RFC3339)
		patch["skip_reason"] = "backfill_grace"
	}
	if w.Fire {
		ev, err := events.CronDue(c.automationID, c.id, w.Newest.In(loc))
		if err != nil {
			return false, err
		}
		if err := t.events.Insert(ctx, tx, ev); err != nil {
			return false, fmt.Errorf("automations tick: %w", err)
		}
	}
	raw, _ := json.Marshal(patch)
	if _, err := tx.Exec(ctx, `UPDATE automation_triggers SET state = state || $2::jsonb WHERE id = $1`, c.id, raw); err != nil {
		return false, fmt.Errorf("automations tick: trigger state: %w", err)
	}
	if w.Skipped > 0 {
		t.log.Info("automations: skipped missed cron boundaries", "trigger_id", c.id, "automation_id", c.automationID, "skipped", w.Skipped)
	}
	return w.Fire, nil
}
