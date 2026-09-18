package destinations

import (
	"context"
	"fmt"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// BitbucketAdapter is GitHubAdapter's twin for bitbucket-kind
// destinations: same push and PR flow through the connector, with the
// repo URL parsed as workspace/slug. Config is GitHubConfig; the two
// kinds share its shape.
type BitbucketAdapter struct {
	Pusher       pusher
	Events       events
	ResolveToken PushTokenResolver
	PR           PRSource
	Attribution  func(context.Context) bool
}

func NewBitbucketAdapter(p pusher, e events, resolveToken PushTokenResolver, pr PRSource) *BitbucketAdapter {
	return &BitbucketAdapter{Pusher: p, Events: e, ResolveToken: resolveToken, PR: pr}
}

func (a *BitbucketAdapter) attribution(ctx context.Context) bool {
	return a.Attribution == nil || a.Attribution(ctx)
}

// PushBranch pushes m's branch and records mission.pushed or
// mission.push_failed, the same events the github path records.
func (a *BitbucketAdapter) PushBranch(ctx context.Context, m missions.Mission, token string) (string, error) {
	return pushBranch(ctx, a.Pusher, a.Events, m, token)
}

// OpenPR pushes then opens (or fetches the existing) pull request on
// the mission's own repo.
func (a *BitbucketAdapter) OpenPR(ctx context.Context, m missions.Mission, token string) (string, int, error) {
	workspace, slug, ok := missions.ParseBitbucketRepoURL(m.RepoURL())
	if !ok {
		return "", 0, fmt.Errorf("pr: mission repo_url is not a recognizable bitbucket https clone URL")
	}
	return a.openPRFor(ctx, m, token, m.ConnectorID(), workspace, slug)
}

func (a *BitbucketAdapter) openPRFor(ctx context.Context, m missions.Mission, token, connectorID, workspace, slug string) (string, int, error) {
	if a.PR == nil {
		return "", 0, fmt.Errorf("pr: connectors are not enabled")
	}
	if _, err := a.PushBranch(ctx, m, token); err != nil {
		return "", 0, err
	}
	base, err := a.PR.DefaultBranch(ctx, connectorID, workspace, slug)
	if err != nil {
		return "", 0, fmt.Errorf("pr: look up repo default branch: %w", err)
	}
	if base == "" {
		return "", 0, fmt.Errorf("pr: repo has no default branch")
	}
	url, number, err := a.PR.CreatePR(ctx, connectorID, workspace, slug, missions.ConventionalPRTitle(m), m.Branch, base, PRBody(m, a.attribution(ctx)))
	if err != nil {
		return "", 0, fmt.Errorf("pr: %w", err)
	}
	if err := a.Events.AppendEvent(ctx, m.ID, "mission.pr_opened", map[string]any{"url": url, "number": number}); err != nil {
		return url, number, fmt.Errorf("pr: record pr_opened: %w", err)
	}
	return url, number, nil
}

// ensureRepo resolves the push target, creating the repo when it does
// not exist and createIfMissing is set. Same cases as the github one, plus:
// an existing target always becomes the worktree's origin, so a scratch
// mission (never cloned, no remote) can still push to a repo that exists.
func (a *BitbucketAdapter) ensureRepo(ctx context.Context, m missions.Mission, connectorID, repoURL string, createIfMissing bool) (string, bool, error) {
	if repoURL == "" && !createIfMissing {
		return repoURL, false, nil
	}
	if connectorID == "" {
		return repoURL, false, fmt.Errorf("ensure repo: no connector configured for the bitbucket destination")
	}
	if a.PR == nil {
		return repoURL, false, fmt.Errorf("ensure repo: connectors are not enabled")
	}

	if repoURL != "" {
		workspace, slug, ok := missions.ParseBitbucketRepoURL(repoURL)
		if !ok {
			return repoURL, false, fmt.Errorf("ensure repo: repo_url is not a recognizable bitbucket https clone URL")
		}
		exists, err := a.PR.RepoExists(ctx, connectorID, workspace, slug)
		if err != nil {
			return repoURL, false, fmt.Errorf("ensure repo: check existence: %w", err)
		}
		if exists {
			cloneURL := "https://bitbucket.org/" + workspace + "/" + slug + ".git"
			if err := a.Pusher.SetOrigin(ctx, m.WorktreePath(), cloneURL); err != nil {
				return repoURL, false, fmt.Errorf("ensure repo: point worktree at repo: %w", err)
			}
			return cloneURL, cloneURL != repoURL, nil
		}
		if !createIfMissing {
			return repoURL, false, fmt.Errorf("ensure repo: repo %s/%s does not exist and create_if_missing is not set", workspace, slug)
		}
		created, err := a.PR.CreateRepo(ctx, connectorID, workspace+"/"+slug, true)
		if err != nil {
			return repoURL, false, fmt.Errorf("ensure repo: create %s/%s: %w", workspace, slug, err)
		}
		cloneURL, ok := missions.BitbucketCloneURL(created)
		if !ok {
			return repoURL, false, fmt.Errorf("ensure repo: created repo has an unusable clone URL")
		}
		if err := a.Pusher.SetOrigin(ctx, m.WorktreePath(), cloneURL); err != nil {
			return repoURL, false, fmt.Errorf("ensure repo: point worktree at new repo: %w", err)
		}
		return cloneURL, true, nil
	}

	name := missions.Slug(m.Goal, m.ID)
	created, err := a.PR.CreateRepo(ctx, connectorID, name, true)
	if err != nil {
		return repoURL, false, fmt.Errorf("ensure repo: create %s: %w", name, err)
	}
	// Bitbucket returns the user@ form for a user principal, which
	// validateRemote rejects (issue #787).
	cloneURL, ok := missions.BitbucketCloneURL(created)
	if !ok {
		return repoURL, false, fmt.Errorf("ensure repo: created repo has an unusable clone URL")
	}
	if err := a.Pusher.SetOrigin(ctx, m.WorktreePath(), cloneURL); err != nil {
		return repoURL, false, fmt.Errorf("ensure repo: point worktree at new repo: %w", err)
	}
	return cloneURL, true, nil
}

// DeliverMission runs a saved bitbucket destination for m and updates e
// with the final repo, branch, host, and PR when one was opened.
func (a *BitbucketAdapter) DeliverMission(ctx context.Context, cfg GitHubConfig, m missions.Mission, e *missions.DestinationEntry) error {
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
		workspace, slug, ok := missions.ParseBitbucketRepoURL(e.RepoURL)
		if !ok {
			return fmt.Errorf("deliver: repo_url is not a recognizable bitbucket https clone URL")
		}
		url, number, err := a.openPRFor(ctx, m, token, cfg.ConnectorID, workspace, slug)
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
