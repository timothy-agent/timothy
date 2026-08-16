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
	runBudgetDefault  = 90 * time.Minute // spec.RunBudget: overall CLI wall-clock cap
	launchTimeout     = 60 * time.Second
	pollInterval      = 10 * time.Second
	pollTimeout       = 60 * time.Second
	idleTimeout       = 10 * time.Minute
	killTimeout       = 60 * time.Second
	tailChunkCap      = 262144 // 256KiB per poll, bounds one exec's output
	cooldownTTL       = 10 * time.Minute
	maxExecutorEvents = 300
	pollInfraRetries  = 3
	pollInfraBackoff  = 5 * time.Second
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

// delegatedRunner wraps nativeRunner: explore/plan/review pass through
// untouched (delegated CLI executors only ever serve worker turns).
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

	log *slog.Logger

	// Overridable in tests so scenarios don't wait real minutes.
	pollInterval time.Duration
	idleTimeout  time.Duration
	runBudget    time.Duration

	mu       sync.Mutex
	cooldown map[cooldownKey]time.Time
}

// NewDelegatedRunner wraps native with the delegated-executor dispatch
// path. sandboxExec must be non-nil (cmd/brain/main.go only constructs
// this when a sandbox manager is present — missions already require
// one); events/lastRun/led may be nil in tests that don't assert on
// them, but production wiring always supplies all four.
func NewDelegatedRunner(native Runner, resolveRoute routeResolver, resolveCred credResolver, sandboxExec sandboxExecEnv, events eventSink, lastRun lastRunStateFunc, led usageRecorder, log *slog.Logger) Runner {
	return &delegatedRunner{
		native: native, resolveRoute: resolveRoute, resolveCred: resolveCred,
		sandboxExec: sandboxExec, events: events, lastRun: lastRun, ledger: led, log: log,
		pollInterval: pollInterval, idleTimeout: idleTimeout, runBudget: runBudgetDefault,
		cooldown: map[cooldownKey]time.Time{},
	}
}

func (r *delegatedRunner) ExploreSession(ctx context.Context, m Mission) (string, error) {
	return r.native.ExploreSession(ctx, m)
}

func (r *delegatedRunner) PlanSession(ctx context.Context, m Mission, exploreNotes string) (Spec, error) {
	return r.native.PlanSession(ctx, m, exploreNotes)
}

func (r *delegatedRunner) RunReview(ctx context.Context, m Mission, packet ReviewPacket, gatekeeper *GatekeeperState) (ReviewVerdict, *GatekeeperState, error) {
	return r.native.RunReview(ctx, m, packet, gatekeeper)
}

// RunWorker dispatches on m.Harness (D-051 rework): "" defers straight
// to native.RunWorker (which itself lets the gateway walk any native
// chain). Otherwise it looks up the adapter and resolves the worker
// route on the executor axis, walking the first usable, non-cooled
// entry into the delegated protocol. An unknown harness, a resolve
// failure, or no usable entry at all falls back to native.RunWorker
// unchanged — today's behavior is always the floor — but a mission
// that explicitly asked for a harness must not fall back silently: an
// executor.skipped event is recorded first (see recordSkipped) so the
// mission's own history shows the requested harness was never actually
// used.
func (r *delegatedRunner) RunWorker(ctx context.Context, m Mission, packet WorkPacket) (WorkerVerdict, string, error) {
	if m.Harness == "" {
		return r.native.RunWorker(ctx, m, packet)
	}
	adapter, ok := executor.Lookup(m.Harness)
	if !ok {
		r.log.Warn("delegated runner: unknown harness; falling back to native", "mission_id", m.ID, "harness", m.Harness)
		r.recordSkipped(ctx, m.ID, m.Harness, "unknown_harness", nil)
		return r.native.RunWorker(ctx, m, packet)
	}

	route, err := r.resolveRoute(ctx, workerRoute(m), m.Harness)
	if err != nil || route == nil {
		if err != nil {
			r.log.Warn("delegated runner: route resolve failed; falling back to native", "mission_id", m.ID, "error", err)
			r.recordSkipped(ctx, m.ID, m.Harness, "resolve_failed", map[string]any{"error": truncate(err.Error(), 2000)})
		} else {
			r.recordSkipped(ctx, m.ID, m.Harness, "resolve_failed", map[string]any{"error": "resolved route was nil without an error"})
		}
		return r.native.RunWorker(ctx, m, packet)
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
const delegatedSystemAppend = " You are running as a delegated coding CLI, not through mission_status. End your turn by producing the required structured output with status DONE, RETRY, or BLOCKED and a short note. Only report DONE when every acceptance criterion for the current unit is genuinely met — the harness independently verifies your artifacts and verify_cmd regardless of what you report, so a false DONE only costs a wasted review round, never actually passes."

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
		r.recordAuthFailed(ctx, m.ID, m.Harness)
		return WorkerVerdict{}, "", err
	}

	runID, err := newRunID()
	if err != nil {
		r.coolDown(m.Harness, entry)
		return WorkerVerdict{}, "", err
	}
	rdir := runDir(m.Workspace, runID)
	system, user := packet.Render()
	system += delegatedSystemAppend

	spec := executor.InvocationSpec{
		MissionID: m.ID, Workdir: workRoot,
		PromptPath:   filepath.Join(rdir, "prompt.md"),
		SystemAppend: system,
		Model:        entry.Model, AuthMode: authMode, APIKey: apiKey, BaseURL: entry.BaseURL,
		AllowTools: delegatedAllowTools, DenyTools: delegatedDenyTools,
		ResultSchema: resultSchemaJSON, RunBudget: r.runBudget, Wire: entry.Wire,
	}
	inv, err := adapter.BuildInvocation(spec)
	if err != nil {
		r.coolDown(m.Harness, entry)
		return WorkerVerdict{}, "", fmt.Errorf("delegated runner: build invocation: %w", err)
	}

	// 0750/0600 suffice: brain and the sandbox CLI share uid 65534 on
	// the workspace volume, so owner permissions cover both readers.
	if err := os.MkdirAll(rdir, 0o750); err != nil {
		r.coolDown(m.Harness, entry)
		return WorkerVerdict{}, "", fmt.Errorf("delegated runner: create run dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rdir, "prompt.md"), []byte(user), 0o600); err != nil {
		r.coolDown(m.Harness, entry)
		return WorkerVerdict{}, "", fmt.Errorf("delegated runner: write prompt: %w", err)
	}
	if err := writeInvocationFiles(rdir, inv.Files); err != nil {
		r.coolDown(m.Harness, entry)
		return WorkerVerdict{}, "", fmt.Errorf("delegated runner: write invocation files: %w", err)
	}

	r.recordSpawned(ctx, m.ID, m.Harness, entry, runID, rdir, authMode)

	if err := r.launch(ctx, m.ID, m.Environment, workRoot, rdir, inv); err != nil {
		r.coolDown(m.Harness, entry)
		r.recordDied(ctx, m.ID, "spawn_failed", nil, err.Error())
		return WorkerVerdict{}, "", fmt.Errorf("delegated runner: launch: %w", err)
	}

	return r.pollToVerdict(ctx, m, workRoot, rdir, runID, entry, adapter, authMode, 0)
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
		r.recordDied(ctx, m.ID, "lost_run", nil, "run directory or pid missing after restart")
		return true, WorkerVerdict{Outcome: "retry", Forced: true, Analysis: "the executor's run was lost across a restart"}, "", nil
	}

	v, t, rerr := r.pollToVerdict(ctx, m, workRoot, state.RunDir, state.RunID, entry, adapter, state.AuthMode, state.ByteOffset)
	return true, v, t, rerr
}

// launch starts the CLI detached: setsid backgrounds it so it survives
// the ExecEnv call returning, pid captured to a file, stdout/stderr
// redirected into the run directory. The `timeout` wrapper (D-052)
// enforces spec.RunBudget itself, independent of anything brain does
// afterward — a crashed brain still lets the container kill a runaway
// CLI.
func (r *delegatedRunner) launch(ctx context.Context, missionID, environment, workdir, rdir string, inv executor.Invocation) error {
	launchCmd, err := buildLaunchCmd(workdir, rdir, inv, r.runBudget)
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

// buildLaunchCmd composes the full detached-launch command. The inner
// script is quoted as ONE unit via shQuote — composing it with literal
// quotes would let the argv elements' own single quotes toggle the
// outer quoting and word-split any argument containing spaces (the
// system-prompt append, most obviously).
func buildLaunchCmd(workdir, rdir string, inv executor.Invocation, runBudget time.Duration) (string, error) {
	argv, err := renderArgv(inv)
	if err != nil {
		return "", err
	}
	inner := fmt.Sprintf(
		"timeout -k 30 %d %s > %s/run.ndjson 2> %s/stderr.log; echo $? > %s/exit_code",
		int(runBudget/time.Second), argv, shQuote(rdir), shQuote(rdir), shQuote(rdir),
	)
	// Braces bound the `&` to the setsid job alone — a bare `cd && mkdir
	// && setsid ... & echo $!` backgrounds the whole chain and races the
	// pid write against the mkdir it depends on.
	return fmt.Sprintf(
		"cd %s && mkdir -p %s && { setsid sh -c %s > /dev/null 2>&1 & echo $! > %s/pid; }",
		shQuote(workdir), shQuote(rdir), shQuote(inner), shQuote(rdir),
	), nil
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
	textBuf      strings.Builder
	eventCount   int
	infraRetries int
}

// pollToVerdict polls rdir until the run terminates (exit_code present
// and run.ndjson fully drained), the idle watchdog fires, ctx is
// cancelled, or repeated infra errors declare the sandbox unreachable.
// startOffset seeds the byte offset on a resumed run; 0 for a fresh
// spawn.
func (r *delegatedRunner) pollToVerdict(ctx context.Context, m Mission, workRoot, rdir, runID string, entry gwclient.ResolvedRouteEntry, adapter executor.Adapter, authMode executor.AuthMode, startOffset int64) (WorkerVerdict, string, error) {
	parser := adapter.NewParser()
	st := &pollState{offset: startOffset, lastByteMove: time.Now(), lastProgress: time.Now()}
	start := time.Now()

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.killRun(context.WithoutCancel(ctx), m.ID, m.Environment, workRoot, rdir)
			r.recordDied(context.WithoutCancel(ctx), m.ID, "ctx_cancelled", nil, ctx.Err().Error())
			r.coolDown(m.Harness, entry)
			return WorkerVerdict{}, st.textBuf.String(), ctx.Err()
		case <-ticker.C:
		}

		chunk, exitCode, hasExit, alive, err := r.pollOnce(ctx, m.ID, m.Environment, workRoot, rdir, runID, st.offset)
		if err != nil {
			st.infraRetries++
			if st.infraRetries >= pollInfraRetries {
				return WorkerVerdict{}, st.textBuf.String(), fmt.Errorf("delegated runner: sandbox unreachable after %d poll retries: %w", pollInfraRetries, err)
			}
			time.Sleep(pollInfraBackoff)
			continue
		}
		st.infraRetries = 0

		if len(chunk) > 0 {
			st.offset += int64(len(chunk))
			st.lastByteMove = time.Now()
			r.feedLines(parser, chunk, st, m.ID)
			r.recordProgressThrottled(ctx, m.ID, runID, st)
		}

		if st.sawResult && hasExit {
			return r.finish(ctx, m, entry, adapter, authMode, st, start, exitCode)
		}
		if hasExit && !alive {
			// Process exited; run.ndjson fully drained (no new bytes this
			// poll) but no result event ever arrived — transport death.
			return r.finishNoResult(ctx, m, entry, workRoot, rdir, authMode, st, start, exitCode)
		}
		if !alive && !hasExit {
			// pid gone, no exit_code file: process died without even
			// writing its own exit status — still transport death.
			return r.finishNoResult(ctx, m, entry, workRoot, rdir, authMode, st, start, -1)
		}

		if time.Since(st.lastByteMove) > r.idleTimeout {
			r.killRun(ctx, m.ID, m.Environment, workRoot, rdir)
			r.recordEvent(ctx, m.ID, st, "executor.idle_killed", map[string]any{"idle_s": int(r.idleTimeout.Seconds())})
			r.coolDown(m.Harness, entry)
			return WorkerVerdict{Outcome: "retry", Forced: true, Analysis: "the executor produced no output for the idle timeout and was killed"}, st.textBuf.String(), nil
		}
	}
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
// illegal option.
func buildPollCmd(rdir, runID string, offset int64) (cmd, boundary string) {
	boundary = pollBoundaryPrefix + runID + "---"
	cmd = fmt.Sprintf(
		`cd %s && tail -c +%d run.ndjson | head -c %d; printf '%%s\n' %s; [ -f exit_code ] && printf 'EXITCODE:%%s\n' "$(cat exit_code)"; kill -0 "$(cat pid)" 2>/dev/null && printf 'ALIVE\n'`,
		shQuote(rdir), offset+1, tailChunkCap, shQuote(boundary),
	)
	return cmd, boundary
}

// pollOnce runs one poll exec: tails run.ndjson from offset (capped at
// tailChunkCap), then a boundary marker, then EXITCODE:/ALIVE status —
// all in ONE exec so the exit_code/pid reads are consistent with the
// tail snapshot they describe.
func (r *delegatedRunner) pollOnce(ctx context.Context, missionID, environment, workRoot, rdir, runID string, offset int64) (chunk []byte, exitCode int, hasExit bool, alive bool, err error) {
	cmd, boundary := buildPollCmd(rdir, runID, offset)
	var out bytes.Buffer
	_, execErr := r.sandboxExec(ctx, missionID, environment, workRoot, cmd, nil, pollTimeout, &out)
	if execErr != nil {
		return nil, 0, false, false, execErr
	}
	return parsePollOutput(out.Bytes(), boundary)
}

// parsePollOutput splits pollOnce's raw output at the boundary marker:
// everything before it is tail content, everything after is the
// EXITCODE:/ALIVE status lines (each optional).
func parsePollOutput(raw []byte, boundary string) (chunk []byte, exitCode int, hasExit bool, alive bool, err error) {
	marker := []byte(boundary + "\n")
	idx := bytes.Index(raw, marker)
	if idx == -1 {
		return nil, 0, false, false, fmt.Errorf("delegated runner: poll boundary marker not found in output")
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
		}
	}
	return chunk, exitCode, hasExit, alive, nil
}

// feedLines advances st.carry/offset bookkeeping and hands each
// complete line to parser, accumulating text/tool/result state.
// Incomplete trailing bytes (no terminating newline yet) are held in
// st.carry until the next chunk completes them — mid-line chunk splits
// must never be fed to the parser as a partial line.
func (r *delegatedRunner) feedLines(parser executor.StreamParser, chunk []byte, st *pollState, missionID string) {
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
func (r *delegatedRunner) finish(ctx context.Context, m Mission, entry gwclient.ResolvedRouteEntry, adapter executor.Adapter, authMode executor.AuthMode, st *pollState, start time.Time, exitCode int) (WorkerVerdict, string, error) {
	res, ok := adapter.ParseResult(st.resultEvent)
	parseKind := "schema"
	var verdict WorkerVerdict
	switch {
	case ok:
		verdict = WorkerVerdict{
			Outcome:  strings.ToLower(res.Status),
			Evidence: res.Note, Analysis: res.Note, Question: res.Note,
		}
	default:
		if raw, sok := extractTextSentinel(st.textBuf.String(), missionStatusToolName); sok {
			if v, vok := tryParseWorkerVerdict(raw); vok {
				verdict, ok, parseKind = v, true, "text_sentinel"
			}
		}
		if !ok {
			parseKind = "none"
			verdict = WorkerVerdict{Outcome: "retry", Forced: true, Analysis: "executor finished without a status report"}
		}
	}

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
	cliCostTrusted := authMode == executor.AuthAPIKey && entry.Driver == "anthropic" && adapter.Capabilities().ReportsCost
	r.recordResult(ctx, m.ID, st, start, exitCode, st.resultEvent, parseKind, strings.ToUpper(verdict.Outcome), cliCostTrusted)
	r.recordLedger(ctx, m, entry, authMode, st.resultEvent.Usage, start, exitCode == 0 && st.resultEvent.Err == "", errorCode)
	if authFailed {
		r.coolDown(m.Harness, entry)
		r.recordAuthFailed(ctx, m.ID, m.Harness)
		return WorkerVerdict{}, st.textBuf.String(), fmt.Errorf("%w: %s", ErrExecutorAuth, st.resultEvent.Err)
	}
	return verdict, st.textBuf.String(), nil
}

// finishNoResult handles transport death: the process ended (or was
// lost) with no result event ever parsed. Per the result ladder this is
// a forced RETRY, EXCEPT auth failure and sandbox-unreachable cases,
// which return an error instead since retrying the same entry is
// futile. An extra exec reads stderr.log's tail only on this path (exit
// != 0, no result) to check for auth-failure signatures.
func (r *delegatedRunner) finishNoResult(ctx context.Context, m Mission, entry gwclient.ResolvedRouteEntry, workRoot, rdir string, authMode executor.AuthMode, st *pollState, start time.Time, exitCode int) (WorkerVerdict, string, error) {
	stderrTail := r.readStderrTail(ctx, m.ID, m.Environment, workRoot, rdir)
	reason := fmt.Sprintf("executor exited (code %d) without a result event", exitCode)
	if exitCode == -1 {
		reason = "executor process was lost (no exit code, no result event)"
	}

	if exitCode != 0 && isAuthFailure(stderrTail) {
		r.coolDown(m.Harness, entry)
		r.recordDied(ctx, m.ID, "auth_failed", &exitCode, stderrTail)
		r.recordLedger(ctx, m, entry, authMode, nil, start, false, errorCodeAuthFailed)
		r.recordAuthFailed(ctx, m.ID, m.Harness)
		return WorkerVerdict{}, st.textBuf.String(), fmt.Errorf("%w: %s", ErrExecutorAuth, stderrTail)
	}

	r.recordDied(ctx, m.ID, "transport_death", &exitCode, stderrTail)
	r.recordLedger(ctx, m, entry, authMode, nil, start, false, "")
	r.coolDown(m.Harness, entry)
	return WorkerVerdict{Outcome: "retry", Forced: true, Analysis: reason}, st.textBuf.String(), nil
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
func (r *delegatedRunner) recordSpawned(ctx context.Context, missionID, harness string, entry gwclient.ResolvedRouteEntry, runID, rdir string, authMode executor.AuthMode) {
	if r.events == nil {
		return
	}
	if err := r.events.AppendEvent(ctx, missionID, "executor.spawned", map[string]any{
		"harness": harness, "provider": entry.ProviderName, "model": entry.Model,
		"auth_mode": string(authMode), "run_id": runID, "run_dir": rdir,
	}); err != nil {
		r.log.Warn("delegated runner: record spawned failed", "mission_id", missionID, "error", err)
	}
}

// recordProgressThrottled writes executor.progress at most once per
// 60s and only when the byte offset actually advanced since the last
// write — st tracks lastProgress across poll iterations.
func (r *delegatedRunner) recordProgressThrottled(ctx context.Context, missionID, runID string, st *pollState) {
	if r.events == nil {
		return
	}
	if time.Since(st.lastProgress) < time.Minute {
		return
	}
	st.lastProgress = time.Now()
	r.recordEvent(ctx, missionID, st, "executor.progress", map[string]any{
		"run_id": runID, "byte_offset": st.offset, "turns": st.turns, "tool_calls": st.toolCalls,
	})
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
func (r *delegatedRunner) recordResult(ctx context.Context, missionID string, st *pollState, start time.Time, exitCode int, ev executor.Event, parseKind, status string, cliCostTrusted bool) {
	payload := map[string]any{
		"status": status, "is_error": ev.Err != "",
		"duration_ms": time.Since(start).Milliseconds(),
		"exit_code":   exitCode, "parse": parseKind,
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
func (r *delegatedRunner) recordDied(ctx context.Context, missionID, reason string, exitCode *int, stderrTail string) {
	payload := map[string]any{"reason": reason, "stderr_tail": NeutralizeSlot(truncate(stderrTail, 2000))}
	if exitCode != nil {
		payload["exit_code"] = *exitCode
	}
	r.recordEventForce(ctx, missionID, "executor.died", payload)
}

// recordAuthFailed writes executor.auth_failed.
func (r *delegatedRunner) recordAuthFailed(ctx context.Context, missionID, harness string) {
	r.recordEventForce(ctx, missionID, "executor.auth_failed", map[string]any{"harness": harness})
}

// recordSkipped writes executor.skipped — the one persisted record that
// a mission explicitly requesting harness ran native instead, and why.
// extra carries the reason-specific fields (error/until+provider+model/
// skip_reasons); nil for unknown_harness, which needs nothing beyond
// harness+reason.
func (r *delegatedRunner) recordSkipped(ctx context.Context, missionID, harness, reason string, extra map[string]any) {
	payload := map[string]any{"harness": harness, "reason": reason}
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
func (r *delegatedRunner) recordLedger(ctx context.Context, m Mission, entry gwclient.ResolvedRouteEntry, authMode executor.AuthMode, usage *executor.Usage, start time.Time, ok bool, errorCode string) {
	if r.ledger == nil {
		return
	}
	status := "ok"
	if !ok {
		status = "error"
	}
	e := ledger.Entry{
		Provider: entry.ProviderName, Model: entry.Model, Route: workerRoute(m),
		Agent: "mission-worker", Purpose: "executor", MissionID: m.ID,
		LatencyMS: time.Since(start).Milliseconds(), Status: status, ErrorCode: errorCode,
	}
	if usage != nil {
		e.Usage = &stream.Usage{
			InputTokens: int(usage.InputTokens), OutputTokens: int(usage.OutputTokens),
			CacheReadTokens: int(usage.CacheReadTokens), CacheWriteTokens: int(usage.CacheWriteTokens),
		}
		e.Cost, e.Unbilled = costSource(entry, authMode, e.Usage, usage.CostUSD)
	}
	r.ledger.Record(ctx, e)
}
