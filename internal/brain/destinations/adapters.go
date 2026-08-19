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
type Adapter interface {
	Deliver(ctx context.Context, config json.RawMessage, payload Payload) error
}

// mailSender is the narrow slice of *connectors.Google the email
// adapter needs — an interface so this package never imports
// connectors, mirroring the rest of destinations' dependency
// direction (connectorLookup above).
type mailSender interface {
	SendMail(ctx context.Context, connectorID, to, subject, body string) error
}

// EmailAdapter sends a destination's payload via the SAME Gmail send
// path gmail_send's tool uses (connectors.Google.SendMail) — never the
// tool-execution layer, so a destination's delivery never depends on
// the agent loop or permission chain.
type EmailAdapter struct {
	Mail mailSender
}

func (a *EmailAdapter) Deliver(ctx context.Context, config json.RawMessage, payload Payload) error {
	var cfg EmailConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return fmt.Errorf("email adapter: config: %w", err)
	}
	subject := "Timothy mission: " + payload.Name
	return a.Mail.SendMail(ctx, cfg.ConnectorID, cfg.To, subject, renderText(payload))
}

// WebhookAdapter POSTs a destination's payload as JSON or plain text
// per config.format. credential_ref-based Authorization is
// deliberately OMITTED in this slice: brain has no secret-value
// resolution path of its own for arbitrary credential refs (secrets
// resolve gateway/connector-side); wiring one here would mean
// inventing a new resolution path rather than reusing an existing one.
// Revisit if a real need for authenticated webhooks shows up.
type WebhookAdapter struct {
	HTTP *http.Client
}

func (a *WebhookAdapter) Deliver(ctx context.Context, config json.RawMessage, payload Payload) error {
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
// a text-format webhook.
func renderText(p Payload) string {
	out := p.Body
	if len(p.Links) > 0 {
		out += "\n\nLinks:\n"
		for _, l := range p.Links {
			out += "- " + l + "\n"
		}
	}
	return out
}
