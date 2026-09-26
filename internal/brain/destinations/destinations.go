// Package destinations implements operator-created outbound sinks
// mission results deliver to: email (rides a google connector's Gmail
// send path), webhook, channel (a Telegram, Slack or email channel's
// transport), and github (push/PR through a github connector). Delivery is harness-owned and deterministic
// (D-061): the model never supplies or addresses a destination, only
// ids resolved against this operator-owned table are ever reachable.
package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Destination is the API/DB shape of one destinations row.
type Destination struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"` // email | webhook | channel | github | bitbucket
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

// ChannelConfig is the config shape for kind='channel': deliver
// through a channel's transport. The channel row holds the
// credentials, so Destination.CredentialRef stays empty. ChatID (and
// ThreadID) address Telegram and Slack channels, To email channels.
type ChannelConfig struct {
	ChannelID string `json:"channel_id"`
	ChatID    string `json:"chat_id,omitempty"`
	ThreadID  string `json:"thread_id,omitempty"`
	To        string `json:"to,omitempty"`
}

// Channel kinds a channel destination delivers through.
const (
	ChannelTelegram = "telegram"
	ChannelSlack    = "slack"
	ChannelEmail    = "email"
)

// ChannelRef is the narrow shape destinations needs from a channels
// row; main fills it from channels.Store.Get. Enabled is left out on
// purpose: it governs inbound polling only.
type ChannelRef struct {
	Kind          string
	CredentialRef string
	ConnectorID   string
}

// ChannelLookup resolves a channel id; nil when channels are off.
type ChannelLookup func(ctx context.Context, id string) (ChannelRef, error)

// RepoDestinationConfig is the config shape every git provider kind
// shares: push (or push+PR) a mission's branch through an existing
// connector of that kind, replacing the mission-create-time
// on_complete/branch_pattern/commit_style fields with a reusable saved
// destination. The token comes from the connector's own credential,
// never CredentialRef.
type RepoDestinationConfig struct {
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

// GitHubConfig is RepoDestinationConfig's former name, kept as an
// alias for one release so an out-of-tree caller still compiles.
type GitHubConfig = RepoDestinationConfig

// validateName trims name and checks it against the plain-text rule
// (same as agents.validateName and connectors.validateName): 1..64
// runes, any printable character, no control characters. Destination
// name is a UI/DB label only, never a tool-facing identifier, so it
// has no slug constraint. Returns the trimmed name.
func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if utf8.RuneCountInString(name) > 64 {
		return "", fmt.Errorf("name must be at most 64 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("name must not contain control characters")
		}
	}
	return name, nil
}

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
	// ErrInvalid wraps every Create/Patch validation rejection.
	ErrInvalid = errors.New("invalid destination")
)

func validate(ctx context.Context, conns connectorLookup, channels ChannelLookup, d *Destination) error {
	name, err := validateName(d.Name)
	if err != nil {
		return err
	}
	d.Name = name
	// Repo kinds are the registry's, not a case list: a new git provider
	// becomes a valid destination kind by registering a Descriptor.
	if gitprovider.IsKind(d.Kind) {
		return validateRepoKind(ctx, conns, d)
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
	case "channel":
		return validateChannel(ctx, channels, d)
	default:
		return fmt.Errorf("unsupported kind %q (only email, webhook, channel, github, bitbucket in this release)", d.Kind)
	}
	return nil
}

// validateChannel validates a channel destination: the channel must
// exist, and its kind decides the address fields. A disabled channel is
// fine: enabled governs inbound polling only, delivery still works.
func validateChannel(ctx context.Context, channels ChannelLookup, d *Destination) error {
	var cfg ChannelConfig
	if err := json.Unmarshal(d.Config, &cfg); err != nil {
		return fmt.Errorf("channel config: %w", err)
	}
	if cfg.ChannelID == "" {
		return fmt.Errorf("channel destination requires config.channel_id")
	}
	if d.CredentialRef != "" {
		return fmt.Errorf("channel destination must not set credential_ref (the channel holds it)")
	}
	if channels == nil {
		return fmt.Errorf("channel destination requires channels to be enabled")
	}
	c, err := channels(ctx, cfg.ChannelID)
	if err != nil {
		return fmt.Errorf("config.channel_id: %w", err)
	}
	switch c.Kind {
	case ChannelTelegram, ChannelSlack:
		if strings.TrimSpace(cfg.ChatID) == "" {
			return fmt.Errorf("a %s channel destination requires config.chat_id", c.Kind)
		}
	case ChannelEmail:
		if a, err := mail.ParseAddress(cfg.To); err != nil || a.Address != strings.TrimSpace(cfg.To) {
			return fmt.Errorf("an email channel destination requires config.to, one email address")
		}
	default:
		return fmt.Errorf("channel kind %q cannot deliver", c.Kind)
	}
	return nil
}

// validateRepoKind validates a destination whose kind is a registered
// git provider: push/PR delivery through a connector of the same kind.
func validateRepoKind(ctx context.Context, conns connectorLookup, d *Destination) error {
	var cfg RepoDestinationConfig
	if err := json.Unmarshal(d.Config, &cfg); err != nil {
		return fmt.Errorf("%s config: %w", d.Kind, err)
	}
	if cfg.ConnectorID == "" {
		return fmt.Errorf("%s destination requires config.connector_id", d.Kind)
	}
	if conns == nil {
		return fmt.Errorf("%s destination requires connectors to be enabled", d.Kind)
	}
	c, err := conns.Get(ctx, cfg.ConnectorID)
	if err != nil {
		return fmt.Errorf("config.connector_id: %w", err)
	}
	if c.Kind != d.Kind {
		return fmt.Errorf("config.connector_id must name a %s-kind connector", d.Kind)
	}
	if !c.Enabled {
		return fmt.Errorf("config.connector_id names a disabled connector")
	}
	switch cfg.Mode {
	case "push", "push_pr":
	default:
		return fmt.Errorf(`%s destination requires config.mode to be "push" or "push_pr"`, d.Kind)
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
		return fmt.Errorf("%s destination must not set credential_ref (token comes from the connector)", d.Kind)
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
	db       *pgpool.Pool
	log      *slog.Logger
	conns    connectorLookup
	channels ChannelLookup
}

// NewStore builds a Store; conns resolves a connector_id at
// create/update time for email destinations. Pass nil when connectors
// are disabled — any email destination create/update then fails
// validation with a clear error, same as api/missions.go's own
// nil-connectors gate. channels resolves a channel destination's
// channel_id; nil rejects channel destinations the same way.
func NewStore(db *pgpool.Pool, conns connectorLookup, channels ChannelLookup, log *slog.Logger) *Store {
	return &Store{db: db, log: log, conns: conns, channels: channels}
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

// KindByID reports id's kind and enabled state, missions.ValidateDeps'
// DestinationKind dep: a mission's destination entry must both exist
// AND be enabled, and a "github" kind entry carries its own extra
// create-time rules (coding-only, repo_url only on github).
func (s *Store) KindByID(ctx context.Context, id string) (kind string, enabled bool, err error) {
	d, err := s.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return d.Kind, d.Enabled, nil
}

// RepoPolicy resolves id's saved branch pattern / commit style for
// missions.GitHubPolicyResolver: ok is false when id does not name a
// repo-kind row (missions has no compile-time dependency on this
// package, see missions.GitHubPolicy's doc comment). Every repo kind
// shares RepoDestinationConfig and validates these fields the same
// way, so all of them resolve here.
func (s *Store) RepoPolicy(ctx context.Context, id string) (missions.GitHubPolicy, bool, error) {
	d, err := s.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return missions.GitHubPolicy{}, false, nil
		}
		return missions.GitHubPolicy{}, false, err
	}
	if !gitprovider.IsKind(d.Kind) {
		return missions.GitHubPolicy{}, false, nil
	}
	var cfg RepoDestinationConfig
	if err := json.Unmarshal(d.Config, &cfg); err != nil {
		return missions.GitHubPolicy{}, false, fmt.Errorf("%s config: %w", d.Kind, err)
	}
	return missions.GitHubPolicy{BranchPattern: cfg.BranchPattern, CommitStyle: cfg.CommitStyle}, true, nil
}

// Create validates and inserts a destination row.
func (s *Store) Create(ctx context.Context, d Destination) (string, error) {
	if err := validate(ctx, s.conns, s.channels, &d); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalid, err)
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
		if isUniqueViolation(err) {
			return "", fmt.Errorf("%w: a destination with this name already exists", ErrInvalid)
		}
		return "", fmt.Errorf("destinations create: %w", err)
	}
	return id, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
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
	if err := validate(ctx, s.conns, s.channels, &after); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
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

// automationReferenceChecker is the slice of *automations.Store
// Delete needs to refuse removing a destination an enabled
// automation's mission action still names; it returns the name for the error.
type automationReferenceChecker interface {
	NameReferencingDestination(ctx context.Context, destinationID string) (name string, ok bool, err error)
}

// Delete removes a destination, refusing with ErrReferenced while any
// non-terminal mission's destination_ids still names it, or any
// enabled automation's mission action still names it (naming the
// automation in the error). A terminal mission's reference, or a
// disabled automation's, never blocks deletion.
func (s *Store) Delete(ctx context.Context, id string, refs missionReferenceChecker, automationRefs automationReferenceChecker) error {
	if refs != nil {
		referenced, err := refs.ActiveMissionReferencesDestination(ctx, id)
		if err != nil {
			return fmt.Errorf("destinations delete: check references: %w", err)
		}
		if referenced {
			return ErrReferenced
		}
	}
	if automationRefs != nil {
		name, referenced, err := automationRefs.NameReferencingDestination(ctx, id)
		if err != nil {
			return fmt.Errorf("destinations delete: check automation references: %w", err)
		}
		if referenced {
			return fmt.Errorf("%w: automation %q", ErrReferenced, name)
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
