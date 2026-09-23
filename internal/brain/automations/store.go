package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Store is the automations tables' Postgres access.
type Store struct {
	db *pgpool.Pool
}

func NewStore(db *pgpool.Pool) *Store {
	return &Store{db: db}
}

// dbtx is the query slice shared by a pool and a transaction.
type dbtx interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const automationColumns = `id, name, description, agent_id, action, concurrency, max_concurrent, max_runs_per_hour,
	continuity, notes_enabled, consecutive_failures, disabled_reason, enabled, expires_at, created_at, updated_at`

const triggerColumns = `id, automation_id, kind, config, credential_ref, tool_allowlist, state, enabled, created_at, updated_at`

const runColumns = `id, automation_id, trigger_id, event_id, dedup_key, status, skip_reason, event, mission_id,
	workflow_run_id, created_at, started_at, finished_at`

func scanAutomation(row pgx.Row) (Automation, error) {
	var a Automation
	var action []byte
	var disabled *string
	if err := row.Scan(&a.ID, &a.Name, &a.Description, &a.AgentID, &action, &a.Concurrency, &a.MaxConcurrent, &a.MaxRunsPerHour,
		&a.Continuity, &a.NotesEnabled, &a.ConsecutiveFailures, &disabled, &a.Enabled, &a.ExpiresAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return Automation{}, err
	}
	_ = json.Unmarshal(action, &a.Action)
	if disabled != nil {
		a.DisabledReason = *disabled
	}
	a.Triggers = []Trigger{}
	return a, nil
}

func scanTrigger(row pgx.Row) (Trigger, error) {
	var t Trigger
	var config, allowlist, state []byte
	var credentialRef *string
	if err := row.Scan(&t.ID, &t.AutomationID, &t.Kind, &config, &credentialRef, &allowlist, &state, &t.Enabled, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return Trigger{}, err
	}
	t.Config, t.State = json.RawMessage(config), json.RawMessage(state)
	if credentialRef != nil {
		t.CredentialRef = *credentialRef
	}
	if len(allowlist) > 0 {
		_ = json.Unmarshal(allowlist, &t.ToolAllowlist)
	}
	return t, nil
}

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var skipReason, missionID, workflowRunID *string
	var event []byte
	if err := row.Scan(&r.ID, &r.AutomationID, &r.TriggerID, &r.EventID, &r.DedupKey, &r.Status, &skipReason, &event, &missionID,
		&workflowRunID, &r.CreatedAt, &r.StartedAt, &r.FinishedAt); err != nil {
		return Run{}, err
	}
	r.Event = json.RawMessage(event)
	if skipReason != nil {
		r.SkipReason = *skipReason
	}
	if missionID != nil {
		r.MissionID = *missionID
	}
	if workflowRunID != nil {
		r.WorkflowRunID = *workflowRunID
	}
	return r, nil
}

// isNotFound reports a missing row or a malformed id (22P02), which
// can never exist either.
func isNotFound(err error) bool {
	var pgErr *pgconn.PgError
	return errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && pgErr.Code == "22P02")
}

// mapWriteErr turns the name index and agent foreign key violations
// into sentinel errors.
func mapWriteErr(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && pgErr.ConstraintName == "automations_name_ci":
			return ErrNameConflict
		case pgErr.Code == "23503" && pgErr.ConstraintName == "automations_agent_id_fkey":
			return ErrUnknownAgent
		}
	}
	return fmt.Errorf("automations %s: %w", op, err)
}

// List returns every automation with its triggers, newest first.
func (s *Store) List(ctx context.Context) ([]Automation, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("automations list: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+automationColumns+` FROM automations ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("automations list: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Automation, error) { return scanAutomation(row) })
	if err != nil {
		return nil, fmt.Errorf("automations list: %w", err)
	}
	rows, err = db.Query(ctx, `SELECT `+triggerColumns+` FROM automation_triggers ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("automations list triggers: %w", err)
	}
	triggers, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Trigger, error) { return scanTrigger(row) })
	if err != nil {
		return nil, fmt.Errorf("automations list triggers: %w", err)
	}
	byAutomation := map[string][]Trigger{}
	for _, t := range triggers {
		byAutomation[t.AutomationID] = append(byAutomation[t.AutomationID], t)
	}
	for i := range out {
		if ts, ok := byAutomation[out[i].ID]; ok {
			out[i].Triggers = ts
		}
	}
	return out, nil
}

// Get returns one automation with its triggers.
func (s *Store) Get(ctx context.Context, id string) (Automation, error) {
	db, err := s.db.Get()
	if err != nil {
		return Automation{}, fmt.Errorf("automations get: %w", err)
	}
	return getAutomation(ctx, db, id, "")
}

// getAutomation reads one automation and its triggers through q;
// lock is appended to the automation SELECT (e.g. "FOR UPDATE").
func getAutomation(ctx context.Context, q dbtx, id, lock string) (Automation, error) {
	a, err := scanAutomation(q.QueryRow(ctx, `SELECT `+automationColumns+` FROM automations WHERE id = $1 `+lock, id))
	if isNotFound(err) {
		return Automation{}, fmt.Errorf("automation %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Automation{}, fmt.Errorf("automations get %s: %w", id, err)
	}
	rows, err := q.Query(ctx, `SELECT `+triggerColumns+` FROM automation_triggers WHERE automation_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		return Automation{}, fmt.Errorf("automations get triggers: %w", err)
	}
	triggers, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Trigger, error) { return scanTrigger(row) })
	if err != nil {
		return Automation{}, fmt.Errorf("automations get triggers: %w", err)
	}
	if len(triggers) > 0 {
		a.Triggers = triggers
	}
	return a, nil
}

// Create validates a and inserts it with its triggers in one
// transaction.
func (s *Store) Create(ctx context.Context, a Automation) (string, error) {
	if err := Validate(&a); err != nil {
		return "", err
	}
	action, err := json.Marshal(a.Action)
	if err != nil {
		return "", fmt.Errorf("automations create: %w", err)
	}
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("automations create: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("automations create: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO automations (name, description, agent_id, action, concurrency, max_concurrent,
			max_runs_per_hour, continuity, notes_enabled, enabled, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		a.Name, a.Description, a.AgentID, action, a.Concurrency, a.MaxConcurrent,
		a.MaxRunsPerHour, a.Continuity, a.NotesEnabled, a.Enabled, a.ExpiresAt).Scan(&id)
	if err != nil {
		return "", mapWriteErr("create", err)
	}
	for _, t := range a.Triggers {
		if err := insertTrigger(ctx, tx, id, t); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("automations create: %w", err)
	}
	return id, nil
}

func allowlistJSON(names []string) ([]byte, error) {
	if names == nil {
		return nil, nil
	}
	return json.Marshal(names)
}

// insertTrigger stamps created_at with clock_timestamp so triggers
// inserted in one transaction keep their order.
func insertTrigger(ctx context.Context, tx pgx.Tx, automationID string, t Trigger) error {
	allowlist, err := allowlistJSON(t.ToolAllowlist)
	if err != nil {
		return fmt.Errorf("automations trigger insert: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO automation_triggers (automation_id, kind, config, credential_ref, tool_allowlist, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, clock_timestamp(), clock_timestamp())`,
		automationID, t.Kind, []byte(t.Config), t.CredentialRef, allowlist, t.Enabled); err != nil {
		return fmt.Errorf("automations trigger insert: %w", err)
	}
	return nil
}

// Patch is a partial update; nil fields are left unchanged. ExpiresAt
// set to a nil inner pointer clears the expiry. Triggers replaces the
// whole trigger set: a supplied id that still exists keeps its row and
// state, other rows are deleted, entries without a known id are new.
type Patch struct {
	Name           *string
	Description    *string
	AgentID        *string
	Action         *Action
	Concurrency    *string
	MaxConcurrent  *int
	MaxRunsPerHour *int
	Continuity     *bool
	NotesEnabled   *bool
	Enabled        *bool
	ExpiresAt      **time.Time
	Triggers       *[]Trigger
}

// apply returns a with p applied.
func (p Patch) apply(a Automation) Automation {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&a.Name, p.Name)
	set(&a.Description, p.Description)
	set(&a.AgentID, p.AgentID)
	set(&a.Concurrency, p.Concurrency)
	if p.Action != nil {
		a.Action = *p.Action
	}
	if p.MaxConcurrent != nil {
		a.MaxConcurrent = *p.MaxConcurrent
	}
	if p.MaxRunsPerHour != nil {
		a.MaxRunsPerHour = *p.MaxRunsPerHour
	}
	if p.Continuity != nil {
		a.Continuity = *p.Continuity
	}
	if p.NotesEnabled != nil {
		a.NotesEnabled = *p.NotesEnabled
	}
	if p.Enabled != nil {
		a.Enabled = *p.Enabled
	}
	if p.ExpiresAt != nil {
		a.ExpiresAt = *p.ExpiresAt
	}
	if p.Triggers != nil {
		a.Triggers = append([]Trigger(nil), (*p.Triggers)...)
	}
	return a
}

// Patch applies p inside one transaction, re-validating the resulting
// automation before writing.
func (s *Store) Patch(ctx context.Context, id string, p Patch) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("automations patch: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("automations patch: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	before, err := getAutomation(ctx, tx, id, "FOR UPDATE")
	if err != nil {
		return err
	}
	after := p.apply(before)
	if err := Validate(&after); err != nil {
		return err
	}
	action, err := json.Marshal(after.Action)
	if err != nil {
		return fmt.Errorf("automations patch: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE automations SET name = $2, description = $3, agent_id = $4, action = $5,
			concurrency = $6, max_concurrent = $7, max_runs_per_hour = $8, continuity = $9, notes_enabled = $10,
			enabled = $11, expires_at = $12, updated_at = now()
		WHERE id = $1`,
		id, after.Name, after.Description, after.AgentID, action, after.Concurrency, after.MaxConcurrent,
		after.MaxRunsPerHour, after.Continuity, after.NotesEnabled, after.Enabled, after.ExpiresAt); err != nil {
		return mapWriteErr("patch", err)
	}
	if p.Triggers != nil {
		if err := replaceTriggers(ctx, tx, id, before.Triggers, after.Triggers); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("automations patch: %w", err)
	}
	return nil
}

// replaceTriggers makes want the automation's trigger set. A kept row
// preserves its state unless its kind changed.
func replaceTriggers(ctx context.Context, tx pgx.Tx, automationID string, existing, want []Trigger) error {
	known := map[string]bool{}
	for _, t := range existing {
		known[t.ID] = true
	}
	keep := []string{}
	for _, t := range want {
		if known[t.ID] {
			keep = append(keep, t.ID)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM automation_triggers WHERE automation_id = $1 AND NOT (id = ANY($2::uuid[]))`, automationID, keep); err != nil {
		return fmt.Errorf("automations trigger delete: %w", err)
	}
	for _, t := range want {
		if !known[t.ID] {
			if err := insertTrigger(ctx, tx, automationID, t); err != nil {
				return err
			}
			continue
		}
		allowlist, err := allowlistJSON(t.ToolAllowlist)
		if err != nil {
			return fmt.Errorf("automations trigger update: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE automation_triggers SET
				state = CASE WHEN kind = $3 THEN state ELSE '{}' END,
				kind = $3, config = $4, credential_ref = NULLIF($5, ''), tool_allowlist = $6, enabled = $7, updated_at = now()
			WHERE id = $1 AND automation_id = $2`,
			t.ID, automationID, t.Kind, []byte(t.Config), t.CredentialRef, allowlist, t.Enabled); err != nil {
			return fmt.Errorf("automations trigger update: %w", err)
		}
	}
	return nil
}

// Delete removes an automation; triggers, runs and notes cascade and
// missions keep their rows with automation_run_id cleared.
func (s *Store) Delete(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("automations delete: %w", err)
	}
	tag, err := db.Exec(ctx, `DELETE FROM automations WHERE id = $1`, id)
	if isNotFound(err) || (err == nil && tag.RowsAffected() == 0) {
		return fmt.Errorf("automation %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("automations delete: %w", err)
	}
	return nil
}

// ListRuns returns an automation's runs, newest first, at most limit.
func (s *Store) ListRuns(ctx context.Context, automationID string, limit int) ([]Run, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("automations runs: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+runColumns+` FROM automation_runs WHERE automation_id = $1
		ORDER BY created_at DESC, id LIMIT $2`, automationID, limit)
	if err != nil {
		return nil, fmt.Errorf("automations runs: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Run, error) { return scanRun(row) })
	if err != nil {
		return nil, fmt.Errorf("automations runs: %w", err)
	}
	if out == nil {
		out = []Run{}
	}
	return out, nil
}

// CreateRun inserts r through tx, or the pool when tx is nil. inserted
// is false when a run with the same dedup key already exists.
func (s *Store) CreateRun(ctx context.Context, tx pgx.Tx, r Run) (id string, inserted bool, err error) {
	var q dbtx = tx
	if tx == nil {
		db, err := s.db.Get()
		if err != nil {
			return "", false, fmt.Errorf("automations run create: %w", err)
		}
		q = db
	}
	event := []byte(r.Event)
	if len(event) == 0 {
		event = []byte(`{}`)
	}
	var createdAt *time.Time
	if !r.CreatedAt.IsZero() {
		createdAt = &r.CreatedAt
	}
	err = q.QueryRow(ctx, `INSERT INTO automation_runs (automation_id, trigger_id, event_id, dedup_key, status, skip_reason,
			event, mission_id, workflow_run_id, created_at, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, NULLIF($8, '')::uuid, NULLIF($9, '')::uuid, COALESCE($10, now()), $11, $12)
		ON CONFLICT (automation_id, dedup_key) DO NOTHING RETURNING id`,
		r.AutomationID, r.TriggerID, r.EventID, r.DedupKey, r.Status, r.SkipReason,
		event, r.MissionID, r.WorkflowRunID, createdAt, r.StartedAt, r.FinishedAt).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("automations run create: %w", err)
	}
	return id, true, nil
}

// DayCount is one sparkline bucket: a local calendar day.
type DayCount struct {
	Day       string `json:"day"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
}

// Stats is the automations overview tiles.
type Stats struct {
	Total       int        `json:"total"`
	Enabled     int        `json:"enabled"`
	Succeeded7d int        `json:"succeeded_7d"`
	Failed7d    int        `json:"failed_7d"`
	Sparkline   []DayCount `json:"sparkline"`
}

// sparklineDays is the number of local days the sparkline covers.
const sparklineDays = 14

// Stats counts automations and finished runs: the last 7 days before
// now, and a 14-day sparkline of local days in loc ending today.
func (s *Store) Stats(ctx context.Context, now time.Time, loc *time.Location) (Stats, error) {
	db, err := s.db.Get()
	if err != nil {
		return Stats{}, fmt.Errorf("automations stats: %w", err)
	}
	var st Stats
	if err := db.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE enabled) FROM automations`).Scan(&st.Total, &st.Enabled); err != nil {
		return Stats{}, fmt.Errorf("automations stats: %w", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'done'), count(*) FILTER (WHERE status = 'failed')
		FROM automation_runs WHERE finished_at > $1 AND finished_at <= $2`, now.Add(-7*24*time.Hour), now).Scan(&st.Succeeded7d, &st.Failed7d); err != nil {
		return Stats{}, fmt.Errorf("automations stats: %w", err)
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day()-(sparklineDays-1), 0, 0, 0, 0, loc)
	end := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc)
	rows, err := db.Query(ctx, `SELECT to_char(finished_at AT TIME ZONE $3, 'YYYY-MM-DD'),
			count(*) FILTER (WHERE status = 'done'), count(*) FILTER (WHERE status = 'failed')
		FROM automation_runs WHERE finished_at >= $1 AND finished_at < $2 AND status IN ('done', 'failed')
		GROUP BY 1`, start, end, loc.String())
	if err != nil {
		return Stats{}, fmt.Errorf("automations stats sparkline: %w", err)
	}
	byDay := map[string]DayCount{}
	for rows.Next() {
		var d DayCount
		if err := rows.Scan(&d.Day, &d.Succeeded, &d.Failed); err != nil {
			rows.Close()
			return Stats{}, fmt.Errorf("automations stats sparkline: %w", err)
		}
		byDay[d.Day] = d
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Stats{}, fmt.Errorf("automations stats sparkline: %w", err)
	}
	st.Sparkline = make([]DayCount, sparklineDays)
	for i := range sparklineDays {
		day := start.AddDate(0, 0, i).Format(time.DateOnly)
		st.Sparkline[i] = byDay[day]
		st.Sparkline[i].Day = day
	}
	return st, nil
}

// AutomationStats is one automation's run summary.
type AutomationStats struct {
	RunsTotal     int        `json:"runs_total"`
	Succeeded7d   int        `json:"succeeded_7d"`
	Failed7d      int        `json:"failed_7d"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
	LastRunStatus string     `json:"last_run_status,omitempty"`
	NextRunAt     *time.Time `json:"next_run_at,omitempty"`
}

// AutomationStats summarizes a's runs and computes its next cron
// boundary in loc.
func (s *Store) AutomationStats(ctx context.Context, a Automation, now time.Time, loc *time.Location) (AutomationStats, error) {
	db, err := s.db.Get()
	if err != nil {
		return AutomationStats{}, fmt.Errorf("automations stats: %w", err)
	}
	var st AutomationStats
	var lastAt *time.Time
	var lastStatus *string
	if err := db.QueryRow(ctx, `SELECT count(*),
			count(*) FILTER (WHERE status = 'done' AND finished_at > $2),
			count(*) FILTER (WHERE status = 'failed' AND finished_at > $2),
			max(created_at),
			(SELECT status FROM automation_runs WHERE automation_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1)
		FROM automation_runs WHERE automation_id = $1`, a.ID, now.Add(-7*24*time.Hour)).
		Scan(&st.RunsTotal, &st.Succeeded7d, &st.Failed7d, &lastAt, &lastStatus); err != nil {
		return AutomationStats{}, fmt.Errorf("automations stats %s: %w", a.ID, err)
	}
	st.LastRunAt = lastAt
	if lastStatus != nil {
		st.LastRunStatus = *lastStatus
	}
	st.NextRunAt = NextFire(a, now, loc)
	return st, nil
}

// NextFire is the earliest next boundary over a's enabled cron
// triggers, each anchored on its state.last_fired_at, else a's
// created_at, and evaluated in loc. nil when a is disabled, expired or
// has no cron trigger.
func NextFire(a Automation, now time.Time, loc *time.Location) *time.Time {
	if !a.Enabled || (a.ExpiresAt != nil && !a.ExpiresAt.After(now)) {
		return nil
	}
	var next *time.Time
	for _, t := range a.Triggers {
		if !t.Enabled || t.Kind != TriggerCron {
			continue
		}
		var c CronConfig
		if json.Unmarshal(t.Config, &c) != nil {
			continue
		}
		var st struct {
			LastFiredAt *time.Time `json:"last_fired_at"`
		}
		_ = json.Unmarshal(t.State, &st)
		anchor := a.CreatedAt
		if st.LastFiredAt != nil {
			anchor = *st.LastFiredAt
		}
		n := NextRun(c.Expr, anchor.In(loc))
		if n.IsZero() {
			continue
		}
		if next == nil || n.Before(*next) {
			next = &n
		}
	}
	return next
}

// ListNotes returns an automation's notes by name.
func (s *Store) ListNotes(ctx context.Context, automationID string) ([]Note, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("automations notes: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT name, content, updated_at, updated_by_run_id FROM automation_notes
		WHERE automation_id = $1 ORDER BY name`, automationID)
	if err != nil {
		return nil, fmt.Errorf("automations notes: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Note, error) { return scanNote(row) })
	if err != nil {
		return nil, fmt.Errorf("automations notes: %w", err)
	}
	if out == nil {
		out = []Note{}
	}
	return out, nil
}

func scanNote(row pgx.Row) (Note, error) {
	var n Note
	var runID *string
	if err := row.Scan(&n.Name, &n.Content, &n.UpdatedAt, &runID); err != nil {
		return Note{}, err
	}
	if runID != nil {
		n.UpdatedByRunID = *runID
	}
	return n, nil
}

// GetNote returns one note.
func (s *Store) GetNote(ctx context.Context, automationID, name string) (Note, error) {
	db, err := s.db.Get()
	if err != nil {
		return Note{}, fmt.Errorf("automations note get: %w", err)
	}
	n, err := scanNote(db.QueryRow(ctx, `SELECT name, content, updated_at, updated_by_run_id FROM automation_notes
		WHERE automation_id = $1 AND name = $2`, automationID, name))
	if isNotFound(err) {
		return Note{}, fmt.Errorf("note %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return Note{}, fmt.Errorf("automations note get: %w", err)
	}
	return n, nil
}

// PutNote creates or replaces a note. runID, when set, records the run
// that wrote it. created reports a new note. A new note past MaxNotes
// fails with ErrNoteLimit; the automation row lock serializes writers.
func (s *Store) PutNote(ctx context.Context, automationID, name, content, runID string) (n Note, created bool, err error) {
	if err := ValidateNote(name, content); err != nil {
		return Note{}, false, err
	}
	db, err := s.db.Get()
	if err != nil {
		return Note{}, false, fmt.Errorf("automations note put: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return Note{}, false, fmt.Errorf("automations note put: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM automations WHERE id = $1 FOR UPDATE`, automationID).Scan(&one); err != nil {
		if isNotFound(err) {
			return Note{}, false, fmt.Errorf("automation %s: %w", automationID, ErrNotFound)
		}
		return Note{}, false, fmt.Errorf("automations note put: %w", err)
	}
	var count int
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT count(*), coalesce(bool_or(name = $2), false) FROM automation_notes WHERE automation_id = $1`,
		automationID, name).Scan(&count, &exists); err != nil {
		return Note{}, false, fmt.Errorf("automations note put: %w", err)
	}
	if !exists && count >= MaxNotes {
		return Note{}, false, ErrNoteLimit
	}
	n, err = scanNote(tx.QueryRow(ctx, `INSERT INTO automation_notes (automation_id, name, content, updated_by_run_id)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid)
		ON CONFLICT (automation_id, name) DO UPDATE SET content = EXCLUDED.content, updated_at = now(),
			updated_by_run_id = EXCLUDED.updated_by_run_id
		RETURNING name, content, updated_at, updated_by_run_id`, automationID, name, content, runID))
	if err != nil {
		return Note{}, false, fmt.Errorf("automations note put: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Note{}, false, fmt.Errorf("automations note put: %w", err)
	}
	return n, !exists, nil
}

// DeleteNote removes one note.
func (s *Store) DeleteNote(ctx context.Context, automationID, name string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("automations note delete: %w", err)
	}
	tag, err := db.Exec(ctx, `DELETE FROM automation_notes WHERE automation_id = $1 AND name = $2`, automationID, name)
	if isNotFound(err) || (err == nil && tag.RowsAffected() == 0) {
		return fmt.Errorf("note %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("automations note delete: %w", err)
	}
	return nil
}

// NameReferencingDestination returns the name of the first enabled
// automation whose mission action delivers to destinationID.
func (s *Store) NameReferencingDestination(ctx context.Context, destinationID string) (name string, ok bool, err error) {
	db, err := s.db.Get()
	if err != nil {
		return "", false, fmt.Errorf("automations destination reference: %w", err)
	}
	err = db.QueryRow(ctx, `SELECT name FROM automations
		WHERE enabled AND action->'mission'->'destination_ids' @> jsonb_build_array($1::text)
		ORDER BY name LIMIT 1`, destinationID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("automations destination reference: %w", err)
	}
	return name, true, nil
}
