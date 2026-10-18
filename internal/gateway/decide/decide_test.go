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
		{"option with id: desc returns id", provider.DecideRequest{Kind: "choice", Options: []string{"coding: writes code", "general: documents"}}, "coding", "coding", false},
		{"reply with full label returns id", provider.DecideRequest{Kind: "choice", Options: []string{"coding: writes code", "general: documents"}}, "coding: writes code", "coding", false},
		{"reply with id and colon returns id", provider.DecideRequest{Kind: "choice", Options: []string{"coding: writes code", "general: documents"}}, "coding:", "coding", false},
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
