package missions

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
