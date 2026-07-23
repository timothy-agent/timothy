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

// RecoverAndSweep runs the boot-time recovery pass once (re-Drives any
// mission left status='working' from a prior process's crash), then
// runs the periodic work-slot sweep until ctx is done. This is the one
// entry point cmd/brain/main.go needs — it owns the Driver, which
// carries its own Store reference.
func RecoverAndSweep(ctx context.Context, d *Driver, store *Store, maxConcurrent int, log *slog.Logger) {
	recoverWorking(ctx, d, store, log)
	runWorkSlotSweep(ctx, d, store, maxConcurrent, log)
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
