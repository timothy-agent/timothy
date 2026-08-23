package missions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/missions/executor"
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
	hub *Hub
}

func NewStore(db *pgpool.Pool, log *slog.Logger) *Store {
	return &Store{db: db, log: log}
}

// SetHub wires the push-notification hub: a nil hub (the default,
// before this is called) makes every publish below a no-op, so a
// mission API test or a build without SSE wired up behaves exactly as
// it did before this feature existed.
func (s *Store) SetHub(hub *Hub) {
	s.hub = hub
}

const missionColumns = `id, goal, name, kind, agent_id, phase, status, pause_reason, pause_message,
	workspace, worktree, branch, base_commit, spec, progress, iteration, max_iterations,
	consecutive_failures, last_gap_fingerprint, stall_count, budget_amount, budget_currency, route, review_route,
	plan_route, escalation_route, prompt_overlay, knowledge,
	pending_permission, pending_permission_tool, pending_permission_args,
	pending_permission_danger, pending_permission_rationale, auto_approve_safe, last_evidence,
	explore_notes, replan_used, schedule_id, session_id, harness, environment, repo_url, connector_id, on_complete,
	branch_pattern, commit_style, parent_mission_id, parent_context, attachments, destination_ids, light, final_output, created_at, updated_at,
	workflow_run_id, workflow_step`

// scanMissionWithFailureReason is scanMission plus one extra trailing
// column: the mission's latest mission.failed event's payload.reason
// (via a lateral join, added by the caller's query), empty for a
// mission that never failed. Used only by List/Get, which are what the
// web UI's mission list/detail views read.
func scanMissionWithFailureReason(row pgx.Row) (Mission, error) {
	var (
		m                                             Mission
		agentID, scheduleID, sessionID, parentMission *string
		phase, status                                 string
		pendingPermission                             *string
		spec, progress, attachmentsRaw, knowledgeRaw  []byte
		failureReason                                 *string
		workflowRunID                                 *string
	)
	if err := row.Scan(&m.ID, &m.Goal, &m.Name, &m.Kind, &agentID, &phase, &status, &m.PauseReason, &m.PauseMessage,
		&m.Workspace, &m.Worktree, &m.Branch, &m.BaseCommit, &spec, &progress, &m.Iteration, &m.MaxIterations,
		&m.ConsecutiveFailures, &m.LastGapFingerprint, &m.StallCount, &m.BudgetAmount, &m.BudgetCurrency, &m.Route, &m.ReviewRoute,
		&m.PlanRoute, &m.EscalationRoute, &m.PromptOverlay, &knowledgeRaw,
		&pendingPermission, &m.PendingPermissionTool, &m.PendingPermissionArgs,
		&m.PendingPermissionDanger, &m.PendingPermissionRationale, &m.AutoApproveSafe, &m.LastEvidence,
		&m.ExploreNotes, &m.ReplanUsed, &scheduleID, &sessionID, &m.Harness, &m.Environment,
		&m.RepoURL, &m.ConnectorID, &m.OnComplete, &m.BranchPattern, &m.CommitStyle, &parentMission, &m.ParentContext, &attachmentsRaw,
		&m.DestinationIDs, &m.Light, &m.FinalOutput,
		&m.CreatedAt, &m.UpdatedAt,
		&workflowRunID, &m.WorkflowStep,
		&failureReason); err != nil {
		return Mission{}, err
	}
	_ = json.Unmarshal(knowledgeRaw, &m.Knowledge)
	if agentID != nil {
		m.AgentID = *agentID
	}
	if scheduleID != nil {
		m.ScheduleID = *scheduleID
	}
	if sessionID != nil {
		m.SessionID = *sessionID
	}
	if parentMission != nil {
		m.ParentMissionID = *parentMission
	}
	if pendingPermission != nil {
		m.PendingPermission = *pendingPermission
	}
	if workflowRunID != nil {
		m.WorkflowRunID = *workflowRunID
	}
	if failureReason != nil {
		m.FailureReason = *failureReason
	}
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
	_ = json.Unmarshal(attachmentsRaw, &m.Attachments)
	return m, nil
}

// failureReasonJoin is a lateral join appended to missionColumns'
// SELECT, adding one trailing column: the LATEST mission.failed
// event's payload->>'reason' for that mission row (mission_events is
// append-only, so "latest by seq" is the one true outcome — see
// ReconcileTerminal). null for a mission that never failed.
const failureReasonJoin = `
	LEFT JOIN LATERAL (
		SELECT payload->>'reason' AS reason FROM mission_events
		WHERE mission_events.mission_id = missions.id AND kind = 'mission.failed'
		ORDER BY seq DESC LIMIT 1
	) fr ON true`

func scanMission(row pgx.Row) (Mission, error) {
	var (
		m                                             Mission
		agentID, scheduleID, sessionID, parentMission *string
		phase, status                                 string
		pendingPermission                             *string
		spec, progress, attachmentsRaw, knowledgeRaw  []byte
		workflowRunID                                 *string
	)
	if err := row.Scan(&m.ID, &m.Goal, &m.Name, &m.Kind, &agentID, &phase, &status, &m.PauseReason, &m.PauseMessage,
		&m.Workspace, &m.Worktree, &m.Branch, &m.BaseCommit, &spec, &progress, &m.Iteration, &m.MaxIterations,
		&m.ConsecutiveFailures, &m.LastGapFingerprint, &m.StallCount, &m.BudgetAmount, &m.BudgetCurrency, &m.Route, &m.ReviewRoute,
		&m.PlanRoute, &m.EscalationRoute, &m.PromptOverlay, &knowledgeRaw,
		&pendingPermission, &m.PendingPermissionTool, &m.PendingPermissionArgs,
		&m.PendingPermissionDanger, &m.PendingPermissionRationale, &m.AutoApproveSafe, &m.LastEvidence,
		&m.ExploreNotes, &m.ReplanUsed, &scheduleID, &sessionID, &m.Harness, &m.Environment,
		&m.RepoURL, &m.ConnectorID, &m.OnComplete, &m.BranchPattern, &m.CommitStyle, &parentMission, &m.ParentContext, &attachmentsRaw,
		&m.DestinationIDs, &m.Light, &m.FinalOutput,
		&m.CreatedAt, &m.UpdatedAt,
		&workflowRunID, &m.WorkflowStep); err != nil {
		return Mission{}, err
	}
	_ = json.Unmarshal(knowledgeRaw, &m.Knowledge)
	if agentID != nil {
		m.AgentID = *agentID
	}
	if scheduleID != nil {
		m.ScheduleID = *scheduleID
	}
	if sessionID != nil {
		m.SessionID = *sessionID
	}
	if parentMission != nil {
		m.ParentMissionID = *parentMission
	}
	if pendingPermission != nil {
		m.PendingPermission = *pendingPermission
	}
	if workflowRunID != nil {
		m.WorkflowRunID = *workflowRunID
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
	_ = json.Unmarshal(attachmentsRaw, &m.Attachments)
	return m, nil
}

// Create inserts a mission row in phase=explore, status=idle.
func (s *Store) Create(ctx context.Context, m Mission) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("missions create: %w", err)
	}
	spec, err := json.Marshal(m.Spec)
	if err != nil {
		return "", fmt.Errorf("missions create spec: %w", err)
	}
	// attachments is NOT NULL; a nil slice marshals to "null", which
	// would violate that, so a never-attached mission gets "[]" instead.
	attachments := m.Attachments
	if attachments == nil {
		attachments = []MissionAttachment{}
	}
	attachmentsJSON, err := json.Marshal(attachments)
	if err != nil {
		return "", fmt.Errorf("missions create attachments: %w", err)
	}
	// knowledge is NOT NULL; a nil slice marshals to "null", so a
	// mission with no knowledge collections gets "[]" instead.
	knowledge := m.Knowledge
	if knowledge == nil {
		knowledge = []string{}
	}
	knowledgeJSON, err := json.Marshal(knowledge)
	if err != nil {
		return "", fmt.Errorf("missions create knowledge: %w", err)
	}
	var id string
	budgetCurrency := m.BudgetCurrency
	if budgetCurrency == "" {
		budgetCurrency = "USD"
	}
	// destination_ids is NOT NULL; a nil slice binds as a NULL array
	// parameter, so a mission with no destinations gets an empty slice
	// instead.
	destinationIDs := m.DestinationIDs
	if destinationIDs == nil {
		destinationIDs = []string{}
	}
	phase := PhaseExplore
	if m.Light {
		phase = PhaseExecute
	}
	err = db.QueryRow(ctx, `INSERT INTO missions
			(goal, name, kind, agent_id, max_iterations, budget_amount, budget_currency, route, review_route, plan_route, escalation_route, prompt_overlay, knowledge, spec, session_id, auto_approve_safe, harness, environment, repo_url, connector_id, on_complete, branch_pattern, commit_style, parent_mission_id, parent_context, attachments, destination_ids, light, phase, workflow_run_id, workflow_step)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NULLIF($15, '')::uuid, $16, $17, $18, $19, $20, $21, $22, $23, NULLIF($24, '')::uuid, $25, $26, $27, $28, $29, NULLIF($30, '')::uuid, $31) RETURNING id`,
		m.Goal, m.Name, m.Kind, m.AgentID, orDefault(m.MaxIterations, 3), m.BudgetAmount, budgetCurrency, m.Route, m.ReviewRoute, m.PlanRoute, m.EscalationRoute, m.PromptOverlay, knowledgeJSON, spec, m.SessionID, m.AutoApproveSafe, m.Harness, m.Environment, m.RepoURL, m.ConnectorID, m.OnComplete, m.BranchPattern, m.CommitStyle, m.ParentMissionID, m.ParentContext, attachmentsJSON, destinationIDs, m.Light, phase, m.WorkflowRunID, m.WorkflowStep,
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
	m, err := scanMissionWithFailureReason(db.QueryRow(ctx, `SELECT `+missionColumns+`, fr.reason FROM missions`+failureReasonJoin+` WHERE missions.id = $1`, id))
	if err != nil {
		return Mission{}, fmt.Errorf("mission %s: %w", id, ErrNotFound)
	}
	return m, nil
}

// Delete permanently removes a terminal mission: its row (mission_events
// and notifications cascade via FK, migrations 0025/0027) plus every
// other trace this package owns. This is NOT a violation of the
// append-only discipline that governs mission_events within a mission's
// life — that rule protects a LIVE mission's history from being
// rewritten mid-flight; deleting the whole aggregate once it has
// finished (test-fixture cleanup, an operator purging old runs) is
// removal of the aggregate, not mutation of it. Refuses with
// ErrNotTerminal if the mission's Phase hasn't reached done/failed
// (covers cancelled too — cancel ends as mission.failed) and
// ErrNotFound for an unknown id. Returns the deleted mission so the
// caller (the API layer, which also owns the session store and
// Workspace) can tear down the hidden session and the on-disk
// workspace — both outside this package's remit.
func (s *Store) Delete(ctx context.Context, id string) (Mission, error) {
	db, err := s.db.Get()
	if err != nil {
		return Mission{}, fmt.Errorf("missions delete: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return Mission{}, fmt.Errorf("missions delete begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	m, err := scanMission(tx.QueryRow(ctx, `SELECT `+missionColumns+` FROM missions WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return Mission{}, fmt.Errorf("mission %s: %w", id, ErrNotFound)
	}
	if !m.Phase.Terminal() {
		return Mission{}, fmt.Errorf("mission %s: %w", id, ErrNotTerminal)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM missions WHERE id = $1`, id); err != nil {
		return Mission{}, fmt.Errorf("missions delete: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Mission{}, fmt.Errorf("missions delete commit: %w", err)
	}
	return m, nil
}

// ListFilter narrows List's result set — the zero value (no filter,
// no limit) is the original "every mission" behavior. Added for a
// recurring schedule's fire history view (?schedule_id=) and any
// future paginated list (?limit=), both optional.
type ListFilter struct {
	ScheduleID string
	// Limit caps the result count; 0 means unlimited.
	Limit int
}

// List returns missions matching filter, newest first. The zero
// ListFilter{} returns every mission, matching the pre-filter
// behavior exactly.
func (s *Store) List(ctx context.Context, filter ListFilter) ([]Mission, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions list: %w", err)
	}
	query := `SELECT ` + missionColumns + `, fr.reason FROM missions` + failureReasonJoin
	var args []any
	if filter.ScheduleID != "" {
		args = append(args, filter.ScheduleID)
		query += fmt.Sprintf(" WHERE schedule_id = $%d", len(args))
	}
	query += " ORDER BY created_at DESC"
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("missions list: %w", err)
	}
	defer rows.Close()
	out := []Mission{}
	for rows.Next() {
		m, err := scanMissionWithFailureReason(rows)
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

// AppendProgress adds one note to the mission's durable progress log
// (read back by WorkPacket.Render so the NEXT fresh worker turn has
// real memory of what happened, since workers carry no transcript of
// their own) and appends the matching mission.progress event in the
// same transaction, so the Timeline and the worker's own memory never
// diverge.
func (s *Store) AppendProgress(ctx context.Context, id, note string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions append progress: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("missions append progress begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	entry, err := json.Marshal(ProgressNote{At: time.Now().UTC(), Note: note})
	if err != nil {
		return fmt.Errorf("missions append progress: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE missions SET
			progress = progress || jsonb_build_array($2::jsonb), updated_at = now()
		WHERE id = $1`, id, entry); err != nil {
		return fmt.Errorf("missions append progress: %w", err)
	}
	if err := appendEventTx(ctx, tx, id, "mission.progress", map[string]any{"note": note}, "live"); err != nil {
		return fmt.Errorf("missions append progress: %w", err)
	}
	return tx.Commit(ctx)
}

// SetPendingPermission records a mission's turn parking on a tool-call
// permission prompt (loop.PermBroker id plus the detail the UI needs
// to render a real decision, not a bare "waiting" banner). Independent
// of ApplyTransition — parking happens mid-turn, inside a single
// Runner call, not at an Advance boundary. Also appends a
// mission.permission_requested event in the same transaction — without
// this, the Timeline shows nothing while a mission is parked, and the
// tool/command detail is lost once ClearPendingPermission runs (the
// pending_permission_* columns are live-only state, not history). Args
// are truncated the same way progress notes are — a shell command can
// be arbitrarily large, and the log only needs enough to identify what
// was approved, not a byte-for-byte replay.
func (s *Store) SetPendingPermission(ctx context.Context, id, permissionID, tool, args, danger, rationale string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set pending permission: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("missions set pending permission begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `UPDATE missions SET
			pending_permission = $2, pending_permission_tool = $3, pending_permission_args = $4,
			pending_permission_danger = $5, pending_permission_rationale = $6, updated_at = now()
		WHERE id = $1`, id, permissionID, tool, args, danger, rationale); err != nil {
		return fmt.Errorf("missions set pending permission: %w", err)
	}
	payload := map[string]any{"tool": tool, "args": truncate(args, 2000), "danger": danger, "rationale": rationale}
	if err := appendEventTx(ctx, tx, id, "mission.permission_requested", payload, "live"); err != nil {
		return fmt.Errorf("missions set pending permission: %w", err)
	}
	return tx.Commit(ctx)
}

// ClearPendingPermission drops a mission's parked-permission state
// once the broker resolves it (decision or timeout-deny) — the same
// Runner call that parked continues past this point on its own.
func (s *Store) ClearPendingPermission(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions clear pending permission: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET
			pending_permission = '', pending_permission_tool = '', pending_permission_args = '',
			pending_permission_danger = '', pending_permission_rationale = '', updated_at = now()
		WHERE id = $1`, id); err != nil {
		return fmt.Errorf("missions clear pending permission: %w", err)
	}
	return nil
}

// SetLastEvidence records the worker's most recent mission_status
// evidence text, read back by the next review round — durable (not
// process-local like gatekeepers) since losing it on restart would
// silently degrade a research mission's review back to diff-only.
func (s *Store) SetLastEvidence(ctx context.Context, id, evidence string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set last evidence: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET last_evidence = $2, updated_at = now() WHERE id = $1`,
		id, evidence); err != nil {
		return fmt.Errorf("missions set last evidence: %w", err)
	}
	return nil
}

// SetFinalOutput stores a light mission's verbatim final worker
// message (D-069) — mission state, not an event.
func (s *Store) SetFinalOutput(ctx context.Context, id, text string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set final output: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET final_output = $2, updated_at = now() WHERE id = $1`,
		id, text); err != nil {
		return fmt.Errorf("missions set final output: %w", err)
	}
	return nil
}

// SetExploreNotes stores the explore phase's findings for the
// planner. Like SetLastEvidence it bypasses the state machine: written
// mid-phase, not at an Advance boundary.
func (s *Store) SetExploreNotes(ctx context.Context, id, notes string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set explore notes: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET explore_notes = $2, updated_at = now() WHERE id = $1`,
		id, notes); err != nil {
		return fmt.Errorf("missions set explore notes: %w", err)
	}
	return nil
}

// SetNameIfEmpty writes an auto-generated display name without
// clobbering one already set (a scheduler-fired mission's own
// template name, or an earlier successful generation) — mirrors
// session.Store.SetTitleIfEmpty exactly: a plain guarded UPDATE, not
// state-machine/append-only, since name is display metadata about the
// row, not a fact about what happened during the mission.
func (s *Store) SetNameIfEmpty(ctx context.Context, id, name string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set name: %w", err)
	}
	_, err = db.Exec(ctx,
		`UPDATE missions SET name = $2, updated_at = now() WHERE id = $1 AND name = ''`,
		id, name)
	if err != nil {
		return fmt.Errorf("missions set name: %w", err)
	}
	return nil
}

// SetEnvironment persists a coding mission's auto-detected sandbox
// environment (D-05x) and appends a mission.environment_detected event
// — sticky, like SetProvisioned: driver.go's ensureProvisioned only
// calls this once, when Environment is still "". Bypasses the state
// machine like SetExploreNotes/SetLastEvidence: detection happens
// mid-provisioning, not at an Advance boundary.
func (s *Store) SetEnvironment(ctx context.Context, id, environment, marker string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set environment: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("missions set environment begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `UPDATE missions SET environment = $2, updated_at = now() WHERE id = $1`,
		id, environment); err != nil {
		return fmt.Errorf("missions set environment: %w", err)
	}
	if err := appendEventTx(ctx, tx, id, "mission.environment_detected", map[string]any{
		"environment": environment, "marker": marker,
	}, "live"); err != nil {
		return fmt.Errorf("missions set environment event: %w", err)
	}
	return tx.Commit(ctx)
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

// LastRunState reads back the delegated executor's most recent run
// manifest (executor.spawned) for missionID, plus whatever happened
// after it — a terminal event (executor.result/executor.died) means the
// run already reached a verdict in a prior process lifetime; otherwise
// the highest byte_offset any executor.progress event recorded is the
// resume point. Returns nil, nil when no executor.spawned event exists
// yet (a mission that never ran a delegated executor, or hasn't yet).
func (s *Store) LastRunState(ctx context.Context, missionID string) (*runState, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions last run state: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT kind, payload FROM mission_events
		WHERE mission_id = $1 AND kind IN ('executor.spawned', 'executor.result', 'executor.died', 'executor.progress')
		ORDER BY seq DESC`, missionID)
	if err != nil {
		return nil, fmt.Errorf("missions last run state: %w", err)
	}
	defer rows.Close()

	var state *runState
	for rows.Next() {
		var kind string
		var payload []byte
		if err := rows.Scan(&kind, &payload); err != nil {
			return nil, fmt.Errorf("missions last run state: %w", err)
		}
		switch kind {
		case "executor.spawned":
			var spawned struct {
				Harness  string `json:"harness"`
				AuthMode string `json:"auth_mode"`
				RunID    string `json:"run_id"`
				RunDir   string `json:"run_dir"`
			}
			if err := json.Unmarshal(payload, &spawned); err != nil {
				return nil, fmt.Errorf("missions last run state: decode spawned: %w", err)
			}
			if state == nil {
				state = &runState{}
			}
			state.Harness, state.RunID, state.RunDir = spawned.Harness, spawned.RunID, spawned.RunDir
			state.AuthMode = executor.AuthMode(spawned.AuthMode)
			return state, nil // found the latest spawn; nothing older matters
		case "executor.result", "executor.died":
			if state == nil {
				state = &runState{}
			}
			state.Finished = true
		case "executor.progress":
			if state == nil {
				state = &runState{}
			}
			if state.ByteOffset == 0 {
				var progress struct {
					ByteOffset int64 `json:"byte_offset"`
				}
				if err := json.Unmarshal(payload, &progress); err == nil {
					state.ByteOffset = progress.ByteOffset
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("missions last run state: %w", err)
	}
	return nil, nil
}

// ApplyTransition persists a Transition atomically: updates the
// mission row to Next and appends its Events in order, all in one
// txn. This is the ONLY way mission state changes after Create —
// Driver never issues a bare UPDATE.
//
// Guards against a cancel (or any other terminal transition) landing
// mid-turn: the row is locked FOR UPDATE and its CURRENT phase
// re-checked before writing. A mission observed non-terminal at the
// top of a Drive turn can be cancelled by a concurrent Signal before
// that turn's own ApplyTransition runs; without this check the turn's
// stale transition would overwrite the terminal row and resurrect the
// mission. Once Terminal(), the row is a dead end — this returns
// ErrTerminal and appends NO event, never a silent no-op write.
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

	var currentPhase string
	if err := tx.QueryRow(ctx, `SELECT phase FROM missions WHERE id = $1 FOR UPDATE`, id).Scan(&currentPhase); err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("mission %s: %w", id, ErrNotFound)
		}
		return fmt.Errorf("missions apply transition lock: %w", err)
	}
	if phase, ok := parsePhase(currentPhase); ok && phase.Terminal() {
		return ErrTerminal
	}

	// pause_message is cleared unconditionally: the state machine's
	// Transition never carries a message string (only scanMission's
	// read-time degrade-on-corruption path ever produces one, in
	// memory only), so any value sitting in the column is leftover from
	// an out-of-band write (e.g. manual DB surgery during an incident)
	// and must not survive the mission's next legitimate transition —
	// otherwise a stale message keeps showing in the UI long after the
	// mission has moved on.
	//
	// A transition landing on a terminal phase (cancel, or the state
	// machine's own done/failed) also clears any pending_permission*
	// detail — a dead mission can never resolve a park (the API's
	// answer-permission handler checks phase first and would already
	// reject it), so leaving the columns populated only stales the
	// Timeline's "Allow" banner for a mission that's no longer running.
	clearPending := t.Next.Phase.Terminal()
	if _, err := tx.Exec(ctx, `UPDATE missions SET
			phase = $2, status = $3, pause_reason = $4, pause_message = '', iteration = $5, max_iterations = $6,
			consecutive_failures = $7, last_gap_fingerprint = $8, stall_count = $9, replan_used = $11, updated_at = now(),
			pending_permission = CASE WHEN $10 THEN '' ELSE pending_permission END,
			pending_permission_tool = CASE WHEN $10 THEN '' ELSE pending_permission_tool END,
			pending_permission_args = CASE WHEN $10 THEN '' ELSE pending_permission_args END,
			pending_permission_danger = CASE WHEN $10 THEN '' ELSE pending_permission_danger END,
			pending_permission_rationale = CASE WHEN $10 THEN '' ELSE pending_permission_rationale END
		WHERE id = $1`,
		id, string(t.Next.Phase), string(t.Next.Status), string(t.Next.PauseReason),
		t.Next.Iteration, t.Next.MaxIterations, t.Next.ConsecutiveFailures,
		t.Next.LastGapFingerprint, t.Next.StallCount, clearPending, t.Next.ReplanUsed,
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
	if s.hub != nil {
		s.hub.Publish(Signal{Kind: "mission", ID: id})
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
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if s.hub != nil {
		s.hub.Publish(Signal{Kind: "mission", ID: id})
	}
	return nil
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

// SetSession attaches the mission's hidden bookkeeping session id —
// used by lazy provisioning (driver.go's ensureProvisioned) for a
// mission that reached the store without going through Driver.Create
// (a scheduler-fired row, see scheduler.go's createFromTemplate).
// Race-idempotent by construction: the WHERE clause only ever matches
// a mission that doesn't already have one, so two concurrent
// ensureProvisioned calls for the same never-provisioned mission (a
// Drive loop racing the work-slot sweep's own re-Drive) can't each
// create their own session and stomp the other's — the loser's write
// silently affects zero rows instead of overwriting a session id a
// worker turn may already be running under.
func (s *Store) SetSession(ctx context.Context, id, sessionID string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set session: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET session_id = $2, updated_at = now()
		WHERE id = $1 AND session_id IS NULL`, id, sessionID); err != nil {
		return fmt.Errorf("missions set session: %w", err)
	}
	return nil
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

// RecoverStaleWorking returns status='working' missions whose updated_at
// is older than staleAfter — the periodic sweep's input. A mission stays
// 'working' across every retry-eligible turn (stepWorkerFailed leaves
// status untouched below the backoff threshold), so Drive's own
// in-process loop is normally what keeps it moving; this is the backstop
// for the rare case that loop stops advancing without a crash (no
// process restart, so RecoverWorking's boot-only pass never sees it) —
// same re-Drive path, just periodic and staleness-gated so it never
// interferes with a mission genuinely mid-turn.
func (s *Store) RecoverStaleWorking(ctx context.Context, staleAfter time.Duration) ([]Mission, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions recover stale working: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+missionColumns+` FROM missions
		WHERE status = 'working' AND updated_at < now() - $1::interval`, staleAfter)
	if err != nil {
		return nil, fmt.Errorf("missions recover stale working: %w", err)
	}
	defer rows.Close()
	out := []Mission{}
	for rows.Next() {
		m, err := scanMission(rows)
		if err != nil {
			return nil, fmt.Errorf("missions recover stale working: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ActiveMissionReferencesDestination reports whether any NON-terminal
// mission (phase NOT IN ('done', 'failed')) still names destinationID
// in its destination_ids — the guard destinations.Store.Delete calls
// before removing a row, so a live mission's delivery target can't
// vanish out from under it. A historical (terminal) mission's
// reference never blocks deletion.
func (s *Store) ActiveMissionReferencesDestination(ctx context.Context, destinationID string) (bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return false, fmt.Errorf("missions active destination reference: %w", err)
	}
	var exists bool
	err = db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM missions
		WHERE phase NOT IN ('done', 'failed') AND $1 = ANY(destination_ids)
	)`, destinationID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("missions active destination reference: %w", err)
	}
	return exists, nil
}

// MissionSpend is a mission's cost_ledger footprint broken out by
// billing currency — never summed across currencies (see toStepState).
type MissionSpend struct {
	ByCurrency map[string]float64
}

// Spend totals a mission's ledger cost per currency. Rows with cost
// NULL (unpriced calls) contribute 0, same as everywhere else in this
// codebase that treats "unknown price" as best-effort zero for
// brake/alert purposes while still recording NULL, never a guess, at
// write time (D-013). Unbilled rows (subscription-billed executor
// runs recording the API-equivalent price) are excluded — the brake
// bounds real marginal spend, and a subscription run costs nothing at
// the margin. Only currencies with at least one ledger row for this
// mission appear in the map — an absent key means zero spend in that
// currency, same as a present zero.
func (s *Store) Spend(ctx context.Context, missionID string) (MissionSpend, error) {
	db, err := s.db.Get()
	if err != nil {
		return MissionSpend{}, fmt.Errorf("missions spend: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT currency, COALESCE(SUM(cost), 0)
		FROM cost_ledger WHERE mission_id = $1 AND NOT unbilled GROUP BY currency`, missionID)
	if err != nil {
		return MissionSpend{}, fmt.Errorf("missions spend: %w", err)
	}
	defer rows.Close()

	out := MissionSpend{ByCurrency: map[string]float64{}}
	for rows.Next() {
		var currency string
		var total float64
		if err := rows.Scan(&currency, &total); err != nil {
			return MissionSpend{}, fmt.Errorf("missions spend: %w", err)
		}
		out.ByCurrency[currency] = total
	}
	return out, rows.Err()
}

// BackoffPausedMission is BackoffPaused's row: just enough to drive the
// auto-resume ladder (sweep.go's autoResumeBackoff) without the cost of
// a full Mission scan.
type BackoffPausedMission struct {
	ID        string
	UpdatedAt time.Time
}

// BackoffPaused returns every mission currently paused with
// pause_reason='backoff' — autoResumeBackoff's input.
func (s *Store) BackoffPaused(ctx context.Context) ([]BackoffPausedMission, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions backoff paused: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT id, updated_at FROM missions WHERE status = 'paused' AND pause_reason = 'backoff'`)
	if err != nil {
		return nil, fmt.Errorf("missions backoff paused: %w", err)
	}
	defer rows.Close()
	out := []BackoffPausedMission{}
	for rows.Next() {
		var m BackoffPausedMission
		if err := rows.Scan(&m.ID, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("missions backoff paused: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountBackoffPauses counts how many times missionID has previously
// paused for backoff (mission.paused events with reason=backoff) —
// autoResumeBackoff's input to the resume ladder.
func (s *Store) CountBackoffPauses(ctx context.Context, missionID string) (int, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("missions count backoff pauses: %w", err)
	}
	var n int
	err = db.QueryRow(ctx, `SELECT count(*) FROM mission_events
		WHERE mission_id = $1 AND kind = 'mission.paused' AND payload->>'reason' = 'backoff'`, missionID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("missions count backoff pauses: %w", err)
	}
	return n, nil
}
