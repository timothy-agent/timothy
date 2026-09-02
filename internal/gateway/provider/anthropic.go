package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/sse"
)

const (
	anthropicDefaultBaseURL = "https://api.anthropic.com"
	// anthropicVersion is the Messages API version header. Verified
	// current 2026-07; model IDs come from configuration, never code.
	anthropicVersion         = "2023-06-01"
	anthropicDefaultMaxToken = 4096
)

// AnthropicConfig configures one Anthropic-driver provider instance.
type AnthropicConfig struct {
	Name    string
	BaseURL string            // default anthropicDefaultBaseURL
	APIKey  string            // resolved secret value, never logged
	Headers map[string]string // extra request headers
	Timeout time.Duration     // hard per-request timeout, default 5m
}

// Anthropic streams from the native Messages API.
type Anthropic struct {
	cfg    AnthropicConfig
	client *http.Client
}

// NewAnthropic builds the driver; it performs no I/O.
func NewAnthropic(cfg AnthropicConfig) *Anthropic {
	if cfg.BaseURL == "" {
		cfg.BaseURL = anthropicDefaultBaseURL
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Anthropic{cfg: cfg, client: &http.Client{}}
}

func (a *Anthropic) Name() string { return a.cfg.Name }
func (a *Anthropic) Kind() Kind   { return KindAPI }

func (a *Anthropic) Capabilities() []Capability {
	return []Capability{CapChat, CapStreaming, CapTools, CapVision}
}

// anthropic wire types (request)

type anthropicTextBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicRequest struct {
	Model     string               `json:"model"`
	MaxTokens int                  `json:"max_tokens"`
	Stream    bool                 `json:"stream"`
	System    []anthropicTextBlock `json:"system,omitempty"`
	Messages  []anthropicMessage   `json:"messages"`
	Tools     []anthropicTool      `json:"tools,omitempty"`
	// ToolChoice forces a specific tool call (D-063: CompletionRequest.ForceTool).
	ToolChoice any `json:"tool_choice,omitempty"`
}

// anthropicMessage carries either a plain string or content blocks
// (tool_use / tool_result round-trips need blocks).
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicContentBlock struct {
	Type      string                `json:"type"`
	Text      string                `json:"text,omitempty"`
	ID        string                `json:"id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Input     json.RawMessage       `json:"input,omitempty"`
	ToolUseID string                `json:"tool_use_id,omitempty"`
	Content   string                `json:"content,omitempty"`
	IsError   bool                  `json:"is_error,omitempty"`
	Source    *anthropicImageSource `json:"source,omitempty"`
	// CacheControl marks the conversation breakpoint (D-093): set on the
	// last block of the last message only.
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

// anthropicCacheEphemeral is the one cache_control value the driver
// ever sends.
var anthropicCacheEphemeral = json.RawMessage(`{"type":"ephemeral"}`)

// markLastBlockCached puts the conversation breakpoint on the last
// content block of the last message (D-093): the system block is the
// first breakpoint, this is the second, so every tool-loop iteration
// reads the previous iteration's whole prefix from cache instead of
// re-sending it. A plain-string message is lifted to one text block
// to carry the marker. Anthropic allows four breakpoints; the driver
// uses two.
func markLastBlockCached(msgs []anthropicMessage) {
	if len(msgs) == 0 {
		return
	}
	last := &msgs[len(msgs)-1]
	switch c := last.Content.(type) {
	case string:
		last.Content = []anthropicContentBlock{{Type: "text", Text: c, CacheControl: anthropicCacheEphemeral}}
	case []anthropicContentBlock:
		if len(c) > 0 {
			c[len(c)-1].CacheControl = anthropicCacheEphemeral
		}
	}
}

// anthropicImageSource is a base64 image block's source (D-045).
type anthropicImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// anthropicMessages translates normalized messages to the wire shape:
// tool calls become tool_use blocks on the assistant message,
// "tool" role results become tool_result blocks on a user message,
// and consecutive results merge into one user message (the API
// requires all results for a turn's calls in the next user turn).
func anthropicMessages(msgs []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		switch {
		case m.Role == "tool" && m.ToolResult != nil:
			block := anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolResult.ID,
				Content:   m.ToolResult.Content,
				IsError:   m.ToolResult.IsError,
			}
			if n := len(out) - 1; n >= 0 && out[n].Role == "user" {
				if blocks, ok := out[n].Content.([]anthropicContentBlock); ok && blocks[0].Type == "tool_result" {
					out[n].Content = append(blocks, block)
					continue
				}
			}
			out = append(out, anthropicMessage{Role: "user", Content: []anthropicContentBlock{block}})
		case len(m.ToolCalls) > 0:
			blocks := make([]anthropicContentBlock, 0, len(m.ToolCalls)+1)
			if m.Content != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				input := tc.Input
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, anthropicContentBlock{
					Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input,
				})
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
		case len(m.Images) > 0:
			blocks := make([]anthropicContentBlock, 0, len(m.Images)+1)
			if m.Content != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				blocks = append(blocks, anthropicContentBlock{
					Type: "image",
					Source: &anthropicImageSource{
						Type: "base64", MediaType: img.MediaType, Data: img.Data,
					},
				})
			}
			out = append(out, anthropicMessage{Role: m.Role, Content: blocks})
		case m.Content != "":
			// Text messages always take the block shape (D-093): the
			// cache breakpoint sits on the last block of the last message,
			// and the same message must serialise byte-identically on the
			// next step when it is no longer last, or the cached prefix
			// misses. An empty message keeps the string form: an empty
			// text block is a wire error.
			out = append(out, anthropicMessage{Role: m.Role, Content: []anthropicContentBlock{{Type: "text", Text: m.Content}}})
		default:
			out = append(out, anthropicMessage{Role: m.Role, Content: m.Content})
		}
	}
	return out
}

// anthropic wire types (SSE payloads; only the fields we read)

type anthropicStreamPayload struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID    string         `json:"id"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage anthropicUsage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// Stream implements Provider.
func (a *Anthropic) Stream(ctx context.Context, req CompletionRequest) (<-chan stream.StreamEvent, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("anthropic: model is required")
	}
	body, err := json.Marshal(a.buildRequest(req))
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	build := func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Api-Key", a.cfg.APIKey)
		r.Header.Set("Anthropic-Version", anthropicVersion)
		for k, v := range a.cfg.Headers {
			r.Header.Set(k, v)
		}
		return r, nil
	}
	return runStream(ctx, a.client, a.cfg.Timeout, retriesFor(req.FinalAttempt), build, a.relay), nil
}

func (a *Anthropic) buildRequest(req CompletionRequest) anthropicRequest {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxToken
	}
	out := anthropicRequest{
		Model:     req.Model,
		MaxTokens: maxTokens,
		Stream:    true,
		Messages:  anthropicMessages(req.Messages),
	}
	markLastBlockCached(out.Messages)
	// req.Effort: no thinking control is wired for this driver yet, so
	// the hint is ignored per D-020.
	if req.System != "" {
		// cache_control on the system block enables prompt caching for
		// the stable prefix (D-018); the conversation breakpoint above
		// (D-093) extends that to the growing tool-loop transcript.
		out.System = []anthropicTextBlock{{
			Type:         "text",
			Text:         req.System,
			CacheControl: anthropicCacheEphemeral,
		}}
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, anthropicTool(t))
	}
	if req.ForceTool != "" && len(out.Tools) > 0 {
		out.ToolChoice = map[string]any{"type": "tool", "name": req.ForceTool}
	}
	return out
}

// relay translates the Messages API SSE stream into normalized events.
func (a *Anthropic) relay(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (bool, error) {
	tools := newToolAccumulator()
	usage := &stream.Usage{}
	var requestID string
	finished := false

	err := sse.Read(body, func(ev sse.Event) bool {
		var p anthropicStreamPayload
		if err := json.Unmarshal([]byte(ev.Data), &p); err != nil {
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
				Code: "malformed_stream", Message: fmt.Sprintf("bad SSE payload: %v", err), Retryable: true,
			}})
			finished = true
			return false
		}

		switch p.Type {
		case "message_start":
			usage.InputTokens = p.Message.Usage.InputTokens
			usage.CacheReadTokens = p.Message.Usage.CacheReadInputTokens
			usage.CacheWriteTokens = p.Message.Usage.CacheCreationInputTokens
			requestID = p.Message.ID
		case "content_block_start":
			if p.ContentBlock.Type == "tool_use" {
				return emit(ctx, ch, tools.start(p.Index, p.ContentBlock.ID, p.ContentBlock.Name))
			}
		case "content_block_delta":
			switch p.Delta.Type {
			case "text_delta":
				return emit(ctx, ch, stream.StreamEvent{Type: stream.EventChunk, Text: p.Delta.Text})
			case "thinking_delta":
				return emit(ctx, ch, stream.StreamEvent{Type: stream.EventReasoningChunk, Text: p.Delta.Thinking})
			case "input_json_delta":
				tools.append(p.Index, p.Delta.PartialJSON)
			}
		case "content_block_stop":
			if ev, ok := tools.finish(p.Index); ok {
				return emit(ctx, ch, ev)
			}
		case "message_delta":
			if p.Usage.OutputTokens > 0 {
				usage.OutputTokens = p.Usage.OutputTokens
			}
		case "message_stop":
			finished = true
			if !emit(ctx, ch, stream.StreamEvent{Type: stream.EventUsage, Usage: usage}) {
				return false
			}
			var meta *stream.Meta
			if requestID != "" {
				meta = &stream.Meta{ProviderRequestID: requestID}
			}
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventDone, Meta: meta})
			return false
		case "error":
			finished = true
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
				Code: p.Error.Type, Message: p.Error.Message, Retryable: p.Error.Type == "overloaded_error",
			}})
			return false
		}
		return ctx.Err() == nil
	})
	return finished, err
}
