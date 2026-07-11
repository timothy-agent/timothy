// Package provider defines the gateway's provider abstraction and the
// API drivers implementing it. Providers are thin wire adapters: they
// translate one provider's protocol into the normalized event stream
// and never loop, route, or hold business logic.
package provider

import (
	"context"
	"encoding/json"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// Kind distinguishes how a provider is reached.
type Kind string

// KindAPI is an HTTP API provider. A CLI subprocess kind arrives in a
// later phase.
const KindAPI Kind = "api"

// Capability names a feature a provider honestly supports; routing
// never assumes an undeclared capability.
type Capability string

const (
	CapChat       Capability = "chat"
	CapStreaming  Capability = "streaming"
	CapTools      Capability = "tools"
	CapEmbeddings Capability = "embeddings"
)

// Message is one conversation turn. The tool fields ride only on
// agent-loop round-trips: an assistant message carries the calls it
// made, and a "tool" role message carries one call's result.
type Message struct {
	Role       string      `json:"role"` // "user" | "assistant" | "tool"
	Content    string      `json:"content"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
}

// ToolCall is one tool invocation the model made.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult is the outcome of one tool call, keyed by the call id.
type ToolResult struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// ToolDef describes a tool offered to the model.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// CompletionRequest is a normalized completion call. Extend only
// additively.
type CompletionRequest struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
	// Effort is the D-020 dial: "low" on routine post-tool
	// continuations, "" or "normal" otherwise. Drivers map it to
	// their provider's reasoning-effort control where one exists and
	// ignore it otherwise.
	Effort string
}

// Provider is one configured LLM provider. Stream returns quickly; all
// wire activity (including connection retries) happens on the returned
// channel per the stream package's channel contract. The error return
// is for immediately invalid requests only.
type Provider interface {
	Name() string
	Kind() Kind
	Capabilities() []Capability
	Stream(ctx context.Context, req CompletionRequest) (<-chan stream.StreamEvent, error)
}

// Embedder is implemented by providers that can embed text. Callers
// type-assert; a provider without it is skipped by embedding routes.
type Embedder interface {
	Embed(ctx context.Context, model string, texts []string) ([][]float32, *stream.Usage, error)
}
