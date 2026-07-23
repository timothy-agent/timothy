package missions

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

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
	// session, judging diff plus the worker's evidence, and returns the
	// verdict.
	RunReview(ctx context.Context, m Mission, diff string, gatekeeper *GatekeeperState) (ReviewVerdict, *GatekeeperState, error)

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

// nativeRunner is Phase 1's only Runner: every call is one loop.Agent
// turn over the gateway, tagged with the mission's route/review_route
// and MissionID (for ledger attribution).
type nativeRunner struct {
	agent agentStream
	log   *slog.Logger
}

// NewNativeRunner wraps a production *loop.Agent as a Runner. The
// agent instance is expected to be brain's existing chat agent — a
// mission worker turn is just another loop.Agent caller, not a
// separate permission/audit/tool-execution stack.
func NewNativeRunner(agent *loop.Agent, log *slog.Logger) Runner {
	return &nativeRunner{agent: agent, log: log}
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
	for ev := range events {
		switch ev.Type {
		case stream.EventChunk:
			b.WriteString(ev.Text)
		case stream.EventToolEnd:
			if ev.ToolCall != nil && ev.ToolCall.Name == sentinelTool {
				sentinelArgs = ev.ToolCall.Input
			}
		case stream.EventError:
			return b.String(), sentinelArgs, fmt.Errorf("mission runner: %s", ev.Err.Message)
		}
	}
	return b.String(), sentinelArgs, nil
}

// RunWorker seeds a fresh session (packet only, no prior transcript)
// and enforces the sentinel ladder: a present, well-formed
// mission_status call is trusted directly; a missing or invalid one
// triggers one recovery re-run, then falls back to bail detection —
// either path that doesn't yield a trusted verdict becomes a forced
// retry, never a silent accept.
func (r *nativeRunner) RunWorker(ctx context.Context, m Mission, packet WorkPacket) (WorkerVerdict, string, error) {
	system, user := packet.Render()
	req := loop.Request{
		Route:     m.Route,
		Agent:     "mission-worker",
		MissionID: m.ID,
		System:    system,
		Messages:  []provider.Message{{Role: "user", Content: user}},
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

// RunReview judges the worker's evidence against the baseline diff.
// gatekeeper carries prior messages to resume the same reviewer
// session on rework (a "delta recheck" instead of cold-reanalyzing);
// nil starts fresh.
func (r *nativeRunner) RunReview(ctx context.Context, m Mission, diff string, gatekeeper *GatekeeperState) (ReviewVerdict, *GatekeeperState, error) {
	system := "You are reviewing one unit of a mission's work. Look for reasons to reject before approving. Judge the actual diff, not the worker's description of it. End your turn with exactly one review_verdict tool call."

	var messages []provider.Message
	if gatekeeper != nil {
		messages = append(messages, gatekeeper.Messages...)
	}
	messages = append(messages, provider.Message{Role: "user", Content: "Diff to review:\n" + diff})

	req := loop.Request{
		Route:     m.ReviewRoute,
		Agent:     "mission-reviewer",
		MissionID: m.ID,
		System:    system,
		Messages:  messages,
	}
	text, args, err := r.runTurn(ctx, req, reviewVerdictToolName)
	if err != nil {
		return ReviewVerdict{}, nil, err
	}
	if len(args) == 0 {
		return ReviewVerdict{}, nil, fmt.Errorf("mission runner: reviewer ended without a review_verdict call")
	}
	verdict, err := parseReviewVerdict(args)
	if err != nil {
		return ReviewVerdict{}, nil, fmt.Errorf("mission runner: parse review_verdict: %w", err)
	}

	nextState := &GatekeeperState{Messages: append(messages, provider.Message{Role: "assistant", Content: NeutralizeSlot(text)})}
	return verdict, nextState, nil
}

// PlanSession runs the planning turn that produces a Spec from the
// mission's goal and research-phase findings.
func (r *nativeRunner) PlanSession(ctx context.Context, m Mission, researchNotes string) (Spec, error) {
	system := "You are planning one mission. Break the goal into an ordered list of verifiable units. Reply with ONLY a JSON object: {\"units\":[{\"title\":\"...\",\"verify_cmd\":\"...\"}]}"
	user := "Goal: " + NeutralizeSlot(m.Goal)
	if researchNotes != "" {
		user += "\n\nResearch findings:\n" + NeutralizeSlot(researchNotes)
	}
	req := loop.Request{
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
