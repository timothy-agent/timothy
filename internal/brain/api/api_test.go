package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/fxrates"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func staticBudget(n int) func(context.Context) int {
	return func(context.Context) int { return n }
}


// memDir is an in-memory Directory + chat.SessionLog.
type memDir struct {
	mu         sync.Mutex
	metas      map[string]session.Meta
	events     map[string][]session.Event
	missionRef map[string]bool
	nextID     int
}

func newMemDir() *memDir {
	return &memDir{metas: map[string]session.Meta{}, events: map[string][]session.Event{}}
}

func (d *memDir) Create(_ context.Context, title string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", d.nextID)
	d.metas[id] = session.Meta{ID: id, Title: title}
	d.events[id] = []session.Event{{SessionID: id, Seq: 1, Kind: session.KindSessionStarted, Payload: []byte(`{}`)}}
	return id, nil
}

func (d *memDir) List(_ context.Context, query string, before time.Time, beforeID string) ([]session.Meta, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []session.Meta
	for _, m := range d.metas {
		// Mirror the store's (updated_at, id) cursor: strictly earlier
		// in the descending ordering.
		if !before.IsZero() {
			if m.UpdatedAt.After(before) || (m.UpdatedAt.Equal(before) && m.ID >= beforeID) {
				continue
			}
		}
		if query == "" && !m.Archived || query != "" && strings.Contains(m.Title, query) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID > out[j].ID
	})
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

func (d *memDir) Delete(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.missionRef[id] {
		return fmt.Errorf("session: delete %s: %w", id, session.ErrMissionReferenced)
	}
	if _, ok := d.metas[id]; !ok {
		return fmt.Errorf("session: delete %s: not found", id)
	}
	delete(d.metas, id)
	delete(d.events, id)
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

func (d *memDir) SetTitleIfEmpty(context.Context, string, string) error      { return nil }
func (d *memDir) SetLastRoute(context.Context, string, string, string) error { return nil }

func (d *memDir) Knowledge(_ context.Context, id string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.metas[id].Knowledge...), nil
}

func (d *memDir) AddKnowledge(_ context.Context, id string, names []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.metas[id]
	for _, name := range names {
		if !slices.Contains(m.Knowledge, name) {
			m.Knowledge = append(m.Knowledge, name)
		}
	}
	d.metas[id] = m
	return nil
}

func (d *memDir) SetKnowledge(_ context.Context, id string, names []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.metas[id]
	if !ok {
		return fmt.Errorf("session: knowledge %s: not found", id)
	}
	m.Knowledge = append([]string(nil), names...)
	d.metas[id] = m
	return nil
}

// PendingPermissions mirrors session.Store's own unresolved-vs-resolved
// logic over the in-memory event log, scoped to sessionIDs — same
// contract the real store's SQL enforces (no matching
// permission_resolved by id).
func (d *memDir) PendingPermissions(_ context.Context, sessionIDs []string) ([]session.PendingPermission, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []session.PendingPermission
	for _, id := range sessionIDs {
		resolved := map[string]bool{}
		var requests []struct {
			id string
			session.PendingPermission
		}
		for _, ev := range d.events[id] {
			switch ev.Kind {
			case session.KindPermissionResolved:
				var r session.PermissionResolved
				if err := json.Unmarshal(ev.Payload, &r); err != nil {
					return nil, err
				}
				resolved[r.ID] = true
			case session.KindPermissionRequest:
				var req session.PermissionRequest
				if err := json.Unmarshal(ev.Payload, &req); err != nil {
					return nil, err
				}
				requests = append(requests, struct {
					id string
					session.PendingPermission
				}{req.ID, session.PendingPermission{
					SessionID: id, SessionTitle: d.metas[id].Title,
					Tool: req.Tool, Rationale: req.Rationale, RequestedAt: ev.CreatedAt,
				}})
			}
		}
		for _, req := range requests {
			if !resolved[req.id] {
				out = append(out, req.PendingPermission)
			}
		}
	}
	return out, nil
}

// fakeGateway yields a canned event sequence, or fails when err set.
// blockCh, when set, delays every event past the first until closed —
// lets a test observe "mid-turn" state (turn_active, a /live replay)
// before the stream completes.
type fakeGateway struct {
	events  []stream.StreamEvent
	err     error
	got     gwclient.StreamRequest
	calls   int
	blockCh chan struct{}
}

func (f *fakeGateway) RouteForRole(_ context.Context, role string) (string, bool, error) {
	return role, true, nil
}

func (f *fakeGateway) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	f.calls++
	if f.calls == 1 { // record the chat call, not the title call
		f.got = req
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.blockCh == nil {
		ch := make(chan stream.StreamEvent, len(f.events))
		for _, ev := range f.events {
			ch <- ev
		}
		close(ch)
		return ch, nil
	}
	ch := make(chan stream.StreamEvent)
	events := append([]stream.StreamEvent(nil), f.events...)
	block := f.blockCh
	go func() {
		defer close(ch)
		if len(events) > 0 {
			select {
			case ch <- events[0]:
			case <-ctx.Done():
				return
			}
		}
		select {
		case <-block:
		case <-ctx.Done():
			return
		}
		for _, ev := range events[1:] {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func testAPI(t *testing.T, token string, events []stream.StreamEvent) (*API, *memDir, *fakeGateway) {
	t.Helper()
	dir := newMemDir()
	gw := &fakeGateway{events: events}
	svc := chat.New(gw, dir, nil, nil, staticBudget(60_000), nil, nil, nil, discard())
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
	a.registerLive(m.Handle)
	m.Handle("PATCH /v1/sessions/{id}", a.auth(http.HandlerFunc(a.handleUpdate)))
	m.Handle("PUT /v1/sessions/{id}/knowledge", a.auth(http.HandlerFunc(a.handleSetKnowledge)))
	m.Handle("DELETE /v1/sessions/{id}", a.auth(http.HandlerFunc(a.handleDelete)))
	m.Handle("POST /v1/sessions/{id}/messages", a.auth(http.HandlerFunc(a.handleMessages)))
	m.Handle("POST /v1/sessions/{id}/messages/retry", a.auth(http.HandlerFunc(a.handleRetry)))
	m.Handle("POST /v1/sessions/{id}/stop", a.auth(http.HandlerFunc(a.handleStop)))
	m.Handle("POST /v1/permissions/{id}", a.auth(http.HandlerFunc(a.handlePermission)))
	m.Handle("GET /v1/permissions/pending", a.auth(http.HandlerFunc(a.handlePendingPermissions)))
	m.Handle("POST /v1/chat", a.auth(http.HandlerFunc(a.handleChatShim)))
	return m
}

func TestMemoryProxyScopedToDocumentedRoutes(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	proxied := 0
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied++
		w.WriteHeader(http.StatusOK)
	})
	m := mux(a)
	// Mount exactly what Register mounts — same pattern list, so this
	// test drifts with the real scope, never away from it.
	for _, pattern := range memoryRoutePatterns {
		m.Handle(pattern, a.auth(stub))
	}

	call := func(method, path, auth string) int {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code
	}

	// Every documented pattern reaches the proxy behind the bearer.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/memories"},
		{http.MethodPost, "/v1/memories"},
		{http.MethodPost, "/v1/memories/abc"},
		{http.MethodGet, "/v1/memories/abc/chain"},
		{http.MethodPost, "/v1/memories/search"},
		{http.MethodGet, "/v1/entities/graph"},
		{http.MethodGet, "/v1/entities/abc/memories"},
	} {
		if code := call(c.method, c.path, "Bearer tok"); code != http.StatusOK {
			t.Fatalf("%s %s = %d, want 200", c.method, c.path, code)
		}
	}
	if proxied != 7 {
		t.Fatalf("proxied = %d, want 7", proxied)
	}

	// memoryd-internal routes must never be reachable through brain —
	// not directly and not via traversal through a proxied prefix.
	proxied = 0
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/extract"},
		{http.MethodPost, "/v1/retrieve"},
		{http.MethodPost, "/v1/memories/../extract"},
	} {
		if code := call(c.method, c.path, "Bearer tok"); code == http.StatusOK {
			t.Fatalf("%s %s = 200, want unreachable", c.method, c.path)
		}
	}
	if proxied != 0 {
		t.Fatalf("internal route reached the proxy %d times", proxied)
	}

	// And the proxied patterns stay behind auth.
	if code := call(http.MethodGet, "/v1/memories", ""); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", code)
	}
}

func TestAdminProxyScopedToUsageRoutes(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	proxied := 0
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied++
		w.WriteHeader(http.StatusOK)
	})
	m := mux(a)
	a.registerAdmin(m.Handle, stub)

	call := func(method, path, auth string) int {
		req := httptest.NewRequest(method, path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w.Code
	}

	for _, path := range []string{
		"/v1/admin/usage/summary",
		"/v1/admin/usage/series",
		"/v1/admin/usage/latency",
	} {
		if code := call(http.MethodGet, path, "Bearer tok"); code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, code)
		}
	}
	if code := call(http.MethodPatch, "/v1/admin/usage/budget", "Bearer tok"); code != http.StatusOK {
		t.Fatalf("PATCH /v1/admin/usage/budget = %d, want 200", code)
	}
	if proxied != 4 {
		t.Fatalf("proxied = %d, want 4", proxied)
	}

	// Only the admin usage surface is mounted; the gateway's other
	// internal routes and unauthenticated calls stay out.
	if code := call(http.MethodPost, "/internal/reload", "Bearer tok"); code == http.StatusOK {
		t.Fatal("internal reload must be unreachable through brain")
	}
	if code := call(http.MethodGet, "/v1/admin/usage/summary", ""); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", code)
	}
}

type fakeResolver struct{ known map[string]string }

func (f *fakeResolver) Resolve(id, decision string) bool {
	if _, ok := f.known[id]; !ok {
		return false
	}
	f.known[id] = decision
	return true
}

func TestPermissionEndpoint(t *testing.T) {
	a, _, _ := testAPI(t, "tok", nil)
	resolver := &fakeResolver{known: map[string]string{"p1": ""}}
	a.perms = resolver
	m := mux(a)

	call := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/permissions/"+id, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w
	}

	if w := call("p1", `{"decision":"session"}`); w.Code != http.StatusOK {
		t.Fatalf("valid decision: %d %s", w.Code, w.Body)
	}
	if resolver.known["p1"] != "session" {
		t.Fatalf("decision not delivered: %q", resolver.known["p1"])
	}
	if w := call("p2", `{"decision":"once"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", w.Code)
	}
	if w := call("p1", `{"decision":"maybe"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad decision: %d", w.Code)
	}

	a.perms = nil
	if w := call("p1", `{"decision":"deny"}`); w.Code != http.StatusNotFound {
		t.Fatalf("nil resolver: %d", w.Code)
	}
}

func doMux(a *API, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	mux(a).ServeHTTP(w, req)
	return w
}

func okEvents() []stream.StreamEvent {
	cost := 0.0012
	return []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "hi "},
		{Type: stream.EventChunk, Text: "there"},
		{Type: stream.EventUsage, Usage: &stream.Usage{InputTokens: 5, OutputTokens: 2}},
		{Type: stream.EventDone, Meta: &stream.Meta{
			Provider: "prov", Model: "mod", LedgerID: "led-1", Cost: &cost, Currency: "USD",
		}},
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

func TestSessionDelete(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "doomed")

	if w := doMux(a, http.MethodDelete, "/v1/sessions/"+id, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, err := dir.Get(t.Context(), id); err == nil {
		t.Fatal("session still present after delete")
	}
	// delete again: gone means 404
	if w := doMux(a, http.MethodDelete, "/v1/sessions/"+id, ""); w.Code != http.StatusNotFound {
		t.Fatalf("re-delete: %d", w.Code)
	}
	// invalid id shape
	if w := doMux(a, http.MethodDelete, "/v1/sessions/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("delete bad id: %d", w.Code)
	}
	// mission-referenced sessions refuse with 409
	held, _ := dir.Create(t.Context(), "mission transcript")
	dir.missionRef = map[string]bool{held: true}
	w := doMux(a, http.MethodDelete, "/v1/sessions/"+held, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "mission_referenced") {
		t.Fatalf("mission-referenced delete: %d %s", w.Code, w.Body.String())
	}
	if _, err := dir.Get(t.Context(), held); err != nil {
		t.Fatal("mission-referenced session must survive")
	}
}

// TestSetKnowledgeRejectsUnknownCollection pins validateKnowledge's
// gate: a name the KB doesn't have is a 400, not a silent accept.
func TestSetKnowledgeRejectsUnknownCollection(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "s")
	a.kbCollections = func(context.Context) ([]kb.Collection, error) {
		return []kb.Collection{{Name: "docs"}}, nil
	}

	w := doMux(a, http.MethodPut, "/v1/sessions/"+id+"/knowledge", `{"collections":["no-such-collection"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT knowledge with unknown collection: %d %s", w.Code, w.Body.String())
	}
}

// TestSetKnowledgeAcceptsValidCollections pins the success path: a
// known collection name is accepted, persisted, and answered 204.
func TestSetKnowledgeAcceptsValidCollections(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "s")
	a.kbCollections = func(context.Context) ([]kb.Collection, error) {
		return []kb.Collection{{Name: "docs"}}, nil
	}

	w := doMux(a, http.MethodPut, "/v1/sessions/"+id+"/knowledge", `{"collections":["docs"]}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT knowledge: %d %s", w.Code, w.Body.String())
	}
	got, err := dir.Knowledge(t.Context(), id)
	if err != nil {
		t.Fatalf("Knowledge: %v", err)
	}
	if !slices.Equal(got, []string{"docs"}) {
		t.Fatalf("session knowledge = %v, want [docs]", got)
	}
}

// TestSetKnowledgeRequiresKBConfigured: kbCollections nil (no kb.Store
// wired) refuses any non-empty knowledge list rather than silently
// accepting names it can never validate.
func TestSetKnowledgeRequiresKBConfigured(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "s")

	w := doMux(a, http.MethodPut, "/v1/sessions/"+id+"/knowledge", `{"collections":["docs"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT knowledge with KB disabled: %d %s", w.Code, w.Body.String())
	}
}

// TestSetKnowledgeMalformedBody: unparsable JSON is a 400 before any
// validation or store call runs.
func TestSetKnowledgeMalformedBody(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "s")

	w := doMux(a, http.MethodPut, "/v1/sessions/"+id+"/knowledge", `{"collections":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT knowledge with malformed body: %d %s", w.Code, w.Body.String())
	}
}

// TestSetKnowledgeUnknownSession: a well-formed but nonexistent session
// id passes validSessionID, so the store's "not found" error must
// surface as 404, not a 500.
func TestSetKnowledgeUnknownSession(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	bogus := "00000000-0000-4000-8000-000000000000"

	w := doMux(a, http.MethodPut, "/v1/sessions/"+bogus+"/knowledge", `{"collections":[]}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("PUT knowledge for unknown session: %d %s", w.Code, w.Body.String())
	}
}

// TestSetKnowledgeCollectionsLookupFails: validateKnowledge propagates
// a kbCollections error as-is, and the handler maps it to 400 like any
// other validation failure.
func TestSetKnowledgeCollectionsLookupFails(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "s")
	a.kbCollections = func(context.Context) ([]kb.Collection, error) {
		return nil, errors.New("kb store unavailable")
	}

	w := doMux(a, http.MethodPut, "/v1/sessions/"+id+"/knowledge", `{"collections":["docs"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT knowledge with kbCollections error: %d %s", w.Code, w.Body.String())
	}
}

// TestHandleMessagesRejectsUnknownKnowledge: a chat request naming an
// unknown kb collection in Knowledge is a 400 before the turn runs.
func TestHandleMessagesRejectsUnknownKnowledge(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", okEvents())
	id, _ := dir.Create(t.Context(), "s")
	a.kbCollections = func(context.Context) ([]kb.Collection, error) {
		return []kb.Collection{{Name: "docs"}}, nil
	}

	w := doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages",
		`{"message":"hi","knowledge":["no-such-collection"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("handleMessages with unknown knowledge: %d %s", w.Code, w.Body.String())
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

// TestTranscriptEndpointCostConversion covers the api.API-side
// decoration of cost, not fxrates/settings themselves: with no
// flags/rates Store wired (testAPI never has a live DB), conversion
// degrades and the billed cost/currency ride through untouched, same
// contract as UsageDecorator.
func TestTranscriptEndpointCostConversion(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "t")
	cost := 0.05
	var turn session.AssistantTurn
	turn.Provider, turn.Model = "prov", "mod"
	turn.Cost, turn.Currency = &cost, "USD"
	_, _ = dir.Append(t.Context(), id, session.KindAssistantTurn, turn)

	w := doMux(a, http.MethodGet, "/v1/sessions/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("transcript: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []session.TranscriptItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %+v, want 1", resp.Items)
	}
	item := resp.Items[0]
	if item.Cost == nil || *item.Cost != cost || item.Currency != "USD" {
		t.Fatalf("billed cost/currency = %+v/%q, want %v/USD", item.Cost, item.Currency, cost)
	}
	if item.ConvertedCost != nil || item.ConvertedCurrency != "" || item.RateAsOf != "" {
		t.Fatalf("converted fields = %+v, want absent with no flags/rates wired", item)
	}
}

// --- chat ---

func TestMessagesEndpointStreamsAndPersists(t *testing.T) {
	t.Parallel()
	a, dir, gw := testAPI(t, "tok", okEvents())
	id, _ := dir.Create(t.Context(), "t")

	w := doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages", `{"message":"hello","route":"mini"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Session-Id"); got != id {
		t.Fatalf("X-Session-Id = %q, want %s", got, id)
	}
	if gw.got.Route != "mini" || !strings.Contains(gw.got.System, "Timothy") {
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
	if m.Cost == nil || *m.Cost != 0.0012 || m.Currency != "USD" {
		t.Fatalf("meta cost/currency = %+v/%q, want 0.0012/USD", m.Cost, m.Currency)
	}
	// No flags/rates wired on the test API (no live settings/fxrates
	// Store) — conversion degrades to absent fields, billed cost/currency
	// left exactly as the gateway reported them.
	if m.ConvertedCost != nil || m.ConvertedCurrency != "" || m.RateAsOf != "" {
		t.Fatalf("meta converted fields = %+v, want absent with no flags/rates wired", m)
	}

	if w = doMux(a, http.MethodPost, "/v1/sessions/missing/messages", `{"message":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("missing session message: %d", w.Code)
	}
}

// TestRetryEndpoint covers the surface the tools-picker-adjacent retry
// feature adds: a failed /messages call leaves a dangling user_message
// (chat.Service never appends the assistant turn without EventDone),
// and /messages/retry must reuse it — no request body, no duplicate.
func TestRetryEndpoint(t *testing.T) {
	t.Parallel()
	a, dir, gw := testAPI(t, "tok", []stream.StreamEvent{
		{Type: stream.EventError, Err: &stream.StreamError{Code: "chain_exhausted", Message: "boom"}},
	})
	id, _ := dir.Create(t.Context(), "t")

	w := doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages", `{"message":"hello","route":"mini"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("initial send code = %d body=%s", w.Code, w.Body.String())
	}

	// A session with no dangling turn (never messaged, or already
	// completed) has nothing to retry.
	if w := doMux(a, http.MethodPost, "/v1/sessions/missing/messages/retry", ``); w.Code != http.StatusNotFound {
		t.Fatalf("retry on missing session: code = %d", w.Code)
	}

	gw.events = okEvents()
	w = doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages/retry", ``)
	if w.Code != http.StatusOK {
		t.Fatalf("retry code = %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Session-Id"); got != id {
		t.Fatalf("X-Session-Id = %q, want %s", got, id)
	}
	if gw.got.Route != "mini" {
		t.Fatalf("retried route = %q, want mini (the original message's persisted route)", gw.got.Route)
	}

	// persistTurn runs after the SSE body closes (close(out) unblocks
	// the client write, persistence follows), so the assistant_turn
	// isn't guaranteed durable the instant doMux returns. The failed
	// initial send leaves turn_failed (D-044) between the dangling
	// user_message and the retried assistant_turn — it doesn't block
	// retry (lastUserMessage only stops at a completed assistant_turn).
	want := []string{session.KindSessionStarted, session.KindUserMessage, session.KindTurnFailed, session.KindAssistantTurn}
	deadline := time.Now().Add(5 * time.Second)
	var kinds []string
	for {
		events, err := dir.Events(t.Context(), id)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		kinds = nil
		for _, ev := range events {
			kinds = append(kinds, ev.Kind)
		}
		if strings.Join(kinds, ",") == strings.Join(want, ",") || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds after retry = %v, want %v (no duplicated user_message)", kinds, want)
	}

	// The turn just completed — nothing left dangling to retry again.
	if w := doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages/retry", ``); w.Code != http.StatusConflict {
		t.Fatalf("retry after completion: code = %d, want 409", w.Code)
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
	a.svc = chat.New(gw, dir, nil, nil, staticBudget(60_000), nil, nil, nil, discard())

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

	if w := doMux(a, http.MethodPost, "/v1/chat", `{"message":"  "}`); w.Code != http.StatusBadRequest {
		t.Fatalf("blank message: code = %d, want 400 (caller bug, not gateway failure)", w.Code)
	}
	if w := doMux(a, http.MethodPost, "/v1/chat", `{not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: code = %d", w.Code)
	}
}

// TestListCursorPaging pins the composite cursor contract: pages split
// on (updated_at, id) so tied timestamps never drop or repeat rows,
// and half a cursor is rejected.
func TestListCursorPaging(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	ts := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	dir.mu.Lock()
	for _, id := range []string{"sess-a", "sess-b", "sess-c"} {
		dir.metas[id] = session.Meta{ID: id, Title: id, UpdatedAt: ts} // all tied
	}
	dir.metas["sess-old"] = session.Meta{ID: "sess-old", Title: "sess-old", UpdatedAt: ts.Add(-time.Hour)}
	dir.mu.Unlock()

	page1 := doMux(a, http.MethodGet, "/v1/sessions", "")
	var got struct{ Sessions []session.Meta }
	if err := json.Unmarshal(page1.Body.Bytes(), &got); err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(got.Sessions) != 4 || got.Sessions[0].ID != "sess-c" {
		t.Fatalf("page1 = %+v", got.Sessions)
	}

	// Resume from the middle of the tie: strictly-after rows only.
	cursor := got.Sessions[1] // sess-b
	page2 := doMux(a, http.MethodGet,
		"/v1/sessions?before="+cursor.UpdatedAt.Format(time.RFC3339Nano)+"&before_id="+cursor.ID, "")
	if err := json.Unmarshal(page2.Body.Bytes(), &got); err != nil {
		t.Fatalf("page2: %v", err)
	}
	ids := make([]string, len(got.Sessions))
	for i, m := range got.Sessions {
		ids[i] = m.ID
	}
	if strings.Join(ids, ",") != "sess-a,sess-old" {
		t.Fatalf("page2 ids = %v, want [sess-a sess-old] (no dup, no skip)", ids)
	}

	// Half a cursor is a client bug, not a guess.
	if w := doMux(a, http.MethodGet, "/v1/sessions?before_id=sess-b", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("before_id alone: code = %d, want 400", w.Code)
	}
	if w := doMux(a, http.MethodGet, "/v1/sessions?before=2026-07-10T12:00:00Z", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("before alone: code = %d, want 400", w.Code)
	}
}

// --- cost currency conversion ---

// TestConvertCost is the pure-function core of convertedCost (mirrors
// DecorateUsageResponse's split from UsageDecorator.Decorate): the
// display-only converted_cost/converted_currency/rate_as_of trio,
// never touching the billed cost/currency it's computed alongside.
func TestConvertCost(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	rates := map[string]fxrates.Rate{"EUR": {Value: 0.86, AsOf: asOf}}
	cost := 100.0

	tests := []struct {
		name   string
		cost   *float64
		billed string
		target string
		rates  map[string]fxrates.Rate
		wantOK bool
	}{
		{
			name: "converts when a usable rate exists", cost: &cost,
			billed: "USD", target: "EUR", rates: rates, wantOK: true,
		},
		{
			name: "nil cost never converts", cost: nil,
			billed: "USD", target: "EUR", rates: rates, wantOK: false,
		},
		{
			name: "target equal to billed does not convert", cost: &cost,
			billed: "USD", target: "USD", rates: rates, wantOK: false,
		},
		{
			name: "missing rate leaves it unconverted", cost: &cost,
			billed: "GBP", target: "EUR", rates: rates, wantOK: false,
		},
		{
			name: "nil rate table never converts", cost: &cost,
			billed: "USD", target: "EUR", rates: nil, wantOK: false,
		},
		{
			name: "blank billed currency never converts", cost: &cost,
			billed: "", target: "EUR", rates: rates, wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			converted, target, rateAsOf, ok := convertCost(tt.cost, tt.billed, tt.target, tt.rates)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				if converted != 0 || target != "" || rateAsOf != "" {
					t.Fatalf("degraded result = (%v, %q, %q), want all zero", converted, target, rateAsOf)
				}
				return
			}
			if target != tt.target {
				t.Fatalf("target = %q, want %q", target, tt.target)
			}
			if converted < 85 || converted > 87 {
				t.Fatalf("converted = %v, want ~86", converted)
			}
			if rateAsOf != "2026-07-20" {
				t.Fatalf("rateAsOf = %q, want 2026-07-20", rateAsOf)
			}
		})
	}
}
