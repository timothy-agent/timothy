package destinations

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// D-102 (issue #796): git clone/push run over SSH when the connector
// has a transport key, falling back to HTTPS+token when SSH cannot
// reach the host. The decision lives HERE, never in the missions
// package: missions mechanically executes whichever missions.RemoteAuth
// it is handed, so one place decides and one place records the
// fallback. REST API calls (PR create, repo create, identity) always
// stay on HTTPS+token whatever this decides — only the git wire
// protocol changes.

// SSHMaterial is a connector's resolved SSH transport state: the
// private key (OpenSSH PEM) when the connector has ssh_transport
// enabled and a registered key, empty otherwise. missions has no
// compile-time dependency on connectors, same reasoning as
// missions.CloneTokenResolver, so the concrete resolution is a closure
// cmd/brain/main.go wires.
type SSHMaterial struct {
	// Enabled reports ssh_transport on the connector config AND a
	// registered public key: either alone is not a usable transport.
	Enabled bool
	// PrivateKey is the transport key's private half.
	PrivateKey string
}

// SSHResolver resolves a connector id to its SSH transport material.
// A resolver error is never fatal: the caller logs it and stays on
// HTTPS, the same degrade as a connector with no key at all.
type SSHResolver func(ctx context.Context, connectorID string) (SSHMaterial, error)

// probeTimeout bounds the one ls-remote that proves the SSH key is
// registered with the host, before a clone or push commits to it.
const probeTimeout = 20 * time.Second

// sshAvailable reports whether the brain image has an ssh binary at
// all, looked up once per process: without it every SSH attempt fails
// identically, and shelling out per mission to find that out is waste.
var sshAvailable = sync.OnceValue(func() bool {
	_, err := exec.LookPath("ssh")
	return err == nil
})

// TransportResolver decides the transport for one connector's git
// operations against one repo, and records the fallback when SSH does
// not work out. Zero value (no ResolveSSH) always resolves HTTPS,
// which is exactly the pre-#796 behavior.
type TransportResolver struct {
	// ResolveSSH resolves a connector's SSH material; nil means no
	// connector ever has any, so every auth is HTTPS.
	ResolveSSH SSHResolver
	// Events records the mission.transport_fallback timeline event; nil
	// skips it (the manual push endpoints build a throwaway resolver
	// with no mission to record against).
	Events events
	Log    *slog.Logger
	// probe runs the reachability check; nil uses
	// missions.ProbeSSHRemote. A seam for tests, which have no host to
	// reach.
	probe func(ctx context.Context, workspaceDir, sshURL string, auth missions.RemoteAuth) error
	// hasSSH reports whether the ssh binary exists; nil uses the cached
	// sshAvailable. A seam for tests, so both branches are reachable
	// whether or not the test image happens to ship ssh.
	hasSSH func() bool
}

// sshPresent resolves the binary check with its cached default.
func (t *TransportResolver) sshPresent() bool {
	if t.hasSSH != nil {
		return t.hasSSH()
	}
	return sshAvailable()
}

// Resolve picks the auth for a git operation against repoURL through
// connectorID, given the descriptor for that connector's kind and the
// HTTPS token already resolved for it.
//
// SSH is attempted only when all of: the descriptor supports it, the
// connector has it enabled with a registered key, the brain image has
// an ssh binary, and a `git ls-remote` probe against the ssh URL
// succeeds. Any miss records mission.transport_fallback and returns
// the HTTPS auth. When SSH was selected and there is no token to fall
// back to, the probe failure is returned as a hard error rather than a
// silent no-op: a connector configured SSH-only has no second way in.
//
// missionID may be empty (no timeline to record against); workspaceDir
// is where the probe writes its key and known_hosts, the same files
// the clone or push will reuse.
func (t *TransportResolver) Resolve(ctx context.Context, connectorID, missionID, workspaceDir string, d gitprovider.Descriptor, ref gitprovider.RepoRef, token string) (missions.RemoteAuth, error) {
	https := missions.HTTPSAuth(token, d.HTTPUsername())
	if t == nil || t.ResolveSSH == nil || !d.Supports(gitprovider.CapSSHTransport) {
		return https, nil
	}
	material, err := t.ResolveSSH(ctx, connectorID)
	if err != nil {
		t.warn("transport: resolve ssh material failed; staying on https", "connector_id", connectorID, "error", err)
		return https, nil
	}
	if !material.Enabled || material.PrivateKey == "" {
		return https, nil
	}

	ssh := missions.RemoteAuth{
		Transport:     missions.TransportSSH,
		HTTPUsername:  d.HTTPUsername(),
		Token:         token,
		SSHPrivateKey: material.PrivateKey,
		KnownHosts:    d.SSHKnownHosts(),
	}
	reason := ""
	switch {
	case !t.sshPresent():
		reason = "no ssh binary in the brain image"
	default:
		if err := ssh.Validate(); err != nil {
			reason = err.Error()
			break
		}
		if err := t.runProbe(ctx, workspaceDir, d.SSHCloneURL(ref), ssh); err != nil {
			reason = err.Error()
		}
	}
	if reason == "" {
		return ssh, nil
	}
	if token == "" {
		// SSH-only connector: there is no second way in, so failing
		// honestly beats a push that quietly never happens.
		return missions.RemoteAuth{}, &SSHUnavailableError{ConnectorID: connectorID, Reason: reason}
	}
	t.warn("transport: ssh unavailable; falling back to https", "connector_id", connectorID, "mission_id", missionID, "reason", reason)
	t.record(ctx, missionID, reason)
	return https, nil
}

// runProbe dispatches to the injected probe or the real one.
func (t *TransportResolver) runProbe(ctx context.Context, workspaceDir, sshURL string, auth missions.RemoteAuth) error {
	if t.probe != nil {
		return t.probe(ctx, workspaceDir, sshURL, auth)
	}
	return missions.ProbeSSHRemote(ctx, workspaceDir, sshURL, auth, probeTimeout)
}

// record appends the fallback to the mission timeline; a failure to
// record is logged, never propagated, since the delivery itself is
// still fine over https.
func (t *TransportResolver) record(ctx context.Context, missionID, reason string) {
	if t.Events == nil || missionID == "" {
		return
	}
	payload := map[string]any{"from": string(missions.TransportSSH), "to": string(missions.TransportHTTPS), "reason": reason}
	if err := t.Events.AppendEvent(ctx, missionID, "mission.transport_fallback", payload); err != nil {
		t.warn("transport: record transport_fallback failed", "mission_id", missionID, "error", err)
	}
}

func (t *TransportResolver) warn(msg string, args ...any) {
	if t.Log != nil {
		t.Log.Warn(msg, args...)
	}
}

// ResolveClone satisfies missions.CloneAuthResolver: the same
// transport decision Resolve makes for a push, applied to a mission's
// clone, plus the clone URL that matches the chosen transport. The
// descriptor comes from the connector's own Client (never from the
// URL's hostname, which cannot identify a self-hosted provider, see
// gitprovider.ForHost), so clients must be wired for ssh to be
// possible at all; without them every clone stays https.
func (t *TransportResolver) ResolveClone(ctx context.Context, clients Clients, connectorID, missionID, workspaceDir, repoURL, token string) (missions.RemoteAuth, string, error) {
	https := missions.HTTPSAuth(token, "")
	if t == nil || clients == nil || connectorID == "" {
		return https, "", nil
	}
	c, release, err := clients.ClientFor(ctx, connectorID)
	if err != nil {
		t.warn("transport: resolve clone client failed; cloning over https", "connector_id", connectorID, "error", err)
		return https, "", nil
	}
	defer release()
	https = missions.HTTPSAuth(token, c.HTTPUsername())
	ref, ok := c.ParseRepoURL(repoURL)
	if !ok {
		return https, "", nil
	}
	auth, err := t.Resolve(ctx, connectorID, missionID, workspaceDir, c, ref, token)
	if err != nil {
		return missions.RemoteAuth{}, "", err
	}
	if auth.IsSSH() {
		return auth, c.SSHCloneURL(ref), nil
	}
	return auth, "", nil
}

// SSHUnavailableError is Resolve's answer when SSH was selected, the
// probe failed, and no token exists to fall back to.
type SSHUnavailableError struct {
	ConnectorID string
	Reason      string
}

func (e *SSHUnavailableError) Error() string {
	return "ssh transport is configured for connector " + e.ConnectorID +
		" but unreachable, and no token is configured to fall back to: " + e.Reason
}
