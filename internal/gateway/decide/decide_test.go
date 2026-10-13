package decide

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		req      provider.DecideRequest
		wantErr  string
		wantOpts []string
	}{
		{"choice keeps distinct trimmed options", provider.DecideRequest{Kind: "choice", Question: "q", Options: []string{" coding ", "research", "Coding", ""}}, "", []string{"coding", "research"}},
		{"choice needs two options", provider.DecideRequest{Kind: "choice", Question: "q", Options: []string{"only"}}, "at least two", nil},
		{"yesno gets the fixed pair", provider.DecideRequest{Kind: "yesno", Question: "q"}, "", []string{"yes", "no"}},
		{"yesno rejects options", provider.DecideRequest{Kind: "yesno", Question: "q", Options: []string{"yes"}}, "takes no options", nil},
		{"score has no options", provider.DecideRequest{Kind: "score", Question: "q"}, "", nil},
		{"score rejects options", provider.DecideRequest{Kind: "score", Question: "q", Options: []string{"a", "b"}}, "takes no options", nil},
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
			if strings.Join(req.Options, ",") != strings.Join(tc.wantOpts, ",") {
				t.Fatalf("options = %v, want %v", req.Options, tc.wantOpts)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	choice := provider.DecideRequest{Kind: "choice", Options: []string{"coding", "research", "general"}}
	yesno := provider.DecideRequest{Kind: "yesno", Options: []string{"yes", "no"}}
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
		{"empty is malformed", choice, "", "", 0, true},
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
	_, err := Parse(provider.DecideRequest{Kind: "yesno", Options: []string{"yes", "no"}}, strings.Repeat("x", 200))
	if err == nil || len(err.Error()) > 160 {
		t.Fatalf("err = %v, want a bounded message", err)
	}
}

func TestPromptListsOptions(t *testing.T) {
	t.Parallel()
	system, user := Prompt(provider.DecideRequest{Kind: "choice", Question: "Which kind?", Options: []string{"coding", "research"}})
	if !strings.Contains(system, "exactly one of the allowed options") {
		t.Fatalf("system = %q", system)
	}
	for _, want := range []string{"Which kind?", "- coding\n", "- research\n"} {
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
	req := provider.DecideRequest{Kind: "choice", Question: "Which kind?", Options: []string{"coding", "research"}}

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
	req := provider.DecideRequest{Kind: "choice", Question: "q", Options: []string{"coding", "research"}}

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
	_, _, err := ViaChat(t.Context(), p, "m1", provider.DecideRequest{Kind: "yesno", Question: "q", Options: []string{"yes", "no"}}, false, "")
	if err == nil || !strings.Contains(err.Error(), "http_500") || errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want the provider error", err)
	}

	p = &scriptedProvider{err: errors.New("bad request")}
	if _, _, err := ViaChat(t.Context(), p, "m1", provider.DecideRequest{Kind: "yesno", Question: "q", Options: []string{"yes", "no"}}, false, ""); err == nil {
		t.Fatal("immediate Stream error not relayed")
	}
}

// TestPromptRendersContextAndDescriptions: the context precedes the
// question and an option carries its description when given.
func TestPromptRendersContextAndDescriptions(t *testing.T) {
	t.Parallel()
	_, user := Prompt(provider.DecideRequest{
		Kind: "choice", Question: "Which kind?", Context: "Survey vector databases",
		Options:      []string{"coding", "research"},
		Descriptions: map[string]string{"research": "gathers and compares information"},
	})
	if !strings.HasPrefix(user, "Context:\nSurvey vector databases\n\nWhich kind?") {
		t.Fatalf("user = %q", user)
	}
	if !strings.Contains(user, "- coding\n") || !strings.Contains(user, "- research: gathers and compares information\n") {
		t.Fatalf("user = %q", user)
	}
}
