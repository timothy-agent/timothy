package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/SumonMSelim/timothy/internal/brain/destinations"
)

const (
	defaultAPIBase = "https://api.telegram.org"
	// pollTimeout is getUpdates' long-poll wait in seconds.
	pollTimeout = 30
	// callTimeout bounds every other Bot API call.
	callTimeout = 30 * time.Second
)

// apiError is a Bot API rejection: the HTTP status (Telegram mirrors
// it in error_code) and its description, token-redacted.
type apiError struct {
	Method      string
	Status      int
	Description string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram %s: status %d: %s", e.Method, e.Status, e.Description)
}

func apiStatus(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// botAPI calls the Telegram Bot API for one channel. The token is
// resolved per call and never leaves this type unredacted.
type botAPI struct {
	http    *http.Client
	base    string
	resolve func(ctx context.Context, ref string) (string, error)
	ref     string
}

// call POSTs one method with a JSON body and decodes result into out
// (nil skips).
func (b *botAPI) call(ctx context.Context, method string, body any, out any, timeout time.Duration) error {
	if b.resolve == nil {
		return fmt.Errorf("telegram %s: no secret resolver", method)
	}
	token, err := b.resolve(ctx, b.ref)
	if err != nil {
		return fmt.Errorf("telegram %s: resolve bot token: %w", method, err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("telegram %s: marshal: %w", method, err)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, b.base+"/bot"+token+"/"+method, bytes.NewReader(payload))
	if err != nil {
		return destinations.RedactToken(fmt.Errorf("telegram %s: request: %w", method, err), token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return destinations.RedactToken(fmt.Errorf("telegram %s: %w", method, err), token)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return destinations.RedactToken(fmt.Errorf("telegram %s: read: %w", method, err), token)
	}
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if jerr := json.Unmarshal(data, &env); jerr != nil || resp.StatusCode >= 300 || !env.OK {
		desc := env.Description
		if desc == "" {
			desc = http.StatusText(resp.StatusCode)
		}
		return &apiError{Method: method, Status: resp.StatusCode, Description: destinations.RedactToken(errors.New(desc), token).Error()}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("telegram %s: decode result: %w", method, err)
	}
	return nil
}

// botIdentity is the bot's own user, for group addressing.
type botIdentity struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

func (b *botAPI) getMe(ctx context.Context) (botIdentity, error) {
	var me botIdentity
	err := b.call(ctx, "getMe", map[string]any{}, &me, callTimeout)
	return me, err
}

func (b *botAPI) getUpdates(ctx context.Context, offset int64) ([]tgUpdate, error) {
	var updates []tgUpdate
	err := b.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         pollTimeout,
		"allowed_updates": []string{"message"},
	}, &updates, (pollTimeout+15)*time.Second)
	return updates, err
}

// sendMessage sends plain text (no parse_mode: model output would
// break MarkdownV2) and returns the new message id.
func (b *botAPI) sendMessage(ctx context.Context, chatID int64, threadID int64, text string, silent bool) (int64, error) {
	body := map[string]any{"chat_id": chatID, "text": text}
	if threadID != 0 {
		body["message_thread_id"] = threadID
	}
	if silent {
		body["disable_notification"] = true
	}
	var msg struct {
		MessageID int64 `json:"message_id"`
	}
	err := b.call(ctx, "sendMessage", body, &msg, callTimeout)
	return msg.MessageID, err
}

// editMessageText replaces a sent message's text; "message is not
// modified" counts as success.
func (b *botAPI) editMessageText(ctx context.Context, chatID, messageID int64, text string) error {
	err := b.call(ctx, "editMessageText", map[string]any{"chat_id": chatID, "message_id": messageID, "text": text}, nil, callTimeout)
	var ae *apiError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified") {
		return nil
	}
	return err
}

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	MessageID       int64      `json:"message_id"`
	MessageThreadID int64      `json:"message_thread_id"`
	From            *tgUser    `json:"from"`
	Chat            tgChat     `json:"chat"`
	Text            string     `json:"text"`
	Caption         string     `json:"caption"`
	Entities        []tgEntity `json:"entities"`
	CaptionEntities []tgEntity `json:"caption_entities"`
	ReplyTo         *tgMessage `json:"reply_to_message"`
}

type tgUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type tgEntity struct {
	Type   string  `json:"type"`
	Offset int     `json:"offset"`
	Length int     `json:"length"`
	User   *tgUser `json:"user"`
}

// inbound is one parsed message the pipeline acts on.
type inbound struct {
	UpdateID    int64
	UserID      string
	DisplayName string
	ChatID      int64
	ThreadID    int64
	Private     bool
	// Addressed is true for private chats and for group messages that
	// mention or reply to the bot.
	Addressed bool
	Text      string
}

// parseUpdate turns an update into an inbound message; ok is false
// for updates without a human sender message.
func parseUpdate(u tgUpdate, bot botIdentity) (inbound, bool) {
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot {
		return inbound{}, false
	}
	in := inbound{
		UpdateID:    u.UpdateID,
		UserID:      strconv.FormatInt(m.From.ID, 10),
		DisplayName: displayName(*m.From),
		ChatID:      m.Chat.ID,
		Private:     m.Chat.Type == "private",
		Text:        m.Text,
	}
	entities := m.Entities
	if in.Text == "" {
		in.Text, entities = m.Caption, m.CaptionEntities
	}
	if !in.Private {
		in.ThreadID = m.MessageThreadID
	}
	in.Addressed = in.Private || mentionsBot(in.Text, entities, bot) ||
		(m.ReplyTo != nil && m.ReplyTo.From != nil && m.ReplyTo.From.IsBot && (bot.ID == 0 || m.ReplyTo.From.ID == bot.ID))
	return in, true
}

func displayName(u tgUser) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" && u.Username != "" {
		name = "@" + u.Username
	}
	if name == "" {
		name = strconv.FormatInt(u.ID, 10)
	}
	return name
}

// mentionsBot reports an @username mention of the bot or a
// text_mention of its user. Entity offsets count UTF-16 units.
func mentionsBot(text string, entities []tgEntity, bot botIdentity) bool {
	units := utf16.Encode([]rune(text))
	for _, e := range entities {
		switch e.Type {
		case "text_mention":
			if e.User != nil && bot.ID != 0 && e.User.ID == bot.ID {
				return true
			}
		case "mention":
			if bot.Username == "" || e.Offset < 0 || e.Length <= 0 || e.Offset+e.Length > len(units) {
				continue
			}
			if strings.EqualFold(string(utf16.Decode(units[e.Offset:e.Offset+e.Length])), "@"+bot.Username) {
				return true
			}
		}
	}
	return false
}

// conversationKey derives the external chat and thread a message maps
// to: private chats by chat id, group topics by thread id.
func conversationKey(in inbound) (chatID, threadID string) {
	chatID = strconv.FormatInt(in.ChatID, 10)
	if !in.Private && in.ThreadID != 0 {
		threadID = strconv.FormatInt(in.ThreadID, 10)
	}
	return chatID, threadID
}
