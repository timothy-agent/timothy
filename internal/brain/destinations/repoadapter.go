package destinations

import (
	"context"
	"errors"
	"fmt"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// PushTokenResolver resolves a repo-connection mission's own token from
// its connector_id: DeliverMission never has an explicit credential_ref
// override, unlike the manual push/pr API endpoints, so this is exactly
// api/missions.go's resolvePushToken minus the credential_ref-override
// branch.
type PushTokenResolver func(ctx context.Context, connectorID string) (string, error)

// Clients resolves a connector id to its credentialed
// gitprovider.Client plus the func that releases it. A local interface
// (destinations has no compile-time dependency on the connectors
// package) satisfied by a one-line adapter over
// connectors.Manager.GitClient that cmd/brain/main.go wires.
type Clients interface {
	ClientFor(ctx context.Context, connectorID string) (gitprovider.Client, func(), error)
}

// pusher is the narrow slice of *missions.Workspace RepoAdapter needs,
// kept as an interface so tests can fake the push (exercising the
// https-only validation is push_test.go's own coverage) without a real
// https origin. SetOrigin points the worktree's origin at the delivery
// target before Push/OpenPR use it.
type pusher interface {
	Push(ctx context.Context, worktree, branch, token string) (string, error)
	SetOrigin(ctx context.Context, worktree, remoteURL string) error
}

// events is the narrow slice of *missions.Store RepoAdapter needs,
// kept as an interface so tests can fake it without a real Postgres
// pool.
type events interface {
	AppendEvent(ctx context.Context, id, kind string, payload map[string]any) error
}

// RepoAdapter runs a mission's push/push+PR delivery for a saved
// destination of any git provider kind (D-101, issue #795: one adapter
// where github.go and bitbucket.go each carried a near-identical copy).
// It shares PushBranch/OpenPR's actual push-and-record-event logic with
// the manual push/pr API endpoints (api/missions.go calls the same two
// methods), so neither path can diverge in what "push" or "PR" means or
// which events land on the Timeline.
//
// The adapter is kind-neutral: every provider-specific decision (which
// URLs parse, the canonical https clone URL, how a repo name is shaped)
// comes from the gitprovider.Descriptor the resolved Client embeds, so
// a new provider needs no change here.
type RepoAdapter struct {
	Pusher       pusher
	Events       events
	ResolveToken PushTokenResolver
	Clients      Clients
	// Attribution reports whether PR bodies end with the Timothy Agent
	// line (settings.KeyPRAttribution); nil means on.
	Attribution func(context.Context) bool
}

// NewRepoAdapter builds a RepoAdapter. resolveToken/clients may be nil
// (no secret store / no connectors wired): DeliverMission reports a
// plain error in that case rather than a panic; pusher/events are
// always present since missions itself requires both to run at all.
func NewRepoAdapter(p pusher, e events, resolveToken PushTokenResolver, clients Clients) *RepoAdapter {
	return &RepoAdapter{Pusher: p, Events: e, ResolveToken: resolveToken, Clients: clients}
}

// attribution resolves the Attribution hook with its nil default.
func (a *RepoAdapter) attribution(ctx context.Context) bool {
	return a.Attribution == nil || a.Attribution(ctx)
}

// client resolves connectorID's Client, erroring rather than panicking
// when connectors are not wired.
func (a *RepoAdapter) client(ctx context.Context, connectorID string) (gitprovider.Client, func(), error) {
	if a.Clients == nil {
		return nil, nil, errors.New("connectors are not enabled")
	}
	return a.Clients.ClientFor(ctx, connectorID)
}

// PushBranch pushes m's branch to its worktree's origin remote with
// token, recording mission.pushed/mission.push_failed either way: the
// shared event-recording shape both the manual push endpoint and the
// driver's auto-fire hook use, so the Timeline reads identically
// regardless of which one fired. The credential username follows the
// remote Push validates, so a cross-kind destination authenticates as
// that host expects.
func (a *RepoAdapter) PushBranch(ctx context.Context, m missions.Mission, token string) (host string, err error) {
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
// with pass state, and, when attribution is on, a closing line
// crediting Timothy Agent with a link to its repository.
func PRBody(m missions.Mission, attribution bool) string {
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
	if attribution {
		body += "_PR was created by [Timothy Agent](https://github.com/timothy-agent/timothy)._\n"
	}
	return body
}

// OpenPR pushes the branch (idempotent re-push, same code path
// PushBranch above uses) then opens (or fetches the existing) pull
// request on the mission's OWN repo, recording mission.pr_opened.
// Repo-connection missions only -- callers check
// m.ConnectorID()/m.RepoURL() first.
func (a *RepoAdapter) OpenPR(ctx context.Context, m missions.Mission, token string) (url string, number int, err error) {
	connectorID := m.ConnectorID()
	c, release, err := a.client(ctx, connectorID)
	if err != nil {
		return "", 0, fmt.Errorf("pr: %w", err)
	}
	defer release()
	ref, ok := c.ParseRepoURL(m.RepoURL())
	if !ok {
		return "", 0, fmt.Errorf("pr: mission repo_url is not a recognizable %s https clone URL", c.Kind())
	}
	return a.openPRFor(ctx, c, m, token, ref)
}

// openPRFor is OpenPR's actual implementation, parameterized by the
// push target instead of always deriving it from m.RepoURL() (the
// mission's clone SOURCE): the create-if-missing path (issue #483)
// calls this with the resolved (possibly just-created) target repo,
// which can legitimately differ from where the mission was cloned from
// (or name a repo when the mission was never cloned from one at all, a
// scratch mission).
func (a *RepoAdapter) openPRFor(ctx context.Context, c gitprovider.Client, m missions.Mission, token string, ref gitprovider.RepoRef) (url string, number int, err error) {
	if _, err := a.PushBranch(ctx, m, token); err != nil {
		return "", 0, err
	}
	repo, err := c.GetRepo(ctx, ref)
	if err != nil {
		return "", 0, fmt.Errorf("pr: look up repo default branch: %w", err)
	}
	if repo.DefaultBranch == "" {
		return "", 0, fmt.Errorf("pr: repo has no default branch")
	}
	pr, err := c.CreatePR(ctx, gitprovider.PRSpec{
		Repo:  ref,
		Title: missions.ConventionalPRTitle(m),
		Head:  m.Branch,
		Base:  repo.DefaultBranch,
		Body:  PRBody(m, a.attribution(ctx)),
	})
	if err != nil {
		return "", 0, fmt.Errorf("pr: %w", err)
	}
	if err := a.Events.AppendEvent(ctx, m.ID, "mission.pr_opened", map[string]any{"url": pr.HTMLURL, "number": pr.Number}); err != nil {
		return pr.HTMLURL, pr.Number, fmt.Errorf("pr: record pr_opened: %w", err)
	}
	return pr.HTMLURL, pr.Number, nil
}

// ensureRepo resolves a delivery's push target before PushBranch/OpenPR
// run, creating the repo when it doesn't exist and createIfMissing is
// set (issue #483). Cases:
//
//  1. repoURL is set and the repo exists: the worktree's origin is
//     pointed at that repo's canonical clone URL. Issue #795 made this
//     unconditional for every kind. The github adapter used to no-op
//     here on the assumption that origin already pointed at the target,
//     which is false for a scratch mission (no origin at all) or any
//     delivery whose target differs from the clone source: the push
//     then went to the wrong remote or failed outright. Re-pointing
//     origin at the repo we just confirmed exists is idempotent, so the
//     previously-correct cases are unaffected.
//  2. repoURL is set (or, if empty, derived from the mission's goal/id
//     via Slug) and the repo does NOT exist: createIfMissing false
//     fails honestly ("never worker-invented," per issue #483's AC)
//     rather than guessing; true creates it, points the worktree's
//     origin at the new clone URL, and returns the final RepoURL.
//  3. A prior attempt already created the repo (retry via the
//     autoResumeInfra sweep): the repo now exists, so this takes case 1
//     instead of erroring or re-creating.
//
// Returns the resolved repoURL and updated=true only when it actually
// changed (the caller's signal to persist it). No-ops entirely
// (updated=false, nil error) when called with neither repoURL nor
// createIfMissing set: delivery proceeds against whatever origin the
// worktree already has.
func (a *RepoAdapter) ensureRepo(ctx context.Context, c gitprovider.Client, m missions.Mission, repoURL string, createIfMissing bool) (finalRepoURL string, updated bool, err error) {
	if repoURL == "" && !createIfMissing {
		return repoURL, false, nil
	}

	if repoURL != "" {
		ref, ok := c.ParseRepoURL(repoURL)
		if !ok {
			return repoURL, false, fmt.Errorf("ensure repo: repo_url is not a recognizable %s https clone URL", c.Kind())
		}
		exists, err := repoExists(ctx, c, ref)
		if err != nil {
			return repoURL, false, fmt.Errorf("ensure repo: check existence: %w", err)
		}
		if exists {
			// Case 1: point origin at the confirmed target, canonicalising
			// a browser or user@ URL into the plain https clone form
			// validateRemote accepts.
			cloneURL := c.HTTPSCloneURL(ref)
			if err := a.Pusher.SetOrigin(ctx, m.WorktreePath(), cloneURL); err != nil {
				return repoURL, false, fmt.Errorf("ensure repo: point worktree at repo: %w", err)
			}
			return cloneURL, cloneURL != repoURL, nil
		}
		if !createIfMissing {
			return repoURL, false, fmt.Errorf("ensure repo: repo %s does not exist and create_if_missing is not set", ref.FullName())
		}
		cloneURL, err := a.createRepo(ctx, c, m, ref.FullName())
		if err != nil {
			return repoURL, false, fmt.Errorf("ensure repo: create %s: %w", ref.FullName(), err)
		}
		return cloneURL, true, nil
	}

	// No repoURL at all: createIfMissing (checked above) with no name to
	// resolve against the provider -- derive one from the mission itself,
	// the same slug ExpandBranchPattern already uses for the branch name.
	name := missions.Slug(m.Goal, m.ID)
	cloneURL, err := a.createRepo(ctx, c, m, name)
	if err != nil {
		return repoURL, false, fmt.Errorf("ensure repo: create %s: %w", name, err)
	}
	return cloneURL, true, nil
}

// createRepo creates name through c and points the worktree's origin at
// the new repo, canonicalising the clone URL the provider reports: a
// Bitbucket user principal hands back the user@ form, which
// validateRemote rejects (issue #787).
func (a *RepoAdapter) createRepo(ctx context.Context, c gitprovider.Client, m missions.Mission, name string) (string, error) {
	created, err := c.CreateRepo(ctx, name, true)
	if err != nil {
		return "", err
	}
	cloneURL := created.CloneURL
	if ref, ok := c.ParseRepoURL(cloneURL); ok {
		cloneURL = c.HTTPSCloneURL(ref)
	} else if ref, ok := c.ParseRepoURL(created.FullName); ok {
		// Some providers report no clone URL on create; the full name is
		// enough to synthesize one.
		cloneURL = c.HTTPSCloneURL(ref)
	}
	if cloneURL == "" {
		return "", errors.New("created repo has an unusable clone URL")
	}
	if err := a.Pusher.SetOrigin(ctx, m.WorktreePath(), cloneURL); err != nil {
		return "", fmt.Errorf("point worktree at new repo: %w", err)
	}
	return cloneURL, nil
}

// repoExists reports whether ref exists and is visible. A confirmed
// ErrRepoNotFound is the only "safe to create" signal; every other
// lookup failure (network, auth) propagates as a hard error rather
// than being read as "absent" (issue #483).
func repoExists(ctx context.Context, c gitprovider.Client, ref gitprovider.RepoRef) (bool, error) {
	if _, err := c.GetRepo(ctx, ref); err != nil {
		if errors.Is(err, gitprovider.ErrRepoNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// DeliverMission runs a saved repo destination's delivery for m (issue
// #560): the Deliverer's own entry point, for every git provider kind.
// Repo resolution order: (a) e.RepoURL if already set, (b) else
// m.RepoURL() (the mission's own clone source), (c) else, if
// cfg.CreateIfMissing, create a repo named after the mission's goal/id,
// (d) else fail. On success e is updated in place with the final
// RepoURL/Branch/RemoteHost, and PRURL/PRNumber when a PR was opened.
func (a *RepoAdapter) DeliverMission(ctx context.Context, cfg RepoDestinationConfig, m missions.Mission, e *missions.DestinationEntry) error {
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
	if cfg.ConnectorID == "" {
		return fmt.Errorf("deliver: ensure repo: no connector configured for the destination")
	}
	c, release, err := a.client(ctx, cfg.ConnectorID)
	if err != nil {
		return fmt.Errorf("deliver: ensure repo: %w", err)
	}
	defer release()

	finalRepoURL, _, err := a.ensureRepo(ctx, c, m, repoURL, cfg.CreateIfMissing)
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
		ref, ok := c.ParseRepoURL(e.RepoURL)
		if !ok {
			return fmt.Errorf("deliver: repo_url is not a recognizable %s https clone URL", c.Kind())
		}
		url, number, err := a.openPRFor(ctx, c, m, token, ref)
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
