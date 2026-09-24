// Package channels connects external chat surfaces (Telegram, Slack,
// email) to
// chat sessions: pairing of external senders, conversation to session
// mapping, per-conversation serialization and streamed replies
// (issues #828, #830). Every ceiling lives in Go before any model call.
package channels

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Pairing statuses.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRevoked  = "revoked"
)

// Channel kinds this slice runs.
const (
	KindTelegram = "telegram"
	KindSlack    = "slack"
	KindEmail    = "email"
)

// Sentinel errors the HTTP layer maps onto status codes.
var (
	ErrNotFound        = errors.New("channel not found")
	ErrNameConflict    = errors.New("a channel with this name already exists")
	ErrUnknownAgent    = errors.New("unknown agent_id")
	ErrKindUnavailable = errors.New("channel kind not available yet")
	ErrInvalid         = errors.New("invalid channel")
)

// credentialRefPattern matches the connectors rule: names and paths,
// never a pasted secret.
var credentialRefPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,128}$`)

var (
	connectorIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)
	domainPattern      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
)

// maxFromAllow caps an email channel's sender allowlist.
const maxFromAllow = 50

// Config is the typed channels.config column.
type Config struct {
	// Dispatch picks an agent per message when the channel has none.
	Dispatch bool `json:"dispatch"`
	// BotUsername is set by the test endpoint, display only.
	BotUsername string `json:"bot_username,omitempty"`
	// AppTokenRef names the Slack app-level token (Socket Mode); the
	// bot token is credential_ref.
	AppTokenRef string `json:"app_token_ref,omitempty"`
	// ConnectorID names the email channel's imap connector, which holds
	// the mailbox password.
	ConnectorID string `json:"connector_id,omitempty"`
	// FromAllow lists the senders an email channel reads: full
	// addresses or @domain suffixes, lowercased.
	FromAllow []string `json:"from_allow,omitempty"`
}

// PairingCounts tallies a channel's senders by status.
type PairingCounts struct {
	Pending  int `json:"pending"`
	Approved int `json:"approved"`
	Revoked  int `json:"revoked"`
}

// Channel is one channels row as the API shows it.
type Channel struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Kind          string        `json:"kind"`
	Config        Config        `json:"config"`
	CredentialRef string        `json:"credential_ref"`
	AgentID       string        `json:"agent_id"`
	Enabled       bool          `json:"enabled"`
	Pairings      PairingCounts `json:"pairings"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// Pairing is one external sender of a channel.
type Pairing struct {
	ChannelID      string     `json:"channel_id"`
	ExternalUserID string     `json:"external_user_id"`
	DisplayName    string     `json:"display_name"`
	Status         string     `json:"status"`
	Code           string     `json:"code,omitempty"`
	CodeExpiresAt  *time.Time `json:"code_expires_at,omitempty"`
	LastPromptAt   *time.Time `json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Conversation maps one external chat (and thread) to a session.
type Conversation struct {
	ID               string
	ChannelID        string
	ExternalChatID   string
	ExternalThreadID string
	ExternalUserID   string
	SessionID        string
	AgentID          string
}

// State is the typed channels.state column: the adapter's receive
// cursor (Telegram's next update offset; Slack keeps none).
type State struct {
	Cursor string `json:"cursor,omitempty"`
}

// decodeState reads a state column. Rows written before #830 hold
// {"update_offset": N}; that loads as the cursor.
func decodeState(raw []byte) State {
	var st struct {
		Cursor       string `json:"cursor"`
		UpdateOffset int64  `json:"update_offset"`
	}
	_ = json.Unmarshal(raw, &st)
	if st.Cursor == "" && st.UpdateOffset != 0 {
		st.Cursor = strconv.FormatInt(st.UpdateOffset, 10)
	}
	return State{Cursor: st.Cursor}
}

// Patch is a partial channel update. AgentID "" clears the agent.
type Patch struct {
	Name          *string
	CredentialRef *string
	AgentID       *string
	Enabled       *bool
	Dispatch      *bool
	AppTokenRef   *string
	ConnectorID   *string
	FromAllow     *[]string
}

// Store is the channel tables' Postgres access. onChange fires after
// every write that affects a running adapter.
type Store struct {
	db       *pgpool.Pool
	onChange func(context.Context)
}

func NewStore(db *pgpool.Pool) *Store {
	return &Store{db: db, onChange: func(context.Context) {}}
}

// SetOnChange registers the post-write hook. Call before serving.
func (s *Store) SetOnChange(fn func(context.Context)) {
	if fn != nil {
		s.onChange = fn
	}
}

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }

// validateName trims name and applies the connectors rule: 1..64
// runes, no control characters.
func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalid("name is required")
	}
	if utf8.RuneCountInString(name) > 64 {
		return "", invalid("name must be at most 64 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", invalid("name must not contain control characters")
		}
	}
	return name, nil
}

func validateCredentialRef(ref string) error {
	if !credentialRefPattern.MatchString(ref) {
		return invalid("credential_ref is required and must be a secret name, never a secret value")
	}
	return nil
}

func validateAppTokenRef(ref string) error {
	if !credentialRefPattern.MatchString(ref) {
		return invalid("config.app_token_ref is required for slack and must be a secret name, never a secret value")
	}
	return nil
}

func validateConnectorID(id string) error {
	if !connectorIDPattern.MatchString(id) {
		return invalid("config.connector_id is required for email and must be an imap connector id")
	}
	return nil
}

// normalizeFromAllow lowercases, trims and dedups an email allowlist:
// 1 to maxFromAllow entries, each a full address or an @domain suffix.
func normalizeFromAllow(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" || seen[e] {
			continue
		}
		if strings.HasPrefix(e, "@") {
			if !domainPattern.MatchString(e[1:]) {
				return nil, invalid("config.from_allow entry " + strconv.Quote(e) + " is not an @domain")
			}
		} else if a, err := mail.ParseAddress(e); err != nil || a.Address != e || strings.Count(e, "@") != 1 {
			return nil, invalid("config.from_allow entry " + strconv.Quote(e) + " is not an email address")
		}
		seen[e] = true
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, invalid("config.from_allow needs at least one address or @domain")
	}
	if len(out) > maxFromAllow {
		return nil, invalid(fmt.Sprintf("config.from_allow allows at most %d entries", maxFromAllow))
	}
	return out, nil
}

// Validate normalizes and checks c for create.
func Validate(c *Channel) error {
	name, err := validateName(c.Name)
	if err != nil {
		return err
	}
	c.Name = name
	if c.Kind != KindEmail && (c.Config.ConnectorID != "" || len(c.Config.FromAllow) > 0) {
		return invalid("config.connector_id and config.from_allow are for email channels only")
	}
	switch c.Kind {
	case KindTelegram:
		if c.Config.AppTokenRef != "" {
			return invalid("config.app_token_ref is for slack channels only")
		}
	case KindSlack:
		if err := validateAppTokenRef(c.Config.AppTokenRef); err != nil {
			return err
		}
	case KindEmail:
		if c.Config.AppTokenRef != "" {
			return invalid("config.app_token_ref is for slack channels only")
		}
		if c.CredentialRef != "" {
			return invalid("credential_ref must be empty for email; the imap connector holds the password")
		}
		if err := validateConnectorID(c.Config.ConnectorID); err != nil {
			return err
		}
		allow, err := normalizeFromAllow(c.Config.FromAllow)
		if err != nil {
			return err
		}
		c.Config.FromAllow = allow
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrKindUnavailable, c.Kind)
	}
	return validateCredentialRef(c.CredentialRef)
}

func isNotFound(err error) bool {
	var pgErr *pgconn.PgError
	return errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && pgErr.Code == "22P02")
}

// mapWriteErr turns the name index and agent foreign key violations
// into sentinel errors.
func mapWriteErr(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && pgErr.ConstraintName == "channels_name_ci":
			return ErrNameConflict
		case pgErr.Code == "23503" && pgErr.ConstraintName == "channels_agent_id_fkey":
			return ErrUnknownAgent
		case pgErr.Code == "22P02":
			return ErrUnknownAgent
		}
	}
	return fmt.Errorf("channels %s: %w", op, err)
}

const channelSelect = `SELECT c.id, c.name, c.kind, c.config, COALESCE(c.credential_ref, ''), COALESCE(c.agent_id::text, ''),
	c.enabled, c.created_at, c.updated_at,
	(SELECT count(*) FROM channel_pairings p WHERE p.channel_id = c.id AND p.status = 'pending'),
	(SELECT count(*) FROM channel_pairings p WHERE p.channel_id = c.id AND p.status = 'approved'),
	(SELECT count(*) FROM channel_pairings p WHERE p.channel_id = c.id AND p.status = 'revoked')
	FROM channels c`

func scanChannel(row pgx.Row) (Channel, error) {
	var c Channel
	var cfg []byte
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &cfg, &c.CredentialRef, &c.AgentID, &c.Enabled, &c.CreatedAt, &c.UpdatedAt,
		&c.Pairings.Pending, &c.Pairings.Approved, &c.Pairings.Revoked); err != nil {
		return Channel{}, err
	}
	_ = json.Unmarshal(cfg, &c.Config)
	return c, nil
}

// List returns every channel by name.
func (s *Store) List(ctx context.Context) ([]Channel, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("channels list: %w", err)
	}
	rows, err := db.Query(ctx, channelSelect+` ORDER BY lower(c.name), c.id`)
	if err != nil {
		return nil, fmt.Errorf("channels list: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Channel, error) { return scanChannel(row) })
	if err != nil {
		return nil, fmt.Errorf("channels list: %w", err)
	}
	return out, nil
}

// Get returns one channel.
func (s *Store) Get(ctx context.Context, id string) (Channel, error) {
	db, err := s.db.Get()
	if err != nil {
		return Channel{}, fmt.Errorf("channels get: %w", err)
	}
	c, err := scanChannel(db.QueryRow(ctx, channelSelect+` WHERE c.id = $1`, id))
	if isNotFound(err) {
		return Channel{}, fmt.Errorf("channel %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Channel{}, fmt.Errorf("channels get: %w", err)
	}
	return c, nil
}

// Create validates c and inserts it.
func (s *Store) Create(ctx context.Context, c Channel) (string, error) {
	if err := Validate(&c); err != nil {
		return "", err
	}
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("channels create: %w", err)
	}
	cfg, err := json.Marshal(c.Config)
	if err != nil {
		return "", fmt.Errorf("channels create: %w", err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO channels (name, kind, config, credential_ref, agent_id, enabled)
		VALUES ($1, $2, $3, $4, NULLIF($5, '')::uuid, $6) RETURNING id`,
		c.Name, c.Kind, cfg, c.CredentialRef, c.AgentID, c.Enabled).Scan(&id)
	if err != nil {
		return "", mapWriteErr("create", err)
	}
	s.onChange(ctx)
	return id, nil
}

// Patch applies p to channel id.
func (s *Store) Patch(ctx context.Context, id string, p Patch) error {
	if p.Name != nil {
		name, err := validateName(*p.Name)
		if err != nil {
			return err
		}
		p.Name = &name
	}
	if p.CredentialRef != nil {
		if err := validateCredentialRef(*p.CredentialRef); err != nil {
			return err
		}
	}
	if p.AppTokenRef != nil {
		if err := validateAppTokenRef(*p.AppTokenRef); err != nil {
			return err
		}
	}
	if p.ConnectorID != nil {
		if err := validateConnectorID(*p.ConnectorID); err != nil {
			return err
		}
	}
	var fromAllow *string
	if p.FromAllow != nil {
		allow, err := normalizeFromAllow(*p.FromAllow)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(allow)
		v := string(raw)
		fromAllow = &v
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("channels patch: %w", err)
	}
	var dispatch *string
	if p.Dispatch != nil {
		v := "false"
		if *p.Dispatch {
			v = "true"
		}
		dispatch = &v
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("channels patch: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var kind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM channels WHERE id = $1 FOR UPDATE`, id).Scan(&kind); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("channel %s: %w", id, ErrNotFound)
		}
		return fmt.Errorf("channels patch: %w", err)
	}
	if p.AppTokenRef != nil && kind != KindSlack {
		return invalid("config.app_token_ref is for slack channels only")
	}
	if (p.ConnectorID != nil || p.FromAllow != nil) && kind != KindEmail {
		return invalid("config.connector_id and config.from_allow are for email channels only")
	}
	if p.CredentialRef != nil && kind == KindEmail {
		return invalid("credential_ref must be empty for email; the imap connector holds the password")
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET
			name = COALESCE($2, name),
			credential_ref = COALESCE($3, credential_ref),
			agent_id = CASE WHEN $4::text IS NULL THEN agent_id ELSE NULLIF($4, '')::uuid END,
			enabled = COALESCE($5, enabled),
			config = CASE WHEN $6::text IS NULL THEN config ELSE jsonb_set(config, '{dispatch}', $6::jsonb) END
				|| CASE WHEN $7::text IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('app_token_ref', $7::text) END
				|| CASE WHEN $8::text IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('connector_id', $8::text) END
				|| CASE WHEN $9::text IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('from_allow', $9::jsonb) END,
			updated_at = now()
		WHERE id = $1`, id, p.Name, p.CredentialRef, p.AgentID, p.Enabled, dispatch, p.AppTokenRef, p.ConnectorID, fromAllow); err != nil {
		return mapWriteErr("patch", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("channels patch: %w", err)
	}
	s.onChange(ctx)
	return nil
}

// Delete removes a channel; pairings, conversations and inbound rows
// cascade, sessions stay.
func (s *Store) Delete(ctx context.Context, id string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("channels delete: %w", err)
	}
	tag, err := db.Exec(ctx, `DELETE FROM channels WHERE id = $1`, id)
	if isNotFound(err) || (err == nil && tag.RowsAffected() == 0) {
		return fmt.Errorf("channel %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("channels delete: %w", err)
	}
	s.onChange(ctx)
	return nil
}

// SetBotUsername records the username a test returned. It leaves
// updated_at alone so running adapters do not restart.
func (s *Store) SetBotUsername(ctx context.Context, id, username string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("channels bot username: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE channels SET config = jsonb_set(config, '{bot_username}', to_jsonb($2::text)) WHERE id = $1`,
		id, username); err != nil {
		return fmt.Errorf("channels bot username: %w", err)
	}
	return nil
}

// GetState reads a channel's adapter state.
func (s *Store) GetState(ctx context.Context, id string) (State, error) {
	db, err := s.db.Get()
	if err != nil {
		return State{}, fmt.Errorf("channels state: %w", err)
	}
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT state FROM channels WHERE id = $1`, id).Scan(&raw); err != nil {
		if isNotFound(err) {
			return State{}, fmt.Errorf("channel %s: %w", id, ErrNotFound)
		}
		return State{}, fmt.Errorf("channels state: %w", err)
	}
	return decodeState(raw), nil
}

// SetState writes a channel's adapter state without touching
// updated_at.
func (s *Store) SetState(ctx context.Context, id string, st State) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("channels set state: %w", err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("channels set state: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE channels SET state = $2 WHERE id = $1`, id, raw); err != nil {
		return fmt.Errorf("channels set state: %w", err)
	}
	return nil
}

const pairingColumns = `channel_id, external_user_id, display_name, status, COALESCE(code, ''), code_expires_at, last_prompt_at, created_at, updated_at`

func scanPairing(row pgx.Row) (Pairing, error) {
	var p Pairing
	err := row.Scan(&p.ChannelID, &p.ExternalUserID, &p.DisplayName, &p.Status, &p.Code, &p.CodeExpiresAt, &p.LastPromptAt, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// EnsurePairing returns the sender's pairing, inserting a pending row
// for a new sender. created reports the insert.
func (s *Store) EnsurePairing(ctx context.Context, channelID, externalUserID, displayName string, now time.Time) (Pairing, bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return Pairing{}, false, fmt.Errorf("channels ensure pairing: %w", err)
	}
	var created bool
	var p Pairing
	err = db.QueryRow(ctx, `INSERT INTO channel_pairings (channel_id, external_user_id, display_name, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'pending', $4, $4)
		ON CONFLICT (channel_id, external_user_id) DO UPDATE SET display_name = EXCLUDED.display_name
		RETURNING `+pairingColumns+`, (xmax = 0)`, channelID, externalUserID, displayName, now).
		Scan(&p.ChannelID, &p.ExternalUserID, &p.DisplayName, &p.Status, &p.Code, &p.CodeExpiresAt, &p.LastPromptAt, &p.CreatedAt, &p.UpdatedAt, &created)
	if err != nil {
		return Pairing{}, false, fmt.Errorf("channels ensure pairing: %w", err)
	}
	return p, created, nil
}

// IssueCode stores a fresh pairing code on a pending sender and stamps
// the prompt time.
func (s *Store) IssueCode(ctx context.Context, channelID, externalUserID, code string, expires, now time.Time) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("channels issue code: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE channel_pairings SET code = $3, code_expires_at = $4, last_prompt_at = $5, updated_at = $5
		WHERE channel_id = $1 AND external_user_id = $2 AND status = 'pending'`,
		channelID, externalUserID, code, expires, now); err != nil {
		return fmt.Errorf("channels issue code: %w", err)
	}
	return nil
}

// RedeemCode approves a pending sender whose unexpired code matches,
// clearing the code so it works once.
func (s *Store) RedeemCode(ctx context.Context, channelID, externalUserID, code string, now time.Time) (bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return false, fmt.Errorf("channels redeem code: %w", err)
	}
	tag, err := db.Exec(ctx, `UPDATE channel_pairings SET status = 'approved', code = NULL, code_expires_at = NULL, updated_at = $4
		WHERE channel_id = $1 AND external_user_id = $2 AND status = 'pending' AND code = $3 AND code_expires_at > $4`,
		channelID, externalUserID, code, now)
	if err != nil {
		return false, fmt.Errorf("channels redeem code: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) setPairingStatus(ctx context.Context, channelID, externalUserID, status string) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("channels pairing %s: %w", status, err)
	}
	tag, err := db.Exec(ctx, `UPDATE channel_pairings SET status = $3, code = NULL, code_expires_at = NULL, updated_at = now()
		WHERE channel_id = $1 AND external_user_id = $2`, channelID, externalUserID, status)
	if isNotFound(err) || (err == nil && tag.RowsAffected() == 0) {
		return fmt.Errorf("pairing %s/%s: %w", channelID, externalUserID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("channels pairing %s: %w", status, err)
	}
	return nil
}

// Approve lets a sender reach the model.
func (s *Store) Approve(ctx context.Context, channelID, externalUserID string) error {
	return s.setPairingStatus(ctx, channelID, externalUserID, StatusApproved)
}

// Revoke silences a sender.
func (s *Store) Revoke(ctx context.Context, channelID, externalUserID string) error {
	return s.setPairingStatus(ctx, channelID, externalUserID, StatusRevoked)
}

// ListPairings returns a channel's senders, pending first.
func (s *Store) ListPairings(ctx context.Context, channelID string) ([]Pairing, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("channels list pairings: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+pairingColumns+` FROM channel_pairings WHERE channel_id = $1
		ORDER BY (status = 'pending') DESC, updated_at DESC, external_user_id`, channelID)
	if err != nil {
		if isNotFound(err) {
			return []Pairing{}, nil
		}
		return nil, fmt.Errorf("channels list pairings: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Pairing, error) { return scanPairing(row) })
	if err != nil {
		if isNotFound(err) {
			return []Pairing{}, nil
		}
		return nil, fmt.Errorf("channels list pairings: %w", err)
	}
	return out, nil
}

const conversationColumns = `id, channel_id, external_chat_id, external_thread_id, external_user_id, session_id, COALESCE(agent_id::text, '')`

func scanConversation(row pgx.Row) (Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.ChannelID, &c.ExternalChatID, &c.ExternalThreadID, &c.ExternalUserID, &c.SessionID, &c.AgentID)
	return c, err
}

// ConversationFor finds the conversation of one external chat and
// thread.
func (s *Store) ConversationFor(ctx context.Context, channelID, chatID, threadID string) (Conversation, bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return Conversation{}, false, fmt.Errorf("channels conversation: %w", err)
	}
	c, err := scanConversation(db.QueryRow(ctx, `SELECT `+conversationColumns+` FROM channel_conversations
		WHERE channel_id = $1 AND external_chat_id = $2 AND external_thread_id = $3`, channelID, chatID, threadID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, false, nil
	}
	if err != nil {
		return Conversation{}, false, fmt.Errorf("channels conversation: %w", err)
	}
	return c, true, nil
}

// ConversationByID returns one conversation.
func (s *Store) ConversationByID(ctx context.Context, id string) (Conversation, error) {
	db, err := s.db.Get()
	if err != nil {
		return Conversation{}, fmt.Errorf("channels conversation: %w", err)
	}
	c, err := scanConversation(db.QueryRow(ctx, `SELECT `+conversationColumns+` FROM channel_conversations WHERE id = $1`, id))
	if isNotFound(err) {
		return Conversation{}, fmt.Errorf("conversation %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("channels conversation: %w", err)
	}
	return c, nil
}

// ConversationIDForSession returns the channel conversation a session
// belongs to, "" for a session outside any channel.
func (s *Store) ConversationIDForSession(ctx context.Context, sessionID string) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("channels conversation for session: %w", err)
	}
	var id string
	err = db.QueryRow(ctx, `SELECT COALESCE(channel_conversation_id::text, '') FROM sessions WHERE id = $1`, sessionID).Scan(&id)
	if isNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("channels conversation for session: %w", err)
	}
	return id, nil
}

// Ask kinds a reply-to answer resolves.
const (
	AskUser = "ask_user"
	AskPlan = "plan"
)

// pendingAsk is one message a reply answers.
type pendingAsk struct {
	MissionID string `json:"mission_id"`
	Kind      string `json:"kind"`
}

// pendingButton is one button of an email message; a reply whose
// first line is Text presses it.
type pendingButton struct {
	Text string `json:"text"`
	Data string `json:"data"`
}

// convState is the typed channel_conversations.state column.
type convState struct {
	Asks map[string]pendingAsk `json:"asks,omitempty"`
	// Buttons holds the buttons of messages sent to transports without
	// them, by message id.
	Buttons map[string][]pendingButton `json:"buttons,omitempty"`
	// Email threading: the original subject, the last inbound
	// Message-ID and its references, the reply address and the last
	// outgoing Message-ID.
	Subject        string   `json:"subject,omitempty"`
	LastMessageID  string   `json:"last_message_id,omitempty"`
	References     []string `json:"references,omitempty"`
	ReplyTo        string   `json:"reply_to,omitempty"`
	LastOutgoingID string   `json:"last_outgoing_id,omitempty"`
}

// remember records an ask, dropping the oldest past maxAsks. Message
// ids order by length then text: Telegram's integers and Slack's
// fixed-width timestamps both sort that way.
func (st *convState) remember(messageID string, a pendingAsk) {
	if st.Asks == nil {
		st.Asks = map[string]pendingAsk{}
	}
	st.Asks[messageID] = a
	for len(st.Asks) > maxAsks {
		oldest := slices.MinFunc(slices.Collect(maps.Keys(st.Asks)), func(a, b string) int {
			return cmp.Or(cmp.Compare(len(a), len(b)), cmp.Compare(a, b))
		})
		delete(st.Asks, oldest)
	}
}

// rememberButtons records a message's buttons, dropping an arbitrary
// entry past maxAsks (email ids carry no order).
func (st *convState) rememberButtons(messageID string, buttons [][]button) {
	if st.Buttons == nil {
		st.Buttons = map[string][]pendingButton{}
	}
	var flat []pendingButton
	for _, row := range buttons {
		for _, b := range row {
			flat = append(flat, pendingButton(b))
		}
	}
	st.Buttons[messageID] = flat
	for k := range st.Buttons {
		if len(st.Buttons) <= maxAsks {
			break
		}
		if k != messageID {
			delete(st.Buttons, k)
		}
	}
}

// takeButton returns the data of messageID's button whose text is
// reply's first line (case-insensitive) and forgets the message's
// buttons.
func (st *convState) takeButton(messageID, reply string) (string, bool) {
	line, _, _ := strings.Cut(strings.TrimSpace(reply), "\n")
	line = strings.TrimSpace(line)
	for _, b := range st.Buttons[messageID] {
		if strings.EqualFold(line, b.Text) {
			delete(st.Buttons, messageID)
			return b.Data, true
		}
	}
	return "", false
}

// take removes and returns the ask of messageID.
func (st *convState) take(messageID string) (pendingAsk, bool) {
	a, ok := st.Asks[messageID]
	if ok {
		delete(st.Asks, messageID)
	}
	return a, ok
}

// updateConvState applies fn to a conversation's state under a row
// lock.
func (s *Store) updateConvState(ctx context.Context, convID string, fn func(*convState)) error {
	db, err := s.db.Get()
	if err != nil {
		return err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT state FROM channel_conversations WHERE id = $1 FOR UPDATE`, convID).Scan(&raw); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("conversation %s: %w", convID, ErrNotFound)
		}
		return err
	}
	var st convState
	_ = json.Unmarshal(raw, &st)
	fn(&st)
	out, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE channel_conversations SET state = $2 WHERE id = $1`, convID, out); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RememberAsk records that a reply to messageID answers a mission's
// ask_user question or plan gate; at most maxAsks per conversation.
func (s *Store) RememberAsk(ctx context.Context, convID, messageID, missionID, kind string) error {
	if err := s.updateConvState(ctx, convID, func(st *convState) {
		st.remember(messageID, pendingAsk{MissionID: missionID, Kind: kind})
	}); err != nil {
		return fmt.Errorf("channels remember ask: %w", err)
	}
	return nil
}

// TakeAsk removes and returns the ask a reply to messageID answers.
func (s *Store) TakeAsk(ctx context.Context, convID, messageID string) (missionID, kind string, ok bool, err error) {
	var a pendingAsk
	if err := s.updateConvState(ctx, convID, func(st *convState) { a, ok = st.take(messageID) }); err != nil {
		return "", "", false, fmt.Errorf("channels take ask: %w", err)
	}
	return a.MissionID, a.Kind, ok, nil
}

// RememberButtons records the buttons of a message sent as text, so a
// reply naming one presses it.
func (s *Store) RememberButtons(ctx context.Context, convID, messageID string, buttons [][]button) error {
	if err := s.updateConvState(ctx, convID, func(st *convState) { st.rememberButtons(messageID, buttons) }); err != nil {
		return fmt.Errorf("channels remember buttons: %w", err)
	}
	return nil
}

// TakeButton returns the data of the button a reply to messageID
// names, forgetting that message's buttons.
func (s *Store) TakeButton(ctx context.Context, convID, messageID, reply string) (string, bool, error) {
	var data string
	var ok bool
	if err := s.updateConvState(ctx, convID, func(st *convState) { data, ok = st.takeButton(messageID, reply) }); err != nil {
		return "", false, fmt.Errorf("channels take button: %w", err)
	}
	return data, ok, nil
}

// mailThread is where an email conversation's next reply goes.
type mailThread struct {
	To, Subject, MessageID string
	References             []string
}

// emailThread reads a conversation's email threading state.
func (s *Store) emailThread(ctx context.Context, conv Conversation) (mailThread, error) {
	db, err := s.db.Get()
	if err != nil {
		return mailThread{}, fmt.Errorf("channels email thread: %w", err)
	}
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT state FROM channel_conversations WHERE id = $1`, conv.ID).Scan(&raw); err != nil {
		return mailThread{}, fmt.Errorf("channels email thread: %w", err)
	}
	var st convState
	_ = json.Unmarshal(raw, &st)
	return mailThread{To: cmp.Or(st.ReplyTo, conv.ExternalUserID), Subject: st.Subject, MessageID: st.LastMessageID, References: st.References}, nil
}

// saveEmailThread records the thread a reply went to and its
// Message-ID.
func (s *Store) saveEmailThread(ctx context.Context, convID string, th mailThread, outgoingID string) error {
	if err := s.updateConvState(ctx, convID, func(st *convState) {
		st.ReplyTo, st.Subject, st.LastMessageID, st.References, st.LastOutgoingID = th.To, th.Subject, th.MessageID, th.References, outgoingID
	}); err != nil {
		return fmt.Errorf("channels save email thread: %w", err)
	}
	return nil
}

// CreateConversation creates the conversation and its session
// (origin_kind channel, session_started event) in one transaction.
func (s *Store) CreateConversation(ctx context.Context, c Conversation, title string) (Conversation, error) {
	db, err := s.db.Get()
	if err != nil {
		return Conversation{}, fmt.Errorf("channels create conversation: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return Conversation{}, fmt.Errorf("channels create conversation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&c.ID); err != nil {
		return Conversation{}, fmt.Errorf("channels create conversation: %w", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO sessions (title, origin_kind, channel_conversation_id)
		VALUES (NULLIF($1, ''), 'channel', $2) RETURNING id`, title, c.ID).Scan(&c.SessionID); err != nil {
		return Conversation{}, fmt.Errorf("channels create conversation session: %w", err)
	}
	payload, err := json.Marshal(session.SessionStarted{Title: title})
	if err != nil {
		return Conversation{}, fmt.Errorf("channels create conversation: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_events (session_id, seq, kind, payload) VALUES ($1, 1, $2, $3)`,
		c.SessionID, session.KindSessionStarted, payload); err != nil {
		return Conversation{}, fmt.Errorf("channels create conversation event: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO channel_conversations
		(id, channel_id, external_chat_id, external_thread_id, external_user_id, session_id, agent_id)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid)`,
		c.ID, c.ChannelID, c.ExternalChatID, c.ExternalThreadID, c.ExternalUserID, c.SessionID, c.AgentID); err != nil {
		return Conversation{}, fmt.Errorf("channels create conversation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Conversation{}, fmt.Errorf("channels create conversation: %w", err)
	}
	return c, nil
}

// TouchConversation stamps the last message time.
func (s *Store) TouchConversation(ctx context.Context, id string, at time.Time) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("channels touch conversation: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE channel_conversations SET last_message_at = $2 WHERE id = $1`, id, at); err != nil {
		return fmt.Errorf("channels touch conversation: %w", err)
	}
	return nil
}

// MarkInbound records an external message id; fresh is false for a
// repeat.
func (s *Store) MarkInbound(ctx context.Context, channelID, externalMessageID string) (bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return false, fmt.Errorf("channels mark inbound: %w", err)
	}
	tag, err := db.Exec(ctx, `INSERT INTO channel_inbound (channel_id, external_message_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, channelID, externalMessageID)
	if err != nil {
		return false, fmt.Errorf("channels mark inbound: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Sweep deletes inbound dedup rows older than 7 days and pending
// pairings idle for a day whose code has expired.
func (s *Store) Sweep(ctx context.Context, now time.Time) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("channels sweep: %w", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM channel_inbound WHERE received_at < $1`, now.Add(-7*24*time.Hour)); err != nil {
		return fmt.Errorf("channels sweep inbound: %w", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM channel_pairings WHERE status = 'pending' AND updated_at < $1
		AND (code_expires_at IS NULL OR code_expires_at < $2)`, now.Add(-24*time.Hour), now); err != nil {
		return fmt.Errorf("channels sweep pairings: %w", err)
	}
	return nil
}
