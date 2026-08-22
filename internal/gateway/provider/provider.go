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
	// CapVision marks a driver that can carry image content parts to
	// its provider; whether a specific MODEL can see images is the
	// model row's own capability declaration (router.attemptCapable
	// double-checks both — D-045).
	CapVision Capability = "vision"
)

// Message is one conversation turn. The tool fields ride only on
// agent-loop round-trips: an assistant message carries the calls it
// made, and a "tool" role message carries one call's result.
type Message struct {
	Role       string      `json:"role"` // "user" | "assistant" | "tool"
	Content    string      `json:"content"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	// Images are transient content parts filled in at request-build
	// time (brain's chat.runTurn, resolving attachment refs into
	// base64) and never persisted anywhere — session_events and logs
	// carry refs only, never bytes (D-045).
	Images []ImageData `json:"images,omitempty"`
	// ImageRefs carries attachment refs (no bytes) from brain's
	// session projection through to chat.runTurn, which resolves them
	// into Images just before the gateway call and clears this field.
	// json:"-": brain-internal only, never crosses the gateway wire
	// (D-045).
	ImageRefs []ImageRef `json:"-"`
}

// ImageData is one base64-encoded image content part.
type ImageData struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"` // base64, no data: URL prefix
}

// ImageRef points at a brain-side stored attachment, never bytes.
type ImageRef struct {
	ID   string
	Mime string
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
	// FinalAttempt tells the driver no other chain entry follows this
	// one: spend the full in-provider retry budget. When false (more
	// providers remain), transient failures get one quick retry and
	// then surface — the chain is the retry, and re-hammering a
	// rate-limited provider only delays failover.
	FinalAttempt bool
	// ReasoningEffortOverride forces a specific model's reasoning_effort
	// value regardless of Effort or the provider's own config-level
	// override — a per-CHAIN-ENTRY setting (router.ChainEntry), not
	// per-provider: some models on an otherwise-fine provider reject
	// tool calls on /chat/completions unless reasoning_effort is an
	// exact value (e.g. OpenAI's gpt-5.6-luna requires "none" for
	// tool-calling; other models on the same OpenAI provider row have
	// no such restriction). Empty means no override; wins over both
	// Effort and the provider's own ReasoningEffort config when set.
	ReasoningEffortOverride string
	// ForceTool names the single offered tool the model must call this
	// step (forced tool_choice), instead of choosing freely among Tools
	// (D-063). Empty means auto, today's behavior. Drivers that cannot
	// express a forced choice on the wire ignore it.
	ForceTool string
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
