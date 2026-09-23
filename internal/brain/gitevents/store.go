package gitevents

import (
	"context"
	"fmt"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/automations"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Cursor is one github_poll_cursors row.
type Cursor struct {
	ConnectorID string
	Repo        string
	ETag        string
	LastEventID string
	RunsSince   time.Time
	NextPollAt  time.Time
}

// Store is the github_poll_cursors table's Postgres access.
type Store struct {
	db *pgpool.Pool
}

func NewStore(db *pgpool.Pool) *Store {
	return &Store{db: db}
}

// Load returns the cursor of (connectorID, repo), inserting one due now
// with runs_since now on first sight.
func (s *Store) Load(ctx context.Context, connectorID, repo string, now time.Time) (Cursor, error) {
	db, err := s.db.Get()
	if err != nil {
		return Cursor{}, fmt.Errorf("github cursor load: %w", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO github_poll_cursors (connector_id, repo, runs_since, next_poll_at)
		VALUES ($1, $2, $3, $3) ON CONFLICT (connector_id, repo) DO NOTHING`, connectorID, repo, now); err != nil {
		return Cursor{}, fmt.Errorf("github cursor load: %w", err)
	}
	c := Cursor{ConnectorID: connectorID, Repo: repo}
	if err := db.QueryRow(ctx, `SELECT etag, last_event_id, runs_since, next_poll_at FROM github_poll_cursors
		WHERE connector_id = $1 AND repo = $2`, connectorID, repo).Scan(&c.ETag, &c.LastEventID, &c.RunsSince, &c.NextPollAt); err != nil {
		return Cursor{}, fmt.Errorf("github cursor load: %w", err)
	}
	return c, nil
}

// Save writes c's bookkeeping back.
func (s *Store) Save(ctx context.Context, c Cursor) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("github cursor save: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE github_poll_cursors SET etag = $3, last_event_id = $4, runs_since = $5, next_poll_at = $6, updated_at = now()
		WHERE connector_id = $1 AND repo = $2`, c.ConnectorID, c.Repo, c.ETag, c.LastEventID, c.RunsSince, c.NextPollAt); err != nil {
		return fmt.Errorf("github cursor save: %w", err)
	}
	return nil
}

// Backoff pushes every cursor of connectorID to poll no sooner than
// until.
func (s *Store) Backoff(ctx context.Context, connectorID string, until time.Time) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("github cursor backoff: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE github_poll_cursors SET next_poll_at = GREATEST(next_poll_at, $2), updated_at = now()
		WHERE connector_id = $1`, connectorID, until); err != nil {
		return fmt.Errorf("github cursor backoff: %w", err)
	}
	return nil
}

// Sweep deletes the cursors no watch in keep names.
func (s *Store) Sweep(ctx context.Context, keep []automations.Watch) (int64, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("github cursor sweep: %w", err)
	}
	ids := make([]string, len(keep))
	repos := make([]string, len(keep))
	for i, w := range keep {
		ids[i], repos[i] = w.ConnectorID, w.Repo
	}
	tag, err := db.Exec(ctx, `DELETE FROM github_poll_cursors c WHERE NOT EXISTS (
		SELECT 1 FROM unnest($1::uuid[], $2::text[]) AS w(connector_id, repo)
		WHERE w.connector_id = c.connector_id AND w.repo = c.repo)`, ids, repos)
	if err != nil {
		return 0, fmt.Errorf("github cursor sweep: %w", err)
	}
	return tag.RowsAffected(), nil
}
