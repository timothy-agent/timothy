package missions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
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
		Harness: "claude-cli", Environment: "node", ExecutorSessionPolicy: SessionPolicyFresh,
		ToolAllowlist: []string{"shell", "write_file", "web_search"},
		Sources:       []SourceEntry{{Source: SourceKindGitHub, RepoURL: "https://github.com/o/r.git", ConnectorID: "conn1"}},
		Destinations: []DestinationEntry{
			{DestinationID: "gh-dest-1", RepoURL: "https://github.com/o/r.git"},
			{DestinationID: "dest-1"},
		},
	})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())
	// PromptOverlay now comes from the agent's current defaults, not the
	// parent's snapshot (ResolveDefaults' own precedence).
	d.SetResolveDeps(ResolveDeps{
		Agent: func(ctx context.Context, agentID string) (AgentDefaults, bool) {
			if agentID == "agent-1" {
				return AgentDefaults{PromptOverlay: "be terse"}, true
			}
			return AgentDefaults{}, false
		},
	})

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
	if child.ExecutorSessionPolicy != SessionPolicyFresh {
		t.Fatalf("child.ExecutorSessionPolicy = %q, want the parent's %q", child.ExecutorSessionPolicy, SessionPolicyFresh)
	}
	if !reflect.DeepEqual(child.ToolAllowlist, []string{"shell", "write_file", "web_search"}) {
		t.Fatalf("child.ToolAllowlist = %v, want the parent's", child.ToolAllowlist)
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

// TestCreateFollowUpResolvesDefaults proves CreateFollowUp routes
// through ResolveDefaults: the agent's current overlay applies,
// OriginKind is followup, the mission is attended with no permission
// timeout, MaxIterations still comes from the parent, and ReviewHarness
// carries over.
func TestCreateFollowUpResolvesDefaults(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{
		ID: "parent", Goal: "fix the login bug", Kind: "coding", Phase: PhaseDone, Status: StatusDone,
		AgentID: "agent-1", MaxIterations: 9, Harness: "claude-cli", ReviewHarness: "codex-cli",
	})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())
	d.SetResolveDeps(ResolveDeps{
		Agent: func(ctx context.Context, agentID string) (AgentDefaults, bool) {
			if agentID == "agent-1" {
				return AgentDefaults{PromptOverlay: "agent overlay"}, true
			}
			return AgentDefaults{}, false
		},
	})

	id, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "now add tests"})
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	child, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get child: %v", err)
	}
	if child.PromptOverlay != "agent overlay" {
		t.Fatalf("child.PromptOverlay = %q, want the agent's current overlay", child.PromptOverlay)
	}
	if child.OriginKind != OriginFollowup {
		t.Fatalf("child.OriginKind = %q, want %q", child.OriginKind, OriginFollowup)
	}
	if child.Unattended {
		t.Fatal("child.Unattended should be false: a follow-up is attended")
	}
	if child.PermissionTimeoutSeconds != nil {
		t.Fatalf("child.PermissionTimeoutSeconds = %v, want nil", child.PermissionTimeoutSeconds)
	}
	if child.MaxIterations != 9 {
		t.Fatalf("child.MaxIterations = %d, want the parent's 9", child.MaxIterations)
	}
	if child.ReviewHarness != "codex-cli" {
		t.Fatalf("child.ReviewHarness = %q, want the parent's codex-cli", child.ReviewHarness)
	}
}

// TestCreateFollowUpRouteGate proves a carried route with no usable
// provider fails through the D-100 gate and leaves no child mission.
func TestCreateFollowUpRouteGate(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{
		ID: "parent", Goal: "ship it", Kind: "general", Phase: PhaseDone, Status: StatusDone,
		Route: "dead",
	})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())
	d.SetResolveDeps(ResolveDeps{ResolveRoute: resolveFixture})

	_, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "ship more"})
	var unusable *RouteUnusableError
	if !errors.As(err, &unusable) {
		t.Fatalf("err = %v, want a RouteUnusableError", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.missions) != 1 {
		t.Fatalf("store holds %d missions, want only the parent", len(store.missions))
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

// TestCreateFollowUpRejectsEmptyGoal proves a blank goal is refused
// before the parent is even looked up.
func TestCreateFollowUpRejectsEmptyGoal(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Goal: "ship it", Kind: "general", Phase: PhaseDone, Status: StatusDone})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	_, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "   "})
	if err == nil || !strings.Contains(err.Error(), "goal is required") {
		t.Fatalf("err = %v, want goal is required", err)
	}
}

// TestCreateFollowUpAttachNeedsParentWorkspace proves attach is refused
// when the parent never got a workspace, with no mission created.
func TestCreateFollowUpAttachNeedsParentWorkspace(t *testing.T) {
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Goal: "ship it", Kind: "general", Phase: PhaseDone, Status: StatusDone})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	_, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "build it", Attach: []string{"ideas.md"}})
	if err == nil || !strings.Contains(err.Error(), "no workspace") {
		t.Fatalf("err = %v, want a no-workspace error", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.missions) != 1 {
		t.Fatalf("store holds %d missions, want only the parent", len(store.missions))
	}
}

// TestCreateFollowUpAttachRejectsSymlinkEscape proves a symlink inside
// the parent workspace pointing outside it is refused as an escape,
// not read.
func TestCreateFollowUpAttachRejectsSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	writeAttachFile(t, outside, "secret.md", "secret\n")
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(ws, "link.md")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Goal: "ship it", Kind: "general", Phase: PhaseDone, Status: StatusDone, Workspace: ws})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	_, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "build it", Attach: []string{"link.md"}})
	if err == nil || !strings.Contains(err.Error(), "outside the parent mission's workspace") {
		t.Fatalf("err = %v, want an outside-workspace error", err)
	}
}

// TestCreateFollowUpAttachBinaryFile proves a non-text file is carried
// with no markdown and an octet-stream mime, so it copies but never
// renders into a prompt.
func TestCreateFollowUpAttachBinaryFile(t *testing.T) {
	ws := t.TempDir()
	writeAttachFile(t, ws, "blob.bin", "\x00\x01\x02binary")
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Goal: "ship it", Kind: "general", Phase: PhaseDone, Status: StatusDone, Workspace: ws})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	id, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "build it", Attach: []string{"blob.bin"}})
	if err != nil {
		t.Fatalf("CreateFollowUp: %v", err)
	}
	child, _ := store.Get(context.Background(), id)
	got := child.Attachments()
	if len(got) != 1 || got[0].Markdown != "" || got[0].Mime != "application/octet-stream" {
		t.Fatalf("attachments = %+v, want one octet-stream entry with no markdown", got)
	}
}

// TestCreateFollowUpAttachRejectsOversizeFile proves a file over
// maxFollowUpAttachmentBytes is refused with no mission created.
func TestCreateFollowUpAttachRejectsOversizeFile(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "big.md"), make([]byte, maxFollowUpAttachmentBytes+1), 0o600); err != nil {
		t.Fatalf("write big.md: %v", err)
	}
	store := newFakeStore()
	store.put("parent", Mission{ID: "parent", Goal: "ship it", Kind: "general", Phase: PhaseDone, Status: StatusDone, Workspace: ws})
	d := NewDriver(store, followUpBlockedRunner(), nil, nil, &fakeSessionCreator{}, &fakeGranter{}, nil, nil, slog.Default())

	_, err := d.CreateFollowUp(context.Background(), "parent", FollowUpOptions{Goal: "build it", Attach: []string{"big.md"}})
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want an oversize error", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.missions) != 1 {
		t.Fatalf("store holds %d missions, want only the parent", len(store.missions))
	}
}

// TestBriefRenderSkipsBlankItems proves blank list entries and fields
// render nothing, and a brief of only blanks counts as zero.
func TestBriefRenderSkipsBlankItems(t *testing.T) {
	blank := Brief{Objective: "  ", AcceptanceCriteria: []string{" ", ""}, References: []string{""}}
	if !blank.IsZero() {
		t.Fatalf("Brief with only blanks should be zero: %+v", blank)
	}
	if got := blank.Render(); got != "" {
		t.Fatalf("Render() = %q, want empty", got)
	}
	partial := Brief{Objective: "ship", AcceptanceCriteria: []string{" ", "tests pass"}}
	if partial.IsZero() {
		t.Fatal("Brief with an objective should not be zero")
	}
	got := partial.Render()
	if got != "Objective: ship\nAcceptance criteria:\n- tests pass" {
		t.Fatalf("Render() = %q", got)
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

// inheritParentFixture is a terminal parent with every inheritable
// field set, for the InheritParent tests.
func inheritParentFixture() Mission {
	budget := 5.0
	return Mission{
		ID: "parent", Goal: "fix the login bug", Kind: KindCoding, Phase: PhaseDone, Status: StatusDone,
		AgentID: "agent-1", Route: "route-a", ReviewRoute: "route-b", PlanRoute: "route-c", EscalationRoute: "route-d",
		RouteModel: "P/m-a", PlanRouteModel: "P/m-c", ReviewRouteModel: "P/m-b",
		MaxIterations: 12, BudgetAmount: &budget, BudgetCurrency: "EUR",
		AutoApproveTools: true, AutoApprovePlan: true,
		Harness: "claude-cli", ReviewHarness: "codex-cli", Environment: "node",
		ExecutorSessionPolicy: SessionPolicyFresh, ToolAllowlist: []string{"shell", "write_file"},
		Flow:         FlowFull,
		Sources:      []SourceEntry{{Source: SourceKindGitHub, RepoURL: "https://github.com/o/r.git", ConnectorID: "conn1"}},
		Destinations: []DestinationEntry{{DestinationID: "dest-1"}},
	}
}

// TestInheritParentEmptyFieldsInherit proves a request carrying only a
// goal and sources resolves to exactly the chat follow-up request.
func TestInheritParentEmptyFieldsInherit(t *testing.T) {
	parent := inheritParentFixture()
	lineage := SourceEntry{Source: SourceKindMission, ID: ParentLineageID, MissionID: parent.ID}
	repo, _ := parent.repoSource()
	req := CreateRequest{Goal: "now add tests", ParentMissionID: parent.ID, Sources: []SourceEntry{lineage}}

	got := InheritParent(req, parent)
	want := FollowUpCreateRequest(parent, "now add tests", []SourceEntry{lineage, repo})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InheritParent =\n%+v\nwant\n%+v", got, want)
	}
}

// TestInheritParentExplicitWins proves every non-empty request field
// overrides the parent's value, including explicit false pointers.
func TestInheritParentExplicitWins(t *testing.T) {
	parent := inheritParentFixture()
	f, budget, timeout, unattended := false, 9.0, 30, true
	for _, tc := range []struct {
		name  string
		req   CreateRequest
		check func(CreateRequest) bool
	}{
		{"name", CreateRequest{Name: "n"}, func(r CreateRequest) bool { return r.Name == "n" }},
		{"kind", CreateRequest{Kind: KindGeneral}, func(r CreateRequest) bool { return r.Kind == KindGeneral }},
		{"agent", CreateRequest{AgentID: "agent-2"}, func(r CreateRequest) bool { return r.AgentID == "agent-2" }},
		{"route", CreateRequest{Route: "x"}, func(r CreateRequest) bool { return r.Route == "x" }},
		{"review_route", CreateRequest{ReviewRoute: "x"}, func(r CreateRequest) bool { return r.ReviewRoute == "x" }},
		{"plan_route", CreateRequest{PlanRoute: "x"}, func(r CreateRequest) bool { return r.PlanRoute == "x" }},
		{"escalation_route", CreateRequest{EscalationRoute: "x"}, func(r CreateRequest) bool { return r.EscalationRoute == "x" }},
		{"route_model", CreateRequest{RouteModel: "Q/x"}, func(r CreateRequest) bool { return r.RouteModel == "Q/x" }},
		{"plan_route_model", CreateRequest{PlanRouteModel: "Q/x"}, func(r CreateRequest) bool { return r.PlanRouteModel == "Q/x" }},
		{"review_route_model", CreateRequest{ReviewRouteModel: "Q/x"}, func(r CreateRequest) bool { return r.ReviewRouteModel == "Q/x" }},
		{"max_iterations", CreateRequest{MaxIterations: 3}, func(r CreateRequest) bool { return r.MaxIterations == 3 }},
		{"budget_amount", CreateRequest{BudgetAmount: &budget}, func(r CreateRequest) bool { return *r.BudgetAmount == 9 }},
		{"budget_currency", CreateRequest{BudgetCurrency: "USD"}, func(r CreateRequest) bool { return r.BudgetCurrency == "USD" }},
		{"auto_approve_tools false", CreateRequest{AutoApproveTools: &f}, func(r CreateRequest) bool { return !*r.AutoApproveTools }},
		{"auto_approve_plan false", CreateRequest{AutoApprovePlan: &f}, func(r CreateRequest) bool { return !*r.AutoApprovePlan }},
		{"harness native", CreateRequest{Harness: "native"}, func(r CreateRequest) bool { return r.Harness == "native" }},
		{"review_harness native", CreateRequest{ReviewHarness: "native"}, func(r CreateRequest) bool { return r.ReviewHarness == "native" }},
		{"environment", CreateRequest{Environment: "go"}, func(r CreateRequest) bool { return r.Environment == "go" }},
		{"executor_session_policy", CreateRequest{ExecutorSessionPolicy: SessionPolicyResume}, func(r CreateRequest) bool { return r.ExecutorSessionPolicy == SessionPolicyResume }},
		{"has_plan", CreateRequest{HasPlan: true}, func(r CreateRequest) bool { return r.HasPlan }},
		{"flow", CreateRequest{Flow: string(FlowLight)}, func(r CreateRequest) bool { return r.Flow == string(FlowLight) }},
		{"permission_timeout", CreateRequest{PermissionTimeoutSeconds: &timeout}, func(r CreateRequest) bool { return *r.PermissionTimeoutSeconds == 30 }},
		{"unattended", CreateRequest{Unattended: &unattended}, func(r CreateRequest) bool { return *r.Unattended }},
		{"tool_allowlist empty", CreateRequest{ToolAllowlist: []string{}}, func(r CreateRequest) bool { return r.ToolAllowlist != nil && len(r.ToolAllowlist) == 0 }},
		{"origin_kind", CreateRequest{OriginKind: OriginChat}, func(r CreateRequest) bool { return r.OriginKind == OriginChat }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.Goal, tc.req.ParentMissionID = "g", parent.ID
			if got := InheritParent(tc.req, parent); !tc.check(got) {
				t.Fatalf("explicit %s lost: %+v", tc.name, got)
			}
		})
	}
}

// TestInheritParentNeverCopiesDestinations proves destinations come
// from the request alone (D-061), empty or not.
func TestInheritParentNeverCopiesDestinations(t *testing.T) {
	parent := inheritParentFixture()
	if got := InheritParent(CreateRequest{Goal: "g"}, parent); len(got.Destinations) != 0 {
		t.Fatalf("Destinations = %+v, want none from the parent", got.Destinations)
	}
	own := []DestinationEntry{{DestinationID: "dest-2"}}
	if got := InheritParent(CreateRequest{Goal: "g", Destinations: own}, parent); !reflect.DeepEqual(got.Destinations, own) {
		t.Fatalf("Destinations = %+v, want the request's %+v", got.Destinations, own)
	}
}

// TestInheritParentLightBodyOverridesParentFullFlow proves light=true
// drops the parent's full flow so ResolveDefaults maps it to light,
// while an omitted light keeps the parent's flow.
func TestInheritParentLightBodyOverridesParentFullFlow(t *testing.T) {
	parent := Mission{ID: "parent", Kind: KindGeneral, Flow: FlowFull, Phase: PhaseDone}
	got := InheritParent(CreateRequest{Goal: "g", Light: true}, parent)
	if !got.Light || got.Flow != "" {
		t.Fatalf("light body: Light=%v Flow=%q, want true and empty", got.Light, got.Flow)
	}
	m, err := ResolveDefaults(context.Background(), got, ResolveDeps{})
	if err != nil || m.Flow != FlowLight {
		t.Fatalf("resolved flow = %q err=%v, want light", m.Flow, err)
	}

	parent.Flow = FlowLight
	if got := InheritParent(CreateRequest{Goal: "g"}, parent); got.Flow != string(FlowLight) {
		t.Fatalf("omitted light: Flow = %q, want the parent's light", got.Flow)
	}
}

// TestInheritParentRepoSourceInherited proves the parent's clone source
// is carried after the request's own sources when the request names no
// repo.
func TestInheritParentRepoSourceInherited(t *testing.T) {
	parent := inheritParentFixture()
	lineage := SourceEntry{Source: SourceKindMission, ID: ParentLineageID, MissionID: parent.ID}
	got := InheritParent(CreateRequest{Goal: "g", Sources: []SourceEntry{lineage}}, parent)
	want := []SourceEntry{lineage, parent.Sources[0]}
	if !reflect.DeepEqual(got.Sources, want) {
		t.Fatalf("Sources = %+v, want %+v", got.Sources, want)
	}
}

// TestInheritParentBodyRepoWins proves a request repo source replaces
// the parent's instead of adding a second one.
func TestInheritParentBodyRepoWins(t *testing.T) {
	parent := inheritParentFixture()
	own := SourceEntry{Source: SourceKindGitHub, RepoURL: "https://github.com/o/other.git", ConnectorID: "conn2"}
	got := InheritParent(CreateRequest{Goal: "g", Sources: []SourceEntry{own}}, parent)
	if !reflect.DeepEqual(got.Sources, []SourceEntry{own}) {
		t.Fatalf("Sources = %+v, want only the request's repo", got.Sources)
	}
}
