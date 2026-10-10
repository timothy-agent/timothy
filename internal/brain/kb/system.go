package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SystemDocument is one page brain writes into a system collection.
type SystemDocument struct {
	Title     string
	SourceRef string
	Markdown  string
	Meta      map[string]string
}

// Writable returns ErrNotFound for an unknown collection and ErrSystem
// for a system one.
func (s *Store) Writable(ctx context.Context, collectionID string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb collection %s: %w", collectionID, err)
	}
	var system bool
	err = db.QueryRow(ctx, `SELECT system FROM kb_collections WHERE id = $1`, collectionID).Scan(&system)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("collection %s: %w", collectionID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("kb collection %s: %w", collectionID, err)
	}
	if system {
		return fmt.Errorf("collection %s: %w", collectionID, ErrSystem)
	}
	return nil
}

// DocumentWritable is Writable for the collection holding documentID.
func (s *Store) DocumentWritable(ctx context.Context, documentID string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb document %s: %w", documentID, err)
	}
	var system bool
	err = db.QueryRow(ctx, `SELECT c.system FROM kb_documents d JOIN kb_collections c ON c.id = d.collection_id
		WHERE d.id = $1`, documentID).Scan(&system)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("document %s: %w", documentID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("kb document %s: %w", documentID, err)
	}
	if system {
		return fmt.Errorf("document %s: %w", documentID, ErrSystem)
	}
	return nil
}

// CountOperatorCollections counts the collections that are not system
// ones.
func (s *Store) CountOperatorCollections(ctx context.Context) (int, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("kb collections count: %w", err)
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM kb_collections WHERE NOT system`).Scan(&n); err != nil {
		return 0, fmt.Errorf("kb collections count: %w", err)
	}
	return n, nil
}

// SystemBundleHash returns the content hash last ingested in full for
// the named system bundle, "" when none was.
func (s *Store) SystemBundleHash(ctx context.Context, name string) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("kb system bundle %s: %w", name, err)
	}
	var hash string
	err = db.QueryRow(ctx, `SELECT content_hash FROM kb_system_bundles WHERE name = $1`, name).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("kb system bundle %s: %w", name, err)
	}
	return hash, nil
}

// SetSystemBundle records hash as fully ingested for the named bundle.
func (s *Store) SetSystemBundle(ctx context.Context, name, version, hash string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("kb system bundle %s: %w", name, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO kb_system_bundles (name, version, content_hash) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET version = EXCLUDED.version, content_hash = EXCLUDED.content_hash, ingested_at = now()`,
		name, version, hash); err != nil {
		return fmt.Errorf("kb system bundle %s: %w", name, err)
	}
	return nil
}

// ReplaceSystemDocuments creates the named system collection if absent
// and swaps its documents for docs as pending rows, in one transaction.
// It returns the new document ids in docs order. A non-system
// collection holding the name is ErrInUse and is left untouched.
func (s *Store) ReplaceSystemDocuments(ctx context.Context, name, description string, docs []SystemDocument) ([]string, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("kb system collection %s: %w", name, err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("kb system collection %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var collectionID string
	var system bool
	err = tx.QueryRow(ctx, `SELECT id, system FROM kb_collections WHERE name = $1 FOR UPDATE`, name).Scan(&collectionID, &system)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `INSERT INTO kb_collections (name, description, system) VALUES ($1, $2, true) RETURNING id`,
			name, description).Scan(&collectionID); err != nil {
			return nil, fmt.Errorf("kb system collection %s create: %w", name, err)
		}
	case err != nil:
		return nil, fmt.Errorf("kb system collection %s: %w", name, err)
	case !system:
		return nil, fmt.Errorf("collection %q belongs to the operator: %w", name, ErrInUse)
	default:
		if _, err := tx.Exec(ctx, `UPDATE kb_collections SET description = $2, updated_at = now() WHERE id = $1`,
			collectionID, description); err != nil {
			return nil, fmt.Errorf("kb system collection %s update: %w", name, err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM kb_documents WHERE collection_id = $1`, collectionID); err != nil {
		return nil, fmt.Errorf("kb system collection %s clear: %w", name, err)
	}
	ids := make([]string, 0, len(docs))
	for _, d := range docs {
		meta, err := json.Marshal(d.Meta)
		if err != nil {
			return nil, fmt.Errorf("kb system document %q meta: %w", d.Title, err)
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO kb_documents
			(collection_id, title, source_type, source_ref, provenance, markdown, meta, status, bytes)
			VALUES ($1, $2, 'selfdocs', $3, 'curated', $4, $5::jsonb, 'pending', $6) RETURNING id`,
			collectionID, d.Title, d.SourceRef, d.Markdown, string(meta), len(d.Markdown)).Scan(&id); err != nil {
			return nil, fmt.Errorf("kb system document %q: %w", d.Title, err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("kb system collection %s commit: %w", name, err)
	}
	return ids, nil
}

// DocumentMeta returns the string fields of kb_documents.meta for each
// of ids that exists, keyed by document id.
func (s *Store) DocumentMeta(ctx context.Context, ids []string) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("kb document meta: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT d.id, kv.key, kv.value FROM kb_documents d, jsonb_each_text(d.meta) kv
		WHERE d.id = ANY($1::uuid[]) AND jsonb_typeof(d.meta -> kv.key) = 'string'`, ids)
	if err != nil {
		return nil, fmt.Errorf("kb document meta: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, key, value string
		if err := rows.Scan(&id, &key, &value); err != nil {
			return nil, fmt.Errorf("kb document meta: %w", err)
		}
		if out[id] == nil {
			out[id] = map[string]string{}
		}
		out[id][key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kb document meta: %w", err)
	}
	return out, nil
}
