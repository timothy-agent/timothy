package provider

import (
	"context"
	"encoding/json"
	"strings"
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

// TestConverseMessagesMergesParallelToolResults reproduces a live
// crash: two tool calls in one assistant turn produced two separate
// user turns (one toolResult block each), which Bedrock's Converse API
// rejects with "Expected toolResult blocks ... for the following Ids"
// — it requires every result for one turn's parallel tool calls in a
// SINGLE following user turn.
func TestConverseMessagesMergesParallelToolResults(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "t1", Name: "calc", Input: json.RawMessage(`{}`)},
			{ID: "t2", Name: "calc", Input: json.RawMessage(`{}`)},
		}},
		{Role: "tool", ToolResult: &ToolResult{ID: "t1", Content: "4"}},
		{Role: "tool", ToolResult: &ToolResult{ID: "t2", Content: "9"}},
	}

	out := converseMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (one assistant turn, one merged user turn)", len(out))
	}
	if out[1].Role != types.ConversationRoleUser {
		t.Fatalf("msg 1 role = %v", out[1].Role)
	}
	if len(out[1].Content) != 2 {
		t.Fatalf("merged user turn has %d blocks, want 2", len(out[1].Content))
	}
	first, ok := out[1].Content[0].(*types.ContentBlockMemberToolResult)
	if !ok || *first.Value.ToolUseId != "t1" {
		t.Fatalf("block 0 = %#v", out[1].Content[0])
	}
	second, ok := out[1].Content[1].(*types.ContentBlockMemberToolResult)
	if !ok || *second.Value.ToolUseId != "t2" {
		t.Fatalf("block 1 = %#v", out[1].Content[1])
	}
}

// TestConverseMessagesDoesNotMergeIntoRealUserText proves the merge
// only fires between consecutive tool-result turns — a plain user
// message must never absorb a tool result or vice versa.
func TestConverseMessagesDoesNotMergeIntoRealUserText(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "calc", Input: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolResult: &ToolResult{ID: "t1", Content: "4"}},
		{Role: "user", Content: "thanks, one more question"},
	}

	out := converseMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3 (assistant, tool result, plain user text kept separate)", len(out))
	}
	if len(out[1].Content) != 1 {
		t.Fatalf("tool result turn has %d blocks, want 1", len(out[1].Content))
	}
	txt, ok := out[2].Content[0].(*types.ContentBlockMemberText)
	if !ok || txt.Value != "thanks, one more question" {
		t.Fatalf("msg 2 = %#v", out[2].Content[0])
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

// TestSanitizeNovaSchema covers the AWS-documented Nova constraint
// (D-037): only "type"/"properties"/"required" survive at the schema's
// top level, while nested keys inside a property's own subschema
// (including additionalProperties there) are left untouched.
func TestSanitizeNovaSchema(t *testing.T) {
	t.Parallel()
	in := json.RawMessage(`{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"title": "calc input",
		"description": "arguments for calc",
		"additionalProperties": false,
		"type": "object",
		"required": ["a"],
		"properties": {
			"a": {
				"type": "object",
				"additionalProperties": false,
				"properties": {"b": {"type": "string"}}
			}
		}
	}`)

	out := sanitizeNovaSchema(in)

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	for _, forbidden := range []string{"$schema", "title", "description", "additionalProperties"} {
		if _, ok := got[forbidden]; ok {
			t.Fatalf("top-level key %q must be stripped, got %#v", forbidden, got)
		}
	}
	if got["type"] != "object" {
		t.Fatalf("type = %#v, want object", got["type"])
	}
	required, ok := got["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "a" {
		t.Fatalf("required = %#v", got["required"])
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", got["properties"])
	}
	a, ok := props["a"].(map[string]any)
	if !ok {
		t.Fatalf("properties.a = %#v", props["a"])
	}
	// Nested additionalProperties (inside a property subschema) must survive.
	if v, ok := a["additionalProperties"]; !ok || v != false {
		t.Fatalf("nested additionalProperties stripped or wrong: %#v", a["additionalProperties"])
	}
}

// TestSanitizeNovaSchemaEmptyOrInvalid proves the sanitize step never
// panics and leaves empty/unparseable input alone so documentFromJSON's
// existing nil-on-empty, nil-on-invalid behavior is unaffected.
func TestSanitizeNovaSchemaEmptyOrInvalid(t *testing.T) {
	t.Parallel()
	if out := sanitizeNovaSchema(nil); out != nil {
		t.Fatalf("nil input must stay nil, got %#v", out)
	}
	if out := sanitizeNovaSchema(json.RawMessage{}); len(out) != 0 {
		t.Fatalf("empty input must stay empty, got %#v", out)
	}
	broken := json.RawMessage(`{broken`)
	if out := sanitizeNovaSchema(broken); string(out) != string(broken) {
		t.Fatalf("invalid JSON must pass through unchanged, got %#v", out)
	}
	if documentFromJSON(sanitizeNovaSchema(nil)) != nil {
		t.Fatal("nil schema through the full pipeline must still yield a nil document")
	}
}

// TestBuildConverseStreamInput covers the D-037 Nova greedy-decoding
// workaround: only requests that are both Nova and tool-bearing get
// temperature 0 + topK 1, and MaxTokens mapping is unaffected.
func TestBuildConverseStreamInput(t *testing.T) {
	t.Parallel()

	novaTools := CompletionRequest{
		Model:     "us.amazon.nova-pro-v1:0",
		Tools:     []ToolDef{{Name: "calc", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		MaxTokens: 512,
	}
	input := buildConverseStreamInput(novaTools)
	if input.InferenceConfig == nil || input.InferenceConfig.Temperature == nil || *input.InferenceConfig.Temperature != 0 {
		t.Fatalf("nova+tools: InferenceConfig = %#v, want Temperature 0", input.InferenceConfig)
	}
	if input.InferenceConfig.MaxTokens == nil || *input.InferenceConfig.MaxTokens != 512 {
		t.Fatalf("nova+tools: MaxTokens = %#v, want 512", input.InferenceConfig.MaxTokens)
	}
	if input.AdditionalModelRequestFields == nil {
		t.Fatal("nova+tools: AdditionalModelRequestFields must be set")
	}

	novaNoTools := CompletionRequest{Model: "us.amazon.nova-pro-v1:0", MaxTokens: 512}
	input = buildConverseStreamInput(novaNoTools)
	if input.InferenceConfig == nil || input.InferenceConfig.Temperature != nil {
		t.Fatalf("nova without tools: Temperature must stay unset, got %#v", input.InferenceConfig)
	}
	if input.AdditionalModelRequestFields != nil {
		t.Fatal("nova without tools: AdditionalModelRequestFields must stay nil")
	}
	if input.InferenceConfig.MaxTokens == nil || *input.InferenceConfig.MaxTokens != 512 {
		t.Fatalf("nova without tools: MaxTokens = %#v, want 512", input.InferenceConfig.MaxTokens)
	}

	titanTools := CompletionRequest{
		Model:     "amazon.titan-text-premier-v1:0",
		Tools:     []ToolDef{{Name: "calc", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		MaxTokens: 256,
	}
	input = buildConverseStreamInput(titanTools)
	if input.InferenceConfig == nil || input.InferenceConfig.Temperature != nil {
		t.Fatalf("titan+tools: Temperature must stay unset, got %#v", input.InferenceConfig)
	}
	if input.AdditionalModelRequestFields != nil {
		t.Fatal("titan+tools: AdditionalModelRequestFields must stay nil")
	}
	if input.InferenceConfig.MaxTokens == nil || *input.InferenceConfig.MaxTokens != 256 {
		t.Fatalf("titan+tools: MaxTokens = %#v, want 256", input.InferenceConfig.MaxTokens)
	}
}

// TestBuildConverseStreamInputForceTool pins D-063: Converse's
// ToolChoice is set only for model families that support forced tool
// choice (Anthropic, Mistral Large); Nova ignores ForceTool entirely
// (graceful degrade) since it rejects the field.
func TestBuildConverseStreamInputForceTool(t *testing.T) {
	t.Parallel()
	tools := []ToolDef{{Name: "submit_plan", InputSchema: json.RawMessage(`{"type":"object"}`)}}

	cases := []struct {
		name    string
		model   string
		wantSet bool
	}{
		{"nova ignores ForceTool", "us.amazon.nova-pro-v1:0", false},
		{"anthropic honors ForceTool", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", true},
		{"mistral large honors ForceTool", "mistral.mistral-large-2407-v1:0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := buildConverseStreamInput(CompletionRequest{
				Model: tc.model, Tools: tools, ForceTool: "submit_plan",
			})
			if !tc.wantSet {
				if input.ToolConfig.ToolChoice != nil {
					t.Fatalf("ToolChoice = %#v, want nil", input.ToolConfig.ToolChoice)
				}
				return
			}
			choice, ok := input.ToolConfig.ToolChoice.(*types.ToolChoiceMemberTool)
			if !ok {
				t.Fatalf("ToolChoice = %#v, want *types.ToolChoiceMemberTool", input.ToolConfig.ToolChoice)
			}
			if choice.Value.Name == nil || *choice.Value.Name != "submit_plan" {
				t.Fatalf("ToolChoice name = %#v, want submit_plan", choice.Value.Name)
			}
		})
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

// TestParseStaticCredentials covers the secret-store JSON contract
// (D-047): valid input, optional fields, missing required fields, and
// malformed JSON.
func TestParseStaticCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		want    *StaticCredentials
	}{
		{
			name: "valid minimal",
			raw:  `{"access_key_id":"AKIA123","secret_access_key":"secret"}`,
			want: &StaticCredentials{AccessKeyID: "AKIA123", SecretAccessKey: "secret"},
		},
		{
			name: "valid with optional fields",
			raw:  `{"access_key_id":"AKIA123","secret_access_key":"secret","session_token":"tok","region":"us-west-2"}`,
			want: &StaticCredentials{AccessKeyID: "AKIA123", SecretAccessKey: "secret", SessionToken: "tok", Region: "us-west-2"},
		},
		{
			name: "unknown keys ignored",
			raw:  `{"access_key_id":"AKIA123","secret_access_key":"secret","bogus":"x"}`,
			want: &StaticCredentials{AccessKeyID: "AKIA123", SecretAccessKey: "secret"},
		},
		{
			name:    "missing access_key_id",
			raw:     `{"secret_access_key":"secret"}`,
			wantErr: true,
		},
		{
			name:    "missing secret_access_key",
			raw:     `{"access_key_id":"AKIA123"}`,
			wantErr: true,
		},
		{
			name:    "empty object",
			raw:     `{}`,
			wantErr: true,
		},
		{
			name:    "malformed json",
			raw:     `{not json`,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseStaticCredentials(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if *got != *tc.want {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestBedrockLazyClientStaticCredentials covers D-047/D-048's region
// precedence and construction-failure rules: secret JSON region wins
// over the provider's options.region-derived region, the provider's
// region is used when the secret carries none, and a static-credentials
// config with no region anywhere fails construction with a message
// naming the region field. LoadDefaultConfig with a static credentials
// provider and an explicit region does no network I/O, so this stays
// a pure unit test.
func TestBedrockLazyClientStaticCredentials(t *testing.T) {
	t.Parallel()

	t.Run("secret region wins over provider region", func(t *testing.T) {
		t.Parallel()
		b := NewBedrock(BedrockConfig{
			Name:              "b",
			Region:            "us-east-1",
			StaticCredentials: &StaticCredentials{AccessKeyID: "AKIA", SecretAccessKey: "secret", Region: "eu-west-1"},
		})
		client, err := b.lazyClient(context.Background())
		if err != nil {
			t.Fatalf("lazyClient: %v", err)
		}
		if got := client.Options().Region; got != "eu-west-1" {
			t.Fatalf("region = %q, want eu-west-1 (secret JSON must win)", got)
		}
	})

	t.Run("falls back to provider region when secret has none", func(t *testing.T) {
		t.Parallel()
		b := NewBedrock(BedrockConfig{
			Name:              "b",
			Region:            "us-west-2",
			StaticCredentials: &StaticCredentials{AccessKeyID: "AKIA", SecretAccessKey: "secret"},
		})
		client, err := b.lazyClient(context.Background())
		if err != nil {
			t.Fatalf("lazyClient: %v", err)
		}
		if got := client.Options().Region; got != "us-west-2" {
			t.Fatalf("region = %q, want us-west-2", got)
		}
	})

	t.Run("no region anywhere fails construction", func(t *testing.T) {
		t.Parallel()
		// Bypass NewBedrock's us-east-1 default to exercise the
		// no-region-derivable path directly.
		b := &Bedrock{cfg: BedrockConfig{
			Name:              "b",
			StaticCredentials: &StaticCredentials{AccessKeyID: "AKIA", SecretAccessKey: "secret"},
		}}
		_, err := b.lazyClient(context.Background())
		if err == nil {
			t.Fatal("want error when no region is derivable")
		}
		if !strings.Contains(err.Error(), "region") {
			t.Fatalf("error must name the region field, got: %v", err)
		}
	})

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
