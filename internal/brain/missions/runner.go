package missions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

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
	// of PlanUnits) from the mission's goal and research-phase findings.
	PlanSession(ctx context.Context, m Mission, researchNotes string) (Spec, error)
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

// parkNotifier reports a mission turn parking on (and later clearing)
// a tool-call permission prompt — without this, the same interactive
// broker chat sessions use silently strands a mission worker turn for
// the full 10-minute timeout with nothing telling the UI a decision is
// needed. Backed by *Store in production (SetPendingPermission /
// ClearPendingPermission), faked in tests.
type parkNotifier interface {
	OnPermissionParked(ctx context.Context, missionID, permissionID, tool, args, danger, rationale string)
	OnPermissionCleared(ctx context.Context, missionID string)
}

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
}

// NewNativeRunner wraps a production *loop.Agent as a Runner. The
// agent instance is expected to be brain's existing chat agent — a
// mission worker turn is just another loop.Agent caller, not a
// separate permission/audit/tool-execution stack. parker may be nil
// (park events are then silently ignored, same as before this existed).
func NewNativeRunner(agent *loop.Agent, parker parkNotifier, log *slog.Logger) Runner {
	return &nativeRunner{agent: agent, parker: parker, log: log}
}

// NewNativeRunnerWithFloor is NewNativeRunner plus a model floor deny
// list (see nativeRunner.modelFloorDeny).
func NewNativeRunnerWithFloor(agent *loop.Agent, parker parkNotifier, floorDeny []string, log *slog.Logger) Runner {
	return &nativeRunner{agent: agent, parker: parker, modelFloorDeny: floorDeny, log: log}
}

// missionTools builds the turn-scoped file tools rooted in the
// mission's OWN directory (worktree for coding, workspace otherwise):
// a shell that replaces the global workspace-rooted one for the turn —
// the root cause of a whole failure family was workers writing into
// the shared root while verify_cmd and the reviewer looked in the
// per-mission directory — and write_file, so artifact writes never go
// through destructive-classified shell redirects.
func missionTools(m Mission) []*tools.Tool {
	root := m.WorkRoot()
	if root == "" {
		return nil
	}
	return []*tools.Tool{
		builtin.Shell(builtin.ShellConfig{WorkspaceRoot: root}),
		builtin.WriteFile(builtin.WriteFileConfig{Root: root}),
	}
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

// runTurn drives one loop.Agent session to completion, capturing the
// text of the sentinel tool call named toolName (if any) alongside the
// full assistant text. It does not itself decide what a missing
// sentinel means — that's the caller's job (worker vs review have
// different enforcement/parsing needs).
func (r *nativeRunner) runTurn(ctx context.Context, req loop.Request, sentinelTool string) (text string, sentinelArgs json.RawMessage, err error) {
	events, err := r.agent.Start(ctx, req)
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	// parked tracks in-flight parks by CallID, not a single flag — a
	// turn can issue concurrent tool calls (executeAll runs up to
	// maxParallelTools at once), so a sibling call finishing first must
	// not be mistaken for the still-blocked destructive one resolving.
	parked := map[string]bool{}
	servedModel := ""
	for ev := range events {
		switch ev.Type {
		case stream.EventChunk:
			b.WriteString(ev.Text)
		case stream.EventToolEnd:
			if ev.ToolCall != nil && ev.ToolCall.Name == sentinelTool {
				sentinelArgs = ev.ToolCall.Input
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
		case stream.EventDone:
			if ev.Meta != nil {
				servedModel = ev.Meta.Model
			}
		case stream.EventError:
			if len(parked) > 0 && r.parker != nil {
				r.parker.OnPermissionCleared(ctx, req.MissionID)
			}
			return b.String(), sentinelArgs, fmt.Errorf("mission runner: %s", ev.Err.Message)
		}
	}
	if servedModel != "" && r.belowFloor(servedModel) {
		return b.String(), sentinelArgs, fmt.Errorf("%w: %s", ErrModelFloor, servedModel)
	}
	return b.String(), sentinelArgs, nil
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

// RunWorker seeds a fresh session (packet only, no prior transcript)
// and enforces the sentinel ladder: a present, well-formed
// mission_status call is trusted directly; a missing or invalid one
// triggers one recovery re-run, then falls back to bail detection —
// either path that doesn't yield a trusted verdict becomes a forced
// retry, never a silent accept.
func (r *nativeRunner) RunWorker(ctx context.Context, m Mission, packet WorkPacket) (WorkerVerdict, string, error) {
	system, user := packet.Render()
	extra := append([]*tools.Tool{MissionStatusTool()}, missionTools(m)...)
	req := loop.Request{
		SessionID:  m.SessionID,
		Route:      workerRoute(m),
		Agent:      "mission-worker",
		MissionID:  m.ID,
		System:     system,
		Messages:   []provider.Message{{Role: "user", Content: user}},
		ExtraTools: extra,
	}

	text, args, err := r.runTurn(ctx, req, missionStatusToolName)
	if err != nil {
		return WorkerVerdict{}, text, err
	}
	if v, ok := r.tryParseVerdict(args); ok {
		return v, text, nil
	}

	// Recovery re-run: inject a system message demanding the call.
	recoverReq := req
	recoverReq.Messages = append(append([]provider.Message{}, req.Messages...),
		provider.Message{Role: "assistant", Content: text},
		provider.Message{Role: "user", Content: "[system] You must end your turn with exactly one mission_status tool call: done, retry, or blocked."},
	)
	recoverText, recoverArgs, err := r.runTurn(ctx, recoverReq, missionStatusToolName)
	if err != nil {
		return WorkerVerdict{}, text + "\n" + recoverText, err
	}
	if v, ok := r.tryParseVerdict(recoverArgs); ok {
		return v, text + "\n" + recoverText, nil
	}

	// Still missing: bail detection informs the log, but either way this
	// is a forced retry — detection accuracy never gates the outcome.
	combined := text + "\n" + recoverText
	if detectBail(combined) {
		r.log.Warn("mission worker bailed without a sentinel call", "mission_id", m.ID)
	} else {
		r.log.Warn("mission worker ended without a sentinel call", "mission_id", m.ID)
	}
	return WorkerVerdict{Outcome: "retry", Analysis: "the worker did not report a status; treated as a failed attempt"}, combined, nil
}

func (r *nativeRunner) tryParseVerdict(args json.RawMessage) (WorkerVerdict, bool) {
	if len(args) == 0 {
		return WorkerVerdict{}, false
	}
	v, err := parseWorkerVerdict(args)
	if err != nil || v.Outcome == "" {
		return WorkerVerdict{}, false
	}
	return v, true
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

	extra := append([]*tools.Tool{ReviewVerdictTool()}, missionTools(m)...)
	req := loop.Request{
		SessionID:  m.SessionID,
		Route:      m.ReviewRoute,
		Agent:      "mission-reviewer",
		MissionID:  m.ID,
		System:     system,
		Messages:   messages,
		ExtraTools: extra,
	}
	text, args, err := r.runTurn(ctx, req, reviewVerdictToolName)
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
		recoverText, recoverArgs, err := r.runTurn(ctx, recoverReq, reviewVerdictToolName)
		if err != nil {
			return ReviewVerdict{}, nil, err
		}
		if len(recoverArgs) == 0 {
			return ReviewVerdict{}, nil, fmt.Errorf("mission runner: reviewer ended without a review_verdict call")
		}
		text, args = text+"\n"+recoverText, recoverArgs
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
// mission's goal and research-phase findings.
func (r *nativeRunner) PlanSession(ctx context.Context, m Mission, researchNotes string) (Spec, error) {
	system := "You are planning one mission. Break the goal into the SMALLEST ordered list of verifiable units that achieves it — one unit is correct for a simple goal; never pad the plan. Reply with ONLY a JSON object: {\"units\":[{\"title\":\"...\",\"artifacts\":[\"relative/path.md\"],\"verify_cmd\":\"...\"}]}. artifacts lists the workspace-relative file(s) the unit must produce — the harness itself checks each exists and is non-empty, so name the real deliverables. verify_cmd is executed literally as `/bin/sh -c \"<verify_cmd>\"` in the mission's own workspace directory — it must be a real POSIX shell command (using binaries like grep, test, wc — NOT a tool name from your own tool list, which does not exist as a shell command) and must check the CONTENT of the artifacts (e.g. grep -qi 'retry-after' summary.md), never a bare echo, which proves nothing. Use paths relative to the workspace; never /tmp or any absolute path outside it, since the worker's shell is confined to the workspace."
	user := "Goal: " + NeutralizeSlot(m.Goal)
	if researchNotes != "" {
		user += "\n\nResearch findings:\n" + NeutralizeSlot(researchNotes)
	}
	req := loop.Request{
		SessionID: m.SessionID,
		Route:     m.Route,
		Agent:     "mission-planner",
		MissionID: m.ID,
		System:    system,
		Messages:  []provider.Message{{Role: "user", Content: user}},
	}
	text, _, err := r.runTurn(ctx, req, "")
	if err != nil {
		return Spec{}, err
	}
	return parseSpec(text)
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
	// Passes is harness-only evidence (RunVerify); a plan is never born
	// pre-verified regardless of what the planner's JSON claims.
	for i := range spec.Units {
		spec.Units[i].Passes = false
	}
	return spec, nil
}
