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

// SSHKnownHosts: see GitHub.SSHKnownHosts.
func (Bitbucket) SSHKnownHosts() []string { return nil }

func (Bitbucket) Supports(c Capability) bool {
	switch c {
	case CapCreateRepo:
		return true
	default:
		return false
	}
}
