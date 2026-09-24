package channels

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func TestDecidePairing(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	in := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	tests := []struct {
		name string
		p    Pairing
		text string
		want pairingStep
	}{
		{"new sender gets a prompt", Pairing{Status: StatusPending}, "hi", stepPrompt},
		{"prompted recently stays silent", Pairing{Status: StatusPending, LastPromptAt: ago(5 * time.Minute)}, "hi", stepSilent},
		{"prompt again after ten minutes", Pairing{Status: StatusPending, LastPromptAt: ago(10 * time.Minute)}, "hi", stepPrompt},
		{"live code redeems", Pairing{Status: StatusPending, Code: "123456", CodeExpiresAt: in(time.Minute), LastPromptAt: ago(time.Minute)}, " 123456 ", stepRedeem},
		{"expired code does not redeem", Pairing{Status: StatusPending, Code: "123456", CodeExpiresAt: ago(time.Second), LastPromptAt: ago(11 * time.Minute)}, "123456", stepPrompt},
		{"wrong code stays silent inside the window", Pairing{Status: StatusPending, Code: "123456", CodeExpiresAt: in(time.Minute), LastPromptAt: ago(time.Minute)}, "654321", stepSilent},
		{"used code (cleared) does not redeem", Pairing{Status: StatusPending, LastPromptAt: ago(time.Minute)}, "123456", stepSilent},
		{"non-numeric text never redeems", Pairing{Status: StatusPending, Code: "abcdef", CodeExpiresAt: in(time.Minute), LastPromptAt: ago(time.Minute)}, "abcdef", stepSilent},
		{"approved continues", Pairing{Status: StatusApproved}, "hi", stepContinue},
		{"revoked drops", Pairing{Status: StatusRevoked, Code: "123456", CodeExpiresAt: in(time.Minute)}, "123456", stepDrop},
		{"unknown status drops", Pairing{Status: "weird"}, "hi", stepDrop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decidePairing(tt.p, tt.text, now); got != tt.want {
				t.Fatalf("decidePairing = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNewCode(t *testing.T) {
	for range 50 {
		c, err := newCode()
		if err != nil {
			t.Fatal(err)
		}
		if !looksLikeCode(c) {
			t.Fatalf("newCode() = %q, want 6 digits", c)
		}
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	for i := range senderRate {
		if ok, _ := l.allow("a", now); !ok {
			t.Fatalf("message %d refused inside the burst", i+1)
		}
	}
	ok, warn := l.allow("a", now)
	if ok || !warn {
		t.Fatalf("11th message: ok=%v warn=%v, want refused with one warning", ok, warn)
	}
	if ok, warn := l.allow("a", now.Add(time.Second)); ok || warn {
		t.Fatalf("12th message: ok=%v warn=%v, want refused silently", ok, warn)
	}
	if ok, _ := l.allow("b", now); !ok {
		t.Fatal("another sender shares the bucket")
	}
	if ok, _ := l.allow("a", now.Add(7*time.Second)); !ok {
		t.Fatal("a token did not refill after 6s")
	}
	if ok, warn := l.allow("a", now.Add(8*time.Second)); ok || warn {
		t.Fatalf("after refill spent: ok=%v warn=%v, want refused silently (warned under a minute ago)", ok, warn)
	}
	if _, warn := l.allow("a", now.Add(61*time.Second+500*time.Millisecond)); warn {
		t.Fatal("warned while tokens were available")
	}
}

func TestConversationKey(t *testing.T) {
	tests := []struct {
		name       string
		in         inbound
		chat, thrd string
	}{
		{"private", inbound{ChatID: 42, Private: true, ThreadID: 7}, "42", ""},
		{"group without topic", inbound{ChatID: -100, Addressed: true}, "-100", ""},
		{"group topic", inbound{ChatID: -100, ThreadID: 7, Addressed: true}, "-100", "7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, th := conversationKey(tt.in)
			if c != tt.chat || th != tt.thrd {
				t.Fatalf("conversationKey = %q,%q want %q,%q", c, th, tt.chat, tt.thrd)
			}
		})
	}
}

func TestParseUpdate(t *testing.T) {
	bot := botIdentity{ID: 999, Username: "timothy_test_bot"}
	user := &tgUser{ID: 5, FirstName: "Ada", LastName: "L"}
	tests := []struct {
		name      string
		u         tgUpdate
		ok        bool
		text      string
		addressed bool
		thread    int64
	}{
		{"private text", tgUpdate{UpdateID: 1, Message: &tgMessage{From: user, Chat: tgChat{ID: 5, Type: "private"}, Text: "hi"}}, true, "hi", true, 0},
		{"private caption", tgUpdate{UpdateID: 2, Message: &tgMessage{From: user, Chat: tgChat{ID: 5, Type: "private"}, Caption: "look"}}, true, "look", true, 0},
		{"private photo without caption", tgUpdate{UpdateID: 3, Message: &tgMessage{From: user, Chat: tgChat{ID: 5, Type: "private"}}}, true, "", true, 0},
		{"no message", tgUpdate{UpdateID: 4}, false, "", false, 0},
		{"bot sender", tgUpdate{UpdateID: 5, Message: &tgMessage{From: &tgUser{ID: 6, IsBot: true}, Chat: tgChat{ID: 6, Type: "private"}, Text: "x"}}, false, "", false, 0},
		{"group unaddressed", tgUpdate{UpdateID: 6, Message: &tgMessage{From: user, Chat: tgChat{ID: -1, Type: "group"}, Text: "hello all"}}, true, "hello all", false, 0},
		{"group mention", tgUpdate{UpdateID: 7, Message: &tgMessage{From: user, Chat: tgChat{ID: -1, Type: "supergroup"}, MessageThreadID: 12,
			Text: "héllo @Timothy_Test_Bot", Entities: []tgEntity{{Type: "mention", Offset: 6, Length: 17}}}}, true, "héllo @Timothy_Test_Bot", true, 12},
		{"group mention of another bot", tgUpdate{UpdateID: 8, Message: &tgMessage{From: user, Chat: tgChat{ID: -1, Type: "group"},
			Text: "@other_bot hi", Entities: []tgEntity{{Type: "mention", Offset: 0, Length: 10}}}}, true, "@other_bot hi", false, 0},
		{"group text_mention", tgUpdate{UpdateID: 9, Message: &tgMessage{From: user, Chat: tgChat{ID: -1, Type: "group"},
			Text: "Timothy hi", Entities: []tgEntity{{Type: "text_mention", Offset: 0, Length: 7, User: &tgUser{ID: 999}}}}}, true, "Timothy hi", true, 0},
		{"group reply to bot", tgUpdate{UpdateID: 10, Message: &tgMessage{From: user, Chat: tgChat{ID: -1, Type: "group"}, Text: "and?",
			ReplyTo: &tgMessage{From: &tgUser{ID: 999, IsBot: true}}}}, true, "and?", true, 0},
		{"group reply to another bot", tgUpdate{UpdateID: 11, Message: &tgMessage{From: user, Chat: tgChat{ID: -1, Type: "group"}, Text: "and?",
			ReplyTo: &tgMessage{From: &tgUser{ID: 1000, IsBot: true}}}}, true, "and?", false, 0},
		{"caption mention", tgUpdate{UpdateID: 12, Message: &tgMessage{From: user, Chat: tgChat{ID: -1, Type: "group"},
			Caption: "@timothy_test_bot see", CaptionEntities: []tgEntity{{Type: "mention", Offset: 0, Length: 17}}}}, true, "@timothy_test_bot see", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, ok := parseUpdate(tt.u, bot)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if in.Text != tt.text || in.Addressed != tt.addressed || in.ThreadID != tt.thread {
				t.Fatalf("parsed text=%q addressed=%v thread=%d, want %q %v %d", in.Text, in.Addressed, in.ThreadID, tt.text, tt.addressed, tt.thread)
			}
			if in.UserID != "5" || in.DisplayName != "Ada L" {
				t.Fatalf("sender = %q %q", in.UserID, in.DisplayName)
			}
		})
	}
}

func TestChunkReply(t *testing.T) {
	para := strings.Repeat("a", 30)
	text := para + "\n\n" + para + "\n\n" + para
	got := chunkReply(text, 70)
	if len(got) != 2 || got[0] != para+"\n\n"+para || got[1] != para {
		t.Fatalf("paragraph split = %q", got)
	}
	got = chunkReply("one two three four", 9)
	if strings.Join(got, "|") != "one two|three|four" {
		t.Fatalf("space split = %q", got)
	}
	got = chunkReply(strings.Repeat("x", 25), 10)
	if strings.Join(got, "|") != "xxxxxxxxxx|xxxxxxxxxx|xxxxx" {
		t.Fatalf("hard split = %q", got)
	}
	emoji := strings.Repeat("\U0001F600", 5) // 2 UTF-16 units each
	for _, c := range chunkReply(emoji, 4) {
		if tgLen(c) > 4 {
			t.Fatalf("chunk %q is %d units, over 4", c, tgLen(c))
		}
	}
	if got := chunkReply("short", messageLimit); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short = %q", got)
	}
	long := strings.Repeat(para+"\n\n", 400)
	for _, c := range chunkReply(long, messageLimit) {
		if tgLen(c) > messageLimit {
			t.Fatalf("chunk over limit: %d", tgLen(c))
		}
	}
}

func TestStreamingView(t *testing.T) {
	if got := streamingView("hello"); got != "hello" {
		t.Fatalf("short view = %q", got)
	}
	text := strings.Repeat("a", 100) + strings.Repeat("b", streamWindow)
	got := streamingView(text)
	if got != "..."+strings.Repeat("b", streamWindow) {
		t.Fatalf("tail view has %d units, want ... plus the last %d", tgLen(got), streamWindow)
	}
	emoji := strings.Repeat("\U0001F600", streamWindow)
	if v := streamingView(emoji); tgLen(v) > streamWindow+3 {
		t.Fatalf("emoji view is %d units", tgLen(v))
	}
}

func TestEditThrottle(t *testing.T) {
	var th editThrottle
	if _, ok := th.due(""); ok {
		t.Fatal("empty text is due")
	}
	if v, ok := th.due("Hel"); !ok || v != "Hel" {
		t.Fatalf("first view = %q %v", v, ok)
	}
	if _, ok := th.due("Hel"); ok {
		t.Fatal("unchanged view is due")
	}
	if v, ok := th.due("Hello"); !ok || v != "Hello" {
		t.Fatalf("changed view = %q %v", v, ok)
	}
}

func TestBotAPIRedactsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "bad request for " + r.URL.Path})
	}))
	b := &botAPI{http: srv.Client(), base: srv.URL, resolve: fakeResolve, ref: "TELEGRAM_BOT"}
	_, err := b.getMe(context.Background())
	if err == nil || strings.Contains(err.Error(), fakeToken) || !strings.Contains(err.Error(), "REDACTED") {
		t.Fatalf("api error = %v, want the token redacted", err)
	}
	if apiStatus(err) != 400 {
		t.Fatalf("status = %d, want 400", apiStatus(err))
	}
	srv.Close()
	_, err = b.getMe(context.Background())
	if err == nil || strings.Contains(err.Error(), fakeToken) {
		t.Fatalf("transport error = %v, want the token redacted", err)
	}
	b.ref = "MISSING"
	if _, err := b.getMe(context.Background()); err == nil || !strings.Contains(err.Error(), "resolve bot token") {
		t.Fatalf("missing secret = %v", err)
	}
}

func TestEditIgnoresNotModified(t *testing.T) {
	f := newFakeBot(t)
	f.editErr = "Bad Request: message is not modified: specified new message content is exactly the same"
	if err := f.api().editMessageText(context.Background(), 1, 2, "x"); err != nil {
		t.Fatalf("not modified = %v, want nil", err)
	}
	f.editErr = "Bad Request: message to edit not found"
	if err := f.api().editMessageText(context.Background(), 1, 2, "x"); err == nil {
		t.Fatal("other edit errors must surface")
	}
}

func testRunner(f *fakeBot) *telegramRunner {
	s := &Service{log: discardLog(), now: time.Now, editEvery: time.Hour}
	return &telegramRunner{svc: s, ch: Channel{ID: "c1"}, bot: f.api(), limits: newLimiter(), workers: map[string]chan turnJob{}}
}

func editTexts(f *fakeBot) []string {
	var out []string
	for _, c := range f.callsOf("editMessageText") {
		out = append(out, c.Body["text"].(string))
	}
	return out
}

func TestDrainEditsOnTicksAndFinalizes(t *testing.T) {
	f := newFakeBot(t)
	r := testRunner(f)
	events := make(chan stream.StreamEvent)
	tick := make(chan time.Time)
	done := make(chan string)
	go func() { done <- r.drain(context.Background(), turnJob{chatID: 7}, 55, events, tick) }()

	events <- stream.StreamEvent{Type: stream.EventChunk, Text: "Hel"}
	tick <- time.Now()
	tick <- time.Now() // unchanged: no edit
	events <- stream.StreamEvent{Type: stream.EventToolStart, ToolCall: &stream.ToolCallEvent{Name: "search_web"}}
	events <- stream.StreamEvent{Type: stream.EventChunk, Text: "lo"}
	events <- stream.StreamEvent{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{Tool: "shell"}}
	events <- stream.StreamEvent{Type: stream.EventPermissionResolved, Resolved: &stream.PermissionResolvedEvent{Decision: "once"}}
	events <- stream.StreamEvent{Type: stream.EventChunk, Text: " world"}
	events <- stream.StreamEvent{Type: stream.EventDone}
	if got := <-done; got != "Hello world" {
		t.Fatalf("drain returned %q", got)
	}
	want := []string{"Hel", "Hello" + msgWaitingNote, "Hello world"}
	if got := editTexts(f); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("edits = %q, want %q", got, want)
	}
	for _, c := range f.callsOf("editMessageText") {
		if c.Body["message_id"].(float64) != 55 || c.Body["parse_mode"] != nil {
			t.Fatalf("edit body = %v", c.Body)
		}
	}
}

func TestDrainSplitsLongFinalReply(t *testing.T) {
	f := newFakeBot(t)
	r := testRunner(f)
	events := make(chan stream.StreamEvent, 2)
	para := strings.Repeat("p", 3000)
	events <- stream.StreamEvent{Type: stream.EventChunk, Text: para + "\n\n" + para}
	events <- stream.StreamEvent{Type: stream.EventDone}
	r.drain(context.Background(), turnJob{chatID: 7}, 55, events, nil)
	if got := editTexts(f); len(got) != 1 || got[0] != para {
		t.Fatalf("placeholder edit = %d edits", len(got))
	}
	sends := f.callsOf("sendMessage")
	if len(sends) != 1 || sends[0].Body["text"] != para {
		t.Fatalf("follow-up sends = %d", len(sends))
	}
}

func TestDrainRendersError(t *testing.T) {
	f := newFakeBot(t)
	r := testRunner(f)
	events := make(chan stream.StreamEvent, 2)
	events <- stream.StreamEvent{Type: stream.EventChunk, Text: "partial"}
	events <- stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{Message: strings.Repeat("e", 400)}}
	r.drain(context.Background(), turnJob{chatID: 7}, 55, events, nil)
	got := editTexts(f)
	if len(got) != 1 || got[0] != "Something went wrong: "+strings.Repeat("e", failureCap) {
		t.Fatalf("error edit = %q", got)
	}
}

func TestDrainEmptyReplyAndClosedStream(t *testing.T) {
	f := newFakeBot(t)
	r := testRunner(f)
	events := make(chan stream.StreamEvent)
	close(events)
	r.drain(context.Background(), turnJob{chatID: 7}, 55, events, nil)
	if got := editTexts(f); len(got) != 1 || got[0] != msgNoReply {
		t.Fatalf("closed stream edit = %q", got)
	}
}

func TestPauseBackoff(t *testing.T) {
	r := testRunner(newFakeBot(t))
	ctx := context.Background()
	if d := r.pause(ctx, &apiError{Status: 409}); d != conflictBackoff || !r.conflictLogged {
		t.Fatalf("409 pause = %v", d)
	}
	var got []time.Duration
	for range 8 {
		got = append(got, r.pause(ctx, errors.New("boom")))
	}
	if got[0] != time.Second || got[1] != 2*time.Second || got[7] != maxBackoff {
		t.Fatalf("backoff = %v", got)
	}
}
