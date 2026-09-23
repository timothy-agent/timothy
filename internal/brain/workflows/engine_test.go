package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

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
	f.runs[id] = Run{ID: id, WorkflowID: workflowID, Status: "running", CurrentStep: currentStep, Context: runContext}
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
	return nil
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
	// Unknown mission: error, retried by the drainer.
	if err := e.Handle(context.Background(), nil, terminalEvent(t, events.MissionPayload{MissionID: "ghost", Phase: "done", WorkflowRunID: runID})); err == nil {
		t.Fatal("Handle for an unknown mission = nil error, want error")
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
