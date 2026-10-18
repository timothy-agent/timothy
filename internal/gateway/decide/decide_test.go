package decide

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// ids builds options that carry only an id.
func ids(names ...string) []provider.DecideOption {
	opts := make([]provider.DecideOption, len(names))
	for i, n := range names {
		opts[i] = provider.DecideOption{ID: n}
	}
	return opts
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		req      provider.DecideRequest
		wantErr  string
		wantOpts []provider.DecideOption
	}{
		{"choice keeps distinct trimmed options", provider.DecideRequest{Kind: "choice", Question: "q", Options: ids(" coding ", "research", "Coding", "")}, "", ids("coding", "research")},
		{"choice dedupes on id, first description wins", provider.DecideRequest{Kind: "choice", Question: "q", Options: []provider.DecideOption{
			{ID: "coding", Description: " writes code "}, {ID: "CODING", Description: "other text"}, {ID: "research", Description: "finds sources"},
		}}, "", []provider.DecideOption{{ID: "coding", Description: "writes code"}, {ID: "research", Description: "finds sources"}}},
		{"same description on distinct ids is kept", provider.DecideRequest{Kind: "choice", Question: "q", Options: []provider.DecideOption{
			{ID: "a", Description: "same"}, {ID: "b", Description: "same"},
		}}, "", []provider.DecideOption{{ID: "a", Description: "same"}, {ID: "b", Description: "same"}}},
		{"colon in id is kept verbatim", provider.DecideRequest{Kind: "choice", Question: "q", Options: []provider.DecideOption{
			{ID: "openai:gpt-5", Description: "chat"}, {ID: "openai:gpt-5-mini"},
		}}, "", []provider.DecideOption{{ID: "openai:gpt-5", Description: "chat"}, {ID: "openai:gpt-5-mini"}}},
		{"description without id is dropped", provider.DecideRequest{Kind: "choice", Question: "q", Options: []provider.DecideOption{
			{Description: "orphan"}, {ID: "a"}, {ID: "b"},
		}}, "", ids("a", "b")},
		{"choice needs two options", provider.DecideRequest{Kind: "choice", Question: "q", Options: ids("only")}, "at least two", nil},
		{"choice needs two distinct ids", provider.DecideRequest{Kind: "choice", Question: "q", Options: []provider.DecideOption{{ID: "a", Description: "x"}, {ID: "A", Description: "y"}}}, "at least two", nil},
		{"yesno gets the fixed pair", provider.DecideRequest{Kind: "yesno", Question: "q"}, "", ids("yes", "no")},
		{"yesno rejects options", provider.DecideRequest{Kind: "yesno", Question: "q", Options: ids("yes")}, "takes no options", nil},
		{"score is an unknown kind", provider.DecideRequest{Kind: "score", Question: "q"}, `unknown kind "score"`, nil},
		{"unknown kind", provider.DecideRequest{Kind: "rank", Question: "q"}, `unknown kind "rank"`, nil},
		{"empty question", provider.DecideRequest{Kind: "yesno", Question: "  "}, "question is required", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := tc.req
			err := Validate(&req)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !slices.Equal(req.Options, tc.wantOpts) {
				t.Fatalf("options = %+v, want %+v", req.Options, tc.wantOpts)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	choice := provider.DecideRequest{Kind: "choice", Options: ids("coding", "research", "general")}
	described := provider.DecideRequest{Kind: "choice", Options: []provider.DecideOption{
		{ID: "coding", Description: "writes code"}, {ID: "general", Description: "documents"},
	}}
	colon := provider.DecideRequest{Kind: "choice", Options: []provider.DecideOption{
		{ID: "openai", Description: "any OpenAI model"},
		{ID: "openai:gpt-5", Description: "flagship"},
		{ID: "OpenAI:GPT-5-mini", Description: "cheap: fast"},
	}}
	period := provider.DecideRequest{Kind: "choice", Options: []provider.DecideOption{
		{ID: "general", Description: "Everyday questions."}, {ID: "coding", Description: "Writes code."},
	}}
	yesno := provider.DecideRequest{Kind: "yesno", Options: ids("yes", "no")}
	tests := []struct {
		name       string
		req        provider.DecideRequest
		text       string
		wantChoice string
		malformed  bool
	}{
		{"exact option", choice, "research", "research", false},
		{"case and whitespace", choice, "  Coding\n", "coding", false},
		{"quoted with period", choice, `"general".`, "general", false},
		{"markdown bold", choice, "**research**", "research", false},
		{"prose around the option is malformed", choice, "I think research fits best", "", true},
		{"two options is malformed", choice, "coding, research", "", true},
		{"described option matches on id", described, "coding", "coding", false},
		{"reply echoing the prompt line returns id", described, "Coding: writes code", "coding", false},
		{"echoed prompt line whose description ends in a period", period, "**general: Everyday questions.**", "general", false},
		{"description alone is malformed", described, "writes code", "", true},
		{"id with trailing colon is malformed", described, "coding:", "", true},
		{"colon id round-trips", colon, "openai:gpt-5", "openai:gpt-5", false},
		{"colon id is not cut to its prefix", colon, "openai", "openai", false},
		{"colon id case-insensitive returns id as given", colon, "openai:gpt-5-MINI", "OpenAI:GPT-5-mini", false},
		{"colon id quoted with period", colon, "`openai:gpt-5`.", "openai:gpt-5", false},
		{"colon id echoing the prompt line", colon, "OpenAI:GPT-5-mini: cheap: fast", "OpenAI:GPT-5-mini", false},
		{"unknown colon id is malformed", colon, "openai:gpt-4", "", true},
		{"yes", yesno, "Yes", "yes", false},
		{"maybe is malformed", yesno, "maybe", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := Parse(tc.req, tc.text)
			if tc.malformed {
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("err = %v, want ErrMalformed", err)
				}
				if res.Choice != "" {
					t.Fatalf("malformed reply produced a result %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if res.Choice != tc.wantChoice {
				t.Fatalf("choice = %q, want %q", res.Choice, tc.wantChoice)
			}
		})
	}
}

// TestParseErrorTruncatesReply keeps a chatty reply short in the error.
func TestParseErrorTruncatesReply(t *testing.T) {
	t.Parallel()
	_, err := Parse(provider.DecideRequest{Kind: "yesno", Options: ids("yes", "no")}, strings.Repeat("x", 200))
	if err == nil || len(err.Error()) > 160 {
		t.Fatalf("err = %v, want a bounded message", err)
	}
}

func TestPromptListsOptions(t *testing.T) {
	t.Parallel()
	system, user := Prompt(provider.DecideRequest{Kind: "choice", Question: "Which kind?", Options: []provider.DecideOption{
		{ID: "coding", Description: "writes code"}, {ID: "research"}, {ID: "openai:gpt-5", Description: "flagship"},
	}})
	if !strings.Contains(system, "exactly one of the allowed option ids") {
		t.Fatalf("system = %q", system)
	}
	for _, want := range []string{"Which kind?", "- coding: writes code\n", "- research\n", "- openai:gpt-5: flagship\n"} {
		if !strings.Contains(user, want) {
			t.Fatalf("user prompt %q lacks %q", user, want)
		}
	}
}

// scriptedProvider records the completion it got and replays events.
type scriptedProvider struct {
	got    provider.CompletionRequest
	events []stream.StreamEvent
	err    error
}

func (p *scriptedProvider) Name() string        { return "scripted" }
func (p *scriptedProvider) Kind() provider.Kind { return provider.KindAPI }
func (p *scriptedProvider) Capabilities() []provider.Capability {
	return []provider.Capability{provider.CapChat}
}
func (p *scriptedProvider) Stream(_ context.Context, req provider.CompletionRequest) (<-chan stream.StreamEvent, error) {
	p.got = req
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan stream.StreamEvent, len(p.events))
	for _, ev := range p.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func chunks(texts ...string) []stream.StreamEvent {
	var evs []stream.StreamEvent
	for _, t := range texts {
		evs = append(evs, stream.StreamEvent{Type: stream.EventChunk, Text: t})
	}
	evs = append(evs,
		stream.StreamEvent{Type: stream.EventUsage, Usage: &stream.Usage{InputTokens: 10, OutputTokens: 2}},
		stream.StreamEvent{Type: stream.EventDone})
	return evs
}

// TestViaChatRunsLowEffortAndParses pins the llm fallback's request
// shape: low effort, a thinking budget, the per-entry reasoning
// override, and chunks joined before parsing.
func TestViaChatRunsLowEffortAndParses(t *testing.T) {
	t.Parallel()
	p := &scriptedProvider{events: append([]stream.StreamEvent{{Type: stream.EventReasoningChunk, Text: "hmm"}}, chunks("res", "earch")...)}
	req := provider.DecideRequest{Kind: "choice", Question: "Which kind?", Options: ids("coding", "research")}

	res, usage, err := ViaChat(t.Context(), p, "m1", req, true, "none")
	if err != nil {
		t.Fatalf("ViaChat: %v", err)
	}
	if res.Choice != "research" {
		t.Fatalf("choice = %q", res.Choice)
	}
	if usage == nil || usage.InputTokens != 10 {
		t.Fatalf("usage = %+v", usage)
	}
	if p.got.Effort != "low" || p.got.MaxTokens != maxTokens || p.got.Model != "m1" || !p.got.FinalAttempt || p.got.ReasoningEffortOverride != "none" {
		t.Fatalf("completion = %+v", p.got)
	}
	if len(p.got.Messages) != 1 || !strings.Contains(p.got.Messages[0].Content, "- coding") {
		t.Fatalf("messages = %+v", p.got.Messages)
	}
}

func TestViaChatMalformedKeepsUsage(t *testing.T) {
	t.Parallel()
	p := &scriptedProvider{events: chunks("Probably coding, but research also fits.")}
	req := provider.DecideRequest{Kind: "choice", Question: "q", Options: ids("coding", "research")}

	res, usage, err := ViaChat(t.Context(), p, "m1", req, false, "")
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	if res.Choice != "" {
		t.Fatalf("choice = %q, want none", res.Choice)
	}
	if usage == nil || usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v, want the spend kept for the ledger", usage)
	}
}

func TestViaChatRelaysProviderErrors(t *testing.T) {
	t.Parallel()
	p := &scriptedProvider{events: []stream.StreamEvent{{Type: stream.EventError, Err: &stream.StreamError{Code: "http_500", Message: "boom"}}}}
	_, _, err := ViaChat(t.Context(), p, "m1", provider.DecideRequest{Kind: "yesno", Question: "q", Options: ids("yes", "no")}, false, "")
	if err == nil || !strings.Contains(err.Error(), "http_500") || errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want the provider error", err)
	}

	p = &scriptedProvider{err: errors.New("bad request")}
	if _, _, err := ViaChat(t.Context(), p, "m1", provider.DecideRequest{Kind: "yesno", Question: "q", Options: ids("yes", "no")}, false, ""); err == nil {
		t.Fatal("immediate Stream error not relayed")
	}
}
