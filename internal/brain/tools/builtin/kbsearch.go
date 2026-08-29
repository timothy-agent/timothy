package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	kbSearchDefaultK = 8
	kbSearchMaxK     = 10
)

var kbSearchModes = map[string]bool{"hybrid": true, "semantic": true, "keyword": true}

// KBSearchHit is one retrieved chunk; memclient.KBChunkHit satisfies
// this shape, passed in as a plain struct to keep this package free of
// a memclient import.
type KBSearchHit struct {
	DocumentID    string
	DocumentTitle string
	Breadcrumb    string
	Content       string
	SourceRef     string
}

// KBSearchFunc runs one hybrid/semantic/keyword search scoped to
// collections; main curries memclient.Client.KBSearch in with the
// calling agent's collection allowlist already bound — collections are
// NEVER a model-controlled argument (D-060: enforced in Go, not a
// prompt).
type KBSearchFunc func(ctx context.Context, query, mode string, k int) ([]KBSearchHit, error)

type kbSearchArgs struct {
	Query string `json:"query"`
	Mode  string `json:"mode"`
	K     *int   `json:"k"`
}

// KBSearch lets the model search one agent's allowed knowledge-base
// collections. search is built per-agent, per-turn (see chat.go) with
// the collection set already bound — this constructor never sees or
// accepts collection names itself.
func KBSearch(search KBSearchFunc) *tools.Tool {
	return &tools.Tool{
		Name: "search_kb",
		Description: `Searches this agent's knowledge base collections and returns matching passages with their source.

Use when the user asks about something that might be documented in the
agent's configured knowledge base — internal docs, reference material,
uploaded files — rather than general knowledge or long-term memory.

Arguments:
- query (string, required): what to search for.
- mode (string, optional): "hybrid" (default, vector + keyword),
  "semantic" (vector only), or "keyword" (full-text only).
- k (integer, optional): how many passages to return, 1-10, default 8.

Returns numbered passages, each with its document title, section
breadcrumb, source reference, and content, plus a "Source:" line giving
a stable kb:// reference. The kb:// reference is internal plumbing —
pass it to read_kb to load the full document, but never show it to the
user; when answering from a result, cite the document by its title.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "What to search for."
				},
				"mode": {
					"type": "string",
					"enum": ["hybrid", "semantic", "keyword"],
					"description": "Retrieval mode; defaults to hybrid."
				},
				"k": {
					"type": "integer",
					"minimum": 1,
					"maximum": 10,
					"description": "Number of passages to return; defaults to 8."
				}
			},
			"required": ["query"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args kbSearchArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("search_kb: invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.Query) == "" {
				return "", fmt.Errorf("search_kb: query must not be empty")
			}
			if args.Mode != "" && !kbSearchModes[args.Mode] {
				return "", fmt.Errorf("search_kb: mode must be hybrid, semantic, or keyword, got %q", args.Mode)
			}
			k := kbSearchDefaultK
			if args.K != nil {
				k = *args.K
				if k < 1 || k > kbSearchMaxK {
					return "", fmt.Errorf("search_kb: k must be between 1 and %d, got %d", kbSearchMaxK, k)
				}
			}
			hits, err := search(ctx, args.Query, args.Mode, k)
			if err != nil {
				return "", fmt.Errorf("search_kb: %w", err)
			}
			return formatKBHits(hits), nil
		},
	}
}

func formatKBHits(hits []KBSearchHit) string {
	if len(hits) == 0 {
		return "no matching passages found"
	}
	var b strings.Builder
	for i, h := range hits {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(h.DocumentTitle)
		if h.Breadcrumb != "" {
			b.WriteString(" — ")
			b.WriteString(h.Breadcrumb)
		}
		if h.SourceRef != "" {
			b.WriteString(" (")
			b.WriteString(h.SourceRef)
			b.WriteString(")")
		}
		b.WriteString("\n")
		if h.DocumentID != "" {
			b.WriteString("Source: kb://")
			b.WriteString(h.DocumentID)
			b.WriteString("\n")
		}
		b.WriteString(h.Content)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}
