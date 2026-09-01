package missions

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
)

// TestParseGitHubRepoURL covers the owner/repo extraction OpenPR needs
// from mission.RepoURL (always an https clone URL) — with and without
// the .git suffix, and a malformed shape reporting ok=false.
func TestParseGitHubRepoURL(t *testing.T) {
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
		{"malformed, no repo segment", "https://github.com/octocat", "", "", false},
		{"not a URL", "not-a-url", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, ok := ParseGitHubRepoURL(tc.url)
			if ok != tc.wantOK || owner != tc.owner || repo != tc.repo {
				t.Fatalf("ParseGitHubRepoURL(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.url, owner, repo, ok, tc.owner, tc.repo, tc.wantOK)
			}
		})
	}
}

// TestPRTitleFallsBackToTruncatedGoal covers PRTitle's name-vs-goal
// precedence and the truncation cap for a long goal with no name yet.
func TestPRTitleFallsBackToTruncatedGoal(t *testing.T) {
	t.Parallel()
	named := Mission{Name: "Fix Login Bug", Goal: "fix the login bug that logs everyone out"}
	if got := PRTitle(named); got != "Fix Login Bug" {
		t.Fatalf("PRTitle with a name = %q, want the name", got)
	}
	short := Mission{Goal: "fix the login bug"}
	if got := PRTitle(short); got != "fix the login bug" {
		t.Fatalf("PRTitle with a short goal and no name = %q, want the goal verbatim", got)
	}
	long := Mission{Goal: strings.Repeat("a", 100)}
	got := PRTitle(long)
	if len(got) != PRTitleGoalCap+len("…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("PRTitle with a long goal and no name = %q (len %d), want truncated to %d chars + ellipsis", got, len(got), PRTitleGoalCap)
	}
}

// TestNotPushableGuards covers the shared kind/branch/worktree gate
// both push and pr require, without touching a store or workspace.
func TestNotPushableGuards(t *testing.T) {
	t.Parallel()
	if reason := NotPushable(Mission{Kind: "general"}); reason == "" {
		t.Fatal("general mission should not be pushable")
	}
	if reason := NotPushable(Mission{Kind: "coding"}); reason == "" {
		t.Fatal("coding mission with no branch should not be pushable")
	}
	if reason := NotPushable(Mission{Kind: "coding", Branch: "mission/x", Workspace: "/does/not/exist"}); reason == "" {
		t.Fatal("coding mission with a missing worktree should not be pushable")
	}
}

// fakeEventAppender records every AppendEvent call for assertion,
// standing in for *Store without a real Postgres pool.
type fakeEventAppender struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (f *fakeEventAppender) AppendEvent(ctx context.Context, id, kind string, payload map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, Event{MissionID: id, Kind: kind})
	return nil
}

func (f *fakeEventAppender) kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.events))
	for i, e := range f.events {
		out[i] = e.Kind
	}
	return out
}

// fakePRSource scripts PRSource for RunOnComplete's push_pr path
// without a real GitHub connector. repoExists/existsErr/newCloneURL/
// createRepoErr back ensureRepo's create-if-missing path (issue #483);
// existsCalls/createRepoCalls count those specifically, distinct from
// createCalls (CreatePR, the pull-request call).
type fakePRSource struct {
	defaultBranch string
	defaultErr    error
	prURL         string
	prNumber      int
	createErr     error
	createCalls   int

	repoExists      bool
	existsErr       error
	existsCalls     int
	newCloneURL     string
	createRepoErr   error
	createRepoCalls int
}

func (f *fakePRSource) DefaultBranch(ctx context.Context, connectorID, owner, repo string) (string, error) {
	return f.defaultBranch, f.defaultErr
}

func (f *fakePRSource) CreatePR(ctx context.Context, connectorID, owner, repo, title, head, base, body string) (string, int, error) {
	f.createCalls++
	if f.createErr != nil {
		return "", 0, f.createErr
	}
	return f.prURL, f.prNumber, nil
}

func (f *fakePRSource) RepoExists(ctx context.Context, connectorID, owner, repo string) (bool, error) {
	f.existsCalls++
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.repoExists, nil
}

func (f *fakePRSource) CreateRepo(ctx context.Context, connectorID, name string, private bool) (string, error) {
	f.createRepoCalls++
	if f.createRepoErr != nil {
		return "", f.createRepoErr
	}
	return f.newCloneURL, nil
}

// fakeBranchPusher scripts branchPusher — the https-only remote
// validation Workspace.Push itself enforces is push_test.go's own
// coverage; Completer's job (proven here) is what it does with a push
// that succeeds or fails, not git plumbing. setOriginCalls/
// setOriginErr back ensureRepo's create-if-missing path (issue #483).
type fakeBranchPusher struct {
	host      string
	err       error
	pushCalls int

	setOriginCalls int
	setOriginErr   error
	lastOriginURL  string
}

func (f *fakeBranchPusher) Push(ctx context.Context, worktree, branch, token string) (string, error) {
	f.pushCalls++
	if f.err != nil {
		return "", f.err
	}
	return f.host, nil
}

func (f *fakeBranchPusher) SetOrigin(ctx context.Context, worktree, remoteURL string) error {
	f.setOriginCalls++
	f.lastOriginURL = remoteURL
	return f.setOriginErr
}

// pushableMission is a coding mission with the guards NotPushable
// requires already satisfied (worktree present, branch set) — t.TempDir()
// stands in for a real workspace since fakeBranchPusher never touches
// the filesystem; WorktreePath() derives workspace/wt from it.
func pushableMission(t *testing.T) Mission {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/wt", 0o750); err != nil {
		t.Fatal(err)
	}
	return Mission{
		ID: "m1", Kind: "coding", Workspace: dir, Branch: "mission/x",
		Sources: []SourceEntry{{Source: SourceKindGitHub, RepoURL: "https://github.com/octo/repo.git", ConnectorID: "conn1"}},
	}
}

// TestCompleterRunOnCompletePush proves on_complete="push" pushes the
// branch and records exactly one mission.pushed event — the same event
// the manual push endpoint records.
func TestCompleterRunOnCompletePush(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	m.Destinations = []DestinationEntry{{Destination: DestinationKindGitHub, Mode: "push"}}
	store := &fakeEventAppender{}
	pusher := &fakeBranchPusher{host: "github.com"}
	resolveToken := func(ctx context.Context, connectorID string) (string, error) { return "dummy-token", nil }
	c := &Completer{workspace: pusher, store: store, resolveToken: resolveToken}

	if _, _, err := c.RunOnComplete(context.Background(), m); err != nil {
		t.Fatalf("RunOnComplete: %v", err)
	}
	if pusher.pushCalls != 1 {
		t.Fatalf("Push called %d times, want exactly 1", pusher.pushCalls)
	}
	if kinds := store.kinds(); len(kinds) != 1 || kinds[0] != "mission.pushed" {
		t.Fatalf("events = %v, want exactly one mission.pushed", kinds)
	}
}

// TestCompleterRunOnCompletePushPR proves on_complete="push_pr" pushes
// then opens a PR, recording both mission.pushed and mission.pr_opened
// — exactly once each.
func TestCompleterRunOnCompletePushPR(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	m.Destinations = []DestinationEntry{{Destination: DestinationKindGitHub, Mode: "push_pr"}}
	store := &fakeEventAppender{}
	pusher := &fakeBranchPusher{host: "github.com"}
	resolveToken := func(ctx context.Context, connectorID string) (string, error) { return "dummy-token", nil }
	pr := &fakePRSource{defaultBranch: "main", prURL: "https://github.com/octo/repo/pull/1", prNumber: 1}
	c := &Completer{workspace: pusher, store: store, resolveToken: resolveToken, pr: pr}

	if _, _, err := c.RunOnComplete(context.Background(), m); err != nil {
		t.Fatalf("RunOnComplete: %v", err)
	}
	if pusher.pushCalls != 1 {
		t.Fatalf("Push called %d times, want exactly 1", pusher.pushCalls)
	}
	kinds := store.kinds()
	if len(kinds) != 2 || kinds[0] != "mission.pushed" || kinds[1] != "mission.pr_opened" {
		t.Fatalf("events = %v, want [mission.pushed mission.pr_opened]", kinds)
	}
	if pr.createCalls != 1 {
		t.Fatalf("CreatePR called %d times, want exactly 1", pr.createCalls)
	}
}

// TestCompleterRunOnCompletePushFailureNoPR proves a push failure during
// push_pr stops before ever calling CreatePR — a failed push must never
// still open a PR against whatever was last pushed.
func TestCompleterRunOnCompletePushFailureNoPR(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	m.Destinations = []DestinationEntry{{Destination: DestinationKindGitHub, Mode: "push_pr"}}
	store := &fakeEventAppender{}
	pusher := &fakeBranchPusher{err: ErrPushRejected}
	resolveToken := func(ctx context.Context, connectorID string) (string, error) { return "dummy-token", nil }
	pr := &fakePRSource{defaultBranch: "main"}
	c := &Completer{workspace: pusher, store: store, resolveToken: resolveToken, pr: pr}

	if _, _, err := c.RunOnComplete(context.Background(), m); err == nil {
		t.Fatal("RunOnComplete should surface the push failure")
	}
	if pr.createCalls != 0 {
		t.Fatalf("CreatePR called %d times, want 0 (push failed first)", pr.createCalls)
	}
	if kinds := store.kinds(); len(kinds) != 1 || kinds[0] != "mission.push_failed" {
		t.Fatalf("events = %v, want exactly one mission.push_failed", kinds)
	}
}

// TestCompleterRunOnCompleteEmptyIsNoop proves a mission with no
// on_complete choice does nothing — no push, no event, no error.
func TestCompleterRunOnCompleteEmptyIsNoop(t *testing.T) {
	t.Parallel()
	store := &fakeEventAppender{}
	c := NewCompleter(nil, store, nil, nil)
	if _, _, err := c.RunOnComplete(context.Background(), Mission{}); err != nil {
		t.Fatalf("RunOnComplete with empty on_complete: %v", err)
	}
	if len(store.kinds()) != 0 {
		t.Fatalf("events = %v, want none", store.kinds())
	}
}

// TestCompleterRunOnCompletePushFailureRecordsEvent proves a push
// failure (unpushable mission) reports an error and records no
// mission.pushed event — the mission itself is never touched, only the
// caller (fireOnComplete) learns of the failure.
func TestCompleterRunOnCompletePushFailureRecordsEvent(t *testing.T) {
	t.Parallel()
	store := &fakeEventAppender{}
	c := NewCompleter(nil, store, nil, nil)
	m := Mission{Kind: "general", Destinations: []DestinationEntry{{Destination: DestinationKindGitHub, Mode: "push"}}} // NotPushable rejects kind != coding
	_, _, err := c.RunOnComplete(context.Background(), m)
	if err == nil {
		t.Fatal("RunOnComplete should fail for an unpushable mission")
	}
	if len(store.kinds()) != 0 {
		t.Fatalf("events = %v, want none (NotPushable fails before any push attempt)", store.kinds())
	}
}

// TestCompleterRunOnCompleteNoResolverFails proves a nil token resolver
// (connectors/secrets not wired) fails cleanly rather than panicking.
func TestCompleterRunOnCompleteNoResolverFails(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	m.Destinations = []DestinationEntry{{Destination: DestinationKindGitHub, Mode: "push"}}
	store := &fakeEventAppender{}
	c := NewCompleter(nil, store, nil, nil)
	if _, _, err := c.RunOnComplete(context.Background(), m); err == nil {
		t.Fatal("RunOnComplete with no token resolver should fail")
	}
	if len(store.kinds()) != 0 {
		t.Fatalf("events = %v, want none", store.kinds())
	}
}

// TestEnsureRepo table-tests ensureRepo's create-if-missing decision
// (issue #483): repo already exists, repo missing with
// create_if_missing=true, repo missing with create_if_missing=false,
// and the retry-after-already-created idempotency path (RepoExists
// now reports true because a prior attempt already created it).
func TestEnsureRepo(t *testing.T) {
	t.Parallel()
	baseEntry := DestinationEntry{Destination: DestinationKindGitHub, ConnectorID: "conn1"}

	cases := []struct {
		name string
		// entry.RepoURL is set for every case except the "no repo_url at
		// all, derive from goal" case.
		repoURL         string
		createIfMissing bool
		repoExists      bool
		existsErr       error
		createErr       error
		newCloneURL     string

		wantErr           bool
		wantUpdated       bool
		wantCreateCalls   int
		wantSetOriginCall int
		wantFinalRepoURL  string
	}{
		{
			name:              "repo exists: no-op",
			repoURL:           "https://github.com/octo/repo.git",
			createIfMissing:   true,
			repoExists:        true,
			wantErr:           false,
			wantUpdated:       false,
			wantCreateCalls:   0,
			wantSetOriginCall: 0,
			wantFinalRepoURL:  "https://github.com/octo/repo.git",
		},
		{
			name:              "repo missing, create_if_missing=true: creates and redirects origin",
			repoURL:           "https://github.com/octo/repo.git",
			createIfMissing:   true,
			repoExists:        false,
			newCloneURL:       "https://github.com/octo/repo.git",
			wantErr:           false,
			wantUpdated:       true,
			wantCreateCalls:   1,
			wantSetOriginCall: 1,
			wantFinalRepoURL:  "https://github.com/octo/repo.git",
		},
		{
			name:              "repo missing, create_if_missing=false: fails honestly, never creates",
			repoURL:           "https://github.com/octo/repo.git",
			createIfMissing:   false,
			repoExists:        false,
			wantErr:           true,
			wantUpdated:       false,
			wantCreateCalls:   0,
			wantSetOriginCall: 0,
		},
		{
			name:              "retry after already created: idempotent, no second create",
			repoURL:           "https://github.com/octo/repo.git",
			createIfMissing:   true,
			repoExists:        true, // a prior attempt already created it
			wantErr:           false,
			wantUpdated:       false,
			wantCreateCalls:   0,
			wantSetOriginCall: 0,
			wantFinalRepoURL:  "https://github.com/octo/repo.git",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := baseEntry
			entry.RepoURL = tc.repoURL
			entry.CreateIfMissing = tc.createIfMissing
			pr := &fakePRSource{repoExists: tc.repoExists, existsErr: tc.existsErr, newCloneURL: tc.newCloneURL, createRepoErr: tc.createErr}
			pusher := &fakeBranchPusher{}
			c := &Completer{workspace: pusher, pr: pr}
			m := pushableMission(t)

			got, updated, err := c.ensureRepo(context.Background(), m, entry)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ensureRepo error = %v, wantErr %v", err, tc.wantErr)
			}
			if updated != tc.wantUpdated {
				t.Fatalf("updated = %v, want %v", updated, tc.wantUpdated)
			}
			if pr.createRepoCalls != tc.wantCreateCalls {
				t.Fatalf("CreateRepo called %d times, want %d", pr.createRepoCalls, tc.wantCreateCalls)
			}
			if pusher.setOriginCalls != tc.wantSetOriginCall {
				t.Fatalf("SetOrigin called %d times, want %d", pusher.setOriginCalls, tc.wantSetOriginCall)
			}
			if !tc.wantErr && got.RepoURL != tc.wantFinalRepoURL {
				t.Fatalf("final RepoURL = %q, want %q", got.RepoURL, tc.wantFinalRepoURL)
			}
		})
	}
}

// TestEnsureRepoNoRepoURLDerivesNameFromGoal proves an entry with no
// RepoURL at all but create_if_missing=true derives a repo name from
// the mission's goal (via Slug) instead of failing: the "brand new
// scratch mission gets its own repo" case.
func TestEnsureRepoNoRepoURLDerivesNameFromGoal(t *testing.T) {
	t.Parallel()
	entry := DestinationEntry{Destination: DestinationKindGitHub, ConnectorID: "conn1", CreateIfMissing: true}
	pr := &fakePRSource{newCloneURL: "https://github.com/octo/fix-the-thing.git"}
	pusher := &fakeBranchPusher{}
	c := &Completer{workspace: pusher, pr: pr}
	m := pushableMission(t)
	m.Goal = "fix the thing"

	got, updated, err := c.ensureRepo(context.Background(), m, entry)
	if err != nil {
		t.Fatalf("ensureRepo: %v", err)
	}
	if !updated {
		t.Fatal("expected updated=true")
	}
	if got.RepoURL != "https://github.com/octo/fix-the-thing.git" {
		t.Fatalf("RepoURL = %q, want the created clone URL", got.RepoURL)
	}
	if pr.existsCalls != 0 {
		t.Fatalf("RepoExists called %d times, want 0 (no owner/repo to check without a name)", pr.existsCalls)
	}
	if pr.createRepoCalls != 1 {
		t.Fatalf("CreateRepo called %d times, want 1", pr.createRepoCalls)
	}
}

// TestRunOnCompletePersistsCreatedRepo proves RunOnComplete's
// create-if-missing path actually pushes to the newly created repo (an
// end-to-end proof that PushBranch runs after SetOrigin, not against a
// stale origin) and reports updated=true so the caller
// (Driver.fireOnComplete) knows to persist the entry.
func TestRunOnCompletePersistsCreatedRepo(t *testing.T) {
	t.Parallel()
	m := pushableMission(t)
	m.Destinations = []DestinationEntry{{
		Destination: DestinationKindGitHub, Mode: "push",
		ConnectorID: "conn1", RepoURL: "https://github.com/octo/new-repo.git", CreateIfMissing: true,
	}}
	store := &fakeEventAppender{}
	pusher := &fakeBranchPusher{host: "github.com"}
	pr := &fakePRSource{repoExists: false, newCloneURL: "https://github.com/octo/new-repo.git"}
	resolveToken := func(ctx context.Context, connectorID string) (string, error) { return "dummy-token", nil }
	c := &Completer{workspace: pusher, store: store, resolveToken: resolveToken, pr: pr}

	entry, updated, err := c.RunOnComplete(context.Background(), m)
	if err != nil {
		t.Fatalf("RunOnComplete: %v", err)
	}
	if !updated {
		t.Fatal("expected updated=true after creating the repo")
	}
	if entry.RepoURL != "https://github.com/octo/new-repo.git" {
		t.Fatalf("entry.RepoURL = %q, want the created clone URL", entry.RepoURL)
	}
	if pusher.setOriginCalls != 1 || pusher.lastOriginURL != "https://github.com/octo/new-repo.git" {
		t.Fatalf("SetOrigin calls = %d, lastURL = %q, want 1 call at the created URL", pusher.setOriginCalls, pusher.lastOriginURL)
	}
	if pusher.pushCalls != 1 {
		t.Fatalf("Push called %d times, want exactly 1", pusher.pushCalls)
	}
}
