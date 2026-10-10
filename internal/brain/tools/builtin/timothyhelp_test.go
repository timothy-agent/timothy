package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeHelpSearch struct {
	hits     []HelpHit
	err      error
	sawQuery string
	sawK     int
}

func (f *fakeHelpSearch) search(_ context.Context, query string, k int) ([]HelpHit, error) {
	f.sawQuery, f.sawK = query, k
	return f.hits, f.err
}

func fullLive(search func(context.Context, string, int) ([]HelpHit, error)) TimothyHelpConfig {
	return TimothyHelpConfig{
		Search:  search,
		Version: "0.1.0-alpha.106",
		Features: func(context.Context) map[string]bool {
			return map[string]bool{"channels_enabled": true, "automations_enabled": true, "kb_image_captioning_enabled": false}
		},
		Connectors: func(context.Context) ([]HelpAccount, error) {
			return []HelpAccount{{Kind: "google", Enabled: true}, {Kind: "github", Enabled: true}, {Kind: "google", Enabled: false}}, nil
		},
		Channels: func(context.Context) ([]HelpAccount, error) {
			return []HelpAccount{{Kind: "telegram", Enabled: true}}, nil
		},
		Agents:    func(context.Context) ([]string, error) { return []string{"general", "coder"}, nil },
		Workflows: true,
		Sandbox:   func(context.Context) error { return nil },
	}
}

func TestTimothyHelpExecute(t *testing.T) {
	t.Parallel()
	bigExcerpt := strings.Repeat("word ", 2000)
	many := make([]HelpHit, 10)
	for i := range many {
		many[i] = HelpHit{Title: "Page", Excerpt: bigExcerpt, AppPath: "/settings/features", AppLabel: "Features"}
	}
	down := errors.New("down")

	tests := []struct {
		name    string
		cfg     func(*fakeHelpSearch) TimothyHelpConfig
		hits    []HelpHit
		args    string
		want    []string
		notWant []string
		wantK   int
		maxLen  int
	}{
		{
			name:  "no hits still carries the live block",
			cfg:   func(f *fakeHelpSearch) TimothyHelpConfig { return fullLive(f.search) },
			args:  `{"query":"what can you do"}`,
			wantK: timothyHelpDefaultK,
			want: []string{
				"- Version: 0.1.0-alpha.106",
				"- Feature switches on: automations_enabled, channels_enabled",
				"- Feature switches off: kb_image_captioning_enabled",
				"- Connectors: github (1 account), google (2 accounts, 1 disabled)",
				"- Channels: telegram (1 account)",
				"- Agents: general, coder",
				"- Workflows: enabled",
				"- Sandbox: available",
				"No matching passages in Timothy's docs.",
			},
		},
		{
			name: "hit with app_path renders a relative link and docs url",
			cfg:  func(f *fakeHelpSearch) TimothyHelpConfig { return fullLive(f.search) },
			hits: []HelpHit{{
				Title: "Connectors", Excerpt: "Add a Gmail account under Connectors.", DocsURL: "https://timothy-agent.github.io/docs/connectors/",
				AppPath: "/settings/connectors", AppLabel: "Connectors",
			}},
			args:  `{"query":"how do I add a Gmail account","k":3}`,
			wantK: 3,
			want: []string{
				"Docs passages:\n1. Connectors\n",
				"App screen: [Connectors](/settings/connectors)",
				"Docs: https://timothy-agent.github.io/docs/connectors/",
				"Add a Gmail account under Connectors.",
			},
		},
		{
			name:    "hit without app_path has no app screen line",
			cfg:     func(f *fakeHelpSearch) TimothyHelpConfig { return fullLive(f.search) },
			hits:    []HelpHit{{Title: "Getting started", Excerpt: "Run make up.", DocsURL: "https://timothy-agent.github.io/docs/start/"}},
			args:    `{"query":"install"}`,
			wantK:   timothyHelpDefaultK,
			want:    []string{"1. Getting started", "Docs: https://timothy-agent.github.io/docs/start/", "Run make up."},
			notWant: []string{"App screen:"},
		},
		{
			name:    "app_label falls back to the title",
			cfg:     func(f *fakeHelpSearch) TimothyHelpConfig { return fullLive(f.search) },
			hits:    []HelpHit{{Title: "Tools", Excerpt: "x", AppPath: "/settings/tools"}},
			args:    `{"query":"tools"}`,
			wantK:   timothyHelpDefaultK,
			want:    []string{"App screen: [Tools](/settings/tools)"},
			notWant: []string{"Docs:"},
		},
		{
			name:   "caps each excerpt and the whole result",
			cfg:    func(f *fakeHelpSearch) TimothyHelpConfig { return fullLive(f.search) },
			hits:   many,
			args:   `{"query":"features","k":10}`,
			wantK:  10,
			want:   []string{"- Version: 0.1.0-alpha.106", "1. Page", " ..."},
			maxLen: timothyHelpMaxBytes + len(" ..."),
		},
		{
			name: "nil live deps render unknown",
			cfg: func(f *fakeHelpSearch) TimothyHelpConfig {
				return TimothyHelpConfig{Search: f.search}
			},
			args:  `{"query":"what is configured"}`,
			wantK: timothyHelpDefaultK,
			want: []string{
				"- Version: unknown (local build)", "- Feature switches: unknown", "- Connectors: unknown",
				"- Channels: unknown", "- Agents: unknown", "- Workflows: disabled", "- Sandbox: unknown",
			},
		},
		{
			name: "empty database renders none",
			cfg: func(f *fakeHelpSearch) TimothyHelpConfig {
				return TimothyHelpConfig{
					Search:     f.search,
					Version:    "v1",
					Features:   func(context.Context) map[string]bool { return map[string]bool{} },
					Connectors: func(context.Context) ([]HelpAccount, error) { return nil, nil },
					Channels:   func(context.Context) ([]HelpAccount, error) { return []HelpAccount{}, nil },
					Agents:     func(context.Context) ([]string, error) { return nil, nil },
					Sandbox:    func(context.Context) error { return down },
				}
			},
			args:  `{"query":"what is configured"}`,
			wantK: timothyHelpDefaultK,
			want: []string{
				"- Feature switches on: none", "- Feature switches off: none", "- Connectors: none configured",
				"- Channels: none configured", "- Agents: none", "- Sandbox: unavailable",
			},
		},
		{
			name: "failing live reads render unknown, never fail the call",
			cfg: func(f *fakeHelpSearch) TimothyHelpConfig {
				return TimothyHelpConfig{
					Search:     f.search,
					Connectors: func(context.Context) ([]HelpAccount, error) { return nil, down },
					Channels:   func(context.Context) ([]HelpAccount, error) { return nil, down },
					Agents:     func(context.Context) ([]string, error) { return nil, down },
				}
			},
			args:  `{"query":"q"}`,
			wantK: timothyHelpDefaultK,
			want:  []string{"- Connectors: unknown", "- Channels: unknown", "- Agents: unknown"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fk := &fakeHelpSearch{hits: tc.hits}
			out, err := TimothyHelp(tc.cfg(fk)).Execute(context.Background(), json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q:\n%s", w, out)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(out, w) {
					t.Errorf("output has %q:\n%s", w, out)
				}
			}
			if fk.sawQuery == "" || !strings.Contains(tc.args, `"`+fk.sawQuery+`"`) {
				t.Errorf("query %q not passed through from %s", fk.sawQuery, tc.args)
			}
			if fk.sawK != tc.wantK {
				t.Errorf("k = %d, want %d", fk.sawK, tc.wantK)
			}
			if tc.maxLen > 0 && len(out) > tc.maxLen {
				t.Errorf("len(out) = %d, want <= %d", len(out), tc.maxLen)
			}
		})
	}
}

func TestTimothyHelpExcerptCap(t *testing.T) {
	t.Parallel()
	fk := &fakeHelpSearch{hits: []HelpHit{{Title: "Long", Excerpt: strings.Repeat("a", 5000)}}}
	out, err := TimothyHelp(TimothyHelpConfig{Search: fk.search}).Execute(context.Background(), json.RawMessage(`{"query":"q"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Count(out, "a"); got > timothyHelpExcerptMax+50 {
		t.Fatalf("excerpt not capped: %d bytes of it survived", got)
	}
	if !strings.Contains(out, strings.Repeat("a", timothyHelpExcerptMax)+" ...") {
		t.Fatalf("capped excerpt missing its cut marker:\n%s", out)
	}
}

func TestTimothyHelpRejectsBadArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  TimothyHelpConfig
		args string
		want string
	}{
		{"empty query", TimothyHelpConfig{Search: (&fakeHelpSearch{}).search}, `{"query":"  "}`, "query must not be empty"},
		{"k too big", TimothyHelpConfig{Search: (&fakeHelpSearch{}).search}, `{"query":"q","k":11}`, "k must be between 1 and 10"},
		{"k zero", TimothyHelpConfig{Search: (&fakeHelpSearch{}).search}, `{"query":"q","k":0}`, "k must be between 1 and 10"},
		{"bad json", TimothyHelpConfig{Search: (&fakeHelpSearch{}).search}, `{`, "invalid arguments"},
		{"no search wired", TimothyHelpConfig{}, `{"query":"q"}`, "docs search is not configured"},
		{"search fails", TimothyHelpConfig{Search: (&fakeHelpSearch{err: errors.New("memoryd down")}).search}, `{"query":"q"}`, "memoryd down"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := TimothyHelp(tc.cfg).Execute(context.Background(), json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// TestTimothyHelpToolShape pins what the loop and permission chain
// key on: the name, a trusted result, and query as the only required
// argument.
func TestTimothyHelpToolShape(t *testing.T) {
	t.Parallel()
	tool := TimothyHelp(TimothyHelpConfig{})
	if tool.Name != "timothy_help" || !tool.Trusted {
		t.Fatalf("tool = %q trusted=%v", tool.Name, tool.Trusted)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "query" {
		t.Fatalf("required = %v", schema.Required)
	}
}
