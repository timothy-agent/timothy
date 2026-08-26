package missions

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// followUpBlockedRunner is a scriptedRunner with one entry, shared by
// every followup test — CreateFollowUp's own Create call kicks off a
// real Drive goroutine, and "blocked" parks it immediately without
// needing a real workspace/sandbox.
func followUpBlockedRunner() *scriptedRunner {
	return &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
}

// TestCreateFollowUpRejectsNonTerminalParent proves a parent still
// mid-flight is refused before any child mission is created.
func TestCreateFollowUpRejectsNonTerminalParent(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Goal: "do the thing", Kind: "general", Phase: PhaseExecute, Status: StatusWorking})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	_, err := d.CreateFollowUp(context.Background(), "parent", "do more")
	if err == nil {
		t.Fatal("expected an error for a non-terminal parent")
	}
	if !strings.Contains(err.Error(), "not finished") {
		t.Fatalf("error %q should say the parent is not finished", err.Error())
	}
}

// TestCreateFollowUpUnknownParent proves an unknown parent id is
// refused with a clear error.
func TestCreateFollowUpUnknownParent(t *testing.T) {
	store := newFakeStore()
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	_, err := d.CreateFollowUp(context.Background(), "does-not-exist", "do more")
	if err == nil {
		t.Fatal("expected an error for an unknown parent id")
	}
}

// TestCreateFollowUpCopiesParentSettings proves the child mission
// inherits the parent's kind/agent/routes/repo/harness settings, carries
// a non-empty ParentContext, and does NOT inherit OnComplete or
// DestinationIDs.
func TestCreateFollowUpCopiesParentSettings(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{
		ID: "parent", Goal: "fix the login bug", Kind: "coding", Phase: PhaseDone, Status: StatusDone,
		AgentID: "agent-1", Route: "route-a", ReviewRoute: "route-b", PlanRoute: "route-c",
		EscalationRoute: "route-d", MaxIterations: 12, BudgetCurrency: "USD",
		RouteModel: "P/m-a", PlanRouteModel: "P/m-c", ReviewRouteModel: "P/m-b",
		AutoApproveSafe: true, PromptOverlay: "be terse", Knowledge: []string{"kb1"},
		Harness: "claude-cli", Environment: "node", RepoURL: "https://github.com/o/r.git",
		ConnectorID: "conn1", BranchPattern: "custom/{slug}", CommitStyle: "conventional",
		OnComplete: "push_pr", DestinationIDs: []string{"dest-1"},
	})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	id, err := d.CreateFollowUp(context.Background(), "parent", "now add tests")
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	child, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get child: %v", err)
	}
	if child.Goal != "now add tests" {
		t.Fatalf("child.Goal = %q, want the follow-up's own goal", child.Goal)
	}
	if child.Kind != "coding" || child.AgentID != "agent-1" || child.Route != "route-a" ||
		child.ReviewRoute != "route-b" || child.PlanRoute != "route-c" || child.EscalationRoute != "route-d" ||
		child.MaxIterations != 12 || child.AutoApproveSafe != true || child.PromptOverlay != "be terse" ||
		child.Harness != "claude-cli" || child.Environment != "node" || child.RepoURL != "https://github.com/o/r.git" ||
		child.ConnectorID != "conn1" || child.BranchPattern != "custom/{slug}" || child.CommitStyle != "conventional" ||
		child.RouteModel != "P/m-a" || child.PlanRouteModel != "P/m-c" || child.ReviewRouteModel != "P/m-b" {
		t.Fatalf("child did not inherit parent settings: %+v", child)
	}
	if len(child.Knowledge) != 1 || child.Knowledge[0] != "kb1" {
		t.Fatalf("child.Knowledge = %v, want inherited from parent", child.Knowledge)
	}
	if child.ParentMissionID != "parent" {
		t.Fatalf("child.ParentMissionID = %q, want %q", child.ParentMissionID, "parent")
	}
	if child.ParentContext == "" {
		t.Fatal("child.ParentContext should carry the parent's outcome digest")
	}
	if !strings.Contains(child.ParentContext, "fix the login bug") {
		t.Fatalf("child.ParentContext = %q, want it to mention the parent's goal", child.ParentContext)
	}
	if child.OnComplete != "" {
		t.Fatalf("child.OnComplete = %q, want empty — push consent is a per-mission choice", child.OnComplete)
	}
	if len(child.DestinationIDs) != 0 {
		t.Fatalf("child.DestinationIDs = %v, want empty — destinations are a per-mission choice", child.DestinationIDs)
	}
}

// TestCreateFollowUpAllowsFailedParent proves a failed (not just done)
// terminal parent is a valid follow-up base too.
func TestCreateFollowUpAllowsFailedParent(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Goal: "attempt one", Kind: "general", Phase: PhaseFailed, Status: StatusError})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	id, err := d.CreateFollowUp(context.Background(), "parent", "attempt two")
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	child, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get child: %v", err)
	}
	if child.ParentMissionID != "parent" {
		t.Fatalf("child.ParentMissionID = %q, want %q", child.ParentMissionID, "parent")
	}
}
