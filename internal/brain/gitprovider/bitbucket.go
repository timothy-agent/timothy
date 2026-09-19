package gitprovider

// Bitbucket is the descriptor for Bitbucket Cloud. Owner is the
// workspace slug and Name the repo slug.
type Bitbucket struct{}

func init() { Register(KindBitbucket, Bitbucket{}) }

func (Bitbucket) Kind() Kind { return KindBitbucket }

func (Bitbucket) Hosts() []string { return []string{"bitbucket.org"} }

// bitbucketPagePaths are the browser-URL tails Bitbucket's own links
// carry after workspace/slug; any other tail is not a repo URL.
var bitbucketPagePaths = []string{"src", "branch", "pull-requests", "commits"}

func (b Bitbucket) ParseRepoURL(raw string) (RepoRef, bool) {
	return parseRepoURL(raw, b.Hosts(), bitbucketPagePaths)
}

func (Bitbucket) HTTPSCloneURL(r RepoRef) string {
	return "https://bitbucket.org/" + r.Owner + "/" + r.Name + ".git"
}

func (Bitbucket) SSHCloneURL(r RepoRef) string {
	return "ssh://git@bitbucket.org/" + r.Owner + "/" + r.Name + ".git"
}

func (Bitbucket) HTTPUsername() string { return "x-token-auth" }

// bitbucketKnownHosts pins bitbucket.org's ed25519 host key, verbatim
// from Atlassian's own published set (https://bitbucket.org/site/ssh,
// fingerprint SHA256:ybgmFkzwOSotHTHLJgHO0QN8L0xErw6vd0VhFA9m3SM in
// the "Configure SSH and two-step verification" doc). See
// githubKnownHosts for why this is pinned rather than fetched, and why
// only ed25519 is listed.
var bitbucketKnownHosts = []string{
	"bitbucket.org ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIazEu89wgQZ4bqs3d63QSMzYVa0MuJ2e2gKTKqu+UUO",
}

func (Bitbucket) SSHKnownHosts() []string { return bitbucketKnownHosts }

func (Bitbucket) Supports(c Capability) bool {
	switch c {
	case CapCreateRepo, CapSSHTransport:
		return true
	default:
		return false
	}
}
