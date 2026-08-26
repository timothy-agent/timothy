package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/sse"
)

// OpenAICompatConfig configures one OpenAI-compatible provider
// instance. One driver covers every conforming chat/completions
// endpoint (Z.AI GLM, xAI Grok, local runtimes); instances differ only
// by base URL, key, and headers.
type OpenAICompatConfig struct {
	Name    string
	BaseURL string            // e.g. https://api.x.ai/v1 — no trailing slash
	APIKey  string            // resolved secret value, never logged
	Headers map[string]string // extra request headers
	Timeout time.Duration     // hard per-request timeout, default 5m
	// ReasoningEffort, when set, overrides the request's own Effort on
	// every call to this provider instance (D-040) — e.g. "none" to
	// disable a local Ollama model's thinking on its OpenAI-compat
	// endpoint, where the native "think": false flag is ignored.
	ReasoningEffort string
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
	return []Capability{CapChat, CapStreaming, CapTools, CapEmbeddings, CapVision}
}

// Embed implements Embedder via the /embeddings endpoint, under the
// same retry/backoff hardening as the streaming path.
func (o *OpenAICompat) Embed(ctx context.Context, model string, texts []string) ([][]float32, *stream.Usage, error) {
	if model == "" {
		return nil, nil, fmt.Errorf("openaicompat: model is required")
	}
	if len(texts) == 0 {
		return nil, nil, fmt.Errorf("openaicompat: no texts to embed")
	}
	body, err := json.Marshal(map[string]any{"model": model, "input": texts})
	if err != nil {
		return nil, nil, fmt.Errorf("openaicompat: marshal embed request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, o.cfg.Timeout)
	defer cancel()
	resp, err := doWithRetry(callCtx, o.client, maxRetries, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(callCtx, http.MethodPost, o.cfg.BaseURL+"/embeddings", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("openaicompat: embed request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
		for k, v := range o.cfg.Headers {
			req.Header.Set(k, v)
		}
		return req, nil
	}, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("openaicompat: embed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, nil, fmt.Errorf("openaicompat: decode embeddings: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, nil, fmt.Errorf("openaicompat: got %d embeddings for %d texts", len(out.Data), len(texts))
	}

	// Fill by provider-reported index, then verify every slot was
	// filled exactly once — gaps, duplicates, and out-of-range indices
	// are provider bugs surfaced as errors, never nil vectors.
	vecs := make([][]float32, len(texts))
	seen := make([]bool, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, nil, fmt.Errorf("openaicompat: embedding index %d out of range", d.Index)
		}
		if seen[d.Index] {
			return nil, nil, fmt.Errorf("openaicompat: duplicate embedding index %d", d.Index)
		}
		seen[d.Index] = true
		vecs[d.Index] = d.Embedding
	}
	for i, ok := range seen {
		if !ok {
			return nil, nil, fmt.Errorf("openaicompat: missing embedding for input %d", i)
		}
	}
	return vecs, &stream.Usage{InputTokens: out.Usage.PromptTokens}, nil
}

// wire types (request)

type oaiMessage struct {
	Role string `json:"role"`
	// Content is a plain string for every text-only message (the
	// original, unchanged wire shape — some OpenAI-compat servers
	// reject a content-parts array) and only becomes []oaiContentPart
	// when the message carries images (D-045).
	Content    any           `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

// oaiContentPart is one OpenAI chat/completions content-parts array
// entry: {"type":"text",...} or {"type":"image_url",...}.
type oaiContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
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
	// MaxCompletionTokens replaces MaxTokens on retry when the backend
	// rejects max_tokens outright (OpenAI's reasoning models: "Use
	// 'max_completion_tokens' instead") — see swapAndRetryOn400. Never
	// set on the first attempt: plenty of compat backends only know
	// max_tokens.
	MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
	// ReasoningEffort is the D-020 dial. Not every OpenAI-compatible
	// backend tolerates the field: Ollama returns HTTP 400 for models
	// that don't recognize it. Stream retries once without it on that
	// exact failure (see retryOn400).
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// ToolChoice forces a specific tool call (D-063: CompletionRequest.ForceTool).
	ToolChoice any `json:"tool_choice,omitempty"`
}

// wire types (stream chunks; only the fields we read)

type oaiChunk struct {
	ID      string `json:"id"`
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
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
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
	wire := o.buildRequest(req)
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: marshal request: %w", err)
	}

	ch := runStream(ctx, o.client, o.cfg.Timeout, retriesFor(req.FinalAttempt), o.buildFor(body), o.relay)

	// Two known HTTP 400 shapes, both surfacing as a single permanent
	// error event with no prior stream activity: OpenAI's reasoning
	// models reject max_tokens (regardless of reasoning_effort), and
	// some OpenAI-compatible backends (Ollama, qwen2.5 family) reject an
	// unrecognized reasoning_effort field. Retry once with whichever fix
	// applies rather than failing the whole turn over either.
	return retryOn400(ctx, o, req, wire, ch), nil
}

// buildFor returns the request builder for a fixed, already-marshaled
// body.
func (o *OpenAICompat) buildFor(body []byte) func(ctx context.Context) (*http.Request, error) {
	return func(ctx context.Context) (*http.Request, error) {
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
}

// retryOn400 peeks the first event off first. Per the runStream
// contract, a request-level failure (doWithRetry returning a permanent
// error) emits exactly one error event and closes the channel —
// nothing can follow it. So a bare http_400 as that first event is
// unambiguously "rejected before any stream activity"; retry once with
// whichever fix the error names. Any other first event means relay
// already ran, so pass everything through unchanged, event by event,
// keeping the success path streaming live rather than buffering.
func retryOn400(ctx context.Context, o *OpenAICompat, req CompletionRequest, wire oaiRequest, first <-chan stream.StreamEvent) <-chan stream.StreamEvent {
	out := make(chan stream.StreamEvent)
	go func() {
		defer close(out)
		ev, ok := <-first
		if !ok {
			return
		}
		if isHTTP400(ev) {
			// Two known 400 shapes, mutually exclusive per backend: OpenAI's
			// reasoning models reject max_tokens (the error names the
			// replacement field) regardless of whether reasoning_effort was
			// sent; everything else here is the Ollama-style
			// reasoning_effort rejection, which only applies when that field
			// was actually on the request. Mutate only what the error asked
			// for, so the other knob survives the retry; if neither
			// condition matches, there's nothing safe to retry.
			if ev.Err != nil && strings.Contains(ev.Err.Message, "max_completion_tokens") {
				retryMaxCompletionTokens(ctx, o, req, wire, out)
				return
			}
			if wire.ReasoningEffort != "" {
				retryReasoningEffortStripped(ctx, o, req, wire, out)
				return
			}
		}
		if !emit(ctx, out, ev) {
			return
		}
		for ev := range first {
			if !emit(ctx, out, ev) {
				return
			}
		}
	}()
	return out
}

// retryMaxCompletionTokens rebuilds the request with the token cap
// moved from max_tokens to max_completion_tokens and relays the
// retried stream to out. A model that rejects max_tokens is a
// reasoning model, whose hidden thinking tokens also draw against
// max_completion_tokens; honoring a tiny caller cap (e.g. an admin
// connection probe's MaxTokens=1) would guarantee the completion is
// cut off before any output. Floor the swapped cap at 512, which
// covers probe-scale requests — real chat callers already pass larger
// budgets, so this never shrinks a legitimate request.
func retryMaxCompletionTokens(ctx context.Context, o *OpenAICompat, req CompletionRequest, wire oaiRequest, out chan<- stream.StreamEvent) {
	wire.MaxCompletionTokens = max(wire.MaxTokens, 512)
	wire.MaxTokens = 0
	body, err := json.Marshal(wire)
	if err != nil {
		emit(ctx, out, errEvent(fmt.Errorf("openaicompat: marshal retry request: %w", err)))
		return
	}
	retry := runStream(ctx, o.client, o.cfg.Timeout, retriesFor(req.FinalAttempt), o.buildFor(body), o.relay)
	for ev := range retry {
		if !emit(ctx, out, ev) {
			return
		}
	}
}

// retryReasoningEffortStripped rebuilds the request without
// ReasoningEffort and relays the retried stream to out.
func retryReasoningEffortStripped(ctx context.Context, o *OpenAICompat, req CompletionRequest, wire oaiRequest, out chan<- stream.StreamEvent) {
	wire.ReasoningEffort = ""
	body, err := json.Marshal(wire)
	if err != nil {
		emit(ctx, out, errEvent(fmt.Errorf("openaicompat: marshal retry request: %w", err)))
		return
	}
	retry := runStream(ctx, o.client, o.cfg.Timeout, retriesFor(req.FinalAttempt), o.buildFor(body), o.relay)
	for ev := range retry {
		if !emit(ctx, out, ev) {
			return
		}
	}
}

// isHTTP400 reports whether ev is the terminal error event for an
// HTTP 400 response.
func isHTTP400(ev stream.StreamEvent) bool {
	return ev.Type == stream.EventError && ev.Err != nil && ev.Err.Code == "http_400"
}

// imageContentParts builds the OpenAI chat/completions content-parts
// array for a message carrying images: a leading text part (only when
// non-empty — an image-only message needs no empty text part) followed
// by one image_url part per image, each a data: URL (D-045).
func imageContentParts(text string, images []ImageData) []oaiContentPart {
	parts := make([]oaiContentPart, 0, len(images)+1)
	if text != "" {
		parts = append(parts, oaiContentPart{Type: "text", Text: text})
	}
	for _, img := range images {
		parts = append(parts, oaiContentPart{
			Type: "image_url",
			ImageURL: &struct {
				URL string `json:"url"`
			}{URL: "data:" + img.MediaType + ";base64," + img.Data},
		})
	}
	return parts
}

func (o *OpenAICompat) buildRequest(req CompletionRequest) oaiRequest {
	out := oaiRequest{
		Model:     req.Model,
		Stream:    true,
		MaxTokens: req.MaxTokens,
	}
	out.StreamOptions.IncludeUsage = true
	if req.Effort == "low" {
		out.ReasoningEffort = "low"
	}
	// A provider-level override wins over the per-request hint (D-040):
	// some OpenAI-compat backends need reasoning_effort forced (e.g.
	// "none") regardless of what the caller asked for.
	if o.cfg.ReasoningEffort != "" {
		out.ReasoningEffort = o.cfg.ReasoningEffort
	}
	// A chain-entry (per-model) override wins over both: some models on
	// an otherwise-fine provider reject tool calls on /chat/completions
	// unless reasoning_effort is an exact value, while other models on
	// the same provider row have no such restriction — the provider-
	// level dial above can't express that.
	if req.ReasoningEffortOverride != "" {
		out.ReasoningEffort = req.ReasoningEffortOverride
	}
	if req.System != "" {
		out.Messages = append(out.Messages, oaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		om := oaiMessage{Role: m.Role, Content: m.Content}
		if len(m.Images) > 0 {
			om.Content = imageContentParts(m.Content, m.Images)
		}
		switch {
		case m.Role == "tool" && m.ToolResult != nil:
			om.ToolCallID = m.ToolResult.ID
			content := m.ToolResult.Content
			if m.ToolResult.IsError {
				// The chat/completions shape has no is_error flag;
				// the marker keeps the failure visible to the model.
				content = "ERROR: " + content
			}
			om.Content = content
		case len(m.ToolCalls) > 0:
			for _, tc := range m.ToolCalls {
				var oc oaiToolCall
				oc.ID = tc.ID
				oc.Type = "function"
				oc.Function.Name = tc.Name
				oc.Function.Arguments = string(tc.Input)
				if oc.Function.Arguments == "" {
					oc.Function.Arguments = "{}"
				}
				om.ToolCalls = append(om.ToolCalls, oc)
			}
		}
		out.Messages = append(out.Messages, om)
	}
	for _, t := range req.Tools {
		var ot oaiTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.InputSchema
		out.Tools = append(out.Tools, ot)
	}
	if req.ForceTool != "" && len(out.Tools) > 0 {
		out.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": req.ForceTool}}
	}
	return out
}

// relay translates the chat/completions SSE stream into normalized
// events.
func (o *OpenAICompat) relay(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (bool, error) {
	tools := newToolAccumulator()
	var usage *stream.Usage
	var requestID string
	finished := false
	truncated := false

	err := sse.Read(body, func(ev sse.Event) bool {
		if ev.Data == "[DONE]" {
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
			var meta *stream.Meta
			if requestID != "" {
				meta = &stream.Meta{ProviderRequestID: requestID}
			}
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventDone, Meta: meta})
			return false
		}

		var c oaiChunk
		if err := json.Unmarshal([]byte(ev.Data), &c); err != nil {
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
		if c.ID != "" {
			requestID = c.ID
		}
		if c.Usage != nil {
			// OpenAI-style prompt_tokens INCLUDES cached tokens;
			// normalize to the Anthropic-style split (InputTokens
			// excludes cache reads) so cost math is uniform.
			cached := c.Usage.PromptTokensDetails.CachedTokens
			usage = &stream.Usage{
				InputTokens:     max(c.Usage.PromptTokens-cached, 0),
				OutputTokens:    c.Usage.CompletionTokens,
				CacheReadTokens: cached,
				ReasoningTokens: c.Usage.CompletionTokensDetails.ReasoningTokens,
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
