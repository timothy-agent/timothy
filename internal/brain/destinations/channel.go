package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/channels"
	"github.com/SumonMSelim/timothy/internal/platform/redact"
)

// ChannelAdapter delivers a channel destination through its channel's
// transport: the Telegram Bot API, the Slack Web API or the email
// channel's mailbox. Credentials come from the channel row, never the
// destination.
type ChannelAdapter struct {
	Lookup       ChannelLookup
	ResolveToken tokenResolver
	HTTP         *http.Client
	// TelegramBase and SlackBase override the API base URLs for tests;
	// empty uses the real APIs.
	TelegramBase string
	SlackBase    string
	// SendMail sends one new mail from an imap connector's mailbox; nil
	// leaves email channels undeliverable.
	SendMail func(ctx context.Context, connectorID, to, subject, body string, files []File) error
	// Check runs a channel's connect check for Test; nil skips it.
	Check func(ctx context.Context, channelID string) error
}

func (a *ChannelAdapter) channel(ctx context.Context, config json.RawMessage) (ChannelConfig, ChannelRef, error) {
	var cfg ChannelConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return cfg, ChannelRef{}, fmt.Errorf("channel adapter: config: %w", err)
	}
	if a.Lookup == nil {
		return cfg, ChannelRef{}, errors.New("channel adapter: channels are not enabled")
	}
	c, err := a.Lookup(ctx, cfg.ChannelID)
	if err != nil {
		return cfg, ChannelRef{}, fmt.Errorf("channel adapter: channel %s: %w", cfg.ChannelID, err)
	}
	return cfg, c, nil
}

// Deliver sends payload through the channel. A disabled channel still
// delivers: enabled governs inbound polling only.
func (a *ChannelAdapter) Deliver(ctx context.Context, config json.RawMessage, _ string, payload Payload) error {
	cfg, c, err := a.channel(ctx, config)
	if err != nil {
		return err
	}
	switch c.Kind {
	case ChannelTelegram:
		if a.ResolveToken == nil || c.CredentialRef == "" {
			return errors.New("channel adapter: telegram channel has no bot token")
		}
		token, err := a.ResolveToken(ctx, c.CredentialRef)
		if err != nil {
			return fmt.Errorf("channel adapter: resolve bot token: %w", err)
		}
		return (&telegramSender{HTTP: a.HTTP, APIBase: a.TelegramBase}).deliver(ctx, token, cfg.ChatID, cfg.ThreadID, payload)
	case ChannelSlack:
		if a.ResolveToken == nil || c.CredentialRef == "" {
			return errors.New("channel adapter: slack channel has no bot token")
		}
		client := a.HTTP
		if client == nil {
			client = &http.Client{Timeout: telegramTimeout}
		}
		return slackSender{channels.NewSlackClient(client, a.SlackBase, a.ResolveToken, c.CredentialRef)}.deliver(ctx, cfg.ChatID, cfg.ThreadID, payload)
	case ChannelEmail:
		if a.SendMail == nil {
			return errors.New("channel adapter: email channels are not enabled")
		}
		subject, body := mailContent(payload)
		if err := a.SendMail(ctx, c.ConnectorID, cfg.To, subject, body, payload.Files); err != nil {
			// Delivery errors reach logs; the recipient must not.
			return redact.Token(fmt.Errorf("channel adapter: send mail: %w", err), cfg.To)
		}
		return nil
	default:
		return fmt.Errorf("channel adapter: channel kind %q cannot deliver", c.Kind)
	}
}

// Test checks the channel resolves and, for Telegram and Slack, that
// its credentials connect. Nothing is sent.
func (a *ChannelAdapter) Test(ctx context.Context, config json.RawMessage) error {
	cfg, c, err := a.channel(ctx, config)
	if err != nil {
		return err
	}
	if (c.Kind == ChannelTelegram || c.Kind == ChannelSlack) && a.Check != nil {
		return a.Check(ctx, cfg.ChannelID)
	}
	return nil
}

// mailContent is an email channel delivery's subject and plain-text
// body, the email destination's: the completion line, links and
// oversize notice, then any text artifacts.
func mailContent(p Payload) (subject, body string) {
	subject = p.Subject
	if subject == "" {
		subject = "Timothy mission: " + p.Name
	}
	body = renderText(p)
	if len(p.TextArtifacts) > 0 {
		body += "\n\n" + renderTextArtifactsPlain(p.TextArtifacts)
	}
	return subject, body
}
