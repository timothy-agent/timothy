package missions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/robfig/cron/v3"
)

// scheduleNamePattern mirrors agents.namePattern: a lowercase slug that
// survives in URLs, ledger rows, and event payloads. Kept as its own
// copy rather than an exported symbol from the agents package — the
// two name spaces (agent names, schedule names) are independent and
// have no reason to import each other over one regexp.
var scheduleNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)

// ErrBadCron and ErrScheduleNameConflict are the sentinel errors the
// HTTP layer maps onto 400/409, mirroring ErrNotFound.
var (
	ErrBadCron              = errors.New("invalid cron expression")
	ErrScheduleNameConflict = errors.New("a schedule with this name already exists")
	ErrScheduleInUse        = errors.New("missions still reference this schedule")
)

// ValidateCron reports whether cronExpr parses as a standard 5-field
// cron expression (robfig/cron/v3's parser — the same one dueDecision
// already uses at fire time, so a schedule that validates here is
// guaranteed to actually fire later).
func ValidateCron(cronExpr string) error {
	if _, err := cron.ParseStandard(cronExpr); err != nil {
		return fmt.Errorf("%w: %s", ErrBadCron, err)
	}
	return nil
}

// NextRun computes the next fire time after now for cronExpr — the
// GET schedules API decorates each row with this so the UI can show
// "next run" without duplicating cron math client-side. Returns the
// zero time on an invalid expression (ValidateCron should already have
// caught that at create/patch time; this is a defensive fallback, not
// the primary guard).
func NextRun(cronExpr string, now time.Time) time.Time {
	schedule, err := cron.ParseStandard(cronExpr)
	if err != nil {
		return time.Time{}
	}
	return schedule.Next(now)
}

const scheduleColumns = `id, name, cron, mission_template, enabled, expires_at, last_run, created_at, updated_at, pending_fire, last_skipped_at, skip_reason`

func scanSchedule(row pgx.Row) (Schedule, error) {
	var sc Schedule
	var templateJSON []byte
	if err := row.Scan(&sc.ID, &sc.Name, &sc.Cron, &templateJSON, &sc.Enabled, &sc.ExpiresAt, &sc.LastRun, &sc.CreatedAt, &sc.UpdatedAt,
		&sc.PendingFire, &sc.LastSkippedAt, &sc.SkipReason); err != nil {
		return Schedule{}, err
	}
	_ = json.Unmarshal(templateJSON, &sc.MissionTemplate)
	return sc, nil
}

// ListSchedules returns every schedule, newest first.
func (s *Store) ListSchedules(ctx context.Context) ([]Schedule, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("schedules list: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+scheduleColumns+` FROM schedules ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("schedules list: %w", err)
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("schedules list: %w", err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// GetSchedule returns one schedule by id.
func (s *Store) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	db, err := s.db.Get()
	if err != nil {
		return Schedule{}, fmt.Errorf("schedules get: %w", err)
	}
	sc, err := scanSchedule(db.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = $1`, id))
	if err != nil {
		return Schedule{}, fmt.Errorf("schedule %s: %w", id, ErrNotFound)
	}
	return sc, nil
}

// CreateSchedule validates name and cron, and inserts. A duplicate name
// (schedules.name is UNIQUE) reports ErrScheduleNameConflict rather
// than a raw pg error.
func (s *Store) CreateSchedule(ctx context.Context, sc Schedule) (string, error) {
	if !scheduleNamePattern.MatchString(sc.Name) {
		return "", fmt.Errorf("name must be a lowercase slug (a-z, 0-9, - or _)")
	}
	if err := ValidateCron(sc.Cron); err != nil {
		return "", err
	}
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("schedules create: %w", err)
	}
	template, err := json.Marshal(sc.MissionTemplate)
	if err != nil {
		return "", fmt.Errorf("schedules create: %w", err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO schedules (name, cron, mission_template, enabled, expires_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		sc.Name, sc.Cron, template, sc.Enabled, sc.ExpiresAt).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrScheduleNameConflict
		}
		return "", fmt.Errorf("schedules create: %w", err)
	}
	return id, nil
}

// SchedulePatch is a partial update; nil fields are left unchanged.
// Name is patchable (unlike agents.Patch, where name is immutable) —
// a schedule's name is a display label with no ledger/event
// attribution riding on it.
type SchedulePatch struct {
	Name            *string
	Cron            *string
	MissionTemplate *MissionTemplate
	Enabled         *bool
	ExpiresAt       **time.Time
}

// PatchSchedule applies a partial update, re-validating name/cron the
// same way CreateSchedule does when they're set.
func (s *Store) PatchSchedule(ctx context.Context, id string, p SchedulePatch) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("schedules patch: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("schedules patch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	before, err := scanSchedule(tx.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return fmt.Errorf("schedule %s: %w", id, ErrNotFound)
	}
	after := before
	if p.Name != nil {
		if !scheduleNamePattern.MatchString(*p.Name) {
			return fmt.Errorf("name must be a lowercase slug (a-z, 0-9, - or _)")
		}
		after.Name = *p.Name
	}
	if p.Cron != nil {
		if err := ValidateCron(*p.Cron); err != nil {
			return err
		}
		after.Cron = *p.Cron
	}
	if p.MissionTemplate != nil {
		after.MissionTemplate = *p.MissionTemplate
	}
	if p.Enabled != nil {
		after.Enabled = *p.Enabled
	}
	if p.ExpiresAt != nil {
		after.ExpiresAt = *p.ExpiresAt
	}
	template, err := json.Marshal(after.MissionTemplate)
	if err != nil {
		return fmt.Errorf("schedules patch: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE schedules SET name = $2, cron = $3, mission_template = $4,
			enabled = $5, expires_at = $6, updated_at = now()
		WHERE id = $1`,
		id, after.Name, after.Cron, template, after.Enabled, after.ExpiresAt); err != nil {
		if isUniqueViolation(err) {
			return ErrScheduleNameConflict
		}
		return fmt.Errorf("schedules patch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("schedules patch: %w", err)
	}
	return nil
}

// DeleteSchedule removes a schedule. missions_schedule_id_fkey (0026)
// has no ON DELETE clause, so Postgres refuses this with a foreign-key
// violation while any mission still references the schedule — a
// schedule's fire history stays attributable rather than silently
// orphaned or cascaded away.
func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("schedules delete: %w", err)
	}
	tag, err := db.Exec(ctx, `DELETE FROM schedules WHERE id = $1`, id)
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("schedule %s: %w", id, ErrScheduleInUse)
		}
		return fmt.Errorf("schedules delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("schedule %s: %w", id, ErrNotFound)
	}
	return nil
}

// ScheduleReferencingDestinationID reports the name of the first
// enabled schedule whose mission_template.destination_ids still names
// destinationID — the guard destinations.Store.Delete calls before
// removing a row, so a schedule's next fire can't lose its delivery
// target out from under it. Disabled schedules never block deletion
// (same "active only" rule ActiveMissionReferencesDestination applies
// to non-terminal missions): a disabled schedule cannot fire again
// until re-enabled, at which point re-attaching or removing the
// destination is the operator's own call. ok reports whether any
// schedule references it.
func (s *Store) ScheduleReferencingDestinationID(ctx context.Context, destinationID string) (name string, ok bool, err error) {
	db, err := s.db.Get()
	if err != nil {
		return "", false, fmt.Errorf("schedules destination reference: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT name, mission_template FROM schedules WHERE enabled`)
	if err != nil {
		return "", false, fmt.Errorf("schedules destination reference: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var scName string
		var templateJSON []byte
		if err := rows.Scan(&scName, &templateJSON); err != nil {
			return "", false, fmt.Errorf("schedules destination reference: %w", err)
		}
		var t MissionTemplate
		_ = json.Unmarshal(templateJSON, &t)
		for _, d := range t.DestinationIDs {
			if d == destinationID {
				return scName, true, rows.Err()
			}
		}
	}
	return "", false, rows.Err()
}

// isForeignKeyViolation reports whether err is Postgres's
// foreign_key_violation (23503) — a schedule delete blocked by
// missions still referencing it.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// isUniqueViolation reports whether err is Postgres's unique_violation
// (23505) — schedules.name's UNIQUE constraint.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
