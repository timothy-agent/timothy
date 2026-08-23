package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

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
// from an on_complete a workflow step can never satisfy, since Step
// carries no repo_url/connector_id) must pause the run with the
// validation error as the reason, not silently create an invalid
// mission or crash the engine.
func TestStartRunPausesOnSpawnValidationError(t *testing.T) {
	def := Definition{
		Entry: "coder",
		Steps: map[string]Step{
			"coder": {Goal: "write the code", Kind: "coding", OnComplete: "push"},
		},
	}
	if err := def.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (on_complete shape is valid at the definition level)", err)
	}
	store := newFakeEngineStore()
	store.putWorkflow("wf1", def, true)
	spawner := &fakeSpawner{createErr: fmt.Errorf("driver: create: %w: on_complete requires repo_url and connector_id on a kind=coding mission", missions.ErrInvalidMission)}
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

	e.OnMissionTerminal(context.Background(), coderMission)

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
	e.OnMissionTerminal(context.Background(), qaMission)

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

	e.OnMissionTerminal(context.Background(), coderMission)

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
	e.OnMissionTerminal(context.Background(), coder1)

	// Manually rewind the run back to "coder" to simulate a second
	// coder mission finishing and trying to take the SAME edge again —
	// this must now exceed the cap of 1.
	run, _ := store.GetRun(context.Background(), runID)
	run.CurrentStep = "coder"
	store.runs[runID] = run

	coder2 := missions.Mission{ID: "coder-2", WorkflowRunID: runID, WorkflowStep: "coder", Phase: missions.PhaseDone}
	e.OnMissionTerminal(context.Background(), coder2)

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
	e.OnMissionTerminal(context.Background(), coderMission)

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
	e.OnMissionTerminal(context.Background(), coderMission)

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
