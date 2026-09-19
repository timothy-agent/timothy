package gitprovider

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestSSHKnownHostsParse proves every pinned known_hosts line is a real
// one ssh would accept: a typo in a base64 blob would otherwise only
// surface as a failed clone against the live host.
func TestSSHKnownHostsParse(t *testing.T) {
	t.Parallel()
	for _, kind := range Kinds() {
		d, ok := Lookup(kind)
		if !ok {
			t.Fatalf("Lookup(%s): not registered", kind)
		}
		lines := d.SSHKnownHosts()
		if !d.Supports(CapSSHTransport) {
			continue
		}
		if len(lines) == 0 {
			t.Fatalf("%s: supports ssh transport but pins no known_hosts line", kind)
		}
		for _, line := range lines {
			marker, hosts, pubKey, _, rest, err := ssh.ParseKnownHosts([]byte(line + "\n"))
			if err != nil {
				t.Fatalf("%s: ParseKnownHosts(%q): %v", kind, line, err)
			}
			if marker != "" {
				t.Fatalf("%s: line %q carries marker %q; a pinned host key needs none", kind, line, marker)
			}
			if len(rest) != 0 {
				t.Fatalf("%s: line %q holds more than one entry", kind, line)
			}
			if pubKey.Type() != ssh.KeyAlgoED25519 {
				t.Fatalf("%s: line %q is %s, want %s", kind, line, pubKey.Type(), ssh.KeyAlgoED25519)
			}
			if len(hosts) != 1 {
				t.Fatalf("%s: line %q names %d hosts, want exactly 1", kind, line, len(hosts))
			}
			if !hostIn(hosts[0], d.Hosts()) {
				t.Fatalf("%s: line %q pins host %q, which is not one of %v", kind, line, hosts[0], d.Hosts())
			}
		}
	}
}

// TestSSHKnownHostsFingerprints pins each line against the SHA256
// fingerprint its provider publishes, so a swapped-but-valid key cannot
// pass TestSSHKnownHostsParse unnoticed.
func TestSSHKnownHostsFingerprints(t *testing.T) {
	t.Parallel()
	want := map[Kind]string{
		// https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints
		KindGitHub: "SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU",
		// https://support.atlassian.com/bitbucket-cloud/docs/configure-ssh-and-two-step-verification/
		KindBitbucket: "SHA256:ybgmFkzwOSotHTHLJgHO0QN8L0xErw6vd0VhFA9m3SM",
	}
	for kind, fingerprint := range want {
		d, ok := Lookup(kind)
		if !ok {
			t.Fatalf("Lookup(%s): not registered", kind)
		}
		lines := d.SSHKnownHosts()
		if len(lines) != 1 {
			t.Fatalf("%s: want exactly one pinned line, got %d", kind, len(lines))
		}
		_, _, pubKey, _, _, err := ssh.ParseKnownHosts([]byte(lines[0] + "\n"))
		if err != nil {
			t.Fatalf("%s: ParseKnownHosts: %v", kind, err)
		}
		if got := ssh.FingerprintSHA256(pubKey); got != fingerprint {
			t.Fatalf("%s: fingerprint %s, want %s", kind, got, fingerprint)
		}
	}
}

// TestSSHCloneURLMatchesKnownHosts proves the host a clone URL points at
// is the host whose key is pinned: a mismatch would mean every ssh
// clone fails StrictHostKeyChecking.
func TestSSHCloneURLMatchesKnownHosts(t *testing.T) {
	t.Parallel()
	for _, kind := range Kinds() {
		d, _ := Lookup(kind)
		if !d.Supports(CapSSHTransport) {
			continue
		}
		url := d.SSHCloneURL(RepoRef{Owner: "octo", Name: "repo"})
		if !strings.HasPrefix(url, "ssh://git@"+d.Hosts()[0]+"/") {
			t.Fatalf("%s: SSHCloneURL = %q, want ssh://git@%s/…", kind, url, d.Hosts()[0])
		}
		for _, line := range d.SSHKnownHosts() {
			if !strings.HasPrefix(line, d.Hosts()[0]+" ") {
				t.Fatalf("%s: known_hosts line %q does not pin %s", kind, line, d.Hosts()[0])
			}
		}
	}
}
