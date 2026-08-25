// Package connectors is brain's integration control plane: third-party
// services (MCP servers, Google Workspace, ...) the agent can call as
// tools. Connectors are data — admin CRUD writes rows, the manager
// reloads and rebuilds tool sources without restarts — mirroring how
// the gateway treats providers (D-004). credential_ref names a secret
// in the shared secret store; a value is never stored or returned here.
package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// kinds whitelists what the manager can actually build. google
// arrives with the OAuth phase; the panel shows it once a builder
// registers. github is identity/credential-only (D-057): it serves no
// chat tools in this slice, existing purely so mission flows (clone,
// push, PR) and Settings can resolve a GitHub identity from a PAT; the
// MCP-based GitHub connector keeps serving GitHub chat tools.
var kinds = map[string]bool{"mcp": true, "google": true, "github": true, "microsoft": true, "imap": true, "caldav": true}

// credentialRefPattern matches the gateway's: names and paths only,
// never anything that could be a pasted secret.
var credentialRefPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]{0,128}$`)

// Connector is the API shape of one connectors row.
type Connector struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	Config        json.RawMessage `json:"config"`
	CredentialRef string          `json:"credential_ref"`
	Enabled       bool            `json:"enabled"`
	// Sensitive marks the WHOLE connector as sensitive: an MCP
	// connector's tools are namespaced "<name>_<tool>" (see
	// Manager.Tools), so its own name is a PREFIX of every tool it
	// serves, and session.SensitiveTools.Matches checks this prefix. A
	// non-MCP connector's tools instead aggregate into unified,
	// un-namespaced tools (mail_search etc.); Matches catches those via
	// AccountConnector resolving a call's account back to this
	// connector's name.
	Sensitive bool `json:"sensitive"`
}

// namePattern keeps connector names usable both as MCP tool-name
// prefixes ("<name>_<tool>") and as a unified aggregate tool's
// "account" argument value (see Manager.aggregateTools): lowercase
// slug, no spaces.
var namePattern = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)

func validate(c Connector) error {
	if !namePattern.MatchString(c.Name) {
		return fmt.Errorf("name must be a lowercase slug (a-z, 0-9, - or _), it prefixes MCP tool names and is used as an account argument")
	}
	if !kinds[c.Kind] {
		return fmt.Errorf("unknown kind %q", c.Kind)
	}
	if !credentialRefPattern.MatchString(c.CredentialRef) {
		return fmt.Errorf("credential_ref must be a name or path, never a secret value")
	}
	if len(c.Config) > 0 && !json.Valid(c.Config) {
		return fmt.Errorf("config must be a JSON object")
	}
	return nil
}

// Sentinel errors the HTTP layer maps onto status codes.
var (
	ErrNotFound    = fmt.Errorf("not found")
	ErrUnsupported = fmt.Errorf("unsupported")
)

// Store is the connectors table's CRUD, audited like the gateway's
// admin mutations. onChange fires after every successful write so the
// manager can rebuild its sources.
type Store struct {
	db       *pgpool.Pool
	log      *slog.Logger
	onChange func(context.Context)
}

func NewStore(db *pgpool.Pool, log *slog.Logger) *Store {
	return &Store{db: db, log: log, onChange: func(context.Context) {}}
}

// SetOnChange registers the post-write hook. Call before serving.
func (s *Store) SetOnChange(fn func(context.Context)) {
	if fn != nil {
		s.onChange = fn
	}
}

// List returns every connector row, config order by name.
func (s *Store) List(ctx context.Context) ([]Connector, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("connectors list: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT id, name, kind, config, credential_ref, enabled, sensitive
		FROM connectors ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("connectors list: %w", err)
	}
	defer rows.Close()

	out := []Connector{}
	for rows.Next() {
		var c Connector
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.CredentialRef, &c.Enabled, &c.Sensitive); err != nil {
			return nil, fmt.Errorf("connectors list: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one connector row by id.
func (s *Store) Get(ctx context.Context, id string) (Connector, error) {
	db, err := s.db.Get()
	if err != nil {
		return Connector{}, fmt.Errorf("connectors get: %w", err)
	}
	return scanConnector(ctx, db, id, "")
}

// Create inserts a connector, audits, and fires the change hook.
func (s *Store) Create(ctx context.Context, c Connector) (string, error) {
	if err := validate(c); err != nil {
		return "", err
	}
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("connectors create: %w", err)
	}
	cfg := c.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO connectors (name, kind, config, credential_ref, enabled, sensitive)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		c.Name, c.Kind, cfg, c.CredentialRef, c.Enabled, c.Sensitive).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("connectors create: %w", err)
	}
	s.audit(ctx, "create", id, nil, c)
	s.onChange(ctx)
	return id, nil
}

// Patch applies a partial update. Name and kind are immutable: the
// name prefixes served tool names and the kind picks the builder —
// changing either mid-flight would silently re-identify every tool.
type Patch struct {
	Config        *json.RawMessage `json:"config"`
	CredentialRef *string          `json:"credential_ref"`
	Enabled       *bool            `json:"enabled"`
	Sensitive     *bool            `json:"sensitive"`
}

func (s *Store) Patch(ctx context.Context, id string, patch Patch) error {
	if patch.CredentialRef != nil && !credentialRefPattern.MatchString(*patch.CredentialRef) {
		return fmt.Errorf("credential_ref must be a name or path, never a secret value")
	}
	if patch.Config != nil && len(*patch.Config) > 0 && !json.Valid(*patch.Config) {
		return fmt.Errorf("config must be a JSON object")
	}

	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("connectors patch: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("connectors patch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// FOR UPDATE holds the row across the read-modify-write, same as
	// the gateway's provider patch.
	before, err := scanConnector(ctx, tx, id, "FOR UPDATE")
	if err != nil {
		return err
	}
	after := before
	if patch.Config != nil {
		after.Config = *patch.Config
	}
	if patch.CredentialRef != nil {
		after.CredentialRef = *patch.CredentialRef
	}
	if patch.Enabled != nil {
		after.Enabled = *patch.Enabled
	}
	if patch.Sensitive != nil {
		after.Sensitive = *patch.Sensitive
	}

	if _, err := tx.Exec(ctx, `UPDATE connectors SET config = $2, credential_ref = $3,
			enabled = $4, sensitive = $5, updated_at = now() WHERE id = $1`,
		id, after.Config, after.CredentialRef, after.Enabled, after.Sensitive); err != nil {
		return fmt.Errorf("connectors patch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("connectors patch: %w", err)
	}
	s.audit(ctx, "update", id, before, after)
	s.onChange(ctx)
	return nil
}

// Delete removes a connector; its tools vanish on the next reload.
func (s *Store) Delete(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("connectors delete: %w", err)
	}
	before, err := scanConnector(ctx, db, id, "")
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `DELETE FROM connectors WHERE id = $1`, id); err != nil {
		return fmt.Errorf("connectors delete: %w", err)
	}
	s.audit(ctx, "delete", id, before, nil)
	s.onChange(ctx)
	return nil
}

// pgxQuerier is satisfied by both *pgxpool.Pool and pgx.Tx.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func scanConnector(ctx context.Context, q pgxQuerier, id, lock string) (Connector, error) {
	var c Connector
	err := q.QueryRow(ctx, `SELECT id, name, kind, config, credential_ref, enabled, sensitive
		FROM connectors WHERE id = $1 `+lock, id).
		Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.CredentialRef, &c.Enabled, &c.Sensitive)
	if err != nil {
		return Connector{}, fmt.Errorf("connector %s: %w", id, ErrNotFound)
	}
	return c, nil
}

// audit records who-did-what in the shared admin_audit table; failures
// log — an audit hiccup must not roll back a successful mutation, but
// it must never be silent.
func (s *Store) audit(ctx context.Context, action, entityID string, before, after any) {
	db, err := s.db.Get()
	if err != nil {
		s.log.Warn("connector audit skipped", "action", action, "error", err)
		return
	}
	b, _ := json.Marshal(before)
	aft, _ := json.Marshal(after)
	if _, err := db.Exec(ctx, `INSERT INTO admin_audit (action, entity, entity_id, before, after)
		VALUES ($1, 'connector', $2, $3, $4)`, action, entityID, b, aft); err != nil {
		s.log.Warn("connector audit failed", "action", action, "error", err)
	}
}
