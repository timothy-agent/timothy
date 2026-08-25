package missions

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

// newTestProvisioner builds a bare provisioner over a fakeStore for
// followUpBaseRef's own unit tests — no workspace/sessions/perms
// needed, since none of those are on this method's call path.
func newTestProvisioner(store driverStore) *provisioner {
	return &provisioner{store: store, log: slog.Default()}
}

// TestFollowUpBaseRefNoParent proves a mission with no ParentMissionID
// never looks anything up.
func TestFollowUpBaseRefNoParent(t *testing.T) {
	store := newFakeStore()
	p := newTestProvisioner(store)
	if got := p.followUpBaseRef(context.Background(), Mission{ID: "m1"}); got != "" {
		t.Fatalf("base = %q, want empty with no parent", got)
	}
}

// TestFollowUpBaseRefUsesParentBranch proves the parent's own branch is
// returned when the parent has one, shares RepoURL, and no PR resolver
// is wired.
func TestFollowUpBaseRefUsesParentBranch(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Branch: "mission/fix-bug", RepoURL: "https://github.com/o/r.git"})
	p := newTestProvisioner(store)
	m := Mission{ID: "m1", ParentMissionID: "parent", RepoURL: "https://github.com/o/r.git"}
	if got, want := p.followUpBaseRef(context.Background(), m), "mission/fix-bug"; got != want {
		t.Fatalf("base = %q, want %q", got, want)
	}
}

// TestFollowUpBaseRefDifferentRepoDegradesToEmpty proves a follow-up
// cloning a different repo than its parent gets no base ref.
func TestFollowUpBaseRefDifferentRepoDegradesToEmpty(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Branch: "mission/fix-bug", RepoURL: "https://github.com/o/r.git"})
	p := newTestProvisioner(store)
	m := Mission{ID: "m1", ParentMissionID: "parent", RepoURL: "https://github.com/o/other.git"}
	if got := p.followUpBaseRef(context.Background(), m); got != "" {
		t.Fatalf("base = %q, want empty for a different repo", got)
	}
}

// TestFollowUpBaseRefMergedPRDegradesToEmpty proves a merged parent PR
// makes followUpBaseRef fall through to the repo default branch ("")
// instead of the parent's own (possibly deleted) branch.
func TestFollowUpBaseRefMergedPRDegradesToEmpty(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Branch: "mission/fix-bug", RepoURL: "https://github.com/o/r.git", ConnectorID: "conn1"})
	if err := store.AppendEvent(context.Background(), "parent", "mission.pr_opened", map[string]any{"url": "https://github.com/o/r/pull/9", "number": 9}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	p := newTestProvisioner(store)
	p.resolvePRState = func(ctx context.Context, connectorID, owner, repo string, number int) (bool, error) {
		if connectorID != "conn1" || owner != "o" || repo != "r" || number != 9 {
			t.Fatalf("resolvePRState called with connectorID=%q owner=%q repo=%q number=%d", connectorID, owner, repo, number)
		}
		return true, nil
	}
	m := Mission{ID: "m1", ParentMissionID: "parent", RepoURL: "https://github.com/o/r.git"}
	if got := p.followUpBaseRef(context.Background(), m); got != "" {
		t.Fatalf("base = %q, want empty when the parent's PR is merged", got)
	}
}

// TestFollowUpBaseRefUnmergedPRUsesParentBranch proves an open
// (unmerged) parent PR still bases the follow-up on the parent's
// branch.
func TestFollowUpBaseRefUnmergedPRUsesParentBranch(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Branch: "mission/fix-bug", RepoURL: "https://github.com/o/r.git", ConnectorID: "conn1"})
	if err := store.AppendEvent(context.Background(), "parent", "mission.pr_opened", map[string]any{"url": "https://github.com/o/r/pull/9", "number": 9}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	p := newTestProvisioner(store)
	p.resolvePRState = func(ctx context.Context, connectorID, owner, repo string, number int) (bool, error) {
		return false, nil
	}
	m := Mission{ID: "m1", ParentMissionID: "parent", RepoURL: "https://github.com/o/r.git"}
	if got, want := p.followUpBaseRef(context.Background(), m), "mission/fix-bug"; got != want {
		t.Fatalf("base = %q, want %q for an unmerged PR", got, want)
	}
}

// TestFollowUpBaseRefResolverErrorFallsBackToParentBranch proves a
// resolver error degrades to the parent-branch base, never fails
// provisioning.
func TestFollowUpBaseRefResolverErrorFallsBackToParentBranch(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Branch: "mission/fix-bug", RepoURL: "https://github.com/o/r.git", ConnectorID: "conn1"})
	if err := store.AppendEvent(context.Background(), "parent", "mission.pr_opened", map[string]any{"url": "https://github.com/o/r/pull/9", "number": 9}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	p := newTestProvisioner(store)
	p.resolvePRState = func(ctx context.Context, connectorID, owner, repo string, number int) (bool, error) {
		return false, errors.New("github unreachable")
	}
	m := Mission{ID: "m1", ParentMissionID: "parent", RepoURL: "https://github.com/o/r.git"}
	if got, want := p.followUpBaseRef(context.Background(), m), "mission/fix-bug"; got != want {
		t.Fatalf("base = %q, want %q when the resolver errors", got, want)
	}
}

// TestFollowUpBaseRefNoPROpenedEventUsesParentBranch proves a parent
// with no recorded PR (never pushed/opened one) still uses its own
// branch when a resolver is wired — there's simply nothing to check.
func TestFollowUpBaseRefNoPROpenedEventUsesParentBranch(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Branch: "mission/fix-bug", RepoURL: "https://github.com/o/r.git", ConnectorID: "conn1"})
	p := newTestProvisioner(store)
	calls := 0
	p.resolvePRState = func(ctx context.Context, connectorID, owner, repo string, number int) (bool, error) {
		calls++
		return true, nil
	}
	m := Mission{ID: "m1", ParentMissionID: "parent", RepoURL: "https://github.com/o/r.git"}
	if got, want := p.followUpBaseRef(context.Background(), m), "mission/fix-bug"; got != want {
		t.Fatalf("base = %q, want %q with no pr_opened event", got, want)
	}
	if calls != 0 {
		t.Fatalf("resolvePRState called %d times, want 0 with no pr_opened event", calls)
	}
}
