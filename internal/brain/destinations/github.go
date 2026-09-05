package destinations

import (
	"context"
	"errors"
	"fmt"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// PushTokenResolver resolves a github-connection mission's own PAT from
// its connector_id: DeliverMission never has an explicit credential_ref
// override, unlike the manual push/pr API endpoints, so this is exactly
// api/missions.go's resolvePushToken minus the credential_ref-override
// branch.
type PushTokenResolver func(ctx context.Context, connectorID string) (string, error)

// PRSource is the narrow slice of a GitHub connector's repo operations
// OpenPR/ensureRepo need, a local interface (destinations has no
// compile-time dependency on the connectors package) satisfied by a
// small adapter cmd/brain/main.go wires.
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

// pusher is the narrow slice of *missions.Workspace GitHubAdapter
// needs, kept as an interface so tests can fake the push (exercising
// the https-only validation is push_test.go's own coverage) without a
// real https origin. SetOrigin backs ensureRepo's create-if-missing
// path (issue #483): pointing the worktree's origin at a repo the
// mission was never cloned from before Push/OpenPR use it.
type pusher interface {
	Push(ctx context.Context, worktree, branch, token string) (string, error)
	SetOrigin(ctx context.Context, worktree, remoteURL string) error
}

// events is the narrow slice of *missions.Store GitHubAdapter needs,
// kept as an interface so tests can fake it without a real Postgres
// pool.
type events interface {
	AppendEvent(ctx context.Context, id, kind string, payload map[string]any) error
}

// GitHubAdapter runs a mission's push/push+PR delivery for a saved
// github-kind destination. It shares PushBranch/OpenPR's actual
// push-and-record-event logic with the manual push/pr API endpoints
// (api/missions.go calls the same two methods), so neither path can
// diverge in what "push" or "PR" means or which events land on the
// Timeline.
type GitHubAdapter struct {
	Pusher       pusher
	Events       events
	ResolveToken PushTokenResolver
	PR           PRSource
}

// NewGitHubAdapter builds a GitHubAdapter. resolveToken/pr may be nil
// (no secret store / no connectors wired): DeliverMission reports a
// plain error in that case rather than a panic; pusher/events are
// always present since missions itself requires both to run at all.
func NewGitHubAdapter(p pusher, e events, resolveToken PushTokenResolver, pr PRSource) *GitHubAdapter {
	return &GitHubAdapter{Pusher: p, Events: e, ResolveToken: resolveToken, PR: pr}
}

// PushBranch pushes m's branch to its worktree's origin remote with
// token, recording mission.pushed/mission.push_failed either way: the
// shared event-recording shape both the manual push endpoint and the
// driver's auto-fire hook use, so the Timeline reads identically
// regardless of which one fired.
func (a *GitHubAdapter) PushBranch(ctx context.Context, m missions.Mission, token string) (host string, err error) {
	host, pushErr := a.Pusher.Push(ctx, m.WorktreePath(), m.Branch, token)
	if pushErr != nil {
		reason := "push failed"
		switch {
		case errors.Is(pushErr, missions.ErrRemoteUnsupported):
			reason = "remote unsupported"
		case errors.Is(pushErr, missions.ErrPushRejected):
			reason = "push rejected"
		}
		if err := a.Events.AppendEvent(ctx, m.ID, "mission.push_failed", map[string]any{"reason": reason}); err != nil {
			return "", fmt.Errorf("push: record push_failed: %w", err)
		}
		return "", pushErr
	}
	if err := a.Events.AppendEvent(ctx, m.ID, "mission.pushed", map[string]any{"branch": m.Branch, "remote_host": host}); err != nil {
		return host, fmt.Errorf("push: record pushed: %w", err)
	}
	return host, nil
}

// PRBody composes the pull request's markdown body: goal, unit list
// with pass state, and a short harness-verification line.
func PRBody(m missions.Mission) string {
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

// OpenPR pushes the branch (idempotent re-push, same code path
// PushBranch above uses) then opens (or fetches the existing) pull
// request via PR, recording mission.pr_opened. github-connection
// missions only -- callers check m.ConnectorID()/m.RepoURL() first.
func (a *GitHubAdapter) OpenPR(ctx context.Context, m missions.Mission, token string) (url string, number int, err error) {
	connectorID := m.ConnectorID()
	owner, repo, ok := missions.ParseGitHubRepoURL(m.RepoURL())
	if !ok {
		return "", 0, fmt.Errorf("pr: mission repo_url is not a recognizable github https clone URL")
	}
	return a.openPRFor(ctx, m, token, connectorID, owner, repo)
}

// openPRFor is OpenPR's actual implementation, parameterized by the
// push target instead of always deriving it from m.RepoURL()/
// m.ConnectorID() (the mission's clone SOURCE): the create-if-missing
// path (issue #483) calls this directly with the resolved (possibly
// just-created) target repo, which can legitimately differ from where
// the mission was cloned from (or name a repo when the mission was
// never cloned from one at all, a scratch mission). OpenPR above is the
// pre-#483 behavior: push target == clone source, exactly as before.
func (a *GitHubAdapter) openPRFor(ctx context.Context, m missions.Mission, token, connectorID, owner, repo string) (url string, number int, err error) {
	if a.PR == nil {
		return "", 0, fmt.Errorf("pr: connectors are not enabled")
	}
	if _, err := a.PushBranch(ctx, m, token); err != nil {
		return "", 0, err
	}
	base, err := a.PR.DefaultBranch(ctx, connectorID, owner, repo)
	if err != nil {
		return "", 0, fmt.Errorf("pr: look up repo default branch: %w", err)
	}
	if base == "" {
		return "", 0, fmt.Errorf("pr: repo has no default branch")
	}
	url, number, err = a.PR.CreatePR(ctx, connectorID, owner, repo, missions.PRTitle(m), m.Branch, base, PRBody(m))
	if err != nil {
		return "", 0, fmt.Errorf("pr: %w", err)
	}
	if err := a.Events.AppendEvent(ctx, m.ID, "mission.pr_opened", map[string]any{"url": url, "number": number}); err != nil {
		return url, number, fmt.Errorf("pr: record pr_opened: %w", err)
	}
	return url, number, nil
}

// ensureRepo resolves a github delivery's push target before
// PushBranch/OpenPR run, creating the repo when it doesn't exist and
// createIfMissing is set (issue #483). Three cases:
//
//  1. repoURL is already set and the repo exists: no-op, the worktree's
//     origin (set at provisioning, or by a prior call to this same
//     method) already points at it.
//  2. repoURL is set (or, if empty, derived from the mission's
//     goal/id via Slug) and the repo does NOT exist: createIfMissing
//     false fails honestly ("never worker-invented," per issue #483's
//     AC) rather than guessing; true creates it via PR.CreateRepo,
//     points the worktree's origin at the new clone URL (SetOrigin),
//     and returns the final RepoURL.
//  3. A prior attempt already created the repo (retry via the
//     autoResumeInfra sweep): RepoExists now reports true, so this
//     no-ops instead of erroring or re-creating.
//
// Returns the resolved repoURL and updated=true only when it actually
// changed (the caller's signal to persist it). No-ops entirely
// (updated=false, nil error) when called with neither repoURL nor
// createIfMissing set: delivery proceeds against whatever origin the
// worktree already has.
func (a *GitHubAdapter) ensureRepo(ctx context.Context, m missions.Mission, connectorID, repoURL string, createIfMissing bool) (finalRepoURL string, updated bool, err error) {
	if repoURL == "" && !createIfMissing {
		return repoURL, false, nil
	}
	if connectorID == "" {
		return repoURL, false, fmt.Errorf("ensure repo: no connector configured for the github destination")
	}
	if a.PR == nil {
		return repoURL, false, fmt.Errorf("ensure repo: connectors are not enabled")
	}

	owner, repo, haveOwner := "", "", false
	if repoURL != "" {
		owner, repo, haveOwner = missions.ParseGitHubRepoURL(repoURL)
		if !haveOwner {
			return repoURL, false, fmt.Errorf("ensure repo: repo_url is not a recognizable github https clone URL")
		}
	}

	if haveOwner {
		exists, err := a.PR.RepoExists(ctx, connectorID, owner, repo)
		if err != nil {
			return repoURL, false, fmt.Errorf("ensure repo: check existence: %w", err)
		}
		if exists {
			return repoURL, false, nil
		}
		if !createIfMissing {
			return repoURL, false, fmt.Errorf("ensure repo: repo %s/%s does not exist and create_if_missing is not set", owner, repo)
		}
		cloneURL, err := a.PR.CreateRepo(ctx, connectorID, repo, true)
		if err != nil {
			return repoURL, false, fmt.Errorf("ensure repo: create %s/%s: %w", owner, repo, err)
		}
		if err := a.Pusher.SetOrigin(ctx, m.WorktreePath(), cloneURL); err != nil {
			return repoURL, false, fmt.Errorf("ensure repo: point worktree at new repo: %w", err)
		}
		return cloneURL, true, nil
	}

	// No repoURL at all: createIfMissing (checked above) with no name to
	// resolve against GitHub -- derive one from the mission itself, same
	// slug ExpandBranchPattern already uses for the branch name.
	name := missions.Slug(m.Goal, m.ID)
	cloneURL, err := a.PR.CreateRepo(ctx, connectorID, name, true)
	if err != nil {
		return repoURL, false, fmt.Errorf("ensure repo: create %s: %w", name, err)
	}
	if err := a.Pusher.SetOrigin(ctx, m.WorktreePath(), cloneURL); err != nil {
		return repoURL, false, fmt.Errorf("ensure repo: point worktree at new repo: %w", err)
	}
	return cloneURL, true, nil
}

// DeliverMission runs a saved "github" destination's delivery for m
// (issue #560): the Deliverer's own entry point. Repo resolution order:
// (a) e.RepoURL if already set, (b) else m.RepoURL() (the mission's own
// clone source), (c) else, if cfg.CreateIfMissing, create a repo named
// after the mission's goal/id, (d) else fail. On success e is updated
// in place with the final RepoURL/Branch/RemoteHost, and PRURL/PRNumber
// when a PR was opened.
func (a *GitHubAdapter) DeliverMission(ctx context.Context, cfg GitHubConfig, m missions.Mission, e *missions.DestinationEntry) error {
	if reason := missions.NotPushable(m); reason != "" {
		return fmt.Errorf("deliver: %s", reason)
	}
	if a.ResolveToken == nil {
		return fmt.Errorf("deliver: connectors are not enabled")
	}

	repoURL := e.RepoURL
	if repoURL == "" {
		repoURL = m.RepoURL()
	}
	if repoURL == "" && !cfg.CreateIfMissing {
		return fmt.Errorf("deliver: no target repository and create_if_missing is off")
	}
	finalRepoURL, _, err := a.ensureRepo(ctx, m, cfg.ConnectorID, repoURL, cfg.CreateIfMissing)
	if err != nil {
		return fmt.Errorf("deliver: %w", err)
	}
	e.RepoURL = finalRepoURL

	token, err := a.ResolveToken(ctx, cfg.ConnectorID)
	if err != nil {
		return fmt.Errorf("deliver: resolve token: %w", err)
	}

	switch cfg.Mode {
	case "push":
		host, err := a.PushBranch(ctx, m, token)
		if err != nil {
			return fmt.Errorf("deliver: %w", err)
		}
		e.Branch = m.Branch
		e.RemoteHost = host
		return nil
	case "push_pr":
		owner, repo, ok := missions.ParseGitHubRepoURL(e.RepoURL)
		if !ok {
			return fmt.Errorf("deliver: repo_url is not a recognizable github https clone URL")
		}
		url, number, err := a.openPRFor(ctx, m, token, cfg.ConnectorID, owner, repo)
		if err != nil {
			return fmt.Errorf("deliver: %w", err)
		}
		e.Branch = m.Branch
		e.PRURL = url
		e.PRNumber = number
		return nil
	default:
		return fmt.Errorf("deliver: unknown mode %q", cfg.Mode)
	}
}
