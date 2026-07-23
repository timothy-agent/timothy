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
	ID                  string         `json:"id"`
	Goal                string         `json:"goal"`
	Kind                string         `json:"kind"` // coding | research | scheduled
	AgentID             string         `json:"agent_id,omitempty"`
	Phase               Phase          `json:"phase"`
	Status              Status         `json:"status"`
	PauseReason         PauseReason    `json:"pause_reason,omitempty"`
	PauseMessage        string         `json:"pause_message,omitempty"`
	Workspace           string         `json:"workspace,omitempty"`
	Worktree            string         `json:"worktree,omitempty"`
	Branch              string         `json:"branch,omitempty"`
	BaseCommit          string         `json:"base_commit,omitempty"`
	Spec                Spec           `json:"spec"`
	Progress            []ProgressNote `json:"progress"`
	Iteration           int            `json:"iteration"`
	MaxIterations       int            `json:"max_iterations"`
	ConsecutiveFailures int            `json:"consecutive_failures"`
	LastGapFingerprint  string         `json:"last_gap_fingerprint,omitempty"`
	StallCount          int            `json:"stall_count"`
	BudgetUSD           *float64       `json:"budget_usd,omitempty"`
	Route               string         `json:"route"`
	ReviewRoute         string         `json:"review_route"`
	PendingPermission   string         `json:"pending_permission,omitempty"`
	ScheduleID          string         `json:"schedule_id,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

// Spec is the mission's plan: an ordered list of units, each verified
// independently by RunVerify before the mission can advance past it.
type Spec struct {
	Units []PlanUnit `json:"units"`
}

// PlanUnit is one item of the plan. Passes is flipped only by the
// harness (RunVerify), never by model output, and only on verify_cmd's
// exit-code evidence.
type PlanUnit struct {
	Title     string `json:"title"`
	VerifyCmd string `json:"verify_cmd"`
	Passes    bool   `json:"passes"`
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

// Sentinel errors the HTTP layer maps onto status codes, mirroring
// agents.ErrNotFound / ErrInUse.
var (
	ErrNotFound       = errors.New("not found")
	ErrBranchConflict = errors.New("workspace or branch already claimed by an active mission")
	ErrTerminal       = errors.New("mission already finished")
)
