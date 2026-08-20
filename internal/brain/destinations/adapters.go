package destinations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// webhookTimeout bounds one webhook POST — matches missions/notify.go's
// own webhookTimeout for the same reasoning (a slow/unreachable
// receiver must never stall the caller).
const webhookTimeout = 10 * time.Second

// Adapter delivers one rendered Payload to a destination's config.
// credentialRef is the destination's stored credential_ref name (never
// a resolved value) — only TelegramAdapter uses it; email/webhook
// ignore it (email rides its connector's own auth, webhook has none in
// this slice).
type Adapter interface {
	Deliver(ctx context.Context, config json.RawMessage, credentialRef string, payload Payload) error
}

// MailAttachment mirrors connectors.Attachment — a local copy so this
// package never imports connectors, mirroring the rest of
// destinations' dependency direction (connectorLookup above).
type MailAttachment struct {
	Name string
	Data []byte
}

// mailSender is the narrow slice of *connectors.Google the email
// adapter needs — an interface so this package never imports
// connectors, mirroring the rest of destinations' dependency
// direction (connectorLookup above).
type mailSender interface {
	SendMail(ctx context.Context, connectorID, to, subject, body string) error
	SendMailWithAttachments(ctx context.Context, connectorID, to, subject, body string, attachments []MailAttachment) error
}

// EmailAdapter sends a destination's payload via the SAME Gmail send
// path gmail_send's tool uses (connectors.Google.SendMail /
// SendMailWithAttachments) — never the tool-execution layer, so a
// destination's delivery never depends on the agent loop or
// permission chain.
type EmailAdapter struct {
	Mail mailSender
}

func (a *EmailAdapter) Deliver(ctx context.Context, config json.RawMessage, _ string, payload Payload) error {
	var cfg EmailConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return fmt.Errorf("email adapter: config: %w", err)
	}
	subject := "Timothy mission: " + payload.Name
	if len(payload.Files) == 0 {
		return a.Mail.SendMail(ctx, cfg.ConnectorID, cfg.To, subject, renderText(payload))
	}
	attachments := make([]MailAttachment, len(payload.Files))
	for i, f := range payload.Files {
		attachments[i] = MailAttachment(f)
	}
	return a.Mail.SendMailWithAttachments(ctx, cfg.ConnectorID, cfg.To, subject, renderText(payload), attachments)
}

// WebhookAdapter POSTs a destination's payload as JSON or plain text
// per config.format. credential_ref-based Authorization is
// deliberately OMITTED in this slice: the plan named no header config
// for it yet (see 2026-08-19-destinations-plan.md's open questions),
// not a resolution-path limitation — brain's own secret store
// (secretstore.Store.Resolve) is already used directly elsewhere
// (connectors, telegram below). Revisit if a real need for
// authenticated webhooks shows up.
type WebhookAdapter struct {
	HTTP *http.Client
}

func (a *WebhookAdapter) Deliver(ctx context.Context, config json.RawMessage, _ string, payload Payload) error {
	var cfg WebhookConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return fmt.Errorf("webhook adapter: config: %w", err)
	}
	var body []byte
	contentType := "text/plain; charset=UTF-8"
	if cfg.Format == "json" {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("webhook adapter: marshal: %w", err)
		}
		body = data
		contentType = "application/json"
	} else {
		body = []byte(renderText(payload))
	}
	cctx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook adapter: request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	client := a.HTTP
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook adapter: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook adapter: status %d", resp.StatusCode)
	}
	return nil
}

// renderText is the plain-text rendering shared by the email body and
// a text-format webhook. Oversize artifact names are listed here for
// every kind — a webhook never receives the files themselves, but
// still gets to know they exist.
func renderText(p Payload) string {
	out := p.Body
	if len(p.Links) > 0 {
		out += "\n\nLinks:\n"
		for _, l := range p.Links {
			out += "- " + l + "\n"
		}
	}
	out += oversizeNotice(p.OversizeFiles)
	return out
}
