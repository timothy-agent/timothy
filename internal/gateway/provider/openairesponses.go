package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/sse"
)

// OpenAIResponsesConfig configures one OpenAI Responses API provider
// instance (D-067): every reasoning-class OpenAI model (gpt-5.4,
// gpt-5.4-mini, gpt-5.6-*) returns empty streams over chat/completions
// on tool-mandatory turns — the Responses API is required for
// reasoning-item continuity.
type OpenAIResponsesConfig struct {
	Name    string
	BaseURL string            // e.g. https://api.openai.com/v1 — no trailing slash
	APIKey  string            // resolved secret value, never logged
	Headers map[string]string // extra request headers
	Timeout time.Duration     // hard per-request timeout, default 5m
	// ReasoningEffort, when set, overrides the request's own Effort on
	// every call to this provider instance (D-040), same precedence
	// rules as OpenAICompatConfig.
	ReasoningEffort string
}

// OpenAIResponses streams from OpenAI's Responses API.
type OpenAIResponses struct {
	cfg    OpenAIResponsesConfig
	client *http.Client
}

// NewOpenAIResponses builds the driver; it performs no I/O.
func NewOpenAIResponses(cfg OpenAIResponsesConfig) *OpenAIResponses {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &OpenAIResponses{cfg: cfg, client: &http.Client{}}
}

func (o *OpenAIResponses) Name() string { return o.cfg.Name }
func (o *OpenAIResponses) Kind() Kind   { return KindAPI }

func (o *OpenAIResponses) Capabilities() []Capability {
	return []Capability{CapChat, CapStreaming, CapTools, CapVision}
}

// wire types (request)

type orsContentPart struct {
	Type     string `json:"type"` // "input_text" | "output_text" | "input_image"
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// orsInputItem is one entry of the Responses API "input" array: a
// message, a function call, or a function call's output. Only the
// fields matching Type are populated.
type orsInputItem struct {
	Type    string           `json:"type"`
	Role    string           `json:"role,omitempty"`
	Content []orsContentPart `json:"content,omitempty"`
	// function_call fields
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// function_call_output field
	Output string `json:"output,omitempty"`
}

type orsTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type orsRequest struct {
	Model              string         `json:"model"`
	Stream             bool           `json:"stream"`
	Instructions       string         `json:"instructions,omitempty"`
	Input              []orsInputItem `json:"input"`
	MaxOutputTokens    int            `json:"max_output_tokens,omitempty"`
	Reasoning          *orsReasoning  `json:"reasoning,omitempty"`
	Tools              []orsTool      `json:"tools,omitempty"`
	ToolChoice         any            `json:"tool_choice,omitempty"`
	Store              bool           `json:"store"`
	PreviousResponseID string         `json:"previous_response_id,omitempty"`
}

type orsReasoning struct {
	Effort string `json:"effort"`
}

// orsState is the opaque continuation state this driver reads from
// CompletionRequest.ProviderState and writes back on
// stream.Meta.ProviderState — the reasoning-item passback the
// Responses API requires without ever storing reasoning content
// itself (D-067).
type orsState struct {
	Driver             string `json:"driver"`
	PreviousResponseID string `json:"previous_response_id"`
}

const orsDriverTag = "openai-responses"

// strictSchema normalizes t's input schema for OpenAI's strict
// function-calling mode: every object node — top level AND nested
// (array items, $defs, anyOf/oneOf/allOf branches) — must carry
// additionalProperties:false and list every declared property as
// required, or the API rejects the whole request with http_400
// ("'additionalProperties' is required to be supplied and to be
// false" naming the nested context). If unmarshal fails or the top
// level has no properties, the schema is returned unchanged and
// strict is false for that tool.
func strictSchema(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return raw, false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw, false
	}
	props, ok := m["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return raw, false
	}
	strictify(m)
	out, err := json.Marshal(m)
	if err != nil {
		return raw, false
	}
	return out, true
}

// strictify walks a schema node applying strict-mode object rules at
// every depth.
func strictify(node map[string]any) {
	if props, ok := node["properties"].(map[string]any); ok {
		required := make([]string, 0, len(props))
		for k := range props {
			required = append(required, k)
		}
		sort.Strings(required)
		node["additionalProperties"] = false
		node["required"] = required
		for _, v := range props {
			if child, ok := v.(map[string]any); ok {
				strictify(child)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		strictify(items)
	}
	if defs, ok := node["$defs"].(map[string]any); ok {
		for _, v := range defs {
			if child, ok := v.(map[string]any); ok {
				strictify(child)
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if branches, ok := node[key].([]any); ok {
			for _, v := range branches {
				if child, ok := v.(map[string]any); ok {
					strictify(child)
				}
			}
		}
	}
}

// imageInputParts builds the input_image content parts for a user
// message carrying images.
func imageInputParts(images []ImageData) []orsContentPart {
	parts := make([]orsContentPart, 0, len(images))
	for _, img := range images {
		parts = append(parts, orsContentPart{
			Type:     "input_image",
			ImageURL: "data:" + img.MediaType + ";base64," + img.Data,
		})
	}
	return parts
}

// messageItem builds one message input item for a user/assistant
// message.
func messageItem(m Message) orsInputItem {
	textType := "input_text"
	if m.Role == "assistant" {
		textType = "output_text"
	}
	item := orsInputItem{Type: "message", Role: m.Role}
	if m.Content != "" {
		item.Content = append(item.Content, orsContentPart{Type: textType, Text: m.Content})
	}
	if m.Role == "user" && len(m.Images) > 0 {
		item.Content = append(item.Content, imageInputParts(m.Images)...)
	}
	return item
}

// appendMessage translates one req.Messages entry into its input
// item(s), mirroring openaicompat.buildRequest's per-message switch:
// a plain message becomes a message item, an assistant's tool calls
// each become a function_call item (after the text item when
// non-empty), and a tool result becomes a function_call_output item.
func appendMessage(items []orsInputItem, m Message) []orsInputItem {
	switch {
	case m.Role == "tool" && m.ToolResult != nil:
		output := m.ToolResult.Content
		if m.ToolResult.IsError {
			// Mirrors openaicompat's chat/completions convention: no
			// native is_error flag on this shape either.
			output = "ERROR: " + output
		}
		return append(items, orsInputItem{
			Type: "function_call_output", CallID: m.ToolResult.ID, Output: output,
		})
	case len(m.ToolCalls) > 0:
		if m.Content != "" {
			items = append(items, messageItem(m))
		}
		for _, tc := range m.ToolCalls {
			args := string(tc.Input)
			if args == "" {
				args = "{}"
			}
			items = append(items, orsInputItem{
				Type: "function_call", CallID: tc.ID, Name: tc.Name, Arguments: args,
			})
		}
		return items
	default:
		item := messageItem(m)
		if len(item.Content) == 0 {
			// The Responses API rejects a message item with no content
			// ("Missing required parameter: 'input[N].content'"). An
			// empty assistant entry is a real history shape — a
			// tool-call-only reply whose text the loop recorded as "" —
			// so drop the item instead of failing the whole request.
			return items
		}
		return append(items, item)
	}
}

// buildRequest builds the wire request. Fresh (state == nil) maps the
// full req.Messages history; a matching continuation state instead
// sets previous_response_id and maps only the messages after the last
// assistant message — within a turn the loop appends
// [assistant(tool_calls), tool results…] each step, and OpenAI holds
// the reasoning items server-side under the previous response id
// (D-067).
func (o *OpenAIResponses) buildRequest(req CompletionRequest, state *orsState) orsRequest {
	out := orsRequest{
		Model:           req.Model,
		Stream:          true,
		Instructions:    req.System,
		MaxOutputTokens: req.MaxTokens,
		Store:           true,
	}

	effort := ""
	if req.Effort == "low" {
		effort = "low"
	}
	// A provider-level override wins over the per-request hint (D-040);
	// a chain-entry override wins over both (D-063 precedent) — same
	// precedence as openaicompat.buildRequest.
	if o.cfg.ReasoningEffort != "" {
		effort = o.cfg.ReasoningEffort
	}
	if req.ReasoningEffortOverride != "" {
		effort = req.ReasoningEffortOverride
	}
	if effort == "none" {
		// The Responses API rejects "none" outright; "minimal" is its
		// closest equivalent for disabling visible reasoning effort.
		effort = "minimal"
	}
	if effort != "" {
		out.Reasoning = &orsReasoning{Effort: effort}
	}

	msgs := req.Messages
	if state != nil {
		msgs = messagesAfterLastAssistant(req.Messages)
		out.PreviousResponseID = state.PreviousResponseID
	}
	for _, m := range msgs {
		out.Input = appendMessage(out.Input, m)
	}

	for _, t := range req.Tools {
		schema, strict := strictSchema(t.InputSchema)
		out.Tools = append(out.Tools, orsTool{
			Type: "function", Name: t.Name, Description: t.Description,
			Parameters: schema, Strict: strict,
		})
	}
	if req.ForceTool != "" && len(out.Tools) > 0 {
		out.ToolChoice = map[string]any{"type": "function", "name": req.ForceTool}
	}
	return out
}

// messagesAfterLastAssistant returns the suffix of msgs strictly after
// the last assistant message — the tool outputs (and any trailing user
// correction) OpenAI hasn't seen yet under the chained response id. No
// assistant message at all means the whole history is "after" (empty
// slice would drop it); that shouldn't arise on a continuation in
// practice but is handled safely by returning everything.
func messagesAfterLastAssistant(msgs []Message) []Message {
	last := -1
	for i, m := range msgs {
		if m.Role == "assistant" {
			last = i
		}
	}
	if last < 0 {
		return msgs
	}
	return msgs[last+1:]
}

// parseState reads req.ProviderState, returning nil unless it names
// this driver — a chain failover mid-turn can change the serving
// driver, and the state carries its origin so a different driver
// ignores foreign state rather than misinterpreting it.
func parseState(raw json.RawMessage) *orsState {
	if len(raw) == 0 {
		return nil
	}
	var s orsState
	if err := json.Unmarshal(raw, &s); err != nil || s.Driver != orsDriverTag {
		return nil
	}
	return &s
}

// Stream implements Provider.
func (o *OpenAIResponses) Stream(ctx context.Context, req CompletionRequest) (<-chan stream.StreamEvent, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("openairesponses: model is required")
	}
	state := parseState(req.ProviderState)
	wire := o.buildRequest(req, state)
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: marshal request: %w", err)
	}

	ch := runStream(ctx, o.client, o.cfg.Timeout, retriesFor(req.FinalAttempt), o.buildFor(body), o.relay)
	if state == nil {
		return ch, nil
	}
	// A stale or foreign previous_response_id degrades to a fresh
	// request (full history) rather than killing the turn — see
	// retryStaleState.
	return retryStaleState(ctx, o, req, ch), nil
}

// buildFor returns the request builder for a fixed, already-marshaled
// body.
func (o *OpenAIResponses) buildFor(body []byte) func(ctx context.Context) (*http.Request, error) {
	return func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.BaseURL+"/responses", bytes.NewReader(body))
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

// retryStaleState peeks the first event off first (same pattern as
// openaicompat.retryOn400): a bare http_400 as the very first event
// means the request was rejected before any stream activity, which for
// a previous_response_id request most likely means that id is stale or
// belongs to a different conversation. Rebuild without continuation
// state (full history) and retry once rather than failing the whole
// turn over a passback quirk.
func retryStaleState(ctx context.Context, o *OpenAIResponses, req CompletionRequest, first <-chan stream.StreamEvent) <-chan stream.StreamEvent {
	out := make(chan stream.StreamEvent)
	go func() {
		defer close(out)
		ev, ok := <-first
		if !ok {
			return
		}
		if isHTTP400(ev) {
			wire := o.buildRequest(req, nil)
			body, err := json.Marshal(wire)
			if err != nil {
				emit(ctx, out, errEvent(fmt.Errorf("openairesponses: marshal retry request: %w", err)))
				return
			}
			retry := runStream(ctx, o.client, o.cfg.Timeout, retriesFor(req.FinalAttempt), o.buildFor(body), o.relay)
			for ev := range retry {
				if !emit(ctx, out, ev) {
					return
				}
			}
			return
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

// wire types (SSE payloads; only the fields we read)

type orsOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
}

type orsUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens        int `json:"output_tokens"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type orsResponse struct {
	ID                string    `json:"id"`
	Status            string    `json:"status"`
	Usage             *orsUsage `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type orsEventPayload struct {
	Delta       string         `json:"delta"`
	OutputIndex int            `json:"output_index"`
	Item        *orsOutputItem `json:"item"`
	Response    *orsResponse   `json:"response"`
	Error       *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// relay translates the Responses API SSE stream into normalized
// events.
func (o *OpenAIResponses) relay(ctx context.Context, body io.Reader, ch chan<- stream.StreamEvent) (bool, error) {
	tools := newToolAccumulator()
	finished := false
	lastTextIndex := -1 // OutputIndex of the previous text delta; -1 means none seen yet

	err := sse.Read(body, func(ev sse.Event) bool {
		var p orsEventPayload
		if ev.Data != "" {
			if err := json.Unmarshal([]byte(ev.Data), &p); err != nil {
				emit(ctx, ch, stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
					Code: "malformed_stream", Message: fmt.Sprintf("bad SSE payload: %v", err), Retryable: true,
				}})
				finished = true
				return false
			}
		}

		switch ev.Name {
		case "response.output_text.delta":
			// Two message output items in one response (e.g. answer + a
			// "## Sources" section) must not fuse: mark the item boundary.
			text := p.Delta
			if lastTextIndex != -1 && p.OutputIndex != lastTextIndex {
				text = "\n\n" + text
			}
			lastTextIndex = p.OutputIndex
			return emit(ctx, ch, stream.StreamEvent{Type: stream.EventChunk, Text: text})
		case "response.reasoning_summary_text.delta":
			return emit(ctx, ch, stream.StreamEvent{Type: stream.EventReasoningChunk, Text: p.Delta})
		case "response.output_item.added":
			if p.Item != nil && p.Item.Type == "function_call" {
				return emit(ctx, ch, tools.start(p.OutputIndex, p.Item.CallID, p.Item.Name))
			}
			return true
		case "response.function_call_arguments.delta":
			tools.append(p.OutputIndex, p.Delta)
			return true
		case "response.output_item.done":
			if p.Item != nil && p.Item.Type == "function_call" {
				if tev, ok := tools.finish(p.OutputIndex); ok {
					return emit(ctx, ch, tev)
				}
			}
			return true
		case "response.completed":
			if p.Response != nil && p.Response.Status == "failed" {
				finished = true
				msg := "response status failed"
				if p.Error != nil && p.Error.Message != "" {
					msg = p.Error.Message
				}
				code, retryable := "provider_error", true
				if isContextLengthMessage(msg) {
					code, retryable = "context_length", false
				}
				emit(ctx, ch, stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
					Code: code, Message: msg, Retryable: retryable,
				}})
				return false
			}
			finished = true
			for _, tev := range tools.finishAll() {
				if !emit(ctx, ch, tev) {
					return false
				}
			}
			var state *orsState
			var requestID string
			if p.Response != nil {
				if p.Response.Usage != nil {
					cached := p.Response.Usage.InputTokensDetails.CachedTokens
					u := &stream.Usage{
						InputTokens:     max(p.Response.Usage.InputTokens-cached, 0),
						OutputTokens:    p.Response.Usage.OutputTokens,
						CacheReadTokens: cached,
						ReasoningTokens: p.Response.Usage.OutputTokensDetails.ReasoningTokens,
					}
					if !emit(ctx, ch, stream.StreamEvent{Type: stream.EventUsage, Usage: u}) {
						return false
					}
				}
				state = &orsState{Driver: orsDriverTag, PreviousResponseID: p.Response.ID}
				requestID = p.Response.ID
			}
			var meta *stream.Meta
			if state != nil {
				if raw, err := json.Marshal(state); err == nil {
					meta = &stream.Meta{ProviderState: raw, ProviderRequestID: requestID}
				}
			}
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventDone, Meta: meta})
			return false
		case "response.failed", "error":
			finished = true
			msg := "response failed"
			if p.Error != nil && p.Error.Message != "" {
				msg = p.Error.Message
			} else if p.Response != nil {
				msg = fmt.Sprintf("response status %s", p.Response.Status)
			}
			code, retryable := "provider_error", true
			if isContextLengthMessage(msg) {
				code, retryable = "context_length", false
			}
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
				Code: code, Message: msg, Retryable: retryable,
			}})
			return false
		case "response.incomplete":
			finished = true
			reason := "response incomplete"
			if p.Response != nil && p.Response.IncompleteDetails != nil && p.Response.IncompleteDetails.Reason != "" {
				reason = p.Response.IncompleteDetails.Reason
			}
			if !emit(ctx, ch, stream.StreamEvent{Type: stream.EventIncomplete, Text: reason}) {
				return false
			}
			emit(ctx, ch, stream.StreamEvent{Type: stream.EventDone})
			return false
		default:
			// Unknown event names (response.created, response.in_progress,
			// output_item indices for message items, etc.) carry nothing
			// this driver needs to forward.
			return ctx.Err() == nil
		}
	})
	return finished, err
}
