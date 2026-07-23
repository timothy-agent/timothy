//go:build integration

package missions

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping worktree integration test")
	}
}

// scratchRepo creates a throwaway git repo with one commit, returning
// its path.
func scratchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("scratch repo\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README.md")
	run("-c", "user.name=test", "-c", "user.email=test@localhost", "commit", "-m", "initial")
	return dir
}

func testWorkspace(t *testing.T) *Workspace {
	t.Helper()
	root := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewWorkspace(root, log)
}

func TestProvisionCodingMission(t *testing.T) {
	requireGit(t)
	w := testWorkspace(t)
	repo := scratchRepo(t)
	ctx := context.Background()

	workspace, worktree, branch, baseCommit, err := w.Provision(ctx, "mission-1", "Fix the login bug", "coding", repo)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if workspace == "" || worktree == "" || branch != "mission/fix-the-login-bug" {
		t.Fatalf("Provision = workspace=%q worktree=%q branch=%q, unexpected shape", workspace, worktree, branch)
	}
	if baseCommit == "" || baseCommit == unavailableCommit {
		t.Fatalf("baseCommit = %q, want a real commit hash", baseCommit)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree directory missing: %v", err)
	}

	// The branch actually exists in the source repo.
	cmd := exec.Command("git", "branch", "--list", branch)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		t.Fatalf("git branch --list %s: out=%q err=%v", branch, out, err)
	}
}

func TestProvisionNonCodingMission(t *testing.T) {
	w := testWorkspace(t)
	ctx := context.Background()

	workspace, worktree, branch, baseCommit, err := w.Provision(ctx, "mission-2", "Research something", "research", "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if workspace == "" {
		t.Fatal("Provision did not create a workspace directory")
	}
	if worktree != "" || branch != "" || baseCommit != "" {
		t.Fatalf("non-coding Provision set git fields: worktree=%q branch=%q baseCommit=%q", worktree, branch, baseCommit)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("workspace directory missing: %v", err)
	}
}

// TestCaptureBaseCommitDegradesOnTimeout proves the tight timeout
// actually degrades to the sentinel rather than blocking provisioning
// — simulated via a context that's already expired.
func TestCaptureBaseCommitDegradesOnTimeout(t *testing.T) {
	requireGit(t)
	w := testWorkspace(t)
	repo := scratchRepo(t)

	expired, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(1 * time.Millisecond) // ensure it's actually expired

	got := w.captureBaseCommit(expired, repo)
	if got != unavailableCommit {
		t.Fatalf("captureBaseCommit under an expired context = %q, want sentinel %q", got, unavailableCommit)
	}
}

func TestRollbackDiscardsUncommittedWork(t *testing.T) {
	requireGit(t)
	w := testWorkspace(t)
	repo := scratchRepo(t)
	ctx := context.Background()

	_, worktree, _, _, err := w.Provision(ctx, "mission-3", "Rollback test", "coding", repo)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	dirty := filepath.Join(worktree, "scratch.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	if err := w.Rollback(ctx, worktree, "coding"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if _, err := os.Stat(dirty); !os.IsNotExist(err) {
		t.Fatalf("dirty file survived rollback: err=%v", err)
	}
}

func TestTeardownRemovesWorktreeButKeepsBranch(t *testing.T) {
	requireGit(t)
	w := testWorkspace(t)
	repo := scratchRepo(t)
	ctx := context.Background()

	workspace, worktree, branch, _, err := w.Provision(ctx, "mission-4", "Teardown test", "coding", repo)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := w.Teardown(ctx, workspace, worktree, "coding"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace survived teardown: err=%v", err)
	}

	cmd := exec.Command("git", "branch", "--list", branch)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		t.Fatalf("branch %s did not survive teardown: out=%q err=%v", branch, out, err)
	}
}

func TestVerifyWorkspaceCatchesSymlinkSwap(t *testing.T) {
	w := testWorkspace(t)
	root := t.TempDir()
	real := filepath.Join(root, "real")
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("mkdir other: %v", err)
	}

	if err := w.VerifyWorkspace(real, real); err != nil {
		t.Fatalf("VerifyWorkspace(same path) = %v, want nil", err)
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(other, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := w.VerifyWorkspace(real, link); err == nil {
		t.Fatal("VerifyWorkspace did not catch a symlink pointing at a different directory")
	}
}
