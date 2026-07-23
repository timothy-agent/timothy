package missions

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// baseCommitTimeout is deliberately tight: provisioning must never
	// block on git. A slow/hung repo degrades the base commit to a
	// sentinel rather than stalling mission creation.
	baseCommitTimeout = 1 * time.Second
	rollbackTimeout   = 30 * time.Second
	gitOpTimeout      = 30 * time.Second

	unavailableCommit = "(unavailable)"

	// commitName/commitEmail are fixed, not the operator's real git
	// identity — commits inside a mission worktree are machine-authored.
	commitName  = "timothy"
	commitEmail = "timothy@localhost"
)

// Workspace provisions and tears down mission working directories. Its
// root is also the tool path-allowlist root for mission workers — a
// mission's shell/file tools can never escape it.
type Workspace struct {
	root string // $WORKSPACES
	log  *slog.Logger
}

func NewWorkspace(root string, log *slog.Logger) *Workspace {
	return &Workspace{root: root, log: log}
}

// Provision creates the mission's directory. Coding missions run `git
// worktree add -b mission/<slug> <dir>` against repoPath and capture
// the base commit with a tight timeout (degrades to the sentinel
// "(unavailable)" rather than blocking). Non-coding missions get a
// plain directory, no git.
func (w *Workspace) Provision(ctx context.Context, missionID, goal, kind, repoPath string) (workspace, worktree, branch, baseCommit string, err error) {
	workspace = filepath.Join(w.root, missionID)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return "", "", "", "", fmt.Errorf("worktree: provision: mkdir %s: %w", workspace, err)
	}

	if kind != "coding" {
		return workspace, "", "", "", nil
	}

	branch = "mission/" + Slug(goal, missionID)
	worktree = filepath.Join(workspace, "wt")
	gctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	cmd := exec.CommandContext(gctx, "git", "worktree", "add", "-b", branch, worktree)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", "", "", fmt.Errorf("worktree: git worktree add: %w: %s", err, string(out))
	}

	baseCommit = w.captureBaseCommit(ctx, worktree)
	return workspace, worktree, branch, baseCommit, nil
}

// captureBaseCommit reads HEAD under a tight timeout — provisioning
// must never block on git, so a slow or hung repo degrades to a
// sentinel instead of stalling mission creation.
func (w *Workspace) captureBaseCommit(ctx context.Context, worktree string) string {
	cctx, cancel := context.WithTimeout(ctx, baseCommitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "rev-parse", "HEAD")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		w.log.Warn("worktree: base commit capture degraded", "worktree", worktree, "error", err)
		return unavailableCommit
	}
	return strings.TrimSpace(string(out))
}

// slugPattern keeps a goal's derived branch name filesystem/git safe:
// lowercase alphanumerics and hyphens only.
var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

const maxSlugLen = 40

// Slug derives a git-branch-safe slug from the goal: lowercased,
// alphanumeric+hyphen only, capped at maxSlugLen; falls back to an
// id-prefixed name if the goal reduces to nothing usable (e.g. an
// all-punctuation goal).
func Slug(goal, id string) string {
	s := slugPattern.ReplaceAllString(strings.ToLower(goal), "-")
	s = strings.Trim(s, "-")
	if len(s) > maxSlugLen {
		s = strings.Trim(s[:maxSlugLen], "-")
	}
	if s == "" {
		prefix := id
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}
		return "m-" + prefix
	}
	return s
}

// Rollback discards uncommitted work: `git checkout -- .` + `git clean
// -fd` for coding missions, no-op otherwise.
func (w *Workspace) Rollback(ctx context.Context, worktree, kind string) error {
	if kind != "coding" || worktree == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, rollbackTimeout)
	defer cancel()
	if out, err := runGit(cctx, worktree, "checkout", "--", "."); err != nil {
		return fmt.Errorf("worktree: rollback checkout: %w: %s", err, out)
	}
	if out, err := runGit(cctx, worktree, "clean", "-fd"); err != nil {
		return fmt.Errorf("worktree: rollback clean: %w: %s", err, out)
	}
	return nil
}

// CommitUnit commits the worktree's current changes under a fixed
// machine identity, independent of host git config.
func (w *Workspace) CommitUnit(ctx context.Context, worktree, message string) error {
	cctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	if out, err := runGit(cctx, worktree, "add", "-A"); err != nil {
		return fmt.Errorf("worktree: commit add: %w: %s", err, out)
	}
	cmd := exec.CommandContext(cctx, "git",
		"-c", "user.name="+commitName, "-c", "user.email="+commitEmail,
		"commit", "-m", message, "--allow-empty")
	cmd.Dir = worktree
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: commit: %w: %s", err, string(out))
	}
	return nil
}

// Teardown removes the workspace. Coding missions: `git worktree
// remove --force` (the branch itself survives, only the checkout
// goes). Other kinds: os.RemoveAll.
func (w *Workspace) Teardown(ctx context.Context, workspace, worktree, kind string) error {
	if kind == "coding" && worktree != "" {
		cctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
		defer cancel()
		// git worktree remove must run from the main repo, not the
		// worktree being removed — but it also works from any directory
		// git can resolve the worktree path from, so run it unanchored.
		cmd := exec.CommandContext(cctx, "git", "worktree", "remove", "--force", worktree)
		if out, err := cmd.CombinedOutput(); err != nil {
			w.log.Warn("worktree: remove failed, falling back to rmdir", "worktree", worktree, "error", err, "output", string(out))
		}
	}
	if err := os.RemoveAll(workspace); err != nil {
		return fmt.Errorf("worktree: teardown: rmdir %s: %w", workspace, err)
	}
	return nil
}

// VerifyWorkspace guards against a parallel-mission "phantom
// completion" race: before trusting a worktree's state as this
// mission's, compare the absolute, symlink-resolved path against what
// SetProvisioned recorded.
func (w *Workspace) VerifyWorkspace(recorded, actual string) error {
	rp, err := filepath.EvalSymlinks(recorded)
	if err != nil {
		return fmt.Errorf("worktree: verify: resolve recorded path %s: %w", recorded, err)
	}
	ap, err := filepath.EvalSymlinks(actual)
	if err != nil {
		return fmt.Errorf("worktree: verify: resolve actual path %s: %w", actual, err)
	}
	if rp != ap {
		return fmt.Errorf("worktree: verify: recorded %s resolves to %s, actual %s resolves to %s — phantom completion guard tripped", recorded, rp, actual, ap)
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
