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

// TestSetOriginAddsWhenNone proves SetOrigin adds an origin remote to
// a worktree that has none (a self-init'd scratch mission's clone,
// see initSelfRepo) -- the create-if-missing delivery path's own step
// (issue #483) before pushing to a repo the mission was never cloned
// from.
func TestSetOriginAddsWhenNone(t *testing.T) {
	requireGitForPush(t)
	workdir := t.TempDir()
	gitRun(t, workdir, "init", "-q")

	w := NewWorkspace(t.TempDir(), nil, discardLog())
	if err := w.SetOrigin(context.Background(), workdir, "https://github.com/octo/new-repo.git"); err != nil {
		t.Fatalf("SetOrigin: %v", err)
	}

	got := strings.TrimSpace(gitRun(t, workdir, "remote", "get-url", "origin"))
	if got != "https://github.com/octo/new-repo.git" {
		t.Fatalf("origin = %q, want the new repo URL", got)
	}
}

// TestSetOriginRepointsExisting proves SetOrigin repoints an origin
// that already exists (a mission cloned from one repo, pushing to a
// different one via a create-if-missing destination entry) rather than
// erroring on "remote already exists."
func TestSetOriginRepointsExisting(t *testing.T) {
	requireGitForPush(t)
	workdir := t.TempDir()
	gitRun(t, workdir, "init", "-q")
	gitRun(t, workdir, "remote", "add", "origin", "https://github.com/octo/old-repo.git")

	w := NewWorkspace(t.TempDir(), nil, discardLog())
	if err := w.SetOrigin(context.Background(), workdir, "https://github.com/octo/new-repo.git"); err != nil {
		t.Fatalf("SetOrigin: %v", err)
	}

	got := strings.TrimSpace(gitRun(t, workdir, "remote", "get-url", "origin"))
	if got != "https://github.com/octo/new-repo.git" {
		t.Fatalf("origin = %q, want repointed to the new repo URL", got)
	}
}

// TestSetOriginRejectsNonHTTPS proves SetOrigin runs remoteURL through
// the same validateRemote gate Push's own read-back uses, before ever
// touching git -- an scp-style or non-https URL never reaches "git
// remote add/set-url" at all.
func TestSetOriginRejectsNonHTTPS(t *testing.T) {
	requireGitForPush(t)
	workdir := t.TempDir()
	gitRun(t, workdir, "init", "-q")

	w := NewWorkspace(t.TempDir(), nil, discardLog())
	err := w.SetOrigin(context.Background(), workdir, "git@github.com:octo/new-repo.git")
	if !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("SetOrigin with an scp-style URL: err = %v, want ErrRemoteUnsupported", err)
	}
	if out, getErr := gitRun2(workdir, "remote", "get-url", "origin"); getErr == nil {
		t.Fatalf("origin should not have been set, got %q", out)
	}
}

// gitRun2 is gitRun without the t.Fatal on error -- used only where a
// non-zero exit is the expected/asserted outcome.
func gitRun2(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...) //nolint:gosec // args are fixed test-fixture git subcommands, not user input
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestNotPushableGuards covers the shared kind/branch/worktree gate
// both push and pr require, without touching a store or workspace.
func TestNotPushableGuards(t *testing.T) {
	t.Parallel()
	if reason := NotPushable(Mission{Kind: "general"}); reason == "" {
		t.Fatal("general mission should not be pushable")
	}
	if reason := NotPushable(Mission{Kind: "coding"}); reason == "" {
		t.Fatal("coding mission with no branch should not be pushable")
	}
	if reason := NotPushable(Mission{Kind: "coding", Branch: "mission/x", Workspace: "/does/not/exist"}); reason == "" {
		t.Fatal("coding mission with a missing worktree should not be pushable")
	}
}

// TestParseGitHubRepoURL covers the owner/repo extraction OpenPR needs
// from mission.RepoURL (always an https clone URL), with and without
// the .git suffix, and a malformed shape reporting ok=false.
func TestParseGitHubRepoURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		url    string
		owner  string
		repo   string
		wantOK bool
	}{
		{"with .git suffix", "https://github.com/octocat/hello-world.git", "octocat", "hello-world", true},
		{"without .git suffix", "https://github.com/octocat/hello-world", "octocat", "hello-world", true},
		{"trailing slash", "https://github.com/octocat/hello-world/", "octocat", "hello-world", true},
		{"malformed, no repo segment", "https://github.com/octocat", "", "", false},
		{"not a URL", "not-a-url", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, ok := ParseGitHubRepoURL(tc.url)
			if ok != tc.wantOK || owner != tc.owner || repo != tc.repo {
				t.Fatalf("ParseGitHubRepoURL(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.url, owner, repo, ok, tc.owner, tc.repo, tc.wantOK)
			}
		})
	}
}

// TestPRTitleFallsBackToTruncatedGoal covers PRTitle's name-vs-goal
// precedence and the truncation cap for a long goal with no name yet.
func TestPRTitleFallsBackToTruncatedGoal(t *testing.T) {
	t.Parallel()
	named := Mission{Name: "Fix Login Bug", Goal: "fix the login bug that logs everyone out"}
	if got := PRTitle(named); got != "Fix Login Bug" {
		t.Fatalf("PRTitle with a name = %q, want the name", got)
	}
	short := Mission{Goal: "fix the login bug"}
	if got := PRTitle(short); got != "fix the login bug" {
		t.Fatalf("PRTitle with a short goal and no name = %q, want the goal verbatim", got)
	}
	long := Mission{Goal: strings.Repeat("a", 100)}
	got := PRTitle(long)
	if len(got) != PRTitleGoalCap+len("…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("PRTitle with a long goal and no name = %q (len %d), want truncated to %d chars + ellipsis", got, len(got), PRTitleGoalCap)
	}
}
