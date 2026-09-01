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
	// cloneTimeout is longer than gitOpTimeout: a real clone crosses the
	// network and can be a lot bigger than one local git op.
	cloneTimeout = 120 * time.Second

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

// Provision creates the mission's directory. Coding missions get their
// own fresh git repo self-initialized inside the workspace (see
// initSelfRepo) — the brain container has no pre-existing repo to work
// against on a real deployment, so every coding mission's repo lives
// wholly inside its own workspace dir. The base commit is captured
// with a tight timeout (degrades to the sentinel "(unavailable)"
// rather than blocking). Non-coding missions get a plain directory, no
// git. repoURL non-empty clones that repo instead of self-initializing
// an empty one (see cloneRepo); token authenticates the clone and is
// required whenever repoURL is set. connIdentity, when non-nil, is the
// connection's resolved (name, email) — set as this clone's LOCAL git
// config so its commits are authored as the connection, never the
// operator's fixed identity; nil (no connector, or identity resolve
// failed) leaves the clone with no local identity override, falling
// back to the fixed commitName/commitEmail same as before this existed.
// branchPattern is the already-resolved effective template (mission
// override > settings > DefaultBranchPattern, resolved by the caller);
// empty falls back to DefaultBranchPattern here so every existing call
// site (tests, any caller not yet passing one) keeps the original
// "<type>/<slug>" shape. baseRef, when non-empty, is a follow-up
// mission's requested worktree base (its parent's branch): a
// self-init'd mission ignores it (there is no prior history to base
// on), a cloned mission tries it first and falls back to the repo's
// default branch on any error (see cloneRepo) — baseUsed reports which
// ref the new branch actually landed on ("" for a self-init'd mission,
// or the clone's default branch on fallback), so the caller can record
// an accurate note. name is the mission's display name when already
// generated: the branch {slug} comes from it (issue #494), falling back
// to the goal when empty; {type} always derives from the goal, whose
// wording ("fix", "docs") carries the intent a six-word title drops.
func (w *Workspace) Provision(ctx context.Context, missionID, goal, name, kind, repoURL, token string, connIdentity *GitIdentity, branchPattern, baseRef string) (workspace, worktree, branch, baseCommit, baseUsed string, err error) {
	workspace = filepath.Join(w.root, kind, missionID)
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		return "", "", "", "", "", fmt.Errorf("worktree: provision: mkdir %s: %w", workspace, err)
	}

	if !policyFor(kind, FlowFull).needsWorktree {
		return workspace, "", "", "", "", nil
	}

	if branchPattern == "" {
		branchPattern = DefaultBranchPattern
	}
	login := ""
	if connIdentity != nil {
		login = connIdentity.Login
	}
	slugSource := name
	if slugSource == "" {
		slugSource = goal
	}
	branch = ExpandBranchPattern(branchPattern, CommitType(goal), Slug(slugSource, missionID), login, branchDate())
	worktree = filepath.Join(workspace, "wt")

	if repoURL != "" {
		used, err := w.cloneRepo(ctx, workspace, worktree, branch, repoURL, token, connIdentity, baseRef)
		if err != nil {
			return "", "", "", "", "", err
		}
		baseUsed = used
	} else if err := w.initSelfRepo(ctx, worktree, branch, missionID); err != nil {
		return "", "", "", "", "", err
	}

	baseCommit = w.captureBaseCommit(ctx, worktree)
	return workspace, worktree, branch, baseCommit, baseUsed, nil
}

// GitIdentity is a resolved commit author (name, email), plus the
// connection's GitHub login — threaded from CloneIdentityResolver
// through Provision to cloneRepo's local git config (Name/Email) and to
// the {login} branch-pattern placeholder (Login). SigningKey, when
// non-empty, is the connector's SSH signing PRIVATE key (OpenSSH PEM),
// resolved fresh at provisioning time same as Name/Email — never
// persisted on the mission row.
type GitIdentity struct {
	Name       string
	Email      string
	Login      string
	SigningKey string
}

// branchDate is the mission-creation date substituted for the {date}
// branch-pattern placeholder, formatted YYYYMMDD.
func branchDate() string {
	return time.Now().UTC().Format("20060102")
}

// signingKeyFileName is the SSH signing private key's filename inside
// the mission's workspace dir, a sibling of wt/ (never inside the
// worktree itself, so it's never accidentally committed/pushed).
const signingKeyFileName = "signing_key"

// cloneRepo clones repoURL's default branch into dir, authenticated
// via the same ephemeral credential-helper pattern push.go's rawPush
// uses (username x-access-token, password from an env var, never
// argv/disk), then creates and checks out the mission's own branch on
// top of the cloned default branch — mission commits land on
// "<type>/<slug>" (see CommitType), the same branch shape a self-init'd mission uses,
// never directly on the repo's default branch. baseRef, when
// non-empty, is tried first as the new branch's base (a follow-up
// mission's parent branch); any failure to check it out (unknown ref,
// since --single-branch only fetched the default branch) falls back to
// the already-cloned default branch — baseUsed reports "" for the
// default-branch fallback (matching the pre-follow-up return shape) or
// the ref actually used. connIdentity, when non-nil, is written as the
// clone's LOCAL (not global) git config right after — CommitUnit/
// initSelfRepo read this back so every commit in this worktree is
// authored as the connection, no token involved. connIdentity.SigningKey
// non-empty additionally writes the private key to
// workspaceDir/signing_key (0600) and points the clone's LOCAL git
// config at it (gpg.format=ssh, user.signingkey, commit.gpgsign=true),
// so CommitUnit's ordinary `git commit` signs every commit — no
// separate signing code path.
//
// D-058: the signing key file lives inside the mission's workspace dir,
// which sandboxd mounts WHOLE (not just wt/) into every mission's
// sandbox container (see internal/sandboxd/api.go's validWorkdir
// comment) — so this key is readable by model-authored shell commands
// in this mission's sandbox, and by any other mission's sandbox
// sharing the same workspace volume. Accepted for the single-operator
// posture, same class as D-054's executor auth-state volume; revisited
// together with agentguard provisioning (U5b).
func (w *Workspace) cloneRepo(ctx context.Context, workspaceDir, dir, branch, repoURL, token string, connIdentity *GitIdentity, baseRef string) (baseUsed string, err error) {
	cctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	helper := `!f() { echo "username=x-access-token"; echo "password=$GIT_CLONE_TOKEN"; }; f`
	cmd := exec.CommandContext(cctx, "git", //nolint:gosec // repoURL/dir are validated https origins/harness-controlled paths; token travels via env, never argv
		"-c", "credential.helper=",
		"-c", "credential.helper="+helper,
		"clone", "-q", "--single-branch", repoURL, dir)
	cmd.Env = append(os.Environ(), "GIT_CLONE_TOKEN="+token, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	scrubbed := strings.ReplaceAll(string(out), token, "***")
	if err != nil {
		return "", fmt.Errorf("worktree: clone: %w: %s", err, scrubbed)
	}
	if baseRef != "" {
		fetchCmd := exec.CommandContext(cctx, "git", //nolint:gosec // repoURL/dir/token same as the clone above; baseRef is a mission's own recorded branch name, not free user input
			"-c", "credential.helper=",
			"-c", "credential.helper="+helper,
			"fetch", "-q", "origin", baseRef)
		fetchCmd.Dir = dir
		fetchCmd.Env = append(os.Environ(), "GIT_CLONE_TOKEN="+token, "GIT_TERMINAL_PROMPT=0")
		if fout, err := fetchCmd.CombinedOutput(); err != nil {
			w.log.Warn("worktree: clone: base ref fetch failed; falling back to default branch", "dir", dir, "base_ref", baseRef, "error", err, "output", strings.ReplaceAll(string(fout), token, "***"))
		} else if out, err := runGit(cctx, dir, "checkout", "-q", "-b", branch, "FETCH_HEAD"); err != nil {
			w.log.Warn("worktree: clone: base ref checkout failed; falling back to default branch", "dir", dir, "base_ref", baseRef, "error", err, "output", out)
		} else {
			baseUsed = baseRef
		}
	}
	if baseUsed == "" {
		if out, err := runGit(cctx, dir, "checkout", "-q", "-b", branch); err != nil {
			return "", fmt.Errorf("worktree: clone: create mission branch: %w: %s", err, out)
		}
	}
	if connIdentity != nil {
		if err := setLocalIdentity(cctx, dir, connIdentity.Name, connIdentity.Email); err != nil {
			// Never fails provisioning: a clone with no local identity just
			// falls back to the fixed commitName/commitEmail, same as any
			// other mission.
			w.log.Warn("worktree: clone identity config failed; commits fall back to fixed identity", "dir", dir, "error", err)
		}
		if connIdentity.SigningKey != "" {
			if err := setLocalSigning(cctx, workspaceDir, dir, connIdentity.SigningKey); err != nil {
				// Never fails provisioning: a clone with no signing config
				// just makes unsigned commits, same as before this feature
				// existed.
				w.log.Warn("worktree: clone signing config failed; commits go unsigned", "dir", dir, "error", err)
			}
		}
	}
	return baseUsed, nil
}

// setLocalSigning writes connIdentity's SSH signing private key to
// workspaceDir/signing_key (0600, outside the worktree) and points
// dir's LOCAL git config at it so every subsequent commit is SSH-signed.
func setLocalSigning(ctx context.Context, workspaceDir, dir, privateKeyPEM string) error {
	keyPath := filepath.Join(workspaceDir, signingKeyFileName)
	if err := os.WriteFile(keyPath, []byte(privateKeyPEM), 0o600); err != nil {
		return fmt.Errorf("write signing key: %w", err)
	}
	if out, err := runGit(ctx, dir, "config", "--local", "gpg.format", "ssh"); err != nil {
		return fmt.Errorf("set gpg.format: %w: %s", err, out)
	}
	if out, err := runGit(ctx, dir, "config", "--local", "user.signingkey", keyPath); err != nil {
		return fmt.Errorf("set user.signingkey: %w: %s", err, out)
	}
	if out, err := runGit(ctx, dir, "config", "--local", "commit.gpgsign", "true"); err != nil {
		return fmt.Errorf("set commit.gpgsign: %w: %s", err, out)
	}
	return nil
}

// setLocalIdentity writes user.name/user.email into dir's LOCAL git
// config (never --global) — scoped to this one clone, never touching
// the container's shared git config.
func setLocalIdentity(ctx context.Context, dir, name, email string) error {
	if name != "" {
		if out, err := runGit(ctx, dir, "config", "--local", "user.name", name); err != nil {
			return fmt.Errorf("set user.name: %w: %s", err, out)
		}
	}
	if email != "" {
		if out, err := runGit(ctx, dir, "config", "--local", "user.email", email); err != nil {
			return fmt.Errorf("set user.email: %w: %s", err, out)
		}
	}
	return nil
}

// initSelfRepo creates a brand-new git repo at dir on branch, with one
// initial commit as the base for BaselineDiff/git log. The initial
// commit tracks a placeholder file rather than being truly empty:
// `git checkout -- .` (Rollback's discard step, run after every
// worker/reviewer turn) fails with "pathspec '.' did not match any
// file(s)" against a commit with zero tracked content, so the repo
// needs at least one tracked file from the start. `git init -b`
// requires git >= 2.28, which the brain image's alpine base satisfies.
func (w *Workspace) initSelfRepo(ctx context.Context, dir, branch, missionID string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("worktree: init self repo: mkdir %s: %w", dir, err)
	}
	gctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	if out, err := runGit(gctx, dir, "init", "-q", "-b", branch); err != nil {
		return fmt.Errorf("worktree: git init: %w: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte(""), 0o600); err != nil {
		return fmt.Errorf("worktree: init self repo: write .gitkeep: %w", err)
	}
	if out, err := runGit(gctx, dir, "add", ".gitkeep"); err != nil {
		return fmt.Errorf("worktree: git init: add .gitkeep: %w: %s", err, out)
	}
	name, email := commitName, commitEmail
	if w.identity != nil {
		if n, e := w.identity(ctx); n != "" || e != "" {
			if n != "" {
				name = n
			}
			if e != "" {
				email = e
			}
		}
	}
	cmd := exec.CommandContext(gctx, "git", //nolint:gosec // name/email travel as -c key=value args, never shell-interpolated; message is driver-built from the mission id
		"-c", "user.name="+name, "-c", "user.email="+email,
		"commit", "-m", "mission "+missionID+" initial")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: git init: initial commit: %w: %s", err, string(out))
	}
	return nil
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

// commitTypeKeywords maps a Conventional Commits type to the keywords
// that select it — checked in order, first match wins, so more
// specific types (fix, docs, test, refactor, chore) are tried before
// the "feat" default.
var commitTypeKeywords = []struct {
	typ      string
	keywords []string
}{
	{"fix", []string{"fix", "bug", "broken"}},
	{"docs", []string{"doc", "readme", "comment"}},
	{"test", []string{"test"}},
	{"refactor", []string{"refactor", "rename", "cleanup"}},
	{"chore", []string{"chore", "bump", "upgrade", "dependency"}},
}

// CommitType derives a Conventional Commits type from text (a mission
// goal or unit title) via a small deterministic keyword heuristic — no
// LLM call. Falls back to "feat" when nothing matches.
func CommitType(text string) string {
	lower := strings.ToLower(text)
	for _, ct := range commitTypeKeywords {
		for _, kw := range ct.keywords {
			if strings.Contains(lower, kw) {
				return ct.typ
			}
		}
	}
	return "feat"
}

// maxCommitSubjectLen matches the repo convention (root CLAUDE.md):
// commit subjects stay at or under 72 chars.
const maxCommitSubjectLen = 72

// CommitMessage builds a unit commit message in the given style
// (empty defaults to CommitStyleConventional, same as every call site
// before commit styles existed): "conventional" produces "<type>:
// <title>" as the subject (type from unitTitle via CommitType, falling
// back to goal when unitTitle is empty); "plain" uses the title as-is,
// no type prefix. Both styles lowercase-and-trim the same way,
// trimming to maxCommitSubjectLen with trailing punctuation removed;
// body is unchanged (whatever the caller already put there, e.g.
// mission id/iteration).
func CommitMessage(unitTitle, goal, body, style string) string {
	title := unitTitle
	if title == "" {
		title = goal
	}
	subject := strings.ToLower(strings.TrimSpace(title))
	subject = strings.TrimRight(subject, ".")
	prefix := ""
	if style != CommitStylePlain {
		prefix = CommitType(title) + ": "
	}
	if max := maxCommitSubjectLen - len(prefix); len(subject) > max {
		subject = strings.TrimRight(subject[:max], " .")
	}
	msg := prefix + subject
	if body != "" {
		msg += "\n\n" + body
	}
	return msg
}

// Rollback discards uncommitted work: `git checkout -- .` + `git clean
// -fd` for coding missions, no-op otherwise.
func (w *Workspace) Rollback(ctx context.Context, worktree, kind string) error {
	if !policyFor(kind, FlowFull).needsWorktree || worktree == "" {
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

// CommitUnit commits the worktree's current changes. A github-
// connection mission's clone carries a LOCAL user.name/user.email (set
// once at Provision time, see cloneRepo/setLocalIdentity) — that takes
// priority so commits are authored as the connection; otherwise falls
// back to the operator's configured git identity when set, then
// commitName/commitEmail, independent of host git config.
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
	if ln, le, ok := localIdentity(cctx, worktree); ok {
		if ln != "" {
			name = ln
		}
		if le != "" {
			email = le
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

// localIdentity reads worktree's LOCAL (not global) user.name/
// user.email, as set by setLocalIdentity at clone time for a
// github-connection mission. ok is false when neither key is set
// locally (a self-init'd or non-connector mission) — never an error,
// since "no local identity" is the normal case.
func localIdentity(ctx context.Context, worktree string) (name, email string, ok bool) {
	name, nameOK := gitConfigLocal(ctx, worktree, "user.name")
	email, emailOK := gitConfigLocal(ctx, worktree, "user.email")
	return name, email, nameOK || emailOK
}

func gitConfigLocal(ctx context.Context, worktree, key string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", "config", "--local", "--get", key) //nolint:gosec // key is always a fixed literal ("user.name"/"user.email"), never user input
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// Teardown removes the workspace. Every coding mission's repo lives
// wholly inside its own workspace dir (see Provision) — there is no
// separate source repo to leave a branch behind in, so this is a plain
// os.RemoveAll for every kind.
func (w *Workspace) Teardown(ctx context.Context, workspace, worktree, kind string) error {
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
