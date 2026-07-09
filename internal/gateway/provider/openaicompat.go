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
)

// OpenAICompatConfig configures one OpenAI-compatible provider
// instance. One driver covers every conforming chat/completions
// endpoint (Z.AI GLM, xAI Grok, OpenRouter, local runtimes); instances
// differ only by base URL, key, and headers.
type OpenAICompatConfig struct {
	Name    string
	BaseURL string            // e.g. https://api.x.ai/v1 — no trailing slash
	APIKey  string            // resolved secret value, never logged
	Headers map[string]string // extra request headers
	Timeout time.Duration     // hard per-request timeout, default 5m
}

// OpenAICompat streams from an OpenAI-compatible chat/completions API.
type OpenAICompat struct {
	cfg    OpenAICompatConfig
	client *http.Client
}

// NewOpenAICompat builds the driver; it performs no I/O.
func NewOpenAICompat(cfg OpenAICompatConfig) *OpenAICompat {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &OpenAICompat{cfg: cfg, client: &http.Client{}}
}

func (o *OpenAICompat) Name() string { return o.cfg.Name }
func (o *OpenAICompat) Kind() Kind   { return KindAPI }

func (o *OpenAICompat) Capabilities() []Capability {
	return []Capability{CapChat, CapStreaming, CapTools}
}

// wire types (request)

type oaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaiRequest struct {
	Model         string       `json:"model"`
	Messages      []oaiMessage `json:"messages"`
	Stream        bool         `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Tools     []oaiTool `json:"tools,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

// wire types (stream chunks; only the fields we read)

type oaiChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Stream implements Provider.
func (o *OpenAICompat) Stream(ctx context.Context, req CompletionRequest) (<-chan stream.StreamEvent, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("openaicompat: model is required")
	}
	body, err := json.Marshal(o.buildRequest(req))
	if err != nil {
		return nil, fmt.Errorf("openaicompat: marshal request: %w", err)
	}

	build := func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
		for k, v := range o.cfg.Headers {
			r.Header.Set(k, v)
		}
		return r, nil
	}
	return runStream(ctx, o.client, o.cfg.Timeout, build, o.relay), nil
}

func (o *OpenAICompat) buildRequest(req CompletionRequest) oaiRequest {
	out := oaiRequest{
		Model:     req.Model,
		Stream:    true,
		MaxTokens: req.MaxTokens,
	}
	out.StreamOptions.IncludeUsage = true
	if req.System != "" {
		out.Messages = append(out.Messages, oaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, oaiMessage(m))
	}
	for _, t := range req.Tools {
		var ot oaiTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.InputSchema
		out.Tools = append(out.Tools, ot)
	}
	return out
}

// relay translates the chat/completions SSE stream into normalized
// events.
func (o *OpenAICompat) relay(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (bool, error) {
	tools := newToolAccumulator()
	var usage *stream.Usage
	finished := false
	truncated := false

	err := readSSE(body, func(ev sseEvent) bool {
		if ev.data == "[DONE]" {
			finished = true
			for _, tev := range tools.finishAll() {
				if !emit(ctx, ch, tev) {
					return false
				}
			}
			if usage != nil && !emit(ctx, ch, stream.StreamEvent{Type: stream.EventUsage, Usage: usage}) {
				return false
			}
			if truncated && !emit(ctx, ch, stream.StreamEvent{Type: stream.EventIncomplete, Text: "finish_reason=length"}) {
				return false
			}
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventDone})
			return false
		}

		var c oaiChunk
		if err := json.Unmarshal([]byte(ev.data), &c); err != nil {
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
				Code: "malformed_stream", Message: fmt.Sprintf("bad SSE payload: %v", err), Retryable: true,
			}})
			finished = true
			return false
		}
		if c.Error != nil {
			finished = true
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
				Code: c.Error.Type, Message: c.Error.Message, Retryable: false,
			}})
			return false
		}
		if c.Usage != nil {
			usage = &stream.Usage{
				InputTokens:     c.Usage.PromptTokens,
				OutputTokens:    c.Usage.CompletionTokens,
				CacheReadTokens: c.Usage.PromptTokensDetails.CachedTokens,
			}
		}
		if len(c.Choices) == 0 {
			return ctx.Err() == nil
		}

		choice := c.Choices[0]
		if choice.FinishReason == "length" {
			truncated = true
		}
		if choice.Delta.Content != "" {
			if !emit(ctx, ch, stream.StreamEvent{Type: stream.EventChunk, Text: choice.Delta.Content}) {
				return false
			}
		}
		if r := choice.Delta.ReasoningContent + choice.Delta.Reasoning; r != "" {
			if !emit(ctx, ch, stream.StreamEvent{Type: stream.EventReasoningChunk, Text: r}) {
				return false
			}
		}
		for _, tc := range choice.Delta.ToolCalls {
			if !tools.known(tc.Index) {
				if !emit(ctx, ch, tools.start(tc.Index, tc.ID, tc.Function.Name)) {
					return false
				}
			}
			tools.append(tc.Index, tc.Function.Arguments)
		}
		return ctx.Err() == nil
	})
	return finished, err
}
