package missions

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGitForPush skips when git isn't on PATH. Named distinctly
// from worktree_integration_test.go's requireGit (behind
// //go:build integration, not visible to this plain test file) rather
// than duplicating that identifier.
func requireGitForPush(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping push test")
	}
}

func TestValidateRemote(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr error
		host    string
	}{
		{"https with .git suffix", "https://github.com/u/r.git", nil, "github.com"},
		{"https without .git suffix", "https://github.com/u/r", nil, "github.com"},
		{"ssh scheme rejected", "ssh://git@github.com/u/r.git", ErrRemoteUnsupported, ""},
		{"scp-form rejected", "git@github.com:u/r.git", ErrRemoteUnsupported, ""},
		{"embedded credentials rejected", "https://user:tok@github.com/u/r.git", ErrRemoteUnsupported, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, err := validateRemote(tc.raw)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("validateRemote(%q) err = %v, want %v", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateRemote(%q): %v", tc.raw, err)
			}
			if host != tc.host {
				t.Fatalf("validateRemote(%q) host = %q, want %q", tc.raw, host, tc.host)
			}
		})
	}
}

func TestValidateRemoteEmbeddedCredsMessage(t *testing.T) {
	_, err := validateRemote("https://user:tok@github.com/u/r.git")
	if err == nil || !strings.Contains(err.Error(), "credential_ref") {
		t.Fatalf("expected error mentioning credential_ref, got: %v", err)
	}
}

func TestValidateRemoteMalformedURL(t *testing.T) {
	_, err := validateRemote("://not a url")
	if !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("malformed URL should surface as ErrRemoteUnsupported, got: %v", err)
	}
}

func TestRawPushScrubsTokenFromError(t *testing.T) {
	requireGitForPush(t)
	// A nonexistent worktree dir makes git fail fast without touching
	// the network — enough to prove the error path never leaks the
	// literal token string.
	root := t.TempDir()
	missing := filepath.Join(root, "does-not-exist")
	const token = "super-secret-token-value"
	err := rawPush(context.Background(), missing, "main", token)
	if err == nil {
		t.Fatal("rawPush against a nonexistent directory should fail")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error contains the raw token: %v", err)
	}
}

// gitRun runs a git command in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // args are fixed test-fixture git subcommands, not user input
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func TestRawPushHappyPath(t *testing.T) {
	requireGitForPush(t)
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare")

	workdir := t.TempDir()
	gitRun(t, workdir, "clone", "-q", bare, ".")
	if err := os.WriteFile(filepath.Join(workdir, "file.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, workdir, "add", "file.txt")
	gitRun(t, workdir, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "add file")
	branch := strings.TrimSpace(gitRun(t, workdir, "rev-parse", "--abbrev-ref", "HEAD"))

	if err := rawPush(context.Background(), workdir, branch, "dummy-token"); err != nil {
		t.Fatalf("rawPush: %v", err)
	}

	localHead := strings.TrimSpace(gitRun(t, workdir, "rev-parse", "HEAD"))
	bareHead := strings.TrimSpace(gitRun(t, bare, "rev-parse", branch))
	if localHead != bareHead {
		t.Fatalf("bare repo ref = %q, want it to match local HEAD %q", bareHead, localHead)
	}
}
