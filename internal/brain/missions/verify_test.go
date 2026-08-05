package missions

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// fakeVerifyBackend writes fixed output to whatever writer it's given
// and returns exitCode — a stand-in for sandbox.Manager.Exec.
func fakeVerifyBackend(output string, exitCode int, err error) verifyBackend {
	return func(ctx context.Context, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		if err != nil {
			return 0, err
		}
		if _, werr := out.Write([]byte(output)); werr != nil {
			return 0, werr
		}
		return exitCode, nil
	}
}

// TestRunVerifyWithBackendDigestCorrectness confirms the streamed
// evidence (digest, excerpt, passed) matches what's actually written
// by the backend, byte for byte — the sha256 hash and tail buffer must
// see exactly what a full CombinedOutput() collection would have.
func TestRunVerifyWithBackendDigestCorrectness(t *testing.T) {
	const output = "hello"
	// sha256("hello")
	const wantDigest = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

	got, err := RunVerifyWithBackend(context.Background(), fakeVerifyBackend(output, 0, nil), "/workspace", "printf hello")
	if err != nil {
		t.Fatalf("RunVerifyWithBackend: %v", err)
	}
	if !got.Passed || got.ExitCode != 0 {
		t.Fatalf("RunVerifyWithBackend(exit 0) = %+v, want passed with exit 0", got)
	}
	if got.OutputSHA256 != wantDigest {
		t.Fatalf("digest = %q, want %q", got.OutputSHA256, wantDigest)
	}
	if got.Excerpt != output {
		t.Fatalf("excerpt = %q, want %q", got.Excerpt, output)
	}
}

func TestRunVerifyWithBackendNonZeroExit(t *testing.T) {
	got, err := RunVerifyWithBackend(context.Background(), fakeVerifyBackend("boom", 3, nil), "/workspace", "exit 3")
	if err != nil {
		t.Fatalf("RunVerifyWithBackend: %v", err)
	}
	if got.Passed || got.ExitCode != 3 {
		t.Fatalf("RunVerifyWithBackend = %+v, want failed with exit 3", got)
	}
}

func TestRunVerifyWithBackendInfraErrorPropagates(t *testing.T) {
	wantErr := context.DeadlineExceeded
	_, err := RunVerifyWithBackend(context.Background(), fakeVerifyBackend("", 0, wantErr), "/workspace", "true")
	if err != wantErr {
		t.Fatalf("RunVerifyWithBackend error = %v, want %v propagated from the backend", err, wantErr)
	}
}

// TestTailBufferBoundsMemoryKeepsEnd confirms the streamed excerpt is
// bounded to the cap and keeps the TAIL of the output, not the head —
// a verify_cmd with runaway output must not balloon memory, and the
// kept slice must still show what actually failed at the end.
func TestTailBufferBoundsMemoryKeepsEnd(t *testing.T) {
	tail := &tailBuffer{max: 5}
	full := "0123456789" // 10 bytes written in one shot, cap is 5
	if _, err := tail.Write([]byte(full)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(tail.buf) != 5 {
		t.Fatalf("tailBuffer retained %d bytes, want exactly 5", len(tail.buf))
	}
	if got, want := tail.String(), "56789"; got != want {
		t.Fatalf("tailBuffer content = %q, want %q (the last 5 bytes)", got, want)
	}
}
