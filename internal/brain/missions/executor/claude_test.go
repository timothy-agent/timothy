package executor

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"
)

// loadFixture returns every non-empty line of a recorded claude-cli fixture.
func loadFixture(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "claude-2.1.223", name)) //nolint:gosec // G304: fixed testdata path.
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

// eventSummary is the subset of an Event this test compares, keeping
// expectations readable.
type eventSummary struct {
	kind     EventKind
	textHead string // first few chars of Text, "" to skip the check
	toolName string
	status   string
}

func summarize(ev Event) eventSummary {
	s := eventSummary{kind: ev.Kind}
	if len(ev.Text) > 0 {
		if len(ev.Text) > 20 {
			s.textHead = ev.Text[:20]
		} else {
			s.textHead = ev.Text
		}
	}
	if ev.Tool != nil {
		s.toolName = ev.Tool.Name
		s.status = ev.Tool.Status
	}
	return s
}

func TestClaudeParser_Basic(t *testing.T) {
	lines := loadFixture(t, "basic.ndjson")
	p := claudeAdapter{}.NewParser()

	want := []eventSummary{
		{kind: KindSystem, textHead: "claude-haiku-4-5-202"},
		{kind: KindText, textHead: "Creating hello.txt w"},
		{kind: KindTool, toolName: "Write", status: "started"},
		{kind: KindTool, toolName: "Write", status: "finished"},
		{kind: KindText, textHead: "Done. File created a"},
		{kind: KindResult, textHead: "Done. File created a"},
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
	if resultEv.Usage == nil {
		t.Fatal("result event carries no usage")
	}
	if resultEv.Usage.InputTokens == 0 && resultEv.Usage.OutputTokens == 0 {
		t.Error("result usage looks empty")
	}
	if resultEv.Usage.CostUSD == nil {
		t.Error("result usage missing cost")
	}
	if resultEv.Err != "" {
		t.Errorf("basic fixture is_error=false, got Err=%q", resultEv.Err)
	}

	stats := p.(ParserStats).Stats()
	if stats.Lines != len(lines) {
		t.Errorf("stats.Lines = %d, want %d", stats.Lines, len(lines))
	}
	if stats.Events != len(want) {
		t.Errorf("stats.Events = %d, want %d", stats.Events, len(want))
	}
	// hook_started, hook_response, and rate_limit_event are the known
	// noise lines in this fixture.
	if stats.Unknown != 3 {
		t.Errorf("stats.Unknown = %d, want 3 (2 hook lines + rate_limit_event)", stats.Unknown)
	}
}

func TestClaudeParser_Schema(t *testing.T) {
	lines := loadFixture(t, "schema.ndjson")
	p := claudeAdapter{}.NewParser()

	var resultCount int
	var resultEv Event
	var sawStructuredOutputTool bool
	for _, line := range lines {
		ev, ok := p.ParseLine(line)
		if !ok {
			continue
		}
		if ev.Kind == KindTool && ev.Tool.Name == "StructuredOutput" {
			sawStructuredOutputTool = true
		}
		if ev.Kind == KindResult {
			resultCount++
			resultEv = ev
		}
	}

	if resultCount != 1 {
		t.Fatalf("KindResult count = %d, want exactly 1", resultCount)
	}
	if !sawStructuredOutputTool {
		t.Error("expected a StructuredOutput tool_use event")
	}
	if resultEv.Result == nil {
		t.Fatal("result event carries no structured Result payload")
	}

	got, ok := claudeAdapter{}.ParseResult(resultEv)
	if !ok {
		t.Fatalf("ParseResult ok=false, payload: %s", resultEv.Result)
	}
	if got.Status != "DONE" {
		t.Errorf("ParseResult status = %q, want DONE", got.Status)
	}
	if got.Note == "" {
		t.Error("ParseResult note is empty")
	}
}

func TestClaudeParser_Error(t *testing.T) {
	lines := loadFixture(t, "error.ndjson")
	p := claudeAdapter{}.NewParser()

	var resultCount int
	var resultEv Event
	for _, line := range lines {
		ev, ok := p.ParseLine(line)
		if !ok {
			continue
		}
		if ev.Kind == KindResult {
			resultCount++
			resultEv = ev
		}
	}

	if resultCount != 1 {
		t.Fatalf("KindResult count = %d, want exactly 1", resultCount)
	}
	if resultEv.Err == "" {
		t.Error("error fixture is_error=true, expected non-empty Err")
	}
	if resultEv.Usage == nil {
		t.Fatal("result event carries no usage even on error")
	}
}

func TestClaudeParser_NoiseTolerance(t *testing.T) {
	p := claudeAdapter{}.NewParser()

	garbageLines := [][]byte{
		[]byte(``),
		[]byte(`not json at all`),
		[]byte(`{"type":"some_future_event_kind","payload":123}`),
		[]byte(`{"type":"system","subtype":"hook_started","hook_id":"x"}`),
	}
	for _, line := range garbageLines {
		if _, ok := p.ParseLine(line); ok {
			t.Errorf("garbage line unexpectedly parsed as event: %s", line)
		}
	}

	valid := []byte(`{"type":"system","subtype":"init","model":"claude-haiku-4-5-20251001"}`)
	ev, ok := p.ParseLine(valid)
	if !ok {
		t.Fatal("valid system/init line failed to parse after noise")
	}
	if ev.Kind != KindSystem || ev.Text != "claude-haiku-4-5-20251001" || ev.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("unexpected event: %+v", ev)
	}

	stats := p.(ParserStats).Stats()
	if stats.Lines != len(garbageLines)+1 {
		t.Errorf("stats.Lines = %d, want %d", stats.Lines, len(garbageLines)+1)
	}
	if stats.Events != 1 {
		t.Errorf("stats.Events = %d, want 1", stats.Events)
	}
	// The empty line never reaches the JSON decode path, so it isn't
	// counted as Unknown - only the 3 non-empty garbage lines are.
	if stats.Unknown != 3 {
		t.Errorf("stats.Unknown = %d, want 3", stats.Unknown)
	}
}

func TestClaudeAdapter_BuildInvocation(t *testing.T) {
	a := claudeAdapter{}
	budget := 5.0

	tests := []struct {
		name    string
		spec    InvocationSpec
		wantErr bool
		check   func(t *testing.T, inv Invocation)
	}{
		{
			name: "subscription auth: no key env",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/p.txt",
				AuthMode: AuthSubscription,
			},
			check: func(t *testing.T, inv Invocation) {
				if _, ok := inv.Env["ANTHROPIC_API_KEY"]; ok {
					t.Error("subscription auth must not set ANTHROPIC_API_KEY")
				}
				if inv.Env["NO_COLOR"] != "1" {
					t.Error("NO_COLOR must always be set")
				}
				if inv.Env["NODE_OPTIONS"] != "--max-old-space-size=768" {
					t.Errorf("NODE_OPTIONS = %q, want --max-old-space-size=768 (D-056, bounds the CLI's node heap)", inv.Env["NODE_OPTIONS"])
				}
			},
		},
		{
			name: "api_key auth: sets key env",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/p.txt",
				AuthMode: AuthAPIKey, APIKey: "sk-test",
			},
			check: func(t *testing.T, inv Invocation) {
				if inv.Env["ANTHROPIC_API_KEY"] != "sk-test" {
					t.Error("api_key auth must set ANTHROPIC_API_KEY")
				}
			},
		},
		{
			name: "api_key auth with GLM base url",
			spec: InvocationSpec{
				Model: "glm-4.6", PromptPath: "/tmp/p.txt",
				AuthMode: AuthAPIKey, APIKey: "glm-key",
				BaseURL: "https://api.z.ai/api/anthropic",
			},
			check: func(t *testing.T, inv Invocation) {
				if inv.Env["ANTHROPIC_BASE_URL"] != "https://api.z.ai/api/anthropic" {
					t.Errorf("ANTHROPIC_BASE_URL = %q", inv.Env["ANTHROPIC_BASE_URL"])
				}
			},
		},
		{
			name: "subscription auth with base url is invalid",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/p.txt",
				AuthMode: AuthSubscription, BaseURL: "https://example.com",
			},
			wantErr: true,
		},
		{
			name: "budget flag only in api_key mode",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/p.txt",
				AuthMode: AuthAPIKey, APIKey: "sk-test", BudgetUSD: &budget,
			},
			check: func(t *testing.T, inv Invocation) {
				if !containsFlag(inv.Argv, "--max-budget-usd") {
					t.Error("expected --max-budget-usd in api_key mode with BudgetUSD set")
				}
			},
		},
		{
			name: "budget flag absent in subscription mode",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/p.txt",
				AuthMode: AuthSubscription, BudgetUSD: &budget,
			},
			check: func(t *testing.T, inv Invocation) {
				if containsFlag(inv.Argv, "--max-budget-usd") {
					t.Error("--max-budget-usd must not appear in subscription mode")
				}
			},
		},
		{
			name: "oauth_token auth: sets oauth env, no api key, no budget flag",
			spec: InvocationSpec{ //nolint:gosec // G101: fixture value, not a real credential.
				Model: "sonnet", PromptPath: "/tmp/p.txt",
				AuthMode: AuthOAuthToken, APIKey: "sk-ant-oat-test", BudgetUSD: &budget,
			},
			check: func(t *testing.T, inv Invocation) {
				if inv.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat-test" {
					t.Error("oauth_token auth must set CLAUDE_CODE_OAUTH_TOKEN")
				}
				if _, ok := inv.Env["ANTHROPIC_API_KEY"]; ok {
					t.Error("oauth_token auth must not set ANTHROPIC_API_KEY")
				}
				if containsFlag(inv.Argv, "--max-budget-usd") {
					t.Error("--max-budget-usd must not appear in oauth_token mode even with BudgetUSD set")
				}
			},
		},
		{
			name: "oauth_token auth with base url is invalid",
			spec: InvocationSpec{ //nolint:gosec // G101: fixture value, not a real credential.
				Model: "sonnet", PromptPath: "/tmp/p.txt",
				AuthMode: AuthOAuthToken, APIKey: "sk-ant-oat-test", BaseURL: "https://example.com",
			},
			wantErr: true,
		},
		{
			name:    "oauth_token auth without token is invalid",
			spec:    InvocationSpec{Model: "sonnet", PromptPath: "/tmp/p.txt", AuthMode: AuthOAuthToken},
			wantErr: true,
		},
		{
			name:    "empty model is invalid",
			spec:    InvocationSpec{PromptPath: "/tmp/p.txt", AuthMode: AuthSubscription},
			wantErr: true,
		},
		{
			name:    "empty prompt path is invalid",
			spec:    InvocationSpec{Model: "sonnet", AuthMode: AuthSubscription},
			wantErr: true,
		},
		{
			name:    "api_key mode without key is invalid",
			spec:    InvocationSpec{Model: "sonnet", PromptPath: "/tmp/p.txt", AuthMode: AuthAPIKey},
			wantErr: true,
		},
		{
			name: "allow/deny tools joined",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/p.txt", AuthMode: AuthSubscription,
				AllowTools: []string{"Write", "Read"}, DenyTools: []string{"Bash"},
			},
			check: func(t *testing.T, inv Invocation) {
				if !containsFlag(inv.Argv, "--allowedTools") {
					t.Error("expected --allowedTools")
				}
				if !containsFlag(inv.Argv, "--disallowedTools") {
					t.Error("expected --disallowedTools")
				}
			},
		},
		{
			name: "read-only forces the read-only allow/deny lists over the spec's own (issue #582)",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/p.txt", AuthMode: AuthSubscription,
				AllowTools: []string{"Write", "Bash"}, DenyTools: []string{"WebFetch"},
				ReadOnly: true,
			},
			check: func(t *testing.T, inv Invocation) {
				if !containsFlagValue(inv.Argv, "--allowedTools", "Read,Grep,Glob") {
					t.Errorf("argv %v missing --allowedTools Read,Grep,Glob", inv.Argv)
				}
				if !containsFlagValue(inv.Argv, "--disallowedTools", "Bash,Edit,Write,MultiEdit,NotebookEdit,WebFetch,WebSearch") {
					t.Errorf("argv %v missing the read-only --disallowedTools list", inv.Argv)
				}
			},
		},
		{
			name: "resume session id appends --resume flag (D-103, issue #499)",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/p.txt", AuthMode: AuthSubscription,
				ResumeSessionID: "sess-abc-123",
			},
			check: func(t *testing.T, inv Invocation) {
				if !containsFlagValue(inv.Argv, "--resume", "sess-abc-123") {
					t.Errorf("argv %v missing --resume sess-abc-123", inv.Argv)
				}
			},
		},
		{
			name: "empty resume session id omits --resume flag",
			spec: InvocationSpec{Model: "sonnet", PromptPath: "/tmp/p.txt", AuthMode: AuthSubscription},
			check: func(t *testing.T, inv Invocation) {
				if containsFlag(inv.Argv, "--resume") {
					t.Error("--resume must not appear without a ResumeSessionID")
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
			if tt.check != nil {
				tt.check(t, inv)
			}
		})
	}
}

func containsFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

// containsFlagValue reports whether argv has flag immediately followed
// by value: shared by every adapter's resume-flag test.
func containsFlagValue(argv []string, flag, value string) bool {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) && argv[i+1] == value {
			return true
		}
	}
	return false
}
