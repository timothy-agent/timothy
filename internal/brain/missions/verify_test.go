package missions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVerifySuccess(t *testing.T) {
	res, err := RunVerify(context.Background(), t.TempDir(), "true")
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	if !res.Passed || res.ExitCode != 0 {
		t.Fatalf("RunVerify(true) = %+v, want passed with exit 0", res)
	}
}

func TestRunVerifyFailure(t *testing.T) {
	res, err := RunVerify(context.Background(), t.TempDir(), "false")
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	if res.Passed || res.ExitCode != 1 {
		t.Fatalf("RunVerify(false) = %+v, want failed with exit 1", res)
	}
}

func TestRunVerifyExitCode(t *testing.T) {
	res, err := RunVerify(context.Background(), t.TempDir(), "exit 3")
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	if res.Passed || res.ExitCode != 3 {
		t.Fatalf("RunVerify(exit 3) = %+v, want failed with exit 3", res)
	}
}

func TestRunVerifyDigestCorrectness(t *testing.T) {
	res, err := RunVerify(context.Background(), t.TempDir(), "printf hello")
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	// sha256("hello")
	const wantDigest = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if res.OutputSHA256 != wantDigest {
		t.Fatalf("digest = %q, want %q", res.OutputSHA256, wantDigest)
	}
}

func TestRunVerifyExcerptTruncatesFromEnd(t *testing.T) {
	// Produce output well over the excerpt cap; the excerpt must be the
	// TRAILING slice, not the head.
	res, err := RunVerify(context.Background(), t.TempDir(), `for i in $(seq 1 5000); do echo "line $i"; done`)
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	if len(res.Excerpt) > verifyExcerptCap {
		t.Fatalf("excerpt length %d exceeds cap %d", len(res.Excerpt), verifyExcerptCap)
	}
	if !strings.Contains(res.Excerpt, "line 5000") {
		t.Fatalf("excerpt does not contain the LAST line of output: %q", res.Excerpt[:min(200, len(res.Excerpt))])
	}
	if strings.Contains(res.Excerpt, "line 1\n") {
		t.Fatal("excerpt contains the first line — truncation kept the head instead of the tail")
	}
}

func TestCheckArtifacts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "summary.md"), []byte("429 means Too Many Requests"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "empty.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o750); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		artifacts []string
		problems  int
	}{
		{"present and non-empty passes", []string{"summary.md"}, 0},
		{"missing file fails", []string{"nope.md"}, 1},
		{"empty file fails", []string{"empty.md"}, 1},
		{"directory fails", []string{"adir"}, 1},
		{"absolute path fails", []string{"/etc/passwd"}, 1},
		{"escape via .. fails", []string{"../outside.md"}, 1},
		{"blank entries skipped", []string{"", "  ", "summary.md"}, 0},
		{"one good one bad reports only the bad", []string{"summary.md", "nope.md"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := CheckArtifacts(root, tc.artifacts)
			if len(problems) != tc.problems {
				t.Fatalf("CheckArtifacts(%v) = %v, want %d problem(s)", tc.artifacts, problems, tc.problems)
			}
		})
	}
}

// TestCheckArtifactsSymlinkEscape confirms a symlink that resolves
// outside workRoot is caught by the WithinRoot check, not blindly
// os.Stat'd (which would follow it and report the artifact as fine).
func TestCheckArtifactsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(target, []byte("outside content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "escape.md")); err != nil {
		t.Fatal(err)
	}

	problems := CheckArtifacts(root, []string{"escape.md"})
	if len(problems) != 1 {
		t.Fatalf("CheckArtifacts(symlink escape) = %v, want exactly 1 problem", problems)
	}
	if !strings.Contains(problems[0], "escapes the workspace") {
		t.Fatalf("problem = %q, want it to mention escaping the workspace", problems[0])
	}
}
