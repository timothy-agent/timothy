package destinations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// telegramTimeout bounds one Telegram Bot API call — same reasoning as
// webhookTimeout.
const telegramTimeout = 10 * time.Second

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
		text := renderTelegramText(payload)
		if err := a.sendMessage(ctx, token, cfg.ChatID, text); err != nil {
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
// the date line rather than guessing "now".
func renderTelegramCaption(p Payload) string {
	var b strings.Builder
	b.WriteString("*")
	b.WriteString(escapeMarkdownV2(p.Name))
	b.WriteString("*")
	if !p.CompletedAt.IsZero() {
		b.WriteString("\n")
		b.WriteString(escapeMarkdownV2(p.CompletedAt.Format("2 Jan 2006, 15:04 UTC")))
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

// renderTelegramText builds the MarkdownV2 body: digest + links +
// oversize notice, escaped, then truncated to telegramMessageLimit
// with a notice appended (the mission link, when present, is preserved
// after truncation rather than being the first thing cut).
func renderTelegramText(p Payload) string {
	var b strings.Builder
	if p.Subject != "" {
		b.WriteString(escapeMarkdownV2(p.Subject))
		b.WriteString("\n\n")
	}
	b.WriteString(escapeMarkdownV2(p.Body))
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
	if len(full) <= telegramMessageLimit {
		return full
	}
	return truncateMarkdownV2(full, p.Links)
}

// truncationSuffix is appended, escaped, when a message is cut for
// length — the mission link (if any) is repeated here since it may
// have been cut from the body itself.
func truncationSuffix(links []string) string {
	suffix := "\n\n" + escapeMarkdownV2("[truncated]")
	if len(links) > 0 {
		suffix += "\n" + escapeMarkdownV2(links[0])
	}
	return suffix
}

// truncateMarkdownV2 cuts full to fit telegramMessageLimit once the
// truncation suffix is accounted for, at a rune boundary so no
// multi-byte rune (or its escaping backslash) is split.
func truncateMarkdownV2(full string, links []string) string {
	suffix := truncationSuffix(links)
	budget := telegramMessageLimit - len(suffix)
	if budget < 0 {
		budget = 0
	}
	cut := 0
	for i, r := range full {
		if i+len(string(r)) > budget {
			break
		}
		cut = i + len(string(r))
	}
	return full[:cut] + suffix
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
// {"ok": false} response body as failure.
func (a *TelegramAdapter) call(ctx context.Context, token, method, contentType string, body io.Reader) error {
	cctx, cancel := context.WithTimeout(ctx, telegramTimeout)
	defer cancel()
	url := a.apiBase() + "/bot" + token + "/" + method
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url, body)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := a.client().Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("api status %d: %s", resp.StatusCode, string(data))
	}
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &result); err == nil && !result.OK {
		return fmt.Errorf("api error: %s", result.Description)
	}
	return nil
}
