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

// Decide picks one option among options for question on a decision model.
type Decide func(ctx context.Context, question string, options []string) (string, error)

// Dispatch picks the agent whose profile best fits message, among
// candidates (only enabled agents should be passed in), and returns
// its id. A single candidate is returned without calling classify:
// there's nothing to choose between. Any classifier failure or an
// out-of-range/unparsable answer falls back to fallbackID (typically
// the default agent's id): dispatch is an ergonomics layer, never a
// hard gate on serving a session.
func Dispatch(ctx context.Context, classify Classify, message string, candidates []Agent, fallbackID string) string {
	return DispatchWithDecide(ctx, nil, classify, message, candidates, fallbackID)
}

// DispatchWithDecide picks the agent using decide when available, falling
// back to classify when decide is nil, unbound, or on error.
func DispatchWithDecide(ctx context.Context, decide Decide, classify Classify, message string, candidates []Agent, fallbackID string) string {
	if len(candidates) == 0 {
		return fallbackID
	}
	if len(candidates) == 1 {
		return candidates[0].ID
	}
	if decide != nil {
		var list strings.Builder
		options := make([]string, len(candidates))
		for i, a := range candidates {
			options[i] = a.Name
			if a.Description != "" {
				fmt.Fprintf(&list, "- %s: %s\n", a.Name, a.Description)
			} else {
				fmt.Fprintf(&list, "- %s\n", a.Name)
			}
		}
		q := fmt.Sprintf("Pick the single best-fit assistant profile for the user's message.\nProfiles:\n%s\nMessage: %s", list.String(), message)
		choice, err := decide(ctx, q, options)
		if err == nil {
			choice = strings.TrimSpace(choice)
			for _, a := range candidates {
				if strings.EqualFold(choice, a.Name) || choice == a.ID || strings.HasPrefix(strings.ToLower(choice), strings.ToLower(a.Name)+":") {
					return a.ID
				}
			}
		}
	}
	if classify == nil {
		return fallbackID
	}

	reply, err := classify(ctx, dispatchPrompt(message, candidates))
	if err != nil {
		return fallbackID
	}
	idx, ok := parseChoice(reply, len(candidates))
	if !ok {
		return fallbackID
	}
	return candidates[idx].ID
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
