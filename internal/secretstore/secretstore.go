// Package secretstore resolves named secret references (provider API
// keys, connector tokens) to their values without ever putting a raw
// secret in an env var or a plaintext DB column. A ref_name is looked
// up in the secrets table; its backend column says how to fetch the
// value: envelope-decrypt a ciphertext column (db), or read from an
// external system by backend_ref (vault: KV v2, asm: AWS Secrets
// Manager).
package secretstore

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// ErrNotFound means no secrets row exists for the given ref name.
var ErrNotFound = errors.New("secretstore: not found")

// Store resolves and stores secret values by reference name.
type Store struct {
	db     *pgpool.Pool
	cipher *sealer

	// asm caches the AWS Secrets Manager client so Resolve doesn't
	// re-run the SDK's config chain on every snapshot reload; cleared
	// when the asm backend config changes.
	asmMu sync.Mutex
	asm   asmAPI
}

// New builds a Store. masterKey must be exactly 32 bytes (AES-256);
// callers should fail startup loudly on a bad key rather than run with
// secrets that can never be decrypted.
func New(db *pgpool.Pool, masterKey []byte) (*Store, error) {
	c, err := newCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("secretstore: %w", err)
	}
	return &Store{db: db, cipher: c}, nil
}

// secretRow is one secrets-table row; shared by Resolve and the vault
// token lookup so both distinguish "absent" from "database broken".
type secretRow struct {
	backend    string
	ciphertext []byte
	nonce      []byte
	backendRef string
}

// row fetches refName's row. A missing row is ErrNotFound; any other
// failure (connection loss, timeout) surfaces as its own error so a
// database blip never reads as "secret was deleted".
func (s *Store) row(ctx context.Context, refName string) (secretRow, error) {
	db, err := s.db.Get()
	if err != nil {
		return secretRow{}, fmt.Errorf("secretstore: %w", err)
	}
	var r secretRow
	err = db.QueryRow(ctx,
		`SELECT backend, ciphertext, nonce, backend_ref FROM secrets WHERE ref_name = $1`,
		refName).Scan(&r.backend, &r.ciphertext, &r.nonce, &r.backendRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return secretRow{}, fmt.Errorf("%w: %s", ErrNotFound, refName)
	}
	if err != nil {
		return secretRow{}, fmt.Errorf("secretstore: read %s: %w", refName, err)
	}
	return r, nil
}

// Resolve returns the plaintext value for refName. Callers treat a
// missing ref as "no secret configured", not an error, for optional
// credentials.
func (s *Store) Resolve(ctx context.Context, refName string) (string, error) {
	r, err := s.row(ctx, refName)
	if err != nil {
		return "", err
	}
	switch r.backend {
	case "db":
		return s.cipher.open(r.ciphertext, r.nonce)
	case "vault":
		return s.resolveVault(ctx, r.backendRef)
	case "asm":
		return s.resolveASM(ctx, r.backendRef)
	default:
		return "", fmt.Errorf("secretstore: unknown backend %q for %s", r.backend, refName)
	}
}

// Status reports whether refName is configured and through which
// backend, without decrypting or contacting the backend — used for
// the UI's "configured" badge.
func (s *Store) Status(ctx context.Context, refName string) (configured bool, backend string, err error) {
	r, err := s.row(ctx, refName)
	if errors.Is(err, ErrNotFound) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, r.backend, nil
}

// Set encrypts value and upserts it under refName with backend "db".
// External backends (vault, asm) are configured by writing their
// secret out-of-band and pointing backend_ref at it — not through Set.
func (s *Store) Set(ctx context.Context, refName, value string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("secretstore: %w", err)
	}
	ciphertext, nonce, err := s.cipher.seal(value)
	if err != nil {
		return fmt.Errorf("secretstore: seal %s: %w", refName, err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO secrets (ref_name, backend, ciphertext, nonce, updated_at)
		VALUES ($1, 'db', $2, $3, now())
		ON CONFLICT (ref_name) DO UPDATE
			SET backend = 'db', ciphertext = $2, nonce = $3, backend_ref = '', updated_at = now()`,
		refName, ciphertext, nonce)
	if err != nil {
		return fmt.Errorf("secretstore: set %s: %w", refName, err)
	}
	return nil
}

// Delete removes a secret reference entirely.
func (s *Store) Delete(ctx context.Context, refName string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("secretstore: %w", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM secrets WHERE ref_name = $1`, refName); err != nil {
		return fmt.Errorf("secretstore: delete %s: %w", refName, err)
	}
	return nil
}
