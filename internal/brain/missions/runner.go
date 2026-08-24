package missions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// ErrModelFloor reports that a mission turn was served by a model on
// the runner's deny list — a fallback too weak to drive tool-using
// agentic turns. The driver pauses the mission immediately (infra)
// instead of burning its iteration budget on a model that cannot
// succeed.
var ErrModelFloor = errors.New("mission turn served by a below-floor model")

// GatekeeperState is whatever a reviewer session needs to resume
// in-memory for a delta recheck on rework, instead of cold-reanalyzing
// from scratch. Acceptable to lose on restart — a cold reviewer just
// re-checks everything, which is safe, just slower.
type GatekeeperState struct {
	Messages []provider.Message
}

// Runner executes ONE session (worker turn, reviewer turn, or planner
// turn) and owns NO state-transition logic — it reports what happened,
// Driver decides what it means. A future delegated CLI executor would
// be an alternate Runner implementation; Phase 1 ships exactly one:
// nativeRunner, backed by loop.Agent. This interface is kept minimal
// on purpose — no executor-selection abstraction is built until a
// second implementation actually needs one.
type Runner interface {
	// RunWorker seeds a FRESH session from packet, runs it to
	// completion (or the sentinel/bail enforcement ladder), and returns
	// the parsed verdict plus raw transcript text (for progress-note
	// extraction and NeutralizeSlot'd storage).
	RunWorker(ctx context.Context, m Mission, packet WorkPacket) (WorkerVerdict, string, error)

	// RunReview resumes (or starts, on the first round) the gatekeeper
	// session, judging the packet — goal, plan, harness-read artifact
	// contents, diff, evidence — and returns the verdict.
	RunReview(ctx context.Context, m Mission, packet ReviewPacket, gatekeeper *GatekeeperState) (ReviewVerdict, *GatekeeperState, error)

	// PlanSession runs the planning turn that produces a Spec (the list
	// of PlanUnits) from the mission's goal and explore-phase findings.
	PlanSession(ctx context.Context, m Mission, exploreNotes string) (Spec, error)

	// ExploreSession runs the explore turn: a tool-using session that
	// explores the workspace and (via base tools) the web, ending with an
	// explore_notes sentinel call. Exploration is advisory — a turn that
	// never produces the sentinel degrades to the raw turn text rather
	// than failing the phase.
	ExploreSession(ctx context.Context, m Mission) (string, error)
}

// agentStream is the narrow slice of *loop.Agent nativeRunner actually
// calls — kept as an interface so tests can fake it without
// constructing a full production Agent (permissions, audit, outputs,
// event log).
type agentStream interface {
	Start(ctx context.Context, req loop.Request) (<-chan stream.StreamEvent, error)
}

// StorePermissionParker adapts *Store to parkNotifier — the production
// implementation NewNativeRunner is constructed with. Errors are
// logged, never returned: a failed park/clear write must not abort the
// turn it's reporting on.
type StorePermissionParker struct {
	Store *Store
	Log   *slog.Logger
}

// NewStorePermissionParker builds the parkNotifier cmd/brain/main.go
// wires into NewNativeRunner.
func NewStorePermissionParker(store *Store, log *slog.Logger) *StorePermissionParker {
	return &StorePermissionParker{Store: store, Log: log}
}

func (p *StorePermissionParker) OnPermissionParked(ctx context.Context, missionID, permissionID, tool, args, danger, rationale string) {
	if err := p.Store.SetPendingPermission(ctx, missionID, permissionID, tool, args, danger, rationale); err != nil {
		p.Log.Error("mission: record pending permission failed", "mission_id", missionID, "error", err)
	}
}

func (p *StorePermissionParker) OnPermissionCleared(ctx context.Context, missionID string) {
	if err := p.Store.ClearPendingPermission(ctx, missionID); err != nil {
		p.Log.Error("mission: clear pending permission failed", "mission_id", missionID, "error", err)
	}
}

// OnPermissionDenied records that a tool call was denied (D-039: most
// often the automatic unattended-turn denial, but any denied result
// qualifies) — best-effort, same stance as OnPermissionParked/Cleared:
// a failed append logs and never aborts the turn it's reporting on.
func (p *StorePermissionParker) OnPermissionDenied(ctx context.Context, missionID, tool, digest string) {
	if err := p.Store.AppendEvent(ctx, missionID, "mission.permission_denied", map[string]any{
		"tool": tool, "detail": digest,
	}); err != nil {
		p.Log.Error("mission: record permission denied failed", "mission_id", missionID, "error", err)
	}
}

// parkNotifier reports a mission turn parking on (and later clearing)
// a tool-call permission prompt — without this, the same interactive
// broker chat sessions use silently strands a mission worker turn for
// the full 10-minute timeout with nothing telling the UI a decision is
// needed. Backed by *Store in production (SetPendingPermission /
// ClearPendingPermission), faked in tests.
type parkNotifier interface {
	OnPermissionParked(ctx context.Context, missionID, permissionID, tool, args, danger, rationale string)
	OnPermissionCleared(ctx context.Context, missionID string)
	// OnPermissionDenied reports a tool result that came back denied —
	// most often the unattended-turn automatic denial (D-039), but any
	// denial qualifies. Best-effort like the two methods above.
	OnPermissionDenied(ctx context.Context, missionID, tool, digest string)
}

// sandboxExec is the narrow slice of *sandboxclient.Client nativeRunner
// needs — kept as a function type (not an import of sandboxclient) so
// missions has no compile-time dependency on Docker; the driver wires
// the real *sandboxclient.Client.Exec in cmd/brain/main.go. environment
// selects the mission's sandbox image (D-05x) — only matters on the
// mission's first exec, since a container's image is fixed once
// created.
type sandboxExec func(ctx context.Context, missionID, environment, workdir, command string, timeout time.Duration, out io.Writer) (exitCode int, err error)

// nativeRunner is Phase 1's only Runner: every call is one loop.Agent
// turn over the gateway, tagged with the mission's route/review_route
// and MissionID (for ledger attribution).
type nativeRunner struct {
	agent  agentStream
	parker parkNotifier
	log    *slog.Logger
	// modelFloorDeny lists model-name substrings too weak to drive a
	// tool-using mission turn (e.g. "nova-lite", "qwen2.5:7b"); a turn
	// served by one returns ErrModelFloor so the driver pauses fast
	// instead of burning iterations. Empty = floor disabled.
	modelFloorDeny []string
	// sandbox executes worker/reviewer shell commands in a per-mission
	// Docker container instead of brain's own process.
	sandbox sandboxExec
	// kbSearch backs the per-turn kb_search ExtraTool (see kbSearchTool)
	// — nil means kb_search is never offered on any mission turn,
	// regardless of a mission's own Knowledge snapshot (same "the
	// dependency's absence turns the feature off entirely" contract as
	// chat.go's SetKBSearch).
	kbSearch KBSearchFunc

	// kbRead backs the per-turn kb_read ExtraTool — same nil contract
	// as kbSearch.
	kbRead KBReadFunc

	// connectorReads resolves a mission's agent to the read-only
	// connector tools (gmail/calendar reads) it may use on worker/explore
	// turns (see SetConnectorReads) — nil-safe: unset means no connector
	// reads are offered, same as before this existed.
	connectorReads ConnectorReadsResolver
}

// ConnectorReadsResolver resolves an agent id to the read-only
// connector tools a mission turn for that agent may use — the
// intersection of the agent's Tools allowlist and every built
// connector's ReadOnly-marked, non-MCP tools (see
// connectors.Manager.ReadOnlyTools). Connector WRITES are never
// included regardless of the allowlist: mission turns stay
// BuiltinsOnly for the base surface, and this resolver only ever
// layers reads on top via ExtraTools — a worker can never side-channel
// a connector write around the worktree/human-consented push
// pipeline. Resolved at DRIVE time (every RunWorker/ExploreSession
// call), not cached on the mission, so an agent's allowlist or a
// connector's enabled state edited mid-mission applies on the very
// next turn. Being offered a read tool here is separate from being
// pre-approved to call it without asking: these tools are not in
// tools.Permissions' exempt set (same as chat), so an unattended,
// schedule-fired mission (Unattended: true, D-039) still needs a
// standing grant or the call fails fast instead of parking. That grant
// already exists — driver.go's grantSessionDefaults seeds one from the
// mission's agent's ApprovalAllowlist (a separate field from Tools) at
// provisioning time, the same path a chat agent's connector-tool
// grants take. An agent that wants its scheduled missions to actually
// read gmail/calendar unattended must list the tool in BOTH Tools (to
// be offered here) and ApprovalAllowlist (to be pre-approved).
type ConnectorReadsResolver func(ctx context.Context, agentID string) []*tools.Tool

// SetConnectorReads wires the resolver RunWorker/ExploreSession use to
// layer read-only connector tools onto a mission turn — a setter (not
// a NewNativeRunner parameter) because cmd/brain/main.go builds the
// runner before the connectors.Manager it closes over.
func (r *nativeRunner) SetConnectorReads(resolve ConnectorReadsResolver) {
	r.connectorReads = resolve
}

// KBSearchFunc runs one kb_search call scoped to collectionNames — main
// curries memclient.Client.KBSearch in, same shape as chat.go's
// KBSearch type. collectionNames travels on every call (the mission's
// own Knowledge snapshot), never bound once, since the same func serves
// every mission.
type KBSearchFunc func(ctx context.Context, query string, collectionNames []string, mode string, k int) ([]builtin.KBSearchHit, error)

// KBReadFunc loads one kb document scoped to collectionNames — same
// travel-on-every-call contract as KBSearchFunc.
type KBReadFunc func(ctx context.Context, documentID string, collectionNames []string) (builtin.KBDocument, error)

// NewNativeRunner wraps a production *loop.Agent as a Runner. The
// agent instance is expected to be brain's existing chat agent — a
// mission worker turn is just another loop.Agent caller, not a
// separate permission/audit/tool-execution stack. parker may be nil
// (park events are then silently ignored, same as before this existed).
func NewNativeRunner(agent *loop.Agent, parker parkNotifier, log *slog.Logger) Runner {
	return &nativeRunner{agent: agent, parker: parker, log: log}
}

// NewNativeRunnerWithFloor is NewNativeRunner plus a model floor deny
// list (see nativeRunner.modelFloorDeny), a sandbox exec backend (a
// *sandbox.Manager.Exec closure) — worker and reviewer shell calls
// route through it instead of brain's own process — and a kb_search
// backend (nil disables kb_search on every mission turn, matching
// SetKBSearch's nil-safe contract).
// The return type is the unexported *nativeRunner, not Runner: callers
// that need to wire SetConnectorReads (cmd/brain/main.go) must hold the
// concrete type to call it, before handing the result on as a Runner
// to buildDelegatedRunner.
func NewNativeRunnerWithFloor(agent *loop.Agent, parker parkNotifier, floorDeny []string, sandbox sandboxExec, kbSearch KBSearchFunc, kbRead KBReadFunc, log *slog.Logger) *nativeRunner {
	return &nativeRunner{agent: agent, parker: parker, modelFloorDeny: floorDeny, sandbox: sandbox, kbSearch: kbSearch, kbRead: kbRead, log: log}
}

// kbSearchTool builds this turn's kb_search ExtraTool bound to m's own
// Knowledge snapshot, or nil when kb_search must not be offered: no
// backend wired, or the mission's creating agent named no collections
// (empty Knowledge = no kb_search, same opt-in-only contract as
// chat.go's kbSearchTool, which this mirrors exactly). A non-nil sink
// records every returned document at execution time — the harness's
// citation evidence must come from here, not from the rendered tool
// result: digests cap at digestCeiling and offload past 8KiB, so a
// full kb_search result never survives into the digest text.
func (r *nativeRunner) kbSearchTool(m Mission, sink *kbRefSink) *tools.Tool {
	if r.kbSearch == nil || len(m.Knowledge) == 0 {
		return nil
	}
	collections := slices.Clone(m.Knowledge)
	return builtin.KBSearch(func(ctx context.Context, query, mode string, k int) ([]builtin.KBSearchHit, error) {
		hits, err := r.kbSearch(ctx, query, collections, mode, k)
		if err == nil && sink != nil {
			sink.record(hits)
		}
		return hits, err
	})
}

// kbReadTool builds this turn's kb_read ExtraTool, gated exactly like
// kbSearchTool: no backend, or an empty Knowledge snapshot, means the
// tool is not offered.
func (r *nativeRunner) kbReadTool(m Mission) *tools.Tool {
	if r.kbRead == nil || len(m.Knowledge) == 0 {
		return nil
	}
	collections := slices.Clone(m.Knowledge)
	return builtin.KBRead(func(ctx context.Context, documentID string) (builtin.KBDocument, error) {
		return r.kbRead(ctx, documentID, collections)
	})
}

// connectorReadTools resolves m's read-only connector tools (nil
// resolver or no agent id means none) — appended to worker/explore
// ExtraTools so a scheduled mission (daily inbox digest, calendar
// summary) can read gmail/calendar without the base connector surface
// (which BuiltinsOnly excludes) ever reopening for a write.
func (r *nativeRunner) connectorReadTools(ctx context.Context, m Mission) []*tools.Tool {
	if r.connectorReads == nil {
		return nil
	}
	return r.connectorReads(ctx, m.AgentID)
}

// kbRefSink accumulates the kb:// refs of documents kb_search actually
// returned across a worker run's turns. Guarded because the loop may
// execute tool calls concurrently within a turn.
type kbRefSink struct {
	mu   sync.Mutex
	refs []string
}

func (s *kbRefSink) record(hits []builtin.KBSearchHit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range hits {
		s.refs = append(s.refs, "kb://"+h.DocumentID)
	}
}

func (s *kbRefSink) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.refs)
}

// missionTools builds the turn-scoped file tools rooted in the
// mission's OWN directory (worktree for coding, workspace otherwise):
// a shell that replaces the global workspace-rooted one for the turn —
// the root cause of a whole failure family was workers writing into
// the shared root while verify_cmd and the reviewer looked in the
// per-mission directory — and write_file, so artifact writes never go
// through destructive-classified shell redirects. The shell's Runner
// routes commands into the mission's own Docker container (see
// builtin.ShellConfig.Runner) instead of brain's own process.
// sandboxShellMaxTimeout is the mission shell's timeout ceiling when
// backed by a sandbox container — 120s (chat's shell ceiling) is far
// too tight for app-development work: package installs, builds, and
// test suites routinely run longer. The container's own resource caps
// (memory/CPU/pids) bound the damage a long-running command can do, so
// a longer ceiling here is a capability tradeoff, not a safety one.
const sandboxShellMaxTimeout = 15 * time.Minute

// turnTimeout bounds one runTurn call (worker, explore, plan, or
// review). Without this, a stream that never emits an error or
// terminal event — no chunk, no EventDone, no EventError, just
// silence — hangs runTurn's `for ev := range events` forever: nothing
// upstream aborts it, and driveTimeBound (4h) is the mission's own
// lifetime cap, not a per-turn one. Set above sandboxShellMaxTimeout
// (15m) plus headroom for a turn issuing several tool calls in
// sequence (e.g. multiple gmail searches/reads), well under
// driveTimeBound.
// var, not const, so tests can shrink it instead of waiting 20 real minutes.
var turnTimeout = 20 * time.Minute

func (r *nativeRunner) missionTools(m Mission) []*tools.Tool {
	shell := r.missionShell(m)
	if shell == nil {
		return nil
	}
	return []*tools.Tool{
		shell,
		builtin.WriteFile(builtin.WriteFileConfig{Root: m.WorkRoot()}),
	}
}

// missionShell builds the turn-scoped shell tool rooted in the
// mission's own directory, routed through the mission's sandbox
// container — shared by missionTools (worker/reviewer, paired with
// write_file) and ExploreSession (shell only, read-only exploration:
// the execute phase does the actual work, so explore never gets
// write_file). Returns nil when the mission has no work root yet
// (WorkRoot's Workspace/Worktree both empty).
func (r *nativeRunner) missionShell(m Mission) *tools.Tool {
	root := m.WorkRoot()
	if root == "" {
		return nil
	}
	missionID, environment, workdir := m.ID, m.Environment, root
	shellCfg := builtin.ShellConfig{
		WorkspaceRoot: root,
		MaxTimeout:    sandboxShellMaxTimeout,
		Runner: func(ctx context.Context, command string, timeout time.Duration) (string, error) {
			var out strings.Builder
			capped := &cappedStringWriter{w: &out, max: shellOutputCap}
			exitCode, err := r.sandbox(ctx, missionID, environment, workdir, command, timeout, capped)
			if err != nil {
				// The sandbox backend's contract mirrors runShell's: a
				// timeout comes back as an error, everything else
				// (including a non-zero exit reached via the returned
				// exitCode) does not.
				return out.String(), err
			}
			result := out.String()
			if capped.truncated {
				result += "\n[output capped]"
			}
			if exitCode != 0 {
				result = fmt.Sprintf("%s\n(exit status %d)", result, exitCode)
			}
			return result, nil
		},
	}
	return builtin.Shell(shellCfg)
}

// shellOutputCap mirrors builtin.Shell's own output cap — the sandbox
// backend's Runner must behave identically to the in-process path, not
// let a runaway sandboxed command balloon memory.
const shellOutputCap = 64 << 10

// cappedStringWriter stops retaining bytes past max (writes still
// succeed so the underlying exec can finish) — the sandbox-Runner
// analog of builtin.capWriter, kept separate because that type is
// unexported in the builtin package.
type cappedStringWriter struct {
	w         *strings.Builder
	max       int
	truncated bool
}

func (c *cappedStringWriter) Write(p []byte) (int, error) {
	if room := c.max - c.w.Len(); room > 0 {
		if len(p) > room {
			c.w.Write(p[:room])
			c.truncated = true
		} else {
			c.w.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

// belowFloor reports whether model matches the deny list.
func (r *nativeRunner) belowFloor(model string) bool {
	for _, deny := range r.modelFloorDeny {
		if deny != "" && strings.Contains(strings.ToLower(model), strings.ToLower(deny)) {
			return true
		}
	}
	return false
}

// turnResult is runTurn's outcome: the full assistant text, the
// sentinel tool call's raw arguments (if any), URLs the turn's
// web_search/web_fetch calls saw, and the final assistant segment
// (see finalSeg's doc below, formerly a bare 5th return value).
type turnResult struct {
	text         string
	sentinelArgs json.RawMessage
	seenURLs     []string
	finalSeg     string
}

// runTurn drives one loop.Agent session to completion, capturing the
// text of the sentinel tool call named toolName (if any) alongside the
// full assistant text. It does not itself decide what a missing
// sentinel means — that's the caller's job (worker vs review have
// different enforcement/parsing needs).
//
// finalSeg is the assistant text written since the last non-sentinel
// tool activity (EventToolEnd/EventToolResult) — a session can run
// several internal tool-calling rounds before its sentinel call, and
// full is every chunk across all of them (draft narration, tool-retry
// commentary included), while finalSeg is just what the model wrote
// right before ending its turn. The sentinel call itself never resets
// finalSeg (it carries no assistant text of its own to lose).
func (r *nativeRunner) runTurn(ctx context.Context, req loop.Request, sentinelTool string) (turnResult, error) {
	ctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	// D-075: the sentinel call's successful execution ends the turn —
	// every mission phase (worker/explore/plan/review) goes through
	// this one call site.
	req.EndTurnTools = []string{sentinelTool}
	events, err := r.agent.Start(ctx, req)
	if err != nil {
		return turnResult{}, err
	}
	var b, finalB strings.Builder
	var sentinelArgs json.RawMessage
	var seenURLs []string
	// parked tracks in-flight parks by CallID, not a single flag — a
	// turn can issue concurrent tool calls (executeAll runs up to
	// maxParallelTools at once), so a sibling call finishing first must
	// not be mistaken for the still-blocked destructive one resolving.
	parked := map[string]bool{}
	servedModel := ""
	// sawTerminal mirrors chat's D-044 discipline: a channel that
	// closes with no done/error/incomplete means every producer lost
	// its terminal (deadline racing a stream cut). Without this check a
	// silent cut returned a clean empty verdict, which the caller read
	// as a missing sentinel and burned a recovery re-run plus a forced
	// retry — two full model turns for one infra failure.
	sawTerminal := false
	for ev := range events {
		switch ev.Type {
		case stream.EventChunk:
			b.WriteString(ev.Text)
			finalB.WriteString(ev.Text)
		case stream.EventToolEnd:
			if ev.ToolCall != nil && ev.ToolCall.Name == sentinelTool {
				sentinelArgs = ev.ToolCall.Input
			}
			if ev.ToolCall != nil && ev.ToolCall.Name == "web_fetch" {
				seenURLs = append(seenURLs, webFetchArgURL(ev.ToolCall.Input)...)
			}
			// Any tool call OTHER than the sentinel is mid-turn activity —
			// the text written before it is stale narration, not the
			// deliverable. The sentinel call itself carries no assistant
			// text of its own, so it must not reset finalSeg.
			if ev.ToolCall != nil && ev.ToolCall.Name != sentinelTool {
				finalB.Reset()
			}
		case stream.EventPermissionRequest:
			if r.parker != nil && ev.Permission != nil {
				parked[ev.Permission.CallID] = true
				r.parker.OnPermissionParked(ctx, req.MissionID, ev.Permission.ID, ev.Permission.Tool,
					ev.Permission.Args, ev.Permission.Danger, ev.Permission.Rationale)
			}
		case stream.EventToolResult:
			// Only the specific call that parked clears it — and only
			// once every parked call in this turn has resolved does the
			// mission stop reporting a pending permission.
			if ev.ToolResult != nil && parked[ev.ToolResult.ID] {
				delete(parked, ev.ToolResult.ID)
				if len(parked) == 0 && r.parker != nil {
					r.parker.OnPermissionCleared(ctx, req.MissionID)
				}
			}
			// A denied result — most often the unattended-turn automatic
			// denial (D-039), which never emits EventPermissionRequest at
			// all — is otherwise invisible to anything watching the
			// mission; record it as an event.
			if ev.ToolResult != nil && ev.ToolResult.Status == "denied" && r.parker != nil {
				r.parker.OnPermissionDenied(ctx, req.MissionID, ev.ToolResult.Name, ev.ToolResult.Digest)
			}
			if ev.ToolResult != nil && ev.ToolResult.Status == "ok" && ev.ToolResult.Name == "web_search" {
				seenURLs = append(seenURLs, webSearchResultURLs(ev.ToolResult.Digest)...)
			}
			if ev.ToolResult != nil && ev.ToolResult.Name != sentinelTool {
				finalB.Reset()
			}
		case stream.EventDone:
			sawTerminal = true
			if ev.Meta != nil {
				servedModel = ev.Meta.Model
			}
		case stream.EventIncomplete:
			// A cut-off stream is an infra failure, not a short clean
			// answer — without this case it was indistinguishable from
			// one and flowed into sentinel parsing. Both this and the
			// error case return directly, so only done needs to mark
			// sawTerminal.
			if len(parked) > 0 && r.parker != nil {
				r.parker.OnPermissionCleared(ctx, req.MissionID)
			}
			return turnResult{b.String(), sentinelArgs, seenURLs, finalB.String()}, fmt.Errorf("mission runner: incomplete stream: %s", ev.Text)
		case stream.EventError:
			if len(parked) > 0 && r.parker != nil {
				r.parker.OnPermissionCleared(ctx, req.MissionID)
			}
			msg := "provider stream error"
			if ev.Err != nil {
				msg = ev.Err.Message
			}
			return turnResult{b.String(), sentinelArgs, seenURLs, finalB.String()}, fmt.Errorf("mission runner: %s", msg)
		}
	}
	if !sawTerminal {
		if len(parked) > 0 && r.parker != nil {
			r.parker.OnPermissionCleared(ctx, req.MissionID)
		}
		return turnResult{b.String(), sentinelArgs, seenURLs, finalB.String()}, fmt.Errorf("mission runner: stream ended without a terminal event")
	}
	if servedModel != "" && r.belowFloor(servedModel) {
		return turnResult{b.String(), sentinelArgs, seenURLs, finalB.String()}, fmt.Errorf("%w: %s", ErrModelFloor, servedModel)
	}
	return turnResult{b.String(), sentinelArgs, seenURLs, finalB.String()}, nil
}

// webFetchArgURL extracts the url arg from a web_fetch call's raw
// input — the exact field name webfetch.go's webFetchArgs decodes
// (D-059). A malformed input yields nothing rather than an error: this
// is best-effort evidence collection, not argument validation (the
// tool's own Execute already rejected a bad call before this point).
func webFetchArgURL(input json.RawMessage) []string {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(input, &args); err != nil || args.URL == "" {
		return nil
	}
	return []string{args.URL}
}

// webSearchResultURLs pulls result URLs out of web_search's rendered
// output — websearch.go's runSearch emits one blank-line-separated
// entry per result as "N. title\nurl\nsnippet" (D-059). Line 2 of each
// three-line group is the URL; anything not matching that shape (the
// "no results found" sentinel, a truncated tail) is skipped rather
// than guessed at. Best-effort only: the digest is capped (and big
// results offload to a stub), so URLs past the cap are lost. That is
// deliberate lenience on top of the reliable contract — cite what you
// web_fetch, whose args are never truncated.
var webSearchResultHeader = regexp.MustCompile(`^\d+\.\s`)

func webSearchResultURLs(digest string) []string {
	lines := strings.Split(digest, "\n")
	var urls []string
	for i := 0; i+1 < len(lines); i++ {
		// A result's first line is "N. title" — only its second line
		// (the URL) is collected.
		if !webSearchResultHeader.MatchString(lines[i]) {
			continue
		}
		candidate := strings.TrimSpace(lines[i+1])
		if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
			urls = append(urls, candidate)
		}
	}
	return urls
}

// workerRoute picks the route a worker turn runs on. With an
// escalation route configured, any evidence the current model is not
// cutting it — a worker failure or a review rework already on the
// books — switches subsequent worker turns to it. Empty escalation
// route means the ladder is off and the mission's own route always
// wins.
func workerRoute(m Mission) string {
	if m.EscalationRoute != "" && (m.ConsecutiveFailures > 0 || m.StallCount > 0) {
		return m.EscalationRoute
	}
	return m.Route
}

// oversightRoute picks the route explore/plan/replan turns run on:
// PlanRoute when set ("GLM plans, local executes" — oversight runs on a
// stronger route while Route stays the worker's cheap/local route),
// otherwise Route unchanged (empty PlanRoute is exact current behavior).
func oversightRoute(m Mission) string {
	if m.PlanRoute != "" {
		return m.PlanRoute
	}
	return m.Route
}

// reviewRoute picks the route a review turn runs on. Precedence:
// ReviewRoute (the existing, already-shipped review-only override) >
// PlanRoute (review is an oversight phase too) > Route.
func reviewRoute(m Mission) string {
	if m.ReviewRoute != "" {
		return m.ReviewRoute
	}
	return oversightRoute(m)
}

// RunWorker seeds a fresh session (packet only, no prior transcript)
// and enforces the sentinel ladder: a present, well-formed
// mission_status call is trusted directly; a missing or invalid one
// triggers one recovery re-run, then falls back to bail detection —
// either path that doesn't yield a trusted verdict becomes a forced
// retry, never a silent accept.
func (r *nativeRunner) RunWorker(ctx context.Context, m Mission, packet WorkPacket) (WorkerVerdict, string, error) {
	system, user := packet.Render()
	kbSeen := &kbRefSink{}
	extra := append([]*tools.Tool{MissionStatusTool()}, r.missionTools(m)...)
	if t := r.kbSearchTool(m, kbSeen); t != nil {
		extra = append(extra, t)
	}
	if t := r.kbReadTool(m); t != nil {
		extra = append(extra, t)
	}
	extra = append(extra, r.connectorReadTools(ctx, m)...)
	req := loop.Request{
		SessionID:    m.SessionID,
		Route:        workerRoute(m),
		Agent:        "mission-worker",
		MissionID:    m.ID,
		System:       system,
		Messages:     []provider.Message{{Role: "user", Content: user}},
		ExtraTools:   extra,
		BuiltinsOnly: true,
		// Schedule-fired missions have nobody watching: asks fail fast
		// with feedback instead of parking (D-039). UI-created missions
		// (ScheduleID empty) keep the park-and-answer flow.
		Unattended: m.ScheduleID != "",
	}

	res, err := r.runTurn(ctx, req, missionStatusToolName)
	text, seenURLs := res.text, res.seenURLs
	if err != nil {
		return WorkerVerdict{}, text, err
	}
	if v, ok := r.tryParseVerdict(res.sentinelArgs); ok {
		v.SeenURLs = append(seenURLs, kbSeen.all()...)
		v.FinalMessage = res.finalSeg
		return v, text, nil
	}

	// Recovery re-run: inject a system message demanding the call.
	recoverReq := req
	recoverReq.Messages = append(append([]provider.Message{}, req.Messages...),
		provider.Message{Role: "assistant", Content: text},
		provider.Message{Role: "user", Content: "[system] You must end your turn with exactly one mission_status tool call: done, retry, or blocked."},
	)
	recoverRes, err := r.runTurn(ctx, recoverReq, missionStatusToolName)
	recoverText := recoverRes.text
	seenURLs = append(seenURLs, recoverRes.seenURLs...)
	if err != nil {
		return WorkerVerdict{}, text + "\n" + recoverText, err
	}
	if v, ok := r.tryParseVerdict(recoverRes.sentinelArgs); ok {
		v.SeenURLs = append(seenURLs, kbSeen.all()...)
		v.FinalMessage = recoverRes.finalSeg
		return v, text + "\n" + recoverText, nil
	}

	// Neither turn produced a tool call: before giving up, check whether
	// the model expressed the sentinel as TEXT instead — observed on
	// non-frontier models (GLM-5.2's XML-ish self-closing tag, qwen3:30b's
	// bare "mission_status" token followed by a JSON object). A text-form
	// verdict is trust-equivalent to the tool-call form: both are the
	// model's own self-report, and the harness's own verify_cmd/
	// CheckArtifacts evidence — never model output — is what actually
	// gates a unit's Passes flag. This is strictly a fallback: the
	// tool-call path above always wins when it succeeds.
	combined := text + "\n" + recoverText
	if raw, ok := extractTextSentinel(combined, missionStatusToolName); ok {
		if v, ok := r.tryParseVerdict(raw); ok {
			r.log.Warn("mission worker expressed mission_status as text, not a tool call", "mission_id", m.ID)
			v.SeenURLs = append(seenURLs, kbSeen.all()...)
			v.FinalMessage = recoverRes.finalSeg
			return v, combined, nil
		}
	}

	// Still missing: bail detection informs the log, but either way this
	// is a forced retry — detection accuracy never gates the outcome.
	if detectBail(combined) {
		r.log.Warn("mission worker bailed without a sentinel call", "mission_id", m.ID)
	} else {
		r.log.Warn("mission worker ended without a sentinel call", "mission_id", m.ID)
	}
	return forcedRetryVerdict("the worker did not report a status; treated as a failed attempt"), combined, nil
}

func (r *nativeRunner) tryParseVerdict(args json.RawMessage) (WorkerVerdict, bool) {
	return tryParseWorkerVerdict(args)
}

// tryParseWorkerVerdict decodes args into a WorkerVerdict, ok=false on
// empty input or a missing outcome — shared by nativeRunner's sentinel
// ladder and delegatedRunner's result ladder (delegated.go), since both
// read a mission_status-shaped payload from a text-form sentinel.
func tryParseWorkerVerdict(args json.RawMessage) (WorkerVerdict, bool) {
	if len(args) == 0 {
		return WorkerVerdict{}, false
	}
	v, err := parseWorkerVerdict(args)
	if err != nil || v.Outcome == "" {
		return WorkerVerdict{}, false
	}
	return v, true
}

// D-074: forcedRetryVerdict builds the fabricated "retry" verdict every
// last rung of the worker/executor verdict ladders falls back to when
// NEITHER a tool call/schema result NOR a text-form sentinel could be
// read — shared by nativeRunner's sentinel ladder (RunWorker, above)
// and delegatedRunner's result ladder (delegated.go's attemptResume,
// pollToVerdict's idle-kill, finish, finishNoResult), which previously
// each spelled out WorkerVerdict{Outcome: "retry", Forced: true, ...}
// with their own Analysis string. reason is that Analysis: what the
// ladder observed (or failed to observe) that forced this fallback.
func forcedRetryVerdict(reason string) WorkerVerdict {
	return WorkerVerdict{Outcome: "retry", Forced: true, Analysis: reason}
}

// ExploreSession runs the mission's explore turn: explore the goal
// before planning commits to a shape. Unlike RunWorker's sentinel
// ladder, a missing explore_notes call never fails the phase — the
// findings are advisory input to the planner, not a gate on progress,
// so the ladder's last resort is the raw turn text rather than a forced
// failure.
func (r *nativeRunner) ExploreSession(ctx context.Context, m Mission) (string, error) {
	system := "You are exploring one mission before it is planned. Investigate the goal: explore the workspace with shell (read-only — do not create or modify files; the execute phase does the actual work), and use web search/fetch tools if available and relevant to the goal. If the goal is self-contained and needs no exploration, say so briefly. End your turn with exactly one explore_notes tool call whose findings field contains everything the planner needs: what exists, what's relevant, constraints, gotchas, unknowns." + toolDisciplineNote + execEnvironmentNote()
	user := "Goal: " + NeutralizeSlot(m.Goal)
	if m.ParentContext != "" {
		user += "\n\nPrevious mission outcome:\n" + NeutralizeSlot(m.ParentContext)
	}
	user += renderAttachments(m.Attachments)

	extra := []*tools.Tool{ExploreNotesTool()}
	if shell := r.missionShell(m); shell != nil {
		extra = append(extra, shell)
	}
	if t := r.kbSearchTool(m, nil); t != nil {
		extra = append(extra, t)
	}
	if t := r.kbReadTool(m); t != nil {
		extra = append(extra, t)
	}
	extra = append(extra, r.connectorReadTools(ctx, m)...)
	req := loop.Request{
		SessionID:    m.SessionID,
		Route:        oversightRoute(m),
		Agent:        "mission-explorer",
		MissionID:    m.ID,
		System:       system,
		Messages:     []provider.Message{{Role: "user", Content: user}},
		ExtraTools:   extra,
		BuiltinsOnly: true,
		Unattended:   m.ScheduleID != "",
	}

	res, err := r.runTurn(ctx, req, exploreNotesToolName)
	text := res.text
	if err != nil {
		return "", err
	}
	if notes, ok := tryParseFindings(res.sentinelArgs); ok {
		return notes, nil
	}

	// One recovery re-run, same ladder shape as RunWorker/PlanSession.
	recoverReq := req
	recoverReq.Messages = append(append([]provider.Message{}, req.Messages...),
		provider.Message{Role: "assistant", Content: text},
		provider.Message{Role: "user", Content: "[system] You must end your turn with exactly one explore_notes tool call containing your findings."},
	)
	recoverRes, err := r.runTurn(ctx, recoverReq, exploreNotesToolName)
	recoverText := recoverRes.text
	if err != nil {
		return "", err
	}
	if notes, ok := tryParseFindings(recoverRes.sentinelArgs); ok {
		return notes, nil
	}

	// Neither turn produced a tool call: check for a text-form sentinel
	// before giving up on structured output entirely.
	combined := text + "\n" + recoverText
	if raw, ok := extractTextSentinel(combined, exploreNotesToolName); ok {
		if notes, ok := tryParseFindings(raw); ok {
			return notes, nil
		}
	}

	// Advisory phase: never a forced failure. The raw turn text is still
	// useful context for the planner even with no structured findings.
	r.log.Warn("mission explore ended without an explore_notes call; using raw turn text", "mission_id", m.ID)
	return strings.TrimSpace(combined), nil
}

// tryParseFindings reports whether args decodes to a non-empty findings
// string.
func tryParseFindings(args json.RawMessage) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	findings, err := parseExploreFindings(args)
	if err != nil || findings == "" {
		return "", false
	}
	return findings, true
}

// RunReview judges the packet: the mission's goal and plan, the
// harness-read artifact contents (never the worker's description of
// them), the baseline diff when one exists, and the worker's evidence
// last. gatekeeper carries prior messages to resume the same reviewer
// session on rework (a "delta recheck" instead of cold-reanalyzing);
// nil starts fresh.
func (r *nativeRunner) RunReview(ctx context.Context, m Mission, packet ReviewPacket, gatekeeper *GatekeeperState) (ReviewVerdict, *GatekeeperState, error) {
	system := "You are reviewing one unit of a mission's work. The mission goal, the plan, and the actual artifact contents (read from disk by the harness, not reported by the worker) are all below — judge against THEM. Look for real reasons to reject before approving: the artifact not satisfying the goal, unsupported claims, missing substance. Do NOT reject for material you were not given (the harness supplies everything there is). End your turn with exactly one review_verdict tool call."
	content := renderReviewContent(packet)

	var messages []provider.Message
	if gatekeeper != nil {
		messages = append(messages, gatekeeper.Messages...)
	}
	messages = append(messages, provider.Message{Role: "user", Content: content})

	extra := append([]*tools.Tool{ReviewVerdictTool()}, r.missionTools(m)...)
	req := loop.Request{
		SessionID:    m.SessionID,
		Route:        reviewRoute(m),
		Agent:        "mission-reviewer",
		MissionID:    m.ID,
		System:       system,
		Messages:     messages,
		ExtraTools:   extra,
		BuiltinsOnly: true,
		Unattended:   m.ScheduleID != "",
	}
	res, err := r.runTurn(ctx, req, reviewVerdictToolName)
	text, args := res.text, res.sentinelArgs
	if err != nil {
		return ReviewVerdict{}, nil, err
	}
	if len(args) == 0 {
		// Recovery re-run: same one-shot ladder RunWorker uses — a
		// reviewer that ran long on analysis and didn't reach its tool
		// call gets one more turn demanding it, instead of failing the
		// whole review round outright.
		recoverReq := req
		recoverReq.Messages = append(append([]provider.Message{}, req.Messages...),
			provider.Message{Role: "assistant", Content: text},
			provider.Message{Role: "user", Content: "[system] You must end your turn with exactly one review_verdict tool call: approve or rework."},
		)
		recoverRes, err := r.runTurn(ctx, recoverReq, reviewVerdictToolName)
		recoverText, recoverArgs := recoverRes.text, recoverRes.sentinelArgs
		if err != nil {
			return ReviewVerdict{}, nil, err
		}
		if len(recoverArgs) == 0 {
			// Neither turn produced a review_verdict tool call: before
			// failing the round, check for a text-form verdict — observed
			// on qwen3:30b reviewers that write prose analysis and never
			// call the tool. Trust-equivalent to the tool-call form (see
			// RunWorker's identical fallback above): the harness's own
			// artifact/diff evidence, not the reviewer's self-report,
			// decides whether a unit actually holds up. A text-form rework
			// with no parseable findings is acceptable — GapFingerprint of
			// empty findings is empty, which the state machine already
			// tolerates.
			combined := text + "\n" + recoverText
			if raw, ok := extractTextSentinel(combined, reviewVerdictToolName); ok {
				r.log.Warn("mission reviewer expressed review_verdict as text, not a tool call", "mission_id", m.ID)
				text, args = combined, raw
			} else {
				return ReviewVerdict{}, nil, fmt.Errorf("mission runner: reviewer ended without a review_verdict call")
			}
		} else {
			text, args = text+"\n"+recoverText, recoverArgs
		}
	}
	verdict, err := parseReviewVerdict(args)
	if err != nil {
		return ReviewVerdict{}, nil, fmt.Errorf("mission runner: parse review_verdict: %w", err)
	}

	nextState := &GatekeeperState{Messages: append(messages, provider.Message{Role: "assistant", Content: NeutralizeSlot(text)})}
	return verdict, nextState, nil
}

// renderReviewContent lays the packet out for the reviewer: goal and
// plan first (context), harness-read artifacts and diff in the middle
// (the evidence that counts), the worker's own account last (the
// least trustworthy part). All model-produced text passes through
// NeutralizeSlot.
func renderReviewContent(p ReviewPacket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mission goal: %s\n", NeutralizeSlot(p.Goal))
	if p.UnitTitle != "" {
		fmt.Fprintf(&b, "Unit under review: %s\n", NeutralizeSlot(p.UnitTitle))
	}
	if len(p.Plan.Units) > 0 {
		b.WriteString("\nPlan:\n")
		for _, u := range p.Plan.Units {
			status := "pending"
			if u.Passes {
				status = "verified"
			}
			fmt.Fprintf(&b, "- [%s] %s\n", status, NeutralizeSlot(u.Title))
		}
	}
	if p.Listing != "" {
		b.WriteString("\nWorkspace files:\n")
		b.WriteString(p.Listing)
	}
	if len(p.Artifacts) > 0 {
		paths := make([]string, 0, len(p.Artifacts))
		for path := range p.Artifacts {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		b.WriteString("\nArtifact contents (read from disk by the harness):\n")
		for _, path := range paths {
			fmt.Fprintf(&b, "\n--- %s ---\n%s\n", path, NeutralizeSlot(p.Artifacts[path]))
		}
	}
	if p.Diff != "" {
		b.WriteString("\nDiff to review:\n")
		b.WriteString(p.Diff)
		b.WriteString("\n")
	}
	if p.Evidence != "" {
		b.WriteString("\nWorker's own report (verify against the artifacts above, do not take at face value):\n")
		b.WriteString(NeutralizeSlot(p.Evidence))
		b.WriteString("\n")
	}
	return b.String()
}

// PlanSession runs the planning turn that produces a Spec from the
// mission's goal and explore-phase findings. The plan is forced
// through the submit_plan tool call (mirroring RunWorker/RunReview's
// sentinel ladder) rather than asked for as bare JSON prose — a model
// free to preface its reply with prose was the root cause of a real
// stuck mission (5 straight "invalid plan JSON" failures, identical
// each retry since nothing told the model what went wrong).
func (r *nativeRunner) PlanSession(ctx context.Context, m Mission, exploreNotes string) (Spec, error) {
	system := "You are planning one mission. Break the goal into the SMALLEST ordered list of verifiable units that achieves it — one unit is correct for a simple goal; never pad the plan. Every unit must list at least one artifact — the workspace-relative file(s) the unit must produce (for a report-style goal, the report file itself is the artifact); the harness itself checks each exists and is non-empty, so name the real deliverables. verify_cmd is executed literally as `/bin/sh -c \"<verify_cmd>\"` in the mission's own workspace directory — it must be a real POSIX shell command (using binaries like grep, test, wc — NOT a tool name from your own tool list, which does not exist as a shell command) and must check the CONTENT of the artifacts (e.g. grep -qi 'retry-after' summary.md), never a bare echo, which proves nothing. Never use command substitution ($(...) or backticks) in verify_cmd — write the direct command instead; for a line-count check use awk, e.g. `awk 'END{exit NR<10}' report.md`, NEVER `test $(wc -l ...)`. Use paths relative to the workspace; never /tmp or any absolute path outside it, since the worker's shell is confined to the workspace. End your turn with exactly one submit_plan tool call." + r.execEnvironmentNote()
	user := "Goal: " + NeutralizeSlot(m.Goal)
	if exploreNotes != "" {
		user += "\n\nExploration findings:\n" + NeutralizeSlot(exploreNotes)
	}
	if m.ParentContext != "" {
		user += "\n\nPrevious mission outcome:\n" + NeutralizeSlot(m.ParentContext)
	}
	user += renderAttachments(m.Attachments)
	extra := []*tools.Tool{PlanTool()}
	if t := r.kbSearchTool(m, nil); t != nil {
		extra = append(extra, t)
	}
	if t := r.kbReadTool(m); t != nil {
		extra = append(extra, t)
	}
	req := loop.Request{
		SessionID: m.SessionID,
		Route:     oversightRoute(m),
		Agent:     "mission-planner",
		MissionID: m.ID,
		System:    system,
		Messages:  []provider.Message{{Role: "user", Content: user}},
		// The planner plans; it must not act. No base tool matches this
		// allowlist (submit_plan/kb_search arrive via ExtraTools, which
		// bypass it, and kb_search is read-only — never "acting"), so
		// the turn's only BASE tool is none — a planner that reaches for
		// shell parked a live canary on the permission gate for ten
		// minutes trying to do the worker's job in plan phase.
		ToolAllow:    []string{planToolName},
		ExtraTools:   extra,
		BuiltinsOnly: true,
		Unattended:   m.ScheduleID != "",
	}
	// Force submit_plan only when it is the turn's sole tool (D-063):
	// a KB-attached mission also offers kb_search/kb_read here, and a
	// forced choice would make consulting them impossible.
	if len(extra) == 1 {
		req.ForceTool = planToolName
	}
	res, err := r.runTurn(ctx, req, planToolName)
	text, args := res.text, res.sentinelArgs
	if err != nil {
		return Spec{}, err
	}
	if len(args) > 0 {
		if spec, specErr := parseSpec(string(args)); specErr == nil {
			return spec, nil
		}
	}

	// Recovery re-run: either the tool call was missing entirely, or its
	// arguments didn't decode as a valid Spec — either way, tell the
	// model exactly what was wrong instead of silently repeating the
	// identical prompt (which previously produced the identical failure
	// on every retry).
	var recoverReason string
	if len(args) == 0 {
		recoverReason = "you did not call submit_plan"
	} else if _, specErr := parseSpec(string(args)); specErr != nil {
		recoverReason = specErr.Error()
	}
	recoverReq := req
	recoverReq.Messages = append(append([]provider.Message{}, req.Messages...),
		provider.Message{Role: "assistant", Content: text},
		provider.Message{Role: "user", Content: "[system] Your last turn did not produce a usable plan: " + recoverReason + ". You must end this turn with exactly one submit_plan tool call."},
	)
	recoverRes, err := r.runTurn(ctx, recoverReq, planToolName)
	recoverText, recoverArgs := recoverRes.text, recoverRes.sentinelArgs
	if err != nil {
		return Spec{}, err
	}
	if len(recoverArgs) == 0 {
		r.log.Warn("mission planner ended without a submit_plan call", "mission_id", m.ID, "text", text, "recover_text", recoverText)
		return Spec{}, fmt.Errorf("mission runner: planner ended without a submit_plan call")
	}
	spec, err := parseSpec(string(recoverArgs))
	if err != nil {
		r.log.Warn("mission planner submitted an invalid plan twice", "mission_id", m.ID, "text", text, "recover_text", recoverText, "error", err)
		return Spec{}, err
	}
	return spec, nil
}

// execEnvironmentNote tells the planner what execution environment
// verify_cmd and shell commands actually run in — without this, a
// planner with no sandbox has no way to know whether e.g. python3
// exists, and can author a verify_cmd for a runtime that was never
// there (the root cause of a real stuck mission: a plan's verify_cmd
// assumed Python in an environment that had none). WorkPacket.Render
// carries the same text to the worker via ExecEnvironmentNote.
func (r *nativeRunner) execEnvironmentNote() string {
	return execEnvironmentNote()
}

// execEnvironmentNote is the shared wording nativeRunner (planner
// prompt) and Driver (worker packet) both need — kept as one function
// so the two prompts never drift out of sync about what's actually
// available.
func execEnvironmentNote() string {
	// The date line rides along so every mission prompt (explore, plan,
	// worker) knows "today" — a model with no other way to know it
	// anchors on training data or on dated examples in tool descriptions
	// and mangles date-bounded calls (calendar windows, Gmail
	// after:/before:). Date only, no clock time, for the same
	// prompt-cache reason as chat's date line (D-018).
	return " Commands run inside an isolated Linux container with python3, node, git, and standard POSIX/coreutils tools available; each mission gets its own container, state persists across your commands within the mission. Today is " + time.Now().UTC().Format("Monday, 2006-01-02") + " (UTC)."
}

// parseSpec decodes the planner's reply strictly: fences stripped,
// unknown fields rejected — same discipline as loop/turnmemory.go's
// distillation parsing.
func parseSpec(raw string) (Spec, error) {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var spec Spec
	if err := dec.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("mission runner: invalid plan JSON: %w", err)
	}
	if len(spec.Units) == 0 {
		return Spec{}, fmt.Errorf("mission runner: plan has no units")
	}
	// verify_cmd runs through RunVerify (a plain /bin/sh -c) — harness-
	// side, outside the permission chain entirely, so D-050's sandbox
	// relaxation (which only changes how a worker/reviewer's own shell
	// TOOL CALL is classified) has no bearing here. The rejection below
	// is a determinism concern, not a permission one: verify_cmd must
	// check the CONTENT of declared artifacts reproducibly, and command
	// substitution invites exactly the kind of "prove nothing" or
	// environment-dependent check (e.g. $(date) in the expected value)
	// that undermines that. Reject it here, at the same point the
	// empty-plan check already rejects a bad plan, so the planner's
	// retry loop sees the real problem immediately.
	for _, u := range spec.Units {
		if strings.Contains(u.VerifyCmd, "$(") || strings.Contains(u.VerifyCmd, "`") {
			return Spec{}, fmt.Errorf("mission runner: verify_cmd must not use command substitution ($(...) or backticks) — write the direct command instead")
		}
	}
	// D-068: every unit must declare at least one artifact so the
	// harness's CheckArtifacts has something to verify.
	for _, u := range spec.Units {
		if len(u.Artifacts) == 0 {
			return Spec{}, fmt.Errorf("mission runner: unit %q must list at least one workspace-relative artifact file the harness can check (for a report-style goal, the report file itself is the artifact)", u.Title)
		}
	}
	// D-068: reject verify_cmds that succeed regardless of outcome.
	// Deny-set on the first shell word only, deliberately not a shell
	// parser; a no-op buried after && is out of scope.
	for _, u := range spec.Units {
		cmd := strings.TrimSpace(u.VerifyCmd)
		if cmd == "" {
			return Spec{}, fmt.Errorf("mission runner: unit %q must have a verify_cmd that checks the CONTENT of its artifacts (e.g. grep) — a bare echo, true, :, or printf proves nothing", u.Title)
		}
		firstWord := cmd
		if i := strings.IndexAny(cmd, " \t"); i >= 0 {
			firstWord = cmd[:i]
		}
		switch firstWord {
		case "echo", "true", ":", "printf":
			return Spec{}, fmt.Errorf("mission runner: unit %q verify_cmd must check the CONTENT of its artifacts (e.g. grep) — a bare echo, true, :, or printf proves nothing", u.Title)
		}
	}
	// D-068: verify_cmd must parse as POSIX shell (-n never executes).
	// Skip silently if /bin/sh is missing so tests stay hermetic.
	if shPath, err := exec.LookPath("/bin/sh"); err == nil {
		for _, u := range spec.Units {
			shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			out, err := exec.CommandContext(shCtx, shPath, "-n", "-c", u.VerifyCmd).CombinedOutput() //nolint:gosec // G204: shPath is LookPath("/bin/sh"); -n parses only, never executes
			cancel()
			if err != nil {
				stderr := strings.TrimSpace(string(out))
				if len(stderr) > 200 {
					stderr = stderr[:200]
				}
				return Spec{}, fmt.Errorf("mission runner: unit %q verify_cmd does not parse as POSIX shell: %s", u.Title, stderr)
			}
		}
	}
	// Passes is harness-only evidence (RunVerify); a plan is never born
	// pre-verified regardless of what the planner's JSON claims.
	for i := range spec.Units {
		spec.Units[i].Passes = false
	}
	return spec, nil
}
