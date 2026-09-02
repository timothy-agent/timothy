package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func toolLoopMessages() []Message {
	return []Message{
		{Role: "user", Content: "what time is it in Nairobi?"},
		{Role: "assistant", Content: "Checking.", ToolCalls: []ToolCall{
			{ID: "call_1", Name: "get_current_time", Input: json.RawMessage(`{"timezone":"Africa/Nairobi"}`)},
			{ID: "call_2", Name: "calculate", Input: json.RawMessage(`{"expression":"19*23"}`)},
		}},
		{Role: "tool", ToolResult: &ToolResult{ID: "call_1", Content: "2026-07-11T09:00:00+03:00"}},
		{Role: "tool", ToolResult: &ToolResult{ID: "call_2", Content: "boom", IsError: true}},
	}
}

func TestAnthropicMessagesToolRoundTrip(t *testing.T) {
	t.Parallel()
	msgs := anthropicMessages(toolLoopMessages())

	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3 (user, assistant, merged tool results)", len(msgs))
	}
	if first, ok := msgs[0].Content.([]anthropicContentBlock); msgs[0].Role != "user" || !ok || len(first) != 1 || first[0].Text != "what time is it in Nairobi?" {
		t.Fatalf("plain message mangled: %+v", msgs[0])
	}

	blocks, ok := msgs[1].Content.([]anthropicContentBlock)
	if !ok || msgs[1].Role != "assistant" {
		t.Fatalf("assistant message = %+v", msgs[1])
	}
	if blocks[0].Type != "text" || blocks[0].Text != "Checking." {
		t.Fatalf("text block = %+v", blocks[0])
	}
	if blocks[1].Type != "tool_use" || blocks[1].ID != "call_1" || blocks[1].Name != "get_current_time" {
		t.Fatalf("tool_use block = %+v", blocks[1])
	}

	results, ok := msgs[2].Content.([]anthropicContentBlock)
	if !ok || msgs[2].Role != "user" {
		t.Fatalf("results message = %+v", msgs[2])
	}
	if len(results) != 2 {
		t.Fatalf("consecutive tool results not merged: %d blocks", len(results))
	}
	if results[0].Type != "tool_result" || results[0].ToolUseID != "call_1" {
		t.Fatalf("result block = %+v", results[0])
	}
	if !results[1].IsError {
		t.Fatal("error result lost is_error")
	}

	// The wire shape must serialize cleanly.
	if _, err := json.Marshal(msgs); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}

func TestAnthropicMessagesEmptyToolInput(t *testing.T) {
	t.Parallel()
	msgs := anthropicMessages([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Name: "get_current_time"}}},
	})
	blocks := msgs[0].Content.([]anthropicContentBlock)
	if string(blocks[0].Input) != "{}" {
		t.Fatalf("empty input = %q, want {}", blocks[0].Input)
	}
}

// TestAnthropicForceTool pins D-063: ForceTool wires onto the wire's
// tool_choice as a forced tool selection, and is absent when ForceTool
// is empty.
func TestAnthropicForceTool(t *testing.T) {
	t.Parallel()
	a := NewAnthropic(AnthropicConfig{APIKey: "k"})
	tools := []ToolDef{{Name: "submit_plan", Description: "d", InputSchema: json.RawMessage(`{}`)}}

	forced := a.buildRequest(CompletionRequest{Model: "m", Tools: tools, ForceTool: "submit_plan"})
	data, err := json.Marshal(forced)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"tool_choice":{"name":"submit_plan","type":"tool"}`) {
		t.Fatalf("wire = %s, want a forced tool_choice", data)
	}

	unforced := a.buildRequest(CompletionRequest{Model: "m", Tools: tools})
	data, _ = json.Marshal(unforced)
	if strings.Contains(string(data), "tool_choice") {
		t.Fatalf("wire = %s, want no tool_choice when ForceTool is empty", data)
	}
}

func TestOpenAICompatToolRoundTrip(t *testing.T) {
	t.Parallel()
	o := NewOpenAICompat(OpenAICompatConfig{BaseURL: "http://x", APIKey: "k"})
	req := o.buildRequest(CompletionRequest{
		Model:    "m",
		Messages: toolLoopMessages(),
	})

	// system absent → 4 messages carried straight through.
	if len(req.Messages) != 4 {
		t.Fatalf("got %d messages, want 4", len(req.Messages))
	}
	asst := req.Messages[1]
	if len(asst.ToolCalls) != 2 || asst.ToolCalls[0].Function.Name != "get_current_time" {
		t.Fatalf("assistant tool_calls = %+v", asst.ToolCalls)
	}
	if asst.ToolCalls[0].Function.Arguments != `{"timezone":"Africa/Nairobi"}` {
		t.Fatalf("arguments = %q", asst.ToolCalls[0].Function.Arguments)
	}

	res1 := req.Messages[2]
	if res1.Role != "tool" || res1.ToolCallID != "call_1" || res1.Content != "2026-07-11T09:00:00+03:00" {
		t.Fatalf("tool result = %+v", res1)
	}
	res2 := req.Messages[3]
	content, ok := res2.Content.(string)
	if !ok || !strings.HasPrefix(content, "ERROR: ") {
		t.Fatalf("error result unmarked: %+v", res2)
	}
}

func TestOpenAICompatEffortDial(t *testing.T) {
	t.Parallel()
	o := NewOpenAICompat(OpenAICompatConfig{BaseURL: "http://x", APIKey: "k"})

	low := o.buildRequest(CompletionRequest{Model: "m", Effort: "low"})
	if low.ReasoningEffort != "low" {
		t.Fatalf("ReasoningEffort = %q, want low", low.ReasoningEffort)
	}
	normal := o.buildRequest(CompletionRequest{Model: "m"})
	if normal.ReasoningEffort != "" {
		t.Fatalf("ReasoningEffort = %q, want empty", normal.ReasoningEffort)
	}
	data, _ := json.Marshal(normal)
	if strings.Contains(string(data), "reasoning_effort") {
		t.Fatal("normal effort must omit the field entirely")
	}
}

// TestOpenAICompatForceTool pins D-063: ForceTool wires onto the
// wire's tool_choice as a forced function call, and is absent when
// ForceTool is empty.
func TestOpenAICompatForceTool(t *testing.T) {
	t.Parallel()
	o := NewOpenAICompat(OpenAICompatConfig{BaseURL: "http://x", APIKey: "k"})
	tools := []ToolDef{{Name: "submit_plan", Description: "d", InputSchema: json.RawMessage(`{}`)}}

	forced := o.buildRequest(CompletionRequest{Model: "m", Tools: tools, ForceTool: "submit_plan"})
	data, err := json.Marshal(forced)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"tool_choice":{"function":{"name":"submit_plan"},"type":"function"}`) {
		t.Fatalf("wire = %s, want a forced tool_choice", data)
	}

	unforced := o.buildRequest(CompletionRequest{Model: "m", Tools: tools})
	data, _ = json.Marshal(unforced)
	if strings.Contains(string(data), "tool_choice") {
		t.Fatalf("wire = %s, want no tool_choice when ForceTool is empty", data)
	}
}

func TestOpenAICompatConfigReasoningEffortOverridesEffort(t *testing.T) {
	t.Parallel()
	o := NewOpenAICompat(OpenAICompatConfig{BaseURL: "http://x", APIKey: "k", ReasoningEffort: "none"})

	withLow := o.buildRequest(CompletionRequest{Model: "m", Effort: "low"})
	if withLow.ReasoningEffort != "none" {
		t.Fatalf("ReasoningEffort = %q, want %q (config overrides Effort=low)", withLow.ReasoningEffort, "none")
	}
	withoutEffort := o.buildRequest(CompletionRequest{Model: "m"})
	if withoutEffort.ReasoningEffort != "none" {
		t.Fatalf("ReasoningEffort = %q, want %q (config applies even without a request Effort)", withoutEffort.ReasoningEffort, "none")
	}
}
