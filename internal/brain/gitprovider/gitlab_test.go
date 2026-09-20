package gitprovider

import (
	"strings"
	"testing"
)

// TestParseRepoURLGitLab covers the shapes the other descriptors cover,
// plus the one they cannot: a group path nested past two segments.
func TestParseRepoURLGitLab(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		url    string
		owner  string
		repo   string
		wantOK bool
	}{
		{"with .git suffix", "https://gitlab.com/acme/widget.git", "acme", "widget", true},
		{"without .git suffix", "https://gitlab.com/acme/widget", "acme", "widget", true},
		{"trailing slash", "https://gitlab.com/acme/widget/", "acme", "widget", true},
		{"uppercase host", "https://GitLab.com/acme/widget", "acme", "widget", true},
		{"clone button user@ form", "https://someone@gitlab.com/acme/widget.git", "acme", "widget", true},
		{"scp ssh form", "git@gitlab.com:acme/widget.git", "acme", "widget", true},
		{"ssh url", "ssh://git@gitlab.com/acme/widget.git", "acme", "widget", true},

		// The nested-group cases: everything before the project slug is
		// the owner, at any depth.
		{"one subgroup", "https://gitlab.com/acme/platform/widget.git", "acme/platform", "widget", true},
		{"two subgroups", "https://gitlab.com/acme/platform/backend/widget.git", "acme/platform/backend", "widget", true},
		{"four subgroups", "https://gitlab.com/a/b/c/d/widget", "a/b/c/d", "widget", true},
		{"nested over ssh", "ssh://git@gitlab.com/acme/platform/widget.git", "acme/platform", "widget", true},
		{"nested scp form", "git@gitlab.com:acme/platform/backend/widget.git", "acme/platform/backend", "widget", true},

		// "/-/" ends the project path, so a page tail never eats a group.
		{"dash separated tree url", "https://gitlab.com/acme/platform/widget/-/tree/main/src", "acme/platform", "widget", true},
		{"dash separated merge request", "https://gitlab.com/acme/platform/widget/-/merge_requests/12", "acme/platform", "widget", true},
		{"dash separated blob deep in tree", "https://gitlab.com/a/b/c/widget/-/blob/main/app/main.go", "a/b/c", "widget", true},
		{"dash separated on a flat project", "https://gitlab.com/acme/widget/-/issues/3", "acme", "widget", true},

		// Legacy tails without "/-/" are still recognized by name.
		{"legacy tree url", "https://gitlab.com/acme/widget/tree/main", "acme", "widget", true},
		{"legacy merge request", "https://gitlab.com/acme/platform/widget/merge_requests/7", "acme/platform", "widget", true},

		{"no project segment", "https://gitlab.com/acme", "", "", false},
		{"empty path", "https://gitlab.com/", "", "", false},
		{"empty segment", "https://gitlab.com/acme//widget", "", "", false},
		{"bare .git project", "https://gitlab.com/acme/.git", "", "", false},
		{"github host is rejected", "https://github.com/octocat/hello-world.git", "", "", false},
		{"lookalike host is rejected", "https://gitlab.com.evil.test/a/b", "", "", false},
		{"self managed host is rejected by the cloud descriptor", "https://gitlab.example.com/a/b", "", "", false},
		{"bad scheme", "ftp://gitlab.com/a/b.git", "", "", false},
		{"not a url", "not-a-url", "", "", false},
		{"unparseable", "https://gitlab.com/o/\x7f\x00", "", "", false},
		{"empty", "", "", "", false},
	}
	d, _ := Lookup(KindGitLab)
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

// TestGitLabNestedGroupRoundTrip is the regression the interface exists
// for (issue #797): a nested group path must survive parse -> clone URL
// -> re-parse unchanged, over both transports, at every depth. A parser
// that took only the first two segments would collapse the owner here
// and push to the wrong project.
func TestGitLabNestedGroupRoundTrip(t *testing.T) {
	t.Parallel()
	d, _ := Lookup(KindGitLab)
	for _, want := range []RepoRef{
		{Owner: "acme", Name: "widget"},
		{Owner: "acme/platform", Name: "widget"},
		{Owner: "acme/platform/backend", Name: "widget"},
		{Owner: "a/b/c/d/e", Name: "widget"},
	} {
		t.Run(want.FullName(), func(t *testing.T) {
			t.Parallel()
			https := d.HTTPSCloneURL(want)
			if got := "https://gitlab.com/" + want.FullName() + ".git"; https != got {
				t.Fatalf("HTTPSCloneURL = %q, want %q", https, got)
			}
			ssh := d.SSHCloneURL(want)
			if got := "ssh://git@gitlab.com/" + want.FullName() + ".git"; ssh != got {
				t.Fatalf("SSHCloneURL = %q, want %q", ssh, got)
			}
			for _, raw := range []string{https, ssh, "git@gitlab.com:" + want.FullName() + ".git",
				"https://gitlab.com/" + want.FullName(),
				"https://gitlab.com/" + want.FullName() + "/-/merge_requests/1"} {
				ref, ok := d.ParseRepoURL(raw)
				if !ok {
					t.Fatalf("ParseRepoURL(%q) rejected", raw)
				}
				if ref != want {
					t.Fatalf("ParseRepoURL(%q) = %+v, want %+v", raw, ref, want)
				}
			}
		})
	}
}

// A self-managed instance parses and serves URLs on its own host, and
// refuses gitlab.com's: the descriptor is per-connector data, not one
// fixed host.
func TestGitLabSelfManagedHost(t *testing.T) {
	t.Parallel()
	d := NewGitLab("https://git.example.com", nil)
	if got := d.Host(); got != "git.example.com" {
		t.Fatalf("Host() = %q", got)
	}
	if got := d.APIBase(); got != "https://git.example.com/api/v4" {
		t.Fatalf("APIBase() = %q", got)
	}
	ref, ok := d.ParseRepoURL("https://git.example.com/acme/platform/widget.git")
	if !ok || ref.Owner != "acme/platform" || ref.Name != "widget" {
		t.Fatalf("ParseRepoURL = (%+v, %v)", ref, ok)
	}
	if got := d.HTTPSCloneURL(ref); got != "https://git.example.com/acme/platform/widget.git" {
		t.Fatalf("HTTPSCloneURL = %q", got)
	}
	if got := d.SSHCloneURL(ref); got != "ssh://git@git.example.com/acme/platform/widget.git" {
		t.Fatalf("SSHCloneURL = %q", got)
	}
	if _, ok := d.ParseRepoURL("https://gitlab.com/acme/widget.git"); ok {
		t.Fatal("self-managed descriptor accepted a gitlab.com URL")
	}
	if d.Kind() != KindGitLab || d.HTTPUsername() != "oauth2" {
		t.Fatalf("kind/username changed for a self-managed instance: %q %q", d.Kind(), d.HTTPUsername())
	}
}

func TestNewGitLabBaseURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ base, host string }{
		{"", "gitlab.com"},
		{"   ", "gitlab.com"},
		{"https://gitlab.com", "gitlab.com"},
		{"https://GitLab.com/", "gitlab.com"},
		{"gitlab.example.com", "gitlab.example.com"},
		{"https://git.example.com:8443/gitlab", "git.example.com:8443"},
		{"http://git.internal", "git.internal"},
		// Unparseable or hostless falls back rather than pointing at nothing.
		{"://nope", "gitlab.com"},
		{"https://", "gitlab.com"},
	} {
		t.Run(tc.base, func(t *testing.T) {
			t.Parallel()
			if got := NewGitLab(tc.base, nil).Host(); got != tc.host {
				t.Fatalf("NewGitLab(%q).Host() = %q, want %q", tc.base, got, tc.host)
			}
		})
	}
}

// An instance on plain http or a non-default port keeps both in its
// API base and https clone URL, but never in the ssh one: the http
// port says nothing about where sshd listens.
func TestGitLabSchemeAndPort(t *testing.T) {
	t.Parallel()
	ref := RepoRef{Owner: "acme/platform", Name: "widgets"}
	for _, tc := range []struct{ base, api, https, ssh string }{
		{
			base:  "http://git.internal",
			api:   "http://git.internal/api/v4",
			https: "http://git.internal/acme/platform/widgets.git",
			ssh:   "ssh://git@git.internal/acme/platform/widgets.git",
		},
		{
			base:  "https://git.example.com:8443",
			api:   "https://git.example.com:8443/api/v4",
			https: "https://git.example.com:8443/acme/platform/widgets.git",
			ssh:   "ssh://git@git.example.com/acme/platform/widgets.git",
		},
	} {
		t.Run(tc.base, func(t *testing.T) {
			t.Parallel()
			d := NewGitLab(tc.base, nil)
			if got := d.APIBase(); got != tc.api {
				t.Fatalf("APIBase = %q, want %q", got, tc.api)
			}
			if got := d.HTTPSCloneURL(ref); got != tc.https {
				t.Fatalf("HTTPSCloneURL = %q, want %q", got, tc.https)
			}
			if got := d.SSHCloneURL(ref); got != tc.ssh {
				t.Fatalf("SSHCloneURL = %q, want %q", got, tc.ssh)
			}
			// The port is not part of the host the parser matches.
			if _, ok := d.ParseRepoURL(tc.https); !ok {
				t.Fatalf("ParseRepoURL(%q) rejected its own clone URL", tc.https)
			}
		})
	}
}

// gitlab.com over plain http is a self-managed-shaped descriptor, not
// the pinned cloud one: the scheme is honored rather than silently
// upgraded, and it carries no pinned key.
func TestGitLabCloudOverHTTPIsNotTheCloudDescriptor(t *testing.T) {
	t.Parallel()
	d := NewGitLab("http://gitlab.com", nil)
	if got := d.APIBase(); got != "http://gitlab.com/api/v4" {
		t.Fatalf("APIBase = %q", got)
	}
	if got := d.SSHKnownHosts(); len(got) != 0 {
		t.Fatalf("SSHKnownHosts = %v, want none", got)
	}
}

// A base URL naming gitlab.com collapses to the zero value, so the
// cloud instance keeps its pinned host key instead of an empty list.
func TestNewGitLabCloudKeepsPinnedKeys(t *testing.T) {
	t.Parallel()
	d := NewGitLab("https://gitlab.com", []string{"ignored ssh-ed25519 AAAA"})
	if got := d.SSHKnownHosts(); len(got) != 1 || !strings.HasPrefix(got[0], "gitlab.com ssh-ed25519 ") {
		t.Fatalf("SSHKnownHosts() = %v, want the pinned gitlab.com line", got)
	}
}

// A self-managed instance has no key anyone can hardcode, so the
// operator's pasted lines are what it serves, and with none pasted it
// serves none, which is what makes the transport resolver degrade to
// HTTPS rather than trust an unknown host.
func TestGitLabSelfManagedKnownHosts(t *testing.T) {
	t.Parallel()
	line := "git.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAfuCHKVTjquxvt6CM6tdG4SLp1Btn/nOeHHE5UOzRdf"
	d := NewGitLab("https://git.example.com", []string{line})
	got := d.SSHKnownHosts()
	if len(got) != 1 || got[0] != line {
		t.Fatalf("SSHKnownHosts() = %v, want the operator's line", got)
	}
	if got := NewGitLab("https://git.example.com", nil).SSHKnownHosts(); len(got) != 0 {
		t.Fatalf("SSHKnownHosts() = %v, want none for an instance with no pasted key", got)
	}
}
