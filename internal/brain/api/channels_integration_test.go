//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/channels"
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const fakeBotID = "42:API-TEST-TOKEN"

type channelHarness struct {
	t     *testing.T
	mux   *http.ServeMux
	store *channels.Store
	pool  *pgpool.Pool
	tag   string
}

func newChannelHarness(t *testing.T) *channelHarness {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
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
	tag := fmt.Sprintf("itest-api-channel-%d-", time.Now().UnixNano())
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Exec(cctx, `DELETE FROM channels WHERE name LIKE $1 || '%'`, tag)
	})
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+fakeBotID+"/getMe" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":7,"is_bot":true,"username":"api_test_bot"}}`)
	}))
	t.Cleanup(fake.Close)
	resolve := func(_ context.Context, ref string) (string, error) {
		if ref == "GOOD_BOT" {
			return fakeBotID, nil
		}
		return "wrong-token", nil
	}
	noChat := func(context.Context, chat.Request) (string, <-chan stream.StreamEvent, error) {
		t.Error("the API must never start a chat turn")
		return "", nil, nil
	}
	store := channels.NewStore(pool)
	svc := channels.New(store, noChat, channels.MissionDeps{}, resolve, fake.Client(), log)
	svc.APIBase = fake.URL
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerChannels(m.Handle, store, svc)
	return &channelHarness{t: t, mux: m, store: store, pool: pool, tag: tag}
}

func (h *channelHarness) do(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, req)
	return w
}

func (h *channelHarness) decode(w *httptest.ResponseRecorder, v any) {
	h.t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		h.t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
}

func (h *channelHarness) create(name, ref string) string {
	h.t.Helper()
	w := h.do("POST", "/v1/channels", fmt.Sprintf(`{"name":%q,"kind":"telegram","credential_ref":%q,"config":{"dispatch":true}}`, h.tag+name, ref))
	if w.Code != 201 {
		h.t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var out struct{ ID string }
	h.decode(w, &out)
	return out.ID
}

func TestChannelsAPICRUD(t *testing.T) {
	h := newChannelHarness(t)
	id := h.create("bot", "GOOD_BOT")

	if w := h.do("POST", "/v1/channels", fmt.Sprintf(`{"name":%q,"kind":"slack","credential_ref":"X"}`, h.tag+"slack")); w.Code != 400 || !strings.Contains(w.Body.String(), "not_available") {
		t.Fatalf("slack = %d %s", w.Code, w.Body.String())
	}
	if w := h.do("POST", "/v1/channels", fmt.Sprintf(`{"name":%q,"kind":"telegram"}`, h.tag+"noref")); w.Code != 400 {
		t.Fatalf("missing credential_ref = %d %s", w.Code, w.Body.String())
	}
	if w := h.do("POST", "/v1/channels", fmt.Sprintf(`{"name":%q,"kind":"telegram","credential_ref":"X"}`, h.tag+"BOT")); w.Code != 409 {
		t.Fatalf("duplicate = %d %s", w.Code, w.Body.String())
	}

	w := h.do("GET", "/v1/channels/"+id, "")
	var c channels.Channel
	h.decode(w, &c)
	if w.Code != 200 || c.Name != h.tag+"bot" || !c.Config.Dispatch || !c.Enabled || c.CredentialRef != "GOOD_BOT" {
		t.Fatalf("get = %d %+v", w.Code, c)
	}

	w = h.do("PATCH", "/v1/channels/"+id, `{"enabled":false,"agent_id":null,"config":{"dispatch":false}}`)
	h.decode(w, &c)
	if w.Code != 200 || c.Enabled || c.Config.Dispatch || c.AgentID != "" {
		t.Fatalf("patch = %d %+v", w.Code, c)
	}
	if w := h.do("PATCH", "/v1/channels/"+id, `{"agent_id":"00000000-0000-0000-0000-000000000000"}`); w.Code != 400 {
		t.Fatalf("unknown agent = %d %s", w.Code, w.Body.String())
	}

	w = h.do("GET", "/v1/channels", "")
	var list struct{ Channels []channels.Channel }
	h.decode(w, &list)
	found := false
	for _, ch := range list.Channels {
		found = found || ch.ID == id
	}
	if w.Code != 200 || !found {
		t.Fatalf("list = %d, found=%v", w.Code, found)
	}

	if w := h.do("DELETE", "/v1/channels/"+id, ""); w.Code != 204 {
		t.Fatalf("delete = %d", w.Code)
	}
	if w := h.do("GET", "/v1/channels/"+id, ""); w.Code != 404 {
		t.Fatalf("get deleted = %d", w.Code)
	}
	if w := h.do("PATCH", "/v1/channels/"+id, `{"enabled":true}`); w.Code != 404 {
		t.Fatalf("patch deleted = %d", w.Code)
	}
}

func TestChannelsAPIPairings(t *testing.T) {
	h := newChannelHarness(t)
	id := h.create("pair", "GOOD_BOT")
	ctx := t.Context()
	now := time.Now()
	if _, _, err := h.store.EnsurePairing(ctx, id, "77", "Ada", now); err != nil {
		t.Fatal(err)
	}
	if err := h.store.IssueCode(ctx, id, "77", "123456", now.Add(10*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.EnsurePairing(ctx, id, "88", "Bob", now); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Approve(ctx, id, "88"); err != nil {
		t.Fatal(err)
	}

	w := h.do("GET", "/v1/channels/"+id+"/pairings", "")
	var out struct{ Pairings []channels.Pairing }
	h.decode(w, &out)
	if w.Code != 200 || len(out.Pairings) != 2 || out.Pairings[0].ExternalUserID != "77" || out.Pairings[0].Code != "123456" || out.Pairings[0].CodeExpiresAt == nil {
		t.Fatalf("pairings = %d %+v", w.Code, out.Pairings)
	}
	if strings.Contains(w.Body.String(), "last_prompt_at") {
		t.Fatal("last_prompt_at leaked into the API")
	}

	if w := h.do("POST", "/v1/channels/"+id+"/pairings/77/approve", ""); w.Code != 204 {
		t.Fatalf("approve = %d %s", w.Code, w.Body.String())
	}
	if w := h.do("POST", "/v1/channels/"+id+"/pairings/88/revoke", ""); w.Code != 204 {
		t.Fatalf("revoke = %d %s", w.Code, w.Body.String())
	}
	w = h.do("GET", "/v1/channels/"+id+"/pairings", "")
	var after struct{ Pairings []channels.Pairing }
	h.decode(w, &after)
	status := map[string]string{}
	for _, p := range after.Pairings {
		status[p.ExternalUserID] = p.Status
		if p.Code != "" {
			t.Fatalf("code survived a decision: %+v", p)
		}
	}
	if status["77"] != "approved" || status["88"] != "revoked" {
		t.Fatalf("statuses = %v", status)
	}
	w = h.do("GET", "/v1/channels/"+id, "")
	var c channels.Channel
	h.decode(w, &c)
	if c.Pairings != (channels.PairingCounts{Approved: 1, Revoked: 1}) {
		t.Fatalf("counts = %+v", c.Pairings)
	}
	if w := h.do("POST", "/v1/channels/"+id+"/pairings/99/approve", ""); w.Code != 404 {
		t.Fatalf("approve unknown sender = %d", w.Code)
	}
	if w := h.do("GET", "/v1/channels/00000000-0000-0000-0000-000000000000/pairings", ""); w.Code != 404 {
		t.Fatalf("pairings of unknown channel = %d", w.Code)
	}
}

func TestChannelsAPITest(t *testing.T) {
	h := newChannelHarness(t)
	good := h.create("good", "GOOD_BOT")
	bad := h.create("bad", "BAD_BOT")

	w := h.do("POST", "/v1/channels/"+good+"/test", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"bot_username":"api_test_bot"`) {
		t.Fatalf("test good = %d %s", w.Code, w.Body.String())
	}
	w = h.do("GET", "/v1/channels/"+good, "")
	var c channels.Channel
	h.decode(w, &c)
	if c.Config.BotUsername != "api_test_bot" {
		t.Fatalf("bot_username not recorded: %+v", c.Config)
	}

	w = h.do("POST", "/v1/channels/"+bad+"/test", "")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "test_failed") || strings.Contains(w.Body.String(), "wrong-token") {
		t.Fatalf("test bad = %d %s", w.Code, w.Body.String())
	}
	if w := h.do("POST", "/v1/channels/00000000-0000-0000-0000-000000000000/test", ""); w.Code != 404 {
		t.Fatalf("test unknown = %d", w.Code)
	}
}

// TestPermissionAPIAndButtonsShareTheBroker is the #829 regression:
// POST /v1/permissions/{id} still resolves a channel chat prompt, and
// a prompt the button path (broker.Resolve) answered is 404 on the API.
func TestPermissionAPIAndButtonsShareTheBroker(t *testing.T) {
	h := newChannelHarness(t)
	ctx := t.Context()
	id := h.create("perm", "GOOD_BOT")
	conv, err := h.store.CreateConversation(ctx, channels.Conversation{ChannelID: id, ExternalChatID: "77", ExternalUserID: "77"}, "Telegram: Ada")
	if err != nil {
		t.Fatal(err)
	}
	broker := loop.NewPermBroker()
	broker.SetStore(loop.NewPGPermStore(h.pool), nil, nil)
	a, _, _ := testAPI(t, "tok", nil)
	a.perms = broker
	prompt := func(cmd string) (string, <-chan string) {
		pid, ch, err := broker.Create(ctx, loop.PendingPermission{SessionID: conv.SessionID, Tool: "shell",
			Args: json.RawMessage(`{"command":"` + cmd + `"}`), OriginKind: loop.PermOriginChat})
		if err != nil {
			t.Fatal(err)
		}
		return pid, ch
	}

	web, webCh := prompt("ls")
	if w := doMux(a, http.MethodPost, "/v1/permissions/"+web, `{"decision":"once"}`); w.Code != http.StatusOK {
		t.Fatalf("web resolve = %d %s", w.Code, w.Body)
	}
	if d := <-webCh; d != loop.DecideOnce {
		t.Fatalf("web decision = %q", d)
	}
	if _, pending, _ := broker.Get(ctx, web); pending {
		t.Fatal("a web-answered prompt still looks pending to the buttons")
	}

	button, buttonCh := prompt("pwd")
	if !broker.Resolve(ctx, button, loop.DecideDeny) {
		t.Fatal("button resolve failed")
	}
	if d := <-buttonCh; d != loop.DecideDeny {
		t.Fatalf("button decision = %q", d)
	}
	if w := doMux(a, http.MethodPost, "/v1/permissions/"+button, `{"decision":"once"}`); w.Code != http.StatusNotFound {
		t.Fatalf("web answer after the button = %d, want 404", w.Code)
	}
}
