package executor

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadPiFixture returns every non-empty line of a recorded pi fixture.
func loadPiFixture(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "pi-0.84.1", name)) //nolint:gosec // G304: fixed testdata path.
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

func TestPiParser_Happy(t *testing.T) {
	lines := loadPiFixture(t, "happy.ndjson")
	p := piAdapter{}.NewParser()

	want := []eventSummary{
		{kind: KindSystem, textHead: "/w"},
		{kind: KindTool, toolName: "write", status: "started"},
		{kind: KindTool, toolName: "write", status: "finished"},
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
		t.Error("pi never reports a trusted cost - CostUSD must stay nil")
	}
	if resultEv.Result == nil {
		t.Fatal("result event carries no structured Result payload")
	}

	res, ok := piAdapter{}.ParseResult(resultEv)
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
	if stats.Unknown != len(lines)-len(want) {
		t.Errorf("stats.Unknown = %d, want %d", stats.Unknown, len(lines)-len(want))
	}
}

func TestPiParser_Error(t *testing.T) {
	lines := loadPiFixture(t, "error.ndjson")
	p := piAdapter{}.NewParser()

	want := []eventSummary{
		{kind: KindSystem, textHead: "/w"},
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
	// CRITICAL: pi's json mode exits 0 on model errors - Err is the
	// only failure signal.
	if resultEv.Err == "" {
		t.Error("error fixture stopReason=error, expected non-empty Err")
	}
	if resultEv.Usage == nil {
		t.Fatal("result event carries no usage even on error")
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
	// 3 noise agent_end (willRetry=true), 4 assistant message_end with
	// no text content, 1 user message_end, 3 auto_retry_start, 1
	// auto_retry_end.
	if stats.Unknown != len(lines)-len(want) {
		t.Errorf("stats.Unknown = %d, want %d", stats.Unknown, len(lines)-len(want))
	}
}

func TestPiParser_NoVerdict(t *testing.T) {
	lines := loadPiFixture(t, "no-verdict.ndjson")
	p := piAdapter{}.NewParser()

	var resultCount int
	var resultEv Event
	var sawText bool
	for _, line := range lines {
		ev, ok := p.ParseLine(line)
		if !ok {
			continue
		}
		if ev.Kind == KindText {
			sawText = true
		}
		if ev.Kind == KindResult {
			resultCount++
			resultEv = ev
		}
	}

	if !sawText {
		t.Error("expected a KindText event for the assistant's prose reply")
	}
	if resultCount != 1 {
		t.Fatalf("KindResult count = %d, want exactly 1", resultCount)
	}
	if resultEv.Err != "" {
		t.Errorf("no-verdict fixture has no error, got Err=%q", resultEv.Err)
	}
	if resultEv.Result != nil {
		t.Errorf("no-verdict fixture has no trailing JSON, want nil Result, got %s", resultEv.Result)
	}
	if _, ok := (piAdapter{}).ParseResult(resultEv); ok {
		t.Error("ParseResult ok=true on a result with no verdict payload")
	}
}

func TestPiParser_NoiseTolerance(t *testing.T) {
	p := piAdapter{}.NewParser()

	garbageLines := [][]byte{
		[]byte(``),
		[]byte(`not json at all`),
		[]byte(`{"type":"some_future_event_kind","payload":123}`),
		[]byte(`{"type":"message_start","message":{"role":"assistant"}}`),
		[]byte(`{"type":"queue_update","steering":false,"followUp":false}`),
	}
	for _, line := range garbageLines {
		if _, ok := p.ParseLine(line); ok {
			t.Errorf("garbage line unexpectedly parsed as event: %s", line)
		}
	}

	valid := []byte(`{"type":"session","version":3,"id":"x","timestamp":"t","cwd":"/work"}`)
	ev, ok := p.ParseLine(valid)
	if !ok {
		t.Fatal("valid session line failed to parse after noise")
	}
	if ev.Kind != KindSystem || ev.Text != "/work" {
		t.Errorf("unexpected event: %+v", ev)
	}

	stats := p.(ParserStats).Stats()
	if stats.Lines != len(garbageLines)+1 {
		t.Errorf("stats.Lines = %d, want %d", stats.Lines, len(garbageLines)+1)
	}
	if stats.Events != 1 {
		t.Errorf("stats.Events = %d, want 1", stats.Events)
	}
	if stats.Unknown != 4 {
		t.Errorf("stats.Unknown = %d, want 4", stats.Unknown)
	}
}

// TestPiParser_AgentEndWillRetryIsNoise: an agent_end with willRetry
// true never yields a KindResult - pi is about to auto-retry, and the
// runner must keep waiting rather than treat this as terminal.
func TestPiParser_AgentEndWillRetryIsNoise(t *testing.T) {
	p := piAdapter{}.NewParser()
	line := []byte(`{"type":"agent_end","messages":[],"willRetry":true}`)
	if _, ok := p.ParseLine(line); ok {
		t.Error("agent_end with willRetry=true must be noise, not a terminal event")
	}
}

// TestPiParser_RPCModeIgnoresResponseAndQueueUpdate pins that rpc
// mode's extra line types (issue #358) never surface as events, while
// the rest of the stream (tool activity, terminal result) still parses
// exactly as it does in json mode.
func TestPiParser_RPCModeIgnoresResponseAndQueueUpdate(t *testing.T) {
	lines := loadPiFixture(t, "rpc.ndjson")
	p := piAdapter{}.NewParser()

	want := []eventSummary{
		{kind: KindSystem, textHead: "/w"},
		{kind: KindTool, toolName: "write", status: "started"},
		{kind: KindTool, toolName: "write", status: "finished"},
		{kind: KindText, textHead: "{\"status\":\"DONE\",\"no"},
		{kind: KindResult, textHead: "{\"status\":\"DONE\",\"no"},
	}

	var got []eventSummary
	var resultCount int
	for _, line := range lines {
		ev, ok := p.ParseLine(line)
		if !ok {
			continue
		}
		got = append(got, summarize(ev))
		if ev.Kind == KindResult {
			resultCount++
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

	stats := p.(ParserStats).Stats()
	if stats.Unknown != len(lines)-len(want) {
		t.Errorf("stats.Unknown = %d, want %d", stats.Unknown, len(lines)-len(want))
	}
}

// TestPiParser_ReviewVerdictLine covers issue #582: a run whose final
// message ends with a review_verdict-shaped JSON line lands that line
// on Event.Result verbatim (the missions package decodes it), while
// ParseResult, which only knows DONE/RETRY/BLOCKED, reports ok=false.
func TestPiParser_ReviewVerdictLine(t *testing.T) {
	tests := []struct {
		fixture      string
		wantDecision string
		wantFindings int
	}{
		{"review-approve.ndjson", "approve", 0},
		{"review-rework.ndjson", "rework", 1},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			p := piAdapter{}.NewParser()
			var result Event
			var results int
			for _, line := range loadPiFixture(t, tt.fixture) {
				ev, ok := p.ParseLine(line)
				if ok && ev.Kind == KindResult {
					results++
					result = ev
				}
			}
			if results != 1 {
				t.Fatalf("KindResult count = %d, want exactly 1", results)
			}
			if result.Result == nil {
				t.Fatalf("Result = nil, want the trailing verdict line; text was %q", result.Text)
			}
			var v struct {
				Decision string           `json:"decision"`
				Findings []map[string]any `json:"findings"`
			}
			if err := json.Unmarshal(result.Result, &v); err != nil {
				t.Fatalf("Result does not decode: %v", err)
			}
			if v.Decision != tt.wantDecision || len(v.Findings) != tt.wantFindings {
				t.Errorf("decision/findings = %q/%d, want %q/%d", v.Decision, len(v.Findings), tt.wantDecision, tt.wantFindings)
			}
			if _, ok := (piAdapter{}).ParseResult(result); ok {
				t.Error("ParseResult accepted a review verdict as a worker status")
			}
		})
	}
}

// TestPiAdapter_ImplementsSteerer pins that piAdapter satisfies
// executor.Steerer (issue #358): the delegated runner type-asserts
// adapters against this interface to decide whether mid-run steering
// is even possible for a harness.
func TestPiAdapter_ImplementsSteerer(t *testing.T) {
	var _ Steerer = piAdapter{}
}

func TestPiAdapter_PromptCommand(t *testing.T) {
	a := piAdapter{}
	got := a.PromptCommand("do the thing")
	want := `{"type":"prompt","message":"do the thing"}`
	if got != want {
		t.Errorf("PromptCommand = %s, want %s", got, want)
	}
}

func TestPiAdapter_SteerCommand(t *testing.T) {
	a := piAdapter{}
	got := a.SteerCommand("focus on staging next")
	want := `{"type":"steer","message":"focus on staging next"}`
	if got != want {
		t.Errorf("SteerCommand = %s, want %s", got, want)
	}
}

// TestPiAdapter_CommandsEscapeQuotesAndNewlines pins that
// PromptCommand/SteerCommand run their message through json.Marshal,
// not string concatenation - a note carrying quotes or newlines (an
// operator steering note is free text) must still produce one valid
// JSON line.
func TestPiAdapter_CommandsEscapeQuotesAndNewlines(t *testing.T) {
	a := piAdapter{}
	note := "she said \"hurry up\"\nand then left"
	for _, cmd := range []string{a.PromptCommand(note), a.SteerCommand(note)} {
		var decoded struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(cmd), &decoded); err != nil {
			t.Fatalf("command %q does not decode as JSON: %v", cmd, err)
		}
		if decoded.Message != note {
			t.Errorf("decoded message = %q, want %q", decoded.Message, note)
		}
		if strings.Contains(cmd, "\n") {
			t.Errorf("command %q must be a single line, embedded newline must be escaped", cmd)
		}
	}
}

func TestPiAdapter_BuildInvocation(t *testing.T) {
	a := piAdapter{}

	tests := []struct {
		name    string
		spec    InvocationSpec
		wantErr bool
		check   func(t *testing.T, inv Invocation)
	}{
		{
			name: "anthropic wire with base url",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "anthropic",
				BaseURL: "https://proxy.example.com",
			},
			check: func(t *testing.T, inv Invocation) {
				if inv.Env["PI_API_KEY"] != "sk-test" {
					t.Error("PI_API_KEY must be set from spec.APIKey")
				}
				if inv.Env["PI_CODING_AGENT_DIR"] != "/tmp/run/pi-agent" {
					t.Errorf("PI_CODING_AGENT_DIR = %q, want /tmp/run/pi-agent", inv.Env["PI_CODING_AGENT_DIR"])
				}
				assertPiModelsJSON(t, inv, "https://proxy.example.com", "anthropic-messages")
			},
		},
		{
			name: "anthropic wire with empty base url defaults to api.anthropic.com",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "anthropic",
			},
			check: func(t *testing.T, inv Invocation) {
				assertPiModelsJSON(t, inv, piDefaultAnthropicBaseURL, "anthropic-messages")
			},
		},
		{
			name: "openai wire with base url",
			spec: InvocationSpec{
				Model: "qwen3:30b-a3b", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "ollama-key", Wire: "openai",
				BaseURL: "http://host.docker.internal:11434/v1",
			},
			check: func(t *testing.T, inv Invocation) {
				assertPiModelsJSON(t, inv, "http://host.docker.internal:11434/v1", "openai-completions")
			},
		},
		{
			name: "openai wire without base url is invalid",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "openai",
			},
			wantErr: true,
		},
		{
			name: "unknown wire is invalid",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "carrier-pigeon",
			},
			wantErr: true,
		},
		{
			name: "subscription auth rejected",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthSubscription, Wire: "anthropic",
			},
			wantErr: true,
		},
		{
			name: "oauth_token auth rejected",
			spec: InvocationSpec{ //nolint:gosec // G101: fixture value, not a real credential.
				Model: "sonnet", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthOAuthToken, APIKey: "sk-ant-oat-test", Wire: "anthropic",
			},
			wantErr: true,
		},
		{
			name:    "api_key mode without key is invalid",
			spec:    InvocationSpec{Model: "sonnet", PromptPath: "/tmp/run/prompt.md", AuthMode: AuthAPIKey, Wire: "anthropic"},
			wantErr: true,
		},
		{
			name:    "empty model is invalid",
			spec:    InvocationSpec{PromptPath: "/tmp/run/prompt.md", AuthMode: AuthAPIKey, APIKey: "sk", Wire: "anthropic"},
			wantErr: true,
		},
		{
			name:    "empty prompt path is invalid",
			spec:    InvocationSpec{Model: "sonnet", AuthMode: AuthAPIKey, APIKey: "sk", Wire: "anthropic"},
			wantErr: true,
		},
		{
			name: "allow/deny tools and budget are ignored",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "anthropic",
				AllowTools: []string{"Write"}, DenyTools: []string{"Bash(git push:*)"},
				BudgetUSD: floatPtr(5.0),
			},
			check: func(t *testing.T, inv Invocation) {
				if containsFlag(inv.Argv, "--allowedTools") || containsFlag(inv.Argv, "--disallowedTools") {
					t.Error("pi has no allow/deny tool flags, must not appear in argv")
				}
				if containsFlag(inv.Argv, "--max-budget-usd") {
					t.Error("pi has no budget flag, must not appear in argv")
				}
			},
		},
		{
			name: "read-only narrows --tools to the read-only list (issue #582)",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "anthropic",
				ReadOnly: true,
			},
			check: func(t *testing.T, inv Invocation) {
				if !containsFlagValue(inv.Argv, "--tools", piReadOnlyTools) {
					t.Errorf("argv %v missing --tools %s", inv.Argv, piReadOnlyTools)
				}
				for _, a := range inv.Argv {
					if strings.Contains(a, "bash") || strings.Contains(a, "edit") || strings.Contains(a, "write") {
						t.Errorf("read-only argv element %q still names a writing tool", a)
					}
				}
			},
		},
		{
			name: "result schema swaps the sentinel for the schema-aware instruction (issue #582)",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "anthropic",
				SystemAppend: "judge it",
				ResultSchema: json.RawMessage(`{"type": "object", "properties": {"decision": {"type": "string"}}}`),
			},
			check: func(t *testing.T, inv Invocation) {
				idx := flagIndex(inv.Argv, "--append-system-prompt")
				if idx == -1 {
					t.Fatalf("argv %v missing --append-system-prompt", inv.Argv)
				}
				want := "judge it" + piSchemaInstruction + `{"properties":{"decision":{"type":"string"}},"type":"object"}`
				if got := inv.Argv[idx+1]; got != want {
					t.Errorf("system append = %q, want %q", got, want)
				}
				if strings.Contains(inv.Argv[idx+1], "DONE") {
					t.Error("schema-aware instruction must not mention the DONE/RETRY/BLOCKED sentinel")
				}
			},
		},
		{
			name: "argv exact",
			spec: InvocationSpec{
				Model: "sonnet", PromptPath: "/tmp/run/prompt.md",
				AuthMode: AuthAPIKey, APIKey: "sk-test", Wire: "anthropic",
				SystemAppend: "be nice",
			},
			check: func(t *testing.T, inv Invocation) {
				want := []string{
					"pi",
					"--mode", "rpc",
					"--no-extensions",
					"--no-skills",
					"--no-prompt-templates",
					"--no-themes",
					"--no-context-files",
					"--no-session",
					"--no-approve",
					"--tools", "read,bash,edit,write,grep,find,ls",
					"--model", "timothy/sonnet",
					"--append-system-prompt", "be nice" + piVerdictInstruction,
				}
				if len(inv.Argv) != len(want) {
					t.Fatalf("argv = %v, want %v", inv.Argv, want)
				}
				for i := range want {
					if inv.Argv[i] != want[i] {
						t.Errorf("argv[%d] = %q, want %q", i, inv.Argv[i], want[i])
					}
				}
				for _, a := range inv.Argv {
					if a == "@PROMPT@" {
						t.Fatal("rpc mode must not carry @PROMPT@ on argv, the prompt rides stdin")
					}
				}
				if inv.PromptFile == "" {
					t.Error("PromptFile must still be set so the runner writes prompt.md for the run dir record")
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
			if inv.Env["NODE_OPTIONS"] != "--max-old-space-size=768" {
				t.Errorf("NODE_OPTIONS = %q, want --max-old-space-size=768", inv.Env["NODE_OPTIONS"])
			}
			if inv.Env["PI_OFFLINE"] != "1" || inv.Env["PI_SKIP_VERSION_CHECK"] != "1" || inv.Env["PI_TELEMETRY"] != "0" {
				t.Errorf("offline/telemetry env not set as expected: %+v", inv.Env)
			}
			if tt.check != nil {
				tt.check(t, inv)
			}
		})
	}
}

func floatPtr(f float64) *float64 { return &f }

// assertPiModelsJSON decodes the models.json Files entry and checks it
// matches the expected baseUrl/api against spec.Model.
func assertPiModelsJSON(t *testing.T, inv Invocation, wantBaseURL, wantAPI string) {
	t.Helper()
	raw, ok := inv.Files["pi-agent/models.json"]
	if !ok {
		t.Fatal("Files missing pi-agent/models.json")
	}
	var cfg piModelsConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("models.json does not decode: %v", err)
	}
	prov, ok := cfg.Providers["timothy"]
	if !ok {
		t.Fatal("models.json missing providers.timothy")
	}
	if prov.BaseURL != wantBaseURL {
		t.Errorf("baseUrl = %q, want %q", prov.BaseURL, wantBaseURL)
	}
	if prov.API != wantAPI {
		t.Errorf("api = %q, want %q", prov.API, wantAPI)
	}
	if prov.APIKey != "$PI_API_KEY" {
		t.Errorf("apiKey = %q, want $PI_API_KEY", prov.APIKey)
	}
	if len(prov.Models) != 1 {
		t.Fatalf("models = %+v, want exactly 1", prov.Models)
	}
}
