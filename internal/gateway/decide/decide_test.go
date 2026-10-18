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
		{"score has no options", provider.DecideRequest{Kind: "score", Question: "q"}, "", nil},
		{"score rejects options", provider.DecideRequest{Kind: "score", Question: "q", Options: ids("a", "b")}, "takes no options", nil},
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
	yesno := provider.DecideRequest{Kind: "yesno", Options: ids("yes", "no")}
	score := provider.DecideRequest{Kind: "score"}
	tests := []struct {
		name       string
		req        provider.DecideRequest
		text       string
		wantChoice string
		wantScore  float64
		malformed  bool
	}{
		{"exact option", choice, "research", "research", 0, false},
		{"case and whitespace", choice, "  Coding\n", "coding", 0, false},
		{"quoted with period", choice, `"general".`, "general", 0, false},
		{"markdown bold", choice, "**research**", "research", 0, false},
		{"prose around the option is malformed", choice, "I think research fits best", "", 0, true},
		{"two options is malformed", choice, "coding, research", "", 0, true},
		{"described option matches on id", described, "coding", "coding", 0, false},
		{"reply echoing the prompt line returns id", described, "Coding: writes code", "coding", 0, false},
		{"description alone is malformed", described, "writes code", "", 0, true},
		{"id with trailing colon is malformed", described, "coding:", "", 0, true},
		{"colon id round-trips", colon, "openai:gpt-5", "openai:gpt-5", 0, false},
		{"colon id is not cut to its prefix", colon, "openai", "openai", 0, false},
		{"colon id case-insensitive returns id as given", colon, "openai:gpt-5-MINI", "OpenAI:GPT-5-mini", 0, false},
		{"colon id quoted with period", colon, "`openai:gpt-5`.", "openai:gpt-5", 0, false},
		{"colon id echoing the prompt line", colon, "OpenAI:GPT-5-mini: cheap: fast", "OpenAI:GPT-5-mini", 0, false},
		{"unknown colon id is malformed", colon, "openai:gpt-4", "", 0, true},
		{"yes", yesno, "Yes", "yes", 0, false},
		{"maybe is malformed", yesno, "maybe", "", 0, true},
		{"score", score, "0.75", "", 0.75, false},
		{"score bounds", score, "1.0", "", 1, false},
		{"score above one is malformed", score, "1.5", "", 0, true},
		{"score percent is malformed", score, "75%", "", 0, true},
		{"score prose is malformed", score, "about 0.5", "", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := Parse(tc.req, tc.text)
			if tc.malformed {
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("err = %v, want ErrMalformed", err)
				}
				if res.Choice != "" || res.Score != nil {
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
			if tc.req.Kind == "score" && (res.Score == nil || *res.Score != tc.wantScore) {
				t.Fatalf("score = %v, want %v", res.Score, tc.wantScore)
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
	system, user = Prompt(provider.DecideRequest{Kind: "score", Question: "How relevant?"})
	if !strings.Contains(system, "between 0 and 1") || strings.Contains(user, "Allowed options") {
		t.Fatalf("score prompt = %q / %q", system, user)
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
