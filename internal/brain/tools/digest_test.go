package tools

import (
	"fmt"
	"strings"
	"testing"
)

func TestDigest(t *testing.T) {
	t.Parallel()
	var lines []string
	for i := 1; i <= 500; i++ {
		lines = append(lines, fmt.Sprintf("line %d ok", i))
	}
	lines[249] = "line 250 ERROR: connection refused"
	content := strings.Join(lines, "\n")

	got := Digest(content, "shell", "9be4c1d2-04a7-47a1-a1a9-3f6d2c9f1e10")

	for _, want := range []string{
		"500 lines",
		"retrieve_output TOOL",
		`ref="9be4c1d2-04a7-47a1-a1a9-3f6d2c9f1e10"`,
		"line 1 ok",   // head
		"line 500 ok", // tail
		"460 lines omitted",
		"line 250: line 250 ERROR: connection refused",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("digest missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "line 100 ok") {
		t.Fatal("digest leaked middle content")
	}
	// The digest itself must be small.
	if len(got) > DefaultOffloadThreshold {
		t.Fatalf("digest is %d bytes — bigger than the offload threshold", len(got))
	}
}

func TestDigestShortContentNoOmission(t *testing.T) {
	t.Parallel()
	got := Digest("a\nb\nc", "shell", "ref-1")
	if strings.Contains(got, "omitted") {
		t.Fatalf("short content claims omission:\n%s", got)
	}
	for _, want := range []string{"a", "b", "c", "3 lines"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestDigestCapsPathologicalLines(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("x", 100_000) // one giant line
	got := Digest(content, "shell", "ref-1")
	if len(got) > 2000 {
		t.Fatalf("digest of one long line is %d bytes", len(got))
	}
	if !strings.Contains(got, "…") {
		t.Fatal("long line not visibly truncated")
	}
}

func TestArgsDigestStable(t *testing.T) {
	t.Parallel()
	a := ArgsDigest([]byte(`{"command":"ls"}`))
	b := ArgsDigest([]byte(`{"command":"ls"}`))
	c := ArgsDigest([]byte(`{"command":"rm"}`))
	if a != b || a == c || len(a) != 16 {
		t.Fatalf("digests: %s %s %s", a, b, c)
	}
}
