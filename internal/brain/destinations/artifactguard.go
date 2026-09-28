package destinations

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// ErrDeliveryNoArtifacts is the D-122 delivery-side guard (issue #949):
// the diff base_commit..HEAD shares no path with the plan's declared
// artifacts/scope, so push_pr delivery refuses rather than opening a
// PR whose content nothing in the plan named (mission f78f7fff opened
// a PR whose only file was an undeclared core dump).
var ErrDeliveryNoArtifacts = errors.New("delivery_no_artifacts")

// checkIgnoreTimeout bounds the git check-ignore probe, run only once
// the guard is about to fail, to explain an empty diff.
const checkIgnoreTimeout = 10 * time.Second

// artifactGuardResult is checkDeliveryArtifacts' result: diff/extra
// always set, ignored only computed on failure (checking .gitignore is
// wasted work otherwise).
type artifactGuardResult struct {
	diff    []string // every path base_commit..HEAD changed
	extra   []string // diff paths not covered by any declared path
	ignored []string // declared paths git ignores in worktree
}

// declaredPaths is the union of every unit's artifacts and scope,
// cleaned and deduplicated in first-seen order: what the plan
// authorized the mission to touch.
func declaredPaths(units []missions.PlanUnit) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		c := cleanDeliveryPath(p)
		if c == "" || seen[c] {
			return
		}
		seen[c] = true
		out = append(out, c)
	}
	for _, u := range units {
		for _, a := range u.Artifacts {
			add(a)
		}
		for _, s := range u.Scope {
			add(s)
		}
	}
	return out
}

func cleanDeliveryPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return path.Clean(filepath.ToSlash(p))
}

// pathCovered reports whether diffPath is authorized by declared: an
// exact match, or a declared entry naming a directory prefix of it.
func pathCovered(diffPath string, declared []string) bool {
	c := cleanDeliveryPath(diffPath)
	for _, d := range declared {
		if c == d || strings.HasPrefix(c, d+"/") {
			return true
		}
	}
	return false
}

// checkDeliveryArtifacts is the D-122 guard, run before any push_pr
// push (issue #949). It fails with ErrDeliveryNoArtifacts when the
// diff base_commit..HEAD shares no path with the plan's declared
// artifacts/scope; on failure it also probes which declared paths git
// ignores in worktree, since a gitignored artifact explains an empty
// diff the worker could never have committed.
func checkDeliveryArtifacts(ctx context.Context, worktree, baseCommit string, plan missions.Plan) (artifactGuardResult, error) {
	declared := declaredPaths(plan.Units)
	diff, err := missions.ChangedFiles(ctx, worktree, baseCommit)
	if err != nil {
		return artifactGuardResult{}, fmt.Errorf("%w: read diff: %v", ErrDeliveryNoArtifacts, err)
	}
	var extra []string
	matched := false
	for _, p := range diff {
		if pathCovered(p, declared) {
			matched = true
		} else {
			extra = append(extra, p)
		}
	}
	if matched {
		return artifactGuardResult{diff: diff, extra: extra}, nil
	}
	ignored := gitignoredArtifacts(ctx, worktree, declared)
	guardErr := fmt.Errorf("%w: diff has no declared artifact or scope path (diff: %s)", ErrDeliveryNoArtifacts, joinOrNone(diff))
	if len(ignored) > 0 {
		guardErr = fmt.Errorf("%w; gitignored: %s", guardErr, strings.Join(ignored, ", "))
	}
	return artifactGuardResult{diff: diff, ignored: ignored}, guardErr
}

// joinOrNone renders diff for the failure message, "none" when the
// diff itself is empty (a no-op push, not just an unrelated one).
func joinOrNone(diff []string) string {
	if len(diff) == 0 {
		return "none"
	}
	return strings.Join(diff, ", ")
}

// gitignoredArtifacts reports which of declared paths git considers
// ignored in worktree: a worker can never have committed one of these,
// so their presence explains an otherwise-empty diff. Any probe error
// (git missing, timeout) is swallowed since this only enriches an
// already-failing guard's message.
func gitignoredArtifacts(ctx context.Context, worktree string, declared []string) []string {
	if len(declared) == 0 {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, checkIgnoreTimeout)
	defer cancel()
	args := append([]string{"check-ignore"}, declared...)
	cmd := exec.CommandContext(cctx, "git", args...) //nolint:gosec // declared is plan artifact/scope entries, passed as positional args to check-ignore, not a shell
	cmd.Dir = worktree
	out, _ := cmd.Output() // exit 1 (nothing ignored) is not an error worth surfacing
	var ignored []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			ignored = append(ignored, line)
		}
	}
	return ignored
}
