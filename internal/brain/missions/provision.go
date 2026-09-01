package missions

// D-074: provisioner holds the mission-provisioning slice split out of
// Driver (ensureProvisioned, grantSessionDefaults, followUpBaseRef and
// their resolver deps) — a pure extraction, no behavior change. Driver
// embeds one and its Set* setters delegate straight through.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// provisioner gives a mission its hidden session, standing grants, and
// workspace/worktree — see ensureProvisioned for the full contract.
type provisioner struct {
	store     driverStore
	workspace *Workspace
	sessions  sessionCreator
	perms     sessionGranter
	log       *slog.Logger

	// resolveAgent resolves a mission's agent_id to its
	// ApprovalAllowlist at provisioning time (see SetAgentResolver) —
	// nil-safe: unset means no allowlist grants, same as before this
	// existed.
	resolveAgent AgentResolver

	// resolveCloneToken resolves a github-kind connector_id to the PAT
	// that authenticates ensureProvisioned's clone (see SetCloneTokenResolver)
	// — nil-safe: unset means a mission with repo_url set fails
	// provisioning (surfaced as an infra pause, same as any other
	// provisioning error), since there would be no way to authenticate
	// the clone.
	resolveCloneToken CloneTokenResolver

	// resolveCloneIdentity resolves a github-kind connector_id to the
	// commit identity (name, email) ensureProvisioned sets as the
	// clone's local git config (see SetCloneIdentityResolver) — nil-safe:
	// unset, or a resolve error, just leaves the clone with no local
	// identity override (commits fall back to the operator's fixed
	// identity), never fails provisioning.
	resolveCloneIdentity CloneIdentityResolver

	// gitBranchPattern resolves the settings-configured default branch
	// pattern (see SetGitBranchPattern) — consulted by ensureProvisioned
	// only when the mission's own github destination entry has no
	// BranchPattern (mission override > settings > worktree.go's
	// DefaultBranchPattern). nil-safe: unset falls straight through to
	// Provision's own DefaultBranchPattern fallback, same as before this
	// setting existed.
	gitBranchPattern func(ctx context.Context) string

	// resolvePRState resolves whether a github PR has been merged (see
	// SetPRStateResolver) — consulted by followUpBaseRef when the parent
	// mission opened a PR, so a follow-up whose parent's branch was
	// already merged bases on the repo's default branch instead of a
	// branch GitHub may since have deleted. nil-safe: unset (or any
	// resolve error) falls straight through to the parent-branch base,
	// same as before this existed.
	resolvePRState PRStateResolver
}

// ensureProvisioned gives a mission everything Create used to set up
// inline — a hidden session, its standing grants, and a workspace —
// but callable a second time for a mission that reached the store some
// OTHER way (scheduler.go's createFromTemplate inserts a bare row
// directly, bypassing Create entirely: no session, no workspace, no
// grants). Advance calls this at the top of every turn so a
// scheduler-born mission gets provisioned the first time anything
// actually drives it, not at fire time. Idempotent: a mission that
// already has both a session and a workspace/worktree is a no-op,
// and SetSession's own WHERE session_id IS NULL guard makes a second
// concurrent attempt safe even without that check.
//
// Grants happen in BOTH the session-creation and the workspace-
// provisioning halves below — same shape as Create always had, plus
// the new ApprovalAllowlist grants (via resolveAgent) once a session
// exists, whichever half of ensureProvisioned actually created it.
func (p *provisioner) ensureProvisioned(ctx context.Context, m Mission) (Mission, error) {
	if m.SessionID == "" && p.sessions != nil {
		sessionID, err := p.sessions.Create(ctx, "")
		if err != nil {
			return m, fmt.Errorf("session: %w", err)
		}
		if err := p.store.SetSession(ctx, m.ID, sessionID); err != nil {
			return m, fmt.Errorf("set session: %w", err)
		}
		m.SessionID = sessionID
		p.grantSessionDefaults(ctx, m)
	}
	if p.workspace != nil && m.Workspace == "" {
		var token string
		var connIdentity *GitIdentity
		repoURL := m.RepoURL()
		if repoURL != "" {
			connectorID := m.ConnectorID()
			if p.resolveCloneToken == nil {
				return m, fmt.Errorf("provision: mission has repo_url but no clone token resolver is configured")
			}
			t, err := p.resolveCloneToken(ctx, connectorID)
			if err != nil {
				return m, fmt.Errorf("provision: resolve clone token: %w", err)
			}
			token = t
			// Identity resolve failure never fails provisioning: it just
			// leaves the clone with no local identity override, falling
			// back to the fixed commitName/commitEmail (worktree.go's
			// CommitUnit).
			if p.resolveCloneIdentity != nil {
				identity, err := p.resolveCloneIdentity(ctx, connectorID)
				if err != nil {
					p.log.Warn("driver: resolve clone identity failed; commits fall back to fixed identity", "mission_id", m.ID, "error", err)
				} else {
					connIdentity = &GitIdentity{Name: identity.Name, Email: identity.Email, Login: identity.Login, SigningKey: identity.SigningKey}
				}
			}
		}
		branchPattern := ""
		if e, ok := m.GitHubEntry(); ok {
			branchPattern = e.BranchPattern
		}
		if branchPattern == "" && p.gitBranchPattern != nil {
			branchPattern = p.gitBranchPattern(ctx)
		}
		baseRef := p.followUpBaseRef(ctx, m)
		workspace, worktree, branch, baseCommit, baseUsed, err := p.workspace.Provision(ctx, m.ID, m.Goal, m.Kind, repoURL, token, connIdentity, branchPattern, baseRef)
		if err != nil {
			return m, fmt.Errorf("provision: %w", err)
		}
		if err := p.store.SetProvisioned(ctx, m.ID, workspace, branch, baseCommit); err != nil {
			return m, err
		}
		m.Workspace, m.Branch, m.BaseCommit = workspace, branch, baseCommit
		if m.ParentMissionID != "" && missionPolicyFor(m).needsWorktree {
			ref := baseUsed
			if ref == "" {
				ref = "the repo's default branch (parent branch unreachable)"
			}
			if err := p.store.AppendProgress(ctx, m.ID, fmt.Sprintf("Follow-up of mission %s: worktree based on %s", m.ParentMissionID, ref)); err != nil {
				p.log.Warn("driver: record follow-up base note failed", "mission_id", m.ID, "error", err)
			}
		}
		if m.AutoApproveSafe && p.perms != nil && m.SessionID != "" {
			// Register the mission's own directory as the session's
			// sandbox: destructive-classified commands provably confined
			// to it (writing the mission's own artifacts, cleaning its
			// own files) stop parking on a human prompt. Best-effort, same
			// as the grants in grantSessionDefaults.
			root := worktree
			if root == "" {
				root = workspace
			}
			if err := p.perms.Grant(ctx, m.SessionID, tools.SandboxGrantTool, root, missionGrantTTL); err != nil {
				p.log.Warn("driver: sandbox grant failed", "mission_id", m.ID, "error", err)
			}
		}
	}
	return m, nil
}

// followUpBaseRef resolves a follow-up mission's worktree base: the
// parent's own branch, but only when the parent actually has one
// (kind=coding) and shares this mission's RepoURL — a follow-up to a
// general mission, or one cloning a different repo, has no
// meaningful base to hand Provision. Any failure (parent gone, no
// branch) degrades to "" (Provision's own default-branch behavior),
// never fails provisioning. When the parent opened a PR and the
// resolver reports it merged, the parent's branch may already be
// deleted on the remote — this bases on the repo's default branch
// instead, same degrade-to-"" path.
func (p *provisioner) followUpBaseRef(ctx context.Context, m Mission) string {
	if m.ParentMissionID == "" {
		return ""
	}
	parent, err := p.store.Get(ctx, m.ParentMissionID)
	if err != nil {
		p.log.Debug("driver: follow-up base ref: parent lookup failed", "mission_id", m.ID, "parent_id", m.ParentMissionID, "error", err)
		return ""
	}
	if parent.Branch == "" || parent.RepoURL() != m.RepoURL() {
		return ""
	}
	if p.resolvePRState != nil {
		if merged := p.parentPRMerged(ctx, m, parent); merged {
			return ""
		}
	}
	return parent.Branch
}

// followUpPROpenedPayload is the shape of a mission.pr_opened event's
// payload this package needs — a local struct (not an import of
// builtin.missionPROpenedPayload) since missions cannot import the
// builtin package (import cycle, see MissionRecord's doc comment in
// builtin/missions.go).
type followUpPROpenedPayload struct {
	Number int `json:"number"`
}

// parentPRMerged reports whether the parent mission's latest opened PR
// has been merged, via resolvePRState — false on any missing event,
// unparseable repo URL, or resolver error, degrading to the existing
// parent-branch behavior in every such case.
func (p *provisioner) parentPRMerged(ctx context.Context, m, parent Mission) bool {
	events, err := p.store.Events(ctx, parent.ID)
	if err != nil {
		p.log.Debug("driver: follow-up base ref: parent events lookup failed", "mission_id", m.ID, "parent_id", parent.ID, "error", err)
		return false
	}
	var number int
	found := false
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != "mission.pr_opened" {
			continue
		}
		var payload followUpPROpenedPayload
		if err := json.Unmarshal(events[i].Payload, &payload); err == nil {
			number = payload.Number
			found = true
		}
		break
	}
	if !found || number == 0 {
		return false
	}
	owner, repo, ok := ParseGitHubRepoURL(parent.RepoURL())
	if !ok {
		return false
	}
	merged, err := p.resolvePRState(ctx, parent.ConnectorID(), owner, repo, number)
	if err != nil {
		p.log.Debug("driver: follow-up base ref: pr state resolve failed", "mission_id", m.ID, "parent_id", parent.ID, "error", err)
		return false
	}
	return merged
}

// grantSessionDefaults pre-authorizes a freshly created hidden session:
// standing "safe shell" approval when the mission opted in, plus every
// tool in the mission's agent's ApprovalAllowlist (resolved at
// provisioning time via resolveAgent, same fire-time-not-create-time
// principle as scheduler.go's createFromTemplate — an agent's allowlist
// edited after the mission started still applies to a not-yet-
// provisioned mission). All best-effort: a failed grant just means the
// mission asks on its first call instead of running unattended —
// degraded autonomy, never a broken mission.
//
// AutoApproveSafe is deliberately shell-scoped only ("shell" + sandbox
// root) — it does not widen to connector tools, which default
// danger=safe unclassified; doing so would silently unlock every
// connector write (send an email, delete a calendar event) for an
// unattended mission with no per-tool review. Autonomy over connector
// tools comes only from the agent's ApprovalAllowlist grants below,
// matched against the connector-namespaced tool name via matchGrant's
// suffix rule (D-036).
func (p *provisioner) grantSessionDefaults(ctx context.Context, m Mission) {
	if p.perms == nil {
		return
	}
	if m.AutoApproveSafe {
		if err := p.perms.Grant(ctx, m.SessionID, "shell", "*", missionGrantTTL); err != nil {
			p.log.Warn("driver: auto-approve grant failed", "mission_id", m.ID, "error", err)
		}
	}
	if p.resolveAgent == nil {
		return
	}
	defaults, ok := p.resolveAgent(ctx, m.AgentID)
	if !ok {
		return
	}
	for _, tool := range defaults.ApprovalAllowlist {
		if err := p.perms.Grant(ctx, m.SessionID, tool, "*", missionGrantTTL); err != nil {
			p.log.Warn("driver: approval allowlist grant failed", "mission_id", m.ID, "tool", tool, "error", err)
		}
	}
}
