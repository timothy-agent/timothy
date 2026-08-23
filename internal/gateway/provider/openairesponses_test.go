package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func orsWrite(w http.ResponseWriter, event, data string) {
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	w.(http.Flusher).Flush()
}

func orsServer(t *testing.T, handler http.HandlerFunc) *OpenAIResponses {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewOpenAIResponses(OpenAIResponsesConfig{
		Name: "ors-test", BaseURL: srv.URL, APIKey: "test-key",
		Timeout: 10 * time.Second,
	})
}

func decodeORSRequest(t *testing.T, r *http.Request) orsRequest {
	t.Helper()
	var req orsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req
}

// --- request build: fresh ---

func TestOpenAIResponsesBuildFreshFullHistory(t *testing.T) {
	t.Parallel()
	var got orsRequest
	p := orsServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = decodeORSRequest(t, r)
		orsWrite(w, "response.completed", `{"response":{"id":"resp_1","status":"completed"}}`)
	})

	req := CompletionRequest{
		Model:  "gpt-5.4",
		System: "be helpful",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	}
	ch, err := p.Stream(t.Context(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collect(t, ch)

	if got.Instructions != "be helpful" {
		t.Fatalf("instructions = %q", got.Instructions)
	}
	if !got.Stream || !got.Store {
		t.Fatalf("stream/store = %v/%v, want true/true", got.Stream, got.Store)
	}
	if got.PreviousResponseID != "" {
		t.Fatalf("previous_response_id = %q, want empty on fresh request", got.PreviousResponseID)
	}
	if len(got.Input) != 2 {
		t.Fatalf("input items = %d, want 2", len(got.Input))
	}
	if got.Input[0].Role != "user" || got.Input[0].Content[0].Type != "input_text" {
		t.Fatalf("input[0] = %+v", got.Input[0])
	}
	if got.Input[1].Role != "assistant" || got.Input[1].Content[0].Type != "output_text" {
		t.Fatalf("input[1] = %+v", got.Input[1])
	}
}

func TestOpenAIResponsesStrictTools(t *testing.T) {
	t.Parallel()
	var got orsRequest
	p := orsServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = decodeORSRequest(t, r)
		orsWrite(w, "response.completed", `{"response":{"id":"resp_1","status":"completed"}}`)
	})

	req := CompletionRequest{
		Model:    "gpt-5.4",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools: []ToolDef{{
			Name: "get_weather", Description: "gets weather",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
		ForceTool: "get_weather",
	}
	ch, err := p.Stream(t.Context(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collect(t, ch)

	if len(got.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(got.Tools))
	}
	tool := got.Tools[0]
	if !tool.Strict {
		t.Fatalf("strict = false, want true for a valid object schema")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %v, want false", schema["additionalProperties"])
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "city" {
		t.Fatalf("required = %v, want [city]", required)
	}
	tc, ok := got.ToolChoice.(map[string]any)
	if !ok || tc["type"] != "function" || tc["name"] != "get_weather" {
		t.Fatalf("tool_choice = %+v", got.ToolChoice)
	}
}

func TestOpenAIResponsesStrictToolFallback(t *testing.T) {
	t.Parallel()
	var got orsRequest
	p := orsServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = decodeORSRequest(t, r)
		orsWrite(w, "response.completed", `{"response":{"id":"resp_1","status":"completed"}}`)
	})

	req := CompletionRequest{
		Model:    "gpt-5.4",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools: []ToolDef{{
			Name: "no_props", Description: "has no properties key",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}
	ch, err := p.Stream(t.Context(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collect(t, ch)

	if got.Tools[0].Strict {
		t.Fatalf("strict = true, want false when the schema has no properties")
	}
	var schema map[string]any
	if err := json.Unmarshal(got.Tools[0].Parameters, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("schema mutated despite no properties: %+v", schema)
	}
}

func TestOpenAIResponsesReasoningEffortPrecedence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		reqEffort  string
		cfgEffort  string
		override   string
		wantEffort string
		wantOmit   bool
	}{
		{name: "empty omits reasoning", wantOmit: true},
		{name: "req low", reqEffort: "low", wantEffort: "low"},
		{name: "config overrides req", reqEffort: "low", cfgEffort: "medium", wantEffort: "medium"},
		{name: "override wins over both", reqEffort: "low", cfgEffort: "medium", override: "high", wantEffort: "high"},
		{name: "none maps to minimal", override: "none", wantEffort: "minimal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got orsRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = decodeORSRequest(t, r)
				orsWrite(w, "response.completed", `{"response":{"id":"resp_1","status":"completed"}}`)
			}))
			t.Cleanup(srv.Close)
			p := NewOpenAIResponses(OpenAIResponsesConfig{
				Name: "ors-test", BaseURL: srv.URL, APIKey: "k",
				Timeout: 10 * time.Second, ReasoningEffort: tt.cfgEffort,
			})
			req := CompletionRequest{
				Model: "gpt-5.4", Messages: []Message{{Role: "user", Content: "hi"}},
				Effort: tt.reqEffort, ReasoningEffortOverride: tt.override,
			}
			ch, err := p.Stream(t.Context(), req)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			collect(t, ch)

			if tt.wantOmit {
				if got.Reasoning != nil {
					t.Fatalf("reasoning = %+v, want omitted", got.Reasoning)
				}
				return
			}
			if got.Reasoning == nil || got.Reasoning.Effort != tt.wantEffort {
				t.Fatalf("reasoning = %+v, want effort %q", got.Reasoning, tt.wantEffort)
			}
		})
	}
}

func TestOpenAIResponsesImages(t *testing.T) {
	t.Parallel()
	var got orsRequest
	p := orsServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = decodeORSRequest(t, r)
		orsWrite(w, "response.completed", `{"response":{"id":"resp_1","status":"completed"}}`)
	})

	req := CompletionRequest{
		Model: "gpt-5.4",
		Messages: []Message{{
			Role: "user", Content: "what is this",
			Images: []ImageData{{MediaType: "image/png", Data: "AAAA"}},
		}},
	}
	ch, err := p.Stream(t.Context(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collect(t, ch)

	if len(got.Input) != 1 || len(got.Input[0].Content) != 2 {
		t.Fatalf("input = %+v", got.Input)
	}
	if got.Input[0].Content[1].Type != "input_image" || got.Input[0].Content[1].ImageURL != "data:image/png;base64,AAAA" {
		t.Fatalf("image part = %+v", got.Input[0].Content[1])
	}
}

// --- request build: continuation ---

func TestOpenAIResponsesContinuationMatchingDriver(t *testing.T) {
	t.Parallel()
	var got orsRequest
	p := orsServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = decodeORSRequest(t, r)
		orsWrite(w, "response.completed", `{"response":{"id":"resp_2","status":"completed"}}`)
	})

	state, _ := json.Marshal(orsState{Driver: "openai-responses", PreviousResponseID: "resp_1"})
	req := CompletionRequest{
		Model: "gpt-5.4",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "get_weather", Input: json.RawMessage(`{"city":"SF"}`)}}},
			{Role: "tool", ToolResult: &ToolResult{ID: "call_1", Content: "sunny"}},
		},
		ProviderState: state,
	}
	ch, err := p.Stream(t.Context(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collect(t, ch)

	if got.PreviousResponseID != "resp_1" {
		t.Fatalf("previous_response_id = %q, want resp_1", got.PreviousResponseID)
	}
	// Only the post-last-assistant messages: the tool result.
	if len(got.Input) != 1 {
		t.Fatalf("input items = %d, want 1 (only the tool result): %+v", len(got.Input), got.Input)
	}
	if got.Input[0].Type != "function_call_output" || got.Input[0].CallID != "call_1" || got.Input[0].Output != "sunny" {
		t.Fatalf("input[0] = %+v", got.Input[0])
	}
}

func TestOpenAIResponsesContinuationForeignDriverIgnored(t *testing.T) {
	t.Parallel()
	var got orsRequest
	p := orsServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = decodeORSRequest(t, r)
		orsWrite(w, "response.completed", `{"response":{"id":"resp_2","status":"completed"}}`)
	})

	state, _ := json.Marshal(orsState{Driver: "openaicompat", PreviousResponseID: "resp_1"})
	req := CompletionRequest{
		Model: "gpt-5.4",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
		ProviderState: state,
	}
	ch, err := p.Stream(t.Context(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collect(t, ch)

	if got.PreviousResponseID != "" {
		t.Fatalf("previous_response_id = %q, want empty for foreign driver state", got.PreviousResponseID)
	}
	if len(got.Input) != 2 {
		t.Fatalf("input items = %d, want full history (2)", len(got.Input))
	}
}

// --- relay ---

func TestOpenAIResponsesRelayFullTurn(t *testing.T) {
	t.Parallel()
	p := orsServer(t, func(w http.ResponseWriter, r *http.Request) {
		orsWrite(w, "response.output_text.delta", `{"delta":"Hel"}`)
		orsWrite(w, "response.output_text.delta", `{"delta":"lo"}`)
		orsWrite(w, "response.output_item.added", `{"output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"get_weather"}}`)
		orsWrite(w, "response.function_call_arguments.delta", `{"output_index":0,"delta":"{\"city"}`)
		orsWrite(w, "response.function_call_arguments.delta", `{"output_index":0,"delta":"\":\"SF\"}"}`)
		orsWrite(w, "response.output_item.done", `{"output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"get_weather"}}`)
		orsWrite(w, "response.completed", `{"response":{"id":"resp_9","status":"completed","usage":{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cached_tokens":40}}}}`)
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "gpt-5.4", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if got := textOf(events, stream.EventChunk); got != "Hello" {
		t.Fatalf("chunks = %q, want Hello", got)
	}
	toolEnds := eventsOfType(events, stream.EventToolEnd)
	if len(toolEnds) != 1 {
		t.Fatalf("tool_end events = %d, want 1", len(toolEnds))
	}
	if toolEnds[0].ToolCall.ID != "call_1" || toolEnds[0].ToolCall.Name != "get_weather" {
		t.Fatalf("tool call = %+v", toolEnds[0].ToolCall)
	}
	if string(toolEnds[0].ToolCall.Input) != `{"city":"SF"}` {
		t.Fatalf("tool input = %s", toolEnds[0].ToolCall.Input)
	}

	usages := eventsOfType(events, stream.EventUsage)
	if len(usages) != 1 {
		t.Fatalf("usage events = %d, want 1", len(usages))
	}
	u := usages[0].Usage
	if u.InputTokens != 60 || u.OutputTokens != 10 || u.CacheReadTokens != 40 {
		t.Fatalf("usage = %+v", u)
	}

	done := events[len(events)-1]
	if done.Type != stream.EventDone {
		t.Fatalf("last = %v, want done", done.Type)
	}
	if done.Meta == nil || len(done.Meta.ProviderState) == 0 {
		t.Fatalf("done Meta.ProviderState missing: %+v", done.Meta)
	}
	var state orsState
	if err := json.Unmarshal(done.Meta.ProviderState, &state); err != nil {
		t.Fatalf("unmarshal provider state: %v", err)
	}
	if state.Driver != "openai-responses" || state.PreviousResponseID != "resp_9" {
		t.Fatalf("state = %+v", state)
	}

	// Ordering: chunks and tool_start before tool_end, usage before done.
	toolStarts := eventsOfType(events, stream.EventToolStart)
	if len(toolStarts) != 1 {
		t.Fatalf("tool_start events = %d, want 1", len(toolStarts))
	}
}

func TestOpenAIResponsesEmptyStream(t *testing.T) {
	t.Parallel()
	p := orsServer(t, func(w http.ResponseWriter, r *http.Request) {
		orsWrite(w, "response.completed", `{"response":{"id":"resp_1","status":"completed","usage":{"input_tokens":5,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}}}`)
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "gpt-5.4", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if len(eventsOfType(events, stream.EventChunk)) != 0 {
		t.Fatalf("want no chunks: %+v", events)
	}
	if len(eventsOfType(events, stream.EventUsage)) != 1 {
		t.Fatalf("want one usage event: %+v", events)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last = %v, want done", lastType(t, events))
	}
}

func TestOpenAIResponsesFailedEvent(t *testing.T) {
	t.Parallel()
	p := orsServer(t, func(w http.ResponseWriter, r *http.Request) {
		orsWrite(w, "response.failed", `{"error":{"message":"boom"}}`)
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "gpt-5.4", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	errs := eventsOfType(events, stream.EventError)
	if len(errs) != 1 || errs[0].Err.Message != "boom" || !errs[0].Err.Retryable {
		t.Fatalf("error event = %+v", errs)
	}
}

func TestOpenAIResponsesIncomplete(t *testing.T) {
	t.Parallel()
	p := orsServer(t, func(w http.ResponseWriter, r *http.Request) {
		orsWrite(w, "response.output_text.delta", `{"delta":"partial"}`)
		orsWrite(w, "response.incomplete", `{"response":{"incomplete_details":{"reason":"max_output_tokens"}}}`)
	})

	ch, err := p.Stream(t.Context(), CompletionRequest{Model: "gpt-5.4", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	incs := eventsOfType(events, stream.EventIncomplete)
	if len(incs) != 1 || incs[0].Text != "max_output_tokens" {
		t.Fatalf("incomplete event = %+v", incs)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last = %v, want done", lastType(t, events))
	}
}

// --- stale-state 400 retry ---

func TestOpenAIResponsesStaleStateRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var sawPreviousID []string
	p := orsServer(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeORSRequest(t, r)
		sawPreviousID = append(sawPreviousID, req.PreviousResponseID)
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		orsWrite(w, "response.output_text.delta", `{"delta":"ok"}`)
		orsWrite(w, "response.completed", `{"response":{"id":"resp_new","status":"completed"}}`)
	})

	state, _ := json.Marshal(orsState{Driver: "openai-responses", PreviousResponseID: "resp_stale"})
	req := CompletionRequest{
		Model: "gpt-5.4",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
		ProviderState: state,
	}
	ch, err := p.Stream(t.Context(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch)

	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (retry after 400)", calls.Load())
	}
	if len(sawPreviousID) != 2 || sawPreviousID[0] != "resp_stale" || sawPreviousID[1] != "" {
		t.Fatalf("previous_response_id across attempts = %v, want [resp_stale, \"\"]", sawPreviousID)
	}
	if got := textOf(events, stream.EventChunk); got != "ok" {
		t.Fatalf("chunks = %q, want ok", got)
	}
	if len(eventsOfType(events, stream.EventError)) != 0 {
		t.Fatalf("want no error event surfaced after successful retry: %+v", events)
	}
	if lastType(t, events) != stream.EventDone {
		t.Fatalf("last = %v, want done", lastType(t, events))
	}
}

func TestStrictSchemaNestedObjects(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {
			"summary": {"type": "string"},
			"units": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"title": {"type": "string"},
						"artifacts": {"type": "array", "items": {"type": "string"}},
						"verify": {"type": "object", "properties": {"cmd": {"type": "string"}}}
					}
				}
			}
		}
	}`)
	out, strict := strictSchema(raw)
	if !strict {
		t.Fatal("want strict=true")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertStrictNode := func(path string, node map[string]any, wantRequired []string) {
		t.Helper()
		if ap, ok := node["additionalProperties"].(bool); !ok || ap {
			t.Fatalf("%s: additionalProperties = %v, want false", path, node["additionalProperties"])
		}
		req, _ := node["required"].([]any)
		if len(req) != len(wantRequired) {
			t.Fatalf("%s: required = %v, want %v", path, req, wantRequired)
		}
		for i, want := range wantRequired {
			if req[i] != want {
				t.Fatalf("%s: required[%d] = %v, want %v", path, i, req[i], want)
			}
		}
	}
	assertStrictNode("top", m, []string{"summary", "units"})
	units := m["properties"].(map[string]any)["units"].(map[string]any)
	items := units["items"].(map[string]any)
	assertStrictNode("units.items", items, []string{"artifacts", "title", "verify"})
	verify := items["properties"].(map[string]any)["verify"].(map[string]any)
	assertStrictNode("units.items.verify", verify, []string{"cmd"})
	// The string-items array must NOT gain object keys.
	artifacts := items["properties"].(map[string]any)["artifacts"].(map[string]any)
	strItems := artifacts["items"].(map[string]any)
	if _, ok := strItems["additionalProperties"]; ok {
		t.Fatal("string items must not gain additionalProperties")
	}
}

func TestStrictSchemaNoPropertiesStaysLoose(t *testing.T) {
	raw := json.RawMessage(`{"type": "object"}`)
	out, strict := strictSchema(raw)
	if strict {
		t.Fatal("want strict=false for schema without properties")
	}
	if string(out) != `{"type": "object"}` {
		t.Fatalf("schema changed: %s", out)
	}
}

func TestAppendMessageDropsEmptyMessage(t *testing.T) {
	items := appendMessage(nil, Message{Role: "assistant", Content: ""})
	if len(items) != 0 {
		t.Fatalf("empty assistant message must be dropped, got %d items", len(items))
	}
	items = appendMessage(nil, Message{Role: "user", Content: "hi"})
	if len(items) != 1 || len(items[0].Content) != 1 {
		t.Fatalf("non-empty message must map to one item, got %+v", items)
	}
}
