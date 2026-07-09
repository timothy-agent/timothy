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
	return []Capability{CapChat, CapStreaming, CapTools}
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
	Messages  []Message            `json:"messages"`
	Tools     []anthropicTool      `json:"tools,omitempty"`
}

// anthropic wire types (SSE payloads; only the fields we read)

type anthropicStreamPayload struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
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
	return runStream(ctx, a.client, a.cfg.Timeout, build, a.relay), nil
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
		Messages:  req.Messages,
	}
	if req.System != "" {
		// cache_control on the system block enables prompt caching for
		// the stable prefix (D-018).
		out.System = []anthropicTextBlock{{
			Type:         "text",
			Text:         req.System,
			CacheControl: json.RawMessage(`{"type":"ephemeral"}`),
		}}
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, anthropicTool(t))
	}
	return out
}

// relay translates the Messages API SSE stream into normalized events.
func (a *Anthropic) relay(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (bool, error) {
	tools := newToolAccumulator()
	usage := &stream.Usage{}
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
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventDone})
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
