package channels

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func envelopeJSON(t *testing.T, env map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseEnvelope(t *testing.T) {
	botEvent := dmEvent("U77", "hi", "1.1")
	botEvent["bot_id"] = "B1"
	self := dmEvent(fakeBotUser, "echo", "1.2")
	edited := dmEvent("U77", "hi", "1.3")
	edited["subtype"] = "message_changed"
	shared := channelEvent("message", "U77", "look", "1.4", "")
	shared["channel_type"] = "app_home"
	tests := []struct {
		name string
		env  map[string]any
		want []inbound
	}{
		{"hello", map[string]any{"type": "hello", "num_connections": 1}, nil},
		{"disconnect", map[string]any{"type": "disconnect", "reason": "refresh_requested"}, nil},
		{"message im", eventEnvelope("e1", dmEvent("U77", "hi &amp; &lt;bye&gt;", "1.5")), []inbound{{
			DedupID: "slack:DU77:1.5", MessageID: "1.5", UserID: "U77", DisplayName: "Ada", ChatID: "DU77", Private: true, Addressed: true, Text: "hi & <bye>",
		}}},
		{"message im thread reply", eventEnvelope("e2", func() map[string]any { e := dmEvent("U77", "red", "1.7"); e["thread_ts"] = "1.6"; return e }()), []inbound{{
			DedupID: "slack:DU77:1.7", MessageID: "1.7", UserID: "U77", DisplayName: "Ada", ChatID: "DU77", Private: true, Addressed: true, Text: "red", ReplyToID: "1.6",
		}}},
		{"message channel unaddressed", eventEnvelope("e3", channelEvent("message", "U77", "hello all", "2.1", "")), []inbound{{
			DedupID: "slack:C1:2.1", MessageID: "2.1", UserID: "U77", DisplayName: "U77", ChatID: "C1", ThreadID: "2.1", Text: "hello all",
		}}},
		{"message channel with mention", eventEnvelope("e4", channelEvent("message", "U77", "<@UBOT> hi", "2.2", "")), []inbound{{
			DedupID: "slack:C1:2.2", MessageID: "2.2", UserID: "U77", DisplayName: "U77", ChatID: "C1", ThreadID: "2.2", Addressed: true, Text: "hi",
		}}},
		{"app_mention", eventEnvelope("e5", channelEvent("app_mention", "U77", "<@UBOT> 123456", "2.3", "")), []inbound{{
			DedupID: "slack:C1:2.3", MessageID: "2.3", UserID: "U77", DisplayName: "U77", ChatID: "C1", ThreadID: "2.3", Addressed: true, Text: "123456",
		}}},
		{"thread reply maybe addressed", eventEnvelope("e6", channelEvent("message", "U77", "and?", "2.5", "2.3")), []inbound{{
			DedupID: "slack:C1:2.5", MessageID: "2.5", UserID: "U77", DisplayName: "U77", ChatID: "C1", ThreadID: "2.3", MaybeThread: true, Text: "and?",
		}}},
		{"bot message ignored", eventEnvelope("e7", botEvent), nil},
		{"own message ignored", eventEnvelope("e8", self), nil},
		{"subtype ignored", eventEnvelope("e9", edited), nil},
		{"unknown channel type ignored", eventEnvelope("e10", shared), nil},
		{"other event ignored", eventEnvelope("e11", map[string]any{"type": "reaction_added", "user": "U77"}), nil},
		{"block_actions in a channel thread", pressEnvelope("env-1", "U77", "C1", "3.2", "3.1", "p:ab:once", "Timothy wants to run shell &amp; more"), []inbound{{
			DedupID: "slack:press:env-1", UserID: "U77", DisplayName: "ada", ChatID: "C1", ThreadID: "3.1",
			Press: &press{ID: "env-1", Data: "p:ab:once", MessageID: "3.2", MessageText: "Timothy wants to run shell & more"},
		}}},
		{"block_actions in a DM", pressEnvelope("env-2", "U77", "DU77", "3.3", "", "m:ab:resume", "x"), []inbound{{
			DedupID: "slack:press:env-2", UserID: "U77", DisplayName: "ada", ChatID: "DU77", Private: true,
			Press: &press{ID: "env-2", Data: "m:ab:resume", MessageID: "3.3", MessageText: "x"},
		}}},
		{"view submission ignored", map[string]any{"envelope_id": "env-3", "type": "interactive", "payload": map[string]any{"type": "view_submission"}}, nil},
		{"slash command ignored", map[string]any{"envelope_id": "env-4", "type": "slash_commands", "payload": map[string]any{"command": "/x"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEnvelope(envelopeJSON(t, tt.env), fakeBotUser)
			if err != nil {
				t.Fatalf("parseEnvelope: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("items = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				g, w := got[i], tt.want[i]
				if (g.Press == nil) != (w.Press == nil) || (g.Press != nil && *g.Press != *w.Press) {
					t.Fatalf("press = %+v, want %+v", g.Press, w.Press)
				}
				g.Press, w.Press = nil, nil
				if !reflect.DeepEqual(g, w) {
					t.Fatalf("item = %+v, want %+v", g, w)
				}
			}
		})
	}
	for _, raw := range []string{`{`, `{"type":"events_api","payload":"nope"}`, `{"type":"interactive","envelope_id":"x","payload":[1]}`} {
		if _, err := parseEnvelope([]byte(raw), fakeBotUser); err == nil {
			t.Fatalf("malformed %s parsed without error", raw)
		}
	}
}

func TestSlackAddressing(t *testing.T) {
	mpim := channelEvent("message", "U77", "hi", "4.1", "")
	mpim["channel_type"] = "mpim"
	group := channelEvent("message", "U77", "<@UBOT> hi", "4.2", "")
	group["channel_type"] = "group"
	tests := []struct {
		name               string
		event              map[string]any
		addressed, private bool
		maybe              bool
	}{
		{"direct message", dmEvent("U77", "hi", "4.0"), true, true, false},
		{"group DM without mention", mpim, false, false, false},
		{"private channel mention", group, true, false, false},
		{"app_mention", channelEvent("app_mention", "U77", "<@UBOT>", "4.3", ""), true, false, false},
		{"mention of another user", channelEvent("message", "U77", "<@U99> hi", "4.4", ""), false, false, false},
		{"thread reply without mention", channelEvent("message", "U77", "more", "4.6", "4.5"), false, false, true},
		{"thread reply with mention", channelEvent("message", "U77", "<@UBOT> more", "4.7", "4.5"), true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, err := parseEnvelope(envelopeJSON(t, eventEnvelope("e", tt.event)), fakeBotUser)
			if err != nil || len(items) != 1 {
				t.Fatalf("items = %+v %v", items, err)
			}
			in := items[0]
			if in.Addressed != tt.addressed || in.Private != tt.private || in.MaybeThread != tt.maybe {
				t.Fatalf("addressed=%v private=%v maybe=%v, want %v %v %v", in.Addressed, in.Private, in.MaybeThread, tt.addressed, tt.private, tt.maybe)
			}
		})
	}
}

func TestSlackConversationKey(t *testing.T) {
	parse := func(event map[string]any) inbound {
		items, err := parseEnvelope(envelopeJSON(t, eventEnvelope("e", event)), fakeBotUser)
		if err != nil || len(items) != 1 {
			t.Fatalf("items = %+v %v", items, err)
		}
		return items[0]
	}
	dmThread := dmEvent("U77", "hi", "5.2")
	dmThread["thread_ts"] = "5.1"
	tests := []struct {
		name string
		in   inbound
		want target
	}{
		{"direct message", parse(dmEvent("U77", "hi", "5.0")), target{ChatID: "DU77"}},
		{"direct message thread reply stays in the DM", parse(dmThread), target{ChatID: "DU77"}},
		{"channel mention starts a thread under itself", parse(channelEvent("app_mention", "U77", "<@UBOT> hi", "5.3", "")), target{ChatID: "C1", ThreadID: "5.3"}},
		{"mention inside a thread keeps the thread", parse(channelEvent("app_mention", "U77", "<@UBOT> hi", "5.5", "5.4")), target{ChatID: "C1", ThreadID: "5.4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conversationKey(tt.in); got != tt.want {
				t.Fatalf("conversationKey = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSlackBlocks(t *testing.T) {
	if b := slackBlocks("plain", nil); b != nil {
		t.Fatalf("blocks without buttons = %v, want none", b)
	}
	b := slackBlocks("Timothy wants to run shell", permKeyboard(testPermID))
	if len(b) != 2 || b[0]["type"] != "section" || b[1]["type"] != "actions" {
		t.Fatalf("blocks = %v", b)
	}
	if txt := b[0]["text"].(map[string]any); txt["type"] != "plain_text" || txt["text"] != "Timothy wants to run shell" {
		t.Fatalf("section = %v", txt)
	}
	els := b[1]["elements"].([]map[string]any)
	if len(els) != 3 {
		t.Fatalf("elements = %v", els)
	}
	for i, e := range els {
		if e["type"] != "button" || e["action_id"] != "tmy_"+string(rune('0'+i)) || e["value"] != permKeyboard(testPermID)[0][i].Data {
			t.Fatalf("button %d = %v", i, e)
		}
	}
	rows := [][]button{{{Text: strings.Repeat("o", 100), Data: "a"}}, {{Text: "b", Data: "b"}}}
	els = slackBlocks(strings.Repeat("t", 5000), rows)[1]["elements"].([]map[string]any)
	if len(els) != 2 || els[1]["action_id"] != "tmy_1" || len(els[0]["text"].(map[string]any)["text"].(string)) != slackButtonText {
		t.Fatalf("rows flatten into one actions block with capped labels: %v", els)
	}
	if n := len(slackBlocks(strings.Repeat("t", 5000), rows)[0]["text"].(map[string]any)["text"].(string)); n != slackMessageLimit {
		t.Fatalf("section text is %d chars, want capped at %d", n, slackMessageLimit)
	}
}

func TestSlackSendAndEditBodies(t *testing.T) {
	f := newFakeSlack(t)
	a := f.adapter()
	ctx := context.Background()
	ts, err := a.send(ctx, target{ChatID: "C1", ThreadID: "9.1"}, "a <!channel> & b", nil, true)
	if err != nil || ts == "" {
		t.Fatalf("send = %q %v", ts, err)
	}
	sent := f.callsOf("chat.postMessage")[0].Body
	if sent["channel"] != "C1" || sent["thread_ts"] != "9.1" || sent["text"] != "a &lt;!channel&gt; &amp; b" || sent["blocks"] != nil {
		t.Fatalf("plain send body = %v", sent)
	}
	if _, err := a.send(ctx, target{ChatID: "DU77"}, "pick", permKeyboard(testPermID), false); err != nil {
		t.Fatal(err)
	}
	sent = f.callsOf("chat.postMessage")[1].Body
	if sent["thread_ts"] != nil || len(sent["blocks"].([]any)) != 2 {
		t.Fatalf("buttons send body = %v", sent)
	}
	if err := a.edit(ctx, target{ChatID: "DU77"}, ts, "Allowed once", nil); err != nil {
		t.Fatal(err)
	}
	upd := f.callsOf("chat.update")[0].Body
	if upd["channel"] != "DU77" || upd["ts"] != ts || upd["text"] != "Allowed once" || len(upd["blocks"].([]any)) != 0 {
		t.Fatalf("closing edit body = %v, want empty blocks", upd)
	}
	if err := a.answerPress(ctx, "env", "x"); err != nil {
		t.Fatalf("answerPress = %v", err)
	}
}

func TestChunkReplySlackLimit(t *testing.T) {
	para := strings.Repeat("s", 1400)
	text := strings.Repeat(para+"\n\n", 5)
	chunks := chunkReply(text, slackMessageLimit)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3 (two paragraphs each)", len(chunks))
	}
	for _, c := range chunks {
		if tgLen(c) > slackMessageLimit {
			t.Fatalf("chunk of %d units over %d", tgLen(c), slackMessageLimit)
		}
	}
	if w := streamWindowFor(slackMessageLimit); tgLen(tailView(strings.Repeat("x", 9000), w)) > slackMessageLimit {
		t.Fatalf("slack streaming view over the limit")
	}
	if streamWindowFor(messageLimit) != streamWindow {
		t.Fatal("telegram streaming window changed")
	}
}

func TestSlackErrorMapping(t *testing.T) {
	f := newFakeSlack(t)
	a := f.adapter()
	ctx := context.Background()
	tests := []struct {
		code   string
		status int
	}{
		{"ratelimited", http.StatusTooManyRequests},
		{"invalid_auth", http.StatusUnauthorized},
		{"not_authed", http.StatusUnauthorized},
		{"account_inactive", http.StatusUnauthorized},
		{"channel_not_found", http.StatusNotFound},
		{"internal_error", http.StatusServiceUnavailable},
		{"msg_too_long", http.StatusBadRequest},
	}
	for _, tt := range tests {
		f.mu.Lock()
		f.errCode = tt.code
		f.mu.Unlock()
		_, err := a.send(ctx, target{ChatID: "C1"}, "x", nil, false)
		if apiStatus(err) != tt.status || !strings.Contains(err.Error(), "slack chat.postMessage") || !strings.Contains(err.Error(), tt.code) {
			t.Fatalf("%s = %v (status %d), want %d", tt.code, err, apiStatus(err), tt.status)
		}
	}
	f.mu.Lock()
	f.errCode, f.status, f.retryAfter = "", http.StatusTooManyRequests, "7"
	f.mu.Unlock()
	err := a.edit(ctx, target{ChatID: "C1"}, "1.1", "x", nil)
	if apiStatus(err) != http.StatusTooManyRequests || retryAfter(err) != 7*time.Second {
		t.Fatalf("HTTP 429 = %v retry %v", err, retryAfter(err))
	}
	r := testRunner(newFakeBot(t))
	if d := r.pause(ctx, err); d != 7*time.Second {
		t.Fatalf("pause after Retry-After 7 = %v", d)
	}
	f.mu.Lock()
	f.status = 0
	f.mu.Unlock()
	a.botRef = "MISSING"
	if _, err := a.connect(ctx); err == nil || !strings.Contains(err.Error(), "resolve token") {
		t.Fatalf("missing secret = %v", err)
	}
	a.botRef = "SLACK_APP"
	_, err = a.connect(ctx)
	if apiStatus(err) != http.StatusUnauthorized || strings.Contains(err.Error(), fakeAppToken) {
		t.Fatalf("wrong token = %v", err)
	}
	f.srv.Close()
	a.botRef = "SLACK_BOT"
	if _, err := a.connect(ctx); err == nil || strings.Contains(err.Error(), fakeBotToken) {
		t.Fatalf("transport error = %v, want no token", err)
	}
}

func TestSlackConnectAndDialRedactsTicket(t *testing.T) {
	f := newFakeSlack(t)
	a := f.adapter()
	me, err := a.connect(context.Background())
	if err != nil || me != (identity{ID: fakeBotUser, Username: "timothy"}) || a.botID != fakeBotUser {
		t.Fatalf("connect = %+v %v", me, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	items, _, err := a.receive(ctx, "")
	if err != nil || len(items) != 0 || f.openCount() != 1 {
		t.Fatalf("hello = %+v %v opens %d", items, err, f.openCount())
	}
	cancel()
	if _, _, err := a.receive(ctx, ""); err == nil {
		t.Fatal("read after cancel succeeded")
	}
	if a.conn != nil {
		t.Fatal("a failed read kept the socket")
	}
	// A dial failure must not log the connection ticket.
	a.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/ws") {
			return nil, errors.New("dial refused for " + r.URL.String())
		}
		return f.srv.Client().Transport.RoundTrip(r)
	})}
	_, _, err = a.receive(context.Background(), "")
	if err == nil || strings.Contains(err.Error(), "SECRET-TICKET") || !strings.Contains(err.Error(), "slack socket dial") {
		t.Fatalf("dial error = %v, want the ticket redacted", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDecodeState(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{`{"cursor":"17"}`, "17"},
		{`{"update_offset":42}`, "42"},
		{`{"cursor":"9","update_offset":42}`, "9"},
		{`{}`, ""},
		{`not json`, ""},
	}
	for _, tt := range tests {
		if got := decodeState([]byte(tt.raw)).Cursor; got != tt.want {
			t.Fatalf("decodeState(%s) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestNewAdapter(t *testing.T) {
	tg, err := newAdapter(Channel{Kind: KindTelegram, CredentialRef: "T"}, nil, nil, "", "", nil)
	if err != nil || tg.(*telegramAdapter).api.base != defaultAPIBase || tg.caps().MessageLimit != messageLimit || tg.caps().EditEvery != editEvery ||
		!tg.caps().Edits || !tg.caps().Buttons {
		t.Fatalf("telegram adapter = %+v %v", tg, err)
	}
	sl, err := newAdapter(Channel{Kind: KindSlack, CredentialRef: "B", Config: Config{AppTokenRef: "A"}}, nil, nil, "", "", nil)
	s := sl.(*slackAdapter)
	if err != nil || s.base != defaultSlackBase || s.botRef != "B" || s.appRef != "A" || sl.caps().MessageLimit != slackMessageLimit || sl.caps().EditEvery != slackEditEvery ||
		!sl.caps().Edits || !sl.caps().Buttons {
		t.Fatalf("slack adapter = %+v %v", sl, err)
	}
	if _, err := newAdapter(Channel{Kind: KindEmail}, nil, nil, "", "", nil); !errors.Is(err, ErrKindUnavailable) {
		t.Fatalf("email adapter without a mailbox = %v", err)
	}
	if _, err := newAdapter(Channel{Kind: KindEmail}, nil, nil, "", "", &emailEnv{}); !errors.Is(err, ErrKindUnavailable) {
		t.Fatalf("email adapter with an empty env = %v", err)
	}
	env := &emailEnv{mailbox: func(context.Context, string) (mailbox, error) { return mailbox{}, nil }}
	em, err := newAdapter(Channel{ID: "c1", Kind: KindEmail, Config: Config{ConnectorID: "x", FromAllow: []string{"a@b.co"}}}, nil, nil, "", "", env)
	e, ok := em.(*emailAdapter)
	if err != nil || !ok || e.connectorID != "x" || e.channelID != "c1" || em.caps() != (capabilities{MessageLimit: emailMessageLimit}) {
		t.Fatalf("email adapter = %+v %v", em, err)
	}
	if _, err := newAdapter(Channel{Kind: "fax"}, nil, nil, "", "", env); !errors.Is(err, ErrKindUnavailable) {
		t.Fatalf("unknown kind = %v", err)
	}
}

func TestValidateSlack(t *testing.T) {
	ok := Channel{Name: "s", Kind: KindSlack, CredentialRef: "B", Config: Config{AppTokenRef: "A"}}
	if err := Validate(&ok); err != nil {
		t.Fatalf("valid slack = %v", err)
	}
	for name, c := range map[string]Channel{
		"slack without app token":   {Name: "s", Kind: KindSlack, CredentialRef: "B"},
		"slack with a pasted token": {Name: "s", Kind: KindSlack, CredentialRef: "B", Config: Config{AppTokenRef: "xapp pasted value"}}, // #nosec G101
		"slack without bot token":   {Name: "s", Kind: KindSlack, Config: Config{AppTokenRef: "A"}},
		"telegram with app token":   {Name: "t", Kind: KindTelegram, CredentialRef: "B", Config: Config{AppTokenRef: "A"}},
	} {
		if err := Validate(&c); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s = %v, want ErrInvalid", name, err)
		}
	}
}
