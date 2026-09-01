package missions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
)

// PushTokenResolver resolves a github-connection mission's own PAT from
// its connector_id — the auto-fire path (RunOnComplete) never has an
// explicit credential_ref override, unlike the manual push/pr API
// endpoints, so this is exactly api/missions.go's resolvePushToken minus
// the credential_ref-override branch. Passed into NewCompleter; the
// driver's own Completer is wired in via Driver.SetCompleter, same
// setter pattern as Driver.SetCloneTokenResolver.
type PushTokenResolver func(ctx context.Context, connectorID string) (string, error)

// PRSource is the narrow slice of a GitHub connector's repo operations
// OpenPR needs — a local interface (missions has no compile-time
// dependency on the connectors package) satisfied by a small adapter
// cmd/brain/main.go wires, same pattern as CloneTokenResolver.
type PRSource interface {
	// DefaultBranch resolves owner/repo's default branch through
	// connectorID's credential.
	DefaultBranch(ctx context.Context, connectorID, owner, repo string) (string, error)
	// CreatePR opens (or fetches the existing) pull request through
	// connectorID's credential.
	CreatePR(ctx context.Context, connectorID, owner, repo, title, head, base, body string) (prURL string, prNumber int, err error)
}

// githubRepoPattern matches the owner/repo path segment of a GitHub
// https clone URL (with or without .git suffix) — the shape
// ParseGitHubRepoURL extracts from mission.RepoURL.
var githubRepoPattern = regexp.MustCompile(`^https://[^/]+/([^/]+)/([^/]+?)(?:\.git)?/?$`)

// ParseGitHubRepoURL extracts owner/repo from repoURL (mission.RepoURL,
// always an https clone URL per validateRemote's own gate at push
// time) — ok is false for anything that doesn't match the expected
// shape.
func ParseGitHubRepoURL(repoURL string) (owner, repo string, ok bool) {
	m := githubRepoPattern.FindStringSubmatch(repoURL)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// eventAppender is the narrow slice of *Store Completer needs — kept as
// an interface so tests can fake it without a real Postgres pool, same
// reasoning as driverStore.
type eventAppender interface {
	AppendEvent(ctx context.Context, id, kind string, payload map[string]any) error
}

// branchPusher is the narrow slice of *Workspace Completer needs — kept
// as an interface so tests can fake the push (exercising the https-only
// validation is push_test.go's job, not this package's) without a real
// https origin.
type branchPusher interface {
	Push(ctx context.Context, worktree, branch, token string) (string, error)
}

// Completer runs the push/push+PR action a mission's on_complete field
// requests when it reaches done — the driver's auto-fire hook. It
// shares PushBranch/OpenPR's actual push-and-record-event logic with
// the manual push/pr API endpoints (internal/brain/api/missions.go
// calls the same two methods), so neither path can diverge in what
// "push" or "PR" means or which events land on the Timeline.
type Completer struct {
	workspace    branchPusher
	store        eventAppender
	resolveToken PushTokenResolver
	pr           PRSource
}

// NewCompleter builds a Completer. resolveToken/pr may be nil (no
// secret store / no connectors wired) — RunOnComplete reports a plain
// error in that case rather than a panic; workspace/store are always
// present since missions itself requires both to run at all.
func NewCompleter(workspace *Workspace, store eventAppender, resolveToken PushTokenResolver, pr PRSource) *Completer {
	return &Completer{workspace: workspace, store: store, resolveToken: resolveToken, pr: pr}
}

// NotPushable reports the shared kind/branch/worktree guards push and
// pr both require — Go code, never a prompt: only a coding mission
// with a live worktree is ever pushable. Shared by the manual push/pr
// API handlers and the driver's auto-fire-on-done hook so neither can
// diverge on what counts as pushable.
func NotPushable(m Mission) string {
	switch {
	case !missionPolicyFor(m).canPush:
		return "only coding missions can be pushed"
	case m.Branch == "":
		return "mission has no branch"
	default:
		if _, err := os.Stat(m.WorktreePath()); err != nil {
			return "mission worktree is not available"
		}
	}
	return ""
}

// PushBranch pushes m's branch to its worktree's origin remote with
// token, recording mission.pushed/mission.push_failed either way — the
// shared event-recording shape both the manual push endpoint and the
// driver's auto-fire hook use, so the Timeline reads identically
// regardless of which one fired.
func (c *Completer) PushBranch(ctx context.Context, m Mission, token string) (host string, err error) {
	host, pushErr := c.workspace.Push(ctx, m.WorktreePath(), m.Branch, token)
	if pushErr != nil {
		reason := "push failed"
		switch {
		case errors.Is(pushErr, ErrRemoteUnsupported):
			reason = "remote unsupported"
		case errors.Is(pushErr, ErrPushRejected):
			reason = "push rejected"
		}
		if err := c.store.AppendEvent(ctx, m.ID, "mission.push_failed", map[string]any{"reason": reason}); err != nil {
			return "", fmt.Errorf("push: record push_failed: %w", err)
		}
		return "", pushErr
	}
	if err := c.store.AppendEvent(ctx, m.ID, "mission.pushed", map[string]any{"branch": m.Branch, "remote_host": host}); err != nil {
		return host, fmt.Errorf("push: record pushed: %w", err)
	}
	return host, nil
}

// PRBody composes the pull request's markdown body: goal, unit list
// with pass state, and a short harness-verification line.
func PRBody(m Mission) string {
	body := m.Goal + "\n\n"
	if len(m.Spec.Units) > 0 {
		body += "## Units\n\n"
		for _, u := range m.Spec.Units {
			mark := "[ ]"
			if u.Passes {
				mark = "[x]"
			}
			body += fmt.Sprintf("- %s %s\n", mark, u.Title)
		}
		body += "\n"
	}
	body += "_Verified by Timothy's mission harness (declared artifacts + verify_cmd, checked by the harness itself, never claimed by the model)._\n"
	return body
}

// PRTitleGoalCap bounds a fallback PR title built from the goal when
// the mission has no generated display name yet.
const PRTitleGoalCap = 72

// PRTitle prefers the mission's generated display name, falling back
// to a truncated goal.
func PRTitle(m Mission) string {
	if m.Name != "" {
		return m.Name
	}
	if len(m.Goal) <= PRTitleGoalCap {
		return m.Goal
	}
	return m.Goal[:PRTitleGoalCap] + "…"
}

// OpenPR pushes the branch (idempotent re-push, same code path
// PushBranch above uses) then opens (or fetches the existing) pull
// request via pr, recording mission.pr_opened. github-connection
// missions only — callers check m.ConnectorID/m.RepoURL first.
func (c *Completer) OpenPR(ctx context.Context, m Mission, token string) (url string, number int, err error) {
	owner, repo, ok := ParseGitHubRepoURL(m.RepoURL)
	if !ok {
		return "", 0, fmt.Errorf("pr: mission repo_url is not a recognizable github https clone URL")
	}
	if c.pr == nil {
		return "", 0, fmt.Errorf("pr: connectors are not enabled")
	}
	if _, err := c.PushBranch(ctx, m, token); err != nil {
		return "", 0, err
	}
	base, err := c.pr.DefaultBranch(ctx, m.ConnectorID, owner, repo)
	if err != nil {
		return "", 0, fmt.Errorf("pr: look up repo default branch: %w", err)
	}
	if base == "" {
		return "", 0, fmt.Errorf("pr: repo has no default branch")
	}
	url, number, err = c.pr.CreatePR(ctx, m.ConnectorID, owner, repo, PRTitle(m), m.Branch, base, PRBody(m))
	if err != nil {
		return "", 0, fmt.Errorf("pr: %w", err)
	}
	if err := c.store.AppendEvent(ctx, m.ID, "mission.pr_opened", map[string]any{"url": url, "number": number}); err != nil {
		return url, number, fmt.Errorf("pr: record pr_opened: %w", err)
	}
	return url, number, nil
}

// RunOnComplete executes m's recorded on_complete choice ("push" or
// "push_pr") — called by the driver exactly once, right after the
// mission's own ApplyTransition into phase=done succeeds. Failure never
// un-dones a verified mission: it appends mission.push_failed (already
// scrubbed of the token by PushBranch/OpenPR's error paths) and returns
// the error for the caller to fire a notification; the mission row
// itself is untouched either way. No retry loop — one attempt, the
// manual push/pr endpoints remain available for the operator.
func (c *Completer) RunOnComplete(ctx context.Context, m Mission) error {
	onComplete := m.OnComplete()
	if onComplete == "" {
		return nil
	}
	if reason := NotPushable(m); reason != "" {
		return fmt.Errorf("on_complete: %s", reason)
	}
	if c.resolveToken == nil {
		return fmt.Errorf("on_complete: connectors are not enabled")
	}
	token, err := c.resolveToken(ctx, m.ConnectorID)
	if err != nil {
		return fmt.Errorf("on_complete: resolve token: %w", err)
	}
	switch onComplete {
	case "push":
		_, err := c.PushBranch(ctx, m, token)
		return err
	case "push_pr":
		_, _, err := c.OpenPR(ctx, m, token)
		return err
	default:
		return fmt.Errorf("on_complete: unknown value %q", onComplete)
	}
}
