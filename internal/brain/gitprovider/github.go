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

// SSHKnownHosts is empty until the ssh transport lands: pinning a host
// key we cannot verify here would be worse than pinning none, and no
// caller reads it yet.
func (GitHub) SSHKnownHosts() []string { return nil }

func (GitHub) Supports(c Capability) bool {
	switch c {
	case CapCreateRepo, CapSSHSigningVerify:
		return true
	default:
		return false
	}
}
