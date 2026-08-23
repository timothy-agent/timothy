package workflows

import (
	"strings"
	"testing"
)

func TestInterpolateOutcome(t *testing.T) {
	got, unknown := interpolate("review this: {{outcome}}", "the digest", nil)
	if got != "review this: the digest" {
		t.Fatalf("got %q", got)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown = %v, want none", unknown)
	}
}

func TestInterpolateContext(t *testing.T) {
	got, unknown := interpolate("do {{context.TASK}} for {{context.REPO}}", "", map[string]string{"TASK": "fix bug", "REPO": "acme/app"})
	if got != "do fix bug for acme/app" {
		t.Fatalf("got %q", got)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown = %v, want none", unknown)
	}
}

func TestInterpolateUnknownContextKeyRendersEmpty(t *testing.T) {
	got, unknown := interpolate("do {{context.MISSING}} now", "", map[string]string{"OTHER": "x"})
	if got != "do  now" {
		t.Fatalf("got %q, want placeholder rendered empty", got)
	}
	if len(unknown) != 1 || unknown[0] != "MISSING" {
		t.Fatalf("unknown = %v, want [MISSING]", unknown)
	}
}

func TestInterpolateOutcomeTruncated(t *testing.T) {
	long := strings.Repeat("x", outcomeTruncateRunes+500)
	got, _ := interpolate("{{outcome}}", long, nil)
	if len([]rune(got)) != outcomeTruncateRunes {
		t.Fatalf("len(got) = %d, want %d", len([]rune(got)), outcomeTruncateRunes)
	}
}

func TestInterpolateNoPlaceholders(t *testing.T) {
	got, unknown := interpolate("plain goal, no placeholders", "outcome", map[string]string{"K": "v"})
	if got != "plain goal, no placeholders" {
		t.Fatalf("got %q", got)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown = %v, want none", unknown)
	}
}
