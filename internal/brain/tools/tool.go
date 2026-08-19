// Package tools defines the tool abstraction the agent loop executes:
// a compiled-in registry of named tools, each with a JSON Schema for
// its input and a plain Execute function. Constraint middleware and
// the permission chain wrap Execute at the loop boundary; the tools
// themselves stay simple functions.
package tools

import (
	"context"
	"encoding/json"
)

// Tool is one capability the model can invoke. Description is the
// model's only manual for the tool — it must say what the tool does,
// when to use it, argument formats, and edge cases. InputSchema is a
// JSON Schema object; arguments are validated against it before
// Execute runs.
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
