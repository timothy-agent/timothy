//go:build integration

package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

// testSource marks every row these tests insert.
const testSource = "itest-events"

const testKind = "itest.kind"

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sweepTestRows(ctx, t, dsn)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		sweepTestRows(cctx, t, dsn)
	})
	return NewStore(pool)
}

// sweepTestRows deletes this package's rows and settles any other
// unprocessed row so a drain here only sees the test's own events.
func sweepTestRows(ctx context.Context, t *testing.T, dsn string) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Logf("sweep skipped: %v", err)
		return
	}
	defer func() { _ = conn.Close(ctx) }()
	_, _ = conn.Exec(ctx, `DELETE FROM events WHERE source = $1`, testSource)
	_, _ = conn.Exec(ctx, `DROP TABLE IF EXISTS itest_events_scratch`)
}

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func insertEvents(t *testing.T, s *Store, keys ...string) []int64 {
	t.Helper()
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, k := range keys {
		if err := s.Insert(ctx, tx, Event{Source: testSource, Kind: testKind, DedupKey: k, Payload: []byte(`{"k":"` + k + `"}`)}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	var ids []int64
	for _, k := range keys {
		var id int64
		if err := db.QueryRow(ctx, `SELECT id FROM events WHERE source = $1 AND dedup_key = $2`, testSource, k).Scan(&id); err != nil {
			t.Fatalf("lookup %s: %v", k, err)
		}
		ids = append(ids, id)
	}
	return ids
}

type eventRow struct {
	processed bool
	attempts  int
	lastError *string
}

func readEvent(t *testing.T, s *Store, id int64) eventRow {
	t.Helper()
	db, _ := s.db.Get()
	var r eventRow
	if err := db.QueryRow(t.Context(), `SELECT processed_at IS NOT NULL, attempts, last_error FROM events WHERE id = $1`, id).
		Scan(&r.processed, &r.attempts, &r.lastError); err != nil {
		t.Fatalf("read event %d: %v", id, err)
	}
	return r
}

// recorder is a consumer that records the event ids it saw and fails
// for the dedup keys in fail.
type recorder struct {
	mu   sync.Mutex
	seen []int64
	fail map[string]bool
}

func (r *recorder) Name() string    { return "recorder" }
func (r *recorder) Kinds() []string { return []string{testKind} }
func (r *recorder) Handle(ctx context.Context, tx pgx.Tx, ev Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, ev.ID)
	if r.fail[ev.DedupKey] {
		return errors.New("permanent failure")
	}
	return nil
}

func TestDrainDeadLettersPermanentFailureAndKeepsOrder(t *testing.T) {
	s := testStore(t)
	ids := insertEvents(t, s, "one", "two", "three")
	rec := &recorder{fail: map[string]bool{"two": true}}
	d := NewDrainer(s, []Consumer{rec}, nil, testLog())

	for i := range maxAttempts {
		if _, err := d.Drain(t.Context()); err != nil {
			t.Fatalf("drain %d: %v", i+1, err)
		}
	}

	for _, id := range []int64{ids[0], ids[2]} {
		r := readEvent(t, s, id)
		if !r.processed || r.attempts != 1 || r.lastError != nil {
			t.Fatalf("event %d = %+v, want processed once with no error", id, r)
		}
	}
	r := readEvent(t, s, ids[1])
	if !r.processed || r.attempts != maxAttempts || r.lastError == nil || *r.lastError == "" {
		t.Fatalf("event %d = %+v, want dead-lettered after %d attempts", ids[1], r, maxAttempts)
	}

	var ours []int64
	for _, id := range rec.seen {
		if slices.Contains(ids, id) {
			ours = append(ours, id)
		}
	}
	if len(ours) < 3 || ours[0] != ids[0] || ours[1] != ids[1] || ours[2] != ids[2] {
		t.Fatalf("first drain order = %v, want %v first", ours, ids)
	}

	// A sixth drain never claims the dead-lettered event again.
	before := len(rec.seen)
	if _, err := d.Drain(t.Context()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	for _, id := range rec.seen[before:] {
		if slices.Contains(ids, id) {
			t.Fatalf("event %d delivered after dead-letter", id)
		}
	}
}

func TestInsertDedupsBySourceAndKey(t *testing.T) {
	s := testStore(t)
	insertEvents(t, s, "dup")
	insertEvents(t, s, "dup")
	db, _ := s.db.Get()
	var n int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE source = $1 AND dedup_key = 'dup'`, testSource).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows for duplicate key = %d, want 1", n)
	}
}

// scratchWriter writes through the drain tx, then fails.
type scratchWriter struct{}

func (scratchWriter) Name() string    { return "scratch" }
func (scratchWriter) Kinds() []string { return []string{testKind} }
func (scratchWriter) Handle(ctx context.Context, tx pgx.Tx, ev Event) error {
	if _, err := tx.Exec(ctx, `INSERT INTO itest_events_scratch (event_id) VALUES ($1)`, ev.ID); err != nil {
		return err
	}
	return errors.New("fail after write")
}

// okWriter writes through the drain tx and succeeds.
type okWriter struct{}

func (okWriter) Name() string    { return "ok-writer" }
func (okWriter) Kinds() []string { return []string{testKind} }
func (okWriter) Handle(ctx context.Context, tx pgx.Tx, ev Event) error {
	_, err := tx.Exec(ctx, `INSERT INTO itest_events_scratch (event_id) VALUES ($1)`, ev.ID)
	return err
}

func TestDrainSavepointRollsBackFailedConsumerWrites(t *testing.T) {
	s := testStore(t)
	db, _ := s.db.Get()
	if _, err := db.Exec(t.Context(), `CREATE TABLE itest_events_scratch (event_id bigint NOT NULL)`); err != nil {
		t.Fatalf("create scratch: %v", err)
	}
	failing := insertEvents(t, s, "savepoint-fail")[0]

	if _, err := NewDrainer(s, []Consumer{scratchWriter{}}, nil, testLog()).Drain(t.Context()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	var n int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM itest_events_scratch WHERE event_id = $1`, failing).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("scratch rows after failed consumer = %d, want 0", n)
	}
	if r := readEvent(t, s, failing); r.processed || r.attempts != 1 || r.lastError == nil {
		t.Fatalf("failed event = %+v, want one unprocessed attempt with an error", r)
	}

	// A later event in the same kind whose consumer succeeds keeps its
	// write: the failure above did not abort the transaction.
	if _, err := db.Exec(t.Context(), `UPDATE events SET processed_at = now() WHERE id = $1`, failing); err != nil {
		t.Fatalf("settle: %v", err)
	}
	ok := insertEvents(t, s, "savepoint-ok")[0]
	if _, err := NewDrainer(s, []Consumer{okWriter{}}, nil, testLog()).Drain(t.Context()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM itest_events_scratch WHERE event_id = $1`, ok).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("scratch rows after successful consumer = %d, want 1", n)
	}
}

func TestDrainFailureDoesNotAbortBatch(t *testing.T) {
	s := testStore(t)
	db, _ := s.db.Get()
	if _, err := db.Exec(t.Context(), `CREATE TABLE itest_events_scratch (event_id bigint NOT NULL)`); err != nil {
		t.Fatalf("create scratch: %v", err)
	}
	ids := insertEvents(t, s, "batch-a", "batch-b")
	// Failing SQL inside the consumer leaves the tx aborted until the
	// savepoint rollback; the next event must still be delivered.
	bad := consumerFunc(func(ctx context.Context, tx pgx.Tx, ev Event) error {
		if ev.DedupKey == "batch-a" {
			_, err := tx.Exec(ctx, `INSERT INTO itest_events_scratch (missing_column) VALUES (1)`)
			return err
		}
		return nil
	})
	if _, err := NewDrainer(s, []Consumer{bad}, nil, testLog()).Drain(t.Context()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if r := readEvent(t, s, ids[0]); r.processed || r.lastError == nil {
		t.Fatalf("event a = %+v, want failed attempt", r)
	}
	if r := readEvent(t, s, ids[1]); !r.processed || r.lastError != nil {
		t.Fatalf("event b = %+v, want processed", r)
	}
}

type consumerFunc func(ctx context.Context, tx pgx.Tx, ev Event) error

func (consumerFunc) Name() string    { return "func" }
func (consumerFunc) Kinds() []string { return []string{testKind} }
func (f consumerFunc) Handle(ctx context.Context, tx pgx.Tx, ev Event) error {
	return f(ctx, tx, ev)
}

func TestDrainPanicIsRecordedAsError(t *testing.T) {
	s := testStore(t)
	id := insertEvents(t, s, "panic")[0]
	p := consumerFunc(func(ctx context.Context, tx pgx.Tx, ev Event) error {
		if ev.DedupKey == "panic" {
			panic("consumer exploded")
		}
		return nil
	})
	if _, err := NewDrainer(s, []Consumer{p}, nil, testLog()).Drain(t.Context()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	r := readEvent(t, s, id)
	if r.processed || r.attempts != 1 || r.lastError == nil {
		t.Fatalf("event = %+v, want one failed attempt", r)
	}
}

func TestDrainSkipsWhileAnotherDrainerHoldsTheLock(t *testing.T) {
	s := testStore(t)
	id := insertEvents(t, s, "locked")[0]
	conn, err := pgx.Connect(t.Context(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	holder, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := holder.Exec(t.Context(), `SELECT pg_advisory_xact_lock($1)`, int64(drainLockKey)); err != nil {
		t.Fatalf("lock: %v", err)
	}

	rec := &recorder{}
	n, err := NewDrainer(s, []Consumer{rec}, nil, testLog()).Drain(t.Context())
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if n != 0 || len(rec.seen) != 0 {
		t.Fatalf("locked drain handled %d events (%v), want 0", n, rec.seen)
	}
	if r := readEvent(t, s, id); r.processed || r.attempts != 0 {
		t.Fatalf("event = %+v, want untouched", r)
	}

	if err := holder.Rollback(t.Context()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := NewDrainer(s, []Consumer{rec}, nil, testLog()).Drain(t.Context()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if r := readEvent(t, s, id); !r.processed {
		t.Fatalf("event = %+v, want processed once the lock is free", r)
	}
}

func TestSweepDeletesOnlyOldProcessedEvents(t *testing.T) {
	s := testStore(t)
	ids := insertEvents(t, s, "old", "recent", "pending", "old-pending")
	db, _ := s.db.Get()
	ctx := t.Context()
	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = now() - interval '31 days' WHERE id = $1`, ids[0]); err != nil {
		t.Fatalf("age: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE events SET processed_at = now() - interval '29 days' WHERE id = $1`, ids[1]); err != nil {
		t.Fatalf("age: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE events SET created_at = now() - interval '60 days' WHERE id = $1`, ids[3]); err != nil {
		t.Fatalf("age: %v", err)
	}

	if _, err := s.Sweep(ctx, retentionDays); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for i, want := range []bool{false, true, true, true} {
		var exists bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE id = $1)`, ids[i]).Scan(&exists); err != nil {
			t.Fatalf("exists: %v", err)
		}
		if exists != want {
			t.Fatalf("event %s exists = %v, want %v", strconv.FormatInt(ids[i], 10), exists, want)
		}
	}
}

func TestRunDrainsAtStartup(t *testing.T) {
	s := testStore(t)
	id := insertEvents(t, s, "boot")[0]
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		NewDrainer(s, []Consumer{&recorder{}}, nil, testLog()).Run(ctx)
		close(done)
	}()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(drainTick / 2)
	for time.Now().Before(deadline) {
		if readEvent(t, s, id).processed {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("event not processed within %s of Run starting, want the boot drain to handle it", drainTick/2)
}
