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
	if _, ok := r.Get("nope"); ok {
		t.Fatal("unknown name resolved")
	}
	if got := len(r.Names()); got != 2 {
		t.Fatalf("Names() = %d, want 2", got)
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
