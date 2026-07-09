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

// Message is one conversation turn.
type Message struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
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
