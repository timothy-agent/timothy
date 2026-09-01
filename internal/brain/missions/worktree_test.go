package missions

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testSigningKeypair generates a fresh ed25519 SSH signing keypair for
// tests — the same OpenSSH-PEM-private / authorized_keys-public shapes
// connectors.generateSigningKeypair produces, without importing that
// package (this is missions, a different package tree).
func testSigningKeypair(t *testing.T) (privatePEM, publicLine string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("derive public key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), string(ssh.MarshalAuthorizedKey(sshPub))
}

func TestCommitType(t *testing.T) {
	cases := []struct {
		name, text, want string
	}{
		{"fix keyword", "Fix the login bug", "fix"},
		{"bug keyword", "there's a bug in checkout", "fix"},
		{"broken keyword", "the build is broken", "fix"},
		{"doc keyword", "Update the doc for setup", "docs"},
		{"readme keyword", "Update README", "docs"},
		{"comment keyword", "Add comments to parser", "docs"},
		{"test keyword", "Add tests for the parser", "test"},
		{"refactor keyword", "Refactor the auth module", "refactor"},
		{"rename keyword", "Rename the package", "refactor"},
		{"cleanup keyword", "Cleanup dead code", "refactor"},
		{"chore keyword", "Chore: repo housekeeping", "chore"},
		{"bump keyword", "Bump dependency versions", "chore"},
		{"upgrade keyword", "Upgrade dependency", "chore"},
		{"dependency keyword", "Update dependency pins", "chore"},
		{"default feat", "Add dark mode toggle", "feat"},
		{"empty text defaults to feat", "", "feat"},
		{"fix checked before feat-like phrasing", "Fix: add new feature", "fix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CommitType(tc.text)
			if got != tc.want {
				t.Fatalf("CommitType(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestCommitMessage(t *testing.T) {
	cases := []struct {
		name, unitTitle, goal, body, style, want string
	}{
		{
			"unit title used when present",
			"Fix the broken login flow", "Some other goal", "body text", "",
			"fix: fix the broken login flow\n\nbody text",
		},
		{
			"falls back to goal when no unit title",
			"", "Refactor the auth module", "body text", "",
			"refactor: refactor the auth module\n\nbody text",
		},
		{
			"no body omits the blank separator",
			"Add tests for parser", "goal", "", "",
			"test: add tests for parser",
		},
		{
			"trailing period trimmed",
			"Add dark mode.", "goal", "", "",
			"feat: add dark mode",
		},
		{
			"long title trimmed to 72 chars",
			strings.Repeat("a", 100), "goal", "", "",
			"feat: " + strings.Repeat("a", 66),
		},
		{
			"empty style defaults to conventional",
			"Fix the broken login flow", "goal", "", CommitStyleConventional,
			"fix: fix the broken login flow",
		},
		{
			"plain style omits the type prefix",
			"Fix the broken login flow", "goal", "body text", CommitStylePlain,
			"fix the broken login flow\n\nbody text",
		},
		{
			"plain style still trims to 72 chars",
			strings.Repeat("a", 100), "goal", "", CommitStylePlain,
			strings.Repeat("a", 72),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CommitMessage(tc.unitTitle, tc.goal, tc.body, tc.style)
			if got != tc.want {
				t.Fatalf("CommitMessage(%q, %q, %q, %q) = %q, want %q", tc.unitTitle, tc.goal, tc.body, tc.style, got, tc.want)
			}
			subject := strings.SplitN(got, "\n", 2)[0]
			if len(subject) > maxCommitSubjectLen {
				t.Fatalf("subject %q exceeds %d chars", subject, maxCommitSubjectLen)
			}
		})
	}
}

func TestSlug(t *testing.T) {
	cases := []struct {
		name, goal, id, want string
	}{
		{"normal goal", "Fix the login bug", "abc12345-full-id", "fix-the-login-bug"},
		{"punctuation and unicode collapse to hyphens", "Add caché! (v2.0)", "abc12345-full-id", "add-cach-v2-0"},
		{
			"exactly at the cap",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 40 a's
			"abc12345-full-id",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			"over the cap truncates and trims a trailing hyphen",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbb", // 40 a's then more
			"abc12345-full-id",
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{"empty goal falls back to id prefix", "", "abc12345-full-id", "m-abc12345"},
		{"all-punctuation goal falls back to id prefix", "!!!???...", "abc12345-full-id", "m-abc12345"},
		{"short id is used whole in the fallback", "", "abc", "m-abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Slug(tc.goal, tc.id)
			if got != tc.want {
				t.Fatalf("Slug(%q, %q) = %q, want %q", tc.goal, tc.id, got, tc.want)
			}
			if len(got) == 0 {
				t.Fatal("Slug returned empty string")
			}
		})
	}
}

// requireGit skips a test rather than failing it when git isn't on
// PATH — mirrors worktree_integration_test.go's requireGit, duplicated
// here since these tests (self-init needs no Docker/Postgres) live
// outside the integration build tag.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping")
	}
}

func newTestWorkspace(t *testing.T) *Workspace {
	t.Helper()
	root := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewWorkspace(root, nil, log)
}

// TestMissionWorktreePath covers Mission.WorktreePath's derivation
// (issue #479 dropped the stored worktree column): workspace/wt for a
// needsWorktree kind/flow with a provisioned workspace, "" otherwise.
func TestMissionWorktreePath(t *testing.T) {
	cases := []struct {
		name string
		m    Mission
		want string
	}{
		{
			name: "coding mission with workspace gets workspace/wt",
			m:    Mission{Kind: KindCoding, Flow: FlowFull, Workspace: "/ws/coding/m1"},
			want: "/ws/coding/m1/wt",
		},
		{
			name: "general mission never gets a worktree",
			m:    Mission{Kind: KindGeneral, Flow: FlowFull, Workspace: "/ws/general/m1"},
			want: "",
		},
		{
			name: "light general mission never gets a worktree",
			m:    Mission{Kind: KindGeneral, Flow: FlowLight, Workspace: "/ws/general/m1"},
			want: "",
		},
		{
			name: "coding mission not yet provisioned has no workspace to derive from",
			m:    Mission{Kind: KindCoding, Flow: FlowFull},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.WorktreePath(); got != tc.want {
				t.Fatalf("WorktreePath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestProvisionWorkspacePathIncludesKind confirms a new mission's
// workspace lands at root/kind/id (issue #479), not root/id: kind
// segments the flat workspace root so a listing separates coding from
// general missions.
func TestProvisionWorkspacePathIncludesKind(t *testing.T) {
	requireGit(t)
	w := newTestWorkspace(t)
	ctx := context.Background()

	workspace, _, _, _, _, err := w.Provision(ctx, "mission-kind-1", "Fix the login bug", "coding", "", "", nil, "", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	want := filepath.Join(w.root, "coding", "mission-kind-1")
	if workspace != want {
		t.Fatalf("workspace = %q, want %q", workspace, want)
	}

	workspace2, _, _, _, _, err := w.Provision(ctx, "mission-kind-2", "Summarize the doc", "general", "", "", nil, "", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	want2 := filepath.Join(w.root, "general", "mission-kind-2")
	if workspace2 != want2 {
		t.Fatalf("workspace = %q, want %q", workspace2, want2)
	}
}

// TestProvisionCodingMissionSelfInitsRepo covers the fix's core case:
// a coding mission always self-initializes its own git repo inside
// its workspace — no repo needs to pre-exist in the container.
func TestProvisionCodingMissionSelfInitsRepo(t *testing.T) {
	requireGit(t)
	w := newTestWorkspace(t)
	ctx := context.Background()

	workspace, worktree, branch, baseCommit, _, err := w.Provision(ctx, "mission-self", "Fix the login bug", "coding", "", "", nil, "", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if branch != "fix/fix-the-login-bug" {
		t.Fatalf("branch = %q, want fix/fix-the-login-bug", branch)
	}
	if baseCommit == "" || baseCommit == unavailableCommit {
		t.Fatalf("baseCommit = %q, want a real commit hash", baseCommit)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree directory missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
		t.Fatalf("worktree has no .git: %v", err)
	}

	// HEAD actually resolves and matches the reported baseCommit.
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	if got := string(out); got[:len(got)-1] != baseCommit {
		t.Fatalf("HEAD = %q, want baseCommit %q", got, baseCommit)
	}

	// The checked-out branch is the one Provision reported.
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = worktree
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("git branch --show-current: %v", err)
	}
	if got := string(out); got[:len(got)-1] != branch {
		t.Fatalf("current branch = %q, want %q", got, branch)
	}

	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("workspace directory missing: %v", err)
	}
}

// TestProvisionSelfInitRollbackAndCommitUnit proves the self-init'd
// repo is a real working git repo, not just a directory that happens
// to satisfy Provision's return shape: Rollback and CommitUnit (the
// harness machinery every coding mission relies on) both work against
// it.
func TestProvisionSelfInitRollbackAndCommitUnit(t *testing.T) {
	requireGit(t)
	w := newTestWorkspace(t)
	ctx := context.Background()

	_, worktree, _, _, _, err := w.Provision(ctx, "mission-self-2", "Add a feature", "coding", "", "", nil, "", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	scratch := filepath.Join(worktree, "scratch.txt")
	if err := os.WriteFile(scratch, []byte("uncommitted"), 0o600); err != nil {
		t.Fatalf("write scratch file: %v", err)
	}
	if err := w.Rollback(ctx, worktree, "coding"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch file survived rollback: err=%v", err)
	}

	committed := filepath.Join(worktree, "unit1.txt")
	if err := os.WriteFile(committed, []byte("unit 1 output"), 0o600); err != nil {
		t.Fatalf("write unit file: %v", err)
	}
	if err := w.CommitUnit(ctx, worktree, "unit 1"); err != nil {
		t.Fatalf("CommitUnit: %v", err)
	}

	cmd := exec.Command("git", "log", "--oneline")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("git log is empty after CommitUnit")
	}
}

// TestProvisionClonesRepo proves a repoURL mission clones the given
// repo's default branch and checks out its own mission branch on top,
// rather than self-initializing an empty repo. The "remote" is a local
// bare repo (a plain filesystem path git accepts as an origin), which
// exercises the same clone/credential-helper code path a real https
// origin would, just without a network round trip.
func TestProvisionClonesRepo(t *testing.T) {
	requireGit(t)
	w := newTestWorkspace(t)
	ctx := context.Background()

	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare", "-b", "main")
	seed := t.TempDir()
	gitRun(t, seed, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", "README.md")
	gitRun(t, seed, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "seed")
	gitRun(t, seed, "remote", "add", "origin", bare)
	gitRun(t, seed, "push", "-q", "origin", "main")

	workspace, worktree, branch, baseCommit, _, err := w.Provision(ctx, "mission-clone", "Fix the login bug", "coding", bare, "dummy-token", nil, "", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if branch != "fix/fix-the-login-bug" {
		t.Fatalf("branch = %q, want fix/fix-the-login-bug", branch)
	}
	if baseCommit == "" || baseCommit == unavailableCommit {
		t.Fatalf("baseCommit = %q, want a real commit hash", baseCommit)
	}
	if _, err := os.Stat(filepath.Join(worktree, "README.md")); err != nil {
		t.Fatalf("cloned repo's README.md missing: %v", err)
	}

	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch --show-current: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != branch {
		t.Fatalf("current branch = %q, want %q", got, branch)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("workspace directory missing: %v", err)
	}
}

// TestProvisionClonesRepoWithBaseRef proves a follow-up mission's
// requested base (baseRef, its parent's own branch) is fetched and
// used as the new branch's base instead of the repo's default branch
// — asserting the new branch's parent commit is the base ref's commit,
// not the default branch's.
func TestProvisionClonesRepoWithBaseRef(t *testing.T) {
	requireGit(t)
	w := newTestWorkspace(t)
	ctx := context.Background()

	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare", "-b", "main")
	seed := t.TempDir()
	gitRun(t, seed, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", "README.md")
	gitRun(t, seed, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "seed")
	gitRun(t, seed, "remote", "add", "origin", bare)
	gitRun(t, seed, "push", "-q", "origin", "main")

	// A parent mission's branch, pushed to the same bare repo, one
	// commit ahead of main.
	gitRun(t, seed, "checkout", "-q", "-b", "feat/parent-work")
	if err := os.WriteFile(filepath.Join(seed, "extra.txt"), []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", "extra.txt")
	gitRun(t, seed, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "parent work")
	gitRun(t, seed, "push", "-q", "origin", "feat/parent-work")
	parentCommit := strings.TrimSpace(gitRun(t, seed, "rev-parse", "feat/parent-work"))
	mainCommit := strings.TrimSpace(gitRun(t, seed, "rev-parse", "main"))
	if parentCommit == mainCommit {
		t.Fatal("test setup: parent branch commit must differ from main")
	}

	_, worktree, branch, baseCommit, baseUsed, err := w.Provision(ctx, "mission-followup", "Continue the work", "coding", bare, "dummy-token", nil, "", "feat/parent-work")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if baseUsed != "feat/parent-work" {
		t.Fatalf("baseUsed = %q, want %q", baseUsed, "feat/parent-work")
	}
	if baseCommit != parentCommit {
		t.Fatalf("baseCommit = %q, want the parent branch's commit %q", baseCommit, parentCommit)
	}
	if _, err := os.Stat(filepath.Join(worktree, "extra.txt")); err != nil {
		t.Fatalf("file only on the parent branch is missing from the new worktree: %v", err)
	}

	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch --show-current: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != branch {
		t.Fatalf("current branch = %q, want %q", got, branch)
	}
}

// TestProvisionClonesRepoWithUnreachableBaseRefFallsBack proves a
// baseRef that doesn't exist in the source repo degrades to the
// existing default-branch behavior instead of failing provisioning —
// baseUsed comes back empty (matching Provision's pre-follow-up
// return shape) and the new branch bases on the default branch's
// commit.
func TestProvisionClonesRepoWithUnreachableBaseRefFallsBack(t *testing.T) {
	requireGit(t)
	w := newTestWorkspace(t)
	ctx := context.Background()

	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare", "-b", "main")
	seed := t.TempDir()
	gitRun(t, seed, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", "README.md")
	gitRun(t, seed, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "seed")
	gitRun(t, seed, "remote", "add", "origin", bare)
	gitRun(t, seed, "push", "-q", "origin", "main")
	mainCommit := strings.TrimSpace(gitRun(t, seed, "rev-parse", "main"))

	_, worktree, branch, baseCommit, baseUsed, err := w.Provision(ctx, "mission-followup-missing", "Continue the work", "coding", bare, "dummy-token", nil, "", "does-not-exist")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if baseUsed != "" {
		t.Fatalf("baseUsed = %q, want empty (fell back to the default branch)", baseUsed)
	}
	if baseCommit != mainCommit {
		t.Fatalf("baseCommit = %q, want the default branch's commit %q", baseCommit, mainCommit)
	}

	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch --show-current: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != branch {
		t.Fatalf("current branch = %q, want %q", got, branch)
	}
}

// TestProvisionClonesRepoWithConnIdentity proves a connection's
// resolved identity (connIdentity) lands as the clone's LOCAL git
// config and that a subsequent CommitUnit authors its commit as that
// identity, not the fixed commitName/commitEmail or any operator
// default — the authorship mechanics driver.go's ensureProvisioned
// wires (SetCloneIdentityResolver).
func TestProvisionClonesRepoWithConnIdentity(t *testing.T) {
	requireGit(t)
	w := newTestWorkspace(t)
	ctx := context.Background()

	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare", "-b", "main")
	seed := t.TempDir()
	gitRun(t, seed, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", "README.md")
	gitRun(t, seed, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "seed")
	gitRun(t, seed, "remote", "add", "origin", bare)
	gitRun(t, seed, "push", "-q", "origin", "main")

	identity := &GitIdentity{Name: "conn-bot", Email: "conn-bot@example.com"}
	_, worktree, _, _, _, err := w.Provision(ctx, "mission-conn-identity", "Fix the login bug", "coding", bare, "dummy-token", identity, "", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	name, ok := gitConfigLocal(ctx, worktree, "user.name")
	if !ok || name != identity.Name {
		t.Fatalf("local user.name = %q (ok=%v), want %q", name, ok, identity.Name)
	}
	email, ok := gitConfigLocal(ctx, worktree, "user.email")
	if !ok || email != identity.Email {
		t.Fatalf("local user.email = %q (ok=%v), want %q", email, ok, identity.Email)
	}

	if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.CommitUnit(ctx, worktree, "conn identity commit"); err != nil {
		t.Fatalf("CommitUnit: %v", err)
	}

	cmd := exec.Command("git", "log", "-1", "--format=%an <%ae>")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want := identity.Name + " <" + identity.Email + ">"
	if got != want {
		t.Fatalf("commit author = %q, want %q", got, want)
	}
}

// TestProvisionClonesRepoWithSigningKey proves a connection identity
// carrying an SSH signing key lands as the clone's LOCAL git config
// (gpg.format=ssh, user.signingkey, commit.gpgsign=true), the key file
// is written outside the worktree at 0600, and a subsequent CommitUnit
// produces a commit `git verify-commit`/`log --format=%G?` reports as
// Good — a real round-trip, not just config-value assertions (per the
// repo's real-shell-tests-for-composed-commands convention).
func TestProvisionClonesRepoWithSigningKey(t *testing.T) {
	requireGit(t)
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found on PATH; skipping")
	}
	w := newTestWorkspace(t)
	ctx := context.Background()

	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare", "-b", "main")
	seed := t.TempDir()
	gitRun(t, seed, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", "README.md")
	gitRun(t, seed, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "seed")
	gitRun(t, seed, "remote", "add", "origin", bare)
	gitRun(t, seed, "push", "-q", "origin", "main")

	privatePEM, publicLine := testSigningKeypair(t)
	identity := &GitIdentity{Name: "conn-bot", Email: "conn-bot@example.com", SigningKey: privatePEM}
	workspace, worktree, _, _, _, err := w.Provision(ctx, "mission-signing", "Fix the login bug", "coding", bare, "dummy-token", identity, "", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	keyPath := filepath.Join(workspace, signingKeyFileName)
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("signing key file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("signing key file perms = %o, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(worktree, signingKeyFileName)); err == nil {
		t.Fatal("signing key file must not live inside the worktree")
	}

	for key, want := range map[string]string{
		"gpg.format":      "ssh",
		"user.signingkey": keyPath,
		"commit.gpgsign":  "true",
	} {
		got, ok := gitConfigLocal(ctx, worktree, key)
		if !ok || got != want {
			t.Fatalf("local %s = %q (ok=%v), want %q", key, got, ok, want)
		}
	}

	// allowed_signers so `git log --format=%G?`/`verify-commit` can
	// actually verify the signature, not just record that one exists.
	allowedSigners := filepath.Join(t.TempDir(), "allowed_signers")
	if err := os.WriteFile(allowedSigners, []byte(identity.Email+" "+publicLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runGit(ctx, worktree, "config", "--local", "gpg.ssh.allowedSignersFile", allowedSigners); err != nil {
		t.Fatalf("set allowedSignersFile: %v: %s", err, out)
	}

	if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.CommitUnit(ctx, worktree, "signed commit"); err != nil {
		t.Fatalf("CommitUnit: %v", err)
	}

	cmd := exec.Command("git", "log", "-1", "--format=%G?")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "G" {
		t.Fatalf("commit signature status = %q, want %q (Good)", got, "G")
	}
}

// TestCloneRepoScrubsTokenFromError mirrors push_test.go's
// TestRawPushScrubsTokenFromError: a clone against a nonexistent remote
// fails fast (no network touched), proving the error path never leaks
// the literal token string.
func TestCloneRepoScrubsTokenFromError(t *testing.T) {
	requireGit(t)
	w := newTestWorkspace(t)
	workspaceDir := t.TempDir()
	dir := filepath.Join(workspaceDir, "wt")
	const token = "super-secret-clone-token"
	_, err := w.cloneRepo(context.Background(), workspaceDir, dir, "mission/x", "/does/not/exist", token, nil, "")
	if err == nil {
		t.Fatal("cloneRepo against a nonexistent remote should fail")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error contains the raw token: %v", err)
	}
}

// TestTeardownRemovesSelfInitRepo proves Teardown removes a coding
// mission's self-init'd workspace outright — there is no separate
// main-repo checkout to leave behind, so this is a plain os.RemoveAll.
func TestTeardownRemovesSelfInitRepo(t *testing.T) {
	requireGit(t)
	w := newTestWorkspace(t)
	ctx := context.Background()

	workspace, worktree, _, _, _, err := w.Provision(ctx, "mission-self-3", "Teardown test", "coding", "", "", nil, "", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := w.Teardown(ctx, workspace, worktree, "coding"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace survived teardown: err=%v", err)
	}
}
