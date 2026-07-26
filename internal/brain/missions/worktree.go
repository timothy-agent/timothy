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
	// identity resolves the operator's configured git author (name,
	// email) per commit; nil or empty-returning falls back to the fixed
	// commitName/commitEmail constants.
	identity func(context.Context) (name, email string)
}

func NewWorkspace(root string, identity func(context.Context) (name, email string), log *slog.Logger) *Workspace {
	return &Workspace{root: root, identity: identity, log: log}
}

// Provision creates the mission's directory. Coding missions run `git
// worktree add -b mission/<slug> <dir>` against repoPath and capture
// the base commit with a tight timeout (degrades to the sentinel
// "(unavailable)" rather than blocking). Non-coding missions get a
// plain directory, no git.
func (w *Workspace) Provision(ctx context.Context, missionID, goal, kind, repoPath string) (workspace, worktree, branch, baseCommit string, err error) {
	workspace = filepath.Join(w.root, missionID)
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		return "", "", "", "", fmt.Errorf("worktree: provision: mkdir %s: %w", workspace, err)
	}

	if kind != "coding" {
		return workspace, "", "", "", nil
	}

	branch = "mission/" + Slug(goal, missionID)
	worktree = filepath.Join(workspace, "wt")
	gctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	cmd := exec.CommandContext(gctx, "git", "worktree", "add", "-b", branch, worktree) //nolint:gosec // branch is Slug()-derived (alphanumeric+hyphen only), not raw user input
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

// CommitUnit commits the worktree's current changes, authored under
// the operator's configured git identity when set (per-field fallback
// to commitName/commitEmail otherwise), independent of host git config.
func (w *Workspace) CommitUnit(ctx context.Context, worktree, message string) error {
	cctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	if out, err := runGit(cctx, worktree, "add", "-A"); err != nil {
		return fmt.Errorf("worktree: commit add: %w: %s", err, out)
	}
	name, email := commitName, commitEmail
	if w.identity != nil {
		if n, e := w.identity(ctx); true {
			if n != "" {
				name = n
			}
			if e != "" {
				email = e
			}
		}
	}
	cmd := exec.CommandContext(cctx, "git", //nolint:gosec // name/email may be operator-supplied but travel as -c key=value args, never shell-interpolated; message is driver-built from mission id/iteration, not user input
		"-c", "user.name="+name, "-c", "user.email="+email,
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
		cmd := exec.CommandContext(cctx, "git", "worktree", "remove", "--force", worktree) //nolint:gosec // worktree is a path this package created under its own workspace root, not user input
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
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // callers pass only fixed subcommands/internally-derived paths, never raw user input
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
