//go:build integration

package missions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// requireGit and newTestWorkspace are defined once in worktree_test.go
// (no build tag, so always compiled alongside this integration file)
// and reused here.

func TestProvisionNonCodingMission(t *testing.T) {
	w := newTestWorkspace(t)
	ctx := context.Background()

	workspace, worktree, branch, baseCommit, _, err := w.Provision(ctx, "mission-2", "Do something", "", "general", "", "", nil, "", "")
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
	w := newTestWorkspace(t)
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("scratch repo\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README.md")
	run("-c", "user.name=test", "-c", "user.email=test@localhost", "commit", "-m", "initial")

	expired, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(1 * time.Millisecond) // ensure it's actually expired

	got := w.captureBaseCommit(expired, repo)
	if got != unavailableCommit {
		t.Fatalf("captureBaseCommit under an expired context = %q, want sentinel %q", got, unavailableCommit)
	}
}

func TestVerifyWorkspaceCatchesSymlinkSwap(t *testing.T) {
	w := newTestWorkspace(t)
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
