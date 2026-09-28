package missions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// TestParsePlanEvidenceOnlyUnit covers D-123 (issue #950): a unit
// marked evidence_only needs no artifacts, since its deliverable is a
// side effect check_cmd alone observes; a unit that merely forgot its
// artifacts (the flag left false) is still rejected, so the explicit
// flag is the only way in, never inference from an empty list.
func TestParsePlanEvidenceOnlyUnit(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantErr string
	}{
		{
			"evidence-only unit accepted",
			`{"units":[{"title":"File GitHub issues","evidence_only":true,"criteria":["a","b"],"check_cmd":"gh issue list --json number --jq 'length' | awk '$1>=2'"}]}`,
			"",
		},
		{
			"accidental empty-artifact unit still rejected",
			`{"units":[{"title":"File GitHub issues","criteria":["a","b"],"check_cmd":"gh issue list --json number --jq 'length' | awk '$1>=2'"}]}`,
			`unit "File GitHub issues" must list at least one workspace-relative artifact`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := parsePlan(tc.json)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parsePlan err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePlan: %v", err)
			}
			if !plan.Units[0].EvidenceOnly {
				t.Fatalf("EvidenceOnly = %v, want true", plan.Units[0].EvidenceOnly)
			}
			if len(plan.Units[0].Artifacts) != 0 {
				t.Fatalf("Artifacts = %v, want none", plan.Units[0].Artifacts)
			}
		})
	}
}

// TestProbeCheckCmdsEvidenceOnlyGateAlreadyPasses confirms the D-123
// evidence-only unit is not exempt from the plan-acceptance rule that
// a gate must fail before the work exists: probeCheckCmds runs over
// every unit regardless of EvidenceOnly.
func TestProbeCheckCmdsEvidenceOnlyGateAlreadyPasses(t *testing.T) {
	m := Mission{ID: "m1", Kind: KindGeneral}
	plan := Plan{Units: []PlanUnit{{
		Title: "File GitHub issues", EvidenceOnly: true,
		CheckCmd: "gh issue list --json number --jq 'length' | awk '$1>=2'",
	}}}
	r := &nativeRunner{log: slog.Default(), sandbox: scriptedSandbox(map[string]int{
		"gh issue list --json number --jq 'length' | awk '$1>=2'": 0,
	}, "2\n")}
	err := r.probeCheckCmds(context.Background(), m, plan)
	if err == nil || !strings.Contains(err.Error(), "already exits 0") {
		t.Fatalf("probeCheckCmds = %v, want an evidence-only gate that already passes rejected", err)
	}
}

// f78f7fffTwoUnitPlan and f78f7fffOneUnitPlan reproduce mission
// f78f7fff's real plan pair (issue #950): the goal asked to write two
// local backlog files and then create and close GitHub issues from
// them; the first plan's second unit (the GitHub work) listed the
// first unit's own artifacts, so checkOwnArtifacts rejected it, and the
// planner's resubmission dropped the unit instead of marking it
// evidence-only or reporting infeasible.
const (
	f78f7fffTwoUnitPlan = `{"units":[
		{"title":"Write backlog files","artifacts":["backlog/a.md","backlog/b.md"],"criteria":["c1","c2"],"check_cmd":"grep -q x backlog/a.md"},
		{"title":"Create and close GitHub issues","artifacts":["backlog/a.md","backlog/b.md"],"criteria":["c1","c2"],"check_cmd":"gh issue list --json number --jq 'length' | awk '$1>=2'"}
	]}`
	f78f7fffOneUnitPlan = `{"units":[
		{"title":"Write backlog files","artifacts":["backlog/a.md","backlog/b.md"],"criteria":["c1","c2"],"check_cmd":"grep -q x backlog/a.md"}
	]}`
)

// TestPlanSessionRecordsScopeDropped replays mission f78f7fff's plan
// pair: PlanSession must still accept the resubmission but record the
// dropped title on the plan, and the reviewer packet must carry it.
func TestPlanSessionRecordsScopeDropped(t *testing.T) {
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, f78f7fffTwoUnitPlan)},
		{toolEndEvent(planToolName, f78f7fffOneUnitPlan)},
	}}
	r := newTestRunner(agent)
	plan, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Kind: KindGeneral}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if len(plan.Units) != 1 {
		t.Fatalf("plan units = %+v, want the resubmission's single unit", plan.Units)
	}
	if len(plan.ScopeDropped) != 1 || plan.ScopeDropped[0] != "Create and close GitHub issues" {
		t.Fatalf("ScopeDropped = %v, want the dropped unit's title", plan.ScopeDropped)
	}

	// The reviewer packet must carry it too (issue #950's second AC).
	packet := ReviewPacket{Plan: plan, ScopeDropped: plan.ScopeDropped}
	content := renderReviewContent(packet)
	if !strings.Contains(content, "Create and close GitHub issues") {
		t.Fatalf("review content = %q, want it to name the dropped unit", content)
	}
}

// TestRegressionF78f7fffPlanPairProducesScopeDroppedEvent is the
// regression fixture the issue asks for: mission f78f7fff's real plan
// pair, driven through the actual Driver+nativeRunner (not a scripted
// fake plan), must record mission.plan_scope_dropped and leave the
// dropped title reachable by the review packet the driver builds next.
func TestRegressionF78f7fffPlanPairProducesScopeDroppedEvent(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Route: "default", Kind: "general", Phase: PhasePlan, Status: StatusWorking, MaxIterations: 8})
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{
		{toolEndEvent(planToolName, f78f7fffTwoUnitPlan)},
		{toolEndEvent(planToolName, f78f7fffOneUnitPlan)},
	}}
	d := testDriver(store, newTestRunner(agent))

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	events, err := store.Events(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var payload map[string]any
	for _, e := range events {
		if e.Kind == "mission.plan_scope_dropped" {
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("unmarshal plan_scope_dropped payload: %v", err)
			}
		}
	}
	if payload == nil {
		t.Fatal("no mission.plan_scope_dropped event recorded")
	}
	titles, _ := payload["titles"].([]any)
	if len(titles) != 1 || titles[0] != "Create and close GitHub issues" {
		t.Fatalf("plan_scope_dropped payload titles = %+v, want the dropped GitHub-work unit", payload["titles"])
	}

	m, err := store.Get(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	idx := []int{0}
	packet, _, err := d.fullReviewPacket(context.Background(), m, idx, m.Plan.Units)
	if err != nil {
		t.Fatalf("fullReviewPacket: %v", err)
	}
	if len(packet.ScopeDropped) != 1 || packet.ScopeDropped[0] != "Create and close GitHub issues" {
		t.Fatalf("review packet ScopeDropped = %v, want the dropped title", packet.ScopeDropped)
	}
}

// TestPlanSessionNoScopeDroppedWhenNothingRejected confirms a plan
// accepted on the first turn never sets ScopeDropped: only a rejected
// first attempt followed by a resubmission can drop a unit.
func TestPlanSessionNoScopeDroppedWhenNothingRejected(t *testing.T) {
	good := `{"units":[{"title":"only unit","artifacts":["out.md"],"criteria":["c1","c2"],"check_cmd":"grep -q x out.md"}]}`
	agent := &scriptedAgent{batches: [][]stream.StreamEvent{{toolEndEvent(planToolName, good)}}}
	r := newTestRunner(agent)
	plan, err := r.PlanSession(context.Background(), Mission{ID: "m1", Route: "default", Kind: KindGeneral}, "")
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if len(plan.ScopeDropped) != 0 {
		t.Fatalf("ScopeDropped = %v, want none", plan.ScopeDropped)
	}
}

// TestDriverRecordsPlanScopeDroppedEvent confirms the driver records a
// mission.plan_scope_dropped event naming the dropped titles when
// PlanSession's returned plan carries them (issue #950's second AC).
func TestDriverRecordsPlanScopeDroppedEvent(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		plans: []Plan{{
			Units:        []PlanUnit{{Title: "only unit"}},
			ScopeDropped: []string{"Create and close GitHub issues"},
		}},
	}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	events, err := store.Events(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var payload map[string]any
	for _, e := range events {
		if e.Kind == "mission.plan_scope_dropped" {
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("unmarshal plan_scope_dropped payload: %v", err)
			}
		}
	}
	if payload == nil {
		t.Fatal("no mission.plan_scope_dropped event recorded")
	}
	titles, ok := payload["titles"].([]any)
	if !ok || len(titles) != 1 || titles[0] != "Create and close GitHub issues" {
		t.Fatalf("plan_scope_dropped payload titles = %+v, want the dropped title", payload["titles"])
	}
}

// TestDriverNoScopeDroppedEventWhenPlanClean confirms a plan with no
// ScopeDropped never emits the event.
func TestDriverNoScopeDroppedEventWhenPlanClean(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{plans: []Plan{{Units: []PlanUnit{{Title: "only unit"}}}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	events, err := store.Events(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, e := range events {
		if e.Kind == "mission.plan_scope_dropped" {
			t.Fatalf("unexpected mission.plan_scope_dropped event: %s", e.Payload)
		}
	}
}

// TestDriverReviewPacketCarriesScopeDropped confirms the review packet
// (both full and findings-only rounds get it via the driver's packet
// builders) carries the plan's ScopeDropped titles.
func TestDriverReviewPacketCarriesScopeDropped(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseProve, Status: StatusWorking, MaxIterations: 3,
		Plan: Plan{
			Units:        []PlanUnit{{Title: "u1", HarnessPassed: true}},
			ScopeDropped: []string{"Create and close GitHub issues"},
		},
	})
	runner := &scriptedRunner{reviewVerdicts: []ReviewVerdict{{Approved: true}}}
	d := testDriver(store, runner)
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if got := runner.reviewCalls[0].ScopeDropped; len(got) != 1 || got[0] != "Create and close GitHub issues" {
		t.Fatalf("review packet ScopeDropped = %v, want the plan's dropped title", got)
	}
}

// TestDriverFindingsOnlyPacketCarriesScopeDropped mirrors the above for
// a findings-only round (D-096): ScopeDropped must not be lost when the
// packet omits Plan/Goal/Listing.
func TestDriverFindingsOnlyPacketCarriesScopeDropped(t *testing.T) {
	root, base := codingWorktree(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := NewDriver(newFakeStore(), &scriptedRunner{}, NewWorkspace("", nil, log), nil, nil, fakeSandboxExec, nil, log)
	m := Mission{
		ID: "m1", Kind: "coding", Workspace: root, BaseCommit: base,
		Plan: Plan{
			Units:            []PlanUnit{{Title: "u1", HarnessPassed: true, Artifacts: []string{"seed.txt"}}},
			ScopeDropped:     []string{"Create and close GitHub issues"},
			LastReviewCommit: base,
		},
	}
	open := []Finding{{ID: "f1", Title: "gap", File: "seed.txt", Unit: 0, Severity: "blocking"}}
	packet, _, err := d.findingsReviewPacket(context.Background(), m, open)
	if err != nil {
		t.Fatalf("findingsReviewPacket: %v", err)
	}
	if got := packet.ScopeDropped; len(got) != 1 || got[0] != "Create and close GitHub issues" {
		t.Fatalf("findings-only review packet ScopeDropped = %v, want the plan's dropped title", got)
	}
}
