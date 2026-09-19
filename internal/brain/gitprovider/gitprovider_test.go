package gitprovider

import (
	"strings"
	"testing"
)

func TestRegistryHoldsBothCloudKinds(t *testing.T) {
	t.Parallel()
	for _, k := range []Kind{KindGitHub, KindBitbucket} {
		d, ok := Lookup(k)
		if !ok {
			t.Fatalf("Lookup(%q): not registered", k)
		}
		if d.Kind() != k {
			t.Fatalf("Lookup(%q).Kind() = %q", k, d.Kind())
		}
	}
	if got := Kinds(); len(got) != 2 || got[0] != KindBitbucket || got[1] != KindGitHub {
		t.Fatalf("Kinds() = %v, want [bitbucket github]", got)
	}
}

func TestIsKind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind string
		want bool
	}{
		{"github", true},
		{"bitbucket", true},
		{"gitlab", false},
		{"email", false},
		{"GitHub", false}, // kinds are stored lowercase
		{"", false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			if got := IsKind(tc.kind); got != tc.want {
				t.Fatalf("IsKind(%q) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestForHost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		host string
		want Kind
		ok   bool
	}{
		{"github.com", KindGitHub, true},
		{"GitHub.com", KindGitHub, true},
		{" bitbucket.org ", KindBitbucket, true},
		{"ghe.example.com", "", false},
		{"github.com.evil.test", "", false},
		{"", "", false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			t.Parallel()
			d, ok := ForHost(tc.host)
			if ok != tc.ok {
				t.Fatalf("ForHost(%q) ok = %v, want %v", tc.host, ok, tc.ok)
			}
			if ok && d.Kind() != tc.want {
				t.Fatalf("ForHost(%q) = %q, want %q", tc.host, d.Kind(), tc.want)
			}
		})
	}
}

// Register is documented as init-time safe; a replacement of a live kind
// must not corrupt the host index either.
func TestRegisterReplacesAndIndexesHosts(t *testing.T) {
	d, _ := Lookup(KindGitHub)
	t.Cleanup(func() { Register(KindGitHub, d) })
	Register(KindGitHub, GitHub{})
	if got, ok := Lookup(KindGitHub); !ok || got.Kind() != KindGitHub {
		t.Fatalf("re-register lost the kind")
	}
	if _, ok := ForHost("github.com"); !ok {
		t.Fatal("re-register lost the host index")
	}
}

// TestParseRepoURLGitHub ports missions.TestParseGitHubRepoURL and adds
// the host gate isGitHubHost used to carry separately (issue #787), plus
// the browser and scp-ssh forms.
func TestParseRepoURLGitHub(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		url    string
		owner  string
		repo   string
		wantOK bool
	}{
		{"with .git suffix", "https://github.com/octocat/hello-world.git", "octocat", "hello-world", true},
		{"without .git suffix", "https://github.com/octocat/hello-world", "octocat", "hello-world", true},
		{"trailing slash", "https://github.com/octocat/hello-world/", "octocat", "hello-world", true},
		{"uppercase host", "https://GitHub.com/octocat/hello-world", "octocat", "hello-world", true},
		{"clone button user@ form", "https://someone@github.com/octocat/hello-world.git", "octocat", "hello-world", true},
		{"browser tree url", "https://github.com/octocat/hello-world/tree/main/src", "octocat", "hello-world", true},
		{"pull request page", "https://github.com/octocat/hello-world/pull/12", "octocat", "hello-world", true},
		{"scp ssh form", "git@github.com:octocat/hello-world.git", "octocat", "hello-world", true},
		{"ssh url", "ssh://git@github.com/octocat/hello-world.git", "octocat", "hello-world", true},
		{"unknown page kind is rejected", "https://github.com/octocat/hello-world/settings", "", "", false},
		{"malformed, no repo segment", "https://github.com/octocat", "", "", false},
		{"not a URL", "not-a-url", "", "", false},
		{"other host is rejected", "https://ghe.example.com/o/r.git", "", "", false},
		{"lookalike host is rejected", "https://github.com.evil.test/o/r", "", "", false},
		{"bitbucket host is rejected", "https://bitbucket.org/ws/repo.git", "", "", false},
		{"empty", "", "", "", false},
	}
	d, _ := Lookup(KindGitHub)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ref, ok := d.ParseRepoURL(tc.url)
			if ok != tc.wantOK || ref.Owner != tc.owner || ref.Name != tc.repo {
				t.Fatalf("ParseRepoURL(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.url, ref.Owner, ref.Name, ok, tc.owner, tc.repo, tc.wantOK)
			}
		})
	}
}

// TestParseRepoURLBitbucket ports missions.TestParseBitbucketRepoURL
// case for case, plus the scp form the old parser rejected outright.
func TestParseRepoURLBitbucket(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		url       string
		workspace string
		slug      string
		wantOK    bool
	}{
		{"with .git suffix", "https://bitbucket.org/acme-team/widget-service.git", "acme-team", "widget-service", true},
		{"without .git suffix", "https://bitbucket.org/ws/repo", "ws", "repo", true},
		{"trailing slash", "https://bitbucket.org/ws/repo/", "ws", "repo", true},
		{"clone button form with a username", "https://someone@bitbucket.org/ws/repo.git", "ws", "repo", true},
		{"browser url with a branch path", "https://bitbucket.org/ws/repo/src/master/", "ws", "repo", true},
		{"browser url deep in the tree", "https://bitbucket.org/ws/repo/src/main/app/Http/", "ws", "repo", true},
		{"pull request page", "https://bitbucket.org/ws/repo/pull-requests/12", "ws", "repo", true},
		{"commits page", "https://bitbucket.org/ws/repo/commits/branch/main", "ws", "repo", true},
		{"unknown page kind is rejected", "https://bitbucket.org/ws/repo/settings", "", "", false},
		{"github host is rejected", "https://github.com/octocat/hello-world.git", "", "", false},
		{"scp ssh form is normalized, not rejected", "git@bitbucket.org:ws/repo.git", "ws", "repo", true},
		{"no slug", "https://bitbucket.org/ws", "", "", false},
		{"empty", "", "", "", false},
	}
	d, _ := Lookup(KindBitbucket)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ref, ok := d.ParseRepoURL(tc.url)
			if ok != tc.wantOK || ref.Owner != tc.workspace || ref.Name != tc.slug {
				t.Fatalf("ParseRepoURL(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.url, ref.Owner, ref.Name, ok, tc.workspace, tc.slug, tc.wantOK)
			}
		})
	}
}

// The shared parser's own rejection paths, reachable through either
// descriptor: a URL url.Parse itself refuses, a scheme neither https
// nor ssh, and a path whose repo segment is nothing but ".git".
func TestParseRepoURLRejections(t *testing.T) {
	t.Parallel()
	d, _ := Lookup(KindGitHub)
	for _, raw := range []string{
		"https://github.com/o/\x7f\x00",
		"://github.com/o/r",
		"ftp://github.com/o/r.git",
		"file:///github.com/o/r",
		"https://github.com/o/.git",
		"https://github.com//r",
		"https://github.com/o/",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if ref, ok := d.ParseRepoURL(raw); ok {
				t.Fatalf("ParseRepoURL(%q) accepted as %+v", raw, ref)
			}
		})
	}
}

// Owner must survive a nested path for a future GitLab group, so nothing
// in the shared parser may assume a single owner segment.
func TestRepoRefHoldsNestedOwner(t *testing.T) {
	t.Parallel()
	r := RepoRef{Owner: "group/subgroup", Name: "widget"}
	if got := r.FullName(); got != "group/subgroup/widget" {
		t.Fatalf("FullName() = %q", got)
	}
}

func TestNormalizeSCP(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want string
		ok   bool
	}{
		{"git@github.com:o/r.git", "ssh://git@github.com/o/r.git", true},
		{"git@bitbucket.org:ws/repo.git", "ssh://git@bitbucket.org/ws/repo.git", true},
		{"git@github.com:/o/r.git", "ssh://git@github.com/o/r.git", true},
		{"https://github.com/o/r.git", "", false},
		{"ssh://git@github.com/o/r.git", "", false},
		{"", "", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			got, ok := NormalizeSCP(tc.raw)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("NormalizeSCP(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestCloneURLsRoundTrip: the https form is exactly what
// missions.BitbucketCloneURL produced, and every accepted input shape
// collapses to the same canonical clone URL.
func TestCloneURLsRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind      Kind
		inputs    []string
		wantHTTPS string
		wantSSH   string
	}{
		{
			kind: KindBitbucket,
			inputs: []string{
				"https://bitbucket.org/acme-team/widget-service.git",
				"https://someone@bitbucket.org/acme-team/widget-service.git",
				"https://bitbucket.org/acme-team/widget-service/src/master/",
				"https://bitbucket.org/acme-team/widget-service",
				"git@bitbucket.org:acme-team/widget-service.git",
			},
			wantHTTPS: "https://bitbucket.org/acme-team/widget-service.git",
			wantSSH:   "ssh://git@bitbucket.org/acme-team/widget-service.git",
		},
		{
			kind: KindGitHub,
			inputs: []string{
				"https://github.com/octocat/hello-world.git",
				"https://github.com/octocat/hello-world",
				"git@github.com:octocat/hello-world.git",
			},
			wantHTTPS: "https://github.com/octocat/hello-world.git",
			wantSSH:   "ssh://git@github.com/octocat/hello-world.git",
		},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			t.Parallel()
			d, _ := Lookup(tc.kind)
			for _, in := range tc.inputs {
				ref, ok := d.ParseRepoURL(in)
				if !ok {
					t.Fatalf("ParseRepoURL(%q) rejected", in)
				}
				if got := d.HTTPSCloneURL(ref); got != tc.wantHTTPS {
					t.Fatalf("HTTPSCloneURL from %q = %q, want %q", in, got, tc.wantHTTPS)
				}
				if got := d.SSHCloneURL(ref); got != tc.wantSSH {
					t.Fatalf("SSHCloneURL from %q = %q, want %q", in, got, tc.wantSSH)
				}
			}
		})
	}
}

// The credential username table this replaces lived in missions/push.go.
func TestHTTPUsername(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ kind, want string }{
		{"github", "x-access-token"},
		{"bitbucket", "x-token-auth"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			d, ok := Lookup(Kind(tc.kind))
			if !ok {
				t.Fatalf("Lookup(%q)", tc.kind)
			}
			if got := d.HTTPUsername(); got != tc.want {
				t.Fatalf("HTTPUsername() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSupportsAndKnownHosts(t *testing.T) {
	t.Parallel()
	gh, _ := Lookup(KindGitHub)
	bb, _ := Lookup(KindBitbucket)
	for _, tc := range []struct {
		name string
		d    Descriptor
		cap  Capability
		want bool
	}{
		{"github creates repos", gh, CapCreateRepo, true},
		{"github verifies ssh signatures", gh, CapSSHSigningVerify, true},
		{"github clones over ssh", gh, CapSSHTransport, true},
		{"bitbucket creates repos", bb, CapCreateRepo, true},
		{"bitbucket ssh signing unknown, defaults false", bb, CapSSHSigningVerify, false},
		{"bitbucket clones over ssh", bb, CapSSHTransport, true},
		{"unknown capability is false", gh, Capability(99), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.d.Supports(tc.cap); got != tc.want {
				t.Fatalf("Supports = %v, want %v", got, tc.want)
			}
		})
	}
	for _, d := range []Descriptor{gh, bb} {
		for _, h := range d.Hosts() {
			if h != strings.ToLower(h) {
				t.Fatalf("%s: host %q must be lowercase for the ForHost index", d.Kind(), h)
			}
		}
	}
}
