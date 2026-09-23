package events

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Store is the events table's Postgres access.
type Store struct {
	db *pgpool.Pool
}

func NewStore(db *pgpool.Pool) *Store {
	return &Store{db: db}
}

// Insert adds ev inside the producer's own transaction. A second insert
// with the same (source, dedup_key) is a no-op.
func (s *Store) Insert(ctx context.Context, tx pgx.Tx, ev Event) error {
	if _, err := tx.Exec(ctx, `INSERT INTO events (source, kind, dedup_key, payload)
		VALUES ($1, $2, $3, $4) ON CONFLICT (source, dedup_key) DO NOTHING`,
		ev.Source, ev.Kind, ev.DedupKey, []byte(ev.Payload)); err != nil {
		return fmt.Errorf("events insert: %w", err)
	}
	return nil
}

// Add inserts ev in its own transaction and returns its id.
func (s *Store) Add(ctx context.Context, ev Event) (int64, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("events add: %w", err)
	}
	var id int64
	if err := db.QueryRow(ctx, `INSERT INTO events (source, kind, dedup_key, payload) VALUES ($1, $2, $3, $4) RETURNING id`,
		ev.Source, ev.Kind, ev.DedupKey, []byte(ev.Payload)).Scan(&id); err != nil {
		return 0, fmt.Errorf("events add: %w", err)
	}
	return id, nil
}

// AddIfNew inserts ev in its own transaction. inserted is false when an
// event with the same (source, dedup_key) already exists.
func (s *Store) AddIfNew(ctx context.Context, ev Event) (id int64, inserted bool, err error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, false, fmt.Errorf("events add: %w", err)
	}
	err = db.QueryRow(ctx, `INSERT INTO events (source, kind, dedup_key, payload) VALUES ($1, $2, $3, $4)
		ON CONFLICT (source, dedup_key) DO NOTHING RETURNING id`,
		ev.Source, ev.Kind, ev.DedupKey, []byte(ev.Payload)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("events add: %w", err)
	}
	return id, true, nil
}

// claimBatch locks up to limit unprocessed events in id order, skipping
// rows another transaction already holds.
func claimBatch(ctx context.Context, tx pgx.Tx, limit int) ([]Event, error) {
	rows, err := tx.Query(ctx, `SELECT id, source, kind, dedup_key, payload, created_at, attempts
		FROM events WHERE processed_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, fmt.Errorf("events claim: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var ev Event
		if err := rows.Scan(&ev.ID, &ev.Source, &ev.Kind, &ev.DedupKey, &ev.Payload, &ev.CreatedAt, &ev.Attempts); err != nil {
			return nil, fmt.Errorf("events claim scan: %w", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// markProcessed records a successful delivery.
func markProcessed(ctx context.Context, tx pgx.Tx, id int64) error {
	if _, err := tx.Exec(ctx, `UPDATE events SET processed_at = now(), attempts = attempts + 1 WHERE id = $1`, id); err != nil {
		return fmt.Errorf("events mark processed: %w", err)
	}
	return nil
}

// markFailed records a failed delivery; deadLetter also sets
// processed_at so the event is never claimed again.
func markFailed(ctx context.Context, tx pgx.Tx, id int64, errText string, deadLetter bool) error {
	if _, err := tx.Exec(ctx, `UPDATE events SET attempts = attempts + 1, last_error = $2,
			processed_at = CASE WHEN $3 THEN now() ELSE processed_at END
		WHERE id = $1`, id, errText, deadLetter); err != nil {
		return fmt.Errorf("events mark failed: %w", err)
	}
	return nil
}

// Sweep deletes events processed longer than retention days ago.
func (s *Store) Sweep(ctx context.Context, retentionDays int) (int64, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("events sweep: %w", err)
	}
	tag, err := db.Exec(ctx, `DELETE FROM events WHERE processed_at < now() - make_interval(days => $1)`, retentionDays)
	if err != nil {
		return 0, fmt.Errorf("events sweep: %w", err)
	}
	return tag.RowsAffected(), nil
}
