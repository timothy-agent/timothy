package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// SearchMemoryHit is one recalled long-term memory; memclient.Memory
// satisfies this shape, passed in as a plain struct to keep this
// package free of a memclient import.
type SearchMemoryHit struct {
	Type    string
	Content string
	Score   float64
}

// SearchMemoryFunc runs one retrieval over long-term memory; main
// curries memclient.Client.Retrieve in with the session already bound.
type SearchMemoryFunc func(ctx context.Context, query string) ([]SearchMemoryHit, error)

type searchMemoryArgs struct {
	Query string `json:"query"`
}

// SearchMemory lets the model recall what Timothy knows about the
// operator. Chat renders memory into every turn's system prompt; a
// mission runs too many turns across too many phases for that to fit,
// so a mission asks when the answer depends on it (issue #627).
func SearchMemory(recall SearchMemoryFunc) *tools.Tool {
	return &tools.Tool{
		Name: "search_memory",
		Description: `Searches long-term memory for what is known about the operator and returns matching memories.

Use when the answer depends on the operator rather than on general
knowledge: their preferred language or stack, conventions they have
asked for, tools and infrastructure they already run, decisions they
have already made. Search before assuming a default that the operator
may have already stated.

Arguments:
- query (string, required): what to recall, in the operator's terms
  ("preferred programming language", "deployment setup").

Returns numbered memories, each with its tier (semantic for durable
facts and preferences, episodic for events, procedural for how-tos).
Zero matches is a normal answer and means nothing is recorded, not that
the operator has no preference.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "What to recall about the operator."
				}
			},
			"required": ["query"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args searchMemoryArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("search_memory: invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.Query) == "" {
				return "", fmt.Errorf("search_memory: query must not be empty")
			}
			hits, err := recall(ctx, args.Query)
			if err != nil {
				return "", fmt.Errorf("search_memory: %w", err)
			}
			return formatMemoryHits(hits), nil
		},
	}
}

func formatMemoryHits(hits []SearchMemoryHit) string {
	if len(hits) == 0 {
		return "nothing recorded for that query"
	}
	var b strings.Builder
	for i, h := range hits {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		if h.Type != "" {
			b.WriteString("[")
			b.WriteString(h.Type)
			b.WriteString("] ")
		}
		b.WriteString(h.Content)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}
