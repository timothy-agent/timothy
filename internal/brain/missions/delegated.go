package missions

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/missions/executor"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// ErrExecutorAuth reports that a delegated executor's own auth failed
// (bad/expired credential, "please run /login", etc) — distinct from a
// transport death because retrying the SAME entry is futile; the
// driver pauses the mission as infra instead of burning iterations.
var ErrExecutorAuth = errors.New("executor: authentication failed")

// ErrGatewayUnavailable reports that a delegated worker turn's route
// resolve never got past the gateway itself being unavailable (D-101,
// issue #511) after routeResolveRetries attempts: the gateway was
// unreachable, or still answering 503 config_unavailable because its
// routing snapshot hadn't loaded (e.g. a brain restart's boot recovery
// sweep re-driving a mission before the gateway finished loading). This
// is never a "no delegated route" answer: RunWorker must not fall back
// to native on it, only the driver's infra pause.
var ErrGatewayUnavailable = errors.New("delegated runner: gateway unavailable")

// D-052: the delegated run protocol. A launch is one detached sandboxd
// exec (setsid + nohup-style backgrounding via `&`, pid captured to a
// file) so the CLI keeps running past the 60s ExecEnv call that started
// it; every subsequent interaction is a SEPARATE short poll exec that
// tails whatever the process has written so far. This keeps every
// single sandboxd call under the ExecEnv-friendly ~60s window (no exec
// blocks on the harness's own multi-minute-to-hour runtime) while still
// surviving a brain restart: the run directory and pid file are the
// only state that matters, and a fresh delegatedRunner instance can
// resume polling the same directory after re-reading the manifest event
// (see attemptResume).
const (
	// runBudgetDefault is the CLI wall-clock cap when no settings-backed
	// budget is wired (tests); production reads
	// settings.ValueExecutorRunBudgetMinutes per launch (issue #498) and
	// settings.DefaultExecutorRunBudget carries the same 8h default. A
	// runaway backstop only: idleTimeout is the watchdog that catches a
	// hung CLI, so this must stay large enough never to kill a healthy
	// multi-hour run.
	runBudgetDefault  = 8 * time.Hour
	launchTimeout     = 60 * time.Second
	runBudgetExitCode = 124 // coreutils `timeout` exit status when it stopped the command
	pollInterval      = 10 * time.Second
	pollTimeout       = 60 * time.Second
	idleTimeout       = 10 * time.Minute
	killTimeout       = 60 * time.Second
	tailChunkCap      = 262144 // 256KiB per poll, bounds one exec's output
	cooldownTTL       = 10 * time.Minute
	maxExecutorEvents = 300
	pollInfraRetries  = 3
	pollInfraBackoff  = 5 * time.Second

	// routeResolveRetries/routeResolveBackoff bound RunWorker's retry of
	// a route resolve that fails with ErrGatewayUnavailable (D-101): 5
	// attempts, backoff doubling from 2s (2+4+8+16 = 30s of sleep across
	// 4 waits before the 5th and final attempt) so the total wall clock
	// stays close to the acceptance criterion's "5 attempts over about
	// 30s" without a fixed-interval retry racing a slow gateway boot.
	routeResolveRetries    = 5
	routeResolveBackoffMin = 2 * time.Second
)

// routeResolver is the narrow seam over gwclient.Client.ResolveRoute —
// faked in tests so scenarios never need a real gateway.
type routeResolver func(ctx context.Context, route, harness string) (*gwclient.ResolvedRoute, error)

// credResolver is the narrow seam over a secret store's Resolve — faked
// in tests. The literal ref "subscription" never reaches this: it's
// handled by delegatedRunner directly (AuthSubscription, no resolution).
type credResolver func(ctx context.Context, ref string) (string, error)

// usageRecorder is the narrow seam over *ledger.Ledger — faked in
// tests so a ledger row's shape can be asserted without Postgres.
type usageRecorder interface {
	Record(ctx context.Context, e ledger.Entry)
}

// eventSink is the narrow seam over *Store's generic event append —
// AppendEvent's {kind, payload} shape already fits every executor.*
// event this runner needs, so no new Store surface is required beyond
// lastRunState's read side (see runState below).
type eventSink interface {
	AppendEvent(ctx context.Context, missionID, kind string, payload map[string]any) error
}

// runState is what attemptResume needs to decide whether an unfinished
// run exists and, if so, where to resume polling from. Backed by
// Store.LastRunState (store.go) — a narrow read over mission_events
// scanning back from the latest executor.spawned for any terminal
// event or progress marker after it.
type runState struct {
	Harness    string
	AuthMode   executor.AuthMode
	RunID      string
	RunDir     string
	ByteOffset int64
	// Finished is true when an executor.result or executor.died event
	// was recorded after the spawn — the run already reached a verdict
	// (or died) in a prior process lifetime; RunWorker must not resume
	// polling it, only (if genuinely still needed) treat it as already
	// decided.
	Finished bool
	// SessionID is the harness's own CLI session id (issue #499),
	// recorded by the executor.session event once the run's first
	// KindSystem line reported one. Empty when the run died before any
	// system line arrived, or the harness never reports one.
	SessionID string
}

// lastRunState is the narrow seam over Store.LastRunState.
type lastRunStateFunc func(ctx context.Context, missionID string) (*runState, error)

// sandboxExecEnv is the narrow slice of *sandboxclient.Client
// delegatedRunner needs — kept as a function type (not a sandboxclient
// import) so missions keeps no compile-time HTTP dependency, same
// reasoning as the sandboxExec type above. environment (D-05x) only
// matters on the mission's first exec, since a container's image is
// fixed once created.
type sandboxExecEnv func(ctx context.Context, missionID, environment, workdir, command string, env map[string]string, timeout time.Duration, out io.Writer) (exitCode int, err error)

// cooldownKey identifies one chain entry for the in-memory failover
// cooldown.
type cooldownKey struct {
	providerID string
	model      string
	harness    string
}

// delegatedRunner wraps nativeRunner: discover/plan pass through
// untouched; prove passes through unless the mission opted into a
// delegated reviewer (RunReview, issue #582).
// RunWorker dispatches on the mission's own Harness column (D-051
// rework — no longer a per-chain-entry field): m.Harness == "" defers
// straight to native; otherwise it resolves the worker route on the
// executor axis and walks the first usable, non-cooled entry into the
// D-052 run protocol below. Resolve/lookup/cred failures all fail OPEN
// to native.RunWorker — a delegated executor is additive capability,
// never a way for today's native path to break.
type delegatedRunner struct {
	native Runner

	resolveRoute routeResolver
	resolveCred  credResolver
	sandboxExec  sandboxExecEnv
	events       eventSink
	lastRun      lastRunStateFunc
	ledger       usageRecorder

	// progressReader backs mid-run steering delivery to a Steerer
	// adapter's run (issue #358): mirrors nativeRunner.progressReader,
	// unset means the feature is off, same nil-safe contract.
	progressReader ProgressReader

	log *slog.Logger

	// Overridable in tests so scenarios don't wait real minutes.
	pollInterval        time.Duration
	idleTimeout         time.Duration
	runBudget           time.Duration
	routeResolveBackoff time.Duration
	// runBudgetFn, when set, is read once per launch and wins over
	// runBudget: the settings-backed cap (issue #498).
	runBudgetFn func(context.Context) time.Duration

	mu       sync.Mutex
	cooldown map[cooldownKey]time.Time
}

// NewDelegatedRunner wraps native with the delegated-executor dispatch
// path. sandboxExec must be non-nil (cmd/brain/main.go only constructs
// this when a sandbox manager is present — missions already require
// one); events/lastRun/led may be nil in tests that don't assert on
// them, but production wiring always supplies all four. runBudget
// resolves the per-launch wall-clock cap; nil means runBudgetDefault.
func NewDelegatedRunner(native Runner, resolveRoute routeResolver, resolveCred credResolver, sandboxExec sandboxExecEnv, events eventSink, lastRun lastRunStateFunc, led usageRecorder, runBudget func(context.Context) time.Duration, log *slog.Logger) Runner {
	return &delegatedRunner{
		native: native, resolveRoute: resolveRoute, resolveCred: resolveCred,
		sandboxExec: sandboxExec, events: events, lastRun: lastRun, ledger: led, log: log,
		pollInterval: pollInterval, idleTimeout: idleTimeout, runBudget: runBudgetDefault, runBudgetFn: runBudget,
		routeResolveBackoff: routeResolveBackoffMin,
		cooldown:            map[cooldownKey]time.Time{},
	}
}

// SetProgressReader wires the reader pollToVerdict polls for mid-run
// operator notes to deliver to a Steerer adapter's run (issue #358):
// same setter pattern as nativeRunner.SetProgressReader, so
// cmd/brain/main.go can pass the same *Store both runners share.
func (r *delegatedRunner) SetProgressReader(pr ProgressReader) {
	r.progressReader = pr
}

// effectiveRunBudget is the wall-clock cap for a launch happening now.
func (r *delegatedRunner) effectiveRunBudget(ctx context.Context) time.Duration {
	if r.runBudgetFn != nil {
		if d := r.runBudgetFn(ctx); d > 0 {
			return d
		}
	}
	return r.runBudget
}

func (r *delegatedRunner) DiscoverSession(ctx context.Context, m Mission) (string, string, string, error) {
	return r.native.DiscoverSession(ctx, m)
}

func (r *delegatedRunner) PlanSession(ctx context.Context, m Mission, discoverNotes string) (Plan, error) {
	return r.native.PlanSession(ctx, m, discoverNotes)
}

// RunReview dispatches on m.ReviewHarness (issue #582): "" defers
// straight to native.RunReview, byte for byte today's behavior.
// Otherwise the review round runs as a read-only delegated CLI in the
// mission's sandbox and worktree, and ANY failure on that path (unknown
// harness, adapter refusal, unusable route, spawn death, a run without
// a result, an unparseable verdict) records review.delegated_fallback
// {harness, reason} and returns the native reviewer's verdict instead:
// native review is the floor. The driver's own gates (evidence gate,
// demotion, rounds) run on the returned verdict unchanged either way.
func (r *delegatedRunner) RunReview(ctx context.Context, m Mission, packet ReviewPacket) (ReviewVerdict, error) {
	if m.ReviewHarness == "" {
		return r.native.RunReview(ctx, m, packet)
	}
	verdict, reason, err := r.runDelegatedReview(ctx, m, packet)
	if reason == "" {
		return verdict, err
	}
	if ctx.Err() != nil {
		// A cancelled driver has no native round to fall back to.
		return ReviewVerdict{}, ctx.Err()
	}
	payload := map[string]any{"harness": m.ReviewHarness, "reason": reason}
	if err != nil {
		payload["error"] = truncate(err.Error(), 2000)
	}
	r.log.Warn("delegated runner: review harness unusable; falling back to native", "mission_id", m.ID, "harness", m.ReviewHarness, "reason", reason, "error", err)
	r.recordEventForce(ctx, m.ID, "review.delegated_fallback", payload)
	return r.native.RunReview(ctx, m, packet)
}

// Fallback reasons review.delegated_fallback records (issue #582).
const (
	fallbackUnknownHarness    = "unknown_harness"
	fallbackRefused           = "refused"
	fallbackResolveFailed     = "resolve_failed"
	fallbackNoUsableEntry     = "no_usable_entry"
	fallbackCooldown          = "cooldown"
	fallbackAuthFailed        = "auth_failed"
	fallbackSpawnFailed       = "spawn_failed"
	fallbackNoResult          = "no_result"
	fallbackUnparseableResult = "unparseable_verdict"
	fallbackPollFailed        = "poll_failed"
)

// delegatedReviewSystemAppend closes the reviewer's system prompt for a
// CLI run (issue #582), in place of reviewSystemToolCall: the CLI has no
// review_verdict tool, its structured result is the verdict channel,
// and the read-only tool surface the adapter enforces is spelled out so
// the model does not waste turns trying to edit.
const delegatedReviewSystemAppend = " You are running as a delegated read-only CLI reviewer, not through a review_verdict tool. You may read files in the working directory to spot-check; you cannot modify files or run commands, so never try. End your turn by producing the required structured output: decision approve or rework, findings (each with title, file, detail, severity and quoted evidence) and the resolved ids of prior findings the work closed."

// runDelegatedReview resolves the review harness, route and entry, runs
// the review packet through the CLI read-only, and parses its result as
// the review verdict (issue #582). A non-empty reason means the caller
// must fall back to native; err carries the detail for the event.
func (r *delegatedRunner) runDelegatedReview(ctx context.Context, m Mission, packet ReviewPacket) (ReviewVerdict, string, error) {
	adapter, ok := executor.Lookup(m.ReviewHarness)
	if !ok {
		return ReviewVerdict{}, fallbackUnknownHarness, nil
	}
	route, err := r.resolveRouteFor(ctx, m, reviewRoute(m), m.ReviewHarness)
	if err != nil {
		return ReviewVerdict{}, fallbackResolveFailed, err
	}
	if route == nil {
		return ReviewVerdict{}, fallbackResolveFailed, errors.New("resolved route was nil without an error")
	}
	entry, reason := r.pickEntry(route.Entries, m.ReviewHarness, reviewModel(m))
	if reason != "" {
		return ReviewVerdict{}, reason, nil
	}
	workRoot := m.WorkRoot()
	authMode, apiKey, err := r.resolveCredential(ctx, entry.CredentialRef, adapter.Capabilities())
	if err != nil {
		r.coolDown(m.ReviewHarness, entry)
		r.recordAuthFailed(ctx, m.ID, string(PhaseProve), m.ReviewHarness)
		return ReviewVerdict{}, fallbackAuthFailed, err
	}
	run := cliRun{
		phase: string(PhaseProve), harness: m.ReviewHarness, entry: entry, adapter: adapter,
		authMode: authMode, route: reviewRoute(m), agent: reviewerAgent,
	}
	runID, err := newRunID()
	if err != nil {
		return ReviewVerdict{}, fallbackSpawnFailed, err
	}
	rdir := runDir(m.Workspace, "review-"+runID)
	spec := executor.InvocationSpec{
		MissionID: m.ID, Workdir: workRoot,
		PromptPath:   filepath.Join(rdir, "prompt.md"),
		SystemAppend: reviewSystemPrompt + delegatedReviewSystemAppend,
		Model:        entry.Model, AuthMode: authMode, APIKey: apiKey, BaseURL: entry.BaseURL,
		ResultSchema: reviewVerdictSchema, RunBudget: r.effectiveRunBudget(ctx), Wire: entry.Wire,
		// ResumeSessionID stays empty: every review round is a cold
		// session (D-092). ReadOnly is the enforced safety knob.
		ReadOnly: true,
	}
	if err := r.launchRun(ctx, m, run, workRoot, rdir, runID, spec, renderReviewContent(packet), resumeDecision{reason: resumeReasonNoPriorRun}); err != nil {
		if errors.Is(err, executor.ErrReadOnlyUnsupported) {
			return ReviewVerdict{}, fallbackRefused, err
		}
		return ReviewVerdict{}, fallbackSpawnFailed, err
	}

	start := time.Now()
	st, end, exitCode, err := r.pollRun(ctx, m, run, workRoot, rdir, runID, 0)
	if err != nil {
		return ReviewVerdict{}, fallbackPollFailed, err
	}
	switch end {
	case runEndResult:
		return r.finishReview(ctx, m, run, st, start, exitCode)
	case runEndIdle:
		return ReviewVerdict{}, fallbackNoResult, errors.New("the executor produced no output for the idle timeout and was killed")
	default:
		reason, err := r.finishNoResult(ctx, m, run, workRoot, rdir, st, start, exitCode)
		if err != nil {
			return ReviewVerdict{}, fallbackAuthFailed, err
		}
		return ReviewVerdict{}, fallbackNoResult, errors.New(reason)
	}
}

// finishReview maps a review run's result event onto a ReviewVerdict
// (issue #582): the structured result must decode with an explicit
// approve/rework decision, else the round falls back to native rather
// than letting a garbage result read as "rework with no findings" (which
// the driver's gate would count as approval). Result and ledger rows are
// recorded either way, same as a worker run.
func (r *delegatedRunner) finishReview(ctx context.Context, m Mission, run cliRun, st *pollState, start time.Time, exitCode int) (ReviewVerdict, string, error) {
	verdict, decision, perr := parseDelegatedReviewVerdict(st.resultEvent.Result)
	parseKind := "schema"
	if perr != nil {
		parseKind = "none"
	}
	authErr := r.finishCommon(ctx, m, run, st, start, exitCode, parseKind, decision)
	if authErr != nil {
		return ReviewVerdict{}, fallbackAuthFailed, authErr
	}
	if perr != nil {
		return ReviewVerdict{}, fallbackUnparseableResult, perr
	}
	verdict.Provider = run.entry.ProviderName
	verdict.Model = run.entry.Model
	if st.reportedModel != "" {
		verdict.Model = st.reportedModel
	}
	return verdict, "", nil
}

// parseDelegatedReviewVerdict decodes a CLI result payload with
// parseReviewVerdict and additionally requires the decision to be
// literally approve or rework. Returns the upper-cased decision for the
// executor.result event ("UNKNOWN" when it fails).
func parseDelegatedReviewVerdict(raw json.RawMessage) (ReviewVerdict, string, error) {
	if len(raw) == 0 {
		return ReviewVerdict{}, "UNKNOWN", errors.New("executor result carried no structured verdict")
	}
	var probe struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ReviewVerdict{}, "UNKNOWN", fmt.Errorf("decode review verdict: %w", err)
	}
	if probe.Decision != "approve" && probe.Decision != "rework" {
		return ReviewVerdict{}, "UNKNOWN", fmt.Errorf("review verdict decision %q is neither approve nor rework", probe.Decision)
	}
	verdict, err := parseReviewVerdict(raw)
	if err != nil {
		return ReviewVerdict{}, "UNKNOWN", fmt.Errorf("decode review verdict: %w", err)
	}
	return verdict, strings.ToUpper(probe.Decision), nil
}

// pickEntry picks the chain entry a review run uses (issue #582): the
// route_model pin when it names a usable, non-cooled entry, else the
// first usable non-cooled one. A non-empty reason names why none fit.
func (r *delegatedRunner) pickEntry(entries []gwclient.ResolvedRouteEntry, harness, pin string) (gwclient.ResolvedRouteEntry, string) {
	if pin != "" {
		for _, entry := range entries {
			if pin != entry.ProviderName+"/"+entry.Model || !entry.Usable {
				continue
			}
			if _, cooled := r.cooledUntil(harness, entry); !cooled {
				return entry, ""
			}
		}
	}
	cooled := false
	for _, entry := range entries {
		if !entry.Usable {
			continue
		}
		if _, c := r.cooledUntil(harness, entry); c {
			cooled = true
			continue
		}
		return entry, ""
	}
	if cooled {
		return gwclient.ResolvedRouteEntry{}, fallbackCooldown
	}
	return gwclient.ResolvedRouteEntry{}, fallbackNoUsableEntry
}

// RunWorker dispatches on m.Harness (D-051 rework): "" defers straight
// to native.RunWorker (which itself lets the gateway walk any native
// chain). Otherwise it looks up the adapter and resolves the worker
// route on the executor axis, walking the first usable, non-cooled
// entry into the delegated protocol. An unknown harness or no usable
// entry at all falls back to native.RunWorker unchanged: today's
// behavior is always the floor, but a mission that explicitly asked
// for a harness must not fall back silently: an executor.skipped event
// is recorded first (see recordSkipped) so the mission's own history
// shows the requested harness was never actually used. A route resolve
// that fails because the gateway itself is unavailable (D-101, issue
// #511) is different: that is transient infra, not "no delegated
// route", so resolveRouteFor retries it with bounded backoff and,
// if still failing, this returns ErrGatewayUnavailable instead of
// falling back: the driver pauses the mission as infra rather than
// running a native worker turn behind a delegated run that may still be
// alive in the sandbox.
func (r *delegatedRunner) RunWorker(ctx context.Context, m Mission, packet WorkPacket) (WorkerVerdict, string, error) {
	if m.Harness == "" {
		return r.native.RunWorker(ctx, m, packet)
	}
	if !missionPolicyFor(m).canDelegate {
		// D-072: enforces the documented coding-only harness rule
		// in-package — ValidateCreate already rejects a non-coding
		// mission with harness set, so this only fires for a row that
		// predates that check or was inserted around it.
		r.log.Warn("delegated runner: harness not allowed for kind; falling back to native", "mission_id", m.ID, "kind", m.Kind, "harness", m.Harness)
		r.recordSkipped(ctx, m.ID, m.Harness, "harness not allowed for kind", nil)
		return r.native.RunWorker(ctx, m, packet)
	}
	adapter, ok := executor.Lookup(m.Harness)
	if !ok {
		r.log.Warn("delegated runner: unknown harness; falling back to native", "mission_id", m.ID, "harness", m.Harness)
		r.recordSkipped(ctx, m.ID, m.Harness, "unknown_harness", nil)
		return r.native.RunWorker(ctx, m, packet)
	}

	route, err := r.resolveRouteFor(ctx, m, workerRoute(m), m.Harness)
	if err != nil {
		if errors.Is(err, gwclient.ErrGatewayUnavailable) {
			r.log.Error("delegated runner: gateway unavailable after retries; pausing as infra", "mission_id", m.ID, "error", err)
			r.recordSkipped(ctx, m.ID, m.Harness, "gateway_unavailable", map[string]any{"error": truncate(err.Error(), 2000)})
			return WorkerVerdict{}, "", fmt.Errorf("%w: %v", ErrGatewayUnavailable, err)
		}
		r.log.Warn("delegated runner: route resolve failed; falling back to native", "mission_id", m.ID, "error", err)
		r.recordSkipped(ctx, m.ID, m.Harness, "resolve_failed", map[string]any{"error": truncate(err.Error(), 2000)})
		return r.native.RunWorker(ctx, m, packet)
	}
	if route == nil {
		r.recordSkipped(ctx, m.ID, m.Harness, "resolve_failed", map[string]any{"error": "resolved route was nil without an error"})
		return r.native.RunWorker(ctx, m, packet)
	}

	// route_model pin (D-078): prefer the exact chain entry it names over
	// the first-usable walk below. Absent, unusable, or cooled falls
	// through to that walk rather than failing — the pin names an entry
	// in this route's chain, and a chain can drift out from under it
	// after create, so today's fallback behavior stays the floor.
	if pin := workerModel(m); pin != "" {
		for i, entry := range route.Entries {
			if pin != entry.ProviderName+"/"+entry.Model {
				continue
			}
			if !entry.Usable {
				r.recordSkipped(ctx, m.ID, m.Harness, "pin_unusable", map[string]any{
					"pin": pin, "skip_reason": entry.SkipReason,
				})
				break
			}
			if exp, cooled := r.cooledUntil(m.Harness, entry); cooled {
				r.recordSkipped(ctx, m.ID, m.Harness, "pin_cooldown", map[string]any{
					"pin": pin, "until": exp.UTC().Format(time.RFC3339),
				})
				break
			}
			return r.runDelegated(ctx, m, packet, route.Entries[i], adapter)
		}
		if !pinInChain(route.Entries, pin) {
			r.recordSkipped(ctx, m.ID, m.Harness, "pin_absent", map[string]any{"pin": pin})
		}
	}

	var skipReasons []string
	var cooledEntry *gwclient.ResolvedRouteEntry
	var cooledUntil time.Time
	for i, entry := range route.Entries {
		if !entry.Usable {
			skipReasons = append(skipReasons, entry.SkipReason)
			continue
		}
		if exp, cooled := r.cooledUntil(m.Harness, entry); cooled {
			if cooledEntry == nil {
				cooledEntry = &route.Entries[i]
				cooledUntil = exp
			}
			continue
		}
		return r.runDelegated(ctx, m, packet, entry, adapter)
	}

	if cooledEntry != nil {
		r.log.Warn("delegated runner: every usable entry cooled down; falling back to native", "mission_id", m.ID, "harness", m.Harness)
		r.recordSkipped(ctx, m.ID, m.Harness, "cooldown", map[string]any{
			"until": cooledUntil.UTC().Format(time.RFC3339), "provider": cooledEntry.ProviderName, "model": cooledEntry.Model,
		})
	} else {
		r.log.Warn("delegated runner: no usable route entry; falling back to native", "mission_id", m.ID, "harness", m.Harness)
		r.recordSkipped(ctx, m.ID, m.Harness, "no_usable_entry", map[string]any{"skip_reasons": boundStrings(skipReasons, 5)})
	}
	return r.native.RunWorker(ctx, m, packet)
}

// resolveRouteFor resolves route on harness's executor axis,
// retrying with bounded exponential backoff (routeResolveRetries
// attempts, starting at routeResolveBackoffMin) ONLY when the failure is
// gwclient.ErrGatewayUnavailable, the gateway unreachable or still
// answering 503 config_unavailable because its routing snapshot hasn't
// loaded (D-101, issue #511). Any other resolve error (unknown route,
// bad request) returns immediately on the first attempt: those are
// definitive "no delegated route" answers, not transient infra, and
// retrying them would just delay the existing native fallback. Bounded
// and ctx-aware: a cancelled ctx or an exhausted retry budget both
// return the last error seen.
func (r *delegatedRunner) resolveRouteFor(ctx context.Context, m Mission, route, harness string) (*gwclient.ResolvedRoute, error) {
	backoff := r.routeResolveBackoff
	var lastErr error
	for attempt := 1; attempt <= routeResolveRetries; attempt++ {
		resolved, err := r.resolveRoute(ctx, route, harness)
		if err == nil {
			return resolved, nil
		}
		lastErr = err
		if !errors.Is(err, gwclient.ErrGatewayUnavailable) {
			return nil, err
		}
		if attempt == routeResolveRetries {
			break
		}
		r.log.Warn("delegated runner: gateway unavailable resolving route, retrying", "mission_id", m.ID, "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil, lastErr
}

// pinInChain reports whether pin ("provider name/model") names any entry
// in entries, usable or not — distinguishes "pin present but unusable/
// cooled" (already recorded inside the walk above) from "pin absent"
// (recorded here) so recordSkipped never double-reports the same miss.
func pinInChain(entries []gwclient.ResolvedRouteEntry, pin string) bool {
	for _, entry := range entries {
		if pin == entry.ProviderName+"/"+entry.Model {
			return true
		}
	}
	return false
}

// boundStrings caps a []string to at most n elements — skip_reasons is
// operator-facing context, not a field anything keys logic on.
func boundStrings(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// cooledUntil reports whether entry is in its post-failure cooldown
// window, and if so, when it expires — RunWorker's walk needs the
// expiry to report a cooldown reason's "until" field.
func (r *delegatedRunner) cooledUntil(harness string, entry gwclient.ResolvedRouteEntry) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	exp, ok := r.cooldown[cooldownKey{entry.ProviderID, entry.Model, harness}]
	return exp, ok && time.Now().Before(exp)
}

// coolDown marks entry unusable for cooldownTTL — set on transport
// death, spawn failure, or auth failure so the NEXT worker turn walks
// past it instead of retrying the same broken entry immediately.
func (r *delegatedRunner) coolDown(harness string, entry gwclient.ResolvedRouteEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cooldown[cooldownKey{entry.ProviderID, entry.Model, harness}] = time.Now().Add(cooldownTTL)
}

// resolveCredential implements the credential contract: the exact
// literal "subscription" means AuthSubscription with no resolution at
// all; anything else resolves to a secret, then classified against the
// adapter's own OAuthTokenPrefix — a value starting with that prefix is
// a long-lived OAuth token (subscription-billed), anything else is a
// metered API key. Never logs the resolved value.
func (r *delegatedRunner) resolveCredential(ctx context.Context, ref string, caps executor.Capabilities) (executor.AuthMode, string, error) {
	if ref == "subscription" {
		return executor.AuthSubscription, "", nil
	}
	if r.resolveCred == nil {
		return "", "", fmt.Errorf("%w: no credential resolver configured", ErrExecutorAuth)
	}
	key, err := r.resolveCred(ctx, ref)
	if err != nil || key == "" {
		return "", "", fmt.Errorf("%w: %v", ErrExecutorAuth, err)
	}
	if caps.OAuthTokenPrefix != "" && strings.HasPrefix(key, caps.OAuthTokenPrefix) {
		return executor.AuthOAuthToken, key, nil
	}
	return executor.AuthAPIKey, key, nil
}

// runDir builds the per-run scratch directory path as a sibling of the
// worktree, under the mission's own directory (Mission.Workspace) rather
// than inside the git worktree itself — keeps it out of git
// status/mission diff/review and away from any CLI-authored commit.
// Still brain-visible on the same shared /workspace volume the sandbox
// container mounts wholesale, so prompt.md can be written with a plain
// os.WriteFile and the sandbox's shell polls resolve the same absolute
// path.
func runDir(missionRoot, runID string) string {
	return filepath.Join(missionRoot, "runs", runID)
}

// writeInvocationFiles writes each of inv.Files under rdir, creating
// parent directories as needed. Keys are slash-separated paths
// relative to rdir (e.g. "pi-agent/models.json"); values are never
// logged.
func writeInvocationFiles(rdir string, files map[string]string) error {
	for rel, content := range files {
		full := filepath.Join(rdir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// newRunID returns a random 12-hex-character run identifier.
func newRunID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("delegated runner: generate run id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// resultSchema is the structured-output contract every harness-based
// worker turn is asked to honor — DONE/RETRY/BLOCKED plus a note,
// mirroring mission_status's own outcome/analysis contract so the
// result ladder (below) can map either onto the same WorkerVerdict.
// additionalProperties:false is load-bearing: OpenAI's strict
// structured-output validation (codex --output-schema) rejects any
// schema without it.
var resultSchemaJSON = json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["DONE","RETRY","BLOCKED"]},"note":{"type":"string"}},"required":["status","note"],"additionalProperties":false}`)

// delegatedSystemAppend is appended to the packet's own system prompt
// (WorkPacket.Render's SystemAppend) — it tells the harness to end with
// the structured status output instead of a mission_status tool call
// (the harness has no such tool; ResultSchema is how it reports back),
// and that DONE means every acceptance criterion is met even though the
// harness-side verify_cmd/CheckArtifacts runs regardless of what it
// reports.
const delegatedSystemAppend = " You are running as a delegated coding CLI, not through mission_status. End your turn by producing the required structured output with status DONE, RETRY, or BLOCKED and a short note. Only report DONE when every acceptance criterion for the current unit is genuinely met — the harness independently verifies your artifacts and verify_cmd regardless of what you report, so a false DONE only costs a wasted review round, never actually passes. The harness commits the unit's files itself after your turn, so never run git add, commit, reset, stash, or checkout."

// delegatedAllowTools/delegatedDenyTools are the delegated worker's
// static tool surface, passed as the CLI's own allow/deny flags at
// spawn (never interactive). The per-mission sandbox container is the
// real boundary (D-050's rationale), so file editing, search, and
// shell are allowed outright; git push and the web tools are denied to
// match the native worker's surface — pushes stay human, and native
// workers have no web tools either.
var (
	delegatedAllowTools = []string{"Read", "Glob", "Grep", "Edit", "Write", "MultiEdit", "Bash", "TodoWrite"}
	delegatedDenyTools  = []string{"Bash(git push:*)", "WebFetch", "WebSearch"}
)

// cliRun is the per-run context the shared D-052 protocol threads
// through launch, poll, finish and bookkeeping (issue #582): a worker
// run (phase generate) and a review run (phase prove) differ only in
// these fields. Every executor.* event a run records carries phase.
type cliRun struct {
	phase    string
	harness  string
	entry    gwclient.ResolvedRouteEntry
	adapter  executor.Adapter
	authMode executor.AuthMode
	// route/agent label the run's ledger row.
	route, agent string
	// steer allows mid-run operator note delivery (worker runs only).
	steer bool
}

// workerRun builds the cliRun for a worker turn.
func workerRun(m Mission, entry gwclient.ResolvedRouteEntry, adapter executor.Adapter, authMode executor.AuthMode) cliRun {
	return cliRun{
		phase: string(PhaseGenerate), harness: m.Harness, entry: entry, adapter: adapter,
		authMode: authMode, route: workerRoute(m), agent: "mission-worker", steer: true,
	}
}

// runDelegated executes the D-052 protocol for one chain entry: resolve
// credentials, build the invocation, launch detached, poll to
// completion (or resume an in-flight run from a prior process
// lifetime), and map the outcome onto a WorkerVerdict.
func (r *delegatedRunner) runDelegated(ctx context.Context, m Mission, packet WorkPacket, entry gwclient.ResolvedRouteEntry, adapter executor.Adapter) (WorkerVerdict, string, error) {
	workRoot := m.WorkRoot()

	if handled, verdict, text, err := r.attemptResume(ctx, m, workRoot, entry, adapter); handled {
		return verdict, text, err
	}

	authMode, apiKey, err := r.resolveCredential(ctx, entry.CredentialRef, adapter.Capabilities())
	if err != nil {
		r.coolDown(m.Harness, entry)
		r.recordAuthFailed(ctx, m.ID, string(PhaseGenerate), m.Harness)
		return WorkerVerdict{}, "", err
	}
	run := workerRun(m, entry, adapter, authMode)

	runID, err := newRunID()
	if err != nil {
		r.coolDown(m.Harness, entry)
		return WorkerVerdict{}, "", err
	}
	rdir := runDir(m.Workspace, runID)
	system, user := packet.RenderForDelegated()
	system += delegatedSystemAppend

	decision := r.planSessionResume(ctx, m, workRoot, adapter)

	spec := executor.InvocationSpec{
		MissionID: m.ID, Workdir: workRoot,
		PromptPath:   filepath.Join(rdir, "prompt.md"),
		SystemAppend: system,
		Model:        entry.Model, AuthMode: authMode, APIKey: apiKey, BaseURL: entry.BaseURL,
		AllowTools: delegatedAllowTools, DenyTools: delegatedDenyTools,
		ResultSchema: resultSchemaJSON, RunBudget: r.effectiveRunBudget(ctx), Wire: entry.Wire,
		ResumeSessionID: decision.sessionID,
	}
	if err := r.launchRun(ctx, m, run, workRoot, rdir, runID, spec, user, decision); err != nil {
		return WorkerVerdict{}, "", err
	}

	return r.pollToVerdict(ctx, m, run, workRoot, rdir, runID, 0)
}

// launchRun builds the invocation, writes prompt.md (plus a Steerer's
// prompt.jsonl/steer.jsonl), records executor.spawned and starts the
// CLI detached. Shared by worker and review runs (issue #582); every
// failure cools the entry down before returning.
func (r *delegatedRunner) launchRun(ctx context.Context, m Mission, run cliRun, workRoot, rdir, runID string, spec executor.InvocationSpec, user string, decision resumeDecision) error {
	inv, err := run.adapter.BuildInvocation(spec)
	if err != nil {
		r.coolDown(run.harness, run.entry)
		return fmt.Errorf("delegated runner: build invocation: %w", err)
	}

	// 0750/0600 suffice: brain and the sandbox CLI share uid 65534 on
	// the workspace volume, so owner permissions cover both readers.
	if err := os.MkdirAll(rdir, 0o750); err != nil {
		r.coolDown(run.harness, run.entry)
		return fmt.Errorf("delegated runner: create run dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rdir, "prompt.md"), []byte(user), 0o600); err != nil {
		r.coolDown(run.harness, run.entry)
		return fmt.Errorf("delegated runner: write prompt: %w", err)
	}
	if err := writeInvocationFiles(rdir, inv.Files); err != nil {
		r.coolDown(run.harness, run.entry)
		return fmt.Errorf("delegated runner: write invocation files: %w", err)
	}
	// issue #358: a Steerer adapter (pi) takes its prompt and any
	// mid-run steering on stdin rather than argv - prompt.jsonl seeds
	// the run's stdin, steer.jsonl starts empty and pollRun appends a
	// line to it for every fresh operator note while the run is alive
	// (see buildLaunchCmd's stdin mode). A review run uses the same
	// stdin shape (pi's rpc mode takes the prompt there) but never
	// appends steering.
	steerer, isSteerer := run.adapter.(executor.Steerer)
	if isSteerer {
		if err := os.WriteFile(filepath.Join(rdir, "prompt.jsonl"), []byte(steerer.PromptCommand(user)+"\n"), 0o600); err != nil {
			r.coolDown(run.harness, run.entry)
			return fmt.Errorf("delegated runner: write prompt.jsonl: %w", err)
		}
		if err := os.WriteFile(filepath.Join(rdir, "steer.jsonl"), nil, 0o600); err != nil {
			r.coolDown(run.harness, run.entry)
			return fmt.Errorf("delegated runner: create steer.jsonl: %w", err)
		}
	}

	r.recordSpawned(ctx, m.ID, run, runID, rdir, decision)

	if err := r.launch(ctx, m.ID, m.Environment, workRoot, rdir, inv, spec.RunBudget, isSteerer); err != nil {
		r.coolDown(run.harness, run.entry)
		r.recordDied(ctx, m.ID, run.phase, "spawn_failed", nil, err.Error())
		return fmt.Errorf("delegated runner: launch: %w", err)
	}
	return nil
}

// attemptResume checks for an unfinished run recorded by a prior
// process lifetime (a brain restart mid-run) and, if the run directory
// still looks alive in the container, resumes polling it from the
// stored byte offset instead of spawning a new one. handled=false means
// there was no unfinished run to resume — the caller proceeds to spawn
// a fresh one; handled=true means this call already produced the final
// verdict/text/err (either a genuine resume, or a "lost_run" forced
// retry when the container/process was gone).
func (r *delegatedRunner) attemptResume(ctx context.Context, m Mission, workRoot string, entry gwclient.ResolvedRouteEntry, adapter executor.Adapter) (handled bool, verdict WorkerVerdict, text string, err error) {
	if r.lastRun == nil {
		return false, WorkerVerdict{}, "", nil
	}
	state, lerr := r.lastRun(ctx, m.ID)
	if lerr != nil || state == nil || state.Finished || state.Harness != m.Harness {
		return false, WorkerVerdict{}, "", nil
	}

	// Probe: is the run directory still present/alive in the container?
	// A cheap existence check via the poll command itself — if pid and
	// exit_code are both gone, the container/process was lost (a fresh
	// sandbox after a restart, or the container itself was recycled).
	var probe bytes.Buffer
	probeCmd := fmt.Sprintf("cd %s && { [ -f exit_code ] || [ -f pid ]; }", shQuote(state.RunDir))
	code, perr := r.sandboxExec(ctx, m.ID, m.Environment, workRoot, probeCmd, nil, launchTimeout, &probe)
	if perr != nil || code != 0 {
		r.recordDied(ctx, m.ID, string(PhaseGenerate), "lost_run", nil, "run directory or pid missing after restart")
		return true, forcedRetryVerdict("the executor's run was lost across a restart"), "", nil
	}

	v, t, rerr := r.pollToVerdict(ctx, m, workerRun(m, entry, adapter, state.AuthMode), workRoot, state.RunDir, state.RunID, state.ByteOffset)
	return true, v, t, rerr
}

// resumeReason* name why a retry started fresh instead of resuming the
// prior CLI session (D-103, issue #499): recorded on executor.spawned
// only when resumed is false, so a mission's own history shows which
// gate stopped it.
const (
	resumeReasonNoPriorRun         = "no_prior_run"
	resumeReasonNoSessionID        = "no_session_id"
	resumeReasonAdapterUnsupported = "adapter_unsupported"
	resumeReasonContainerRecreated = "container_recreated"
)

// resumeDecision is what planSessionResume works out before a fresh
// spawn: whether to relaunch through the adapter's resume path with a
// stored session id, and if not, why.
type resumeDecision struct {
	resume    bool
	sessionID string
	reason    string // set only when resume is false
}

// planSessionResume decides whether this launch should resume the prior
// CLI session instead of starting fresh (D-103, issue #499). Called
// only after attemptResume has already ruled out a still-pollable live
// run (handled=false there means the prior run, if any, already reached
// a terminal event or never existed), so a non-nil, Finished state
// here means the prior run genuinely ended (executor.died, most often)
// and this is a retry. Gates, in order: a prior run must exist for this
// harness; it must have recorded a session id; the adapter must
// support resume; and the SAME sandbox container (never recreated
// since that run's launch) must still be alive.
func (r *delegatedRunner) planSessionResume(ctx context.Context, m Mission, workRoot string, adapter executor.Adapter) resumeDecision {
	if r.lastRun == nil {
		return resumeDecision{reason: resumeReasonNoPriorRun}
	}
	state, err := r.lastRun(ctx, m.ID)
	if err != nil || state == nil || state.Harness != m.Harness {
		return resumeDecision{reason: resumeReasonNoPriorRun}
	}
	if state.SessionID == "" {
		return resumeDecision{reason: resumeReasonNoSessionID}
	}
	if !adapter.Capabilities().SupportsResume {
		return resumeDecision{reason: resumeReasonAdapterUnsupported}
	}
	if !r.probeContainerMarker(ctx, m.ID, m.Environment, workRoot) {
		return resumeDecision{reason: resumeReasonContainerRecreated}
	}
	return resumeDecision{resume: true, sessionID: state.SessionID}
}

// launch starts the CLI detached: setsid backgrounds it so it survives
// the ExecEnv call returning, pid captured to a file, stdout/stderr
// redirected into the run directory. The `timeout` wrapper (D-052)
// enforces spec.RunBudget itself, independent of anything brain does
// afterward — a crashed brain still lets the container kill a runaway
// CLI. stdinMode is true for a Steerer adapter (issue #358): the CLI's
// stdin stays open for the run's life via `tail -f steer.jsonl` so a
// later mid-run steer command can still reach it.
func (r *delegatedRunner) launch(ctx context.Context, missionID, environment, workdir, rdir string, inv executor.Invocation, runBudget time.Duration, stdinMode bool) error {
	launchCmd, err := buildLaunchCmd(workdir, rdir, inv, runBudget, stdinMode)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	code, err := r.sandboxExec(ctx, missionID, environment, workdir, launchCmd, inv.Env, launchTimeout, &out)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("launch command exited %d: %s", code, out.String())
	}
	return nil
}

// containerMarkerFile names a marker written directly under $HOME (D-103,
// issue #499): not under /workspace (the shared workspace volume, which
// survives a container being recreated from scratch) and not under
// executorStateMountPath's .claude subtree (its own separate persistent
// volume): $HOME itself, outside that subtree, lives on the container's
// own writable layer, so the marker exists only for as long as THIS
// container instance does. Gone after a real recreate (removed + created
// fresh), present after a mere stop/restart in place (sandboxd's
// ensureContainer reuses the container by name unless it was actually
// removed). Read via $HOME rather than a hardcoded container path so the
// same command also runs against a real /bin/sh in the composed-command
// round-trip test, where $HOME is the test host's own home directory.
const containerMarkerFile = ".timothy-container-marker"

// buildLaunchCmd composes the full detached-launch command. The inner
// script is quoted as ONE unit via shQuote — composing it with literal
// quotes would let the argv elements' own single quotes toggle the
// outer quoting and word-split any argument containing spaces (the
// system-prompt append, most obviously). stdinMode (issue #358) feeds
// the CLI's stdin from prompt.jsonl followed by `tail -f steer.jsonl`
// instead of the plain argv-only invocation: `tail -f` keeps stdin open
// for the run's life, so a later append to steer.jsonl (pollToVerdict's
// steering injection) still reaches the running process; killRun's
// existing TERM/KILL path ends the process (and its stdin pipe) exactly
// as it does today.
func buildLaunchCmd(workdir, rdir string, inv executor.Invocation, runBudget time.Duration, stdinMode bool) (string, error) {
	argv, err := renderArgv(inv)
	if err != nil {
		return "", err
	}
	var inner string
	if stdinMode {
		// The feeder (prompt line, then tail -f steer.jsonl) writes into a
		// fifo the CLI reads as stdin; its pid lands in stdin.pid so
		// closeStdin can end the feed once the run's result has arrived,
		// which gives the CLI EOF and lets it exit on its own. exec keeps
		// tail's pid equal to the recorded one.
		inner = fmt.Sprintf(
			"rm -f %[1]s/stdin.fifo && mkfifo %[1]s/stdin.fifo && { ( cat %[1]s/prompt.jsonl; exec tail -f %[1]s/steer.jsonl ) > %[1]s/stdin.fifo & echo $! > %[1]s/stdin.pid; }; timeout -k 30 %[2]d %[3]s < %[1]s/stdin.fifo > %[1]s/run.ndjson 2> %[1]s/stderr.log; echo $? > %[1]s/exit_code; kill \"$(cat %[1]s/stdin.pid)\" 2>/dev/null",
			shQuote(rdir), int(runBudget/time.Second), argv,
		)
	} else {
		inner = fmt.Sprintf(
			"timeout -k 30 %d %s > %s/run.ndjson 2> %s/stderr.log; echo $? > %s/exit_code",
			int(runBudget/time.Second), argv, shQuote(rdir), shQuote(rdir), shQuote(rdir),
		)
	}
	// Braces bound the `&` to the setsid job alone — a bare `cd && mkdir
	// && setsid ... & echo $!` backgrounds the whole chain and races the
	// pid write against the mkdir it depends on. The marker write runs
	// synchronously before backgrounding, so it's in place before this
	// exec even returns.
	return fmt.Sprintf(
		"cd %s && mkdir -p %s && echo ok > \"$HOME/%s\" && { setsid sh -c %s > /dev/null 2>&1 & echo $! > %s/pid; }",
		shQuote(workdir), shQuote(rdir), containerMarkerFile, shQuote(inner), shQuote(rdir),
	), nil
}

// probeContainerMarker reports whether $HOME/containerMarkerFile still
// exists in missionID's sandbox container: true means the same
// container instance that wrote it at the died run's launch is still
// around (D-103, issue #499's "same sandbox container is still alive"
// test); false means it was recreated from scratch (or the probe itself
// failed, treated the same as recreated: never guess a resume is safe).
func (r *delegatedRunner) probeContainerMarker(ctx context.Context, missionID, environment, workRoot string) bool {
	var out bytes.Buffer
	cmd := fmt.Sprintf("[ -f \"$HOME/%s\" ]", containerMarkerFile)
	code, err := r.sandboxExec(ctx, missionID, environment, workRoot, cmd, nil, launchTimeout, &out)
	return err == nil && code == 0
}

// renderArgv substitutes the adapter's "@PROMPT@" placeholder with
// `$(cat '<promptfile>')` and shell-quotes every other argv element —
// adapters stay shell-free themselves (executor.Invocation.PromptFile
// is a plain path), so the runner is the one and only place that
// touches shell syntax.
func renderArgv(inv executor.Invocation) (string, error) {
	if inv.PromptFile == "" {
		return "", fmt.Errorf("delegated runner: invocation has no prompt file")
	}
	parts := make([]string, 0, len(inv.Argv))
	for _, a := range inv.Argv {
		if a == "@PROMPT@" {
			parts = append(parts, "\"$(cat "+shQuote(inv.PromptFile)+")\"")
			continue
		}
		parts = append(parts, shQuote(a))
	}
	return strings.Join(parts, " "), nil
}

// shQuote wraps s in single quotes for /bin/sh, escaping any embedded
// single quote as '\” (close quote, escaped literal quote, reopen
// quote) — the standard POSIX shell quoting idiom.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pollLoop's accumulated per-run state, carried across poll iterations
// (and, on resume, seeded from the stored manifest).
type pollState struct {
	offset       int64
	carry        []byte // partial trailing line from the last chunk
	lastProgress time.Time
	lastByteMove time.Time
	turns        int
	toolCalls    int
	sawResult    bool
	resultEvent  executor.Event
	// stdinClosed marks that closeStdin already ended a Steerer run's
	// stdin feed after its result arrived (issue #358).
	stdinClosed bool
	// reportedModel is the model the harness itself said it ran, from
	// its KindSystem init line. Preferred over the route entry's model
	// when recording usage: a self-paired harness's provider row carries
	// a placeholder (cursor-cli's "default"), so entry.Model would book
	// real tokens against a model name that never ran.
	reportedModel string
	textBuf       strings.Builder
	eventCount    int
	infraRetries  int
	// worktree is the latest non-nil WT: summary seen across polls
	// (issue #500); nil until the first successful git status.
	worktree *WorktreeSummary
	// sessionID/sessionRecorded track the harness's own CLI session id
	// (D-103, issue #499), from the run's KindSystem event.
	// sessionRecorded guards recordSessionSeen against writing the
	// executor.session event more than once per run.
	sessionID       string
	sessionRecorded bool
}

// pollToVerdict runs pollRun for a worker turn and maps how the run
// ended onto a WorkerVerdict: the result ladder (finish), a forced
// retry for an idle kill, or finishNoResult's transport-death handling.
func (r *delegatedRunner) pollToVerdict(ctx context.Context, m Mission, run cliRun, workRoot, rdir, runID string, startOffset int64) (WorkerVerdict, string, error) {
	start := time.Now()
	st, end, exitCode, err := r.pollRun(ctx, m, run, workRoot, rdir, runID, startOffset)
	if err != nil {
		return WorkerVerdict{}, st.textBuf.String(), err
	}
	switch end {
	case runEndResult:
		return r.finish(ctx, m, run, st, start, exitCode)
	case runEndIdle:
		return forcedRetryVerdict("the executor produced no output for the idle timeout and was killed"), st.textBuf.String(), nil
	default:
		reason, err := r.finishNoResult(ctx, m, run, workRoot, rdir, st, start, exitCode)
		if err != nil {
			return WorkerVerdict{}, st.textBuf.String(), err
		}
		return forcedRetryVerdict(reason), st.textBuf.String(), nil
	}
}

// runEnd names how pollRun saw a run end.
type runEnd int

const (
	runEndResult   runEnd = iota // result event seen and the process exited
	runEndNoResult               // process exited or was lost with no result event
	runEndIdle                   // idle watchdog killed it
)

// pollRun polls rdir until the run terminates (exit_code present and
// run.ndjson fully drained), the idle watchdog fires, ctx is cancelled,
// or repeated infra errors declare the sandbox unreachable. startOffset
// seeds the byte offset on a resumed run; 0 for a fresh spawn. The
// returned pollState is never nil, so callers can read the accumulated
// text even on error. exitCode is -1 when the process was lost.
func (r *delegatedRunner) pollRun(ctx context.Context, m Mission, run cliRun, workRoot, rdir, runID string, startOffset int64) (*pollState, runEnd, int, error) {
	parser := run.adapter.NewParser()
	st := &pollState{offset: startOffset, lastByteMove: time.Now(), lastProgress: time.Now()}

	// Mid-run steering (issue #358) applies only to a Steerer adapter
	// (pi's rpc mode) on a worker run with a progress reader wired;
	// steerer stays nil otherwise and the injection below is skipped.
	// The stdin feed itself (stdinFeed) exists for every Steerer run,
	// review runs included, and is closed once the result arrives. The
	// watermark starts past the operator notes already rendered into
	// this turn's packet (same seed as nativeRunner.steeringFor), so a
	// note from an earlier turn is never redelivered as fresh steering.
	_, stdinFeed := run.adapter.(executor.Steerer)
	var steerer executor.Steerer
	if s, ok := run.adapter.(executor.Steerer); ok && run.steer && r.progressReader != nil {
		steerer = s
	}
	steerWatermark := countOperatorNotes(m.Progress)

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.killRun(context.WithoutCancel(ctx), m.ID, m.Environment, workRoot, rdir)
			r.recordDied(context.WithoutCancel(ctx), m.ID, run.phase, "ctx_cancelled", nil, ctx.Err().Error())
			r.coolDown(run.harness, run.entry)
			return st, runEndNoResult, -1, ctx.Err()
		case <-ticker.C:
		}

		chunk, exitCode, hasExit, alive, worktree, err := r.pollOnce(ctx, m.ID, m.Environment, workRoot, rdir, runID, st.offset)
		if err != nil {
			st.infraRetries++
			if st.infraRetries >= pollInfraRetries {
				return st, runEndNoResult, -1, fmt.Errorf("delegated runner: sandbox unreachable after %d poll retries: %w", pollInfraRetries, err)
			}
			time.Sleep(pollInfraBackoff)
			continue
		}
		st.infraRetries = 0
		if worktree != nil {
			st.worktree = worktree
		}

		// Steer injection runs BEFORE this poll's own terminal checks: a
		// run already alive (before we know from this same poll whether
		// it just finished) and never having produced a result yet is the
		// only safe window - pi exits 0 on stdin EOF, and a steer command
		// arriving after agent_end starts a whole NEW agent run instead of
		// queuing onto the current one (verified against pi 0.84.1),
		// so injection must stop the instant a terminal event was parsed.
		if steerer != nil && alive && !st.sawResult {
			steerWatermark = r.injectSteering(ctx, m, workRoot, rdir, steerer, steerWatermark)
		}

		if len(chunk) > 0 {
			st.offset += int64(len(chunk))
			st.lastByteMove = time.Now()
			r.feedLines(ctx, parser, chunk, st, m.ID, run.phase, runID)
			r.recordProgressThrottled(ctx, m.ID, run.phase, runID, st)
		}

		// A Steerer run never exits on its own: its stdin feed holds the
		// CLI open (issue #358). Once the result has arrived, end the feed
		// so the CLI sees EOF, exits, and the next poll finishes normally.
		if stdinFeed && st.sawResult && alive && !hasExit && !st.stdinClosed {
			r.closeStdin(ctx, m, workRoot, rdir)
			st.stdinClosed = true
		}

		if st.sawResult && hasExit {
			return st, runEndResult, exitCode, nil
		}
		if hasExit && !alive {
			// Process exited; run.ndjson fully drained (no new bytes this
			// poll) but no result event ever arrived: transport death.
			return st, runEndNoResult, exitCode, nil
		}
		if !alive && !hasExit {
			// pid gone, no exit_code file: process died without even
			// writing its own exit status, still transport death.
			return st, runEndNoResult, -1, nil
		}

		if time.Since(st.lastByteMove) > r.idleTimeout {
			r.killRun(ctx, m.ID, m.Environment, workRoot, rdir)
			r.recordEvent(ctx, m.ID, st, "executor.idle_killed", map[string]any{"idle_s": int(r.idleTimeout.Seconds()), "phase": run.phase})
			r.coolDown(run.harness, run.entry)
			return st, runEndIdle, -1, nil
		}
	}
}

// closeStdin ends a Steerer run's stdin feed by killing the recorded
// feeder pid (issue #358): the fifo's write side closes, the CLI reads
// EOF and exits, and the run finishes through the regular exit path.
// Best effort: a failure logs and leaves the idle timeout as the
// fallback.
func (r *delegatedRunner) closeStdin(ctx context.Context, m Mission, workRoot, rdir string) {
	cmd := fmt.Sprintf("kill \"$(cat %s/stdin.pid)\" 2>/dev/null", shQuote(rdir))
	var out bytes.Buffer
	if code, err := r.sandboxExec(ctx, m.ID, m.Environment, workRoot, cmd, nil, launchTimeout, &out); err != nil || code != 0 {
		r.log.Warn("delegated runner: close stdin failed", "mission_id", m.ID, "error", err, "exit_code", code)
	}
}

// injectSteering delivers every fresh operator note (past watermark) to
// a Steerer adapter's in-flight run by appending one SteerCommand line
// per note to rdir/steer.jsonl, and records an executor.steered event
// for each (issue #358). Returns the advanced watermark; on a failed
// poll or append, returns watermark unchanged (never advanced past what
// was actually delivered) and logs a warning - steering is a best-
// effort mid-run nicety, same stance as nativeRunner.steeringFor, so a
// failure here never fails the run.
func (r *delegatedRunner) injectSteering(ctx context.Context, m Mission, workRoot, rdir string, steerer executor.Steerer, watermark int) int {
	notes, err := r.progressReader.Progress(ctx, m.ID)
	if err != nil {
		r.log.Warn("delegated runner: poll steering notes failed", "mission_id", m.ID, "error", err)
		return watermark
	}
	fresh, newWatermark := freshOperatorNotes(notes, watermark)
	delivered := watermark
	for _, note := range fresh {
		line := steerer.SteerCommand("Operator steering note (mid-run): " + note)
		cmd := fmt.Sprintf("printf '%%s\\n' %s >> %s/steer.jsonl", shQuote(line), shQuote(rdir))
		var out bytes.Buffer
		if code, err := r.sandboxExec(ctx, m.ID, m.Environment, workRoot, cmd, nil, launchTimeout, &out); err != nil || code != 0 {
			// Only the notes actually appended so far count toward the
			// watermark - a note that failed to append must be retried on
			// the next poll, never skipped.
			r.log.Warn("delegated runner: steer append failed", "mission_id", m.ID, "error", err, "exit_code", code)
			return delivered
		}
		delivered++
		r.recordEventForce(ctx, m.ID, "executor.steered", map[string]any{"note": note, "harness": m.Harness, "phase": string(PhaseGenerate)})
	}
	return newWatermark
}

// pollBoundaryPrefix + runID makes the boundary marker printf'd between
// tail output and the EXITCODE:/ALIVE status lines collision-proof: it
// embeds the run's own random id, which no NDJSON line the CLI emits
// could ever contain by chance.
const pollBoundaryPrefix = "---TIMOTHY-RUN-"

// buildPollCmd composes one poll exec's command and the boundary marker
// it emits between tail content and status lines. The boundary rides as
// a printf ARGUMENT ('%s\n' format), never as the format itself: dash's
// printf builtin rejects a format string with a leading dash as an
// illegal option. workdir is the exec's cwd (the mission worktree for
// coding missions, see Mission.WorkRoot); the WT: line runs `git
// status --porcelain` against it directly, since the rest of the
// command `cd`s into rdir (a sibling run-log dir, not the worktree).
// Everything past the ALIVE check is best-effort (issue #500): a
// missing/non-git workdir emits no WT: line rather than failing the
// poll.
// wtStatusLines bounds how many git status entries the WT: summary
// inspects per poll.
const wtStatusLines = 200

func buildPollCmd(workdir, rdir, runID string, offset int64) (cmd, boundary string) {
	boundary = pollBoundaryPrefix + runID + "---"
	// wtCmd checks git status's own exit code first: a git failure (not
	// a repo, git missing) prints nothing, distinct from a clean
	// worktree's legitimate zero lines (still WT:0 0 0). Each status
	// line's path is columns 4+; ?? marks untracked, everything else
	// modified; newest is the max mtime seen, 0 when no path stats. The
	// loop reads at most wtStatusLines entries so a huge untracked tree
	// cannot turn one poll into thousands of stat calls.
	wtCmd := `out=$(git status --porcelain 2>/dev/null) && printf '%s\n' "$out" | head -n ` + strconv.Itoa(wtStatusLines) + ` | { ` +
		`u=0; m=0; newest=0; ` +
		`while IFS= read -r line; do ` +
		`[ -z "$line" ] && continue; ` +
		`case "$line" in '??'*) u=$((u+1));; *) m=$((m+1));; esac; ` +
		`path=$(printf '%s' "$line" | cut -c4-); ` +
		`mt=$(stat -c %Y -- "$path" 2>/dev/null) && [ "$mt" -gt "$newest" ] && newest=$mt; ` +
		`done; ` +
		`printf 'WT:%d %d %d\n' "$u" "$m" "$newest"; ` +
		`}`
	cmd = fmt.Sprintf(
		`cd %s && tail -c +%d run.ndjson | head -c %d; printf '%%s\n' %s; [ -f exit_code ] && printf 'EXITCODE:%%s\n' "$(cat exit_code)"; kill -0 "$(cat pid)" 2>/dev/null && printf 'ALIVE\n'; (cd %s && %s) 2>/dev/null || true`,
		shQuote(rdir), offset+1, tailChunkCap, shQuote(boundary), shQuote(workdir), wtCmd,
	)
	return cmd, boundary
}

// WorktreeSummary is the cheap per-poll worktree signal (issue #500):
// counts only, never file contents or paths, so a working delegated
// mission with no commits yet doesn't look stuck.
type WorktreeSummary struct {
	Untracked   int   `json:"untracked"`
	Modified    int   `json:"modified"`
	NewestMtime int64 `json:"newest_mtime"`
}

// pollOnce runs one poll exec: tails run.ndjson from offset (capped at
// tailChunkCap), then a boundary marker, then EXITCODE:/ALIVE status,
// then the WT: worktree summary, all in ONE exec so every field
// reflects the same snapshot.
func (r *delegatedRunner) pollOnce(ctx context.Context, missionID, environment, workRoot, rdir, runID string, offset int64) (chunk []byte, exitCode int, hasExit bool, alive bool, worktree *WorktreeSummary, err error) {
	cmd, boundary := buildPollCmd(workRoot, rdir, runID, offset)
	var out bytes.Buffer
	_, execErr := r.sandboxExec(ctx, missionID, environment, workRoot, cmd, nil, pollTimeout, &out)
	if execErr != nil {
		return nil, 0, false, false, nil, execErr
	}
	return parsePollOutput(out.Bytes(), boundary)
}

// parsePollOutput splits pollOnce's raw output at the boundary marker:
// everything before it is tail content, everything after is the
// EXITCODE:/ALIVE/WT: status lines (each optional).
func parsePollOutput(raw []byte, boundary string) (chunk []byte, exitCode int, hasExit bool, alive bool, worktree *WorktreeSummary, err error) {
	marker := []byte(boundary + "\n")
	idx := bytes.Index(raw, marker)
	if idx == -1 {
		return nil, 0, false, false, nil, fmt.Errorf("delegated runner: poll boundary marker not found in output")
	}
	chunk = raw[:idx]
	status := raw[idx+len(marker):]
	for _, line := range strings.Split(strings.TrimRight(string(status), "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "EXITCODE:"):
			hasExit = true
			exitCode, _ = strconv.Atoi(strings.TrimPrefix(line, "EXITCODE:"))
		case line == "ALIVE":
			alive = true
		case strings.HasPrefix(line, "WT:"):
			if ws, ok := parseWorktreeLine(strings.TrimPrefix(line, "WT:")); ok {
				worktree = ws
			}
		}
	}
	return chunk, exitCode, hasExit, alive, worktree, nil
}

// parseWorktreeLine parses "<untracked> <modified> <newest_mtime>";
// nil on any malformed field.
func parseWorktreeLine(fields string) (*WorktreeSummary, bool) {
	parts := strings.Fields(fields)
	if len(parts) != 3 {
		return nil, false
	}
	untracked, err1 := strconv.Atoi(parts[0])
	modified, err2 := strconv.Atoi(parts[1])
	newest, err3 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, false
	}
	return &WorktreeSummary{Untracked: untracked, Modified: modified, NewestMtime: newest}, true
}

// feedLines advances st.carry/offset bookkeeping and hands each
// complete line to parser, accumulating text/tool/result state.
// Incomplete trailing bytes (no terminating newline yet) are held in
// st.carry until the next chunk completes them — mid-line chunk splits
// must never be fed to the parser as a partial line.
func (r *delegatedRunner) feedLines(ctx context.Context, parser executor.StreamParser, chunk []byte, st *pollState, missionID, phase, runID string) {
	data := append(st.carry, chunk...)
	st.carry = nil
	for {
		i := bytes.IndexByte(data, '\n')
		if i == -1 {
			st.carry = append([]byte{}, data...)
			return
		}
		line := data[:i]
		data = data[i+1:]
		ev, ok := parser.ParseLine(line)
		if !ok {
			continue
		}
		switch ev.Kind {
		case executor.KindSystem:
			if ev.Model != "" {
				st.reportedModel = ev.Model
			}
			if ev.SessionID != "" && st.sessionID == "" {
				st.sessionID = ev.SessionID
				r.recordSessionSeen(ctx, missionID, phase, runID, st)
			}
		case executor.KindText:
			st.textBuf.WriteString(ev.Text)
			st.turns++
		case executor.KindTool:
			st.toolCalls++
		case executor.KindResult:
			st.sawResult = true
			st.resultEvent = ev
		}
	}
}

// killRun sends TERM to the process group, waits, then KILL — best
// effort: a failed kill is logged, never fatal to the caller (the
// caller is already on a failure/cancellation path itself).
func (r *delegatedRunner) killRun(ctx context.Context, missionID, environment, workRoot, rdir string) {
	cmd := fmt.Sprintf(
		`kill -TERM -"$(cat %s/pid)" 2>/dev/null; sleep 5; kill -KILL -"$(cat %s/pid)" 2>/dev/null`,
		shQuote(rdir), shQuote(rdir),
	)
	var out bytes.Buffer
	if _, err := r.sandboxExec(ctx, missionID, environment, workRoot, cmd, nil, killTimeout, &out); err != nil {
		r.log.Warn("delegated runner: kill run failed", "mission_id", missionID, "run_dir", rdir, "error", err)
	}
}

// finish handles the "result event seen, process exited" case — the
// result ladder's rungs 1-3: a schema-parsed status governs outright
// (is_error included — ParseResult succeeding is what matters, not
// whether the run also flagged an error); failing that, a text-form
// sentinel in the accumulated text; failing that, a forced retry noting
// the executor never reported a status.
func (r *delegatedRunner) finish(ctx context.Context, m Mission, run cliRun, st *pollState, start time.Time, exitCode int) (WorkerVerdict, string, error) {
	res, ok := run.adapter.ParseResult(st.resultEvent)
	parseKind := "schema"
	var verdict WorkerVerdict
	switch {
	case ok:
		verdict = WorkerVerdict{
			Outcome:  strings.ToLower(res.Status),
			Evidence: res.Note, Analysis: res.Note, Question: res.Note, Note: res.Note,
		}
	default:
		if raw, sok := extractTextSentinel(st.textBuf.String(), missionStatusToolName); sok {
			if v, vok := tryParseWorkerVerdict(raw); vok {
				verdict, ok, parseKind = v, true, "text_sentinel"
			}
		}
		if !ok {
			parseKind = "none"
			verdict = forcedRetryVerdict("executor finished without a status report")
		}
	}
	// issue #507: the executor did produce a result (or a text-form
	// sentinel), so the entry that ran it is who served the turn.
	// reportedModel (the harness's own claimed model) takes precedence
	// over entry.Model, same precedence recordLedger already uses.
	verdict.Provider = run.entry.ProviderName
	verdict.Model = run.entry.Model
	if st.reportedModel != "" {
		verdict.Model = st.reportedModel
	}

	if err := r.finishCommon(ctx, m, run, st, start, exitCode, parseKind, strings.ToUpper(verdict.Outcome)); err != nil {
		return WorkerVerdict{}, st.textBuf.String(), err
	}
	return verdict, st.textBuf.String(), nil
}

// finishCommon is the bookkeeping every finished run shares (issue
// #582): the executor.result event, the ledger row, and the auth-failure
// check, which cools the entry down and returns ErrExecutorAuth since
// retrying the same entry is futile. status is the parsed verdict
// (DONE/RETRY/BLOCKED for a worker, APPROVE/REWORK for a review).
func (r *delegatedRunner) finishCommon(ctx context.Context, m Mission, run cliRun, st *pollState, start time.Time, exitCode int, parseKind, status string) error {
	authFailed := st.resultEvent.Err != "" && isAuthFailure(st.resultEvent.Err)
	errorCode := ""
	if authFailed {
		errorCode = errorCodeAuthFailed
	}
	// cliCostTrusted tells recordResult whether executor.result's
	// cost_usd (the CLI's OWN reported figure) is the same number
	// recordLedger just booked as real spend — true only for the
	// Anthropic-first-party api_key case (costSource), AND only when
	// the adapter itself claims to report cost at all: pi computes
	// cost client-side from its own catalog (D-013, never trusted)
	// regardless of which driver it ran against, so
	// Capabilities().ReportsCost gates this even for a pi run against
	// an anthropic-driver row. Every other path either books a
	// different, provider-priced figure or none at all, so the UI must
	// not present the raw CLI number as billed.
	cliCostTrusted := run.authMode == executor.AuthAPIKey && run.entry.Driver == "anthropic" && run.adapter.Capabilities().ReportsCost
	r.recordResult(ctx, m.ID, run.phase, st, start, exitCode, st.resultEvent, parseKind, status, cliCostTrusted)
	r.recordLedger(ctx, m, run, st.resultEvent.Usage, start, exitCode == 0 && st.resultEvent.Err == "", errorCode, st.reportedModel)
	if authFailed {
		r.coolDown(run.harness, run.entry)
		r.recordAuthFailed(ctx, m.ID, run.phase, run.harness)
		return fmt.Errorf("%w: %s", ErrExecutorAuth, st.resultEvent.Err)
	}
	return nil
}

// finishNoResult handles transport death: the process ended (or was
// lost) with no result event ever parsed. Per the result ladder this is
// a forced RETRY (the returned reason is its note), EXCEPT auth failure
// and sandbox-unreachable cases, which return an error instead since
// retrying the same entry is futile. An extra exec reads stderr.log's
// tail only on this path (exit != 0, no result) to check for
// auth-failure signatures.
func (r *delegatedRunner) finishNoResult(ctx context.Context, m Mission, run cliRun, workRoot, rdir string, st *pollState, start time.Time, exitCode int) (string, error) {
	stderrTail := r.readStderrTail(ctx, m.ID, m.Environment, workRoot, rdir)
	reason := fmt.Sprintf("executor exited (code %d) without a result event", exitCode)
	if exitCode == -1 {
		reason = "executor process was lost (no exit code, no result event)"
	}

	// 124 is `timeout`'s own exit status when it had to stop the CLI:
	// the wall-clock run budget fired (issue #498). Recorded under its
	// own reason so the UI and canary can tell it from a crash.
	if exitCode == runBudgetExitCode {
		reason = "executor exceeded the run budget and was killed"
		r.recordDied(ctx, m.ID, run.phase, "run_budget", &exitCode, stderrTail)
		r.recordLedger(ctx, m, run, nil, start, false, "", st.reportedModel)
		r.coolDown(run.harness, run.entry)
		return reason, nil
	}

	if exitCode != 0 && isAuthFailure(stderrTail) {
		r.coolDown(run.harness, run.entry)
		r.recordDied(ctx, m.ID, run.phase, "auth_failed", &exitCode, stderrTail)
		r.recordLedger(ctx, m, run, nil, start, false, errorCodeAuthFailed, st.reportedModel)
		r.recordAuthFailed(ctx, m.ID, run.phase, run.harness)
		return "", fmt.Errorf("%w: %s", ErrExecutorAuth, stderrTail)
	}

	r.recordDied(ctx, m.ID, run.phase, "transport_death", &exitCode, stderrTail)
	r.recordLedger(ctx, m, run, nil, start, false, "", st.reportedModel)
	r.coolDown(run.harness, run.entry)
	return reason, nil
}

// readStderrTail fetches the last ~2KB of stderr.log — a single extra
// exec, only taken on the transport-death path, to check for an
// auth-failure signature the result ladder must distinguish from a
// generic forced retry.
func (r *delegatedRunner) readStderrTail(ctx context.Context, missionID, environment, workRoot, rdir string) string {
	var out bytes.Buffer
	cmd := fmt.Sprintf("cd %s && tail -c 2048 stderr.log 2>/dev/null", shQuote(rdir))
	if _, err := r.sandboxExec(ctx, missionID, environment, workRoot, cmd, nil, pollTimeout, &out); err != nil {
		return ""
	}
	return out.String()
}

// authFailureSignatures are the known stderr/result phrasings a CLI
// emits on a bad or expired credential — checked case-insensitively.
var authFailureSignatures = []string{
	"invalid api key",
	"please run /login",
	"authentication_error",
}

func isAuthFailure(text string) bool {
	lower := strings.ToLower(text)
	for _, sig := range authFailureSignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// --- events -----------------------------------------------------------

// recordSpawned writes the executor.spawned manifest event —
// re-attach's source of truth for run_id/run_dir/harness across a brain
// restart. argv is deliberately never included: BuildInvocation's argv
// carries the prompt substitution placeholder only ("@PROMPT@"), never
// the rendered prompt text, and env values are never recorded, only
// names.
func (r *delegatedRunner) recordSpawned(ctx context.Context, missionID string, run cliRun, runID, rdir string, decision resumeDecision) {
	if r.events == nil {
		return
	}
	payload := map[string]any{
		"harness": run.harness, "provider": run.entry.ProviderName, "model": run.entry.Model,
		"auth_mode": string(run.authMode), "run_id": runID, "run_dir": rdir,
		"resumed": decision.resume, "phase": run.phase,
	}
	if decision.resume {
		payload["session_id"] = decision.sessionID
	} else {
		payload["resume_reason"] = decision.reason
	}
	if err := r.events.AppendEvent(ctx, missionID, "executor.spawned", payload); err != nil {
		r.log.Warn("delegated runner: record spawned failed", "mission_id", missionID, "error", err)
	}
}

// recordSessionSeen writes executor.session once for a run: the first
// time its stream reports a CLI session/thread id (D-103, issue #499).
// Guarded by st.sessionRecorded so a run's later KindSystem-shaped noise
// (there is none today, but the guard costs nothing) never double-writes.
func (r *delegatedRunner) recordSessionSeen(ctx context.Context, missionID, phase, runID string, st *pollState) {
	if r.events == nil || st.sessionRecorded {
		return
	}
	st.sessionRecorded = true
	r.recordEventForce(ctx, missionID, "executor.session", map[string]any{
		"run_id": runID, "session_id": st.sessionID, "phase": phase,
	})
}

// recordProgressThrottled writes executor.progress at most once per
// 60s and only when the byte offset actually advanced since the last
// write — st tracks lastProgress across poll iterations.
func (r *delegatedRunner) recordProgressThrottled(ctx context.Context, missionID, phase, runID string, st *pollState) {
	if r.events == nil {
		return
	}
	if time.Since(st.lastProgress) < time.Minute {
		return
	}
	st.lastProgress = time.Now()
	payload := map[string]any{
		"run_id": runID, "byte_offset": st.offset, "turns": st.turns, "tool_calls": st.toolCalls, "phase": phase,
	}
	if st.worktree != nil {
		payload["worktree"] = st.worktree
	}
	r.recordEvent(ctx, missionID, st, "executor.progress", payload)
}

// recordResult writes the terminal executor.result event. status is
// the PARSED verdict (DONE/RETRY/BLOCKED), never the raw result text —
// the raw text mirrors the whole structured-output JSON and belongs in
// the transcript, not an event field the UI and canary key on.
// cliCostTrusted mirrors costSource's decision (computed once by the
// caller so this stays a pure payload builder): true only when
// usage.cost_usd is the SAME figure recordLedger just booked as real
// billed spend (Anthropic first-party api_key) — false for every other
// path (subscription/oauth unbilled, or a non-anthropic provider where
// cost_usd is priced against Anthropic's table and was never booked at
// all). The UI keys off this to avoid presenting an unbooked number as
// billed.
func (r *delegatedRunner) recordResult(ctx context.Context, missionID, phase string, st *pollState, start time.Time, exitCode int, ev executor.Event, parseKind, status string, cliCostTrusted bool) {
	payload := map[string]any{
		"status": status, "is_error": ev.Err != "",
		"duration_ms": time.Since(start).Milliseconds(),
		"exit_code":   exitCode, "parse": parseKind, "phase": phase,
	}
	if len(ev.Denials) > 0 {
		payload["denials"] = ev.Denials
	}
	if ev.Usage != nil {
		payload["usage"] = map[string]any{
			"input_tokens": ev.Usage.InputTokens, "output_tokens": ev.Usage.OutputTokens,
			"cache_read": ev.Usage.CacheReadTokens, "cache_write": ev.Usage.CacheWriteTokens,
			"cost_usd": ev.Usage.CostUSD, "cost_usd_billed": cliCostTrusted,
		}
	}
	r.recordEventForce(ctx, missionID, "executor.result", payload)
}

// recordDied writes executor.died with a capped, neutralized stderr
// tail.
func (r *delegatedRunner) recordDied(ctx context.Context, missionID, phase, reason string, exitCode *int, stderrTail string) {
	payload := map[string]any{"reason": reason, "stderr_tail": NeutralizeSlot(truncate(stderrTail, 2000)), "phase": phase}
	if exitCode != nil {
		payload["exit_code"] = *exitCode
	}
	r.recordEventForce(ctx, missionID, "executor.died", payload)
}

// recordAuthFailed writes executor.auth_failed.
func (r *delegatedRunner) recordAuthFailed(ctx context.Context, missionID, phase, harness string) {
	r.recordEventForce(ctx, missionID, "executor.auth_failed", map[string]any{"harness": harness, "phase": phase})
}

// recordSkipped writes executor.skipped — the one persisted record that
// a mission explicitly requesting harness ran native instead, and why.
// extra carries the reason-specific fields (error/until+provider+model/
// skip_reasons); nil for unknown_harness, which needs nothing beyond
// harness+reason.
func (r *delegatedRunner) recordSkipped(ctx context.Context, missionID, harness, reason string, extra map[string]any) {
	payload := map[string]any{"harness": harness, "reason": reason, "phase": string(PhaseGenerate)}
	for k, v := range extra {
		payload[k] = v
	}
	r.recordEventForce(ctx, missionID, "executor.skipped", payload)
}

// recordEvent appends one executor.* event, respecting maxExecutorEvents
// — past the cap, only terminal events (recordEventForce's callers)
// keep writing.
func (r *delegatedRunner) recordEvent(ctx context.Context, missionID string, st *pollState, kind string, payload map[string]any) {
	if r.events == nil {
		return
	}
	if st.eventCount >= maxExecutorEvents {
		return
	}
	st.eventCount++
	if err := r.events.AppendEvent(ctx, missionID, kind, payload); err != nil {
		r.log.Warn("delegated runner: record event failed", "mission_id", missionID, "kind", kind, "error", err)
	}
}

// recordEventForce appends a terminal executor.* event unconditionally
// — the event cap only ever throttles the high-frequency progress/text
// coalescing events, never the one-per-run result/died/auth_failed
// events a mission's own history depends on.
func (r *delegatedRunner) recordEventForce(ctx context.Context, missionID, kind string, payload map[string]any) {
	if r.events == nil {
		return
	}
	if err := r.events.AppendEvent(ctx, missionID, kind, payload); err != nil {
		r.log.Warn("delegated runner: record event failed", "mission_id", missionID, "kind", kind, "error", err)
	}
}

// errorCodeAuthFailed marks a cost_ledger row as a delegated executor
// auth failure — HealthRow.LastError only means something to an
// operator staring at a kind='cli' provider if it's distinguishable
// from an ordinary run error.
const errorCodeAuthFailed = "executor_auth"

// costSource decides where a delegated run's ledger cost comes from
// and whether it's real billed spend or unbilled (D-05x, fixes the
// bug where a non-anthropic api_key provider's CLI-reported cost —
// priced against ANTHROPIC's table, not that provider's — got booked
// as real spend; a local Ollama run this way showed $0.34 billed for
// $0 of actual spend):
//
//   - AuthSubscription/AuthOAuthToken: a kind='cli' provider row
//     (driver "claude-cli"/"codex-cli", D-051) — never real spend,
//     billed on the user's existing subscription instead. The CLI's
//     reported figure is kept as-is, Unbilled=true, same as before.
//   - AuthAPIKey + driver "anthropic": a kind='api' row repurposed as
//     an executor entry (admin.go's validateHarnessWireFormat), the
//     CLI talking to Anthropic's own real endpoint under that row's
//     API key — the ONLY case where the CLI is genuinely pricing
//     against its own provider's table. The reported cost is trusted,
//     Unbilled=false, same as before.
//   - AuthAPIKey + any other driver (GLM, Ollama, etc — a kind='api'
//     row wired via options.anthropic_base_url instead): the CLI still
//     prices against Anthropic's table regardless of which backend it
//     actually talked to — that number is fiction for this provider.
//     Cost is instead computed from the provider's OWN configured
//     price row (entry.Prices) × the run's reported tokens
//     (ledger.Cost, same formula the gateway uses for native calls).
//     No price row → Cost stays nil (D-013: unknown price recorded as
//     NULL, never guessed; tokens still surface the usage as
//     unpriced).
//
// Driver, not kind, is the signal: kind='cli' rows always use
// AuthSubscription/AuthOAuthToken (never AuthAPIKey) by construction,
// so checking kind again in the AuthAPIKey branch would be dead code.
func costSource(entry gwclient.ResolvedRouteEntry, authMode executor.AuthMode, usage *stream.Usage, cliReportedCost *float64) (cost *float64, unbilled bool) {
	if authMode != executor.AuthAPIKey {
		return cliReportedCost, true
	}
	if entry.Driver == "anthropic" {
		return cliReportedCost, false
	}
	return ledger.Cost(entry.Prices, usage), false
}

// recordLedger writes one cost_ledger row at the run's terminal point
// (D-055) — see costSource for how Cost/Unbilled are decided.
// errorCode is optional (e.g. errorCodeAuthFailed); blank on ok=true.
// reportedModel is the harness's own KindSystem model name, preferred
// over entry.Model so a self-paired row's placeholder ("default" on
// cursor-cli) never stands in for the model that actually ran; blank
// falls back to entry.Model, which is what every route-resolved
// harness already agrees on. run.route/run.agent label the row: a
// review run books under reviewerAgent (issue #582) so the D-097 review
// token ceiling counts it like a native reviewer turn.
func (r *delegatedRunner) recordLedger(ctx context.Context, m Mission, run cliRun, usage *executor.Usage, start time.Time, ok bool, errorCode string, reportedModel string) {
	if r.ledger == nil {
		return
	}
	status := "ok"
	if !ok {
		status = "error"
	}
	model := run.entry.Model
	if reportedModel != "" {
		model = reportedModel
	}
	e := ledger.Entry{
		Provider: run.entry.ProviderName, Model: model, Route: run.route,
		Agent: run.agent, Purpose: "executor", MissionID: m.ID,
		LatencyMS: time.Since(start).Milliseconds(), Status: status, ErrorCode: errorCode,
	}
	if usage != nil {
		e.Usage = &stream.Usage{
			InputTokens: int(usage.InputTokens), OutputTokens: int(usage.OutputTokens),
			CacheReadTokens: int(usage.CacheReadTokens), CacheWriteTokens: int(usage.CacheWriteTokens),
		}
		e.Cost, e.Unbilled = costSource(run.entry, run.authMode, e.Usage, usage.CostUSD)
	}
	r.ledger.Record(ctx, e)
}
