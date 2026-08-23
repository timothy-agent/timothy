package missions

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// workSlotSweepInterval is how often the idle-over-cap sweep retries
// claiming a work slot for missions parked idle because the
// concurrency cap was full when they last tried, and how often the
// stale-working sweep below checks for a mission whose Drive loop
// stopped advancing without a crash.
const workSlotSweepInterval = 30 * time.Second

// staleWorkingAfter bounds how long a 'working' mission may go without a
// turn before the periodic sweep re-Drives it. Invariant: staleWorkingAfter
// > turnTimeout (runner.go) — otherwise this sweep can re-Drive a mission
// still genuinely mid-turn (only claimDriving's no-op saves it from a
// second concurrent Drive loop). Derived from turnTimeout plus margin
// rather than a bare constant so that invariant can't silently drift out
// of sync if turnTimeout ever changes.
var staleWorkingAfter = turnTimeout + 5*time.Minute

// recoverWorkingRetries and recoverWorkingRetryDelay bound the boot
// recovery pass's tolerance for Postgres not yet accepting connections
// (brain can start before its DB dependency is ready) — without a
// retry here, a single transient connect failure strands every
// mission left status='working' with no other path back to Drive.
const (
	recoverWorkingRetries    = 5
	recoverWorkingRetryDelay = 2 * time.Second
)

// sandboxSweeper is the narrow slice of the sandbox backend the
// periodic orphan pass needs — kept as an interface (not an import of
// sandboxd or sandboxclient) for the same reason as Driver's
// sandboxRemover: no compile-time dependency on Docker from this
// package.
type sandboxSweeper interface {
	Sweep(ctx context.Context, isTerminal func(missionID string) bool) error
}

// capacityChecker is the narrow slice of sandboxclient.Client's
// admission gate (D-056) the sweep needs — kept as an interface for the
// same reason sandboxSweeper is: no compile-time dependency on Docker
// from this package.
type capacityChecker interface {
	Capacity(ctx context.Context) (admit bool, reason string, err error)
}

// messageNotifier is the narrow slice of Notifier autoResumeBackoff
// needs to warn about a mission it's given up auto-resuming — kept as
// an interface for the same reason sandboxSweeper/capacityChecker are:
// no unnecessary coupling to notify.go's concrete type from here.
type messageNotifier interface {
	NotifyMessage(ctx context.Context, missionID, kind, message string) error
}

// backoffStore is the narrow slice of *Store autoResumeBackoff needs —
// an interface so its ladder logic is unit-testable against a faked
// store, the same reasoning as driverStore in driver.go.
type backoffStore interface {
	BackoffPaused(ctx context.Context) ([]BackoffPausedMission, error)
	CountBackoffPauses(ctx context.Context, missionID string) (int, error)
}

// pausedByReasonStore is the narrow slice of *Store autoResumeInfra
// needs — the reason-parameterized counterpart of backoffStore, kept
// separate rather than folded into it since autoResumeBackoff's own
// existing tests fake backoffStore's fixed-reason shape.
type pausedByReasonStore interface {
	PausedByReason(ctx context.Context, reason string) ([]BackoffPausedMission, error)
	CountPausesByReason(ctx context.Context, missionID, reason string) (int, error)
}

// signaler is the narrow slice of *Driver autoResumeBackoff needs to
// resume a mission — an interface so the ladder logic is testable
// against a fake capturing Signal calls without a real Store/Runner.
type signaler interface {
	Signal(ctx context.Context, id string, input Input) error
}

// autoResumeBackoffDelays ladders how long a backoff-paused mission
// waits (since its last pause) before autoResumeBackoff resumes it
// again, indexed by prior-backoff-pause count (1st, 2nd, 3rd). A
// mission that has paused for backoff 4 or more times has a persistent
// problem, not a transient outage, and needs a human — see
// autoResumeBackoff. This is the post-pause ladder: it paces resuming a
// mission the sweep finds already stopped, minutes apart. Its
// counterpart is driver.go's workerFailedRetryDelays, which paces
// retries WITHIN a live Drive loop, seconds apart, before a pause ever
// happens — see that ladder's own comment for the split's rationale.
var autoResumeBackoffDelays = [...]time.Duration{5 * time.Minute, 15 * time.Minute, 60 * time.Minute}

// autoResumeExhaustedAfter is the prior-backoff-pause count at and
// above which autoResumeBackoff stops resuming a mission and instead
// notifies once and leaves it paused for a human.
const autoResumeExhaustedAfter = 4

// autoResumeInfraDelays ladders how long an infra-paused mission waits
// before autoResumeInfra resumes it again, indexed by prior-infra-pause
// count (1st, 2nd, 3rd) — its own, shorter ladder than
// autoResumeBackoffDelays: infra failures (a gateway blip, a docker
// hiccup) are usually transient and worth retrying sooner, while a
// genuinely permanent one (a missing image) re-pauses immediately and
// hits autoResumeInfraExhaustedAfter fast regardless of how short the
// ladder is.
var autoResumeInfraDelays = [...]time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

// autoResumeInfraExhaustedAfter is the prior-infra-pause count at and
// above which autoResumeInfra stops resuming a mission and instead
// notifies once and leaves it paused for a human — deliberately lower
// than autoResumeExhaustedAfter's 4: a repeatedly failing infra
// dependency needs a human sooner than a repeatedly failing model call
// does.
const autoResumeInfraExhaustedAfter = 3

// admitWork reports whether the host can afford claiming another work
// slot right now (D-056). A nil gate always admits (tests, and any
// sandbox-less setup that never wired sandboxclient in). A gate that
// itself errors also admits, logged at WARN: a dead sandboxd must not
// freeze the mission queue — a mission that actually needs the sandbox
// will fail loudly at exec time anyway, so failing this check open costs
// nothing a live gate wouldn't also risk.
func admitWork(ctx context.Context, gate capacityChecker, log *slog.Logger) (admit bool, reason string) {
	if gate == nil {
		return true, ""
	}
	admit, reason, err := gate.Capacity(ctx)
	if err != nil {
		log.Warn("work slot sweep: capacity check failed, admitting open", "error", err)
		return true, ""
	}
	return admit, reason
}

// RecoverAndSweep runs the boot-time recovery pass once (re-Drives any
// mission left status='working' from a prior process's crash), then
// runs the periodic work-slot sweep until ctx is done — which now also
// sweeps orphaned sandbox containers on the same tick (see
// runWorkSlotSweep). This is the one entry point cmd/brain/main.go
// needs — it owns the Driver, which carries its own Store reference.
func RecoverAndSweep(ctx context.Context, d *Driver, store *Store, maxConcurrent int, sandbox sandboxSweeper, capacity capacityChecker, notify messageNotifier, log *slog.Logger) {
	recoverWorking(ctx, d, store, log)
	runWorkSlotSweep(ctx, d, store, maxConcurrent, sandbox, capacity, notify, log)
}

// sweepOrphanSandboxes runs on every runWorkSlotSweep tick (previously
// boot-only — fixes a pre-existing race where a straggler exec racing
// a mission's own terminal-transition Remove could recreate a
// container that then lived until the next brain restart; a 30s-later
// retry now cleans it up instead). A mission is terminal (safe to
// remove its container) if it doesn't exist at all (deleted) or its
// Phase reports Terminal(); everything else — including phases this
// process doesn't recognize — is left alone, since removing a live
// mission's container out from under it is far worse than leaving an
// orphan a few seconds longer.
func sweepOrphanSandboxes(ctx context.Context, store *Store, sandbox sandboxSweeper, log *slog.Logger) {
	if sandbox == nil {
		return
	}
	isTerminal := func(missionID string) bool {
		m, err := store.Get(ctx, missionID)
		if err != nil {
			return true // gone from the store entirely — safe to remove
		}
		return m.Phase.Terminal()
	}
	if err := sandbox.Sweep(ctx, isTerminal); err != nil {
		log.Error("sandbox sweep: failed", "error", err)
	}
}

// recoverWorking runs once at service boot: every mission Store
// reports via RecoverWorking (status='working' at process start,
// meaning the prior process died mid-Advance) gets re-Driven — this is
// what makes driveTimeBound and any hard crash NOT a dead end.
func recoverWorking(ctx context.Context, d *Driver, store *Store, log *slog.Logger) {
	var missions []Mission
	var err error
	for attempt := 1; attempt <= recoverWorkingRetries; attempt++ {
		missions, err = store.RecoverWorking(ctx)
		if err == nil {
			break
		}
		log.Error("mission recovery sweep: list working missions", "attempt", attempt, "error", err)
		if attempt < recoverWorkingRetries {
			select {
			case <-ctx.Done():
				return
			case <-time.After(recoverWorkingRetryDelay):
			}
		}
	}
	if err != nil {
		log.Error("mission recovery sweep: giving up after retries", "attempts", recoverWorkingRetries, "error", err)
		return
	}
	for _, m := range missions {
		log.Info("mission recovery: re-driving a mission left working at boot", "mission_id", m.ID)
		go func(id string) {
			if err := d.Drive(ctx, id); err != nil {
				log.Error("mission recovery: drive failed", "mission_id", id, "error", err)
			}
		}(m.ID)
	}
}

// runWorkSlotSweep retries missions parked idle over the work-slot cap
// every workSlotSweepInterval via ClaimWorkSlot, kicking off a Drive
// for whichever gets claimed, sweeps orphaned sandbox containers, and
// re-Drives any 'working' mission stale past staleWorkingAfter — all on
// the same tick. Runs until ctx is done — this call blocks, so
// RecoverAndSweep's caller runs it in its own goroutine.
func runWorkSlotSweep(ctx context.Context, d *Driver, store *Store, maxConcurrent int, sandbox sandboxSweeper, capacity capacityChecker, notify messageNotifier, log *slog.Logger) {
	ticker := time.NewTicker(workSlotSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepOrphanSandboxes(ctx, store, sandbox, log)
			reDriveStaleWorking(ctx, d, store, log)
			autoResumeBackoff(ctx, d, store, notify, log)
			autoResumeInfra(ctx, d, store, notify, log)
			// D-056: skip the claim entirely this tick if the host can't
			// afford another working mission — the mission stays idle,
			// and this same sweep retries it in workSlotSweepInterval; that
			// retry IS the queue, no separate parking state needed.
			if admit, reason := admitWork(ctx, capacity, log); !admit {
				log.Info("work slot sweep: capacity denied, leaving missions idle", "reason", reason)
				continue
			}
			id, ok, err := store.ClaimWorkSlot(ctx, maxConcurrent)
			if err != nil {
				log.Error("work slot sweep: claim failed", "error", err)
				continue
			}
			if !ok {
				continue
			}
			go func(id string) {
				if err := d.Drive(ctx, id); err != nil {
					log.Error("work slot sweep: drive failed", "mission_id", id, "error", err)
				}
			}(id)
		}
	}
}

// reDriveStaleWorking re-Drives any mission RecoverStaleWorking reports.
// Safe to call on a mission that is, in fact, still being actively
// Driven: Driver.claimDriving makes a second concurrent Drive for the
// same id a no-op, so this can never race or duplicate a live turn — it
// only ever rescues a genuinely stopped loop.
func reDriveStaleWorking(ctx context.Context, d *Driver, store *Store, log *slog.Logger) {
	missions, err := store.RecoverStaleWorking(ctx, staleWorkingAfter)
	if err != nil {
		log.Error("stale working sweep: list failed", "error", err)
		return
	}
	for _, m := range missions {
		log.Warn("stale working sweep: re-driving a mission whose Drive loop stopped advancing", "mission_id", m.ID)
		go func(id string) {
			if err := d.Drive(ctx, id); err != nil {
				log.Error("stale working sweep: drive failed", "mission_id", id, "error", err)
			}
		}(m.ID)
	}
}

// autoResumeBackoff self-heals missions paused for backoff (transient
// worker/provider failures, PauseBackoff) without a human hitting
// resume every time (D-065): n = prior backoff-pause count for the
// mission; the ladder in autoResumeBackoffDelays gates how long since
// the pause before resuming, growing with each successive pause so a
// mission that keeps hitting backoff backs off harder each time. At
// autoResumeExhaustedAfter or more prior pauses, the problem is
// probably not transient — stop resuming and notify a human once
// instead; NotifyMessage's per-(mission,kind,unread) dedupe (notify.go)
// makes this safe to call again on every later tick without extra
// state here.
//
// A resumed mission that is still over budget re-pauses immediately
// with pause_reason='budget' (statemachine.go's budget brake runs
// before the input switch), not 'backoff' — it naturally leaves
// BackoffPaused's result set on the very next tick, no special case
// needed here.
func autoResumeBackoff(ctx context.Context, d signaler, store backoffStore, notify messageNotifier, log *slog.Logger) {
	missions, err := store.BackoffPaused(ctx)
	if err != nil {
		log.Error("auto-resume backoff sweep: list failed", "error", err)
		return
	}
	for _, m := range missions {
		n, err := store.CountBackoffPauses(ctx, m.ID)
		if err != nil {
			log.Error("auto-resume backoff sweep: count pauses failed", "mission_id", m.ID, "error", err)
			continue
		}
		if n <= 0 {
			continue // no recorded backoff pause yet — nothing to ladder from
		}
		if n >= autoResumeExhaustedAfter {
			if notify != nil {
				msg := fmt.Sprintf("this mission has paused for backoff %d times and will not auto-resume again — it needs a human look", n)
				if err := notify.NotifyMessage(ctx, m.ID, "auto_resume_exhausted", msg); err != nil {
					log.Warn("auto-resume backoff sweep: notify failed", "mission_id", m.ID, "error", err)
				}
			}
			continue
		}
		idx := n - 1
		if idx >= len(autoResumeBackoffDelays) {
			idx = len(autoResumeBackoffDelays) - 1
		}
		if time.Since(m.UpdatedAt) < autoResumeBackoffDelays[idx] {
			continue // not due yet
		}
		log.Info("auto-resume backoff sweep: resuming a backoff-paused mission", "mission_id", m.ID, "prior_pauses", n)
		if err := d.Signal(ctx, m.ID, InputResume); err != nil {
			log.Error("auto-resume backoff sweep: signal failed", "mission_id", m.ID, "error", err)
		}
	}
}

// autoResumeInfra self-heals missions paused for infra failure
// (PauseInfra: gateway blip, docker hiccup, harness/reviewer/driver
// error) without a human hitting resume every time — same ladder shape
// as autoResumeBackoff, but its own shorter delays
// (autoResumeInfraDelays) and its own lower cap
// (autoResumeInfraExhaustedAfter): infra failures are usually transient
// and worth retrying sooner than a repeatedly-failing model call, while
// a genuinely permanent infra failure (a missing image) re-pauses
// immediately and hits the cap fast regardless.
//
// A resumed mission still hitting the same (or a different) pause
// condition re-pauses with whatever reason applies (statemachine.go's
// checks run before the input switch) — it naturally leaves
// PausedByReason's result set on the very next tick if that reason
// isn't 'infra', no special case needed here.
func autoResumeInfra(ctx context.Context, d signaler, store pausedByReasonStore, notify messageNotifier, log *slog.Logger) {
	missions, err := store.PausedByReason(ctx, string(PauseInfra))
	if err != nil {
		log.Error("auto-resume infra sweep: list failed", "error", err)
		return
	}
	for _, m := range missions {
		n, err := store.CountPausesByReason(ctx, m.ID, string(PauseInfra))
		if err != nil {
			log.Error("auto-resume infra sweep: count pauses failed", "mission_id", m.ID, "error", err)
			continue
		}
		if n <= 0 {
			continue // no recorded infra pause yet — nothing to ladder from
		}
		if n >= autoResumeInfraExhaustedAfter {
			if notify != nil {
				msg := fmt.Sprintf("this mission has paused for infra failure %d times and will not auto-resume again — it needs a human look", n)
				if err := notify.NotifyMessage(ctx, m.ID, "auto_resume_exhausted", msg); err != nil {
					log.Warn("auto-resume infra sweep: notify failed", "mission_id", m.ID, "error", err)
				}
			}
			continue
		}
		idx := n - 1
		if idx >= len(autoResumeInfraDelays) {
			idx = len(autoResumeInfraDelays) - 1
		}
		if time.Since(m.UpdatedAt) < autoResumeInfraDelays[idx] {
			continue // not due yet
		}
		log.Info("auto-resume infra sweep: resuming an infra-paused mission", "mission_id", m.ID, "prior_pauses", n)
		if err := d.Signal(ctx, m.ID, InputResume); err != nil {
			log.Error("auto-resume infra sweep: signal failed", "mission_id", m.ID, "error", err)
		}
	}
}
