package missions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeCapacityChecker scripts one Capacity call per test case.
type fakeCapacityChecker struct {
	admit  bool
	reason string
	err    error
}

func (f fakeCapacityChecker) Capacity(ctx context.Context) (bool, string, error) {
	return f.admit, f.reason, f.err
}

// TestAdmitWork covers D-056's three gate outcomes: nil gate always
// admits (tests, sandbox-less setups), a gate error also admits (fails
// open so a dead sandboxd never freezes the mission queue), and a live
// gate's own admit/reason pass through unchanged.
func TestAdmitWork(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cases := []struct {
		name       string
		gate       capacityChecker
		wantAdmit  bool
		wantReason string
	}{
		{name: "nil gate admits", gate: nil, wantAdmit: true},
		{
			name:      "gate error admits open",
			gate:      fakeCapacityChecker{err: errors.New("sandboxd unreachable")},
			wantAdmit: true,
		},
		{
			name:       "gate denies",
			gate:       fakeCapacityChecker{admit: false, reason: "mem_available 900MB < floor 1024 + per-sandbox 768"},
			wantAdmit:  false,
			wantReason: "mem_available 900MB < floor 1024 + per-sandbox 768",
		},
		{
			name:      "gate admits",
			gate:      fakeCapacityChecker{admit: true},
			wantAdmit: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			admit, reason := admitWork(context.Background(), tc.gate, log)
			if admit != tc.wantAdmit {
				t.Errorf("admit = %v, want %v", admit, tc.wantAdmit)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// fakeBackoffStore scripts BackoffPaused/CountBackoffPauses for
// autoResumeBackoff tests without a real Postgres pool.
type fakeBackoffStore struct {
	paused []BackoffPausedMission
	counts map[string]int
}

func (f fakeBackoffStore) BackoffPaused(ctx context.Context) ([]BackoffPausedMission, error) {
	return f.paused, nil
}

func (f fakeBackoffStore) CountBackoffPauses(ctx context.Context, missionID string) (int, error) {
	return f.counts[missionID], nil
}

// fakeSignaler captures Signal calls for autoResumeBackoff tests.
type fakeSignaler struct {
	signaled []string
}

func (f *fakeSignaler) Signal(ctx context.Context, id string, input Input) error {
	f.signaled = append(f.signaled, id)
	return nil
}

// fakeMessageNotifier captures NotifyMessage calls for
// autoResumeBackoff tests.
type fakeMessageNotifier struct {
	notified []string
}

func (f *fakeMessageNotifier) NotifyMessage(ctx context.Context, missionID, kind, message string) error {
	f.notified = append(f.notified, missionID)
	return nil
}

// TestAutoResumeBackoffLadder covers D-065's ladder boundaries: just
// before each threshold, the mission is left alone; just after, it's
// resumed. n>=4 never resumes, and notifies exactly once per tick.
func TestAutoResumeBackoffLadder(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Now()

	cases := []struct {
		name       string
		n          int
		since      time.Duration
		wantResume bool
		wantNotify bool
	}{
		{"n=1 just before 5m due", 1, 5*time.Minute - time.Second, false, false},
		{"n=1 just after 5m due", 1, 5*time.Minute + time.Second, true, false},
		{"n=2 just before 15m due", 2, 15*time.Minute - time.Second, false, false},
		{"n=2 just after 15m due", 2, 15*time.Minute + time.Second, true, false},
		{"n=3 just before 60m due", 3, 60*time.Minute - time.Second, false, false},
		{"n=3 just after 60m due", 3, 60*time.Minute + time.Second, true, false},
		{"n=4 exhausted, never resumes", 4, 999 * time.Hour, false, true},
		{"n=5 exhausted, never resumes", 5, 999 * time.Hour, false, true},
		{"n=0 no recorded pause yet, skipped", 0, 999 * time.Hour, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := fakeBackoffStore{
				paused: []BackoffPausedMission{{ID: "m1", UpdatedAt: now.Add(-tc.since)}},
				counts: map[string]int{"m1": tc.n},
			}
			signaler := &fakeSignaler{}
			notifier := &fakeMessageNotifier{}
			autoResumeBackoff(context.Background(), signaler, store, notifier, log)

			if resumed := len(signaler.signaled) == 1; resumed != tc.wantResume {
				t.Fatalf("signaled = %v, want resume=%v", signaler.signaled, tc.wantResume)
			}
			if notified := len(notifier.notified) == 1; notified != tc.wantNotify {
				t.Fatalf("notified = %v, want notify=%v", notifier.notified, tc.wantNotify)
			}
		})
	}
}

// TestAutoResumeBackoffNilNotifierSafe confirms a nil notifier (no
// wiring, matching every other nil-safe hook in this package) never
// panics on the exhausted path.
func TestAutoResumeBackoffNilNotifierSafe(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := fakeBackoffStore{
		paused: []BackoffPausedMission{{ID: "m1", UpdatedAt: time.Now().Add(-999 * time.Hour)}},
		counts: map[string]int{"m1": 4},
	}
	signaler := &fakeSignaler{}
	autoResumeBackoff(context.Background(), signaler, store, nil, log)
	if len(signaler.signaled) != 0 {
		t.Fatal("exhausted mission must never be resumed")
	}
}

// fakePausedByReasonStore scripts PausedByReason/CountPausesByReason
// for autoResumeInfra tests without a real Postgres pool — the
// reason-parameterized counterpart of fakeBackoffStore.
type fakePausedByReasonStore struct {
	paused []BackoffPausedMission
	counts map[string]int
}

func (f fakePausedByReasonStore) PausedByReason(ctx context.Context, reason string) ([]BackoffPausedMission, error) {
	return f.paused, nil
}

func (f fakePausedByReasonStore) CountPausesByReason(ctx context.Context, missionID, reason string) (int, error) {
	return f.counts[missionID], nil
}

// TestAutoResumeInfraLadder covers autoResumeInfra's own, shorter
// ladder ({2m, 10m, 30m}) and lower cap (3 prior pauses) — mirrors
// TestAutoResumeBackoffLadder's boundary structure.
func TestAutoResumeInfraLadder(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Now()

	cases := []struct {
		name       string
		n          int
		since      time.Duration
		wantResume bool
		wantNotify bool
	}{
		{"n=1 just before 2m due", 1, 2*time.Minute - time.Second, false, false},
		{"n=1 just after 2m due", 1, 2*time.Minute + time.Second, true, false},
		{"n=2 just before 10m due", 2, 10*time.Minute - time.Second, false, false},
		{"n=2 just after 10m due", 2, 10*time.Minute + time.Second, true, false},
		{"n=3 exhausted, never resumes", 3, 999 * time.Hour, false, true},
		{"n=4 exhausted, never resumes", 4, 999 * time.Hour, false, true},
		{"n=0 no recorded pause yet, skipped", 0, 999 * time.Hour, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := fakePausedByReasonStore{
				paused: []BackoffPausedMission{{ID: "m1", UpdatedAt: now.Add(-tc.since)}},
				counts: map[string]int{"m1": tc.n},
			}
			signaler := &fakeSignaler{}
			notifier := &fakeMessageNotifier{}
			autoResumeInfra(context.Background(), signaler, store, notifier, log)

			if resumed := len(signaler.signaled) == 1; resumed != tc.wantResume {
				t.Fatalf("signaled = %v, want resume=%v", signaler.signaled, tc.wantResume)
			}
			if notified := len(notifier.notified) == 1; notified != tc.wantNotify {
				t.Fatalf("notified = %v, want notify=%v", notifier.notified, tc.wantNotify)
			}
		})
	}
}

// TestAutoResumeInfraNotifiesOncePerTick confirms the exhausted path
// notifies exactly once per sweep tick (NotifyMessage's own dedupe in
// notify.go covers repeat ticks) — mirrors the backoff sweep's
// single-notify-per-call behavior already covered by the ladder table
// above; this test isolates it explicitly for the infra ladder's own
// (lower) cap.
func TestAutoResumeInfraNotifiesOncePerTick(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := fakePausedByReasonStore{
		paused: []BackoffPausedMission{{ID: "m1", UpdatedAt: time.Now().Add(-999 * time.Hour)}},
		counts: map[string]int{"m1": 3},
	}
	signaler := &fakeSignaler{}
	notifier := &fakeMessageNotifier{}
	autoResumeInfra(context.Background(), signaler, store, notifier, log)
	if len(notifier.notified) != 1 {
		t.Fatalf("notified = %d, want 1", len(notifier.notified))
	}
	if len(signaler.signaled) != 0 {
		t.Fatal("exhausted mission must never be resumed")
	}
}

// TestAutoResumeInfraNilNotifierSafe confirms a nil notifier never
// panics on the exhausted path — same nil-safe contract as
// autoResumeBackoff.
func TestAutoResumeInfraNilNotifierSafe(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := fakePausedByReasonStore{
		paused: []BackoffPausedMission{{ID: "m1", UpdatedAt: time.Now().Add(-999 * time.Hour)}},
		counts: map[string]int{"m1": 3},
	}
	signaler := &fakeSignaler{}
	autoResumeInfra(context.Background(), signaler, store, nil, log)
	if len(signaler.signaled) != 0 {
		t.Fatal("exhausted mission must never be resumed")
	}
}

// TestShouldTimeoutPermission covers issue #445's timeout-detection
// decision: a mission override always wins over the global setting, and
// <= 0 (either source) means disabled, the settings default that
// preserves park-forever behavior for a deployment that never opts in.
func TestShouldTimeoutPermission(t *testing.T) {
	t.Parallel()
	now := time.Now()

	intp := func(n int) *int { return &n }

	cases := []struct {
		name        string
		parkedAgo   time.Duration
		override    *int
		global      int
		wantTimeout bool
	}{
		{"global disabled (0), no override: never times out", 999 * time.Hour, nil, 0, false},
		{"global set, just before due", 59 * time.Second, nil, 60, false},
		{"global set, just after due", 61 * time.Second, nil, 60, true},
		{"override disables even with global set", 999 * time.Hour, intp(0), 300, false},
		{"override shorter than global, fires sooner", 31 * time.Second, intp(30), 300, true},
		{"override longer than global, waits longer", 31 * time.Second, intp(300), 30, false},
		{"negative override treated as disabled", 999 * time.Hour, intp(-1), 300, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldTimeoutPermission(now, now.Add(-tc.parkedAgo), tc.override, tc.global)
			if got != tc.wantTimeout {
				t.Errorf("shouldTimeoutPermission() = %v, want %v", got, tc.wantTimeout)
			}
		})
	}
}

// fakePermissionTimeoutStore scripts PendingPermissions/
// ResolvePendingPermissionTimeout for sweepPermissionTimeouts tests
// without a real Postgres pool.
type fakePermissionTimeoutStore struct {
	pending  []ParkedPermission
	resolved []string
}

func (f *fakePermissionTimeoutStore) PendingPermissions(ctx context.Context) ([]ParkedPermission, error) {
	return f.pending, nil
}

func (f *fakePermissionTimeoutStore) ResolvePendingPermissionTimeout(ctx context.Context, id, tool string) error {
	f.resolved = append(f.resolved, id)
	return nil
}

// fakePermissionTimeoutDriver captures Drive calls, safe for concurrent
// use since sweepPermissionTimeouts fires Drive in a goroutine per
// mission (mirroring reDriveStaleWorking's own fire-and-forget shape).
type fakePermissionTimeoutDriver struct {
	mu    sync.Mutex
	wg    sync.WaitGroup
	drove []string
}

func (f *fakePermissionTimeoutDriver) Drive(ctx context.Context, id string) error {
	defer f.wg.Done()
	f.mu.Lock()
	f.drove = append(f.drove, id)
	f.mu.Unlock()
	return nil
}

// TestSweepPermissionTimeoutsDisabledByDefault confirms a global setting
// of 0 (settings.Store.PermissionTimeoutSeconds' own default for an
// absent/unset row) leaves every parked mission untouched, no matter how
// long it has been parked: existing missions with pending_permission
// stay parked forever, matching current behavior exactly.
func TestSweepPermissionTimeoutsDisabledByDefault(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &fakePermissionTimeoutStore{
		pending: []ParkedPermission{{MissionID: "m1", PermissionID: "p1", Tool: "shell", ParkedAt: time.Now().Add(-999 * time.Hour)}},
	}
	driver := &fakePermissionTimeoutDriver{}
	notifier := &fakeMessageNotifier{}
	sweepPermissionTimeouts(context.Background(), driver, store, func(context.Context) int { return 0 }, nil, notifier, log)

	if len(store.resolved) != 0 {
		t.Fatalf("resolved = %v, want none (sweep disabled)", store.resolved)
	}
	if len(notifier.notified) != 0 {
		t.Fatalf("notified = %v, want none (sweep disabled)", notifier.notified)
	}
}

// TestSweepPermissionTimeoutsResolvesAndDrives covers the enabled path:
// a mission parked past the effective timeout is resolved (denied),
// notified, and re-Driven; one still within its window is left alone.
func TestSweepPermissionTimeoutsResolvesAndDrives(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Now()
	store := &fakePermissionTimeoutStore{
		pending: []ParkedPermission{
			{MissionID: "timed-out", PermissionID: "p1", Tool: "shell", ParkedAt: now.Add(-10 * time.Minute)},
			{MissionID: "still-waiting", PermissionID: "p2", Tool: "shell", ParkedAt: now.Add(-1 * time.Second)},
		},
	}
	driver := &fakePermissionTimeoutDriver{}
	driver.wg.Add(1) // exactly one mission (timed-out) crosses the 60s global timeout
	notifier := &fakeMessageNotifier{}
	var resolvedBrokerIDs []string
	resolveBroker := func(id, decision string) bool {
		resolvedBrokerIDs = append(resolvedBrokerIDs, id+"|"+decision)
		return true
	}

	sweepPermissionTimeouts(context.Background(), driver, store, func(context.Context) int { return 60 }, resolveBroker, notifier, log)
	driver.wg.Wait()

	if len(store.resolved) != 1 || store.resolved[0] != "timed-out" {
		t.Fatalf("resolved = %v, want exactly [timed-out]", store.resolved)
	}
	if len(notifier.notified) != 1 || notifier.notified[0] != "timed-out" {
		t.Fatalf("notified = %v, want exactly [timed-out]", notifier.notified)
	}
	if len(driver.drove) != 1 || driver.drove[0] != "timed-out" {
		t.Fatalf("drove = %v, want exactly [timed-out]", driver.drove)
	}
	if len(resolvedBrokerIDs) != 1 || resolvedBrokerIDs[0] != "p1|deny" {
		t.Fatalf("resolveBroker calls = %v, want exactly [p1|deny]", resolvedBrokerIDs)
	}
}

// TestSweepPermissionTimeoutsNilResolveBrokerSafe confirms a nil
// resolveBroker (a still-live worker turn just times out on its own
// in-process permissionTimeout instead) never panics.
func TestSweepPermissionTimeoutsNilResolveBrokerSafe(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &fakePermissionTimeoutStore{
		pending: []ParkedPermission{{MissionID: "m1", PermissionID: "p1", Tool: "shell", ParkedAt: time.Now().Add(-999 * time.Hour)}},
	}
	driver := &fakePermissionTimeoutDriver{}
	driver.wg.Add(1)
	sweepPermissionTimeouts(context.Background(), driver, store, func(context.Context) int { return 60 }, nil, nil, log)
	driver.wg.Wait()

	if len(store.resolved) != 1 {
		t.Fatalf("resolved = %v, want exactly one", store.resolved)
	}
}

// TestShouldTimeoutAsk mirrors TestShouldTimeoutPermission's table for
// ask_user's own timeout decision (D-088, issue #457): no per-mission
// override exists (unlike permission's), just the global setting.
func TestShouldTimeoutAsk(t *testing.T) {
	t.Parallel()
	now := time.Now()

	cases := []struct {
		name        string
		askedAgo    time.Duration
		global      int
		wantTimeout bool
	}{
		{"global disabled (0): never times out", 999 * time.Hour, 0, false},
		{"global negative: never times out", 999 * time.Hour, -1, false},
		{"just before due", 59 * time.Second, 60, false},
		{"just after due", 61 * time.Second, 60, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldTimeoutAsk(now, now.Add(-tc.askedAgo), tc.global)
			if got != tc.wantTimeout {
				t.Errorf("shouldTimeoutAsk() = %v, want %v", got, tc.wantTimeout)
			}
		})
	}
}

// fakeAskTimeoutStore scripts PendingInputs/AnswerPendingInput for
// sweepAskTimeouts tests, mirroring fakePermissionTimeoutStore.
type fakeAskTimeoutStore struct {
	pending  []ParkedInput
	resolved []string
}

func (f *fakeAskTimeoutStore) PendingInputs(ctx context.Context) ([]ParkedInput, error) {
	return f.pending, nil
}

func (f *fakeAskTimeoutStore) AnswerPendingInput(ctx context.Context, id, eventKind string, payload map[string]any) error {
	f.resolved = append(f.resolved, id+"|"+eventKind)
	return nil
}

// fakeAskTimeoutDriver captures Drive calls, mirroring
// fakePermissionTimeoutDriver.
type fakeAskTimeoutDriver struct {
	mu    sync.Mutex
	wg    sync.WaitGroup
	drove []string
}

func (f *fakeAskTimeoutDriver) Drive(ctx context.Context, id string) error {
	defer f.wg.Done()
	f.mu.Lock()
	f.drove = append(f.drove, id)
	f.mu.Unlock()
	return nil
}

// TestSweepAskTimeoutsDisabledByDefault mirrors
// TestSweepPermissionTimeoutsDisabledByDefault: a global setting of 0
// leaves every parked question untouched.
func TestSweepAskTimeoutsDisabledByDefault(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &fakeAskTimeoutStore{
		pending: []ParkedInput{{MissionID: "m1", Input: PendingInput{AskedAt: time.Now().Add(-999 * time.Hour)}}},
	}
	driver := &fakeAskTimeoutDriver{}
	notifier := &fakeMessageNotifier{}
	sweepAskTimeouts(context.Background(), driver, store, func(context.Context) int { return 0 }, notifier, log)

	if len(store.resolved) != 0 {
		t.Fatalf("resolved = %v, want none (sweep disabled)", store.resolved)
	}
	if len(notifier.notified) != 0 {
		t.Fatalf("notified = %v, want none (sweep disabled)", notifier.notified)
	}
}

// TestSweepAskTimeoutsAppliesDefaultAndDrives covers the enabled path:
// a mission parked past the effective timeout has its proposed_default
// applied via mission.input_timed_out, is notified, and re-Driven; one
// still within its window is left alone.
func TestSweepAskTimeoutsAppliesDefaultAndDrives(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Now()
	store := &fakeAskTimeoutStore{
		pending: []ParkedInput{
			{MissionID: "timed-out", Input: PendingInput{ProposedDefault: "yes", AskedAt: now.Add(-10 * time.Minute)}},
			{MissionID: "still-waiting", Input: PendingInput{ProposedDefault: "no", AskedAt: now.Add(-1 * time.Second)}},
		},
	}
	driver := &fakeAskTimeoutDriver{}
	driver.wg.Add(1) // exactly one mission (timed-out) crosses the 60s global timeout
	notifier := &fakeMessageNotifier{}

	sweepAskTimeouts(context.Background(), driver, store, func(context.Context) int { return 60 }, notifier, log)
	driver.wg.Wait()

	if len(store.resolved) != 1 || store.resolved[0] != "timed-out|mission.input_timed_out" {
		t.Fatalf("resolved = %v, want exactly [timed-out|mission.input_timed_out]", store.resolved)
	}
	if len(notifier.notified) != 1 || notifier.notified[0] != "timed-out" {
		t.Fatalf("notified = %v, want exactly [timed-out]", notifier.notified)
	}
	if len(driver.drove) != 1 || driver.drove[0] != "timed-out" {
		t.Fatalf("drove = %v, want exactly [timed-out]", driver.drove)
	}
}
