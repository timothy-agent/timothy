package missions

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOutcomeDigest(t *testing.T) {
	tests := []struct {
		name          string
		mission       Mission
		events        []Event
		terminal      Phase
		failureReason string
		wantContains  []string
		wantExcludes  []string
	}{
		{
			name: "goal explore units review done",
			mission: Mission{
				Goal: "add a widget", Name: "Widget mission", Kind: "coding",
				ExploreNotes: "found the widget package",
				Spec:         Spec{Units: []PlanUnit{{Title: "write widget.go", Passes: true}}},
			},
			events: []Event{
				{Kind: "mission.turn", Payload: json.RawMessage(`{"phase":"execute","duration_ms":500}`)},
				{Kind: "mission.review_verdict", Payload: json.RawMessage(`{"decision":"approved","findings":"looks good"}`)},
			},
			terminal: PhaseDone,
			wantContains: []string{
				"add a widget", "Widget mission", "coding",
				"found the widget package",
				"write widget.go: passed",
				"review verdict: approved", "review findings: looks good",
				"terminal state: done",
			},
			wantExcludes: []string{"duration_ms", "\"phase\":\"execute\""},
		},
		{
			name: "review skipped",
			mission: Mission{
				Goal: "general task", Kind: "general",
				Spec: Spec{Units: []PlanUnit{{Title: "only unit", Passes: true}}},
			},
			events: []Event{
				{Kind: "mission.review_skipped", Payload: json.RawMessage(`{"unit":0,"reason":"artifacts and verify_cmd passed harness checks"}`)},
			},
			terminal:     PhaseDone,
			wantContains: []string{"review: skipped"},
			wantExcludes: []string{"review verdict:"},
		},
		{
			name: "failed with reason",
			mission: Mission{
				Goal: "risky task", Kind: "coding",
				Spec: Spec{Units: []PlanUnit{{Title: "unit one", Passes: false}}},
			},
			terminal:      PhaseFailed,
			failureReason: "max_iterations",
			wantContains:  []string{"terminal state: failed", "failure reason: max_iterations", "unit one: not verified"},
		},
		{
			name:         "no raw transcript noise",
			mission:      Mission{Goal: "quiet task", Kind: "general"},
			events:       []Event{{Kind: "mission.turn", Payload: json.RawMessage(`{"ok":true,"input":"phase_complete"}`)}},
			terminal:     PhaseDone,
			wantExcludes: []string{"mission.turn", "\"ok\":true", "phase_complete"},
		},
		{
			name: "light mission includes final output",
			mission: Mission{
				Goal: "summarize the doc", Kind: "general", Light: true,
				FinalOutput: "the doc says X, Y, and Z",
			},
			events:       []Event{{Kind: "mission.review_skipped", Payload: json.RawMessage(`{"reason":"light"}`)}},
			terminal:     PhaseDone,
			wantContains: []string{"final output:", "the doc says X, Y, and Z"},
		},
		{
			name:         "non-light mission never includes final output even if set",
			mission:      Mission{Goal: "coded task", Kind: "coding", FinalOutput: "should never appear"},
			terminal:     PhaseDone,
			wantExcludes: []string{"final output:", "should never appear"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			digest := OutcomeDigest(tt.mission, tt.events, tt.terminal, tt.failureReason)
			for _, want := range tt.wantContains {
				if !strings.Contains(digest, want) {
					t.Errorf("digest missing %q\ngot:\n%s", want, digest)
				}
			}
			for _, exclude := range tt.wantExcludes {
				if strings.Contains(digest, exclude) {
					t.Errorf("digest must not contain %q\ngot:\n%s", exclude, digest)
				}
			}
		})
	}
}

// TestOutcomeDigestTruncatesLightFinalOutput confirms a light mission's
// FinalOutput is capped at finalOutputDigestCap runes (D-069) — the
// digest feeds memory extraction and parent_context, not the whole
// deliverable verbatim.
func TestOutcomeDigestTruncatesLightFinalOutput(t *testing.T) {
	long := strings.Repeat("é", finalOutputDigestCap+500) // multi-byte rune, proves rune- not byte-counting
	m := Mission{Goal: "g", Kind: "general", Light: true, FinalOutput: long}
	digest := OutcomeDigest(m, nil, PhaseDone, "")
	i := strings.Index(digest, "final output:\n")
	if i < 0 {
		t.Fatal("digest missing final output section")
	}
	rendered := digest[i+len("final output:\n"):]
	rendered, _, ok := strings.Cut(rendered, "\n\nterminal state:")
	if !ok {
		t.Fatalf("could not isolate the final output section from the rest of the digest:\n%s", digest)
	}
	got := []rune(rendered)
	// truncateRunes appends one "…" rune after the cap.
	if len(got) != finalOutputDigestCap+1 {
		t.Fatalf("rendered final output = %d runes, want %d (cap + ellipsis)", len(got), finalOutputDigestCap+1)
	}
}

// recordingExtract is a MemoryExtract that records every call it
// receives, synchronized so tests can assert on it after the driver's
// dispatching goroutine has had a chance to run.
type recordingExtract struct {
	mu    sync.Mutex
	calls []string // sessionID per call
}

func (r *recordingExtract) fn() MemoryExtract {
	return func(ctx context.Context, sessionID string, seq int64, text, route string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, sessionID)
	}
}

func (r *recordingExtract) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// waitForCalls polls count() until it reaches want or the deadline
// passes — the extraction dispatch is `go d.memory(...)`, so tests
// need a brief allowance for that goroutine to run.
func waitForCalls(t *testing.T, r *recordingExtract, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.count() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("extraction calls = %d, want %d", r.count(), want)
}

func TestDriverExtractsMemoryOnDone(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8, SessionID: "sess-1"})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingExtract{}
	d.SetMemoryExtract(rec.fn())

	driveN(t, d, "m1", 4) // explore -> plan -> execute -> review -> done

	waitForCalls(t, rec, 1)

	events, _ := store.Events(context.Background(), "m1")
	if !alreadyExtracted(events) {
		t.Fatal("expected mission.memory_extracted event after done transition")
	}
}

func TestDriverExtractsMemoryOnFailed(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 1, SessionID: "sess-1"})
	runner := &scriptedRunner{
		workerVerdicts: []WorkerVerdict{{Outcome: "retry", Analysis: "nope"}},
	}
	d := testDriver(store, runner)
	rec := &recordingExtract{}
	d.SetMemoryExtract(rec.fn())

	// MaxIterations=1: the first retry immediately exhausts iterations
	// and the state machine fails the mission (stepWorkerRetry).
	driveN(t, d, "m1", 1)

	m, _ := store.Get(context.Background(), "m1")
	if m.Phase != PhaseFailed {
		t.Fatalf("mission phase = %s, want failed", m.Phase)
	}
	waitForCalls(t, rec, 1)
}

func TestDriverDoesNotExtractOnNonTerminalTransitions(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8, SessionID: "sess-1"})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingExtract{}
	d.SetMemoryExtract(rec.fn())

	// explore -> plan -> execute: 2 non-terminal Advance calls.
	driveN(t, d, "m1", 2)

	// Give any (wrongly) dispatched goroutine a chance to run before
	// asserting zero.
	time.Sleep(20 * time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("extraction calls after non-terminal transitions = %d, want 0", got)
	}
}

func TestDriverExtractsMemoryOnceOnRedrive(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDone, Status: StatusDone, SessionID: "sess-1"})
	// The mission is already terminal; simulate a re-drive (sweep/boot
	// recovery) by calling extractMissionMemory directly a second time
	// after the first has already recorded its idempotency event.
	d := testDriver(store, &scriptedRunner{})
	rec := &recordingExtract{}
	d.SetMemoryExtract(rec.fn())

	m, _ := store.Get(context.Background(), "m1")
	d.extractMissionMemory(context.Background(), m, PhaseDone, "")
	waitForCalls(t, rec, 1)

	d.extractMissionMemory(context.Background(), m, PhaseDone, "")
	time.Sleep(20 * time.Millisecond)
	if got := rec.count(); got != 1 {
		t.Fatalf("extraction calls after re-drive = %d, want 1 (idempotency guard must suppress the second attempt)", got)
	}
}

// TestDriverMemoryExtractionErrorDoesNotBlockTransition asserts the
// mission transition completes regardless of what the wired
// MemoryExtract does — its signature has no error return (see
// MemoryExtract's doc comment), so a caller like main.go's wrapper
// that fails to reach memoryd only ever logs; nothing propagates back
// to the driver that could block or unwind the transition already
// committed before extraction was even dispatched.
func TestDriverMemoryExtractionErrorDoesNotBlockTransition(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8, SessionID: "sess-1"})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner)
	rec := &recordingExtract{}
	d.SetMemoryExtract(func(ctx context.Context, sessionID string, seq int64, text, route string) {
		// Simulates main.go's wrapper swallowing a memoryd error: logged
		// there, never surfaced here.
		rec.fn()(ctx, sessionID, seq, text, route)
	})

	driveN(t, d, "m1", 4)
	m, err := store.Get(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Phase != PhaseDone {
		t.Fatalf("mission phase = %s, want done despite extraction failure", m.Phase)
	}
	waitForCalls(t, rec, 1)
}

func TestDriverSkipsExtractionWhenNilClient(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseExplore, Status: StatusWorking, MaxIterations: 8, SessionID: "sess-1"})
	runner := &scriptedRunner{
		plans:          []Spec{{Units: []PlanUnit{{Title: "only unit"}}}},
		workerVerdicts: []WorkerVerdict{{Outcome: "done", Evidence: "did it"}},
		reviewVerdicts: []ReviewVerdict{{Approved: true}},
	}
	d := testDriver(store, runner) // no SetMemoryExtract call: d.memory stays nil

	driveN(t, d, "m1", 4)

	events, _ := store.Events(context.Background(), "m1")
	if alreadyExtracted(events) {
		t.Fatal("nil memory client must never record mission.memory_extracted")
	}
}

func TestDriverSkipsExtractionWhenNoSession(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseDone, Status: StatusDone, SessionID: ""})
	d := testDriver(store, &scriptedRunner{})
	rec := &recordingExtract{}
	d.SetMemoryExtract(rec.fn())

	m, _ := store.Get(context.Background(), "m1")
	d.extractMissionMemory(context.Background(), m, PhaseDone, "")
	time.Sleep(20 * time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("extraction calls with no hidden session = %d, want 0", got)
	}
}

// recordingNameMission is a nameMission fake that records every goal
// it was called with, synchronized so tests can assert on it after the
// driver's dispatching goroutine has had a chance to run.
type recordingNameMission struct {
	mu    sync.Mutex
	calls []string // goal per call
	name  string   // name to return; "" simulates a generation failure
}

func (r *recordingNameMission) fn() func(context.Context, string) string {
	return func(ctx context.Context, goal string) string {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, goal)
		return r.name
	}
}

func (r *recordingNameMission) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// TestBackfillMissionNameFillsEmptyName covers backfillMissionName
// itself (D-073: now synchronous — runTerminalHooks is what decides
// whether a mission needs naming; backfillMissionName just does it).
func TestBackfillMissionNameFillsEmptyName(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Goal: "add a widget", Phase: PhaseDone, Status: StatusDone})
	d := testDriver(store, &scriptedRunner{})
	rec := &recordingNameMission{name: "Nice Name"}
	d.SetNameMission(rec.fn())

	d.backfillMissionName(context.Background(), "m1", "add a widget")

	if rec.count() != 1 {
		t.Fatalf("nameMission calls = %d, want 1", rec.count())
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Name != "Nice Name" {
		t.Fatalf("mission name = %q, want %q", m.Name, "Nice Name")
	}
}

// TestRunTerminalHooksSkipsNamingWhenAlreadyNamed covers the
// already-named guard, which now lives in runTerminalHooks rather than
// backfillMissionName itself.
func TestRunTerminalHooksSkipsNamingWhenAlreadyNamed(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Goal: "add a widget", Name: "Already Named", Phase: PhaseDone, Status: StatusDone})
	d := testDriver(store, &scriptedRunner{})
	rec := &recordingNameMission{name: "Nice Name"}
	d.SetNameMission(rec.fn())

	d.runTerminalHooks(context.Background(), "m1", PhaseDone, nil)
	if got := rec.count(); got != 0 {
		t.Fatalf("nameMission calls for an already-named mission = %d, want 0", got)
	}
}

func TestBackfillMissionNameSkipsWhenNilFn(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Goal: "add a widget", Phase: PhaseDone, Status: StatusDone})
	d := testDriver(store, &scriptedRunner{}) // no SetNameMission call: d.nameMission stays nil

	d.backfillMissionName(context.Background(), "m1", "add a widget")

	m, _ := store.Get(context.Background(), "m1")
	if m.Name != "" {
		t.Fatalf("mission name = %q, want empty (nil nameMission must never fire)", m.Name)
	}
}

func TestBackfillMissionNameSkipsSaveWhenGenerationEmpty(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Goal: "add a widget", Phase: PhaseDone, Status: StatusDone})
	d := testDriver(store, &scriptedRunner{})
	rec := &recordingNameMission{name: ""} // simulates a generation failure
	d.SetNameMission(rec.fn())

	d.backfillMissionName(context.Background(), "m1", "add a widget")

	if rec.count() != 1 {
		t.Fatalf("nameMission calls = %d, want 1", rec.count())
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Name != "" {
		t.Fatalf("mission name = %q, want empty (empty generation must not write)", m.Name)
	}
}
