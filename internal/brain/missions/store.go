package missions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
	workspace, branch, base_commit, plan, progress, iteration, max_iterations,
	consecutive_failures, last_gap_fingerprint, stall_count, budget_amount, budget_currency, route, review_route,
	plan_route, escalation_route, route_model, plan_route_model, review_route_model, prompt_overlay,
	pending_permission, auto_approve_tools, auto_approve_plan, last_evidence,
	discover_notes, replan_used, schedule_id, session_id, harness, environment,
	parent_mission_id, sources, destinations, final_output, created_at, updated_at,
	workflow_run_id, workflow_step, artifact_refs, permission_timeout_seconds, pending_input, asks_used, flow`

// pendingPermissionRow is pending_permission's jsonb shape in the
// missions table: bundles the five columns the API's flat
// PendingPermission/PendingPermissionTool/... fields used to be, so
// the wire shape stays unchanged while the DB representation is one
// column instead of five. ParkedAt (issue #445) is when the park
// started, the timeout sweep's elapsed-time input.
type pendingPermissionRow struct {
	ID        string    `json:"id"`
	Tool      string    `json:"tool"`
	Args      string    `json:"args"`
	Danger    string    `json:"danger"`
	Rationale string    `json:"rationale"`
	ParkedAt  time.Time `json:"parked_at"`
}

// scanPendingPermission unmarshals the pending_permission jsonb column
// (nil when there's no pending request) into Mission's flat fields.
func scanPendingPermission(m *Mission, raw []byte) {
	if raw == nil {
		return
	}
	var p pendingPermissionRow
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	m.PendingPermission = p.ID
	m.PendingPermissionTool = p.Tool
	m.PendingPermissionArgs = p.Args
	m.PendingPermissionDanger = p.Danger
	m.PendingPermissionRationale = p.Rationale
	m.PendingPermissionParkedAt = p.ParkedAt
}

// scanPendingInput unmarshals the pending_input jsonb column (nil when
// there's no pending question) into Mission.PendingInput.
func scanPendingInput(m *Mission, raw []byte) {
	if raw == nil {
		return
	}
	var p PendingInput
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	m.PendingInput = &p
}

// scanMissionWithFailureReason is scanMission plus one extra trailing
// column: the mission's latest mission.failed event's payload.reason
// (via a lateral join, added by the caller's query), empty for a
// mission that never failed. Used only by List/Get, which are what the
// web UI's mission list/detail views read.
func scanMissionWithFailureReason(row pgx.Row) (Mission, error) {
	var (
		m                                                            Mission
		agentID, scheduleID, sessionID, parentMission                *string
		phase, status                                                string
		pendingPermissionRaw                                         []byte
		plan, progress, sourcesRaw, artifactRefsRaw, destinationsRaw []byte
		failureReason                                                *string
		workflowRunID                                                *string
		permissionTimeoutSeconds                                     *int
		pendingInputRaw                                              []byte
		flow                                                         string
	)
	if err := row.Scan(&m.ID, &m.Goal, &m.Name, &m.Kind, &agentID, &phase, &status, &m.PauseReason, &m.PauseMessage,
		&m.Workspace, &m.Branch, &m.BaseCommit, &plan, &progress, &m.Iteration, &m.MaxIterations,
		&m.ConsecutiveFailures, &m.LastGapFingerprint, &m.StallCount, &m.BudgetAmount, &m.BudgetCurrency, &m.Route, &m.ReviewRoute,
		&m.PlanRoute, &m.EscalationRoute, &m.RouteModel, &m.PlanRouteModel, &m.ReviewRouteModel, &m.PromptOverlay,
		&pendingPermissionRaw, &m.AutoApproveTools, &m.AutoApprovePlan, &m.LastEvidence,
		&m.DiscoverNotes, &m.ReplanUsed, &scheduleID, &sessionID, &m.Harness, &m.Environment,
		&parentMission, &sourcesRaw, &destinationsRaw, &m.FinalOutput,
		&m.CreatedAt, &m.UpdatedAt,
		&workflowRunID, &m.WorkflowStep, &artifactRefsRaw, &permissionTimeoutSeconds,
		&pendingInputRaw, &m.AsksUsed, &flow,
		&failureReason); err != nil {
		return Mission{}, err
	}
	m.Flow = Flow(flow)
	_ = json.Unmarshal(artifactRefsRaw, &m.ArtifactRefs)
	_ = json.Unmarshal(destinationsRaw, &m.Destinations)
	scanPendingPermission(&m, pendingPermissionRaw)
	scanPendingInput(&m, pendingInputRaw)
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
	if workflowRunID != nil {
		m.WorkflowRunID = *workflowRunID
	}
	m.PermissionTimeoutSeconds = permissionTimeoutSeconds
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
	_ = json.Unmarshal(plan, &m.Plan)
	_ = json.Unmarshal(progress, &m.Progress)
	if m.Progress == nil {
		m.Progress = []ProgressNote{}
	}
	_ = json.Unmarshal(sourcesRaw, &m.Sources)
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
		m                                                            Mission
		agentID, scheduleID, sessionID, parentMission                *string
		phase, status                                                string
		pendingPermissionRaw                                         []byte
		plan, progress, sourcesRaw, artifactRefsRaw, destinationsRaw []byte
		workflowRunID                                                *string
		permissionTimeoutSeconds                                     *int
		pendingInputRaw                                              []byte
		flow                                                         string
	)
	if err := row.Scan(&m.ID, &m.Goal, &m.Name, &m.Kind, &agentID, &phase, &status, &m.PauseReason, &m.PauseMessage,
		&m.Workspace, &m.Branch, &m.BaseCommit, &plan, &progress, &m.Iteration, &m.MaxIterations,
		&m.ConsecutiveFailures, &m.LastGapFingerprint, &m.StallCount, &m.BudgetAmount, &m.BudgetCurrency, &m.Route, &m.ReviewRoute,
		&m.PlanRoute, &m.EscalationRoute, &m.RouteModel, &m.PlanRouteModel, &m.ReviewRouteModel, &m.PromptOverlay,
		&pendingPermissionRaw, &m.AutoApproveTools, &m.AutoApprovePlan, &m.LastEvidence,
		&m.DiscoverNotes, &m.ReplanUsed, &scheduleID, &sessionID, &m.Harness, &m.Environment,
		&parentMission, &sourcesRaw, &destinationsRaw, &m.FinalOutput,
		&m.CreatedAt, &m.UpdatedAt,
		&workflowRunID, &m.WorkflowStep, &artifactRefsRaw, &permissionTimeoutSeconds,
		&pendingInputRaw, &m.AsksUsed, &flow); err != nil {
		return Mission{}, err
	}
	m.Flow = Flow(flow)
	_ = json.Unmarshal(artifactRefsRaw, &m.ArtifactRefs)
	_ = json.Unmarshal(destinationsRaw, &m.Destinations)
	scanPendingPermission(&m, pendingPermissionRaw)
	scanPendingInput(&m, pendingInputRaw)
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
	if workflowRunID != nil {
		m.WorkflowRunID = *workflowRunID
	}
	m.PermissionTimeoutSeconds = permissionTimeoutSeconds
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
	_ = json.Unmarshal(plan, &m.Plan)
	_ = json.Unmarshal(progress, &m.Progress)
	if m.Progress == nil {
		m.Progress = []ProgressNote{}
	}
	_ = json.Unmarshal(sourcesRaw, &m.Sources)
	return m, nil
}

// Create inserts a mission row in phase=discover, status=idle.
func (s *Store) Create(ctx context.Context, m Mission) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("missions create: %w", err)
	}
	plan, err := json.Marshal(m.Plan)
	if err != nil {
		return "", fmt.Errorf("missions create plan: %w", err)
	}
	// sources is NOT NULL; a nil slice marshals to "null", which would
	// violate that, so a mission with no sources gets "[]" instead.
	sources := m.Sources
	if sources == nil {
		sources = []SourceEntry{}
	}
	sourcesJSON, err := json.Marshal(sources)
	if err != nil {
		return "", fmt.Errorf("missions create sources: %w", err)
	}
	var id string
	budgetCurrency := m.BudgetCurrency
	if budgetCurrency == "" {
		budgetCurrency = "USD"
	}
	// destinations is NOT NULL; a nil slice marshals to "null", which
	// would violate that, so a mission with no destinations gets "[]"
	// instead (same fix as attachments/sources above).
	destinations := m.Destinations
	if destinations == nil {
		destinations = []DestinationEntry{}
	}
	destinationsJSON, err := json.Marshal(destinations)
	if err != nil {
		return "", fmt.Errorf("missions create destinations: %w", err)
	}
	// flow defaults to full for any caller (test fixture, a pre-#459
	// code path) that never set it: matches the column's own DB
	// default and today's pre-#459 behavior exactly.
	flow := m.Flow
	if flow == "" {
		flow = FlowFull
	}
	phase := initialPhase(m.Kind, flow)
	err = db.QueryRow(ctx, `INSERT INTO missions
			(goal, name, kind, agent_id, max_iterations, budget_amount, budget_currency, route, review_route, plan_route, escalation_route, route_model, plan_route_model, review_route_model, prompt_overlay, plan, session_id, auto_approve_tools, auto_approve_plan, harness, environment, parent_mission_id, sources, destinations, phase, workflow_run_id, workflow_step, permission_timeout_seconds, flow)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, NULLIF($17, '')::uuid, $18, $19, $20, $21, NULLIF($22, '')::uuid, $23, $24, $25, NULLIF($26, '')::uuid, $27, $28, $29) RETURNING id`,
		m.Goal, m.Name, m.Kind, m.AgentID, orDefault(m.MaxIterations, 3), m.BudgetAmount, budgetCurrency, m.Route, m.ReviewRoute, m.PlanRoute, m.EscalationRoute, m.RouteModel, m.PlanRouteModel, m.ReviewRouteModel, m.PromptOverlay, plan, m.SessionID, m.AutoApproveTools, m.AutoApprovePlan, m.Harness, m.Environment, m.ParentMissionID, sourcesJSON, destinationsJSON, phase, m.WorkflowRunID, m.WorkflowStep, m.PermissionTimeoutSeconds, flow,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("missions create: %w", err)
	}
	return id, nil
}

// escapeLike escapes ILIKE wildcards in a user-supplied query so they
// match as literals, not patterns (mirrors session.Store.List's own
// escaping).
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
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
	// Query, when set, keeps only missions whose name or goal contains
	// it (case-insensitive): the composer #-mention "type to find a
	// mission" search (GET /v1/missions?q=).
	Query string
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
	var where []string
	if filter.ScheduleID != "" {
		args = append(args, filter.ScheduleID)
		where = append(where, fmt.Sprintf("schedule_id = $%d", len(args)))
	}
	if filter.Query != "" {
		args = append(args, "%"+escapeLike(filter.Query)+"%")
		where = append(where, fmt.Sprintf("(name ILIKE $%d OR goal ILIKE $%d)", len(args), len(args)))
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
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

// SetPlan persists the mission's plan -- written by the plan phase
// (initial plan) and by unit verification (flipping a unit's Passes),
// independent of ApplyTransition since a plan update doesn't always
// coincide with a phase/status change.
func (s *Store) SetPlan(ctx context.Context, id string, plan Plan) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set plan: %w", err)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("missions set plan: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET plan = $2, updated_at = now() WHERE id = $1`, id, data); err != nil {
		return fmt.Errorf("missions set plan: %w", err)
	}
	return nil
}

// SetArtifactRefs persists the mission's artifact-store refs (see
// driver.go's copyArtifacts) — bypasses the state machine, same as
// SetPlan, since it runs from a terminal-transition hook rather than
// an Advance step.
func (s *Store) SetArtifactRefs(ctx context.Context, id string, refs []ArtifactRef) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set artifact refs: %w", err)
	}
	if refs == nil {
		refs = []ArtifactRef{}
	}
	data, err := json.Marshal(refs)
	if err != nil {
		return fmt.Errorf("missions set artifact refs: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET artifact_refs = $2, updated_at = now() WHERE id = $1`, id, data); err != nil {
		return fmt.Errorf("missions set artifact refs: %w", err)
	}
	return nil
}

// SetDestinations persists the mission's destinations entries whole,
// written by the result phase's step (runResult) after each delivery
// attempt records its own delivered_at/error onto its entry. Bypasses
// the state machine, same as SetPlan/SetArtifactRefs: a delivery-state
// update doesn't coincide with a phase/status change.
func (s *Store) SetDestinations(ctx context.Context, id string, entries []DestinationEntry) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set destinations: %w", err)
	}
	if entries == nil {
		entries = []DestinationEntry{}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("missions set destinations: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET destinations = $2, updated_at = now() WHERE id = $1`, id, data); err != nil {
		return fmt.Errorf("missions set destinations: %w", err)
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

// Progress reads back one mission's live progress log: a narrower
// query than Get (just the progress column), used by nativeRunner's
// mid-run steering poll (missions.ProgressReader) so an in-flight
// worker turn can see an operator note posted after it started.
func (s *Store) Progress(ctx context.Context, id string) ([]ProgressNote, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions progress: %w", err)
	}
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT progress FROM missions WHERE id = $1`, id).Scan(&raw); err != nil {
		return nil, fmt.Errorf("missions progress: %w", err)
	}
	var notes []ProgressNote
	if err := json.Unmarshal(raw, &notes); err != nil {
		return nil, fmt.Errorf("missions progress: %w", err)
	}
	return notes, nil
}

// SetPendingPermission records a mission's turn parking on a tool-call
// permission prompt (loop.PermBroker id plus the detail the UI needs
// to render a real decision, not a bare "waiting" banner). Independent
// of ApplyTransition — parking happens mid-turn, inside a single
// Runner call, not at an Advance boundary. Also appends a
// mission.permission_requested event in the same transaction — without
// this, the Timeline shows nothing while a mission is parked, and the
// tool/command detail is lost once ClearPendingPermission runs (the
// pending_permission column is live-only state, not history). Args
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
	data, err := json.Marshal(pendingPermissionRow{ID: permissionID, Tool: tool, Args: args, Danger: danger, Rationale: rationale, ParkedAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("missions set pending permission: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE missions SET
			pending_permission = $2, updated_at = now()
		WHERE id = $1`, id, data); err != nil {
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
			pending_permission = NULL, updated_at = now()
		WHERE id = $1`, id); err != nil {
		return fmt.Errorf("missions clear pending permission: %w", err)
	}
	return nil
}

// ParkedPermission is one PendingPermissions row: just enough for the
// timeout sweep's elapsed-time decision, without the cost of a full
// Mission scan.
type ParkedPermission struct {
	MissionID string
	// PermissionID is the broker-issued id (pendingPermissionRow.ID),
	// distinct from MissionID, needed to resolve a still-live worker
	// turn's in-memory PermBroker wait (loop.PermBroker.Resolve takes
	// this id, not the mission id).
	PermissionID    string
	Tool            string
	ParkedAt        time.Time
	TimeoutOverride *int // this mission's own permission_timeout_seconds, nil = inherit the global setting
}

// PendingPermissions returns every mission currently parked on
// pending_permission, the timeout sweep's input.
func (s *Store) PendingPermissions(ctx context.Context) ([]ParkedPermission, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions pending permissions: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT id, pending_permission, permission_timeout_seconds
		FROM missions WHERE pending_permission IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("missions pending permissions: %w", err)
	}
	defer rows.Close()
	out := []ParkedPermission{}
	for rows.Next() {
		var (
			id      string
			raw     []byte
			timeout *int
		)
		if err := rows.Scan(&id, &raw, &timeout); err != nil {
			return nil, fmt.Errorf("missions pending permissions: %w", err)
		}
		var p pendingPermissionRow
		if err := json.Unmarshal(raw, &p); err != nil {
			continue // corrupt row: skip rather than fail the whole sweep
		}
		out = append(out, ParkedPermission{MissionID: id, PermissionID: p.ID, Tool: p.Tool, ParkedAt: p.ParkedAt, TimeoutOverride: timeout})
	}
	return out, rows.Err()
}

// ResolvePendingPermissionTimeout auto-denies a mission's parked
// permission request after it sat unanswered past the effective
// timeout (issue #445): clears pending_permission and appends the SAME
// mission.permission_answered event kind an operator's manual deny
// produces, with reason=timed_out distinguishing it from a human
// decision. Never approves: there is no other decision value this
// method can write.
func (s *Store) ResolvePendingPermissionTimeout(ctx context.Context, id, tool string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions resolve pending permission timeout: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("missions resolve pending permission timeout begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `UPDATE missions SET
			pending_permission = NULL, updated_at = now()
		WHERE id = $1`, id); err != nil {
		return fmt.Errorf("missions resolve pending permission timeout: %w", err)
	}
	if err := appendEventTx(ctx, tx, id, "mission.permission_answered", map[string]any{
		"tool": tool, "decision": "deny", "reason": "timed_out",
	}, "live"); err != nil {
		return fmt.Errorf("missions resolve pending permission timeout: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("missions resolve pending permission timeout commit: %w", err)
	}
	if s.hub != nil {
		s.hub.Publish(Signal{Kind: "mission", ID: id})
	}
	return nil
}

// SetPendingInput records ask_user's park (D-088): a second park kind
// alongside pending_permission, mirroring SetPendingPermission's shape
// and reasoning exactly: bypasses ApplyTransition since parking
// happens mid-turn, appends mission.input_requested in the same
// transaction so the Timeline shows the question immediately. AskedAt
// is stamped here, server-side, same as SetPendingPermission's ParkedAt.
func (s *Store) SetPendingInput(ctx context.Context, id string, input PendingInput) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set pending input: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("missions set pending input begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	input.AskedAt = time.Now().UTC()
	data, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("missions set pending input: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE missions SET
			pending_input = $2, asks_used = asks_used + 1, updated_at = now()
		WHERE id = $1`, id, data); err != nil {
		return fmt.Errorf("missions set pending input: %w", err)
	}
	payload := map[string]any{
		"question": input.Question, "kind": input.Kind, "options": input.Options,
		"proposed_default": input.ProposedDefault, "phase": string(input.Phase),
	}
	if err := appendEventTx(ctx, tx, id, "mission.input_requested", payload, "live"); err != nil {
		return fmt.Errorf("missions set pending input: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("missions set pending input commit: %w", err)
	}
	if s.hub != nil {
		s.hub.Publish(Signal{Kind: "mission", ID: id})
	}
	return nil
}

// ParkAskUser satisfies askUserParker (asktool.go): the tool's own
// Execute callback into the store, kept as a thin alias so the
// runner-facing interface name documents ask_user's specific purpose
// rather than exposing SetPendingInput's more general store-level name.
func (s *Store) ParkAskUser(ctx context.Context, missionID string, input PendingInput) error {
	return s.SetPendingInput(ctx, missionID, input)
}

// ClearPendingInput drops a mission's parked question once it's
// answered or timed out.
func (s *Store) ClearPendingInput(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions clear pending input: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET
			pending_input = NULL, updated_at = now()
		WHERE id = $1`, id); err != nil {
		return fmt.Errorf("missions clear pending input: %w", err)
	}
	return nil
}

// ParkedInput is one PendingInputs row: just enough for the timeout
// sweep's elapsed-time decision, mirroring ParkedPermission. Name and
// Goal let the sweep's notification name the mission (PRTitle takes a
// Mission, so both are carried rather than a pre-computed title).
type ParkedInput struct {
	MissionID string
	Name      string
	Goal      string
	Input     PendingInput
}

// PendingInputs returns every mission currently parked on pending_input,
// the ask-timeout sweep's input.
func (s *Store) PendingInputs(ctx context.Context) ([]ParkedInput, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions pending inputs: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT id, name, goal, pending_input FROM missions WHERE pending_input IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("missions pending inputs: %w", err)
	}
	defer rows.Close()
	out := []ParkedInput{}
	for rows.Next() {
		var id, name, goal string
		var raw []byte
		if err := rows.Scan(&id, &name, &goal, &raw); err != nil {
			return nil, fmt.Errorf("missions pending inputs: %w", err)
		}
		var p PendingInput
		if err := json.Unmarshal(raw, &p); err != nil {
			continue // corrupt row: skip rather than fail the whole sweep
		}
		out = append(out, ParkedInput{MissionID: id, Name: name, Goal: goal, Input: p})
	}
	return out, rows.Err()
}

// AnswerPendingInput resolves a mission's parked question with answer
// (an operator's own text, or the proposed_default on timeout): clears
// pending_input, appends the given event kind (mission.input_answered
// or mission.input_timed_out) and payload, and resumes the mission from
// waiting_for_input back to idle so the next Drive re-enters the same
// phase the question was asked in: the answer rides into that turn's
// prompt via the progress log (recordProgress), not through this
// method, which only owns the park/resume transition.
func (s *Store) AnswerPendingInput(ctx context.Context, id, eventKind string, payload map[string]any) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions answer pending input: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("missions answer pending input begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `UPDATE missions SET
			pending_input = NULL, status = 'idle', updated_at = now()
		WHERE id = $1`, id); err != nil {
		return fmt.Errorf("missions answer pending input: %w", err)
	}
	if err := appendEventTx(ctx, tx, id, eventKind, payload, "live"); err != nil {
		return fmt.Errorf("missions answer pending input: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("missions answer pending input commit: %w", err)
	}
	if s.hub != nil {
		s.hub.Publish(Signal{Kind: "mission", ID: id})
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

// SetDiscoverNotes stores the discover phase's findings for the
// planner. Like SetLastEvidence it bypasses the state machine: written
// mid-phase, not at an Advance boundary.
func (s *Store) SetDiscoverNotes(ctx context.Context, id, notes string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("missions set discover notes: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET discover_notes = $2, updated_at = now() WHERE id = $1`,
		id, notes); err != nil {
		return fmt.Errorf("missions set discover notes: %w", err)
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
// machine like SetDiscoverNotes/SetLastEvidence: detection happens
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
	// An unrecognized phase is treated as terminal, not writable: unlike
	// scanMission's read path (which must always return something),
	// corruption discovered here should freeze the mission and refuse
	// the write rather than let stale code overwrite an unreadable row.
	if phase, ok := parsePhase(currentPhase); !ok || phase.Terminal() {
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
	// machine's own done/failed) also clears any pending_permission
	// detail — a dead mission can never resolve a park (the API's
	// answer-permission handler checks phase first and would already
	// reject it), so leaving the column populated only stales the
	// Timeline's "Allow" banner for a mission that's no longer running.
	clearPending := t.Next.Phase.Terminal()
	if _, err := tx.Exec(ctx, `UPDATE missions SET
			phase = $2, status = $3, pause_reason = $4, pause_message = '', iteration = $5, max_iterations = $6,
			consecutive_failures = $7, last_gap_fingerprint = $8, stall_count = $9, replan_used = $11, updated_at = now(),
			pending_permission = CASE WHEN $10 THEN NULL ELSE pending_permission END
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

// SetProvisioned checks-and-writes workspace/branch/base_commit in one
// txn (SELECT ... FOR UPDATE across active missions sharing the same
// workspace+branch), refusing with ErrBranchConflict if another ACTIVE
// mission (phase NOT IN ('done','failed')) already holds it. worktree
// is no longer persisted (issue #479): it's a fixed derivation of
// workspace (Mission.WorktreePath), so the caller-provided value is
// only used by ensureProvisioned itself, never written here.
func (s *Store) SetProvisioned(ctx context.Context, id, workspace, branch, baseCommit string) error {
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
			workspace = $2, branch = $3, base_commit = $4, updated_at = now()
		WHERE id = $1`, id, workspace, branch, baseCommit); err != nil {
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
//
// A delegated executor run is one Advance that can outlast staleAfter
// many times over and never touches the mission row while the CLI
// runs; its liveness signal is the executor.progress event the poll
// loop appends (issue #497). A mission with one newer than staleAfter
// is mid-turn, not stale, whatever updated_at says.
func (s *Store) RecoverStaleWorking(ctx context.Context, staleAfter time.Duration) ([]Mission, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions recover stale working: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+missionColumns+` FROM missions m
		WHERE m.status = 'working' AND m.updated_at < now() - $1::interval
		  AND NOT EXISTS (
		    SELECT 1 FROM mission_events e
		    WHERE e.mission_id = m.id AND e.kind = 'executor.progress'
		      AND e.created_at >= now() - $1::interval)`, staleAfter)
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
// in its destinations entries, the guard destinations.Store.Delete
// calls before removing a row, so a live mission's delivery target
// can't vanish out from under it. A historical (terminal) mission's
// reference never blocks deletion.
func (s *Store) ActiveMissionReferencesDestination(ctx context.Context, destinationID string) (bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return false, fmt.Errorf("missions active destination reference: %w", err)
	}
	var exists bool
	err = db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM missions
		WHERE phase NOT IN ('done', 'failed')
		AND EXISTS (SELECT 1 FROM jsonb_array_elements(destinations) d WHERE d->>'destination_id' = $1)
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

// BackoffPausedMission is PausedByReason's row: just enough to drive
// the auto-resume ladders (sweep.go's autoResumeBackoff and
// autoResumeInfra) without the cost of a full Mission scan.
type BackoffPausedMission struct {
	ID        string
	UpdatedAt time.Time
}

// BackoffPaused returns every mission currently paused with
// pause_reason='backoff' — autoResumeBackoff's input.
func (s *Store) BackoffPaused(ctx context.Context) ([]BackoffPausedMission, error) {
	return s.PausedByReason(ctx, string(PauseBackoff))
}

// PausedByReason returns every mission currently paused with the given
// pause_reason — shared by autoResumeBackoff (reason="backoff") and
// autoResumeInfra (reason="infra").
func (s *Store) PausedByReason(ctx context.Context, reason string) ([]BackoffPausedMission, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("missions paused by reason: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT id, updated_at FROM missions WHERE status = 'paused' AND pause_reason = $1`, reason)
	if err != nil {
		return nil, fmt.Errorf("missions paused by reason: %w", err)
	}
	defer rows.Close()
	out := []BackoffPausedMission{}
	for rows.Next() {
		var m BackoffPausedMission
		if err := rows.Scan(&m.ID, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("missions paused by reason: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountBackoffPauses counts how many times missionID has previously
// paused for backoff (mission.paused events with reason=backoff) —
// autoResumeBackoff's input to the resume ladder.
func (s *Store) CountBackoffPauses(ctx context.Context, missionID string) (int, error) {
	return s.CountPausesByReason(ctx, missionID, string(PauseBackoff))
}

// CountPausesByReason counts how many times missionID has previously
// paused for the given reason (mission.paused events with a matching
// payload reason) — shared by autoResumeBackoff and autoResumeInfra's
// resume ladders.
func (s *Store) CountPausesByReason(ctx context.Context, missionID, reason string) (int, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("missions count pauses by reason: %w", err)
	}
	var n int
	err = db.QueryRow(ctx, `SELECT count(*) FROM mission_events
		WHERE mission_id = $1 AND kind = 'mission.paused' AND payload->>'reason' = $2`, missionID, reason).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("missions count pauses by reason: %w", err)
	}
	return n, nil
}
