package channels

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// Outcomes is the events consumer that reports a channel mission's
// done or failed outcome to its conversation.
type Outcomes struct {
	store   *Store
	deps    MissionDeps
	resolve func(ctx context.Context, ref string) (string, error)
	http    *http.Client
	enabled func(context.Context) bool
	log     *slog.Logger
	// APIBase and SlackAPIBase override the transport API base URLs
	// for tests.
	APIBase      string
	SlackAPIBase string
	// mail wires email channels; SetEmail fills its mailbox.
	mail emailEnv
}

// NewOutcomes wires the consumer; deps.Get is required, Events and
// WebBaseURL are optional. enabled is the channels switch (nil sends
// always).
func NewOutcomes(store *Store, deps MissionDeps, resolveSecret func(ctx context.Context, ref string) (string, error), client *http.Client, enabled func(context.Context) bool, log *slog.Logger) *Outcomes {
	return &Outcomes{store: store, deps: deps, resolve: resolveSecret, http: client, enabled: enabled, log: log,
		mail: emailEnv{store: store, log: log}}
}

// SetEmail lets outcomes reach email channels through open.
func (o *Outcomes) SetEmail(open func(ctx context.Context, id string) (*connectors.IMAPMailbox, error)) {
	o.mail.mailbox = openMailbox(open)
}

func (*Outcomes) Name() string { return "channels" }

func (*Outcomes) Kinds() []string { return []string{events.KindMissionDone, events.KindMissionFailed} }

// Handle sends one outcome message. A transport 4xx (chat gone, bot
// blocked) logs and returns nil; transport failures and 429 return the
// error so the drainer retries.
func (o *Outcomes) Handle(ctx context.Context, _ pgx.Tx, ev events.Event) error {
	p, err := events.DecodeMission(ev)
	if err != nil {
		return err
	}
	m, err := o.deps.Get(ctx, p.MissionID)
	if errors.Is(err, missions.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if m.ChannelConversationID == "" {
		return nil
	}
	conv, err := o.store.ConversationByID(ctx, m.ChannelConversationID)
	if errors.Is(err, ErrNotFound) {
		o.log.Warn("channels: outcome conversation missing", "mission_id", m.ID)
		return nil
	}
	if err != nil {
		return err
	}
	ch, err := o.store.Get(ctx, conv.ChannelID)
	if errors.Is(err, ErrNotFound) {
		o.log.Warn("channels: outcome channel missing", "mission_id", m.ID)
		return nil
	}
	if err != nil {
		return err
	}
	if !ch.Enabled || (o.enabled != nil && !o.enabled(ctx)) {
		o.log.Info("channels: outcome not sent, channel disabled", "channel_id", ch.ID, "mission_id", m.ID)
		return nil
	}
	ad, err := newAdapter(ch, o.http, o.resolve, o.APIBase, o.SlackAPIBase, &o.mail)
	if err != nil {
		o.log.Warn("channels: outcome channel unsupported", "channel_id", ch.ID, "error", err)
		return nil
	}
	reason := p.Reason
	if reason == "" {
		reason = m.FailureReason
	}
	terminal := missions.PhaseDone
	if ev.Kind == events.KindMissionFailed {
		terminal = missions.PhaseFailed
	}
	var evs []missions.Event
	if o.deps.Events != nil {
		if evs, err = o.deps.Events(ctx, m.ID); err != nil {
			o.log.Warn("channels: outcome events failed", "mission_id", m.ID, "error", err)
		}
	}
	base := ""
	if o.deps.WebBaseURL != nil {
		base = o.deps.WebBaseURL(ctx)
	}
	text := outcomeText(m, terminal, reason, missions.OutcomeDigest(m, evs, terminal, reason), base)
	if _, err := ad.send(ctx, conversationTarget(conv), text, nil, false); err != nil {
		if errors.Is(err, errMailBudget) {
			o.log.Warn("channels: outcome dropped, mail budget spent", "channel_id", ch.ID, "mission_id", m.ID)
			return nil
		}
		if st := apiStatus(err); st >= 400 && st < 500 && st != http.StatusTooManyRequests {
			o.log.Warn("channels: outcome rejected", "channel_id", ch.ID, "kind", ch.Kind, "mission_id", m.ID, "error", err)
			return nil
		}
		return err
	}
	o.log.Info("channels: outcome sent", "channel_id", ch.ID, "conversation_id", conv.ID, "mission_id", m.ID, "kind", ev.Kind)
	return nil
}

// outcomeText is the outcome message: headline, capped digest and the
// web link when base is set.
func outcomeText(m missions.Mission, terminal missions.Phase, reason, digest, base string) string {
	title := missions.PRTitle(m)
	var head string
	switch {
	case terminal == missions.PhaseDone:
		head = "Mission " + title + " is done"
	case reason == "cancelled":
		head = "Mission " + title + " is cancelled"
	case reason != "":
		head = "Mission " + title + " failed: " + capRunes(reason, failureCap)
	default:
		head = "Mission " + title + " failed"
	}
	text := head
	if d := strings.TrimSpace(digest); d != "" {
		text += "\n\n" + capRunes(d, digestCap)
	}
	if base = strings.TrimRight(base, "/"); base != "" {
		text += "\n\n" + base + "/missions/" + m.ID
	}
	return text
}
