package missions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testCloneAuth is the CloneAuth every pre-#796 clone test wants: a
// plain https token, no transport decision, no URL rewrite.
func testCloneAuth(token string) CloneAuth {
	return func(context.Context, string) (RemoteAuth, string, error) {
		return HTTPSAuth(token, ""), "", nil
	}
}

// testKnownHosts is a syntactically valid pinned line; the tests below
// never open a connection, so the key itself only has to be well-formed.
const testKnownHosts = "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

// testPrivateKey stands in for an OpenSSH PEM; nothing parses it in
// these tests, only writes and permissions are asserted.
const testPrivateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nZmFrZQ==\n-----END OPENSSH PRIVATE KEY-----"

func sshAuth() RemoteAuth {
	return RemoteAuth{
		Transport:     TransportSSH,
		HTTPUsername:  "x-access-token",
		SSHPrivateKey: testPrivateKey,
		KnownHosts:    []string{testKnownHosts},
	}
}

func TestHTTPSAuth(t *testing.T) {
	t.Parallel()
	a := HTTPSAuth("tok", "x-token-auth")
	if a.Transport != TransportHTTPS || a.Token != "tok" || a.HTTPUsername != "x-token-auth" {
		t.Fatalf("HTTPSAuth = %+v", a)
	}
	if a.IsSSH() {
		t.Fatal("an https auth reports IsSSH")
	}
}

func TestRemoteAuthCredentialUsername(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"empty falls back to github's", "", "x-access-token"},
		{"github", "x-access-token", "x-access-token"},
		{"bitbucket", "x-token-auth", "x-token-auth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := (RemoteAuth{HTTPUsername: tc.in}).credentialUsername(); got != tc.want {
				t.Fatalf("credentialUsername() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRemoteAuthValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		auth    RemoteAuth
		wantErr bool
	}{
		{"zero value is a plain https auth", RemoteAuth{}, false},
		{"https with a token", HTTPSAuth("tok", "x-access-token"), false},
		{"https with no token is still valid here", HTTPSAuth("", ""), false},
		{"complete ssh auth", sshAuth(), false},
		{"ssh with no key", RemoteAuth{Transport: TransportSSH, KnownHosts: []string{testKnownHosts}}, true},
		{"ssh with a blank key", RemoteAuth{Transport: TransportSSH, SSHPrivateKey: "   ", KnownHosts: []string{testKnownHosts}}, true},
		{"ssh with no pinned hosts", RemoteAuth{Transport: TransportSSH, SSHPrivateKey: testPrivateKey}, true},
		{"unknown transport", RemoteAuth{Transport: Transport("rsync")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.auth.Validate()
			if tc.wantErr {
				if !errors.Is(err, ErrRemoteUnsupported) {
					t.Fatalf("Validate() = %v, want an ErrRemoteUnsupported", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate(): %v", err)
			}
		})
	}
}

func TestTransportForURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want Transport
		ok   bool
	}{
		{"https://github.com/o/r.git", TransportHTTPS, true},
		{"ssh://git@github.com/o/r.git", TransportSSH, true},
		{"ssh://git@bitbucket.org/acme/w.git", TransportSSH, true},
		{"http://github.com/o/r.git", "", false},
		{"git@github.com:o/r.git", "", false},
		{"", "", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			got, ok := transportForURL(tc.raw)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("transportForURL(%q) = %q,%v, want %q,%v", tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestOriginTransportDefaultsToHTTPS(t *testing.T) {
	t.Parallel()
	if got := originTransport(RemoteAuth{}); got != TransportHTTPS {
		t.Fatalf("originTransport(zero) = %q, want %q", got, TransportHTTPS)
	}
	if got := originTransport(sshAuth()); got != TransportSSH {
		t.Fatalf("originTransport(ssh) = %q, want %q", got, TransportSSH)
	}
}

// The key must land 0600 and outside the worktree: the workspace dir is
// mounted into the mission's sandbox, but wt/ is what gets committed.
func TestWriteSSHFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f, err := writeSSHFiles(dir, sshAuth())
	if err != nil {
		t.Fatalf("writeSSHFiles: %v", err)
	}
	if f.keyPath != filepath.Join(dir, "ssh_key") || f.knownHostsPath != filepath.Join(dir, "known_hosts") {
		t.Fatalf("unexpected paths: %+v", f)
	}
	info, err := os.Stat(f.keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("ssh key mode = %o, want 600", perm)
	}
	key, err := os.ReadFile(f.keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if !strings.HasSuffix(string(key), "\n") {
		t.Fatal("ssh rejects a key file with no trailing newline")
	}
	hosts, err := os.ReadFile(f.knownHostsPath)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	if string(hosts) != testKnownHosts+"\n" {
		t.Fatalf("known_hosts = %q", hosts)
	}
}

// A second provisioning must not leave the previous run's key behind.
func TestWriteSSHFilesOverwrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := sshAuth()
	first.SSHPrivateKey = "old-key"
	if _, err := writeSSHFiles(dir, first); err != nil {
		t.Fatalf("first writeSSHFiles: %v", err)
	}
	f, err := writeSSHFiles(dir, sshAuth())
	if err != nil {
		t.Fatalf("second writeSSHFiles: %v", err)
	}
	key, err := os.ReadFile(f.keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if strings.Contains(string(key), "old-key") {
		t.Fatalf("stale key survived: %q", key)
	}
}

// Every option in GIT_SSH_COMMAND is a security decision; a silent
// regression on any of them weakens the connection without failing.
func TestSSHCommand(t *testing.T) {
	t.Parallel()
	cmd := SSHCommand(sshFiles{keyPath: "/ws/ssh_key", knownHostsPath: "/ws/known_hosts"})
	for _, want := range []string{
		"ssh -F /dev/null",
		"-i '/ws/ssh_key'",
		"-o IdentitiesOnly=yes",
		"-o UserKnownHostsFile='/ws/known_hosts'",
		"-o StrictHostKeyChecking=yes",
		"-o BatchMode=yes",
		"-o ConnectTimeout=15",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("GIT_SSH_COMMAND %q is missing %q", cmd, want)
		}
	}
	for _, never := range []string{"StrictHostKeyChecking=no", "StrictHostKeyChecking=accept-new"} {
		if strings.Contains(cmd, never) {
			t.Fatalf("GIT_SSH_COMMAND %q weakens host key checking with %q", cmd, never)
		}
	}
}

// git runs GIT_SSH_COMMAND through a shell, so a workspace root with a
// space (or worse) must survive intact rather than splitting into args.
func TestSSHCommandQuotesAwkwardPaths(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"/tmp/my work/ws", "/tmp/it's here/ws"} {
		f := sshFiles{keyPath: filepath.Join(dir, "ssh_key"), knownHostsPath: filepath.Join(dir, "known_hosts")}
		cmd := SSHCommand(f)
		// Echo the composed argv through a real shell and read back what
		// the key path actually became: a fake would forgive the bug.
		out, err := exec.Command("/bin/sh", "-c", "set -- "+strings.TrimPrefix(cmd, "ssh ")+`; for a; do echo "$a"; done`).CombinedOutput() //nolint:gosec // the composed command is the thing under test
		if err != nil {
			t.Fatalf("sh: %v: %s", err, out)
		}
		if !strings.Contains(string(out), f.keyPath) {
			t.Fatalf("key path %q did not survive the shell: %s", f.keyPath, out)
		}
	}
}

func TestGitAuthArgs(t *testing.T) {
	t.Parallel()
	const secret = "super-secret-token-value"
	https := gitAuthArgs("GIT_PUSH_TOKEN", HTTPSAuth(secret, "x-token-auth"))
	joined := strings.Join(https, " ")
	if !strings.Contains(joined, "credential.helper=") || !strings.Contains(joined, "x-token-auth") {
		t.Fatalf("https auth args = %v", https)
	}
	if strings.Contains(joined, secret) {
		t.Fatalf("the token leaked into argv: %v", https)
	}
	if got := gitAuthArgs("GIT_PUSH_TOKEN", sshAuth()); len(got) != 0 {
		t.Fatalf("an ssh auth needs no credential helper, got %v", got)
	}
}

func TestGitEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	https, err := gitEnv(dir, "GIT_PUSH_TOKEN", HTTPSAuth("tok", ""))
	if err != nil {
		t.Fatalf("gitEnv https: %v", err)
	}
	if !hasEnv(https, "GIT_PUSH_TOKEN=tok") || !hasEnv(https, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("https env missing entries: %v", tail(https))
	}
	if envValue(https, "GIT_SSH_COMMAND") != "" {
		t.Fatal("an https auth must not set GIT_SSH_COMMAND")
	}

	ssh, err := gitEnv(dir, "GIT_PUSH_TOKEN", sshAuth())
	if err != nil {
		t.Fatalf("gitEnv ssh: %v", err)
	}
	if !hasEnv(ssh, "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("ssh env must still disable the terminal prompt")
	}
	cmd := envValue(ssh, "GIT_SSH_COMMAND")
	if !strings.Contains(cmd, filepath.Join(dir, "ssh_key")) {
		t.Fatalf("GIT_SSH_COMMAND does not point at the workspace key: %q", cmd)
	}
	if envValue(ssh, "GIT_PUSH_TOKEN") != "" {
		t.Fatal("an ssh auth must not put the token in the environment")
	}
}

// An ssh auth that cannot write its files must fail before git runs,
// rather than silently connecting with no key.
func TestGitEnvSSHWriteFailure(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	if _, err := gitEnv(missing, "GIT_PUSH_TOKEN", sshAuth()); err == nil {
		t.Fatal("gitEnv into a nonexistent workspace dir should fail")
	}
}

func TestScrubAuth(t *testing.T) {
	t.Parallel()
	if got := scrubAuth("fatal: bad credentials for tok", HTTPSAuth("tok", "")); strings.Contains(got, "tok") {
		t.Fatalf("token survived scrubbing: %q", got)
	}
	// An ssh auth has no token to scrub and the key never reaches output.
	const out = "Permission denied (publickey)."
	if got := scrubAuth(out, sshAuth()); got != out {
		t.Fatalf("scrubAuth mangled ssh output: %q", got)
	}
}

func TestProbeSSHRemoteRejectsNonSSHAuth(t *testing.T) {
	t.Parallel()
	err := ProbeSSHRemote(context.Background(), t.TempDir(), "ssh://git@github.com/o/r.git", HTTPSAuth("tok", ""), time.Second)
	if !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("probe with an https auth = %v, want ErrRemoteUnsupported", err)
	}
}

func TestProbeSSHRemoteRejectsIncompleteAuth(t *testing.T) {
	t.Parallel()
	auth := sshAuth()
	auth.KnownHosts = nil
	err := ProbeSSHRemote(context.Background(), t.TempDir(), "ssh://git@github.com/o/r.git", auth, time.Second)
	if !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("probe with no pinned hosts = %v, want ErrRemoteUnsupported", err)
	}
}

// The probe must report a failure rather than hang or panic when the
// remote is unusable. A file:// URL with an ssh auth fails locally, so
// this needs no network.
func TestProbeSSHRemoteFailsOnUnreachableRemote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	dir := t.TempDir()
	err := ProbeSSHRemote(context.Background(), dir, "ssh://git@127.0.0.1:1/o/r.git", sshAuth(), 10*time.Second)
	if err == nil {
		t.Fatal("probe against a dead port should fail")
	}
	if !strings.Contains(err.Error(), "ssh probe") {
		t.Fatalf("probe error is not labeled: %v", err)
	}
}

// Push must refuse when the resolved auth and the origin's own scheme
// disagree: that means the adapter and the worktree are pushing to
// different remotes (issue #796).
func TestPushRejectsTransportMismatch(t *testing.T) {
	t.Parallel()
	requireGitForPush(t)
	ctx := context.Background()
	ws := t.TempDir()
	wt := filepath.Join(ws, "wt")
	if err := os.MkdirAll(wt, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", "https://github.com/o/r.git"},
	} {
		if out, err := runGit(ctx, wt, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	w := NewWorkspace(ws, nil, discardLogger())
	_, err := w.Push(ctx, wt, "main", sshAuth())
	if !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("Push with an ssh auth against an https origin = %v, want ErrRemoteUnsupported", err)
	}
	if err == nil || !strings.Contains(err.Error(), "credentials were resolved") {
		t.Fatalf("mismatch error should name the disagreement, got: %v", err)
	}
}

// The mirror case: an https auth against an ssh origin.
func TestPushRejectsHTTPSAuthOnSSHOrigin(t *testing.T) {
	t.Parallel()
	requireGitForPush(t)
	ctx := context.Background()
	ws := t.TempDir()
	wt := filepath.Join(ws, "wt")
	if err := os.MkdirAll(wt, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", "ssh://git@github.com/o/r.git"},
	} {
		if out, err := runGit(ctx, wt, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	w := NewWorkspace(ws, nil, discardLogger())
	if _, err := w.Push(ctx, wt, "main", HTTPSAuth("tok", "")); !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("Push with an https auth against an ssh origin = %v, want ErrRemoteUnsupported", err)
	}
}

// SetOrigin must now accept an ssh:// remote, which validateRemote
// rejected before #796.
func TestSetOriginAcceptsSSH(t *testing.T) {
	t.Parallel()
	requireGitForPush(t)
	ctx := context.Background()
	ws := t.TempDir()
	wt := filepath.Join(ws, "wt")
	if err := os.MkdirAll(wt, 0o750); err != nil {
		t.Fatal(err)
	}
	if out, err := runGit(ctx, wt, "init", "-q", "-b", "main"); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	w := NewWorkspace(ws, nil, discardLogger())
	const remote = "ssh://git@github.com/o/r.git"
	if err := w.SetOrigin(ctx, wt, remote); err != nil {
		t.Fatalf("SetOrigin(ssh): %v", err)
	}
	out, err := runGit(ctx, wt, "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("get-url: %v: %s", err, out)
	}
	if strings.TrimSpace(out) != remote {
		t.Fatalf("origin = %q, want %q", strings.TrimSpace(out), remote)
	}
}

func TestWorkspaceDirOf(t *testing.T) {
	t.Parallel()
	if got := workspaceDirOf("/ws/coding/m1/wt"); got != "/ws/coding/m1" {
		t.Fatalf("workspaceDirOf = %q, want /ws/coding/m1", got)
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func envValue(env []string, key string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v
		}
	}
	return ""
}

// tail keeps a failure message short: os.Environ() dominates the slice.
func tail(env []string) []string {
	if len(env) > 4 {
		return env[len(env)-4:]
	}
	return env
}
