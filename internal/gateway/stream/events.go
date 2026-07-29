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
type Meta struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	LedgerID string `json:"ledger_id,omitempty"`
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
