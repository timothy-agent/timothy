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
		name      string
		raw       string
		wantErr   error
		host      string
		transport Transport
	}{
		{"https with .git suffix", "https://github.com/u/r.git", nil, "github.com", TransportHTTPS},
		{"https without .git suffix", "https://github.com/u/r", nil, "github.com", TransportHTTPS},
		{"ssh accepted since #796", "ssh://git@github.com/u/r.git", nil, "github.com", TransportSSH},
		{"ssh bitbucket accepted", "ssh://git@bitbucket.org/acme/widgets.git", nil, "bitbucket.org", TransportSSH},
		{"ssh without user accepted", "ssh://github.com/u/r.git", nil, "github.com", TransportSSH},
		{"ssh as another user rejected", "ssh://root@github.com/u/r.git", ErrRemoteUnsupported, "", ""},
		{"scp-form rejected, normalize first", "git@github.com:u/r.git", ErrRemoteUnsupported, "", ""},
		{"http rejected", "http://github.com/u/r.git", ErrRemoteUnsupported, "", ""},
		{"git protocol rejected", "git://github.com/u/r.git", ErrRemoteUnsupported, "", ""},
		{"file protocol rejected", "file:///tmp/repo", ErrRemoteUnsupported, "", ""},
		{"embedded credentials rejected", "https://user:tok@github.com/u/r.git", ErrRemoteUnsupported, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, transport, err := validateRemote(tc.raw)
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
			if transport != tc.transport {
				t.Fatalf("validateRemote(%q) transport = %q, want %q", tc.raw, transport, tc.transport)
			}
		})
	}
}

func TestValidateRemoteEmbeddedCredsMessage(t *testing.T) {
	_, _, err := validateRemote("https://user:tok@github.com/u/r.git")
	if err == nil || !strings.Contains(err.Error(), "credential_ref") {
		t.Fatalf("expected error mentioning credential_ref, got: %v", err)
	}
}

func TestValidateRemoteMalformedURL(t *testing.T) {
	_, _, err := validateRemote("://not a url")
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
	err := rawPush(context.Background(), root, missing, "main", HTTPSAuth(token, ""))
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

	if err := rawPush(context.Background(), workdir, workdir, branch, HTTPSAuth("dummy-token", "")); err != nil {
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

func TestParseBitbucketRepoURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		url       string
		workspace string
		slug      string
		wantOK    bool
	}{
		{"with .git suffix", "https://bitbucket.org/acme-team/widget-service.git", "acme-team", "widget-service", true},
		{"without .git suffix", "https://bitbucket.org/ws/repo", "ws", "repo", true},
		{"trailing slash", "https://bitbucket.org/ws/repo/", "ws", "repo", true},
		{"clone button form with a username", "https://someone@bitbucket.org/ws/repo.git", "ws", "repo", true},
		{"browser url with a branch path", "https://bitbucket.org/ws/repo/src/master/", "ws", "repo", true},
		{"browser url deep in the tree", "https://bitbucket.org/ws/repo/src/main/app/Http/", "ws", "repo", true},
		{"pull request page", "https://bitbucket.org/ws/repo/pull-requests/12", "ws", "repo", true},
		{"unknown page kind is rejected", "https://bitbucket.org/ws/repo/settings", "", "", false},
		{"github host is rejected", "https://github.com/octocat/hello-world.git", "", "", false},
		{"ssh form is rejected", "git@bitbucket.org:ws/repo.git", "", "", false},
		{"no slug", "https://bitbucket.org/ws", "", "", false},
		{"empty", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspace, slug, ok := ParseBitbucketRepoURL(tc.url)
			if ok != tc.wantOK || workspace != tc.workspace || slug != tc.slug {
				t.Fatalf("ParseBitbucketRepoURL(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.url, workspace, slug, ok, tc.workspace, tc.slug, tc.wantOK)
			}
		})
	}
}

func TestBitbucketCloneURL(t *testing.T) {
	t.Parallel()
	want := "https://bitbucket.org/acme-team/widget-service.git"
	for _, in := range []string{
		"https://bitbucket.org/acme-team/widget-service.git",
		"https://someone@bitbucket.org/acme-team/widget-service.git",
		"https://bitbucket.org/acme-team/widget-service/src/master/",
		"https://bitbucket.org/acme-team/widget-service",
	} {
		got, ok := BitbucketCloneURL(in)
		if !ok || got != want {
			t.Fatalf("BitbucketCloneURL(%q) = (%q, %v), want %q", in, got, ok, want)
		}
	}
	if _, ok := BitbucketCloneURL("git@bitbucket.org:acme-team/widget-service.git"); ok {
		t.Fatal("ssh form accepted")
	}
}

func TestParseRepoURLForKind(t *testing.T) {
	t.Parallel()
	if _, _, ok := parseRepoURLForKind(SourceKindBitbucket, "https://github.com/o/r"); ok {
		t.Fatal("bitbucket kind accepted a github URL")
	}
	if o, r, ok := parseRepoURLForKind(SourceKindGitHub, "https://github.com/o/r"); !ok || o != "o" || r != "r" {
		t.Fatalf("github kind: (%q, %q, %v)", o, r, ok)
	}
	if w, s, ok := parseRepoURLForKind(SourceKindBitbucket, "https://bitbucket.org/w/s.git"); !ok || w != "w" || s != "s" {
		t.Fatalf("bitbucket kind: (%q, %q, %v)", w, s, ok)
	}
}

// TestConventionalPRTitle covers the Conventional Commits shape the
// github destination uses for PR titles (issue #709).
func TestConventionalPRTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		m    Mission
		want string
	}{
		{"feat by default, lowercase", Mission{Name: "Molla-go URL Shortener Design"}, "feat: molla-go url shortener design"},
		{"fix cue from the name", Mission{Name: "Fix Login Bug"}, "fix: fix login bug"},
		{"docs cue from the goal", Mission{Goal: "Document the deploy runbook."}, "docs: document the deploy runbook"},
		{"explicit prefix is not doubled", Mission{Name: "fix: login redirect loop"}, "fix: login redirect loop"},
		{"long goal is cut at the cap", Mission{Goal: strings.Repeat("a", 100)}, "feat: " + strings.Repeat("a", PRTitleGoalCap-len("feat: "))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ConventionalPRTitle(tc.m)
			if got != tc.want {
				t.Fatalf("ConventionalPRTitle = %q, want %q", got, tc.want)
			}
			if len(got) > PRTitleGoalCap {
				t.Fatalf("len = %d, want <= %d", len(got), PRTitleGoalCap)
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

// The helper is a shell snippet git runs; execute it through a real /bin/sh
// and read what it prints, per host kind. The token must only ever come from
// the environment.
func TestGitCredentialHelperRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, user, wantUser string }{
		// The username is the Descriptor's own HTTPUsername, carried on
		// RemoteAuth (issue #796), with github's as the empty fallback.
		{"empty falls back to github", "", "x-access-token"},
		{"github", "x-access-token", "x-access-token"},
		{"bitbucket", "x-token-auth", "x-token-auth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			helper := gitCredentialHelper(RemoteAuth{HTTPUsername: tc.user}.credentialUsername(), "GIT_TEST_TOKEN")
			if strings.Contains(helper, "tok-value") {
				t.Fatalf("token leaked into the helper string: %s", helper)
			}
			// git strips the leading "!" before handing the rest to the shell
			cmd := exec.Command("/bin/sh", "-c", strings.TrimPrefix(helper, "!")) //nolint:gosec // the composed helper is the thing under test
			cmd.Env = append(os.Environ(), "GIT_TEST_TOKEN=tok-value")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("sh: %v: %s", err, out)
			}
			want := "username=" + tc.wantUser + "\npassword=tok-value\n"
			if string(out) != want {
				t.Fatalf("helper printed %q, want %q", out, want)
			}
		})
	}
}

// A bitbucket push against a local bare remote must succeed with the
// x-token-auth helper in place: the remote ignores credentials, so this
// proves the composed command still runs end to end for the second kind.
func TestRawPushBitbucketKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bare := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "-q", bare).CombinedOutput(); err != nil { //nolint:gosec // test-only temp dir
		t.Fatalf("init bare: %v: %s", err, out)
	}
	wt := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"remote", "add", "origin", bare},
		{"commit", "--allow-empty", "-q", "-m", "init"},
		{"checkout", "-q", "-b", "feat/x"},
		{"commit", "--allow-empty", "-q", "-m", "work"},
	} {
		if out, err := runGit(ctx, wt, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := rawPush(ctx, wt, wt, "feat/x", HTTPSAuth("unused-token", "x-token-auth")); err != nil {
		t.Fatalf("rawPush: %v", err)
	}
	out, err := exec.Command("git", "-C", bare, "branch", "--list", "feat/x").CombinedOutput() //nolint:gosec // test-only temp dir
	if err != nil || !strings.Contains(string(out), "feat/x") {
		t.Fatalf("branch not on remote: %v: %s", err, out)
	}
}

// githubRepoPattern is host-agnostic on purpose, so the host gate is
// its own check (issue #787).
func TestIsGitHubHost(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{url: "https://github.com/o/r.git", want: true},
		{url: "https://GitHub.com/o/r", want: true},
		{url: "https://bitbucket.org/acme/widgets.git", want: false},
		{url: "https://ghe.example.com/o/r.git", want: false},
		{url: "https://github.com.evil.test/o/r", want: false},
		{url: "", want: false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			if got := isGitHubHost(tc.url); got != tc.want {
				t.Fatalf("isGitHubHost(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

