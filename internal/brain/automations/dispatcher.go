package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/events"
)

const (
	// breakerFailures disables an automation after this many
	// consecutive failed runs.
	breakerFailures = 3

	SkipDisabled    = "disabled"
	SkipRateLimited = "rate_limited"
	SkipActiveRun   = "active_run"
	SkipSuperseded  = "superseded"
)

// Dispatcher is the events consumer that turns cron.due and run.now
// events into runs and finalizes runs when their mission ends. All of
// its writes go through the drain transaction.
type Dispatcher struct {
	// notify is Notifier.NotifyMessage; nil skips the notification.
	notify func(ctx context.Context, missionID, kind, message string) error
	log    *slog.Logger
	now    func() time.Time
}

// NewDispatcher wires the dispatcher; notify is Notifier.NotifyMessage.
func NewDispatcher(notify func(ctx context.Context, missionID, kind, message string) error, log *slog.Logger) *Dispatcher {
	return &Dispatcher{notify: notify, log: log, now: time.Now}
}

func (d *Dispatcher) Name() string { return "automations" }

func (d *Dispatcher) Kinds() []string {
	return append([]string{events.KindCronDue, events.KindRunNow, events.KindMissionDone, events.KindMissionFailed}, events.ConnectorKinds()...)
}

// Handle routes ev to fire or finalize.
func (d *Dispatcher) Handle(ctx context.Context, tx pgx.Tx, ev events.Event) error {
	switch ev.Kind {
	case events.KindCronDue:
		p, err := events.DecodeCronDue(ev)
		if err != nil {
			return err
		}
		trigger := p.TriggerID
		return d.fire(ctx, tx, ev, p.AutomationID, &trigger, p.Boundary.Format(time.RFC3339),
			map[string]any{"kind": ev.Kind, "source": ev.Source, "boundary": p.Boundary.Format(time.RFC3339)})
	case events.KindRunNow:
		p, err := events.DecodeRunNow(ev)
		if err != nil {
			return err
		}
		return d.fire(ctx, tx, ev, p.AutomationID, nil, ev.DedupKey,
			map[string]any{"kind": ev.Kind, "source": ev.Source, "requested_at": p.RequestedAt.Format(time.RFC3339)})
	case events.KindMissionDone, events.KindMissionFailed:
		p, err := events.DecodeMission(ev)
		if err != nil {
			return err
		}
		return d.finalizeMission(ctx, tx, p.MissionID, ev.Kind == events.KindMissionFailed)
	}
	if slices.Contains(events.ConnectorKinds(), ev.Kind) {
		return d.handleConnectorEvent(ctx, tx, ev)
	}
	return nil
}

// handleConnectorEvent fires every enabled connector_event trigger of
// an enabled, unexpired automation whose filters match ev. An event
// the connector's own identity authored never fires.
func (d *Dispatcher) handleConnectorEvent(ctx context.Context, tx pgx.Tx, ev events.Event) error {
	p, err := events.DecodeConnectorEvent(ev)
	if err != nil {
		return err
	}
	if p.Self {
		d.log.Debug("automations: connector event authored by the connector identity, skipping", "event_id", ev.ID, "kind", ev.Kind, "repo", p.Repo)
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT t.id, t.automation_id, t.config
		FROM automation_triggers t JOIN automations a ON a.id = t.automation_id
		WHERE t.kind = 'connector_event' AND t.enabled AND a.enabled AND (a.expires_at IS NULL OR a.expires_at > $1)
		ORDER BY t.created_at, t.id`, d.now())
	if err != nil {
		return fmt.Errorf("automations connector event: query triggers: %w", err)
	}
	type match struct{ triggerID, automationID string }
	var matched []match
	for rows.Next() {
		var m match
		var raw []byte
		if err := rows.Scan(&m.triggerID, &m.automationID, &raw); err != nil {
			rows.Close()
			return fmt.Errorf("automations connector event: scan trigger: %w", err)
		}
		var cfg ConnectorEventConfig
		if json.Unmarshal(raw, &cfg) == nil && matchConnectorEvent(cfg, p) {
			matched = append(matched, m)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("automations connector event: triggers: %w", err)
	}
	if len(matched) == 0 {
		return nil
	}
	var runEvent map[string]any
	_ = json.Unmarshal(ev.Payload, &runEvent)
	runEvent["source"] = ev.Source
	for _, m := range matched {
		trigger := m.triggerID
		if err := d.fire(ctx, tx, ev, m.automationID, &trigger, trigger+"|"+p.ProviderEventID, runEvent); err != nil {
			return err
		}
	}
	return nil
}

// matchConnectorEvent reports whether p passes cfg's connector, repo,
// kind and label filters. Empty cfg.Labels matches any label set.
func matchConnectorEvent(cfg ConnectorEventConfig, p events.ConnectorEventPayload) bool {
	if !strings.EqualFold(cfg.ConnectorID, p.ConnectorID) || !strings.EqualFold(cfg.Repo, p.Repo) || !slices.Contains(cfg.Events, p.Kind) {
		return false
	}
	if len(cfg.Labels) == 0 {
		return true
	}
	for _, want := range cfg.Labels {
		for _, have := range p.Labels {
			if strings.EqualFold(want, have) {
				return true
			}
		}
	}
	return false
}

// decisionInput is what decide needs to know about an automation at
// the moment a trigger fires.
type decisionInput struct {
	Enabled        bool
	Expired        bool
	RunsLastHour   int
	MaxRunsPerHour int
	Active         int
	Concurrency    string
	MaxConcurrent  int
}

// decision is a new run's status. Supersede collapses every other
// queued run of the automation.
type decision struct {
	Status     string
	SkipReason string
	Supersede  bool
}

// decide applies the ceilings in order: enabled, rate limit, then the
// concurrency policy.
func decide(in decisionInput) decision {
	if !in.Enabled || in.Expired {
		return decision{Status: RunSkipped, SkipReason: SkipDisabled}
	}
	if in.RunsLastHour >= in.MaxRunsPerHour {
		return decision{Status: RunSkipped, SkipReason: SkipRateLimited}
	}
	switch in.Concurrency {
	case ConcurrencyQueue:
		if in.Active > 0 {
			return decision{Status: RunQueued, Supersede: true}
		}
	case ConcurrencyParallel:
		if in.Active >= in.MaxConcurrent {
			return decision{Status: RunQueued, Supersede: true}
		}
	default:
		if in.Active > 0 {
			return decision{Status: RunSkipped, SkipReason: SkipActiveRun}
		}
	}
	return decision{Status: RunStarting}
}

// fire records one run of automationID for ev under the automation's
// row lock. A duplicate dedup key is a no-op.
func (d *Dispatcher) fire(ctx context.Context, tx pgx.Tx, ev events.Event, automationID string, triggerID *string, dedupKey string, runEvent map[string]any) error {
	// A malformed id would abort the drain transaction at the uuid cast.
	if !isUUID(automationID) || (triggerID != nil && !isUUID(*triggerID)) {
		d.log.Warn("automations: event with a malformed id, skipping", "automation_id", automationID, "event_id", ev.ID)
		return nil
	}
	now := d.now()
	var enabled bool
	var expiresAt *time.Time
	var concurrency string
	var maxConcurrent, maxRunsPerHour int
	err := tx.QueryRow(ctx, `SELECT enabled, expires_at, concurrency, max_concurrent, max_runs_per_hour
		FROM automations WHERE id = $1 FOR UPDATE`, automationID).Scan(&enabled, &expiresAt, &concurrency, &maxConcurrent, &maxRunsPerHour)
	if errors.Is(err, pgx.ErrNoRows) {
		d.log.Info("automations: event for a missing automation, skipping", "automation_id", automationID, "event_id", ev.ID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("automations fire: lock automation: %w", err)
	}
	if self, err := selfTriggered(ctx, tx, automationID, ev.Payload); err != nil {
		return err
	} else if self {
		d.log.Info("automations: event came from this automation's own mission, skipping", "automation_id", automationID, "event_id", ev.ID)
		return nil
	}
	if triggerID != nil {
		var triggerEnabled bool
		err := tx.QueryRow(ctx, `SELECT enabled FROM automation_triggers WHERE id = $1 AND automation_id = $2`, *triggerID, automationID).Scan(&triggerEnabled)
		if errors.Is(err, pgx.ErrNoRows) {
			d.log.Info("automations: event for a missing trigger, skipping", "automation_id", automationID, "trigger_id", *triggerID, "event_id", ev.ID)
			return nil
		}
		if err != nil {
			return fmt.Errorf("automations fire: trigger: %w", err)
		}
		enabled = enabled && triggerEnabled
	}
	in := decisionInput{
		Enabled: enabled, Expired: expiresAt != nil && !expiresAt.After(now),
		MaxRunsPerHour: maxRunsPerHour, Concurrency: concurrency, MaxConcurrent: maxConcurrent,
	}
	if err := tx.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE created_at > $2 AND status IN ('queued', 'starting', 'running', 'done', 'failed')),
			count(*) FILTER (WHERE status IN ('starting', 'running'))
		FROM automation_runs WHERE automation_id = $1`, automationID, now.Add(-time.Hour)).Scan(&in.RunsLastHour, &in.Active); err != nil {
		return fmt.Errorf("automations fire: count runs: %w", err)
	}
	dec := decide(in)
	raw, _ := json.Marshal(runEvent)
	r := Run{
		AutomationID: automationID, TriggerID: triggerID, EventID: &ev.ID, DedupKey: dedupKey,
		Status: dec.Status, SkipReason: dec.SkipReason, Event: raw, CreatedAt: now,
	}
	if dec.Status == RunStarting {
		r.StartedAt = &now
	}
	runID, inserted, err := insertRun(ctx, tx, r)
	if err != nil {
		return err
	}
	if !inserted {
		return nil
	}
	if dec.Supersede {
		if _, err := tx.Exec(ctx, `UPDATE automation_runs SET status = 'skipped', skip_reason = $3, finished_at = $4
			WHERE automation_id = $1 AND status = 'queued' AND id <> $2`, automationID, runID, SkipSuperseded, now); err != nil {
			return fmt.Errorf("automations fire: supersede: %w", err)
		}
	}
	d.log.Info("automations: run recorded", "automation_id", automationID, "run_id", runID, "status", dec.Status, "skip_reason", dec.SkipReason)
	return nil
}

// originMissionID reads the mission a Phase 2 event says produced it.
func originMissionID(payload json.RawMessage) string {
	var p struct {
		OriginMissionID string `json:"origin_mission_id"`
	}
	_ = json.Unmarshal(payload, &p)
	return p.OriginMissionID
}

// selfTriggered reports whether payload names a mission started by a
// run of automationID, which must never trigger it again.
func selfTriggered(ctx context.Context, tx pgx.Tx, automationID string, payload json.RawMessage) (bool, error) {
	origin := originMissionID(payload)
	if !isUUID(origin) {
		return false, nil
	}
	var self bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM missions m JOIN automation_runs r ON r.id = m.automation_run_id
		WHERE m.id = $1 AND r.automation_id = $2)`, origin, automationID).Scan(&self); err != nil {
		return false, fmt.Errorf("automations fire: self-trigger guard: %w", err)
	}
	return self, nil
}

// finalizeMission finishes the run that started missionID. A mission
// outside any run, or an already finished run, is a no-op.
func (d *Dispatcher) finalizeMission(ctx context.Context, tx pgx.Tx, missionID string, failed bool) error {
	if !isUUID(missionID) {
		return nil
	}
	var runID, automationID, status string
	err := tx.QueryRow(ctx, `SELECT r.id, r.automation_id, r.status FROM automation_runs r
		JOIN missions m ON m.automation_run_id = r.id WHERE m.id = $1 FOR NO KEY UPDATE OF r`, missionID).Scan(&runID, &automationID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("automations finalize: run for mission %s: %w", missionID, err)
	}
	if status == RunDone || status == RunFailed {
		return nil
	}
	name, disabled, err := finalizeRun(ctx, tx, runID, automationID, missionID, failed, "", d.now(), d.log)
	if err != nil {
		return err
	}
	if disabled && d.notify != nil {
		if err := d.notify(ctx, missionID, "automation_disabled", disabledMessage(name)); err != nil {
			d.log.Warn("automations: disable notification failed", "automation_id", automationID, "mission_id", missionID, "error", err)
		}
	}
	return nil
}

func disabledMessage(name string) string {
	return fmt.Sprintf("%s was disabled after %d consecutive failed runs", name, breakerFailures)
}

// finalizeRun marks run runID done or failed, updates the circuit
// breaker and promotes the newest queued run when a slot is free.
// disabled reports that this run tripped the breaker.
func finalizeRun(ctx context.Context, tx pgx.Tx, runID, automationID, missionID string, failed bool, skipReason string, now time.Time, log *slog.Logger) (name string, disabled bool, err error) {
	status := RunDone
	if failed {
		status = RunFailed
	}
	if _, err := tx.Exec(ctx, `UPDATE automation_runs SET status = $2, skip_reason = COALESCE(NULLIF($3, ''), skip_reason), finished_at = $4
		WHERE id = $1`, runID, status, skipReason, now); err != nil {
		return "", false, fmt.Errorf("automations finalize: run: %w", err)
	}
	var enabled bool
	var failures, maxConcurrent int
	var concurrency string
	err = tx.QueryRow(ctx, `SELECT name, enabled, consecutive_failures, concurrency, max_concurrent FROM automations WHERE id = $1 FOR UPDATE`,
		automationID).Scan(&name, &enabled, &failures, &concurrency, &maxConcurrent)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("automations finalize: lock automation: %w", err)
	}
	if failed {
		failures++
	} else {
		failures = 0
	}
	disabled = enabled && failures >= breakerFailures
	if _, err := tx.Exec(ctx, `UPDATE automations SET consecutive_failures = $2,
			enabled = enabled AND NOT $3, disabled_reason = CASE WHEN $3 THEN $4 ELSE disabled_reason END, updated_at = now()
		WHERE id = $1`, automationID, failures, disabled, fmt.Sprintf("%d consecutive failed runs", breakerFailures)); err != nil {
		return "", false, fmt.Errorf("automations finalize: breaker: %w", err)
	}
	log.Info("automations: run finished", "automation_id", automationID, "run_id", runID, "mission_id", missionID, "status", status, "consecutive_failures", failures)
	if disabled {
		log.Warn("automations: disabled after consecutive failed runs", "automation_id", automationID, "name", name, "failures", failures)
		if _, err := tx.Exec(ctx, `UPDATE automation_runs SET status = 'skipped', skip_reason = $2, finished_at = $3
			WHERE automation_id = $1 AND status = 'queued'`, automationID, SkipDisabled, now); err != nil {
			return "", false, fmt.Errorf("automations finalize: drop queue: %w", err)
		}
		return name, true, nil
	}
	if !enabled {
		return name, false, nil
	}
	return name, false, promoteQueued(ctx, tx, automationID, concurrency, maxConcurrent, now)
}

// promoteQueued starts the newest queued run when active runs are under
// the automation's cap and supersedes older queued ones. It is the only
// way a queued run leaves the queue.
func promoteQueued(ctx context.Context, tx pgx.Tx, automationID, concurrency string, maxConcurrent int, now time.Time) error {
	limit := 1
	if concurrency == ConcurrencyParallel {
		limit = maxConcurrent
	}
	var active int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND status IN ('starting', 'running')`,
		automationID).Scan(&active); err != nil {
		return fmt.Errorf("automations finalize: count active: %w", err)
	}
	if active >= limit {
		return nil
	}
	var newest string
	err := tx.QueryRow(ctx, `SELECT id FROM automation_runs WHERE automation_id = $1 AND status = 'queued'
		ORDER BY created_at DESC, id DESC LIMIT 1 FOR NO KEY UPDATE`, automationID).Scan(&newest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("automations finalize: newest queued: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE automation_runs SET status = 'starting', started_at = $2 WHERE id = $1`, newest, now); err != nil {
		return fmt.Errorf("automations finalize: promote: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE automation_runs SET status = 'skipped', skip_reason = $3, finished_at = $4
		WHERE automation_id = $1 AND status = 'queued' AND id <> $2`, automationID, newest, SkipSuperseded, now); err != nil {
		return fmt.Errorf("automations finalize: supersede: %w", err)
	}
	return nil
}
