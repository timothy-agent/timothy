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
// without a real GitHub connector.
type fakePRSource struct {
	defaultBranch string
	defaultErr    error
	prURL         string
	prNumber      int
	createErr     error
	createCalls   int
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

// fakeBranchPusher scripts branchPusher — the https-only remote
// validation Workspace.Push itself enforces is push_test.go's own
// coverage; Completer's job (proven here) is what it does with a push
// that succeeds or fails, not git plumbing.
type fakeBranchPusher struct {
	host      string
	err       error
	pushCalls int
}

func (f *fakeBranchPusher) Push(ctx context.Context, worktree, branch, token string) (string, error) {
	f.pushCalls++
	if f.err != nil {
		return "", f.err
	}
	return f.host, nil
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

	if err := c.RunOnComplete(context.Background(), m); err != nil {
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

	if err := c.RunOnComplete(context.Background(), m); err != nil {
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

	if err := c.RunOnComplete(context.Background(), m); err == nil {
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
	if err := c.RunOnComplete(context.Background(), Mission{}); err != nil {
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
	err := c.RunOnComplete(context.Background(), m)
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
	if err := c.RunOnComplete(context.Background(), m); err == nil {
		t.Fatal("RunOnComplete with no token resolver should fail")
	}
	if len(store.kinds()) != 0 {
		t.Fatalf("events = %v, want none", store.kinds())
	}
}

