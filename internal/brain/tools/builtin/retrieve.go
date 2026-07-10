package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// OutputStore is the slice of the offloaded-output store the retrieve
// tool needs.
type OutputStore interface {
	Get(ctx context.Context, id string) (tools.Output, error)
}

type retrieveArgs struct {
	Ref string `json:"ref"`
}

func RetrieveOutput(store OutputStore) *tools.Tool {
	return &tools.Tool{
		Name: "retrieve_output",
		Description: `Retrieves the full content of an offloaded tool result.

When a tool result is too large for the conversation, you receive a
digest (an excerpt plus counts) together with a ref id. Use this tool
when the digest is not enough and you need the complete output — for
example to quote an exact line, inspect a section the excerpt cut, or
process the whole result.

Arguments:
- ref (string, required): the ref id exactly as it appeared in the
  digest, e.g. "9be4c1d2-04a7-47a1-a1a9-3f6d2c9f1e10".

Returns the stored output in full. Very large outputs may again arrive
as a digest of a smaller slice — retrieval pages rather than flooding.

Edge cases: refs expire after a retention window (default 7 days);
an expired or mistyped ref returns an error saying so.

Example: {"ref": "9be4c1d2-04a7-47a1-a1a9-3f6d2c9f1e10"} → the full
50,000-line build log the digest summarized.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"ref": {
					"type": "string",
					"description": "Ref id from a digest, a UUID"
				}
			},
			"required": ["ref"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args retrieveArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			out, err := store.Get(ctx, args.Ref)
			if errors.Is(err, tools.ErrOutputNotFound) {
				return "", fmt.Errorf("unknown or expired ref %q: offloaded outputs expire after the retention window", args.Ref)
			}
			if err != nil {
				return "", fmt.Errorf("retrieve output: %w", err)
			}
			return out.Content, nil
		},
	}
}
