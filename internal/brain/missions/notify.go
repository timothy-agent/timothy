package missions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

const webhookTimeout = 10 * time.Second

// Notifier fires ONLY on actionable transitions (waiting_for_input,
// paused, done, error) and only exactly on the transition INTO that
// state — re-kicking an already-paused mission stays silent.
type Notifier struct {
	db         *pgpool.Pool
	webhookURL string // NOTIFY_WEBHOOK_URL; empty disables fan-out, inbox row still always written
	log        *slog.Logger
	http       *http.Client
	hub        *Hub
}

func NewNotifier(db *pgpool.Pool, webhookURL string, log *slog.Logger) *Notifier {
	return &Notifier{db: db, webhookURL: webhookURL, log: log, http: &http.Client{Timeout: webhookTimeout}}
}

// SetHub wires the push-notification hub; nil (the default) makes
// OnTransition's publish a no-op, same nil-safe convention as
// Store.SetHub.
func (n *Notifier) SetHub(hub *Hub) {
	n.hub = hub
}

// isActionableTransition is the pure classification Driver's
// before/after Status pair goes through: only these four destination
// states are actionable, and only when the mission just arrived there
// this call (a repeat Advance on an already-paused mission reports the
// SAME before/after, so it's silent by construction).
func isActionableTransition(before, after Status) (kind string, ok bool) {
	if before == after {
		return "", false
	}
	switch after {
	case StatusWaitingForInput, StatusPaused, StatusDone, StatusError:
		return string(after), true
	default:
		return "", false
	}
}

// composeMessage builds the operator-facing notification text for an
// actionable transition: the mission's display title (PRTitle's own
// name-vs-truncated-goal fallback, reused here so the two surfaces
// never diverge) plus wording keyed on kind, with "failed" replaced by
// "cancelled" when reason is the cancel state machine's own marker
// (see statemachine.go's InputCancel branch, the only place that
// reason is set).
func composeMessage(kind, title, reason string) string {
	switch kind {
	case string(StatusDone):
		return fmt.Sprintf("Mission - %s is done", title)
	case string(StatusError):
		if reason == "cancelled" {
			return fmt.Sprintf("Mission - %s is cancelled", title)
		}
		return fmt.Sprintf("Mission - %s is failed", title)
	case string(StatusPaused):
		return fmt.Sprintf("Mission - %s is paused, needs your intervention.", title)
	case string(StatusWaitingForInput):
		return fmt.Sprintf("Mission - %s is waiting for your input.", title)
	default:
		return fmt.Sprintf("Mission - %s is now %s", title, kind)
	}
}

// OnTransition is called by Driver after ApplyTransition succeeds, with
// the mission (for its title), the before/after Status, and — only
// populated on a transition into StatusError — the mission.failed
// event's reason (e.g. "cancelled"), read from the same Transition.Events
// the driver already has in hand rather than a second query. Durable
// channel: a notifications row is always written for a qualifying
// transition. Best-effort fan-out: webhook POST of a generic JSON
// payload; failure only logs, never blocks or loses the notification.
func (n *Notifier) OnTransition(ctx context.Context, m Mission, before, after Status, reason string) error {
	kind, ok := isActionableTransition(before, after)
	if !ok {
		return nil
	}
	message := composeMessage(kind, PRTitle(m), reason)
	if err := n.sendOncePerMission(ctx, m.ID, kind, message); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	n.fanOut(ctx, m.ID, kind, message)
	return nil
}

// sendOncePerMission dedupes by "already has an unread notification of
// this kind" — checked inside the same insert (via a NOT EXISTS
// subquery), not a separate pre-check, to avoid a TOCTOU race between
// two concurrent Advance calls for the same mission. RETURNING id
// yields pgx.ErrNoRows exactly when the NOT EXISTS guard suppressed
// the insert (already an unread row of this kind) — not a failure,
// just "nothing to publish."
func (n *Notifier) sendOncePerMission(ctx context.Context, missionID, kind, message string) error {
	db, err := n.db.Get()
	if err != nil {
		return fmt.Errorf("get pool: %w", err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO notifications (mission_id, kind, message)
		SELECT $1, $2, $3
		WHERE NOT EXISTS (
			SELECT 1 FROM notifications WHERE mission_id = $1 AND kind = $2 AND NOT read
		) RETURNING id`, missionID, kind, message).Scan(&id)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return fmt.Errorf("insert: %w", err)
	}
	if n.hub != nil {
		n.hub.Publish(Signal{Kind: "notification", ID: id})
	}
	return nil
}

// fanOut posts a generic JSON payload to webhookURL for ntfy/Slack/
// Telegram-style receivers. Best-effort: a failure only logs, it never
// blocks the caller or drops the durable inbox row already written.
func (n *Notifier) fanOut(ctx context.Context, missionID, kind, message string) {
	if n.webhookURL == "" {
		return
	}
	body, err := json.Marshal(map[string]string{
		"mission_id": missionID, "kind": kind, "message": message,
	})
	if err != nil {
		n.log.Warn("notify: webhook marshal failed", "error", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		n.log.Warn("notify: webhook request build failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		n.log.Warn("notify: webhook post failed", "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		n.log.Warn("notify: webhook returned non-2xx", "status", resp.StatusCode)
	}
}

// NotifyMessage fires a notification for an event OnTransition's own
// before/after Status classification doesn't cover — used by the
// driver's on_complete auto-fire hook (fireOnComplete) to surface a
// failed automatic push/PR, the same durable-inbox-row-plus-best-effort-
// webhook shape OnTransition itself uses.
func (n *Notifier) NotifyMessage(ctx context.Context, missionID, kind, message string) error {
	if err := n.sendOncePerMission(ctx, missionID, kind, message); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	n.fanOut(ctx, missionID, kind, message)
	return nil
}

// ClearMission marks unread rows read once the mission advances past
// waiting_for_input/paused — stale "waiting for input" rows don't
// linger once the situation's resolved.
func (n *Notifier) ClearMission(ctx context.Context, missionID string) error {
	db, err := n.db.Get()
	if err != nil {
		return fmt.Errorf("notify: clear: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE notifications SET read = true WHERE mission_id = $1 AND NOT read`, missionID); err != nil {
		return fmt.Errorf("notify: clear: %w", err)
	}
	return nil
}

// List returns unread-first notifications across all missions.
func (n *Notifier) List(ctx context.Context) ([]Notification, error) {
	db, err := n.db.Get()
	if err != nil {
		return nil, fmt.Errorf("notify: list: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT id, mission_id, kind, message, read, created_at
		FROM notifications ORDER BY read, created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("notify: list: %w", err)
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var note Notification
		if err := rows.Scan(&note.ID, &note.MissionID, &note.Kind, &note.Message, &note.Read, &note.CreatedAt); err != nil {
			return nil, fmt.Errorf("notify: list: %w", err)
		}
		out = append(out, note)
	}
	return out, rows.Err()
}

// MarkRead marks one notification read (idempotent).
func (n *Notifier) MarkRead(ctx context.Context, id string) error {
	db, err := n.db.Get()
	if err != nil {
		return fmt.Errorf("notify: mark read: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE notifications SET read = true WHERE id = $1`, id); err != nil {
		return fmt.Errorf("notify: mark read: %w", err)
	}
	return nil
}
