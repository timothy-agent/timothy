// Package decide answers fixed-choice questions on the gateway (D-163):
// request validation shared by every driver, and the llm fallback that
// serves a decide route through any chat provider when no decision
// model is configured.
package decide

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// ErrMalformed reports a model reply that is not one of the allowed
// answers. It is never resolved to a guessed option.
var ErrMalformed = errors.New("decide: malformed reply")

// yesNo is the fixed option set every yesno question carries.
var yesNo = []provider.DecideOption{{ID: "yes"}, {ID: "no"}}

// Validate normalizes req in place: trims options and dedupes them on
// id case-insensitively, fixes the yesno option set, and rejects a
// shape no driver could answer.
func Validate(req *provider.DecideRequest) error {
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" {
		return errors.New("decide: question is required")
	}
	switch req.Kind {
	case provider.DecideChoice:
		var opts []provider.DecideOption
		seen := map[string]bool{}
		for _, o := range req.Options {
			o.ID = strings.TrimSpace(o.ID)
			o.Description = strings.TrimSpace(o.Description)
			key := strings.ToLower(o.ID)
			if o.ID == "" || seen[key] {
				continue
			}
			seen[key] = true
			opts = append(opts, o)
		}
		if len(opts) < 2 {
			return errors.New("decide: a choice needs at least two distinct options")
		}
		req.Options = opts
	case provider.DecideYesNo:
		if len(req.Options) != 0 {
			return errors.New("decide: a yesno question takes no options")
		}
		req.Options = append([]provider.DecideOption(nil), yesNo...)
	default:
		return fmt.Errorf("decide: unknown kind %q", req.Kind)
	}
	return nil
}

// Prompt renders the question for a chat model. The closed option
// list is restated at the end, one "id: description" line per option,
// so a small model answers from it rather than from the question text.
func Prompt(req provider.DecideRequest) (system, user string) {
	var b strings.Builder
	b.WriteString(req.Question)
	if req.State != "" {
		b.WriteString("\n\n")
		b.WriteString(req.State)
	}
	b.WriteString("\n\n")
	system = "You are a classifier. Reply with exactly one of the allowed option ids, spelled as given, and nothing else."
	b.WriteString("Allowed options:\n")
	for _, o := range req.Options {
		b.WriteString("- ")
		b.WriteString(o.Label())
		b.WriteString("\n")
	}
	b.WriteString("\nReply with one option id.")
	return system, b.String()
}

// maxTokens includes a reasoning model's thinking tokens; 512 leaves
// room to think before the one-word answer.
const maxTokens = 512

// ViaChat is the llm fallback: it runs req on a chat provider at low
// effort and parses the text reply into a typed result. The usage is
// returned even when parsing fails so the caller can book the spend.
func ViaChat(ctx context.Context, p provider.Provider, model string, req provider.DecideRequest, finalAttempt bool, reasoningEffort string) (provider.DecideResult, *stream.Usage, error) {
	system, user := Prompt(req)
	ch, err := p.Stream(ctx, provider.CompletionRequest{
		Model:                   model,
		System:                  system,
		Messages:                []provider.Message{{Role: "user", Content: user}},
		MaxTokens:               maxTokens,
		Effort:                  "low",
		FinalAttempt:            finalAttempt,
		ReasoningEffortOverride: reasoningEffort,
	})
	if err != nil {
		return provider.DecideResult{}, nil, err
	}
	var (
		text  strings.Builder
		usage *stream.Usage
	)
	for ev := range ch {
		switch ev.Type {
		case stream.EventChunk:
			text.WriteString(ev.Text)
		case stream.EventUsage:
			usage = ev.Usage
		case stream.EventError:
			return provider.DecideResult{}, usage, fmt.Errorf("decide: %s: %s", ev.Err.Code, ev.Err.Message)
		}
	}
	res, err := Parse(req, text.String())
	return res, usage, err
}

// Parse maps a chat model's reply onto the request's answer set. After
// trimming whitespace, quotes, emphasis and periods, a choice must
// equal one option's id case-insensitively, or echo one option's full
// prompt line; the result is that option's id as given. Anything else
// is ErrMalformed.
func Parse(req provider.DecideRequest, text string) (provider.DecideResult, error) {
	answer := strings.Trim(text, " \t\r\n\"'`*.")
	for _, o := range req.Options {
		if strings.EqualFold(answer, o.ID) {
			return provider.DecideResult{Choice: o.ID}, nil
		}
	}
	for _, o := range req.Options {
		if strings.EqualFold(answer, o.Label()) {
			return provider.DecideResult{Choice: o.ID}, nil
		}
	}
	return provider.DecideResult{}, fmt.Errorf("%w: %q is not an allowed option", ErrMalformed, truncate(text))
}

// truncate bounds a reply quoted in an error: a chatty model can
// return paragraphs.
func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
