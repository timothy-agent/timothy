package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// drainTick is how often the drainer polls without a Kick.
	drainTick = 2 * time.Second
	// sweepEvery is how often processed events past retention are deleted.
	sweepEvery = time.Hour
	// retentionDays keeps processed events this long before the sweep.
	retentionDays = 30
	// batchSize caps the events one drain claims.
	batchSize = 100
	// maxAttempts dead-letters an event after this many failed deliveries.
	maxAttempts = 5
	// consumerTimeout bounds one consumer's Handle call.
	consumerTimeout = 5 * time.Minute
	// drainIdleTimeout replaces pgpool's 60s idle-in-transaction timeout
	// for the drain tx, which stays idle while a consumer runs.
	drainIdleTimeout = consumerTimeout + time.Minute
)

// drainLockKey is this package's advisory lock key ("EVNT"), distinct
// from migrate's "TIMO" and missions' "TIMS".
const drainLockKey = 0x45564E54

// Drainer delivers unprocessed events to their consumers (D-117).
type Drainer struct {
	store     *Store
	consumers []Consumer
	processed *prometheus.CounterVec // labels kind, result; nil skips
	kick      chan struct{}
	log       *slog.Logger
}

// NewDrainer wires consumers; processed may be nil.
func NewDrainer(store *Store, consumers []Consumer, processed *prometheus.CounterVec, log *slog.Logger) *Drainer {
	return &Drainer{store: store, consumers: consumers, processed: processed, kick: make(chan struct{}, 1), log: log}
}

// Kick asks for a drain now instead of at the next tick. Never blocks.
func (d *Drainer) Kick() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// Run drains once at start (events a crash left behind), then on every
// tick or Kick, and sweeps hourly, until ctx is done.
func (d *Drainer) Run(ctx context.Context) {
	ticker := time.NewTicker(drainTick)
	defer ticker.Stop()
	sweep := time.NewTicker(sweepEvery)
	defer sweep.Stop()
	d.drainLogged(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.drainLogged(ctx)
		case <-d.kick:
			d.drainLogged(ctx)
		case <-sweep.C:
			if n, err := d.store.Sweep(ctx, retentionDays); err != nil {
				d.log.Error("events: sweep failed", "error", err)
			} else if n > 0 {
				d.log.Info("events: swept processed events", "deleted", n)
			}
		}
	}
}

func (d *Drainer) drainLogged(ctx context.Context) {
	if _, err := d.Drain(ctx); err != nil && ctx.Err() == nil {
		d.log.Error("events: drain failed", "error", err)
	}
}

// Drain processes one batch and reports how many events it handled.
// A consumer error never aborts the batch: the event's savepoint rolls
// back and its attempt is recorded.
func (d *Drainer) Drain(ctx context.Context) (int, error) {
	db, err := d.store.db.Get()
	if err != nil {
		return 0, fmt.Errorf("events drain: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("events drain: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// Transaction-scoped: the pooled connection keeps its default after.
	if _, err := tx.Exec(ctx, `SELECT set_config('idle_in_transaction_session_timeout', $1, true)`,
		strconv.FormatInt(drainIdleTimeout.Milliseconds(), 10)); err != nil {
		return 0, fmt.Errorf("events drain: idle timeout: %w", err)
	}

	var acquired bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, int64(drainLockKey)).Scan(&acquired); err != nil {
		return 0, fmt.Errorf("events drain: advisory lock: %w", err)
	}
	if !acquired {
		return 0, tx.Commit(ctx) // another instance is draining
	}
	batch, err := claimBatch(ctx, tx, batchSize)
	if err != nil {
		return 0, err
	}
	for _, ev := range batch {
		if err := d.deliver(ctx, tx, ev); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("events drain: commit: %w", err)
	}
	return len(batch), nil
}

// deliver runs ev's consumers inside a savepoint and records the
// outcome. Only bookkeeping failures are returned.
func (d *Drainer) deliver(ctx context.Context, tx pgx.Tx, ev Event) error {
	if _, err := tx.Exec(ctx, `SAVEPOINT ev`); err != nil {
		return fmt.Errorf("events drain: savepoint: %w", err)
	}
	var errs []error
	for _, c := range consumersFor(d.consumers, ev.Kind) {
		if err := handleSafely(ctx, c, tx, ev); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Name(), err))
		}
	}
	handleErr := errors.Join(errs...)
	processed, deadLetter := outcome(ev.Attempts, handleErr)
	if handleErr == nil {
		if err := markProcessed(ctx, tx, ev.ID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT ev`); err != nil {
			return fmt.Errorf("events drain: rollback to savepoint: %w", err)
		}
		if err := markFailed(ctx, tx, ev.ID, handleErr.Error(), deadLetter); err != nil {
			return err
		}
		if deadLetter {
			d.log.Warn("events: event dead-lettered", "event_id", ev.ID, "kind", ev.Kind, "attempts", ev.Attempts+1, "error", handleErr)
		} else {
			d.log.Warn("events: consumer failed, will retry", "event_id", ev.ID, "kind", ev.Kind, "attempts", ev.Attempts+1, "error", handleErr)
		}
	}
	if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT ev`); err != nil {
		return fmt.Errorf("events drain: release savepoint: %w", err)
	}
	if d.processed != nil {
		d.processed.WithLabelValues(ev.Kind, resultLabel(processed, deadLetter)).Inc()
	}
	return nil
}

// consumersFor returns the consumers subscribed to kind, in
// registration order.
func consumersFor(consumers []Consumer, kind string) []Consumer {
	var out []Consumer
	for _, c := range consumers {
		if slices.Contains(c.Kinds(), kind) {
			out = append(out, c)
		}
	}
	return out
}

// outcome decides an event's state after a delivery attempt, given its
// attempts count before this one: processed is true on success or once
// the attempt cap is reached (deadLetter).
func outcome(attempts int, err error) (processed, deadLetter bool) {
	if err == nil {
		return true, false
	}
	if attempts+1 >= maxAttempts {
		return true, true
	}
	return false, false
}

// resultLabel is the events_processed_total result label.
func resultLabel(processed, deadLetter bool) string {
	switch {
	case deadLetter:
		return "dead_letter"
	case processed:
		return "ok"
	default:
		return "retry"
	}
}

// handleSafely calls c.Handle under consumerTimeout, turning a panic
// into an error.
func handleSafely(ctx context.Context, c Consumer, tx pgx.Tx, ev Event) (err error) {
	cctx, cancel := context.WithTimeout(ctx, consumerTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return c.Handle(cctx, tx, ev)
}
