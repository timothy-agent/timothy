package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// RememberFunc stores one user-explicit memory and returns its id;
// main curries the memoryd client in.
type RememberFunc func(ctx context.Context, content, memoryType string) (string, error)

type rememberArgs struct {
	Content string `json:"content"`
	Type    string `json:"type"`
}

// Remember lets the model store a fact the user explicitly asked to
// keep ("Timothy, remember…"). User-explicit memories activate
// immediately — no confirmation queue (D-011).
func Remember(save RememberFunc) *tools.Tool {
	return &tools.Tool{
		Name: "remember",
		Description: `Stores one fact in long-term memory, permanently.

Use ONLY when the user explicitly asks to remember something ("remember
that...", "don't forget...", "make a note that..."). Never store facts
the user did not ask to keep — routine facts are captured automatically.

Arguments:
- content (string, required): the fact, one self-contained sentence
  with absolute dates and full names (no "he", "it", "tomorrow").
- type (string, optional): "semantic" (durable fact or preference,
  default), "episodic" (something that happened), or "procedural"
  (a how-to).

Returns a confirmation with the stored memory's id.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"content": {
					"type": "string",
					"description": "The fact to store: one self-contained sentence, absolute dates, full names."
				},
				"type": {
					"type": "string",
					"enum": ["semantic", "episodic", "procedural"],
					"description": "Memory tier; defaults to semantic."
				}
			},
			"required": ["content"]
		}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a rememberArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("remember: %w", err)
			}
			content := strings.TrimSpace(a.Content)
			if content == "" {
				return "", fmt.Errorf("remember: content is required")
			}
			typ := a.Type
			if typ == "" {
				typ = "semantic"
			}
			id, err := save(ctx, content, typ)
			if err != nil {
				return "", fmt.Errorf("remember: %w", err)
			}
			return "Stored in long-term memory (id " + id + "): " + content, nil
		},
	}
}
