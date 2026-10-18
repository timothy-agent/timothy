package agents

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestDispatchNoCandidatesReturnsFallback(t *testing.T) {
	got := Dispatch(context.Background(), nil, "hello", nil, "general")
	if got != "general" {
		t.Fatalf("got %q, want fallback general", got)
	}
}

func TestDispatchSingleCandidateSkipsClassifier(t *testing.T) {
	called := false
	classify := func(context.Context, string) (string, error) {
		called = true
		return "1", nil
	}
	got := Dispatch(context.Background(), classify, "hello", []Agent{{ID: "solo-id", Name: "solo"}}, "general-id")
	if got != "solo-id" {
		t.Fatalf("got %q, want solo-id", got)
	}
	if called {
		t.Fatal("classify called for a single candidate; want short-circuit")
	}
}

func TestDispatchNilClassifierReturnsFallback(t *testing.T) {
	agents := []Agent{{ID: "a-id", Name: "a"}, {ID: "b-id", Name: "b"}}
	got := Dispatch(context.Background(), nil, "hello", agents, "general-id")
	if got != "general-id" {
		t.Fatalf("got %q, want fallback general-id", got)
	}
}

func TestDispatchPicksClassifiedAgent(t *testing.T) {
	agents := []Agent{
		{ID: "general-id", Name: "general", Description: "everyday tasks"},
		{ID: "researcher-id", Name: "researcher", Description: "consults tools before answering"},
		{ID: "summarizer-id", Name: "summarizer", Description: "condenses long content"},
	}
	classify := func(_ context.Context, prompt string) (string, error) {
		if len(prompt) == 0 {
			t.Fatal("empty prompt")
		}
		return "2", nil // researcher
	}
	got := Dispatch(context.Background(), classify, "what does the latest RFC say?", agents, "general-id")
	if got != "researcher-id" {
		t.Fatalf("got %q, want researcher-id", got)
	}
}

func TestDispatchFallsBackOnClassifierError(t *testing.T) {
	agents := []Agent{{ID: "a-id", Name: "a"}, {ID: "b-id", Name: "b"}}
	classify := func(context.Context, string) (string, error) {
		return "", errors.New("route unavailable")
	}
	got := Dispatch(context.Background(), classify, "hello", agents, "general-id")
	if got != "general-id" {
		t.Fatalf("got %q, want fallback general-id on classifier error", got)
	}
}

func TestDispatchFallsBackOnUnparsableReply(t *testing.T) {
	agents := []Agent{{ID: "a-id", Name: "a"}, {ID: "b-id", Name: "b"}}
	for _, reply := range []string{"", "researcher", "the second one", "0", "3", "-1"} {
		classify := func(context.Context, string) (string, error) { return reply, nil }
		got := Dispatch(context.Background(), classify, "hello", agents, "general-id")
		if got != "general-id" {
			t.Errorf("reply %q: got %q, want fallback general-id", reply, got)
		}
	}
}

func TestDispatchPromptListsAllCandidatesAndMessage(t *testing.T) {
	agents := []Agent{
		{Name: "general", Description: "everyday tasks"},
		{Name: "bare-name-only"},
	}
	p := dispatchPrompt("what's the weather", agents)
	for _, want := range []string{"1. general: everyday tasks", "2. bare-name-only", "what's the weather"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}

func TestParseChoice(t *testing.T) {
	cases := []struct {
		reply   string
		n       int
		wantIdx int
		wantOk  bool
	}{
		{"1", 3, 0, true},
		{"2", 3, 1, true},
		{"3", 3, 2, true},
		{" 2 \n", 3, 1, true},
		{"2.", 3, 1, true},
		{"2 - researcher", 3, 1, true},
		{"0", 3, 0, false},
		{"4", 3, 0, false},
		{"-1", 3, 0, false},
		{"", 3, 0, false},
		{"researcher", 3, 0, false},
		{"  ", 3, 0, false},
	}
	for _, c := range cases {
		idx, ok := parseChoice(c.reply, c.n)
		if ok != c.wantOk || (ok && idx != c.wantIdx) {
			t.Errorf("parseChoice(%q, %d) = (%d, %v), want (%d, %v)",
				c.reply, c.n, idx, ok, c.wantIdx, c.wantOk)
		}
	}
}

func TestDispatchWithDecide(t *testing.T) {
	agentsList := []Agent{
		{ID: "general-id", Name: "general", Description: "everyday tasks"},
		{ID: "coding-id", Name: "coding", Description: "software engineer"},
	}

	decideReturns := func(choice string, err error) Decide {
		return func(context.Context, string, string, string, []DecideOption) (string, error) { return choice, err }
	}

	cases := []struct {
		name         string
		decide       Decide
		candidates   []Agent
		want         string
		wantDecide   int
		wantClassify int
	}{
		{"decide picks by name", decideReturns("coding", nil), agentsList, "coding-id", 1, 0},
		{"decide picks by id", decideReturns(" CODING-ID \n", nil), agentsList, "coding-id", 1, 0},
		{"decide error returns fallback without classify", decideReturns("", errors.New("chain_exhausted")), agentsList, "fallback-id", 1, 0},
		{"decide no match returns fallback without classify", decideReturns("unknown-role", nil), agentsList, "fallback-id", 1, 0},
		{"decide empty choice returns fallback without classify", decideReturns("", nil), agentsList, "fallback-id", 1, 0},
		{"nil decide uses classify", nil, agentsList, "coding-id", 0, 1},
		{"single candidate skips decide and classify", decideReturns("general", nil), agentsList[1:], "coding-id", 0, 0},
		{"no candidates returns fallback", decideReturns("general", nil), nil, "fallback-id", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decideCalls, classifyCalls := 0, 0
			var decide Decide
			if tc.decide != nil {
				decide = func(ctx context.Context, kind, question, state string, options []DecideOption) (string, error) {
					decideCalls++
					if kind != "choice" {
						t.Errorf("kind = %q, want choice", kind)
					}
					if state != "fix race condition" {
						t.Errorf("state = %q, want message as state", state)
					}
					want := []DecideOption{{ID: "general", Description: "everyday tasks"}, {ID: "coding", Description: "software engineer"}}
					if !slices.Equal(options, want) {
						t.Errorf("options = %+v, want %+v", options, want)
					}
					return tc.decide(ctx, kind, question, state, options)
				}
			}
			classify := func(context.Context, string) (string, error) {
				classifyCalls++
				return "2", nil // coding-id
			}

			got := DispatchWithDecide(context.Background(), decide, classify, "fix race condition", tc.candidates, "fallback-id")
			if got != tc.want {
				t.Fatalf("got = %q, want %q", got, tc.want)
			}
			if decideCalls != tc.wantDecide {
				t.Fatalf("decide calls = %d, want %d", decideCalls, tc.wantDecide)
			}
			if classifyCalls != tc.wantClassify {
				t.Fatalf("classify calls = %d, want %d", classifyCalls, tc.wantClassify)
			}
		})
	}
}

// TestDispatchWithDecideColonName confirms an agent name with a colon
// goes out as one option id and its exact echo maps back to the agent.
func TestDispatchWithDecideColonName(t *testing.T) {
	candidates := []Agent{
		{ID: "team-id", Name: "team", Description: "team chat"},
		{ID: "ops-id", Name: "team:ops", Description: "runs deploys: prod"},
	}
	for _, tc := range []struct {
		choice, want string
	}{
		{"team:ops", "ops-id"},
		{"team", "team-id"},
		{"team:ops: runs deploys: prod", "fallback-id"},
	} {
		var got []DecideOption
		decide := func(_ context.Context, _, _, _ string, options []DecideOption) (string, error) {
			got = options
			return tc.choice, nil
		}
		if id := DispatchWithDecide(context.Background(), decide, nil, "ship it", candidates, "fallback-id"); id != tc.want {
			t.Fatalf("choice %q: dispatched to %q, want %q", tc.choice, id, tc.want)
		}
		want := []DecideOption{{ID: "team", Description: "team chat"}, {ID: "team:ops", Description: "runs deploys: prod"}}
		if !slices.Equal(got, want) {
			t.Fatalf("options = %+v, want %+v", got, want)
		}
	}
}
