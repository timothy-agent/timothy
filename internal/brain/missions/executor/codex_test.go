package executor

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadCodexFixture returns every non-empty line of a recorded codex fixture.
func loadCodexFixture(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "codex-0.147.0", name)) //nolint:gosec // G304: fixed testdata path.
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

func TestCodexParser_Happy(t *testing.T) {
	lines := loadCodexFixture(t, "happy.ndjson")
	p := codexAdapter{}.NewParser()

	want := []eventSummary{
		{kind: KindSystem, textHead: "THREAD_ID"},
		{kind: KindText, textHead: "{\"status\":\"DONE\",\"no"},
		{kind: KindResult, textHead: "{\"status\":\"DONE\",\"no"},
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
	if resultEv.Usage.InputTokens == 0 && resultEv.Usage.OutputTokens == 0 {
		t.Error("result usage looks empty")
	}
	if resultEv.Usage.CostUSD != nil {
		t.Error("codex never reports a trusted cost - CostUSD must stay nil")
	}
	if resultEv.Result == nil {
		t.Fatal("result event carries no structured Result payload")
	}

	res, ok := codexAdapter{}.ParseResult(resultEv)
	if !ok {
		t.Fatalf("ParseResult ok=false, payload: %s", resultEv.Result)
	}
	if res.Status != "DONE" {
		t.Errorf("ParseResult status = %q, want DONE", res.Status)
	}
	if res.Note == "" {
		t.Error("ParseResult note is empty")
	}

	stats := p.(ParserStats).Stats()
	if stats.Lines != len(lines) {
		t.Errorf("stats.Lines = %d, want %d", stats.Lines, len(lines))
	}
	if stats.Events != len(want) {
		t.Errorf("stats.Events = %d, want %d", stats.Events, len(want))
	}
	// noise: item-level "error" (Model metadata not found), turn.started,
	// reasoning item.completed.
	if stats.Unknown != len(lines)-len(want) {
		t.Errorf("stats.Unknown = %d, want %d", stats.Unknown, len(lines)-len(want))
	}
}

func TestCodexParser_Tool(t *testing.T) {
	lines := loadCodexFixture(t, "tool.ndjson")
	p := codexAdapter{}.NewParser()

	want := []eventSummary{
		{kind: KindSystem, textHead: "THREAD_ID"},
		{kind: KindTool, toolName: "shell", status: "started"},
		{kind: KindTool, toolName: "shell", status: "finished"},
		{kind: KindTool, toolName: "shell", status: "started"},
		{kind: KindTool, toolName: "shell", status: "finished"},
		{kind: KindText, textHead: "{\"status\":\"DONE\",\"no"},
		{kind: KindResult, textHead: "{\"status\":\"DONE\",\"no"},
	}

	var got []eventSummary
	var resultEv Event
	var resultCount int
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
	if resultEv.Usage == nil || resultEv.Usage.OutputTokens == 0 {
		t.Error("result usage looks empty")
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

func TestCodexParser_Error(t *testing.T) {
	lines := loadCodexFixture(t, "error.ndjson")
	p := codexAdapter{}.NewParser()

	want := []eventSummary{
		{kind: KindSystem, textHead: "THREAD_ID"},
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
	for i, w := range want {
		if got[i] != w {
			t.Errorf("event[%d] = %+v, want %+v", i, got[i], w)
		}
	}
	if resultCount != 1 {
		t.Fatalf("KindResult count = %d, want exactly 1", resultCount)
	}
	// CRITICAL: codex exits 0 regardless - turn.failed's Err is the only
	// failure signal a caller has, the mid-run "error" reconnect lines are
	// noise.
	if resultEv.Err == "" {
		t.Error("error fixture ends in turn.failed, expected non-empty Err")
	}
	if resultEv.Usage != nil {
		t.Errorf("turn.failed carries no usage field, want nil Usage, got %+v", resultEv.Usage)
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

	if _, ok := (codexAdapter{}).ParseResult(resultEv); ok {
		t.Error("ParseResult ok=true on a turn.failed result with no verdict payload")
	}
}

func TestCodexParser_NoiseTolerance(t *testing.T) {
	p := codexAdapter{}.NewParser()

	garbageLines := [][]byte{
		[]byte(``),
		[]byte(`not json at all`),
		[]byte(`{"type":"some_future_event_kind","payload":123}`),
		[]byte(`{"type":"turn.started"}`),
		[]byte(`{"type":"item.updated","item":{"id":"item_1","type":"reasoning"}}`),
		[]byte(`{"type":"item.completed","item":{"id":"item_1","type":"reasoning","text":"thinking"}}`),
		[]byte(`{"type":"item.completed","item":{"id":"item_1","type":"error","message":"Model metadata not found."}}`),
		[]byte(`{"type":"error","message":"Reconnecting... 1/5"}`),
	}
	for _, line := range garbageLines {
		if _, ok := p.ParseLine(line); ok {
			t.Errorf("garbage line unexpectedly parsed as event: %s", line)
		}
	}

	valid := []byte(`{"type":"thread.started","thread_id":"abc-123"}`)
	ev, ok := p.ParseLine(valid)
	if !ok {
		t.Fatal("valid thread.started line failed to parse after noise")
	}
	if ev.Kind != KindSystem || ev.Text != "abc-123" {
		t.Errorf("unexpected event: %+v", ev)
	}

	stats := p.(ParserStats).Stats()
	if stats.Lines != len(garbageLines)+1 {
		t.Errorf("stats.Lines = %d, want %d", stats.Lines, len(garbageLines)+1)
	}
	if stats.Events != 1 {
		t.Errorf("stats.Events = %d, want 1", stats.Events)
	}
	// the empty line short-circuits before Unknown++ (same as pi/claude),
	// so only 7 of the 8 garbage lines count.
	if stats.Unknown != len(garbageLines)-1 {
		t.Errorf("stats.Unknown = %d, want %d", stats.Unknown, len(garbageLines)-1)
	}
}

func TestCodexAdapter_BuildInvocation(t *testing.T) {
	a := codexAdapter{}

	tests := []struct {
		name    string
		spec    InvocationSpec
		wantErr bool
		check   func(t *testing.T, inv Invocation)
	}{
		{
			name: "openai wire with base url",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				BaseURL: "http://host.docker.internal:11434/v1",
			},
			check: func(t *testing.T, inv Invocation) {
				if inv.Env["CODEX_API_KEY"] != "sk-test" {
					t.Error("CODEX_API_KEY must be set from spec.APIKey")
				}
				if inv.Env["CODEX_HOME"] != "/tmp/run/codex-home" {
					t.Errorf("CODEX_HOME = %q, want /tmp/run/codex-home", inv.Env["CODEX_HOME"])
				}
				assertCodexConfigTOML(t, inv, "http://host.docker.internal:11434/v1")
			},
		},
		{
			name: "empty base url defaults to api.openai.com",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
			},
			check: func(t *testing.T, inv Invocation) {
				assertCodexConfigTOML(t, inv, codexDefaultBaseURL)
			},
		},
		{
			name: "anthropic wire is invalid",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "anthropic",
			},
			wantErr: true,
		},
		{
			name: "unknown wire is invalid",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "carrier-pigeon",
			},
			wantErr: true,
		},
		{
			name: "subscription auth rejected",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthSubscription, Wire: "openai",
			},
			wantErr: true,
		},
		{
			name: "oauth_token auth rejected",
			spec: InvocationSpec{ //nolint:gosec // G101: fixture value, not a real credential.
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthOAuthToken, APIKey: "sk-ant-oat-test", Wire: "openai",
			},
			wantErr: true,
		},
		{
			name:    "api_key mode without key is invalid",
			spec:    InvocationSpec{Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md", AuthMode: AuthAPIKey, Wire: "openai"},
			wantErr: true,
		},
		{
			name:    "empty model is invalid",
			spec:    InvocationSpec{PromptPath: "/tmp/run/prompt.md", AuthMode: AuthAPIKey, APIKey: "sk", Wire: "openai"},
			wantErr: true,
		},
		{
			name:    "empty prompt path is invalid",
			spec:    InvocationSpec{Model: "gpt-5.3-codex", AuthMode: AuthAPIKey, APIKey: "sk", Wire: "openai"},
			wantErr: true,
		},
		{
			name: "allow/deny tools and budget are ignored",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				AllowTools: []string{"Write"}, DenyTools: []string{"Bash(git push:*)"},
				BudgetUSD: floatPtr(5.0),
			},
			check: func(t *testing.T, inv Invocation) {
				if containsFlag(inv.Argv, "--allowedTools") || containsFlag(inv.Argv, "--disallowedTools") {
					t.Error("codex has no allow/deny tool flags, must not appear in argv")
				}
				if containsFlag(inv.Argv, "--max-budget-usd") {
					t.Error("codex has no budget flag, must not appear in argv")
				}
			},
		},
		{
			name: "result schema writes codex-home/schema.json and passes --output-schema",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				ResultSchema: json.RawMessage(`{"type":"object"}`),
			},
			check: func(t *testing.T, inv Invocation) {
				if _, ok := inv.Files["codex-home/schema.json"]; !ok {
					t.Fatal("Files missing codex-home/schema.json")
				}
				if !containsFlag(inv.Argv, "--output-schema") {
					t.Error("--output-schema must be passed when ResultSchema is set")
				}
				idx := flagIndex(inv.Argv, "--output-schema")
				want := "/tmp/run/codex-home/schema.json"
				if idx == -1 || inv.Argv[idx+1] != want {
					t.Errorf("--output-schema value = %q, want %q", inv.Argv[idx+1], want)
				}
			},
		},
		{
			name: "no result schema omits --output-schema",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
			},
			check: func(t *testing.T, inv Invocation) {
				if containsFlag(inv.Argv, "--output-schema") {
					t.Error("--output-schema must not appear without a ResultSchema")
				}
				if _, ok := inv.Files["codex-home/schema.json"]; ok {
					t.Error("schema.json must not be written without a ResultSchema")
				}
			},
		},
		{
			name: "system append writes codex-home/AGENTS.md",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				SystemAppend: "be nice",
			},
			check: func(t *testing.T, inv Invocation) {
				if got := inv.Files["codex-home/AGENTS.md"]; got != "be nice" {
					t.Errorf("codex-home/AGENTS.md = %q, want %q", got, "be nice")
				}
			},
		},
		{
			name: "empty system append omits AGENTS.md",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
			},
			check: func(t *testing.T, inv Invocation) {
				if _, ok := inv.Files["codex-home/AGENTS.md"]; ok {
					t.Error("AGENTS.md must not be written without a SystemAppend")
				}
			},
		},
		{
			name: "resume session id switches to the resume subcommand (D-103, issue #499)",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				ResumeSessionID: "thread-abc-123",
			},
			check: func(t *testing.T, inv Invocation) {
				if inv.Argv[0] != "codex" || inv.Argv[1] != "exec" || inv.Argv[2] != "resume" || inv.Argv[3] != "thread-abc-123" {
					t.Fatalf("argv = %v, want [codex exec resume thread-abc-123 ...]", inv.Argv)
				}
				if containsFlag(inv.Argv, "-C") {
					t.Error("codex exec resume has no -C flag, must not appear in argv")
				}
			},
		},
		{
			name: "empty resume session id keeps the plain exec subcommand",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
			},
			check: func(t *testing.T, inv Invocation) {
				if containsFlag(inv.Argv, "resume") {
					t.Error("resume subcommand must not appear without a ResumeSessionID")
				}
				if !containsFlagValue(inv.Argv, "-C", "/tmp/run/ws") {
					t.Error("plain exec must still pass -C <workdir>")
				}
			},
		},
		{
			name: "hostile base url is toml-escaped, not broken out of the string",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				BaseURL: `https://evil.example.com/"` + "\n" + `[otel]` + "\n" + `exporter = "otlp-grpc`,
			},
			check: func(t *testing.T, inv Invocation) {
				raw := inv.Files["codex-home/config.toml"]
				// base_url must stay one line: an escaped quote and \n
				// sequences, never a raw newline that could close the
				// string early and let a hostile value forge new lines.
				lines := strings.Split(raw, "\n")
				var baseURLLines, realOtelTables int
				for _, l := range lines {
					if strings.HasPrefix(l, "base_url = ") {
						baseURLLines++
						if !strings.Contains(l, `\"`) || !strings.Contains(l, `\n[otel]`) {
							t.Errorf("base_url line lost its escaping: %s", l)
						}
					}
					// a real table header line is exactly "[otel]", never
					// the escaped text embedded inside base_url's string.
					if l == "[otel]" {
						realOtelTables++
					}
				}
				if baseURLLines != 1 {
					t.Errorf("base_url must occupy exactly one line, got %d: %s", baseURLLines, raw)
				}
				if realOtelTables != 1 {
					t.Errorf("hostile input injected an extra real [otel] table line, got %d: %s", realOtelTables, raw)
				}
			},
		},
		{
			name: "read-only swaps the bypass flag for --sandbox read-only (issue #582)",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				ReadOnly: true,
			},
			check: func(t *testing.T, inv Invocation) {
				if !containsFlagValue(inv.Argv, "--sandbox", "read-only") {
					t.Errorf("argv %v missing --sandbox read-only", inv.Argv)
				}
				if containsFlag(inv.Argv, "--dangerously-bypass-approvals-and-sandbox") {
					t.Error("read-only run must not carry the bypass flag")
				}
			},
		},
		{
			name: "read-only resume keeps --sandbox read-only on the resume subcommand (issue #582)",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
				ReadOnly: true, ResumeSessionID: "thread-abc-123",
			},
			check: func(t *testing.T, inv Invocation) {
				if !containsFlagValue(inv.Argv, "--sandbox", "read-only") {
					t.Errorf("argv %v missing --sandbox read-only", inv.Argv)
				}
				if containsFlag(inv.Argv, "--dangerously-bypass-approvals-and-sandbox") {
					t.Error("read-only run must not carry the bypass flag")
				}
			},
		},
		{
			name: "argv exact",
			spec: InvocationSpec{
				Model: "gpt-5.3-codex", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
			},
			check: func(t *testing.T, inv Invocation) {
				want := []string{
					"codex", "exec", "--json",
					"-C", "/tmp/run/ws",
					"--dangerously-bypass-approvals-and-sandbox",
					"--skip-git-repo-check",
					"-m", "gpt-5.3-codex",
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

// assertCodexConfigTOML checks the config.toml Files entry declares the
// expected base_url inside model_providers.timothy.
func assertCodexConfigTOML(t *testing.T, inv Invocation, wantBaseURL string) {
	t.Helper()
	raw, ok := inv.Files["codex-home/config.toml"]
	if !ok {
		t.Fatal("Files missing codex-home/config.toml")
	}
	want := `base_url = "` + wantBaseURL + `"`
	if !strings.Contains(raw, want) {
		t.Errorf("config.toml missing %q:\n%s", want, raw)
	}
	if !strings.Contains(raw, `wire_api = "responses"`) {
		t.Errorf("config.toml missing wire_api = \"responses\":\n%s", raw)
	}
}

func flagIndex(argv []string, flag string) int {
	for i, a := range argv {
		if a == flag {
			return i
		}
	}
	return -1
}
