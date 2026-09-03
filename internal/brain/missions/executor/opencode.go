package executor

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// opencodeHarness names this adapter's registration key.
const opencodeHarness = "opencode"

// opencodeAdapter wires the opencode CLI's headless json mode (verified
// against opencode-ai 1.18.18 fixtures in testdata/opencode-1.18.18).
// opencode has no output-schema flag, so it uses the same pi-style
// sentinel-verdict path as pi/codex's trailing-JSON extraction.
type opencodeAdapter struct{}

func init() {
	Register(opencodeAdapter{})
}

func (opencodeAdapter) Harness() string { return opencodeHarness }

func (opencodeAdapter) Capabilities() Capabilities {
	//nolint:gosec // G101: env var names the runner injects into, not credential values.
	return Capabilities{
		StructuredFinalOutput: false,
		ReportsTokens:         true,
		// step_finish carries a cost field, but it's priced client-side
		// from opencode's own bundled pricing table, not billed spend -
		// never trusted (D-013).
		ReportsCost: false,
		WireFormat:  "openai",
		APIKeyEnv:   "OPENCODE_API_KEY",
		// BaseURL rides the config file (provider.timothy.options.baseURL),
		// not an env var - opencode has no base-url env override.
		BaseURLEnv: "",
		StateDirs:  nil,
		// SupportsResume stays false (D-103, issue #499): opencode is not
		// installed in this environment and no `--help` text or repo
		// doc records its resume/continue flag, so BuildInvocation never
		// guesses one - the runner starts fresh instead.
	}
}

// opencodeDefaultBaseURL is used when spec.BaseURL is empty.
const opencodeDefaultBaseURL = "https://api.openai.com/v1"

// opencodeVerdictInstruction is appended to the instructions file so the
// agent's final message ends with the DONE/RETRY/BLOCKED verdict line -
// opencode has no --json-schema flag, so this sentence is the only
// verdict channel (spec.ResultSchema is intentionally never passed to
// opencode's argv or config).
const opencodeVerdictInstruction = "End your final message with a single line containing only a JSON object of the form {\"status\":\"DONE\"|\"RETRY\"|\"BLOCKED\",\"note\":\"...\"} and nothing after it."

// BuildInvocation validates spec and translates it to an opencode CLI
// argv + env. The prompt never rides the argv directly - PromptFile
// names the path the runner substitutes via `$(cat PromptFile)` at
// spawn time, same mechanism as codex/pi. spec.AllowTools/DenyTools are
// ignored: opencode has no per-tool deny surface we use - the
// per-mission sandbox container is the actual boundary (accepted risk,
// same rationale as pi/codex). spec.BudgetUSD is ignored - opencode has
// no budget flag. Working dir rides the runner's own process cwd - like
// pi, opencode has no -C/chdir flag, so the runner spawning in the run
// dir is the only mechanism (see delegated.go).
func (opencodeAdapter) BuildInvocation(spec InvocationSpec) (Invocation, error) {
	if spec.Model == "" {
		return Invocation{}, fmt.Errorf("executor/opencode: empty model")
	}
	if spec.PromptPath == "" {
		return Invocation{}, fmt.Errorf("executor/opencode: empty prompt path")
	}
	if spec.AuthMode != AuthAPIKey {
		return Invocation{}, fmt.Errorf("executor/opencode: auth mode %q not supported, api_key only", spec.AuthMode)
	}
	if spec.APIKey == "" {
		return Invocation{}, fmt.Errorf("executor/opencode: api_key auth requires a key")
	}
	if spec.Wire != "openai" {
		return Invocation{}, fmt.Errorf("executor/opencode: wire %q not supported, opencode speaks openai only", spec.Wire)
	}

	baseURL := spec.BaseURL
	if baseURL == "" {
		baseURL = opencodeDefaultBaseURL
	}

	runDir := filepath.Dir(spec.PromptPath)
	configPath := filepath.Join(runDir, "opencode", "opencode.json")
	instructionsPath := filepath.Join(runDir, "opencode", "instructions.md")

	instructions := spec.SystemAppend
	if instructions != "" {
		instructions += "\n\n"
	}
	instructions += opencodeVerdictInstruction

	config, err := json.Marshal(opencodeConfig{
		Schema: "https://opencode.ai/config.json",
		// Headless runs auto-reject opencode's own permission prompts
		// (observed: external_directory on an absolute-path write ends
		// the run exit 0 with no verdict) - allow everything; the
		// per-mission sandbox container is the actual boundary, same
		// accepted risk as AllowTools/DenyTools above.
		Permission: "allow",
		Provider: map[string]opencodeProviderConfig{
			"timothy": {
				NPM:  opencodeProviderNPM(baseURL),
				Name: "Timothy",
				//nolint:gosec // G101: apiKey holds opencode's env-substitution template, not a credential value.
				Options: opencodeProviderOptions{
					BaseURL: baseURL,
					APIKey:  "{env:OPENCODE_API_KEY}",
				},
				Models: map[string]opencodeModelConfig{spec.Model: {}},
			},
		},
		Instructions: []string{instructionsPath},
	})
	if err != nil {
		return Invocation{}, fmt.Errorf("executor/opencode: marshal config: %w", err)
	}

	argv := []string{
		"opencode", "run",
		"--format", "json",
		"-m", "timothy/" + spec.Model,
		"@PROMPT@", // substituted by the runner via PromptFile
	}

	env := map[string]string{
		"NO_COLOR":         "1",
		"OPENCODE_API_KEY": spec.APIKey,
		"OPENCODE_CONFIG":  configPath,
	}

	files := map[string]string{
		"opencode/opencode.json":   string(config),
		"opencode/instructions.md": instructions,
	}

	return Invocation{Argv: argv, Env: env, PromptFile: spec.PromptPath, Files: files}, nil
}

// opencodeProviderNPM picks the AI SDK provider package by baseURL host.
// @ai-sdk/openai-compatible always sends max_tokens, which OpenAI's
// reasoning-model family (gpt-5.x) rejects ("Unsupported parameter:
// 'max_tokens' ... Use 'max_completion_tokens' instead") - the harness
// CLI talks to the provider endpoint directly, so the gateway's
// retryOn400 param swap never sees the request. api.openai.com gets the
// official @ai-sdk/openai package instead, which sends
// max_completion_tokens and speaks the current OpenAI wire; every other
// host (GLM, local Ollama, etc.) keeps openai-compatible. An unparseable
// baseURL falls back to openai-compatible.
func opencodeProviderNPM(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() != "api.openai.com" {
		return "@ai-sdk/openai-compatible"
	}
	return "@ai-sdk/openai"
}

// opencodeConfig/opencodeProviderConfig/opencodeProviderOptions/
// opencodeModelConfig mirror opencode's config.json shape - marshaled
// properly rather than string-templated, since baseURL is provider-row-
// controlled, not literal-safe. "{env:OPENCODE_API_KEY}" is opencode's
// documented env-substitution template; the key value itself never
// enters the file.
type opencodeConfig struct {
	Schema       string                            `json:"$schema"`
	Permission   string                            `json:"permission"`
	Provider     map[string]opencodeProviderConfig `json:"provider"`
	Instructions []string                          `json:"instructions"`
}

type opencodeProviderConfig struct {
	NPM     string                         `json:"npm"`
	Name    string                         `json:"name"`
	Options opencodeProviderOptions        `json:"options"`
	Models  map[string]opencodeModelConfig `json:"models"`
}

type opencodeProviderOptions struct {
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
}

type opencodeModelConfig struct{}

// opencodeParser is a fresh, stateful StreamParser per spawn. It
// remembers the last "text" part's text as the terminal event's
// Text/Result source, and accumulates step_finish tokens across the
// whole run - there is no aggregate usage event.
type opencodeParser struct {
	stats Stats

	lastText  string
	usage     Usage
	sawSystem bool
}

func (opencodeAdapter) NewParser() StreamParser {
	return &opencodeParser{}
}

func (p *opencodeParser) Stats() Stats { return p.stats }

// opencode wire types - every line is {"type","timestamp","sessionID",
// "part":{...}} except top-level "error" (has "error" instead of
// "part"). Event "type" uses underscores, "part.type" uses hyphens.
// Decoded strictly per top-level "type", tolerating extra fields;
// unrecognized types map to ok=false.

type opencodeLineEnvelope struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
}

type opencodePart struct {
	Text   string            `json:"text"`
	Tool   string            `json:"tool"`
	State  opencodeToolState `json:"state"`
	Reason string            `json:"reason"`
	Tokens opencodeTokens    `json:"tokens"`
}

type opencodeToolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
}

type opencodeTokens struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Cache  struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

type opencodeLine struct {
	Part opencodePart `json:"part"`
}

type opencodeErrorLine struct {
	Error struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// ParseLine implements StreamParser. ok=false means noise, never an
// error. Exit code is a reliable failure signal for opencode (unlike
// codex) but the runner still needs the top-level "error" event's
// message surfaced as the terminal Err.
func (p *opencodeParser) ParseLine(line []byte) (Event, bool) {
	p.stats.Lines++
	line = opencodeTrimSpace(line)
	if len(line) == 0 {
		return Event{}, false
	}

	var env opencodeLineEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		p.stats.Unknown++
		return Event{}, false
	}

	switch env.Type {
	case "step_start":
		if p.sawSystem {
			p.stats.Unknown++
			return Event{}, false
		}
		p.sawSystem = true
		p.stats.Events++
		return Event{Kind: KindSystem, Text: env.SessionID, SessionID: env.SessionID}, true

	case "text":
		var l opencodeLine
		if err := json.Unmarshal(line, &l); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		p.lastText = l.Part.Text
		p.stats.Events++
		return Event{Kind: KindText, Text: l.Part.Text}, true

	case "tool_use":
		var l opencodeLine
		if err := json.Unmarshal(line, &l); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		return p.handleToolUse(l.Part)

	case "step_finish":
		var l opencodeLine
		if err := json.Unmarshal(line, &l); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		return p.handleStepFinish(l.Part)

	case "error":
		var e opencodeErrorLine
		if err := json.Unmarshal(line, &e); err != nil {
			p.stats.Unknown++
			return Event{}, false
		}
		msg := e.Error.Data.Message
		if msg == "" {
			msg = e.Error.Name
		}
		p.stats.Events++
		return Event{Kind: KindResult, Err: msg}, true

	default:
		p.stats.Unknown++
		return Event{}, false
	}
}

// handleToolUse handles one tool_use line. opencode emits tool_use only
// once, after completion - there is no started/finished pair in the
// stream, unlike claude/pi/codex. A "completed" status yields a single
// KindTool "finished" event; anything else (e.g. "error") is noise for
// now, no fixture has observed it.
func (p *opencodeParser) handleToolUse(part opencodePart) (Event, bool) {
	if part.State.Status != "completed" {
		p.stats.Unknown++
		return Event{}, false
	}
	p.stats.Events++
	return Event{Kind: KindTool, Tool: &ToolActivity{
		Name:   part.Tool,
		Detail: truncate(string(part.State.Input), 200),
		Status: "finished",
	}}, true
}

// handleStepFinish accumulates one step_finish's tokens into the
// running totals - there is no per-run aggregate event, so every
// step_finish (reason "stop" or "tool-calls") counts. reason "stop" is
// the terminal event: Text/Result come from the last "text" part seen,
// Usage is the accumulated total (CostUSD always nil - D-013). reason
// "tool-calls" is noise once its tokens are folded in.
func (p *opencodeParser) handleStepFinish(part opencodePart) (Event, bool) {
	p.usage.InputTokens += part.Tokens.Input
	p.usage.OutputTokens += part.Tokens.Output
	p.usage.CacheReadTokens += part.Tokens.Cache.Read
	p.usage.CacheWriteTokens += part.Tokens.Cache.Write

	if part.Reason != "stop" {
		p.stats.Unknown++
		return Event{}, false
	}

	p.stats.Events++
	usage := p.usage
	ev := Event{
		Kind:  KindResult,
		Text:  p.lastText,
		Usage: &usage,
	}
	if raw, ok := opencodeExtractVerdict(p.lastText); ok {
		ev.Result = raw
	}
	return ev, true
}

// opencodeExtractVerdict finds the verdict JSON object in a final
// message's text. Tries extractTrailingJSONObject first (bare object,
// same as pi/codex), then falls back to a trailing "{...}" substring -
// models often prefix the object with a label (the recorded happy
// fixture carries "VERDICT: {...}"), which neither of
// extractTrailingJSONObject's cases (whole text / last line) covers.
func opencodeExtractVerdict(text string) (json.RawMessage, bool) {
	if raw, ok := extractTrailingJSONObject(text); ok {
		return raw, true
	}
	trimmed := strings.TrimSpace(text)
	start := strings.LastIndex(trimmed, "{")
	if start == -1 || !strings.HasSuffix(trimmed, "}") {
		return nil, false
	}
	candidate := trimmed[start:]
	if looksLikeJSONObject(candidate) {
		return json.RawMessage(candidate), true
	}
	return nil, false
}

// ParseResult decodes a KindResult event's Result payload into DONE,
// RETRY, or BLOCKED. Any other value, or a payload that doesn't decode,
// returns ok=false. Semantics identical to claude's/pi's/codex's.
func (opencodeAdapter) ParseResult(ev Event) (Result, bool) {
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

func opencodeTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
