package catalog

import (
	"reflect"
	"testing"
)

func TestCandidateProviders(t *testing.T) {
	cases := []struct {
		name    string
		driver  string
		baseURL string
		want    []string
	}{
		{"anthropic", "anthropic", "", []string{"anthropic"}},
		{"bedrock", "bedrock", "", []string{"bedrock", "bedrock_converse"}},
		{"openai host", "openaicompat", "https://api.openai.com/v1", []string{"openai"}},
		{"gemini host", "openaicompat", "https://generativelanguage.googleapis.com/v1beta", []string{"gemini"}},
		{"zai host", "openaicompat", "https://api.z.ai/v1", []string{"zai"}},
		{"xai host", "openaicompat", "https://api.x.ai/v1", []string{"xai"}},
		{"deepseek host", "openaicompat", "https://api.deepseek.com", []string{"deepseek"}},
		{"nebius host", "openaicompat", "https://api.tokenfactory.nebius.com/v1", []string{"nebius"}},
		{"legacy nebius studio host", "openaicompat", "https://api.studio.nebius.com/v1", []string{"nebius"}},
		{"openrouter host", "openaicompat", "https://openrouter.ai/api/v1", []string{"openrouter"}},
		{"localhost ollama", "openaicompat", "http://localhost:11434/v1", []string{"ollama"}},
		{"127.0.0.1 ollama", "openaicompat", "http://127.0.0.1:11434/v1", []string{"ollama"}},
		{"docker internal ollama", "openaicompat", "http://host.docker.internal:11434/v1", []string{"ollama"}},
		{"port 11434 non-loopback host", "openaicompat", "http://ollama-box:11434/v1", []string{"ollama"}},
		{"unrecognized host: no restriction", "openaicompat", "https://example.com/v1", nil},
		{"jev driver", "jev", "", []string{"typesafe", "jev"}},
		{"unknown driver", "weird", "", nil},
		{"legacy openai-responses driver", "openai-responses", "https://api.openai.com/v1", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CandidateProviders(tc.driver, tc.baseURL)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("CandidateProviders(%q, %q) = %v, want %v", tc.driver, tc.baseURL, got, tc.want)
			}
		})
	}
}

func TestMatch(t *testing.T) {
	pool := []Model{
		{ModelKey: "claude-3-5-sonnet"},
		{ModelKey: "anthropic/claude-3-opus"},
		{ModelKey: "gemini/gemini-1.5-pro"},
		{ModelKey: "nebius/deepseek-ai/DeepSeek-V4-Flash", LitellmProvider: "nebius"},
		{ModelKey: "nebius/nvidia/nemotron-3-super-120b-a12b", LitellmProvider: "nebius"},
		{ModelKey: "other/deepseek-ai/DeepSeek-V4-Pro", LitellmProvider: "nebius"},
	}
	cases := []struct {
		name    string
		modelID string
		want    string
	}{
		{"exact full key match", "claude-3-5-sonnet", "claude-3-5-sonnet"},
		{"exact match wins over segment match", "gemini/gemini-1.5-pro", "gemini/gemini-1.5-pro"},
		{"segment match after last slash", "claude-3-opus", "anthropic/claude-3-opus"},
		{"segment match for gemini", "gemini-1.5-pro", "gemini/gemini-1.5-pro"},
		{"own provider prefix stripped", "deepseek-ai/DeepSeek-V4-Flash", "nebius/deepseek-ai/DeepSeek-V4-Flash"},
		{"nebius nvidia nemotron prefix stripped", "nvidia/nemotron-3-super-120b-a12b", "nebius/nvidia/nemotron-3-super-120b-a12b"},
		{"foreign prefix not stripped", "deepseek-ai/DeepSeek-V4-Pro", ""},
		{"no match", "gpt-4o", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.modelID, pool); got != tc.want {
				t.Fatalf("Match(%q) = %q, want %q", tc.modelID, got, tc.want)
			}
		})
	}
}

func TestStripOwnPrefix(t *testing.T) {
	cases := []struct {
		name            string
		modelKey        string
		litellmProvider string
		want            string
	}{
		{"bare key passes through", "gpt-4o", "openai", "gpt-4o"},
		{"single prefix stripped", "zai/glm-4.5", "zai", "glm-4.5"},
		{"non-matching prefix passes through", "anthropic/claude-3-opus", "bedrock", "anthropic/claude-3-opus"},
		{"multi-segment remainder keeps its own path", "fireworks_ai/accounts/fireworks/models/x", "fireworks_ai", "accounts/fireworks/models/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripOwnPrefix(tc.modelKey, tc.litellmProvider); got != tc.want {
				t.Fatalf("StripOwnPrefix(%q, %q) = %q, want %q", tc.modelKey, tc.litellmProvider, got, tc.want)
			}
		})
	}
}

func TestSuggestPreservesUnmatchedOrder(t *testing.T) {
	// Suggest's DB-backed candidate lookup is exercised via the admin
	// integration tests; this only checks Match's pure ordering
	// contract holds across a mixed matched/unmatched slice, driven
	// directly against a fixed pool (no Store needed).
	pool := []Model{{ModelKey: "openai/gpt-4o"}}
	ids := []string{"gpt-4o", "unknown-model"}
	var got []string
	for _, id := range ids {
		got = append(got, Match(id, pool))
	}
	want := []string{"openai/gpt-4o", ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCandidateProvidersForRow(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		driver  string
		baseURL string
		opts    map[string]string
		hints   []string
		want    []string
	}{
		{
			name:   "explicit litellm_provider option",
			kind:   "api",
			driver: "openaicompat",
			opts:   map[string]string{"litellm_provider": "anthropic"},
			want:   []string{"anthropic"},
		},
		{
			name:   "explicit bedrock option expands to pair",
			kind:   "api",
			driver: "bedrock",
			opts:   map[string]string{"litellm_provider": "bedrock"},
			want:   []string{"bedrock", "bedrock_converse"},
		},
		{
			name:   "claude-cli cli row",
			kind:   "cli",
			driver: "claude-cli",
			want:   []string{"anthropic"},
		},
		{
			name:    "cliproxy with claude name hint",
			kind:    "api",
			driver:  "openaicompat",
			baseURL: "http://cliproxy:8317/v1",
			hints:   []string{"Claude Subscription", "claude-3-7-sonnet-20250219"},
			want:    []string{"anthropic"},
		},
		{
			name:    "cliproxy with openai name hint",
			kind:    "api",
			driver:  "openaicompat",
			baseURL: "http://cliproxy:8317/v1",
			hints:   []string{"OpenAI Subscription", "gpt-4o"},
			want:    []string{"openai"},
		},
		{
			name:    "cliproxy with chatgpt hint",
			kind:    "api",
			driver:  "openaicompat",
			baseURL: "http://cliproxy:8317/v1",
			hints:   []string{"ChatGPT Subscription"},
			want:    []string{"openai"},
		},
		{
			name:    "cliproxy port 8317 on localhost",
			kind:    "api",
			driver:  "openaicompat",
			baseURL: "http://127.0.0.1:8317/v1",
			hints:   []string{"claude"},
			want:    []string{"anthropic"},
		},
		{
			name:    "cliproxy with no hints",
			kind:    "api",
			driver:  "openaicompat",
			baseURL: "http://cliproxy:8317/v1",
			want:    []string{"anthropic", "openai"},
		},
		{
			name:    "cliproxy o-series model is openai",
			kind:    "api",
			driver:  "openaicompat",
			baseURL: "http://cliproxy:8317/v1",
			hints:   []string{"Work", "o4-mini"},
			want:    []string{"openai"},
		},
		{
			name:    "cliproxy conflicting hints keep both sections",
			kind:    "api",
			driver:  "openaicompat",
			baseURL: "http://cliproxy:8317/v1",
			hints:   []string{"Claude Subscription", "gpt-4o"},
			want:    []string{"anthropic", "openai"},
		},
		{
			name:    "cliproxy ignores gpt buried in an unrelated token",
			kind:    "api",
			driver:  "openaicompat",
			baseURL: "http://127.0.0.1:8317/v1",
			hints:   []string{"fingpt-local"},
			want:    []string{"anthropic", "openai"},
		},
		{
			name:    "cliproxy sonnet hint is anthropic",
			kind:    "api",
			driver:  "openaicompat",
			baseURL: "http://cliproxy:8317/v1",
			hints:   []string{"sonnet"},
			want:    []string{"anthropic"},
		},
		{
			name:    "unrecognized host with no hints or opts",
			kind:    "api",
			driver:  "openaicompat",
			baseURL: "https://example.com/v1",
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CandidateProvidersForRow(tc.kind, tc.driver, tc.baseURL, tc.opts, tc.hints...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("CandidateProvidersForRow() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCliproxyServes(t *testing.T) {
	const proxy = "http://cliproxy:8317/v1"
	claudeRow := []string{"Claude Subscription", "claude-sonnet-4-5"}
	chatgptRow := []string{"ChatGPT Subscription", "gpt-5"}
	ambiguous := []string{"Subscriptions", ""}

	cases := []struct {
		name    string
		baseURL string
		hints   []string
		id      string
		want    bool
	}{
		{"claude row keeps claude", proxy, claudeRow, "claude-opus-4-1", true},
		{"claude row drops gpt", proxy, claudeRow, "gpt-5", false},
		{"claude row drops o3", proxy, claudeRow, "o3", false},
		{"claude row drops codex", proxy, claudeRow, "codex-auto-review", false},
		{"claude row drops gpt-image", proxy, claudeRow, "gpt-image-1", false},
		{"chatgpt row keeps gpt", proxy, chatgptRow, "gpt-5", true},
		{"chatgpt row keeps o3", proxy, chatgptRow, "o3", true},
		{"chatgpt row keeps codex", proxy, chatgptRow, "codex-auto-review", true},
		{"chatgpt row keeps gpt-image", proxy, chatgptRow, "gpt-image-1", true},
		{"chatgpt row drops claude", proxy, chatgptRow, "claude-sonnet-4-5", false},
		{"chatgpt row drops unsignalled id", proxy, chatgptRow, "gemini-2.5-pro", false},
		{"port 8317 counts as cliproxy", "http://127.0.0.1:8317/v1", chatgptRow, "claude-sonnet-4-5", false},
		{"ambiguous row keeps claude", proxy, ambiguous, "claude-sonnet-4-5", true},
		{"ambiguous row keeps gpt", proxy, ambiguous, "gpt-5", true},
		{"conflicting hints keep everything", proxy, []string{"Claude Subscription", "gpt-5"}, "gpt-5", true},
		{"non-cliproxy row untouched", "https://api.openai.com/v1", chatgptRow, "claude-sonnet-4-5", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CliproxyServes(tc.baseURL, tc.hints, tc.id); got != tc.want {
				t.Fatalf("CliproxyServes(%q, %v, %q) = %v, want %v", tc.baseURL, tc.hints, tc.id, got, tc.want)
			}
		})
	}
}
