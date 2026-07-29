package provider

import (
	"strings"
	"testing"
)

func TestBuildRegistry(t *testing.T) {
	t.Parallel()
	lookups := map[string]string{"ANTHROPIC_API_KEY": "sk-a", "XAI_API_KEY": "sk-x"}
	r, err := Build([]Config{
		// CredentialRef values are env var *names*, not secrets.
		{Name: "anthropic", Kind: KindAPI, Driver: "anthropic", CredentialRef: "ANTHROPIC_API_KEY"},                             // #nosec G101
		{Name: "xai-grok", Kind: KindAPI, Driver: "openaicompat", BaseURL: "https://api.x.ai/v1", CredentialRef: "XAI_API_KEY"}, // #nosec G101
		{Name: "bedrock", Kind: KindAPI, Driver: "bedrock", BaseURL: "us-east-1", CredentialRef: "sumonmselim"},
	}, func(ref string) string { return lookups[ref] })
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	a, ok := r.Get("anthropic")
	if !ok || a.Kind() != KindAPI || a.Name() != "anthropic" {
		t.Fatalf("anthropic provider = %v, %v", a, ok)
	}
	if _, ok := r.Get("xai-grok"); !ok {
		t.Fatal("xai-grok missing")
	}
	if _, ok := r.Get("bedrock"); !ok {
		t.Fatal("bedrock missing")
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("unknown name resolved")
	}
	if got := len(r.Names()); got != 3 {
		t.Fatalf("Names() = %d, want 3", got)
	}
}

func TestBuildPassesReasoningEffortToOpenAICompat(t *testing.T) {
	t.Parallel()
	r, err := Build([]Config{
		{Name: "ollama", Kind: KindAPI, Driver: "openaicompat", BaseURL: "http://ollama.local/v1", ReasoningEffort: "none"},
		{Name: "grok", Kind: KindAPI, Driver: "openaicompat", BaseURL: "https://api.x.ai/v1"},
	}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	p, ok := r.Get("ollama")
	if !ok {
		t.Fatal("ollama missing")
	}
	oai, ok := p.(*OpenAICompat)
	if !ok {
		t.Fatalf("ollama provider type = %T, want *OpenAICompat", p)
	}
	if oai.cfg.ReasoningEffort != "none" {
		t.Fatalf("ReasoningEffort = %q, want %q", oai.cfg.ReasoningEffort, "none")
	}

	p2, _ := r.Get("grok")
	grokOai := p2.(*OpenAICompat)
	if grokOai.cfg.ReasoningEffort != "" {
		t.Fatalf("grok ReasoningEffort = %q, want empty when unset", grokOai.cfg.ReasoningEffort)
	}
}

func TestBuildErrors(t *testing.T) {
	t.Parallel()
	noLookup := func(string) string { return "" }
	tests := []struct {
		name    string
		cfgs    []Config
		wantErr string
	}{
		{
			name:    "unknown driver",
			cfgs:    []Config{{Name: "p", Driver: "grpc"}},
			wantErr: "unknown driver",
		},
		{
			name: "duplicate name",
			cfgs: []Config{
				{Name: "p", Driver: "anthropic"},
				{Name: "p", Driver: "anthropic"},
			},
			wantErr: "duplicate provider",
		},
		{
			name:    "openaicompat without base url",
			cfgs:    []Config{{Name: "p", Driver: "openaicompat"}},
			wantErr: "requires base_url",
		},
		{
			name:    "empty name",
			cfgs:    []Config{{Driver: "anthropic"}},
			wantErr: "empty name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Build(tt.cfgs, noLookup)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Build() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewBedrock(t *testing.T) {
	t.Parallel()
	p := NewBedrock(BedrockConfig{Name: "bedrock-test", Region: "us-west-2", Profile: "sumonmselim"})
	if p.Name() != "bedrock-test" {
		t.Fatalf("name = %q", p.Name())
	}
	if p.Kind() != KindAPI {
		t.Fatalf("kind = %v", p.Kind())
	}
	caps := p.Capabilities()
	if len(caps) != 4 || caps[0] != CapChat || caps[2] != CapTools {
		t.Fatalf("caps = %v", caps)
	}
}
