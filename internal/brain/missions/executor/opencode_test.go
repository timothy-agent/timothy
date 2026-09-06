package executor

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadOpencodeFixture returns every non-empty line of a recorded opencode fixture.
func loadOpencodeFixture(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "opencode-1.18.18", name)) //nolint:gosec // G304: fixed testdata path.
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()

	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		lines = append(lines, cp)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return lines
}

func TestOpencodeParser_Happy(t *testing.T) {
	lines := loadOpencodeFixture(t, "happy.ndjson")
	p := opencodeAdapter{}.NewParser()

	want := []eventSummary{
		{kind: KindSystem, textHead: "ses_SESSION01"},
		{kind: KindText, textHead: "VERDICT: {\"status\":\""},
		{kind: KindResult, textHead: "VERDICT: {\"status\":\""},
	}

	var got []eventSummary
	var resultCount int
	var resultEv Event
	for _, line := range lines {
		ev, ok := p.ParseLine(line)
		if !ok {
			continue
		}
		got = append(got, summarize(ev))
		if ev.Kind == KindResult {
			resultCount++
			resultEv = ev
		}
	}

	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\ngot: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("event[%d] = %+v, want %+v", i, got[i], w)
		}
	}
	if resultCount != 1 {
		t.Fatalf("KindResult count = %d, want exactly 1", resultCount)
	}
	if resultEv.Err != "" {
		t.Errorf("happy fixture has no error, got Err=%q", resultEv.Err)
	}
	if resultEv.Usage == nil {
		t.Fatal("result event carries no usage")
	}
	if resultEv.Usage.InputTokens != 6212 {
		t.Errorf("Usage.InputTokens = %d, want 6212", resultEv.Usage.InputTokens)
	}
	if resultEv.Usage.OutputTokens != 55 {
		t.Errorf("Usage.OutputTokens = %d, want 55", resultEv.Usage.OutputTokens)
	}
	if resultEv.Usage.CostUSD != nil {
		t.Error("opencode never reports a trusted cost - CostUSD must stay nil")
	}
	if resultEv.Result == nil {
		t.Fatal("result event carries no structured Result payload")
	}

	res, ok := opencodeAdapter{}.ParseResult(resultEv)
	if !ok {
		t.Fatalf("ParseResult ok=false, payload: %s", resultEv.Result)
	}
	if res.Status != "DONE" {
		t.Errorf("ParseResult status = %q, want DONE", res.Status)
	}
	if res.Note != "all checks passed" {
		t.Errorf("ParseResult note = %q, want %q", res.Note, "all checks passed")
	}

	stats := p.(ParserStats).Stats()
	if stats.Lines != len(lines) {
		t.Errorf("stats.Lines = %d, want %d", stats.Lines, len(lines))
	}
	if stats.Events != len(want) {
		t.Errorf("stats.Events = %d, want %d", stats.Events, len(want))
	}
	if stats.Unknown != len(lines)-len(want) {
		t.Errorf("stats.Unknown = %d, want %d", stats.Unknown, len(lines)-len(want))
	}
}

func TestOpencodeParser_Tool(t *testing.T) {
	lines := loadOpencodeFixture(t, "tool.ndjson")
	p := opencodeAdapter{}.NewParser()

	want := []eventSummary{
		{kind: KindSystem, textHead: "ses_SESSION01"},
		{kind: KindText, textHead: "Executing the file c"},
		{kind: KindTool, toolName: "write", status: "finished"},
		// step_finish reason "tool-calls" after the write is noise (tokens folded in).
		{kind: KindText, textHead: "Reading back the fil"},
		{kind: KindTool, toolName: "read", status: "finished"},
		// step_finish reason "tool-calls" after the read is noise too.
		{kind: KindText, textHead: "**check.txt**\n\n```\no"},
		{kind: KindResult, textHead: "**check.txt**\n\n```\no"},
	}

	var got []eventSummary
	var resultCount int
	var resultEv Event
	for _, line := range lines {
		ev, ok := p.ParseLine(line)
		if !ok {
			continue
		}
		got = append(got, summarize(ev))
		if ev.Kind == KindResult {
			resultCount++
			resultEv = ev
		}
	}

	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\ngot: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("event[%d] = %+v, want %+v", i, got[i], w)
		}
	}
	if resultCount != 1 {
		t.Fatalf("KindResult count = %d, want exactly 1", resultCount)
	}
	if resultEv.Err != "" {
		t.Errorf("tool fixture has no error, got Err=%q", resultEv.Err)
	}
	if resultEv.Usage == nil {
		t.Fatal("result event carries no usage")
	}
	if resultEv.Usage.InputTokens != 6216+6279+6389 {
		t.Errorf("Usage.InputTokens = %d, want 6216+6279+6389=18884", resultEv.Usage.InputTokens)
	}
	if resultEv.Usage.OutputTokens != 240+55+16 {
		t.Errorf("Usage.OutputTokens = %d, want 240+55+16=311", resultEv.Usage.OutputTokens)
	}
	// no verdict sentinel in this fixture's final text.
	if resultEv.Result != nil {
		t.Errorf("tool fixture's final text has no verdict, want nil Result, got %s", resultEv.Result)
	}

	stats := p.(ParserStats).Stats()
	if stats.Lines != len(lines) {
		t.Errorf("stats.Lines = %d, want %d", stats.Lines, len(lines))
	}
	if stats.Events != len(want) {
		t.Errorf("stats.Events = %d, want %d", stats.Events, len(want))
	}
	if stats.Unknown != len(lines)-len(want) {
		t.Errorf("stats.Unknown = %d, want %d", stats.Unknown, len(lines)-len(want))
	}
}

func TestOpencodeParser_Error(t *testing.T) {
	lines := loadOpencodeFixture(t, "error.ndjson")
	p := opencodeAdapter{}.NewParser()

	want := []eventSummary{
		{kind: KindResult},
	}

	var got []eventSummary
	var resultCount int
	var resultEv Event
	for _, line := range lines {
		ev, ok := p.ParseLine(line)
		if !ok {
			continue
		}
		got = append(got, summarize(ev))
		if ev.Kind == KindResult {
			resultCount++
			resultEv = ev
		}
	}

	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\ngot: %+v", len(got), len(want), got)
	}
	if resultCount != 1 {
		t.Fatalf("KindResult count = %d, want exactly 1", resultCount)
	}
	if resultEv.Err == "" {
		t.Error("error fixture ends in a top-level error event, expected non-empty Err")
	}
	if !strings.Contains(resultEv.Err, "Cannot connect") {
		t.Errorf("Err = %q, want it to contain %q", resultEv.Err, "Cannot connect")
	}
	if resultEv.Usage != nil {
		t.Errorf("error event carries no usage field, want nil Usage, got %+v", resultEv.Usage)
	}
	if resultEv.Result != nil {
		t.Errorf("error fixture has no verdict text, want nil Result, got %s", resultEv.Result)
	}

	stats := p.(ParserStats).Stats()
	if stats.Lines != len(lines) {
		t.Errorf("stats.Lines = %d, want %d", stats.Lines, len(lines))
	}
	if stats.Events != len(want) {
		t.Errorf("stats.Events = %d, want %d", stats.Events, len(want))
	}
	if stats.Unknown != len(lines)-len(want) {
		t.Errorf("stats.Unknown = %d, want %d", stats.Unknown, len(lines)-len(want))
	}

	if _, ok := (opencodeAdapter{}).ParseResult(resultEv); ok {
		t.Error("ParseResult ok=true on an error result with no verdict payload")
	}
}

func TestOpencodeParser_NoiseTolerance(t *testing.T) {
	p := opencodeAdapter{}.NewParser()

	garbageLines := [][]byte{
		[]byte(``),
		[]byte(`not json at all`),
		[]byte(`{"type":"some_future_event_kind","payload":123}`),
		[]byte(`{"type":"tool_use","part":{"tool":"write","state":{"status":"error"}}}`),
		[]byte(`{"type":"step_finish","part":{"reason":"tool-calls","tokens":{"input":1,"output":1}}}`),
	}
	for _, line := range garbageLines {
		if _, ok := p.ParseLine(line); ok {
			t.Errorf("garbage/noise line unexpectedly parsed as event: %s", line)
		}
	}

	valid := []byte(`{"type":"step_start","sessionID":"abc-123","part":{"type":"step-start"}}`)
	ev, ok := p.ParseLine(valid)
	if !ok {
		t.Fatal("valid step_start line failed to parse after noise")
	}
	if ev.Kind != KindSystem || ev.Text != "abc-123" {
		t.Errorf("unexpected event: %+v", ev)
	}

	// a second step_start is noise (only the first becomes KindSystem).
	second := []byte(`{"type":"step_start","sessionID":"abc-123","part":{"type":"step-start"}}`)
	if _, ok := p.ParseLine(second); ok {
		t.Error("second step_start unexpectedly parsed as event")
	}

	stats := p.(ParserStats).Stats()
	if stats.Lines != len(garbageLines)+2 {
		t.Errorf("stats.Lines = %d, want %d", stats.Lines, len(garbageLines)+2)
	}
	if stats.Events != 1 {
		t.Errorf("stats.Events = %d, want 1", stats.Events)
	}
	// the empty line short-circuits before Unknown++ (same as codex/pi/claude).
	if stats.Unknown != len(garbageLines)-1+1 {
		t.Errorf("stats.Unknown = %d, want %d", stats.Unknown, len(garbageLines)-1+1)
	}
}

func TestOpencodeAdapter_BuildInvocation(t *testing.T) {
	a := opencodeAdapter{}

	tests := []struct {
		name    string
		spec    InvocationSpec
		wantErr bool
		check   func(t *testing.T, inv Invocation)
	}{
		{
			name: "read-only is refused: no verified opencode knob (issue #582)",
			spec: InvocationSpec{
				Model: "gpt-oss:20b", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai", ReadOnly: true,
			},
			wantErr: true,
		},
		{
			name: "openai wire with base url",
			spec: InvocationSpec{
				Model: "gpt-oss:20b", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				BaseURL: "http://host.docker.internal:11434/v1",
			},
			check: func(t *testing.T, inv Invocation) {
				if inv.Env["OPENCODE_API_KEY"] != "sk-test" {
					t.Error("OPENCODE_API_KEY must be set from spec.APIKey")
				}
				if inv.Env["OPENCODE_CONFIG"] != "/tmp/run/opencode/opencode.json" {
					t.Errorf("OPENCODE_CONFIG = %q, want /tmp/run/opencode/opencode.json", inv.Env["OPENCODE_CONFIG"])
				}
				assertOpencodeConfig(t, inv, "http://host.docker.internal:11434/v1", "gpt-oss:20b", "@ai-sdk/openai-compatible")
			},
		},
		{
			name: "empty base url defaults to api.openai.com and selects the official openai package",
			spec: InvocationSpec{
				Model: "gpt-5.3", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
			},
			check: func(t *testing.T, inv Invocation) {
				assertOpencodeConfig(t, inv, opencodeDefaultBaseURL, "gpt-5.3", "@ai-sdk/openai")
			},
		},
		{
			name: "explicit api.openai.com base url selects the official openai package",
			spec: InvocationSpec{
				Model: "gpt-5.2", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				BaseURL: "https://api.openai.com/v1",
			},
			check: func(t *testing.T, inv Invocation) {
				assertOpencodeConfig(t, inv, "https://api.openai.com/v1", "gpt-5.2", "@ai-sdk/openai")
			},
		},
		{
			name: "glm-style base url keeps openai-compatible",
			spec: InvocationSpec{
				Model: "glm-4.6", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				BaseURL: "https://api.z.ai/api/paas/v4",
			},
			check: func(t *testing.T, inv Invocation) {
				assertOpencodeConfig(t, inv, "https://api.z.ai/api/paas/v4", "glm-4.6", "@ai-sdk/openai-compatible")
			},
		},
		{
			name: "unparseable base url falls back to openai-compatible",
			spec: InvocationSpec{
				Model: "gpt-5.3", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				BaseURL: "http://[::1]:not-a-port/v1",
			},
			check: func(t *testing.T, inv Invocation) {
				assertOpencodeConfig(t, inv, "http://[::1]:not-a-port/v1", "gpt-5.3", "@ai-sdk/openai-compatible")
			},
		},
		{
			name: "anthropic wire is invalid",
			spec: InvocationSpec{
				Model: "gpt-5.3", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "anthropic",
			},
			wantErr: true,
		},
		{
			name: "unknown wire is invalid",
			spec: InvocationSpec{
				Model: "gpt-5.3", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "carrier-pigeon",
			},
			wantErr: true,
		},
		{
			name: "subscription auth rejected",
			spec: InvocationSpec{
				Model: "gpt-5.3", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthSubscription, Wire: "openai",
			},
			wantErr: true,
		},
		{
			name: "oauth_token auth rejected",
			spec: InvocationSpec{ //nolint:gosec // G101: fixture value, not a real credential.
				Model: "gpt-5.3", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthOAuthToken, APIKey: "sk-ant-oat-test", Wire: "openai",
			},
			wantErr: true,
		},
		{
			name:    "api_key mode without key is invalid",
			spec:    InvocationSpec{Model: "gpt-5.3", PromptPath: "/tmp/run/prompt.md", AuthMode: AuthAPIKey, Wire: "openai"},
			wantErr: true,
		},
		{
			name:    "empty model is invalid",
			spec:    InvocationSpec{PromptPath: "/tmp/run/prompt.md", AuthMode: AuthAPIKey, APIKey: "sk", Wire: "openai"},
			wantErr: true,
		},
		{
			name:    "empty prompt path is invalid",
			spec:    InvocationSpec{Model: "gpt-5.3", AuthMode: AuthAPIKey, APIKey: "sk", Wire: "openai"},
			wantErr: true,
		},
		{
			name: "allow/deny tools and budget are ignored",
			spec: InvocationSpec{
				Model: "gpt-5.3", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				AllowTools: []string{"Write"}, DenyTools: []string{"Bash(git push:*)"},
				BudgetUSD: floatPtr(5.0),
			},
			check: func(t *testing.T, inv Invocation) {
				if containsFlag(inv.Argv, "--allowedTools") || containsFlag(inv.Argv, "--disallowedTools") {
					t.Error("opencode has no allow/deny tool flags, must not appear in argv")
				}
				if containsFlag(inv.Argv, "--max-budget-usd") {
					t.Error("opencode has no budget flag, must not appear in argv")
				}
			},
		},
		{
			name: "system append lands in instructions file",
			spec: InvocationSpec{
				Model: "gpt-5.3", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				SystemAppend: "be nice",
			},
			check: func(t *testing.T, inv Invocation) {
				got := inv.Files["opencode/instructions.md"]
				if !strings.Contains(got, "be nice") {
					t.Errorf("instructions.md missing SystemAppend text: %q", got)
				}
				if !strings.Contains(got, "DONE") || !strings.Contains(got, "RETRY") || !strings.Contains(got, "BLOCKED") {
					t.Errorf("instructions.md missing verdict instruction: %q", got)
				}
			},
		},
		{
			name: "empty system append still includes verdict instruction",
			spec: InvocationSpec{
				Model: "gpt-5.3", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
			},
			check: func(t *testing.T, inv Invocation) {
				got := inv.Files["opencode/instructions.md"]
				if !strings.Contains(got, "DONE") {
					t.Errorf("instructions.md must always carry the verdict instruction, got %q", got)
				}
			},
		},
		{
			name: "hostile base url round-trips byte-identical through the json config",
			spec: InvocationSpec{
				Model: "gpt-5.3", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				BaseURL: "https://evil.example.com/\"" + "\n" + `\` + "back\\slash" + "\r\t",
			},
			check: func(t *testing.T, inv Invocation) {
				raw := inv.Files["opencode/opencode.json"]
				var cfg struct {
					Provider struct {
						Timothy struct {
							Options struct {
								BaseURL string `json:"baseURL"`
							} `json:"options"`
						} `json:"timothy"`
					} `json:"provider"`
				}
				if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
					t.Fatalf("config.json doesn't parse: %v\n%s", err, raw)
				}
				want := "https://evil.example.com/\"" + "\n" + `\` + "back\\slash" + "\r\t"
				if cfg.Provider.Timothy.Options.BaseURL != want {
					t.Errorf("baseURL round-trip mismatch:\ngot  %q\nwant %q", cfg.Provider.Timothy.Options.BaseURL, want)
				}
			},
		},
		{
			name: "argv exact",
			spec: InvocationSpec{
				Model: "gpt-oss:20b", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
			},
			check: func(t *testing.T, inv Invocation) {
				want := []string{
					"opencode", "run",
					"--format", "json",
					"-m", "timothy/gpt-oss:20b",
					"@PROMPT@",
				}
				if len(inv.Argv) != len(want) {
					t.Fatalf("argv = %v, want %v", inv.Argv, want)
				}
				for i := range want {
					if inv.Argv[i] != want[i] {
						t.Errorf("argv[%d] = %q, want %q", i, inv.Argv[i], want[i])
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := a.BuildInvocation(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if inv.PromptFile != tt.spec.PromptPath {
				t.Errorf("PromptFile = %q, want %q", inv.PromptFile, tt.spec.PromptPath)
			}
			if inv.Env["NO_COLOR"] != "1" {
				t.Error("NO_COLOR must always be set")
			}
			if tt.check != nil {
				tt.check(t, inv)
			}
		})
	}
}

// TestOpencodeAdapter_ResumeUnsupported covers D-103 (issue #499):
// opencode is not installed in this environment and no --help text or
// repo doc records a resume/continue flag, so the adapter must declare
// SupportsResume false and never invent one - BuildInvocation ignores
// ResumeSessionID entirely rather than guessing a flag.
func TestOpencodeAdapter_ResumeUnsupported(t *testing.T) {
	a := opencodeAdapter{}
	if a.Capabilities().SupportsResume {
		t.Fatal("opencode Capabilities().SupportsResume = true, want false (no verified resume flag)")
	}
	inv, err := a.BuildInvocation(InvocationSpec{
		Model: "gpt-oss:20b", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
		AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
		ResumeSessionID: "ses_should_be_ignored",
	})
	if err != nil {
		t.Fatalf("BuildInvocation: %v", err)
	}
	for _, a := range inv.Argv {
		if strings.Contains(a, "ses_should_be_ignored") {
			t.Errorf("argv %v must never carry ResumeSessionID, no resume flag exists", inv.Argv)
		}
	}
}

// assertOpencodeConfig checks the opencode.json Files entry parses and
// declares the expected npm/baseURL/model under provider.timothy.
func assertOpencodeConfig(t *testing.T, inv Invocation, wantBaseURL, wantModel, wantNPM string) {
	t.Helper()
	raw, ok := inv.Files["opencode/opencode.json"]
	if !ok {
		t.Fatal("Files missing opencode/opencode.json")
	}
	var cfg struct {
		Schema     string `json:"$schema"`
		Permission string `json:"permission"`
		Provider   struct {
			Timothy struct {
				NPM     string `json:"npm"`
				Options struct {
					BaseURL string `json:"baseURL"`
					APIKey  string `json:"apiKey"`
				} `json:"options"`
				Models map[string]json.RawMessage `json:"models"`
			} `json:"timothy"`
		} `json:"provider"`
		Instructions []string `json:"instructions"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("config.json doesn't parse: %v\n%s", err, raw)
	}
	if cfg.Schema != "https://opencode.ai/config.json" {
		t.Errorf("$schema = %q, want https://opencode.ai/config.json", cfg.Schema)
	}
	if cfg.Permission != "allow" {
		t.Errorf("permission = %q, want allow (headless runs auto-reject prompts)", cfg.Permission)
	}
	if cfg.Provider.Timothy.NPM != wantNPM {
		t.Errorf("provider.timothy.npm = %q, want %q", cfg.Provider.Timothy.NPM, wantNPM)
	}
	if cfg.Provider.Timothy.Options.BaseURL != wantBaseURL {
		t.Errorf("baseURL = %q, want %q", cfg.Provider.Timothy.Options.BaseURL, wantBaseURL)
	}
	if cfg.Provider.Timothy.Options.APIKey != "{env:OPENCODE_API_KEY}" {
		t.Errorf("apiKey = %q, want the env-substitution template", cfg.Provider.Timothy.Options.APIKey)
	}
	if _, ok := cfg.Provider.Timothy.Models[wantModel]; !ok {
		t.Errorf("models missing key %q: %v", wantModel, cfg.Provider.Timothy.Models)
	}
	if len(cfg.Instructions) != 1 || !strings.HasSuffix(cfg.Instructions[0], "opencode/instructions.md") {
		t.Errorf("instructions = %v, want one path ending in opencode/instructions.md", cfg.Instructions)
	}
}
