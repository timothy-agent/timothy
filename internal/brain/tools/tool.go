// Package tools defines the tool abstraction the agent loop executes:
// a compiled-in registry of named tools, each with a JSON Schema for
// its input and a plain Execute function. Constraint middleware and
// the permission chain wrap Execute at the loop boundary; the tools
// themselves stay simple functions.
package tools

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrTimeout marks a tool failure caused by the tool's own deadline
// expiring, so the loop can classify it as retryable without matching
// on message text. Wrap it with %w from any tool that enforces a
// timeout (see builtin.newShell).
var ErrTimeout = errors.New("tool: timed out")

// Tool is one capability the model can invoke. Description is the
// model's only manual for the tool — it must say what the tool does,
// when to use it, argument formats, and edge cases. InputSchema is a
// JSON Schema object; arguments are validated against it before
// Execute runs.
//
// Result convention: on success Execute returns prose (or whatever
// text the model should read) and a nil error. On failure it returns
// a Go error; the loop wraps that error into the structured tool
// error the model sees, so a tool must not format its own "error:"
// prose. A tool that genuinely needs to emit a machine-readable
// failure of its own returns an error whose message is a top-level
// JSON object carrying an "error" key, and the loop relays that
// object unwrapped instead of nesting one error inside another.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Execute     func(ctx context.Context, args json.RawMessage) (string, error)
	// ReadOnly marks a tool as having no side effects — set only on
	// connector tools a mission turn may see despite BuiltinsOnly (see
	// loop.Request.BuiltinsOnly and missions.nativeRunner's connector
	// reads resolver). Unset (false) everywhere else; setting it is a
	// deliberate, per-tool decision, never inferred from a naming
	// convention or a connector's kind.
	ReadOnly bool
}
