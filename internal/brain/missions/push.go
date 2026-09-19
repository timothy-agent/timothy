package missions

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// NotPushable reports the shared kind/branch/worktree guards push and
// pr both require, Go code, never a prompt: only a coding mission
// with a live worktree is ever pushable. Shared by the manual push/pr
// API handlers and the driver's auto-fire-on-done hook, and by
// destinations.RepoAdapter's delivery, so none of them can diverge on
// what counts as pushable.
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

var (
	ErrPushRejected      = errors.New("push rejected by remote")
	ErrRemoteUnsupported = errors.New("remote is not a supported https origin")
	ErrNotPushable       = errors.New("mission is not pushable")
)

// githubRepoPattern matches the owner/repo path segment of a GitHub
// https clone URL (with or without .git suffix), the shape
// ParseGitHubRepoURL extracts from mission.RepoURL.
var githubRepoPattern = regexp.MustCompile(`^https://[^/]+/([^/]+)/([^/]+?)(?:\.git)?/?$`)

// isGitHubHost reports whether repoURL points at github.com, the only
// host the github connector talks to (githubAPIBase is fixed, there is
// no Enterprise support). githubRepoPattern itself is host-agnostic
// because it also parses origins the mission was cloned from.
func isGitHubHost(repoURL string) bool {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "github.com")
}

// ParseGitHubRepoURL extracts owner/repo from repoURL (always an https
// clone URL per validateRemote's own gate at push time); ok is false
// for anything that doesn't match the expected shape.
func ParseGitHubRepoURL(repoURL string) (owner, repo string, ok bool) {
	m := githubRepoPattern.FindStringSubmatch(repoURL)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// bitbucketRepoPattern pins the host, unlike githubRepoPattern: a
// bitbucket connector paired with another host's URL is a misconfiguration.
// A browser URL (…/src/main/, …/pull-requests/) and the user@ form Bitbucket's
// Clone button shows are accepted too; ssh is not, missions are https-only.
var bitbucketRepoPattern = regexp.MustCompile(`^https://(?:[^@/]+@)?bitbucket\.org/([^/]+)/([^/]+?)(?:\.git)?(?:/(?:src|branch|pull-requests|commits)(?:/.*)?)?/?$`)

// ParseBitbucketRepoURL extracts workspace/slug from a bitbucket.org https URL.
func ParseBitbucketRepoURL(repoURL string) (workspace, slug string, ok bool) {
	m := bitbucketRepoPattern.FindStringSubmatch(repoURL)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// BitbucketCloneURL turns any accepted bitbucket URL into the plain https
// clone URL: the browser and user@ forms would otherwise be stored as-is
// and fail validateRemote at push time.
func BitbucketCloneURL(repoURL string) (string, bool) {
	workspace, slug, ok := ParseBitbucketRepoURL(repoURL)
	if !ok {
		return "", false
	}
	return "https://bitbucket.org/" + workspace + "/" + slug + ".git", true
}

// parseRepoURLForKind picks the parser for a source or destination kind.
func parseRepoURLForKind(kind, repoURL string) (owner, repo string, ok bool) {
	if kind == SourceKindBitbucket {
		return ParseBitbucketRepoURL(repoURL)
	}
	return ParseGitHubRepoURL(repoURL)
}

// PRTitleGoalCap bounds a fallback title built from the goal when the
// mission has no generated display name yet.
const PRTitleGoalCap = 72

// PRTitle prefers the mission's generated display name, falling back
// to a truncated goal: used for PR titles and operator notifications.
func PRTitle(m Mission) string {
	if m.Name != "" {
		return m.Name
	}
	if len(m.Goal) <= PRTitleGoalCap {
		return m.Goal
	}
	return m.Goal[:PRTitleGoalCap] + "…"
}

// prTitleTypes maps the first word of a mission's name or goal to a
// Conventional Commits type; anything else is a feat (issue #709).
var prTitleTypes = map[string]string{
	"fix": "fix", "fixes": "fix", "bug": "fix", "bugfix": "fix", "hotfix": "fix", "repair": "fix",
	"docs": "docs", "doc": "docs", "document": "docs", "documentation": "docs", "readme": "docs",
	"refactor": "refactor", "refactoring": "refactor", "rename": "refactor", "restructure": "refactor",
	"test": "test", "tests": "test",
	"chore": "chore", "ci": "chore", "bump": "chore", "upgrade": "chore",
}

// ConventionalPRTitle renders PRTitle as a Conventional Commits subject
// line (issue #709): "<type>: <subject>", lowercase, no trailing
// period, at most PRTitleGoalCap bytes. The type comes from the first
// word of the name (or goal) via prTitleTypes, feat otherwise; an
// explicit "type:" prefix already in the name is kept, not doubled.
func ConventionalPRTitle(m Mission) string {
	subject := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(PRTitle(m), "…")))
	subject = strings.TrimRight(subject, ". ")
	kind := "feat"
	if fields := strings.Fields(subject); len(fields) > 0 {
		if t, ok := prTitleTypes[strings.Trim(fields[0], ":,")]; ok {
			kind = t
			if len(fields) > 1 && strings.HasSuffix(fields[0], ":") {
				subject = strings.TrimSpace(strings.TrimPrefix(subject, fields[0]))
			}
		}
	}
	title := kind + ": " + subject
	if len(title) > PRTitleGoalCap {
		title = strings.TrimRight(title[:PRTitleGoalCap], " ")
	}
	return title
}

// pushTimeout bounds one push attempt — long enough for a real repo
// over the network, short enough that a hung remote doesn't pin the
// request indefinitely.
const pushTimeout = 120 * time.Second

// scpLikePattern matches scp-form remotes (e.g. git@github.com:u/r.git)
// — no "://", so url.Parse would otherwise mis-parse or silently
// accept them; must be checked before handing raw to url.Parse.
var scpLikePattern = regexp.MustCompile(`^[\w.-]+@[\w.-]+:`)

// gitCredentialHelper is the ephemeral helper git runs for token auth:
// user is the credential username the host expects (GitHub
// x-access-token, Bitbucket Cloud x-token-auth, from
// gitprovider.Descriptor.HTTPUsername) and the token comes from envVar,
// never argv.
func gitCredentialHelper(user, envVar string) string {
	return `!f() { echo "username=` + user + `"; echo "password=$` + envVar + `"; }; f`
}

// validateRemote allows plain https:// origins with no embedded
// credentials, and, since issue #796, ssh:// origins. scp-form
// (git@host:owner/repo.git) is still rejected here: it is normalized
// to canonical ssh:// by gitprovider.NormalizeSCP before storage, so
// anything still in that shape reached here unvalidated. Returns the
// remote's host and the transport its scheme implies.
func validateRemote(raw string) (host string, transport Transport, err error) {
	raw = strings.TrimSpace(raw)
	if scpLikePattern.MatchString(raw) {
		return "", "", fmt.Errorf("%w: scp-style git remotes are not stored raw; use the canonical ssh:// form", ErrRemoteUnsupported)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrRemoteUnsupported, err)
	}
	transport, ok := transportForURL(raw)
	if !ok {
		return "", "", fmt.Errorf("%w: only https:// and ssh:// remotes are supported (got %q)", ErrRemoteUnsupported, u.Scheme)
	}
	// An ssh remote's user info is the protocol's own "git@", not a
	// credential; anything else embedded in an https URL is.
	if u.User != nil && transport != TransportSSH {
		return "", "", fmt.Errorf("%w: origin URL embeds credentials; remove them and use credential_ref", ErrRemoteUnsupported)
	}
	if transport == TransportSSH && u.User != nil && u.User.Username() != "git" {
		return "", "", fmt.Errorf("%w: ssh origin must connect as git@, got %q", ErrRemoteUnsupported, u.User.Username())
	}
	return u.Hostname(), transport, nil
}

// Push validates the worktree's origin remote, then pushes branch to
// it authenticating via auth — whose secrets are never written to
// argv, DB, logs, or events. Returns the remote's host for
// event/response use.
//
// auth.Transport must match the origin's own scheme: a mismatch means
// the adapter that resolved the auth and the worktree that holds the
// origin disagree about which remote is being pushed to, so it is a
// hard error rather than a silent fallback to whichever the URL says
// (issue #796). The mission's workspace dir (the worktree's parent)
// holds the ssh key and known_hosts files an ssh auth needs.
func (w *Workspace) Push(ctx context.Context, worktree, branch string, auth RemoteAuth) (string, error) {
	out, err := runGit(ctx, worktree, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("push: read origin: %w: %s", err, out)
	}
	origin := strings.TrimSpace(out)
	host, transport, err := validateRemote(origin)
	if err != nil {
		return "", err
	}
	if err := auth.Validate(); err != nil {
		return "", err
	}
	if originTransport(auth) != transport {
		return "", fmt.Errorf("%w: origin %s is a %s remote but %s credentials were resolved for it", ErrRemoteUnsupported, origin, transport, originTransport(auth))
	}
	return host, rawPush(ctx, workspaceDirOf(worktree), worktree, branch, auth)
}

// originTransport reads auth's transport with the zero value's https
// default applied, so a comparison never trips on "" vs "https".
func originTransport(auth RemoteAuth) Transport {
	if auth.Transport == "" {
		return TransportHTTPS
	}
	return auth.Transport
}

// workspaceDirOf is the mission workspace dir holding a worktree:
// Provision always creates the worktree as <workspace>/wt, and the ssh
// key/known_hosts are siblings of it (see sshKeyFileName).
func workspaceDirOf(worktree string) string { return filepath.Dir(worktree) }

// SetOrigin points worktree's origin remote at remoteURL, adding it if
// the worktree has none (a self-init'd scratch mission's clone never
// gets one, see initSelfRepo) or repointing it if one already exists:
// the create-if-missing delivery path's own step before pushing to a
// repo the mission was never cloned from (issue #483). remoteURL is
// validated the same way Push's own read-back is (validateRemote),
// so a bad origin can never slip through unnoticed here either.
func (w *Workspace) SetOrigin(ctx context.Context, worktree, remoteURL string) error {
	if _, _, err := validateRemote(remoteURL); err != nil {
		return err
	}
	gctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	if _, err := runGit(gctx, worktree, "remote", "get-url", "origin"); err != nil {
		if out, err := runGit(gctx, worktree, "remote", "add", "origin", remoteURL); err != nil {
			return fmt.Errorf("set origin: remote add: %w: %s", err, out)
		}
		return nil
	}
	if out, err := runGit(gctx, worktree, "remote", "set-url", "origin", remoteURL); err != nil {
		return fmt.Errorf("set origin: remote set-url: %w: %s", err, out)
	}
	return nil
}

// rawPush execs the authenticated git push, independent of remote
// validation — split out so tests can exercise the exec/env/dir
// plumbing against a local bare repo (which validateRemote's
// scheme gate would otherwise block) without touching a real
// https origin. workspaceDir holds the ssh key/known_hosts an ssh
// auth writes.
func rawPush(ctx context.Context, workspaceDir, worktree, branch string, auth RemoteAuth) error {
	cctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	env, err := gitEnv(workspaceDir, "GIT_PUSH_TOKEN", auth)
	if err != nil {
		return fmt.Errorf("push: %w", err)
	}
	args := append(gitAuthArgs("GIT_PUSH_TOKEN", auth), "push", "origin", branch)
	cmd := exec.CommandContext(cctx, "git", args...) //nolint:gosec // worktree/branch are harness-controlled; the token travels via env and the ssh key via a 0600 file, never argv
	cmd.Dir = worktree
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	scrubbed := scrubAuth(string(out), auth)
	if err != nil {
		if strings.Contains(scrubbed, "rejected") || strings.Contains(scrubbed, "non-fast-forward") {
			return fmt.Errorf("%w: %s", ErrPushRejected, scrubbed)
		}
		return fmt.Errorf("push: %w: %s", err, scrubbed)
	}
	return nil
}
