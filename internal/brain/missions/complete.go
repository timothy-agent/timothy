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
// OpenPR/ensureRepo need, a local interface (missions has no
// compile-time dependency on the connectors package) satisfied by a
// small adapter cmd/brain/main.go wires, same pattern as
// CloneTokenResolver.
type PRSource interface {
	// DefaultBranch resolves owner/repo's default branch through
	// connectorID's credential.
	DefaultBranch(ctx context.Context, connectorID, owner, repo string) (string, error)
	// CreatePR opens (or fetches the existing) pull request through
	// connectorID's credential.
	CreatePR(ctx context.Context, connectorID, owner, repo, title, head, base, body string) (prURL string, prNumber int, err error)
	// RepoExists reports whether owner/repo exists (and is visible)
	// through connectorID's credential, ensureRepo's existence check
	// (issue #483). ok is false, err is nil for a confirmed 404;
	// ok is false, err is non-nil for any other lookup failure (network,
	// auth), which ensureRepo treats as a hard failure rather than
	// "safe to create."
	RepoExists(ctx context.Context, connectorID, owner, repo string) (ok bool, err error)
	// CreateRepo creates a new repo named name through connectorID's
	// credential, returning its https clone URL.
	CreateRepo(ctx context.Context, connectorID, name string, private bool) (cloneURL string, err error)
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
// https origin. SetOrigin backs ensureRepo's create-if-missing path
// (issue #483): pointing the worktree's origin at a repo the mission
// was never cloned from before Push/OpenPR use it.
type branchPusher interface {
	Push(ctx context.Context, worktree, branch, token string) (string, error)
	SetOrigin(ctx context.Context, worktree, remoteURL string) error
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
	if len(m.Plan.Units) > 0 {
		body += "## Units\n\n"
		for _, u := range m.Plan.Units {
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
// missions only -- callers check m.ConnectorID()/m.RepoURL() first.
func (c *Completer) OpenPR(ctx context.Context, m Mission, token string) (url string, number int, err error) {
	connectorID := m.ConnectorID()
	owner, repo, ok := ParseGitHubRepoURL(m.RepoURL())
	if !ok {
		return "", 0, fmt.Errorf("pr: mission repo_url is not a recognizable github https clone URL")
	}
	return c.openPRFor(ctx, m, token, connectorID, owner, repo)
}

// openPRFor is OpenPR's actual implementation, parameterized by the
// push target instead of always deriving it from m.RepoURL()/
// m.ConnectorID() (the mission's clone SOURCE): RunOnComplete's
// create-if-missing path (issue #483) calls this directly with the
// github DESTINATION entry's own (possibly just-created) repo, which
// can legitimately differ from where the mission was cloned from (or
// name a repo when the mission was never cloned from one at all, a
// scratch mission). OpenPR above is the pre-#483 behavior: push target
// == clone source, exactly as before.
func (c *Completer) openPRFor(ctx context.Context, m Mission, token, connectorID, owner, repo string) (url string, number int, err error) {
	if c.pr == nil {
		return "", 0, fmt.Errorf("pr: connectors are not enabled")
	}
	if _, err := c.PushBranch(ctx, m, token); err != nil {
		return "", 0, err
	}
	base, err := c.pr.DefaultBranch(ctx, connectorID, owner, repo)
	if err != nil {
		return "", 0, fmt.Errorf("pr: look up repo default branch: %w", err)
	}
	if base == "" {
		return "", 0, fmt.Errorf("pr: repo has no default branch")
	}
	url, number, err = c.pr.CreatePR(ctx, connectorID, owner, repo, PRTitle(m), m.Branch, base, PRBody(m))
	if err != nil {
		return "", 0, fmt.Errorf("pr: %w", err)
	}
	if err := c.store.AppendEvent(ctx, m.ID, "mission.pr_opened", map[string]any{"url": url, "number": number}); err != nil {
		return url, number, fmt.Errorf("pr: record pr_opened: %w", err)
	}
	return url, number, nil
}

// ensureRepo resolves the github destination entry's push target
// before PushBranch/OpenPR run, creating the repo when it doesn't
// exist and CreateIfMissing is set (issue #483). Three cases:
//
//  1. entry.RepoURL is already set and the repo exists: no-op, the
//     worktree's origin (set at provisioning, or by a prior call to
//     this same method) already points at it.
//  2. entry.RepoURL is set (or, if empty, derived from the mission's
//     goal/id via Slug) and the repo does NOT exist: CreateIfMissing
//     false fails honestly ("never worker-invented," per issue #483's
//     AC) rather than guessing; true creates it via pr.CreateRepo,
//     points the worktree's origin at the new clone URL (SetOrigin),
//     and returns the entry with RepoURL updated to the final URL.
//  3. A prior attempt already created the repo (retry via the
//     autoResumeInfra sweep): RepoExists now reports true, so this
//     no-ops instead of erroring or re-creating, the same
//     "check existence first" idempotency deliverToDestinations'
//     DeliveredAt check already follows for email/webhook/telegram.
//
// Returns the (possibly updated) entry so the caller can persist it,
// and updated=true only when RepoURL actually changed (the caller's
// signal to call SetDestinations). No-ops entirely (updated=false, nil
// error) when the entry has neither RepoURL nor CreateIfMissing set:
// the pre-#483 default, delivery proceeds against whatever origin the
// worktree already has.
func (c *Completer) ensureRepo(ctx context.Context, m Mission, entry DestinationEntry) (DestinationEntry, bool, error) {
	if entry.RepoURL == "" && !entry.CreateIfMissing {
		return entry, false, nil
	}
	connectorID := entry.ConnectorID
	if connectorID == "" {
		connectorID = m.ConnectorID()
	}
	if connectorID == "" {
		return entry, false, fmt.Errorf("ensure repo: no connector configured for the github destination")
	}
	if c.pr == nil {
		return entry, false, fmt.Errorf("ensure repo: connectors are not enabled")
	}

	owner, repo, haveOwner := "", "", false
	if entry.RepoURL != "" {
		owner, repo, haveOwner = ParseGitHubRepoURL(entry.RepoURL)
		if !haveOwner {
			return entry, false, fmt.Errorf("ensure repo: repo_url is not a recognizable github https clone URL")
		}
	}

	if haveOwner {
		exists, err := c.pr.RepoExists(ctx, connectorID, owner, repo)
		if err != nil {
			return entry, false, fmt.Errorf("ensure repo: check existence: %w", err)
		}
		if exists {
			return entry, false, nil
		}
		if !entry.CreateIfMissing {
			return entry, false, fmt.Errorf("ensure repo: repo %s/%s does not exist and create_if_missing is not set", owner, repo)
		}
		cloneURL, err := c.pr.CreateRepo(ctx, connectorID, repo, true)
		if err != nil {
			return entry, false, fmt.Errorf("ensure repo: create %s/%s: %w", owner, repo, err)
		}
		if err := c.workspace.SetOrigin(ctx, m.WorktreePath(), cloneURL); err != nil {
			return entry, false, fmt.Errorf("ensure repo: point worktree at new repo: %w", err)
		}
		entry.RepoURL = cloneURL
		return entry, true, nil
	}

	// No RepoURL at all: CreateIfMissing (checked above) with no name to
	// resolve against GitHub -- derive one from the mission itself,
	// same slug ExpandBranchPattern already uses for the branch name.
	name := Slug(m.Goal, m.ID)
	cloneURL, err := c.pr.CreateRepo(ctx, connectorID, name, true)
	if err != nil {
		return entry, false, fmt.Errorf("ensure repo: create %s: %w", name, err)
	}
	if err := c.workspace.SetOrigin(ctx, m.WorktreePath(), cloneURL); err != nil {
		return entry, false, fmt.Errorf("ensure repo: point worktree at new repo: %w", err)
	}
	entry.RepoURL = cloneURL
	entry.ConnectorID = connectorID
	return entry, true, nil
}

// RunOnComplete executes m's recorded on_complete choice ("push" or
// "push_pr") — called by the driver exactly once, right after the
// mission's own ApplyTransition into phase=done succeeds. Failure never
// un-dones a verified mission: it appends mission.push_failed (already
// scrubbed of the token by PushBranch/OpenPR's error paths) and returns
// the error for the caller to fire a notification; the mission row
// itself is untouched either way. No retry loop — one attempt, the
// manual push/pr endpoints remain available for the operator.
//
// Before pushing, this runs ensureRepo against the mission's github
// destination entry (issue #483): when that entry names its own repo
// (or asks for one to be created), ensureRepo may create it and
// redirect the worktree's origin, in which case the push/PR target is
// the entry's repo, not m.RepoURL()'s clone source (see openPRFor).
// entry, updated is the caller's (fireOnComplete's) signal to persist
// the entry via SetDestinations: updated is true only when ensureRepo
// actually changed RepoURL (a repo was created this call).
func (c *Completer) RunOnComplete(ctx context.Context, m Mission) (entry DestinationEntry, updated bool, err error) {
	onComplete := m.OnComplete()
	entry, _ = m.GitHubEntry()
	if onComplete == "" {
		return entry, false, nil
	}
	if reason := NotPushable(m); reason != "" {
		return entry, false, fmt.Errorf("on_complete: %s", reason)
	}
	if c.resolveToken == nil {
		return entry, false, fmt.Errorf("on_complete: connectors are not enabled")
	}

	entry, updated, err = c.ensureRepo(ctx, m, entry)
	if err != nil {
		return entry, updated, fmt.Errorf("on_complete: %w", err)
	}

	connectorID := entry.ConnectorID
	if connectorID == "" {
		connectorID = m.ConnectorID()
	}
	token, err := c.resolveToken(ctx, connectorID)
	if err != nil {
		return entry, updated, fmt.Errorf("on_complete: resolve token: %w", err)
	}

	switch onComplete {
	case "push":
		_, err := c.PushBranch(ctx, m, token)
		return entry, updated, err
	case "push_pr":
		if entry.RepoURL != "" {
			owner, repo, ok := ParseGitHubRepoURL(entry.RepoURL)
			if !ok {
				return entry, updated, fmt.Errorf("on_complete: destination repo_url is not a recognizable github https clone URL")
			}
			_, _, err := c.openPRFor(ctx, m, token, connectorID, owner, repo)
			return entry, updated, err
		}
		_, _, err := c.OpenPR(ctx, m, token)
		return entry, updated, err
	default:
		return entry, updated, fmt.Errorf("on_complete: unknown value %q", onComplete)
	}
}
