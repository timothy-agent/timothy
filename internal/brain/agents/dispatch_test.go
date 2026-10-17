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

	t.Run("decide success", func(t *testing.T) {
		decide := func(_ context.Context, kind, question, state string, options []string) (string, error) {
			if kind != "choice" {
				t.Fatalf("kind = %q, want choice", kind)
			}
			if state != "fix race condition" {
				t.Fatalf("state = %q, want message as state", state)
			}
			if len(options) != 2 || options[1] != "coding: software engineer" {
				t.Fatalf("options = %v, want rich option labels", options)
			}
			return "coding", nil
		}
		classifyCalled := false
		classify := func(context.Context, string) (string, error) {
			classifyCalled = true
			return "1", nil
		}

		got := DispatchWithDecide(context.Background(), decide, classify, "fix race condition", agentsList, "general-id")
		if got != "coding-id" {
			t.Fatalf("got = %q, want coding-id", got)
		}
		if classifyCalled {
			t.Fatal("classify was called when decide succeeded")
		}
	})

	t.Run("decide error falls back to classify", func(t *testing.T) {
		decide := func(context.Context, string, string, string, []string) (string, error) {
			return "", errors.New("decide unavailable")
		}
		classify := func(context.Context, string) (string, error) {
			return "2", nil // coding-id
		}

		got := DispatchWithDecide(context.Background(), decide, classify, "fix race condition", agentsList, "general-id")
		if got != "coding-id" {
			t.Fatalf("got = %q, want coding-id via classify fallback", got)
		}
	})

	t.Run("decide unrecognised choice falls back to classify", func(t *testing.T) {
		decide := func(context.Context, string, string, string, []string) (string, error) {
			return "unknown-role", nil
		}
		classify := func(context.Context, string) (string, error) {
			return "1", nil
		}

		got := DispatchWithDecide(context.Background(), decide, classify, "hello", agentsList, "general-id")
		if got != "general-id" {
			t.Fatalf("got = %q, want general-id via classify fallback", got)
		}
	})
}

