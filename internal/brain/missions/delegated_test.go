package missions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/missions/executor"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// loadDelegatedFixture returns every non-empty line of a recorded
// claude-cli fixture — same loader shape as executor/claude_test.go's
// loadFixture (unexported there, so duplicated rather than exported
// across a package boundary for one test helper).
func loadDelegatedFixture(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("executor", "testdata", "claude-2.1.223", name)) //nolint:gosec // G304: fixed testdata path.
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()

	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		lines = append(lines, cp)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return lines
}

// fakeRun is one simulated container-side CLI run: a fixture replayed
// into run.ndjson one poll at a time (each poll call's tail sees a bit
// more of the fixture than the last, deliberately split mid-line to
// exercise the carry-buffer), an exit code written once the fixture is
// exhausted, and a pid that goes away once exited or killed.
type fakeRun struct {
	mu sync.Mutex

	remaining [][]byte
	chunkLen  int // bytes appended per poll; 0 = whole fixture at once

	written  []byte
	exited   bool
	exitCode int
	killed   bool
	idle     bool // never advances past what's already written, however often polled
	env      map[string]string
}

// advance appends the next slice of the fixture to written, splitting
// mid-line when chunkLen is set — the deliberate mid-line-split
// scenario the plan calls out as mandatory.
func (f *fakeRun) advance() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idle || f.exited || len(f.remaining) == 0 {
		if !f.idle && !f.exited && len(f.remaining) == 0 {
			f.exited = true
		}
		return
	}
	next := f.remaining[0]
	if f.chunkLen <= 0 || f.chunkLen >= len(next)+1 {
		f.written = append(f.written, next...)
		f.written = append(f.written, '\n')
		f.remaining = f.remaining[1:]
	} else {
		f.written = append(f.written, next[:f.chunkLen]...)
		f.remaining[0] = next[f.chunkLen:]
	}
	if len(f.remaining) == 0 {
		f.exited = true
	}
}

// fakeSandbox implements sandboxExecEnv: an in-memory simulation of the
// container-side shell commands delegatedRunner issues (launch, poll,
// kill, stderr tail, resume probe) against fakeRuns keyed by run dir.
// seedLines/seedChunk configure every run this sandbox launches — one
// scenario per test, so one seed suffices.
type fakeSandbox struct {
	mu   sync.Mutex
	runs map[string]*fakeRun

	seedLines    [][]byte
	seedChunk    int
	seedExitCode int
	seedIdle     bool

	launches   int
	launchErr  error
	lastLaunch string
	pollErrN   int // fail this many poll calls with an infra error before succeeding
	pollErrs   int

	stderrText string
}

func newFakeSandbox() *fakeSandbox {
	return &fakeSandbox{runs: map[string]*fakeRun{}}
}

// lastLaunchCmd is the full shell command of the most recent launch.
func (s *fakeSandbox) lastLaunchCmd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastLaunch
}

func (s *fakeSandbox) Exec(ctx context.Context, missionID, environment, workdir, command string, env map[string]string, timeout time.Duration, out io.Writer) (int, error) {
	switch {
	case strings.Contains(command, "setsid sh -c"):
		return s.launch(command, env)
	case strings.Contains(command, "tail -c +"):
		return s.poll(command, out)
	case strings.Contains(command, "kill -TERM"):
		dir := singleQuoted(command, 0) // `.../pid` path's directory portion
		s.mu.Lock()
		if r := s.runs[dirOf(dir)]; r != nil {
			r.mu.Lock()
			r.killed = true
			r.mu.Unlock()
		}
		s.mu.Unlock()
		return 0, nil
	case strings.Contains(command, "tail -c 2048 stderr.log"):
		_, _ = out.Write([]byte(s.stderrText))
		return 0, nil
	case strings.Contains(command, "[ -f exit_code ]"):
		return s.probe(command)
	default:
		return 0, fmt.Errorf("fakeSandbox: unrecognized command: %s", command)
	}
}

// singleQuoted returns the nth (0-based) single-quoted substring in
// command — the test's one shared primitive for pulling shQuote'd path
// arguments back out of the shell commands delegated.go builds.
func singleQuoted(command string, n int) string {
	rest := command
	for i := 0; i <= n; i++ {
		start := strings.Index(rest, "'")
		if start == -1 {
			return ""
		}
		rest = rest[start+1:]
		end := strings.Index(rest, "'")
		if end == -1 {
			return ""
		}
		if i == n {
			return rest[:end]
		}
		rest = rest[end+1:]
	}
	return ""
}

// dirOf strips a trailing /pid or /exit_code file suffix, leaving the
// run directory — killRun's command references "<rdir>/pid" as one
// quoted argument, not a bare quoted directory.
func dirOf(pathWithFile string) string {
	return filepath.Dir(pathWithFile)
}

// launchRunDir pulls the run directory out of a launch command: `cd
// '<workdir>' && mkdir -p '<rdir>' && ...` — the SECOND single-quoted
// argument, since the first is the mission workdir.
func launchRunDir(command string) string {
	return singleQuoted(command, 1)
}

// pollRunDir pulls the run directory out of a poll/probe/stderr-tail
// command: `cd '<rdir>' && ...` — the FIRST single-quoted argument.
func pollRunDir(command string) string {
	return singleQuoted(command, 0)
}

func (s *fakeSandbox) launch(command string, env map[string]string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.launches++
	s.lastLaunch = command
	if s.launchErr != nil {
		return 1, s.launchErr
	}
	dir := launchRunDir(command)
	lines := make([][]byte, len(s.seedLines))
	copy(lines, s.seedLines)
	s.runs[dir] = &fakeRun{remaining: lines, chunkLen: s.seedChunk, exitCode: s.seedExitCode, idle: s.seedIdle, env: env}
	return 0, nil
}

func (s *fakeSandbox) poll(command string, out io.Writer) (int, error) {
	s.mu.Lock()
	if s.pollErrs < s.pollErrN {
		s.pollErrs++
		s.mu.Unlock()
		return 0, fmt.Errorf("fakeSandbox: simulated infra error")
	}
	s.mu.Unlock()

	dir := pollRunDir(command)
	s.mu.Lock()
	r := s.runs[dir]
	s.mu.Unlock()
	if r == nil {
		return 1, fmt.Errorf("fakeSandbox: no run for dir %s", dir)
	}
	r.advance()

	offset := parseOffset(command)
	boundary := parseBoundary(command)

	r.mu.Lock()
	defer r.mu.Unlock()
	var chunk []byte
	if offset-1 < len(r.written) {
		chunk = r.written[offset-1:]
	}
	if len(chunk) > tailChunkCap {
		chunk = chunk[:tailChunkCap]
	}
	buf := &bytes.Buffer{}
	buf.Write(chunk)
	fmt.Fprintf(buf, "%s\n", boundary)
	if r.exited {
		fmt.Fprintf(buf, "EXITCODE:%d\n", r.exitCode)
	}
	if !r.exited && !r.killed {
		buf.WriteString("ALIVE\n")
	}
	_, _ = out.Write(buf.Bytes())
	return 0, nil
}

func (s *fakeSandbox) probe(command string) (int, error) {
	dir := pollRunDir(command)
	s.mu.Lock()
	r := s.runs[dir]
	s.mu.Unlock()
	if r == nil {
		return 1, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exited || !r.killed {
		return 0, nil
	}
	return 1, nil
}

func parseOffset(command string) int {
	const marker = "tail -c +"
	i := strings.Index(command, marker)
	if i == -1 {
		return 1
	}
	rest := command[i+len(marker):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	n, _ := strconv.Atoi(rest[:j])
	return n
}

// parseBoundary recovers the boundary marker from a poll command by its
// stable prefix rather than the printf syntax around it — the marker
// rides as a quoted printf argument, not as the format string.
func parseBoundary(command string) string {
	i := strings.Index(command, pollBoundaryPrefix)
	if i == -1 {
		return ""
	}
	rest := command[i:]
	j := strings.Index(rest[len(pollBoundaryPrefix):], "---")
	if j == -1 {
		return ""
	}
	return rest[:len(pollBoundaryPrefix)+j+3]
}

// fakeEventSink records every AppendEvent call in order.
type fakeEventSink struct {
	mu     sync.Mutex
	events []Event
}

func (f *fakeEventSink) AppendEvent(ctx context.Context, missionID, kind string, payload map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := json.Marshal(payload)
	f.events = append(f.events, Event{MissionID: missionID, Kind: kind, Payload: raw})
	return nil
}

func (f *fakeEventSink) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func (f *fakeEventSink) last(kind string) (Event, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.events) - 1; i >= 0; i-- {
		if f.events[i].Kind == kind {
			return f.events[i], true
		}
	}
	return Event{}, false
}

// fakeLedger records every Record call for assertion.
type fakeLedger struct {
	mu      sync.Mutex
	entries []ledger.Entry
}

func (f *fakeLedger) Record(ctx context.Context, e ledger.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
}

func scriptedResolver(route *gwclient.ResolvedRoute, err error) routeResolver {
	return func(ctx context.Context, name, harness string) (*gwclient.ResolvedRoute, error) {
		return route, err
	}
}

func scriptedCred(key string, err error) credResolver {
	return func(ctx context.Context, ref string) (string, error) {
		return key, err
	}
}

// harnessEntry builds a claude-cli executor-axis chain entry (D-051
// rework: harness is no longer a field on the entry itself — RunWorker
// now dispatches on the mission's own Harness column and passes it
// alongside the entry to the cooldown/event helpers).
func harnessEntry(credRef string) gwclient.ResolvedRouteEntry {
	return gwclient.ResolvedRouteEntry{
		ProviderID: "prov-1", ProviderName: "anthropic", Driver: "anthropic",
		Model: "claude-haiku-4-5-20251001", CredentialRef: credRef, Usable: true,
	}
}

// fakeNative is a minimal Runner recording whether RunWorker was
// called — the dispatch/fallback tests assert against this instead of
// a real nativeRunner, which needs a full loop.Agent to construct.
type fakeNative struct {
	mu      sync.Mutex
	called  int
	verdict WorkerVerdict
	text    string
	err     error
}

func (f *fakeNative) RunWorker(ctx context.Context, m Mission, packet WorkPacket) (WorkerVerdict, string, error) {
	f.mu.Lock()
	f.called++
	f.mu.Unlock()
	return f.verdict, f.text, f.err
}
func (f *fakeNative) RunReview(ctx context.Context, m Mission, packet ReviewPacket) (ReviewVerdict, error) {
	return ReviewVerdict{}, nil
}
func (f *fakeNative) PlanSession(ctx context.Context, m Mission, discoverNotes string) (Plan, error) {
	return Plan{}, nil
}
func (f *fakeNative) DiscoverSession(ctx context.Context, m Mission) (string, string, string, error) {
	return "", "", "", nil
}

func (f *fakeNative) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.called
}

// newTestDelegatedRunner builds a delegatedRunner with short test
// timings so scenarios never wait real production minutes.
func newTestDelegatedRunner(native Runner, resolve routeResolver, cred credResolver, sandbox *fakeSandbox, events eventSink, lastRun lastRunStateFunc, led usageRecorder) *delegatedRunner {
	r := NewDelegatedRunner(native, resolve, cred, sandbox.Exec, events, lastRun, led, nil, slog.Default()).(*delegatedRunner)
	r.pollInterval = 2 * time.Millisecond
	r.idleTimeout = 30 * time.Millisecond
	return r
}

func testMission(id, workRoot string) Mission {
	return Mission{ID: id, Kind: "coding", Workspace: workRoot, Route: "default", Harness: "claude-cli"}
}

// testNativeMission is testMission with Harness left empty — RunWorker
// dispatches straight to native for these without ever resolving a
// route (D-051 rework).
func testNativeMission(id, workRoot string) Mission {
	return Mission{ID: id, Kind: "coding", Workspace: workRoot, Route: "default"}
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// --- scenario 1: happy path --------------------------------------------

func TestDelegatedRunWorker_HappyPath(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")
	sandbox.seedChunk = 40
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	led := &fakeLedger{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, led)
	m := testMission("m1", t.TempDir())

	verdict, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Outcome != "done" {
		t.Fatalf("Outcome = %q, want done", verdict.Outcome)
	}
	if events.count("executor.spawned") != 1 {
		t.Fatalf("executor.spawned count = %d, want 1", events.count("executor.spawned"))
	}
	if events.count("executor.result") != 1 {
		t.Fatalf("executor.result count = %d, want 1", events.count("executor.result"))
	}
	result, _ := events.last("executor.result")
	var resultPayload map[string]any
	_ = json.Unmarshal(result.Payload, &resultPayload)
	if resultPayload["status"] != "DONE" {
		t.Fatalf("executor.result status = %v, want parsed DONE, never raw result text", resultPayload["status"])
	}
	spawned, _ := events.last("executor.spawned")
	var spawnedPayload map[string]any
	_ = json.Unmarshal(spawned.Payload, &spawnedPayload)
	if spawnedPayload["auth_mode"] != "subscription" {
		t.Fatalf("spawned auth_mode = %v, want subscription", spawnedPayload["auth_mode"])
	}
	if len(led.entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(led.entries))
	}
	if led.entries[0].Cost == nil {
		t.Fatal("subscription entry Cost = nil, want the CLI-reported unbilled cost")
	}
	if !led.entries[0].Unbilled {
		t.Fatal("subscription entry Unbilled = false, want true (subscription-billed, not real marginal spend)")
	}
	if led.entries[0].Purpose != "executor" || led.entries[0].Agent != "mission-worker" {
		t.Fatalf("ledger entry purpose/agent = %q/%q, want executor/mission-worker", led.entries[0].Purpose, led.entries[0].Agent)
	}
}

// TestDelegatedRunWorker_HappyPath_VerdictCarriesServedProviderAndModel
// covers issue #507: a delegated turn's verdict reports the executor's
// own chain entry as who served it (the same values executor.spawned
// already carries).
func TestDelegatedRunWorker_HappyPath_VerdictCarriesServedProviderAndModel(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")
	sandbox.seedChunk = 40
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	led := &fakeLedger{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, led)
	m := testMission("m1", t.TempDir())

	verdict, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Provider != entry.ProviderName {
		t.Fatalf("verdict.Provider = %q, want %q", verdict.Provider, entry.ProviderName)
	}
	if verdict.Model != entry.Model {
		t.Fatalf("verdict.Model = %q, want %q", verdict.Model, entry.Model)
	}
}

// --- scenario 2: missing result (transport death) -----------------------

func TestDelegatedRunWorker_MissingResult_ForcedRetry(t *testing.T) {
	fixture := loadDelegatedFixture(t, "schema.ndjson")
	sandbox := newFakeSandbox()
	sandbox.seedLines = fixture[:len(fixture)-1] // truncate before the result line
	sandbox.seedExitCode = 1
	events := &fakeEventSink{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())

	verdict, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Outcome != "retry" || !verdict.Forced {
		t.Fatalf("verdict = %+v, want forced retry", verdict)
	}
	if events.count("executor.died") != 1 {
		t.Fatalf("executor.died count = %d, want 1", events.count("executor.died"))
	}
}

// --- scenario 2b: wall-clock run budget hit (issue #498) -----------------

func TestDelegatedRunWorker_RunBudgetExit_RecordedAsRunBudget(t *testing.T) {
	fixture := loadDelegatedFixture(t, "schema.ndjson")
	sandbox := newFakeSandbox()
	sandbox.seedLines = fixture[:len(fixture)-1]
	sandbox.seedExitCode = runBudgetExitCode
	events := &fakeEventSink{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	budgetReads := 0
	r.runBudgetFn = func(context.Context) time.Duration { budgetReads++; return 3 * time.Hour }
	m := testMission("m1", t.TempDir())

	verdict, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Outcome != "retry" || !verdict.Forced {
		t.Fatalf("verdict = %+v, want forced retry", verdict)
	}
	if budgetReads != 1 {
		t.Fatalf("run budget read %d times, want once per launch", budgetReads)
	}
	if !strings.Contains(sandbox.lastLaunchCmd(), "timeout -k 30 10800 ") {
		t.Fatalf("launch cmd does not carry the settings-backed budget: %s", sandbox.lastLaunchCmd())
	}
	died, ok := events.last("executor.died")
	if !ok {
		t.Fatal("no executor.died event")
	}
	var payload map[string]any
	_ = json.Unmarshal(died.Payload, &payload)
	if payload["reason"] != "run_budget" {
		t.Fatalf("executor.died reason = %v, want run_budget", payload["reason"])
	}
}

// --- scenario 3: idle hang -----------------------------------------------

func TestDelegatedRunWorker_IdleHang_KilledAndRetried(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")[:1] // one line, then silence
	sandbox.seedIdle = true
	events := &fakeEventSink{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())

	verdict, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Outcome != "retry" || !verdict.Forced {
		t.Fatalf("verdict = %+v, want forced retry", verdict)
	}
	if events.count("executor.idle_killed") != 1 {
		t.Fatalf("executor.idle_killed count = %d, want 1", events.count("executor.idle_killed"))
	}
}

// --- scenario 4: auth failure --------------------------------------------

func TestDelegatedRunWorker_AuthFailure_ReturnsErrExecutorAuth(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = nil // process exits immediately, no result
	sandbox.seedExitCode = 1
	sandbox.stderrText = "Error: please run /login to authenticate"
	events := &fakeEventSink{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}
	led := &fakeLedger{}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, led)
	m := testMission("m1", t.TempDir())

	_, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if !errors.Is(err, ErrExecutorAuth) {
		t.Fatalf("err = %v, want ErrExecutorAuth", err)
	}
	if events.count("executor.auth_failed") != 1 {
		t.Fatalf("executor.auth_failed count = %d, want 1", events.count("executor.auth_failed"))
	}
	// A ledger row on auth failure lets HealthRow.LastError reflect a
	// bad/expired token instead of staying silent about it.
	if len(led.entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(led.entries))
	}
	if led.entries[0].Status != "error" {
		t.Fatalf("ledger entry status = %q, want error", led.entries[0].Status)
	}
	if led.entries[0].ErrorCode != errorCodeAuthFailed {
		t.Fatalf("ledger entry error_code = %q, want %q", led.entries[0].ErrorCode, errorCodeAuthFailed)
	}
}

// TestDriverErrExecutorAuthPausesImmediately mirrors
// TestDriverModelFloorPausesImmediately: ErrExecutorAuth from the
// runner must pause the mission as infra on the FIRST turn.
func TestDriverErrExecutorAuthPausesImmediately(t *testing.T) {
	store := newFakeStore()
	store.put("m1", Mission{ID: "m1", Kind: "general", Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8})
	runner := &scriptedRunner{workerErr: fmt.Errorf("%w: stderr said please run /login", ErrExecutorAuth)}
	d := testDriver(store, runner)

	if _, err := d.Advance(context.Background(), "m1"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	m, _ := store.Get(context.Background(), "m1")
	if m.Status != StatusPaused || m.PauseReason != PauseInfra {
		t.Fatalf("mission after auth-failure turn = %s/%s, want paused/infra immediately", m.Status, m.PauseReason)
	}
}

// --- scenario 5: re-attach ------------------------------------------------

func TestDelegatedRunWorker_ReattachResumesWithoutRespawning(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")
	sandbox.seedChunk = 60
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())

	// First "process lifetime": spawn the run and poll it exactly once,
	// then abandon it mid-run WITHOUT cancelling its context — a genuine
	// ctx cancellation (Drive's own bound, an explicit mission cancel) is
	// a deliberate stop that kills the process and records executor.died,
	// which correctly blocks resume; a brain restart is neither of
	// those; it's the whole process vanishing before recordDied ever
	// gets a chance to run. Simulate that directly: call the same
	// unexported helpers RunWorker itself would call, stopping after one
	// poll iteration instead of running the full loop to completion.
	adapter, _ := executor.Lookup(m.Harness)
	handled, _, _, _ := r.attemptResume(context.Background(), m, m.WorkRoot(), entry, adapter)
	if handled {
		t.Fatalf("attemptResume handled on a mission with no prior run; want false")
	}
	authMode, _, err := r.resolveCredential(context.Background(), entry.CredentialRef, adapter.Capabilities())
	if err != nil {
		t.Fatalf("resolveCredential: %v", err)
	}
	runID, err := newRunID()
	if err != nil {
		t.Fatalf("newRunID: %v", err)
	}
	rdir := runDir(m.Workspace, runID)
	spec := executor.InvocationSpec{
		MissionID: m.ID, Workdir: m.WorkRoot(), PromptPath: filepath.Join(rdir, "prompt.md"),
		Model: entry.Model, AuthMode: authMode, ResultSchema: resultSchemaJSON, RunBudget: r.runBudget,
	}
	inv, err := adapter.BuildInvocation(spec)
	if err != nil {
		t.Fatalf("BuildInvocation: %v", err)
	}
	if err := os.MkdirAll(rdir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rdir, "prompt.md"), []byte("prompt"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	r.recordSpawned(context.Background(), m.ID, m.Harness, entry, runID, rdir, authMode)
	if err := r.launch(context.Background(), m.ID, m.Environment, m.WorkRoot(), rdir, inv, time.Minute); err != nil {
		t.Fatalf("launch: %v", err)
	}
	// One poll tick's worth of progress, then the "process" (this test's
	// simulated first lifetime) ends here — no result, no died event,
	// exactly what a mid-run crash leaves behind.
	sandbox.mu.Lock()
	sandbox.runs[rdir].advance()
	progressed := len(sandbox.runs[rdir].written)
	sandbox.mu.Unlock()
	if err := events.AppendEvent(context.Background(), m.ID, "executor.progress", map[string]any{
		"run_id": runID, "byte_offset": progressed,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if sandbox.launches != 1 {
		t.Fatalf("launches after first lifetime = %d, want 1", sandbox.launches)
	}

	// Second runner instance (simulating a fresh process), same events
	// store backing lastRun — resumes instead of respawning.
	lastRun := func(ctx context.Context, missionID string) (*runState, error) {
		spawned, ok := events.last("executor.spawned")
		if !ok {
			return nil, nil
		}
		var payload struct {
			Harness string `json:"harness"`
			RunID   string `json:"run_id"`
			RunDir  string `json:"run_dir"`
		}
		_ = json.Unmarshal(spawned.Payload, &payload)
		finished := events.count("executor.result") > 0 || events.count("executor.died") > 0
		return &runState{Harness: payload.Harness, RunID: payload.RunID, RunDir: payload.RunDir, Finished: finished}, nil
	}
	r2 := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, lastRun, &fakeLedger{})

	verdict, _, err := r2.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("second RunWorker: %v", err)
	}
	if verdict.Outcome != "done" {
		t.Fatalf("resumed verdict.Outcome = %q, want done", verdict.Outcome)
	}
	if sandbox.launches != 1 {
		t.Fatalf("launches after resume = %d, want still 1 (no respawn)", sandbox.launches)
	}
	if events.count("executor.spawned") != 1 {
		t.Fatalf("executor.spawned count = %d, want 1 (no second spawn)", events.count("executor.spawned"))
	}
}

// --- scenario 6: cooldown failover ---------------------------------------

// TestDelegatedRunWorker_CooldownSkipsToNativeFallback covers D-051's
// rework: harness is no longer mixed per-entry into one chain, so once
// every executor-axis entry is cooled down there is no "next native
// entry" to walk to within the same resolve result — the floor is
// native.RunWorker itself, same as an unusable/empty resolve.
func TestDelegatedRunWorker_CooldownSkipsToNativeFallback(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = nil
	sandbox.seedExitCode = 1
	sandbox.stderrText = "some unrelated crash"
	events := &fakeEventSink{}
	failing := harnessEntry("subscription")
	native := &fakeNative{verdict: WorkerVerdict{Outcome: "done"}}
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{failing}}

	r := newTestDelegatedRunner(native, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())

	// First call: the only entry transport-dies (forced retry, not an
	// error) and gets cooled down.
	verdict1, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("first RunWorker: %v", err)
	}
	if verdict1.Outcome != "retry" {
		t.Fatalf("first verdict = %+v, want retry", verdict1)
	}

	// Second call: the entry is now cooled down, so the walk finds
	// nothing usable and falls back to native.RunWorker.
	if _, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("second RunWorker: %v", err)
	}
	if native.callCount() != 1 {
		t.Fatalf("native call count = %d, want 1 (cooldown should fall back to native)", native.callCount())
	}
	if sandbox.launches != 1 {
		t.Fatalf("sandbox launches = %d, want 1 (second call must not retry the cooled-down entry)", sandbox.launches)
	}
	if events.count("executor.skipped") != 1 {
		t.Fatalf("executor.skipped count = %d, want 1", events.count("executor.skipped"))
	}
	skipped, _ := events.last("executor.skipped")
	var skippedPayload map[string]any
	_ = json.Unmarshal(skipped.Payload, &skippedPayload)
	if skippedPayload["reason"] != "cooldown" {
		t.Fatalf("executor.skipped reason = %v, want cooldown", skippedPayload["reason"])
	}
	if skippedPayload["harness"] != m.Harness {
		t.Fatalf("executor.skipped harness = %v, want %q", skippedPayload["harness"], m.Harness)
	}
	if skippedPayload["provider"] != failing.ProviderName || skippedPayload["model"] != failing.Model {
		t.Fatalf("executor.skipped provider/model = %v/%v, want %v/%v", skippedPayload["provider"], skippedPayload["model"], failing.ProviderName, failing.Model)
	}
	if _, ok := skippedPayload["until"].(string); !ok {
		t.Fatalf("executor.skipped until = %v, want an RFC3339 string", skippedPayload["until"])
	}
}

// TestDelegatedRunWorker_Dispatch_NoUsableEntryFallsBackToNative covers
// the route-had-entries-but-none-Usable case, distinct from cooldown:
// no entry was ever usable in the first place, so there is nothing to
// cool down.
func TestDelegatedRunWorker_Dispatch_NoUsableEntryFallsBackToNative(t *testing.T) {
	native := &fakeNative{verdict: WorkerVerdict{Outcome: "done"}}
	sandbox := newFakeSandbox()
	events := &fakeEventSink{}
	unusable := gwclient.ResolvedRouteEntry{
		ProviderID: "prov-1", ProviderName: "anthropic", Driver: "anthropic",
		Model: "claude-haiku-4-5-20251001", Usable: false, SkipReason: "no credential configured",
	}
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{unusable}}

	r := newTestDelegatedRunner(native, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())

	if _, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if native.callCount() != 1 {
		t.Fatalf("native call count = %d, want 1", native.callCount())
	}
	if events.count("executor.skipped") != 1 {
		t.Fatalf("executor.skipped count = %d, want 1", events.count("executor.skipped"))
	}
	skipped, _ := events.last("executor.skipped")
	var skippedPayload map[string]any
	_ = json.Unmarshal(skipped.Payload, &skippedPayload)
	if skippedPayload["reason"] != "no_usable_entry" {
		t.Fatalf("executor.skipped reason = %v, want no_usable_entry", skippedPayload["reason"])
	}
	reasons, ok := skippedPayload["skip_reasons"].([]any)
	if !ok || len(reasons) != 1 || reasons[0] != "no credential configured" {
		t.Fatalf("executor.skipped skip_reasons = %v, want [%q]", skippedPayload["skip_reasons"], "no credential configured")
	}
}

// --- scenario 6b: route_model pin (D-078) ---------------------------------

// pinnedHarnessEntry is harnessEntry with a distinct provider/model so
// pin tests can tell which entry actually dispatched.
func pinnedHarnessEntry(providerName, model, credRef string) gwclient.ResolvedRouteEntry {
	return gwclient.ResolvedRouteEntry{
		ProviderID: "prov-" + providerName, ProviderName: providerName, Driver: "anthropic",
		Model: model, CredentialRef: credRef, Usable: true,
	}
}

// TestDelegatedRunWorker_RouteModelPin_PrefersPinnedEntryOverFirstUsable
// confirms a route_model pin dispatches its named entry even though an
// earlier, also-usable entry sits ahead of it in the chain.
func TestDelegatedRunWorker_RouteModelPin_PrefersPinnedEntryOverFirstUsable(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	first := pinnedHarnessEntry("anthropic", "claude-sonnet-5", "subscription")
	pinned := pinnedHarnessEntry("glm", "glm-5.3", "subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{first, pinned}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())
	m.RouteModel = "glm/glm-5.3"

	if _, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	spawned, ok := events.last("executor.spawned")
	if !ok {
		t.Fatal("no executor.spawned event")
	}
	var payload map[string]any
	_ = json.Unmarshal(spawned.Payload, &payload)
	if payload["provider"] != "glm" || payload["model"] != "glm-5.3" {
		t.Fatalf("spawned provider/model = %v/%v, want the pinned glm/glm-5.3, not the first-in-chain entry", payload["provider"], payload["model"])
	}
	if events.count("executor.skipped") != 0 {
		t.Fatalf("executor.skipped count = %d, want 0 (the pin applied cleanly)", events.count("executor.skipped"))
	}
}

// TestDelegatedRunWorker_RouteModelPin_UnusableFallsBackToWalk covers a
// pin naming an entry that IS in the chain but not usable: the walk
// must still fall back to the first usable entry (never fail outright),
// and record why the pin didn't apply.
func TestDelegatedRunWorker_RouteModelPin_UnusableFallsBackToWalk(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	unusablePin := gwclient.ResolvedRouteEntry{
		ProviderID: "prov-glm", ProviderName: "glm", Driver: "anthropic",
		Model: "glm-5.3", Usable: false, SkipReason: "disabled",
	}
	fallback := pinnedHarnessEntry("anthropic", "claude-sonnet-5", "subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{unusablePin, fallback}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())
	m.RouteModel = "glm/glm-5.3"

	if _, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	spawned, ok := events.last("executor.spawned")
	if !ok {
		t.Fatal("no executor.spawned event")
	}
	var spawnedPayload map[string]any
	_ = json.Unmarshal(spawned.Payload, &spawnedPayload)
	if spawnedPayload["provider"] != "anthropic" {
		t.Fatalf("spawned provider = %v, want fallback to the first usable entry", spawnedPayload["provider"])
	}
	if events.count("executor.skipped") != 1 {
		t.Fatalf("executor.skipped count = %d, want 1 (the pin miss must be recorded)", events.count("executor.skipped"))
	}
	skipped, _ := events.last("executor.skipped")
	var skippedPayload map[string]any
	_ = json.Unmarshal(skipped.Payload, &skippedPayload)
	if skippedPayload["reason"] != "pin_unusable" {
		t.Fatalf("executor.skipped reason = %v, want pin_unusable", skippedPayload["reason"])
	}
	if skippedPayload["pin"] != "glm/glm-5.3" {
		t.Fatalf("executor.skipped pin = %v, want glm/glm-5.3", skippedPayload["pin"])
	}
}

// TestDelegatedRunWorker_RouteModelPin_AbsentFallsBackToWalk covers a
// pin naming an entry that isn't in the chain at all — never a failure,
// falls straight to the first-usable walk with its own reason recorded.
func TestDelegatedRunWorker_RouteModelPin_AbsentFallsBackToWalk(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	entry := harnessEntry("subscription")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())
	m.RouteModel = "no-such-provider/no-such-model"

	if _, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if events.count("executor.skipped") != 1 {
		t.Fatalf("executor.skipped count = %d, want 1", events.count("executor.skipped"))
	}
	skipped, _ := events.last("executor.skipped")
	var skippedPayload map[string]any
	_ = json.Unmarshal(skipped.Payload, &skippedPayload)
	if skippedPayload["reason"] != "pin_absent" {
		t.Fatalf("executor.skipped reason = %v, want pin_absent", skippedPayload["reason"])
	}
}

// --- scenario 7: api-key mode ---------------------------------------------

func TestDelegatedRunWorker_APIKeyMode_EnvAndCostRecorded(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	led := &fakeLedger{}
	entry := harnessEntry("cred-ref-1")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("sk-test-key", nil), sandbox, events, nil, led)
	m := testMission("m1", t.TempDir())

	verdict, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Outcome != "done" {
		t.Fatalf("Outcome = %q, want done", verdict.Outcome)
	}

	sandbox.mu.Lock()
	var sawKey string
	for _, run := range sandbox.runs {
		sawKey = run.env["ANTHROPIC_API_KEY"]
	}
	sandbox.mu.Unlock()
	if sawKey != "sk-test-key" {
		t.Fatalf("launch env ANTHROPIC_API_KEY = %q, want sk-test-key", sawKey)
	}

	if len(led.entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(led.entries))
	}
	// The schema fixture's result line does not carry total_cost_usd (it
	// was recorded from a StructuredOutput turn, cost 0) — assert Cost is
	// set (non-nil) for the api_key path, whatever its value.
	if led.entries[0].Cost == nil {
		t.Fatalf("api_key entry Cost = nil, want a recorded (possibly zero) figure")
	}
	if led.entries[0].Unbilled {
		t.Fatal("api_key entry Unbilled = true, want false (real marginal spend)")
	}
}

// loadPiDelegatedFixture returns every non-empty line of a recorded pi
// fixture — same loader shape as loadDelegatedFixture, pointed at the
// pi-0.84.1 testdata directory instead.
func loadPiDelegatedFixture(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("executor", "testdata", "pi-0.84.1", name)) //nolint:gosec // G304: fixed testdata path.
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()

	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		lines = append(lines, cp)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return lines
}

// piHarnessEntry builds a pi executor-axis chain entry with an
// anthropic driver — the exact shape cliCostTrusted must NOT treat as
// cost-trusted, since pi's ReportsCost capability is false regardless
// of which driver it ran against (D-013).
func piHarnessEntry(credRef string) gwclient.ResolvedRouteEntry {
	return gwclient.ResolvedRouteEntry{
		ProviderID: "prov-1", ProviderName: "anthropic", Driver: "anthropic",
		Model: "claude-sonnet-4", CredentialRef: credRef, Usable: true, Wire: "anthropic",
	}
}

func piTestMission(id, workRoot string) Mission {
	return Mission{ID: id, Kind: "coding", Workspace: workRoot, Route: "default", Harness: "pi"}
}

// TestDelegatedRunWorker_PiAdapter_FilesWrittenUnderRunDir: pi's
// BuildInvocation always emits an extra Invocation.Files entry
// (pi-agent/models.json) - runDelegated must write it to disk under
// the run directory alongside prompt.md before spawn.
func TestDelegatedRunWorker_PiAdapter_FilesWrittenUnderRunDir(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadPiDelegatedFixture(t, "happy.ndjson")
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	led := &fakeLedger{}
	entry := piHarnessEntry("cred-ref-pi")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("sk-test-key", nil), sandbox, events, nil, led)
	m := piTestMission("m1", t.TempDir())

	verdict, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Outcome != "done" {
		t.Fatalf("Outcome = %q, want done", verdict.Outcome)
	}

	spawned, ok := events.last("executor.spawned")
	if !ok {
		t.Fatal("no executor.spawned event recorded")
	}
	var payload struct {
		RunDir string `json:"run_dir"`
	}
	if err := json.Unmarshal(spawned.Payload, &payload); err != nil {
		t.Fatalf("decode executor.spawned payload: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(payload.RunDir, "pi-agent", "models.json")) //nolint:gosec // G304: path under t.TempDir.
	if err != nil {
		t.Fatalf("read written models.json: %v", err)
	}
	var cfg struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("models.json does not decode: %v", err)
	}
	if cfg.Providers["timothy"].API != "anthropic-messages" {
		t.Fatalf("written models.json = %s, want providers.timothy.api = anthropic-messages", got)
	}
}

// TestDelegatedRunWorker_PiAdapter_CostNeverTrustedEvenOnAnthropicDriver:
// cliCostTrusted must be false for a pi run even against an
// anthropic-driver entry with api_key auth - unlike claude-cli, pi's
// Capabilities().ReportsCost is false (D-013: pi prices client-side
// against its own catalog, never trusted as billed spend).
func TestDelegatedRunWorker_PiAdapter_CostNeverTrustedEvenOnAnthropicDriver(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadPiDelegatedFixture(t, "happy.ndjson")
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	led := &fakeLedger{}
	entry := piHarnessEntry("cred-ref-pi")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("sk-test-key", nil), sandbox, events, nil, led)
	m := piTestMission("m1", t.TempDir())

	verdict, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Outcome != "done" {
		t.Fatalf("Outcome = %q, want done", verdict.Outcome)
	}

	result, ok := events.last("executor.result")
	if !ok {
		t.Fatal("no executor.result event recorded")
	}
	var payload struct {
		Usage struct {
			CostUSDBilled bool `json:"cost_usd_billed"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(result.Payload, &payload); err != nil {
		t.Fatalf("decode executor.result payload: %v", err)
	}
	if payload.Usage.CostUSDBilled {
		t.Fatal("cost_usd_billed = true for a pi run, want false regardless of driver == anthropic")
	}
}

// --- scenario 8: oauth-token mode -----------------------------------------

func TestDelegatedRunWorker_OAuthTokenMode_EnvSetAndCostSuppressed(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	led := &fakeLedger{}
	entry := harnessEntry("cred-ref-oauth")
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("sk-ant-oat-test-token", nil), sandbox, events, nil, led)
	m := testMission("m1", t.TempDir())

	verdict, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Outcome != "done" {
		t.Fatalf("Outcome = %q, want done", verdict.Outcome)
	}

	sandbox.mu.Lock()
	var sawToken string
	for _, run := range sandbox.runs {
		sawToken = run.env["CLAUDE_CODE_OAUTH_TOKEN"]
	}
	sandbox.mu.Unlock()
	if sawToken != "sk-ant-oat-test-token" { //nolint:gosec // G101: fixture value, not a real credential.
		t.Fatalf("launch env CLAUDE_CODE_OAUTH_TOKEN = %q, want sk-ant-oat-test-token", sawToken)
	}

	spawned, _ := events.last("executor.spawned")
	var spawnedPayload map[string]any
	_ = json.Unmarshal(spawned.Payload, &spawnedPayload)
	if spawnedPayload["auth_mode"] != "oauth_token" {
		t.Fatalf("spawned auth_mode = %v, want oauth_token", spawnedPayload["auth_mode"])
	}

	if len(led.entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(led.entries))
	}
	if led.entries[0].Cost == nil {
		t.Fatal("oauth_token entry Cost = nil, want the CLI-reported unbilled cost")
	}
	if !led.entries[0].Unbilled {
		t.Fatal("oauth_token entry Unbilled = false, want true (subscription-billed, not real marginal spend)")
	}
}

// TestCostSource is the decision table the D-05x fix hinges on: cost
// source and Unbilled must key on (authMode, entry.Driver), never on
// harness name or provider display name — a local Ollama run behind
// options.anthropic_base_url must never book the CLI's
// Anthropic-priced figure as real spend.
func TestCostSource(t *testing.T) {
	glmPrices := &router.ModelPrices{InputPerMTok: 1, OutputPerMTok: 2}
	usage := &stream.Usage{InputTokens: 1_000_000, OutputTokens: 500_000}
	cliCost := 0.34

	tests := []struct {
		name         string
		entry        gwclient.ResolvedRouteEntry
		authMode     executor.AuthMode
		wantCost     *float64
		wantUnbilled bool
	}{
		{
			name:         "anthropic api_key trusts the CLI-reported cost, billed",
			entry:        gwclient.ResolvedRouteEntry{Driver: "anthropic"},
			authMode:     executor.AuthAPIKey,
			wantCost:     &cliCost,
			wantUnbilled: false,
		},
		{
			name:         "non-anthropic api_key (GLM) prices from its own rows, billed",
			entry:        gwclient.ResolvedRouteEntry{Driver: "openaicompat", Prices: glmPrices},
			authMode:     executor.AuthAPIKey,
			wantCost:     ledger.Cost(glmPrices, usage),
			wantUnbilled: false,
		},
		{
			name:         "non-anthropic api_key with no price row stays unpriced (nil), never the CLI's number",
			entry:        gwclient.ResolvedRouteEntry{Driver: "openaicompat"},
			authMode:     executor.AuthAPIKey,
			wantCost:     nil,
			wantUnbilled: false,
		},
		{
			name:         "kind=cli subscription auth: CLI-reported cost kept, unbilled",
			entry:        gwclient.ResolvedRouteEntry{Driver: "claude-cli", Kind: "cli"},
			authMode:     executor.AuthSubscription,
			wantCost:     &cliCost,
			wantUnbilled: true,
		},
		{
			name:         "kind=cli oauth_token auth: CLI-reported cost kept, unbilled",
			entry:        gwclient.ResolvedRouteEntry{Driver: "claude-cli", Kind: "cli"},
			authMode:     executor.AuthOAuthToken,
			wantCost:     &cliCost,
			wantUnbilled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCost, gotUnbilled := costSource(tt.entry, tt.authMode, usage, &cliCost)
			if tt.wantCost == nil {
				if gotCost != nil {
					t.Fatalf("cost = %v, want nil", *gotCost)
				}
			} else {
				if gotCost == nil {
					t.Fatalf("cost = nil, want %v", *tt.wantCost)
				}
				if *gotCost != *tt.wantCost {
					t.Fatalf("cost = %v, want %v", *gotCost, *tt.wantCost)
				}
			}
			if gotUnbilled != tt.wantUnbilled {
				t.Fatalf("unbilled = %v, want %v", gotUnbilled, tt.wantUnbilled)
			}
		})
	}
}

// TestDelegatedRunWorker_NonAnthropicAPIKey_PricedFromProviderRows is
// the end-to-end regression for the reported bug: a GLM (openaicompat
// driver) provider row wired via options.anthropic_base_url, api_key
// auth. The CLI's own reported total_cost_usd (Anthropic-priced
// fiction for this provider) must never reach the ledger — cost comes
// from the provider's configured price row × reported tokens instead.
func TestDelegatedRunWorker_NonAnthropicAPIKey_PricedFromProviderRows(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	led := &fakeLedger{}
	entry := gwclient.ResolvedRouteEntry{
		ProviderID: "prov-glm", ProviderName: "GLM (Z.ai)", Driver: "openaicompat",
		Model: "glm-4.7", CredentialRef: "GLM_KEY", Usable: true,
		Prices: &router.ModelPrices{InputPerMTok: 1, OutputPerMTok: 2},
	}
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("sk-test-key", nil), sandbox, events, nil, led)
	m := testMission("m1", t.TempDir())

	verdict, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Outcome != "done" {
		t.Fatalf("Outcome = %q, want done", verdict.Outcome)
	}
	if len(led.entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(led.entries))
	}
	if led.entries[0].Unbilled {
		t.Fatal("non-anthropic api_key entry Unbilled = true, want false (real marginal spend, just priced from the provider's own rows)")
	}
	if led.entries[0].Cost == nil {
		t.Fatal("non-anthropic api_key entry Cost = nil, want a figure priced from the provider's own rows")
	}
	// The schema fixture's total_cost_usd is ~0.0727 (Anthropic-priced);
	// this provider's own rows price the same tokens differently, so the
	// booked cost must NOT equal the CLI's raw figure.
	if *led.entries[0].Cost == 0.07270210000000002 {
		t.Fatal("booked cost equals the CLI's Anthropic-priced figure — the bug this fix closes")
	}
}

// TestDelegatedRunWorker_NonAnthropicAPIKey_NoPriceRowStaysUnpriced
// covers the no-configured-price case: cost stays nil (D-013) rather
// than falling back to the CLI's Anthropic-priced number.
func TestDelegatedRunWorker_NonAnthropicAPIKey_NoPriceRowStaysUnpriced(t *testing.T) {
	sandbox := newFakeSandbox()
	sandbox.seedLines = loadDelegatedFixture(t, "schema.ndjson")
	sandbox.seedExitCode = 0
	events := &fakeEventSink{}
	led := &fakeLedger{}
	entry := gwclient.ResolvedRouteEntry{
		ProviderID: "prov-ollama", ProviderName: "Local Ollama", Driver: "openaicompat",
		Model: "qwen3", CredentialRef: "", Usable: true, // no Prices set
	}
	route := &gwclient.ResolvedRoute{Route: "default", Entries: []gwclient.ResolvedRouteEntry{entry}}

	r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(route, nil), scriptedCred("sk-test-key", nil), sandbox, events, nil, led)
	m := testMission("m1", t.TempDir())

	verdict, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if verdict.Outcome != "done" {
		t.Fatalf("Outcome = %q, want done", verdict.Outcome)
	}
	if len(led.entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(led.entries))
	}
	if led.entries[0].Unbilled {
		t.Fatal("non-anthropic api_key entry Unbilled = true, want false")
	}
	if led.entries[0].Cost != nil {
		t.Fatalf("no-price-row entry Cost = %v, want nil (D-013: never guess)", *led.entries[0].Cost)
	}
}

func TestResolveCredential(t *testing.T) {
	adapter, ok := executor.Lookup("claude-cli")
	if !ok {
		t.Fatal("claude-cli adapter not registered")
	}
	caps := adapter.Capabilities()

	tests := []struct {
		name     string
		ref      string
		key      string
		wantMode executor.AuthMode
	}{
		{name: "subscription literal", ref: "subscription", wantMode: executor.AuthSubscription},
		{name: "plain api key", ref: "cred-ref-1", key: "sk-test-key", wantMode: executor.AuthAPIKey},
		{name: "oauth token prefix", ref: "cred-ref-oauth", key: "sk-ant-oat-test-token", wantMode: executor.AuthOAuthToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestDelegatedRunner(&fakeNative{}, scriptedResolver(nil, nil), scriptedCred(tt.key, nil), newFakeSandbox(), nil, nil, nil)
			mode, key, err := r.resolveCredential(context.Background(), tt.ref, caps)
			if err != nil {
				t.Fatalf("resolveCredential: %v", err)
			}
			if mode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", mode, tt.wantMode)
			}
			if tt.ref != "subscription" && key != tt.key {
				t.Fatalf("key = %q, want %q", key, tt.key)
			}
		})
	}
}

// --- scenario 8: dispatch --------------------------------------------------

// TestDelegatedRunWorker_Dispatch_EmptyHarnessSkipsResolve covers
// m.Harness == "" (D-051 rework): RunWorker must defer straight to
// native without ever calling resolveRoute at all — resolve is only
// meaningful once a harness names a real executor.
func TestDelegatedRunWorker_Dispatch_EmptyHarnessSkipsResolve(t *testing.T) {
	native := &fakeNative{verdict: WorkerVerdict{Outcome: "done"}}
	resolveCalled := false
	resolve := func(ctx context.Context, name, harness string) (*gwclient.ResolvedRoute, error) {
		resolveCalled = true
		return nil, nil
	}
	events := &fakeEventSink{}
	sandbox := newFakeSandbox()

	r := newTestDelegatedRunner(native, resolve, scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testNativeMission("m1", t.TempDir())

	if _, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if native.callCount() != 1 {
		t.Fatalf("native call count = %d, want 1", native.callCount())
	}
	if resolveCalled {
		t.Fatal("resolveRoute called for an empty-harness mission; want it skipped entirely")
	}
	if len(events.events) != 0 {
		t.Fatalf("executor events recorded = %d, want 0 for a native mission", len(events.events))
	}
	if sandbox.launches != 0 {
		t.Fatalf("sandbox launches = %d, want 0", sandbox.launches)
	}
}

func TestDelegatedRunWorker_Dispatch_UnknownHarnessFallsBackToNative(t *testing.T) {
	native := &fakeNative{verdict: WorkerVerdict{Outcome: "done"}}
	sandbox := newFakeSandbox()
	events := &fakeEventSink{}

	r := newTestDelegatedRunner(native, scriptedResolver(nil, nil), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())
	m.Harness = "codex-cli-unregistered"

	if _, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if native.callCount() != 1 {
		t.Fatalf("native call count = %d, want 1", native.callCount())
	}
	if events.count("executor.skipped") != 1 {
		t.Fatalf("executor.skipped count = %d, want 1", events.count("executor.skipped"))
	}
	skipped, _ := events.last("executor.skipped")
	var skippedPayload map[string]any
	_ = json.Unmarshal(skipped.Payload, &skippedPayload)
	if skippedPayload["reason"] != "unknown_harness" {
		t.Fatalf("executor.skipped reason = %v, want unknown_harness", skippedPayload["reason"])
	}
	if skippedPayload["harness"] != m.Harness {
		t.Fatalf("executor.skipped harness = %v, want %q", skippedPayload["harness"], m.Harness)
	}
}

// D-072: a general mission with Harness set (a row predating
// ValidateCreate's coding-only rule, or inserted around it) must fall
// back to native and record why — enforces the coding-only harness
// rule in-package instead of trusting the caller already checked it.
func TestDelegatedRunWorker_Dispatch_HarnessOnGeneralFallsBackToNative(t *testing.T) {
	native := &fakeNative{verdict: WorkerVerdict{Outcome: "done"}}
	resolveCalled := false
	resolve := func(ctx context.Context, name, harness string) (*gwclient.ResolvedRoute, error) {
		resolveCalled = true
		return nil, nil
	}
	sandbox := newFakeSandbox()
	events := &fakeEventSink{}

	r := newTestDelegatedRunner(native, resolve, scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())
	m.Kind = "general"

	if _, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if native.callCount() != 1 {
		t.Fatalf("native call count = %d, want 1", native.callCount())
	}
	if resolveCalled {
		t.Fatal("resolveRoute called for a harness-not-allowed mission; want it skipped entirely")
	}
	if events.count("executor.skipped") != 1 {
		t.Fatalf("executor.skipped count = %d, want 1", events.count("executor.skipped"))
	}
	skipped, _ := events.last("executor.skipped")
	var skippedPayload map[string]any
	_ = json.Unmarshal(skipped.Payload, &skippedPayload)
	if skippedPayload["reason"] != "harness not allowed for kind" {
		t.Fatalf("executor.skipped reason = %v, want %q", skippedPayload["reason"], "harness not allowed for kind")
	}
	if skippedPayload["harness"] != m.Harness {
		t.Fatalf("executor.skipped harness = %v, want %q", skippedPayload["harness"], m.Harness)
	}
}

func TestDelegatedRunWorker_Dispatch_ResolveErrorFallsBackToNative(t *testing.T) {
	native := &fakeNative{verdict: WorkerVerdict{Outcome: "done"}}
	sandbox := newFakeSandbox()
	events := &fakeEventSink{}

	r := newTestDelegatedRunner(native, scriptedResolver(nil, fmt.Errorf("gateway unreachable")), scriptedCred("", nil), sandbox, events, nil, &fakeLedger{})
	m := testMission("m1", t.TempDir())

	if _, _, err := r.RunWorker(testCtx(t), m, WorkPacket{Goal: "test"}); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if native.callCount() != 1 {
		t.Fatalf("native call count = %d, want 1", native.callCount())
	}
	if events.count("executor.skipped") != 1 {
		t.Fatalf("executor.skipped count = %d, want 1", events.count("executor.skipped"))
	}
	skipped, _ := events.last("executor.skipped")
	var skippedPayload map[string]any
	_ = json.Unmarshal(skipped.Payload, &skippedPayload)
	if skippedPayload["reason"] != "resolve_failed" {
		t.Fatalf("executor.skipped reason = %v, want resolve_failed", skippedPayload["reason"])
	}
	if errStr, _ := skippedPayload["error"].(string); errStr == "" {
		t.Fatalf("executor.skipped error = %q, want a non-empty error string", errStr)
	}
}

// --- discover/plan/review pass-through -------------------------------------

func TestDelegatedRunner_PassesThroughNonWorkerSessions(t *testing.T) {
	native := &fakeNative{}
	r := newTestDelegatedRunner(native, scriptedResolver(nil, nil), scriptedCred("", nil), newFakeSandbox(), nil, nil, nil)
	m := testMission("m1", t.TempDir())

	if _, _, _, err := r.DiscoverSession(context.Background(), m); err != nil {
		t.Fatalf("DiscoverSession: %v", err)
	}
	if _, err := r.PlanSession(context.Background(), m, ""); err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if _, err := r.RunReview(context.Background(), m, ReviewPacket{}); err != nil {
		t.Fatalf("RunReview: %v", err)
	}
}

// shRoundTrip runs `printf '%s' <quoted>` through a real /bin/sh and
// returns what it printed — the ground truth for whether shQuote's
// escaping actually holds against a real shell, not just this test's
// own assumptions about POSIX quoting.
func shRoundTrip(quoted string) (string, error) {
	cmd := exec.Command("/bin/sh", "-c", "printf '%s' "+quoted) //nolint:gosec // test-only, quoted is the thing under test
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// --- quote escaper ----------------------------------------------------------

// TestParsePollOutputWorktreeLine covers the WT: line's three states:
// present and well-formed, absent entirely, and malformed, issue #500
// requires nil (not an error) on the latter two.
func TestParsePollOutputWorktreeLine(t *testing.T) {
	const boundary = "---TIMOTHY-RUN-abc---"
	tests := []struct {
		name string
		raw  string
		want *WorktreeSummary
	}{
		{
			name: "present",
			raw:  boundary + "\nEXITCODE:0\nWT:3 1 1735689600\n",
			want: &WorktreeSummary{Untracked: 3, Modified: 1, NewestMtime: 1735689600},
		},
		{
			name: "absent",
			raw:  boundary + "\nEXITCODE:0\nALIVE\n",
			want: nil,
		},
		{
			name: "malformed_too_few_fields",
			raw:  boundary + "\nWT:3 1\n",
			want: nil,
		},
		{
			name: "malformed_non_numeric",
			raw:  boundary + "\nWT:x y z\n",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, worktree, err := parsePollOutput([]byte(tc.raw), boundary)
			if err != nil {
				t.Fatalf("parsePollOutput: %v", err)
			}
			if (worktree == nil) != (tc.want == nil) {
				t.Fatalf("worktree = %+v, want %+v", worktree, tc.want)
			}
			if tc.want != nil && *worktree != *tc.want {
				t.Fatalf("worktree = %+v, want %+v", worktree, tc.want)
			}
		})
	}
}

// TestBuildPollCmdRealShell runs the composed poll command through a real
// /bin/sh against an on-disk run dir — the fake sandbox re-implements the
// poll semantics, so only a real shell catches syntax the fake forgives
// (a dash-leading printf format was rejected as an illegal option by
// dash's builtin and broke every live poll before this test existed).
func TestBuildPollCmdRealShell(t *testing.T) {
	rdir := t.TempDir()
	line := `{"type":"system","subtype":"init"}` + "\n"
	if err := os.WriteFile(filepath.Join(rdir, "run.ndjson"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rdir, "exit_code"), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rdir, "pid"), []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir() // not a git repo: WT: line must be absent, not fatal
	cmd, boundary := buildPollCmd(workdir, rdir, "cafe0123beef", 0)
	out, err := exec.Command("/bin/sh", "-c", cmd).CombinedOutput() //nolint:gosec // G204: executing the composed command is the point of the test.
	if err != nil {
		// The trailing `kill -0` legitimately exits nonzero for a dead
		// pid; only treat a shell-level failure (no output) as fatal.
		if len(out) == 0 {
			t.Fatalf("poll command failed: %v", err)
		}
	}
	chunk, exitCode, hasExit, alive, worktree, perr := parsePollOutput(out, boundary)
	if perr != nil {
		t.Fatalf("parsePollOutput: %v (raw %q)", perr, out)
	}
	if string(chunk) != line {
		t.Fatalf("chunk = %q, want %q", chunk, line)
	}
	if !hasExit || exitCode != 0 {
		t.Fatalf("exit = (%v,%d), want (true,0)", hasExit, exitCode)
	}
	if alive {
		t.Fatal("pid 999999 should not be alive")
	}
	if worktree != nil {
		t.Fatalf("worktree = %+v, want nil (workdir is not a git repo)", worktree)
	}
}

// TestBuildPollCmdRealShellWorktreeSummary runs the composed poll
// command against a real git repo (issue #500): one committed file,
// one modified, two untracked. Asserts WT:2 1 <epoch> with epoch > 0.
func TestBuildPollCmdRealShellWorktreeSummary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable on this host; runs in the containerized suite")
	}
	workdir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...) //nolint:gosec // G204: test-only, args are fixed literals from this test.
		cmd.Dir = workdir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	runGit("init")
	if err := os.WriteFile(filepath.Join(workdir, "committed.txt"), []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "committed.txt")
	runGit("commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(workdir, "committed.txt"), []byte("v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "untracked1.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "untracked2.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rdir, "run.ndjson"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rdir, "exit_code"), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rdir, "pid"), []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, boundary := buildPollCmd(workdir, rdir, "beef0123cafe", 0)
	out, err := exec.Command("/bin/sh", "-c", cmd).CombinedOutput() //nolint:gosec // G204: executing the composed command is the point of the test.
	if err != nil && len(out) == 0 {
		t.Fatalf("poll command failed: %v", err)
	}
	_, _, _, _, worktree, perr := parsePollOutput(out, boundary)
	if perr != nil {
		t.Fatalf("parsePollOutput: %v (raw %q)", perr, out)
	}
	if worktree == nil {
		t.Fatalf("worktree = nil, want a summary (raw %q)", out)
	}
	if worktree.Untracked != 2 || worktree.Modified != 1 {
		t.Fatalf("worktree = %+v, want {Untracked:2 Modified:1}", worktree)
	}
	if worktree.NewestMtime <= 0 {
		t.Fatalf("worktree.NewestMtime = %d, want > 0", worktree.NewestMtime)
	}
}

// TestBuildLaunchCmdRealShell runs the composed launch command through a
// real /bin/sh (needs setsid, present in the containerized test image) and
// asserts every argv element — spaces, quotes, prompt substitution — arrives
// in the child process intact. The fakeSandbox only pattern-matches command
// shapes, so quote-nesting bugs are invisible to it; this is the ground truth.
func TestBuildLaunchCmdRealShell(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid unavailable on this host; runs in the containerized suite")
	}
	work := t.TempDir()
	rdir := filepath.Join(work, "runs", "test01")
	promptPath := filepath.Join(work, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hello prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stand-in CLI: prints each received arg on its own line.
	cli := filepath.Join(work, "fakecli")
	//nolint:gosec // G306: an executable stand-in script needs the exec bit.
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	inv := executor.Invocation{
		Argv: []string{
			cli, "-p", "@PROMPT@",
			"--append-system-prompt", "two words with 'embedded quotes' and $DOLLAR",
			"--model", "glm-4.7",
		},
		PromptFile: promptPath,
	}
	cmd, err := buildLaunchCmd(work, rdir, inv, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/sh", "-c", cmd).CombinedOutput(); err != nil { //nolint:gosec // G204: executing the composed command is the point of the test.
		t.Fatalf("launch command failed: %v: %s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	exitPath := filepath.Join(rdir, "exit_code")
	for {
		if _, err := os.Stat(exitPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exit_code never appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := os.ReadFile(filepath.Join(rdir, "run.ndjson")) //nolint:gosec // G304: path under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	want := "-p\nhello prompt\n--append-system-prompt\ntwo words with 'embedded quotes' and $DOLLAR\n--model\nglm-4.7\n"
	if string(got) != want {
		t.Fatalf("child argv mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	b, err := os.ReadFile(exitPath) //nolint:gosec // G304: path under t.TempDir.
	if err != nil || strings.TrimSpace(string(b)) != "0" {
		t.Fatalf("exit_code = %q, err %v; want 0", b, err)
	}
}

func TestShQuote(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"plain", "hello"},
		{"single quote", "it's a test"},
		{"multiple quotes", "'''"},
		{"double quotes", `he said "hi"`},
		{"dollar sign", "$HOME/foo"},
		{"backtick", "`whoami`"},
		{"semicolon injection attempt", "foo'; rm -rf /; echo '"},
		{"newline", "line1\nline2"},
		{"empty", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			quoted := shQuote(c.in)
			// A round trip through /bin/sh -c "printf '%s' <quoted>" must
			// reproduce the original string exactly.
			out, err := shRoundTrip(quoted)
			if err != nil {
				t.Fatalf("round trip exec: %v", err)
			}
			if out != c.in {
				t.Fatalf("shQuote(%q) round-tripped to %q, want %q", c.in, out, c.in)
			}
		})
	}
}

// TestRecordLedgerPrefersReportedModel proves a ledger row names the
// model the harness said it ran, falling back to the route entry's
// model when the harness reports none. The self-paired case is the
// reason this exists: cursor-cli's provider row carries the literal
// placeholder "default" as its model, so without the harness's own
// report every Cursor run books its tokens against a model name that
// does not exist.
func TestRecordLedgerPrefersReportedModel(t *testing.T) {
	cases := []struct {
		name          string
		entryModel    string
		reportedModel string
		want          string
	}{
		{"harness report wins over a self-paired placeholder", "default", "claude-4.5-sonnet", "claude-4.5-sonnet"},
		{"no report falls back to the route entry", "claude-sonnet-5", "", "claude-sonnet-5"},
		{"harness report wins over a real entry model too", "claude-sonnet-5", "claude-opus-5", "claude-opus-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			led := &fakeLedger{}
			r := &delegatedRunner{ledger: led, log: slog.Default()}
			entry := gwclient.ResolvedRouteEntry{ProviderName: "Cursor", Model: tc.entryModel}

			r.recordLedger(context.Background(), Mission{ID: "m1"}, entry,
				executor.AuthSubscription, nil, time.Now(), true, "", tc.reportedModel)

			if len(led.entries) != 1 {
				t.Fatalf("got %d ledger entries, want 1", len(led.entries))
			}
			if got := led.entries[0].Model; got != tc.want {
				t.Errorf("ledger Model = %q, want %q", got, tc.want)
			}
		})
	}
}
