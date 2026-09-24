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

// apiError is a transport API rejection: the HTTP status (Telegram
// mirrors it in error_code; Slack errors are mapped onto one) and the
// description, token-redacted.
type apiError struct {
	// Service is "telegram" or "slack"; empty reads as telegram.
	Service     string
	Method      string
	Status      int
	Description string
	// RetryAfter is the server's rate limit wait, 0 when unknown.
	RetryAfter time.Duration
}

func (e *apiError) Error() string {
	svc := e.Service
	if svc == "" {
		svc = KindTelegram
	}
	return fmt.Sprintf("%s %s: status %d: %s", svc, e.Method, e.Status, e.Description)
}

func apiStatus(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

func retryAfter(err error) time.Duration {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.RetryAfter
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
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if jerr := json.Unmarshal(data, &env); jerr != nil || resp.StatusCode >= 300 || !env.OK {
		desc := env.Description
		if desc == "" {
			desc = http.StatusText(resp.StatusCode)
		}
		return &apiError{Method: method, Status: resp.StatusCode, Description: destinations.RedactToken(errors.New(desc), token).Error(),
			RetryAfter: time.Duration(env.Parameters.RetryAfter) * time.Second}
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
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates, (pollTimeout+15)*time.Second)
	return updates, err
}

// sendMessage sends plain text (no parse_mode: model output would
// break MarkdownV2) with an optional inline keyboard and returns the
// new message id.
func (b *botAPI) sendMessage(ctx context.Context, chatID, threadID int64, text string, keyboard [][]tgButton, silent bool) (int64, error) {
	body := map[string]any{"chat_id": chatID, "text": text}
	if silent {
		body["disable_notification"] = true
	}
	if len(keyboard) > 0 {
		body["reply_markup"] = map[string]any{"inline_keyboard": keyboard}
	}
	if threadID != 0 {
		body["message_thread_id"] = threadID
	}
	var msg struct {
		MessageID int64 `json:"message_id"`
	}
	err := b.call(ctx, "sendMessage", body, &msg, callTimeout)
	return msg.MessageID, err
}

// tgButton is one inline keyboard button.
type tgButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// answerCallbackQuery stops the button spinner with a short notice.
func (b *botAPI) answerCallbackQuery(ctx context.Context, id, text string) error {
	return b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil, callTimeout)
}

// editMessageText replaces a sent message's text and keyboard (empty
// removes it); "message is not modified" counts as success.
func (b *botAPI) editMessageText(ctx context.Context, chatID, messageID int64, text string, keyboard [][]tgButton) error {
	if keyboard == nil {
		keyboard = [][]tgButton{}
	}
	err := b.call(ctx, "editMessageText", map[string]any{
		"chat_id": chatID, "message_id": messageID, "text": text,
		"reply_markup": map[string]any{"inline_keyboard": keyboard},
	}, nil, callTimeout)
	var ae *apiError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified") {
		return nil
	}
	return err
}

// telegramAdapter is the Bot API transport: long polling, update
// offsets as the cursor.
type telegramAdapter struct {
	api *botAPI
	me  botIdentity
}

func (t *telegramAdapter) connect(ctx context.Context) (identity, error) {
	me, err := t.api.getMe(ctx)
	if err != nil {
		return identity{}, err
	}
	t.me = me
	return identity{ID: strconv.FormatInt(me.ID, 10), Username: me.Username}, nil
}

func (t *telegramAdapter) receive(ctx context.Context, cursor string) ([]inbound, string, error) {
	var offset int64
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, cursor, fmt.Errorf("telegram cursor %q: %w", cursor, err)
		}
		offset = n
	}
	updates, err := t.api.getUpdates(ctx, offset)
	if err != nil || len(updates) == 0 {
		return nil, cursor, err
	}
	items := make([]inbound, 0, len(updates))
	for _, u := range updates {
		if in, ok := parseUpdate(u, t.me); ok {
			items = append(items, in)
		}
		offset = u.UpdateID + 1
	}
	return items, strconv.FormatInt(offset, 10), nil
}

func (t *telegramAdapter) send(ctx context.Context, to target, text string, buttons [][]button, silent bool) (string, error) {
	chatID, threadID, err := tgTarget(to)
	if err != nil {
		return "", err
	}
	id, err := t.api.sendMessage(ctx, chatID, threadID, text, tgKeyboard(buttons), silent)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}

func (t *telegramAdapter) edit(ctx context.Context, to target, messageID, text string, buttons [][]button) error {
	chatID, _, err := tgTarget(to)
	if err != nil {
		return err
	}
	msgID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram message id %q: %w", messageID, err)
	}
	return t.api.editMessageText(ctx, chatID, msgID, text, tgKeyboard(buttons))
}

func (t *telegramAdapter) answerPress(ctx context.Context, pressID, text string) error {
	return t.api.answerCallbackQuery(ctx, pressID, text)
}

func (*telegramAdapter) caps() capabilities {
	return capabilities{Edits: true, Buttons: true, MessageLimit: messageLimit, EditEvery: editEvery}
}

// tgTarget parses a target's numeric chat and thread ids.
func tgTarget(to target) (chatID, threadID int64, err error) {
	chatID, err = strconv.ParseInt(to.ChatID, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("telegram chat id %q: %w", to.ChatID, err)
	}
	if to.ThreadID != "" {
		if threadID, err = strconv.ParseInt(to.ThreadID, 10, 64); err != nil {
			return 0, 0, fmt.Errorf("telegram thread id %q: %w", to.ThreadID, err)
		}
	}
	return chatID, threadID, nil
}

func tgKeyboard(buttons [][]button) [][]tgButton {
	if buttons == nil {
		return nil
	}
	out := make([][]tgButton, 0, len(buttons))
	for _, row := range buttons {
		r := make([]tgButton, 0, len(row))
		for _, b := range row {
			r = append(r, tgButton{Text: b.Text, CallbackData: b.Data})
		}
		out = append(out, r)
	}
	return out
}

type tgUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *tgMessage       `json:"message"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

// tgCallbackQuery is an inline button press.
type tgCallbackQuery struct {
	ID      string     `json:"id"`
	From    *tgUser    `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
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

// parseUpdate turns an update into an inbound item; ok is false for
// updates without a human sender message or a button press.
func parseUpdate(u tgUpdate, bot botIdentity) (inbound, bool) {
	dedup := "telegram:" + strconv.FormatInt(u.UpdateID, 10)
	if q := u.CallbackQuery; q != nil {
		in := inbound{DedupID: dedup, Press: &press{ID: q.ID, Data: q.Data}}
		// A press without a human sender or message keeps UserID empty;
		// the runner only closes it.
		if q.From == nil || q.From.IsBot || q.Message == nil {
			return in, true
		}
		in.UserID, in.DisplayName = strconv.FormatInt(q.From.ID, 10), displayName(*q.From)
		in.ChatID, in.Private = strconv.FormatInt(q.Message.Chat.ID, 10), q.Message.Chat.Type == "private"
		if !in.Private && q.Message.MessageThreadID != 0 {
			in.ThreadID = strconv.FormatInt(q.Message.MessageThreadID, 10)
		}
		in.Press.MessageID, in.Press.MessageText = strconv.FormatInt(q.Message.MessageID, 10), q.Message.Text
		return in, true
	}
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot {
		return inbound{}, false
	}
	in := inbound{
		DedupID:     dedup,
		MessageID:   strconv.FormatInt(m.MessageID, 10),
		UserID:      strconv.FormatInt(m.From.ID, 10),
		DisplayName: displayName(*m.From),
		ChatID:      strconv.FormatInt(m.Chat.ID, 10),
		Private:     m.Chat.Type == "private",
		Text:        m.Text,
	}
	entities := m.Entities
	if in.Text == "" {
		in.Text, entities = m.Caption, m.CaptionEntities
	}
	if !in.Private && m.MessageThreadID != 0 {
		in.ThreadID = strconv.FormatInt(m.MessageThreadID, 10)
	}
	if m.ReplyTo != nil {
		in.ReplyToID = strconv.FormatInt(m.ReplyTo.MessageID, 10)
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
