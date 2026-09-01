// Package missions implements Phase 1 of the mission engine: an
// agent-driven, long-running unit of work that walks a fixed phase
// pipeline (discover -> plan -> generate -> prove -> result ->
// done|failed) under a pure state machine, executed by native
// (in-process) model turns via loop.Agent. Delegated CLI executors
// (claude/codex subprocess shelling) are explicitly out of scope for
// this phase.
package missions

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	// at create time, see migrations/0001_init.sql for why this isn't a live
	// agent lookup.
	PromptOverlay string `json:"prompt_overlay,omitempty"`
	Phase         Phase  `json:"phase"`
	Status        Status `json:"status"`
	// FailureReason is derived (no column) from this mission's latest
	// mission.failed event's payload.reason — "cancelled" or
	// "max_iterations" (statemachine.go) — set only by Store.List/Get for
	// a phase=failed mission. Empty for every other mission.
	FailureReason string         `json:"failure_reason,omitempty"`
	PauseReason   PauseReason    `json:"pause_reason,omitempty"`
	PauseMessage  string         `json:"pause_message,omitempty"`
	Workspace     string         `json:"workspace,omitempty"`
	Branch        string         `json:"branch,omitempty"`
	BaseCommit    string         `json:"base_commit,omitempty"`
	Plan          Plan           `json:"plan"`
	Progress      []ProgressNote `json:"progress"`
	// LastEvidence is the most recent worker mission_status call's
	// evidence text — carried from execute into review so a
	// general mission (whose baseline diff is always empty,
	// having touched no tracked files) still gives the reviewer
	// something real to judge instead of nothing.
	LastEvidence string `json:"last_evidence,omitempty"`
	// DiscoverNotes is the discover phase's findings, carried forward
	// into the plan phase's prompt (see runner.go's PlanSession).
	DiscoverNotes       string `json:"discover_notes,omitempty"`
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
	// PlanRoute, when non-empty, is the route oversight phases (discover,
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
	// execute, PlanRouteModel backs discover/plan, ReviewRouteModel falls
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
	// Flow is the phase set this mission runs (D-090, issue #459),
	// chosen once at create time, snapshotted here, never model-
	// mutable. FlowLight (D-069, general kind only) skips discover/plan/
	// prove entirely: born in phase=generate, one bare worker turn, the
	// final worker message is the deliverable, then result/done. Flow is
	// the single source of truth (issue #479 dropped the redundant light
	// column); code that means "light mission" tests Flow == FlowLight.
	// The API layer derives a "light" JSON field from this for the web
	// client (api/missions.go's missionResponse).
	Flow Flow `json:"flow"`
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
	// same as SetPlan/SetProvisioned: parking happens mid-turn, not at
	// an Advance boundary.
	PendingPermissionTool      string `json:"pending_permission_tool,omitempty"`
	PendingPermissionArgs      string `json:"pending_permission_args,omitempty"`
	PendingPermissionDanger    string `json:"pending_permission_danger,omitempty"`
	PendingPermissionRationale string `json:"pending_permission_rationale,omitempty"`
	// PendingPermissionParkedAt is when this park started (issue #445),
	// the periodic timeout sweep's elapsed-time input, also exposed so
	// the UI can show how long a request has been waiting.
	PendingPermissionParkedAt time.Time `json:"pending_permission_parked_at,omitempty"`
	// PermissionTimeoutSeconds overrides the global
	// settings.ValuePermissionTimeoutSeconds for this mission alone; nil
	// inherits the global setting. Never negative: the sweep treats a
	// stored value <= 0 the same as "disabled," matching the global
	// setting's own 0-means-off convention.
	PermissionTimeoutSeconds *int `json:"permission_timeout_seconds,omitempty"`
	// PendingInput is ask_user's park (D-088, issue #457): non-nil while
	// a phase turn is waiting on the operator's answer, cleared once
	// answered or timed out. A second park kind alongside
	// PendingPermission, the two never overlap since a turn only ever
	// parks on one or the other at a time.
	PendingInput *PendingInput `json:"pending_input,omitempty"`
	// AsksUsed counts ask_user calls this mission has spent (D-088);
	// enforced against askBudget in asktool.go.
	AsksUsed int `json:"asks_used"`
	// AutoApproveTools, when true (the default for new missions), grants
	// the hidden session standing approval for any shell call the
	// danger classifier rates safe — a mission runs for hours
	// unattended, and per-command-shape approval built for a human
	// watching a chat session would otherwise park it on every novel
	// (but harmless) shell invocation. Destructive-classified commands
	// still always ask: tools.Permissions.Resolve forces that
	// regardless of any grant, so this cannot weaken that guarantee.
	AutoApproveTools bool `json:"auto_approve_tools"`
	// AutoApprovePlan, when true (the default), advances straight from
	// plan to generate the moment a plan lands, exactly as every
	// mission has always worked. false parks the mission on
	// PauseApproval instead (D-087, issue #456), waiting for an
	// operator to approve/replan/rediscover. Snapshotted at create
	// time, same as AutoApproveTools; scheduler.go and the workflow
	// engine both force this true regardless of template/step input:
	// an unattended mission has nobody to approve its plan.
	AutoApprovePlan bool   `json:"auto_approve_plan"`
	ScheduleID      string `json:"schedule_id,omitempty"`
	// ParentMissionID names the terminal mission this one follows up on
	// (api/missions.go's create) — empty for an ordinary mission.
	ParentMissionID string `json:"parent_mission_id,omitempty"`
	// Sources is the union of what were five separate mission columns
	// (issue #481: repo_url, connector_id, attachments, parent_context,
	// referenced_context): a jsonb array of entries, one per clone
	// source (kind "github"), attached PDF (kind "pdf"), parent-mission
	// outcome digest (kind "mission", set at follow-up create), or
	// picked #-mention reference (kind "chat"/"mission"/"kb", one entry
	// per resolved pick instead of one concatenated string). Rendered
	// into this mission's discover/plan/work prompts by packet.go's
	// renderSources, preserving the pre-#481 render order exactly:
	// parent-mission digest, then referenced picks, then attached PDFs.
	Sources []SourceEntry `json:"sources,omitempty"`
	// SessionID is a hidden, non-chat-facing session row this mission's
	// worker/reviewer/planner turns run under — loop.Agent's tool-call
	// bookkeeping (session_events, audit) hard-requires a real session
	// id, which a mission otherwise has no reason to have.
	SessionID string `json:"-"`
	// Destinations names every sink this mission's outcome delivers to
	// or acts on in the result phase's step (issue #480, replacing the
	// five columns destination_ids/on_complete/branch_pattern/
	// commit_style/promote_kb_collection_id): destination/webhook/email/
	// telegram entries (D-061, validated against the operator-owned
	// destinations table at create time), a "kb" entry (D-081), and a
	// "github" entry (the operator's consent-at-create push/push_pr
	// choice). Never model-decided. Each entry's delivered_at/error
	// fields are the result step's own delivery-state record, written
	// back by runResult (see SetDestinations).
	Destinations []DestinationEntry `json:"destinations,omitempty"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	// WorkflowRunID/WorkflowStep name the workflow run and step
	// (internal/brain/workflows) this mission was spawned as, if any —
	// empty for an ordinary mission. Set only at create time; the
	// workflow engine reads terminal missions, it never mutates them.
	WorkflowRunID string `json:"workflow_run_id,omitempty"`
	WorkflowStep  string `json:"workflow_step,omitempty"`
	// ArtifactRefs are this mission's declared artifact files, best-
	// effort copied into the attachment store on the terminal done
	// transition (driver.go's copyArtifacts) — survive workspace
	// deletion, unlike the live-workspace files ArtifactsSection
	// browses. Empty until that copy runs, or if it finds nothing to
	// copy.
	ArtifactRefs []ArtifactRef `json:"artifact_refs,omitempty"`
}

// ArtifactRef is one mission artifact file copied into the attachment
// store at terminal done — id names an attachments-store row, mirrors
// a "pdf" SourceEntry's shape (id/mime/name, never bytes -- D-045).
type ArtifactRef struct {
	ID   string `json:"id"`
	Mime string `json:"mime"`
	Name string `json:"name,omitempty"`
}

// destinationKind names DestinationEntry.Destination's known values:
// "email"/"webhook"/"telegram" ride an operator-created destinations
// table row (DestinationID); "kb" and "github" are harness-native
// sinks with no such row.
const (
	DestinationKindKB     = "kb"
	DestinationKindGitHub = "github"
)

// DestinationEntry is one sink this mission's result phase delivers to
// or acts on (issue #480): the union of what were five separate mission
// columns (destination_ids, promote_kb_collection_id, on_complete,
// branch_pattern, commit_style). Destination names the kind
// ("email"/"webhook"/"telegram"/"kb"/"github"); DestinationID, when
// set, names the operator-owned destinations table row carrying that
// sink's auth/channel config. DeliveredAt/Error are the result step's
// own delivery-state record (runResult, SetDestinations): DeliveredAt
// (RFC3339) is set on success, Error on failure, mutually exclusive,
// both empty before the first attempt. A retry skips an entry whose
// DeliveredAt is already set and retries one whose Error is set (or
// which was never attempted).
type DestinationEntry struct {
	Destination   string `json:"destination"`
	DestinationID string `json:"destination_id,omitempty"`
	// CollectionID names a kb_collections row for a "kb" entry (D-081):
	// the collection this mission's markdown artifacts promote into.
	CollectionID string `json:"collection_id,omitempty"`
	// ConnectorID/RepoURL/Mode/BranchPattern/CommitStyle are a "github"
	// entry's fields: the operator's consent-at-create push automation
	// (mirrors the old on_complete/branch_pattern/commit_style columns).
	// Mode is "push" or "push_pr". BranchPattern/CommitStyle empty means
	// "use the settings default," resolved fresh at provisioning/commit
	// time (driver.go), same precedence the old columns had.
	ConnectorID   string `json:"connector_id,omitempty"`
	RepoURL       string `json:"repo_url,omitempty"`
	Mode          string `json:"mode,omitempty"`
	BranchPattern string `json:"branch_pattern,omitempty"`
	CommitStyle   string `json:"commit_style,omitempty"`
	// CreateIfMissing (issue #483), github entries only: when the
	// entry's repo doesn't exist yet at delivery time, create it
	// through ConnectorID's credential instead of failing the push/PR.
	// "" RepoURL with this set derives the repo name from the mission
	// (see Completer.ensureRepo); false (the default) never creates:
	// delivery fails honestly into Error instead of inventing a repo.
	CreateIfMissing bool   `json:"create_if_missing,omitempty"`
	DeliveredAt     string `json:"delivered_at,omitempty"`
	Error           string `json:"error,omitempty"`
}

// GitHubEntry returns this mission's "github" destination entry, if
// any: there is at most one per mission (api/missions.go's create
// only ever appends one from on_complete). ok is false when the
// mission has none.
func (m Mission) GitHubEntry() (DestinationEntry, bool) {
	for _, e := range m.Destinations {
		if e.Destination == DestinationKindGitHub {
			return e, true
		}
	}
	return DestinationEntry{}, false
}

// OnComplete is the effective on_complete value the pre-#480 Mission
// column used to carry: "" when there is no github entry, else its
// Mode ("push" or "push_pr"). Kept as a read-only derivation for the
// handful of call sites (Completer, NotPushable, the missions builtin
// tool) that only ever need this one field, not the full entry.
func (m Mission) OnComplete() string {
	e, ok := m.GitHubEntry()
	if !ok {
		return ""
	}
	return e.Mode
}

// KBCollectionID is the effective promote_kb_collection_id the
// pre-#480 Mission column used to carry: "" when the mission has no
// "kb" destination entry.
func (m Mission) KBCollectionID() string {
	for _, e := range m.Destinations {
		if e.Destination == DestinationKindKB {
			return e.CollectionID
		}
	}
	return ""
}

// DestinationIDs returns every entry's DestinationID in order, empty
// entries (kb/github, which have none) skipped: the shape
// destinations.Deliverer historically took, kept for call sites that
// only need the id list, not the full entry (e.g. the webhook payload's
// dedup against mission_events was the pre-#480 mechanism; delivery
// dedup itself now reads Destinations directly, see driver.go's
// deliverToDestinations).
func (m Mission) DestinationIDs() []string {
	var ids []string
	for _, e := range m.Destinations {
		if e.DestinationID != "" {
			ids = append(ids, e.DestinationID)
		}
	}
	return ids
}

// sourceKind names SourceEntry.Source's known values.
const (
	SourceKindGitHub  = "github"
	SourceKindPDF     = "pdf"
	SourceKindMission = "mission"
	SourceKindChat    = "chat"
	SourceKindKB      = "kb"
)

// SourceEntry is one input a mission's discover/plan/work prompts draw
// on (issue #481, replacing the five separate repo_url/connector_id/
// attachments/parent_context/referenced_context columns): Source names
// the kind, the rest of the fields are populated per kind.
//
//   - "github": ConnectorID/RepoURL -- the repo this coding mission
//     clones from instead of self-initializing an empty one.
//   - "pdf": Name/Markdown -- an attached document, ID names an
//     attachments-store row. Markdown is that PDF's markitdown
//     conversion, snapshotted ONCE at create time -- the same rationale
//     as chat's validateAttachments: re-converting on every turn would
//     re-call the markitdown sidecar every turn, and any output drift
//     would rewrite an earlier rendered prompt.
//   - "mission" (as a parent lineage snapshot): MissionID/Digest -- the
//     parent mission's outcome digest (OutcomeDigest), snapshotted at
//     follow-up create time.
//   - "chat"/"mission"/"kb" (as a picked #-mention reference):
//     SessionID/MissionID/DocID plus Digest -- one entry per resolved
//     composer pick, additive to the parent-lineage "mission" entry (a
//     follow-up mission can also carry its own picked references).
type SourceEntry struct {
	Source      string `json:"source"`
	ConnectorID string `json:"connector_id,omitempty"`
	RepoURL     string `json:"repo_url,omitempty"`
	ID          string `json:"id,omitempty"`
	Mime        string `json:"mime,omitempty"`
	Name        string `json:"name,omitempty"`
	Markdown    string `json:"markdown,omitempty"`
	MissionID   string `json:"mission_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	DocID       string `json:"doc_id,omitempty"`
	Digest      string `json:"digest,omitempty"`
}

// GitHubSource returns this mission's "github" source entry, if any:
// there is at most one per mission (api/missions.go's create only ever
// builds one from repo_url/connector_id). ok is false when the mission
// has none -- the self-init'd empty repo case.
func (m Mission) GitHubSource() (SourceEntry, bool) {
	for _, e := range m.Sources {
		if e.Source == SourceKindGitHub {
			return e, true
		}
	}
	return SourceEntry{}, false
}

// RepoURL is the effective repo_url the pre-#481 Mission column used
// to carry: "" when the mission has no "github" source entry.
func (m Mission) RepoURL() string {
	e, _ := m.GitHubSource()
	return e.RepoURL
}

// ConnectorID is the effective connector_id the pre-#481 Mission
// column used to carry: "" when the mission has no "github" source
// entry.
func (m Mission) ConnectorID() string {
	e, _ := m.GitHubSource()
	return e.ConnectorID
}

// Attachments returns every "pdf" source entry, in order -- the
// pre-#481 Mission.Attachments column's replacement.
func (m Mission) Attachments() []SourceEntry {
	var out []SourceEntry
	for _, e := range m.Sources {
		if e.Source == SourceKindPDF {
			out = append(out, e)
		}
	}
	return out
}

// ParentLineageID marks the one "mission" source entry that is the
// parent-lineage snapshot (SourceEntry.ID), distinguishing it from a
// referenced #-mention pick of kind "mission" -- a plain MissionID ==
// ParentMissionID match would break once the row's parent_mission_id
// FK is cleared (ON DELETE SET NULL, the parent got deleted), but the
// digest snapshot must survive independent of that FK. Set by every
// caller building a follow-up mission's lineage entry
// (followup.go, api/missions.go's create).
const ParentLineageID = "parent"

// ParentContext returns the parent-lineage "mission" source entry's
// digest -- the pre-#481 Mission.ParentContext column's replacement.
// "" when there is no lineage snapshot (an ordinary, non-follow-up
// mission).
func (m Mission) ParentContext() string {
	for _, e := range m.Sources {
		if e.Source == SourceKindMission && e.ID == ParentLineageID {
			return e.Digest
		}
	}
	return ""
}

// ReferencedContext renders every picked #-mention reference entry
// (kinds "chat"/"kb", plus any "mission" entry that is NOT the parent
// lineage snapshot ParentContext already covers) into one digest, the
// same "name:\ndigest\n\n" shape api/missions.go's pre-#481
// resolveReferenceContext produced -- the pre-#481
// Mission.ReferencedContext column's replacement.
func (m Mission) ReferencedContext() string {
	var b strings.Builder
	for _, e := range m.Sources {
		switch e.Source {
		case SourceKindChat, SourceKindKB:
		case SourceKindMission:
			if e.ID == ParentLineageID {
				continue // the lineage snapshot, not a referenced pick
			}
		default:
			continue
		}
		if e.Digest == "" {
			continue
		}
		name := e.Name
		if name == "" {
			name = e.MissionID + e.SessionID + e.DocID
		}
		fmt.Fprintf(&b, "%s:\n%s\n\n", name, e.Digest)
	}
	return strings.TrimRight(b.String(), "\n")
}

// WorktreePath derives the worktree directory (issue #479 dropped the
// stored worktree column): worktree.go's Provision always lays a
// needsWorktree mission's git checkout at workspace/wt, so this is
// pure derivation, never a fresh lookup. "" when the mission's kind/
// flow policy has no worktree, or Workspace itself is empty (not yet
// provisioned).
func (m Mission) WorktreePath() string {
	if m.Workspace == "" || !missionPolicyFor(m).needsWorktree {
		return ""
	}
	return filepath.Join(m.Workspace, "wt")
}

// WorkRoot is where mission workers/verify/review actually operate:
// the worktree for coding missions, the plain workspace dir otherwise.
func (m Mission) WorkRoot() string {
	if wt := m.WorktreePath(); wt != "" {
		return wt
	}
	return m.Workspace
}

// Plan is the mission's submitted plan: an ordered list of units, each
// verified independently by RunVerify before the mission can advance
// past it.
type Plan struct {
	Units []PlanUnit `json:"units"`
	// Infeasible marks a plan the planner refused to write because the
	// goal cannot be achieved as stated (D-077); Units is empty when
	// this is true. InfeasibleReason carries why (json tag "reason" to
	// match the submit_plan tool schema's property name).
	Infeasible       bool   `json:"infeasible,omitempty"`
	InfeasibleReason string `json:"reason,omitempty"`
	// Assumptions lists ambiguities the planner resolved silently and
	// the default it chose for each (issue #446): informational only,
	// never a gate — the operator catches a wrong guess via a steering
	// note, not a pause.
	Assumptions []PlanAssumption `json:"assumptions,omitempty"`
}

// PlanAssumption is one ambiguity the planner resolved on its own,
// paired with the default it picked.
type PlanAssumption struct {
	Assumption string `json:"assumption"`
	Default    string `json:"default"`
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

// PendingInput is ask_user's park detail (D-088, issue #457): the
// structured question a phase turn is waiting on the operator to
// answer. Kind is "mcq", "yes_no", or "open"; Options is populated
// only for "mcq". ProposedDefault is required on every ask: the
// timeout sweep applies it verbatim if nobody answers in time.
type PendingInput struct {
	Question        string    `json:"question"`
	Kind            string    `json:"kind"`
	Options         []string  `json:"options,omitempty"`
	ProposedDefault string    `json:"proposed_default"`
	AskedAt         time.Time `json:"asked_at"`
	Phase           Phase     `json:"phase"`
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
	// ErrNotAwaitingApproval guards the three plan-approval verbs
	// (D-087, issue #456): approve/replan/rediscover are only valid
	// while the mission is parked on PauseApproval.
	ErrNotAwaitingApproval = errors.New("mission is not awaiting plan approval")
)
