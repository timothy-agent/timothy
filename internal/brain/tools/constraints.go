package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

// Violation is a constraint breach reported back to the model as a
// corrective tool result. It never panics and never ends the turn —
// the model reads the message and adjusts (D-009).
type Violation struct {
	Msg string
}

func (v *Violation) Error() string { return v.Msg }

// IsViolation reports whether err is a constraint violation the loop
// should relay as tool feedback rather than an infrastructure fault.
func IsViolation(err error) bool {
	var v *Violation
	return errors.As(err, &v)
}

// Clamp rewrites model-supplied arguments to enforced values (e.g.
// capping a timeout). Clamps run after schema validation, so they see
// well-formed input; they must not reject, only override.
type Clamp func(args json.RawMessage) (json.RawMessage, error)

// Constrained wraps a registry so every execution passes the
// constraint chain: schema validation, then per-tool clamps, then the
// tool itself.
type Constrained struct {
	reg     *Registry
	schemas map[string]*jsonschema.Schema
	clamps  map[string]Clamp
}

// NewConstrained compiles every registered tool's input schema up
// front; a malformed schema is a programming error caught at startup.
func NewConstrained(reg *Registry) (*Constrained, error) {
	c := &Constrained{
		reg:     reg,
		schemas: make(map[string]*jsonschema.Schema),
		clamps:  make(map[string]Clamp),
	}
	for _, t := range reg.List() {
		if len(t.InputSchema) == 0 {
			return nil, fmt.Errorf("tools: %s has no input schema", t.Name)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(t.InputSchema))
		if err != nil {
			return nil, fmt.Errorf("tools: %s schema: %w", t.Name, err)
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(t.Name+".json", doc); err != nil {
			return nil, fmt.Errorf("tools: %s schema: %w", t.Name, err)
		}
		schema, err := compiler.Compile(t.Name + ".json")
		if err != nil {
			return nil, fmt.Errorf("tools: %s schema: %w", t.Name, err)
		}
		c.schemas[t.Name] = schema
	}
	return c, nil
}

// SetClamp installs an argument clamp for one tool.
func (c *Constrained) SetClamp(tool string, clamp Clamp) {
	c.clamps[tool] = clamp
}

// Execute runs one tool call through the constraint chain. A
// *Violation error is model feedback; any other error is an
// infrastructure fault.
func (c *Constrained) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	tool, ok := c.reg.Get(name)
	if !ok {
		return "", &Violation{Msg: fmt.Sprintf(
			"unknown tool %q — use one of the tools you were given", name)}
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return "", &Violation{Msg: fmt.Sprintf(
			"arguments for %s are not valid JSON: %v — resend the call with corrected arguments", name, err)}
	}
	if err := c.schemas[name].Validate(doc); err != nil {
		return "", &Violation{Msg: fmt.Sprintf(
			"arguments for %s failed validation: %v — check the tool description for the expected format and resend", name, err)}
	}
	if clamp, ok := c.clamps[name]; ok {
		clamped, err := clamp(args)
		if err != nil {
			return "", fmt.Errorf("tools: clamp %s: %w", name, err)
		}
		args = clamped
	}
	return tool.Execute(ctx, args)
}

// WithinRoot resolves path (which must be absolute) against symlinks
// and reports whether it stays under root. Nonexistent trailing
// components are allowed — the deepest existing ancestor is what gets
// resolved — so "write to a new file" still validates.
func WithinRoot(root, path string) error {
	if !filepath.IsAbs(path) {
		return &Violation{Msg: fmt.Sprintf(
			"path %q must be absolute (start from %s)", path, root)}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("tools: resolve workspace root: %w", err)
	}
	resolved, err := resolveExisting(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("tools: resolve path: %w", err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return &Violation{Msg: fmt.Sprintf(
			"path %q is outside the workspace %s — only paths under the workspace are allowed", path, root)}
	}
	return nil
}

// resolveExisting walks up from path to the deepest existing ancestor,
// resolves that through symlinks, and rejoins the nonexistent tail.
func resolveExisting(path string) (string, error) {
	suffix := ""
	for cur := path; ; {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(resolved, suffix), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no existing ancestor for %s", path)
		}
		suffix = filepath.Join(filepath.Base(cur), suffix)
		cur = parent
	}
}

// StepDirective is the loop-ceiling decision for one step of a turn.
type StepDirective int

const (
	// StepProceed: run the step normally.
	StepProceed StepDirective = iota
	// StepWarnFinalize: run the step but inject a "finalize next"
	// warning — one step remains before the ceiling.
	StepWarnFinalize
	// StepForceSynthesis: drop tool schemas; the model must answer
	// with what it has.
	StepForceSynthesis
)

// DefaultMaxSteps bounds tool-loop iterations per turn.
const DefaultMaxSteps = 16

// CeilingFor returns the directive for a 1-based step number under a
// max-steps ceiling.
func CeilingFor(step, maxSteps int) StepDirective {
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	switch {
	case step >= maxSteps:
		return StepForceSynthesis
	case step == maxSteps-1:
		return StepWarnFinalize
	default:
		return StepProceed
	}
}

// repeatThreshold: the same tool called with the same arguments this
// many times in one turn forces synthesis early — a model retrying an
// unproductive call (e.g. hoping a search engine will "book" something
// on a later attempt) would otherwise burn steps up to the full
// ceiling before answering.
const repeatThreshold = 3

// RepeatGuard tracks (tool, args) signatures across a turn's steps and
// reports when the same call has repeated too many times in a row.
type RepeatGuard struct {
	lastSig   string
	lastCount int
}

// Record folds in one step's calls and returns true once any single
// call signature has repeated repeatThreshold times consecutively.
// Consecutive, not merely "seen before": a model that tries A, B, A
// is exploring, not stuck; A, A, A is stuck. Signatures compare the
// tool name and raw argument JSON — args differing by whitespace only
// would already have been re-marshaled identically by the model.
func (g *RepeatGuard) Record(calls []provider.ToolCall) bool {
	stuck := false
	for _, c := range calls {
		sig := c.Name + ":" + string(c.Input)
		if sig == g.lastSig {
			g.lastCount++
		} else {
			g.lastSig = sig
			g.lastCount = 1
		}
		if g.lastCount >= repeatThreshold {
			stuck = true
		}
	}
	return stuck
}

// MinToolCallsFor is the per-category floor of tool calls before a
// final answer counts (research answers without retrieval get coerced
// once). Categories absent from the table have no floor.
var MinToolCallsFor = map[string]int{
	"research": 1,
}

// NeedsRetrievalCoercion reports whether a final answer should be
// bounced back with a coercion message: the category has a floor, the
// model hasn't met it, and it hasn't been coerced already this turn
// (one bounce only — a model that insists gets its answer through).
func NeedsRetrievalCoercion(category string, toolCalls int, alreadyCoerced bool) bool {
	minCalls, ok := MinToolCallsFor[category]
	if !ok || alreadyCoerced {
		return false
	}
	return toolCalls < minCalls
}
