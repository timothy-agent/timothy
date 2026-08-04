// Package missions implements Phase 1 of the mission engine: an
// agent-driven, long-running unit of work that walks a fixed phase
// pipeline (research -> plan -> execute -> review -> done|failed)
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
	ID      string `json:"id"`
	Goal    string `json:"goal"`
	Kind    string `json:"kind"` // coding | research | scheduled
	AgentID string `json:"agent_id,omitempty"`
	// PromptOverlay is a snapshot of the creating agent's overlay text
	// at create time — see 0010_missions.sql for why this isn't a live
	// agent lookup.
	PromptOverlay string         `json:"prompt_overlay,omitempty"`
	Phase         Phase          `json:"phase"`
	Status        Status         `json:"status"`
	PauseReason   PauseReason    `json:"pause_reason,omitempty"`
	PauseMessage  string         `json:"pause_message,omitempty"`
	Workspace     string         `json:"workspace,omitempty"`
	Worktree      string         `json:"worktree,omitempty"`
	Branch        string         `json:"branch,omitempty"`
	BaseCommit    string         `json:"base_commit,omitempty"`
	Spec          Spec           `json:"spec"`
	Progress      []ProgressNote `json:"progress"`
	// LastEvidence is the most recent worker mission_status call's
	// evidence text — carried from execute into review so a
	// research/scheduled mission (whose baseline diff is always empty,
	// having touched no tracked files) still gives the reviewer
	// something real to judge instead of nothing.
	LastEvidence        string   `json:"last_evidence,omitempty"`
	Iteration           int      `json:"iteration"`
	MaxIterations       int      `json:"max_iterations"`
	ConsecutiveFailures int      `json:"consecutive_failures"`
	LastGapFingerprint  string   `json:"last_gap_fingerprint,omitempty"`
	StallCount          int      `json:"stall_count"`
	BudgetUSD           *float64 `json:"budget_usd,omitempty"`
	Route               string   `json:"route"`
	ReviewRoute         string   `json:"review_route"`
	// EscalationRoute, when non-empty, is the route worker turns switch
	// to after a worker failure or review rework — instead of burning
	// iterations on a model that already proved too weak for the unit.
	// Empty disables escalation.
	EscalationRoute   string `json:"escalation_route,omitempty"`
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
	// SessionID is a hidden, non-chat-facing session row this mission's
	// worker/reviewer/planner turns run under — loop.Agent's tool-call
	// bookkeeping (session_events, audit) hard-requires a real session
	// id, which a mission otherwise has no reason to have.
	SessionID string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
