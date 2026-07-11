package provider

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func TestConverseMessages(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "calling", ToolCalls: []ToolCall{
			{ID: "t1", Name: "calc", Input: json.RawMessage(`{"a":1}`)},
		}},
		{Role: "tool", ToolResult: &ToolResult{ID: "t1", Content: "2", IsError: true}},
		{Role: "assistant"}, // no text, no tool calls: must be dropped
		{Role: "tool"},      // no result payload: must be dropped
	}

	out := converseMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	if out[0].Role != types.ConversationRoleUser {
		t.Fatalf("msg 0 role = %v", out[0].Role)
	}
	if out[1].Role != types.ConversationRoleAssistant || len(out[1].Content) != 2 {
		t.Fatalf("msg 1: role %v, %d blocks", out[1].Role, len(out[1].Content))
	}
	tu, ok := out[1].Content[1].(*types.ContentBlockMemberToolUse)
	if !ok || *tu.Value.ToolUseId != "t1" || *tu.Value.Name != "calc" {
		t.Fatalf("msg 1 tool use block = %#v", out[1].Content[1])
	}
	// Tool results ride as user-role toolResult blocks.
	if out[2].Role != types.ConversationRoleUser {
		t.Fatalf("msg 2 role = %v", out[2].Role)
	}
	tr, ok := out[2].Content[0].(*types.ContentBlockMemberToolResult)
	if !ok || *tr.Value.ToolUseId != "t1" || tr.Value.Status != types.ToolResultStatusError {
		t.Fatalf("msg 2 tool result block = %#v", out[2].Content[0])
	}
}

func TestConverseSystem(t *testing.T) {
	t.Parallel()
	if converseSystem("", "us.amazon.nova-pro-v1:0") != nil {
		t.Fatal("empty system must yield nil")
	}

	// Nova: system text followed by a cache point.
	blocks := converseSystem("persona", "us.amazon.nova-pro-v1:0")
	if len(blocks) != 2 {
		t.Fatalf("nova blocks = %d, want 2", len(blocks))
	}
	txt, ok := blocks[0].(*types.SystemContentBlockMemberText)
	if !ok || txt.Value != "persona" {
		t.Fatalf("block 0 = %#v", blocks[0])
	}
	if _, ok := blocks[1].(*types.SystemContentBlockMemberCachePoint); !ok {
		t.Fatalf("block 1 = %#v", blocks[1])
	}

	// Non-Nova models must not get a cache point — Titan rejects it.
	blocks = converseSystem("persona", "amazon.titan-text-premier-v1:0")
	if len(blocks) != 1 {
		t.Fatalf("titan blocks = %d, want 1", len(blocks))
	}
}

func TestConverseTools(t *testing.T) {
	t.Parallel()
	if converseTools(nil) != nil {
		t.Fatal("no tools must yield nil config")
	}
	cfg := converseTools([]ToolDef{
		{Name: "calc", Description: "adds", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	if cfg == nil || len(cfg.Tools) != 1 {
		t.Fatalf("config = %#v", cfg)
	}
	spec, ok := cfg.Tools[0].(*types.ToolMemberToolSpec)
	if !ok || *spec.Value.Name != "calc" || spec.Value.InputSchema == nil {
		t.Fatalf("tool spec = %#v", cfg.Tools[0])
	}
}

func TestDocumentFromJSON(t *testing.T) {
	t.Parallel()
	if documentFromJSON(nil) != nil {
		t.Fatal("empty input must yield nil")
	}
	if documentFromJSON(json.RawMessage(`{broken`)) != nil {
		t.Fatal("invalid JSON must yield nil")
	}
	if documentFromJSON(json.RawMessage(`{"a":1}`)) == nil {
		t.Fatal("valid JSON must yield a document")
	}
}

func TestParseTitanEmbedding(t *testing.T) {
	t.Parallel()
	emb, tokens, err := parseTitanEmbedding([]byte(`{"embedding":[0.1,-0.2,0.3],"inputTextTokenCount":7}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(emb) != 3 || emb[1] != -0.2 || tokens != 7 {
		t.Fatalf("emb = %v, tokens = %d", emb, tokens)
	}

	if _, _, err := parseTitanEmbedding([]byte(`{"inputTextTokenCount":7}`)); err == nil {
		t.Fatal("missing embedding must error")
	}
	if _, _, err := parseTitanEmbedding([]byte(`not json`)); err == nil {
		t.Fatal("garbage must error")
	}
}
