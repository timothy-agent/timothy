package missions

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
	store.put("parent", Mission{ID: "parent", Goal: "do the thing", Kind: "general", Phase: PhaseBuild, Status: StatusWorking})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	_, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "do more"})
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

	_, err := d.CreateFollowUp(context.Background(), "does-not-exist", FollowUpOptions{Goal: "do more"})
	if err == nil {
		t.Fatal("expected an error for an unknown parent id")
	}
}

// TestCreateFollowUpCopiesParentSettings proves the child mission
// inherits the parent's kind/agent/routes/repo/harness settings, carries
// a non-empty ParentContext, and does NOT inherit Destinations (push
// consent, destination ids, kb promotion).
func TestCreateFollowUpCopiesParentSettings(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{
		ID: "parent", Goal: "fix the login bug", Kind: "coding", Phase: PhaseDone, Status: StatusDone,
		AgentID: "agent-1", Route: "route-a", ReviewRoute: "route-b", PlanRoute: "route-c",
		EscalationRoute: "route-d", MaxIterations: 12, BudgetCurrency: "USD",
		RouteModel: "P/m-a", PlanRouteModel: "P/m-c", ReviewRouteModel: "P/m-b",
		AutoApproveTools: true, PromptOverlay: "be terse",
		Harness: "claude-cli", Environment: "node",
		Sources: []SourceEntry{{Source: SourceKindGitHub, RepoURL: "https://github.com/o/r.git", ConnectorID: "conn1"}},
		Destinations: []DestinationEntry{
			{DestinationID: "gh-dest-1", RepoURL: "https://github.com/o/r.git"},
			{DestinationID: "dest-1"},
		},
	})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	id, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "now add tests"})
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
		child.MaxIterations != 12 || child.AutoApproveTools != true || child.PromptOverlay != "be terse" ||
		child.Harness != "claude-cli" || child.Environment != "node" || child.RepoURL() != "https://github.com/o/r.git" ||
		child.ConnectorID() != "conn1" ||
		child.RouteModel != "P/m-a" || child.PlanRouteModel != "P/m-c" || child.ReviewRouteModel != "P/m-b" {
		t.Fatalf("child did not inherit parent settings: %+v", child)
	}
	if child.ParentMissionID != "parent" {
		t.Fatalf("child.ParentMissionID = %q, want %q", child.ParentMissionID, "parent")
	}
	if child.ParentContext() == "" {
		t.Fatal("child.ParentContext() should carry the parent's outcome digest")
	}
	if !strings.Contains(child.ParentContext(), "fix the login bug") {
		t.Fatalf("child.ParentContext() = %q, want it to mention the parent's goal", child.ParentContext())
	}
	if len(child.Destinations) != 0 {
		t.Fatalf("child.Destinations = %+v, want empty, destinations are a per-mission choice", child.Destinations)
	}
}

// TestCreateFollowUpAllowsFailedParent proves a failed (not just done)
// terminal parent is a valid follow-up base too.
func TestCreateFollowUpAllowsFailedParent(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Goal: "attempt one", Kind: "general", Phase: PhaseFailed, Status: StatusError})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	id, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "attempt two"})
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

// TestCreateFollowUpGoalOnly proves a call with neither attach nor
// brief produces the same sources a follow-up always had.
func TestCreateFollowUpGoalOnly(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Goal: "ship it", Kind: "general", Phase: PhaseDone, Status: StatusDone})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	id, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "ship more"})
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	child, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get child: %v", err)
	}
	for _, e := range child.Sources {
		if e.Source == SourceKindBrief || e.Source == SourceKindPDF {
			t.Fatalf("goal-only follow-up carries a %q source: %+v", e.Source, e)
		}
	}
	if child.ReferencedContext() != "" {
		t.Fatalf("child.ReferencedContext() = %q, want empty", child.ReferencedContext())
	}
}

// TestCreateFollowUpBriefRendersAsReferencedContext proves every brief
// field reaches the child's referenced context, and the parent lineage
// digest still travels separately.
func TestCreateFollowUpBriefRendersAsReferencedContext(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Goal: "rank the ideas", Kind: "general", Phase: PhaseDone, Status: StatusDone})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	brief := Brief{
		Objective:          "build the ranked idea",
		Scope:              "the thin slice that demos",
		NonGoals:           "no billing",
		AcceptanceCriteria: []string{"demo video recorded", "README maps criteria"},
		References:         []string{"ideas.md", "https://example.com/rules"},
	}
	id, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "build it", Brief: brief})
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	child, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get child: %v", err)
	}
	ref := child.ReferencedContext()
	for _, want := range []string{
		"Brief:", "Objective: build the ranked idea", "Scope: the thin slice that demos",
		"Non-goals: no billing", "Acceptance criteria:", "- demo video recorded",
		"- README maps criteria", "References:", "- ideas.md", "- https://example.com/rules",
	} {
		if !strings.Contains(ref, want) {
			t.Fatalf("ReferencedContext() = %q, missing %q", ref, want)
		}
	}
	if child.ParentContext() == "" {
		t.Fatal("child.ParentContext() should still carry the parent's outcome digest")
	}
	if strings.Contains(ref, child.ParentContext()) {
		t.Fatal("the parent lineage digest should not render as referenced context")
	}
}

// TestCreateFollowUpAttachCarriesParentFiles proves attached parent
// workspace files become "pdf" sources with their markdown, path, mime,
// and the parent id the provisioner copies from, reusing an
// ArtifactRefs id/mime when the parent has one for that path.
func TestCreateFollowUpAttachCarriesParentFiles(t *testing.T) {
	ws := t.TempDir()
	writeAttachFile(t, ws, "ideas.md", "# Ideas\n\nOne good one.\n")
	writeAttachFile(t, ws, "sub/notes.txt", "plain notes\n")

	store := newFakeStore()
	store.put("parent", Mission{
		ID: "parent", Goal: "rank the ideas", Kind: "general", Phase: PhaseDone, Status: StatusDone,
		Workspace:    ws,
		ArtifactRefs: []ArtifactRef{{ID: "att-1", Mime: "text/markdown", Name: "ideas.md"}},
	})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	id, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{
		Goal: "build it", Attach: []string{"ideas.md", "sub/notes.txt", " ideas.md "},
	})
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	child, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get child: %v", err)
	}
	got := child.Attachments()
	if len(got) != 2 {
		t.Fatalf("child.Attachments() = %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Name != "ideas.md" || got[0].ID != "att-1" || got[0].Mime != "text/markdown" ||
		got[0].MissionID != "parent" || !strings.Contains(got[0].Markdown, "One good one.") {
		t.Fatalf("first attachment = %+v, want the parent's ideas.md with its artifact ref reused", got[0])
	}
	if got[1].Name != "sub/notes.txt" || got[1].ID != "" || got[1].Mime != "text/plain" ||
		got[1].MissionID != "parent" || !strings.Contains(got[1].Markdown, "plain notes") {
		t.Fatalf("second attachment = %+v, want the parent's sub/notes.txt", got[1])
	}
}

// TestCreateFollowUpAttachRejectsBadPaths proves a missing, escaping,
// or absolute path fails with the path named and no mission created,
// and that more than maxFollowUpAttachments is refused.
func TestCreateFollowUpAttachRejectsBadPaths(t *testing.T) {
	ws := t.TempDir()
	writeAttachFile(t, ws, "ideas.md", "# Ideas\n")

	many := make([]string, 0, maxFollowUpAttachments+1)
	for i := 0; i <= maxFollowUpAttachments; i++ {
		many = append(many, fmt.Sprintf("file-%d.md", i))
	}
	cases := []struct {
		name    string
		attach  []string
		wantErr string
	}{
		{"missing", []string{"nope.md"}, "nope.md"},
		{"escaping", []string{"../outside"}, "../outside"},
		{"absolute", []string{"/etc/passwd"}, "/etc/passwd"},
		{"too many", many, "at most"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.put("parent", Mission{ID: "parent", Goal: "rank the ideas", Kind: "general", Phase: PhaseDone, Status: StatusDone, Workspace: ws})
			d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

			_, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "build it", Attach: tc.attach})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q should mention %q", err.Error(), tc.wantErr)
			}
			store.mu.Lock()
			count := len(store.missions)
			store.mu.Unlock()
			if count != 1 {
				t.Fatalf("store holds %d missions, want only the parent", count)
			}
		})
	}
}

// writeAttachFile writes rel under root, creating parent directories.
func writeAttachFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
