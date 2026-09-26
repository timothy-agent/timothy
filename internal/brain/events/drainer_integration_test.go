//go:build integration

package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
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
	return NewStore(pool)
}

// cleanupExec runs sql on a fresh connection when the test ends; the
// store's pool dies with t.Context before cleanups run.
func cleanupExec(t *testing.T, sql string, args ...any) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Errorf("cleanup %q: %v", sql, err)
		}
	})
}

// testSource returns a source unique to this test and deletes its rows
// when the test ends.
func testSource(t *testing.T) string {
	t.Helper()
	src := fmt.Sprintf("itest-%s-%x", t.Name(), rand.Uint64())
	cleanupExec(t, `DELETE FROM events WHERE source = $1`, src)
	return src
}

// scratchTable creates a table unique to this test and drops it when
// the test ends.
func scratchTable(t *testing.T, s *Store) string {
	t.Helper()
	name := fmt.Sprintf("itest_events_scratch_%x", rand.Uint64())
	db, _ := s.db.Get()
	if _, err := db.Exec(t.Context(), `CREATE TABLE `+name+` (event_id bigint NOT NULL)`); err != nil {
		t.Fatalf("create scratch: %v", err)
	}
	cleanupExec(t, `DROP TABLE IF EXISTS `+name)
	return name
}

// guardOthers row-locks every unprocessed event outside src until the
// returned release runs, so a drain here skips rows it does not own.
func guardOthers(t *testing.T, s *Store, src string) (release func()) {
	t.Helper()
	db, _ := s.db.Get()
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatalf("guard begin: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT id FROM events WHERE processed_at IS NULL AND source <> $1 FOR UPDATE SKIP LOCKED`, src); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("guard lock: %v", err)
	}
	return func() { _ = tx.Rollback(context.Background()) }
}

// drain runs one d.Drain touching only src's rows.
func drain(t *testing.T, s *Store, src string, d *Drainer) (int, error) {
	t.Helper()
	release := guardOthers(t, s, src)
	defer release()
	return d.Drain(t.Context())
}

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func insertEvents(t *testing.T, s *Store, src string, keys ...string) []int64 {
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
		if err := s.Insert(ctx, tx, Event{Source: src, Kind: testKind, DedupKey: k, Payload: []byte(`{"k":"` + k + `"}`)}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	var ids []int64
	for _, k := range keys {
		var id int64
		if err := db.QueryRow(ctx, `SELECT id FROM events WHERE source = $1 AND dedup_key = $2`, src, k).Scan(&id); err != nil {
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
	src := testSource(t)
	ids := insertEvents(t, s, src, "one", "two", "three")
	rec := &recorder{fail: map[string]bool{"two": true}}
	d := NewDrainer(s, []Consumer{rec}, nil, testLog())

	for i := range maxAttempts {
		if _, err := drain(t, s, src, d); err != nil {
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
	if _, err := drain(t, s, src, d); err != nil {
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
	src := testSource(t)
	insertEvents(t, s, src, "dup")
	insertEvents(t, s, src, "dup")
	db, _ := s.db.Get()
	var n int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE source = $1 AND dedup_key = 'dup'`, src).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows for duplicate key = %d, want 1", n)
	}
}

func TestAddIfNewReportsInsertOnce(t *testing.T) {
	s := testStore(t)
	src := testSource(t)
	ev := Event{Source: src, Kind: testKind, DedupKey: "once", Payload: json.RawMessage(`{}`)}
	id, inserted, err := s.AddIfNew(t.Context(), ev)
	if err != nil || !inserted || id == 0 {
		t.Fatalf("first AddIfNew = (%d, %v, %v), want a new id", id, inserted, err)
	}
	id2, inserted, err := s.AddIfNew(t.Context(), ev)
	if err != nil || inserted || id2 != 0 {
		t.Fatalf("second AddIfNew = (%d, %v, %v), want (0, false, nil)", id2, inserted, err)
	}
}

// scratchWriter writes to table through the drain tx, then fails.
type scratchWriter struct{ table string }

func (scratchWriter) Name() string    { return "scratch" }
func (scratchWriter) Kinds() []string { return []string{testKind} }
func (w scratchWriter) Handle(ctx context.Context, tx pgx.Tx, ev Event) error {
	if _, err := tx.Exec(ctx, `INSERT INTO `+w.table+` (event_id) VALUES ($1)`, ev.ID); err != nil {
		return err
	}
	return errors.New("fail after write")
}

// okWriter writes to table through the drain tx and succeeds.
type okWriter struct{ table string }

func (okWriter) Name() string    { return "ok-writer" }
func (okWriter) Kinds() []string { return []string{testKind} }
func (w okWriter) Handle(ctx context.Context, tx pgx.Tx, ev Event) error {
	_, err := tx.Exec(ctx, `INSERT INTO `+w.table+` (event_id) VALUES ($1)`, ev.ID)
	return err
}

func TestDrainSavepointRollsBackFailedConsumerWrites(t *testing.T) {
	s := testStore(t)
	src := testSource(t)
	table := scratchTable(t, s)
	db, _ := s.db.Get()
	failing := insertEvents(t, s, src, "savepoint-fail")[0]

	if _, err := drain(t, s, src, NewDrainer(s, []Consumer{scratchWriter{table}}, nil, testLog())); err != nil {
		t.Fatalf("drain: %v", err)
	}
	var n int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE event_id = $1`, failing).Scan(&n); err != nil {
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
	ok := insertEvents(t, s, src, "savepoint-ok")[0]
	if _, err := drain(t, s, src, NewDrainer(s, []Consumer{okWriter{table}}, nil, testLog())); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE event_id = $1`, ok).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("scratch rows after successful consumer = %d, want 1", n)
	}
}

func TestDrainFailureDoesNotAbortBatch(t *testing.T) {
	s := testStore(t)
	src := testSource(t)
	table := scratchTable(t, s)
	ids := insertEvents(t, s, src, "batch-a", "batch-b")
	// Failing SQL inside the consumer leaves the tx aborted until the
	// savepoint rollback; the next event must still be delivered.
	bad := consumerFunc(func(ctx context.Context, tx pgx.Tx, ev Event) error {
		if ev.DedupKey == "batch-a" {
			_, err := tx.Exec(ctx, `INSERT INTO `+table+` (missing_column) VALUES (1)`)
			return err
		}
		return nil
	})
	if _, err := drain(t, s, src, NewDrainer(s, []Consumer{bad}, nil, testLog())); err != nil {
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
	src := testSource(t)
	id := insertEvents(t, s, src, "panic")[0]
	p := consumerFunc(func(ctx context.Context, tx pgx.Tx, ev Event) error {
		if ev.DedupKey == "panic" {
			panic("consumer exploded")
		}
		return nil
	})
	if _, err := drain(t, s, src, NewDrainer(s, []Consumer{p}, nil, testLog())); err != nil {
		t.Fatalf("drain: %v", err)
	}
	r := readEvent(t, s, id)
	if r.processed || r.attempts != 1 || r.lastError == nil {
		t.Fatalf("event = %+v, want one failed attempt", r)
	}
}

func TestDrainSkipsWhileAnotherDrainerHoldsTheLock(t *testing.T) {
	s := testStore(t)
	src := testSource(t)
	id := insertEvents(t, s, src, "locked")[0]
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
	n, err := drain(t, s, src, NewDrainer(s, []Consumer{rec}, nil, testLog()))
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
	if _, err := drain(t, s, src, NewDrainer(s, []Consumer{rec}, nil, testLog())); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if r := readEvent(t, s, id); !r.processed {
		t.Fatalf("event = %+v, want processed once the lock is free", r)
	}
}

func TestHoldDrainLockFencesOtherDrainers(t *testing.T) {
	s := testStore(t)
	src := testSource(t)
	conn, err := pgx.Connect(t.Context(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if err := HoldDrainLock(t.Context(), conn); err != nil {
		t.Fatalf("HoldDrainLock: %v", err)
	}
	// Inserted after the lock: no drainer anywhere can have claimed it.
	id := insertEvents(t, s, src, "fenced")[0]

	rec := &recorder{}
	n, err := drain(t, s, src, NewDrainer(s, []Consumer{rec}, nil, testLog()))
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if n != 0 || len(rec.seen) != 0 {
		t.Fatalf("fenced drain handled %d events (%v), want 0", n, rec.seen)
	}
	if r := readEvent(t, s, id); r.processed || r.attempts != 0 {
		t.Fatalf("event = %+v, want untouched", r)
	}
	var held bool
	if err := conn.QueryRow(t.Context(), `SELECT pg_advisory_unlock($1)`, int64(drainLockKey)).Scan(&held); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if !held {
		t.Fatal("conn did not hold the drain lock at session level")
	}
}

func TestSweepDeletesOnlyOldProcessedEvents(t *testing.T) {
	s := testStore(t)
	src := testSource(t)
	ids := insertEvents(t, s, src, "old", "recent", "pending", "old-pending")
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
	src := testSource(t)
	id := insertEvents(t, s, src, "boot")[0]
	release := guardOthers(t, s, src)
	defer release()
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

// TestDrainRaisesIdleTimeoutForTheBatch: the drain tx runs with
// drainIdleTimeout instead of pgpool's 60s, so a consumer slower than
// 60s does not abort the batch; the pooled connection keeps 60s after.
func TestDrainRaisesIdleTimeoutForTheBatch(t *testing.T) {
	s := testStore(t)
	src := testSource(t)
	id := insertEvents(t, s, src, "idle")[0]
	const query = `SELECT setting FROM pg_settings WHERE name = 'idle_in_transaction_session_timeout'`
	var inside string
	c := consumerFunc(func(ctx context.Context, tx pgx.Tx, ev Event) error {
		return tx.QueryRow(ctx, query).Scan(&inside)
	})
	if _, err := drain(t, s, src, NewDrainer(s, []Consumer{c}, nil, testLog())); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if want := strconv.FormatInt(drainIdleTimeout.Milliseconds(), 10); inside != want {
		t.Fatalf("idle timeout inside drain = %q, want %q", inside, want)
	}
	if r := readEvent(t, s, id); !r.processed {
		t.Fatalf("event = %+v, want processed", r)
	}
	db, _ := s.db.Get()
	var after string
	if err := db.QueryRow(t.Context(), query).Scan(&after); err != nil {
		t.Fatalf("read setting: %v", err)
	}
	if after != "60000" {
		t.Fatalf("idle timeout after drain = %q, want the pool default 60000", after)
	}
}

func TestDrainRunsAfterCommitOnlyForANonEmptyBatch(t *testing.T) {
	s := testStore(t)
	src := testSource(t)
	d := NewDrainer(s, []Consumer{&recorder{}}, nil, testLog())
	calls := 0
	d.SetAfterCommit(func() { calls++ })
	if _, err := drain(t, s, src, d); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if calls != 0 {
		t.Fatalf("after-commit calls on an empty batch = %d, want 0", calls)
	}
	insertEvents(t, s, src, "a", "b")
	if n, err := drain(t, s, src, d); err != nil || n != 2 {
		t.Fatalf("Drain = %d, %v, want 2", n, err)
	}
	if calls != 1 {
		t.Fatalf("after-commit calls = %d, want 1", calls)
	}
}
