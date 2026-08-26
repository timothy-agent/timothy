// Package stream defines the normalized event vocabulary every
// provider translates into: downstream consumers see one ordered
// stream regardless of the provider's wire format.
//
// Channel contract: a stream ends with exactly one terminal event —
// "done" (provider finished, possibly preceded by "incomplete" when
// the stream was cut off) or "error" (nothing more is coming) — and
// the channel closes immediately after.
package stream

import "encoding/json"

// EventType enumerates the normalized stream events.
type EventType string

const (
	EventChunk          EventType = "chunk"           // assistant text
	EventReasoningChunk EventType = "reasoning_chunk" // thinking/reasoning text
	EventToolStart      EventType = "tool_start"      // model began a tool call
	EventToolEnd        EventType = "tool_end"        // tool call arguments complete
	EventUsage          EventType = "usage"           // token accounting
	EventRetry          EventType = "retry"           // transient failure, retrying
	EventFailover       EventType = "failover"        // chain moved to the next provider
	EventIncomplete     EventType = "incomplete"      // stream cut off before finish
	EventDone           EventType = "done"            // terminal: success
	EventError          EventType = "error"           // terminal: failure

	// Agent-loop events (brain-side; never emitted by providers).
	EventToolResult         EventType = "tool_result"         // a tool execution finished
	EventPermissionRequest  EventType = "permission_request"  // the turn parked awaiting approval
	EventPermissionResolved EventType = "permission_resolved" // the parked ask got a decision
)

// StreamEvent is one normalized event. Exactly the field matching Type
// is populated; Meta additionally rides the terminal done event when
// the gateway API attributes the serving provider.
type StreamEvent struct {
	Type       EventType                `json:"type"`
	Text       string                   `json:"text,omitempty"`
	ToolCall   *ToolCallEvent           `json:"tool_call,omitempty"`
	ToolResult *ToolResultEvent         `json:"tool_result,omitempty"`
	Permission *PermissionRequestEvent  `json:"permission,omitempty"`
	Resolved   *PermissionResolvedEvent `json:"resolved,omitempty"`
	Usage      *Usage                   `json:"usage,omitempty"`
	Err        *StreamError             `json:"error,omitempty"`
	Retry      *RetryInfo               `json:"retry,omitempty"`
	Failover   *FailoverInfo            `json:"failover,omitempty"`
	Meta       *Meta                    `json:"meta,omitempty"`
}

// ToolResultEvent reports a finished tool execution to the client:
// status ok|error|denied, a digest (never the raw result), and timing.
type ToolResultEvent struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Digest     string `json:"digest,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	// Args is the call's own input, brain-internal only (json:"-": never
	// reaches the client wire); chat's sensitivity check needs it to
	// resolve which account a unified aggregate tool call (e.g.
	// mail_search) actually routed to (session.SensitiveTools.Matches).
	Args json.RawMessage `json:"-"`
}

// PermissionRequestEvent tells the client the turn parked waiting for
// the user's decision on a tool call (D-010). CallID ties it to the
// tool call whose result will follow, so a client tracking several
// parallel prompts clears exactly the right one.
type PermissionRequestEvent struct {
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Tool      string `json:"tool"`
	Args      string `json:"args"`
	Danger    string `json:"danger_level"`
	Rationale string `json:"rationale"`
}

// PermissionResolvedEvent tells the client (and the persistence path)
// a parked ask got a decision, however it arrived: an explicit
// once/session/deny answer, or the permissionTimeout/ctx-cancellation
// deny askUser falls back to. ID ties it to the PermissionRequestEvent
// it resolves.
type PermissionResolvedEvent struct {
	ID       string `json:"id"`
	Decision string `json:"decision"`
}

// Meta identifies who served a request — attached to the done event by
// the gateway API so callers attribute without a second lookup.
// DurationMs is never set by the gateway (it has no notion of a brain-
// level turn); brain's chat package stamps it on the relayed event
// before forwarding downstream. Cost is nil when the price table has
// no entry for this provider/model — unknown price is never guessed
// (D-013); Currency is blank in that case too.
type Meta struct {
	Provider   string   `json:"provider"`
	Model      string   `json:"model"`
	LedgerID   string   `json:"ledger_id,omitempty"`
	DurationMs int64    `json:"duration_ms,omitempty"`
	Cost       *float64 `json:"cost,omitempty"`
	Currency   string   `json:"currency,omitempty"`
	// ProviderRequestID is the provider's own id for this request (e.g.
	// OpenAI resp_.../chatcmpl-..., Anthropic msg_...), for reconciling a
	// ledger row against the provider's own usage export.
	ProviderRequestID string `json:"provider_request_id,omitempty"`
	// ProviderState is opaque driver continuation state (D-067) — e.g.
	// the openai-responses driver's previous_response_id — echoed back
	// by the caller on the next CompletionRequest.ProviderState so the
	// driver can chain reasoning items across turn steps.
	ProviderState json.RawMessage `json:"provider_state,omitempty"`
}

// ToolCallEvent identifies a tool call. Input carries the complete
// argument JSON on tool_end; it is empty on tool_start.
type ToolCallEvent struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Usage is provider-reported token accounting for one request.
// InputTokens EXCLUDES cache reads and writes (Anthropic-style split);
// drivers for APIs that fold cached tokens into their prompt count
// normalize before emitting.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	// ReasoningTokens is already included in OutputTokens and billed as
	// output (D-013 unaffected); it exists purely so an operator can see
	// how much output spend went to invisible reasoning.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// StreamError describes a terminal stream failure.
type StreamError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// RetryInfo reports a transient failure the driver is retrying.
type RetryInfo struct {
	Attempt   int    `json:"attempt"`
	BackoffMs int64  `json:"backoff_ms"`
	Reason    string `json:"reason"`
}

// FailoverInfo reports the chain moving from one provider to the
// next after a failed attempt. Code is the classified error (e.g.
// "timeout", "http_500") — never the raw provider error text, which
// can leak wire-level detail the client has no use for.
type FailoverInfo struct {
	FromProvider string `json:"from_provider"`
	FromModel    string `json:"from_model"`
	ToProvider   string `json:"to_provider"`
	ToModel      string `json:"to_model"`
	Code         string `json:"code"`
}
