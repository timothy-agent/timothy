package missions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// driveTimeBound bounds one Drive call: long enough for a real
// multi-iteration mission to make meaningful progress in one process
// lifetime, short enough that a runaway mission doesn't pin a
// goroutine indefinitely across a deploy. A mission that hits the
// bound is left in whatever state the last successful Advance
// persisted (working, most likely) — NOT a dead end, since sweep.go's
// boot-time recoverWorking re-Drives any mission still 'working' when
// a process starts, and the periodic work-slot sweep retries anything
// left idle.
const driveTimeBound = 4 * time.Hour

// notifier is the transition-notification hook Driver calls after
// every successful ApplyTransition; notify.go's Notifier satisfies it
// (added in M3). nil is valid — M2 has no notifications wired yet.
type notifier interface {
	OnTransition(ctx context.Context, missionID string, before, after Status) error
}

// sessionCreator opens the hidden, non-chat-facing session every
// mission runs its worker/reviewer/planner turns under —
// *session.Store satisfies it. loop.Agent's tool-call bookkeeping
// (session_events, audit) hard-requires a real session id; a mission
// has no chat session of its own to supply one.
type sessionCreator interface {
	Create(ctx context.Context, title string) (string, error)
}

// sessionGranter is the narrow slice of *tools.Permissions Driver
// needs to pre-authorize a mission's hidden session — a mission runs
// for hours unattended, and per-command-shape approval (built for a
// human watching a chat) would otherwise park it on every novel shell
// invocation. Granting "*" for "shell" only ever reaches
// DangerSafe calls: tools.Permissions.Resolve hard-forces
// DangerDestructive to DecisionAsk before any grant is even
// consulted, so this cannot auto-approve a destructive command no
// matter what pattern is granted.
type sessionGranter interface {
	Grant(ctx context.Context, sessionID, tool, pattern string, ttl time.Duration) error
}

// missionGrantTTL matches loop.sessionGrantTTL (12h) — comfortably
// covers driveTimeBound (4h) and any recovery re-drive reusing the
// same hidden session across restarts. A mission genuinely spanning
// longer just resumes asking once the grant expires — a degrade, not
// a break.
const missionGrantTTL = 12 * time.Hour

// driverStore is the narrow slice of *Store the Driver actually calls
// — kept as an interface so tests can fake it without a real Postgres
// pool.
type driverStore interface {
	Create(ctx context.Context, m Mission) (string, error)
	Get(ctx context.Context, id string) (Mission, error)
	ApplyTransition(ctx context.Context, id string, t Transition) error
	AppendEvent(ctx context.Context, id, kind string, payload map[string]any) error
	SetSpec(ctx context.Context, id string, spec Spec) error
	SetSession(ctx context.Context, id, sessionID string) error
	SetProvisioned(ctx context.Context, id, workspace, worktree, branch, baseCommit string) error
	SetLastEvidence(ctx context.Context, id, evidence string) error
	AppendProgress(ctx context.Context, id, note string) error
}

// sandboxRemover is the narrow slice of *sandbox.Manager Driver needs —
// kept as an interface (not an import of the sandbox package) so
// missions has no compile-time dependency on Docker; cmd/brain/main.go
// wires the real *sandbox.Manager.
type sandboxRemover interface {
	Remove(ctx context.Context, missionID string) error
}

// Driver walks the state machine for one mission: calls Runner for the
// phase-appropriate session type, interprets the outcome into a
// StepInput, calls Step, and persists the Transition via
// Store.ApplyTransition — crash-resumable at every boundary because
// nothing advances in memory only.
type Driver struct {
	store     driverStore
	runner    Runner
	workspace *Workspace
	notify    notifier
	sessions  sessionCreator
	perms     sessionGranter
	log       *slog.Logger
	cfg       Config

	// sandboxExec routes a plan unit's verify_cmd through the mission's
	// sandbox container — the same backend nativeRunner uses for
	// worker/reviewer shell calls. sandboxRemove tears the container
	// down at a mission's terminal transition.
	sandboxExec   sandboxExec
	sandboxRemove sandboxRemover

	// resolveAgent resolves a mission's agent_id to its
	// ApprovalAllowlist at provisioning time (see SetAgentResolver) —
	// nil-safe: unset means no allowlist grants, same as before this
	// existed.
	resolveAgent AgentResolver

	// gatekeepers holds each mission's in-progress reviewer session
	// state, keyed by mission id, for the "delta recheck" resume on
	// rework. Process-local by design: lost on restart is acceptable —
	// a cold reviewer just re-checks everything from scratch.
	gatekeepers map[string]*GatekeeperState

	// driving guards against two Drive loops racing the same mission:
	// Advance's own state transitions pass through status='idle'
	// transiently between steps (e.g. stepReviewApprove moving to the
	// next unit) while the owning Drive loop is about to call Advance
	// again — if the work-slot sweep's ClaimWorkSlot claims that same
	// transient idle row, a second Drive spawns and both goroutines
	// read-then-write the mission concurrently, silently clobbering
	// each other's transitions (observed: a mission.done transition
	// overwritten moments later by a stale rework transition). Drive
	// claims the mission's slot here for its own lifetime; a second
	// caller (sweep or a resume racing an already-running Drive)
	// no-ops instead of starting a competing loop.
	drivingMu sync.Mutex
	driving   map[string]bool
}

func NewDriver(store driverStore, runner Runner, workspace *Workspace, notify notifier, sessions sessionCreator, perms sessionGranter, sandboxExec sandboxExec, sandboxRemove sandboxRemover, log *slog.Logger) *Driver {
	return &Driver{
		store: store, runner: runner, workspace: workspace, notify: notify, sessions: sessions, perms: perms, log: log,
		sandboxExec: sandboxExec, sandboxRemove: sandboxRemove,
		cfg:         DefaultConfig,
		gatekeepers: map[string]*GatekeeperState{},
		driving:     map[string]bool{},
	}
}

// SetAgentResolver wires the resolver ensureProvisioned uses to grant
// each of a mission's agent's ApprovalAllowlist tools at provisioning
// time — a setter (not a NewDriver parameter) because cmd/brain/main.go
// builds the Driver before it builds the agents.Store the resolver
// closes over.
func (d *Driver) SetAgentResolver(resolve AgentResolver) {
	d.resolveAgent = resolve
}

// removeSandbox best-effort tears down a mission's sandbox container in
// the background — a slow or unreachable daemon must never block the
// terminal state transition (or the notify that follows it), only log
// the failure. The boot-time orphan sweep is the backstop for a removal
// that fails here.
func (d *Driver) removeSandbox(id string) {
	if d.sandboxRemove == nil {
		return
	}
	go func() {
		// Independent of the transition's own ctx: the mission is already
		// terminal, so this cleanup must not be tied to a request that
		// may be winding down.
		rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.sandboxRemove.Remove(rctx, id); err != nil {
			d.log.Warn("driver: sandbox container removal failed", "mission_id", id, "error", err)
		}
	}()
}

// Create inserts the mission row bare (no session, no workspace yet),
// then calls ensureProvisioned to give it both — the exact same
// provisioning step Advance calls lazily for a mission that reached
// the store some other way (scheduler.go's createFromTemplate). Kicks
// off the first Drive in a background goroutine — callers (the API's
// create handler) get the new id back immediately without waiting on
// the mission to actually run.
func (d *Driver) Create(ctx context.Context, m Mission, repoPath string) (string, error) {
	id, err := d.store.Create(ctx, m)
	if err != nil {
		return "", fmt.Errorf("driver: create: %w", err)
	}
	m.ID = id
	if _, err := d.ensureProvisioned(ctx, m, repoPath); err != nil {
		return "", fmt.Errorf("driver: create: %w", err)
	}
	go func() { //nolint:gosec // G118: deliberate — the mission must outlive the HTTP request that created it, driveTimeBound is Drive's own cap
		if err := d.Drive(context.Background(), id); err != nil {
			d.log.Error("driver: initial drive failed", "mission_id", id, "error", err)
		}
	}()
	return id, nil
}

// ensureProvisioned gives a mission everything Create used to set up
// inline — a hidden session, its standing grants, and a workspace —
// but callable a second time for a mission that reached the store some
// OTHER way (scheduler.go's createFromTemplate inserts a bare row
// directly, bypassing Create entirely: no session, no workspace, no
// grants). Advance calls this at the top of every turn so a
// scheduler-born mission gets provisioned the first time anything
// actually drives it, not at fire time. Idempotent: a mission that
// already has both a session and a workspace/worktree is a no-op,
// and SetSession's own WHERE session_id IS NULL guard makes a second
// concurrent attempt safe even without that check.
//
// Grants happen in BOTH the session-creation and the workspace-
// provisioning halves below — same shape as Create always had, plus
// the new ApprovalAllowlist grants (via resolveAgent) once a session
// exists, whichever half of ensureProvisioned actually created it.
func (d *Driver) ensureProvisioned(ctx context.Context, m Mission, repoPath string) (Mission, error) {
	if m.SessionID == "" && d.sessions != nil {
		sessionID, err := d.sessions.Create(ctx, "")
		if err != nil {
			return m, fmt.Errorf("session: %w", err)
		}
		if err := d.store.SetSession(ctx, m.ID, sessionID); err != nil {
			return m, fmt.Errorf("set session: %w", err)
		}
		m.SessionID = sessionID
		d.grantSessionDefaults(ctx, m)
	}
	if d.workspace != nil && m.Workspace == "" && m.Worktree == "" {
		workspace, worktree, branch, baseCommit, err := d.workspace.Provision(ctx, m.ID, m.Goal, m.Kind, repoPath)
		if err != nil {
			return m, fmt.Errorf("provision: %w", err)
		}
		if err := d.store.SetProvisioned(ctx, m.ID, workspace, worktree, branch, baseCommit); err != nil {
			return m, err
		}
		m.Workspace, m.Worktree, m.Branch, m.BaseCommit = workspace, worktree, branch, baseCommit
		if m.AutoApproveSafe && d.perms != nil && m.SessionID != "" {
			// Register the mission's own directory as the session's
			// sandbox: destructive-classified commands provably confined
			// to it (writing the mission's own artifacts, cleaning its
			// own files) stop parking on a human prompt. Best-effort, same
			// as the grants in grantSessionDefaults.
			root := worktree
			if root == "" {
				root = workspace
			}
			if err := d.perms.Grant(ctx, m.SessionID, tools.SandboxGrantTool, root, missionGrantTTL); err != nil {
				d.log.Warn("driver: sandbox grant failed", "mission_id", m.ID, "error", err)
			}
		}
	}
	return m, nil
}

// grantSessionDefaults pre-authorizes a freshly created hidden session:
// standing "safe shell" approval when the mission opted in, plus every
// tool in the mission's agent's ApprovalAllowlist (resolved at
// provisioning time via resolveAgent, same fire-time-not-create-time
// principle as scheduler.go's createFromTemplate — an agent's allowlist
// edited after the mission started still applies to a not-yet-
// provisioned mission). All best-effort: a failed grant just means the
// mission asks on its first call instead of running unattended —
// degraded autonomy, never a broken mission.
//
// AutoApproveSafe is deliberately shell-scoped only ("shell" + sandbox
// root) — it does not widen to connector tools, which default
// danger=safe unclassified; doing so would silently unlock every
// connector write (send an email, delete a calendar event) for an
// unattended mission with no per-tool review. Autonomy over connector
// tools comes only from the agent's ApprovalAllowlist grants below,
// matched against the connector-namespaced tool name via matchGrant's
// suffix rule (D-036).
func (d *Driver) grantSessionDefaults(ctx context.Context, m Mission) {
	if d.perms == nil {
		return
	}
	if m.AutoApproveSafe {
		if err := d.perms.Grant(ctx, m.SessionID, "shell", "*", missionGrantTTL); err != nil {
			d.log.Warn("driver: auto-approve grant failed", "mission_id", m.ID, "error", err)
		}
	}
	if d.resolveAgent == nil {
		return
	}
	defaults, ok := d.resolveAgent(ctx, m.AgentID)
	if !ok {
		return
	}
	for _, tool := range defaults.ApprovalAllowlist {
		if err := d.perms.Grant(ctx, m.SessionID, tool, "*", missionGrantTTL); err != nil {
			d.log.Warn("driver: approval allowlist grant failed", "mission_id", m.ID, "tool", tool, "error", err)
		}
	}
}

// Advance performs exactly one worker turn, review round, or planning
// turn for mission id — whatever its current phase calls for — then
// persists the resulting transition and returns whether the mission
// can be Advanced again immediately (false on terminal, paused,
// waiting_for_input, or idle).
func (d *Driver) Advance(ctx context.Context, id string) (canContinue bool, err error) {
	m, err := d.store.Get(ctx, id)
	if err != nil {
		return false, fmt.Errorf("driver advance: %w", err)
	}
	if m.Phase.Terminal() || m.Status == StatusPaused || m.Status == StatusWaitingForInput {
		return false, nil
	}
	// A mission that reached the store without going through Create
	// (scheduler.go's createFromTemplate inserts a bare row directly) has
	// no session and no workspace yet — provision it now, on the first
	// turn that actually drives it, rather than leaving the worker with
	// no shell/write_file tools (runner.go's missionTools returns nil for
	// an empty WorkRoot).
	if m.SessionID == "" || (m.Workspace == "" && m.Worktree == "") {
		m, err = d.ensureProvisioned(ctx, m, "")
		if err != nil {
			return false, fmt.Errorf("driver advance: ensure provisioned: %w", err)
		}
	}

	before := m.Status
	turnStart := time.Now()
	in, err := d.runPhase(ctx, m)
	turnMs := time.Since(turnStart).Milliseconds()
	if err != nil {
		d.log.Error("driver: phase run failed", "mission_id", id, "phase", m.Phase, "error", err)
		switch {
		case errors.Is(err, ErrModelFloor):
			// A below-floor fallback model served this turn: it cannot
			// drive tool-using work, so retrying just burns iterations.
			// Pause immediately as infra with the model named.
			in = StepInput{Input: InputReviewInfraFailure, Reason: err.Error()}
		case m.Phase == PhaseReview:
			in = StepInput{Input: InputReviewInfraFailure, Reason: err.Error()}
		default:
			// review_infra_failure is review's error input; other phases
			// report the equivalent-shaped worker_failed so the same
			// backoff/pause machinery applies uniformly regardless of
			// which phase actually failed.
			in = StepInput{Input: InputWorkerFailed, Reason: err.Error()}
		}
	}
	// Turn telemetry: one event per phase run, so a mission's cost in
	// wall-clock and outcomes is readable from its event log alone
	// (token/model cost is in cost_ledger, keyed by mission_id).
	payload := map[string]any{
		"phase": string(m.Phase), "duration_ms": turnMs,
		"ok": err == nil, "input": string(in.Input), "reason": in.Reason,
	}
	if m.Phase == PhaseExecute && workerRoute(m) != m.Route {
		payload["escalated_route"] = workerRoute(m)
	}
	if evErr := d.store.AppendEvent(ctx, id, "mission.turn", payload); evErr != nil {
		d.log.Warn("driver: record turn failed", "mission_id", id, "error", evErr)
	}

	// runPhase may have written spec changes of its own (e.g. review's
	// verifyCurrentUnit -> markUnitPassed flipping a unit to passed) —
	// re-fetch so the completion check below sees that write rather
	// than the pre-round snapshot loaded at the top of this call.
	m, err = d.store.Get(ctx, id)
	if err != nil {
		return false, fmt.Errorf("driver advance: reload after phase: %w", err)
	}
	state := toStepState(m)
	t := Step(state, in, d.cfg)
	if err := d.store.ApplyTransition(ctx, id, t); err != nil {
		return false, fmt.Errorf("driver advance: apply transition: %w", err)
	}
	if t.Next.Phase.Terminal() {
		delete(d.gatekeepers, id)
		d.removeSandbox(id)
	}
	if d.notify != nil {
		if err := d.notify.OnTransition(ctx, id, before, t.Next.Status); err != nil {
			d.log.Warn("driver: notify failed", "mission_id", id, "error", err)
		}
	}
	return t.Next.Status == StatusIdle || t.Next.Status == StatusWorking, nil
}

// Drive loops Advance until it returns false (terminal, paused,
// waiting_for_input, or idle), bounded by driveTimeBound. Crash-
// resumable at every boundary: each Advance call persists before the
// next begins, so a process death mid-Drive leaves the mission in a
// valid, resumable state rather than a torn one.
func (d *Driver) Drive(ctx context.Context, id string) error {
	if !d.claimDriving(id) {
		return nil // another Drive loop already owns this mission
	}
	defer d.releaseDriving(id)

	dctx, cancel := context.WithTimeout(ctx, driveTimeBound)
	defer cancel()
	for {
		canContinue, err := d.Advance(dctx, id)
		if err != nil {
			return err
		}
		if !canContinue {
			return nil
		}
		if dctx.Err() != nil {
			return nil
		}
	}
}

func (d *Driver) claimDriving(id string) bool {
	d.drivingMu.Lock()
	defer d.drivingMu.Unlock()
	if d.driving[id] {
		return false
	}
	d.driving[id] = true
	return true
}

func (d *Driver) releaseDriving(id string) {
	d.drivingMu.Lock()
	defer d.drivingMu.Unlock()
	delete(d.driving, id)
}

// Signal applies an externally-triggered input (resume or cancel) to a
// mission outside the normal Advance loop — the API layer's
// resume/cancel endpoints call this. On resume, it re-kicks Drive in a
// background goroutine since the mission may have work to do again.
func (d *Driver) Signal(ctx context.Context, id string, input Input) error {
	if input != InputResume && input != InputCancel {
		return fmt.Errorf("driver: signal: %q is not an externally-triggerable input", input)
	}
	m, err := d.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("driver: signal: %w", err)
	}
	if m.Phase.Terminal() {
		return ErrTerminal
	}
	before := m.Status
	t := Step(toStepState(m), StepInput{Input: input}, d.cfg)
	if err := d.store.ApplyTransition(ctx, id, t); err != nil {
		return fmt.Errorf("driver: signal: apply transition: %w", err)
	}
	if t.Next.Phase.Terminal() {
		delete(d.gatekeepers, id)
		d.removeSandbox(id)
	}
	if d.notify != nil {
		if err := d.notify.OnTransition(ctx, id, before, t.Next.Status); err != nil {
			d.log.Warn("driver: notify failed", "mission_id", id, "error", err)
		}
	}
	if input == InputResume {
		go func() { //nolint:gosec // G118: deliberate — the mission must outlive the HTTP request that resumed it, driveTimeBound is Drive's own cap
			if err := d.Drive(context.Background(), id); err != nil {
				d.log.Error("driver: post-resume drive failed", "mission_id", id, "error", err)
			}
		}()
	}
	return nil
}

// toStepState projects a Mission onto the state machine's input shape.
func toStepState(m Mission) StepState {
	spent := 0.0 // Phase 1 has no spend-tracking wired into StepState yet beyond BudgetUSD's presence; see M3/M4 for ledger integration.
	return StepState{
		Phase: m.Phase, Status: m.Status, PauseReason: m.PauseReason,
		Iteration: m.Iteration, MaxIterations: m.MaxIterations,
		ConsecutiveFailures: m.ConsecutiveFailures, LastGapFingerprint: m.LastGapFingerprint,
		StallCount: m.StallCount, SpentUSD: spent, BudgetUSD: m.BudgetUSD,
		LastUnit: isLastUnit(m.Spec),
	}
}

// isLastUnit reports whether every unit in the plan has passed — the
// mission is only done once nothing remains unverified, not merely
// when the first still-unverified unit happens to sit at the last
// index (that check alone is one unit short: it's true the moment the
// second-to-last unit passes, before the actual last unit ever runs).
func isLastUnit(spec Spec) bool {
	for _, u := range spec.Units {
		if !u.Passes {
			return false
		}
	}
	return true
}

// runPhase runs the phase-appropriate session and returns the StepInput
// its outcome maps to. It does not itself decide pass/fail semantics
// beyond what each phase's contract already defines (worker sentinel,
// review verdict, planner output).
func (d *Driver) runPhase(ctx context.Context, m Mission) (StepInput, error) {
	switch m.Phase {
	case PhaseResearch:
		return d.runResearch(ctx, m)
	case PhasePlan:
		return d.runPlan(ctx, m)
	case PhaseExecute:
		return d.runExecute(ctx, m)
	case PhaseReview:
		return d.runReview(ctx, m)
	default:
		return StepInput{}, fmt.Errorf("driver: mission %s in unhandled phase %q", m.ID, m.Phase)
	}
}

// runResearch is a placeholder single-turn phase in Phase 1: it
// completes immediately, carrying no findings forward beyond what the
// plan phase's own packet re-derives from the goal. A real research
// loop (multi-turn, tool-using) is out of scope for this milestone.
func (d *Driver) runResearch(ctx context.Context, m Mission) (StepInput, error) {
	return StepInput{Input: InputPhaseComplete}, nil
}

func (d *Driver) runPlan(ctx context.Context, m Mission) (StepInput, error) {
	spec, err := d.runner.PlanSession(ctx, m, "")
	if err != nil {
		return StepInput{}, err
	}
	if err := d.store.AppendEvent(ctx, m.ID, "mission.plan_created", map[string]any{"units": len(spec.Units)}); err != nil {
		return StepInput{}, fmt.Errorf("driver: record plan: %w", err)
	}
	if err := d.store.SetSpec(ctx, m.ID, spec); err != nil {
		return StepInput{}, err
	}
	return StepInput{Input: InputPhaseComplete}, nil
}

func (d *Driver) runExecute(ctx context.Context, m Mission) (StepInput, error) {
	packet, err := d.packet(ctx, m)
	if err != nil {
		return StepInput{}, err
	}
	verdict, text, err := d.runner.RunWorker(ctx, m, packet)
	if err != nil {
		return StepInput{}, err
	}
	if err := d.recordProgress(ctx, m.ID, text); err != nil {
		d.log.Warn("driver: record progress failed", "mission_id", m.ID, "error", err)
	}

	switch verdict.Outcome {
	case "done":
		if m.Worktree != "" {
			if err := d.workspace.CommitUnit(ctx, m.Worktree, "mission "+m.ID+" iteration "+fmt.Sprint(m.Iteration)); err != nil {
				d.log.Warn("driver: commit unit failed", "mission_id", m.ID, "error", err)
			}
		}
		if err := d.store.SetLastEvidence(ctx, m.ID, verdict.Evidence); err != nil {
			d.log.Warn("driver: record evidence failed", "mission_id", m.ID, "error", err)
		}
		if in, ok := d.trySkipReview(ctx, m); ok {
			return in, nil
		}
		return StepInput{Input: InputPhaseComplete}, nil
	case "blocked":
		return StepInput{Input: InputWorkerBlocked, Message: verdict.Question}, nil
	default: // "retry" or anything unrecognized
		if m.Worktree != "" {
			if err := d.workspace.Rollback(ctx, m.Worktree, m.Kind); err != nil {
				d.log.Warn("driver: rollback failed", "mission_id", m.ID, "error", err)
			}
		}
		return StepInput{Input: InputWorkerRetry, Reason: truncate(verdict.Analysis, 500)}, nil
	}
}

// trySkipReview short-circuits the review round for a non-coding
// mission's unit when the harness's own deterministic evidence
// (declared artifacts present and non-empty, verify_cmd passing)
// already establishes the unit holds up — an LLM review round on top
// of passing harness checks adds tokens, latency, and (with the
// reviewer being the least reliable link) failure modes, not safety.
// Coding missions always review: a diff can be wrong in ways
// existence checks can't see. Units with no declared artifacts always
// review too — there is no harness evidence to stand on.
func (d *Driver) trySkipReview(ctx context.Context, m Mission) (StepInput, bool) {
	if m.Kind == "coding" {
		return StepInput{}, false
	}
	unit, idx := currentUnit(m.Spec)
	if unit == nil || len(unit.Artifacts) == 0 {
		return StepInput{}, false
	}
	if err := d.verifyCurrentUnit(ctx, m); err != nil {
		var vf *verifyFailure
		if errors.As(err, &vf) {
			note := fmt.Sprintf("Verification failed for unit %d before review: %s", vf.unit+1, vf.excerpt)
			if perr := d.recordProgress(ctx, m.ID, note); perr != nil {
				d.log.Warn("driver: record verify-failure note failed", "mission_id", m.ID, "error", perr)
			}
			fp := fmt.Sprintf("verify_failed:unit_%d", vf.unit)
			return StepInput{Input: InputWorkerRetry, Reason: truncate(note, 500), GapFingerprint: fp}, true
		}
		d.log.Warn("driver: pre-review verify errored; falling back to review", "mission_id", m.ID, "error", err)
		return StepInput{}, false
	}
	if err := d.store.AppendEvent(ctx, m.ID, "mission.review_skipped", map[string]any{
		"unit": idx, "reason": "artifacts and verify_cmd passed harness checks",
	}); err != nil {
		d.log.Warn("driver: record review skip failed", "mission_id", m.ID, "error", err)
	}
	return StepInput{Input: InputReviewApprove}, true
}

// currentUnit returns the plan's first unverified unit and its index,
// or nil when every unit already passed.
func currentUnit(spec Spec) (*PlanUnit, int) {
	for i := range spec.Units {
		if !spec.Units[i].Passes {
			return &spec.Units[i], i
		}
	}
	return nil, -1
}

func (d *Driver) runReview(ctx context.Context, m Mission) (StepInput, error) {
	var diff string
	if m.Worktree != "" {
		var err error
		diff, err = BaselineDiff(ctx, m.Worktree, m.BaseCommit)
		if err != nil {
			return StepInput{}, err
		}
	}
	workRoot := m.WorkRoot()
	packet := ReviewPacket{
		Goal: m.Goal, Plan: m.Spec, Diff: diff, Evidence: m.LastEvidence,
		Listing: ListWorkspace(workRoot),
	}
	if unit, _ := currentUnit(m.Spec); unit != nil {
		packet.UnitTitle = unit.Title
		packet.Artifacts = ReadArtifacts(workRoot, unit.Artifacts)
	}
	gk := d.gatekeepers[m.ID]
	verdict, nextGK, err := d.runner.RunReview(ctx, m, packet, gk)
	if err != nil {
		return StepInput{}, err
	}
	d.gatekeepers[m.ID] = nextGK
	// The reviewer's own worktree side effects (it may run tests) are
	// rolled back unconditionally after every review round.
	if m.Worktree != "" {
		if err := d.workspace.Rollback(ctx, m.Worktree, m.Kind); err != nil {
			d.log.Warn("driver: post-review rollback failed", "mission_id", m.ID, "error", err)
		}
	}

	// Every verdict is recorded, approvals included — a review that
	// leaves no event is indistinguishable from a review that never
	// ran (the coding canary asserts on exactly this).
	decision := "rework"
	if verdict.Approved {
		decision = "approved"
	}
	if err := d.store.AppendEvent(ctx, m.ID, "mission.review_verdict", map[string]any{
		"decision": decision, "findings": verdict.Findings,
	}); err != nil {
		d.log.Warn("driver: record review verdict failed", "mission_id", m.ID, "error", err)
	}

	if verdict.Approved {
		var vf *verifyFailure
		if err := d.verifyCurrentUnit(ctx, m); err != nil {
			if errors.As(err, &vf) {
				// The reviewer approved, but the harness's own verify_cmd
				// disagrees — real evidence the approval didn't hold up
				// (e.g. a claimed file was never actually written), not an
				// infra fault. Route through the SAME rework path a
				// reviewer's own rejection takes: back to execute, costs an
				// iteration, and (via GapFingerprint) stall-pauses if the
				// exact same verify keeps failing round after round instead
				// of looping forever. Tell the worker exactly what
				// verification found, so the next turn doesn't just repeat
				// the same false claim.
				note := fmt.Sprintf("Verification failed for unit %d: the harness ran verify_cmd and it did NOT pass. Output:\n%s", vf.unit+1, vf.excerpt)
				if err := d.recordProgress(ctx, m.ID, note); err != nil {
					d.log.Warn("driver: record verify-failure note failed", "mission_id", m.ID, "error", err)
				}
				fp := fmt.Sprintf("verify_failed:unit_%d", vf.unit)
				return StepInput{Input: InputReviewRework, GapFingerprint: fp}, nil
			}
			return StepInput{Input: InputReviewInfraFailure}, err
		}
		return StepInput{Input: InputReviewApprove}, nil
	}
	fp := GapFingerprint(verdict.Findings)
	if fp != "" && fp == m.LastGapFingerprint {
		// Same gap rejected twice: the resumed reviewer session is
		// anchored to its earlier judgment. Drop it so the next round
		// (if the stall brake doesn't pause first) re-reads everything
		// with fresh eyes instead of re-asserting its previous verdict.
		delete(d.gatekeepers, m.ID)
	}
	return StepInput{Input: InputReviewRework, GapFingerprint: fp, Reason: truncate(reviewReason(verdict.Findings), 500)}, nil
}

// reviewReason flattens findings into one line for event payloads.
func reviewReason(findings []Finding) string {
	titles := make([]string, 0, len(findings))
	for _, f := range findings {
		titles = append(titles, f.Title)
	}
	return strings.Join(titles, "; ")
}

// verifyFailure is a unit's verify_cmd genuinely running and reporting
// failure — real evidence the reviewer's approval didn't hold up, not
// an infrastructure fault. Kept distinct from a plain error (RunVerify
// itself erroring: timeout, exec failure) so the caller can route each
// to a different StepInput instead of conflating both as infra.
type verifyFailure struct {
	unit    int
	excerpt string
}

func (e *verifyFailure) Error() string {
	return fmt.Sprintf("driver: unit %d verify_cmd failed", e.unit)
}

// verifyCurrentUnit runs the harness's own checks for the plan's
// current (first unverified) unit and marks it passed — only this
// harness-run evidence may flip a unit's Passes flag, never model
// output. Declared artifacts are checked first (exists, non-empty):
// a tautological verify_cmd cannot pass a unit whose artifact was
// never written.
func (d *Driver) verifyCurrentUnit(ctx context.Context, m Mission) error {
	for i, u := range m.Spec.Units {
		if u.Passes {
			continue
		}
		workRoot := m.WorkRoot()
		if problems := CheckArtifacts(workRoot, u.Artifacts); len(problems) > 0 {
			excerpt := "declared artifacts failed the harness check:\n" + strings.Join(problems, "\n")
			// Show what DOES exist: the dominant failure here is a
			// worker writing a real file under a slightly different
			// name and never spotting the mismatch from "not found"
			// alone.
			if listing := ListWorkspace(workRoot); listing != "" {
				excerpt += "\nfiles currently in the workspace:\n" + listing
			} else {
				excerpt += "\nthe workspace is currently empty"
			}
			if err := d.store.AppendEvent(ctx, m.ID, "mission.unit_verified", map[string]any{
				"unit": i, "passed": false, "check": "artifacts", "problems": problems,
			}); err != nil {
				d.log.Warn("driver: record verify failed", "mission_id", m.ID, "error", err)
			}
			return &verifyFailure{unit: i, excerpt: excerpt}
		}
		if u.VerifyCmd == "" {
			return d.markUnitPassed(ctx, m, i)
		}
		res, err := d.runVerify(ctx, m.ID, workRoot, u.VerifyCmd)
		if err != nil {
			return fmt.Errorf("driver: verify unit %d: %w", i, err)
		}
		if err := d.store.AppendEvent(ctx, m.ID, "mission.unit_verified", map[string]any{
			"unit": i, "passed": res.Passed, "check": "verify_cmd", "exit_code": res.ExitCode, "output_sha256": res.OutputSHA256,
		}); err != nil {
			d.log.Warn("driver: record verify failed", "mission_id", m.ID, "error", err)
		}
		if !res.Passed {
			return &verifyFailure{unit: i, excerpt: res.Excerpt}
		}
		return d.markUnitPassed(ctx, m, i)
	}
	return nil
}

// markUnitPassed persists unit as passed. It copies Units before
// mutating — m.Spec.Units is a slice header, so writing through it
// in place would silently mutate the caller's own Mission value too
// (same backing array), corrupting whatever that caller does with it
// afterward in the same round (e.g. Advance's toStepState(m) call).
func (d *Driver) markUnitPassed(ctx context.Context, m Mission, unit int) error {
	units := make([]PlanUnit, len(m.Spec.Units))
	copy(units, m.Spec.Units)
	units[unit].Passes = true
	return d.store.SetSpec(ctx, m.ID, Spec{Units: units})
}

// packet builds the WorkPacket for the current phase/iteration
// directly from mission fields — no unused indirection.
func (d *Driver) packet(ctx context.Context, m Mission) (WorkPacket, error) {
	gitLog := ""
	if m.Worktree != "" {
		gitLog, _ = gitLogSince(ctx, m.Worktree, m.BaseCommit)
	}
	return WorkPacket{
		Goal: m.Goal, Kind: m.Kind, Spec: m.Spec, Progress: m.Progress,
		GitLog: gitLog, Iteration: m.Iteration, PromptOverlay: m.PromptOverlay,
		ExecEnvironmentNote: execEnvironmentNote(),
	}, nil
}

// runVerify executes verify_cmd via the mission's sandbox container —
// the verify-side counterpart of nativeRunner routing shell/write_file
// through the same backend.
func (d *Driver) runVerify(ctx context.Context, missionID, workRoot, verifyCmd string) (VerifyResult, error) {
	backend := func(ctx context.Context, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		return d.sandboxExec(ctx, missionID, workdir, command, timeout, out)
	}
	return RunVerifyWithBackend(ctx, backend, workRoot, verifyCmd)
}

func (d *Driver) recordProgress(ctx context.Context, id, text string) error {
	if text == "" {
		return nil
	}
	return d.store.AppendProgress(ctx, id, NeutralizeSlot(truncate(text, 2000)))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// gitLogSince returns a capped, one-line-per-commit log of everything
// committed since baseCommit — a fresh worker's window into what prior
// iterations actually did, bounded so it never balloons the packet.
func gitLogSince(ctx context.Context, worktree, baseCommit string) (string, error) {
	if baseCommit == "" || baseCommit == unavailableCommit {
		return "", nil
	}
	cctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	out, err := runGit(cctx, worktree, "log", "--oneline", baseCommit+"..HEAD")
	if err != nil {
		return "", err
	}
	if len(out) > gitLogCap {
		out = out[:gitLogCap] + "…"
	}
	return out, nil
}
