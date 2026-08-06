package missions

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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

// TestProvisionCodingMissionSelfInitsRepo covers the fix's core case:
// a coding mission always self-initializes its own git repo inside
// its workspace — no repo needs to pre-exist in the container.
func TestProvisionCodingMissionSelfInitsRepo(t *testing.T) {
	requireGit(t)
	w := newTestWorkspace(t)
	ctx := context.Background()

	workspace, worktree, branch, baseCommit, err := w.Provision(ctx, "mission-self", "Fix the login bug", "coding")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if branch != "mission/fix-the-login-bug" {
		t.Fatalf("branch = %q, want mission/fix-the-login-bug", branch)
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

	_, worktree, _, _, err := w.Provision(ctx, "mission-self-2", "Add a feature", "coding")
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

// TestTeardownRemovesSelfInitRepo proves Teardown removes a coding
// mission's self-init'd workspace outright — there is no separate
// main-repo checkout to leave behind, so this is a plain os.RemoveAll.
func TestTeardownRemovesSelfInitRepo(t *testing.T) {
	requireGit(t)
	w := newTestWorkspace(t)
	ctx := context.Background()

	workspace, worktree, _, _, err := w.Provision(ctx, "mission-self-3", "Teardown test", "coding")
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
