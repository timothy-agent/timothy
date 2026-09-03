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
	SetPlan(ctx context.Context, id string, plan Plan) error
	SetSession(ctx context.Context, id, sessionID string) error
	SetProvisioned(ctx context.Context, id, workspace, branch, baseCommit string) error
	SetLastEvidence(ctx context.Context, id, evidence string) error
	SetFinalOutput(ctx context.Context, id, text string) error
	SetDiscoverNotes(ctx context.Context, id, notes string) error
	SetEnvironment(ctx context.Context, id, environment, marker string) error
	SetNameIfEmpty(ctx context.Context, id, name string) error
	SetArtifactRefs(ctx context.Context, id string, refs []ArtifactRef) error
	SetDestinations(ctx context.Context, id string, entries []DestinationEntry) error
	AppendProgress(ctx context.Context, id, note string) error
	Spend(ctx context.Context, missionID string) (MissionSpend, error)
	ReviewInputTokens(ctx context.Context, missionID string) (int64, error)
	AnswerPendingInput(ctx context.Context, id, eventKind string, payload map[string]any) error
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
	// type (D-074): verifyAll's batch pass (D-094), whose outcomes are
	// the only evidence that ever flips a PlanUnit's HarnessPassed and,
	// through stepReviewApprove, Passes.
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

	// reviewWindow resolves the context window (tokens) of the model a
	// review route would serve, for the packet byte budget (D-097, see
	// SetReviewWindow); nil or 0 means unknown, reviewByteBudget's
	// fallback applies.
	reviewWindow func(ctx context.Context, route, model string) int
	// resolveRoute names the dead chain entry when a review turn fails
	// on the gateway's no usable provider error (D-100); nil leaves the
	// pause detail as the raw error.
	resolveRoute routeResolver

	// reviewTokenCeiling reads the per-mission review input token cap
	// (D-097, see SetReviewTokenCeiling); nil or 0 disables the check.
	reviewTokenCeiling func(ctx context.Context) int64

	// location resolves the operator's configured timezone for the
	// worker packet's exec-environment note and progress-note timestamps
	// (see SetLocation), nil-safe: unset renders both in UTC.
	location func(ctx context.Context) *time.Location

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

	// artifactCopy wires the best-effort copy of declared artifact
	// files into the attachment store on a mission's terminal done
	// transition (see SetArtifactCopy / copyArtifacts) — nil-safe:
	// unset leaves ArtifactRefs empty, same as before this existed. A
	// func type for the same import-cycle reason as deliverDestinations:
	// the copy needs the attachment store and destinations' own
	// artifact-path resolution, both out of missions' reach.
	artifactCopy ArtifactCopy

	// onTerminal wires the workflows engine's reaction to a mission
	// reaching a terminal phase (see SetOnTerminal / fireOnTerminal) —
	// nil-safe: unset skips it entirely, same as deliverDestinations. A
	// func type for the same import-cycle reason: workflows needs
	// missions.Mission, so missions can't import workflows back.
	onTerminal OnTerminal

	// promoteKB wires the kb-promotion hook fired on a mission's terminal
	// done transition (see SetPromoteKB / promoteToKB), nil-safe: unset
	// skips promotion entirely, same as a mission with no
	// promote_kb_collection_id. A func type for the same import-cycle
	// reason as deliverDestinations: promotion needs the attachment store
	// and kb store, both out of missions' reach.
	promoteKB PromoteKB

	// validateDeps backs ValidateCreate's store-backed checks (D-071):
	// a *ValidateDeps, not a value, so "unset" is distinguishable from
	// "set to the zero value": nil skips ValidateCreate's call entirely
	// (existing callers/tests that predate D-071 keep working
	// unvalidated), while a non-nil ValidateDeps{} runs every
	// struct-shape check but skips only the dep-backed ones (nil fields
	// within it). See SetValidateDeps.
	validateDeps *ValidateDeps

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

// agentName resolves a mission's agent to its display name for the
// mission.turn event payload (issue #473). Empty agentID or an unwired/
// missing resolver both return "" rather than erroring: a label is a
// nicety, never worth failing turn telemetry over.
func (d *Driver) agentName(ctx context.Context, agentID string) string {
	if agentID == "" || d.provision.resolveAgent == nil {
		return ""
	}
	defaults, ok := d.provision.resolveAgent(ctx, agentID)
	if !ok {
		return ""
	}
	return defaults.Name
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

// PRStateResolver resolves whether a github connector's owner/repo pull
// request number has been merged — connectors.Manager.PRMerged
// satisfies this. missions has no compile-time dependency on the
// connectors package, same reasoning as CloneTokenResolver.
type PRStateResolver func(ctx context.Context, connectorID, owner, repo string, number int) (merged bool, err error)

// SetPRStateResolver wires the resolver followUpBaseRef uses to detect
// a merged parent PR — a setter (not a NewDriver parameter) for the
// same reason SetAgentResolver is.
func (d *Driver) SetPRStateResolver(resolve PRStateResolver) {
	d.provision.resolvePRState = resolve
}

// SetGitCommitStyle wires the live settings getter runExecute falls
// back to when a mission's github destination entry has no CommitStyle,
// a setter for the same reason SetGitBranchPattern is.
func (d *Driver) SetGitCommitStyle(get func(ctx context.Context) string) {
	d.gitCommitStyle = get
}

// SetLocation wires the operator timezone accessor used for worker
// packet date/timestamp rendering, a setter for the same reason
// SetGitCommitStyle is.
func (d *Driver) SetLocation(loc func(ctx context.Context) *time.Location) {
	d.location = loc
}

// SetReviewWindow wires the review model window lookup (D-097), a
// setter for the same reason SetLocation is.
func (d *Driver) SetReviewWindow(fn func(ctx context.Context, route, model string) int) {
	d.reviewWindow = fn
}

// SetRouteResolver wires gwclient.ResolveRoute for review-route pause
// details (D-100).
func (d *Driver) SetRouteResolver(fn routeResolver) {
	d.resolveRoute = fn
}

// SetReviewTokenCeiling wires the settings-backed review token ceiling
// (D-097).
func (d *Driver) SetReviewTokenCeiling(fn func(ctx context.Context) int64) {
	d.reviewTokenCeiling = fn
}

// GatewayReviewWindow builds a Driver.reviewWindow over the gateway
// client: the model hint's own id when the catalog knows it, else the
// route's first usable chain entry. 0 when neither has a window.
func GatewayReviewWindow(resolve routeResolver, windows func(ctx context.Context) (map[string]int, error)) func(ctx context.Context, route, model string) int {
	return func(ctx context.Context, route, model string) int {
		ws, err := windows(ctx)
		if err != nil {
			return 0
		}
		// A D-078 pin is "provider name/model"; the catalog keys by model id.
		if i := strings.LastIndex(model, "/"); i >= 0 {
			model = model[i+1:]
		}
		if w := ws[model]; w > 0 {
			return w
		}
		resolved, err := resolve(ctx, route, "")
		if err != nil {
			return 0
		}
		for _, e := range resolved.Entries {
			if e.Usable {
				return ws[e.Model]
			}
		}
		return 0
	}
}

// reviewTokensExceeded reports whether used review input tokens have
// reached ceiling; a ceiling of 0 disables the check.
func reviewTokensExceeded(used, ceiling int64) bool {
	return ceiling > 0 && used >= ceiling
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

// SetNameMission installs the display-name generator: ensureProvisioned
// names a mission before its branch is cut so the slug comes from the
// title, not the goal (issue #494), and runResult backfills any mission
// that still reached a terminal phase unnamed. A setter for the same
// reason SetMemoryExtract is. Optional — nil leaves unnamed missions
// unnamed and branches slugged from the goal, same as before.
func (d *Driver) SetNameMission(fn func(context.Context, string) string) {
	d.nameMission = fn
	d.provision.nameMission = fn
}

// DestinationDeliver delivers a mission's generated output to its
// attached destination entries (Mission.Destinations, filtered to
// email/webhook/telegram kinds by the caller), returning the entries
// with delivered_at/error updated in place plus an error naming any
// destination that failed: destinations.Deliverer.Deliver satisfies
// this signature. Called SYNCHRONOUSLY from the result phase's step
// (runResult, slice 1 of the phase redesign): a returned error parks
// the mission in result rather than being logged and lost. Idempotent
// per destination: an entry whose DeliveredAt is already set is
// skipped on retry, only one with Error set (or never attempted) is
// retried.
type DestinationDeliver func(ctx context.Context, m Mission, entries []DestinationEntry) ([]DestinationEntry, error)

// SetDestinationDeliver wires the destinations delivery hook run by
// the result phase's step (see deliverToDestinations): a setter for
// the same reason SetMemoryExtract is. Optional, nil skips delivery
// entirely, same as a mission with no destination entries.
func (d *Driver) SetDestinationDeliver(fn DestinationDeliver) {
	d.deliverDestinations = fn
}

// deliverableEntries filters m.Destinations down to the kinds
// DestinationDeliver actually delivers: email/webhook/telegram, i.e.
// every entry naming an operator-owned destinations table row. "kb"
// and "github" entries are acted on by promoteToKB/fireOnComplete
// instead.
func deliverableEntries(entries []DestinationEntry) []DestinationEntry {
	var out []DestinationEntry
	for _, e := range entries {
		if e.DestinationID != "" {
			out = append(out, e)
		}
	}
	return out
}

// deliverToDestinations runs the destinations delivery hook against
// the mission's deliverable entries (if any), writing back each
// entry's delivered_at/error via SetDestinations and returning the
// hook's error (if any) for the result step to fold into its overall
// outcome.
func (d *Driver) deliverToDestinations(ctx context.Context, m Mission) error {
	targets := deliverableEntries(m.Destinations)
	if d.deliverDestinations == nil || len(targets) == 0 {
		return nil
	}
	updated, err := d.deliverDestinations(ctx, m, targets)
	if len(updated) > 0 {
		merged := mergeDestinationEntries(m.Destinations, updated)
		if setErr := d.store.SetDestinations(ctx, m.ID, merged); setErr != nil {
			d.log.Warn("driver: persist destination delivery state failed", "mission_id", m.ID, "error", setErr)
		}
	}
	return err
}

// mergeDestinationEntries replaces each of all's entries with its
// updated counterpart (matched by DestinationID) when one was
// delivered; entries deliverableEntries excluded (kb/github) pass
// through untouched.
func mergeDestinationEntries(all, updated []DestinationEntry) []DestinationEntry {
	byID := make(map[string]DestinationEntry, len(updated))
	for _, e := range updated {
		byID[e.DestinationID] = e
	}
	merged := make([]DestinationEntry, len(all))
	for i, e := range all {
		if u, ok := byID[e.DestinationID]; ok && e.DestinationID != "" {
			merged[i] = u
			continue
		}
		merged[i] = e
	}
	return merged
}

// PromoteKB promotes a mission's markdown artifacts into a kb
// collection with provenance='mission', returning any promotion
// error: kb.PromoteMission (D-081, issue #370) satisfies this
// signature. Called SYNCHRONOUSLY from the result phase's step, same
// reasoning as DestinationDeliver. Idempotent: promoteOne dedups by
// source_ref, replacing an existing document rather than duplicating
// it on retry.
type PromoteKB func(ctx context.Context, m Mission, collectionID string) error

// SetPromoteKB wires the kb-promotion hook run by the result phase's
// step (see promoteToKB): a setter for the same reason
// SetDestinationDeliver is. Optional: nil skips promotion entirely,
// same as a mission with no "kb" destination entry.
func (d *Driver) SetPromoteKB(fn PromoteKB) {
	d.promoteKB = fn
}

// promoteToKB runs the kb-promotion hook when the mission has a "kb"
// destination entry, returning its error (if any) for the result step
// to fold into its overall outcome.
func (d *Driver) promoteToKB(ctx context.Context, m Mission) error {
	collectionID := m.KBCollectionID()
	if d.promoteKB == nil || collectionID == "" {
		return nil
	}
	return d.promoteKB(ctx, m, collectionID)
}

// ArtifactCopy best-effort copies a terminal mission's declared
// artifact files into the attachment store and returns the resulting
// refs — destinations.CopyArtifacts satisfies this signature. Must
// never error or block indefinitely: a vanished/oversize/disallowed-
// mime file is simply skipped, never fails the transition.
type ArtifactCopy func(ctx context.Context, m Mission) []ArtifactRef

// SetArtifactCopy wires the artifact-copy hook run by the result
// phase's step (see copyArtifacts): a setter for the same reason
// SetDestinationDeliver is. Optional, nil leaves ArtifactRefs empty,
// same as a mission with no declared artifacts.
func (d *Driver) SetArtifactCopy(fn ArtifactCopy) {
	d.artifactCopy = fn
}

// copyArtifacts runs the artifact-copy hook and persists the
// resulting refs onto the mission row, so deliverToDestinations'
// webhook payload (built from the same reloaded Mission) can include
// them. Bounded so a slow attachment store never stalls the result
// step indefinitely. Content-addressed on the attachment store side,
// so re-running on a result retry is naturally idempotent: no error
// return, matching ArtifactCopy's own never-fails contract.
func (d *Driver) copyArtifacts(ctx context.Context, id string, m Mission) Mission {
	if d.artifactCopy == nil {
		return m
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	refs := d.artifactCopy(cctx, m)
	if len(refs) == 0 {
		return m
	}
	if err := d.store.SetArtifactRefs(ctx, id, refs); err != nil {
		d.log.Warn("driver: persist artifact refs failed", "mission_id", id, "error", err)
		return m
	}
	m.ArtifactRefs = refs
	return m
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
// (push/push_pr), the SAME Completer code the manual push/pr API
// endpoints use, so an auto-fired push/PR can never diverge from what
// a human clicking the button gets. Completer.RunOnComplete already
// appends mission.push_failed itself (via PushBranch/OpenPR's own
// error path) on failure; this additionally fires a best-effort
// notification so the operator hears about it, mirroring notify.go's
// own best-effort webhook fan-out. The error return is the result
// step's own signal to park (an explicit operator choice, made at
// create time, so a failure here must be visible and retryable, not
// silently swallowed).
//
// RunOnComplete's create-if-missing path (issue #483) may create a
// repo and resolve the github destination entry's final RepoURL: when
// that happened (updated), this persists it via SetDestinations before
// returning, same "write back what actually changed" pattern
// deliverToDestinations/promoteToKB already follow. A retry (the
// autoResumeInfra sweep) then sees the created repo on its next
// attempt instead of trying to create it again.
func (d *Driver) fireOnComplete(ctx context.Context, id string, m Mission) error {
	onComplete := m.OnComplete()
	if d.completer == nil || onComplete == "" {
		return nil
	}
	entry, updated, err := d.completer.RunOnComplete(ctx, m)
	if updated {
		merged := mergeGitHubDestinationEntry(m.Destinations, entry)
		if setErr := d.store.SetDestinations(ctx, id, merged); setErr != nil {
			d.log.Warn("driver: persist github destination entry failed", "mission_id", id, "error", setErr)
		}
	}
	if err != nil {
		d.log.Error("driver: on_complete auto-fire failed", "mission_id", id, "on_complete", onComplete, "error", err)
		if d.notifyPushFailed != nil {
			d.notifyPushFailed(ctx, id, fmt.Sprintf("mission %s: automatic %s failed: %s", id, onComplete, err.Error()))
		}
		return err
	}
	return nil
}

// mergeGitHubDestinationEntry replaces all's "github" entry with
// updated, leaving every other entry untouched: mergeDestinationEntries'
// counterpart for the single github entry (which, unlike email/webhook/
// telegram entries, has no DestinationID to match on).
func mergeGitHubDestinationEntry(all []DestinationEntry, updated DestinationEntry) []DestinationEntry {
	merged := make([]DestinationEntry, len(all))
	for i, e := range all {
		if e.Destination == DestinationKindGitHub {
			merged[i] = updated
			continue
		}
		merged[i] = e
	}
	return merged
}

// resultStepOrder documents runResult's fixed sequence (slice 1 of the
// phase redesign, D-086): every piece the old terminal-done transition
// used to fire, now run as the result phase's own deterministic step,
// zero LLM turns.
//
//  1. name backfill (see backfillMissionName), then one mission
//     reload so every piece past this point sees the backfilled name.
//  2. artifact copy (see copyArtifacts): runs before destinations
//     delivery below so its webhook payload can include the refs.
//  3. destinations delivery, kb promotion, on_complete: each a
//     required piece now (unlike before this phase existed); any
//     failure among them means runResult reports failure and the
//     driver parks the mission in result instead of losing it.
//
// A failure in one piece does not stop the others from running this
// same round: every piece gets a chance, and every failure is folded
// into one summary event, so a retry only needs to re-run what
// actually failed (each piece is independently idempotent, see their
// own doc comments).
func (d *Driver) runResult(ctx context.Context, m Mission) (StepInput, error) {
	if d.nameMission != nil {
		if m.Name == "" {
			nctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			d.backfillMissionName(nctx, m.ID, m.Goal)
			cancel()
			if reloaded, err := d.store.Get(ctx, m.ID); err == nil {
				m = reloaded
			}
		}
	}
	m = d.copyArtifacts(ctx, m.ID, m)

	summary := map[string]any{}
	var failures []string

	targets := deliverableEntries(m.Destinations)
	if err := d.deliverToDestinations(ctx, m); err != nil {
		failures = append(failures, "delivery: "+err.Error())
		summary["delivery_error"] = err.Error()
	} else if len(targets) > 0 {
		summary["delivered"] = len(targets)
	}

	kbCollectionID := m.KBCollectionID()
	if err := d.promoteToKB(ctx, m); err != nil {
		failures = append(failures, "kb promotion: "+err.Error())
		summary["promote_kb_error"] = err.Error()
	} else if kbCollectionID != "" {
		summary["promoted_kb_collection_id"] = kbCollectionID
	}

	onComplete := m.OnComplete()
	if err := d.fireOnComplete(ctx, m.ID, m); err != nil {
		failures = append(failures, "on_complete: "+err.Error())
		summary["on_complete_error"] = err.Error()
	} else if onComplete != "" {
		summary["on_complete"] = onComplete
	}

	if len(m.ArtifactRefs) > 0 {
		summary["artifacts_copied"] = len(m.ArtifactRefs)
	}
	if err := d.store.AppendEvent(ctx, m.ID, "mission.result_complete", summary); err != nil {
		d.log.Warn("driver: record result complete failed", "mission_id", m.ID, "error", err)
	}

	if len(failures) > 0 {
		return StepInput{Input: InputResultFailed, Reason: strings.Join(failures, "; ")}, nil
	}
	return StepInput{Input: InputResultComplete}, nil
}

// D-073: runTerminalHooks is the single place Advance and Signal fire
// every terminal-transition hook, replacing what used to be a verbatim
// block duplicated in both. Since D-086 (the result phase), this only
// covers the hooks that fire on EVERY terminal transition (done or
// failed): delivery/copy/promote/on_complete moved into runResult,
// which the driver runs as an ordinary phase step before the mission
// ever reaches this function.
//
// Fixed order:
//  1. sandbox container removal (async, best-effort)
//  2. one mission reload
//  3. memory extraction, workflow onTerminal: each handed the SAME
//     reloaded Mission, no hook reloads on its own
//
// A hook failure is logged by the hook itself and never stops the
// rest of the sequence.
func (d *Driver) runTerminalHooks(ctx context.Context, id string, terminal Phase, events []EventDraft) {
	d.removeSandbox(id)

	reason := failedReason(events)
	m, err := d.store.Get(ctx, id)
	if err != nil {
		d.log.Warn("driver: terminal hooks: reload mission failed", "mission_id", id, "error", err)
		return
	}
	d.extractMissionMemory(ctx, m, terminal, reason)
	d.fireOnTerminal(m)
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

// Advance performs exactly one worker turn, review round, planning
// turn, or result step for mission id, whatever its current phase
// calls for, then persists the resulting transition and returns
// whether the mission can be Advanced again immediately (false on
// terminal, paused, waiting_for_input, or idle).
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
			d.log.Info("driver: capacity denied, mission stays idle", "mission_id", id, "phase", m.Phase, "reason", reason)
			return false, nil
		}
	}
	// A mission that reached the store without going through Create
	// (scheduler.go's createFromTemplate inserts a bare row directly) has
	// no session and no workspace yet — provision it now, on the first
	// turn that actually drives it, rather than leaving the worker with
	// no shell/write_file tools (runner.go's missionTools returns nil for
	// an empty WorkRoot).
	if m.SessionID == "" || m.Workspace == "" {
		provisioned, provErr := d.ensureProvisioned(ctx, m)
		if provErr != nil {
			// Returning the error here would leave the mission idle for
			// the work-slot sweep to retry ensureProvisioned every 30s
			// forever. Pause it as an infra failure instead, same path a
			// phase-run error takes below.
			d.log.Error("driver: provisioning failed", "mission_id", id, "agent", d.agentName(ctx, m.AgentID), "error", provErr)
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
	if err != nil && errors.Is(err, ErrAskedUser) {
		// The turn already parked (nativeRunner.askUserTool's Execute
		// recorded pending_input and incremented asks_used before ending
		// the turn): this is not a failure, just the phase's own runner
		// method reporting "no verdict, ask_user handled it instead."
		in = StepInput{Input: InputAskUser}
		err = nil
	}
	if err != nil {
		d.log.Error("driver: phase run failed", "mission_id", id, "phase", m.Phase,
			"route", phaseRoute(m), "agent", d.agentName(ctx, m.AgentID), "error", err)
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
		case m.Phase == PhaseProve:
			in = StepInput{Input: InputReviewInfraFailure, Reason: err.Error()}
			if route, skip, ok := d.reviewRouteSkip(ctx, m, err); ok {
				in.Route = route
				in.Reason = fmt.Sprintf("review route %q has no usable provider: %s", route, skip)
			}
		case m.Phase == PhaseResult:
			// runResult itself never returns an error (each hook's own
			// failure is folded into the result, see runResult); this
			// case only guards against a future bug in runResult's own
			// wiring, same fail-safe reasoning as the other cases here.
			in = StepInput{Input: InputResultFailed, Reason: err.Error()}
		default:
			// review_infra_failure is prove's error input; other phases
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
	if m.Phase == PhaseGenerate && workerRoute(m) != m.Route {
		payload["escalated_route"] = workerRoute(m)
	}
	// Route/agent context (issue #473): route is the mission's own
	// effective route for the phase that just ran (same helpers
	// runner.go's request builders already use), never the resolved
	// model, since the runner doesn't surface which chain entry
	// actually served the turn, and guessing one is worse than omitting it.
	if route := phaseRoute(m); route != "" {
		payload["route"] = route
	}
	if agent := d.agentName(ctx, m.AgentID); agent != "" {
		payload["agent"] = agent
	}
	// Provider/model (issue #507): who actually served the turn, from
	// the runner's own verdict/plan; empty (never guessed) when the
	// turn failed before any provider answered.
	if in.Provider != "" {
		payload["provider"] = in.Provider
	}
	if in.Model != "" {
		payload["model"] = in.Model
	}
	if evErr := d.store.AppendEvent(ctx, id, "mission.turn", payload); evErr != nil {
		d.log.Warn("driver: record turn failed", "mission_id", id, "error", evErr)
	}

	// runPhase may have written mission state of its own (runPlan's
	// SetPlan, progress notes, evidence): re-fetch so Step sees those
	// writes rather than the pre-round snapshot loaded at the top of
	// this call.
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
			d.log.Info("driver: mission reached terminal state mid-turn, discarding turn result", "mission_id", id, "phase", m.Phase)
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

// isNoRouteError reports whether err is the gateway's no usable
// provider failure, as gwclient surfaces it (an HTTP 502 body carrying
// the no_route code, or the router's own message).
func isNoRouteError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, `"no_route"`) || strings.Contains(msg, "no usable provider")
}

// reviewRouteSkip names the review route a failed prove turn ran on
// and the first chain entry's skip reason (D-100), so the pause detail
// says which entry is dead. ok is false when err is not the gateway's
// no usable provider error or the route cannot be resolved.
func (d *Driver) reviewRouteSkip(ctx context.Context, m Mission, err error) (route, skip string, ok bool) {
	if d.resolveRoute == nil || !isNoRouteError(err) {
		return "", "", false
	}
	route = reviewRoute(m)
	resolved, rerr := d.resolveRoute(ctx, route, "")
	if rerr != nil || resolved == nil {
		return "", "", false
	}
	skip = "route has no chain entries"
	for _, e := range resolved.Entries {
		if e.Usable {
			return "", "", false
		}
		if e.SkipReason != "" {
			skip = e.SkipReason
			break
		}
	}
	return route, skip, true
}

// ChangeRouting rewrites a paused mission's review route and model pin
// (D-100, issue #536): the API layer's routing PATCH calls this after
// validating the route resolves. ErrNotPaused for any other status,
// mapped to 409 by the API layer. The mission stays paused; resume is
// a separate operator action.
func (d *Driver) ChangeRouting(ctx context.Context, id, reviewRoute, reviewRouteModel string) error {
	m, err := d.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("driver: change routing: %w", err)
	}
	if m.Phase.Terminal() {
		return ErrTerminal
	}
	if m.Status != StatusPaused {
		return ErrNotPaused
	}
	t := Step(d.toStepState(ctx, m), StepInput{Input: InputRouteChange, ReviewRoute: reviewRoute, ReviewRouteModel: reviewRouteModel}, d.cfg)
	if err := d.store.ApplyTransition(ctx, id, t); err != nil {
		return fmt.Errorf("driver: change routing: apply transition: %w", err)
	}
	return nil
}

// DecidePlan applies one of the three plan-approval verbs (D-087,
// issue #456) to a mission parked on PauseApproval: the API layer's
// approve/replan/rediscover endpoints call this, never the model.
// feedback is only meaningful on InputPlanReplan (folded into the next
// planning prompt by runPlan via replanNotes); ignored otherwise.
// Returns ErrNotAwaitingApproval for any other state, mapped to 409 by
// the API layer.
func (d *Driver) DecidePlan(ctx context.Context, id string, input Input, feedback string) error {
	if input != InputPlanApprove && input != InputPlanReplan && input != InputPlanRediscover {
		return fmt.Errorf("driver: decide plan: %q is not a plan-approval input", input)
	}
	m, err := d.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("driver: decide plan: %w", err)
	}
	if m.Phase != PhasePlan || m.PauseReason != PauseApproval {
		return ErrNotAwaitingApproval
	}
	before := m.Status
	t := Step(d.toStepState(ctx, m), StepInput{Input: input, Reason: feedback}, d.cfg)
	if err := d.store.ApplyTransition(ctx, id, t); err != nil {
		return fmt.Errorf("driver: decide plan: apply transition: %w", err)
	}
	if d.notify != nil {
		if err := d.notify.OnTransition(ctx, m, before, t.Next.Status, failedReason(t.Events)); err != nil {
			d.log.Warn("driver: notify failed", "mission_id", id, "error", err)
		}
	}
	go func() { //nolint:gosec // G118: deliberate, the mission must outlive the HTTP request that decided it, driveTimeBound is Drive's own cap
		if err := d.Drive(context.Background(), id); err != nil {
			d.log.Error("driver: post-plan-decision drive failed", "mission_id", id, "error", err)
		}
	}()
	return nil
}

// AnswerAskUser resolves a mission's parked ask_user question (D-088):
// the API layer's answer endpoint calls this, never the model.
// Records the Q+A as a progress note (the same durable channel a
// worker/review/plan turn already reads back: WorkPacket.Progress,
// ReviewPacket.Progress, replanNotes' recentProgressNotes), so the
// NEXT turn of the SAME phase (Store.AnswerPendingInput resumes to
// idle without touching Phase) carries the question and answer
// verbatim. Returns ErrNotAwaitingApproval if the mission isn't
// currently parked on a question, reused rather than a new sentinel
// since both mean the same thing to a caller: "there's nothing here to
// answer."
func (d *Driver) AnswerAskUser(ctx context.Context, id, answer string) error {
	m, err := d.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("driver: answer ask user: %w", err)
	}
	if m.PendingInput == nil {
		return ErrNotAwaitingApproval
	}
	note := fmt.Sprintf("Operator answered your question %q: %s", m.PendingInput.Question, NeutralizeSlot(answer))
	if err := d.store.AppendProgress(ctx, id, note); err != nil {
		d.log.Warn("driver: record ask_user answer progress failed", "mission_id", id, "error", err)
	}
	if err := d.store.AnswerPendingInput(ctx, id, "mission.input_answered", map[string]any{"answer": answer}); err != nil {
		return fmt.Errorf("driver: answer ask user: %w", err)
	}
	go func() { //nolint:gosec // G118: deliberate, the mission must outlive the HTTP request that answered it, driveTimeBound is Drive's own cap
		if err := d.Drive(context.Background(), id); err != nil {
			d.log.Error("driver: post-answer drive failed", "mission_id", id, "error", err)
		}
	}()
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
		Units: m.Plan.Units, ReplanUsed: m.ReplanUsed,
		AutoApprovePlan: m.AutoApprovePlan, Flow: m.Flow,
		ReviewFindings: m.ReviewFindings, ReworkRounds: m.ReworkRounds,
		LastReviewCommit: m.Plan.LastReviewCommit, LastReviewAt: m.Plan.LastReviewAt,
		ReviewRoute: m.ReviewRoute, ReviewRouteModel: m.ReviewRouteModel,
	}
}

// runPhase runs the phase-appropriate session and returns the StepInput
// its outcome maps to. It does not itself decide pass/fail semantics
// beyond what each phase's contract already defines (worker sentinel,
// review verdict, planner output). Light missions (D-069) are born in
// PhaseGenerate and short-circuit out of runExecute's done branch
// straight to InputReviewApprove, so they never reach the discover/plan/
// prove cases below by construction. flow=discover_generate (D-090,
// issue #459) DOES reach PhaseDiscover (it runs discover as normal),
// but its own PhaseGenerate turn takes the same planless short-circuit
// as light, so it never reaches PhasePlan or PhaseProve either.
func (d *Driver) runPhase(ctx context.Context, m Mission) (StepInput, error) {
	switch m.Phase {
	case PhaseDiscover:
		return d.runDiscover(ctx, m)
	case PhasePlan:
		return d.runPlan(ctx, m)
	case PhaseGenerate:
		return d.runExecute(ctx, m)
	case PhaseProve:
		return d.runReview(ctx, m)
	case PhaseResult:
		return d.runResult(ctx, m)
	default:
		return StepInput{}, fmt.Errorf("driver: mission %s in unhandled phase %q", m.ID, m.Phase)
	}
}

// discoverNotesCap bounds how much of the discover turn's findings get
// stored and fed into the plan phase's prompt — the findings feed
// straight into the planner's prompt, and unbounded notes would blow
// out that turn's context.
const discoverNotesCap = 8000

// runDiscover runs one discover turn: a tool-using session that
// explores the goal before planning (or, for flow=discover_generate,
// D-090, the planless generate pass) commits to a shape. The findings
// are stored on the mission (SetDiscoverNotes) so the next phase's own
// (separate) Advance call reads them back via a fresh Get.
func (d *Driver) runDiscover(ctx context.Context, m Mission) (StepInput, error) {
	notes, provider, model, err := d.runner.DiscoverSession(ctx, m)
	if err != nil {
		return StepInput{}, err
	}
	notes = truncate(notes, discoverNotesCap)
	if err := d.store.SetDiscoverNotes(ctx, m.ID, notes); err != nil {
		return StepInput{}, fmt.Errorf("driver: store discover notes: %w", err)
	}
	if err := d.store.AppendEvent(ctx, m.ID, "mission.discover_complete", map[string]any{"chars": len(notes)}); err != nil {
		d.log.Warn("driver: record discover complete failed", "mission_id", m.ID, "error", err)
	}
	d.recreateSandboxIfEnvironmentChanged(ctx, m)
	return StepInput{Input: InputPhaseComplete, Provider: provider, Model: model}, nil
}

// recreateSandboxIfEnvironmentChanged removes the mission's sandbox
// container when the discover turn just set an environment (issue
// #495): the container's image is fixed at create, and discover's own
// shell calls already created it on base, so without this the mission
// would keep running on base whatever Environment now says. Discover
// holds no CLI session, so nothing in the container is lost; the next
// exec recreates it on the right image. Best-effort: a removal failure
// leaves the mission on base, which is what it had before.
func (d *Driver) recreateSandboxIfEnvironmentChanged(ctx context.Context, before Mission) {
	if d.sandboxRemove == nil {
		return
	}
	after, err := d.store.Get(ctx, before.ID)
	if err != nil || after.Environment == before.Environment {
		return
	}
	if err := d.sandboxRemove.Remove(ctx, before.ID); err != nil {
		d.log.Warn("driver: sandbox recreate after environment change failed; mission stays on base", "mission_id", before.ID, "environment", after.Environment, "error", err)
		return
	}
	if err := d.store.AppendEvent(ctx, before.ID, "mission.sandbox_recreated", map[string]any{"environment": after.Environment}); err != nil {
		d.log.Warn("driver: record sandbox recreate failed", "mission_id", before.ID, "error", err)
	}
}

func (d *Driver) runPlan(ctx context.Context, m Mission) (StepInput, error) {
	priorPlan := m.Plan
	plan, err := d.runner.PlanSession(ctx, m, replanNotes(m))
	if err != nil {
		return StepInput{}, err
	}
	// D-077: the planner refused to write a plan because the goal cannot
	// be achieved as stated: fail the mission instead of storing a plan
	// and advancing to execute.
	if plan.Infeasible {
		if err := d.store.AppendEvent(ctx, m.ID, "mission.plan_infeasible", map[string]any{"reason": plan.InfeasibleReason}); err != nil {
			return StepInput{}, fmt.Errorf("driver: record plan infeasible: %w", err)
		}
		return StepInput{Input: InputPlanInfeasible, Reason: plan.InfeasibleReason, Provider: plan.Provider, Model: plan.Model}, nil
	}
	planCreatedPayload := map[string]any{"units": len(plan.Units)}
	if len(plan.Assumptions) > 0 {
		planCreatedPayload["assumptions"] = plan.Assumptions
	}
	if err := d.store.AppendEvent(ctx, m.ID, "mission.plan_created", planCreatedPayload); err != nil {
		return StepInput{}, fmt.Errorf("driver: record plan: %w", err)
	}
	if m.ReplanUsed {
		// Carry forward prior harness evidence: a unit the new plan kept
		// unchanged (same title, same verify_cmd) and that had already
		// passed stays passed -- the planner's own parsePlan forces every
		// unit to passes=false, since it can't itself claim pre-verified.
		restorePassedUnits(&plan, priorPlan)
	}
	if err := d.store.SetPlan(ctx, m.ID, plan); err != nil {
		return StepInput{}, err
	}
	return StepInput{Input: InputPhaseComplete, Provider: plan.Provider, Model: plan.Model}, nil
}

// replanNotes extends the planner's discoverNotes input with what
// stalled, on a replan only, first-time planning passes m.DiscoverNotes
// through unchanged. Folded into the existing discoverNotes string
// argument (rather than a new Runner parameter) so PlanSession's
// interface never has to change for this feature.
//
// Gated on len(m.Plan.Units) > 0 rather than m.ReplanUsed alone: an
// operator-requested replan (D-087, issue #456) re-enters PhasePlan
// without setting ReplanUsed (that flag is reserved for the automatic
// stall-replan budget, never spent by a free operator iteration (see
// stepPlanReplan), so the prior plan and any operator feedback
// (appended as a progress note by the API's replan handler, same
// channel as a steering note) must reach the prompt through this same
// "a plan already exists" check instead.
func replanNotes(m Mission) string {
	if len(m.Plan.Units) == 0 {
		notes := m.DiscoverNotes
		// D-088: an ask_user answer during a first-time plan turn (no
		// plan landed yet, so the "being replanned" branch below never
		// runs) still needs to reach the very next plan turn's prompt,
		// same recentProgressNotes channel, just not gated on a plan
		// existing yet.
		if progress := recentProgressNotes(m.Progress, 3); progress != "" {
			notes += "\n\nRecent progress (includes any operator answers to prior questions):\n" + progress
		}
		return notes
	}
	var b strings.Builder
	b.WriteString(m.DiscoverNotes)
	b.WriteString("\n\nThe previous plan is being replanned. Current plan:\n")
	for _, u := range m.Plan.Units {
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

// restorePassedUnits re-marks plan's units passed when a prior unit
// with the exact same title and verify_cmd had already passed — harness
// evidence a replan must not silently erase for work it didn't touch.
func restorePassedUnits(plan *Plan, prior Plan) {
	passed := make(map[string]bool, len(prior.Units))
	for _, u := range prior.Units {
		if u.Passes {
			passed[u.Title+"\x00"+u.VerifyCmd] = true
		}
	}
	for i := range plan.Units {
		if passed[plan.Units[i].Title+"\x00"+plan.Units[i].VerifyCmd] {
			plan.Units[i].Passes, plan.Units[i].HarnessPassed = true, true
		}
	}
}

// effectiveCommitStyle resolves the precedence mission override >
// settings default > CommitMessage's own conventional-style default.
func (d *Driver) effectiveCommitStyle(ctx context.Context, m Mission) string {
	if e, ok := m.GitHubEntry(); ok && e.CommitStyle != "" {
		return e.CommitStyle
	}
	if d.gitCommitStyle != nil {
		return d.gitCommitStyle(ctx)
	}
	return ""
}

func (d *Driver) runExecute(ctx context.Context, m Mission) (StepInput, error) {
	// D-094: every unit already harness-passed and no finding open means
	// there is nothing for a worker to do; go straight to prove.
	if !m.RunsPlanless() && len(m.Plan.Units) > 0 && firstUnverified(m.Plan) == -1 && len(OpenFindings(m.ReviewFindings)) == 0 {
		if err := d.store.AppendEvent(ctx, m.ID, "mission.generate_skipped", map[string]any{
			"reason": "every unit is harness-verified and no review finding is open",
		}); err != nil {
			d.log.Warn("driver: record generate skip failed", "mission_id", m.ID, "error", err)
		}
		return d.routeVerified(ctx, m, nil), nil
	}
	packet, err := d.packet(ctx, m)
	if err != nil {
		return StepInput{}, err
	}
	// D-092: the pre-turn HEAD anchors the touched-files diff below, so
	// the rework brakes score exactly this turn's changes.
	preTurnHead := ""
	if wt := m.WorktreePath(); wt != "" {
		preTurnHead = worktreeHead(ctx, wt)
	}
	verdict, text, err := d.runner.RunWorker(ctx, m, packet)
	if err != nil {
		return StepInput{}, err
	}
	if err := d.recordProgress(ctx, m.ID, progressNote(verdict, text)); err != nil {
		d.log.Warn("driver: record progress failed", "mission_id", m.ID, "error", err)
	}

	switch verdict.Outcome {
	case "done":
		if wt := m.WorktreePath(); wt != "" {
			var unitTitle string
			if unit, _ := currentUnit(m.Plan); unit != nil {
				unitTitle = unit.Title
			}
			body := "mission " + m.ID + " iteration " + fmt.Sprint(m.Iteration)
			msg := CommitMessage(unitTitle, m.Goal, body, d.effectiveCommitStyle(ctx, m))
			switch err := d.workspace.CommitUnit(ctx, wt, msg); {
			case errors.Is(err, errNothingToCommit):
				d.log.Debug("driver: commit unit skipped, no changes", "mission_id", m.ID)
			case err != nil:
				d.log.Warn("driver: commit unit failed", "mission_id", m.ID, "error", err)
			}
		}
		if err := d.store.SetLastEvidence(ctx, m.ID, verdict.Evidence); err != nil {
			d.log.Warn("driver: record evidence failed", "mission_id", m.ID, "error", err)
		}
		if m.RunsPlanless() {
			// D-069/D-090: a planless mission (light, or
			// flow=discover_generate) has no plan/artifacts for
			// routeVerified to check (it would bail into review for want
			// of them); the worker's final message IS the deliverable,
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
			reason := "light"
			if m.Flow == FlowDiscoverGenerate {
				reason = "discover_generate"
			}
			if err := d.store.AppendEvent(ctx, m.ID, "mission.review_skipped", map[string]any{"reason": reason, "verified": false}); err != nil {
				d.log.Warn("driver: record review skip failed", "mission_id", m.ID, "error", err)
			}
			return StepInput{Input: InputReviewApprove, Provider: verdict.Provider, Model: verdict.Model}, nil
		}
		// D-094: the harness checks every unit now, not the reviewer
		// later. The worker's unit failing, or an earlier unit regressing,
		// buys another worker turn with the excerpt in its packet.
		verified, err := d.verify.verifyAll(ctx, m, verdict.SeenURLs, true)
		if err != nil {
			return StepInput{}, err
		}
		if failing := failedUnits(verified); len(failing) > 0 {
			in := StepInput{Input: InputWorkerRetry, Verified: verified, Provider: verdict.Provider, Model: verdict.Model}
			in.Reason, in.GapFingerprint = harnessFailureReason(m.Plan, failing)
			return in, nil
		}
		in := d.routeVerified(ctx, m, verified)
		in.Provider, in.Model = verdict.Provider, verdict.Model
		if preTurnHead != "" {
			in.TouchedFiles = touchedFiles(ctx, m.WorktreePath(), preTurnHead)
		}
		return in, nil
	case "blocked":
		return StepInput{Input: InputWorkerBlocked, Message: verdict.Question, Provider: verdict.Provider, Model: verdict.Model}, nil
	default: // "retry" or anything unrecognized
		if wt := m.WorktreePath(); wt != "" {
			if err := d.workspace.Rollback(ctx, wt, m.Kind); err != nil {
				d.log.Warn("driver: rollback failed", "mission_id", m.ID, "error", err)
			}
		}
		in := StepInput{Input: InputWorkerRetry, Reason: truncate(verdict.Analysis, 500), Provider: verdict.Provider, Model: verdict.Model}
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

// routeVerified picks the input after a batch pass with no failures
// (D-094): review_approve when the harness evidence alone settles the
// units awaiting approval, phase_complete (into prove) otherwise. The
// skip needs a policy that does not always review (coding missions
// do: a diff can be wrong in ways existence checks can't see), no open
// finding (a reviewer must close those), and declared artifacts on every
// unit awaiting approval (none means no harness evidence to stand on).
// flow=no_prove (D-090, issue #459) forces alwaysReview false the same
// way general kind does (policyFor), so this is its only skip mechanism.
func (d *Driver) routeVerified(ctx context.Context, m Mission, verified []UnitVerification) StepInput {
	in := StepInput{Input: InputPhaseComplete, Verified: verified}
	if missionPolicyFor(m).alwaysReview || len(OpenFindings(m.ReviewFindings)) > 0 {
		return in
	}
	units, _ := applyVerification(StepState{Units: m.Plan.Units}, verified)
	pending := unitsUnderReview(units.Units)
	for _, i := range pending {
		if len(units.Units[i].Artifacts) == 0 {
			return in
		}
	}
	if err := d.store.AppendEvent(ctx, m.ID, "mission.review_skipped", map[string]any{
		"units": pending, "reason": "artifacts and verify_cmd passed harness checks",
	}); err != nil {
		d.log.Warn("driver: record review skip failed", "mission_id", m.ID, "error", err)
	}
	in.Input = InputReviewApprove
	return in
}

// harnessFailureReason renders a batch pass's failures into the retry
// event's reason and the stall fingerprint: a regression of an earlier
// unit and the current unit failing are different stalls.
func harnessFailureReason(plan Plan, failing []UnitVerification) (reason, fingerprint string) {
	parts := make([]string, 0, len(failing))
	for _, f := range failing {
		label := fmt.Sprintf("unit %d", f.Unit+1)
		if f.Unit < len(plan.Units) && plan.Units[f.Unit].verified() {
			label = fmt.Sprintf("regression in unit %d %q", f.Unit+1, plan.Units[f.Unit].Title)
			if fingerprint == "" {
				fingerprint = fmt.Sprintf("regression:unit_%d", f.Unit)
			}
		} else if fingerprint == "" {
			fingerprint = fmt.Sprintf("verify_failed:unit_%d", f.Unit)
		}
		parts = append(parts, fmt.Sprintf("%s failed the harness %s check", label, f.Check))
	}
	return truncate(strings.Join(parts, "; "), 500), fingerprint
}

// currentUnit returns the plan's first unit without harness evidence
// (the one the next worker turn works on) and its index, or nil when
// every unit is harness-passed.
func currentUnit(plan Plan) (*PlanUnit, int) {
	if i := firstUnverified(plan); i >= 0 {
		return &plan.Units[i], i
	}
	return nil, -1
}

// reviewUnits returns the indices and units a prove round judges: the
// harness-passed, not yet approved ones (unitsUnderReview), falling
// back to the first unit still pending for rows written before
// HarnessPassed existed.
func reviewUnits(plan Plan) (idx []int, units []PlanUnit) {
	idx = unitsUnderReview(plan.Units)
	if len(idx) == 0 {
		for i, u := range plan.Units {
			if !u.Passes {
				idx = []int{i}
				break
			}
		}
	}
	for _, i := range idx {
		units = append(units, plan.Units[i])
	}
	return idx, units
}

// reviewScope is the union of the units' scope paths (D-095), in
// first-seen order; empty, meaning the whole tree, when no unit
// declares one (legacy plans).
func reviewScope(units []PlanUnit) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range units {
		for _, p := range u.Scope {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// hasCriteria reports whether any unit carries acceptance criteria; a
// plan without them predates D-095 and reviews against the goal.
func hasCriteria(units []PlanUnit) bool {
	for _, u := range units {
		if len(u.Criteria) > 0 {
			return true
		}
	}
	return false
}

// planArtifacts lists every artifact the plan declares.
func planArtifacts(plan Plan) []string {
	var out []string
	for _, u := range plan.Units {
		out = append(out, u.Artifacts...)
	}
	return out
}

// reviewWithShrink runs RunReview, recovering from a prompt rejected
// for exceeding the model's context window (ErrPromptTooLong) instead
// of treating it like any other infra failure: one retry with the
// packet itself shrunk, never a loop. (The pre-D-092 first stage,
// dropping the resumed reviewer session, is gone with that session.)
func (d *Driver) reviewWithShrink(ctx context.Context, m Mission, packet ReviewPacket) (ReviewVerdict, error) {
	verdict, err := d.runner.RunReview(ctx, m, packet)
	if err == nil {
		return verdict, nil
	}
	if !errors.Is(err, ErrPromptTooLong) {
		return ReviewVerdict{}, err
	}

	diffCap, artifactsCap := baselineDiffCap/4, reviewArtifactsCap/2
	trimmed := packet
	if wt := m.WorktreePath(); wt != "" {
		// D-096: a findings-only packet diffs from the last review commit
		// over the finding files and the affected units' scope.
		base, scope := m.BaseCommit, reviewScope(packet.Units)
		if packet.FindingsOnly {
			base, scope = m.Plan.LastReviewCommit, append(findingFiles(packet.OpenFindings), scope...)
		}
		trimmedDiff, diffErr := baselineDiffCapped(ctx, wt, base, scope, diffCap)
		if diffErr != nil {
			return ReviewVerdict{}, diffErr
		}
		trimmed.Diff = trimmedDiff
	}
	if artifacts := reviewArtifacts(packet.Units, packet.Diff != ""); !packet.FindingsOnly && len(artifacts) > 0 {
		trimmed.Artifacts = ReadArtifactsCapped(m.WorkRoot(), artifacts, artifactsCap)
	}
	if evErr := d.store.AppendEvent(ctx, m.ID, "mission.review_prompt_shrunk", map[string]any{
		"action": "trimmed_packet", "diff_cap": diffCap, "artifacts_cap": artifactsCap,
	}); evErr != nil {
		d.log.Warn("driver: record review prompt shrunk failed", "mission_id", m.ID, "error", evErr)
	}
	verdict, err = d.runner.RunReview(ctx, m, trimmed)
	if err != nil {
		if errors.Is(err, ErrPromptTooLong) {
			return ReviewVerdict{}, fmt.Errorf("review_prompt_too_large: %w", err)
		}
		return ReviewVerdict{}, err
	}
	return verdict, nil
}

// fullReviewPacket builds the packet for a round over the whole change
// set (D-095): the reviewed units' criteria in place of the goal
// (legacy plans without criteria keep the goal), the whole-change stat,
// the diff restricted to the units' scope and, per unit, the changed
// files inside its own scope (D-098). The returned files are every path
// the change touches, for the evidence gate.
func (d *Driver) fullReviewPacket(ctx context.Context, m Mission, units []PlanUnit) (ReviewPacket, []string, error) {
	var changedFiles []string
	packet := ReviewPacket{
		Plan: m.Plan, Units: units, Evidence: m.LastEvidence,
		Listing: ListWorkspace(m.WorkRoot()), Progress: m.Progress,
		OpenFindings: OpenFindings(m.ReviewFindings),
	}
	if !hasCriteria(units) {
		packet.Goal = m.Goal
	}
	if wt := m.WorktreePath(); wt != "" {
		var err error
		if packet.Diff, err = BaselineDiff(ctx, wt, m.BaseCommit, reviewScope(units)); err != nil {
			return ReviewPacket{}, nil, err
		}
		if packet.DiffStat, err = DiffStat(ctx, wt, m.BaseCommit); err != nil {
			return ReviewPacket{}, nil, err
		}
		if changedFiles, err = ChangedFiles(ctx, wt, m.BaseCommit); err != nil {
			return ReviewPacket{}, nil, err
		}
		packet.UnitFiles = make([][]string, len(units))
		for i, u := range units {
			packet.UnitFiles[i] = insideScope(changedFiles, u.Scope)
		}
	}
	if artifacts := reviewArtifacts(units, packet.Diff != ""); len(artifacts) > 0 {
		packet.Artifacts = ReadArtifacts(m.WorkRoot(), artifacts)
	}
	return packet, changedFiles, nil
}

// findingsReviewPacket builds the re-review packet (D-096): the open
// findings, the diff since the last review commit restricted to the
// finding files and the affected units' scope, the finding files'
// contents, the affected units' harness state (criteria dropped, the
// findings are the question), the files changed outside every unit's
// scope, and the progress notes written since the last round (D-098:
// the rework turns' own account). The returned files are the delta's
// paths plus the finding files, for the evidence gate.
func (d *Driver) findingsReviewPacket(ctx context.Context, m Mission, open []Finding) (ReviewPacket, []string, error) {
	files := findingFiles(open)
	var affected []PlanUnit
	seen := map[int]bool{}
	for _, f := range open {
		if f.Unit < 0 || f.Unit >= len(m.Plan.Units) || seen[f.Unit] {
			continue
		}
		seen[f.Unit] = true
		u := m.Plan.Units[f.Unit]
		u.Criteria = nil
		affected = append(affected, u)
	}
	packet := ReviewPacket{
		FindingsOnly: true, Units: affected, OpenFindings: open,
		Progress: progressSince(m.Progress, m.Plan.LastReviewAt),
	}
	wt := m.WorktreePath()
	paths := append(append([]string{}, files...), reviewScope(affected)...)
	var err error
	if packet.Diff, err = BaselineDiff(ctx, wt, m.Plan.LastReviewCommit, paths); err != nil {
		return ReviewPacket{}, nil, err
	}
	changed, err := ChangedFiles(ctx, wt, m.Plan.LastReviewCommit)
	if err != nil {
		return ReviewPacket{}, nil, err
	}
	packet.ScopeCreep = outsideScope(changed, reviewScope(m.Plan.Units))
	if len(files) > 0 {
		packet.Files = ReadArtifactsCapped(m.WorkRoot(), files, len(files)*reviewArtifactFileCap)
	}
	return packet, append(changed, files...), nil
}

func (d *Driver) runReview(ctx context.Context, m Mission) (StepInput, error) {
	// D-097: the review token ceiling is checked from the ledger before
	// any model call; at or above it the mission parks on budget.
	if d.reviewTokenCeiling != nil {
		if ceiling := d.reviewTokenCeiling(ctx); ceiling > 0 {
			used, err := d.store.ReviewInputTokens(ctx, m.ID)
			if err != nil {
				return StepInput{}, err
			}
			if reviewTokensExceeded(used, ceiling) {
				return StepInput{Input: InputReviewBudget, Reason: fmt.Sprintf("review input tokens %d reached the ceiling %d", used, ceiling)}, nil
			}
		}
	}
	idx, units := reviewUnits(m.Plan)
	unitIdx := -1
	if len(idx) > 0 {
		unitIdx = idx[0]
	}
	// D-096: a round after one that left findings open reviews only
	// those findings against the diff since it; missions without a
	// recorded review commit (or no worktree) get the full packet.
	var (
		packet       ReviewPacket
		changedFiles []string
		err          error
	)
	if open := OpenFindings(m.ReviewFindings); len(open) > 0 && m.Plan.LastReviewCommit != "" && m.WorktreePath() != "" {
		packet, changedFiles, err = d.findingsReviewPacket(ctx, m, open)
	} else {
		packet, changedFiles, err = d.fullReviewPacket(ctx, m, units)
	}
	if err != nil {
		return StepInput{}, err
	}
	// D-097: size the packet to the resolved model's window before the
	// first send; reviewWithShrink's context_length retry stays behind it.
	window := 0
	if d.reviewWindow != nil {
		window = d.reviewWindow(ctx, reviewRoute(m), reviewModel(m))
	}
	var shrink reviewShrink
	if packet, shrink = fitReviewPacket(packet, reviewByteBudget(window)); shrink.cut() {
		if evErr := d.store.AppendEvent(ctx, m.ID, "mission.review_prompt_shrunk", map[string]any{
			"action": "byte_budget", "window": window, "budget": shrink.Budget, "rendered": shrink.Rendered,
			"diff_cut": shrink.DiffCut, "artifacts_cut": shrink.ArtifactsCut,
		}); evErr != nil {
			d.log.Warn("driver: record review prompt shrunk failed", "mission_id", m.ID, "error", evErr)
		}
	}
	verdict, err := d.reviewWithShrink(ctx, m, packet)
	if err != nil {
		return StepInput{}, err
	}
	// D-095 evidence gate: a blocking finding must name a changed or
	// declared file and quote evidence, or it is demoted to minor here,
	// deterministically. A rework whose findings all end up minor, with
	// no prior blocking finding left unresolved (D-096: a re-review that
	// resolves every open blocking finding), counts as approval.
	if !verdict.Approved {
		gated, demoted := gateFindings(verdict.Findings, append(changedFiles, planArtifacts(m.Plan)...))
		for _, dm := range demoted {
			if err := d.store.AppendEvent(ctx, m.ID, "mission.finding_demoted", map[string]any{
				"title": dm.Finding.Title, "file": dm.Finding.File, "reason": dm.Reason,
			}); err != nil {
				d.log.Warn("driver: record finding demoted failed", "mission_id", m.ID, "error", err)
			}
		}
		verdict.Findings = gated
		if !blockingRemain(gated, packet.OpenFindings, verdict.Resolved) {
			verdict.Approved = true
		}
	}
	// The reviewer's own worktree side effects (it may run tests) are
	// rolled back unconditionally after every review round. The HEAD
	// left behind anchors the next round's findings-only diff (D-096);
	// the round's time anchors that packet's progress notes (D-098).
	reviewAt := time.Now().UTC()
	reviewCommit := ""
	if wt := m.WorktreePath(); wt != "" {
		if err := d.workspace.Rollback(ctx, wt, m.Kind); err != nil {
			d.log.Warn("driver: post-review rollback failed", "mission_id", m.ID, "error", err)
		}
		reviewCommit = worktreeHead(ctx, wt)
	}

	// Every verdict is recorded, approvals included — a review that
	// leaves no event is indistinguishable from a review that never
	// ran (the coding canary asserts on exactly this).
	decision := "rework"
	if verdict.Approved {
		decision = "approved"
	}
	if err := d.store.AppendEvent(ctx, m.ID, "mission.review_verdict", map[string]any{
		"decision": decision, "findings": verdict.Findings, "resolved": verdict.Resolved, "findings_only": packet.FindingsOnly,
	}); err != nil {
		d.log.Warn("driver: record review verdict failed", "mission_id", m.ID, "error", err)
	}

	if verdict.Approved {
		// The approval only lands on harness evidence: the batch pass
		// runs again (no citations: the worker turn's seenURLs are gone)
		// and a unit failing it, reviewed or regressed, routes through the
		// SAME rework path a reviewer's own rejection takes, as a
		// harness-authored blocking finding (D-092): back to generate with
		// the failure in the worker's packet, counted against the rework
		// ceiling, and deduplicated by title so the same failing verify
		// never opens a second finding.
		verified, err := d.verify.verifyAll(ctx, m, nil, false)
		if err != nil {
			return StepInput{Input: InputReviewInfraFailure}, err
		}
		if failing := failedUnits(verified); len(failing) > 0 {
			findings := make([]Finding, 0, len(failing))
			for _, f := range failing {
				findings = append(findings, Finding{
					Title:    fmt.Sprintf("harness %s check failed for unit %d", f.Check, f.Unit+1),
					Detail:   truncate(f.Excerpt, 500),
					Severity: SeverityBlocking,
				})
			}
			return StepInput{
				Input: InputReviewRework, Findings: findings, Unit: failing[0].Unit, Reason: reviewReason(findings),
				Verified: verified, ReviewCommit: reviewCommit, ReviewAt: reviewAt, Provider: verdict.Provider, Model: verdict.Model,
			}, nil
		}
		return StepInput{Input: InputReviewApprove, Verified: verified, Provider: verdict.Provider, Model: verdict.Model}, nil
	}
	return StepInput{
		Input: InputReviewRework, Findings: verdict.Findings, Resolved: verdict.Resolved, Unit: unitIdx,
		Reason:       truncate(reviewReason(verdict.Findings), 500),
		ReviewCommit: reviewCommit, ReviewAt: reviewAt, Provider: verdict.Provider, Model: verdict.Model,
	}, nil
}

// reviewReason flattens findings into one line for event payloads.
func reviewReason(findings []Finding) string {
	titles := make([]string, 0, len(findings))
	for _, f := range findings {
		titles = append(titles, f.Title)
	}
	return strings.Join(titles, "; ")
}

// packet builds the WorkPacket for the current phase/iteration
// directly from mission fields — no unused indirection.
func (d *Driver) packet(ctx context.Context, m Mission) (WorkPacket, error) {
	gitLog := ""
	if wt := m.WorktreePath(); wt != "" {
		gitLog, _ = gitLogSince(ctx, wt, m.BaseCommit)
	}
	var loc *time.Location
	if d.location != nil {
		loc = d.location(ctx)
	}
	p := WorkPacket{
		Goal: m.Goal, Kind: m.Kind, Plan: m.Plan, Progress: m.Progress,
		GitLog: gitLog, Iteration: m.Iteration, PromptOverlay: m.PromptOverlay,
		ExecEnvironmentNote: execEnvironmentNote(loc), ParentContext: m.ParentContext(), ReferencedContext: m.ReferencedContext(), Attachments: m.Attachments(),
		Light: m.RunsPlanless(), Location: loc,
		Findings: m.ReviewFindings, ReworkRound: m.ReworkRounds, MaxRounds: m.MaxIterations,
	}
	// D-090: only flow=discover_generate ever has discover notes AND
	// runs planless at the same time (Light is born in PhaseGenerate,
	// never visits discover); gating on p.Light here is redundant but
	// harmless, m.DiscoverNotes is simply empty for every other case.
	if p.Light {
		p.DiscoverNotes = m.DiscoverNotes
	}
	if d.skillsIndex != nil && m.AgentID != "" {
		p.SkillsIndex = d.skillsIndex(ctx, m.AgentID)
	}
	return p, nil
}

// progressNoteCap bounds a raw-turn-text progress note (D-099).
const progressNoteCap = 2000

// progressNote picks the note a worker turn leaves for the next
// session (D-099, issue #533): the handoff, else a delegated result's
// note, else the raw turn text capped at progressNoteCap.
func progressNote(verdict WorkerVerdict, text string) string {
	switch {
	case verdict.Handoff != "":
		return verdict.Handoff
	case verdict.Note != "":
		return verdict.Note
	}
	return truncate(text, progressNoteCap)
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

// worktreeHead returns the worktree's current HEAD hash, or "" when
// git cannot answer (no commits yet, hung repo): the caller then skips
// touched-file scoring for the turn rather than guessing.
func worktreeHead(ctx context.Context, worktree string) string {
	cctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	out, err := runGit(cctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// touchedFiles lists every path changed since commit, committed or
// not (git diff against the working tree). Returns a non-nil empty
// slice for a turn that changed nothing, and nil only when git itself
// fails, which the state machine reads as "unknown"
// (StepInput.TouchedFiles).
func touchedFiles(ctx context.Context, worktree, commit string) []string {
	cctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	out, err := runGit(cctx, worktree, "diff", "--name-only", commit)
	if err != nil {
		return nil
	}
	files := []string{}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files
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
