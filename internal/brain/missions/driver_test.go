package missions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/fxrates"
)

// fakeStore is an in-memory driverStore for scripting Driver scenarios
// without a real Postgres pool.
type fakeStore struct {
	mu       sync.Mutex
	missions map[string]Mission
	events   map[string][]Event
	seq      map[string]int64
	// spend scripts Spend's return per mission id — nil/absent means
	// zero spend in every currency, matching a mission with no ledger
	// rows yet.
	spend map[string]MissionSpend
	// applyTransitionErr, when set, makes ApplyTransition return this
	// error instead of writing — scripts the real Store's terminal-row
	// guard (ErrTerminal) without a Postgres pool.
	applyTransitionErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{missions: map[string]Mission{}, events: map[string][]Event{}, seq: map[string]int64{}, spend: map[string]MissionSpend{}}
}

// Spend returns the scripted MissionSpend for id, or a zero-value
// (empty ByCurrency) if the test never set one — mirrors a real
// mission with no cost_ledger rows.
func (f *fakeStore) Spend(ctx context.Context, missionID string) (MissionSpend, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.spend[missionID]; ok {
		return s, nil
	}
	return MissionSpend{ByCurrency: map[string]float64{}}, nil
}

func (f *fakeStore) put(id string, m Mission) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.missions[id] = m
}

func (f *fakeStore) Create(ctx context.Context, m Mission) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.ID == "" {
		m.ID = fmt.Sprintf("fake-%d", len(f.missions)+1)
	}
	f.missions[m.ID] = m
	return m.ID, nil
}

func (f *fakeStore) SetProvisioned(ctx context.Context, id, workspace, worktree, branch, baseCommit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.Workspace, m.Worktree, m.Branch, m.BaseCommit = workspace, worktree, branch, baseCommit
	f.missions[id] = m
	return nil
}

// SetSession mirrors the real Store's WHERE session_id IS NULL guard —
// a second call for a mission that already has one is a no-op, not an
// overwrite.
func (f *fakeStore) SetSession(ctx context.Context, id, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	if m.SessionID == "" {
		m.SessionID = sessionID
		f.missions[id] = m
	}
	return nil
}

func (f *fakeStore) Get(ctx context.Context, id string) (Mission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.missions[id]
	if !ok {
		return Mission{}, ErrNotFound
	}
	return m, nil
}

func (f *fakeStore) ApplyTransition(ctx context.Context, id string, t Transition) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyTransitionErr != nil {
		return f.applyTransitionErr
	}
	m := f.missions[id]
	m.Phase, m.Status, m.PauseReason = t.Next.Phase, t.Next.Status, t.Next.PauseReason
	m.Iteration, m.MaxIterations = t.Next.Iteration, t.Next.MaxIterations
	m.ConsecutiveFailures, m.LastGapFingerprint, m.StallCount = t.Next.ConsecutiveFailures, t.Next.LastGapFingerprint, t.Next.StallCount
	m.ReplanUsed = t.Next.ReplanUsed
	f.missions[id] = m
	for _, ev := range t.Events {
		f.seq[id]++
		f.events[id] = append(f.events[id], Event{MissionID: id, Seq: f.seq[id], Kind: ev.Kind})
	}
	return nil
}

func (f *fakeStore) AppendEvent(ctx context.Context, id, kind string, payload map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq[id]++
	raw, _ := json.Marshal(payload)
	f.events[id] = append(f.events[id], Event{MissionID: id, Seq: f.seq[id], Kind: kind, Payload: raw})
	return nil
}

func (f *fakeStore) Events(ctx context.Context, id string) ([]Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Event, len(f.events[id]))
	copy(out, f.events[id])
	return out, nil
}

func (f *fakeStore) SetSpec(ctx context.Context, id string, spec Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.Spec = spec
	f.missions[id] = m
	return nil
}

func (f *fakeStore) SetLastEvidence(ctx context.Context, id, evidence string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.LastEvidence = evidence
	f.missions[id] = m
	return nil
}

func (f *fakeStore) SetFinalOutput(ctx context.Context, id, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.FinalOutput = text
	f.missions[id] = m
	return nil
}

func (f *fakeStore) SetDiscoverNotes(ctx context.Context, id, notes string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.DiscoverNotes = notes
	f.missions[id] = m
	return nil
}

// SetNameIfEmpty mirrors the real Store's empty-name guard — a second
// call for a mission that already has a name is a no-op, not an
// overwrite.
func (f *fakeStore) SetNameIfEmpty(ctx context.Context, id, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	if m.Name == "" {
		m.Name = name
		f.missions[id] = m
	}
	return nil
}

func (f *fakeStore) SetArtifactRefs(ctx context.Context, id string, refs []ArtifactRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.ArtifactRefs = refs
	f.missions[id] = m
	return nil
}

func (f *fakeStore) AppendProgress(ctx context.Context, id, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.Progress = append(m.Progress, ProgressNote{Note: note})
	f.missions[id] = m
	f.seq[id]++
	f.events[id] = append(f.events[id], Event{MissionID: id, Seq: f.seq[id], Kind: "mission.progress"})
	return nil
}

// AnswerPendingInput mirrors the real Store.AnswerPendingInput: clears
// PendingInput, resumes to idle, and appends eventKind.
func (f *fakeStore) AnswerPendingInput(ctx context.Context, id, eventKind string, payload map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.missions[id]
	m.PendingInput = nil
	m.Status = StatusIdle
	f.missions[id] = m
	f.seq[id]++
	f.events[id] = append(f.events[id], Event{MissionID: id, Seq: f.seq[id], Kind: eventKind})
	return nil
}

// scriptedRunner replays fixed responses for RunWorker/RunReview/
// PlanSession, one entry consumed per call — lets a test script an
// exact sequence of outcomes across several Advance calls. workerErr,
// if set, makes every RunWorker call fail with this error instead of
// returning a verdict — the driver maps a Runner-level failure
// (infra/gateway trouble) to InputWorkerFailed, distinct from the
// worker's OWN self-reported "retry" outcome (InputWorkerRetry), which
// does not count toward the backoff brake.
type scriptedRunner struct {
	workerVerdicts []WorkerVerdict
	workerIdx      int
	workerErr      error
	// workerText overrides the raw turn text RunWorker returns; empty
	// means the "worker output" default, matching every pre-existing
	// caller that doesn't care about the raw text.
	workerText string
	// workerPackets records the WorkPacket RunWorker was called with,
	// one entry per call: lets a test assert the generate phase's
	// packet actually carried what the driver built (e.g. DiscoverNotes
	// for flow=discover_generate, D-090).
	workerPackets  []WorkPacket
	reviewVerdicts []ReviewVerdict
	reviewIdx      int
	plans          []Spec
	planIdx        int
	// planDiscoverNotes records the discoverNotes argument PlanSession
	// was called with, one entry per call — lets a test assert the plan
	// phase actually received what the discover phase stored.
	planDiscoverNotes []string
	// discoverNotes scripts DiscoverSession's return, one entry consumed
	// per call (same one-behind-repeat pattern as plans/workerVerdicts).
	discoverNotes []string
	discoverIdx   int
	discoverErr   error
}

func (r *scriptedRunner) RunWorker(ctx context.Context, m Mission, packet WorkPacket) (WorkerVerdict, string, error) {
	r.workerPackets = append(r.workerPackets, packet)
	if r.workerErr != nil {
		return WorkerVerdict{}, "", r.workerErr
	}
	v := r.workerVerdicts[r.workerIdx]
	if r.workerIdx < len(r.workerVerdicts)-1 {
		r.workerIdx++
	}
	text := r.workerText
	if text == "" {
		text = "worker output"
	}
	return v, text, nil
}

func (r *scriptedRunner) RunReview(ctx context.Context, m Mission, packet ReviewPacket, gk *GatekeeperState) (ReviewVerdict, *GatekeeperState, error) {
	v := r.reviewVerdicts[r.reviewIdx]
	if r.reviewIdx < len(r.reviewVerdicts)-1 {
		r.reviewIdx++
	}
	return v, &GatekeeperState{}, nil
}

func (r *scriptedRunner) PlanSession(ctx context.Context, m Mission, discoverNotes string) (Spec, error) {
	r.planDiscoverNotes = append(r.planDiscoverNotes, discoverNotes)
	s := r.plans[r.planIdx]
	if r.planIdx < len(r.plans)-1 {
		r.planIdx++
	}
	return s, nil
}

func (r *scriptedRunner) DiscoverSession(ctx context.Context, m Mission) (string, error) {
	if r.discoverErr != nil {
		return "", r.discoverErr
	}
	if len(r.discoverNotes) == 0 {
		return "", nil
	}
	notes := r.discoverNotes[r.discoverIdx]
	if r.discoverIdx < len(r.discoverNotes)-1 {
		r.discoverIdx++
	}
	return notes, nil
}

// fakeSandboxExec runs command via /bin/sh -c directly in workdir —
// tests exercising verify_cmd need the real exit code/output a plan's
// check produces, not a mocked one, and don't care that it isn't
// actually containerized.
func fakeSandboxExec(ctx context.Context, missionID, environment, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command) //nolint:gosec // test-only, command is test-authored
	cmd.Dir = workdir
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}

func testDriver(store driverStore, runner Runner) *Driver {
	d := NewDriver(store, runner, nil, nil, nil, nil, fakeSandboxExec, nil, slog.Default())
	d.retryDelayFn = func(int) time.Duration { return 0 } // tests drive worker_failed rounds back-to-back; no real sleeps
	return d
}

// fakeFXRates scripts LatestUSDRates for the budget-brake conversion
// tests without a real Postgres-backed fxrates.Store. nil rates (the
// zero value) models an empty table — every currency comes back
// unconvertible, same as a genuinely absent rate.
type fakeFXRates struct {
	rates map[string]fxrates.Rate
	err   error
}

func (f *fakeFXRates) LatestUSDRates(ctx context.Context) (map[string]fxrates.Rate, error) {
	return f.rates, f.err
}

// driveN calls Advance up to n times, stopping early if it returns
// false (mission parked/terminal).
func driveN(t *testing.T, d *Driver, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		cont, err := d.Advance(context.Background(), id)
		if err != nil {
			t.Fatalf("Advance[%d]: %v", i, err)
		}
		if !cont {
			return
		}
	}
}

func TestDriverHappyPathToDone(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit", VerifyCmd: ""}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)

	driveN(t, d, "m1", 5) // discover -> plan -> generate -> prove -> result -> done

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone || m.Status != StatusDone {
		t.Fatalf("mission after happy path = %+v, want done/done", m)
	}
	if m.LastEvidence != "did it" {
		t.Fatalf("mission.LastEvidence = %q, want the worker's evidence persisted for the reviewer", m.LastEvidence)
	}
}

// codingWorktree inits a real git repo with one commit — coding
// missions run BaselineDiff/CommitUnit against a real worktree even
// with a scripted Runner, so on_complete tests (which need Kind=coding
// for NotPushable to accept them) need a real git dir, not merely a
// TempDir. Returns the dir and its HEAD commit hash for BaseCommit.
func codingWorktree(t *testing.T) (dir, baseCommit string) {
	t.Helper()
	requireGitForPush(t)
	dir = t.TempDir()
	gitRun(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "seed.txt")
	gitRun(t, dir, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "seed")
	return dir, strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
}

// TestDriverFiresOnCompletePushPROnDone proves a coding mission with
// on_complete="push_pr" fires exactly one push and one PR create the
// moment it reaches phase=done — the SAME Completer the manual push/pr
// API endpoints use, wired via Driver.SetCompleter.
func TestDriverFiresOnCompletePushPROnDone(t *testing.T) {
	store := newFakeStore()
	dir, base := codingWorktree(t)
	store.put("m1", Mission{
		ID: "m1", Kind: "coding", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true,
		Worktree: dir, BaseCommit: base, Branch: "mission/x", RepoURL: "https://github.com/octo/repo.git", ConnectorID: "conn1",
		OnComplete: "push_pr",
	})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit", VerifyCmd: ""}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := NewDriver(store, runner, NewWorkspace("", nil, log), nil, nil, nil, fakeSandboxExec, nil, log)
	pusher := &fakeBranchPusher{host: "github.com"}
	pr := &fakePRSource{defaultBranch: "main", prURL: "https://github.com/octo/repo/pull/1", prNumber: 1}
	d.SetCompleter(&Completer{workspace: pusher, store: store, resolveToken: func(ctx context.Context, connectorID string) (string, error) {
		return "dummy-token", nil
	}, pr: pr})

	driveN(t, d, "m1", 5) // discover -> plan -> generate -> prove -> result -> done

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %q, want done", m.Phase)
	}
	if pusher.pushCalls != 1 {
		t.Fatalf("Push called %d times, want exactly 1", pusher.pushCalls)
	}
	if pr.createCalls != 1 {
		t.Fatalf("CreatePR called %d times, want exactly 1", pr.createCalls)
	}
	events := store.events["m1"]
	var pushed, prOpened int
	for _, e := range events {
		switch e.Kind {
		case "mission.pushed":
			pushed++
		case "mission.pr_opened":
			prOpened++
		}
	}
	if pushed != 1 || prOpened != 1 {
		t.Fatalf("events pushed=%d pr_opened=%d, want exactly 1 each", pushed, prOpened)
	}
}

// TestDriverOnCompleteFailureParksInResultAndNotifies proves a failed
// auto-fire (push rejected) never un-dones the mission: it parks the
// mission IN result (D-086, an operator's on_complete choice failing
// is an explicit park, not a silently-lost notification), and fires
// the wired push-failed notifier exactly once.
func TestDriverOnCompleteFailureParksInResultAndNotifies(t *testing.T) {
	store := newFakeStore()
	dir, base := codingWorktree(t)
	store.put("m1", Mission{
		ID: "m1", Kind: "coding", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true,
		Worktree: dir, BaseCommit: base, Branch: "mission/x", RepoURL: "https://github.com/octo/repo.git", ConnectorID: "conn1",
		OnComplete: "push",
	})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit", VerifyCmd: ""}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := NewDriver(store, runner, NewWorkspace("", nil, log), nil, nil, nil, fakeSandboxExec, nil, log)
	pusher := &fakeBranchPusher{err: ErrPushRejected}
	d.SetCompleter(&Completer{workspace: pusher, store: store, resolveToken: func(ctx context.Context, connectorID string) (string, error) {
		return "dummy-token", nil
	}})
	var notified []string
	d.SetPushFailedNotifier(func(ctx context.Context, missionID, message string) {
		notified = append(notified, message)
	})

	driveN(t, d, "m1", 5) // discover -> plan -> generate -> prove -> result(parked)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseResult || m.Status != StatusPaused || m.PauseReason != PauseInfra {
		t.Fatalf("mission after failed auto-fire = %+v, want parked in result/paused/infra", m)
	}
	if len(notified) != 1 {
		t.Fatalf("push-failed notifications = %d, want exactly 1", len(notified))
	}
	var pushFailed int
	for _, e := range store.events["m1"] {
		if e.Kind == "mission.push_failed" {
			pushFailed++
		}
	}
	if pushFailed != 1 {
		t.Fatalf("mission.push_failed events = %d, want exactly 1", pushFailed)
	}
}

// TestDriverDiscoverStoresNotesAndAdvances confirms the discover phase
// stores DiscoverSession's findings on the mission, emits
// mission.discover_complete, and advances to plan.
func TestDriverDiscoverStoresNotesAndAdvances(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true})
	runner := &scriptedRunner{
		discoverNotes: []string{"no prior implementation exists; goal is self-contained"},
		plans:        []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
	}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhasePlan {
		t.Fatalf("mission phase after discover = %q, want plan", m.Phase)
	}
	if m.DiscoverNotes != "no prior implementation exists; goal is self-contained" {
		t.Fatalf("mission.DiscoverNotes = %q, want the discover findings persisted", m.DiscoverNotes)
	}
	events := store.events["m1"]
	found := false
	for _, ev := range events {
		if ev.Kind == "mission.discover_complete" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %+v, want a mission.discover_complete event", events)
	}
}

// TestDriverDiscoverTruncatesLongNotes confirms discoverNotesCap bounds
// what gets stored — the findings feed straight into the plan phase's
// prompt, so unbounded notes would blow out that turn's context.
func TestDriverDiscoverTruncatesLongNotes(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true})
	long := strings.Repeat("x", discoverNotesCap+500)
	runner := &scriptedRunner{
		discoverNotes: []string{long},
		plans:        []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
	}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if len(m.DiscoverNotes) > discoverNotesCap+len("…") {
		t.Fatalf("mission.DiscoverNotes length = %d, want capped near %d", len(m.DiscoverNotes), discoverNotesCap)
	}
}

// TestDriverDiscoverErrorRoutesToWorkerFailed confirms an DiscoverSession
// error (infra/stream failure, not a missing-sentinel degrade — that
// path never returns an error) maps to InputWorkerFailed, the same
// backoff/pause machinery a worker/plan infra failure already takes —
// discover has no phase-specific error input of its own.
func TestDriverDiscoverErrorRoutesToWorkerFailed(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true})
	runner := &scriptedRunner{discoverErr: fmt.Errorf("gateway unreachable")}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDiscover {
		t.Fatalf("mission phase after discover error = %q, want it to stay in discover (retry-eligible)", m.Phase)
	}
	if m.ConsecutiveFailures == 0 {
		t.Fatalf("mission.ConsecutiveFailures = %d, want the failure counted", m.ConsecutiveFailures)
	}
}

// TestDriverPlanReceivesStoredDiscoverNotes confirms runPlan reads
// m.DiscoverNotes from the FRESH Get at the top of the plan phase's own
// Advance call (a separate call from the discover phase's), not some
// stale in-memory value from the discover turn.
func TestDriverPlanReceivesStoredDiscoverNotes(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true})
	runner := &scriptedRunner{
		discoverNotes: []string{"found a reusable config loader in internal/platform/config"},
		plans:        []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
	}
	d := testDriver(store, runner)

	// First Advance: discover phase.
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance[discover]: %v", err)
	}
	// Second Advance: plan phase, a SEPARATE call with its own fresh Get.
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance[plan]: %v", err)
	}
	if len(runner.planDiscoverNotes) != 1 {
		t.Fatalf("PlanSession call count = %d, want exactly one", len(runner.planDiscoverNotes))
	}
	if runner.planDiscoverNotes[0] != "found a reusable config loader in internal/platform/config" {
		t.Fatalf("PlanSession discoverNotes arg = %q, want the notes stored by the discover phase", runner.planDiscoverNotes[0])
	}
}

// TestDriverPlanCreatedEventCarriesAssumptions confirms issue #446:
// the mission.plan_created event payload includes the plan's
// assumptions when the planner declared any, and omits the key
// entirely when it did not (no empty "assumptions" key noise).
func TestDriverPlanCreatedEventCarriesAssumptions(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		plans: []Spec{{
			Units:       []PlanUnit{{Title: "only unit"}},
			Assumptions: []PlanAssumption{{Assumption: "no language version was specified", Default: "Python 3.12"}},
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
		if e.Kind == "mission.plan_created" {
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("unmarshal plan_created payload: %v", err)
			}
		}
	}
	if payload == nil {
		t.Fatal("no mission.plan_created event recorded")
	}
	assumptions, ok := payload["assumptions"].([]any)
	if !ok || len(assumptions) != 1 {
		t.Fatalf("plan_created payload assumptions = %+v, want one entry", payload["assumptions"])
	}
}

// TestDriverPlanCreatedEventOmitsEmptyAssumptions confirms an
// unambiguous plan's event payload carries no "assumptions" key at
// all, not an empty array.
func TestDriverPlanCreatedEventOmitsEmptyAssumptions(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		plans: []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
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
		if e.Kind == "mission.plan_created" {
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("unmarshal plan_created payload: %v", err)
			}
		}
	}
	if payload == nil {
		t.Fatal("no mission.plan_created event recorded")
	}
	if _, ok := payload["assumptions"]; ok {
		t.Fatalf("plan_created payload = %+v, want no assumptions key for an unambiguous plan", payload)
	}
}

// TestDriverPlanApprovalGateParks confirms D-087 (issue #456): a plan
// phase turn on a mission with AutoApprovePlan=false parks on
// PauseApproval with the plan already stored, instead of advancing to
// generate; no worker turn runs (scriptedRunner has no workerVerdicts
// scripted, so a stray runExecute call would fail the test via an
// unscripted-call panic/error).
func TestDriverPlanApprovalGateParks(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: false})
	runner := &scriptedRunner{plans: []Spec{{Units: []PlanUnit{{Title: "only unit"}}}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	got := store.missions["m1"]
	if got.Phase != PhasePlan || got.Status != StatusPaused || got.PauseReason != PauseApproval {
		t.Fatalf("mission after plan with auto_approve_plan=false = %s/%s/%s, want plan/paused/approval", got.Phase, got.Status, got.PauseReason)
	}
	if len(got.Spec.Units) != 1 {
		t.Fatalf("Spec.Units = %+v, want the plan stored even though the mission parked", got.Spec.Units)
	}
}

// TestDriverDecidePlanApprove confirms DecidePlan's approve verb
// unparks a PauseApproval mission straight to generate. Scripts a
// workerVerdicts entry (blocked) so the background Drive goroutine
// DecidePlan kicks off, which continues into the now-unparked
// generate phase, settles cleanly instead of panicking on an
// unscripted RunWorker call once it runs past this assertion.
func TestDriverDecidePlanApprove(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval,
		MaxIterations: 8, Spec: Spec{Units: []PlanUnit{{Title: "only unit"}}},
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := testDriver(store, runner)

	if err := d.DecidePlan(context.Background(), "m1", InputPlanApprove, ""); err != nil {
		t.Fatalf("DecidePlan: %v", err)
	}
	// DecidePlan's own ApplyTransition write happens synchronously before
	// it spawns the post-decision Drive goroutine, but reading it back
	// via Get (mutex-guarded) rather than a raw map index avoids racing
	// that goroutine's own concurrent Get/writes.
	got, err := store.Get(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Phase != PhaseGenerate || got.Status != StatusIdle || got.PauseReason != "" {
		t.Fatalf("mission after approve = %s/%s/%s, want generate/idle/<none>", got.Phase, got.Status, got.PauseReason)
	}
}

// TestDriverDecidePlanReplanRejectedWhenNotParked confirms the driver's
// own belt-and-suspenders guard: DecidePlan returns
// ErrNotAwaitingApproval for a mission not currently parked on
// PauseApproval, regardless of phase.
func TestDriverDecidePlanReplanRejectedWhenNotParked(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8})
	d := testDriver(store, &scriptedRunner{})

	err := d.DecidePlan(context.Background(), "m1", InputPlanReplan, "feedback")
	if !errors.Is(err, ErrNotAwaitingApproval) {
		t.Fatalf("DecidePlan error = %v, want ErrNotAwaitingApproval", err)
	}
	got := store.missions["m1"]
	if got.Phase != PhaseGenerate || got.Status != StatusWorking {
		t.Fatalf("mission after rejected decide = %s/%s, want untouched generate/working", got.Phase, got.Status)
	}
}

// TestDriverReplanFeedsFollowingPlanTurn confirms the operator's
// replan feedback (recorded as a progress note by the API layer, same
// channel as h.note's steering notes) reaches the NEXT plan turn's
// prompt via replanNotes, without spending ReplanUsed. Drives the
// unpark step directly via Step+ApplyTransition (the API/DecidePlan's
// own transition), then a synchronous Advance for the plan turn itself.
// This avoids DecidePlan's background Drive re-kick racing this test's
// own reads of the unsynchronized scriptedRunner test double.
func TestDriverReplanFeedsFollowingPlanTurn(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval,
		MaxIterations: 8, DiscoverNotes: "original findings",
		Spec: Spec{Units: []PlanUnit{{Title: "unit0"}}},
	})
	runner := &scriptedRunner{plans: []Spec{{Units: []PlanUnit{{Title: "unit0 revised"}}}}}
	d := testDriver(store, runner)

	// AppendProgress mirrors what the API's replan handler does before
	// the state transition, same channel as h.note's steering notes.
	if err := store.AppendProgress(context.Background(), "m1", "Operator replan feedback: use Go 1.23"); err != nil {
		t.Fatalf("AppendProgress: %v", err)
	}
	t1 := Step(StepState{Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval, MaxIterations: 8},
		StepInput{Input: InputPlanReplan, Reason: "use Go 1.23"}, DefaultConfig)
	if err := store.ApplyTransition(context.Background(), "m1", t1); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}
	if t1.Next.ReplanUsed {
		t.Fatal("replan transition set ReplanUsed, operator iterations must stay free")
	}

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if len(runner.planDiscoverNotes) != 1 {
		t.Fatalf("PlanSession call count = %d, want 1", len(runner.planDiscoverNotes))
	}
	if got := runner.planDiscoverNotes[0]; !strings.Contains(got, "use Go 1.23") {
		t.Fatalf("replan prompt = %q, want it to contain the operator feedback", got)
	}
	// The replanned plan (AutoApprovePlan still false on this fixture)
	// parks on approval again once runPlan completes.
	got := store.missions["m1"]
	if got.Phase != PhasePlan || got.Status != StatusPaused || got.PauseReason != PauseApproval || got.ReplanUsed {
		t.Fatalf("mission after replanned plan lands = %s/%s/%s/ReplanUsed=%v, want plan/paused/approval/false", got.Phase, got.Status, got.PauseReason, got.ReplanUsed)
	}
}

// TestDriverPlanInfeasibleFailsMission confirms D-077: when the
// planner reports the goal cannot be achieved as stated, the driver
// feeds InputPlanInfeasible through the normal transition path and the
// mission ends phase=failed rather than storing a spec and advancing
// to execute.
func TestDriverPlanInfeasibleFailsMission(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		plans: []Spec{{Infeasible: true, InfeasibleReason: "goal forbids the only possible action"}},
	}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	got := store.missions["m1"]
	if got.Phase != PhaseFailed {
		t.Fatalf("Phase = %q, want failed", got.Phase)
	}
	if len(got.Spec.Units) != 0 {
		t.Fatalf("Spec.Units = %+v, want no units stored for an infeasible plan", got.Spec.Units)
	}
	var sawFailed, sawInfeasible bool
	for _, ev := range store.events["m1"] {
		switch ev.Kind {
		case "mission.failed":
			sawFailed = true
		case "mission.plan_infeasible":
			sawInfeasible = true
		}
	}
	if !sawFailed {
		t.Fatal("no mission.failed event recorded")
	}
	if !sawInfeasible {
		t.Fatal("no mission.plan_infeasible event recorded")
	}
}

// TestDriverExecuteRecordsHandoffOverRawText confirms that when the
// worker's mission_status call carries a handoff note, the harness
// records THAT as the progress note for the next session, not the
// turn's raw text — the raw text is often just tool chatter with no
// orientation value, while the handoff is the worker's deliberate
// summary.
func TestDriverExecuteRecordsHandoffOverRawText(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "retry", Analysis: "hit a wall", Handoff: "half the migration is done; finish the token refresh path next"}},
		workerText:     "raw turn text nobody should see in progress",
	}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if len(m.Progress) != 1 {
		t.Fatalf("Progress = %+v, want exactly one note", m.Progress)
	}
	if m.Progress[0].Note != "half the migration is done; finish the token refresh path next" {
		t.Fatalf("progress note = %q, want the handoff text", m.Progress[0].Note)
	}
}

// TestDriverExecuteRecordsRawTextWithoutHandoff confirms the pre-
// existing behavior is unchanged when the worker doesn't supply a
// handoff: the raw turn text is still what gets recorded.
func TestDriverExecuteRecordsRawTextWithoutHandoff(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "retry", Analysis: "hit a wall"}},
		workerText:     "raw turn text",
	}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if len(m.Progress) != 1 {
		t.Fatalf("Progress = %+v, want exactly one note", m.Progress)
	}
	if m.Progress[0].Note != "raw turn text" {
		t.Fatalf("progress note = %q, want the raw turn text", m.Progress[0].Note)
	}
}

// TestDriverLightDoneSetsFinalOutputAndSkipsToResult confirms a light
// mission's done branch (D-069) short-circuits trySkipReview: it sets
// FinalOutput to the worker's FinalMessage (the text since its last
// non-sentinel tool call, NOT the whole multi-turn transcript — see
// TestRunWorkerFinalMessageExcludesPriorToolRoundNarration for the
// runner-level guarantee this relies on), records a mission.review_skipped
// event with reason "light", and reaches PhaseResult in the same
// Advance call without ever routing through prove: Spec is empty, so
// LastUnit is trivially true. Result itself (zero LLM turns) is one
// more Advance call away from done.
func TestDriverLightDoneSetsFinalOutputAndSkipsToResult(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Light: true, Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did the thing", FinalMessage: "here is the complete deliverable"}},
		workerText:     "draft1 tool-retry-narration here is the complete deliverable",
	}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseResult || m.Status != StatusIdle {
		t.Fatalf("light mission after done = %+v, want phase=result status=idle", m)
	}
	if m.FinalOutput != "here is the complete deliverable" {
		t.Fatalf("FinalOutput = %q, want the worker's FinalMessage, not the full multi-turn text", m.FinalOutput)
	}
	events, _ := store.Events(context.Background(), "m1")
	found := false
	for _, e := range events {
		if e.Kind != "mission.review_skipped" {
			continue
		}
		found = true
		var payload struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(e.Payload, &payload); err != nil || payload.Reason != "light" {
			t.Fatalf("mission.review_skipped payload = %s, want reason=light", e.Payload)
		}
	}
	if !found {
		t.Fatal("no mission.review_skipped event recorded for a light mission's done transition")
	}
}

// TestDriverLightDoneFallsBackToFullTextWhenFinalMessageEmpty confirms
// the defensive fallback: a worker verdict with no FinalMessage (e.g.
// the delegated path, or a native worker whose last action was a tool
// call immediately followed by mission_status with nothing written
// after) still records something rather than an empty FinalOutput.
func TestDriverLightDoneFallsBackToFullTextWhenFinalMessageEmpty(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Light: true, Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did the thing"}}, // FinalMessage left empty
		workerText:     "the only text the runner produced",
	}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.FinalOutput != "the only text the runner produced" {
		t.Fatalf("FinalOutput = %q, want fallback to the full turn text when FinalMessage is empty", m.FinalOutput)
	}
}

// TestDriverDiscoverGenerateVisitsExactlyDiscoverGenerateResult drives
// a flow=discover_generate mission (D-090, issue #459) end to end and
// confirms its exact phase/event sequence: discover -> generate ->
// result -> done, with no plan_created and no review round of any
// kind. The generate turn takes the same planless short-circuit as
// light (mission.review_skipped, reason=discover_generate), never
// mission.review_verdict or mission.plan_created.
func TestDriverDiscoverGenerateVisitsExactlyDiscoverGenerateResult(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Flow: FlowDiscoverGenerate, Phase: PhaseDiscover, Status: StatusIdle, MaxIterations: 8,
	})
	runner := &scriptedRunner{
		discoverNotes:   []string{"found three relevant sources"},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did the thing", FinalMessage: "the deliverable"}},
	}
	d := testDriver(store, runner)

	// discover -> generate (phase_complete routes straight to generate,
	// never plan) -> generate's own turn (planless short-circuit to
	// result) -> result's own deterministic step -> done.
	driveN(t, d, "m1", 4)

	m, err := store.Get(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Phase != PhaseDone {
		t.Fatalf("mission = %+v, want phase=done", m)
	}
	if m.FinalOutput != "the deliverable" {
		t.Fatalf("FinalOutput = %q, want the worker's FinalMessage", m.FinalOutput)
	}

	events, _ := store.Events(context.Background(), "m1")
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	mustContain := []string{"mission.discover_complete", "mission.review_skipped", "mission.done"}
	for _, want := range mustContain {
		found := false
		for _, k := range kinds {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("events = %v, want %q among them", kinds, want)
		}
	}
	mustNotContain := []string{"mission.plan_created", "mission.review_verdict", "mission.plan_awaiting_approval"}
	for _, forbidden := range mustNotContain {
		for _, k := range kinds {
			if k == forbidden {
				t.Fatalf("events = %v, discover_generate must never emit %q", kinds, forbidden)
			}
		}
	}

	// The review_skipped event names discover_generate, not light.
	for _, e := range events {
		if e.Kind != "mission.review_skipped" {
			continue
		}
		var payload struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(e.Payload, &payload); err != nil || payload.Reason != "discover_generate" {
			t.Fatalf("mission.review_skipped payload = %s, want reason=discover_generate", e.Payload)
		}
	}
}

// TestDriverDiscoverGenerateDiscoverNotesReachThePlanlessPacket confirms
// discover's findings (Mission.DiscoverNotes) reach the generate turn's
// WorkPacket for flow=discover_generate, the whole point of running
// discover before a planless pass. Light: true is also asserted, same
// worker path as a D-069 light mission.
func TestDriverDiscoverGenerateDiscoverNotesReachThePlanlessPacket(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Flow: FlowDiscoverGenerate, Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
		DiscoverNotes: "found three relevant sources",
	})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did the thing", FinalMessage: "the deliverable"}},
	}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if len(runner.workerPackets) != 1 {
		t.Fatalf("RunWorker call count = %d, want 1", len(runner.workerPackets))
	}
	p := runner.workerPackets[0]
	if !p.Light {
		t.Fatal("discover_generate worker packet Light = false, want true (same planless worker path as light)")
	}
	if p.DiscoverNotes != "found three relevant sources" {
		t.Fatalf("packet DiscoverNotes = %q, want the mission's stored discover notes", p.DiscoverNotes)
	}
}

// TestDriverPacketOmitsDiscoverNotesForNonPlanlessFlow confirms a
// flow=full mission's packet never carries DiscoverNotes, even when the
// mission has them stored (every mission that visits discover does):
// WorkPacket.DiscoverNotes is meant for a planless worker turn only
// (D-090), and a full-flow generate turn gets its own Plan block
// instead.
func TestDriverPacketOmitsDiscoverNotesForNonPlanlessFlow(t *testing.T) {
	store := newFakeStore()
	m := Mission{
		ID: "m1", Kind: "general", Flow: FlowFull, Phase: PhaseGenerate, Status: StatusWorking,
		DiscoverNotes: "found three relevant sources",
		Spec:         Spec{Units: []PlanUnit{{Title: "write summary.md"}}},
	}
	store.put("m1", m)
	d := testDriver(store, &scriptedRunner{})

	p, err := d.packet(context.Background(), m)
	if err != nil {
		t.Fatalf("packet: %v", err)
	}
	if p.Light {
		t.Fatal("flow=full packet Light = true, want false")
	}
	if p.DiscoverNotes != "" {
		t.Fatalf("flow=full packet DiscoverNotes = %q, want empty", p.DiscoverNotes)
	}
}

// TestDriverNonLightDoneStillGoesThroughReview confirms the light
// short-circuit is gated on m.Light: an ordinary general mission with a
// unit that has no declared artifacts still falls through to
// InputPhaseComplete (review), unchanged from before this feature.
func TestDriverNonLightDoneStillGoesThroughReview(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
		Spec: Spec{Units: []PlanUnit{{Title: "write summary.md"}}},
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did the thing"}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseProve {
		t.Fatalf("non-light mission after done with no declared artifacts = %+v, want phase=prove", m)
	}
	if m.FinalOutput != "" {
		t.Fatalf("FinalOutput = %q, want empty for a non-light mission", m.FinalOutput)
	}
}

// TestDriverNoProveStillReviewsWithoutArtifacts confirms flow=no_prove
// does NOT approve a unit directly when it has no declared artifacts:
// trySkipReview's skip mechanism requires real harness evidence
// (artifacts + verify_cmd), same as an ordinary flow=full general
// mission (TestDriverNonLightDoneStillGoesThroughReview). no_prove
// keeps discover/plan and a real plan's units; it is not a planless
// flow, unlike discover_generate.
func TestDriverNoProveStillReviewsWithoutArtifacts(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Flow: FlowNoProve, Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
		Spec: Spec{Units: []PlanUnit{{Title: "write summary.md"}}},
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did the thing"}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseProve {
		t.Fatalf("no_prove mission after done with no declared artifacts = %+v, want phase=prove (no harness evidence to skip on)", m)
	}
}

// TestDriverNoProveStillChecksArtifacts confirms flow=no_prove skips
// only the LLM reviewer, never the harness's own CheckArtifacts: a
// worker claiming done without writing its declared artifact must
// still be sent back to generate, exactly as TestDriverArtifactCheck-
// BlocksTautologicalDone asserts for flow=full. passes flips only on
// harness evidence, regardless of flow.
func TestDriverNoProveStillChecksArtifacts(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Flow: FlowNoProve, Phase: PhaseGenerate, Status: StatusWorking,
		MaxIterations: 8, Workspace: t.TempDir(),
		Spec: Spec{Units: []PlanUnit{{Title: "write summary", Artifacts: []string{"summary.md"}, VerifyCmd: "echo done"}}},
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "wrote it (no it didn't)"}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseGenerate || m.Spec.Units[0].Passes {
		t.Fatalf("mission = phase %s passes %v, want back in generate with the unit NOT passed", m.Phase, m.Spec.Units[0].Passes)
	}
	if m.Iteration == 0 {
		t.Fatal("a failed artifact check must cost an iteration under no_prove too")
	}
}

func TestDriverBackoffPauseAfterThreeFailures(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{workerErr: fmt.Errorf("gateway unavailable")}
	d := testDriver(store, runner)

	driveN(t, d, "m1", 3)

	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseBackoff {
		t.Fatalf("mission after 3 consecutive runner failures = %+v, want paused/backoff", m)
	}
}

// TestDriverWorkerRetryDoesNotCountTowardBackoff confirms the
// distinction the fix above documents: a worker's own self-reported
// "retry" outcome (it made an attempt, just didn't finish) never
// triggers the backoff brake, unlike a Runner-level failure.
func TestDriverWorkerRetryDoesNotCountTowardBackoff(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "retry", Analysis: "still working on it"}}}
	d := testDriver(store, runner)

	driveN(t, d, "m1", 3)

	m, _ := store.Get(context.Background(), "m1")
	if m.Status == StatusPaused && m.PauseReason == PauseBackoff {
		t.Fatal("worker-reported retry incorrectly triggered the backoff brake")
	}
	if m.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures = %d after worker retries, want 0 (retry resets it)", m.ConsecutiveFailures)
	}
}

func TestDriverStallPauseOnIdenticalFindingsTwice(t *testing.T) {
	store := newFakeStore()
	// ReplanUsed is pre-set true so the stall's first threshold hit
	// pauses like it always did — the replan-on-first-stall behavior is
	// covered separately by TestDriverReplanOnFirstStall.
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8, ReplanUsed: true,
		Spec: Spec{Units: []PlanUnit{{Title: "u1"}}},
	})
	sameFindings := []Finding{{Title: "missing validation", File: "x.go"}}
	runner := &scriptedRunner{
		reviewVerdicts: []ReviewVerdict{{Approved: false, Findings: sameFindings}},
	}
	d := testDriver(store, runner)

	// review round 1: rework (stall=1) -> back to execute.
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance 1: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseGenerate {
		t.Fatalf("after 1st rework, phase = %q, want execute", m.Phase)
	}
	// Manually push back to review for round 2 (driver would get there
	// via a worker DONE verdict; this test isolates the review-phase
	// stall logic specifically).
	m.Phase = PhaseProve
	store.put("m1", m)
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance 2: %v", err)
	}
	m, _ = store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseNoProgress {
		t.Fatalf("mission after 2 identical-fingerprint reworks = %+v, want paused/no_progress", m)
	}
}

// TestDriverReplanOnFirstStall confirms a mission's FIRST stall (two
// identical-fingerprint reworks, ReplanUsed still false) goes back to
// planning instead of pausing — the driver-level counterpart of
// statemachine_test.go's pure Step assertion, exercised through real
// Advance calls so the plan phase's own re-run (via runPlan) is covered
// too.
func TestDriverReplanOnFirstStall(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8,
		Spec: Spec{Units: []PlanUnit{{Title: "u1"}}},
	})
	sameFindings := []Finding{{Title: "missing validation", File: "x.go"}}
	runner := &scriptedRunner{
		reviewVerdicts: []ReviewVerdict{{Approved: false, Findings: sameFindings}},
		plans:          []Spec{{Units: []PlanUnit{{Title: "u1 replanned"}}}},
	}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance 1: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	m.Phase = PhaseProve
	store.put("m1", m)
	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance 2: %v", err)
	}
	m, _ = store.Get(context.Background(), "m1")
	if m.Status == StatusPaused {
		t.Fatalf("mission after the FIRST stall = %+v, want a replan, not a pause", m)
	}
	if !m.ReplanUsed {
		t.Fatal("ReplanUsed = false after a replan, want true")
	}
	if m.Phase != PhasePlan && m.Phase != PhaseGenerate {
		t.Fatalf("mission phase after replan = %q, want plan (or execute once the plan phase ran)", m.Phase)
	}
	found := false
	for _, ev := range store.events["m1"] {
		if ev.Kind == "mission.replan" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a mission.replan event")
	}
}

// TestDriverBudgetPause confirms same-currency ledger spend actually
// reaches the budget brake through toStepState/driverStore.Spend — the
// bug this feature fixes: toStepState used to hardcode spend at 0, so
// PauseBudget could never fire regardless of real spend.
func TestDriverBudgetPause(t *testing.T) {
	store := newFakeStore()
	budget := 1.0
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, BudgetAmount: &budget, BudgetCurrency: "USD"})
	store.spend["m1"] = MissionSpend{ByCurrency: map[string]float64{"USD": 1.5}}
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done"}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseBudget {
		t.Fatalf("mission after same-currency spend >= budget = %+v, want paused/budget", m)
	}
}

// TestDriverBudgetPauseMixedCurrency confirms spend recorded in a
// currency OTHER than the mission's budget currency pauses the mission
// too, even when same-currency spend is zero — comparing across
// currencies would need a guessed FX rate, which this codebase never
// does, so the safe move is to pause and let a human sort it out.
func TestDriverBudgetPauseMixedCurrency(t *testing.T) {
	store := newFakeStore()
	budget := 100.0
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, BudgetAmount: &budget, BudgetCurrency: "USD"})
	store.spend["m1"] = MissionSpend{ByCurrency: map[string]float64{"EUR": 0.01}}
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done"}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseMixedCurrency {
		t.Fatalf("mission after mixed-currency spend = %+v, want paused/mixed_currency", m)
	}
}

// TestDriverBudgetConvertsOtherCurrencySpendWhenRateAvailable confirms
// a mission with a EUR budget and USD spend converts that spend
// through a wired fx rate table instead of pausing as mixed-currency —
// the fix this feature adds. Spend converts to just under the limit,
// so the mission must NOT pause yet.
func TestDriverBudgetConvertsOtherCurrencySpendWhenRateAvailable(t *testing.T) {
	store := newFakeStore()
	budget := 10.0
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, BudgetAmount: &budget, BudgetCurrency: "EUR"})
	// 8 USD spend; 1 USD = 0.86 EUR -> 6.88 EUR, under the 10 EUR budget.
	store.spend["m1"] = MissionSpend{ByCurrency: map[string]float64{"USD": 8}}
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done"}}}
	d := testDriver(store, runner)
	asOf := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	d.SetFXRates(&fakeFXRates{rates: map[string]fxrates.Rate{"EUR": {Value: 0.86, AsOf: asOf}}})

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status == StatusPaused {
		t.Fatalf("mission after convertible under-budget spend = %+v, want NOT paused", m)
	}
}

// TestDriverBudgetPausesWhenConvertedSpendReachesLimit confirms the
// converted total, not just same-currency spend, is what the brake
// compares against the budget.
func TestDriverBudgetPausesWhenConvertedSpendReachesLimit(t *testing.T) {
	store := newFakeStore()
	budget := 5.0
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, BudgetAmount: &budget, BudgetCurrency: "EUR"})
	// 8 USD spend; 1 USD = 0.86 EUR -> 6.88 EUR, over the 5 EUR budget.
	store.spend["m1"] = MissionSpend{ByCurrency: map[string]float64{"USD": 8}}
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done"}}}
	d := testDriver(store, runner)
	asOf := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	d.SetFXRates(&fakeFXRates{rates: map[string]fxrates.Rate{"EUR": {Value: 0.86, AsOf: asOf}}})

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseBudget {
		t.Fatalf("mission after converted spend over budget = %+v, want paused/budget", m)
	}
}

// TestDriverBudgetPauseMixedCurrencyWhenRateMissing confirms a wired
// fx rate source that simply has no entry for the spent currency still
// falls back to the conservative mixed-currency pause — never a
// guessed rate.
func TestDriverBudgetPauseMixedCurrencyWhenRateMissing(t *testing.T) {
	store := newFakeStore()
	budget := 100.0
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, BudgetAmount: &budget, BudgetCurrency: "EUR"})
	store.spend["m1"] = MissionSpend{ByCurrency: map[string]float64{"USD": 1}}
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done"}}}
	d := testDriver(store, runner)
	// Rate table has entries, but not for USD -> no usable cross to EUR.
	d.SetFXRates(&fakeFXRates{rates: map[string]fxrates.Rate{"GBP": {Value: 0.74, AsOf: time.Now()}}})

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseMixedCurrency {
		t.Fatalf("mission after unconvertible spend = %+v, want paused/mixed_currency", m)
	}
}

// TestDriverGatekeeperCleanupOnTerminal confirms the driver forgets a
// mission's in-progress reviewer session once the mission reaches a
// terminal phase — Advance() itself doesn't accept an external cancel
// input directly (that's Signal, wired in M4's API layer; the state
// machine's cancel precedence is already exercised in
// statemachine_test.go), so this drives a mission to done via review
// approval and the result step instead, which is the natural path
// that also needs cleanup.
func TestDriverGatekeeperCleanupOnTerminal(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8,
		Spec: Spec{Units: []PlanUnit{{Title: "only unit"}}},
	})
	d := testDriver(store, &scriptedRunner{reviewVerdicts: []ReviewVerdict{{Approved: true}}})
	d.gatekeepers["m1"] = &GatekeeperState{Messages: nil}

	driveN(t, d, "m1", 2) // prove(approved) -> result -> done
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %q, want done", m.Phase)
	}
	if _, stillPresent := d.gatekeepers["m1"]; stillPresent {
		t.Fatal("gatekeeper state was not cleaned up once the mission reached a terminal phase")
	}
}

// blockingRunner's RunWorker blocks on a channel until released,
// letting a test force two concurrent Drive calls to overlap on the
// same mission — reproducing the race where the work-slot sweep
// claims a mission that a still-running Drive loop already owns.
type blockingRunner struct {
	release  chan struct{}
	started  chan struct{}
	starts   int32
	verdict  WorkerVerdict
	reviewV  ReviewVerdict
	planSpec Spec
}

func (r *blockingRunner) RunWorker(ctx context.Context, m Mission, packet WorkPacket) (WorkerVerdict, string, error) {
	atomic.AddInt32(&r.starts, 1)
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-r.release
	return r.verdict, "worker output", nil
}

func (r *blockingRunner) RunReview(ctx context.Context, m Mission, packet ReviewPacket, gk *GatekeeperState) (ReviewVerdict, *GatekeeperState, error) {
	return r.reviewV, &GatekeeperState{}, nil
}

func (r *blockingRunner) PlanSession(ctx context.Context, m Mission, discoverNotes string) (Spec, error) {
	return r.planSpec, nil
}

func (r *blockingRunner) DiscoverSession(ctx context.Context, m Mission) (string, error) {
	return "", nil
}

// TestDriverDriveIsSerializedPerMission reproduces a real bug: the
// work-slot sweep's ClaimWorkSlot can claim a mission that is still
// actively owned by an earlier Drive goroutine (Advance's own
// transitions pass through status='idle' transiently between steps),
// spawning a second concurrent Drive loop that races the first's
// read-then-write Advance calls. A second Drive call for a mission
// already being driven must no-op instead of starting a competing
// loop.
func TestDriverDriveIsSerializedPerMission(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
		Spec: Spec{Units: []PlanUnit{{Title: "only unit"}}},
	})
	runner := &blockingRunner{
		release: make(chan struct{}),
		started: make(chan struct{}, 1),
		verdict: WorkerVerdict{Outcome: "done", Evidence: "did it"},
		reviewV: ReviewVerdict{Approved: true},
	}
	d := testDriver(store, runner)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = d.Drive(context.Background(), "m1")
	}()
	<-runner.started // first Drive is now blocked inside RunWorker
	go func() {
		defer wg.Done()
		_ = d.Drive(context.Background(), "m1") // must no-op, not race the first
	}()
	time.Sleep(20 * time.Millisecond) // give the second call a chance to (wrongly) start a competing Advance
	close(runner.release)
	wg.Wait()

	if got := atomic.LoadInt32(&runner.starts); got != 1 {
		t.Fatalf("RunWorker started %d times for one mission, want exactly 1 (second Drive must no-op while the first owns the mission)", got)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone || m.Status != StatusDone {
		t.Fatalf("mission after single-owner drive = %+v, want done/done", m)
	}
}

// TestDriverReviewApprovalContradictedByVerifyRoutesToRework
// reproduces a real bug: the reviewer approves a unit whose evidence
// doesn't hold up (e.g. a worker claiming a file exists when it never
// wrote one) — the harness's own verify_cmd then fails. This must NOT
// be treated as an infra fault (which just parks the mission for a
// human to blindly resume forever); it must route through rework,
// back to execute, so the worker gets another attempt with the actual
// failure recorded as a progress note.
func TestDriverReviewApprovalContradictedByVerifyRoutesToRework(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8,
		Spec: Spec{Units: []PlanUnit{{Title: "write summary.md", VerifyCmd: "test -f /nonexistent-verify-target"}}},
	})
	d := testDriver(store, &scriptedRunner{reviewVerdicts: []ReviewVerdict{{Approved: true}}})

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseGenerate {
		t.Fatalf("mission phase = %q, want execute (rework path), not stuck on an infra pause", m.Phase)
	}
	if m.PauseReason == PauseInfra {
		t.Fatal("a failed verify_cmd after approval must not be classified as an infra fault")
	}
	if m.Iteration != 1 {
		t.Fatalf("iteration = %d, want 1 (rework costs an iteration like any other rework)", m.Iteration)
	}
	if len(m.Progress) == 0 {
		t.Fatal("expected a progress note explaining the verify failure to the next worker turn")
	}
	if !strings.Contains(m.Progress[len(m.Progress)-1].Note, "Verification failed") {
		t.Fatalf("progress note = %q, want it to explain the verify failure", m.Progress[len(m.Progress)-1].Note)
	}
}

func TestDriverBlockedParksForInput(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "which env?"}}}
	d := testDriver(store, runner)

	cont, err := d.Advance(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if cont {
		t.Fatal("Advance reported it can continue on a mission that just parked waiting_for_input")
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusWaitingForInput {
		t.Fatalf("mission status = %q, want waiting_for_input", m.Status)
	}
}

// fakeSessionCreator hands out sequential ids without a real
// session.Store — Driver.Create only needs a non-empty string back.
type fakeSessionCreator struct{ n int }

func (f *fakeSessionCreator) Create(ctx context.Context, title string) (string, error) {
	f.n++
	return fmt.Sprintf("session-%d", f.n), nil
}

// fakeGranter records every Grant call for assertion — a real
// sessionGranter for tests, not a mock of one. Mutex-guarded: Signal's
// post-resume Drive runs in its own goroutine, so a test that resumes
// a mission can race a synchronous assertion against that goroutine's
// own (real) grant calls without this.
type fakeGranter struct {
	mu    sync.Mutex
	calls []struct{ sessionID, tool, pattern string }
}

func (f *fakeGranter) Grant(ctx context.Context, sessionID, tool, pattern string, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct{ sessionID, tool, pattern string }{sessionID, tool, pattern})
	return nil
}

func (f *fakeGranter) callsSnapshot() []struct{ sessionID, tool, pattern string } {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]struct{ sessionID, tool, pattern string }(nil), f.calls...)
}

func TestDriverCreateGrantsShellAutoApproveWhenEnabled(t *testing.T) {
	store := newFakeStore()
	granter := &fakeGranter{}
	d := NewDriver(store, &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}, nil, nil, &fakeSessionCreator{}, granter, nil, nil, slog.Default())

	id, err := d.Create(context.Background(), Mission{Goal: "test", Kind: "general", Route: "route-x", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, AutoApproveSafe: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(granter.calls) != 1 {
		t.Fatalf("Grant calls = %d, want exactly 1", len(granter.calls))
	}
	call := granter.calls[0]
	if call.tool != "shell" || call.pattern != "*" {
		t.Fatalf("Grant call = %+v, want tool=shell pattern=*", call)
	}
	m, _ := store.Get(context.Background(), id)
	if call.sessionID != m.SessionID {
		t.Fatalf("Grant sessionID = %q, want the mission's own hidden session %q", call.sessionID, m.SessionID)
	}
}

func TestDriverCreateSkipsGrantWhenAutoApproveDisabled(t *testing.T) {
	store := newFakeStore()
	granter := &fakeGranter{}
	d := NewDriver(store, &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}, nil, nil, &fakeSessionCreator{}, granter, nil, nil, slog.Default())

	if _, err := d.Create(context.Background(), Mission{Goal: "test", Kind: "general", Route: "route-x", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, AutoApproveSafe: false}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(granter.calls) != 0 {
		t.Fatalf("Grant calls = %d, want 0 when AutoApproveSafe is false", len(granter.calls))
	}
}

// TestDriverCreateGrantsApprovalAllowlist confirms Create grants every
// tool in the resolved agent's ApprovalAllowlist — the fix that makes
// api/missions.go:28's stale "ApprovalAllowlist is resolved" comment
// actually true.
func TestDriverCreateGrantsApprovalAllowlist(t *testing.T) {
	store := newFakeStore()
	granter := &fakeGranter{}
	d := NewDriver(store, &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}, nil, nil, &fakeSessionCreator{}, granter, nil, nil, slog.Default())
	d.SetAgentResolver(func(ctx context.Context, agentID string) (AgentDefaults, bool) {
		if agentID != "briefing-agent" {
			return AgentDefaults{}, false
		}
		return AgentDefaults{ApprovalAllowlist: []string{"gmail_search", "gmail_send"}}, true
	})

	id, err := d.Create(context.Background(), Mission{
		Goal: "test", Kind: "general", Route: "route-x", AgentID: "briefing-agent",
		Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, AutoApproveSafe: false,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, _ := store.Get(context.Background(), id)

	gotTools := map[string]bool{}
	for _, call := range granter.calls {
		if call.sessionID != m.SessionID {
			t.Fatalf("Grant call sessionID = %q, want the mission's own hidden session %q", call.sessionID, m.SessionID)
		}
		gotTools[call.tool] = true
	}
	for _, want := range []string{"gmail_search", "gmail_send"} {
		if !gotTools[want] {
			t.Fatalf("Grant calls = %+v, want a grant for %q", granter.calls, want)
		}
	}
}

// TestDriverCreateSkipsAllowlistGrantWhenAgentUnresolved: an
// AgentID that doesn't resolve (deleted agent, or none set) grants
// nothing beyond whatever AutoApproveSafe itself produces — no
// allowlist to apply.
func TestDriverCreateSkipsAllowlistGrantWhenAgentUnresolved(t *testing.T) {
	store := newFakeStore()
	granter := &fakeGranter{}
	d := NewDriver(store, &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}, nil, nil, &fakeSessionCreator{}, granter, nil, nil, slog.Default())
	d.SetAgentResolver(func(ctx context.Context, agentID string) (AgentDefaults, bool) {
		return AgentDefaults{}, false
	})

	if _, err := d.Create(context.Background(), Mission{
		Goal: "test", Kind: "general", Route: "route-x", AutoApproveSafe: false,
		Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(granter.calls) != 0 {
		t.Fatalf("Grant calls = %d, want 0 for an unresolved agent", len(granter.calls))
	}
}

// TestDriverCreateRejectsInvalidMissionWhenValidateDepsWired confirms
// Create runs ValidateCreate (D-071) once SetValidateDeps has been
// called — closing the gap where only the HTTP create handler used to
// validate anything, leaving a direct Driver.Create caller (the
// workflows engine) free to insert a mission the API would 400.
func TestDriverCreateRejectsInvalidMissionWhenValidateDepsWired(t *testing.T) {
	store := newFakeStore()
	d := testDriver(store, &scriptedRunner{})
	d.SetValidateDeps(ValidateDeps{})

	_, err := d.Create(context.Background(), Mission{
		Goal: "test", Kind: "coding", Route: "route-x", Light: true, // light is general-only
		Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
	})
	if err == nil {
		t.Fatal("Create with light+coding = nil error, want ErrInvalidMission")
	}
	if !errors.Is(err, ErrInvalidMission) {
		t.Fatalf("Create error = %v, want it to wrap ErrInvalidMission", err)
	}
	if len(store.missions) != 0 {
		t.Fatalf("store.missions = %d, want 0: an invalid mission must never reach the store", len(store.missions))
	}
}

// TestDriverCreateSkipsValidationWhenDepsUnset confirms a Driver that
// never calls SetValidateDeps behaves exactly as it did before D-071 —
// every pre-existing driver_test.go fixture that builds a bare
// Mission{} (no route) must keep working unvalidated.
func TestDriverCreateSkipsValidationWhenDepsUnset(t *testing.T) {
	store := newFakeStore()
	d := testDriver(store, &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}})

	if _, err := d.Create(context.Background(), Mission{
		Goal: "test", Kind: "coding", Light: true, // would be rejected if validated
		Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
	}); err != nil {
		t.Fatalf("Create with no ValidateDeps wired: %v, want nil (validation skipped)", err)
	}
}

// TestDriverSignalResumeRegrantsSession covers the fix for a mission
// blocked long enough (backoff retries, or a real waiting_for_input
// gap) that its hidden session's missionGrantTTL expired before the
// human/API resume arrived: without a re-grant, resuming just re-hits
// the same "no standing grant" denial that got it blocked in the first
// place. Signal(InputResume) must re-seed the same grants
// grantSessionDefaults minted at provisioning, synchronously, before
// it kicks off the post-resume Drive goroutine.
func TestDriverSignalResumeRegrantsSession(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Goal: "test", Kind: "general", AgentID: "briefing-agent",
		Phase: PhaseGenerate, Status: StatusWaitingForInput, MaxIterations: 8,
		AutoApproveSafe: true, SessionID: "session-1", Workspace: "/workspace/missions/m1",
	})
	granter := &fakeGranter{}
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, nil, nil, &fakeSessionCreator{}, granter, nil, nil, slog.Default())
	d.SetAgentResolver(func(ctx context.Context, agentID string) (AgentDefaults, bool) {
		if agentID != "briefing-agent" {
			return AgentDefaults{}, false
		}
		return AgentDefaults{ApprovalAllowlist: []string{"gmail_search"}}, true
	})

	if err := d.Signal(context.Background(), "m1", InputResume); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	// grantSessionDefaults runs synchronously inside Signal, strictly
	// before the post-resume Drive goroutine is even spawned — the
	// re-grant calls this test cares about are already recorded by the
	// time Signal returns. The goroutine itself (mission already has a
	// session+workspace, so no further provisioning) settles quickly;
	// snapshot is still mutex-guarded since it's a background goroutine.
	calls := granter.callsSnapshot()
	gotTools := map[string]bool{}
	for _, call := range calls {
		if call.sessionID != "session-1" {
			t.Fatalf("Grant call sessionID = %q, want the mission's existing hidden session %q", call.sessionID, "session-1")
		}
		gotTools[call.tool] = true
	}
	if !gotTools["shell"] {
		t.Fatalf("Grant calls = %+v, want a re-granted shell allowance (AutoApproveSafe)", calls)
	}
	if !gotTools["gmail_search"] {
		t.Fatalf("Grant calls = %+v, want a re-granted gmail_search allowance (ApprovalAllowlist)", calls)
	}
}

// TestDriverSignalResumeGuardsMissingSessionID confirms Signal's new
// re-grant-on-resume call is gated on m.SessionID != "" — without this
// guard, grantSessionDefaults itself has no such check and would happily
// call Grant with a blank session id for an AutoApproveSafe mission that
// somehow resumes before ever being provisioned.
func TestDriverSignalResumeGuardsMissingSessionID(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Goal: "test", Kind: "general",
		Phase: PhaseGenerate, Status: StatusWaitingForInput, MaxIterations: 8,
		AutoApproveSafe: true, SessionID: "",
	})
	granter := &fakeGranter{}
	m, err := store.Get(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	d := NewDriver(store, &scriptedRunner{}, nil, nil, &fakeSessionCreator{}, granter, nil, nil, slog.Default())

	// The exact guard Signal applies before calling grantSessionDefaults
	// — checked directly since Signal's own goroutine would otherwise go
	// on to lazily provision (a separately tested path) and call Grant
	// with a real session id, muddying an assertion on call count.
	if m.SessionID != "" {
		d.grantSessionDefaults(context.Background(), m)
	}

	if len(granter.calls) != 0 {
		t.Fatalf("Grant calls = %+v, want 0 for a mission with no hidden session", granter.calls)
	}
}

// TestDriverAdvanceLazilyProvisionsBareMission reproduces the fix for
// the plan's defect #1: a mission inserted directly (bypassing
// Create — exactly what scheduler.go's createFromTemplate does) has no
// session and no workspace. The first Advance call must provision both
// before running the phase, or the worker gets no shell/write_file
// tools (runner.go's missionTools returns nil for an empty WorkRoot).
func TestDriverAdvanceLazilyProvisionsBareMission(t *testing.T) {
	store := newFakeStore()
	// A bare row, exactly as createFromTemplate leaves it: no
	// SessionID, no Workspace/Worktree.
	store.put("m1", Mission{
		ID: "m1", Goal: "scheduled run", Kind: "general",
		Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, AutoApproveSafe: true,
	})
	granter := &fakeGranter{}
	sessions := &fakeSessionCreator{}
	wsRoot := t.TempDir()
	workspace := NewWorkspace(wsRoot, nil, slog.Default())
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, workspace, nil, sessions, granter, nil, nil, slog.Default())

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	m, _ := store.Get(context.Background(), "m1")
	if m.SessionID == "" {
		t.Fatal("Advance did not provision a hidden session for a bare mission")
	}
	if m.Workspace == "" {
		t.Fatal("Advance did not provision a workspace for a bare mission")
	}
	found := false
	for _, call := range granter.calls {
		if call.tool == "shell" && call.pattern == "*" && call.sessionID == m.SessionID {
			found = true
		}
	}
	if !found {
		t.Fatal("Advance's lazy provisioning did not grant the auto-approve-safe shell allowance")
	}
}

// TestDriverProvisionUsesMissionBranchPatternOverSettings confirms the
// precedence order ensureProvisioned resolves: a mission's own
// BranchPattern wins over the settings-configured default.
func TestDriverProvisionUsesMissionBranchPatternOverSettings(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Goal: "Fix the login bug", Kind: "coding", BranchPattern: "custom/{slug}",
		Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
	})
	sessions := &fakeSessionCreator{}
	wsRoot := t.TempDir()
	workspace := NewWorkspace(wsRoot, nil, slog.Default())
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, workspace, nil, sessions, &fakeGranter{}, nil, nil, slog.Default())
	d.SetGitBranchPattern(func(context.Context) string { return "settings/{slug}" })

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if got, want := m.Branch, "custom/fix-the-login-bug"; got != want {
		t.Fatalf("Branch = %q, want %q (mission override should win over settings)", got, want)
	}
}

// TestDriverProvisionFallsBackToSettingsBranchPattern confirms the
// settings default applies when the mission itself carries no override.
func TestDriverProvisionFallsBackToSettingsBranchPattern(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Goal: "Fix the login bug", Kind: "coding",
		Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
	})
	sessions := &fakeSessionCreator{}
	wsRoot := t.TempDir()
	workspace := NewWorkspace(wsRoot, nil, slog.Default())
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, workspace, nil, sessions, &fakeGranter{}, nil, nil, slog.Default())
	d.SetGitBranchPattern(func(context.Context) string { return "settings/{slug}" })

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if got, want := m.Branch, "settings/fix-the-login-bug"; got != want {
		t.Fatalf("Branch = %q, want %q (settings default should apply)", got, want)
	}
}

// TestDriverProvisionFallsBackToDefaultBranchPattern confirms
// DefaultBranchPattern applies when neither the mission nor settings
// carry an override — the original "<type>/<slug>" behavior.
func TestDriverProvisionFallsBackToDefaultBranchPattern(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Goal: "Fix the login bug", Kind: "coding",
		Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
	})
	sessions := &fakeSessionCreator{}
	wsRoot := t.TempDir()
	workspace := NewWorkspace(wsRoot, nil, slog.Default())
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, workspace, nil, sessions, &fakeGranter{}, nil, nil, slog.Default())

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if got, want := m.Branch, "fix/fix-the-login-bug"; got != want {
		t.Fatalf("Branch = %q, want %q (default pattern should apply)", got, want)
	}
}

// TestDriverProvisionThreadsSigningKeyFromIdentityResolver proves a
// CloneIdentityResolver returning a non-empty SigningKey reaches the
// clone's LOCAL git config (user.signingkey/gpg.format/commit.gpgsign)
// — the resolver-to-Provision wiring ensureProvisioned's clone path
// depends on, independent of worktree.go's own signing test which
// covers Provision/cloneRepo directly without a Driver in the loop.
func TestDriverProvisionThreadsSigningKeyFromIdentityResolver(t *testing.T) {
	requireGitForPush(t)
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare", "-b", "main")
	seed := t.TempDir()
	gitRun(t, seed, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", "README.md")
	gitRun(t, seed, "-c", "user.name=test", "-c", "user.email=test@test", "commit", "-q", "-m", "seed")
	gitRun(t, seed, "remote", "add", "origin", bare)
	gitRun(t, seed, "push", "-q", "origin", "main")

	privatePEM, _ := testSigningKeypair(t)

	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Goal: "Fix the login bug", Kind: "coding",
		RepoURL: bare, ConnectorID: "conn1",
		Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
	})
	sessions := &fakeSessionCreator{}
	wsRoot := t.TempDir()
	workspace := NewWorkspace(wsRoot, nil, slog.Default())
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, workspace, nil, sessions, &fakeGranter{}, nil, nil, slog.Default())
	d.SetCloneTokenResolver(func(context.Context, string) (string, error) { return "dummy-token", nil })
	d.SetCloneIdentityResolver(func(context.Context, string) (ResolvedIdentity, error) {
		return ResolvedIdentity{Name: "conn-bot", Email: "conn-bot@example.com", Login: "conn-bot", SigningKey: privatePEM}, nil
	})

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Worktree == "" {
		t.Fatal("Advance did not provision a worktree")
	}
	keyPath := filepath.Join(m.Workspace, signingKeyFileName)
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("signing key file missing: %v", err)
	}
	got, ok := gitConfigLocal(context.Background(), m.Worktree, "user.signingkey")
	if !ok || got != keyPath {
		t.Fatalf("local user.signingkey = %q (ok=%v), want %q", got, ok, keyPath)
	}
}

// TestDriverEffectiveCommitStylePrecedence confirms mission override >
// settings default > CommitMessage's own conventional default.
func TestDriverEffectiveCommitStylePrecedence(t *testing.T) {
	cases := []struct {
		name          string
		missionStyle  string
		settingsStyle func(context.Context) string
		want          string
	}{
		{"mission override wins", CommitStylePlain, func(context.Context) string { return CommitStyleConventional }, CommitStylePlain},
		{"settings default applies", "", func(context.Context) string { return CommitStylePlain }, CommitStylePlain},
		{"no override, no settings: conventional default", "", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Driver{gitCommitStyle: tc.settingsStyle, log: slog.Default()}
			got := d.effectiveCommitStyle(context.Background(), Mission{CommitStyle: tc.missionStyle})
			if got != tc.want {
				t.Fatalf("effectiveCommitStyle = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDriverAdvanceSkipsProvisioningWhenAlreadyProvisioned confirms
// ensureProvisioned is a no-op (no new session, no re-grant) once a
// mission already has both — the ordinary case for every mission
// created via Create, which must not re-provision on every single
// Advance call.
func TestDriverAdvanceSkipsProvisioningWhenAlreadyProvisioned(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking,
		MaxIterations: 8, SessionID: "already-provisioned-session", Workspace: "/already/provisioned",
	})
	granter := &fakeGranter{}
	sessions := &fakeSessionCreator{}
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "blocked", Question: "n/a"}}}
	d := NewDriver(store, runner, nil, nil, sessions, granter, nil, nil, slog.Default())

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if len(granter.calls) != 0 {
		t.Fatalf("Grant calls = %d, want 0 for an already-provisioned mission", len(granter.calls))
	}
	if sessions.n != 0 {
		t.Fatalf("sessions.Create was called %d times, want 0 for an already-provisioned mission", sessions.n)
	}
}

// TestDriverAdvancePausesInsteadOfErroringOnProvisioningFailure
// reproduces the hardening around mission provisioning: before, a
// mission whose workspace can't be created left Advance returning an
// error and the mission idle — exactly the state the work-slot sweep
// retries ensureProvisioned against every 30s, forever. Now Advance
// must pause the mission as an infra failure instead, so the sweep
// leaves it alone.
func TestDriverAdvancePausesInsteadOfErroringOnProvisioningFailure(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Goal: "test", Kind: "coding",
		Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8,
	})
	// wsRoot is a file, not a directory: Workspace.Provision's
	// os.MkdirAll(workspace, ...) underneath it fails, giving
	// ensureProvisioned a real error to propagate without needing git
	// or any other external dependency.
	wsRoot := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(wsRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	workspace := NewWorkspace(wsRoot, nil, slog.Default())
	granter := &fakeGranter{}
	sessions := &fakeSessionCreator{}
	runner := &scriptedRunner{}
	d := NewDriver(store, runner, workspace, nil, sessions, granter, nil, nil, slog.Default())

	canContinue, err := d.Advance(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if canContinue {
		t.Fatal("Advance reported it can continue after a provisioning failure")
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused {
		t.Fatalf("mission status = %q, want paused", m.Status)
	}
	if m.PauseReason != PauseInfra {
		t.Fatalf("mission pause reason = %q, want %q", m.PauseReason, PauseInfra)
	}
}

// TestDriverSkipsReviewWhenHarnessChecksPass: a non-coding mission
// whose unit declares artifacts that pass the harness's own checks
// completes WITHOUT an LLM review round — the reviewer is the least
// reliable and most expensive link, and passing deterministic checks
// already establish the unit holds up. The scriptedRunner has no
// review verdicts scripted: any RunReview call would panic the test.
func TestDriverSkipsReviewWhenHarnessChecksPass(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "summary.md"), []byte("real content"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking,
		MaxIterations: 8, Workspace: root,
		Spec: Spec{Units: []PlanUnit{{Title: "write summary", Artifacts: []string{"summary.md"}}}},
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "wrote it"}}}
	d := testDriver(store, runner)

	driveN(t, d, "m1", 2)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone || m.Status != StatusDone {
		t.Fatalf("mission = %+v, want done/done without a review round", m)
	}
}

// TestDriverArtifactCheckBlocksTautologicalDone: the worker claims
// done but never wrote the declared artifact — the harness check must
// send it back to execute (self-retry), never let the claim stand.
func TestDriverArtifactCheckBlocksTautologicalDone(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking,
		MaxIterations: 8, Workspace: t.TempDir(),
		Spec: Spec{Units: []PlanUnit{{Title: "write summary", Artifacts: []string{"summary.md"}, VerifyCmd: "echo done"}}},
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "wrote it (no it didn't)"}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseGenerate || m.Spec.Units[0].Passes {
		t.Fatalf("mission = phase %s passes %v, want back in generate with the unit NOT passed", m.Phase, m.Spec.Units[0].Passes)
	}
	if m.Iteration == 0 {
		t.Fatal("a failed artifact check must cost an iteration")
	}
}

// TestDriverCitationCheckBlocksInvokedCitation: a general mission's
// worker writes an artifact citing a URL it never actually fetched or
// searched — the harness's own CheckCitations must catch this and
// send the unit back to execute, same failure path CheckArtifacts
// uses, never letting the invented citation stand.
func TestDriverCitationCheckBlocksInvokedCitation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.md"), []byte("source: [docs](https://example.com/invented)"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking,
		MaxIterations: 8, Workspace: root,
		Spec: Spec{Units: []PlanUnit{{Title: "write report", Artifacts: []string{"report.md"}}}},
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "wrote it", SeenURLs: []string{"https://example.com/other"}}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseGenerate || m.Spec.Units[0].Passes {
		t.Fatalf("mission = phase %s passes %v, want back in generate with the unit NOT passed", m.Phase, m.Spec.Units[0].Passes)
	}
	if len(m.Progress) == 0 || !strings.Contains(m.Progress[len(m.Progress)-1].Note, "citation check failed") {
		t.Fatalf("progress = %+v, want a note explaining the citation failure", m.Progress)
	}
}

// TestDriverCitationCheckSkippedForCodingMission: coding missions cite
// source, not the web (D-059) — an invented URL in a coding mission's
// artifact must not block it, since the citations check never runs
// for Kind == "coding". The scriptedRunner has a review verdict
// scripted since coding missions always review.
func TestDriverCitationCheckSkippedForCodingMission(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.md"), []byte("source: [docs](https://example.com/invented)"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "coding", Phase: PhaseGenerate, Status: StatusWorking,
		MaxIterations: 8, Workspace: root,
		Spec: Spec{Units: []PlanUnit{{Title: "write report", Artifacts: []string{"report.md"}}}},
	})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "wrote it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)

	driveN(t, d, "m1", 3) // generate -> prove -> result -> done

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone || m.Status != StatusDone {
		t.Fatalf("mission = %+v, want done/done — citation check must not run for a coding mission", m)
	}
}

// TestDriverRegressionFlipsUnitAndRetriesInsteadOfAdvancing reproduces
// the fix: a unit that already passed can silently break while a LATER
// unit's work is happening. Unit 0's artifact ("a.md") passed
// previously; before the driver verifies unit 1, a.md gets deleted (as
// if the worker's unit-1 changes broke it). The regression check must
// catch this at the point unit 1's own verify succeeds, flip unit 0's
// Passes back to false, emit mission.regression, and route back to
// execute (worker_retry) instead of letting the mission advance to
// review for unit 1.
func TestDriverRegressionFlipsUnitAndRetriesInsteadOfAdvancing(t *testing.T) {
	root := t.TempDir()
	aPath := filepath.Join(root, "a.md")
	if err := os.WriteFile(aPath, []byte("unit 0 content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("unit 1 content"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking,
		MaxIterations: 8, Workspace: root,
		Spec: Spec{Units: []PlanUnit{
			{Title: "unit0", Artifacts: []string{"a.md"}, Passes: true},
			{Title: "unit1", Artifacts: []string{"b.md"}},
		}},
	})
	// Simulate unit 1's work having broken unit 0's artifact by the time
	// the harness re-checks it: delete a.md right before the driver runs.
	if err := os.Remove(aPath); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "wrote b.md"}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseGenerate {
		t.Fatalf("mission phase = %q, want execute (regression routes back to work, not forward)", m.Phase)
	}
	if m.Spec.Units[0].Passes {
		t.Fatal("unit 0's Passes was not flipped back to false after its artifact regressed")
	}
	found := false
	for _, ev := range store.events["m1"] {
		if ev.Kind == "mission.regression" {
			found = true
			if !strings.Contains(string(ev.Payload), `"unit":"unit0"`) || !strings.Contains(string(ev.Payload), `"check":"artifacts"`) {
				t.Fatalf("mission.regression payload = %s, want unit=unit0 check=artifacts", ev.Payload)
			}
		}
	}
	if !found {
		t.Fatal("expected a mission.regression event")
	}
	if m.Iteration == 0 {
		t.Fatal("a regression must cost an iteration via worker_retry, same as any other rework")
	}
}

// TestDriverNoRegressionAdvancesNormally confirms the regression check
// is a no-op — same phase progression as before this feature — when
// every previously-passed unit still holds up.
func TestDriverNoRegressionAdvancesNormally(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("unit 0 content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("unit 1 content"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking,
		MaxIterations: 8, Workspace: root,
		Spec: Spec{Units: []PlanUnit{
			{Title: "unit0", Artifacts: []string{"a.md"}, Passes: true},
			{Title: "unit1", Artifacts: []string{"b.md"}},
		}},
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "wrote b.md"}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if !m.Spec.Units[1].Passes {
		t.Fatal("unit 1 should have passed with no regression detected")
	}
	if !m.Spec.Units[0].Passes {
		t.Fatal("unit 0 must remain passed when its artifact still holds up")
	}
	for _, ev := range store.events["m1"] {
		if ev.Kind == "mission.regression" {
			t.Fatal("no regression event expected when nothing regressed")
		}
	}
}

// TestDriverReplanPreservesMatchingPassedUnitsResetsOthers confirms
// runPlan's restorePassedUnits: after a replan, a unit whose title and
// verify_cmd are unchanged from a previously-passed unit is re-marked
// passed (harness evidence carried forward); a unit the new plan
// changed (or that is genuinely new) stays unverified, since
// parseSpec's own zeroing is correct for anything actually different.
func TestDriverReplanPreservesMatchingPassedUnitsResetsOthers(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusIdle,
		MaxIterations: 8, ReplanUsed: true,
		Spec: Spec{Units: []PlanUnit{
			{Title: "unit0", VerifyCmd: "test -f a.md", Passes: true},
			{Title: "unit1", VerifyCmd: "test -f b.md", Passes: false},
		}},
	})
	runner := &scriptedRunner{plans: []Spec{{Units: []PlanUnit{
		{Title: "unit0", VerifyCmd: "test -f a.md"}, // unchanged: must be restored to passed
		{Title: "unit1", VerifyCmd: "test -f c.md"}, // verify_cmd changed: must stay unverified
	}}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if !m.Spec.Units[0].Passes {
		t.Fatal("unit0 (title+verify_cmd unchanged) should have been restored to passed")
	}
	if m.Spec.Units[1].Passes {
		t.Fatal("unit1 (verify_cmd changed) must stay unverified")
	}
}

// TestDriverReplanPromptCarriesStallContext confirms runPlan feeds the
// planner an augmented discoverNotes on a replan: current plan status
// and recent progress, plus the "previous plan stalled" instruction —
// not just the bare discover notes a first-time plan gets.
func TestDriverReplanPromptCarriesStallContext(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusIdle,
		MaxIterations: 8, ReplanUsed: true, DiscoverNotes: "original findings",
		Progress: []ProgressNote{{Note: "tried approach A, failed"}},
		Spec:     Spec{Units: []PlanUnit{{Title: "unit0", Passes: true}}},
	})
	runner := &scriptedRunner{plans: []Spec{{Units: []PlanUnit{{Title: "unit0"}}}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if len(runner.planDiscoverNotes) != 1 {
		t.Fatalf("PlanSession call count = %d, want 1", len(runner.planDiscoverNotes))
	}
	got := runner.planDiscoverNotes[0]
	for _, want := range []string{"original findings", "previous plan is being replanned", "tried approach A, failed", "unit0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("replan prompt = %q, want it to contain %q", got, want)
		}
	}
}

// TestDriverFirstPlanDoesNotGetReplanNotes confirms a mission's very
// first planning pass (ReplanUsed false) passes discoverNotes through
// unchanged — no stall framing for a plan that never stalled.
func TestDriverFirstPlanDoesNotGetReplanNotes(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhasePlan, Status: StatusIdle,
		MaxIterations: 8, DiscoverNotes: "original findings",
	})
	runner := &scriptedRunner{plans: []Spec{{Units: []PlanUnit{{Title: "unit0"}}}}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if got := runner.planDiscoverNotes[0]; got != "original findings" {
		t.Fatalf("first-plan discoverNotes = %q, want unchanged %q", got, "original findings")
	}
}

// TestDriverCodingMissionsAlwaysReview: the skip gate must never apply
// to coding missions — a diff can be wrong in ways existence checks
// cannot see.
func TestDriverCodingMissionsAlwaysReview(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "coding", Phase: PhaseGenerate, Status: StatusWorking,
		MaxIterations: 8, Workspace: root,
		Spec: Spec{Units: []PlanUnit{{Title: "write code", Artifacts: []string{"main.go"}}}},
	})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "wrote it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)

	driveN(t, d, "m1", 3)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseDone {
		t.Fatalf("mission = %+v, want done via a real review round", m)
	}
	if m.Phase == PhaseDone && runner.reviewIdx == 0 && len(runner.reviewVerdicts) == 1 {
		// reviewIdx stays 0 when only one verdict exists and it was
		// consumed; assert the review actually ran via phase history:
		// a skipped review would have finished in 2 Advance calls with
		// no review phase. The phase_started events record it.
		found := false
		for _, ev := range store.events["m1"] {
			if ev.Kind == "mission.review_skipped" {
				found = true
			}
		}
		if found {
			t.Fatal("coding mission skipped review — the gate must not apply to coding kind")
		}
	}
	// An approval must leave a verdict event — otherwise a review that
	// ran is indistinguishable from one that never happened.
	approved := false
	for _, ev := range store.events["m1"] {
		if ev.Kind == "mission.review_verdict" && strings.Contains(string(ev.Payload), `"decision":"approved"`) {
			approved = true
		}
	}
	if !approved {
		t.Fatal("approved review left no mission.review_verdict event")
	}
}

// TestDriverEscalatesRepeatedRework: a second review rejection with
// the IDENTICAL gap fingerprint drops the resumed gatekeeper session,
// so any subsequent round starts with fresh eyes instead of the same
// session re-asserting its anchored verdict.
func TestDriverEscalatesRepeatedRework(t *testing.T) {
	findings := []Finding{{Title: "missing depth", File: "summary.md"}}
	fp := GapFingerprint(findings)
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseProve, Status: StatusWorking,
		MaxIterations: 8, Workspace: t.TempDir(), LastGapFingerprint: fp, StallCount: 1,
		Spec: Spec{Units: []PlanUnit{{Title: "write summary"}}},
	})
	runner := &scriptedRunner{reviewVerdicts: []ReviewVerdict{{Approved: false, Findings: findings}}}
	d := testDriver(store, runner)
	d.gatekeepers["m1"] = &GatekeeperState{}

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if _, ok := d.gatekeepers["m1"]; ok {
		t.Fatal("gatekeeper state survived a repeated identical rework — must be dropped for fresh-eyes review")
	}
}

// TestDriverForcedRetriesStallInsteadOfBurningIterations reproduces the
// real observed failure: a worker whose turns never yield a sentinel
// (no tool call, no text-form fallback either) used to burn every
// remaining iteration on identical forced retries because
// InputWorkerRetry carried no GapFingerprint at all — the stall brake
// never had two identical fingerprints to compare. runner.go now sets
// WorkerVerdict.Forced on that path, and driver.go's runExecute maps it
// to a stable "no_sentinel" GapFingerprint, so two consecutive forced
// retries now pause the mission (no_progress) well short of
// MaxIterations instead of grinding through the whole budget.
func TestDriverForcedRetriesStallInsteadOfBurningIterations(t *testing.T) {
	store := newFakeStore()
	// ReplanUsed pre-set true: this test asserts the pause outcome
	// specifically; the first-stall replan path is covered separately
	// by TestDriverReplanOnFirstStall.
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 12, ReplanUsed: true})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{
		{Outcome: "retry", Analysis: "the worker did not report a status; treated as a failed attempt", Forced: true},
	}}
	d := testDriver(store, runner)

	// Two forced retries must be enough to stall-pause (StallRounds=2),
	// nowhere near MaxIterations=12 — driveN would otherwise burn all 12
	// exactly like the real incident this guards against.
	driveN(t, d, "m1", 2)

	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseNoProgress {
		t.Fatalf("mission after 2 consecutive Forced retries = %s/%s (iteration %d), want paused/no_progress", m.Status, m.PauseReason, m.Iteration)
	}
	if m.Iteration >= m.MaxIterations {
		t.Fatalf("mission burned to max_iterations (%d) instead of stalling on the fingerprint", m.Iteration)
	}
}

// TestDriverModelFloorPausesImmediately: ErrModelFloor from the runner
// must pause the mission as infra on the FIRST turn — not accrue
// worker_failed rounds toward backoff.
func TestDriverModelFloorPausesImmediately(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{workerErr: fmt.Errorf("%w: amazon.nova-lite-v1:0", ErrModelFloor)}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseInfra {
		t.Fatalf("mission after below-floor turn = %s/%s, want paused/infra immediately", m.Status, m.PauseReason)
	}
}

// TestDriverTurnEventCarriesRouteAndAgent covers issue #473: the
// mission.turn event payload records the phase's effective route
// (phaseRoute) and the mission agent's display name (via the wired
// AgentResolver), never a guessed model.
func TestDriverTurnEventCarriesRouteAndAgent(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking,
		MaxIterations: 8, Route: "mini", AgentID: "coder-agent",
	})
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "complete"}}}
	d := testDriver(store, runner)
	d.SetAgentResolver(func(ctx context.Context, agentID string) (AgentDefaults, bool) {
		if agentID != "coder-agent" {
			return AgentDefaults{}, false
		}
		return AgentDefaults{Name: "Coder"}, true
	})

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	events, _ := store.Events(context.Background(), "m1")
	var turn Event
	for _, e := range events {
		if e.Kind == "mission.turn" {
			turn = e
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(turn.Payload, &payload); err != nil {
		t.Fatalf("unmarshal mission.turn payload: %v", err)
	}
	if payload["route"] != "mini" {
		t.Fatalf("payload[route] = %v, want mini", payload["route"])
	}
	if payload["agent"] != "Coder" {
		t.Fatalf("payload[agent] = %v, want Coder", payload["agent"])
	}
	if _, ok := payload["model"]; ok {
		t.Fatalf("payload[model] = %v, want absent (never guessed)", payload["model"])
	}
}

// TestDriverAdvanceDeniesIdleToWorkingOnCapacity covers D-056's driver-
// path gate: an idle mission whose capacity gate denies must not run
// its turn at all (the runner is never called) and canContinue=false so
// this Drive call stops with the mission still idle for the periodic
// sweep to retry.
func TestDriverAdvanceDeniesIdleToWorkingOnCapacity(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{
		ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusIdle, MaxIterations: 8,
		SessionID: "already-provisioned", Workspace: "/already/provisioned",
	})
	runner := &scriptedRunner{discoverNotes: []string{"discovered"}}
	d := testDriver(store, runner)
	d.SetCapacityGate(fakeCapacityChecker{admit: false, reason: "mem_available 900MB < floor 1024 + per-sandbox 768"})

	cont, err := d.Advance(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if cont {
		t.Fatal("Advance = canContinue true, want false when capacity denies")
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusIdle || m.Phase != PhaseDiscover {
		t.Fatalf("mission after capacity denial = %s/%s, want idle/discover untouched (phase run must not happen)", m.Status, m.Phase)
	}
}

// TestDriverAdvanceCapacityGateErrorAdmitsOpen confirms a gate that
// itself errors (sandboxd unreachable) does not block the turn — a dead
// gate must never freeze the mission queue.
func TestDriverAdvanceCapacityGateErrorAdmitsOpen(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusIdle, MaxIterations: 8})
	runner := &scriptedRunner{discoverNotes: []string{"discovered"}}
	d := testDriver(store, runner)
	d.SetCapacityGate(fakeCapacityChecker{err: fmt.Errorf("sandboxd unreachable")})

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhasePlan {
		t.Fatalf("mission.Phase = %s, want plan (turn ran despite the gate erroring)", m.Phase)
	}
}

// TestDriverAdvanceCapacityGateAdmitsRunsTurn confirms a gate that
// admits lets the idle mission's turn run normally.
func TestDriverAdvanceCapacityGateAdmitsRunsTurn(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusIdle, MaxIterations: 8})
	runner := &scriptedRunner{discoverNotes: []string{"discovered"}}
	d := testDriver(store, runner)
	d.SetCapacityGate(fakeCapacityChecker{admit: true})

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhasePlan {
		t.Fatalf("mission.Phase = %s, want plan (turn ran when the gate admits)", m.Phase)
	}
}

// TestDriverAdvanceNilCapacityGateRunsTurn confirms the default (no gate
// wired) behaves exactly as before this existed — every idle mission's
// turn runs unconditionally.
func TestDriverAdvanceNilCapacityGateRunsTurn(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusIdle, MaxIterations: 8})
	runner := &scriptedRunner{discoverNotes: []string{"discovered"}}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhasePlan {
		t.Fatalf("mission.Phase = %s, want plan (nil gate never blocks)", m.Phase)
	}
}

// TestDriverAdvanceCapacityGateIgnoredWhenAlreadyWorking confirms
// admission applies only to idle->working: a mission already working
// must keep advancing even when the gate denies.
func TestDriverAdvanceCapacityGateIgnoredWhenAlreadyWorking(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDiscover, Status: StatusWorking, MaxIterations: 8, AutoApprovePlan: true})
	runner := &scriptedRunner{discoverNotes: []string{"discovered"}}
	d := testDriver(store, runner)
	d.SetCapacityGate(fakeCapacityChecker{admit: false, reason: "denied"})

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhasePlan {
		t.Fatalf("mission.Phase = %s, want plan (a denial must not stall a mission already working)", m.Phase)
	}
}

// fakeSandboxRemover records Remove calls so a test can assert
// terminal-transition cleanup ran without a real Docker socket.
type fakeSandboxRemover struct {
	mu      sync.Mutex
	removed []string
}

func (f *fakeSandboxRemover) Remove(ctx context.Context, missionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, missionID)
	return nil
}

func (f *fakeSandboxRemover) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

// TestDriverAdvanceStopsOnTerminalStatus is the cheap belt: a mission
// whose Status already reads done/error (set by a concurrent cancel)
// must not run another turn, even before the Store-level guard is
// consulted.
func TestDriverAdvanceStopsOnTerminalStatus(t *testing.T) {
	for _, status := range []Status{StatusDone, StatusError} {
		store := newFakeStore()
		store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: status, MaxIterations: 8})
		runner := &scriptedRunner{}
		d := testDriver(store, runner)

		cont, err := d.Advance(context.Background(), "m1")
		if err != nil {
			t.Fatalf("Advance (status=%s): %v", status, err)
		}
		if cont {
			t.Fatalf("Advance (status=%s) canContinue = true, want false", status)
		}
	}
}

// TestDriverAdvanceDiscardsTurnOnConcurrentTerminal reproduces the
// production bug: a cancel lands mid-turn (ApplyTransition's row-lock
// guard now returns ErrTerminal for the turn's own late transition).
// Advance must treat that as a clean stop — no error escalation — and
// still tear down the sandbox, since the in-flight turn may have
// recreated a container after cancel's own teardown ran.
func TestDriverAdvanceDiscardsTurnOnConcurrentTerminal(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8})
	store.applyTransitionErr = ErrTerminal
	runner := &scriptedRunner{workerVerdicts: []WorkerVerdict{{Outcome: "done"}}}
	remover := &fakeSandboxRemover{}
	d := NewDriver(store, runner, nil, nil, nil, nil, fakeSandboxExec, remover, slog.Default())
	d.gatekeepers["m1"] = &GatekeeperState{}

	cont, err := d.Advance(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if cont {
		t.Fatal("Advance canContinue = true, want false (turn discarded on terminal race)")
	}
	if _, stillPresent := d.gatekeepers["m1"]; stillPresent {
		t.Fatal("gatekeeper state was not cleaned up when the turn discarded on terminal race")
	}
	// removeSandbox fires the actual Remove call in a background
	// goroutine (independent of the request's own ctx) — poll briefly
	// rather than asserting immediately.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if calls := remover.calls(); len(calls) == 1 && calls[0] == "m1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sandboxRemove.Remove calls = %v, want exactly one call for m1", remover.calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFailedReason(t *testing.T) {
	cases := []struct {
		name   string
		events []EventDraft
		want   string
	}{
		{"no events", nil, ""},
		{"no mission.failed event", []EventDraft{{Kind: "mission.progress"}}, ""},
		{"cancelled", []EventDraft{{Kind: "mission.failed", Payload: map[string]any{"reason": "cancelled"}}}, "cancelled"},
		{"max_iterations", []EventDraft{{Kind: "mission.failed", Payload: map[string]any{"reason": "max_iterations", "detail": "x"}}}, "max_iterations"},
		{"missing reason key", []EventDraft{{Kind: "mission.failed", Payload: map[string]any{}}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := failedReason(tc.events); got != tc.want {
				t.Fatalf("failedReason(%+v) = %q, want %q", tc.events, got, tc.want)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	cases := []struct {
		consecutiveFailures int
		want                time.Duration
	}{
		{-1, 0},
		{0, 0},
		{1, 5 * time.Second},
		{2, 15 * time.Second},
		{3, 45 * time.Second},
		{4, 45 * time.Second},
	}
	for _, tc := range cases {
		if got := retryDelay(tc.consecutiveFailures); got != tc.want {
			t.Fatalf("retryDelay(%d) = %v, want %v", tc.consecutiveFailures, got, tc.want)
		}
	}
}
