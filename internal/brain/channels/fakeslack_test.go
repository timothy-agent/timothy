package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

const (
	fakeBotToken = "xoxb-FAKE-BOT-TOKEN" // #nosec G101
	fakeAppToken = "xapp-FAKE-APP-TOKEN" // #nosec G101
	fakeBotUser  = "UBOT"
)

// slackCall is one recorded Web API request.
type slackCall struct {
	Method string
	Body   map[string]any
	// TS is the message ts a chat.postMessage returned.
	TS string
}

// fakeSlack is an httptest Slack: the Web API under /api and a Socket
// Mode websocket under /ws that says hello, records acks and carries
// envelopes pushed by the test.
type fakeSlack struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	calls []slackCall
	opens int
	acks  []string
	conn  *websocket.Conn
	conns int
	ts    int
	// errCode, when set, answers every Web API call ok:false with it.
	errCode string
	// status and retryAfter, when set, answer every Web API call with
	// that HTTP status and Retry-After header.
	status     int
	retryAfter string
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()
	f := &fakeSlack{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", f.serveAPI)
	mux.HandleFunc("/ws", f.serveSocket)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func slackResolve(_ context.Context, ref string) (string, error) {
	switch ref {
	case "SLACK_BOT":
		return fakeBotToken, nil
	case "SLACK_APP":
		return fakeAppToken, nil
	}
	return "", io.EOF
}

func (f *fakeSlack) adapter() *slackAdapter {
	return &slackAdapter{http: f.srv.Client(), base: f.srv.URL + "/api", resolve: slackResolve, botRef: "SLACK_BOT", appRef: "SLACK_APP"}
}

func (f *fakeSlack) serveAPI(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	f.calls = append(f.calls, slackCall{Method: method, Body: body})
	idx := len(f.calls) - 1
	errCode, status, retry := f.errCode, f.status, f.retryAfter
	f.mu.Unlock()
	if status != 0 {
		if retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		w.WriteHeader(status)
		return
	}
	if errCode != "" {
		writeSlack(w, map[string]any{"ok": false, "error": errCode})
		return
	}
	want := fakeBotToken
	if method == "apps.connections.open" {
		want = fakeAppToken
	}
	if auth != want {
		writeSlack(w, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	switch method {
	case "auth.test":
		writeSlack(w, map[string]any{"ok": true, "user_id": fakeBotUser, "user": "timothy", "team_id": "T1"})
	case "apps.connections.open":
		f.mu.Lock()
		f.opens++
		f.mu.Unlock()
		writeSlack(w, map[string]any{"ok": true, "url": "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/ws?ticket=SECRET-TICKET"})
	case "chat.postMessage":
		f.mu.Lock()
		f.ts++
		ts := fmt.Sprintf("1700000000.%06d", f.ts)
		f.calls[idx].TS = ts
		f.mu.Unlock()
		writeSlack(w, map[string]any{"ok": true, "channel": body["channel"], "ts": ts})
	case "chat.update":
		writeSlack(w, map[string]any{"ok": true})
	default:
		writeSlack(w, map[string]any{"ok": false, "error": "unknown_method"})
	}
}

func writeSlack(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// serveSocket greets with hello, publishes the connection for push and
// records acks until the client hangs up.
func (f *fakeSlack) serveSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	ctx := r.Context()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","num_connections":1}`)); err != nil {
		return
	}
	f.mu.Lock()
	f.conn = conn
	f.conns++
	f.mu.Unlock()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var ack struct {
			ID string `json:"envelope_id"`
		}
		_ = json.Unmarshal(data, &ack)
		f.mu.Lock()
		f.acks = append(f.acks, ack.ID)
		f.mu.Unlock()
	}
}

func (f *fakeSlack) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

func (f *fakeSlack) callsOf(method string) []slackCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []slackCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// eventEnvelope wraps a Slack event in an events_api envelope.
func eventEnvelope(envelopeID string, event map[string]any) map[string]any {
	return map[string]any{
		"envelope_id": envelopeID,
		"type":        "events_api",
		"payload":     map[string]any{"type": "event_callback", "event_id": "Ev" + envelopeID, "event": event},
	}
}

// dmEvent is a direct message from user.
func dmEvent(user, text, ts string) map[string]any {
	return map[string]any{"type": "message", "channel_type": "im", "channel": "D" + user, "user": user, "text": text, "ts": ts,
		"user_profile": map[string]any{"display_name": "Ada", "real_name": "Ada L"}}
}

// channelEvent is a message (or app_mention) in channel C1.
func channelEvent(kind, user, text, ts, threadTS string) map[string]any {
	e := map[string]any{"type": kind, "channel": "C1", "user": user, "text": text, "ts": ts}
	if kind == "message" {
		e["channel_type"] = "channel"
	}
	if threadTS != "" {
		e["thread_ts"] = threadTS
	}
	return e
}

// pressEnvelope is a block_actions press on messageTS in channel.
func pressEnvelope(envelopeID, user, channel, messageTS, threadTS, value, text string) map[string]any {
	container := map[string]any{"type": "message", "message_ts": messageTS, "channel_id": channel}
	if threadTS != "" {
		container["thread_ts"] = threadTS
	}
	return map[string]any{
		"envelope_id": envelopeID,
		"type":        "interactive",
		"payload": map[string]any{
			"type":      "block_actions",
			"user":      map[string]any{"id": user, "username": "ada", "name": "ada"},
			"container": container,
			"channel":   map[string]any{"id": channel},
			"message":   map[string]any{"text": text, "ts": messageTS},
			"actions":   []any{map[string]any{"action_id": "tmy_0", "type": "button", "value": value}},
		},
	}
}
