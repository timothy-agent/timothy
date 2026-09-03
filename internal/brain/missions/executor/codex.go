package executor

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// codexHarness names this adapter's registration key.
const codexHarness = "codex-cli"

// codexAdapter wires the OpenAI Codex CLI's headless json mode (verified
// against @openai/codex 0.147.0 fixtures in testdata/codex-0.147.0). codex
// speaks only the openai wire (its own responses API), api_key auth only.
type codexAdapter struct{}

func init() {
	Register(codexAdapter{})
}

func (codexAdapter) Harness() string { return codexHarness }

func (codexAdapter) Capabilities() Capabilities {
	//nolint:gosec // G101: env var name the runner injects into, not a credential value.
	return Capabilities{
		StructuredFinalOutput: true,
		ReportsTokens:         true,
		// codex's turn.completed usage carries no cost field - never
		// trusted as billed spend (D-013).
		ReportsCost: false,
		WireFormat:  "openai",
		APIKeyEnv:   "CODEX_API_KEY",
		// BaseURL rides config.toml (model_providers.timothy.base_url),
		// not an env var - codex has no base-url env override.
		BaseURLEnv: "",
		StateDirs:  nil,
		// `codex exec resume <SESSION_ID>` is documented (`codex exec
		// resume --help`); BuildInvocation switches to that subcommand
		// form when ResumeSessionID is set.
		SupportsResume: true,
	}
}

// codexDefaultBaseURL is used when spec.BaseURL is empty.
const codexDefaultBaseURL = "https://api.openai.com/v1"

// BuildInvocation validates spec and translates it to a codex CLI argv +
// env. The prompt never rides the argv directly - PromptFile names the
// path the runner substitutes via `$(cat PromptFile)` at spawn time, same
// mechanism as pi/claude. spec.AllowTools/DenyTools are ignored: codex has
// no per-tool deny surface we use - the per-mission sandbox container is
// the actual boundary (accepted risk, same rationale as pi). spec.BudgetUSD
// is ignored - codex has no budget flag.
func (codexAdapter) BuildInvocation(spec InvocationSpec) (Invocation, error) {
	if spec.Model == "" {
		return Invocation{}, fmt.Errorf("executor/codex: empty model")
	}
	if spec.PromptPath == "" {
		return Invocation{}, fmt.Errorf("executor/codex: empty prompt path")
	}
	if spec.AuthMode != AuthAPIKey {
		return Invocation{}, fmt.Errorf("executor/codex: auth mode %q not supported, api_key only", spec.AuthMode)
	}
	if spec.APIKey == "" {
		return Invocation{}, fmt.Errorf("executor/codex: api_key auth requires a key")
	}
	if spec.Wire != "openai" {
		return Invocation{}, fmt.Errorf("executor/codex: wire %q not supported, codex speaks openai only", spec.Wire)
	}

	baseURL := spec.BaseURL
	if baseURL == "" {
		baseURL = codexDefaultBaseURL
	}

	runDir := filepath.Dir(spec.PromptPath)
	codexHome := filepath.Join(runDir, "codex-home")

	files := map[string]string{
		"codex-home/config.toml": codexConfigTOML(baseURL),
	}

	// `codex exec resume <SESSION_ID>` is a distinct subcommand form
	// (confirmed via `codex exec resume --help`): no `-C` flag - the
	// resumed session keeps the working root recorded at its original
	// spawn - but the same --json/--output-schema/-m/bypass flags apply.
	var argv []string
	if spec.ResumeSessionID != "" {
		argv = []string{
			"codex", "exec", "resume", spec.ResumeSessionID, "--json",
			"--dangerously-bypass-approvals-and-sandbox",
			"--skip-git-repo-check",
			"-m", spec.Model,
		}
	} else {
		argv = []string{
			"codex", "exec", "--json",
			"-C", spec.Workdir,
			"--dangerously-bypass-approvals-and-sandbox",
			"--skip-git-repo-check",
			"-m", spec.Model,
		}
	}
	if spec.ResultSchema != nil {
		schemaPath := filepath.Join(codexHome, "schema.json")
		compact, err := compactJSON(spec.ResultSchema)
		if err != nil {
			return Invocation{}, fmt.Errorf("executor/codex: result schema: %w", err)
		}
		files["codex-home/schema.json"] = compact
		argv = append(argv, "--output-schema", schemaPath)
	}
	argv = append(argv, "@PROMPT@") // substituted by the runner via PromptFile

	// codex has no --append-system-prompt flag; CODEX_HOME/AGENTS.md is
	// read and merged into the model's instructions (confirmed against
	// codex's docs: "Custom instructions with AGENTS.md"), so
	// SystemAppend rides that file instead.
	if spec.SystemAppend != "" {
		files["codex-home/AGENTS.md"] = spec.SystemAppend
	}

	env := map[string]string{
		"NO_COLOR":      "1",
		"CODEX_API_KEY": spec.APIKey,
		"CODEX_HOME":    codexHome,
	}

	return Invocation{Argv: argv, Env: env, PromptFile: spec.PromptPath, Files: files}, nil
}

// codexConfigTOML builds CODEX_HOME/config.toml registering a custom
// "timothy" model provider pointed at baseURL, speaking the responses wire
// (the only one codex 0.147.0 accepts - wire_api="chat" is a hard config
// error). baseURL is TOML-escaped rather than string-templated blind,
// since it rides spec.BaseURL (provider-row-controlled, not literal-safe).
func codexConfigTOML(baseURL string) string {
	return fmt.Sprintf(`model_provider = "timothy"

[model_providers.timothy]
name = "Timothy"
base_url = %s
env_key = "CODEX_API_KEY"
wire_api = "responses"

[analytics]
enabled = false

[otel]
exporter = "none"
`, tomlQuote(baseURL))
}

// tomlQuote renders s as a TOML basic string: quotes and backslashes are
// escaped, and every control character (TOML basic strings forbid these
// literal - a raw newline would otherwise close the line and let a
// hostile baseURL forge new keys/tables) is escaped too.
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// codexParser is a fresh, stateful StreamParser per spawn. It remembers
// the last agent_message text as the terminal event's Text/Result source -
// turn.completed carries usage only, never the message text itself.
type codexParser struct {
	stats Stats

	lastAgentMessage string
}

func (codexAdapter) NewParser() StreamParser {
	return &codexParser{}
}

func (p *codexParser) Stats() Stats { return p.stats }

// codex wire types - decoded strictly per top-level "type", tolerating
// extra fields; unrecognized types map to ok=false.

type codexLineEnvelope struct {
	Type string `json:"type"`
}

type codexThreadStartedLine struct {
	ThreadID string `json:"thread_id"`
}

type codexItem struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Text    string `json:"text"`
}

type codexItemLine struct {
	Item codexItem `json:"item"`
}

type codexUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
}

type codexTurnCompletedLine struct {
	Usage codexUsage `json:"usage"`
}

type codexTurnFailedLine struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ParseLine implements StreamParser. ok=false means noise, never an error.
// codex exits 0 on success but its exit codes are unreliable upstream -
// turn.failed/top-level error are the only failure signals (README.md).
func (p *codexParser) ParseLine(line []byte) (Event, bool) {
	p.stats.Lines++
	line = codexTrimSpace(line)
	if len(line) == 0 {
		return Event{}, false
	}

	var env codexLineEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		p.stats.Unknown++
		return Event{}, false
	}

	switch env.Type {
	case "thread.started":
		var t codexThreadStartedLine
		if err := json.Unmarshal(line, &t); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return Event{Kind: KindSystem, Text: t.ThreadID, SessionID: t.ThreadID}, true

	case "item.started":
		var it codexItemLine
		if err := json.Unmarshal(line, &it); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		if it.Item.Type != "command_execution" {
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return Event{Kind: KindTool, Tool: &ToolActivity{
			Name:   "shell",
			Detail: truncate(it.Item.Command, 200),
			Status: "started",
		}}, true

	case "item.completed":
		var it codexItemLine
		if err := json.Unmarshal(line, &it); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		return p.handleItemCompleted(it.Item)

	case "turn.completed":
		var tc codexTurnCompletedLine
		if err := json.Unmarshal(line, &tc); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return p.buildResultEvent(tc.Usage), true

	case "turn.failed":
		var tf codexTurnFailedLine
		if err := json.Unmarshal(line, &tf); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return Event{Kind: KindResult, Err: tf.Error.Message}, true

	default:
		// turn.started, item.updated, top-level "error" (transient
		// reconnect noise unless it's the terminal signal, which arrives
		// as turn.failed instead - see README.md), and any future type -
		// all noise the harness never acts on.
		p.stats.Unknown++
		return Event{}, false
	}
}

// handleItemCompleted handles one item.completed line: command_execution
// yields a KindTool "finished" event; agent_message yields KindText and is
// remembered for the terminal turn.completed event's Text/Result. reasoning
// and item-level "error" (advisory, distinct from the top-level error
// event) are noise, as is any item type not yet observed (e.g. mcp_tool_call).
func (p *codexParser) handleItemCompleted(item codexItem) (Event, bool) {
	switch item.Type {
	case "command_execution":
		p.stats.Events++
		return Event{Kind: KindTool, Tool: &ToolActivity{
			Name:   "shell",
			Status: "finished",
		}}, true
	case "agent_message":
		p.lastAgentMessage = item.Text
		p.stats.Events++
		return Event{Kind: KindText, Text: item.Text}, true
	default:
		p.stats.Unknown++
		return Event{}, false
	}
}

// buildResultEvent builds the one terminal KindResult event a
// turn.completed line carries: usage from the line itself, text/verdict
// from the last agent_message item.completed line seen this turn.
func (p *codexParser) buildResultEvent(usage codexUsage) Event {
	ev := Event{
		Kind: KindResult,
		Text: p.lastAgentMessage,
		// CostUSD stays nil always - turn.completed.usage carries no cost
		// field (D-013, see Capabilities.ReportsCost).
		Usage: &Usage{
			InputTokens:      usage.InputTokens,
			OutputTokens:     usage.OutputTokens,
			CacheReadTokens:  usage.CachedInputTokens,
			CacheWriteTokens: usage.CacheWriteInputTokens,
		},
	}
	if raw, ok := extractTrailingJSONObject(p.lastAgentMessage); ok {
		ev.Result = raw
	}
	return ev
}

// ParseResult decodes a KindResult event's Result payload into DONE,
// RETRY, or BLOCKED. Any other value, or a payload that doesn't decode,
// returns ok=false. Semantics identical to claude's/pi's.
func (codexAdapter) ParseResult(ev Event) (Result, bool) {
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

func codexTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
