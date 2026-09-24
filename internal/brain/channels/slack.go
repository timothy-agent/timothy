package channels

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/SumonMSelim/timothy/internal/platform/redact"
)

const (
	defaultSlackBase = "https://slack.com/api"
	// slackEditEvery is slower than Telegram's: Slack throttles
	// chat.update harder.
	slackEditEvery = 3 * time.Second
	// slackReadLimit caps one Socket Mode envelope.
	slackReadLimit = 1 << 20
	// slackButtonText is Slack's button label cap.
	slackButtonText = 75
)

// slackAdapter is the Slack transport: Socket Mode for inbound (no
// public URL), the Web API for replies. The bot token (xoxb) is
// credential_ref, the app-level token (xapp) config.app_token_ref.
// Tokens only travel in the Authorization header.
type slackAdapter struct {
	http    *http.Client
	base    string
	resolve func(ctx context.Context, ref string) (string, error)
	botRef  string
	appRef  string
	botID   string

	// conn is the open socket; receive's goroutine only.
	conn *websocket.Conn
	// unwatch detaches conn's close-on-cancel hook.
	unwatch func() bool
}

// slackError maps a Web API error code onto an HTTP-style status so
// pause and the outcomes consumer treat Slack like Telegram.
func slackError(method, code string, retry time.Duration) *apiError {
	status := http.StatusBadRequest
	switch code {
	case "ratelimited":
		status = http.StatusTooManyRequests
	case "invalid_auth", "not_authed", "account_inactive", "token_revoked", "token_expired":
		status = http.StatusUnauthorized
	case "channel_not_found", "not_in_channel", "message_not_found", "is_archived":
		status = http.StatusNotFound
	case "internal_error", "fatal_error", "service_unavailable", "request_timeout":
		status = http.StatusServiceUnavailable
	}
	return &apiError{Service: KindSlack, Method: method, Status: status, Description: code, RetryAfter: retry}
}

// call POSTs one Web API method with the token behind ref.
func (a *slackAdapter) call(ctx context.Context, ref, method string, body, out any) error {
	return NewSlackClient(a.http, a.base, a.resolve, ref).Call(ctx, method, body, out)
}

// SlackClient calls the Slack Web API with the bot token behind one
// credential ref; the channel destination shares it. The token only
// travels in the Authorization header and never leaves unredacted.
type SlackClient struct {
	http    *http.Client
	base    string
	resolve func(ctx context.Context, ref string) (string, error)
	ref     string
}

// NewSlackClient builds a client; an empty base uses the real API.
func NewSlackClient(client *http.Client, base string, resolve func(ctx context.Context, ref string) (string, error), ref string) *SlackClient {
	return &SlackClient{http: client, base: cmp.Or(base, defaultSlackBase), resolve: resolve, ref: ref}
}

// Call POSTs one method and decodes the response into out (nil
// skips). A url.Values body goes form-encoded, anything else as JSON.
func (c *SlackClient) Call(ctx context.Context, method string, body, out any) error {
	if c.resolve == nil {
		return fmt.Errorf("slack %s: no secret resolver", method)
	}
	token, err := c.resolve(ctx, c.ref)
	if err != nil {
		return fmt.Errorf("slack %s: resolve token: %w", method, err)
	}
	contentType := "application/json; charset=utf-8"
	var payload []byte
	if form, ok := body.(url.Values); ok {
		contentType, payload = "application/x-www-form-urlencoded", []byte(form.Encode())
	} else if payload, err = json.Marshal(body); err != nil {
		return fmt.Errorf("slack %s: marshal: %w", method, err)
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, c.base+"/"+method, bytes.NewReader(payload))
	if err != nil {
		return redact.Token(fmt.Errorf("slack %s: request: %w", method, err), token)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http.Do(req)
	if err != nil {
		return redact.Token(fmt.Errorf("slack %s: %w", method, err), token)
	}
	defer func() { _ = resp.Body.Close() }()
	var retry time.Duration
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		retry = time.Duration(s) * time.Second
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return slackError(method, "ratelimited", retry)
	}
	if resp.StatusCode >= 300 {
		return &apiError{Service: KindSlack, Method: method, Status: resp.StatusCode, Description: http.StatusText(resp.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return redact.Token(fmt.Errorf("slack %s: read: %w", method, err), token)
	}
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("slack %s: decode: %w", method, err)
	}
	if !env.OK {
		return slackError(method, redact.Token(errors.New(cmp.Or(env.Error, "unknown_error")), token).Error(), retry)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("slack %s: decode result: %w", method, err)
	}
	return nil
}

// UploadFile shares one file in channelID (under threadTS when set)
// with comment as its message: an upload URL, the bytes, then the
// completion call.
func (c *SlackClient) UploadFile(ctx context.Context, channelID, threadTS, name string, data []byte, comment string) error {
	var up struct {
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
	}
	if err := c.Call(ctx, "files.getUploadURLExternal", url.Values{"filename": {name}, "length": {strconv.Itoa(len(data))}}, &up); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, up.UploadURL, bytes.NewReader(data))
	if err != nil {
		return redact.Token(fmt.Errorf("slack upload: request: %w", err), up.UploadURL)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		// The upload URL is a signed grant; keep it out of logs.
		return redact.Token(fmt.Errorf("slack upload: %w", err), up.UploadURL)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &apiError{Service: KindSlack, Method: "upload", Status: resp.StatusCode, Description: http.StatusText(resp.StatusCode)}
	}
	done := map[string]any{"files": []map[string]string{{"id": up.FileID, "title": name}}, "channel_id": channelID}
	if threadTS != "" {
		done["thread_ts"] = threadTS
	}
	if comment != "" {
		done["initial_comment"] = comment
	}
	return c.Call(ctx, "files.completeUploadExternal", done, nil)
}

func (a *slackAdapter) connect(ctx context.Context) (identity, error) {
	var me struct {
		UserID string `json:"user_id"`
		User   string `json:"user"`
	}
	if err := a.call(ctx, a.botRef, "auth.test", map[string]any{}, &me); err != nil {
		return identity{}, err
	}
	a.botID = me.UserID
	return identity{ID: me.UserID, Username: me.User}, nil
}

// receive reads one envelope, dialing first when no socket is open.
// Every envelope is acked before it is parsed so Slack never
// redelivers; a disconnect envelope or a read error drops the socket
// and the next call redials. Slack keeps no cursor.
func (a *slackAdapter) receive(ctx context.Context, cursor string) ([]inbound, string, error) {
	if a.conn == nil {
		if err := a.dial(ctx); err != nil {
			return nil, cursor, err
		}
	}
	_, data, err := a.conn.Read(ctx)
	if err != nil {
		a.hangUp()
		return nil, cursor, fmt.Errorf("slack socket read: %w", err)
	}
	var head struct {
		ID   string `json:"envelope_id"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(data, &head)
	if head.ID != "" {
		ack, _ := json.Marshal(map[string]string{"envelope_id": head.ID})
		if err := a.conn.Write(ctx, websocket.MessageText, ack); err != nil {
			a.hangUp()
			return nil, cursor, fmt.Errorf("slack socket ack: %w", err)
		}
	}
	if head.Type == "disconnect" {
		a.hangUp()
	}
	items, err := parseEnvelope(data, a.botID)
	return items, cursor, err
}

// dial opens a Socket Mode connection with the app token. The socket
// closes when ctx ends.
func (a *slackAdapter) dial(ctx context.Context) error {
	var open struct {
		URL string `json:"url"`
	}
	if err := a.call(ctx, a.appRef, "apps.connections.open", map[string]any{}, &open); err != nil {
		return err
	}
	conn, _, err := websocket.Dial(ctx, open.URL, &websocket.DialOptions{HTTPClient: a.http})
	if err != nil {
		// The URL's query is a connection ticket; keep it out of logs.
		if u, perr := url.Parse(open.URL); perr == nil && u.RawQuery != "" {
			err = redact.Token(err, u.RawQuery)
		}
		return fmt.Errorf("slack socket dial: %w", err)
	}
	conn.SetReadLimit(slackReadLimit)
	a.conn = conn
	a.unwatch = context.AfterFunc(ctx, func() { _ = conn.CloseNow() })
	return nil
}

func (a *slackAdapter) hangUp() {
	if a.conn == nil {
		return
	}
	a.unwatch()
	_ = a.conn.CloseNow()
	a.conn, a.unwatch = nil, nil
}

func (a *slackAdapter) send(ctx context.Context, to target, text string, buttons [][]button, _ bool) (string, error) {
	body := map[string]any{"channel": to.ChatID, "text": slackEscape(text)}
	if to.ThreadID != "" {
		body["thread_ts"] = to.ThreadID
	}
	if blocks := slackBlocks(text, buttons); blocks != nil {
		body["blocks"] = blocks
	}
	var out struct {
		TS string `json:"ts"`
	}
	if err := a.call(ctx, a.botRef, "chat.postMessage", body, &out); err != nil {
		return "", err
	}
	return out.TS, nil
}

// edit updates a message; an empty blocks array removes old buttons.
func (a *slackAdapter) edit(ctx context.Context, to target, messageID, text string, buttons [][]button) error {
	blocks := slackBlocks(text, buttons)
	if blocks == nil {
		blocks = []map[string]any{}
	}
	return a.call(ctx, a.botRef, "chat.update", map[string]any{
		"channel": to.ChatID, "ts": messageID, "text": slackEscape(text), "blocks": blocks,
	}, nil)
}

// answerPress is a no-op: the envelope ack already answered the press.
func (*slackAdapter) answerPress(context.Context, string, string) error { return nil }

func (*slackAdapter) caps() capabilities {
	return capabilities{Edits: true, Buttons: true, MessageLimit: slackMessageLimit, EditEvery: slackEditEvery}
}

// slackBlocks renders text plus buttons as one plain_text section and
// one actions block; nil without buttons.
func slackBlocks(text string, buttons [][]button) []map[string]any {
	var elements []map[string]any
	for _, row := range buttons {
		for _, b := range row {
			elements = append(elements, map[string]any{
				"type":      "button",
				"text":      map[string]any{"type": "plain_text", "text": capRunes(b.Text, slackButtonText)},
				"action_id": "tmy_" + strconv.Itoa(len(elements)),
				"value":     b.Data,
			})
		}
	}
	if len(elements) == 0 {
		return nil
	}
	return []map[string]any{
		{"type": "section", "text": map[string]any{"type": "plain_text", "text": capRunes(text, slackMessageLimit)}},
		{"type": "actions", "elements": elements},
	}
}

// slackEscape escapes the three characters Slack parses in text, so
// model output cannot mention users or channels.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func slackUnescape(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(s)
}

type slackEvent struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	BotID       string `json:"bot_id"`
	User        string `json:"user"`
	Text        string `json:"text"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	UserProfile *struct {
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
	} `json:"user_profile"`
}

type slackBlockActions struct {
	Type string `json:"type"`
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Container struct {
		MessageTS string `json:"message_ts"`
		ThreadTS  string `json:"thread_ts"`
		ChannelID string `json:"channel_id"`
	} `json:"container"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	Message struct {
		Text     string `json:"text"`
		ThreadTS string `json:"thread_ts"`
	} `json:"message"`
	Actions []struct {
		Value string `json:"value"`
	} `json:"actions"`
}

// parseEnvelope turns one Socket Mode envelope into inbound items:
// events_api messages and mentions, interactive block_actions presses.
// Other envelopes (hello, disconnect) yield none.
func parseEnvelope(raw []byte, botID string) ([]inbound, error) {
	var env struct {
		ID      string          `json:"envelope_id"`
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("slack envelope: %w", err)
	}
	switch env.Type {
	case "events_api":
		var p struct {
			Event slackEvent `json:"event"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("slack events_api payload: %w", err)
		}
		if in, ok := parseSlackEvent(p.Event, botID); ok {
			return []inbound{in}, nil
		}
	case "interactive":
		var p slackBlockActions
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("slack interactive payload: %w", err)
		}
		if in, ok := parseSlackPress(env.ID, p); ok {
			return []inbound{in}, nil
		}
	}
	return nil, nil
}

// parseSlackEvent turns a message or app_mention event into an inbound
// message; ok is false for bot, edited or system messages.
func parseSlackEvent(e slackEvent, botID string) (inbound, bool) {
	switch e.Type {
	case "app_mention":
	case "message":
		switch e.ChannelType {
		case "im", "channel", "group", "mpim":
		default:
			return inbound{}, false
		}
	default:
		return inbound{}, false
	}
	if e.BotID != "" || e.Subtype != "" || e.User == "" || e.User == botID || e.Channel == "" || e.TS == "" {
		return inbound{}, false
	}
	text, mentioned := e.Text, false
	if botID != "" {
		tag := "<@" + botID + ">"
		mentioned = strings.Contains(text, tag)
		text = strings.ReplaceAll(text, tag, "")
	}
	in := inbound{
		// A channel mention arrives as both app_mention and message with
		// different event ids; channel plus ts is the same in both.
		DedupID:     "slack:" + e.Channel + ":" + e.TS,
		MessageID:   e.TS,
		UserID:      e.User,
		DisplayName: e.User,
		ChatID:      e.Channel,
		Private:     e.Type == "message" && e.ChannelType == "im",
		Text:        strings.TrimSpace(slackUnescape(text)),
	}
	if p := e.UserProfile; p != nil {
		in.DisplayName = cmp.Or(p.DisplayName, p.RealName, e.User)
	}
	if in.Private {
		in.Addressed = true
		if e.ThreadTS != "" && e.ThreadTS != e.TS {
			in.ReplyToID = e.ThreadTS
		}
		return in, true
	}
	// Channel conversations live in the thread under the mentioning
	// message.
	in.ThreadID = cmp.Or(e.ThreadTS, e.TS)
	in.Addressed = e.Type == "app_mention" || mentioned
	in.MaybeThread = !in.Addressed && e.ThreadTS != ""
	return in, true
}

// parseSlackPress turns a block_actions payload into a press; the
// envelope id is the press id.
func parseSlackPress(envelopeID string, p slackBlockActions) (inbound, bool) {
	if p.Type != "block_actions" || envelopeID == "" || len(p.Actions) == 0 {
		return inbound{}, false
	}
	chat := cmp.Or(p.Channel.ID, p.Container.ChannelID)
	in := inbound{
		DedupID:     "slack:press:" + envelopeID,
		UserID:      p.User.ID,
		DisplayName: cmp.Or(p.User.Name, p.User.Username, p.User.ID),
		ChatID:      chat,
		// Direct message channel ids start with D.
		Private: strings.HasPrefix(chat, "D"),
		Press: &press{ID: envelopeID, Data: p.Actions[0].Value, MessageID: p.Container.MessageTS,
			MessageText: slackUnescape(p.Message.Text)},
	}
	if !in.Private {
		in.ThreadID = cmp.Or(p.Container.ThreadTS, p.Message.ThreadTS, p.Container.MessageTS)
	}
	if chat == "" || in.Press.MessageID == "" {
		in.UserID = ""
	}
	return in, true
}
