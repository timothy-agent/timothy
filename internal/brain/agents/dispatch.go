package agents

import (
	"context"
	"fmt"
	"strings"
)

// Classify sends prompt to a cheap model and returns its raw text
// response. Callers supply this over a route (e.g. gwclient against a
// "default"-strategy chain) — the package stays free of any gateway
// dependency so the prompt-building and response-parsing logic is
// unit-testable without a live model.
type Classify func(ctx context.Context, prompt string) (string, error)

// Dispatch picks the agent whose profile best fits message, among
// candidates (only enabled agents should be passed in). A single
// candidate is returned without calling classify — there's nothing to
// choose between. Any classifier failure or an out-of-range/unparsable
// answer falls back to fallbackName (typically the default agent):
// dispatch is an ergonomics layer, never a hard gate on serving a
// session.
func Dispatch(ctx context.Context, classify Classify, message string, candidates []Agent, fallbackName string) string {
	if len(candidates) == 0 {
		return fallbackName
	}
	if len(candidates) == 1 {
		return candidates[0].Name
	}
	if classify == nil {
		return fallbackName
	}

	reply, err := classify(ctx, dispatchPrompt(message, candidates))
	if err != nil {
		return fallbackName
	}
	idx, ok := parseChoice(reply, len(candidates))
	if !ok {
		return fallbackName
	}
	return candidates[idx].Name
}

// dispatchPrompt lists each candidate as "N. name: description" (or
// just "N. name" when the agent has no description) and asks for a
// single number back — a closed choice is far more reliable to parse
// from a small/cheap model than free-form agent-name text.
func dispatchPrompt(message string, candidates []Agent) string {
	var b strings.Builder
	b.WriteString("Pick the single best-fit assistant profile for the user's message. ")
	b.WriteString("Reply with ONLY the number, nothing else.\n\n")
	for i, a := range candidates {
		if a.Description != "" {
			fmt.Fprintf(&b, "%d. %s: %s\n", i+1, a.Name, a.Description)
		} else {
			fmt.Fprintf(&b, "%d. %s\n", i+1, a.Name)
		}
	}
	b.WriteString("\nMessage: ")
	b.WriteString(message)
	return b.String()
}

// parseChoice extracts a 1-based choice number from an LLM reply and
// returns its 0-based index. Tolerates surrounding whitespace and
// punctuation ("2", "2.", " 2 \n") but never guesses past the first
// number it finds — a model padding its answer with prose still
// resolves cleanly as long as a number leads.
func parseChoice(reply string, n int) (int, bool) {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return 0, false
	}
	// Take the leading run of ASCII digits only; anything else (a
	// model that answered with the agent's name instead of a number)
	// is treated as unparsable rather than guessed at.
	end := 0
	for end < len(reply) && reply[end] >= '0' && reply[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	var choice int
	if _, err := fmt.Sscanf(reply[:end], "%d", &choice); err != nil {
		return 0, false
	}
	if choice < 1 || choice > n {
		return 0, false
	}
	return choice - 1, true
}
