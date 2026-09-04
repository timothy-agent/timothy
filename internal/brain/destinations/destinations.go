// Package destinations implements operator-created outbound sinks
// mission results deliver to: email (rides a google connector's Gmail
// send path), webhook, telegram (Bot API), and github (push/PR through
// a github connector). Delivery is harness-owned and deterministic
// (D-061): the model never supplies or addresses a destination, only
// ids resolved against this operator-owned table are ever reachable.
package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Destination is the API/DB shape of one destinations row.
type Destination struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"` // email | webhook | telegram | github
	Config        json.RawMessage `json:"config"`
	CredentialRef string          `json:"credential_ref"`
	Enabled       bool            `json:"enabled"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// EmailConfig is the config shape for kind='email': rides an existing
// google connector's Gmail send path rather than owning its own auth.
type EmailConfig struct {
	ConnectorID string `json:"connector_id"`
	To          string `json:"to"`
}

// WebhookConfig is the config shape for kind='webhook'.
type WebhookConfig struct {
	URL    string `json:"url"`
	Format string `json:"format"` // json | text
}

// TelegramConfig is the config shape for kind='telegram'. The bot
// token lives in Destination.CredentialRef, never here.
type TelegramConfig struct {
	ChatID string `json:"chat_id"`
}

// GitHubConfig is the config shape for kind='github': push (or push+PR)
// a mission's branch through an existing github connector, replacing
// the mission-create-time on_complete/branch_pattern/commit_style
// fields with a reusable saved destination. The token comes from the
// connector's own credential, never CredentialRef.
type GitHubConfig struct {
	ConnectorID string `json:"connector_id"`
	Mode        string `json:"mode"` // push | push_pr
	// BranchPattern/CommitStyle empty means "use the settings default,"
	// same precedence as the old mission-level fields.
	BranchPattern string `json:"branch_pattern,omitempty"`
	CommitStyle   string `json:"commit_style,omitempty"`
	// CreateIfMissing, when the mission has no target repo at delivery
	// time, creates one through ConnectorID's credential instead of
	// failing the push/PR.
	CreateIfMissing bool `json:"create_if_missing,omitempty"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)

// connectorLookup is the narrow slice of *connectors.Store a
// destination's email config validates against — an interface so this
// package never imports connectors (avoiding a cycle risk and keeping
// the dependency direction the same as api/missions.go's own
// connector validation).
type connectorLookup interface {
	Get(ctx context.Context, id string) (Connector, error)
}

// Connector is the narrow shape destinations needs from a connectors
// row to validate an email destination's connector_id.
type Connector struct {
	Kind    string
	Enabled bool
}

// Sentinel errors the HTTP layer maps onto status codes.
var (
	ErrNotFound = fmt.Errorf("not found")
	// ErrReferenced guards Delete: a destination referenced by any
	// non-terminal mission cannot be removed out from under it.
	ErrReferenced = fmt.Errorf("destination is referenced by an active mission")
)

func validate(ctx context.Context, conns connectorLookup, d Destination) error {
	if !namePattern.MatchString(d.Name) {
		return fmt.Errorf("name must be a lowercase slug (a-z, 0-9, - or _)")
	}
	switch d.Kind {
	case "email":
		var cfg EmailConfig
		if err := json.Unmarshal(d.Config, &cfg); err != nil {
			return fmt.Errorf("email config: %w", err)
		}
		if cfg.ConnectorID == "" {
			return fmt.Errorf("email destination requires config.connector_id")
		}
		if cfg.To == "" {
			return fmt.Errorf("email destination requires config.to")
		}
		if conns == nil {
			return fmt.Errorf("email destination requires connectors to be enabled")
		}
		c, err := conns.Get(ctx, cfg.ConnectorID)
		if err != nil {
			return fmt.Errorf("config.connector_id: %w", err)
		}
		if c.Kind != "google" {
			return fmt.Errorf("config.connector_id must name a google-kind connector")
		}
		if !c.Enabled {
			return fmt.Errorf("config.connector_id names a disabled connector")
		}
	case "webhook":
		var cfg WebhookConfig
		if err := json.Unmarshal(d.Config, &cfg); err != nil {
			return fmt.Errorf("webhook config: %w", err)
		}
		if !hasHTTPScheme(cfg.URL) {
			return fmt.Errorf("webhook destination requires config.url starting with http:// or https://")
		}
		switch cfg.Format {
		case "json", "text":
		default:
			return fmt.Errorf(`webhook destination requires config.format to be "json" or "text"`)
		}
	case "telegram":
		var cfg TelegramConfig
		if err := json.Unmarshal(d.Config, &cfg); err != nil {
			return fmt.Errorf("telegram config: %w", err)
		}
		if cfg.ChatID == "" {
			return fmt.Errorf("telegram destination requires config.chat_id")
		}
		if d.CredentialRef == "" {
			return fmt.Errorf("telegram destination requires credential_ref (bot token)")
		}
	case "github":
		var cfg GitHubConfig
		if err := json.Unmarshal(d.Config, &cfg); err != nil {
			return fmt.Errorf("github config: %w", err)
		}
		if cfg.ConnectorID == "" {
			return fmt.Errorf("github destination requires config.connector_id")
		}
		if conns == nil {
			return fmt.Errorf("github destination requires connectors to be enabled")
		}
		c, err := conns.Get(ctx, cfg.ConnectorID)
		if err != nil {
			return fmt.Errorf("config.connector_id: %w", err)
		}
		if c.Kind != "github" {
			return fmt.Errorf("config.connector_id must name a github-kind connector")
		}
		if !c.Enabled {
			return fmt.Errorf("config.connector_id names a disabled connector")
		}
		switch cfg.Mode {
		case "push", "push_pr":
		default:
			return fmt.Errorf(`github destination requires config.mode to be "push" or "push_pr"`)
		}
		if cfg.BranchPattern != "" {
			if err := missions.ValidateBranchPattern(cfg.BranchPattern); err != nil {
				return fmt.Errorf("config.branch_pattern: %w", err)
			}
		}
		if err := missions.ValidateCommitStyle(cfg.CommitStyle); err != nil {
			return fmt.Errorf("config.commit_style: %w", err)
		}
		if d.CredentialRef != "" {
			return fmt.Errorf("github destination must not set credential_ref (token comes from the connector)")
		}
	default:
		return fmt.Errorf("unsupported kind %q (only email, webhook, telegram, github in this release)", d.Kind)
	}
	return nil
}

func hasHTTPScheme(url string) bool {
	const httpPrefix, httpsPrefix = "http://", "https://"
	return len(url) > len(httpPrefix) && url[:len(httpPrefix)] == httpPrefix ||
		len(url) > len(httpsPrefix) && url[:len(httpsPrefix)] == httpsPrefix
}

// Store is the destinations table's CRUD.
type Store struct {
	db    *pgpool.Pool
	log   *slog.Logger
	conns connectorLookup
}

// NewStore builds a Store; conns resolves a connector_id at
// create/update time for email destinations. Pass nil when connectors
// are disabled — any email destination create/update then fails
// validation with a clear error, same as api/missions.go's own
// nil-connectors gate.
func NewStore(db *pgpool.Pool, conns connectorLookup, log *slog.Logger) *Store {
	return &Store{db: db, log: log, conns: conns}
}

const columns = `id, name, kind, config, credential_ref, enabled, created_at, updated_at`

func scan(row pgx.Row) (Destination, error) {
	var d Destination
	if err := row.Scan(&d.ID, &d.Name, &d.Kind, &d.Config, &d.CredentialRef, &d.Enabled, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return Destination{}, err
	}
	return d, nil
}

// List returns every destination, ordered by name.
func (s *Store) List(ctx context.Context) ([]Destination, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("destinations list: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+columns+` FROM destinations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("destinations list: %w", err)
	}
	defer rows.Close()
	out := []Destination{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("destinations list: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Get returns one destination by id.
func (s *Store) Get(ctx context.Context, id string) (Destination, error) {
	db, err := s.db.Get()
	if err != nil {
		return Destination{}, fmt.Errorf("destinations get: %w", err)
	}
	d, err := scan(db.QueryRow(ctx, `SELECT `+columns+` FROM destinations WHERE id = $1`, id))
	if err != nil {
		return Destination{}, fmt.Errorf("destination %s: %w", id, ErrNotFound)
	}
	return d, nil
}

// EnabledByID reports whether id names a real, enabled destination —
// the mission create handler's validation call (api/missions.go): an
// id must exist AND be enabled to be accepted onto a mission's
// destination_ids, never a bare existence check.
func (s *Store) EnabledByID(ctx context.Context, id string) (bool, error) {
	d, err := s.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return d.Enabled, nil
}

// Create validates and inserts a destination row.
func (s *Store) Create(ctx context.Context, d Destination) (string, error) {
	if err := validate(ctx, s.conns, d); err != nil {
		return "", err
	}
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("destinations create: %w", err)
	}
	cfg := d.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO destinations (name, kind, config, credential_ref, enabled)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		d.Name, d.Kind, cfg, d.CredentialRef, d.Enabled).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("destinations create: %w", err)
	}
	return id, nil
}

// Patch applies a partial update. Name and kind are immutable — a
// destination's kind decides which adapter delivers it, and changing
// it mid-flight would silently re-target existing mission references.
type Patch struct {
	Config        *json.RawMessage `json:"config"`
	CredentialRef *string          `json:"credential_ref"`
	Enabled       *bool            `json:"enabled"`
}

func (s *Store) Patch(ctx context.Context, id string, patch Patch) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("destinations patch: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("destinations patch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	before, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM destinations WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return fmt.Errorf("destination %s: %w", id, ErrNotFound)
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
	if err := validate(ctx, s.conns, after); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `UPDATE destinations SET config = $2, credential_ref = $3,
			enabled = $4, updated_at = now() WHERE id = $1`,
		id, after.Config, after.CredentialRef, after.Enabled); err != nil {
		return fmt.Errorf("destinations patch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("destinations patch: %w", err)
	}
	return nil
}

// missionReferenceChecker is the narrow slice of *missions.Store
// Delete needs to refuse removing a destination still referenced by an
// active mission — an interface so this package never imports
// missions (missions already imports nothing from here; the harness
// hook flows the other way, through a func type in missions itself).
type missionReferenceChecker interface {
	ActiveMissionReferencesDestination(ctx context.Context, destinationID string) (bool, error)
}

// scheduleReferenceChecker is the narrow slice of *missions.Store
// Delete needs to refuse removing a destination still referenced by an
// enabled schedule's mission_template — same interface-boundary
// reasoning as missionReferenceChecker. Returns the schedule's name
// (not just a bool) so Delete's error can name it, same reason the
// mission-side check settles for a bare bool: a mission has no
// operator-facing name worth surfacing, a schedule does.
type scheduleReferenceChecker interface {
	ScheduleReferencingDestinationID(ctx context.Context, destinationID string) (name string, ok bool, err error)
}

// Delete removes a destination, refusing with ErrReferenced while any
// non-terminal mission's destination_ids still names it, or any
// enabled schedule's mission_template still names it (naming the
// schedule in the error) — a historical (terminal) mission's
// reference, or a disabled schedule's, never blocks deletion.
func (s *Store) Delete(ctx context.Context, id string, refs missionReferenceChecker, scheduleRefs scheduleReferenceChecker) error {
	if refs != nil {
		referenced, err := refs.ActiveMissionReferencesDestination(ctx, id)
		if err != nil {
			return fmt.Errorf("destinations delete: check references: %w", err)
		}
		if referenced {
			return ErrReferenced
		}
	}
	if scheduleRefs != nil {
		name, referenced, err := scheduleRefs.ScheduleReferencingDestinationID(ctx, id)
		if err != nil {
			return fmt.Errorf("destinations delete: check schedule references: %w", err)
		}
		if referenced {
			return fmt.Errorf("%w: schedule %q", ErrReferenced, name)
		}
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("destinations delete: %w", err)
	}
	tag, err := db.Exec(ctx, `DELETE FROM destinations WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("destinations delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("destination %s: %w", id, ErrNotFound)
	}
	return nil
}
