package agents

import (
	"context"
	"errors"
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
	got := Dispatch(context.Background(), classify, "hello", []Agent{{Name: "solo"}}, "general")
	if got != "solo" {
		t.Fatalf("got %q, want solo", got)
	}
	if called {
		t.Fatal("classify called for a single candidate; want short-circuit")
	}
}

func TestDispatchNilClassifierReturnsFallback(t *testing.T) {
	agents := []Agent{{Name: "a"}, {Name: "b"}}
	got := Dispatch(context.Background(), nil, "hello", agents, "general")
	if got != "general" {
		t.Fatalf("got %q, want fallback general", got)
	}
}

func TestDispatchPicksClassifiedAgent(t *testing.T) {
	agents := []Agent{
		{Name: "general", Description: "everyday tasks"},
		{Name: "researcher", Description: "consults tools before answering"},
		{Name: "summarizer", Description: "condenses long content"},
	}
	classify := func(_ context.Context, prompt string) (string, error) {
		if len(prompt) == 0 {
			t.Fatal("empty prompt")
		}
		return "2", nil // researcher
	}
	got := Dispatch(context.Background(), classify, "what does the latest RFC say?", agents, "general")
	if got != "researcher" {
		t.Fatalf("got %q, want researcher", got)
	}
}

func TestDispatchFallsBackOnClassifierError(t *testing.T) {
	agents := []Agent{{Name: "a"}, {Name: "b"}}
	classify := func(context.Context, string) (string, error) {
		return "", errors.New("route unavailable")
	}
	got := Dispatch(context.Background(), classify, "hello", agents, "general")
	if got != "general" {
		t.Fatalf("got %q, want fallback general on classifier error", got)
	}
}

func TestDispatchFallsBackOnUnparsableReply(t *testing.T) {
	agents := []Agent{{Name: "a"}, {Name: "b"}}
	for _, reply := range []string{"", "researcher", "the second one", "0", "3", "-1"} {
		classify := func(context.Context, string) (string, error) { return reply, nil }
		got := Dispatch(context.Background(), classify, "hello", agents, "general")
		if got != "general" {
			t.Errorf("reply %q: got %q, want fallback general", reply, got)
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
		reply    string
		n        int
		wantIdx  int
		wantOk   bool
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
