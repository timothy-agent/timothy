package missions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// workSlotLockKey serializes ClaimWorkSlot's read-then-conditional-
// update across concurrent callers within one transaction each —
// distinct from migrate.go's advisoryLockKey (0x54494D4F, "TIMO") so
// schema migrations and work-slot claims never contend.
const workSlotLockKey = 0x54494D53 // "TIMS"

// Store is missions' Postgres access: CRUD, transition persistence,
// event append with per-mission seq, and the two locking primitives
// (branch/workspace collision guard, work-slot cap) that must be
// check-and-write atomic under concurrent drivers.
type Store struct {
	db  *pgpool.Pool
	log *slog.Logger
}

func NewStore(db *pgpool.Pool, log *slog.Logger) *Store {
	return &Store{db: db, log: log}
}

const missionColumns = `id, goal, kind, agent_id, phase, status, pause_reason, pause_message,
	workspace, worktree, branch, base_commit, spec, progress, iteration, max_iterations,
	consecutive_failures, last_gap_fingerprint, stall_count, budget_usd, route, review_route,
	pending_permission, schedule_id, created_at, updated_at`

func scanMission(row pgx.Row) (Mission, error) {
	var (
		m                   Mission
		agentID, scheduleID *string
		phase, status       string
		pendingPermission   *string
		spec, progress      []byte
	)
	if err := row.Scan(&m.ID, &m.Goal, &m.Kind, &agentID, &phase, &status, &m.PauseReason, &m.PauseMessage,
		&m.Workspace, &m.Worktree, &m.Branch, &m.BaseCommit, &spec, &progress, &m.Iteration, &m.MaxIterations,
		&m.ConsecutiveFailures, &m.LastGapFingerprint, &m.StallCount, &m.BudgetUSD, &m.Route, &m.ReviewRoute,
		&pendingPermission, &scheduleID, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return Mission{}, err
	}
	if agentID != nil {
		m.AgentID = *agentID
	}
	if scheduleID != nil {
		m.ScheduleID = *scheduleID
	}
	if pendingPermission != nil {
		m.PendingPermission = *pendingPermission
	}
	// Fail-safe degrade: an unrecognized phase/status (e.g. a future
	// value an older binary doesn't know) loads as paused/infra rather
	// than making the row unreadable.
	if p, ok := parsePhase(phase); ok {
		m.Phase = p
	} else {
		m.Phase = PhaseFailed
		m.PauseMessage = fmt.Sprintf("unrecognized phase %q degraded to failed", phase)
	}
	if st, ok := parseStatus(status); ok {
		m.Status = st
	} else {
		m.Status = StatusPaused
		m.PauseReason = PauseInfra
		if m.PauseMessage == "" {
			m.PauseMessage = fmt.Sprintf("unrecognized status %q degraded to paused", status)
		}
	}
	_ = json.Unmarshal(spec, &m.Spec)
	_ = json.Unmarshal(progress, &m.Progress)
	if m.Progress == nil {
		m.Progress = []ProgressNote{}
	}
	return m, nil
}

// Create inserts a mission row in phase=research, status=idle.
func (s *Store) Create(ctx context.Context, m Mission) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("missions create: %w", err)
	}
	spec, err := json.Marshal(m.Spec)
	if err != nil {
		return "", fmt.Errorf("missions create spec: %w", err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO missions
			(goal, kind, agent_id, max_iterations, budget_usd, route, review_route, spec)
		VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8) RETURNING id`,
		m.Goal, m.Kind, m.AgentID, orDefault(m.MaxIterations, 8), m.BudgetUSD, m.Route, m.ReviewRoute, spec,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("missions create: %w", err)
	}
	return id, nil
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// Get returns one mission by id.
func (s *Store) Get(ctx context.Context, id string) (Mission, error) {
	db, err := s.db.Get()
	if err != nil {
		return Mission{}, fmt.Errorf("missions get: %w", err)
	}
	m, err := scanMission(db.QueryRow(ctx, `SELECT `+missionColumns+` FROM missions WHERE id = $1`, id))
	if err != nil {
		return Mission{}, fmt.Errorf("mission %s: %w", id, ErrNotFound)
	}
	return m, nil
}

// List returns every mission, newest first.
func (s *Store) List(ctx context.Context) ([]Mission, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions list: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+missionColumns+` FROM missions ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("missions list: %w", err)
	}
	defer rows.Close()
	out := []Mission{}
	for rows.Next() {
		m, err := scanMission(rows)
		if err != nil {
			return nil, fmt.Errorf("missions list: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetSpec persists the mission's plan — written by the plan phase
// (initial spec) and by unit verification (flipping a unit's Passes),
// independent of ApplyTransition since a spec update doesn't always
// coincide with a phase/status change.
func (s *Store) SetSpec(ctx context.Context, id string, spec Spec) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set spec: %w", err)
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("missions set spec: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET spec = $2, updated_at = now() WHERE id = $1`, id, data); err != nil {
		return fmt.Errorf("missions set spec: %w", err)
	}
	return nil
}

// Events returns a mission's full event log in seq order.
func (s *Store) Events(ctx context.Context, id string) ([]Event, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions events: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT mission_id, seq, kind, payload, provenance, fingerprint, created_at
		FROM mission_events WHERE mission_id = $1 ORDER BY seq`, id)
	if err != nil {
		return nil, fmt.Errorf("missions events: %w", err)
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.MissionID, &e.Seq, &e.Kind, &e.Payload, &e.Provenance, &e.Fingerprint, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("missions events: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ApplyTransition persists a Transition atomically: updates the
// mission row to Next and appends its Events in order, all in one
// txn. This is the ONLY way mission state changes after Create —
// Driver never issues a bare UPDATE.
func (s *Store) ApplyTransition(ctx context.Context, id string, t Transition) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions apply transition: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("missions apply transition begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `UPDATE missions SET
			phase = $2, status = $3, pause_reason = $4, iteration = $5, max_iterations = $6,
			consecutive_failures = $7, last_gap_fingerprint = $8, stall_count = $9, updated_at = now()
		WHERE id = $1`,
		id, string(t.Next.Phase), string(t.Next.Status), string(t.Next.PauseReason),
		t.Next.Iteration, t.Next.MaxIterations, t.Next.ConsecutiveFailures,
		t.Next.LastGapFingerprint, t.Next.StallCount,
	); err != nil {
		return fmt.Errorf("missions apply transition update: %w", err)
	}
	for _, ev := range t.Events {
		if err := appendEventTx(ctx, tx, id, ev.Kind, ev.Payload, "live"); err != nil {
			return fmt.Errorf("missions apply transition event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("missions apply transition commit: %w", err)
	}
	return nil
}

// AppendEvent assigns seq under SELECT ... FOR UPDATE on the mission
// row (serializes appends per-mission only) and inserts, outside of
// ApplyTransition — for callers that need to log something
// (mission.recovery, mission.violation) without a state change.
func (s *Store) AppendEvent(ctx context.Context, id, kind string, payload map[string]any) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions append event: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("missions append event begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := appendEventTx(ctx, tx, id, kind, payload, "live"); err != nil {
		return fmt.Errorf("missions append event: %w", err)
	}
	return tx.Commit(ctx)
}

// appendEventTx does the actual locked-seq insert within an
// already-open transaction, shared by ApplyTransition and AppendEvent.
func appendEventTx(ctx context.Context, tx pgx.Tx, missionID, kind string, payload map[string]any, provenance string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM missions WHERE id = $1 FOR UPDATE`, missionID).Scan(&exists); err != nil {
		return fmt.Errorf("lock mission %s: %w", missionID, err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	fp := ""
	if kind == "mission.done" || kind == "mission.failed" {
		fp = terminalFingerprint(kind, payload)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO mission_events (mission_id, seq, kind, payload, provenance, fingerprint)
		 SELECT $1, COALESCE(MAX(seq), 0) + 1, $2, $3, $4, $5 FROM mission_events WHERE mission_id = $1`,
		missionID, kind, data, provenance, fp,
	); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

// terminalFingerprint identifies a terminal event's outcome for
// ReconcileTerminal's duplicate/contradiction check.
func terminalFingerprint(kind string, payload map[string]any) string {
	b, _ := json.Marshal(payload)
	h := sha256.Sum256(append([]byte(kind+"|"), b...))
	return hex.EncodeToString(h[:])
}

// ReconcileTerminal handles the crash-safety case: a duplicate of the
// existing terminal outcome is a no-op; a CONTRADICTORY second
// terminal (e.g. a worker reports failed after a done was already
// persisted) writes a mission.reconciled event naming the canonical
// (first-by-seq) outcome instead of a second ending.
func (s *Store) ReconcileTerminal(ctx context.Context, id string, proposed Phase, payload map[string]any) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions reconcile: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("missions reconcile begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var existingPhase string
	err = tx.QueryRow(ctx, `SELECT phase FROM missions WHERE id = $1 FOR UPDATE`, id).Scan(&existingPhase)
	if err != nil {
		return fmt.Errorf("mission %s: %w", id, ErrNotFound)
	}
	if existingPhase != "done" && existingPhase != "failed" {
		return fmt.Errorf("missions reconcile: mission %s is not terminal (phase=%s)", id, existingPhase)
	}

	kind := "mission.done"
	if proposed == PhaseFailed {
		kind = "mission.failed"
	}
	proposedFP := terminalFingerprint(kind, payload)

	var lastFP string
	err = tx.QueryRow(ctx, `SELECT fingerprint FROM mission_events
		WHERE mission_id = $1 AND kind IN ('mission.done', 'mission.failed')
		ORDER BY seq LIMIT 1`, id).Scan(&lastFP)
	if err != nil {
		return fmt.Errorf("missions reconcile: read canonical terminal: %w", err)
	}
	if proposedFP == lastFP {
		return tx.Commit(ctx) // duplicate of the existing outcome: no-op
	}
	if err := appendEventTx(ctx, tx, id, "mission.reconciled", map[string]any{
		"canonical_phase": existingPhase, "rejected_kind": kind,
	}, "live"); err != nil {
		return fmt.Errorf("missions reconcile event: %w", err)
	}
	return tx.Commit(ctx)
}

// SetProvisioned checks-and-writes workspace/worktree/branch/base_commit
// in one txn (SELECT ... FOR UPDATE across active missions sharing the
// same workspace+branch), refusing with ErrBranchConflict if another
// ACTIVE mission (phase NOT IN ('done','failed')) already holds it.
func (s *Store) SetProvisioned(ctx context.Context, id, workspace, worktree, branch, baseCommit string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set provisioned: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("missions set provisioned begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if branch != "" {
		rows, err := tx.Query(ctx, `SELECT id FROM missions
			WHERE workspace = $1 AND branch = $2 AND id != $3 AND phase NOT IN ('done', 'failed')
			FOR UPDATE`, workspace, branch, id)
		if err != nil {
			return fmt.Errorf("missions set provisioned collision check: %w", err)
		}
		hasConflict := rows.Next()
		rows.Close()
		if hasConflict {
			return ErrBranchConflict
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE missions SET
			workspace = $2, worktree = $3, branch = $4, base_commit = $5, updated_at = now()
		WHERE id = $1`, id, workspace, worktree, branch, baseCommit); err != nil {
		return fmt.Errorf("missions set provisioned update: %w", err)
	}
	if err := appendEventTx(ctx, tx, id, "mission.provisioned", map[string]any{
		"workspace": workspace, "branch": branch,
	}, "live"); err != nil {
		return fmt.Errorf("missions set provisioned event: %w", err)
	}
	return tx.Commit(ctx)
}

// ClaimWorkSlot atomically flips ONE idle mission to working iff fewer
// than max missions are currently working, via
// pg_advisory_xact_lock(workSlotLockKey) + conditional UPDATE. Returns
// the claimed mission's id, or ok=false if none claimed (either no
// idle missions, or slots full).
func (s *Store) ClaimWorkSlot(ctx context.Context, max int) (id string, ok bool, err error) {
	db, err := s.db.Get()
	if err != nil {
		return "", false, fmt.Errorf("missions claim work slot: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", false, fmt.Errorf("missions claim work slot begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(workSlotLockKey)); err != nil {
		return "", false, fmt.Errorf("missions claim work slot lock: %w", err)
	}

	var working int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM missions WHERE status = 'working'`).Scan(&working); err != nil {
		return "", false, fmt.Errorf("missions claim work slot count: %w", err)
	}
	if working >= max {
		return "", false, tx.Commit(ctx)
	}

	err = tx.QueryRow(ctx, `UPDATE missions SET status = 'working', updated_at = now()
		WHERE id = (SELECT id FROM missions WHERE status = 'idle' ORDER BY created_at LIMIT 1)
		RETURNING id`).Scan(&id)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", false, tx.Commit(ctx)
		}
		return "", false, fmt.Errorf("missions claim work slot update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, fmt.Errorf("missions claim work slot commit: %w", err)
	}
	return id, true, nil
}

// RecoverWorking returns every mission left in status='working' — the
// boot sweep's input: a process crash mid-Advance leaves rows here,
// and they must be re-driven (Drive resumes cleanly since every
// boundary persists before the next step) rather than stuck forever.
func (s *Store) RecoverWorking(ctx context.Context) ([]Mission, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions recover working: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+missionColumns+` FROM missions WHERE status = 'working'`)
	if err != nil {
		return nil, fmt.Errorf("missions recover working: %w", err)
	}
	defer rows.Close()
	out := []Mission{}
	for rows.Next() {
		m, err := scanMission(rows)
		if err != nil {
			return nil, fmt.Errorf("missions recover working: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
