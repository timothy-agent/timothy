package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// KBDocument is one full document as read_kb returns it.
type KBDocument struct {
	Title     string
	SourceRef string
	Markdown  string
}

// KBReadFunc loads one document by id. main curries the store lookup
// in with the calling agent's collection allowlist already bound —
// a document outside the allowed collections reads as not found, and
// collections are NEVER a model-controlled argument (D-060: enforced
// in Go, not a prompt).
type KBReadFunc func(ctx context.Context, documentID string) (KBDocument, error)

type kbReadArgs struct {
	Ref string `json:"ref"`
}

// KBRead lets the model read a knowledge-base document in full after
// search_kb surfaced an excerpt from it. read is built per-agent,
// per-turn with the collection set already bound — this constructor
// never sees or accepts collection names itself.
func KBRead(read KBReadFunc) *tools.Tool {
	return &tools.Tool{
		Name: "read_kb",
		Description: `Reads a full knowledge-base document by its kb:// reference.

Use after search_kb when the returned passages are not enough — to see
a passage's surrounding context, or to read the whole document a hit
came from. Only documents in this agent's knowledge base collections
are readable.

Arguments:
- ref (string, required): the "kb://<document-id>" reference from a
  search_kb result's "Source:" line (the bare document id also works).

Returns the document's title, source reference, and full markdown
content. When citing it to the user, use the document title — never
the kb:// reference.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"ref": {
					"type": "string",
					"description": "kb:// reference (or bare document id) from a search_kb result."
				}
			},
			"required": ["ref"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args kbReadArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("read_kb: invalid arguments: %w", err)
			}
			id := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args.Ref), "kb://"))
			if id == "" {
				return "", fmt.Errorf("read_kb: ref must be a kb:// reference or document id")
			}
			doc, err := read(ctx, id)
			if err != nil {
				return "", fmt.Errorf("read_kb: %w", err)
			}
			var b strings.Builder
			b.WriteString(doc.Title)
			if doc.SourceRef != "" {
				b.WriteString(" (")
				b.WriteString(doc.SourceRef)
				b.WriteString(")")
			}
			b.WriteString("\n\n")
			b.WriteString(doc.Markdown)
			return b.String(), nil
		},
	}
}
