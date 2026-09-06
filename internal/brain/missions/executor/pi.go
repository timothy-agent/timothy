package executor

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// piHarness names this adapter's registration key.
const piHarness = "pi"

// piAdapter wires the pi coding agent's headless json mode (verified
// against pi-coding-agent 0.84.1 fixtures in testdata/pi-0.84.1). pi
// supports both the anthropic and openai-compatible wire formats
// (Capabilities().WireFormat names only the default; BuildInvocation
// picks the actual one from spec.Wire).
type piAdapter struct{}

func init() {
	Register(piAdapter{})
}

func (piAdapter) Harness() string { return piHarness }

func (piAdapter) Capabilities() Capabilities {
	//nolint:gosec // G101: env var NAME the runner injects into, not a credential value.
	return Capabilities{
		// pi has no CLI-enforced structured output (no --json-schema);
		// the trailing verdict JSON rides Event.Result via the prompt-
		// instructed sentinel line ParseLine extracts, and the runner's
		// finish calls ParseResult ungated regardless of this flag.
		StructuredFinalOutput: false,
		ReportsTokens:         true,
		// pi prices runs client-side from its own model catalog — never
		// trusted as billed spend (D-013), regardless of which driver it
		// actually ran against.
		ReportsCost: false,
		WireFormat:  "anthropic", // informational; pi also does openai (see BuildInvocation)
		APIKeyEnv:   "PI_API_KEY",
		BaseURLEnv:  "",
		StateDirs:   nil,
	}
}

// piDefaultAnthropicBaseURL is used when spec.Wire == "anthropic" and
// spec.BaseURL is empty — pi's models.json requires an explicit
// baseUrl even for the vendor's own default endpoint.
const piDefaultAnthropicBaseURL = "https://api.anthropic.com"

// piVerdictInstruction is appended to the system prompt so the agent's
// final message ends with the DONE/RETRY/BLOCKED verdict line — pi has
// no --json-schema flag, so this sentence is the only verdict channel
// (spec.ResultSchema is intentionally never passed to pi's argv or env).
const piVerdictInstruction = " End your final message with a single line containing only a JSON object of the form {\"status\":\"DONE\"|\"RETRY\"|\"BLOCKED\",\"note\":\"...\"} and nothing after it."

// BuildInvocation validates spec and translates it to a pi CLI argv +
// env. The prompt never rides the argv directly - PromptFile names the
// path the runner substitutes via `$(cat PromptFile)` at spawn time,
// same mechanism as claude. spec.AllowTools/DenyTools (claude tool
// names) are ignored: pi's builtin tool surface (read/bash/edit/write/
// grep/find/ls) has no web tools and no way to deny a specific bash
// subcommand like "git push" - the per-mission sandbox container and
// worktree are the actual boundary here (accepted risk). spec.BudgetUSD
// is ignored - pi has no budget flag.
func (piAdapter) BuildInvocation(spec InvocationSpec) (Invocation, error) {
	if spec.Model == "" {
		return Invocation{}, fmt.Errorf("executor/pi: empty model")
	}
	if spec.PromptPath == "" {
		return Invocation{}, fmt.Errorf("executor/pi: empty prompt path")
	}
	if spec.AuthMode != AuthAPIKey {
		return Invocation{}, fmt.Errorf("executor/pi: auth mode %q not supported, api_key only", spec.AuthMode)
	}
	if spec.APIKey == "" {
		return Invocation{}, fmt.Errorf("executor/pi: api_key auth requires a key")
	}

	var api, baseURL string
	switch spec.Wire {
	case "anthropic":
		api = "anthropic-messages"
		baseURL = spec.BaseURL
		if baseURL == "" {
			baseURL = piDefaultAnthropicBaseURL
		}
	case "openai":
		api = "openai-completions"
		if spec.BaseURL == "" {
			return Invocation{}, fmt.Errorf("executor/pi: openai wire requires a base url")
		}
		baseURL = spec.BaseURL
	default:
		return Invocation{}, fmt.Errorf("executor/pi: unknown wire %q", spec.Wire)
	}

	system := spec.SystemAppend + piVerdictInstruction

	// issue #358: --mode rpc replaces --mode json. rpc mode takes the
	// prompt as a JSON line on stdin (see PromptCommand), not on argv,
	// so the @PROMPT@ placeholder is dropped; the runner still writes
	// PromptFile to disk for the run dir's own record.
	argv := []string{
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
		"--model", "timothy/" + spec.Model,
		"--append-system-prompt", system,
	}

	agentDir := filepath.Join(filepath.Dir(spec.PromptPath), "pi-agent")
	env := map[string]string{
		"NO_COLOR": "1",
		// nodeMaxOldSpaceMB (D-056, same rationale as claude): bounds the
		// CLI's node heap so a long run's transcript doesn't balloon
		// toward the sandbox's 2 GiB cap.
		"NODE_OPTIONS":          "--max-old-space-size=768",
		"PI_CODING_AGENT_DIR":   agentDir,
		"PI_OFFLINE":            "1",
		"PI_SKIP_VERSION_CHECK": "1",
		"PI_TELEMETRY":          "0",
		"PI_API_KEY":            spec.APIKey,
	}

	models, err := json.Marshal(piModelsConfig{
		Providers: map[string]piProviderConfig{
			"timothy": { //nolint:gosec // G101: apiKey below is pi's own $VAR env-interpolation syntax (docs/models.md), not a credential literal.
				BaseURL: baseURL,
				API:     api,
				APIKey:  "$PI_API_KEY",
				Models:  []piModelConfig{{ID: spec.Model}},
			},
		},
	})
	if err != nil {
		return Invocation{}, fmt.Errorf("executor/pi: marshal models.json: %w", err)
	}

	return Invocation{
		Argv:       argv,
		Env:        env,
		PromptFile: spec.PromptPath,
		Files:      map[string]string{"pi-agent/models.json": string(models)},
	}, nil
}

// piRPCCommand is one rpc-mode stdin command's wire shape: {"type":
// "prompt"|"steer", "message": "..."} (issue #358). json.Marshal
// handles quote/newline escaping, so PromptCommand/SteerCommand never
// need to think about shell or JSON quoting themselves.
type piRPCCommand struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// PromptCommand implements Steerer: the one stdin line that starts a
// rpc-mode run with prompt.
func (piAdapter) PromptCommand(prompt string) string {
	return piMarshalRPCCommand("prompt", prompt)
}

// SteerCommand implements Steerer: the one stdin line that queues note
// for the currently running agent, delivered as a user message after
// the current tool calls finish and before the next LLM call.
func (piAdapter) SteerCommand(note string) string {
	return piMarshalRPCCommand("steer", note)
}

// piMarshalRPCCommand marshals a piRPCCommand; json.Marshal on a
// struct with only string fields never errors, so the error is
// discarded rather than threaded through Steerer's error-free
// signature.
func piMarshalRPCCommand(kind, message string) string {
	b, _ := json.Marshal(piRPCCommand{Type: kind, Message: message})
	return string(b)
}

// piModelsConfig/piProviderConfig/piModelConfig mirror pi's models.json
// shape (docs/models.md) - marshaled properly rather than string-
// templated, since baseURL/model/apiKey ride user- or spec-controlled
// values.
type piModelsConfig struct {
	Providers map[string]piProviderConfig `json:"providers"`
}

type piProviderConfig struct {
	BaseURL string          `json:"baseUrl"`
	API     string          `json:"api"`
	APIKey  string          `json:"apiKey"`
	Models  []piModelConfig `json:"models"`
}

type piModelConfig struct {
	ID string `json:"id"`
}

// piParser is a fresh, stateful StreamParser per spawn. It remembers
// the last assistant message's text/stopReason/errorMessage from
// message_end events as a fallback for buildTerminalEvent, in case
// agent_end's own messages slice ever omits the final assistant turn.
type piParser struct {
	stats Stats

	lastAssistantText string
	lastStopReason    string
	lastErrorMessage  string
}

func (piAdapter) NewParser() StreamParser {
	return &piParser{}
}

func (p *piParser) Stats() Stats { return p.stats }

// pi wire types - decoded strictly per top-level "type", tolerating
// extra fields; unrecognized types map to ok=false.

type piLineEnvelope struct {
	Type string `json:"type"`
}

type piContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type piUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

type piAssistantMessage struct {
	Role         string           `json:"role"`
	Content      []piContentBlock `json:"content"`
	Usage        piUsage          `json:"usage"`
	StopReason   string           `json:"stopReason"`
	ErrorMessage string           `json:"errorMessage"`
}

type piMessageEndLine struct {
	Message piAssistantMessage `json:"message"`
}

type piToolExecutionStartLine struct {
	ToolName string          `json:"toolName"`
	Args     json.RawMessage `json:"args"`
}

type piToolExecutionEndLine struct {
	ToolName string `json:"toolName"`
}

type piAgentEndLine struct {
	Messages  []piAssistantMessage `json:"messages"`
	WillRetry bool                 `json:"willRetry"`
}

// ParseLine implements StreamParser. ok=false means noise, never an
// error. pi's json mode exits 0 even on model errors - the Err set on
// the terminal agent_end event (see buildTerminalEvent) is the only
// failure signal a caller has.
func (p *piParser) ParseLine(line []byte) (Event, bool) {
	p.stats.Lines++
	line = piTrimSpace(line)
	if len(line) == 0 {
		return Event{}, false
	}

	var env piLineEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		p.stats.Unknown++
		return Event{}, false
	}

	switch env.Type {
	case "session":
		var sess struct {
			CWD string `json:"cwd"`
		}
		if err := json.Unmarshal(line, &sess); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return Event{Kind: KindSystem, Text: sess.CWD}, true

	case "message_end":
		var m piMessageEndLine
		if err := json.Unmarshal(line, &m); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		if m.Message.Role != "assistant" {
			p.stats.Unknown++
			return Event{}, false
		}
		return p.handleMessageEnd(m.Message)

	case "tool_execution_start":
		var t piToolExecutionStartLine
		if err := json.Unmarshal(line, &t); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return Event{Kind: KindTool, Tool: &ToolActivity{
			Name:   t.ToolName,
			Detail: truncate(string(t.Args), 200),
			Status: "started",
		}}, true

	case "tool_execution_end":
		var t piToolExecutionEndLine
		if err := json.Unmarshal(line, &t); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return Event{Kind: KindTool, Tool: &ToolActivity{
			Name:   t.ToolName,
			Status: "finished",
		}}, true

	case "agent_end":
		var a piAgentEndLine
		if err := json.Unmarshal(line, &a); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		if a.WillRetry {
			// pi auto-retries; another cycle follows - noise, not
			// terminal.
			p.stats.Unknown++
			return Event{}, false
		}
		p.stats.Events++
		return p.buildTerminalEvent(a), true

	default:
		// message_start, message_update, turn_*, queue_update, response
		// (rpc mode's per-command ack, issue #358), compaction_*,
		// agent_start, auto_retry_*, and any future type - all noise the
		// harness never acts on.
		p.stats.Unknown++
		return Event{}, false
	}
}

// handleMessageEnd concatenates one assistant message's text blocks
// into a KindText event and remembers its stopReason/errorMessage/text
// as buildTerminalEvent's fallback - agent_end's own messages slice is
// authoritative and normally supersedes this.
func (p *piParser) handleMessageEnd(m piAssistantMessage) (Event, bool) {
	p.lastStopReason = m.StopReason
	p.lastErrorMessage = m.ErrorMessage

	var texts []string
	for _, b := range m.Content {
		if b.Type == "text" {
			texts = append(texts, b.Text)
		}
	}
	if len(texts) == 0 {
		p.stats.Unknown++
		return Event{}, false
	}
	text := strings.Join(texts, "")
	p.lastAssistantText = text
	p.stats.Events++
	return Event{Kind: KindText, Text: text}, true
}

// buildTerminalEvent builds the one terminal KindResult event a
// non-retrying agent_end line carries: text and usage are recomputed
// from ALL assistant messages in a.Messages (authoritative, replacing
// the incrementally accumulated running sums); Result extracts a
// trailing verdict JSON object from the last assistant message's text;
// Err is set when that message's stopReason signals failure - pi's
// json mode exits 0 regardless, so this is the only failure signal.
func (p *piParser) buildTerminalEvent(a piAgentEndLine) Event {
	var usage Usage
	var lastText, lastStopReason, lastErrorMessage string
	for _, m := range a.Messages {
		if m.Role != "assistant" {
			continue
		}
		usage.InputTokens += m.Usage.Input
		usage.OutputTokens += m.Usage.Output
		usage.CacheReadTokens += m.Usage.CacheRead
		usage.CacheWriteTokens += m.Usage.CacheWrite

		var texts []string
		for _, b := range m.Content {
			if b.Type == "text" {
				texts = append(texts, b.Text)
			}
		}
		if len(texts) > 0 {
			lastText = strings.Join(texts, "")
		}
		lastStopReason = m.StopReason
		lastErrorMessage = m.ErrorMessage
	}
	if lastText == "" {
		lastText = p.lastAssistantText
	}
	if lastStopReason == "" {
		lastStopReason = p.lastStopReason
	}
	if lastErrorMessage == "" {
		lastErrorMessage = p.lastErrorMessage
	}

	ev := Event{
		Kind: KindResult,
		Text: lastText,
		// CostUSD stays nil always - pi prices client-side against its
		// own catalog, never trusted (D-013, see Capabilities.ReportsCost).
		Usage: &usage,
	}
	if lastStopReason == "error" || lastStopReason == "aborted" {
		ev.Err = lastErrorMessage
		if ev.Err == "" {
			ev.Err = lastStopReason
		}
	}
	if raw, ok := extractTrailingJSONObject(lastText); ok {
		ev.Result = raw
	}
	return ev
}

// extractTrailingJSONObject finds a trailing JSON object in text: the
// whole text if it parses as one, otherwise its last non-empty line.
// pi has no --json-schema, so the prompt-instructed verdict line is
// the only structured-output channel; this is the extraction the
// system prompt's instruction (piVerdictInstruction) sets up for.
func extractTrailingJSONObject(text string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}
	if looksLikeJSONObject(trimmed) {
		return json.RawMessage(trimmed), true
	}
	lines := strings.Split(trimmed, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if looksLikeJSONObject(last) {
		return json.RawMessage(last), true
	}
	return nil, false
}

// looksLikeJSONObject reports whether s decodes as a JSON object
// value (not merely valid JSON - a bare string or number must not be
// mistaken for the verdict object).
func looksLikeJSONObject(s string) bool {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return false
	}
	_, ok := v.(map[string]any)
	return ok
}

// ParseResult decodes a KindResult event's Result payload into DONE,
// RETRY, or BLOCKED. Any other value, or a payload that doesn't
// decode, returns ok=false. Semantics identical to claude's.
func (piAdapter) ParseResult(ev Event) (Result, bool) {
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

func piTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
