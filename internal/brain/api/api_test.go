package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// memDir is an in-memory Directory + chat.SessionLog.
type memDir struct {
	mu     sync.Mutex
	metas  map[string]session.Meta
	events map[string][]session.Event
	nextID int
}

func newMemDir() *memDir {
	return &memDir{metas: map[string]session.Meta{}, events: map[string][]session.Event{}}
}

func (d *memDir) Create(_ context.Context, title string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	id := fmt.Sprintf("sess-%d", d.nextID)
	d.metas[id] = session.Meta{ID: id, Title: title}
	d.events[id] = []session.Event{{SessionID: id, Seq: 1, Kind: session.KindSessionStarted, Payload: []byte(`{}`)}}
	return id, nil
}

func (d *memDir) List(_ context.Context, query string) ([]session.Meta, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []session.Meta
	for _, m := range d.metas {
		if query == "" && !m.Archived || query != "" && strings.Contains(m.Title, query) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (d *memDir) Get(_ context.Context, id string) (session.Meta, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.metas[id]
	if !ok {
		return session.Meta{}, fmt.Errorf("session: get %s: %w", id, pgx.ErrNoRows)
	}
	return m, nil
}

func (d *memDir) Events(_ context.Context, id string) ([]session.Event, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]session.Event(nil), d.events[id]...), nil
}

func (d *memDir) Update(_ context.Context, id string, title *string, archived *bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.metas[id]
	if !ok {
		return fmt.Errorf("session: update %s: not found", id)
	}
	if title != nil {
		m.Title = *title
	}
	if archived != nil {
		m.Archived = *archived
	}
	d.metas[id] = m
	return nil
}

func (d *memDir) Append(_ context.Context, id, kind string, payload any) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	seq := int64(len(d.events[id]) + 1)
	d.events[id] = append(d.events[id], session.Event{SessionID: id, Seq: seq, Kind: kind, Payload: data})
	return seq, nil
}

func (d *memDir) SetTitleIfEmpty(context.Context, string, string) error { return nil }
func (d *memDir) SetLastCategory(context.Context, string, string) error { return nil }

// fakeGateway yields a canned event sequence, or fails when err set.
type fakeGateway struct {
	events []stream.StreamEvent
	err    error
	got    gwclient.StreamRequest
	calls  int
}

func (f *fakeGateway) Stream(_ context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	f.calls++
	if f.calls == 1 { // record the chat call, not the title call
		f.got = req
	}
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan stream.StreamEvent, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func testAPI(t *testing.T, token string, events []stream.StreamEvent) (*API, *memDir, *fakeGateway) {
	t.Helper()
	dir := newMemDir()
	gw := &fakeGateway{events: events}
	svc := chat.New(gw, dir, nil, nil, 60_000, discard())
	return &API{svc: svc, dir: dir, token: token, log: discard()}, dir, gw
}

func do(a *API, h http.HandlerFunc, method, path, authHeader, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	a.auth(h).ServeHTTP(w, req)
	return w
}

// mux builds the real route table so path values resolve.
func mux(a *API) *http.ServeMux {
	m := http.NewServeMux()
	m.Handle("GET /v1/sessions", a.auth(http.HandlerFunc(a.handleList)))
	m.Handle("POST /v1/sessions", a.auth(http.HandlerFunc(a.handleCreate)))
	m.Handle("GET /v1/sessions/{id}", a.auth(http.HandlerFunc(a.handleTranscript)))
	m.Handle("PATCH /v1/sessions/{id}", a.auth(http.HandlerFunc(a.handleUpdate)))
	m.Handle("POST /v1/sessions/{id}/messages", a.auth(http.HandlerFunc(a.handleMessages)))
	m.Handle("POST /v1/chat", a.auth(http.HandlerFunc(a.handleChatShim)))
	return m
}

func doMux(a *API, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	mux(a).ServeHTTP(w, req)
	return w
}

func okEvents() []stream.StreamEvent {
	return []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "hi "},
		{Type: stream.EventChunk, Text: "there"},
		{Type: stream.EventUsage, Usage: &stream.Usage{InputTokens: 5, OutputTokens: 2}},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod", LedgerID: "led-1"}},
	}
}

// --- auth ---

func TestAuthRejectsBadTokens(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "secret-token", nil)

	for name, header := range map[string]string{
		"missing":      "",
		"wrong scheme": "Basic secret-token",
		"wrong token":  "Bearer nope",
		"empty bearer": "Bearer ",
	} {
		if w := do(a, a.handleList, http.MethodGet, "/v1/sessions", header, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: code = %d, want 401", name, w.Code)
		}
	}
}

func TestAuthAcceptsRobustHeaderShapes(t *testing.T) {
	t.Parallel()
	for name, header := range map[string]string{
		"lowercase scheme":   "bearer secret-token",
		"surrounding spaces": "  Bearer secret-token  ",
		"space before token": "Bearer  secret-token",
	} {
		a, _, _ := testAPI(t, "secret-token", nil)
		if w := do(a, a.handleList, http.MethodGet, "/v1/sessions", header, ""); w.Code != http.StatusOK {
			t.Fatalf("%s: code = %d, want 200", name, w.Code)
		}
	}
}

func TestAuthFailsClosedWhenUnconfigured(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "", nil)
	if w := do(a, a.handleList, http.MethodGet, "/v1/sessions", "Bearer anything", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503 (fail closed)", w.Code)
	}
}

// --- session management ---

func TestSessionCRUD(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)

	// create
	w := doMux(a, http.MethodPost, "/v1/sessions", `{"title":"my session"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct{ ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &created)

	// list
	w = doMux(a, http.MethodGet, "/v1/sessions", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), created.ID) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}

	// rename + archive
	w = doMux(a, http.MethodPatch, "/v1/sessions/"+created.ID, `{"title":"renamed","archived":true}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	if m, _ := dir.Get(t.Context(), created.ID); m.Title != "renamed" || !m.Archived {
		t.Fatalf("meta after patch = %+v", m)
	}

	// patch nothing
	if w = doMux(a, http.MethodPatch, "/v1/sessions/"+created.ID, `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d", w.Code)
	}
	// patch missing
	if w = doMux(a, http.MethodPatch, "/v1/sessions/nope", `{"archived":true}`); w.Code != http.StatusNotFound {
		t.Fatalf("patch missing: %d", w.Code)
	}
}

func TestTranscriptEndpoint(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "t")
	_, _ = dir.Append(t.Context(), id, session.KindUserMessage, session.UserMessage{Text: "hello"})

	w := doMux(a, http.MethodGet, "/v1/sessions/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("transcript: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Session session.Meta             `json:"session"`
		Items   []session.TranscriptItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Session.ID != id || len(resp.Items) != 1 || resp.Items[0].Text != "hello" {
		t.Fatalf("resp = %+v", resp)
	}

	if w = doMux(a, http.MethodGet, "/v1/sessions/missing", ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing session: %d", w.Code)
	}
}

// --- chat ---

func TestMessagesEndpointStreamsAndPersists(t *testing.T) {
	t.Parallel()
	a, dir, gw := testAPI(t, "tok", okEvents())
	id, _ := dir.Create(t.Context(), "t")

	w := doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages", `{"message":"hello","task_category":"mini"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Session-Id"); got != id {
		t.Fatalf("X-Session-Id = %q, want %s", got, id)
	}
	if gw.got.TaskCategory != "mini" || !strings.Contains(gw.got.System, "Timothy") {
		t.Fatalf("gateway request = %+v", gw.got)
	}

	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n\n")
	last := strings.TrimPrefix(lines[len(lines)-1], "data: ")
	var m meta
	if err := json.Unmarshal([]byte(last), &m); err != nil {
		t.Fatalf("terminal not meta: %v (%s)", err, last)
	}
	if m.Type != "meta" || m.SessionID != id || m.Provider != "prov" || m.Usage == nil {
		t.Fatalf("meta = %+v", m)
	}

	if w = doMux(a, http.MethodPost, "/v1/sessions/missing/messages", `{"message":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("missing session message: %d", w.Code)
	}
}

func TestChatShimCreatesSessionAndDeprecates(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", okEvents())

	w := doMux(a, http.MethodPost, "/v1/chat", `{"message":"hello"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Deprecation") != "true" {
		t.Fatal("deprecation header missing")
	}
	if w.Header().Get("X-Session-Id") == "" {
		t.Fatal("created session id missing from header")
	}
}

func TestChatErrorCarriesSessionID(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "t")
	// gateway with error
	gw := &fakeGateway{err: context.DeadlineExceeded}
	a.svc = chat.New(gw, dir, nil, nil, 60_000, discard())

	w := doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages", `{"message":"hi"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["session_id"] != id {
		t.Fatalf("session_id = %q, want %s", body["session_id"], id)
	}
}

func TestChatValidation(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)

	if w := doMux(a, http.MethodPost, "/v1/chat", `{"message":"  "}`); w.Code != http.StatusBadGateway {
		t.Fatalf("blank message: code = %d", w.Code)
	}
	if w := doMux(a, http.MethodPost, "/v1/chat", `{not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: code = %d", w.Code)
	}
}
