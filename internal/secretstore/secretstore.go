// Package secretstore resolves named secret references (provider API
// keys, connector tokens) to their values without ever putting a raw
// secret in an env var or a plaintext DB column. A ref_name is looked
// up in the secrets table; its backend column says how to fetch the
// value: envelope-decrypt a ciphertext column (db), or read from an
// external system by backend_ref (vault: KV v2, asm: AWS Secrets
// Manager). Writes are through: Set always takes a raw secret value
// and, for external backends, writes it into that system itself under
// a backend_ref Timothy generates and owns (timothy/<ref_name>) —
// there is no user-typed external reference.
package secretstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

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

// Ref is one secrets-table row's directory metadata — name and
// timestamps only, never the value or its backend_ref (which can leak
// the external path an operator otherwise never sees).
type Ref struct {
	RefName   string
	Backend   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// List returns every stored ref's directory metadata, ordered by name,
// for the credentials panel. Never resolves or exposes a value.
func (s *Store) List(ctx context.Context) ([]Ref, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("secretstore: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT ref_name, backend, created_at, updated_at FROM secrets ORDER BY ref_name`)
	if err != nil {
		return nil, fmt.Errorf("secretstore: list: %w", err)
	}
	defer rows.Close()

	out := []Ref{}
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.RefName, &r.Backend, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("secretstore: list: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// externalRef is the backend_ref every write-through secret gets in an
// external backend — Timothy-owned, never user input, so Delete can
// safely tell "a copy we wrote" from a pre-existing reference.
func externalRef(refName string) string {
	return "timothy/" + refName
}

// refNamePattern bounds ref names that become external paths/names:
// externalRef embeds refName in a Vault URL path and an ASM secret
// name, so separators or ".." would escape the timothy/ prefix.
var refNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func validExternalRefName(refName string) error {
	if !refNamePattern.MatchString(refName) || strings.Contains(refName, "..") {
		return fmt.Errorf("secretstore: ref name %q: external backends need a plain name (letters, digits, _ . -)", refName)
	}
	return nil
}

// Set stores value under refName through the store-wide default
// backend: "db" encrypts it in place, "vault"/"asm" write it into that
// system (at a Timothy-owned path/name derived from refName) and keep
// only the pointer locally.
func (s *Store) Set(ctx context.Context, refName, value string) error {
	backend, err := s.DefaultBackend(ctx)
	if err != nil {
		return err
	}
	switch backend {
	case "vault":
		if err := validExternalRefName(refName); err != nil {
			return err
		}
		if err := s.writeVault(ctx, externalRef(refName), value); err != nil {
			return err
		}
		return s.upsertRef(ctx, refName, "vault", externalRef(refName)+"#value")
	case "asm":
		if err := validExternalRefName(refName); err != nil {
			return err
		}
		if err := s.writeASM(ctx, externalRef(refName), value); err != nil {
			return err
		}
		return s.upsertRef(ctx, refName, "asm", externalRef(refName))
	default:
		return s.SetDB(ctx, refName, value)
	}
}

// SetDB encrypts value and upserts it under refName with backend "db",
// bypassing the store-wide default. Used to pin backend-bootstrap
// credentials (the vault token, the ASM static secret key) that can
// never live behind the external backend they unlock.
func (s *Store) SetDB(ctx context.Context, refName, value string) error {
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

// upsertRef points refName at an external secret this store just wrote
// (or overwrote) — ciphertext/nonce cleared, since the value now lives
// out there, not here.
func (s *Store) upsertRef(ctx context.Context, refName, backend, backendRef string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("secretstore: %w", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO secrets (ref_name, backend, backend_ref, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (ref_name) DO UPDATE
			SET backend = $2, backend_ref = $3, ciphertext = NULL, nonce = NULL, updated_at = now()`,
		refName, backend, backendRef)
	if err != nil {
		return fmt.Errorf("secretstore: set %s: %w", refName, err)
	}
	return nil
}

// BootstrapRefs is the exported form of bootstrapRefs, for callers
// outside this package (the gateway admin's secrets list, which flags
// each ref as "system" so the UI can hide its delete action).
func (s *Store) BootstrapRefs(ctx context.Context) (map[string]string, error) {
	return s.bootstrapRefs(ctx)
}

// bootstrapRefs collects the ref names every configured external
// backend needs to log in with — vault's token/secret_id ref, asm's
// static secret key ref — applying each config loader's own
// defaulting for an empty ref field, mapped to the backend name that
// needs it. Unconfigured backends contribute nothing. These refs
// unlock the backend they name, so migrating or deleting one of them
// is a chicken-and-egg lockout: nothing left in db can decrypt (or
// nothing remains at all for) the login credential needed to resolve
// every other secret in that backend.
func (s *Store) bootstrapRefs(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	vaultCfg, err := s.GetBackendConfig(ctx, "vault")
	if err != nil {
		return nil, err
	}
	if string(vaultCfg) != "{}" {
		var v VaultConfig
		if err := json.Unmarshal(vaultCfg, &v); err != nil {
			return nil, fmt.Errorf("secretstore: vault config: %w", err)
		}
		tokenRef := v.TokenRef
		if tokenRef == "" {
			tokenRef = defaultVaultTokenRef
		}
		out[tokenRef] = "vault"
		if v.Auth == "approle" {
			secretIDRef := v.SecretIDRef
			if secretIDRef == "" {
				secretIDRef = defaultVaultSecretIDRef
			}
			out[secretIDRef] = "vault"
		}
	}
	asmCfg, err := s.GetBackendConfig(ctx, "asm")
	if err != nil {
		return nil, err
	}
	if string(asmCfg) != "{}" {
		var a ASMConfig
		if err := json.Unmarshal(asmCfg, &a); err != nil {
			return nil, fmt.Errorf("secretstore: asm config: %w", err)
		}
		if a.Auth == "keys" {
			secretKeyRef := a.SecretKeyRef
			if secretKeyRef == "" {
				secretKeyRef = defaultASMSecretKeyRef
			}
			out[secretKeyRef] = "asm"
		}
	}
	return out, nil
}

// Migrate moves refName's stored value onto targetBackend, replacing
// its current backend entirely. Idempotent no-op when refName already
// lives on targetBackend. Refuses to move a backend bootstrap
// credential (see bootstrapRefs) off db into the very backend it
// unlocks. Ordering matters for crash safety: the new
// home is written FIRST, the row's backend column flipped SECOND, and
// only then is the old storage wiped — a crash between any two steps
// leaves the value readable at its new home (a stray old-backend copy
// is merely wasteful, never lost) rather than gone. The whole thing
// cannot be one DB transaction since external writes (vault/asm HTTP
// calls) aren't transactional with Postgres; the ordering above is the
// substitute safety net. Deleting the old copy is best-effort, silent
// (like Delete's own external cleanup) — a leftover external secret is
// orphaned but harmless once the row no longer points at it, and never
// fails the migration itself.
func (s *Store) Migrate(ctx context.Context, refName, targetBackend string) error {
	if targetBackend != "db" {
		if err := validExternalBackend(targetBackend); err != nil {
			return err
		}
		bootstrap, err := s.bootstrapRefs(ctx)
		if err != nil {
			return err
		}
		if bootstrap[refName] != "" {
			return fmt.Errorf("secretstore: %q is a backend bootstrap credential (vault/asm login secret) and must stay in built-in db storage", refName)
		}
	}
	r, err := s.row(ctx, refName)
	if err != nil {
		return err
	}
	if r.backend == targetBackend {
		return nil
	}

	value, err := s.Resolve(ctx, refName)
	if err != nil {
		return fmt.Errorf("secretstore: migrate %s: resolve current value: %w", refName, err)
	}

	// Step 1: write the value into its new home. Nothing about refName's
	// row changes yet, so a crash here just leaves the old copy in place
	// with the row still pointing at it — safe, retryable.
	switch targetBackend {
	case "db":
		if err := s.SetDB(ctx, refName, value); err != nil {
			return fmt.Errorf("secretstore: migrate %s: write db: %w", refName, err)
		}
	case "vault":
		if err := validExternalRefName(refName); err != nil {
			return err
		}
		if err := s.writeVault(ctx, externalRef(refName), value); err != nil {
			return fmt.Errorf("secretstore: migrate %s: write vault: %w", refName, err)
		}
	case "asm":
		if err := validExternalRefName(refName); err != nil {
			return err
		}
		if err := s.writeASM(ctx, externalRef(refName), value); err != nil {
			return fmt.Errorf("secretstore: migrate %s: write asm: %w", refName, err)
		}
	}

	// Step 2: flip the row to point at the new home. SetDB/upsertRef
	// already did this as part of step 1 for "db"; external targets need
	// it done explicitly since writeVault/writeASM only touch the
	// external system, not the row.
	if targetBackend != "db" {
		backendRef := externalRef(refName)
		if targetBackend == "vault" {
			backendRef += "#value"
		}
		if err := s.upsertRef(ctx, refName, targetBackend, backendRef); err != nil {
			return fmt.Errorf("secretstore: migrate %s: point row at %s: %w", refName, targetBackend, err)
		}
	}

	// Step 3: wipe the old home now that nothing points at it. Best
	// effort, silently — the row is already correct either way (same as
	// Delete's own external cleanup, which has nowhere to log either), so
	// a leftover external secret never fails Migrate. A db old home needs
	// no separate wipe here: upsertRef (step 2, for an external target)
	// already cleared ciphertext/nonce as part of the same statement.
	switch r.backend {
	case "vault":
		_ = s.deleteVault(ctx, externalRef(refName))
	case "asm":
		_ = s.deleteASM(ctx, externalRef(refName))
	}
	return nil
}

// Delete removes a secret reference entirely, best-effort deleting its
// external copy first when this store owns it (backend_ref follows the
// timothy/<ref_name> convention it writes in Set) — a reference typed
// against the old external-reference model points at a secret Timothy
// never owned and must never delete. Refused for a backend bootstrap
// credential (see bootstrapRefs): deleting the login secret a
// configured backend needs would strand every other secret already
// stored there, unresolvable, with no recovery path (Migrate at least
// leaves the old copy in place on a rejected move; a delete has
// nothing to fall back to).
func (s *Store) Delete(ctx context.Context, refName string) error {
	bootstrap, err := s.bootstrapRefs(ctx)
	if err != nil {
		return err
	}
	if backend := bootstrap[refName]; backend != "" {
		return fmt.Errorf("secretstore: refusing to delete %q: it is the bootstrap credential for the %s secret backend; deleting it would make every %s-stored secret unresolvable",
			refName, backend, backend)
	}
	r, err := s.row(ctx, refName)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err == nil {
		path, _ := splitRef(r.backendRef, "")
		if path == externalRef(refName) {
			// Best-effort: the external copy failing to delete must
			// never block the row delete below. Nothing to log to here
			// (the store carries no logger) — the row is the source of
			// truth for "is refName configured", and it's gone either way.
			switch r.backend {
			case "vault":
				_ = s.deleteVault(ctx, path)
			case "asm":
				_ = s.deleteASM(ctx, path)
			}
		}
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("secretstore: %w", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM secrets WHERE ref_name = $1`, refName); err != nil {
		return fmt.Errorf("secretstore: delete %s: %w", refName, err)
	}
	return nil
}
