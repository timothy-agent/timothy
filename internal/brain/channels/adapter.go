package channels

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
)

// adapter is one chat transport; telegram, slack and email implement
// it.
type adapter interface {
	// connect verifies the credentials and returns the bot identity.
	connect(ctx context.Context) (identity, error)
	// receive blocks for the next batch of inbound items (a long poll,
	// a socket envelope) and returns the cursor to commit afterwards.
	receive(ctx context.Context, cursor string) ([]inbound, string, error)
	// send posts text with optional buttons; silent asks for no
	// notification where the transport supports it.
	send(ctx context.Context, to target, text string, buttons [][]button, silent bool) (messageID string, err error)
	// edit replaces a sent message's text and buttons; nil buttons
	// removes them.
	edit(ctx context.Context, to target, messageID, text string, buttons [][]button) error
	// answerPress closes a button press on transports that need it.
	answerPress(ctx context.Context, pressID, text string) error
	caps() capabilities
}

// capabilities are a transport's reply limits.
type capabilities struct {
	// Edits is false for transports whose messages cannot change: the
	// runner sends no placeholder and no streaming edits, only the
	// final reply.
	Edits bool
	// Buttons is false for transports without inline buttons: the
	// runner renders them as a reply hint and a reply naming one
	// presses it.
	Buttons bool
	// MessageLimit caps one message in UTF-16 units.
	MessageLimit int
	// EditEvery is the streaming edit cadence.
	EditEvery time.Duration
}

// target is where a message goes: a chat and, for threaded chats, a
// thread.
type target struct{ ChatID, ThreadID string }

// button is one inline button; Data comes back on a press.
type button struct{ Text, Data string }

// identity is the bot's own user.
type identity struct{ ID, Username string }

// inbound is one transport-neutral item the pipeline acts on: a
// message, or a button press when Press is set.
type inbound struct {
	// DedupID is the channel_inbound key, prefixed by kind.
	DedupID     string
	MessageID   string
	UserID      string
	DisplayName string
	ChatID      string
	ThreadID    string
	Private     bool
	// Addressed is true for private chats and for group messages that
	// mention or reply to the bot.
	Addressed bool
	// MaybeThread marks an unaddressed thread reply; it counts as
	// addressed when the thread already has a conversation.
	MaybeThread bool
	Text        string
	// ReplyToID is the message this one replies to, "" for none.
	ReplyToID string
	// Attachments are stored files the message carried (email).
	Attachments []chat.AttachmentRef
	Press       *press
}

// press is a button press. ID closes it via answerPress; MessageID is
// the message holding the button.
type press struct {
	ID          string
	Data        string
	MessageID   string
	MessageText string
}

// conversationKey is the conversation (and reply target) a message
// maps to: private chats by chat, group chats by chat and thread.
func conversationKey(in inbound) target {
	if in.Private {
		return target{ChatID: in.ChatID}
	}
	return target{ChatID: in.ChatID, ThreadID: in.ThreadID}
}

// conversationTarget is where a conversation's messages go.
func conversationTarget(c Conversation) target {
	return target{ChatID: c.ExternalChatID, ThreadID: c.ExternalThreadID}
}

// kindLabel names a kind in session titles.
func kindLabel(kind string) string {
	switch kind {
	case KindSlack:
		return "Slack"
	case KindEmail:
		return "Email"
	default:
		return "Telegram"
	}
}

// newAdapter builds the transport of channel c. The API bases are test
// overrides; empty uses the real APIs. mail wires email channels; nil
// or without a mailbox leaves them unavailable.
func newAdapter(c Channel, client *http.Client, resolve func(ctx context.Context, ref string) (string, error), telegramBase, slackBase string, mail *emailEnv) (adapter, error) {
	switch c.Kind {
	case KindTelegram:
		if telegramBase == "" {
			telegramBase = defaultAPIBase
		}
		return &telegramAdapter{api: &botAPI{http: client, base: telegramBase, resolve: resolve, ref: c.CredentialRef}}, nil
	case KindSlack:
		if slackBase == "" {
			slackBase = defaultSlackBase
		}
		return &slackAdapter{http: client, base: slackBase, resolve: resolve, botRef: c.CredentialRef, appRef: c.Config.AppTokenRef}, nil
	case KindEmail:
		if mail != nil && mail.mailbox != nil {
			return newEmailAdapter(c, *mail), nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrKindUnavailable, c.Kind)
}
