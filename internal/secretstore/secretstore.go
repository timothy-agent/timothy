// Package secretstore resolves named secret references (provider API
// keys, connector tokens) to their values without ever putting a raw
// secret in an env var or a plaintext DB column. A ref_name is looked
// up in the secrets table; its backend column says how to fetch the
// value: envelope-decrypt a ciphertext column (db), or read from an
// external system by backend_ref (vault, asm — not yet implemented).
package secretstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// ErrNotFound means no secrets row exists for the given ref name.
var ErrNotFound = errors.New("secretstore: not found")

// Store resolves and stores secret values by reference name.
type Store struct {
	db     *pgpool.Pool
	cipher *sealer
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

// Resolve returns the plaintext value for refName. Callers treat a
// missing ref as "no secret configured", not an error, for optional
// credentials.
func (s *Store) Resolve(ctx context.Context, refName string) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("secretstore: %w", err)
	}
	var (
		backend    string
		ciphertext []byte
		nonce      []byte
		backendRef string
	)
	err = db.QueryRow(ctx,
		`SELECT backend, ciphertext, nonce, backend_ref FROM secrets WHERE ref_name = $1`,
		refName).Scan(&backend, &ciphertext, &nonce, &backendRef)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotFound, refName)
	}

	switch backend {
	case "db":
		return s.cipher.open(ciphertext, nonce)
	case "vault":
		return s.resolveVault(ctx, backendRef)
	case "asm":
		return s.resolveASM(ctx, backendRef)
	default:
		return "", fmt.Errorf("secretstore: unknown backend %q for %s", backend, refName)
	}
}

// Status reports whether refName is configured and through which
// backend, without decrypting or contacting the backend — used for
// the UI's "configured" badge.
func (s *Store) Status(ctx context.Context, refName string) (configured bool, backend string, err error) {
	db, err := s.db.Get()
	if err != nil {
		return false, "", fmt.Errorf("secretstore: %w", err)
	}
	err = db.QueryRow(ctx,
		`SELECT backend FROM secrets WHERE ref_name = $1`, refName).Scan(&backend)
	if err != nil {
		return false, "", nil
	}
	return true, backend, nil
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

// Has reports whether refName has a stored secret, without decrypting
// it — used to show "configured" state in the UI.
func (s *Store) Has(ctx context.Context, refName string) (bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return false, fmt.Errorf("secretstore: %w", err)
	}
	var exists bool
	err = db.QueryRow(ctx, `SELECT true FROM secrets WHERE ref_name = $1`, refName).Scan(&exists)
	if err != nil {
		return false, nil
	}
	return exists, nil
}
