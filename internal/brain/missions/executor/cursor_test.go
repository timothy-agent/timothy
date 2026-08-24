package executor

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// loadCursorFixture returns every non-empty line of a recorded cursor
// fixture.
func loadCursorFixture(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "cursor-2026.08.11", name)) //nolint:gosec // G304: fixed testdata path.
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

func TestCursorParser_Happy(t *testing.T) {
	lines := loadCursorFixture(t, "happy.ndjson")
	p := cursorAdapter{}.NewParser()

	want := []eventSummary{
		{kind: KindSystem, textHead: "Auto Balance"},
		{kind: KindText, textHead: "I'll create `hello.t"},
		{kind: KindTool, toolName: "edit", status: "started"},
		{kind: KindTool, toolName: "shell", status: "started"},
		{kind: KindTool, toolName: "shell", status: "finished"},
		{kind: KindTool, toolName: "edit", status: "finished"},
		{kind: KindText, textHead: "The first `cat` ran "},
		{kind: KindTool, toolName: "shell", status: "started"},
		{kind: KindTool, toolName: "shell", status: "finished"},
		{kind: KindText, textHead: "Created `hello.txt` "},
		{kind: KindResult, textHead: "I'll create `hello.t"},
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
	if resultEv.Usage.InputTokens != 47674 || resultEv.Usage.OutputTokens != 329 {
		t.Errorf("result usage = %+v, want inputTokens=47674 outputTokens=329", resultEv.Usage)
	}
	if resultEv.Usage.CacheReadTokens != 9088 {
		t.Errorf("result usage cache read = %d, want 9088", resultEv.Usage.CacheReadTokens)
	}
	if resultEv.Usage.CostUSD != nil {
		t.Error("cursor never reports a trusted cost - CostUSD must stay nil")
	}
	// this recording's final message carries no verdict sentinel - the
	// prompt that produced it never asked for one.
	if resultEv.Result != nil {
		t.Errorf("happy fixture has no verdict sentinel, want nil Result, got %s", resultEv.Result)
	}

	stats := p.(ParserStats).Stats()
	if stats.Lines != len(lines) {
		t.Errorf("stats.Lines = %d, want %d", stats.Lines, len(lines))
	}
	if stats.Events != len(want) {
		t.Errorf("stats.Events = %d, want %d", stats.Events, len(want))
	}
	// noise: "user" (echoed prompt) and every "thinking" delta/completed line.
	if stats.Unknown != len(lines)-len(want) {
		t.Errorf("stats.Unknown = %d, want %d", stats.Unknown, len(lines)-len(want))
	}
}

func TestCursorParser_Verdict(t *testing.T) {
	p := cursorAdapter{}.NewParser()

	if _, ok := p.ParseLine([]byte(`{"type":"system","subtype":"init","model":"m"}`)); !ok {
		t.Fatal("system/init line failed to parse")
	}

	resultLine, err := json.Marshal(struct {
		Type    string      `json:"type"`
		Subtype string      `json:"subtype"`
		IsError bool        `json:"is_error"`
		Result  string      `json:"result"`
		Usage   cursorUsage `json:"usage"`
	}{
		Type: "result", Subtype: "success", IsError: false,
		Result: "All done.\n{\"status\":\"DONE\",\"note\":\"created and verified hello.txt\"}",
		Usage:  cursorUsage{InputTokens: 10, OutputTokens: 5},
	})
	if err != nil {
		t.Fatalf("marshal result line: %v", err)
	}

	ev, ok := p.ParseLine(resultLine)
	if !ok {
		t.Fatal("result line failed to parse")
	}
	if ev.Result == nil {
		t.Fatal("result event carries no structured Result payload")
	}

	res, ok := cursorAdapter{}.ParseResult(ev)
	if !ok {
		t.Fatalf("ParseResult ok=false, payload: %s", ev.Result)
	}
	if res.Status != "DONE" {
		t.Errorf("ParseResult status = %q, want DONE", res.Status)
	}
	if res.Note == "" {
		t.Error("ParseResult note is empty")
	}
}

func TestCursorParser_ResultIsError(t *testing.T) {
	p := cursorAdapter{}.NewParser()
	line := []byte(`{"type":"result","subtype":"error","is_error":true,"result":"model not found","usage":{"inputTokens":1,"outputTokens":0,"cacheReadTokens":0,"cacheWriteTokens":0}}`)
	ev, ok := p.ParseLine(line)
	if !ok {
		t.Fatal("result line failed to parse")
	}
	if ev.Err != "model not found" {
		t.Errorf("Err = %q, want %q", ev.Err, "model not found")
	}
}

func TestCursorParser_NoiseTolerance(t *testing.T) {
	p := cursorAdapter{}.NewParser()

	garbageLines := [][]byte{
		[]byte(``),
		[]byte(`not json at all`),
		[]byte(`{"type":"some_future_event_kind"}`),
		[]byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`),
		[]byte(`{"type":"thinking","subtype":"delta","text":"..."}`),
		[]byte(`{"type":"thinking","subtype":"completed"}`),
		[]byte(`{"type":"system","subtype":"other"}`),
		[]byte(`{"type":"tool_call","subtype":"unknown","call_id":"x","tool_call":{}}`),
	}
	for _, line := range garbageLines {
		if _, ok := p.ParseLine(line); ok {
			t.Errorf("garbage line unexpectedly parsed as event: %s", line)
		}
	}

	valid := []byte(`{"type":"system","subtype":"init","model":"cursor-model"}`)
	ev, ok := p.ParseLine(valid)
	if !ok {
		t.Fatal("valid system/init line failed to parse after noise")
	}
	if ev.Kind != KindSystem || ev.Text != "cursor-model" {
		t.Errorf("unexpected event: %+v", ev)
	}

	stats := p.(ParserStats).Stats()
	if stats.Lines != len(garbageLines)+1 {
		t.Errorf("stats.Lines = %d, want %d", stats.Lines, len(garbageLines)+1)
	}
	if stats.Events != 1 {
		t.Errorf("stats.Events = %d, want 1", stats.Events)
	}
	// the empty line short-circuits before Unknown++ (same as the other
	// adapters), so only 7 of the 8 garbage lines count.
	if stats.Unknown != len(garbageLines)-1 {
		t.Errorf("stats.Unknown = %d, want %d", stats.Unknown, len(garbageLines)-1)
	}
}

func TestCursorToolCall_Name(t *testing.T) {
	cases := []struct {
		name string
		call cursorToolCall
		want string
	}{
		{name: "edit", call: cursorToolCall{EditToolCall: json.RawMessage(`{}`)}, want: "edit"},
		{name: "shell", call: cursorToolCall{ShellToolCall: json.RawMessage(`{}`)}, want: "shell"},
		{name: "neither", call: cursorToolCall{}, want: "unknown"},
	}
	for _, c := range cases {
		if got := c.call.name(); got != c.want {
			t.Errorf("%s: name() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCursorAdapter_BuildInvocation(t *testing.T) {
	a := cursorAdapter{}

	tests := []struct {
		name    string
		spec    InvocationSpec
		wantErr bool
		check   func(t *testing.T, inv Invocation)
	}{
		{
			name: "basic",
			spec: InvocationSpec{ //nolint:gosec // G101: fixture value, not a real credential.
				Model: "claude-sonnet-5-high", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-cursor-test",
			},
			check: func(t *testing.T, inv Invocation) {
				if inv.Env["CURSOR_API_KEY"] != "sk-cursor-test" {
					t.Error("CURSOR_API_KEY must be set from spec.APIKey")
				}
				if inv.Env["CURSOR_CONFIG_DIR"] != "/tmp/run/cursor-home" {
					t.Errorf("CURSOR_CONFIG_DIR = %q, want /tmp/run/cursor-home", inv.Env["CURSOR_CONFIG_DIR"])
				}
				if inv.Env["AGENT_CLI_CREDENTIAL_STORE"] != "file" {
					t.Error("AGENT_CLI_CREDENTIAL_STORE must be \"file\"")
				}
				if !containsFlag(inv.Argv, "@PROMPT@") {
					t.Error("argv must carry the @PROMPT@ sentinel as its own element")
				}
			},
		},
		{
			name: "config file carries attribution flags disabled",
			spec: InvocationSpec{ //nolint:gosec // G101: fixture value, not a real credential.
				Model: "claude-sonnet-5-high", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-cursor-test",
			},
			check: func(t *testing.T, inv Invocation) {
				raw, ok := inv.Files["cursor-home/cli-config.json"]
				if !ok {
					t.Fatal("Files missing cursor-home/cli-config.json")
				}
				var cfg cursorConfig
				if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
					t.Fatalf("cli-config.json does not decode: %v", err)
				}
				if cfg.Attribution.AttributeCommitsToAgent {
					t.Error("attributeCommitsToAgent must be false - repo bans AI attribution")
				}
				if cfg.Attribution.AttributePRsToAgent {
					t.Error("attributePRsToAgent must be false - repo bans AI attribution")
				}
			},
		},
		{
			name: "allow/deny tools map into permissions",
			spec: InvocationSpec{ //nolint:gosec // G101: fixture value, not a real credential.
				Model: "claude-sonnet-5-high", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-cursor-test",
				AllowTools: []string{"Write", "Read"}, DenyTools: []string{"Bash(git push:*)"},
			},
			check: func(t *testing.T, inv Invocation) {
				raw := inv.Files["cursor-home/cli-config.json"]
				var cfg cursorConfig
				if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
					t.Fatalf("cli-config.json does not decode: %v", err)
				}
				if len(cfg.Permissions.Allow) != 2 || cfg.Permissions.Allow[0] != "Write" {
					t.Errorf("permissions.allow = %v, want [Write Read]", cfg.Permissions.Allow)
				}
				if len(cfg.Permissions.Deny) != 1 || cfg.Permissions.Deny[0] != "Bash(git push:*)" {
					t.Errorf("permissions.deny = %v, want [Bash(git push:*)]", cfg.Permissions.Deny)
				}
			},
		},
		{
			name: "system append rides a separate argv element before @PROMPT@",
			spec: InvocationSpec{ //nolint:gosec // G101: fixture value, not a real credential.
				Model: "claude-sonnet-5-high", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-cursor-test",
				SystemAppend: "be nice",
			},
			check: func(t *testing.T, inv Invocation) {
				idx := flagIndex(inv.Argv, "@PROMPT@")
				if idx <= 0 {
					t.Fatalf("@PROMPT@ not found or has no preceding element, argv=%v", inv.Argv)
				}
				if inv.Argv[idx-1] != "be nice" {
					t.Errorf("argv element before @PROMPT@ = %q, want the SystemAppend text", inv.Argv[idx-1])
				}
			},
		},
		{
			name: "argv exact with no system append",
			spec: InvocationSpec{ //nolint:gosec // G101: fixture value, not a real credential.
				Model: "claude-sonnet-5-high", PromptPath: "/tmp/run/prompt.md", Workdir: "/tmp/run/ws",
				AuthMode: AuthAPIKey, APIKey: "sk-cursor-test",
			},
			check: func(t *testing.T, inv Invocation) {
				want := []string{
					"cursor-agent", "-p", "--force",
					"--output-format", "stream-json", "--trust",
					"--model", "claude-sonnet-5-high",
					"@PROMPT@", cursorVerdictInstruction,
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
		{
			name:    "subscription auth rejected",
			spec:    InvocationSpec{Model: "m", PromptPath: "/tmp/run/prompt.md", AuthMode: AuthSubscription},
			wantErr: true,
		},
		{
			name: "oauth_token auth rejected",
			spec: InvocationSpec{ //nolint:gosec // G101: fixture value, not a real credential.
				Model: "m", PromptPath: "/tmp/run/prompt.md", AuthMode: AuthOAuthToken, APIKey: "sk-ant-oat-test",
			},
			wantErr: true,
		},
		{
			name:    "api_key mode without key is invalid",
			spec:    InvocationSpec{Model: "m", PromptPath: "/tmp/run/prompt.md", AuthMode: AuthAPIKey},
			wantErr: true,
		},
		{
			name:    "empty model is invalid",
			spec:    InvocationSpec{PromptPath: "/tmp/run/prompt.md", AuthMode: AuthAPIKey, APIKey: "sk"},
			wantErr: true,
		},
		{
			name:    "empty prompt path is invalid",
			spec:    InvocationSpec{Model: "m", AuthMode: AuthAPIKey, APIKey: "sk"},
			wantErr: true,
		},
		{
			name: "custom base url is rejected - no endpoint override support",
			spec: InvocationSpec{
				Model: "m", PromptPath: "/tmp/run/prompt.md", AuthMode: AuthAPIKey, APIKey: "sk",
				BaseURL: "https://example.com",
			},
			wantErr: true,
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
