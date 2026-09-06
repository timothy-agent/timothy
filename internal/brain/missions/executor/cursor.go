package executor

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// cursorHarness names this adapter's registration key.
const cursorHarness = "cursor-cli"

// cursorAdapter wires the Cursor CLI's headless stream-json mode
// (verified against cursor-agent 2026.08.11 fixtures in
// testdata/cursor-2026.08.11). cursor-agent is subscription-auth only
// (login), like claude-cli - it never routes through the gateway wire.
type cursorAdapter struct{}

func init() {
	Register(cursorAdapter{})
}

func (cursorAdapter) Harness() string { return cursorHarness }

func (cursorAdapter) Capabilities() Capabilities {
	//nolint:gosec // G101: env var name the runner injects into, not a credential value.
	return Capabilities{
		// no --output-schema flag; the terminal result's text carries a
		// prompt-instructed sentinel line instead, same as pi/opencode.
		StructuredFinalOutput: false,
		ReportsTokens:         true,
		// terminal result carries no cost field - never guessed (D-013).
		ReportsCost: false,
		WireFormat:  "anthropic",
		APIKeyEnv:   "CURSOR_API_KEY",
		BaseURLEnv:  "",
		StateDirs:   []string{".cursor"},
		// No OAuthTokenEnv/OAuthTokenPrefix: CURSOR_API_KEY is a
		// subscription-billed credential (login auth, same billing
		// posture as claude-cli's CLAUDE_CODE_OAUTH_TOKEN), but with no
		// known distinguishing value prefix to classify on - it resolves
		// as AuthAPIKey regardless (resolveCredential, delegated.go),
		// which BuildInvocation already requires.
		// cursor-agent's `--resume [chatId]` is documented (`cursor-agent
		// --help`) and combines with `-p`.
		SupportsResume: true,
	}
}

// cursorVerdictInstruction is appended to the prompt so the agent's
// final message ends with the DONE/RETRY/BLOCKED verdict line -
// cursor-agent has no --json-schema flag, so this sentence is the only
// verdict channel (spec.ResultSchema is intentionally never passed to
// cursor's argv or config).
const cursorVerdictInstruction = "End your final message with a single line containing only a JSON object of the form {\"status\":\"DONE\"|\"RETRY\"|\"BLOCKED\",\"note\":\"...\"} and nothing after it."

// BuildInvocation validates spec and translates it to a cursor-agent
// CLI argv + env. The prompt never rides the argv directly - PromptFile
// names the path the runner substitutes via `$(cat PromptFile)` at
// spawn time, same mechanism as the other adapters. cursor-agent has no
// -C/chdir flag, so the runner spawning in the run dir is the only
// working-dir mechanism (same as pi/opencode). spec.BudgetUSD is
// ignored - cursor-agent has no budget flag.
func (cursorAdapter) BuildInvocation(spec InvocationSpec) (Invocation, error) {
	if spec.Model == "" {
		return Invocation{}, fmt.Errorf("executor/cursor: empty model")
	}
	if spec.PromptPath == "" {
		return Invocation{}, fmt.Errorf("executor/cursor: empty prompt path")
	}
	if spec.AuthMode != AuthAPIKey {
		return Invocation{}, fmt.Errorf("executor/cursor: auth mode %q not supported, api_key only", spec.AuthMode)
	}
	if spec.APIKey == "" {
		return Invocation{}, fmt.Errorf("executor/cursor: api_key auth requires a key")
	}
	if spec.BaseURL != "" {
		return Invocation{}, fmt.Errorf("executor/cursor: cursor-agent has no custom endpoint support, base url must be empty")
	}
	if spec.ReadOnly {
		// issue #582: cursor-agent has no CLI-enforced read-only mode.
		return Invocation{}, fmt.Errorf("executor/cursor: %w", ErrReadOnlyUnsupported)
	}

	runDir := filepath.Dir(spec.PromptPath)
	configDir := filepath.Join(runDir, "cursor-home")

	config, err := json.Marshal(cursorConfig{
		Permissions: cursorPermissions{
			Allow: spec.AllowTools,
			Deny:  spec.DenyTools,
		},
		// repo bans AI attribution in commits/PRs (CLAUDE.md); cursor-agent
		// defaults both flags to true.
		Attribution: cursorAttribution{
			AttributeCommitsToAgent: false,
			AttributePRsToAgent:     false,
		},
	})
	if err != nil {
		return Invocation{}, fmt.Errorf("executor/cursor: marshal config: %w", err)
	}

	// cursor-agent has no --append-system-prompt-equivalent flag and no
	// auto-read instructions file (unlike codex's AGENTS.md / opencode's
	// instructions array), and the runner's "@PROMPT@" sentinel must be
	// its own whole argv element (delegated.go's renderArgv does an
	// exact-string match, never a substring replace) - the prompt file
	// itself is written by the runner after BuildInvocation runs, so the
	// adapter can't touch its content either. SystemAppend + the verdict
	// instruction therefore ride a separate argv element immediately
	// before "@PROMPT@": cursor-agent's free-text positional args join
	// with a space, so this reads as one prompt exactly as if the two
	// pieces had been concatenated with "\n\n".
	argv := []string{
		"cursor-agent",
		"-p",
		"--force",
		"--output-format", "stream-json",
		"--trust",
		"--model", spec.Model,
	}
	if spec.SystemAppend != "" {
		argv = append(argv, spec.SystemAppend)
	}
	if spec.ResumeSessionID != "" {
		argv = append(argv, "--resume", spec.ResumeSessionID)
	}
	argv = append(argv, "@PROMPT@", cursorVerdictInstruction)

	env := map[string]string{
		"NO_COLOR":                   "1",
		"CURSOR_API_KEY":             spec.APIKey,
		"CURSOR_CONFIG_DIR":          configDir,
		"AGENT_CLI_CREDENTIAL_STORE": "file",
	}

	return Invocation{
		Argv:       argv,
		Env:        env,
		PromptFile: spec.PromptPath,
		Files:      map[string]string{"cursor-home/cli-config.json": string(config)},
	}, nil
}

// cursorConfig/cursorPermissions/cursorAttribution mirror cursor-agent's
// CURSOR_CONFIG_DIR/cli-config.json shape - marshaled properly rather
// than string-templated, since allow/deny tool names ride spec-
// controlled values.
type cursorConfig struct {
	Permissions cursorPermissions `json:"permissions"`
	Attribution cursorAttribution `json:"attribution"`
}

type cursorPermissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

type cursorAttribution struct {
	AttributeCommitsToAgent bool `json:"attributeCommitsToAgent"`
	AttributePRsToAgent     bool `json:"attributePRsToAgent"`
}

// cursorParser is a fresh, stateful StreamParser per spawn. It tracks
// call_id -> tool name from tool_call/started lines so the matching
// completed line can name the tool it answers.
type cursorParser struct {
	stats Stats

	toolNames map[string]string
}

func (cursorAdapter) NewParser() StreamParser {
	return &cursorParser{toolNames: make(map[string]string)}
}

func (p *cursorParser) Stats() Stats { return p.stats }

// cursor wire types - decoded strictly per top-level "type", tolerating
// extra fields; unrecognized types map to ok=false.

type cursorLineEnvelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
}

type cursorSystemLine struct {
	Model     string `json:"model"`
	SessionID string `json:"session_id"`
}

type cursorAssistantBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type cursorAssistantLine struct {
	Message struct {
		Content []cursorAssistantBlock `json:"content"`
	} `json:"message"`
}

// cursorToolCall carries whichever variant key the CLI populated -
// editToolCall for file edits, shellToolCall for shell commands. Only
// the presence of the key matters for naming; Args are not decoded
// further, RawMessage covers whatever shape a variant carries.
type cursorToolCall struct {
	EditToolCall  json.RawMessage `json:"editToolCall"`
	ShellToolCall json.RawMessage `json:"shellToolCall"`
}

func (c cursorToolCall) name() string {
	switch {
	case c.EditToolCall != nil:
		return "edit"
	case c.ShellToolCall != nil:
		return "shell"
	default:
		return "unknown"
	}
}

type cursorToolCallLine struct {
	CallID   string         `json:"call_id"`
	ToolCall cursorToolCall `json:"tool_call"`
}

type cursorUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

type cursorResultLine struct {
	IsError bool        `json:"is_error"`
	Result  string      `json:"result"`
	Usage   cursorUsage `json:"usage"`
}

// ParseLine implements StreamParser. ok=false means noise, never an
// error. cursor-agent's exit code is unreliable on success per the
// harness's own known bug - a terminal "result" event is the only
// success signal, matching claude/pi/codex's failure-signal precedent.
func (p *cursorParser) ParseLine(line []byte) (Event, bool) {
	p.stats.Lines++
	line = cursorTrimSpace(line)
	if len(line) == 0 {
		return Event{}, false
	}

	var env cursorLineEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		p.stats.Unknown++
		return Event{}, false
	}

	switch env.Type {
	case "system":
		if env.Subtype != "init" {
			p.stats.Unknown++
			return Event{}, false
		}
		var sys cursorSystemLine
		if err := json.Unmarshal(line, &sys); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return Event{Kind: KindSystem, Text: sys.Model, Model: sys.Model, SessionID: sys.SessionID}, true

	case "assistant":
		var a cursorAssistantLine
		if err := json.Unmarshal(line, &a); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		return p.parseAssistantBlocks(a.Message.Content)

	case "tool_call":
		var t cursorToolCallLine
		if err := json.Unmarshal(line, &t); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		return p.handleToolCall(env.Subtype, t)

	case "result":
		var r cursorResultLine
		if err := json.Unmarshal(line, &r); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return p.buildResultEvent(r), true

	default:
		// "user" (echoed prompt) and "thinking" (delta/completed) - noise
		// the harness never acts on.
		p.stats.Unknown++
		return Event{}, false
	}
}

// parseAssistantBlocks concatenates one assistant message's text blocks
// into a KindText event, same shape as claude's.
func (p *cursorParser) parseAssistantBlocks(blocks []cursorAssistantBlock) (Event, bool) {
	var texts []string
	for _, b := range blocks {
		if b.Type == "text" {
			texts = append(texts, b.Text)
		}
	}
	if len(texts) == 0 {
		p.stats.Unknown++
		return Event{}, false
	}
	p.stats.Events++
	return Event{Kind: KindText, Text: strings.Join(texts, "")}, true
}

// handleToolCall handles one tool_call line: "started" remembers the
// call's name against call_id and yields a KindTool "started" event;
// "completed" looks that name back up (falling back to the completed
// line's own variant key, in case a resume ever sees completed without
// started).
func (p *cursorParser) handleToolCall(subtype string, t cursorToolCallLine) (Event, bool) {
	name := t.ToolCall.name()
	switch subtype {
	case "started":
		p.toolNames[t.CallID] = name
		p.stats.Events++
		return Event{Kind: KindTool, Tool: &ToolActivity{Name: name, Status: "started"}}, true
	case "completed":
		if known, ok := p.toolNames[t.CallID]; ok {
			name = known
		}
		p.stats.Events++
		return Event{Kind: KindTool, Tool: &ToolActivity{Name: name, Status: "finished"}}, true
	default:
		p.stats.Unknown++
		return Event{}, false
	}
}

// buildResultEvent builds the one terminal KindResult event a fixture
// carries: usage and error come straight off the line, the verdict
// rides Event.Result via extractTrailingJSONObject over the result
// text - cursor-agent has no --json-schema flag.
func (p *cursorParser) buildResultEvent(r cursorResultLine) Event {
	ev := Event{
		Kind: KindResult,
		Text: r.Result,
		Usage: &Usage{
			InputTokens:      r.Usage.InputTokens,
			OutputTokens:     r.Usage.OutputTokens,
			CacheReadTokens:  r.Usage.CacheReadTokens,
			CacheWriteTokens: r.Usage.CacheWriteTokens,
		},
	}
	if r.IsError {
		ev.Err = r.Result
	}
	if raw, ok := extractTrailingJSONObject(r.Result); ok {
		ev.Result = raw
	}
	return ev
}

// ParseResult decodes a KindResult event's Result payload into DONE,
// RETRY, or BLOCKED. Any other value, or a payload that doesn't decode,
// returns ok=false. Semantics identical to the other adapters.
func (cursorAdapter) ParseResult(ev Event) (Result, bool) {
	if ev.Kind != KindResult || ev.Result == nil {
		return Result{}, false
	}
	var v struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := json.Unmarshal(ev.Result, &v); err != nil {
		return Result{}, false
	}
	status := strings.ToUpper(v.Status)
	switch status {
	case "DONE", "RETRY", "BLOCKED":
		return Result{Status: status, Note: v.Note}, true
	default:
		return Result{}, false
	}
}

func cursorTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
