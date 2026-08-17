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
		{"localhost ollama", "openaicompat", "http://localhost:11434/v1", []string{"ollama"}},
		{"127.0.0.1 ollama", "openaicompat", "http://127.0.0.1:11434/v1", []string{"ollama"}},
		{"docker internal ollama", "openaicompat", "http://host.docker.internal:11434/v1", []string{"ollama"}},
		{"port 11434 non-loopback host", "openaicompat", "http://ollama-box:11434/v1", []string{"ollama"}},
		{"unrecognized host: no restriction", "openaicompat", "https://example.com/v1", nil},
		{"unknown driver", "weird", "", nil},
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
