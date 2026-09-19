package missions

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Transport names the git wire protocol a clone or push runs over.
// D-102 (issue #796): the missions package never DECIDES which one to
// use — RepoAdapter/the provisioner hands it a fully resolved
// RemoteAuth and this package mechanically executes it, so the probe
// and fallback policy lives in one place instead of being re-derived
// per git call.
type Transport string

const (
	TransportHTTPS Transport = "https"
	TransportSSH   Transport = "ssh"
)

// RemoteAuth is everything one git network operation needs to
// authenticate, for whichever transport it runs over. Its secret fields
// (Token, SSHPrivateKey) never reach argv, the DB, logs or events: the
// token travels through an env var read by an ephemeral credential
// helper, the private key through a 0600 file inside the mission's own
// workspace.
//
// Zero value is a plain HTTPS auth with no token, which is what every
// call site that has no credential to offer passes.
type RemoteAuth struct {
	Transport Transport
	// HTTPUsername is the username git's credential helper sends
	// alongside Token (gitprovider.Descriptor.HTTPUsername); empty
	// falls back to the github default, matching the pre-#796 behavior
	// of an unknown host.
	HTTPUsername string
	Token        string
	// SSHPrivateKey is the OpenSSH PEM private half of the connector's
	// transport key.
	SSHPrivateKey string
	// KnownHosts are the pinned known_hosts lines for the remote's host
	// (gitprovider.Descriptor.SSHKnownHosts). Required for an ssh
	// RemoteAuth: StrictHostKeyChecking is always yes, so an empty list
	// can only fail the connection, and failing early says why.
	KnownHosts []string
}

// HTTPSAuth builds the token auth every pre-#796 call site used, with
// the credential username the host expects.
func HTTPSAuth(token, httpUsername string) RemoteAuth {
	return RemoteAuth{Transport: TransportHTTPS, HTTPUsername: httpUsername, Token: token}
}

// IsSSH reports whether this auth runs over the ssh wire protocol.
func (a RemoteAuth) IsSSH() bool { return a.Transport == TransportSSH }

// Validate reports why an auth cannot be used, before any git command
// is built from it: an ssh auth with no key or no pinned host keys
// would otherwise fail deep inside git with a confusing message.
func (a RemoteAuth) Validate() error {
	switch a.Transport {
	case "", TransportHTTPS:
		return nil
	case TransportSSH:
		if strings.TrimSpace(a.SSHPrivateKey) == "" {
			return fmt.Errorf("%w: ssh transport selected with no private key", ErrRemoteUnsupported)
		}
		if len(a.KnownHosts) == 0 {
			return fmt.Errorf("%w: ssh transport selected with no pinned known_hosts", ErrRemoteUnsupported)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown transport %q", ErrRemoteUnsupported, a.Transport)
	}
}

// credentialUsername resolves the credential-helper username, falling
// back to github's when the caller named none.
func (a RemoteAuth) credentialUsername() string {
	if a.HTTPUsername != "" {
		return a.HTTPUsername
	}
	return "x-access-token"
}

// transportForURL reports the transport a remote URL's scheme implies.
// scp-form is normalized to ssh:// before storage
// (gitprovider.NormalizeSCP), so anything still in that shape here is
// an unvalidated remote and reads as unknown.
func transportForURL(raw string) (Transport, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	switch u.Scheme {
	case "https":
		return TransportHTTPS, true
	case "ssh":
		return TransportSSH, true
	default:
		return "", false
	}
}

const (
	// sshKeyFileName / knownHostsFileName are the transport key and
	// pinned host keys inside the mission's WORKSPACE dir, siblings of
	// wt/ (never inside the worktree itself, so neither is ever
	// accidentally committed or pushed) and of signing_key, which they
	// deliberately never share a file with.
	//
	// D-058 applies here too: the workspace dir is mounted whole into
	// this mission's sandbox, so the transport key is readable by
	// model-authored shell commands in this mission's own sandbox.
	sshKeyFileName     = "ssh_key"
	knownHostsFileName = "known_hosts"
)

// sshFiles are the two paths writeSSHFiles materializes.
type sshFiles struct {
	keyPath        string
	knownHostsPath string
}

// writeSSHFiles writes the transport private key (0600) and the pinned
// known_hosts into workspaceDir. Both are rewritten on every call:
// the key is resolved fresh per provisioning and a stale file from an
// earlier run must never outlive it.
func writeSSHFiles(workspaceDir string, a RemoteAuth) (sshFiles, error) {
	f := sshFiles{
		keyPath:        filepath.Join(workspaceDir, sshKeyFileName),
		knownHostsPath: filepath.Join(workspaceDir, knownHostsFileName),
	}
	key := a.SSHPrivateKey
	if !strings.HasSuffix(key, "\n") {
		// ssh rejects a private key file with no trailing newline.
		key += "\n"
	}
	if err := os.WriteFile(f.keyPath, []byte(key), 0o600); err != nil {
		return sshFiles{}, fmt.Errorf("write ssh key: %w", err)
	}
	hosts := strings.Join(a.KnownHosts, "\n") + "\n"
	if err := os.WriteFile(f.knownHostsPath, []byte(hosts), 0o600); err != nil {
		return sshFiles{}, fmt.Errorf("write known_hosts: %w", err)
	}
	return f, nil
}

// SSHCommand renders the GIT_SSH_COMMAND value git runs for an ssh
// remote. Every option is deliberate:
//
//   - -F /dev/null ignores any ssh_config the container happens to have,
//     so no host-level Host block can redirect or weaken the connection.
//   - IdentitiesOnly=yes stops ssh offering an agent key instead of the
//     connector's own.
//   - StrictHostKeyChecking=yes with a pinned UserKnownHostsFile is the
//     whole point: never no, never accept-new, so a substituted host key
//     fails the push instead of trusting it (issue #796).
//   - BatchMode=yes and ConnectTimeout keep a prompt or a black-holed
//     port from hanging a mission turn.
//
// Paths are quoted because the workspace root is operator-configured;
// git runs this value through a shell.
func SSHCommand(f sshFiles) string {
	return "ssh -F /dev/null" +
		" -i " + shellQuote(f.keyPath) +
		" -o IdentitiesOnly=yes" +
		" -o UserKnownHostsFile=" + shellQuote(f.knownHostsPath) +
		" -o StrictHostKeyChecking=yes" +
		" -o BatchMode=yes" +
		" -o ConnectTimeout=15"
}

// shellQuote single-quotes s for the shell git runs GIT_SSH_COMMAND
// through, escaping any embedded single quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// gitEnv builds the environment one authenticated git command runs
// with: the token for the https credential helper, or GIT_SSH_COMMAND
// for ssh. GIT_TERMINAL_PROMPT=0 on both, so a missing or rejected
// credential fails instead of blocking on a prompt no one can answer.
// tokenVar names the env var the credential helper reads.
func gitEnv(workspaceDir, tokenVar string, a RemoteAuth) ([]string, error) {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if !a.IsSSH() {
		return append(env, tokenVar+"="+a.Token), nil
	}
	f, err := writeSSHFiles(workspaceDir, a)
	if err != nil {
		return nil, err
	}
	return append(env, "GIT_SSH_COMMAND="+SSHCommand(f)), nil
}

// gitAuthArgs are the -c flags an authenticated git command carries
// before its subcommand: the ephemeral credential helper for https,
// nothing at all for ssh (which authenticates through GIT_SSH_COMMAND).
func gitAuthArgs(tokenVar string, a RemoteAuth) []string {
	if a.IsSSH() {
		return nil
	}
	return []string{
		"-c", "credential.helper=",
		"-c", "credential.helper=" + gitCredentialHelper(a.credentialUsername(), tokenVar),
	}
}

// ProbeSSHRemote reports whether sshURL is reachable and readable with
// auth: one `git ls-remote --exit-code … HEAD` under timeout, the
// cheapest round trip that proves the key is registered with the host
// and the host key matches the pin. The caller (the transport
// resolver, never this package) decides what a failure means.
//
// Writes the same key/known_hosts files into workspaceDir that the
// subsequent clone or push reuses, so a passing probe leaves the exact
// material the real operation runs with.
func ProbeSSHRemote(ctx context.Context, workspaceDir, sshURL string, auth RemoteAuth, timeout time.Duration) error {
	if err := auth.Validate(); err != nil {
		return err
	}
	if !auth.IsSSH() {
		return fmt.Errorf("%w: probe called with a %s auth", ErrRemoteUnsupported, originTransport(auth))
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	env, err := gitEnv(workspaceDir, "GIT_PROBE_TOKEN", auth)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(pctx, "git", "ls-remote", "--exit-code", sshURL, "HEAD") //nolint:gosec // sshURL is a Descriptor-synthesized clone URL; the key travels via a 0600 file, never argv
	cmd.Dir = workspaceDir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh probe: %w: %s", err, strings.TrimSpace(scrubAuth(string(out), auth)))
	}
	return nil
}

// scrubAuth removes the token from git's combined output. The ssh
// private key never reaches a command line or git's output at all, so
// there is nothing of it to scrub.
func scrubAuth(out string, a RemoteAuth) string {
	if a.Token == "" {
		return out
	}
	return strings.ReplaceAll(out, a.Token, "***")
}
