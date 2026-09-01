package missions

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var (
	ErrPushRejected      = errors.New("push rejected by remote")
	ErrRemoteUnsupported = errors.New("remote is not a supported https origin")
	ErrNotPushable       = errors.New("mission is not pushable")
)

// pushTimeout bounds one push attempt — long enough for a real repo
// over the network, short enough that a hung remote doesn't pin the
// request indefinitely.
const pushTimeout = 120 * time.Second

// scpLikePattern matches scp-form remotes (e.g. git@github.com:u/r.git)
// — no "://", so url.Parse would otherwise mis-parse or silently
// accept them; must be checked before handing raw to url.Parse.
var scpLikePattern = regexp.MustCompile(`^[\w.-]+@[\w.-]+:`)

// validateRemote allows only plain https:// origins with no embedded
// credentials — v1 deliberately has no ssh/scp support and no
// force-push option.
func validateRemote(raw string) (host string, err error) {
	raw = strings.TrimSpace(raw)
	if scpLikePattern.MatchString(raw) {
		return "", fmt.Errorf("%w: scp-style git remotes are not supported, use an https:// origin", ErrRemoteUnsupported)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRemoteUnsupported, err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("%w: only https:// remotes are supported (got %q)", ErrRemoteUnsupported, u.Scheme)
	}
	if u.User != nil {
		return "", fmt.Errorf("%w: origin URL embeds credentials; remove them and use credential_ref", ErrRemoteUnsupported)
	}
	return u.Hostname(), nil
}

// Push validates the worktree's origin remote, then pushes branch to
// it authenticating via token — never written to argv, DB, logs, or
// events. Returns the remote's host for event/response use.
func (w *Workspace) Push(ctx context.Context, worktree, branch, token string) (string, error) {
	out, err := runGit(ctx, worktree, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("push: read origin: %w: %s", err, out)
	}
	origin := strings.TrimSpace(out)
	host, err := validateRemote(origin)
	if err != nil {
		return "", err
	}
	return host, rawPush(ctx, worktree, branch, token)
}

// SetOrigin points worktree's origin remote at remoteURL, adding it if
// the worktree has none (a self-init'd scratch mission's clone never
// gets one, see initSelfRepo) or repointing it if one already exists:
// the create-if-missing delivery path's own step before pushing to a
// repo the mission was never cloned from (issue #483). remoteURL is
// validated the same way Push's own read-back is (validateRemote),
// so a bad origin can never slip through unnoticed here either.
func (w *Workspace) SetOrigin(ctx context.Context, worktree, remoteURL string) error {
	if _, err := validateRemote(remoteURL); err != nil {
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
// https-only gate would otherwise block) without touching a real
// https origin.
func rawPush(ctx context.Context, worktree, branch, token string) error {
	cctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	helper := `!f() { echo "username=x-access-token"; echo "password=$GIT_PUSH_TOKEN"; }; f`
	cmd := exec.CommandContext(cctx, "git", //nolint:gosec // worktree/branch are harness-controlled; token travels via env, never argv
		"-c", "credential.helper=",
		"-c", "credential.helper="+helper,
		"push", "origin", branch)
	cmd.Dir = worktree
	cmd.Env = append(os.Environ(), "GIT_PUSH_TOKEN="+token, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	scrubbed := strings.ReplaceAll(string(out), token, "***")
	if err != nil {
		if strings.Contains(scrubbed, "rejected") || strings.Contains(scrubbed, "non-fast-forward") {
			return fmt.Errorf("%w: %s", ErrPushRejected, scrubbed)
		}
		return fmt.Errorf("push: %w: %s", err, scrubbed)
	}
	return nil
}
