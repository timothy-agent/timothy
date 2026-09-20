// Package gitprovider holds the provider-agnostic knowledge about a git
// host: URL parsing, clone URL synthesis, the credential username token
// auth expects, and what the host can do. D-100: a leaf package with no
// imports from connectors, destinations or missions, so every layer above
// can name a kind without a string literal and a new host (GitLab, Gitea)
// arrives as one Descriptor instead of scattered `kind == "github"` checks.
package gitprovider

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Kind names a git-hosting provider; the same string the connectors and
// destinations rows store in their kind column.
type Kind string

const (
	KindGitHub    Kind = "github"
	KindBitbucket Kind = "bitbucket"
	KindGitLab    Kind = "gitlab"
)

// RepoRef identifies one repository on a provider. Owner is GitHub's
// owner, Bitbucket's workspace, and may hold a multi-segment path
// (a GitLab group/subgroup), so nothing here restricts it to one segment.
type RepoRef struct {
	Owner string
	Name  string
}

// FullName renders the owner/name form both APIs use.
func (r RepoRef) FullName() string { return r.Owner + "/" + r.Name }

// Repo is the provider-agnostic shape of one repository: the fields the
// repo picker, the create-new flow and the create-if-missing delivery
// path need, nothing more.
type Repo struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	PushedAt      string `json:"pushed_at"`
}

// PullRequest is the provider-agnostic shape of one pull request: enough
// for the PR response and to report an already-open PR for the same head.
type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
}

// Identity is who a connector's credential authenticates as, and what it
// can commit as.
type Identity struct {
	Login  string `json:"login"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Scopes string `json:"scopes"`
}

// PRSpec is one pull request to open: the request side of PullRequest.
type PRSpec struct {
	Repo  RepoRef
	Title string
	Head  string
	Base  string
	Body  string
}

// Capability names an optional provider feature. Whoever adds a
// Descriptor declares what it supports; unknown means false.
type Capability int

const (
	// CapCreateRepo: the provider API can create a repository, which the
	// create-if-missing delivery path needs.
	CapCreateRepo Capability = iota
	// CapSSHSigningVerify: the provider verifies SSH-signed commits from
	// an uploaded signing key.
	CapSSHSigningVerify
	// CapSSHTransport: clone and push over ssh:// are supported.
	CapSSHTransport
	// CapNestedOwner: RepoRef.Owner may be a multi-segment path (a
	// GitLab group/subgroup), not a single owner or workspace segment.
	CapNestedOwner
)

// Descriptor is everything static and credential-free about one provider
// kind. Implementations hold no tokens and make no network calls.
type Descriptor interface {
	Kind() Kind
	// Hosts lists the cloud hostnames this kind serves, lowercase.
	Hosts() []string
	// ParseRepoURL extracts the repo a URL points at, accepting https
	// clone URLs, browser URLs, the user@ clone-button form, and
	// scp-form ssh (normalized, not rejected). ok is false for any other
	// shape or another provider's host.
	ParseRepoURL(raw string) (RepoRef, bool)
	HTTPSCloneURL(RepoRef) string
	SSHCloneURL(RepoRef) string
	// HTTPUsername is the username git's credential helper must send
	// alongside a token for this provider.
	HTTPUsername() string
	// SSHKnownHosts returns the known_hosts lines for the cloud hosts.
	SSHKnownHosts() []string
	Supports(Capability) bool
}

// ErrRepoNotFound marks GetRepo's 404: the repo does not exist, or the
// credential cannot see it. The create-if-missing delivery path's
// signal to create it rather than treat the lookup as a hard failure
// (issue #483). Distinct from connectors.ErrNotFound, which names a
// missing connectors table row, not a missing repository.
var ErrRepoNotFound = errors.New("gitprovider: repo not found")

// Client is one connector's credentialed view of its provider: the
// Descriptor's static knowledge plus every call that needs a token.
// One interface for both kinds (D-101, issue #795), so destinations and
// the chat tool builder hold a Client instead of a per-kind adapter and
// a new provider arrives as one implementation.
//
// The three PR read methods return rendered text, not structs: what
// each provider can report differs (GitHub carries merged/
// mergeable_state and line counts on the pull request object,
// Bitbucket computes counts from a paged diffstat), so a common struct
// would either lose detail or carry fields half the providers leave
// empty. The chat tools are text tools, so rendering is the contract.
type Client interface {
	Descriptor

	// Identity reports who the credential authenticates as.
	Identity(ctx context.Context) (Identity, error)
	// ListRepos lists every repo the credential can see, most recently
	// pushed first, bounded by the implementation.
	ListRepos(ctx context.Context) ([]Repo, error)
	// GetRepo resolves one repo's metadata. A confirmed 404 wraps
	// ErrRepoNotFound; any other failure is a plain error.
	GetRepo(ctx context.Context, ref RepoRef) (Repo, error)
	// CreateRepo creates a repo. name is provider-shaped: a bare name on
	// GitHub, owner/name (or a bare slug against a configured
	// workspace) on Bitbucket.
	CreateRepo(ctx context.Context, name string, private bool) (Repo, error)
	// CreatePR opens a pull request, or returns the open one already
	// serving spec.Head when the provider refuses a duplicate.
	CreatePR(ctx context.Context, spec PRSpec) (PullRequest, error)
	// GetPR reads one pull request's provider-agnostic state.
	GetPR(ctx context.Context, ref RepoRef, number int) (PullRequest, error)

	// ListPRs renders the repo's pull requests, most recently updated
	// first. state is open, closed or all; max bounds the result.
	ListPRs(ctx context.Context, ref RepoRef, state string, max int) (string, error)
	// PRDescription renders one pull request's metadata and body.
	PRDescription(ctx context.Context, ref RepoRef, number int) (string, error)
	// PRDiff renders the pull request's unified diff, truncated with an
	// explicit marker rather than silently.
	PRDiff(ctx context.Context, ref RepoRef, number int) (string, error)
	// PRComments renders the discussion in chronological order.
	PRComments(ctx context.Context, ref RepoRef, number int) (string, error)
}

var (
	registryMu sync.RWMutex
	registry   = map[Kind]Descriptor{}
	byHost     = map[string]Descriptor{}
)

// Register adds a Descriptor under its kind, replacing any earlier one.
// Called from package init; safe to call concurrently.
func Register(k Kind, d Descriptor) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[k] = d
	for _, h := range d.Hosts() {
		byHost[strings.ToLower(h)] = d
	}
}

// Lookup returns the Descriptor registered for k.
func Lookup(k Kind) (Descriptor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	d, ok := registry[k]
	return d, ok
}

// IsKind reports whether kind names a registered git provider: the
// replacement for the `kind == "github" || kind == "bitbucket"` literals
// spread across connectors, destinations, missions and api.
func IsKind(kind string) bool {
	_, ok := Lookup(Kind(kind))
	return ok
}

// Kinds lists the registered kinds, sorted, for error messages and tests.
func Kinds() []Kind {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Kind, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ForHost resolves a cloud hostname to its Descriptor. Transitional and
// deliberately narrow: only for a URL that arrives before a connector is
// known (validating a pasted repo_url). It must never decide the
// Descriptor on a push, clone or PR path — a self-hosted GitLab or Gitea
// cannot be recognized from its hostname, so those paths resolve the
// Descriptor from the connector's kind instead.
func ForHost(host string) (Descriptor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	d, ok := byHost[strings.ToLower(strings.TrimSpace(host))]
	return d, ok
}
