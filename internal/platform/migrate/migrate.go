// Package migrate applies numbered SQL migrations at service startup,
// under a Postgres advisory lock so concurrently starting services
// race safely.
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey serializes migration runs across services sharing
// one database. Arbitrary but stable ("TIMO").
const advisoryLockKey = 0x54494D4F

var namePattern = regexp.MustCompile(`^\d{4}_[a-z0-9_]+\.sql$`)

// List returns migration filenames in apply order. Any .sql file
// outside the NNNN_name.sql convention is an error, not a skip —
// silent skips hide typos forever.
func List(files fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: read dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		if !namePattern.MatchString(name) {
			return nil, fmt.Errorf("migrate: %q does not match NNNN_name.sql", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Run applies every migration not yet recorded in schema_migrations,
// each inside its own transaction. Idempotent: applied versions are
// skipped by name.
func Run(ctx context.Context, db *pgxpool.Pool, files fs.FS, log *slog.Logger) error {
	names, err := List(files)
	if err != nil {
		return err
	}

	conn, err := db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire connection: %w", err)
	}
	defer conn.Release()

	// Blocking here is deliberate: a concurrently starting service that
	// holds the lock is applying the same migrations — waiting for it is
	// the correct behavior, so no lock timeout.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("migrate: advisory lock: %w", err)
	}
	defer func() {
		// Best-effort unlock even when ctx was canceled mid-run.
		// Advisory locks are session-scoped, so the deferred
		// conn.Release() frees the lock regardless of this call.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("migrate: ensure schema_migrations: %w", err)
	}

	for _, name := range names {
		var applied bool
		if err := conn.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)", name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("migrate: check %s: %w", name, err)
		}
		if applied {
			continue
		}

		sqlText, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("migrate: begin %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate: apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_migrations (version) VALUES ($1)", name,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate: record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrate: commit %s: %w", name, err)
		}
		log.Info("migration applied", "version", name)
	}
	return nil
}
