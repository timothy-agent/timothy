// Package kb is brain's knowledge-base control plane (D-060):
// collection and document CRUD, upload-time markitdown conversion, and
// the background ingest handoff to memoryd. Chunk storage and search
// live in memoryd (internal/memory/store, internal/memory/api) — this
// package only owns kb_collections and kb_documents.
package kb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// ErrNotFound reports that no collection/document matched the given id.
var ErrNotFound = errors.New("not found")

// ErrInUse reports an operation blocked by a dependent row.
var ErrInUse = errors.New("in use")

// Collection is one named group of documents agents can search.
type Collection struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	DocCount    int       `json:"doc_count"`
	ChunkCount  int       `json:"chunk_count"`
	FailedCount int       `json:"failed_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Document is one ingested file within a collection. Markdown is
// deliberately excluded from this struct's JSON — API handlers strip
// it before serving (mirrors mission attachments, D-05x); it still
// lives in the row for reingest to reuse without re-converting.
type Document struct {
	ID           string     `json:"id"`
	CollectionID string     `json:"collection_id"`
	Title        string     `json:"title"`
	SourceType   string     `json:"source_type"`
	SourceRef    string     `json:"source_ref"`
	Status       string     `json:"status"`
	Error        string     `json:"error"`
	Bytes        int64      `json:"bytes"`
	ChunkCount   int        `json:"chunk_count"`
	IngestedAt   *time.Time `json:"ingested_at"`
	CreatedAt    time.Time  `json:"created_at"`
	// Markdown is the markitdown-converted document body — never
	// serialized in API responses (handlers zero it before writeJSON);
	// present here only so reingest can read it back without a
	// separate query.
	Markdown string `json:"-"`
}

// Store is the kb_collections/kb_documents CRUD.
type Store struct {
	db *pgpool.Pool
}

func New(db *pgpool.Pool) *Store {
	return &Store{db: db}
}

const collectionColumns = `c.id, c.name, c.description, c.created_at, c.updated_at,
	(SELECT count(*) FROM kb_documents d WHERE d.collection_id = c.id),
	(SELECT coalesce(sum(d.chunk_count), 0) FROM kb_documents d WHERE d.collection_id = c.id),
	(SELECT count(*) FROM kb_documents d WHERE d.collection_id = c.id AND d.status = 'failed')`

func scanCollection(row pgx.Row) (Collection, error) {
	var c Collection
	err := row.Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt, &c.DocCount, &c.ChunkCount, &c.FailedCount)
	return c, err
}

// ListCollections returns every collection, by name.
func (s *Store) ListCollections(ctx context.Context) ([]Collection, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("kb collections list: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+collectionColumns+` FROM kb_collections c ORDER BY c.name`)
	if err != nil {
		return nil, fmt.Errorf("kb collections list: %w", err)
	}
	defer rows.Close()
	out := []Collection{}
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, fmt.Errorf("kb collections list: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCollection returns one collection by id.
func (s *Store) GetCollection(ctx context.Context, id string) (Collection, error) {
	db, err := s.db.Get()
	if err != nil {
		return Collection{}, fmt.Errorf("kb collection %s: %w", id, err)
	}
	c, err := scanCollection(db.QueryRow(ctx, `SELECT `+collectionColumns+` FROM kb_collections c WHERE c.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Collection{}, fmt.Errorf("collection %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Collection{}, fmt.Errorf("kb collection %s: %w", id, err)
	}
	return c, nil
}

// CreateCollection inserts a new collection.
func (s *Store) CreateCollection(ctx context.Context, name, description string) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("kb collections create: %w", err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO kb_collections (name, description) VALUES ($1, $2) RETURNING id`,
		name, description).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("kb collections create: %w", err)
	}
	return id, nil
}

// UpdateCollection renames a collection and/or replaces its
// description. nil leaves a field unchanged, so a bare rename never
// clobbers the description the classifier matches against.
func (s *Store) UpdateCollection(ctx context.Context, id string, name, description *string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb collections update: %w", err)
	}
	tag, err := db.Exec(ctx, `UPDATE kb_collections
		SET name = COALESCE($2, name), description = COALESCE($3, description), updated_at = now()
		WHERE id = $1`, id, name, description)
	if err != nil {
		return fmt.Errorf("kb collections update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("collection %s: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteCollection removes a collection; ON DELETE CASCADE takes its
// documents and chunks with it.
func (s *Store) DeleteCollection(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb collections delete: %w", err)
	}
	tag, err := db.Exec(ctx, `DELETE FROM kb_collections WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("kb collections delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("collection %s: %w", id, ErrNotFound)
	}
	return nil
}

const documentColumns = `id, collection_id, title, source_type, source_ref, status, error, bytes, chunk_count, ingested_at, created_at, markdown`

func scanDocument(row pgx.Row) (Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.CollectionID, &d.Title, &d.SourceType, &d.SourceRef,
		&d.Status, &d.Error, &d.Bytes, &d.ChunkCount, &d.IngestedAt, &d.CreatedAt, &d.Markdown)
	return d, err
}

// ListDocuments returns every document in a collection, newest first.
func (s *Store) ListDocuments(ctx context.Context, collectionID string) ([]Document, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("kb documents list: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+documentColumns+` FROM kb_documents WHERE collection_id = $1 ORDER BY created_at DESC`, collectionID)
	if err != nil {
		return nil, fmt.Errorf("kb documents list: %w", err)
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("kb documents list: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDocument returns one document by id.
func (s *Store) GetDocument(ctx context.Context, id string) (Document, error) {
	db, err := s.db.Get()
	if err != nil {
		return Document{}, fmt.Errorf("kb document %s: %w", id, err)
	}
	d, err := scanDocument(db.QueryRow(ctx, `SELECT `+documentColumns+` FROM kb_documents WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Document{}, fmt.Errorf("document %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Document{}, fmt.Errorf("kb document %s: %w", id, err)
	}
	return d, nil
}

// CreateDocument inserts a pending document row: markdown is the
// markitdown conversion done once at upload/fetch time (or the raw
// content for markdown/plain text, which skip conversion); sourceType
// is 'file' or 'url'; sourceRef names the uploaded filename or the
// fetched URL.
func (s *Store) CreateDocument(ctx context.Context, collectionID, title, sourceType, sourceRef, markdown string, bytes int64) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("kb documents create: %w", err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO kb_documents
		(collection_id, title, source_type, source_ref, markdown, status, bytes)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6) RETURNING id`,
		collectionID, title, sourceType, sourceRef, markdown, bytes).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("kb documents create: %w", err)
	}
	return id, nil
}

// SweepStale fails documents parked at pending/ingesting. Ingest runs
// on a fire-and-forget goroutine (api's startIngest), so a brain
// restart mid-ingest strands the row in a non-terminal status forever
// — the UI polls a spinner that never resolves. Called once at boot on
// its own goroutine, which races the freshly started API server: the
// 30-second grace window keeps a legitimately in-flight upload from
// being swept. The stored markdown survives, so reingest recovers the
// document.
func (s *Store) SweepStale(ctx context.Context) (int64, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("kb sweep stale: %w", err)
	}
	tag, err := db.Exec(ctx, `UPDATE kb_documents
		SET status = 'failed', error = 'ingestion interrupted by a restart — re-ingest to retry', updated_at = now()
		WHERE status IN ('pending', 'ingesting')
		  AND updated_at < now() - interval '30 seconds'`)
	if err != nil {
		return 0, fmt.Errorf("kb sweep stale: %w", err)
	}
	return tag.RowsAffected(), nil
}

// SetIngesting flips a document to the ingesting phase right before
// the memoryd call — the background goroutine's own status update,
// distinct from memoryd's own ready/failed report (store.KBStore.
// SetIngested/SetFailed in internal/memory/store/kb.go).
func (s *Store) SetIngesting(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb document %s ingesting: %w", id, err)
	}
	if _, err := db.Exec(ctx, `UPDATE kb_documents SET status = 'ingesting', updated_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("kb document %s ingesting: %w", id, err)
	}
	return nil
}

// SetFailed marks a document failed — used when brain itself (not
// memoryd) hits an error before the ingest call ever reaches memoryd,
// e.g. memoryd unreachable.
func (s *Store) SetFailed(ctx context.Context, id, errMsg string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb document %s failed: %w", id, err)
	}
	if _, err := db.Exec(ctx, `UPDATE kb_documents SET status = 'failed', error = $2, updated_at = now() WHERE id = $1`, id, errMsg); err != nil {
		return fmt.Errorf("kb document %s failed: %w", id, err)
	}
	return nil
}

// FindDocumentBySource looks up a document by its dedup key
// (source_type, source_ref) — used by clip ingestion to tell a fresh
// clip from a re-clip of the same URL.
func (s *Store) FindDocumentBySource(ctx context.Context, sourceType, sourceRef string) (Document, error) {
	db, err := s.db.Get()
	if err != nil {
		return Document{}, fmt.Errorf("kb document by source: %w", err)
	}
	d, err := scanDocument(db.QueryRow(ctx,
		`SELECT `+documentColumns+` FROM kb_documents WHERE source_type = $1 AND source_ref = $2`,
		sourceType, sourceRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return Document{}, fmt.Errorf("document %s %s: %w", sourceType, sourceRef, ErrNotFound)
	}
	if err != nil {
		return Document{}, fmt.Errorf("kb document by source: %w", err)
	}
	return d, nil
}

// ReplaceDocumentContent overwrites a document's content on a re-clip:
// title, markdown, and bytes are replaced, status resets to pending for
// a fresh ingest, and any error is cleared. collectionID moves the
// document only when non-empty, leaving it in place otherwise.
func (s *Store) ReplaceDocumentContent(ctx context.Context, id, title, markdown string, bytes int64, collectionID string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb document %s replace: %w", id, err)
	}
	tag, err := db.Exec(ctx, `UPDATE kb_documents
		SET title = $2, markdown = $3, bytes = $4, status = 'pending', error = '',
			collection_id = COALESCE(NULLIF($5, '')::uuid, collection_id), updated_at = now()
		WHERE id = $1`, id, title, markdown, bytes, collectionID)
	if err != nil {
		return fmt.Errorf("kb document %s replace: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("document %s: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteDocument removes a document; ON DELETE CASCADE takes its
// chunks with it.
func (s *Store) DeleteDocument(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb documents delete: %w", err)
	}
	tag, err := db.Exec(ctx, `DELETE FROM kb_documents WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("kb documents delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("document %s: %w", id, ErrNotFound)
	}
	return nil
}
