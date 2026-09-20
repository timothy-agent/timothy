package gitprovider

import (
	"net/url"
	"strings"
)

// GitLab is the descriptor for GitLab. Unlike GitHub and Bitbucket it
// is not a zero-value struct: a self-managed instance serves the same
// API on an operator's own host, so the host and API base come from the
// connector's config (issue #797). The zero value is gitlab.com, which
// is what the registry holds and what IsKind/Kinds report on.
//
// Owner is the full group path and may nest to any depth
// (group/subgroup/subsubgroup); Name is the project slug.
type GitLab struct {
	// host is the git and browser host, lowercase, no scheme, with a
	// non-default port when the instance uses one. Empty means
	// gitlab.com.
	host string
	// scheme is the instance's URL scheme, for an internal instance
	// served over plain http. Empty means https.
	scheme string
	// knownHosts are the operator-pasted known_hosts lines for a
	// self-managed instance. Empty on gitlab.com, which is pinned below.
	knownHosts []string
}

func init() { Register(KindGitLab, GitLab{}) }

// gitlabCloudHost is the host the zero-value descriptor serves.
const gitlabCloudHost = "gitlab.com"

// NewGitLab builds a descriptor for one connector. baseURL is the
// instance root ("https://gitlab.example.com", or empty for
// gitlab.com); knownHosts are the operator's pinned host keys for a
// self-managed instance, ignored on gitlab.com, which ships its own.
// An unparseable baseURL falls back to gitlab.com rather than
// producing a descriptor pointed at nothing.
func NewGitLab(baseURL string, knownHosts []string) GitLab {
	host, scheme := gitlabHost(baseURL)
	if host == gitlabCloudHost && scheme == "https" {
		return GitLab{}
	}
	return GitLab{host: host, scheme: scheme, knownHosts: knownHosts}
}

// gitlabHost extracts the lowercase host (with a non-default port) and
// the scheme from an instance root, tolerating a missing scheme
// ("gitlab.example.com") and a trailing path
// ("https://git.example.com/gitlab": GitLab can be mounted under a
// relative URL root, but the git host is still just the authority).
func gitlabHost(baseURL string) (host, scheme string) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return gitlabCloudHost, "https"
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return gitlabCloudHost, "https"
	}
	scheme = strings.ToLower(u.Scheme)
	if scheme != "http" {
		scheme = "https"
	}
	return strings.ToLower(u.Host), scheme
}

func (GitLab) Kind() Kind { return KindGitLab }

// Host is the instance's git host, gitlab.com for the zero value.
func (g GitLab) Host() string {
	if g.host == "" {
		return gitlabCloudHost
	}
	return g.host
}

// Scheme is the instance's URL scheme, https unless an operator
// pointed base_url at a plain-http internal instance.
func (g GitLab) Scheme() string {
	if g.scheme == "" {
		return "https"
	}
	return g.scheme
}

// APIBase is the GitLab API v4 root for this instance. Self-managed
// instances answer on their own host, so nothing here is fixed.
func (g GitLab) APIBase() string { return g.Scheme() + "://" + g.Host() + "/api/v4" }

// Hosts is the hostname without a port: the ForHost index and
// ParseRepoURL both compare against url.Hostname().
func (g GitLab) Hosts() []string {
	host := g.Host()
	if h, _, found := strings.Cut(host, ":"); found {
		host = h
	}
	return []string{host}
}

// gitlabPagePaths are the browser-URL tails that can follow a project
// path. GitLab separates project-scoped pages from the project path
// with "/-/", which parseGitLabPath uses as the authoritative end of
// the nested group path; these remain for the handful of legacy URLs
// that omit it.
var gitlabPagePaths = []string{"tree", "blob", "merge_requests", "commits", "commit", "issues"}

// ParseRepoURL reads a nested group path off a GitLab URL. The shared
// parseRepoURL cannot serve this: it takes segs[0] as the owner and
// segs[1] as the name, which silently truncates group/subgroup/project
// to group/subgroup. Everything before the last path segment (or before
// "/-/") is the owner here, at any depth.
func (g GitLab) ParseRepoURL(raw string) (RepoRef, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RepoRef{}, false
	}
	if ssh, ok := NormalizeSCP(raw); ok {
		raw = ssh
	}
	u, err := url.Parse(raw)
	if err != nil {
		return RepoRef{}, false
	}
	switch u.Scheme {
	case "https", "http", "ssh":
	default:
		return RepoRef{}, false
	}
	if !hostIn(u.Hostname(), g.Hosts()) {
		return RepoRef{}, false
	}
	return parseGitLabPath(u.Path)
}

// parseGitLabPath splits a GitLab project path into its group path and
// project slug. "/-/" ends the project path where GitLab puts it; with
// no such marker the last segment is the project and a page tail is
// accepted only from gitlabPagePaths, so a settings URL is still
// rejected the way the other descriptors reject theirs.
func parseGitLabPath(path string) (RepoRef, bool) {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return RepoRef{}, false
	}
	if before, _, found := strings.Cut(trimmed, "/-/"); found {
		trimmed = before
	}
	segs := strings.Split(trimmed, "/")
	for _, s := range segs {
		if s == "" {
			return RepoRef{}, false
		}
	}
	// A page tail can only be told from a deeper group by its name, and
	// only when the URL omitted "/-/": cut at the first one seen.
	for i := 1; i < len(segs); i++ {
		if pageIn(segs[i], gitlabPagePaths) {
			segs = segs[:i]
			break
		}
	}
	if len(segs) < 2 {
		return RepoRef{}, false
	}
	name := strings.TrimSuffix(segs[len(segs)-1], ".git")
	if name == "" {
		return RepoRef{}, false
	}
	return RepoRef{Owner: strings.Join(segs[:len(segs)-1], "/"), Name: name}, true
}

func (g GitLab) HTTPSCloneURL(r RepoRef) string {
	return g.Scheme() + "://" + g.Host() + "/" + r.Owner + "/" + r.Name + ".git"
}

// SSHCloneURL uses the bare hostname: an instance's http port says
// nothing about where its ssh daemon listens.
func (g GitLab) SSHCloneURL(r RepoRef) string {
	return "ssh://git@" + g.Hosts()[0] + "/" + r.Owner + "/" + r.Name + ".git"
}

// HTTPUsername is GitLab's own credential username for a personal or
// project access token over https, which is neither GitHub's
// x-access-token nor Bitbucket's x-token-auth.
func (GitLab) HTTPUsername() string { return "oauth2" }

// gitlabKnownHosts pins gitlab.com's ed25519 host key, verbatim from
// GitLab's own published set (https://docs.gitlab.com/user/gitlab_com/#ssh-host-keys-fingerprints,
// fingerprint SHA256:eUXGGm1YGsMAS7vkcx6JOJdOGHPem5gQp4taiCfCLB8). See
// githubKnownHosts for why this is pinned rather than fetched, and why
// only ed25519 is listed.
var gitlabKnownHosts = []string{
	"gitlab.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAfuCHKVTjquxvt6CM6tdG4SLp1Btn/nOeHHE5UOzRdf",
}

// SSHKnownHosts serves the pinned cloud key, or the operator's pasted
// lines for a self-managed instance whose host key nobody can hardcode.
func (g GitLab) SSHKnownHosts() []string {
	if g.host == "" {
		return gitlabKnownHosts
	}
	return g.knownHosts
}

func (GitLab) Supports(c Capability) bool {
	switch c {
	// GitLab verifies SSH-signed commits from an uploaded signing key
	// since 15.7, and its groups nest to any depth.
	case CapCreateRepo, CapSSHSigningVerify, CapSSHTransport, CapNestedOwner:
		return true
	default:
		return false
	}
}
