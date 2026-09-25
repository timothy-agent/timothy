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
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const fakeBotID = "42:API-TEST-TOKEN"

// Slack tokens the fake accepts for auth.test and apps.connections.open.
const (
	fakeSlackBot = "xoxb-API-TEST-BOT" // #nosec G101
	fakeSlackApp = "xapp-API-TEST-APP" // #nosec G101
)

// Connector ids the email channel lookup knows.
const (
	imapConnID     = "11111111-1111-1111-1111-111111111111"
	imapOffConnID  = "22222222-2222-2222-2222-222222222222"
	githubConnID   = "33333333-3333-3333-3333-333333333333"
	missingConnID  = "44444444-4444-4444-4444-444444444444"
)

func fakeConnectorLookup(_ context.Context, id string) (string, bool, error) {
	switch id {
	case imapConnID:
		return "imap", true, nil
	case imapOffConnID:
		return "imap", false, nil
	case githubConnID:
		return "github", true, nil
	}
	return "", false, fmt.Errorf("connector %s: %w", id, connectors.ErrNotFound)
}

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
		if method, ok := strings.CutPrefix(r.URL.Path, "/slack/"); ok {
			want := map[string]string{"auth.test": fakeSlackBot, "apps.connections.open": fakeSlackApp}[method]
			if r.Header.Get("Authorization") != "Bearer "+want {
				_, _ = io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"user_id":"UBOT","user":"api_slack_bot","url":"wss://unused.invalid/ws"}`)
			return
		}
		if r.URL.Path != "/bot"+fakeBotID+"/getMe" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":7,"is_bot":true,"username":"api_test_bot"}}`)
	}))
	t.Cleanup(fake.Close)
	resolve := func(_ context.Context, ref string) (string, error) {
		switch ref {
		case "GOOD_BOT":
			return fakeBotID, nil
		case "SLACK_BOT":
			return fakeSlackBot, nil
		case "SLACK_APP":
			return fakeSlackApp, nil
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
	svc.SlackAPIBase = fake.URL + "/slack"
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerChannels(m.Handle, store, svc, fakeConnectorLookup)
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

	if w := h.do("POST", "/v1/channels", fmt.Sprintf(`{"name":%q,"kind":"fax","credential_ref":"X"}`, h.tag+"fax")); w.Code != 400 || !strings.Contains(w.Body.String(), "not_available") {
		t.Fatalf("fax = %d %s", w.Code, w.Body.String())
	}
	if w := h.do("POST", "/v1/channels", fmt.Sprintf(`{"name":%q,"kind":"slack","credential_ref":"X"}`, h.tag+"slack")); w.Code != 400 || !strings.Contains(w.Body.String(), "app_token_ref") {
		t.Fatalf("slack without app token = %d %s", w.Code, w.Body.String())
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

// TestChannelsAPITestSlackTokens: Slack Test checks both tokens and a
// 502 names the one that failed.
func TestChannelsAPITestSlackTokens(t *testing.T) {
	h := newChannelHarness(t)
	create := func(name, botRef, appRef string) string {
		w := h.do("POST", "/v1/channels", fmt.Sprintf(`{"name":%q,"kind":"slack","credential_ref":%q,"config":{"app_token_ref":%q}}`, h.tag+name, botRef, appRef))
		if w.Code != 201 {
			t.Fatalf("create %s = %d %s", name, w.Code, w.Body.String())
		}
		var out struct{ ID string }
		h.decode(w, &out)
		return out.ID
	}
	good := create("slack-good", "SLACK_BOT", "SLACK_APP")
	badApp := create("slack-bad-app", "SLACK_BOT", "BAD_APP")
	badBot := create("slack-bad-bot", "BAD_BOT", "SLACK_APP")

	if w := h.do("POST", "/v1/channels/"+good+"/test", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"bot_username":"api_slack_bot"`) {
		t.Fatalf("test good = %d %s", w.Code, w.Body.String())
	}
	w := h.do("POST", "/v1/channels/"+badApp+"/test", "")
	if body := w.Body.String(); w.Code != 502 || !strings.Contains(body, "test_failed") || !strings.Contains(body, "slack app token") ||
		!strings.Contains(body, "apps.connections.open") || strings.Contains(body, "wrong-token") || strings.Contains(body, fakeSlackBot) {
		t.Fatalf("test bad app token = %d %s", w.Code, body)
	}
	w = h.do("POST", "/v1/channels/"+badBot+"/test", "")
	if body := w.Body.String(); w.Code != 502 || !strings.Contains(body, "slack bot token") || strings.Contains(body, "slack app token") || strings.Contains(body, "wrong-token") {
		t.Fatalf("test bad bot token = %d %s", w.Code, body)
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

// TestChannelsAPIEmail: an email channel needs an enabled imap
// connector and an allowlist, carries no credential_ref, and its
// allowlist and connector patch.
func TestChannelsAPIEmail(t *testing.T) {
	h := newChannelHarness(t)
	body := func(name, connID, allow string) string {
		return fmt.Sprintf(`{"name":%q,"kind":"email","config":{"connector_id":%q,"from_allow":%s}}`, h.tag+name, connID, allow)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"unknown connector":  {body("m1", missingConnID, `["a@x.com"]`), "unknown connector_id"},
		"disabled connector": {body("m2", imapOffConnID, `["a@x.com"]`), "disabled"},
		"github connector":   {body("m3", githubConnID, `["a@x.com"]`), "not imap"},
		"empty allowlist":    {body("m4", imapConnID, `[]`), "from_allow"},
		"bad entry":          {body("m5", imapConnID, `["not an address"]`), "from_allow"},
		"credential_ref":     {fmt.Sprintf(`{"name":%q,"kind":"email","credential_ref":"X","config":{"connector_id":%q,"from_allow":["a@x.com"]}}`, h.tag+"m6", imapConnID), "credential_ref must be empty"},
	} {
		if w := h.do("POST", "/v1/channels", tc.body); w.Code != 400 || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s = %d %s", name, w.Code, w.Body.String())
		}
	}
	w := h.do("POST", "/v1/channels", body("mail", imapConnID, `[" Ada@X.com ","@Example.COM","ada@x.com"]`))
	if w.Code != 201 {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var out struct{ ID string }
	h.decode(w, &out)
	var c channels.Channel
	h.decode(h.do("GET", "/v1/channels/"+out.ID, ""), &c)
	if c.Kind != channels.KindEmail || c.CredentialRef != "" || c.Config.ConnectorID != imapConnID || strings.Join(c.Config.FromAllow, ",") != "ada@x.com,@example.com" {
		t.Fatalf("email channel = %+v", c)
	}
	w = h.do("PATCH", "/v1/channels/"+out.ID, `{"config":{"from_allow":["bob@y.org"]}}`)
	h.decode(w, &c)
	if w.Code != 200 || strings.Join(c.Config.FromAllow, ",") != "bob@y.org" || c.Config.ConnectorID != imapConnID {
		t.Fatalf("patch allowlist = %d %+v", w.Code, c)
	}
	if w := h.do("PATCH", "/v1/channels/"+out.ID, fmt.Sprintf(`{"config":{"connector_id":%q}}`, githubConnID)); w.Code != 400 {
		t.Fatalf("patch to a github connector = %d %s", w.Code, w.Body.String())
	}
	if w := h.do("PATCH", "/v1/channels/"+out.ID, `{"credential_ref":"X"}`); w.Code != 400 {
		t.Fatalf("patch credential_ref = %d %s", w.Code, w.Body.String())
	}
	tg := h.create("tg", "GOOD_BOT")
	if w := h.do("PATCH", "/v1/channels/"+tg, `{"config":{"from_allow":["a@x.com"]}}`); w.Code != 400 {
		t.Fatalf("allowlist on telegram = %d %s", w.Code, w.Body.String())
	}
}
