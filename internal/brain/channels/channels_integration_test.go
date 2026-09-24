//go:build integration

package channels

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const marker = "itest-channel-"

func testStore(t *testing.T) (*Store, *pgpool.Pool) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := discardLog()
	poolCtx, poolCancel := context.WithCancel(context.Background())
	t.Cleanup(poolCancel)
	pool := pgpool.New(poolCtx, dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		rows, _ := db.Query(cctx, `SELECT cc.session_id::text FROM channel_conversations cc JOIN channels c ON c.id = cc.channel_id WHERE c.name LIKE $1 || '%'`, marker)
		var sessions []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				sessions = append(sessions, id)
			}
		}
		rows.Close()
		_, _ = db.Exec(cctx, `DELETE FROM channels WHERE name LIKE $1 || '%'`, marker)
		for _, id := range sessions {
			_, _ = db.Exec(cctx, `DELETE FROM session_events WHERE session_id = $1`, id)
			_, _ = db.Exec(cctx, `DELETE FROM sessions WHERE id = $1`, id)
		}
	})
	return NewStore(pool), pool
}

func runTag() string { return fmt.Sprintf("%s%d-", marker, time.Now().UnixNano()) }

func createChannel(t *testing.T, s *Store, name string) string {
	t.Helper()
	id, err := s.Create(t.Context(), Channel{Name: name, Kind: KindTelegram, CredentialRef: "TELEGRAM_BOT", Enabled: true})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return id
}

func TestStoreChannelCRUD(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	tag := runTag()
	changes := 0
	s.SetOnChange(func(context.Context) { changes++ })

	id := createChannel(t, s, tag+"bot")
	if _, err := s.Create(ctx, Channel{Name: " " + strings.ToUpper(tag) + "BOT ", Kind: KindTelegram, CredentialRef: "X"}); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("duplicate name = %v, want ErrNameConflict", err)
	}
	if _, err := s.Create(ctx, Channel{Name: tag + "email", Kind: "email", CredentialRef: "X"}); !errors.Is(err, ErrKindUnavailable) {
		t.Fatalf("email = %v, want ErrKindUnavailable", err)
	}
	if _, err := s.Create(ctx, Channel{Name: tag + "slack", Kind: KindSlack, CredentialRef: "X"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("slack without app token = %v, want ErrInvalid", err)
	}
	if _, err := s.Create(ctx, Channel{Name: tag + "noref", Kind: KindTelegram}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing credential_ref = %v, want ErrInvalid", err)
	}
	if _, err := s.Create(ctx, Channel{Name: tag + "agent", Kind: KindTelegram, CredentialRef: "X", AgentID: "00000000-0000-0000-0000-000000000000"}); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("unknown agent = %v, want ErrUnknownAgent", err)
	}

	db, _ := pool.Get()
	var agentID string
	if err := db.QueryRow(ctx, `SELECT id FROM agents WHERE is_default LIMIT 1`).Scan(&agentID); err != nil {
		t.Fatalf("default agent: %v", err)
	}
	name, ref, dispatch, off := tag+"renamed", "OTHER_BOT", true, false
	if err := s.Patch(ctx, id, Patch{Name: &name, CredentialRef: &ref, AgentID: &agentID, Dispatch: &dispatch, Enabled: &off}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	c, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c.Name != name || c.CredentialRef != ref || c.AgentID != agentID || !c.Config.Dispatch || c.Enabled {
		t.Fatalf("patched = %+v", c)
	}
	empty := ""
	if err := s.Patch(ctx, id, Patch{AgentID: &empty}); err != nil {
		t.Fatalf("clear agent: %v", err)
	}
	bad := "not-a-uuid"
	if err := s.Patch(ctx, id, Patch{AgentID: &bad}); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("bad agent patch = %v", err)
	}
	if err := s.Patch(ctx, "00000000-0000-0000-0000-000000000000", Patch{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("patch missing = %v", err)
	}
	if err := s.Patch(ctx, "nope", Patch{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("patch malformed id = %v", err)
	}
	before, _ := s.Get(ctx, id)
	if err := s.SetBotUsername(ctx, id, "timothy_test_bot"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetState(ctx, id, State{Cursor: "42"}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(ctx, id)
	if after.Config.BotUsername != "timothy_test_bot" || !after.Config.Dispatch || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("username/state must not bump updated_at: %+v", after)
	}
	if st, err := s.GetState(ctx, id); err != nil || st.Cursor != "42" {
		t.Fatalf("state = %+v %v", st, err)
	}
	if c.Pairings != (PairingCounts{}) {
		t.Fatalf("counts = %+v", c.Pairings)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted = %v", err)
	}
	if err := s.Delete(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice = %v", err)
	}
	if changes != 4 {
		t.Fatalf("onChange fired %d times, want 4 (create, two patches, delete)", changes)
	}
}

func TestStorePairingsInboundAndConversations(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	id := createChannel(t, s, runTag()+"pair")
	now := time.Now().UTC().Truncate(time.Microsecond)

	p, created, err := s.EnsurePairing(ctx, id, "77", "Ada", now)
	if err != nil || !created || p.Status != StatusPending {
		t.Fatalf("ensure new = %+v %v %v", p, created, err)
	}
	if p, created, _ = s.EnsurePairing(ctx, id, "77", "Ada L", now); created || p.DisplayName != "Ada L" {
		t.Fatalf("ensure existing = %+v %v", p, created)
	}
	if err := s.IssueCode(ctx, id, "77", "123456", now.Add(codeTTL), now); err != nil {
		t.Fatal(err)
	}
	p, _, _ = s.EnsurePairing(ctx, id, "77", "Ada L", now)
	if p.Code != "123456" || p.LastPromptAt == nil || !p.LastPromptAt.Equal(now) {
		t.Fatalf("issued = %+v", p)
	}
	if ok, _ := s.RedeemCode(ctx, id, "77", "654321", now); ok {
		t.Fatal("wrong code redeemed")
	}
	if ok, _ := s.RedeemCode(ctx, id, "77", "123456", now.Add(codeTTL+time.Second)); ok {
		t.Fatal("expired code redeemed")
	}
	if ok, err := s.RedeemCode(ctx, id, "77", "123456", now.Add(time.Minute)); !ok || err != nil {
		t.Fatalf("redeem = %v %v", ok, err)
	}
	if ok, _ := s.RedeemCode(ctx, id, "77", "123456", now.Add(time.Minute)); ok {
		t.Fatal("code redeemed twice")
	}
	p, _, _ = s.EnsurePairing(ctx, id, "77", "Ada L", now)
	if p.Status != StatusApproved || p.Code != "" {
		t.Fatalf("after redeem = %+v", p)
	}
	if err := s.Revoke(ctx, id, "77"); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, id, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approve missing = %v", err)
	}
	_, _, _ = s.EnsurePairing(ctx, id, "88", "Bob", now)
	list, err := s.ListPairings(ctx, id)
	if err != nil || len(list) != 2 || list[0].ExternalUserID != "88" || list[1].Status != StatusRevoked {
		t.Fatalf("list = %+v %v", list, err)
	}
	c, _ := s.Get(ctx, id)
	if c.Pairings != (PairingCounts{Pending: 1, Revoked: 1}) {
		t.Fatalf("counts = %+v", c.Pairings)
	}

	if fresh, _ := s.MarkInbound(ctx, id, "telegram:1"); !fresh {
		t.Fatal("first inbound not fresh")
	}
	if fresh, _ := s.MarkInbound(ctx, id, "telegram:1"); fresh {
		t.Fatal("repeat inbound fresh")
	}

	if _, found, _ := s.ConversationFor(ctx, id, "77", ""); found {
		t.Fatal("conversation before create")
	}
	conv, err := s.CreateConversation(ctx, Conversation{ChannelID: id, ExternalChatID: "77", ExternalUserID: "77"}, "Telegram: Ada")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	got, found, err := s.ConversationFor(ctx, id, "77", "")
	if err != nil || !found || got.SessionID != conv.SessionID || got.ID != conv.ID {
		t.Fatalf("conversation for = %+v %v %v", got, found, err)
	}
	db, _ := pool.Get()
	var origin, convRef, title, kind string
	if err := db.QueryRow(ctx, `SELECT s.origin_kind, s.channel_conversation_id::text, s.title, e.kind FROM sessions s
		JOIN session_events e ON e.session_id = s.id AND e.seq = 1 WHERE s.id = $1`, conv.SessionID).Scan(&origin, &convRef, &title, &kind); err != nil {
		t.Fatal(err)
	}
	if origin != "channel" || convRef != conv.ID || title != "Telegram: Ada" || kind != "session_started" {
		t.Fatalf("session = %s %s %s %s", origin, convRef, title, kind)
	}
	if err := s.TouchConversation(ctx, conv.ID, now); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(ctx, `UPDATE channel_inbound SET received_at = now() - interval '8 days' WHERE channel_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE channel_pairings SET updated_at = now() - interval '2 days' WHERE channel_id = $1 AND external_user_id = '88'`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Sweep(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := s.MarkInbound(ctx, id, "telegram:1"); !fresh {
		t.Fatal("sweep kept an 8-day-old inbound row")
	}
	list, _ = s.ListPairings(ctx, id)
	if len(list) != 1 || list[0].ExternalUserID != "77" {
		t.Fatalf("sweep pairings = %+v", list)
	}
}

// fakeChat records turns and streams three deltas per turn; release,
// when set, holds each turn until it receives.
type fakeChat struct {
	mu      sync.Mutex
	reqs    []chat.Request
	release chan struct{}
}

func (c *fakeChat) chat(_ context.Context, req chat.Request) (string, <-chan stream.StreamEvent, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	release := c.release
	c.mu.Unlock()
	out := make(chan stream.StreamEvent, 8)
	go func() {
		defer close(out)
		if release != nil {
			<-release
		}
		for _, d := range []string{"Hello", " there", " friend"} {
			out <- stream.StreamEvent{Type: stream.EventChunk, Text: d}
			time.Sleep(40 * time.Millisecond)
		}
		out <- stream.StreamEvent{Type: stream.EventDone}
	}()
	return req.SessionID, out, nil
}

func (c *fakeChat) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func startService(t *testing.T, s *Store, f *fakeBot, fc *fakeChat) (stop func()) {
	t.Helper()
	svc := New(s, fc.chat, MissionDeps{}, fakeResolve, f.srv.Client(), discardLog())
	svc.APIBase = f.srv.URL
	svc.editEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(ctx, nil)
	}()
	return func() {
		cancel()
		<-done
	}
}

func sendTexts(f *fakeBot) []string {
	var out []string
	for _, c := range f.callsOf("sendMessage") {
		out = append(out, c.Body["text"].(string))
	}
	return out
}

func TestRunnerPairingThenChat(t *testing.T) {
	s, pool := testStore(t)
	ctx := t.Context()
	id := createChannel(t, s, runTag()+"e2e")
	f := newFakeBot(t)
	fc := &fakeChat{}

	// Unknown sender: one prompt, zero model calls, offset persisted.
	f.script(privateUpdate(1, 77, "hello"))
	stop := startService(t, s, f, fc)
	f.waitConsumed(t)
	if got := sendTexts(f); len(got) != 1 || got[0] != msgPairingPrompt {
		t.Fatalf("unknown sender replies = %q", got)
	}
	if fc.count() != 0 {
		t.Fatalf("unpaired sender reached the model %d times", fc.count())
	}
	if st, _ := s.GetState(ctx, id); st.Cursor != "2" {
		t.Fatalf("offset after batch = %q, want 2", st.Cursor)
	}
	pairings, _ := s.ListPairings(ctx, id)
	if len(pairings) != 1 || pairings[0].Status != StatusPending || !looksLikeCode(pairings[0].Code) {
		t.Fatalf("pairing = %+v", pairings)
	}
	// A second message inside ten minutes gets no second prompt.
	f.script(privateUpdate(2, 77, "anyone?"))
	f.waitConsumed(t)
	if got := sendTexts(f); len(got) != 1 {
		t.Fatalf("prompt throttle: replies = %q", got)
	}

	// Approved sender: conversation, session, streamed reply.
	if err := s.Approve(ctx, id, "77"); err != nil {
		t.Fatal(err)
	}
	f.script(privateUpdate(3, 77, "what's up"))
	waitFor(t, "the final edit", func() bool {
		edits := editTexts(f)
		return len(edits) > 0 && edits[len(edits)-1] == "Hello there friend"
	})
	if fc.count() != 1 || fc.reqs[0].Message != "what's up" || fc.reqs[0].Agent != "" || fc.reqs[0].SessionID == "" {
		t.Fatalf("chat requests = %+v", fc.reqs)
	}
	sends := f.callsOf("sendMessage")
	if len(sends) != 2 || sends[1].Body["text"] != msgThinking || sends[1].Body["disable_notification"] != true {
		t.Fatalf("sends = %+v", sends)
	}
	db, _ := pool.Get()
	var origin, convID string
	if err := db.QueryRow(ctx, `SELECT s.origin_kind, cc.id::text FROM sessions s JOIN channel_conversations cc ON cc.session_id = s.id
		WHERE s.id = $1 AND s.channel_conversation_id = cc.id AND cc.channel_id = $2`, fc.reqs[0].SessionID, id).Scan(&origin, &convID); err != nil {
		t.Fatalf("session lookup: %v", err)
	}
	if origin != "channel" {
		t.Fatalf("origin_kind = %s", origin)
	}

	// Redelivered update id: ignored.
	f.script(privateUpdate(3, 77, "what's up"))
	f.waitConsumed(t)
	if fc.count() != 1 || len(f.callsOf("sendMessage")) != 2 {
		t.Fatalf("duplicate update produced work: chats=%d sends=%d", fc.count(), len(f.callsOf("sendMessage")))
	}

	// A fresh service resumes from the persisted offset.
	stop()
	before := len(f.pollOffsets())
	stop = startService(t, s, f, fc)
	waitFor(t, "a poll after restart", func() bool { return len(f.pollOffsets()) > before })
	if got := f.pollOffsets()[before]; got != 4 {
		t.Fatalf("first poll after restart at offset %d, want 4", got)
	}

	// Revoked sender: no reply, no model call.
	if err := s.Revoke(ctx, id, "77"); err != nil {
		t.Fatal(err)
	}
	f.script(privateUpdate(4, 77, "still there?"))
	f.waitConsumed(t)
	if fc.count() != 1 || len(f.callsOf("sendMessage")) != 2 {
		t.Fatalf("revoked sender produced work: chats=%d sends=%d", fc.count(), len(f.callsOf("sendMessage")))
	}
	stop()
}

func TestServiceFollowsChannelChanges(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	id := createChannel(t, s, runTag()+"follow")
	f := newFakeBot(t)
	stop := startService(t, s, f, &fakeChat{})
	defer stop()
	waitFor(t, "the runner to start", func() bool { return len(f.callsOf("getMe")) == 1 })

	off := false
	if err := s.Patch(ctx, id, Patch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	polls := len(f.callsOf("getUpdates"))
	time.Sleep(700 * time.Millisecond)
	if got := len(f.callsOf("getUpdates")); got != polls {
		t.Fatalf("a disabled channel kept polling: %d -> %d", polls, got)
	}

	on := true
	if err := s.Patch(ctx, id, Patch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the runner to restart", func() bool { return len(f.callsOf("getMe")) == 2 })
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	polls = len(f.callsOf("getUpdates"))
	time.Sleep(700 * time.Millisecond)
	if got := len(f.callsOf("getUpdates")); got != polls {
		t.Fatalf("a deleted channel kept polling: %d -> %d", polls, got)
	}
}

func TestRunnerQueueDepth(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	id := createChannel(t, s, runTag()+"queue")
	if _, _, err := s.EnsurePairing(ctx, id, "77", "Ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, id, "77"); err != nil {
		t.Fatal(err)
	}
	f := newFakeBot(t)
	fc := &fakeChat{release: make(chan struct{})}
	f.script(privateUpdate(1, 77, "one"))
	stop := startService(t, s, f, fc)
	defer stop()
	waitFor(t, "the first turn", func() bool { return fc.count() == 1 })
	f.script(privateUpdate(2, 77, "two"), privateUpdate(3, 77, "three"), privateUpdate(4, 77, "four"), privateUpdate(5, 77, "five"))
	f.waitConsumed(t)
	var full int
	for _, txt := range sendTexts(f) {
		if txt == msgQueueFull {
			full++
		}
	}
	if full != 1 {
		t.Fatalf("queue-full replies = %d, want 1 (one running, three queued)", full)
	}
	close(fc.release)
	waitFor(t, "the queued turns", func() bool { return fc.count() == 4 })
	var msgs []string
	for _, r := range fc.reqs {
		msgs = append(msgs, r.Message)
	}
	if strings.Join(msgs, ",") != "one,two,three,four" {
		t.Fatalf("turn order = %v", msgs)
	}
}

// script queues one getUpdates batch.
func (f *fakeBot) script(updates ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, updates)
}

// waitConsumed waits until every scripted batch was served and fully
// handled (a later poll arrived).
func (f *fakeBot) waitConsumed(t *testing.T) {
	t.Helper()
	waitFor(t, "scripted updates to be handled", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.batches) == 0 && f.pollsSinceServe >= 1
	})
}

func (f *fakeBot) pollOffsets() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.offsets...)
}

// privateUpdate is a private-chat text message update.
func privateUpdate(updateID, userID int64, text string) map[string]any {
	return map[string]any{
		"update_id": updateID,
		"message": map[string]any{
			"message_id": updateID,
			"from":       map[string]any{"id": userID, "is_bot": false, "first_name": "Ada"},
			"chat":       map[string]any{"id": userID, "type": "private"},
			"text":       text,
		},
	}
}

// waitFor polls cond for up to 5 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
