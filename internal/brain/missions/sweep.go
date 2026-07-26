package missions

import (
	"context"
	"log/slog"
	"time"
)

// workSlotSweepInterval is how often the idle-over-cap sweep retries
// claiming a work slot for missions parked idle because the
// concurrency cap was full when they last tried.
const workSlotSweepInterval = 30 * time.Second

// recoverWorkingRetries and recoverWorkingRetryDelay bound the boot
// recovery pass's tolerance for Postgres not yet accepting connections
// (brain can start before its DB dependency is ready) — without a
// retry here, a single transient connect failure strands every
// mission left status='working' with no other path back to Drive.
const (
	recoverWorkingRetries    = 5
	recoverWorkingRetryDelay = 2 * time.Second
)

// sandboxSweeper is the narrow slice of *sandbox.Manager the boot-time
// orphan pass needs — kept as an interface (not an import of the
// sandbox package) for the same reason as Driver's sandboxRemover: no
// compile-time dependency on Docker from this package.
type sandboxSweeper interface {
	Sweep(ctx context.Context, isTerminal func(missionID string) bool) error
}

// RecoverAndSweep runs the boot-time recovery pass once (re-Drives any
// mission left status='working' from a prior process's crash), sweeps
// any sandbox container whose mission is terminal or unknown (the
// day-to-day path is Driver removing its own mission's container at
// its terminal transition; this is the backstop for missions that
// terminated, or were deleted, while brain was down), then runs the
// periodic work-slot sweep until ctx is done. This is the one entry
// point cmd/brain/main.go needs — it owns the Driver, which carries
// its own Store reference. sandbox may be nil (MISSION_SANDBOX_IMAGE
// unset), in which case the container sweep is skipped entirely.
func RecoverAndSweep(ctx context.Context, d *Driver, store *Store, maxConcurrent int, sandbox sandboxSweeper, log *slog.Logger) {
	recoverWorking(ctx, d, store, log)
	sweepOrphanSandboxes(ctx, store, sandbox, log)
	runWorkSlotSweep(ctx, d, store, maxConcurrent, log)
}

// sweepOrphanSandboxes runs once at boot. A mission is terminal (safe
// to remove its container) if it doesn't exist at all (deleted) or its
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
// for whichever gets claimed. Runs until ctx is done — this call
// blocks, so RecoverAndSweep's caller runs it in its own goroutine.
func runWorkSlotSweep(ctx context.Context, d *Driver, store *Store, maxConcurrent int, log *slog.Logger) {
	ticker := time.NewTicker(workSlotSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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
