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
)

// StreamEvent is one normalized event. Exactly the field matching Type
// is populated.
type StreamEvent struct {
	Type     EventType      `json:"type"`
	Text     string         `json:"text,omitempty"`
	ToolCall *ToolCallEvent `json:"tool_call,omitempty"`
	Usage    *Usage         `json:"usage,omitempty"`
	Err      *StreamError   `json:"error,omitempty"`
	Retry    *RetryInfo     `json:"retry,omitempty"`
}

// ToolCallEvent identifies a tool call. Input carries the complete
// argument JSON on tool_end; it is empty on tool_start.
type ToolCallEvent struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Usage is provider-reported token accounting for one request.
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
