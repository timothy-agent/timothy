// Package missions implements Phase 1 of the mission engine: an
// agent-driven, long-running unit of work that walks a fixed phase
// pipeline (explore -> plan -> execute -> review -> done|failed)
// under a pure state machine, executed by native (in-process) model
// turns via loop.Agent. Delegated CLI executors (claude/codex
// subprocess shelling) are explicitly out of scope for this phase.
package missions

import (
	"encoding/json"
	"errors"
	"time"
)

// Mission is the API/DB shape of one missions row.
type Mission struct {
	ID   string `json:"id"`
	Goal string `json:"goal"`
	// Name is a short display name generated once from Goal, the same
	// way a chat session's title is (chat.go's autoTitle) — see
	// Store.SetNameIfEmpty. Scheduler-fired missions get the schedule's
	// own name directly, no LLM call. Empty means generation hasn't
	// landed yet; the UI falls back to a truncated Goal.
	Name    string `json:"name,omitempty"`
	Kind    string `json:"kind"` // coding | general
	AgentID string `json:"agent_id,omitempty"`
	// PromptOverlay is a snapshot of the creating agent's overlay text
	// at create time — see 0010_missions.sql for why this isn't a live
	// agent lookup.
	PromptOverlay string `json:"prompt_overlay,omitempty"`
	// Knowledge is a snapshot of the creating agent's kb_collections
	// allowlist at create time, same reasoning as PromptOverlay above.
	// Empty means kb_search is never offered on this mission's turns.
	Knowledge []string `json:"knowledge,omitempty"`
	Phase     Phase    `json:"phase"`
	Status        Status `json:"status"`
	// FailureReason is derived (no column) from this mission's latest
	// mission.failed event's payload.reason — "cancelled" or
	// "max_iterations" (statemachine.go) — set only by Store.List/Get for
	// a phase=failed mission. Empty for every other mission.
	FailureReason string      `json:"failure_reason,omitempty"`
	PauseReason   PauseReason `json:"pause_reason,omitempty"`
	PauseMessage  string      `json:"pause_message,omitempty"`
	Workspace     string      `json:"workspace,omitempty"`
	Worktree      string      `json:"worktree,omitempty"`
	Branch        string      `json:"branch,omitempty"`
	BaseCommit    string      `json:"base_commit,omitempty"`
	// RepoURL is the GitHub repo this coding mission was cloned from
	// (https clone URL) — empty means the self-init'd empty repo
	// Workspace.Provision otherwise creates. ConnectorID names the
	// github-kind connectors row whose PAT authenticated the clone; the
	// token itself is never stored, only resolved fresh at provisioning.
	RepoURL     string `json:"repo_url,omitempty"`
	ConnectorID string `json:"connector_id,omitempty"`
	// BranchPattern/CommitStyle are this mission's own override of the
	// settings-configured git strategy defaults (git_branch_pattern/
	// git_commit_style), snapshotted at create time: "" means "use the
	// settings default," resolved fresh at provisioning/commit time
	// (driver.go), never baked in here. See branchtemplate.go for the
	// template placeholders and style values.
	BranchPattern string `json:"branch_pattern,omitempty"`
	CommitStyle   string `json:"commit_style,omitempty"`
	// OnComplete is the operator's consent-at-create choice for what
	// happens when this mission reaches phase=done: "" (default) does
	// nothing, "push" pushes the branch, "push_pr" pushes then opens a
	// pull request. Only ever set at create time (api/missions.go's
	// validation); the harness (driver.go) executes it automatically on
	// the done transition using the SAME push/PR code path the manual
	// push/pr endpoints use.
	OnComplete string         `json:"on_complete,omitempty"`
	Spec       Spec           `json:"spec"`
	Progress   []ProgressNote `json:"progress"`
	// LastEvidence is the most recent worker mission_status call's
	// evidence text — carried from execute into review so a
	// general mission (whose baseline diff is always empty,
	// having touched no tracked files) still gives the reviewer
	// something real to judge instead of nothing.
	LastEvidence string `json:"last_evidence,omitempty"`
	// ExploreNotes is the explore phase's findings, carried forward
	// into the plan phase's prompt (see runner.go's PlanSession).
	ExploreNotes        string `json:"explore_notes,omitempty"`
	Iteration           int    `json:"iteration"`
	MaxIterations       int    `json:"max_iterations"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	LastGapFingerprint  string `json:"last_gap_fingerprint,omitempty"`
	StallCount          int    `json:"stall_count"`
	// ReplanUsed reports whether this mission already spent its one
	// automatic replan-on-stall attempt (statemachine.go's
	// stepWorkerRetry/stepReviewRework).
	ReplanUsed     bool     `json:"replan_used"`
	BudgetAmount   *float64 `json:"budget_amount,omitempty"`
	BudgetCurrency string   `json:"budget_currency,omitempty"`
	Route          string   `json:"route"`
	ReviewRoute    string   `json:"review_route"`
	// PlanRoute, when non-empty, is the route oversight phases (explore,
	// plan, replan) run on instead of Route — "GLM plans, local
	// executes": the oversight phases can run on a strong model while
	// Route stays the cheap/local worker route. Empty means Route covers
	// everything, exactly as before this field existed. Precedence for
	// review specifically is ReviewRoute > PlanRoute > Route (see
	// oversightRoute) — ReviewRoute is the more specific, already-shipped
	// override, so it still wins.
	PlanRoute string `json:"plan_route,omitempty"`
	// EscalationRoute, when non-empty, is the route worker turns switch
	// to after a worker failure or review rework — instead of burning
	// iterations on a model that already proved too weak for the unit.
	// Empty disables escalation.
	EscalationRoute string `json:"escalation_route,omitempty"`
	// RouteModel/PlanRouteModel/ReviewRouteModel (D-078) pin one phase
	// axis to one exact chain entry in the route it would otherwise
	// resolve, as "provider name/model" — empty keeps the first-usable
	// walk. Precedence mirrors the route helpers: RouteModel backs
	// execute, PlanRouteModel backs explore/plan, ReviewRouteModel falls
	// back ReviewRouteModel > PlanRouteModel > RouteModel (runner.go's
	// reviewRouteModel). Never validated to name a chain entry that
	// exists — the chain can change after create and the runtime already
	// falls back to first-usable when a pin doesn't match.
	RouteModel       string `json:"route_model,omitempty"`
	PlanRouteModel   string `json:"plan_route_model,omitempty"`
	ReviewRouteModel string `json:"review_route_model,omitempty"`
	// Harness is the execution strategy for this mission's worker turns,
	// snapshotted at create time (D-051): "" is native in-process
	// dispatch, "claude-cli" (etc) names a registered delegated executor
	// (internal/brain/missions/executor). Coding-only; a general mission
	// always runs native.
	Harness string `json:"harness,omitempty"`
	// Light missions (D-069, general kind only) skip explore/plan/
	// review entirely: born in phase=execute, one bare worker turn, the
	// final worker message is the deliverable.
	Light bool `json:"light"`
	// FinalOutput is a light mission's verbatim final worker message,
	// set on the done transition (driver.go's runExecute) — the
	// deliverable for destinations delivery and memory extraction.
	FinalOutput string `json:"final_output,omitempty"`
	// Environment selects the per-language sandbox image (D-05x) a
	// coding mission's container runs: "" is the base image. Unlike
	// Harness, there is no settings default — precedence is explicit
	// request -> auto-detect from repo markers at provisioning ->
	// base. Sticky once set (Store.SetEnvironment), never re-detected.
	// General missions never set this.
	Environment       string `json:"environment,omitempty"`
	PendingPermission string `json:"pending_permission,omitempty"`
	// PendingPermissionTool/Args/Danger/Rationale describe the parked
	// tool call for the UI — set alongside PendingPermission whenever a
	// worker/reviewer/planner turn parks on stream.EventPermissionRequest,
	// cleared together once the broker resolves it. Bypasses the state
	// machine entirely (SetPendingPermission/ClearPendingPermission),
	// same as SetSpec/SetProvisioned: parking happens mid-turn, not at
	// an Advance boundary.
	PendingPermissionTool      string `json:"pending_permission_tool,omitempty"`
	PendingPermissionArgs      string `json:"pending_permission_args,omitempty"`
	PendingPermissionDanger    string `json:"pending_permission_danger,omitempty"`
	PendingPermissionRationale string `json:"pending_permission_rationale,omitempty"`
	// AutoApproveSafe, when true (the default for new missions), grants
	// the hidden session standing approval for any shell call the
	// danger classifier rates safe — a mission runs for hours
	// unattended, and per-command-shape approval built for a human
	// watching a chat session would otherwise park it on every novel
	// (but harmless) shell invocation. Destructive-classified commands
	// still always ask: tools.Permissions.Resolve forces that
	// regardless of any grant, so this cannot weaken that guarantee.
	AutoApproveSafe bool   `json:"auto_approve_safe"`
	ScheduleID      string `json:"schedule_id,omitempty"`
	// ParentMissionID names the terminal mission this one follows up on
	// (api/missions.go's create) — empty for an ordinary mission.
	ParentMissionID string `json:"parent_mission_id,omitempty"`
	// ParentContext is the parent mission's outcome digest
	// (OutcomeDigest), snapshotted at follow-up create time — rendered
	// into this mission's explore/plan/work prompts.
	ParentContext string `json:"parent_context,omitempty"`
	// Attachments are PDF documents attached at create time, each
	// markitdown-converted ONCE (same rationale as chat's
	// validateAttachments) and snapshotted onto the row — rendered into
	// this mission's explore/plan/work prompts every turn.
	Attachments []MissionAttachment `json:"attachments,omitempty"`
	// SessionID is a hidden, non-chat-facing session row this mission's
	// worker/reviewer/planner turns run under — loop.Agent's tool-call
	// bookkeeping (session_events, audit) hard-requires a real session
	// id, which a mission otherwise has no reason to have.
	SessionID string `json:"-"`
	// DestinationIDs names the operator-created destinations (email,
	// webhook) this mission's outcome digest delivers to on the
	// terminal done transition (driver.go's deliverToDestinations) —
	// validated against the destinations table at create time
	// (api/missions.go), never model-decided.
	DestinationIDs []string  `json:"destination_ids,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	// WorkflowRunID/WorkflowStep name the workflow run and step
	// (internal/brain/workflows) this mission was spawned as, if any —
	// empty for an ordinary mission. Set only at create time; the
	// workflow engine reads terminal missions, it never mutates them.
	WorkflowRunID string `json:"workflow_run_id,omitempty"`
	WorkflowStep  string `json:"workflow_step,omitempty"`
}

// MissionAttachment is one PDF document attached at mission create
// time. ID names an attachments-store row. Markdown is that PDF's
// markitdown conversion, snapshotted ONCE at create time — the same
// rationale as chat's validateAttachments: re-converting on every turn
// would re-call the markitdown sidecar every turn, and any output
// drift would rewrite an earlier rendered prompt.
type MissionAttachment struct {
	ID       string `json:"id"`
	Mime     string `json:"mime"`
	Name     string `json:"name,omitempty"`
	Markdown string `json:"markdown,omitempty"`
}

// WorkRoot is where mission workers/verify/review actually operate:
// the worktree for coding missions, the plain workspace dir otherwise.
func (m Mission) WorkRoot() string {
	if m.Worktree != "" {
		return m.Worktree
	}
	return m.Workspace
}

// Spec is the mission's plan: an ordered list of units, each verified
// independently by RunVerify before the mission can advance past it.
type Spec struct {
	Units []PlanUnit `json:"units"`
	// Infeasible marks a plan the planner refused to write because the
	// goal cannot be achieved as stated (D-077); Units is empty when
	// this is true. InfeasibleReason carries why (json tag "reason" to
	// match the submit_plan tool schema's property name).
	Infeasible       bool   `json:"infeasible,omitempty"`
	InfeasibleReason string `json:"reason,omitempty"`
}

// PlanUnit is one item of the plan. Passes is flipped only by the
// harness (RunVerify + CheckArtifacts), never by model output, and
// only on that harness-run evidence.
type PlanUnit struct {
	Title     string `json:"title"`
	VerifyCmd string `json:"verify_cmd"`
	// Artifacts are workspace-relative paths this unit must produce.
	// The harness checks each exists and is non-empty BEFORE running
	// verify_cmd — a tautological verify_cmd (echo 'done') can no
	// longer fake completion when the declared artifact is missing.
	Artifacts []string `json:"artifacts,omitempty"`
	Passes    bool     `json:"passes"`
}

// ProgressNote is one append-only entry in the mission's progress log
// — durability for a stateless worker: the next fresh session reads
// this instead of any prior transcript.
type ProgressNote struct {
	At   time.Time `json:"at"`
	Note string    `json:"note"`
}

// Event is one row from mission_events.
type Event struct {
	MissionID   string          `json:"mission_id"`
	Seq         int64           `json:"seq"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
	Provenance  string          `json:"provenance"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// Notification is one row from the notifications inbox.
type Notification struct {
	ID        string    `json:"id"`
	MissionID string    `json:"mission_id"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// Sentinel errors the HTTP layer maps onto status codes, mirroring
// agents.ErrNotFound / ErrInUse.
var (
	ErrNotFound       = errors.New("not found")
	ErrBranchConflict = errors.New("workspace or branch already claimed by an active mission")
	ErrTerminal       = errors.New("mission already finished")
	// ErrNotTerminal guards Store.Delete: only a mission whose Phase has
	// reached done or failed (which includes cancelled — cancel ends as
	// mission.failed, there is no separate terminal status) may be
	// deleted, so a live mission's row/events/workspace can never vanish
	// out from under a running Driver.Advance.
	ErrNotTerminal = errors.New("mission is not terminal")
)
