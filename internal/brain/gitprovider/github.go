package gitprovider

// GitHub is the descriptor for github.com. There is no Enterprise
// support: the API base is fixed, so the host list is the single cloud
// host, and a URL on any other host is not a github repo URL.
type GitHub struct{}

func init() { Register(KindGitHub, GitHub{}) }

func (GitHub) Kind() Kind { return KindGitHub }

func (GitHub) Hosts() []string { return []string{"github.com"} }

// githubPagePaths are the browser-URL tails a pasted github URL may
// carry after owner/repo.
var githubPagePaths = []string{"tree", "blob", "pull", "commits", "commit"}

func (g GitHub) ParseRepoURL(raw string) (RepoRef, bool) {
	return parseRepoURL(raw, g.Hosts(), githubPagePaths)
}

func (GitHub) HTTPSCloneURL(r RepoRef) string {
	return "https://github.com/" + r.Owner + "/" + r.Name + ".git"
}

func (GitHub) SSHCloneURL(r RepoRef) string {
	return "ssh://git@github.com/" + r.Owner + "/" + r.Name + ".git"
}

func (GitHub) HTTPUsername() string { return "x-access-token" }

// githubKnownHosts pins github.com's ed25519 host key, verbatim from
// GitHub's own published set (https://api.github.com/meta's ssh_keys,
// fingerprint SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU in
// the SSH key fingerprints doc). Pinned at build time, never fetched
// at runtime: a key fetched over the same network the connection uses
// verifies nothing. Only ed25519 is pinned — offering the RSA and
// ECDSA keys too would let a downgrade pick the weakest of the three.
var githubKnownHosts = []string{
	"github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl",
}

func (GitHub) SSHKnownHosts() []string { return githubKnownHosts }

func (GitHub) Supports(c Capability) bool {
	switch c {
	case CapCreateRepo, CapSSHSigningVerify, CapSSHTransport:
		return true
	default:
		return false
	}
}
