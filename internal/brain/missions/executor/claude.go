package executor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// claudeHarness names this adapter's registration key.
const claudeHarness = "claude-cli"

// claudeAdapter wires the claude CLI's headless stream-json mode (verified
// against claude-cli 2.1.223 fixtures in testdata/claude-2.1.223).
type claudeAdapter struct{}

func init() {
	Register(claudeAdapter{})
}

func (claudeAdapter) Harness() string { return claudeHarness }

func (claudeAdapter) Capabilities() Capabilities {
	//nolint:gosec // G101: env var NAMES the runner injects into, not credential values.
	return Capabilities{
		StructuredFinalOutput: true,
		ReportsTokens:         true,
		ReportsCost:           true,
		WireFormat:            "anthropic",
		APIKeyEnv:             "ANTHROPIC_API_KEY",
		BaseURLEnv:            "ANTHROPIC_BASE_URL",
		StateDirs:             []string{".claude"},
		OAuthTokenEnv:         "CLAUDE_CODE_OAUTH_TOKEN",
		OAuthTokenPrefix:      "sk-ant-oat",
		// claude-cli's `--resume <session-id>` is documented (`claude
		// --help`) and combines with `-p` for a headless resumed turn.
		SupportsResume: true,
	}
}

// BuildInvocation validates spec and translates it to a claude CLI argv +
// env. The prompt never rides the argv directly - PromptFile names the
// path the runner substitutes via `$(cat PromptFile)` at spawn time, since
// the exec API has no stdin and adapters stay shell-free.
func (claudeAdapter) BuildInvocation(spec InvocationSpec) (Invocation, error) {
	if spec.Model == "" {
		return Invocation{}, fmt.Errorf("executor/claude: empty model")
	}
	if spec.PromptPath == "" {
		return Invocation{}, fmt.Errorf("executor/claude: empty prompt path")
	}
	switch spec.AuthMode {
	case AuthAPIKey:
		if spec.APIKey == "" {
			return Invocation{}, fmt.Errorf("executor/claude: api_key auth requires a key")
		}
	case AuthSubscription:
		if spec.BaseURL != "" {
			return Invocation{}, fmt.Errorf("executor/claude: subscription auth cannot set a base url")
		}
	case AuthOAuthToken:
		if spec.APIKey == "" {
			return Invocation{}, fmt.Errorf("executor/claude: oauth_token auth requires a token")
		}
		if spec.BaseURL != "" {
			return Invocation{}, fmt.Errorf("executor/claude: oauth token bills the subscription, anthropic endpoint only - a custom base url means a mismatched provider row")
		}
	default:
		return Invocation{}, fmt.Errorf("executor/claude: unknown auth mode %q", spec.AuthMode)
	}

	argv := []string{
		"claude",
		"-p", "@PROMPT@", // substituted by the runner via PromptFile
		"--output-format", "stream-json",
		"--verbose",
		"--model", spec.Model,
		"--permission-mode", "dontAsk",
	}
	if len(spec.AllowTools) > 0 {
		argv = append(argv, "--allowedTools", strings.Join(spec.AllowTools, ","))
	}
	if len(spec.DenyTools) > 0 {
		argv = append(argv, "--disallowedTools", strings.Join(spec.DenyTools, ","))
	}
	if spec.BudgetUSD != nil && spec.AuthMode == AuthAPIKey {
		argv = append(argv, "--max-budget-usd", fmt.Sprintf("%v", *spec.BudgetUSD))
	}
	if spec.ResultSchema != nil {
		compact, err := compactJSON(spec.ResultSchema)
		if err != nil {
			return Invocation{}, fmt.Errorf("executor/claude: result schema: %w", err)
		}
		argv = append(argv, "--json-schema", compact)
	}
	if spec.SystemAppend != "" {
		argv = append(argv, "--append-system-prompt", spec.SystemAppend)
	}
	if spec.ResumeSessionID != "" {
		argv = append(argv, "--resume", spec.ResumeSessionID)
	}

	// nodeMaxOldSpaceMB (D-056) bounds the CLI's node heap: unbounded,
	// node sizes its heap from cgroup limits, and a long run's transcript
	// heap balloons toward the sandbox's 2 GiB cap.
	env := map[string]string{"NO_COLOR": "1", "NODE_OPTIONS": "--max-old-space-size=768"}
	if spec.AuthMode == AuthAPIKey {
		env["ANTHROPIC_API_KEY"] = spec.APIKey
	}
	if spec.AuthMode == AuthOAuthToken {
		env["CLAUDE_CODE_OAUTH_TOKEN"] = spec.APIKey
	}
	if spec.BaseURL != "" {
		env["ANTHROPIC_BASE_URL"] = spec.BaseURL
	}

	return Invocation{Argv: argv, Env: env, PromptFile: spec.PromptPath}, nil
}

// compactJSON re-marshals raw into its shortest form for the CLI arg.
func compactJSON(raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// claudeParser is a fresh, stateful StreamParser per spawn: it tracks
// tool_use id -> name so a later tool_result line can name the tool it
// answers.
type claudeParser struct {
	toolNames map[string]string
	stats     Stats
}

func (claudeAdapter) NewParser() StreamParser {
	return &claudeParser{toolNames: make(map[string]string)}
}

func (p *claudeParser) Stats() Stats { return p.stats }

// claude wire types - decoded strictly per top-level "type", tolerating
// extra fields; unrecognized types map to ok=false.

type claudeLineEnvelope struct {
	Type string `json:"type"`
}

type claudeSystemLine struct {
	Subtype   string `json:"subtype"`
	Model     string `json:"model"`
	SessionID string `json:"session_id"`
}

type claudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

type claudeAssistantLine struct {
	Message struct {
		Content []claudeContentBlock `json:"content"`
	} `json:"message"`
}

type claudeUserLine struct {
	Message struct {
		Content []claudeContentBlock `json:"content"`
	} `json:"message"`
}

type claudeResultLine struct {
	IsError          bool            `json:"is_error"`
	Subtype          string          `json:"subtype"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     *float64        `json:"total_cost_usd"`
	Usage            struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
	PermissionDenials []claudeDenial `json:"permission_denials"`
}

// claudeDenial is the plan-documented shape; never observed populated in
// recorded fixtures (StreamParser tolerates unexpected fields either way).
type claudeDenial struct {
	ToolName string `json:"tool_name"`
}

// ParseLine implements StreamParser. ok=false means noise, never an error.
func (p *claudeParser) ParseLine(line []byte) (Event, bool) {
	p.stats.Lines++
	line = trimSpace(line)
	if len(line) == 0 {
		return Event{}, false
	}

	var env claudeLineEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		p.stats.Unknown++
		return Event{}, false
	}

	switch env.Type {
	case "system":
		var sys claudeSystemLine
		if err := json.Unmarshal(line, &sys); err != nil || sys.Subtype != "init" {
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return Event{Kind: KindSystem, Text: sys.Model, Model: sys.Model, SessionID: sys.SessionID}, true

	case "assistant":
		var a claudeAssistantLine
		if err := json.Unmarshal(line, &a); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		return p.parseAssistantBlocks(a.Message.Content)

	case "user":
		var u claudeUserLine
		if err := json.Unmarshal(line, &u); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		return p.parseUserBlocks(u.Message.Content)

	case "result":
		var r claudeResultLine
		if err := json.Unmarshal(line, &r); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return p.buildResultEvent(r), true

	default:
		// system/hook_started, system/hook_response, rate_limit_event, and
		// any future type - all noise the harness never acts on.
		p.stats.Unknown++
		return Event{}, false
	}
}

// parseAssistantBlocks handles one assistant message's content blocks.
// Text blocks concatenate into one KindText event; a tool_use block
// (StructuredOutput aside - it carries the terminal verdict, surfaced via
// the result event's structured_output, not as a tool call) yields one
// KindTool "started" event. A message has one content array, so at most
// one Event returns per line; a message with both text and tool_use blocks
// yields the tool event (the text is transient status commentary the
// harness doesn't need to track separately).
func (p *claudeParser) parseAssistantBlocks(blocks []claudeContentBlock) (Event, bool) {
	var texts []string
	for _, b := range blocks {
		switch b.Type {
		case "tool_use":
			p.toolNames[b.ID] = b.Name
			p.stats.Events++
			return Event{Kind: KindTool, Tool: &ToolActivity{
				Name:   b.Name,
				Detail: truncate(string(b.Input), 200),
				Status: "started",
			}}, true
		case "text":
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

// parseUserBlocks handles a tool_result block, naming the tool from the
// id->name map populated by the matching tool_use. Plain text user blocks
// (synthetic enforcement nudges) are noise.
func (p *claudeParser) parseUserBlocks(blocks []claudeContentBlock) (Event, bool) {
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		name := p.toolNames[b.ToolUseID]
		p.stats.Events++
		return Event{Kind: KindTool, Tool: &ToolActivity{
			Name:   name,
			Status: "finished",
		}}, true
	}
	p.stats.Unknown++
	return Event{}, false
}

// buildResultEvent builds the one terminal KindResult event a fixture
// carries. Usage/cost sit on Usage; the structured verdict (when present)
// sits on Result for ParseResult to decode; denied tool names (if any)
// sit on Denials rather than as separate events, since a single line
// yields a single Event.
func (p *claudeParser) buildResultEvent(r claudeResultLine) Event {
	ev := Event{
		Kind: KindResult,
		Text: r.Result,
		Usage: &Usage{
			InputTokens:      r.Usage.InputTokens,
			OutputTokens:     r.Usage.OutputTokens,
			CacheReadTokens:  r.Usage.CacheReadInputTokens,
			CacheWriteTokens: r.Usage.CacheCreationInputTokens,
			CostUSD:          r.TotalCostUSD,
		},
	}
	if r.IsError {
		ev.Err = r.Result
	}
	if r.StructuredOutput != nil {
		ev.Result = r.StructuredOutput
	} else if r.Result != "" {
		var v any
		if json.Unmarshal([]byte(r.Result), &v) == nil {
			ev.Result = json.RawMessage(r.Result)
		}
	}
	for _, d := range r.PermissionDenials {
		if d.ToolName != "" {
			ev.Denials = append(ev.Denials, d.ToolName)
		}
	}
	return ev
}

// ParseResult decodes a KindResult event's Result payload into DONE,
// RETRY, or BLOCKED. Any other value, or a payload that doesn't decode,
// returns ok=false.
func (claudeAdapter) ParseResult(ev Event) (Result, bool) {
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func trimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
