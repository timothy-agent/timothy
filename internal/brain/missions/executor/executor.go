// Package executor defines the delegated coding-harness contract (D-029, D-051).
// An adapter is pure translation: spawn spec -> CLI invocation, CLI output lines
// -> normalized events. It never touches Docker, the store, secret resolution,
// or verdict policy - those live in missions.
package executor

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// AuthMode selects how an adapter authenticates its CLI invocation.
type AuthMode string

const (
	AuthSubscription AuthMode = "subscription"
	AuthAPIKey       AuthMode = "api_key"
	AuthOAuthToken   AuthMode = "oauth_token" //nolint:gosec // G101: mode name, not a credential value.
)

// Capabilities declares what a harness honestly supports; the runner never
// assumes an undeclared capability.
type Capabilities struct {
	StructuredFinalOutput bool
	ReportsTokens         bool
	ReportsCost           bool
	WireFormat            string // "anthropic" | "openai"
	APIKeyEnv             string
	BaseURLEnv            string
	StateDirs             []string
	// OAuthTokenEnv is the env var a long-lived OAuth token is injected
	// under; OAuthTokenPrefix is the value prefix that identifies one.
	// Empty OAuthTokenEnv means the adapter doesn't support this mode.
	OAuthTokenEnv    string
	OAuthTokenPrefix string
}

// InvocationSpec is what the runner supplies to build one CLI invocation.
type InvocationSpec struct {
	MissionID    string
	Workdir      string
	PromptPath   string
	SystemAppend string
	Model        string
	AuthMode     AuthMode
	APIKey       string
	BaseURL      string
	BudgetUSD    *float64
	AllowTools   []string
	DenyTools    []string
	ResultSchema json.RawMessage
	RunBudget    time.Duration
	// Wire is the wire format the resolved route entry exposes —
	// "anthropic" or "openai" — set by the runner from
	// gwclient.ResolvedRouteEntry.Wire. Only a dual-wire harness (pi)
	// needs this; a single-wire harness ignores it.
	Wire string
}

// Invocation is the argv + env an adapter wants spawned. PromptFile, when
// non-empty, names a path (equal to InvocationSpec.PromptPath) the runner
// substitutes into the command line at spawn time via `$(cat PromptFile)` -
// the exec API takes no stdin, and adapters stay shell-free by never
// embedding shell syntax themselves.
type Invocation struct {
	Argv       []string
	Env        map[string]string // allowlisted names only; values never logged
	PromptFile string
	// Files are extra files the runner writes into the run dir before
	// spawn, keyed by slash-separated path relative to the run dir
	// (e.g. "pi-agent/models.json"). Values are never logged.
	Files map[string]string
}

// EventKind names one normalized event's shape.
type EventKind string

const (
	KindSystem EventKind = "system"
	KindText   EventKind = "text"
	KindTool   EventKind = "tool"
	KindUsage  EventKind = "usage"
	KindResult EventKind = "result"
	KindError  EventKind = "error"
)

// Usage is normalized token/cost accounting for one run. CostUSD is a
// pointer so "unknown" (nil) is never confused with zero cost (D-013's
// cost-honesty invariant).
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          *float64
}

// ToolActivity is one tool call's normalized lifecycle point.
type ToolActivity struct {
	Name   string
	Detail string // input summary, <=200 chars
	Status string // "started" | "finished" | "denied"
}

// Event is one normalized line of harness output.
type Event struct {
	Kind EventKind
	Text string
	// Model is the model the harness reports it actually ran, set only
	// on KindSystem and only by harnesses whose init line names one
	// (claude-cli, cursor-cli). Empty everywhere else: KindSystem's Text
	// carries a different identifier per harness (thread id, session id,
	// cwd), so the model needs its own field rather than an overload.
	Model string
	Tool  *ToolActivity
	// Usage and Result are set only on KindResult; Usage may also be set
	// standalone on KindUsage for harnesses that report it out of band.
	Usage *Usage
	// Result is the raw structured-output payload when the harness
	// supports StructuredFinalOutput; ParseResult decodes it.
	Result json.RawMessage
	Err    string
	// Denials lists tool names the harness's own permission layer refused
	// during the run, populated only on the KindResult event - a single
	// line yields one Event, so per-denial detail rides alongside the
	// terminal event rather than as separate events.
	Denials []string
}

// Result is the decoded verdict from a structured-output-capable harness.
type Result struct {
	Status string // DONE | RETRY | BLOCKED
	Note   string
}

// StreamParser turns one raw output line into zero or one normalized Event.
// ok=false means "not an event" (noise, unknown type) - never an error.
// Parsers may be stateful (e.g. tracking tool_use id -> name); NewParser
// returns a fresh one per spawn.
type StreamParser interface {
	ParseLine(line []byte) (Event, bool)
}

// Stats counts a parser's line handling for drift detection - the runner
// logs these; an elevated Unknown count is caught by contract tests against
// recorded fixtures, never treated as fatal on a live run.
type Stats struct {
	Lines   int
	Events  int
	Unknown int
}

// ParserStats is implemented by StreamParser instances that track Stats.
type ParserStats interface {
	Stats() Stats
}

// Adapter is one coding harness's wire translation. Adapters hold no
// process, store, or secret-resolution logic.
type Adapter interface {
	// Harness names this adapter, e.g. "claude-cli".
	Harness() string
	Capabilities() Capabilities
	BuildInvocation(spec InvocationSpec) (Invocation, error)
	// NewParser returns a fresh StreamParser for one spawn.
	NewParser() StreamParser
	// ParseResult decodes a KindResult event's Result payload into a
	// Result, when Capabilities().StructuredFinalOutput is true. ok=false
	// means the payload didn't decode into a recognized verdict.
	ParseResult(ev Event) (Result, bool)
}

var registry = map[string]Adapter{}

// Register adds an adapter under its own Harness() name. Panics on a
// duplicate registration - a programming error, caught at init time.
func Register(a Adapter) {
	name := a.Harness()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("executor: duplicate adapter %q", name))
	}
	registry[name] = a
}

// Lookup returns the adapter registered for harness, if any.
func Lookup(harness string) (Adapter, bool) {
	a, ok := registry[harness]
	return a, ok
}

// Registered returns every registered harness name, sorted.
func Registered() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
