package destinations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// telegramTimeout bounds one Telegram Bot API call — same reasoning as
// webhookTimeout. 30s, not 10s: a slow path to api.telegram.org (seen
// on the homelab VM) hit 10s AFTER Telegram had already accepted the
// send, so every retry re-sent an already-delivered message.
const telegramTimeout = 30 * time.Second

// telegramMessageLimit is the Bot API's sendMessage text cap. A
// message over this is truncated with a notice + mission link
// appended, never rejected.
const telegramMessageLimit = 4096

// tokenResolver resolves a credential_ref to its secret value. Brain's
// own secretstore.Store.Resolve satisfies this directly, the same way
// connectors and missions already resolve their own credential_refs —
// there is no gateway hop for this (see D-062 in adapters.go/deliver.go
// comments for the fuller reasoning).
type tokenResolver func(ctx context.Context, refName string) (string, error)

// TelegramAdapter sends a destination's payload via the Telegram Bot
// API: sendMessage for the body (MarkdownV2, truncated to the API's
// 4096-char cap), sendDocument for each attached file.
type TelegramAdapter struct {
	ResolveToken tokenResolver
	HTTP         *http.Client
	// APIBase overrides the Bot API's base URL for tests; empty uses
	// the real API.
	APIBase string
}

func (a *TelegramAdapter) apiBase() string {
	if a.APIBase != "" {
		return a.APIBase
	}
	return "https://api.telegram.org"
}

func (a *TelegramAdapter) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: telegramTimeout}
}

func (a *TelegramAdapter) Deliver(ctx context.Context, config json.RawMessage, credentialRef string, payload Payload) error {
	var cfg TelegramConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return fmt.Errorf("telegram adapter: config: %w", err)
	}
	if a.ResolveToken == nil {
		return fmt.Errorf("telegram adapter: no token resolver configured")
	}
	if credentialRef == "" {
		return fmt.Errorf("telegram adapter: destination has no credential_ref")
	}
	token, err := a.ResolveToken(ctx, credentialRef)
	if err != nil {
		return fmt.Errorf("telegram adapter: resolve bot token: %w", err)
	}

	// Recipients want the mission's generated output, not a separate
	// process digest alongside it — three cases, in priority order:
	// text artifacts (.md/.txt) render inline as formatted, chunked
	// MarkdownV2 messages (the bold title+date heads the first chunk);
	// otherwise, non-text artifacts attach as files, with the same
	// title+date on the first file's caption; only when there's neither
	// does the plain completion-line message stand alone.
	switch {
	case len(payload.TextArtifacts) > 0:
		if err := a.sendTextArtifacts(ctx, token, cfg.ChatID, payload); err != nil {
			return fmt.Errorf("telegram adapter: send text artifacts: %w", err)
		}
	case len(payload.Files) > 0:
		caption := renderTelegramCaption(payload)
		for i, f := range payload.Files {
			c := ""
			if i == 0 {
				c = caption
			}
			if err := a.sendDocument(ctx, token, cfg.ChatID, f, c); err != nil {
				return fmt.Errorf("telegram adapter: send document %s: %w", f.Name, err)
			}
		}
	default:
		if err := a.sendTelegramText(ctx, token, cfg.ChatID, payload); err != nil {
			return fmt.Errorf("telegram adapter: send message: %w", err)
		}
	}
	return nil
}

// sendTextArtifactSeparator visually separates consecutive text
// artifacts within the same delivery when there's more than one —
// each gets its own name heading, joined into one render pass so
// chunking treats the whole set as one flow of blocks rather than
// resetting per artifact (which could otherwise waste a chunk on a
// single short trailing artifact).
const sendTextArtifactSeparator = "\n\n---\n\n"

// sendTextArtifacts renders every .md/.txt artifact as MarkdownV2,
// heads the whole rendered content with the bold title + completion
// date (same content the file-caption path would show), and sends it
// as one or more sendMessage calls via ChunkMarkdownV2 — never a file
// attachment, so a digest reads directly in the chat.
func (a *TelegramAdapter) sendTextArtifacts(ctx context.Context, token, chatID string, payload Payload) error {
	var sourceParts []string
	for i, ta := range payload.TextArtifacts {
		if i > 0 {
			sourceParts = append(sourceParts, sendTextArtifactSeparator)
		}
		if len(payload.TextArtifacts) > 1 {
			sourceParts = append(sourceParts, "## "+ta.Name+"\n\n")
		}
		sourceParts = append(sourceParts, ta.Content)
	}
	rendered := RenderMarkdownV2(strings.Join(sourceParts, ""))
	header := renderTelegramCaption(payload)
	full := rendered
	if header != "" {
		full = header + "\n\n" + rendered
	}
	chunks := ChunkMarkdownV2(full, telegramMessageLimit)
	for _, chunk := range chunks {
		if err := a.sendMessage(ctx, token, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// telegramCaptionLimit is the Bot API's sendDocument caption cap.
const telegramCaptionLimit = 1024

// renderTelegramCaption builds the bold title + completion date shown
// above the first attached file — MarkdownV2, escaped. CompletedAt
// zero (the ad-hoc deliver tool's payload, which never sets it) omits
// the date line rather than guessing "now"; otherwise it renders in
// whatever location Render already converted it into, "MST" showing
// that zone's abbreviation instead of a hardcoded "UTC".
func renderTelegramCaption(p Payload) string {
	var b strings.Builder
	b.WriteString("*")
	b.WriteString(escapeMarkdownV2(p.Name))
	b.WriteString("*")
	if !p.CompletedAt.IsZero() {
		b.WriteString("\n")
		b.WriteString(escapeMarkdownV2(p.CompletedAt.Format("2 Jan 2006, 15:04 MST")))
	}
	full := b.String()
	if len(full) <= telegramCaptionLimit {
		return full
	}
	cut := 0
	for i, r := range full {
		if i+len(string(r)) > telegramCaptionLimit {
			break
		}
		cut = i + len(string(r))
	}
	return full[:cut]
}

// sendTelegramText renders a body-only delivery (the light-mission
// digest path, D-069: no artifacts, the deliverable is Payload.Body)
// the same way sendTextArtifacts renders a text artifact: a bold
// title + completion date head, the body run through RenderMarkdownV2
// instead of raw-escaped, chunked via ChunkMarkdownV2 rather than a
// single truncated message so a chunk boundary never lands inside an
// unescaped entity. Name heads the message when set (missions always
// set it); Subject (the ad-hoc deliver tool's only identifying field)
// stands in for it when Name is empty, so an ad-hoc send still gets a
// title instead of renderTelegramCaption's bare "**".
func (a *TelegramAdapter) sendTelegramText(ctx context.Context, token, chatID string, p Payload) error {
	var header string
	switch {
	case p.Name != "":
		header = renderTelegramCaption(p)
	case p.Subject != "":
		header = "*" + escapeMarkdownV2(p.Subject) + "*"
	}
	var b strings.Builder
	b.WriteString(RenderMarkdownV2(p.Body))
	if len(p.Links) > 0 {
		b.WriteString("\n\n")
		b.WriteString(escapeMarkdownV2("Links:"))
		for _, l := range p.Links {
			b.WriteString("\n")
			b.WriteString(escapeMarkdownV2("- " + l))
		}
	}
	if notice := oversizeNotice(p.OversizeFiles); notice != "" {
		b.WriteString(escapeMarkdownV2(notice))
	}
	full := b.String()
	if header != "" {
		full = header + "\n\n" + full
	}
	for _, chunk := range ChunkMarkdownV2(full, telegramMessageLimit) {
		if err := a.sendMessage(ctx, token, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// markdownV2Escapes are the characters Telegram's MarkdownV2 requires
// escaped anywhere in plain text (Bot API docs, "MarkdownV2 style").
const markdownV2Escapes = "_*[]()~`>#+-=|{}.!\\"

// escapeMarkdownV2 backslash-escapes every MarkdownV2 special
// character in s so arbitrary mission text (goal, digest, links) can
// never break message formatting or be misread as formatting markup.
func escapeMarkdownV2(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if strings.ContainsRune(markdownV2Escapes, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (a *TelegramAdapter) sendMessage(ctx context.Context, token, chatID, text string) error {
	body, err := json.Marshal(map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "MarkdownV2",
	})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return a.call(ctx, token, "sendMessage", "application/json", bytes.NewReader(body))
}

func (a *TelegramAdapter) sendDocument(ctx context.Context, token, chatID string, f File, caption string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("chat_id", chatID); err != nil {
		return fmt.Errorf("build form: %w", err)
	}
	if caption != "" {
		if err := w.WriteField("caption", caption); err != nil {
			return fmt.Errorf("build form: %w", err)
		}
		if err := w.WriteField("parse_mode", "MarkdownV2"); err != nil {
			return fmt.Errorf("build form: %w", err)
		}
	}
	part, err := w.CreateFormFile("document", f.Name)
	if err != nil {
		return fmt.Errorf("build form: %w", err)
	}
	if _, err := part.Write(f.Data); err != nil {
		return fmt.Errorf("build form: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("build form: %w", err)
	}
	return a.call(ctx, token, "sendDocument", w.FormDataContentType(), &buf)
}

// call POSTs one Bot API method and treats any non-2xx status or
// {"ok": false} response body as failure. Every returned error is
// redacted (redactToken) before it leaves this function: url embeds
// the bot token, and a *url.Error from http.Client.Do carries the full
// request URL verbatim, so an unredacted error would leak the token
// into WARN logs.
func (a *TelegramAdapter) call(ctx context.Context, token, method, contentType string, body io.Reader) error {
	cctx, cancel := context.WithTimeout(ctx, telegramTimeout)
	defer cancel()
	url := a.apiBase() + "/bot" + token + "/" + method
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url, body)
	if err != nil {
		return redactToken(fmt.Errorf("request: %w", err), token)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := a.client().Do(req)
	if err != nil {
		return redactToken(fmt.Errorf("post: %w", classifySendErr(err)), token)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		// A non-2xx response was definitely processed by Telegram; retrying
		// a rejection is pointless and a 5xx may still have side effects.
		return redactToken(fmt.Errorf("api status %d: %s: %w", resp.StatusCode, string(data), errMaybeDelivered), token)
	}
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &result); err == nil && !result.OK {
		return redactToken(fmt.Errorf("api error: %s: %w", result.Description, errMaybeDelivered), token)
	}
	return nil
}

// redactToken rebuilds err with every occurrence of token in its
// message replaced by "REDACTED". errors.Is(err, errMaybeDelivered)
// still holds afterwards (wrapped via %w against the already-redacted
// text, never re-appending the sentinel's own message) since deliver.go's
// retry loop classifies on the returned error.
func redactToken(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), token, "REDACTED")
	if errors.Is(err, errMaybeDelivered) {
		return fmt.Errorf("%w", redactedMaybeDelivered{msg})
	}
	return errors.New(msg)
}

// redactedMaybeDelivered carries a redacted message while still
// satisfying errors.Is(err, errMaybeDelivered) via Unwrap, needed
// because the original message (with the token already replaced) must
// not be reconstructed by re-wrapping errMaybeDelivered's own text.
type redactedMaybeDelivered struct{ msg string }

func (e redactedMaybeDelivered) Error() string { return e.msg }
func (e redactedMaybeDelivered) Unwrap() error { return errMaybeDelivered }
