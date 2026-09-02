package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// Executor runs one constrained tool call; tools.Constrained
// satisfies it.
type Executor interface {
	Execute(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// Permissioner resolves the permission chain; tools.Permissions
// satisfies it.
type Permissioner interface {
	Resolve(ctx context.Context, sessionID, tool string, args json.RawMessage) (tools.Resolution, error)
	Grant(ctx context.Context, sessionID, tool, pattern string, ttl time.Duration) error
}

// OutputSink stores offloaded results; tools.Outputs satisfies it.
type OutputSink interface {
	Put(ctx context.Context, sessionID, tool, content string) (string, error)
}

// AuditSink records execution audit rows; tools.Audit satisfies it.
type AuditSink interface {
	Record(ctx context.Context, e tools.AuditEntry) error
}

// EventAppender writes session events; session.Store satisfies it.
type EventAppender interface {
	Append(ctx context.Context, sessionID, kind string, payload any) (int64, error)
}

const (
	permissionTimeout = 10 * time.Minute
	maxParallelTools  = 4
	persistTimeout    = 10 * time.Second
	sessionGrantTTL   = 12 * time.Hour
	// retrieveInlineCap bounds what retrieve_output returns inline;
	// re-offloading a retrieval would chase its own tail, so it
	// truncates with an honest note instead.
	retrieveInlineCap = 32 << 10
	// digestCeiling bounds the tool-result digest shown to the
	// browser's activity panel and recorded in the audit trail. Results
	// over tools.DefaultOffloadThreshold (8KB) are offloaded before
	// this point, so most full results already fit within 4KB — a
	// smaller cap would needlessly hide mid-size results from view.
	digestCeiling = 4000
)

// maxStepRetries bounds automatic retry of one step's stream (D-038):
// a 3rd consecutive failure surfaces as today rather than retrying
// forever.
const maxStepRetries = 2

// stepRetryBackoff scales the wait before each step retry (attempt *
// this duration); a package-level var so tests can shrink it instead
// of plumbing a config knob through Request/NewAgent for one knob
// only tests need.
var stepRetryBackoff = time.Second

const finalizeWarning = "[system] You have one tool step left before the limit. Finish gathering and produce your final answer on the next step."

const coercionMessage = "[system] This task category requires consulting your tools before answering. Make the relevant tool call(s), then answer."

// Agent runs the think→act loop for one turn (D-003): stream with
// tool definitions, execute what the model calls through constraints
// and permissions, feed results back, repeat until a final answer or
// the step ceiling.
type Agent struct {
	gw             Gateway
	perms          Permissioner
	outputs        OutputSink
	audit          AuditSink
	events         EventAppender
	broker         *PermBroker
	askGate        *askGate
	maxSteps       int
	offloadAt      int
	offloadPerTool map[string]int
	logger         *slog.Logger

	// The tool surface swaps at runtime when connectors reload; a turn
	// snapshots both under one lock at its start, so defs and executor
	// always agree within a turn.
	toolMu sync.RWMutex
	exec   Executor
	defs   []provider.ToolDef

	// baseExec/baseDefs is the compiled-in-builtins-only surface a
	// Request.BuiltinsOnly turn resolves to instead of exec/defs: set
	// once at startup (buildAgent compiles it before any connector or
	// chat-only mission tool exists) and never swapped afterward, so no
	// lock is needed reading it alongside toolMu below.
	baseExec Executor
	baseDefs []provider.ToolDef

	// forceRouteBySuffix maps a tool name SUFFIX to a resolver for the
	// route its output must be processed under — e.g. a route chained
	// only to a local/trusted provider, for tools whose results carry
	// sensitive data (raw email content) that should never reach a
	// third-party model. Suffix, not exact name: connector tools are
	// namespaced "<connector-name>_<tool-name>" and the connector name
	// is user-chosen, so "gmail_read" must match regardless of prefix.
	// A func, not a static string: the route is resolved at flip time
	// (settings-backed), so a runtime settings change applies to the
	// next turn without a restart; an empty result means the feature is
	// currently off and no flip happens.
	forceRouteBySuffix map[string]func(context.Context) string

	// forceRouteByConnectorNames/forceRouteByConnectorRoute cover a
	// WHOLE connector marked sensitive in settings (session.SensitiveTools'
	// ConnectorNames), not just a single named tool. Two ways a call can
	// match: an MCP-style namespaced name ("<connector-name>_<tool-
	// name>") where the connector's own name is a PREFIX; or, for a
	// unified aggregate tool (connectors.Manager's search_mail etc.,
	// which carries no connector name in its own name)
	// forceRouteByAccountConnector resolving the call's actual account
	// to a sensitive connector. A single dynamic name-list func rather
	// than a map, since the caller (main.go) already resolves the live
	// sensitive-connector list as one query; each name shares the one
	// route resolver (same sensitiveRoute the static suffix floor
	// uses). All nil-safe: a turn with no connectors flagged sensitive
	// (or no AccountConnector resolver wired) just never flips this way.
	forceRouteByConnectorNames   func(context.Context) []string
	forceRouteByConnectorRoute   func(context.Context) string
	forceRouteByAccountConnector func(ctx context.Context, toolName string, args json.RawMessage) string

	// waitToolsReady, if set, runs at the start of every turn before the
	// tool snapshot (D-043): it lets main.go block briefly on the
	// connectors.Manager's first successful load without loop importing
	// the connectors package (layering). nil = no-op — the default until
	// main.go wires it, and always a no-op once the manager is ready
	// (the hook should return instantly by then).
	waitToolsReady func(context.Context)

	// mediaSaver persists media a tool emits during its call (see
	// tools.Collector). nil (the default) leaves media emission
	// unconfigured — a tool's Emit call then always errors, same as
	// ATTACHMENTS_DIR unset gates chat's own attachment upload path.
	mediaSaver tools.SaveFunc
}

func NewAgent(gw Gateway, exec Executor, perms Permissioner, outputs OutputSink, audit AuditSink, events EventAppender, broker *PermBroker, defs []provider.ToolDef, logger *slog.Logger) *Agent {
	return &Agent{
		gw: gw, exec: exec, perms: perms, outputs: outputs, audit: audit,
		events: events, broker: broker, askGate: newAskGate(), defs: defs,
		maxSteps:           tools.DefaultMaxSteps,
		offloadAt:          tools.DefaultOffloadThreshold,
		offloadPerTool:     map[string]int{},
		forceRouteBySuffix: map[string]func(context.Context) string{},
		logger:             logger,
	}
}

// SwapTools atomically replaces the tool surface for future turns;
// in-flight turns keep the snapshot they started with.
func (a *Agent) SwapTools(exec Executor, defs []provider.ToolDef) {
	a.toolMu.Lock()
	defer a.toolMu.Unlock()
	a.exec, a.defs = exec, defs
}

func (a *Agent) toolset(builtinsOnly bool) (Executor, []provider.ToolDef) {
	if builtinsOnly {
		return a.baseExec, a.baseDefs
	}
	a.toolMu.RLock()
	defer a.toolMu.RUnlock()
	return a.exec, a.defs
}

// SetBaseTools registers the compiled-in-builtins-only tool surface a
// Request.BuiltinsOnly turn (mission-driven: discover/plan/worker/
// reviewer) resolves to, instead of the full shared registry
// (connector tools + chat-only mission tools). Startup-time only —
// unlike SwapTools' surface, this snapshot never changes at runtime.
func (a *Agent) SetBaseTools(exec Executor, defs []provider.ToolDef) {
	a.baseExec, a.baseDefs = exec, defs
}

// Tools returns the live tool surface (builtins + connector tools,
// SwapTools keeps it current) — for surfaces that need to list what's
// available, like an agent's tools-allowlist picker, without executing
// anything.
func (a *Agent) Tools() []provider.ToolDef {
	_, defs := a.toolset(false)
	return defs
}

// SetOffloadThreshold overrides the offload size for one tool (D-019
// "per-tool overridable"). A tool whose results are always small can
// raise it; a chatty tool can lower it.
func (a *Agent) SetOffloadThreshold(tool string, bytes int) {
	a.offloadPerTool[tool] = bytes
}

// SetForceRoute pins a tool's output to route(ctx) for the rest of the
// turn: once a tool whose name ENDS WITH suffix is called, every
// subsequent LLM step in the SAME turn uses the resolved route instead
// of the turn's normal route — sticky, never un-forced mid-turn, since
// the sensitive content is already in context by then. The turn's
// FIRST step (before any tool has run) still uses the turn's own
// route; this only takes effect after. suffix, not the exact
// registered name: connector tools are namespaced
// "<connector-name>_<tool-name>" with a user-chosen connector name, so
// "gmail_read" matches regardless of which connector name serves it.
// Forcing also clears the turn's model hint from then on, since a hint
// outranks the route at the gateway. route is resolved at flip time
// (not when SetForceRoute is called), so a settings change takes
// effect on the next turn without a restart; an empty result means the
// feature is currently off and the turn's route is left alone.
func (a *Agent) SetForceRoute(suffix string, route func(context.Context) string) {
	a.forceRouteBySuffix[suffix] = route
}

// SetForceRouteByConnector is SetForceRoute's counterpart for a WHOLE
// connector marked sensitive (as opposed to one hardcoded tool name):
// once any tool call matching a name currently in names(ctx) runs
// (either an MCP-style "<name>_"-prefixed tool, or a unified aggregate
// tool such as search_mail whose call resolves to that connector via
// accountConnector), the rest of the turn pins to route(ctx), same
// sticky/settings-live semantics as SetForceRoute. names, route, and
// accountConnector are all re-resolved at flip/call time, so toggling a
// connector's sensitive flag (or the configured route) takes effect on
// the very next tool call, no restart. accountConnector may be nil (no
// unified tool surface wired); the MCP-prefix path still works either way.
func (a *Agent) SetForceRouteByConnector(names func(context.Context) []string, route func(context.Context) string, accountConnector func(ctx context.Context, toolName string, args json.RawMessage) string) {
	a.forceRouteByConnectorNames = names
	a.forceRouteByConnectorRoute = route
	a.forceRouteByAccountConnector = accountConnector
}

// SetWaitToolsReady registers the turn-entry readiness hook (see the
// waitToolsReady field doc). Startup-time only.
// SetMediaSaver wires the media-emission collector's save backend
// (see tools.Collector, tools.SaveFunc). Optional — nil (the default)
// leaves media emission unconfigured, same nil-gate shape as
// SetForceRoute and friends.
func (a *Agent) SetMediaSaver(fn tools.SaveFunc) {
	a.mediaSaver = fn
}

func (a *Agent) SetWaitToolsReady(fn func(context.Context)) {
	a.waitToolsReady = fn
}

// Request is one turn's loop input.
type Request struct {
	SessionID string
	Route     string
	Agent     string // serving agent, for ledger attribution
	// ToolAllow filters the offered tool surface; empty means every
	// base tool. Missions leave this nil for anything but the planner
	// call (see runner.go). Chat's agent-authored allowlist (empty =
	// no tools to the user) is resolved to a non-empty list before it
	// ever reaches here — see chat.resolveToolAllow.
	ToolAllow []string
	ModelHint string
	System    string
	Messages  []provider.Message
	MissionID string // ledger tag: set when this turn serves a mission, not chat

	// BuiltinsOnly restricts the turn's base tool surface to compiled-in
	// builtins (calculator, shell, search_web, ...), excluding connector
	// tools (e.g. a write-capable GitHub MCP token) and the chat-only
	// mission tools (list_missions/get_mission/push_mission_branch). Set by
	// every mission-driven request (discover/plan/worker/reviewer), a
	// mission worker must never side-channel a connector write around
	// the worktree/human-consented push pipeline, and push_mission_branch
	// is nonsensical inside a mission turn. Deliberately independent of
	// MissionID (a ledger attribution tag only) so tool-surface scoping
	// stays an explicit, intentional choice at each call site rather
	// than inferred.
	// ExtraTools still layer on top as normal — missions.nativeRunner's
	// worker/discover turns use exactly this to add back read-only
	// connector tools (gmail/calendar reads) that scheduled general
	// missions need, gated on the agent's Tools allowlist and never
	// including MCP or write-capable tools (missions.ConnectorReadsResolver).
	BuiltinsOnly bool

	// ExtraTools are tool defs available ONLY for this turn, on top of
	// the agent's shared base set — e.g. the missions package's
	// mission_status/review_verdict sentinel calls, which chat sessions
	// must never see. Executed via extraExecutor, never merged into the
	// shared registry other callers read.
	ExtraTools []*tools.Tool

	// Unattended marks a turn nobody is watching (schedule-fired
	// missions): a permission ask resolves as immediate denial instead
	// of parking on a human prompt for the full timeout (D-039).
	Unattended bool

	// ForceTool names the single offered tool the model must call this
	// step (D-063). Only set by callers whose turn ends on that tool
	// (mission sentinel turns). Empty means auto, today's behavior.
	ForceTool string

	// EndTurnTools names offered tools whose successful EXECUTION ends
	// the turn immediately — no further model call to react to the
	// result. Set by mission sentinel turns (mission_status/
	// discover_notes/submit_plan/review_verdict), whose call already
	// answers everything the turn needed; empty means today's behavior
	// (always continue).
	EndTurnTools []string

	// Steering, when set, is polled once per iteration at the top of
	// every model round after the first; each returned string is
	// appended to Messages as a user-role message before that round's
	// call. Lets an operator note posted while a turn is mid-flight
	// reach the SAME turn instead of only the next one. Empty result is
	// a no-op. nil for every caller but mission worker turns: the loop
	// itself knows nothing about missions.
	Steering func(ctx context.Context) []string

	// MaxSteps, when > 0, replaces the agent's default step ceiling
	// (tools.DefaultMaxSteps) for this turn (D-093): a reviewer that
	// already holds the harness's verify output needs a spot check, not
	// sixteen iterations of re-sent packet.
	MaxSteps int

	// ToolResultCap, when > 0, bounds the bytes of every tool result
	// that enters this turn's conversation (D-093): a longer result is
	// cut on a line boundary with a "[truncated: N bytes]" marker before
	// the model sees it. The audit trail and offload store keep the
	// full result; only the transcript is capped.
	ToolResultCap int
}

// Start launches the loop and returns its event stream. The channel
// follows the stream package's terminal contract: exactly one done,
// error, or incomplete event ends it. Usage on the terminal path is
// the sum over all steps.
func (a *Agent) Start(ctx context.Context, req Request) (<-chan stream.StreamEvent, error) {
	out := make(chan stream.StreamEvent)
	go func() {
		defer close(out)
		a.run(ctx, req, out)
	}()
	return out, nil
}

func (a *Agent) run(ctx context.Context, req Request, out chan<- stream.StreamEvent) {
	var emitMu sync.Mutex
	emit := func(ev stream.StreamEvent) {
		emitMu.Lock()
		defer emitMu.Unlock()
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}
	// emitFinal delivers end-of-turn events (the usage/terminal pair)
	// without racing ctx.Done(): when the turn deadline fires at the
	// same instant the terminal is sent, both select cases in emit are
	// ready and Go picks randomly — a lost flip drops the only evidence
	// the turn ever existed (the relay persists what crosses it, and
	// persistTurn has nothing without a terminal). Every consumer
	// drains out until close, so this send succeeds immediately in
	// practice; the timeout only bounds a hypothetically abandoned
	// consumer.
	emitFinal := func(ev stream.StreamEvent) {
		emitMu.Lock()
		defer emitMu.Unlock()
		select {
		case out <- ev:
		case <-time.After(5 * time.Second):
		}
	}

	if a.waitToolsReady != nil {
		a.waitToolsReady(ctx)
	}

	exec, defs := a.toolset(req.BuiltinsOnly)
	defs = filterDefs(defs, req.ToolAllow)
	if len(req.ExtraTools) > 0 {
		var extraDefs []provider.ToolDef
		exec, extraDefs = withExtraTools(exec, req.ExtraTools)
		// An extra tool that shares a base tool's name REPLACES it for
		// this turn (extraExecutor already resolves extras first): the
		// model must see exactly one def per name, and it must be the
		// turn-scoped one — e.g. a mission's workspace-rooted shell
		// shadowing the global shell.
		override := make(map[string]bool, len(extraDefs))
		for _, d := range extraDefs {
			override[d.Name] = true
		}
		kept := make([]provider.ToolDef, 0, len(defs))
		for _, d := range defs {
			if !override[d.Name] {
				kept = append(kept, d)
			}
		}
		defs = append(kept, extraDefs...)
	}
	// toolNames is the turn's actual offered surface (post ToolAllow
	// filter, post ExtraTools override) — built once per turn so a
	// hallucinated tool name can be rejected before it ever reaches the
	// permission chain (D-039). tools.Constrained.Execute already checks
	// this, but only after askUser has parked; an unattended mission
	// can't afford the wait to find out.
	toolNames := make(map[string]bool, len(defs))
	for _, d := range defs {
		toolNames[d.Name] = true
	}
	// ForceTool is wire-invalid against a tool the turn doesn't actually
	// offer (D-063) — defensive only, callers are expected to name a
	// tool already in their own ToolAllow.
	forceTool := req.ForceTool
	if forceTool != "" && !toolNames[forceTool] {
		forceTool = ""
	}
	endTurnTools := make(map[string]bool, len(req.EndTurnTools))
	for _, name := range req.EndTurnTools {
		endTurnTools[name] = true
	}
	msgs := append([]provider.Message(nil), req.Messages...)
	total := stream.Usage{}
	var lastMeta *stream.Meta
	// providerState is turn-local driver continuation state (D-067,
	// e.g. openai-responses' previous_response_id): captured off each
	// step's done Meta and echoed on the next step's request. It dies
	// with the turn — nothing resets it mid-turn on purpose. On chain
	// failover the serving driver can change mid-turn; the state names
	// its origin driver, so a different driver ignores foreign state
	// with no handling needed here.
	var providerState json.RawMessage
	effort := ""
	toolCallCount := 0
	coerced := false
	var repeats tools.RepeatGuard
	stuck := false
	// route is req.Route until a tool with a forced route runs; from
	// then on it's sticky for the rest of the turn (see SetForceRoute).
	// hint follows the same lifecycle: it's dropped alongside the flip
	// because gateway hint resolution is not chain-constrained — a
	// surviving hint would let the model pick a provider outside the
	// forced route's chain and bypass it entirely.
	route := req.Route
	hint := req.ModelHint
	maxSteps := a.maxSteps
	if req.MaxSteps > 0 {
		maxSteps = req.MaxSteps
	}

	for step := 1; ; step++ {
		if step > 1 && req.Steering != nil {
			for _, note := range req.Steering(ctx) {
				msgs = append(msgs, provider.Message{Role: "user", Content: note})
			}
		}
		directive := tools.CeilingFor(step, maxSteps)
		// A model retrying the same call over and over (e.g. hoping a
		// search tool will "book" something on a later attempt) forces
		// synthesis early rather than burning steps up to the ceiling.
		if stuck && directive == tools.StepProceed {
			directive = tools.StepForceSynthesis
		}
		sreq := gwclient.StreamRequest{
			Route:         route,
			Agent:         req.Agent,
			Purpose:       "chat",
			ModelHint:     hint,
			System:        req.System,
			Messages:      msgs,
			Tools:         defs,
			Effort:        effort,
			SessionID:     req.SessionID,
			MissionID:     req.MissionID,
			ForceTool:     forceTool,
			ProviderState: providerState,
		}
		switch directive {
		case tools.StepWarnFinalize:
			sreq.Messages = append(sreq.Messages, provider.Message{Role: "user", Content: finalizeWarning})
			msgs = sreq.Messages
		case tools.StepForceSynthesis:
			// Drop the schemas: the model must answer with what it has.
			// ForceTool goes with them — forcing a call with no tools
			// offered is a wire error.
			sreq.Tools = nil
			sreq.ForceTool = ""
		}

		// Inner attempt loop: a stream that dies before anything
		// user-visible went out for THIS step can be re-tried transparently
		// — the web "retry" handler appends a notice but never resets
		// partial text, so re-streaming after any emission would duplicate
		// content in the saved turn. Retrying never advances step/directive:
		// same sreq, same msgs.
		var text strings.Builder
		var calls []provider.ToolCall
		terminal := stream.StreamEvent{}
		emittedThisAttempt := false
		// incompleteEv is sticky across this attempt: the gateway sends
		// incomplete THEN done for a cut-off or zero-output stream
		// (D-044), and terminal is last-write-wins — done would
		// otherwise clobber incomplete and the abnormal-end guard below
		// would never fire. done cannot clear this.
		var incompleteEv *stream.StreamEvent

		for attempt := 0; ; attempt++ {
			upstream, err := a.gw.Stream(ctx, sreq)
			if err != nil {
				if attempt < maxStepRetries && retryStep(ctx, emit, attempt+1, err.Error()) {
					continue
				}
				// Flush usage accumulated by earlier steps before the
				// terminal error, matching the in-stream error path — a
				// mid-loop gateway failure must not undercount the turn.
				emitFinal(stream.StreamEvent{Type: stream.EventUsage, Usage: &total})
				emitFinal(stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
					Code: "gateway_unavailable", Message: err.Error(), Retryable: true,
				}})
				return
			}

			text.Reset()
			calls = nil
			terminal = stream.StreamEvent{}
			emittedThisAttempt = false
			incompleteEv = nil
			for ev := range upstream {
				switch ev.Type {
				case stream.EventChunk:
					text.WriteString(ev.Text)
					emittedThisAttempt = true
					emit(ev)
				case stream.EventReasoningChunk, stream.EventRetry, stream.EventToolStart:
					if ev.Type != stream.EventRetry {
						emittedThisAttempt = true
					}
					emit(ev)
				case stream.EventToolEnd:
					if ev.ToolCall != nil {
						calls = append(calls, provider.ToolCall{
							ID: ev.ToolCall.ID, Name: ev.ToolCall.Name, Input: ev.ToolCall.Input,
						})
					}
					emittedThisAttempt = true
					emit(ev)
				case stream.EventUsage:
					if ev.Usage != nil {
						total.InputTokens += ev.Usage.InputTokens
						total.OutputTokens += ev.Usage.OutputTokens
						total.CacheReadTokens += ev.Usage.CacheReadTokens
						total.CacheWriteTokens += ev.Usage.CacheWriteTokens
					}
				case stream.EventDone:
					terminal = ev
					if ev.Meta != nil {
						lastMeta = ev.Meta
						providerState = ev.Meta.ProviderState
					}
				case stream.EventIncomplete:
					terminal = ev
					evCopy := ev
					incompleteEv = &evCopy
				case stream.EventError:
					terminal = ev
				}
			}

			// EventIncomplete is a cut-off stream, not a retryable failure
			// signal on its own — only a terminal EventError with
			// Retryable set qualifies, and only before anything emitted.
			if terminal.Type == stream.EventError && terminal.Err != nil && terminal.Err.Retryable &&
				!emittedThisAttempt && attempt < maxStepRetries {
				if retryStep(ctx, emit, attempt+1, terminal.Err.Message) {
					continue
				}
			}
			break
		}

		// Abnormal step end: surface it and stop — the relay persists
		// the partial exactly as before. incompleteEv keeps a cut-off or
		// zero-output stream on this path even though done arrived after
		// incomplete and would otherwise look like a clean finish; it
		// carries the incomplete event's own text instead of the terminal
		// done event's (near-empty) content.
		if terminal.Type != stream.EventDone || incompleteEv != nil {
			if terminal.Type == "" {
				terminal = stream.StreamEvent{Type: stream.EventIncomplete, Text: "stream ended without a terminal event"}
			} else if incompleteEv != nil {
				terminal = *incompleteEv
			}
			emitFinal(stream.StreamEvent{Type: stream.EventUsage, Usage: &total})
			emitFinal(terminal)
			return
		}

		// At the ceiling the schemas were dropped; a model that still
		// emits tool calls (or hallucinates one) does not get them
		// executed — the turn ends with whatever text it produced.
		// This is the loop's hard termination: it can never run past
		// the ceiling no matter what the model returns.
		if directive == tools.StepForceSynthesis {
			emitFinal(stream.StreamEvent{Type: stream.EventUsage, Usage: &total})
			emitFinal(stream.StreamEvent{Type: stream.EventDone, Meta: lastMeta})
			return
		}

		if len(calls) == 0 {
			if directive == tools.StepProceed &&
				tools.NeedsRetrievalCoercion(req.Route, toolCallCount, coerced) {
				coerced = true
				msgs = append(msgs,
					provider.Message{Role: "assistant", Content: text.String()},
					provider.Message{Role: "user", Content: coercionMessage},
				)
				continue
			}
			emitFinal(stream.StreamEvent{Type: stream.EventUsage, Usage: &total})
			emitFinal(stream.StreamEvent{Type: stream.EventDone, Meta: lastMeta})
			return
		}

		toolCallCount += len(calls)
		stuck = repeats.Record(calls)
		for _, c := range calls {
			for suffix, fn := range a.forceRouteBySuffix {
				if strings.HasSuffix(c.Name, suffix) {
					if forced := fn(ctx); forced != "" {
						route = forced
						hint = ""
					}
				}
			}
			if a.forceRouteByConnectorNames != nil {
				sensitiveNames := a.forceRouteByConnectorNames(ctx)
				matched := false
				for _, name := range sensitiveNames {
					if strings.HasPrefix(c.Name, name+"_") {
						matched = true
						break
					}
				}
				if !matched && a.forceRouteByAccountConnector != nil {
					connector := a.forceRouteByAccountConnector(ctx, c.Name, c.Input)
					for _, name := range sensitiveNames {
						if name == connector {
							matched = true
							break
						}
					}
				}
				if matched {
					if forced := a.forceRouteByConnectorRoute(ctx); forced != "" {
						route = forced
						hint = ""
					}
				}
			}
		}
		results := a.executeAll(ctx, exec, req.SessionID, calls, toolNames, req.Unattended, emit)

		msgs = append(msgs, provider.Message{
			Role: "assistant", Content: text.String(), ToolCalls: calls,
		})
		for i := range results {
			results[i].Content = capToolResult(results[i].Content, req.ToolResultCap)
			msgs = append(msgs, provider.Message{Role: "tool", ToolResult: &results[i]})
		}

		// D-075: a sentinel call's successful execution already answers
		// everything the turn needed (mission_status/discover_notes/
		// submit_plan/review_verdict) — sending its result back for
		// another completion just burns a model call, and on reasoning
		// models over openai-responses that pointless continuation
		// returns empty output and fails the whole turn (D-063). An
		// errored/denied sentinel result still needs the model to see
		// it and retry, so only a clean result ends the turn here.
		endTurn := false
		for i, c := range calls {
			if endTurnTools[c.Name] && !results[i].IsError {
				endTurn = true
				break
			}
		}
		if endTurn {
			emitFinal(stream.StreamEvent{Type: stream.EventUsage, Usage: &total})
			emitFinal(stream.StreamEvent{Type: stream.EventDone, Meta: lastMeta})
			return
		}

		effort = EffortFor(results)
	}
}

// capToolResult truncates content to at most limit bytes on a line
// boundary (falling back to a hard cut when no newline exists that
// early) and appends a marker naming the full size (D-093). limit <= 0
// or content within the limit returns content unchanged.
func capToolResult(content string, limit int) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	cut := limit
	if i := strings.LastIndexByte(content[:limit+1], '\n'); i > 0 {
		cut = i
	}
	return fmt.Sprintf("%s\n[truncated: %d bytes]", content[:cut], len(content))
}

// retryStep emits a stream.EventRetry for the upcoming attempt and
// waits the scaled backoff, aborting early if ctx is done. It reports
// whether the caller should retry; false means ctx ended mid-wait, in
// which case the caller falls through to its normal failure path.
func retryStep(ctx context.Context, emit func(stream.StreamEvent), attempt int, reason string) bool {
	backoff := time.Duration(attempt) * stepRetryBackoff
	emit(stream.StreamEvent{Type: stream.EventRetry, Retry: &stream.RetryInfo{
		Attempt: attempt, BackoffMs: backoff.Milliseconds(), Reason: reason,
	}})
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// EffortFor implements the D-020 dial as a pure function: a
// continuation after uniformly successful tool results runs at low
// effort; any error or denial keeps full effort.
func EffortFor(results []provider.ToolResult) string {
	for _, r := range results {
		if r.IsError {
			return ""
		}
	}
	if len(results) == 0 {
		return ""
	}
	return "low"
}

// executeAll runs a step's tool calls concurrently (bounded) and
// returns results in call order. toolNames is the turn's offered
// surface (see run's toolNames comment); unattended is req.Unattended,
// threaded down to resolveAndRun's DecisionAsk handling (D-039).
func (a *Agent) executeAll(ctx context.Context, exec Executor, sessionID string, calls []provider.ToolCall, toolNames map[string]bool, unattended bool, emit func(stream.StreamEvent)) []provider.ToolResult {
	results := make([]provider.ToolResult, len(calls))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxParallelTools)
	for i, call := range calls {
		g.Go(func() error {
			results[i] = a.executeOne(gctx, exec, sessionID, call, toolNames, unattended, emit)
			return nil
		})
	}
	_ = g.Wait() // workers never return errors; failures live in results
	return results
}

func (a *Agent) executeOne(ctx context.Context, exec Executor, sessionID string, call provider.ToolCall, toolNames map[string]bool, unattended bool, emit func(stream.StreamEvent)) provider.ToolResult {
	start := time.Now()
	// A fresh collector per call: media a tool emits during THIS
	// Execute rides on ctx, drained right below, never leaking into a
	// concurrent sibling call's result.
	collector := tools.NewCollector(a.mediaSaver)
	ctx = tools.WithCollector(ctx, collector)
	// A panicking tool must become feedback the model can read (D-009),
	// never a process crash: executeOne runs inside an errgroup worker,
	// and an unrecovered panic there takes down the whole brain — every
	// active turn and mission with it.
	content, status := func() (content, status string) {
		defer func() {
			if p := recover(); p != nil {
				a.logger.Error("tool panicked", "tool", call.Name, "panic", p, "stack", string(debug.Stack()))
				content, status = fmt.Sprintf("tool %s failed with an internal error: %v", call.Name, p), "error"
			}
		}()
		return a.resolveAndRun(ctx, exec, sessionID, call, toolNames, unattended, emit)
	}()
	isError := status != "ok"
	media := collector.Drain()
	if len(media) > tools.MaxMediaPerCall {
		media = media[:tools.MaxMediaPerCall]
	}
	streamMedia := make([]stream.MediaRef, len(media))
	for i, m := range media {
		streamMedia[i] = stream.MediaRef{ID: m.ID, Mime: m.Mime, Name: m.Name}
	}

	if status == "ok" {
		offloaded, err := a.offloadIfBig(ctx, sessionID, call.Name, content)
		if err != nil {
			a.logger.Warn("offload failed; result kept inline", "tool", call.Name, "error", err)
		} else {
			content = offloaded
		}
	}

	duration := time.Since(start)
	// load_skill's result is the pack's full rule text — useful to the
	// model, never to the client. Every other tool's result is fair to
	// show (truncated) in the audit trail and the UI.
	var digest, traceContent string
	if call.Name == "load_skill" {
		digest = "skill loaded"
	} else {
		digest = content
		if len(digest) > digestCeiling {
			digest = digest[:digestCeiling] + "…"
		}
		// traceContent is the untruncated result, for missions' own trace
		// parsing (kb_hits, search_web URLs): Digest is capped well below
		// where a multi-hit result's later hits live (issue #418).
		traceContent = content
	}

	// Bookkeeping uses a detached context: a client disconnect must
	// not lose the audit trail (same discipline as chat persistence).
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	if _, err := a.events.Append(bctx, sessionID, session.KindToolExecution, session.ToolExecution{
		CallID: call.ID, Name: call.Name,
		Args:         string(call.Input),
		ResultDigest: digest,
		Status:       status,
		DurationMs:   duration.Milliseconds(),
	}); err != nil {
		a.logger.Error("persist tool_execution", "session_id", sessionID, "tool", call.Name, "error", err)
	}
	auditErr := ""
	if isError {
		auditErr = firstLine(content)
	}
	if err := a.audit.Record(bctx, tools.AuditEntry{
		SessionID: sessionID, Tool: call.Name,
		ArgsDigest: tools.ArgsDigest(call.Input),
		Status:     status, Duration: duration, Error: auditErr,
	}); err != nil {
		a.logger.Error("audit tool execution", "session_id", sessionID, "tool", call.Name, "error", err)
	}

	emit(stream.StreamEvent{Type: stream.EventToolResult, ToolResult: &stream.ToolResultEvent{
		ID: call.ID, Name: call.Name, Status: status,
		Digest: digest, DurationMs: duration.Milliseconds(), Args: call.Input,
		Media: streamMedia, Content: traceContent,
	}})

	return provider.ToolResult{ID: call.ID, Content: content, IsError: isError}
}

// resolveAndRun walks the permission chain, parks on Ask, and executes
// on allow. It returns the content the model sees plus a status of
// ok, denied, or error — denials and failures come back as feedback
// text, never as a broken turn (D-009).
//
// A call whose name is absent from toolNames (a hallucinated tool)
// never reaches a.perms.Resolve at all — it is rejected here, before
// the permission chain and before any askUser park, with the same
// feedback tools.Constrained.Execute would eventually give (D-039):
// an unattended mission can't afford to wait out a 10-minute prompt
// timeout just to learn the name never existed.
func (a *Agent) resolveAndRun(ctx context.Context, exec Executor, sessionID string, call provider.ToolCall, toolNames map[string]bool, unattended bool, emit func(stream.StreamEvent)) (content, status string) {
	if !toolNames[call.Name] {
		return fmt.Sprintf("unknown tool %q — use one of the tools you were given", call.Name), "error"
	}

	res, err := a.perms.Resolve(ctx, sessionID, call.Name, call.Input)
	if err != nil {
		return "permission check failed: " + err.Error(), "error"
	}

	switch res.Decision {
	case tools.DecisionDeny:
		return "denied: " + res.Rationale + ". This is a hard policy; do not retry the same call.", "denied"
	case tools.DecisionAsk:
		if unattended {
			// No human is watching a schedule-fired mission's turn — parking
			// on askUser would strand it for the full permissionTimeout with
			// nobody to answer (D-039). Fail fast with feedback that steers
			// the model toward calls the allowlist actually grants.
			return fmt.Sprintf("permission denied automatically (unattended mission): %s. No human is available to approve. Rewrite the call to avoid the flagged pattern — create files with write_file instead of shell redirects, avoid command substitution — or use only tools the agent's allowlist grants.", res.Rationale), "denied"
		}

		// A step's parallel same-tool calls (executeAll) all land here
		// before any of them has been answered — without the gate, each
		// would independently park on askUser and the user would see N
		// identical prompts for what is really one decision. The gate
		// serializes them per (session, tool, subject): only the first
		// caller through asks; the rest wait, then re-resolve below to
		// pick up whatever grant the first one just recorded.
		key := sessionID + "\x00" + call.Name + "\x00" + res.Subject
		release, ok := a.askGate.lock(ctx, key)
		if !ok {
			return "the user denied permission for this call. Adapt your approach or ask the user what they want.", "denied"
		}

		// Re-resolve under the gate: a parallel sibling's session grant
		// may have landed while we waited — Allow means zero prompt.
		res, err = a.perms.Resolve(ctx, sessionID, call.Name, call.Input)
		if err != nil {
			release()
			return "permission check failed: " + err.Error(), "error"
		}
		switch res.Decision {
		case tools.DecisionAllow:
			// fall through to execution below.
		case tools.DecisionDeny:
			release()
			return "denied: " + res.Rationale + ". This is a hard policy; do not retry the same call.", "denied"
		default: // still DecisionAsk
			decision := a.askUser(ctx, call, res, emit)
			switch decision {
			case DecideSession:
				if err := a.perms.Grant(ctx, sessionID, call.Name, res.Subject, sessionGrantTTL); err != nil {
					a.logger.Warn("record session grant", "session_id", sessionID, "error", err)
				}
			case DecideOnce:
				// proceed
			default: // deny or timeout
				release()
				return "the user denied permission for this call. Adapt your approach or ask the user what they want.", "denied"
			}
		}
		// Execution runs outside the gate: only the ask/re-resolve/grant
		// needs serializing, not the tool call itself — parallel same-tool
		// calls must still execute concurrently once permission is settled.
		release()
	}

	out, err := exec.Execute(ctx, call.Name, call.Input)
	if err != nil {
		if tools.IsViolation(err) {
			return err.Error(), "error"
		}
		if out != "" {
			// e.g. shell timeout with partial output.
			return err.Error() + "\n" + out, "error"
		}
		return "tool failed: " + err.Error(), "error"
	}
	return out, "ok"
}

// askUser parks the call: emits a permission_request and blocks for
// the decision (timeout = deny, D-010). Turn durability while parked
// comes from the relay's periodic pending_state flushes; the prompt
// itself is in-memory only — a restart drops it, which resolves as a
// deny. Whatever the outcome — an explicit answer, a timeout, or ctx
// cancellation — a permission_resolved event follows so anything
// tracking the request (persistence, a replay client) learns the
// outcome too; the live web client already clears its prompt off
// tool_result, so this is additive, not a behavior change for it.
func (a *Agent) askUser(ctx context.Context, call provider.ToolCall, res tools.Resolution, emit func(stream.StreamEvent)) string {
	id, answer := a.broker.Create()
	defer a.broker.Forget(id)

	emit(stream.StreamEvent{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{
		ID: id, CallID: call.ID, Tool: call.Name,
		Args:      string(call.Input),
		Danger:    res.Danger.String(),
		Rationale: res.Rationale,
	}})

	decision := func() string {
		timer := time.NewTimer(permissionTimeout)
		defer timer.Stop()
		select {
		case d := <-answer:
			return d
		case <-timer.C:
			return DecideDeny
		case <-ctx.Done():
			return DecideDeny
		}
	}()
	emit(stream.StreamEvent{Type: stream.EventPermissionResolved, Resolved: &stream.PermissionResolvedEvent{
		ID: id, Decision: decision,
	}})
	return decision
}

func (a *Agent) offloadIfBig(ctx context.Context, sessionID, tool, content string) (string, error) {
	threshold := a.offloadAt
	if t, ok := a.offloadPerTool[tool]; ok {
		threshold = t
	}
	if len(content) <= threshold {
		return content, nil
	}
	// retrieve_output must not re-offload what it just retrieved;
	// truncate with an honest note instead (retrieval pages by size).
	// The offload threshold can be smaller than the inline cap, so a
	// result can land here already shorter than retrieveInlineCap —
	// that's not truncation, just an oversized-but-not-huge output.
	if tool == "retrieve_output" {
		if len(content) <= retrieveInlineCap {
			return content, nil
		}
		return fmt.Sprintf("%s\n[truncated at %d bytes: the stored output is %d bytes]",
			content[:retrieveInlineCap], retrieveInlineCap, len(content)), nil
	}
	ref, err := a.outputs.Put(ctx, sessionID, tool, content)
	if err != nil {
		return content, err
	}
	return tools.Digest(content, tool, ref), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// filterDefs narrows the tool surface to an agent's allowlist. The
// model never sees a disallowed tool, so there is nothing to refuse
// at execution time; empty allow means the full surface.
//
// D-036: connector tools register namespaced as
// "<connector-name>_<tool-name>" (connectors.Manager.Tools), so an
// allowlist entry like "gmail_search" (agent-authored, before any
// connector name is known) must still match
// "gmail_gmail_search" at offer time. tools.ToolMatches already
// implements this suffix rule for the permission-grant chain; reused
// here so the two layers can never disagree about what a name in an
// agent config refers to.
func filterDefs(defs []provider.ToolDef, allow []string) []provider.ToolDef {
	if len(allow) == 0 {
		return defs
	}
	out := make([]provider.ToolDef, 0, len(defs))
	for _, d := range defs {
		for _, name := range allow {
			if tools.ToolMatches(d.Name, name) {
				out = append(out, d)
				break
			}
		}
	}
	return out
}

// extraExecutor tries a turn's ExtraTools before falling back to the
// shared base executor — extras are never merged into the shared
// registry, so no other caller (chat, another turn) ever sees them.
type extraExecutor struct {
	base  Executor
	extra map[string]*tools.Tool
}

func (e *extraExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if t, ok := e.extra[name]; ok {
		return t.Execute(ctx, args)
	}
	return e.base.Execute(ctx, name, args)
}

// withExtraTools wraps base so calls to req.ExtraTools resolve without
// touching the agent's shared registry, and derives the matching
// []provider.ToolDef the model needs to see them as callable. Unlike
// tools.Constrained, this does not schema-validate the call args
// before Execute runs — ExtraTools come from this package's own
// trusted callers (missions), whose Execute already parses/rejects
// malformed args itself, not from arbitrary registered tools.
func withExtraTools(base Executor, extra []*tools.Tool) (Executor, []provider.ToolDef) {
	byName := make(map[string]*tools.Tool, len(extra))
	defs := make([]provider.ToolDef, 0, len(extra))
	for _, t := range extra {
		byName[t.Name] = t
		defs = append(defs, provider.ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return &extraExecutor{base: base, extra: byName}, defs
}
