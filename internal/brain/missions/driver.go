package missions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/fxrates"
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
	OnTransition(ctx context.Context, m Mission, before, after Status, reason string) error
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
	Events(ctx context.Context, id string) ([]Event, error)
	SetSpec(ctx context.Context, id string, spec Spec) error
	SetSession(ctx context.Context, id, sessionID string) error
	SetProvisioned(ctx context.Context, id, workspace, worktree, branch, baseCommit string) error
	SetLastEvidence(ctx context.Context, id, evidence string) error
	SetFinalOutput(ctx context.Context, id, text string) error
	SetExploreNotes(ctx context.Context, id, notes string) error
	SetNameIfEmpty(ctx context.Context, id, name string) error
	AppendProgress(ctx context.Context, id, note string) error
	Spend(ctx context.Context, missionID string) (MissionSpend, error)
}

// fxRateSource is the narrow slice of *fxrates.Store Driver needs for
// the budget brake's cross-currency conversion — an interface (not a
// concrete *fxrates.Store field) so driver_test.go's scenarios can fake
// a rate table without a real Postgres pool, same reasoning as
// driverStore.
type fxRateSource interface {
	LatestUSDRates(ctx context.Context) (map[string]fxrates.Rate, error)
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

	// skillsIndex renders the mission agent's skill index for worker
	// prompts (SetSkillsIndex) — nil means workers get no index.
	skillsIndex func(ctx context.Context, agentID string) string

	// provision holds the mission-provisioning slice split out into its
	// own type (D-074): ensureProvisioned, grantSessionDefaults,
	// followUpBaseRef and their resolver deps. Driver's Set* setters
	// delegate straight into it.
	provision provisioner

	// budget holds the budget/FX projection slice split out into its own
	// type (D-074): convertOtherCurrencySpend and the budget-projection
	// half of toStepState. SetFXRates delegates straight into it.
	// nil-safe: an unset fxRates means the brake never converts,
	// degrading to the original mixed-currency-always-pauses behavior.
	budget budgetProjector

	// verify holds the harness-evidence slice split out into its own
	// type (D-074): verifyCurrentUnit, markUnitPassed, checkRegressions,
	// regressed — the only code allowed to flip a PlanUnit's Passes
	// flag.
	verify verifier

	// capacity backs the D-056 admission gate (see SetCapacityGate /
	// Advance) — nil-safe: unset means every idle->working transition is
	// admitted, same as before this existed.
	capacity capacityChecker

	// retryDelayFn paces a worker_failed retry (see retryDelay) —
	// defaults to retryDelay in NewDriver, overridden to a zero-delay
	// stub by tests that drive multiple worker_failed rounds and can't
	// afford to actually sleep.
	retryDelayFn func(consecutiveFailures int) time.Duration

	// gitCommitStyle resolves the settings-configured default commit
	// style (see SetGitCommitStyle) — consulted by runExecute only when
	// the mission's own CommitStyle is empty (mission override > settings
	// > CommitStyleConventional). nil-safe: unset falls straight through
	// to CommitMessage's own conventional-style default.
	gitCommitStyle func(ctx context.Context) string

	// completer runs a mission's recorded on_complete choice
	// (push/push_pr) once it reaches phase=done (see SetCompleter,
	// fireOnComplete) — nil-safe: unset (no connectors/secrets wired)
	// just means the auto-fire hook never runs, same as a mission whose
	// on_complete is "".
	completer *Completer

	// notifyPushFailed fires a best-effort notification when the
	// auto-fire hook's push/PR attempt fails (see SetPushFailedNotifier)
	// — nil-safe: unset just skips the notification, the mission.push_failed
	// event (Completer.PushBranch's own append) is still recorded either way.
	notifyPushFailed func(ctx context.Context, missionID, message string)

	// memory wires the memoryd extraction hook (see SetMemoryExtract) —
	// nil-safe: unset skips extraction entirely, same as chat's own
	// MemoryExtract field.
	memory MemoryExtract

	// nameMission wires the display-name generator used to backfill
	// missions that reached a terminal phase without a name (see
	// SetNameMission / backfillMissionName) — nil-safe: unset leaves
	// unnamed missions unnamed, same as before this existed.
	nameMission func(context.Context, string) string

	// deliverDestinations wires the destinations delivery hook fired on
	// a mission's terminal done transition (see SetDestinationDeliver /
	// deliverToDestinations) — nil-safe: unset skips delivery entirely.
	// A func type, not an interface importing the destinations package,
	// since destinations.Deliverer needs missions.Mission/Event —
	// importing missions from there and missions from destinations back
	// would cycle; cmd/brain/main.go's wiring closure bridges the two,
	// same as SetMemoryExtract does for memoryd.
	deliverDestinations DestinationDeliver

	// onTerminal wires the workflows engine's reaction to a mission
	// reaching a terminal phase (see SetOnTerminal / fireOnTerminal) —
	// nil-safe: unset skips it entirely, same as deliverDestinations. A
	// func type for the same import-cycle reason: workflows needs
	// missions.Mission, so missions can't import workflows back.
	onTerminal OnTerminal

	// validateDeps backs ValidateCreate's store-backed checks (D-071) —
	// a *ValidateDeps, not a value, so "unset" is distinguishable from
	// "set to the zero value": nil skips ValidateCreate's call entirely
	// (existing callers/tests that predate D-071 keep working
	// unvalidated), while a non-nil ValidateDeps{} runs every
	// struct-shape check but skips only the dep-backed ones (nil fields
	// within it). See SetValidateDeps.
	validateDeps *ValidateDeps

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
		provision:    provisioner{store: store, workspace: workspace, sessions: sessions, perms: perms, log: log},
		budget:       budgetProjector{log: log},
		verify:       verifier{store: store, sandboxExec: sandboxExec, log: log},
		cfg:          DefaultConfig,
		gatekeepers:  map[string]*GatekeeperState{},
		driving:      map[string]bool{},
		retryDelayFn: retryDelay,
	}
}

// workerFailedRetryDelays paces retries after a worker_failed
// transition (D-064): back-to-back Advance calls with no delay hammer
// a failing model at ~1/sec until the backoff pause finally kicks in —
// this ladder slows that down without changing when the pause itself
// fires (stepWorkerFailed's cfg.BackoffFailures ceiling is unchanged).
// This is the in-band ladder: it paces retries WITHIN a live Drive loop,
// seconds apart, while the mission is still working. Its counterpart is
// sweep.go's autoResumeBackoffDelays/autoResumeInfraDelays, which pace
// resuming a mission that already paused — minutes apart, run from the
// periodic sweep rather than inline in Advance. The split exists because
// the two problems are different: this ladder slows a mission that is
// still actively (if unsuccessfully) making attempts; the sweep's
// ladders self-heal a mission that has already stopped and gone quiet.
var workerFailedRetryDelays = [...]time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}

// retryDelay maps a post-transition ConsecutiveFailures count to a
// delay from workerFailedRetryDelays, saturating at the last rung.
// consecutiveFailures <= 0 returns 0 (shouldn't happen post-failure,
// but never a negative sleep).
func retryDelay(consecutiveFailures int) time.Duration {
	if consecutiveFailures <= 0 {
		return 0
	}
	idx := consecutiveFailures - 1
	if idx >= len(workerFailedRetryDelays) {
		idx = len(workerFailedRetryDelays) - 1
	}
	return workerFailedRetryDelays[idx]
}

// SetAgentResolver wires the resolver ensureProvisioned uses to grant
// each of a mission's agent's ApprovalAllowlist tools at provisioning
// time — a setter (not a NewDriver parameter) because cmd/brain/main.go
// builds the Driver before it builds the agents.Store the resolver
// closes over.
func (d *Driver) SetAgentResolver(resolve AgentResolver) {
	d.provision.resolveAgent = resolve
}

// SetSkillsIndex wires the resolver packet() uses to render the
// mission agent's skill index into worker prompts — a setter for the
// same construction-order reason SetAgentResolver is. nil (unwired)
// means workers see no skill index, matching every other nil-gated
// optional dependency.
func (d *Driver) SetSkillsIndex(fn func(ctx context.Context, agentID string) string) {
	d.skillsIndex = fn
}

// SetFXRates wires the stored USD-base rate table the budget brake
// converts cross-currency spend through (toStepState) — a setter (not
// a NewDriver parameter) for the same reason SetAgentResolver is: it
// keeps the constructor's parameter list from growing for every
// optional cross-cutting dependency.
func (d *Driver) SetFXRates(store fxRateSource) {
	d.budget.fxRates = store
}

// SetCapacityGate wires the D-056 admission gate Advance consults before
// flipping a mission idle->working — a setter (not a NewDriver
// parameter) for the same reason SetFXRates is.
func (d *Driver) SetCapacityGate(gate capacityChecker) {
	d.capacity = gate
}

// CloneTokenResolver resolves a mission's connector_id to the PAT that
// authenticates ensureProvisioned's clone — api/missions.go validates
// connector_id names a github-kind connector at create time; this
// resolves the connector's credential_ref fresh at provisioning time,
// the same "never persist the token" discipline push.go's credential_ref
// resolution already follows.
type CloneTokenResolver func(ctx context.Context, connectorID string) (string, error)

// SetCloneTokenResolver wires the resolver ensureProvisioned uses to
// authenticate a repo_url mission's clone — a setter (not a NewDriver
// parameter) for the same reason SetAgentResolver is.
func (d *Driver) SetCloneTokenResolver(resolve CloneTokenResolver) {
	d.provision.resolveCloneToken = resolve
}

// ResolvedIdentity is what CloneIdentityResolver resolves for a
// mission's connector_id: the commit identity ensureProvisioned's clone
// is authored as, plus (when the connector has SSH commit signing
// enabled) the private signing key. A struct, not more return values,
// since SigningKey is the second field added after (name, email, login)
// — the shape CloneIdentityResolver started with.
type ResolvedIdentity struct {
	Name       string
	Email      string
	Login      string
	SigningKey string
}

// CloneIdentityResolver resolves a mission's connector_id to the
// commit identity ensureProvisioned's clone is authored as — the
// identity counterpart of CloneTokenResolver, resolved fresh at
// provisioning time (never persisted on the mission row). Login backs
// the {login} branch-pattern placeholder; SigningKey (when non-empty)
// is the connector's SSH signing private key.
type CloneIdentityResolver func(ctx context.Context, connectorID string) (ResolvedIdentity, error)

// SetCloneIdentityResolver wires the resolver ensureProvisioned uses to
// set a repo_url mission's clone local git identity — a setter (not a
// NewDriver parameter) for the same reason SetAgentResolver is.
func (d *Driver) SetCloneIdentityResolver(resolve CloneIdentityResolver) {
	d.provision.resolveCloneIdentity = resolve
}

// SetGitBranchPattern wires the live settings getter ensureProvisioned
// falls back to when a mission's own BranchPattern is empty — a setter
// (not a NewDriver parameter) for the same reason SetAgentResolver is.
func (d *Driver) SetGitBranchPattern(get func(ctx context.Context) string) {
	d.provision.gitBranchPattern = get
}

// SetGitCommitStyle wires the live settings getter runExecute falls
// back to when a mission's own CommitStyle is empty — a setter for the
// same reason SetGitBranchPattern is.
func (d *Driver) SetGitCommitStyle(get func(ctx context.Context) string) {
	d.gitCommitStyle = get
}

// SetCompleter wires the Completer the driver's auto-fire-on-done hook
// (fireOnComplete) runs a mission's on_complete choice through — a
// setter (not a NewDriver parameter) for the same reason SetAgentResolver
// is: cmd/brain/main.go builds it after the Driver, once connectors/
// secrets are available.
func (d *Driver) SetCompleter(c *Completer) {
	d.completer = c
}

// SetPushFailedNotifier wires the best-effort notification fired when
// the auto-fire hook's push/PR attempt fails — a setter for the same
// reason SetCompleter is.
func (d *Driver) SetPushFailedNotifier(notify func(ctx context.Context, missionID, message string)) {
	d.notifyPushFailed = notify
}

// SetMemoryExtract wires the memoryd extraction hook fired once a
// mission reaches a terminal phase (see extractMissionMemory) — a
// setter (not a NewDriver parameter) for the same reason SetCompleter
// is. Optional — nil (today's default) leaves missions extracting
// nothing into memory.
func (d *Driver) SetMemoryExtract(fn MemoryExtract) {
	d.memory = fn
}

// SetNameMission installs the display-name generator used to backfill
// missions that reached a terminal phase without a name (the create-time
// fire-and-forget call failed). A setter for the same reason
// SetMemoryExtract is. Optional — nil leaves unnamed missions unnamed.
func (d *Driver) SetNameMission(fn func(context.Context, string) string) {
	d.nameMission = fn
}

// DestinationDeliver delivers a terminal mission's generated output to
// its attached destination_ids — destinations.Deliverer.Deliver
// satisfies this signature. Fire-and-forget by contract, same as
// MemoryExtract: it must never return an error, block, or affect
// mission state.
type DestinationDeliver func(ctx context.Context, m Mission, destinationIDs []string)

// SetDestinationDeliver wires the destinations delivery hook fired on
// a mission's terminal done transition (see deliverToDestinations) — a
// setter for the same reason SetMemoryExtract is. Optional — nil skips
// delivery entirely, same as a mission with no destination_ids.
func (d *Driver) SetDestinationDeliver(fn DestinationDeliver) {
	d.deliverDestinations = fn
}

// deliverToDestinations fires the destinations delivery hook exactly
// on a transition into phase=done (never phase=failed — v1 doesn't
// deliver on failure, the notifier already covers failure alerts) and
// only when the mission actually has destination_ids attached. m is
// the terminal-transition's own reload (onTerminal's single Get,
// after backfillMissionName) — this no longer reloads itself, so
// delivery always sees the backfilled name. Detached from ctx like
// fireOnComplete: the mission is already terminal, so delivery must
// outlive a request that may be winding down.
func (d *Driver) deliverToDestinations(m Mission, terminal Phase) {
	if d.deliverDestinations == nil || terminal != PhaseDone || len(m.DestinationIDs) == 0 {
		return
	}
	rctx := context.Background()
	go d.deliverDestinations(rctx, m, m.DestinationIDs) //nolint:gosec // G118: deliberate — the mission is already terminal, delivery must outlive whatever request/ctx observed that transition
}

// OnTerminal reacts to a mission reaching a terminal phase —
// workflows.Engine.OnMissionTerminal satisfies this. Fire-and-forget by
// contract, same as DestinationDeliver: it must never return an error,
// block, or affect mission state (the engine only reads terminal
// missions and creates new ones, never mutates the terminal mission).
type OnTerminal func(ctx context.Context, m Mission)

// SetOnTerminal wires the workflows engine's reaction hook (see
// fireOnTerminal) — a setter for the same reason SetDestinationDeliver
// is. Optional — nil skips it entirely, same as a mission with no
// workflow_run_id.
func (d *Driver) SetOnTerminal(fn OnTerminal) {
	d.onTerminal = fn
}

// SetValidateDeps wires the store-backed checks ValidateCreate needs
// (D-071) — a setter for the same reason SetAgentResolver is: cmd/brain/
// main.go builds the gateway route resolver and destination store after
// the Driver. Unset (the default, and every driver_test.go fixture
// predating D-071) skips Create's ValidateCreate call entirely, so
// existing tests that build a bare Mission{} keep passing.
func (d *Driver) SetValidateDeps(deps ValidateDeps) {
	d.validateDeps = &deps
}

// fireOnTerminal fires the workflows engine hook for any mission
// reaching a terminal phase (done or failed) that names a
// workflow_run_id — an ordinary mission (workflow_run_id empty) never
// reaches the engine at all. m is onTerminal's single reload, same as
// deliverToDestinations. Detached from ctx: the mission is already
// terminal, so this must outlive a request that may be winding down.
func (d *Driver) fireOnTerminal(m Mission) {
	if d.onTerminal == nil || m.WorkflowRunID == "" {
		return
	}
	rctx := context.Background()
	go d.onTerminal(rctx, m) //nolint:gosec // G118: deliberate — the mission is already terminal, the hook must outlive whatever request/ctx observed that transition
}

// fireOnComplete runs a mission's recorded on_complete choice
// (push/push_pr) exactly once, right after its own ApplyTransition into
// phase=done succeeds — the SAME Completer code the manual push/pr API
// endpoints use, so an auto-fired push/PR can never diverge from what a
// human clicking the button gets. Failure never un-dones the mission:
// Completer.RunOnComplete already appended mission.push_failed (via
// PushBranch/OpenPR's own error path); this only additionally fires a
// best-effort notification so the operator hears about it, mirroring
// notify.go's own best-effort webhook fan-out. No retry — one attempt,
// the manual push/pr endpoints remain available.
func (d *Driver) fireOnComplete(ctx context.Context, id string, m Mission) {
	if d.completer == nil || m.OnComplete == "" {
		return
	}
	// Detached from ctx: the mission is already terminal, so this must
	// not be cancelled by a request winding down (same reasoning as
	// removeSandbox).
	rctx := context.Background()
	if err := d.completer.RunOnComplete(rctx, m); err != nil {
		d.log.Error("driver: on_complete auto-fire failed", "mission_id", id, "on_complete", m.OnComplete, "error", err)
		if d.notifyPushFailed != nil {
			d.notifyPushFailed(rctx, id, fmt.Sprintf("mission %s: automatic %s failed: %s", id, m.OnComplete, err.Error()))
		}
	}
}

// D-073: runTerminalHooks is the single place Advance and Signal fire
// every terminal-transition hook, replacing what used to be a verbatim block
// duplicated in both (and had already drifted: Signal's copy omitted
// fireOnComplete, benign only because Signal never produces a done
// transition today — cancel always fails a mission, never completes
// it).
//
// Fixed order:
//  1. gatekeeper state drop (in-process only, no I/O)
//  2. sandbox container removal (async, best-effort)
//  3. name backfill (SYNCHRONOUS — see backfillMissionName)
//  4. one mission reload, AFTER the backfill above, so every hook past
//     this point sees a stable, already-backfilled name
//  5. memory extraction, destinations delivery, workflow onTerminal —
//     each handed the SAME reloaded Mission, no hook reloads on its own
//  6. fireOnComplete, done transitions only
//
// Each hook may still dispatch its own goroutine (memory/destinations/
// workflow all remain fire-and-forget past this function returning) —
// the order guarantee above only covers the synchronous portion: it
// guarantees WHAT each hook is handed, not when its own background work
// finishes. notify.OnTransition is deliberately NOT one of these hooks:
// it fires on every transition, not just terminal ones, so callers keep
// invoking it separately.
//
// A hook failure is logged by the hook itself and never stops the rest
// of the sequence — one hook's trouble (a dead memoryd, a bad
// destination) must never suppress another's.
func (d *Driver) runTerminalHooks(ctx context.Context, id string, terminal Phase, events []EventDraft) {
	delete(d.gatekeepers, id)
	d.removeSandbox(id)

	reason := failedReason(events)
	if d.nameMission != nil {
		if pre, err := d.store.Get(ctx, id); err == nil && pre.Name == "" {
			// Synchronous but bounded: the backfill is an LLM call, and
			// a wedged provider must not stall the drive loop past this.
			nctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			d.backfillMissionName(nctx, id, pre.Goal)
			cancel()
		}
	}

	m, err := d.store.Get(ctx, id)
	if err != nil {
		d.log.Warn("driver: terminal hooks: reload mission failed", "mission_id", id, "error", err)
		return
	}
	d.extractMissionMemory(ctx, m, terminal, reason)
	d.deliverToDestinations(m, terminal)
	d.fireOnTerminal(m)
	if terminal == PhaseDone {
		d.fireOnComplete(ctx, id, m)
	}
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
func (d *Driver) Create(ctx context.Context, m Mission) (string, error) {
	if d.validateDeps != nil {
		if err := ValidateCreate(ctx, m, *d.validateDeps); err != nil {
			return "", fmt.Errorf("driver: create: %w", err)
		}
	}
	id, err := d.store.Create(ctx, m)
	if err != nil {
		return "", fmt.Errorf("driver: create: %w", err)
	}
	m.ID = id
	if _, err := d.ensureProvisioned(ctx, m); err != nil {
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
// inline — a hidden session, its standing grants, and a workspace — see
// provisioner.ensureProvisioned (provision.go, D-074) for the full
// contract. Kept as a Driver method so every call site below reads the
// same as before the split.
func (d *Driver) ensureProvisioned(ctx context.Context, m Mission) (Mission, error) {
	return d.provision.ensureProvisioned(ctx, m)
}

// grantSessionDefaults delegates to provisioner.grantSessionDefaults
// (provision.go, D-074) — kept as a Driver method since Signal's resume
// re-grant, and driver_test.go's direct guard-behavior assertion, both
// call it as d.grantSessionDefaults.
func (d *Driver) grantSessionDefaults(ctx context.Context, m Mission) {
	d.provision.grantSessionDefaults(ctx, m)
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
	if m.Phase.Terminal() || m.Status == StatusPaused || m.Status == StatusWaitingForInput ||
		m.Status == StatusDone || m.Status == StatusError {
		return false, nil
	}
	// D-056: admission applies only to idle->working, never to a mission
	// already mid-flight (Drive's own loop calling Advance again finds
	// m.Status == StatusWorking here, past this check) — a memory-tight
	// host must queue NEW work, not stall work already in progress.
	// canContinue=false stops just THIS Drive call; the mission's row is
	// untouched (still status='idle'), so the periodic work-slot sweep
	// retries it in workSlotSweepInterval exactly like a mission that
	// never got an immediate Drive at all.
	if m.Status == StatusIdle {
		if admit, reason := admitWork(ctx, d.capacity, d.log); !admit {
			d.log.Info("driver: capacity denied, mission stays idle", "mission_id", id, "reason", reason)
			return false, nil
		}
	}
	// A mission that reached the store without going through Create
	// (scheduler.go's createFromTemplate inserts a bare row directly) has
	// no session and no workspace yet — provision it now, on the first
	// turn that actually drives it, rather than leaving the worker with
	// no shell/write_file tools (runner.go's missionTools returns nil for
	// an empty WorkRoot).
	if m.SessionID == "" || (m.Workspace == "" && m.Worktree == "") {
		provisioned, provErr := d.ensureProvisioned(ctx, m)
		if provErr != nil {
			// Returning the error here would leave the mission idle for
			// the work-slot sweep to retry ensureProvisioned every 30s
			// forever. Pause it as an infra failure instead, same path a
			// phase-run error takes below.
			d.log.Error("driver: provisioning failed", "mission_id", id, "error", provErr)
			in := StepInput{Input: InputReviewInfraFailure, Reason: "provisioning failed: " + provErr.Error()}
			t := Step(d.toStepState(ctx, m), in, d.cfg)
			if err := d.store.ApplyTransition(ctx, id, t); err != nil {
				return false, fmt.Errorf("driver advance: apply transition after provisioning failure: %w", err)
			}
			return false, nil
		}
		m = provisioned
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
		case errors.Is(err, ErrExecutorAuth):
			// A delegated executor's own credential failed: retrying the
			// same entry is futile, so pause immediately as infra instead
			// of burning iterations (same reasoning as ErrModelFloor above).
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
	state := d.toStepState(ctx, m)
	t := Step(state, in, d.cfg)
	if err := d.store.ApplyTransition(ctx, id, t); err != nil {
		if errors.Is(err, ErrTerminal) {
			// The mission reached a terminal state (e.g. cancel) while this
			// turn was in flight — the turn's own transition arrived too
			// late and must be discarded, not written over the terminal
			// row. Cancel's own transition already tore down the sandbox;
			// this is the belt for a turn that raced past that teardown and
			// may have started a fresh container of its own.
			d.log.Info("driver: mission reached terminal state mid-turn, discarding turn result", "mission_id", id)
			delete(d.gatekeepers, id)
			d.removeSandbox(id)
			return false, nil
		}
		return false, fmt.Errorf("driver advance: apply transition: %w", err)
	}
	if t.Next.Phase.Terminal() {
		d.runTerminalHooks(ctx, id, t.Next.Phase, t.Events)
	}
	if d.notify != nil {
		if err := d.notify.OnTransition(ctx, m, before, t.Next.Status, failedReason(t.Events)); err != nil {
			d.log.Warn("driver: notify failed", "mission_id", id, "error", err)
		}
	}
	// D-064: a worker_failed retry that stays working (mission.retry, not
	// a pause/fail) paces the next Advance instead of spinning back-to-back
	// against a failing model at ~1/sec until the backoff pause. Only this
	// input paces — InputWorkerRetry is the worker actively progressing,
	// not failing, so it must not slow down.
	if in.Input == InputWorkerFailed && t.Next.Status == StatusWorking {
		if delay := d.retryDelayFn(t.Next.ConsecutiveFailures); delay > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}
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
	t := Step(d.toStepState(ctx, m), StepInput{Input: input}, d.cfg)
	if err := d.store.ApplyTransition(ctx, id, t); err != nil {
		return fmt.Errorf("driver: signal: apply transition: %w", err)
	}
	if t.Next.Phase.Terminal() {
		d.runTerminalHooks(ctx, id, t.Next.Phase, t.Events)
	}
	if d.notify != nil {
		if err := d.notify.OnTransition(ctx, m, before, t.Next.Status, failedReason(t.Events)); err != nil {
			d.log.Warn("driver: notify failed", "mission_id", id, "error", err)
		}
	}
	if input == InputResume {
		// A mission blocked long enough (backoff retries, or genuinely
		// waiting_for_input) can outlive its hidden session's
		// missionGrantTTL — resuming with an expired grant just re-hits
		// the same "no standing grant" denial that got it blocked in the
		// first place (D-039 degrade with no way back). Re-seeding here,
		// on the human/API resume signal, is the self-heal: same
		// best-effort grants grantSessionDefaults minted at provisioning.
		if m.SessionID != "" {
			d.grantSessionDefaults(ctx, m)
		}
		go func() { //nolint:gosec // G118: deliberate — the mission must outlive the HTTP request that resumed it, driveTimeBound is Drive's own cap
			if err := d.Drive(context.Background(), id); err != nil {
				d.log.Error("driver: post-resume drive failed", "mission_id", id, "error", err)
			}
		}()
	}
	return nil
}

// toStepState projects a Mission onto the state machine's input shape,
// pulling actual ledger spend for the budget brake via budgetProjector
// (budget.go, D-074).
func (d *Driver) toStepState(ctx context.Context, m Mission) StepState {
	spent, mixed, rateAsOf := d.budget.projectSpend(ctx, d.store, m)
	return StepState{
		Phase: m.Phase, Status: m.Status, PauseReason: m.PauseReason,
		Iteration: m.Iteration, MaxIterations: m.MaxIterations,
		ConsecutiveFailures: m.ConsecutiveFailures, LastGapFingerprint: m.LastGapFingerprint,
		StallCount: m.StallCount, Spent: spent, Budget: m.BudgetAmount,
		MixedCurrencySpend: mixed, RateAsOf: rateAsOf,
		LastUnit: isLastUnit(m.Spec), ReplanUsed: m.ReplanUsed, Light: m.Light,
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
// review verdict, planner output). Light missions (D-069) are born in
// PhaseExecute and short-circuit out of runExecute's done branch
// straight to InputReviewApprove, so they never reach the explore/plan/
// review cases below by construction.
func (d *Driver) runPhase(ctx context.Context, m Mission) (StepInput, error) {
	switch m.Phase {
	case PhaseExplore:
		return d.runExplore(ctx, m)
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

// exploreNotesCap bounds how much of the explore turn's findings get
// stored and fed into the plan phase's prompt — the findings feed
// straight into the planner's prompt, and unbounded notes would blow
// out that turn's context.
const exploreNotesCap = 8000

// runExplore runs one explore turn: a tool-using session that
// explores the goal before planning commits to a shape. The findings
// are stored on the mission (SetExploreNotes) so the plan phase's own
// (separate) Advance call reads them back via a fresh Get.
func (d *Driver) runExplore(ctx context.Context, m Mission) (StepInput, error) {
	notes, err := d.runner.ExploreSession(ctx, m)
	if err != nil {
		return StepInput{}, err
	}
	notes = truncate(notes, exploreNotesCap)
	if err := d.store.SetExploreNotes(ctx, m.ID, notes); err != nil {
		return StepInput{}, fmt.Errorf("driver: store explore notes: %w", err)
	}
	if err := d.store.AppendEvent(ctx, m.ID, "mission.explore_complete", map[string]any{"chars": len(notes)}); err != nil {
		d.log.Warn("driver: record explore complete failed", "mission_id", m.ID, "error", err)
	}
	return StepInput{Input: InputPhaseComplete}, nil
}

func (d *Driver) runPlan(ctx context.Context, m Mission) (StepInput, error) {
	priorSpec := m.Spec
	spec, err := d.runner.PlanSession(ctx, m, replanNotes(m))
	if err != nil {
		return StepInput{}, err
	}
	if err := d.store.AppendEvent(ctx, m.ID, "mission.plan_created", map[string]any{"units": len(spec.Units)}); err != nil {
		return StepInput{}, fmt.Errorf("driver: record plan: %w", err)
	}
	if m.ReplanUsed {
		// Carry forward prior harness evidence: a unit the new plan kept
		// unchanged (same title, same verify_cmd) and that had already
		// passed stays passed — the planner's own parseSpec forces every
		// unit to passes=false, since it can't itself claim pre-verified.
		restorePassedUnits(&spec, priorSpec)
	}
	if err := d.store.SetSpec(ctx, m.ID, spec); err != nil {
		return StepInput{}, err
	}
	return StepInput{Input: InputPhaseComplete}, nil
}

// replanNotes extends the planner's exploreNotes input with what
// stalled, on a replan only — first-time planning passes m.ExploreNotes
// through unchanged. Folded into the existing exploreNotes string
// argument (rather than a new Runner parameter) so PlanSession's
// interface never has to change for this feature.
func replanNotes(m Mission) string {
	if !m.ReplanUsed || len(m.Spec.Units) == 0 {
		return m.ExploreNotes
	}
	var b strings.Builder
	b.WriteString(m.ExploreNotes)
	b.WriteString("\n\nThe previous plan stalled and is being replanned. Current plan:\n")
	for _, u := range m.Spec.Units {
		status := "pending"
		if u.Passes {
			status = "verified"
		}
		fmt.Fprintf(&b, "- [%s] %s\n", status, u.Title)
	}
	if notes := recentProgressNotes(m.Progress, 3); notes != "" {
		b.WriteString("\nRecent progress:\n")
		b.WriteString(notes)
	}
	b.WriteString("\nKeep verified units unchanged, restructure or fix the rest.")
	return b.String()
}

// recentProgressNotes renders the last n progress notes, oldest first.
func recentProgressNotes(notes []ProgressNote, n int) string {
	if len(notes) > n {
		notes = notes[len(notes)-n:]
	}
	var b strings.Builder
	for _, note := range notes {
		fmt.Fprintf(&b, "- %s\n", note.Note)
	}
	return b.String()
}

// restorePassedUnits re-marks spec's units passed when a prior unit
// with the exact same title and verify_cmd had already passed — harness
// evidence a replan must not silently erase for work it didn't touch.
func restorePassedUnits(spec *Spec, prior Spec) {
	passed := make(map[string]bool, len(prior.Units))
	for _, u := range prior.Units {
		if u.Passes {
			passed[u.Title+"\x00"+u.VerifyCmd] = true
		}
	}
	for i := range spec.Units {
		if passed[spec.Units[i].Title+"\x00"+spec.Units[i].VerifyCmd] {
			spec.Units[i].Passes = true
		}
	}
}

// effectiveCommitStyle resolves the precedence mission override >
// settings default > CommitMessage's own conventional-style default.
func (d *Driver) effectiveCommitStyle(ctx context.Context, m Mission) string {
	if m.CommitStyle != "" {
		return m.CommitStyle
	}
	if d.gitCommitStyle != nil {
		return d.gitCommitStyle(ctx)
	}
	return ""
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
	// A handoff note is the worker's deliberate summary for the next
	// session; prefer it over the raw turn text, which is often just
	// tool chatter with no orientation value once the turn ends.
	progressNote := text
	if verdict.Handoff != "" {
		progressNote = verdict.Handoff
	}
	if err := d.recordProgress(ctx, m.ID, progressNote); err != nil {
		d.log.Warn("driver: record progress failed", "mission_id", m.ID, "error", err)
	}

	switch verdict.Outcome {
	case "done":
		if m.Worktree != "" {
			var unitTitle string
			if unit, _ := currentUnit(m.Spec); unit != nil {
				unitTitle = unit.Title
			}
			body := "mission " + m.ID + " iteration " + fmt.Sprint(m.Iteration)
			msg := CommitMessage(unitTitle, m.Goal, body, d.effectiveCommitStyle(ctx, m))
			if err := d.workspace.CommitUnit(ctx, m.Worktree, msg); err != nil {
				d.log.Warn("driver: commit unit failed", "mission_id", m.ID, "error", err)
			}
		}
		if err := d.store.SetLastEvidence(ctx, m.ID, verdict.Evidence); err != nil {
			d.log.Warn("driver: record evidence failed", "mission_id", m.ID, "error", err)
		}
		if m.Light {
			// D-069: a light mission has no plan/artifacts for
			// trySkipReview to check (it would bail into review for want
			// of them) — the worker's final message IS the deliverable,
			// so approve directly instead. FinalMessage is the text
			// written since the worker's last non-sentinel tool call, not
			// the whole multi-turn transcript (text) — fall back to text
			// only when a worker never wrote anything after its last tool
			// call (e.g. it called a tool, then immediately mission_status).
			// Priority: the sentinel's own final_output argument (the
			// reliable carrier — reasoning models often emit tool calls
			// with no plain text at all), then the free-text segment,
			// then the whole transcript.
			finalOutput := verdict.FinalOutput
			if finalOutput == "" {
				finalOutput = verdict.FinalMessage
			}
			if finalOutput == "" {
				finalOutput = text
			}
			if err := d.store.SetFinalOutput(ctx, m.ID, finalOutput); err != nil {
				d.log.Warn("driver: record final output failed", "mission_id", m.ID, "error", err)
			}
			if err := d.store.AppendEvent(ctx, m.ID, "mission.review_skipped", map[string]any{"reason": "light", "verified": false}); err != nil {
				d.log.Warn("driver: record review skip failed", "mission_id", m.ID, "error", err)
			}
			return StepInput{Input: InputReviewApprove}, nil
		}
		if in, ok := d.trySkipReview(ctx, m, verdict.SeenURLs); ok {
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
		in := StepInput{Input: InputWorkerRetry, Reason: truncate(verdict.Analysis, 500)}
		if verdict.Forced {
			// Neither a tool call nor a text-form sentinel (runner.go's
			// extractTextSentinel) could be read from this turn — a
			// distinct, stable fingerprint (rather than none at all) so
			// the stall brake (stepWorkerRetry, StallRounds consecutive
			// identical fingerprints) pauses the mission after repeated
			// sentinel-less turns instead of grinding to max_iterations on
			// a model that never learns to end its turn correctly.
			in.GapFingerprint = "no_sentinel"
		}
		return in, nil
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
func (d *Driver) trySkipReview(ctx context.Context, m Mission, seenURLs []string) (StepInput, bool) {
	if missionPolicyFor(m).alwaysReview {
		return StepInput{}, false
	}
	unit, idx := currentUnit(m.Spec)
	if unit == nil || len(unit.Artifacts) == 0 {
		return StepInput{}, false
	}
	if err := d.verify.verifyCurrentUnit(ctx, m, seenURLs); err != nil {
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
	if in, regressed := d.verify.checkRegressions(ctx, m); regressed {
		return in, true
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
		Listing: ListWorkspace(workRoot), Progress: m.Progress,
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
		if err := d.verify.verifyCurrentUnit(ctx, m, nil); err != nil {
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
		if in, regressed := d.verify.checkRegressions(ctx, m); regressed {
			return in, nil
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

// markUnitPassed delegates to verifier.markUnitPassed (verifier.go,
// D-074) — kept as a Driver method since units_test.go's alias-safety
// guard calls it directly as d.markUnitPassed.
func (d *Driver) markUnitPassed(ctx context.Context, m Mission, unit int) error {
	return d.verify.markUnitPassed(ctx, m, unit)
}

// packet builds the WorkPacket for the current phase/iteration
// directly from mission fields — no unused indirection.
func (d *Driver) packet(ctx context.Context, m Mission) (WorkPacket, error) {
	gitLog := ""
	if m.Worktree != "" {
		gitLog, _ = gitLogSince(ctx, m.Worktree, m.BaseCommit)
	}
	p := WorkPacket{
		Goal: m.Goal, Kind: m.Kind, Spec: m.Spec, Progress: m.Progress,
		GitLog: gitLog, Iteration: m.Iteration, PromptOverlay: m.PromptOverlay,
		ExecEnvironmentNote: execEnvironmentNote(), ParentContext: m.ParentContext, Attachments: m.Attachments,
		Light: missionPolicyFor(m).skipsPlanning,
	}
	if d.skillsIndex != nil && m.AgentID != "" {
		p.SkillsIndex = d.skillsIndex(ctx, m.AgentID)
	}
	return p, nil
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

// failedReason pulls payload["reason"] off a transition's mission.failed
// event, if any — the notifier's cheapest source for distinguishing
// "cancelled" from an ordinary failure, since Step already built this
// event this same round rather than requiring a second store query.
func failedReason(events []EventDraft) string {
	for _, ev := range events {
		if ev.Kind != "mission.failed" {
			continue
		}
		if reason, ok := ev.Payload["reason"].(string); ok {
			return reason
		}
	}
	return ""
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
