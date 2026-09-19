package destinations

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

const testPrivateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nZmFrZQ==\n-----END OPENSSH PRIVATE KEY-----"

// sshEnabled resolves every connector as ssh-capable.
func sshEnabled(context.Context, string) (SSHMaterial, error) {
	return SSHMaterial{Enabled: true, PrivateKey: testPrivateKey}, nil
}

// okProbe / failProbe stand in for the real ls-remote: CI has no host
// to reach, so the seam is the probe itself, not the network.
func okProbe(context.Context, string, string, missions.RemoteAuth) error { return nil }

func failProbe(context.Context, string, string, missions.RemoteAuth) error {
	return errors.New("Permission denied (publickey)")
}

func testRef() gitprovider.RepoRef { return gitprovider.RepoRef{Owner: "octo", Name: "repo"} }

// yesSSH/noSSH pin the binary check, so both branches are reachable
// whether or not the test image happens to ship ssh.
func yesSSH() bool { return true }

func noSSH() bool { return false }

// noSSHDescriptor is a Descriptor that does not support the transport,
// standing in for a provider added before ssh (or one that never will).
type noSSHDescriptor struct{ gitprovider.GitHub }

func (noSSHDescriptor) Supports(c gitprovider.Capability) bool {
	return c != gitprovider.CapSSHTransport
}

func TestTransportResolveStaysHTTPSWhenUnconfigured(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		r    *TransportResolver
	}{
		{"nil resolver", nil},
		{"no ssh resolver wired", &TransportResolver{}},
		{"resolver reports disabled", &TransportResolver{ResolveSSH: func(context.Context, string) (SSHMaterial, error) {
			return SSHMaterial{}, nil
		}}},
		{"enabled but no key", &TransportResolver{ResolveSSH: func(context.Context, string) (SSHMaterial, error) {
			return SSHMaterial{Enabled: true}, nil
		}}},
		{"resolver errors", &TransportResolver{ResolveSSH: func(context.Context, string) (SSHMaterial, error) {
			return SSHMaterial{}, errors.New("secret store down")
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			auth, err := tc.r.Resolve(context.Background(), "conn1", "m1", t.TempDir(), gitprovider.GitHub{}, testRef(), "tok")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if auth.IsSSH() {
				t.Fatal("transport must stay https")
			}
			if auth.Token != "tok" || auth.HTTPUsername != "x-access-token" {
				t.Fatalf("https auth = %+v", auth)
			}
		})
	}
}

// A provider that does not support the transport is never probed, even
// with a key configured.
func TestTransportResolveSkipsUnsupportedProvider(t *testing.T) {
	t.Parallel()
	probed := false
	r := &TransportResolver{
		ResolveSSH: sshEnabled,
		hasSSH:     yesSSH,
		probe: func(context.Context, string, string, missions.RemoteAuth) error {
			probed = true
			return nil
		},
	}
	auth, err := r.Resolve(context.Background(), "conn1", "m1", t.TempDir(), noSSHDescriptor{}, testRef(), "tok")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if auth.IsSSH() || probed {
		t.Fatalf("unsupported provider was probed (auth=%+v probed=%v)", auth, probed)
	}
}

func TestTransportResolvePicksSSHOnProbeSuccess(t *testing.T) {
	t.Parallel()
	ev := &fakeEvents{}
	r := &TransportResolver{ResolveSSH: sshEnabled, Events: ev, probe: okProbe, hasSSH: yesSSH}
	auth, err := r.Resolve(context.Background(), "conn1", "m1", t.TempDir(), gitprovider.GitHub{}, testRef(), "tok")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !auth.IsSSH() {
		t.Fatalf("want an ssh auth, got %+v", auth)
	}
	if auth.SSHPrivateKey != testPrivateKey {
		t.Fatal("the resolved key did not reach the auth")
	}
	if len(auth.KnownHosts) == 0 {
		t.Fatal("the descriptor's pinned known_hosts did not reach the auth")
	}
	// The token still travels, because a PR create is REST over https
	// whatever the git transport is.
	if auth.Token != "tok" {
		t.Fatal("the https token must survive an ssh transport choice")
	}
	if n := ev.countOf("mission.transport_fallback"); n != 0 {
		t.Fatalf("a successful probe recorded %d fallback events", n)
	}
}

func TestTransportResolveFallsBackOnProbeFailure(t *testing.T) {
	t.Parallel()
	ev := &fakeEvents{}
	r := &TransportResolver{ResolveSSH: sshEnabled, Events: ev, probe: failProbe, hasSSH: yesSSH}
	auth, err := r.Resolve(context.Background(), "conn1", "m1", t.TempDir(), gitprovider.GitHub{}, testRef(), "tok")
	if err != nil {
		t.Fatalf("a fallback is not an error: %v", err)
	}
	if auth.IsSSH() {
		t.Fatalf("want the https fallback, got %+v", auth)
	}
	if auth.Token != "tok" {
		t.Fatalf("fallback lost the token: %+v", auth)
	}
	e, ok := ev.find("mission.transport_fallback")
	if !ok {
		t.Fatal("no mission.transport_fallback recorded")
	}
	if e["from"] != "ssh" || e["to"] != "https" {
		t.Fatalf("fallback payload = %v, want from ssh to https", e)
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "publickey") {
		t.Fatalf("fallback payload lost the reason: %v", e)
	}
}

// An SSH-only connector has no second way in, so a failed probe is a
// hard error rather than a silent no-op (issue #796's third AC).
func TestTransportResolveFailsWhenNoTokenToFallBackOn(t *testing.T) {
	t.Parallel()
	ev := &fakeEvents{}
	r := &TransportResolver{ResolveSSH: sshEnabled, Events: ev, probe: failProbe, hasSSH: yesSSH}
	_, err := r.Resolve(context.Background(), "conn1", "m1", t.TempDir(), gitprovider.GitHub{}, testRef(), "")
	var unavailable *SSHUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Resolve = %v, want an SSHUnavailableError", err)
	}
	if unavailable.ConnectorID != "conn1" || !strings.Contains(unavailable.Error(), "publickey") {
		t.Fatalf("error lost its detail: %v", err)
	}
	if n := ev.countOf("mission.transport_fallback"); n != 0 {
		t.Fatal("a hard failure must not claim a fallback happened")
	}
}

// A mission-less caller (the manual push endpoints build a throwaway
// resolver) must not panic on the event record.
func TestTransportResolveWithoutMissionOrEvents(t *testing.T) {
	t.Parallel()
	r := &TransportResolver{ResolveSSH: sshEnabled, probe: failProbe, hasSSH: yesSSH}
	auth, err := r.Resolve(context.Background(), "conn1", "", t.TempDir(), gitprovider.GitHub{}, testRef(), "tok")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if auth.IsSSH() {
		t.Fatal("want the https fallback")
	}
}

func TestResolveCloneReturnsSSHCloneURL(t *testing.T) {
	t.Parallel()
	c := &fakeGitClient{Descriptor: gitprovider.GitHub{}}
	r := &TransportResolver{ResolveSSH: sshEnabled, Events: &fakeEvents{}, probe: okProbe, hasSSH: yesSSH}
	auth, cloneURL, err := r.ResolveClone(context.Background(), clients(c), "conn1", "m1", t.TempDir(), "https://github.com/octo/repo.git", "tok")
	if err != nil {
		t.Fatalf("ResolveClone: %v", err)
	}
	if !auth.IsSSH() {
		t.Fatalf("want an ssh auth, got %+v", auth)
	}
	if cloneURL != "ssh://git@github.com/octo/repo.git" {
		t.Fatalf("clone URL = %q, want the ssh form", cloneURL)
	}
}

// An https clone keeps the caller's own URL: returning "" means "do not
// rewrite", which is what Provision reads.
func TestResolveCloneLeavesHTTPSURLAlone(t *testing.T) {
	t.Parallel()
	c := &fakeGitClient{Descriptor: gitprovider.GitHub{}}
	r := &TransportResolver{ResolveSSH: sshEnabled, Events: &fakeEvents{}, probe: failProbe, hasSSH: yesSSH}
	auth, cloneURL, err := r.ResolveClone(context.Background(), clients(c), "conn1", "m1", t.TempDir(), "https://github.com/octo/repo.git", "tok")
	if err != nil {
		t.Fatalf("ResolveClone: %v", err)
	}
	if auth.IsSSH() || cloneURL != "" {
		t.Fatalf("want an https auth and no rewrite, got %+v / %q", auth, cloneURL)
	}
}

func TestResolveCloneDegradesWithoutClientsOrConnector(t *testing.T) {
	t.Parallel()
	r := &TransportResolver{ResolveSSH: sshEnabled, probe: okProbe, hasSSH: yesSSH}
	for _, tc := range []struct {
		name        string
		clients     Clients
		connectorID string
	}{
		{"no clients wired", nil, "conn1"},
		{"no connector on the mission", clients(&fakeGitClient{Descriptor: gitprovider.GitHub{}}), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			auth, cloneURL, err := r.ResolveClone(context.Background(), tc.clients, tc.connectorID, "m1", t.TempDir(), "https://github.com/octo/repo.git", "tok")
			if err != nil {
				t.Fatalf("ResolveClone: %v", err)
			}
			if auth.IsSSH() || cloneURL != "" {
				t.Fatalf("want a plain https clone, got %+v / %q", auth, cloneURL)
			}
		})
	}
}

// An unparseable repo URL leaves the probe no target, so the clone
// stays https rather than failing.
func TestResolveCloneUnparseableURLStaysHTTPS(t *testing.T) {
	t.Parallel()
	c := &fakeGitClient{Descriptor: gitprovider.GitHub{}}
	r := &TransportResolver{ResolveSSH: sshEnabled, probe: okProbe, hasSSH: yesSSH}
	auth, cloneURL, err := r.ResolveClone(context.Background(), clients(c), "conn1", "m1", t.TempDir(), "https://example.com/not-a-repo", "tok")
	if err != nil {
		t.Fatalf("ResolveClone: %v", err)
	}
	if auth.IsSSH() || cloneURL != "" {
		t.Fatalf("want a plain https clone, got %+v / %q", auth, cloneURL)
	}
}

// The cached lookup must report what PATH actually holds: a wrong
// answer either disables ssh everywhere or attempts it with no binary.
func TestSSHAvailableMatchesPath(t *testing.T) {
	t.Parallel()
	_, err := exec.LookPath("ssh")
	if want := err == nil; sshAvailable() != want {
		t.Fatalf("sshAvailable() = %v, want %v", sshAvailable(), want)
	}
}

// sshPresent must fall through to the cached lookup when no seam is
// injected, so production never silently runs on a test default.
func TestSSHPresentDefaultsToTheCachedLookup(t *testing.T) {
	t.Parallel()
	if (&TransportResolver{}).sshPresent() != sshAvailable() {
		t.Fatal("sshPresent with no seam does not use the cached lookup")
	}
}

// Without an ssh binary there is nothing to probe, so the resolver must
// fall back rather than shelling out to a command that cannot exist.
func TestTransportResolveFallsBackWithoutSSHBinary(t *testing.T) {
	t.Parallel()
	ev := &fakeEvents{}
	probed := false
	r := &TransportResolver{ResolveSSH: sshEnabled, Events: ev, hasSSH: noSSH, probe: func(context.Context, string, string, missions.RemoteAuth) error {
		probed = true
		return nil
	}}
	auth, err := r.Resolve(context.Background(), "conn1", "m1", t.TempDir(), gitprovider.GitHub{}, testRef(), "tok")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if auth.IsSSH() || probed {
		t.Fatalf("resolved ssh with no ssh binary (auth=%+v probed=%v)", auth, probed)
	}
	e, ok := ev.find("mission.transport_fallback")
	if !ok {
		t.Fatal("no fallback recorded")
	}
	if reason, _ := e["reason"].(string); !strings.Contains(reason, "ssh binary") {
		t.Fatalf("fallback reason = %v, want it to name the missing binary", e)
	}
}
