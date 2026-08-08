package missions

import (
	"context"
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
// turn before the periodic sweep re-Drives it — well above any observed
// legit single-turn duration (the slowest logged this project: ~9.4min,
// a remote-model turn), so this never fires on a mission genuinely
// mid-turn, only one whose Drive loop has actually stopped.
const staleWorkingAfter = 15 * time.Minute

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
func RecoverAndSweep(ctx context.Context, d *Driver, store *Store, maxConcurrent int, sandbox sandboxSweeper, capacity capacityChecker, log *slog.Logger) {
	recoverWorking(ctx, d, store, log)
	runWorkSlotSweep(ctx, d, store, maxConcurrent, sandbox, capacity, log)
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
func runWorkSlotSweep(ctx context.Context, d *Driver, store *Store, maxConcurrent int, sandbox sandboxSweeper, capacity capacityChecker, log *slog.Logger) {
	ticker := time.NewTicker(workSlotSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepOrphanSandboxes(ctx, store, sandbox, log)
			reDriveStaleWorking(ctx, d, store, log)
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
