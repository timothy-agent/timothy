package missions

import (
	"context"
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
