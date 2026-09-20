package destinations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// fakeEvents records every AppendEvent call for assertion, standing in
// for *missions.Store without a real Postgres pool.
type fakeEvents struct {
	mu     sync.Mutex
	events []missions.Event
	err    error
	// payloads parallels events: mission.transport_fallback's own
	// {from, to, reason} is the assertion (issue #796), and
	// missions.Event carries the payload as raw JSON.
	payloads []map[string]any
}

func (f *fakeEvents) AppendEvent(_ context.Context, id, kind string, payload map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, missions.Event{MissionID: id, Kind: kind})
	f.payloads = append(f.payloads, payload)
	return nil
}

// countOf reports how many events of kind were recorded.
func (f *fakeEvents) countOf(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// find returns the first payload recorded for kind.
func (f *fakeEvents) find(kind string) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, e := range f.events {
		if e.Kind == kind {
			return f.payloads[i], true
		}
	}
	return nil, false
}

// fakeGitClient scripts gitprovider.Client for the adapter tests
// without a real connector. The embedded Descriptor is the real one,
// so URL parsing and clone-URL synthesis are exercised for real; only
// the credentialed calls are faked.
//
// repoExists/existsErr/newCloneURL/createRepoErr back ensureRepo's
// create-if-missing path (issue #483); existsCalls/createRepoCalls
// count those specifically, distinct from createCalls (CreatePR).
type fakeGitClient struct {
	gitprovider.Descriptor

	defaultBranch string
	defaultErr    error
	prURL         string
	prNumber      int
	createErr     error
	createCalls   int
	lastTitle     string
	lastPRRepo    gitprovider.RepoRef

	repoExists      bool
	existsErr       error
	existsCalls     int
	newCloneURL     string
	createRepoErr   error
	createRepoCalls int
	lastCreateRepo  string
	// bitbucketNames models bitbucketSource's name contract so a
	// permissive fake can't hide issue #786: a bare slug resolves
	// only when the connector has a workspace configured.
	bitbucketNames bool
	workspace      string
}

// githubClient/bitbucketClient build a fake over the real Descriptor
// for that kind, so a test states only what the credentialed calls do.
func githubClient(f *fakeGitClient) *fakeGitClient {
	f.Descriptor = gitprovider.GitHub{}
	return f
}

func bitbucketClient(f *fakeGitClient) *fakeGitClient {
	f.Descriptor = gitprovider.Bitbucket{}
	return f
}

func gitlabClient(f *fakeGitClient) *fakeGitClient {
	f.Descriptor = gitprovider.GitLab{}
	return f
}

func (f *fakeGitClient) Identity(context.Context) (gitprovider.Identity, error) {
	return gitprovider.Identity{}, nil
}

func (f *fakeGitClient) ListRepos(context.Context) ([]gitprovider.Repo, error) { return nil, nil }

func (f *fakeGitClient) GetRepo(_ context.Context, _ gitprovider.RepoRef) (gitprovider.Repo, error) {
	f.existsCalls++
	if f.existsErr != nil {
		return gitprovider.Repo{}, f.existsErr
	}
	if !f.repoExists {
		return gitprovider.Repo{}, fmt.Errorf("get repo: %w", gitprovider.ErrRepoNotFound)
	}
	return gitprovider.Repo{DefaultBranch: f.defaultBranch}, f.defaultErr
}

func (f *fakeGitClient) CreateRepo(_ context.Context, name string, _ bool) (gitprovider.Repo, error) {
	f.createRepoCalls++
	f.lastCreateRepo = name
	if f.bitbucketNames && !strings.Contains(name, "/") && f.workspace == "" {
		return gitprovider.Repo{}, fmt.Errorf("create repo: bitbucket needs a workspace/slug name, got %q", name)
	}
	if f.createRepoErr != nil {
		return gitprovider.Repo{}, f.createRepoErr
	}
	return gitprovider.Repo{CloneURL: f.newCloneURL}, nil
}

func (f *fakeGitClient) CreatePR(_ context.Context, spec gitprovider.PRSpec) (gitprovider.PullRequest, error) {
	f.createCalls++
	f.lastTitle = spec.Title
	f.lastPRRepo = spec.Repo
	if f.createErr != nil {
		return gitprovider.PullRequest{}, f.createErr
	}
	return gitprovider.PullRequest{Number: f.prNumber, HTMLURL: f.prURL}, nil
}

func (f *fakeGitClient) GetPR(context.Context, gitprovider.RepoRef, int) (gitprovider.PullRequest, error) {
	return gitprovider.PullRequest{}, nil
}

func (f *fakeGitClient) ListPRs(context.Context, gitprovider.RepoRef, string, int) (string, error) {
	return "", nil
}

func (f *fakeGitClient) PRDescription(context.Context, gitprovider.RepoRef, int) (string, error) {
	return "", nil
}

func (f *fakeGitClient) PRDiff(context.Context, gitprovider.RepoRef, int) (string, error) {
	return "", nil
}

func (f *fakeGitClient) PRComments(context.Context, gitprovider.RepoRef, int) (string, error) {
	return "", nil
}

// staticClients hands the same Client to every connector id.
type staticClients struct {
	c   gitprovider.Client
	err error
}

func (s staticClients) ClientFor(context.Context, string) (gitprovider.Client, func(), error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	return s.c, func() {}, nil
}

// clients wraps a fake Client as the adapter's Clients dependency.
func clients(c gitprovider.Client) Clients { return staticClients{c: c} }

// fakePusher scripts pusher, the https-only remote validation
// Workspace.Push itself enforces is missions' own push_test.go
// coverage; RepoAdapter's job (proven here) is what it does with a
// push that succeeds or fails, not git plumbing.
type fakePusher struct {
	host      string
	err       error
	pushCalls int

	setOriginCalls int
	setOriginErr   error
	lastOriginURL  string
	// lastAuth records what transport the adapter resolved for the push
	// (issue #796).
	lastAuth missions.RemoteAuth
}

func (f *fakePusher) Push(_ context.Context, _, _ string, auth missions.RemoteAuth) (string, error) {
	f.pushCalls++
	f.lastAuth = auth
	if f.err != nil {
		return "", f.err
	}
	return f.host, nil
}

func (f *fakePusher) SetOrigin(_ context.Context, _, remoteURL string) error {
	f.setOriginCalls++
	f.lastOriginURL = remoteURL
	return f.setOriginErr
}

// pushableMission is a coding mission with the guards missions.NotPushable
// requires already satisfied (worktree present, branch set); t.TempDir()
// stands in for a real workspace since fakePusher never touches the
// filesystem; WorktreePath() derives workspace/wt from it.
func pushableMission(t *testing.T) missions.Mission {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/wt", 0o750); err != nil {
		t.Fatal(err)
	}
	return missions.Mission{
		ID: "m1", Kind: "coding", Workspace: dir, Branch: "mission/x",
		Sources: []missions.SourceEntry{{Source: missions.SourceKindGitHub, RepoURL: "https://github.com/octo/repo.git", ConnectorID: "conn1"}},
	}
}

func bitbucketMission(t *testing.T) missions.Mission {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/wt", 0o750); err != nil {
		t.Fatal(err)
	}
	return missions.Mission{
		ID: "m1", Kind: "coding", Workspace: dir, Branch: "mission/x",
		Sources: []missions.SourceEntry{{Source: missions.SourceKindBitbucket, RepoURL: "https://bitbucket.org/acme/widgets.git", ConnectorID: "bb1"}},
	}
}

// gitlabMission is a mission on a NESTED GitLab group path, the shape
// the two-segment assumptions inherited from github/bitbucket collapse.
func gitlabMission(t *testing.T, repoURL string) missions.Mission {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/wt", 0o750); err != nil {
		t.Fatal(err)
	}
	return missions.Mission{
		ID: "m1", Kind: "coding", Workspace: dir, Branch: "mission/x",
		Sources: []missions.SourceEntry{{Source: missions.SourceKindGitLab, RepoURL: repoURL, ConnectorID: "gl1"}},
	}
}

// TestGitLabNestedGroupRoundTrip is issue #797's regression gate: a
// nested group path must survive the whole delivery path (clone URL ->
// push remote -> merge request) with its full depth intact. A parser
// that kept only the first two segments would push and open the merge
// request against "acme/platform" instead of the real project, which is
// a wrong-repo write, not a visible failure.
func TestGitLabNestedGroupRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, repoURL, owner, repo string
	}{
		{"flat group", "https://gitlab.com/acme/widgets.git", "acme", "widgets"},
		{"one subgroup", "https://gitlab.com/acme/platform/widgets.git", "acme/platform", "widgets"},
		{"two subgroups", "https://gitlab.com/acme/platform/backend/widgets.git", "acme/platform/backend", "widgets"},
		{"four subgroups", "https://gitlab.com/a/b/c/d/widgets.git", "a/b/c/d", "widgets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := gitlabMission(t, tc.repoURL)
			p := &fakePusher{host: "gitlab.com"}
			c := gitlabClient(&fakeGitClient{
				repoExists: true, defaultBranch: "main",
				prURL: "https://gitlab.com/" + tc.owner + "/" + tc.repo + "/-/merge_requests/7", prNumber: 7,
			})
			a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}, Clients: clients(c)}

			if _, err := a.PushBranch(t.Context(), m, "tok"); err != nil {
				t.Fatalf("PushBranch: %v", err)
			}
			// GitLab's own credential username, not github's or bitbucket's.
			if p.lastAuth.HTTPUsername != "oauth2" {
				t.Fatalf("credential username = %q, want oauth2", p.lastAuth.HTTPUsername)
			}

			url, number, err := a.OpenPR(t.Context(), m, "tok")
			if err != nil {
				t.Fatalf("OpenPR: %v", err)
			}
			if number != 7 || url == "" {
				t.Fatalf("OpenPR = (%q, %d)", url, number)
			}
			if got := c.lastPRRepo; got.Owner != tc.owner || got.Name != tc.repo {
				t.Fatalf("merge request opened against %q/%q, want %q/%q", got.Owner, got.Name, tc.owner, tc.repo)
			}
			if got := c.lastPRRepo.FullName(); got != tc.owner+"/"+tc.repo {
				t.Fatalf("FullName() = %q", got)
			}
		})
	}
}

// TestEnsureRepoPointsOriginAtAnExistingRepo is issue #795's
// behavioral fix, and the most load-bearing test in this package.
//
// Before unification, GitHubAdapter.ensureRepo returned early the
// moment RepoExists said yes, on a comment that claimed "the worktree's
// origin already points at it". That claim is false in exactly the case
// below: a SCRATCH mission (never cloned, so the worktree has no origin
// at all) delivering to an explicitly named repo that already exists.
// The github path then never called SetOrigin, and the push that
// followed had no remote to push to. BitbucketAdapter.ensureRepo had it
// right, and the unified RepoAdapter adopts that behavior for every
// kind.
//
// The test is written so the OLD github behavior fails it: it asserts
// SetOrigin was called with the target repo's canonical clone URL, and
// that the push then proceeds.
func TestEnsureRepoPointsOriginAtAnExistingRepo(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// kind picks the Descriptor, proving the fix holds for both.
		client  *fakeGitClient
		repoURL string
		// wantOriginURL is the canonical https clone URL origin must end
		// up pointing at.
		wantOriginURL string
		// wantUpdated is true when the stored repo_url itself changed
		// (a browser URL canonicalised), false when it was already
		// canonical and only origin needed pointing.
		wantUpdated bool
	}{
		{
			name:          "github, scratch mission, explicit existing repo",
			client:        githubClient(&fakeGitClient{repoExists: true, defaultBranch: "main"}),
			repoURL:       "https://github.com/octo/target.git",
			wantOriginURL: "https://github.com/octo/target.git",
		},
		{
			name:          "github, browser url is canonicalised",
			client:        githubClient(&fakeGitClient{repoExists: true, defaultBranch: "main"}),
			repoURL:       "https://github.com/octo/target",
			wantOriginURL: "https://github.com/octo/target.git",
			wantUpdated:   true,
		},
		{
			name:          "bitbucket keeps the behavior it already had",
			client:        bitbucketClient(&fakeGitClient{repoExists: true, defaultBranch: "main"}),
			repoURL:       "https://bitbucket.org/acme/widgets.git",
			wantOriginURL: "https://bitbucket.org/acme/widgets.git",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// A scratch mission: no Sources at all, so the worktree was
			// never cloned and has no origin set.
			m := pushableMission(t)
			m.Sources = nil
			p := &fakePusher{host: "example.com"}
			a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}, Clients: clients(tc.client)}

			got, updated, err := a.ensureRepo(t.Context(), tc.client, m, tc.repoURL, false)
			if err != nil {
				t.Fatalf("ensureRepo: %v", err)
			}
			if p.setOriginCalls != 1 {
				t.Fatalf("SetOrigin called %d times, want exactly 1: a scratch mission has no origin, so an existing target must still be pointed at (issue #795)", p.setOriginCalls)
			}
			if p.lastOriginURL != tc.wantOriginURL {
				t.Fatalf("origin set to %q, want %q", p.lastOriginURL, tc.wantOriginURL)
			}
			if got != tc.wantOriginURL {
				t.Fatalf("final repoURL = %q, want %q", got, tc.wantOriginURL)
			}
			if updated != tc.wantUpdated {
				t.Fatalf("updated = %v, want %v", updated, tc.wantUpdated)
			}
			if tc.client.createRepoCalls != 0 {
				t.Fatalf("CreateRepo called %d times, want 0: the repo already exists", tc.client.createRepoCalls)
			}

			// ... and the push that follows now has a remote to reach.
			resolveToken := func(context.Context, string) (string, error) { return "tok", nil }
			a.ResolveToken = resolveToken
			e := &missions.DestinationEntry{RepoURL: tc.repoURL}
			if err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "c1", Mode: "push"}, m, e); err != nil {
				t.Fatalf("DeliverMission after ensureRepo: %v", err)
			}
			if p.pushCalls != 1 {
				t.Fatalf("Push called %d times, want 1", p.pushCalls)
			}
			if e.RepoURL != tc.wantOriginURL {
				t.Fatalf("entry repo_url = %q, want %q", e.RepoURL, tc.wantOriginURL)
			}
		})
	}
}

// TestEnsureRepo table-tests the create-if-missing decision (issue
// #483) across both kinds: repo already exists, repo missing with
// create_if_missing=true, repo missing with create_if_missing=false,
// the retry-after-already-created idempotency path, and the URL and
// name shapes each provider reports.
func TestEnsureRepo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// repoURL is empty for the "derive a name from the goal" cases.
		repoURL         string
		createIfMissing bool
		client          *fakeGitClient

		wantErr          string
		wantUpdated      bool
		wantCreateCalls  int
		wantSetOrigin    int
		wantFinalRepoURL string
		wantCreateName   string
	}{
		{
			name:             "github: repo exists, origin repointed",
			repoURL:          "https://github.com/octo/repo.git",
			createIfMissing:  true,
			client:           githubClient(&fakeGitClient{repoExists: true}),
			wantSetOrigin:    1,
			wantFinalRepoURL: "https://github.com/octo/repo.git",
		},
		{
			name:             "github: repo missing, create_if_missing=true creates and redirects origin",
			repoURL:          "https://github.com/octo/repo.git",
			createIfMissing:  true,
			client:           githubClient(&fakeGitClient{newCloneURL: "https://github.com/octo/repo.git"}),
			wantUpdated:      true,
			wantCreateCalls:  1,
			wantSetOrigin:    1,
			wantFinalRepoURL: "https://github.com/octo/repo.git",
			wantCreateName:   "octo/repo",
		},
		{
			name:            "github: repo missing, create_if_missing=false fails honestly and never creates",
			repoURL:         "https://github.com/octo/repo.git",
			createIfMissing: false,
			client:          githubClient(&fakeGitClient{}),
			wantErr:         "does not exist and create_if_missing is not set",
		},
		{
			name:             "github: retry after already created is idempotent",
			repoURL:          "https://github.com/octo/repo.git",
			createIfMissing:  true,
			client:           githubClient(&fakeGitClient{repoExists: true}), // a prior attempt created it
			wantSetOrigin:    1,
			wantFinalRepoURL: "https://github.com/octo/repo.git",
		},
		{
			name:             "github: no repo_url derives a name from the goal",
			createIfMissing:  true,
			client:           githubClient(&fakeGitClient{newCloneURL: "https://github.com/octo/fix-the-thing.git"}),
			wantUpdated:      true,
			wantCreateCalls:  1,
			wantSetOrigin:    1,
			wantFinalRepoURL: "https://github.com/octo/fix-the-thing.git",
			wantCreateName:   "fix-the-thing",
		},
		{
			name:             "neither repo_url nor create_if_missing: no-op",
			client:           githubClient(&fakeGitClient{}),
			wantFinalRepoURL: "",
		},
		{
			name:            "github: existence check failure is hard, never read as absent",
			repoURL:         "https://github.com/octo/repo.git",
			createIfMissing: true,
			client:          githubClient(&fakeGitClient{existsErr: errors.New("boom")}),
			wantErr:         "check existence: boom",
		},
		{
			name:             "bitbucket: exists, browser url canonicalised",
			repoURL:          "https://bitbucket.org/acme/widgets/src/main/",
			client:           bitbucketClient(&fakeGitClient{repoExists: true}),
			wantUpdated:      true,
			wantSetOrigin:    1,
			wantFinalRepoURL: "https://bitbucket.org/acme/widgets.git",
		},
		{
			name:             "bitbucket: missing, create takes the workspace/slug name",
			repoURL:          "https://bitbucket.org/acme/widgets.git",
			createIfMissing:  true,
			client:           bitbucketClient(&fakeGitClient{newCloneURL: "https://bitbucket.org/acme/widgets-2.git"}),
			wantUpdated:      true,
			wantCreateCalls:  1,
			wantSetOrigin:    1,
			wantFinalRepoURL: "https://bitbucket.org/acme/widgets-2.git",
			wantCreateName:   "acme/widgets",
		},
		// Bitbucket hands back the user@ form for a user principal, which
		// validateRemote rejects, so it must be canonicalised before it
		// reaches SetOrigin (issue #787).
		{
			name:             "bitbucket: created repo returns the user@ clone form",
			createIfMissing:  true,
			client:           bitbucketClient(&fakeGitClient{newCloneURL: "https://sumon@bitbucket.org/acme/fix-the-thing.git", workspace: "acme"}),
			wantUpdated:      true,
			wantCreateCalls:  1,
			wantSetOrigin:    1,
			wantFinalRepoURL: "https://bitbucket.org/acme/fix-the-thing.git",
		},
		// bitbucketNames holds the fake to bitbucketSource's real name
		// contract: a bare mission slug resolves against the connector's
		// configured workspace, and fails loudly without one (issue #786).
		{
			name:            "bitbucket: no url, create from goal, connector has no workspace",
			createIfMissing: true,
			client:          bitbucketClient(&fakeGitClient{bitbucketNames: true, newCloneURL: "https://bitbucket.org/acme/fix-the-thing.git"}),
			wantErr:         "needs a workspace/slug name",
			wantCreateCalls: 1,
		},
		{
			name:    "bitbucket: a github url on a bitbucket destination is rejected",
			repoURL: "https://github.com/acme/widgets.git",
			client:  bitbucketClient(&fakeGitClient{}),
			wantErr: "bitbucket https clone URL",
		},
		{
			name:    "github: a bitbucket url on a github destination is rejected",
			repoURL: "https://bitbucket.org/acme/widgets.git",
			client:  githubClient(&fakeGitClient{}),
			wantErr: "github https clone URL",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := pushableMission(t)
			m.Goal = "fix the thing"
			p := &fakePusher{}
			a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}, Clients: clients(tc.client)}

			got, updated, err := a.ensureRepo(t.Context(), tc.client, m, tc.repoURL, tc.createIfMissing)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				if tc.client.createRepoCalls != tc.wantCreateCalls {
					t.Fatalf("CreateRepo called %d times, want %d", tc.client.createRepoCalls, tc.wantCreateCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensureRepo: %v", err)
			}
			if got != tc.wantFinalRepoURL {
				t.Fatalf("final repoURL = %q, want %q", got, tc.wantFinalRepoURL)
			}
			if updated != tc.wantUpdated {
				t.Fatalf("updated = %v, want %v", updated, tc.wantUpdated)
			}
			if tc.client.createRepoCalls != tc.wantCreateCalls {
				t.Fatalf("CreateRepo called %d times, want %d", tc.client.createRepoCalls, tc.wantCreateCalls)
			}
			if p.setOriginCalls != tc.wantSetOrigin {
				t.Fatalf("SetOrigin called %d times, want %d", p.setOriginCalls, tc.wantSetOrigin)
			}
			if tc.wantCreateName != "" && tc.client.lastCreateRepo != tc.wantCreateName {
				t.Fatalf("CreateRepo name = %q, want %q", tc.client.lastCreateRepo, tc.wantCreateName)
			}
		})
	}
}

// TestEnsureRepoNoRepoURLSkipsExistenceCheck pins that the derive-a-name
// path never calls GetRepo: there is no owner/repo to check yet.
func TestEnsureRepoNoRepoURLSkipsExistenceCheck(t *testing.T) {
	t.Parallel()
	c := githubClient(&fakeGitClient{newCloneURL: "https://github.com/octo/fix-the-thing.git"})
	p := &fakePusher{}
	a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}, Clients: clients(c)}
	m := pushableMission(t)
	m.Goal = "fix the thing"

	if _, _, err := a.ensureRepo(t.Context(), c, m, "", true); err != nil {
		t.Fatalf("ensureRepo: %v", err)
	}
	if c.existsCalls != 0 {
		t.Fatalf("GetRepo called %d times, want 0 (no owner/repo to check without a name)", c.existsCalls)
	}
}

// TestDeliverMissionRepoResolution table-tests DeliverMission's repo
// resolution order (issue #560): (a) the entry's own RepoURL, (b) the
// mission's own clone-source RepoURL, (c) create-if-missing when
// neither is set, (d) failure when none of the above apply.
func TestDeliverMissionRepoResolution(t *testing.T) {
	t.Parallel()
	resolveToken := func(context.Context, string) (string, error) { return "tok", nil }

	t.Run("a: entry RepoURL wins", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t) // clone source is octo/repo
		p := &fakePusher{host: "github.com"}
		c := githubClient(&fakeGitClient{repoExists: true})
		a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, Clients: clients(c)}
		e := &missions.DestinationEntry{RepoURL: "https://github.com/octo/entry-repo.git"}

		if err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push"}, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.RepoURL != "https://github.com/octo/entry-repo.git" {
			t.Fatalf("RepoURL = %q, want the entry's own repo", e.RepoURL)
		}
		if e.Branch != "mission/x" || e.RemoteHost != "github.com" {
			t.Fatalf("entry after delivery = %+v", e)
		}
	})

	t.Run("b: falls back to mission clone source", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t) // clone source is octo/repo
		p := &fakePusher{host: "github.com"}
		c := githubClient(&fakeGitClient{repoExists: true})
		a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, Clients: clients(c)}
		e := &missions.DestinationEntry{}

		if err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push"}, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.RepoURL != m.RepoURL() {
			t.Fatalf("RepoURL = %q, want the mission's clone source %q", e.RepoURL, m.RepoURL())
		}
	})

	t.Run("c: create_if_missing derives a repo", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		m.Sources = nil // no clone source at all -- a scratch mission
		m.Goal = "fix the thing"
		p := &fakePusher{host: "github.com"}
		c := githubClient(&fakeGitClient{newCloneURL: "https://github.com/octo/fix-the-thing.git"})
		a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, Clients: clients(c)}
		e := &missions.DestinationEntry{}

		if err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push", CreateIfMissing: true}, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.RepoURL != "https://github.com/octo/fix-the-thing.git" {
			t.Fatalf("RepoURL = %q, want the created repo", e.RepoURL)
		}
		if c.createRepoCalls != 1 {
			t.Fatalf("CreateRepo called %d times, want 1", c.createRepoCalls)
		}
	})

	t.Run("d: no repo and create_if_missing off fails", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		m.Sources = nil
		c := githubClient(&fakeGitClient{})
		a := &RepoAdapter{Pusher: &fakePusher{}, Events: &fakeEvents{}, ResolveToken: resolveToken, Clients: clients(c)}
		e := &missions.DestinationEntry{}

		if err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push"}, m, e); err == nil {
			t.Fatal("DeliverMission with no repo and create_if_missing off: want an error, got nil")
		}
	})

	t.Run("no connector configured fails before any call", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		c := githubClient(&fakeGitClient{repoExists: true})
		a := &RepoAdapter{Pusher: &fakePusher{}, Events: &fakeEvents{}, ResolveToken: resolveToken, Clients: clients(c)}
		err := a.DeliverMission(t.Context(), RepoDestinationConfig{Mode: "push"}, m, &missions.DestinationEntry{})
		if err == nil || !strings.Contains(err.Error(), "no connector configured") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("connectors not enabled fails cleanly", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		a := &RepoAdapter{Pusher: &fakePusher{}, Events: &fakeEvents{}, ResolveToken: resolveToken}
		err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push"}, m, &missions.DestinationEntry{})
		if err == nil || !strings.Contains(err.Error(), "connectors are not enabled") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestDeliverMissionModes covers push vs push_pr recording the right
// entry fields for both kinds, and that a push failure leaves the
// Error path to the caller (deliverOne/recordOutcome) rather than
// swallowing it.
func TestDeliverMissionModes(t *testing.T) {
	t.Parallel()
	resolveToken := func(context.Context, string) (string, error) { return "tok", nil }

	t.Run("github push records branch and host", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		p := &fakePusher{host: "github.com"}
		c := githubClient(&fakeGitClient{repoExists: true})
		a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, Clients: clients(c)}
		e := &missions.DestinationEntry{RepoURL: "https://github.com/octo/repo.git"}

		if err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push"}, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.Branch != "mission/x" || e.RemoteHost != "github.com" || e.PRURL != "" {
			t.Fatalf("entry after push = %+v", e)
		}
	})

	t.Run("github push_pr records branch, pr url and number", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		m.Name = "Molla-go URL Shortener Design"
		p := &fakePusher{host: "github.com"}
		c := githubClient(&fakeGitClient{repoExists: true, defaultBranch: "main", prURL: "https://github.com/octo/repo/pull/1", prNumber: 1})
		a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}, ResolveToken: resolveToken, Clients: clients(c)}
		e := &missions.DestinationEntry{RepoURL: "https://github.com/octo/repo.git"}

		if err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push_pr"}, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.PRURL != "https://github.com/octo/repo/pull/1" || e.PRNumber != 1 {
			t.Fatalf("entry after push_pr = %+v", e)
		}
		if c.lastTitle != "feat: molla-go url shortener design" {
			t.Fatalf("PR title = %q, want a Conventional Commits title (issue #709)", c.lastTitle)
		}
	})

	t.Run("bitbucket push_pr records pr url, number and events", func(t *testing.T) {
		t.Parallel()
		m := bitbucketMission(t)
		m.Name = "Widget Pricing Fix"
		ev := &fakeEvents{}
		c := bitbucketClient(&fakeGitClient{repoExists: true, defaultBranch: "main",
			prURL: "https://bitbucket.org/acme/widgets/pull-requests/7", prNumber: 7})
		a := &RepoAdapter{Pusher: &fakePusher{host: "bitbucket.org"}, Events: ev, ResolveToken: resolveToken, Clients: clients(c)}
		e := &missions.DestinationEntry{RepoURL: "https://bitbucket.org/acme/widgets.git"}

		if err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "bb1", Mode: "push_pr"}, m, e); err != nil {
			t.Fatalf("DeliverMission: %v", err)
		}
		if e.PRURL != "https://bitbucket.org/acme/widgets/pull-requests/7" || e.PRNumber != 7 {
			t.Fatalf("entry after push_pr = %+v", e)
		}
		if c.lastTitle != "feat: widget pricing fix" {
			t.Fatalf("PR title = %q", c.lastTitle)
		}
		var kinds []string
		for _, evt := range ev.events {
			kinds = append(kinds, evt.Kind)
		}
		if strings.Join(kinds, ",") != "mission.pushed,mission.pr_opened" {
			t.Fatalf("events = %v", kinds)
		}
	})

	t.Run("github url on a bitbucket destination is rejected", func(t *testing.T) {
		t.Parallel()
		m := bitbucketMission(t)
		c := bitbucketClient(&fakeGitClient{repoExists: true})
		a := &RepoAdapter{Pusher: &fakePusher{}, Events: &fakeEvents{}, ResolveToken: resolveToken, Clients: clients(c)}
		e := &missions.DestinationEntry{RepoURL: "https://github.com/acme/widgets.git"}

		err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "bb1", Mode: "push_pr"}, m, e)
		if err == nil || !strings.Contains(err.Error(), "bitbucket https clone URL") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("push failure surfaces as an error", func(t *testing.T) {
		t.Parallel()
		m := pushableMission(t)
		c := githubClient(&fakeGitClient{repoExists: true})
		a := &RepoAdapter{Pusher: &fakePusher{err: missions.ErrPushRejected}, Events: &fakeEvents{}, ResolveToken: resolveToken, Clients: clients(c)}
		e := &missions.DestinationEntry{RepoURL: "https://github.com/octo/repo.git"}

		if err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push"}, m, e); !errors.Is(err, missions.ErrPushRejected) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("unknown mode", func(t *testing.T) {
		t.Parallel()
		m := bitbucketMission(t)
		c := bitbucketClient(&fakeGitClient{repoExists: true})
		a := &RepoAdapter{Pusher: &fakePusher{}, Events: &fakeEvents{}, ResolveToken: resolveToken, Clients: clients(c)}
		e := &missions.DestinationEntry{RepoURL: "https://bitbucket.org/acme/widgets.git"}
		if err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "bb1", Mode: "merge"}, m, e); err == nil {
			t.Fatal("want an error for an unknown mode")
		}
	})
}

// TestOpenPRUsesTheMissionsOwnRepo pins the manual pr endpoint's path:
// the target is the mission's clone source, parsed by the connector's
// own Descriptor.
func TestOpenPRUsesTheMissionsOwnRepo(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	c := githubClient(&fakeGitClient{repoExists: true, defaultBranch: "main", prURL: "https://github.com/octo/repo/pull/3", prNumber: 3})
	a := &RepoAdapter{Pusher: &fakePusher{host: "github.com"}, Events: &fakeEvents{}, Clients: clients(c)}

	url, number, err := a.OpenPR(t.Context(), m, "tok")
	if err != nil || url != "https://github.com/octo/repo/pull/3" || number != 3 {
		t.Fatalf("OpenPR = (%q, %d, %v)", url, number, err)
	}
}

func TestOpenPRRejectsAnUnparseableRepoURL(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	m.Sources = []missions.SourceEntry{{Source: missions.SourceKindGitHub, RepoURL: "https://example.com/nope", ConnectorID: "conn1"}}
	c := githubClient(&fakeGitClient{repoExists: true})
	a := &RepoAdapter{Pusher: &fakePusher{}, Events: &fakeEvents{}, Clients: clients(c)}

	if _, _, err := a.OpenPR(t.Context(), m, "tok"); err == nil || !strings.Contains(err.Error(), "github https clone URL") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenPRWithoutConnectorsFails(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	a := &RepoAdapter{Pusher: &fakePusher{}, Events: &fakeEvents{}}
	if _, _, err := a.OpenPR(t.Context(), m, "tok"); err == nil || !strings.Contains(err.Error(), "connectors are not enabled") {
		t.Fatalf("err = %v", err)
	}
}

// A repo with no default branch cannot take a PR.
func TestOpenPRRequiresADefaultBranch(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	c := githubClient(&fakeGitClient{repoExists: true}) // defaultBranch empty
	a := &RepoAdapter{Pusher: &fakePusher{host: "github.com"}, Events: &fakeEvents{}, Clients: clients(c)}
	if _, _, err := a.OpenPR(t.Context(), m, "tok"); err == nil || !strings.Contains(err.Error(), "no default branch") {
		t.Fatalf("err = %v", err)
	}
}

func TestPRBody(t *testing.T) {
	m := missions.Mission{Goal: "Add base62"}
	m.Plan.Units = []missions.PlanUnit{{Title: "encode", Passes: true}, {Title: "decode"}}
	got := PRBody(m, true)
	for _, want := range []string{
		"Add base62\n\n## Units\n\n",
		"- [x] encode\n",
		"- [ ] decode\n",
		"_PR was created by [Timothy Agent](https://github.com/timothy-agent/timothy)._\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PRBody missing %q in:\n%s", want, got)
		}
	}
	if off := PRBody(m, false); strings.Contains(off, "Timothy Agent") {
		t.Errorf("PRBody(attribution=false) still carries the attribution line:\n%s", off)
	}
}

func TestRepoAdapterAttributionDefaultsOn(t *testing.T) {
	a := &RepoAdapter{}
	if !a.attribution(context.Background()) {
		t.Fatal("nil Attribution hook = false, want true")
	}
	a.Attribution = func(context.Context) bool { return false }
	if a.attribution(context.Background()) {
		t.Fatal("Attribution hook returning false was ignored")
	}
}

// sshTransport builds the resolver that always picks ssh, with the
// probe faked out: no host to reach in a test.
func sshTransport(ev events) *TransportResolver {
	return &TransportResolver{ResolveSSH: sshEnabled, Events: ev, probe: okProbe, hasSSH: yesSSH}
}

// TestPushBranchUsesSSHWhenResolved proves the transport decision
// reaches the pusher AND that origin is repointed at the ssh clone
// URL first: Push rejects an auth whose transport disagrees with the
// origin's scheme, so skipping the repoint would fail every ssh push
// (issue #796).
func TestPushBranchUsesSSHWhenResolved(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	p := &fakePusher{host: "github.com"}
	c := githubClient(&fakeGitClient{})
	ev := &fakeEvents{}
	a := &RepoAdapter{Pusher: p, Events: ev, Clients: clients(c), Transport: sshTransport(ev)}

	if _, err := a.PushBranch(t.Context(), m, "tok"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if !p.lastAuth.IsSSH() {
		t.Fatalf("push ran over %+v, want ssh", p.lastAuth)
	}
	if p.lastOriginURL != "ssh://git@github.com/octo/repo.git" {
		t.Fatalf("origin = %q, want the ssh clone URL", p.lastOriginURL)
	}
}

// The same push with no transport resolver stays https and never
// touches origin: the pre-#796 behavior, unchanged.
func TestPushBranchStaysHTTPSWithoutTransport(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	p := &fakePusher{host: "github.com"}
	c := githubClient(&fakeGitClient{})
	a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}, Clients: clients(c)}

	if _, err := a.PushBranch(t.Context(), m, "tok"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if p.lastAuth.IsSSH() {
		t.Fatal("push ran over ssh with no transport resolver wired")
	}
	if p.lastAuth.Token != "tok" || p.lastAuth.HTTPUsername != "x-access-token" {
		t.Fatalf("https auth = %+v", p.lastAuth)
	}
	if p.setOriginCalls != 0 {
		t.Fatalf("an https push repointed origin %d times, want 0", p.setOriginCalls)
	}
}

// A bitbucket mission's https push must still carry bitbucket's own
// credential username (issue #787's fix, preserved through #796).
func TestPushBranchCarriesBitbucketUsername(t *testing.T) {
	t.Parallel()
	m := bitbucketMission(t)
	p := &fakePusher{host: "bitbucket.org"}
	c := bitbucketClient(&fakeGitClient{})
	a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}, Clients: clients(c)}

	if _, err := a.PushBranch(t.Context(), m, "tok"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if p.lastAuth.HTTPUsername != "x-token-auth" {
		t.Fatalf("credential username = %q, want x-token-auth", p.lastAuth.HTTPUsername)
	}
}

// A mission with no connector at all (no Clients wired) still pushes,
// over https: PushBranch must never fail just because the transport
// could not be resolved.
func TestPushBranchWithoutClientsStillPushes(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	p := &fakePusher{host: "github.com"}
	a := &RepoAdapter{Pusher: p, Events: &fakeEvents{}}

	if _, err := a.PushBranch(t.Context(), m, "tok"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if p.pushCalls != 1 || p.lastAuth.IsSSH() {
		t.Fatalf("pushCalls=%d auth=%+v", p.pushCalls, p.lastAuth)
	}
}

// An SSH-only connector whose probe fails must fail the delivery, not
// push silently over a transport it was told not to use.
func TestDeliverMissionFailsWhenSSHOnlyAndUnreachable(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	p := &fakePusher{host: "github.com"}
	c := githubClient(&fakeGitClient{repoExists: true})
	ev := &fakeEvents{}
	a := &RepoAdapter{
		Pusher: p, Events: ev, Clients: clients(c),
		ResolveToken: func(context.Context, string) (string, error) { return "", nil },
		Transport:    &TransportResolver{ResolveSSH: sshEnabled, Events: ev, probe: failProbe, hasSSH: yesSSH},
	}
	err := a.DeliverMission(t.Context(), RepoDestinationConfig{ConnectorID: "conn1", Mode: "push"}, m, &missions.DestinationEntry{})
	var unavailable *SSHUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("DeliverMission = %v, want an SSHUnavailableError", err)
	}
	if p.pushCalls != 0 {
		t.Fatalf("a failed ssh-only delivery still pushed %d times", p.pushCalls)
	}
}
