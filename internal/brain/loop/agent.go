package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
)

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
}

func NewAgent(gw Gateway, exec Executor, perms Permissioner, outputs OutputSink, audit AuditSink, events EventAppender, broker *PermBroker, defs []provider.ToolDef, logger *slog.Logger) *Agent {
	return &Agent{
		gw: gw, exec: exec, perms: perms, outputs: outputs, audit: audit,
		events: events, broker: broker, defs: defs,
		maxSteps:       tools.DefaultMaxSteps,
		offloadAt:      tools.DefaultOffloadThreshold,
		offloadPerTool: map[string]int{},
		logger:         logger,
	}
}

// SwapTools atomically replaces the tool surface for future turns;
// in-flight turns keep the snapshot they started with.
func (a *Agent) SwapTools(exec Executor, defs []provider.ToolDef) {
	a.toolMu.Lock()
	defer a.toolMu.Unlock()
	a.exec, a.defs = exec, defs
}

func (a *Agent) toolset() (Executor, []provider.ToolDef) {
	a.toolMu.RLock()
	defer a.toolMu.RUnlock()
	return a.exec, a.defs
}

// Tools returns the live tool surface (builtins + connector tools,
// SwapTools keeps it current) — for surfaces that need to list what's
// available, like an agent's tools-allowlist picker, without executing
// anything.
func (a *Agent) Tools() []provider.ToolDef {
	_, defs := a.toolset()
	return defs
}

// SetOffloadThreshold overrides the offload size for one tool (D-019
// "per-tool overridable"). A tool whose results are always small can
// raise it; a chatty tool can lower it.
func (a *Agent) SetOffloadThreshold(tool string, bytes int) {
	a.offloadPerTool[tool] = bytes
}

// Request is one turn's loop input.
type Request struct {
	SessionID string
	Route     string
	Agent     string   // serving agent, for ledger attribution
	ToolAllow []string // agent's tool allowlist; empty = all
	ModelHint string
	System    string
	Messages  []provider.Message
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

	exec, defs := a.toolset()
	defs = filterDefs(defs, req.ToolAllow)
	msgs := append([]provider.Message(nil), req.Messages...)
	total := stream.Usage{}
	var lastMeta *stream.Meta
	effort := ""
	toolCallCount := 0
	coerced := false
	var repeats tools.RepeatGuard
	stuck := false

	for step := 1; ; step++ {
		directive := tools.CeilingFor(step, a.maxSteps)
		// A model retrying the same call over and over (e.g. hoping a
		// search tool will "book" something on a later attempt) forces
		// synthesis early rather than burning steps up to the ceiling.
		if stuck && directive == tools.StepProceed {
			directive = tools.StepForceSynthesis
		}
		sreq := gwclient.StreamRequest{
			Route: req.Route,
			Agent:        req.Agent,
			Purpose:      "chat",
			ModelHint:    req.ModelHint,
			System:       req.System,
			Messages:     msgs,
			Tools:        defs,
			Effort:       effort,
			SessionID:    req.SessionID,
		}
		switch directive {
		case tools.StepWarnFinalize:
			sreq.Messages = append(sreq.Messages, provider.Message{Role: "user", Content: finalizeWarning})
			msgs = sreq.Messages
		case tools.StepForceSynthesis:
			// Drop the schemas: the model must answer with what it has.
			sreq.Tools = nil
		}

		upstream, err := a.gw.Stream(ctx, sreq)
		if err != nil {
			// Flush usage accumulated by earlier steps before the
			// terminal error, matching the in-stream error path — a
			// mid-loop gateway failure must not undercount the turn.
			emit(stream.StreamEvent{Type: stream.EventUsage, Usage: &total})
			emit(stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
				Code: "gateway_unavailable", Message: err.Error(), Retryable: true,
			}})
			return
		}

		var text strings.Builder
		var calls []provider.ToolCall
		terminal := stream.StreamEvent{}
		for ev := range upstream {
			switch ev.Type {
			case stream.EventChunk:
				text.WriteString(ev.Text)
				emit(ev)
			case stream.EventReasoningChunk, stream.EventRetry, stream.EventToolStart:
				emit(ev)
			case stream.EventToolEnd:
				if ev.ToolCall != nil {
					calls = append(calls, provider.ToolCall{
						ID: ev.ToolCall.ID, Name: ev.ToolCall.Name, Input: ev.ToolCall.Input,
					})
				}
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
				}
			case stream.EventIncomplete, stream.EventError:
				terminal = ev
			}
		}

		// Abnormal step end: surface it and stop — the relay persists
		// the partial exactly as before.
		if terminal.Type != stream.EventDone {
			if terminal.Type == "" {
				terminal = stream.StreamEvent{Type: stream.EventIncomplete, Text: "stream ended without a terminal event"}
			}
			emit(stream.StreamEvent{Type: stream.EventUsage, Usage: &total})
			emit(terminal)
			return
		}

		// At the ceiling the schemas were dropped; a model that still
		// emits tool calls (or hallucinates one) does not get them
		// executed — the turn ends with whatever text it produced.
		// This is the loop's hard termination: it can never run past
		// the ceiling no matter what the model returns.
		if directive == tools.StepForceSynthesis {
			emit(stream.StreamEvent{Type: stream.EventUsage, Usage: &total})
			emit(stream.StreamEvent{Type: stream.EventDone, Meta: lastMeta})
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
			emit(stream.StreamEvent{Type: stream.EventUsage, Usage: &total})
			emit(stream.StreamEvent{Type: stream.EventDone, Meta: lastMeta})
			return
		}

		toolCallCount += len(calls)
		stuck = repeats.Record(calls)
		results := a.executeAll(ctx, exec, req.SessionID, calls, emit)

		msgs = append(msgs, provider.Message{
			Role: "assistant", Content: text.String(), ToolCalls: calls,
		})
		for i := range results {
			msgs = append(msgs, provider.Message{Role: "tool", ToolResult: &results[i]})
		}
		effort = EffortFor(results)
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
// returns results in call order.
func (a *Agent) executeAll(ctx context.Context, exec Executor, sessionID string, calls []provider.ToolCall, emit func(stream.StreamEvent)) []provider.ToolResult {
	results := make([]provider.ToolResult, len(calls))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxParallelTools)
	for i, call := range calls {
		g.Go(func() error {
			results[i] = a.executeOne(gctx, exec, sessionID, call, emit)
			return nil
		})
	}
	_ = g.Wait() // workers never return errors; failures live in results
	return results
}

func (a *Agent) executeOne(ctx context.Context, exec Executor, sessionID string, call provider.ToolCall, emit func(stream.StreamEvent)) provider.ToolResult {
	start := time.Now()
	content, status := a.resolveAndRun(ctx, exec, sessionID, call, emit)
	isError := status != "ok"

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
	var digest string
	if call.Name == "load_skill" {
		digest = "skill loaded"
	} else {
		digest = content
		if len(digest) > 1000 {
			digest = digest[:1000] + "…"
		}
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
		Digest: digest, DurationMs: duration.Milliseconds(),
	}})

	return provider.ToolResult{ID: call.ID, Content: content, IsError: isError}
}

// resolveAndRun walks the permission chain, parks on Ask, and executes
// on allow. It returns the content the model sees plus a status of
// ok, denied, or error — denials and failures come back as feedback
// text, never as a broken turn (D-009).
func (a *Agent) resolveAndRun(ctx context.Context, exec Executor, sessionID string, call provider.ToolCall, emit func(stream.StreamEvent)) (content, status string) {
	res, err := a.perms.Resolve(ctx, sessionID, call.Name, call.Input)
	if err != nil {
		return "permission check failed: " + err.Error(), "error"
	}

	switch res.Decision {
	case tools.DecisionDeny:
		return "denied: " + res.Rationale + ". This is a hard policy; do not retry the same call.", "denied"
	case tools.DecisionAsk:
		decision := a.askUser(ctx, call, res, emit)
		switch decision {
		case DecideSession:
			if err := a.perms.Grant(ctx, sessionID, call.Name, res.Subject, sessionGrantTTL); err != nil {
				a.logger.Warn("record session grant", "session_id", sessionID, "error", err)
			}
		case DecideOnce:
			// proceed
		default: // deny or timeout
			return "the user denied permission for this call. Adapt your approach or ask the user what they want.", "denied"
		}
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
// deny.
func (a *Agent) askUser(ctx context.Context, call provider.ToolCall, res tools.Resolution, emit func(stream.StreamEvent)) string {
	id, answer := a.broker.Create()
	defer a.broker.Forget(id)

	emit(stream.StreamEvent{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{
		ID: id, CallID: call.ID, Tool: call.Name,
		Args:      string(call.Input),
		Danger:    res.Danger.String(),
		Rationale: res.Rationale,
	}})

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
func filterDefs(defs []provider.ToolDef, allow []string) []provider.ToolDef {
	if len(allow) == 0 {
		return defs
	}
	allowed := make(map[string]bool, len(allow))
	for _, name := range allow {
		allowed[name] = true
	}
	out := make([]provider.ToolDef, 0, len(defs))
	for _, d := range defs {
		if allowed[d.Name] {
			out = append(out, d)
		}
	}
	return out
}
