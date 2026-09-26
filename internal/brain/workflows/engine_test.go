package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// fakeEngineStore is an in-memory engineStore for scripting Engine
// scenarios without a real Postgres pool — mirrors missions/driver_test.go's
// fakeStore approach.
type fakeEngineStore struct {
	mu        sync.Mutex
	workflows map[string]Workflow
	runs      map[string]Run
	events    map[string][]RunEvent
	seq       map[string]int64
	// failNextApply, when set, fails the next ApplyRunTransition and clears.
	failNextApply error
	// failAfterApply, when set, applies the next ApplyRunTransition, then
	// returns it and clears: a commit whose result was lost.
	failAfterApply error
	// linked reports whether a mission links to a run; nil means none.
	linked func(runID string) bool
}

func newFakeEngineStore() *fakeEngineStore {
	return &fakeEngineStore{
		workflows: map[string]Workflow{},
		runs:      map[string]Run{},
		events:    map[string][]RunEvent{},
		seq:       map[string]int64{},
	}
}

func (f *fakeEngineStore) putWorkflow(id string, def Definition, enabled bool) {
	raw, _ := json.Marshal(def)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workflows[id] = Workflow{ID: id, Definition: raw, Enabled: enabled}
}

func (f *fakeEngineStore) Get(ctx context.Context, id string) (Workflow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workflows[id]
	if !ok {
		return Workflow{}, ErrNotFound
	}
	return w, nil
}

func (f *fakeEngineStore) CreateRun(ctx context.Context, workflowID, currentStep string, runContext map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("run-%d", len(f.runs)+1)
	f.runs[id] = Run{ID: id, WorkflowID: workflowID, Status: "running", CurrentStep: currentStep, Context: runContext, CreatedAt: time.Now()}
	return id, nil
}

func (f *fakeEngineStore) GetRun(ctx context.Context, id string) (Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	return r, nil
}

func (f *fakeEngineStore) ApplyRunTransition(ctx context.Context, id string, t RunTransition) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failNextApply; err != nil {
		f.failNextApply = nil
		return err
	}
	r, ok := f.runs[id]
	if !ok {
		return ErrNotFound
	}
	r.Status, r.CurrentStep = t.Status, t.CurrentStep
	f.runs[id] = r
	for _, ev := range t.Events {
		f.seq[id]++
		payload, _ := json.Marshal(ev.Payload)
		f.events[id] = append(f.events[id], RunEvent{RunID: id, Seq: f.seq[id], Kind: ev.Kind, Payload: payload})
	}
	if err := f.failAfterApply; err != nil {
		f.failAfterApply = nil
		return err
	}
	return nil
}

func (f *fakeEngineStore) RunsWithoutMissions(ctx context.Context, cutoff time.Time) ([]Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Run
	for _, r := range f.runs {
		if r.Status == "running" && r.CreatedAt.Before(cutoff) && (f.linked == nil || !f.linked(r.ID)) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Run) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (f *fakeEngineStore) CountEdgeFirings(ctx context.Context, runID, from, on, to string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, ev := range f.events[runID] {
		if ev.Kind != "edge.taken" {
			continue
		}
		var p struct {
			From string `json:"from"`
			On   string `json:"on"`
			To   string `json:"to"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		if p.From == from && p.On == on && p.To == to {
			n++
		}
	}
	return n, nil
}

func (f *fakeEngineStore) RunEvents(ctx context.Context, runID string) ([]RunEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RunEvent(nil), f.events[runID]...), nil
}

func (f *fakeEngineStore) eventKinds(runID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, ev := range f.events[runID] {
		out = append(out, ev.Kind)
	}
	return out
}

// fakeSpawner is an in-memory missionSpawner + missionEvents fake.
type fakeSpawner struct {
	mu        sync.Mutex
	missions  []missions.Mission
	createErr error
}

func (f *fakeSpawner) Create(ctx context.Context, m missions.Mission) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m.ID = fmt.Sprintf("mission-%d", len(f.missions)+1)
	f.missions = append(f.missions, m)
	return m.ID, nil
}

func (f *fakeSpawner) Events(ctx context.Context, id string) ([]missions.Event, error) {
	return nil, nil
}

func (f *fakeSpawner) Get(ctx context.Context, id string) (missions.Mission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.missions {
		if m.ID == id {
			return m, nil
		}
	}
	return missions.Mission{}, missions.ErrNotFound
}

func (f *fakeSpawner) WorkflowChild(ctx context.Context, runID, parentMissionID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.missions {
		if m.WorkflowRunID == runID && m.ParentMissionID == parentMissionID {
			return m.ID, nil
		}
	}
	return "", nil
}

func (f *fakeSpawner) last() missions.Mission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.missions[len(f.missions)-1]
}

func (f *fakeSpawner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.missions)
}

func testEngine(store *fakeEngineStore, spawner *fakeSpawner) *Engine {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewEngine(store, spawner, spawner, log)
}

func coderQADefinition() Definition {
	d := Definition{
		Entry: "coder",
		Steps: map[string]Step{
			"coder": {Goal: "write the code", Kind: "coding"},
			"qa":    {Goal: "review {{outcome}}", Kind: "general"},
		},
		Edges: []Edge{
			{From: "coder", On: "mission.done", To: "qa", MaxIterations: 5},
			{From: "qa", On: "mission.done", To: endStep, MaxIterations: 1},
		},
	}
	_ = d.Validate()
	return d
}

func TestStartRunSpawnsEntryStep(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, err := e.StartRun(context.Background(), "wf1", map[string]string{"K": "v"})
	if err != nil {
		t.Fatalf("StartRun() = %v", err)
	}
	if spawner.count() != 1 {
		t.Fatalf("spawned missions = %d, want 1", spawner.count())
	}
	m := spawner.last()
	if m.Goal != "write the code" || m.WorkflowRunID != runID || m.WorkflowStep != "coder" {
		t.Fatalf("spawned mission = %+v", m)
	}
	run, _ := store.GetRun(context.Background(), runID)
	if run.Status != "running" || run.CurrentStep != "coder" {
		t.Fatalf("run = %+v", run)
	}
}

// TestStartRunForcesAutoApprovePlanTrue confirms D-087 (issue #456): a
// workflow-spawned mission always gets AutoApprovePlan=true. Step has
// no field for it at all, so this really confirms spawnStep's own
// missions.Mission{} literal sets it explicitly rather than leaving it
// at the Go zero-value false.
func TestStartRunForcesAutoApprovePlanTrue(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	if _, err := e.StartRun(context.Background(), "wf1", map[string]string{"K": "v"}); err != nil {
		t.Fatalf("StartRun() = %v", err)
	}
	if m := spawner.last(); !m.AutoApprovePlan {
		t.Fatalf("spawned mission AutoApprovePlan = false, want true (unattended missions have nobody to approve a plan)")
	}
}

// TestStartRunSpawnsUnattendedWorkflowMission covers issue #817: a
// workflow-spawned mission records origin_kind=workflow and is
// unattended, so permission asks deny instead of parking forever.
func TestStartRunSpawnsUnattendedWorkflowMission(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	if _, err := e.StartRun(context.Background(), "wf1", map[string]string{"K": "v"}); err != nil {
		t.Fatalf("StartRun() = %v", err)
	}
	m := spawner.last()
	if m.OriginKind != missions.OriginWorkflow || !m.Unattended {
		t.Fatalf("spawned mission origin_kind=%q unattended=%v, want workflow true", m.OriginKind, m.Unattended)
	}
	if m.PermissionTimeoutSeconds == nil || *m.PermissionTimeoutSeconds != 1800 {
		t.Fatalf("spawned mission permission_timeout_seconds = %v, want 1800", m.PermissionTimeoutSeconds)
	}
}

func TestStartRunRefusesDisabledWorkflow(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), false)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	if _, err := e.StartRun(context.Background(), "wf1", nil); err == nil {
		t.Fatal("StartRun() = nil, want error for disabled workflow")
	}
	if spawner.count() != 0 {
		t.Fatalf("spawned missions = %d, want 0", spawner.count())
	}
}

// TestStartRunPausesOnSpawnValidationError covers D-071: a step whose
// spawned mission Driver.Create rejects (here, missions.ErrInvalidMission
// from a destination id validation a workflow step can never itself
// resolve) must pause the run with the validation error as the reason,
// not silently create an invalid mission or crash the engine.
func TestStartRunPausesOnSpawnValidationError(t *testing.T) {
	def := Definition{
		Entry: "coder",
		Steps: map[string]Step{
			"coder": {Goal: "write the code", Kind: "coding", DestinationIDs: []string{"unknown-dest"}},
		},
	}
	if err := def.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (destination_ids shape is valid at the definition level)", err)
	}
	store := newFakeEngineStore()
	store.putWorkflow("wf1", def, true)
	spawner := &fakeSpawner{createErr: fmt.Errorf("driver: create: %w: unknown or disabled destination id(s): unknown-dest", missions.ErrInvalidMission)}
	e := testEngine(store, spawner)

	runID, err := e.StartRun(context.Background(), "wf1", nil)
	if err == nil {
		t.Fatal("StartRun() = nil error, want the spawn validation error surfaced")
	}
	if spawner.count() != 0 {
		t.Fatalf("spawned missions = %d, want 0 (Create rejected it)", spawner.count())
	}
	run, getErr := store.GetRun(context.Background(), runID)
	if getErr != nil {
		t.Fatalf("GetRun: %v", getErr)
	}
	if run.Status != "paused" {
		t.Fatalf("run status = %s, want paused", run.Status)
	}
	found := false
	for _, k := range store.eventKinds(runID) {
		if k == "run.paused" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want run.paused", store.eventKinds(runID))
	}
}

func TestOnMissionTerminalAdvancesToNextStep(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	coderMission := spawner.last()
	coderMission.ID = "coder-mission"
	coderMission.Phase = missions.PhaseDone

	if err := e.OnMissionTerminal(context.Background(), coderMission); err != nil {
		t.Fatalf("OnMissionTerminal: %v", err)
	}

	if spawner.count() != 2 {
		t.Fatalf("spawned missions = %d, want 2 (coder + qa)", spawner.count())
	}
	qa := spawner.last()
	if qa.WorkflowStep != "qa" || qa.ParentMissionID != "coder-mission" {
		t.Fatalf("qa mission = %+v", qa)
	}
	run, _ := store.GetRun(context.Background(), runID)
	if run.CurrentStep != "qa" || run.Status != "running" {
		t.Fatalf("run = %+v", run)
	}
}

func TestOnMissionTerminalEndsRunOnToEnd(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	run, _ := store.GetRun(context.Background(), runID)
	run.CurrentStep = "qa"
	store.runs[runID] = run

	qaMission := missions.Mission{ID: "qa-mission", WorkflowRunID: runID, WorkflowStep: "qa", Phase: missions.PhaseDone}
	if err := e.OnMissionTerminal(context.Background(), qaMission); err != nil {
		t.Fatalf("OnMissionTerminal: %v", err)
	}

	run, _ = store.GetRun(context.Background(), runID)
	if run.Status != "done" {
		t.Fatalf("run status = %s, want done", run.Status)
	}
}

func TestOnMissionTerminalPausesOnNoMatchingEdge(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	coderMission := spawner.last()
	coderMission.ID = "coder-mission"
	coderMission.Phase = missions.PhaseFailed // no edge wired for coder+mission.failed

	if err := e.OnMissionTerminal(context.Background(), coderMission); err != nil {
		t.Fatalf("OnMissionTerminal: %v", err)
	}

	run, _ := store.GetRun(context.Background(), runID)
	if run.Status != "paused" {
		t.Fatalf("run status = %s, want paused", run.Status)
	}
	found := false
	for _, k := range store.eventKinds(runID) {
		if k == "run.paused" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want run.paused", store.eventKinds(runID))
	}
}

func TestOnMissionTerminalCapExceededFailsRun(t *testing.T) {
	def := coderQADefinition()
	// Rewrite the coder->qa edge with a cap of 1 for this test.
	def.Edges[0].MaxIterations = 1
	store := newFakeEngineStore()
	store.putWorkflow("wf1", def, true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)

	// Fire the coder->qa edge once (allowed): advances to qa.
	coder1 := spawner.last()
	coder1.ID = "coder-1"
	coder1.Phase = missions.PhaseDone
	if err := e.OnMissionTerminal(context.Background(), coder1); err != nil {
		t.Fatalf("OnMissionTerminal: %v", err)
	}

	// Manually rewind the run back to "coder" to simulate a second
	// coder mission finishing and trying to take the SAME edge again —
	// this must now exceed the cap of 1.
	run, _ := store.GetRun(context.Background(), runID)
	run.CurrentStep = "coder"
	store.runs[runID] = run

	coder2 := missions.Mission{ID: "coder-2", WorkflowRunID: runID, WorkflowStep: "coder", Phase: missions.PhaseDone}
	if err := e.OnMissionTerminal(context.Background(), coder2); err != nil {
		t.Fatalf("OnMissionTerminal: %v", err)
	}

	run, _ = store.GetRun(context.Background(), runID)
	if run.Status != "failed" {
		t.Fatalf("run status = %s, want failed (cap exceeded)", run.Status)
	}
	found := false
	for _, k := range store.eventKinds(runID) {
		if k == "run.cap_exceeded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want run.cap_exceeded", store.eventKinds(runID))
	}
}

func TestOnMissionTerminalIgnoresNonRunningRun(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	run, _ := store.GetRun(context.Background(), runID)
	run.Status = "cancelled"
	store.runs[runID] = run

	coderMission := spawner.last()
	coderMission.ID = "coder-mission"
	coderMission.Phase = missions.PhaseDone
	if err := e.OnMissionTerminal(context.Background(), coderMission); err != nil {
		t.Fatalf("OnMissionTerminal: %v", err)
	}

	if spawner.count() != 1 {
		t.Fatalf("spawned missions = %d, want 1 (no further spawn on a non-running run)", spawner.count())
	}
}

func TestOnMissionTerminalRecordsUnknownPlaceholderWarning(t *testing.T) {
	def := Definition{
		Entry: "coder",
		Steps: map[string]Step{
			"coder": {Goal: "write code", Kind: "coding"},
			"qa":    {Goal: "check {{context.MISSING}}", Kind: "general"},
		},
		Edges: []Edge{
			{From: "coder", On: "mission.done", To: "qa", MaxIterations: 1},
		},
	}
	_ = def.Validate()
	store := newFakeEngineStore()
	store.putWorkflow("wf1", def, true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	coderMission := spawner.last()
	coderMission.ID = "coder-mission"
	coderMission.Phase = missions.PhaseDone
	if err := e.OnMissionTerminal(context.Background(), coderMission); err != nil {
		t.Fatalf("OnMissionTerminal: %v", err)
	}

	found := false
	for _, k := range store.eventKinds(runID) {
		if k == "run.warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want run.warning for unknown placeholder", store.eventKinds(runID))
	}
	qa := spawner.last()
	if qa.Goal != "check " {
		t.Fatalf("qa goal = %q, want unknown placeholder rendered empty", qa.Goal)
	}
}

// TestOnMissionTerminalFailedSpawnKeepsCurrentStep: the placeholder
// warning written before a failed Create must not advance the run.
func TestOnMissionTerminalFailedSpawnKeepsCurrentStep(t *testing.T) {
	def := Definition{
		Entry: "coder",
		Steps: map[string]Step{
			"coder": {Goal: "write code", Kind: "coding"},
			"qa":    {Goal: "check {{context.MISSING}}", Kind: "general"},
		},
		Edges: []Edge{
			{From: "coder", On: "mission.done", To: "qa", MaxIterations: 1},
		},
	}
	_ = def.Validate()
	store := newFakeEngineStore()
	store.putWorkflow("wf1", def, true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, err := e.StartRun(context.Background(), "wf1", nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	coderMission := spawner.last()
	coderMission.ID = "coder-mission"
	coderMission.Phase = missions.PhaseDone
	spawner.createErr = errors.New("create failed")
	if err := e.OnMissionTerminal(context.Background(), coderMission); err != nil {
		t.Fatalf("OnMissionTerminal: %v", err)
	}

	run, _ := store.GetRun(context.Background(), runID)
	if run.Status != "paused" || run.CurrentStep != "coder" {
		t.Fatalf("run = %s/%s, want paused on coder", run.Status, run.CurrentStep)
	}
	kinds := store.eventKinds(runID)
	if !slices.Contains(kinds, "run.warning") || slices.Contains(kinds, "edge.taken") {
		t.Fatalf("events = %v, want run.warning and no edge.taken", kinds)
	}
}

// TestStartRunResolvesStepThroughSharedDefaults covers issue #816: a
// step's mission gets its agent's route/review route/overlay and the
// harness precedence chain, not just a default route.
func TestStartRunResolvesStepThroughSharedDefaults(t *testing.T) {
	store := newFakeEngineStore()
	d := Definition{
		Entry: "coder",
		Steps: map[string]Step{"coder": {Goal: "write the code", Kind: "coding", AgentID: "agent-1"}},
		Edges: []Edge{{From: "coder", On: "mission.done", To: endStep, MaxIterations: 1}},
	}
	_ = d.Validate()
	store.putWorkflow("wf1", d, true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)
	e.SetResolveDeps(missions.ResolveDeps{
		Agent: func(_ context.Context, id string) (missions.AgentDefaults, bool) {
			if id != "agent-1" {
				return missions.AgentDefaults{}, false
			}
			return missions.AgentDefaults{Route: "agent-route", ReviewRoute: "agent-review", PromptOverlay: "overlay", Harness: "codex-cli"}, true
		},
		RouteForRole:          func(context.Context, string) string { return "default" },
		CodingExecutorDefault: func(context.Context) string { return "claude-cli" },
	})

	if _, err := e.StartRun(context.Background(), "wf1", nil); err != nil {
		t.Fatalf("StartRun() = %v", err)
	}
	m := spawner.last()
	if m.Route != "agent-route" || m.ReviewRoute != "agent-review" || m.PromptOverlay != "overlay" || m.Harness != "codex-cli" {
		t.Fatalf("spawned mission route=%q review=%q overlay=%q harness=%q, want the agent's defaults", m.Route, m.ReviewRoute, m.PromptOverlay, m.Harness)
	}
	if !m.AutoApprovePlan || m.AutoApproveTools || m.BudgetCurrency != "USD" || m.Flow != missions.FlowFull {
		t.Fatalf("spawned mission plan=%v tools=%v currency=%q flow=%q", m.AutoApprovePlan, m.AutoApproveTools, m.BudgetCurrency, m.Flow)
	}
}

// TestStepCreateRequestReviewRoute covers issue #849: a step with a
// route and no plan_route keeps prove on its route, never the default.
func TestStepCreateRequestReviewRoute(t *testing.T) {
	t.Parallel()
	deps := missions.ResolveDeps{RouteForRole: func(context.Context, string) string { return "default" }}
	for _, tc := range []struct {
		name       string
		step       Step
		wantReview string
	}{
		{"route only reviews on the route", Step{Kind: "general", Route: "fast"}, "fast"},
		{"plan_route covers review", Step{Kind: "general", Route: "fast", PlanRoute: "strong"}, "strong"},
		{"no routes falls back to default", Step{Kind: "general"}, "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := missions.ResolveDefaults(context.Background(), StepCreateRequest(tc.step, "goal", "run-1", "build", "", ""), deps)
			if err != nil {
				t.Fatalf("ResolveDefaults: %v", err)
			}
			if m.ReviewRoute != tc.wantReview {
				t.Fatalf("review_route = %q, want %q", m.ReviewRoute, tc.wantReview)
			}
		})
	}
}

// TestStartRunPausesOnUnusableRoute confirms the D-100 gate applies to a
// workflow step: the run pauses instead of spawning a mission that
// would park on its first turn.
func TestStartRunPausesOnUnusableRoute(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)
	e.SetResolveDeps(missions.ResolveDeps{
		RouteForRole: func(context.Context, string) string { return "dead" },
		ResolveRoute: func(_ context.Context, route, _ string) (*gwclient.ResolvedRoute, error) {
			return &gwclient.ResolvedRoute{Route: route, Entries: []gwclient.ResolvedRouteEntry{{Usable: false, SkipReason: "disabled"}}}, nil
		},
	})
	runID, err := e.StartRun(context.Background(), "wf1", nil)
	if err == nil {
		t.Fatal("StartRun() = nil, want the route gate error")
	}
	if spawner.count() != 0 {
		t.Fatalf("spawned missions = %d, want 0", spawner.count())
	}
	if run, _ := store.GetRun(context.Background(), runID); run.Status != "paused" {
		t.Fatalf("run status = %q, want paused", run.Status)
	}
}

// TestOnMissionTerminalRedeliveryIsNoOp covers D-117's at-least-once
// delivery: the same terminal mission handled twice advances the run
// once.
func TestOnMissionTerminalRedeliveryIsNoOp(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	coderMission := spawner.last()
	coderMission.Phase = missions.PhaseDone
	for range 2 {
		if err := e.OnMissionTerminal(context.Background(), coderMission); err != nil {
			t.Fatalf("OnMissionTerminal: %v", err)
		}
	}
	if spawner.count() != 2 {
		t.Fatalf("spawned missions = %d, want 2 (coder + one qa)", spawner.count())
	}
	run, _ := store.GetRun(context.Background(), runID)
	if run.CurrentStep != "qa" {
		t.Fatalf("run step = %s, want qa", run.CurrentStep)
	}
}

// TestOnMissionTerminalSelfLoopRedeliveryIsNoOp covers a step whose
// edge loops back to itself: current step still matches, so only the
// edge.taken record for the mission stops a second spawn.
func TestOnMissionTerminalSelfLoopRedeliveryIsNoOp(t *testing.T) {
	def := Definition{
		Entry: "coder",
		Steps: map[string]Step{"coder": {Goal: "write code", Kind: "coding"}},
		Edges: []Edge{{From: "coder", On: "mission.done", To: "coder", MaxIterations: 5}},
	}
	_ = def.Validate()
	store := newFakeEngineStore()
	store.putWorkflow("wf1", def, true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	if _, err := e.StartRun(context.Background(), "wf1", nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	first := spawner.last()
	first.Phase = missions.PhaseDone
	for range 2 {
		if err := e.OnMissionTerminal(context.Background(), first); err != nil {
			t.Fatalf("OnMissionTerminal: %v", err)
		}
	}
	if spawner.count() != 2 {
		t.Fatalf("spawned missions = %d, want 2", spawner.count())
	}
}

// edgeTakenPayloads returns the decoded edge.taken payloads of runID.
func edgeTakenPayloads(t *testing.T, store *fakeEngineStore, runID string) []map[string]any {
	t.Helper()
	evs, _ := store.RunEvents(context.Background(), runID)
	var out []map[string]any
	for _, ev := range evs {
		if ev.Kind != "edge.taken" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode edge.taken: %v", err)
		}
		out = append(out, p)
	}
	return out
}

// TestOnMissionTerminalRecordFailureReturnsError covers issue #842: a
// failed edge.taken record after the spawn returns an error so the
// drainer retries, instead of leaving the run stuck on the old step.
func TestOnMissionTerminalRecordFailureReturnsError(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	coder := spawner.last()
	coder.Phase = missions.PhaseDone
	store.failNextApply = errors.New("connection reset")
	if err := e.OnMissionTerminal(context.Background(), coder); err == nil {
		t.Fatal("OnMissionTerminal = nil, want the record error so the drainer retries")
	}
	if spawner.count() != 2 {
		t.Fatalf("spawned missions = %d, want 2 (coder + qa)", spawner.count())
	}
	run, _ := store.GetRun(context.Background(), runID)
	if run.Status != "running" || run.CurrentStep != "coder" {
		t.Fatalf("run = %s/%s, want running/coder until the record lands", run.Status, run.CurrentStep)
	}
}

// TestOnMissionTerminalRetryAfterRecordFailureAdoptsChild: the retry
// after a failed record adopts the qa mission and advances the run.
func TestOnMissionTerminalRetryAfterRecordFailureAdoptsChild(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	coder := spawner.last()
	coder.Phase = missions.PhaseDone
	store.failNextApply = errors.New("connection reset")
	_ = e.OnMissionTerminal(context.Background(), coder)
	qaID := spawner.last().ID

	if err := e.OnMissionTerminal(context.Background(), coder); err != nil {
		t.Fatalf("retry OnMissionTerminal: %v", err)
	}
	if spawner.count() != 2 {
		t.Fatalf("spawned missions = %d, want 2 (retry adopts qa)", spawner.count())
	}
	run, _ := store.GetRun(context.Background(), runID)
	if run.Status != "running" || run.CurrentStep != "qa" {
		t.Fatalf("run = %s/%s, want running/qa", run.Status, run.CurrentStep)
	}
	edges := edgeTakenPayloads(t, store, runID)
	if len(edges) != 1 || edges[0]["spawned_mission_id"] != qaID {
		t.Fatalf("edge.taken payloads = %v, want one with spawned_mission_id %s", edges, qaID)
	}
}

// TestOnMissionTerminalRedeliveryAfterCrashBetweenSpawnAndRecordSpawnsNothing:
// a qa mission committed before a crash, with no edge.taken, is adopted
// by the redelivered terminal event.
func TestOnMissionTerminalRedeliveryAfterCrashBetweenSpawnAndRecordSpawnsNothing(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	coder := spawner.last()
	coder.Phase = missions.PhaseDone
	qaID, _ := spawner.Create(context.Background(), missions.Mission{WorkflowRunID: runID, WorkflowStep: "qa", ParentMissionID: coder.ID})

	if err := e.OnMissionTerminal(context.Background(), coder); err != nil {
		t.Fatalf("OnMissionTerminal: %v", err)
	}
	if spawner.count() != 2 {
		t.Fatalf("spawned missions = %d, want 2 (no second qa)", spawner.count())
	}
	run, _ := store.GetRun(context.Background(), runID)
	if run.CurrentStep != "qa" {
		t.Fatalf("run step = %s, want qa", run.CurrentStep)
	}
	edges := edgeTakenPayloads(t, store, runID)
	if len(edges) != 1 || edges[0]["spawned_mission_id"] != qaID {
		t.Fatalf("edge.taken payloads = %v, want spawned_mission_id %s", edges, qaID)
	}
}

// TestOnMissionTerminalSelfLoopAdoptsPerParent: on a self-loop each
// iteration's child is keyed by its own parent, so a retry adopts the
// right mission and a new iteration still spawns.
func TestOnMissionTerminalSelfLoopAdoptsPerParent(t *testing.T) {
	def := Definition{
		Entry: "coder",
		Steps: map[string]Step{"coder": {Goal: "write code", Kind: "coding"}},
		Edges: []Edge{{From: "coder", On: "mission.done", To: "coder", MaxIterations: 5}},
	}
	_ = def.Validate()
	store := newFakeEngineStore()
	store.putWorkflow("wf1", def, true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	first := spawner.last()
	first.Phase = missions.PhaseDone
	if err := e.OnMissionTerminal(context.Background(), first); err != nil {
		t.Fatalf("first OnMissionTerminal: %v", err)
	}
	second := spawner.last()
	second.Phase = missions.PhaseDone
	store.failNextApply = errors.New("connection reset")
	if err := e.OnMissionTerminal(context.Background(), second); err == nil {
		t.Fatal("second OnMissionTerminal = nil, want record error")
	}
	third := spawner.last()
	if third.ParentMissionID != second.ID {
		t.Fatalf("third mission parent = %q, want %q", third.ParentMissionID, second.ID)
	}
	if err := e.OnMissionTerminal(context.Background(), second); err != nil {
		t.Fatalf("retry OnMissionTerminal: %v", err)
	}
	if spawner.count() != 3 {
		t.Fatalf("spawned missions = %d, want 3", spawner.count())
	}
	edges := edgeTakenPayloads(t, store, runID)
	if len(edges) != 2 || edges[0]["spawned_mission_id"] != second.ID || edges[1]["spawned_mission_id"] != third.ID {
		t.Fatalf("edge.taken payloads = %v, want spawned %s then %s", edges, second.ID, third.ID)
	}
}

// TestOnMissionTerminalEdgeTakenPayloadCarriesSpawnedMissionID: the
// edge.taken record names both the terminal and the spawned mission.
func TestOnMissionTerminalEdgeTakenPayloadCarriesSpawnedMissionID(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	coder := spawner.last()
	coder.Phase = missions.PhaseDone
	if err := e.OnMissionTerminal(context.Background(), coder); err != nil {
		t.Fatalf("OnMissionTerminal: %v", err)
	}
	edges := edgeTakenPayloads(t, store, runID)
	if len(edges) != 1 {
		t.Fatalf("edge.taken payloads = %v, want 1", edges)
	}
	if edges[0]["mission_id"] != coder.ID || edges[0]["spawned_mission_id"] != spawner.last().ID {
		t.Fatalf("edge.taken payload = %v, want mission_id %s spawned_mission_id %s", edges[0], coder.ID, spawner.last().ID)
	}
}

// TestStartRunAdoptsExistingEntryMission: an entry mission already
// linked to the run is adopted instead of spawning a second one.
func TestStartRunAdoptsExistingEntryMission(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	// The fake store names the first run "run-1".
	if _, err := spawner.Create(context.Background(), missions.Mission{WorkflowRunID: "run-1", WorkflowStep: "coder"}); err != nil {
		t.Fatalf("seed entry mission: %v", err)
	}
	e := testEngine(store, spawner)

	runID, err := e.StartRun(context.Background(), "wf1", nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if runID != "run-1" {
		t.Fatalf("runID = %s, want run-1", runID)
	}
	if spawner.count() != 1 {
		t.Fatalf("spawned missions = %d, want 1 (entry adopted)", spawner.count())
	}
	run, _ := store.GetRun(context.Background(), runID)
	if run.Status != "running" || run.CurrentStep != "coder" {
		t.Fatalf("run = %s/%s, want running/coder", run.Status, run.CurrentStep)
	}
}

func TestOnMissionTerminalIgnoresMissionFromAnotherStep(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)

	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	stale := missions.Mission{ID: "qa-old", WorkflowRunID: runID, WorkflowStep: "qa", Phase: missions.PhaseDone}
	if err := e.OnMissionTerminal(context.Background(), stale); err != nil {
		t.Fatalf("OnMissionTerminal: %v", err)
	}
	run, _ := store.GetRun(context.Background(), runID)
	if run.Status != "running" || run.CurrentStep != "coder" || spawner.count() != 1 {
		t.Fatalf("run = %+v, spawned = %d, want untouched", run, spawner.count())
	}
}

func TestOnMissionTerminalReturnsLoadError(t *testing.T) {
	e := testEngine(newFakeEngineStore(), &fakeSpawner{})
	err := e.OnMissionTerminal(context.Background(), missions.Mission{ID: "m", WorkflowRunID: "missing", Phase: missions.PhaseDone})
	if err == nil {
		t.Fatal("OnMissionTerminal on an unknown run = nil error, want error so the drainer retries")
	}
}

func terminalEvent(t *testing.T, p events.MissionPayload) events.Event {
	t.Helper()
	ev, err := events.MissionTerminal(p)
	if err != nil {
		t.Fatalf("MissionTerminal: %v", err)
	}
	return ev
}

func TestEngineHandle(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)
	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	coder := spawner.last()
	// The fake spawner hands back what it stored; mark it terminal there.
	spawner.mu.Lock()
	spawner.missions[0].Phase = missions.PhaseDone
	spawner.mu.Unlock()

	if got := strings.Join(e.Kinds(), ","); got != "mission.done,mission.failed" {
		t.Fatalf("Kinds() = %s", got)
	}

	// No workflow run: nothing loads, nothing spawns.
	if err := e.Handle(context.Background(), nil, terminalEvent(t, events.MissionPayload{MissionID: "other", Phase: "done"})); err != nil {
		t.Fatalf("Handle without run: %v", err)
	}
	// Deleted mission: skipped, the event is processed rather than retried.
	if err := e.Handle(context.Background(), nil, terminalEvent(t, events.MissionPayload{MissionID: "ghost", Phase: "done", WorkflowRunID: runID})); err != nil {
		t.Fatalf("Handle for a deleted mission = %v, want nil", err)
	}
	if spawner.count() != 1 {
		t.Fatalf("spawned = %d, want 1 before the real event", spawner.count())
	}

	if err := e.Handle(context.Background(), nil, terminalEvent(t, events.MissionPayload{MissionID: coder.ID, Phase: "done", WorkflowRunID: runID})); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	run, _ := store.GetRun(context.Background(), runID)
	if run.CurrentStep != "qa" || spawner.count() != 2 {
		t.Fatalf("run = %+v, spawned = %d, want advanced to qa", run, spawner.count())
	}
}

func TestEdgeTakenFor(t *testing.T) {
	evs := []RunEvent{
		{Kind: "run.warning", Payload: json.RawMessage(`{"mission_id":"m1"}`)},
		{Kind: "edge.taken", Payload: json.RawMessage(`{"mission_id":"m2"}`)},
		{Kind: "edge.taken", Payload: json.RawMessage(`not json`)},
	}
	tests := []struct {
		id   string
		want bool
	}{
		{"m1", false},
		{"m2", true},
		{"m3", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := edgeTakenFor(evs, tt.id); got != tt.want {
			t.Fatalf("edgeTakenFor(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

func TestHandleSkipsDeletedStepMission(t *testing.T) {
	spawner := &fakeSpawner{}
	e := testEngine(newFakeEngineStore(), spawner)
	ev := terminalEvent(t, events.MissionPayload{MissionID: "gone", Phase: string(missions.PhaseDone), WorkflowRunID: "run"})
	if err := e.Handle(context.Background(), nil, ev); err != nil {
		t.Fatalf("Handle for a deleted step mission = %v, want nil so the event is processed, not retried", err)
	}
	if got := len(spawner.missions); got != 0 {
		t.Fatalf("spawned missions = %d, want 0", got)
	}
}

// countKind counts runID's run events of kind.
func countKind(store *fakeEngineStore, runID, kind string) int {
	n := 0
	for _, k := range store.eventKinds(runID) {
		if k == kind {
			n++
		}
	}
	return n
}

// TestRunTransitionFailureReturnsErrorAndRetryAppliesOnce covers issue
// #931: a failed pause, fail or end transition returns an error from
// Handle so the drainer retries, and neither a failure before the
// commit nor a commit whose result was lost applies it twice.
func TestRunTransitionFailureReturnsErrorAndRetryAppliesOnce(t *testing.T) {
	type setup struct {
		mission    missions.Mission
		wantStatus string
		wantKind   string
	}
	cases := []struct {
		name    string
		prepare func(store *fakeEngineStore, runID string) setup
	}{
		{"pause on no matching edge", func(store *fakeEngineStore, runID string) setup {
			return setup{missions.Mission{WorkflowRunID: runID, WorkflowStep: "coder", Phase: missions.PhaseFailed}, "paused", "run.paused"}
		}},
		{"fail on cap exceeded", func(store *fakeEngineStore, runID string) setup {
			for range 5 {
				payload, _ := json.Marshal(map[string]any{"from": "coder", "on": "mission.done", "to": "qa", "mission_id": "earlier"})
				store.seq[runID]++
				store.events[runID] = append(store.events[runID], RunEvent{RunID: runID, Seq: store.seq[runID], Kind: "edge.taken", Payload: payload})
			}
			return setup{missions.Mission{WorkflowRunID: runID, WorkflowStep: "coder", Phase: missions.PhaseDone}, "failed", "run.cap_exceeded"}
		}},
		{"end on edge to end", func(store *fakeEngineStore, runID string) setup {
			run := store.runs[runID]
			run.CurrentStep = "qa"
			store.runs[runID] = run
			return setup{missions.Mission{WorkflowRunID: runID, WorkflowStep: "qa", Phase: missions.PhaseDone}, "done", "run.done"}
		}},
	}
	for _, tc := range cases {
		for _, afterCommit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/after_commit=%v", tc.name, afterCommit), func(t *testing.T) {
				store := newFakeEngineStore()
				store.putWorkflow("wf1", coderQADefinition(), true)
				spawner := &fakeSpawner{}
				e := testEngine(store, spawner)
				runID, err := e.StartRun(context.Background(), "wf1", nil)
				if err != nil {
					t.Fatalf("StartRun: %v", err)
				}
				s := tc.prepare(store, runID)
				s.mission.ID = "terminal-mission"
				spawner.mu.Lock()
				spawner.missions = append(spawner.missions, s.mission)
				spawner.mu.Unlock()
				spawned := spawner.count()
				ev := terminalEvent(t, events.MissionPayload{MissionID: s.mission.ID, Phase: string(s.mission.Phase), WorkflowRunID: runID})

				if afterCommit {
					store.failAfterApply = errors.New("connection reset")
				} else {
					store.failNextApply = errors.New("connection reset")
				}
				if err := e.Handle(context.Background(), nil, ev); err == nil {
					t.Fatal("Handle = nil, want the transition error so the drainer retries")
				}
				run, _ := store.GetRun(context.Background(), runID)
				wantAfterFailure, wantEvents := "running", 0
				if afterCommit {
					wantAfterFailure, wantEvents = s.wantStatus, 1
				}
				if run.Status != wantAfterFailure || countKind(store, runID, s.wantKind) != wantEvents {
					t.Fatalf("after failure: status %s, %s events %d, want %s, %d", run.Status, s.wantKind, countKind(store, runID, s.wantKind), wantAfterFailure, wantEvents)
				}

				for attempt := 1; attempt <= 2; attempt++ {
					if err := e.Handle(context.Background(), nil, ev); err != nil {
						t.Fatalf("retry %d: Handle: %v", attempt, err)
					}
				}
				run, _ = store.GetRun(context.Background(), runID)
				if run.Status != s.wantStatus || countKind(store, runID, s.wantKind) != 1 {
					t.Fatalf("after retries: status %s, %s events %d, want %s, 1", run.Status, s.wantKind, countKind(store, runID, s.wantKind), s.wantStatus)
				}
				if spawner.count() != spawned {
					t.Fatalf("spawned missions = %d, want %d (no spawn on these paths)", spawner.count(), spawned)
				}
			})
		}
	}
}

// TestOnMissionTerminalSpawnFailureThenPauseFailureRetries: a failed
// spawn whose pause also fails returns an error; the retry spawns the
// step once and advances the run.
func TestOnMissionTerminalSpawnFailureThenPauseFailureRetries(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)
	runID, _ := e.StartRun(context.Background(), "wf1", nil)
	coder := spawner.last()
	coder.Phase = missions.PhaseDone

	spawner.createErr = errors.New("create failed")
	store.failNextApply = errors.New("connection reset")
	if err := e.OnMissionTerminal(context.Background(), coder); err == nil {
		t.Fatal("OnMissionTerminal = nil, want the pause error")
	}
	if run, _ := store.GetRun(context.Background(), runID); run.Status != "running" || run.CurrentStep != "coder" {
		t.Fatalf("run = %s/%s, want running/coder", run.Status, run.CurrentStep)
	}

	spawner.createErr = nil
	if err := e.OnMissionTerminal(context.Background(), coder); err != nil {
		t.Fatalf("retry OnMissionTerminal: %v", err)
	}
	if spawner.count() != 2 {
		t.Fatalf("spawned missions = %d, want 2 (coder + qa)", spawner.count())
	}
	if run, _ := store.GetRun(context.Background(), runID); run.Status != "running" || run.CurrentStep != "qa" {
		t.Fatalf("run = %s/%s, want running/qa", run.Status, run.CurrentStep)
	}
}

// TestStartRunSpawnFailureReturnsPauseError: when the entry spawn and
// its pause both fail, StartRun reports both and the run stays running
// for boot recovery.
func TestStartRunSpawnFailureReturnsPauseError(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{createErr: errors.New("create failed")}
	store.failNextApply = errors.New("connection reset")
	e := testEngine(store, spawner)

	runID, err := e.StartRun(context.Background(), "wf1", nil)
	if err == nil || !strings.Contains(err.Error(), "create failed") || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("StartRun error = %v, want spawn and pause errors", err)
	}
	if run, _ := store.GetRun(context.Background(), runID); run.Status != "running" {
		t.Fatalf("run status = %s, want running", run.Status)
	}
}

// TestRecoverRunsWithoutEntry covers issue #931: a running run with no
// mission (crash between CreateRun and the entry spawn) gets its entry
// mission once; a second sweep spawns nothing, and runs with a mission,
// runs no longer running, and runs newer than the cutoff are left alone.
func TestRecoverRunsWithoutEntry(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	store.linked = func(runID string) bool {
		id, _ := spawner.WorkflowChild(context.Background(), runID, "")
		return id != ""
	}
	e := testEngine(store, spawner)
	ctx := context.Background()

	orphan, _ := store.CreateRun(ctx, "wf1", "coder", map[string]string{"K": "v"})
	withEntry, err := e.StartRun(ctx, "wf1", nil)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	paused, _ := store.CreateRun(ctx, "wf1", "coder", nil)
	_ = store.ApplyRunTransition(ctx, paused, RunTransition{Status: "paused", CurrentStep: "coder"})
	cutoff := time.Now().Add(time.Millisecond)
	newer, _ := store.CreateRun(ctx, "wf1", "coder", nil)
	run := store.runs[newer]
	run.CreatedAt = cutoff.Add(time.Second)
	store.runs[newer] = run

	n, err := e.RecoverRunsWithoutEntry(ctx, cutoff)
	if err != nil || n != 1 {
		t.Fatalf("RecoverRunsWithoutEntry = %d, %v, want 1, nil", n, err)
	}
	if spawner.count() != 2 {
		t.Fatalf("spawned missions = %d, want 2 (StartRun entry + recovered entry)", spawner.count())
	}
	m := spawner.last()
	if m.WorkflowRunID != orphan || m.WorkflowStep != "coder" || m.ParentMissionID != "" || m.Goal != "write the code" {
		t.Fatalf("recovered mission = %+v, want entry of %s", m, orphan)
	}
	for _, id := range []string{paused, newer} {
		if got, _ := spawner.WorkflowChild(ctx, id, ""); got != "" {
			t.Fatalf("run %s got mission %s, want untouched", id, got)
		}
	}
	if got, _ := spawner.WorkflowChild(ctx, withEntry, ""); got != "mission-1" {
		t.Fatalf("run with entry has mission %q, want its original mission-1", got)
	}

	n, err = e.RecoverRunsWithoutEntry(ctx, cutoff)
	if err != nil || n != 0 || spawner.count() != 2 {
		t.Fatalf("second sweep = %d, %v, spawned %d, want 0, nil, 2", n, err, spawner.count())
	}
}

// TestRecoverRunsWithoutEntryPausesOnSpawnFailure: a recovery spawn that
// fails pauses the run with a reason and is reported.
func TestRecoverRunsWithoutEntryPausesOnSpawnFailure(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{createErr: errors.New("create failed")}
	e := testEngine(store, spawner)
	ctx := context.Background()
	runID, _ := store.CreateRun(ctx, "wf1", "coder", nil)

	n, err := e.RecoverRunsWithoutEntry(ctx, time.Now().Add(time.Second))
	if err == nil || n != 0 {
		t.Fatalf("RecoverRunsWithoutEntry = %d, %v, want 0 and the spawn error", n, err)
	}
	if run, _ := store.GetRun(ctx, runID); run.Status != "paused" || countKind(store, runID, "run.paused") != 1 {
		t.Fatalf("run = %s, events %v, want paused with one run.paused", run.Status, store.eventKinds(runID))
	}
}

// TestRecoverRunsWithoutEntryPausesOnUnknownStep: a run whose step left
// the definition is paused instead of spawning.
func TestRecoverRunsWithoutEntryPausesOnUnknownStep(t *testing.T) {
	store := newFakeEngineStore()
	store.putWorkflow("wf1", coderQADefinition(), true)
	spawner := &fakeSpawner{}
	e := testEngine(store, spawner)
	ctx := context.Background()
	runID, _ := store.CreateRun(ctx, "wf1", "renamed", nil)

	if _, err := e.RecoverRunsWithoutEntry(ctx, time.Now().Add(time.Second)); err == nil {
		t.Fatal("RecoverRunsWithoutEntry = nil, want unknown step error")
	}
	if spawner.count() != 0 {
		t.Fatalf("spawned missions = %d, want 0", spawner.count())
	}
	if run, _ := store.GetRun(ctx, runID); run.Status != "paused" {
		t.Fatalf("run status = %s, want paused", run.Status)
	}
}
