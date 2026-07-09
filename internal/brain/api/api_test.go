package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeGateway yields a canned event sequence.
type fakeGateway struct {
	events []stream.StreamEvent
	got    gwclient.StreamRequest
}

func (f *fakeGateway) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	f.got = req
	ch := make(chan stream.StreamEvent, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func testAPI(t *testing.T, token string, events []stream.StreamEvent) (*API, *fakeGateway) {
	t.Helper()
	gw := &fakeGateway{events: events}
	svc := chat.New(gw, pgpool.New(t.Context(), "", discard()), discard())
	return &API{svc: svc, token: token, log: discard()}, gw
}

func doChat(a *API, authHeader, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(body))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	a.auth(http.HandlerFunc(a.handleChat)).ServeHTTP(w, req)
	return w
}

func TestAuthRejectsBadTokens(t *testing.T) {
	t.Parallel()
	a, _ := testAPI(t, "secret-token", nil)

	for name, header := range map[string]string{
		"missing":      "",
		"wrong scheme": "Basic secret-token",
		"wrong token":  "Bearer nope",
		"empty bearer": "Bearer ",
	} {
		if w := doChat(a, header, `{}`); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: code = %d, want 401", name, w.Code)
		}
	}
}

func TestAuthFailsClosedWhenUnconfigured(t *testing.T) {
	t.Parallel()
	a, _ := testAPI(t, "", nil)

	w := doChat(a, "Bearer anything", `{}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503 (fail closed)", w.Code)
	}
}

func TestChatRelaysEventsAndAppendsMeta(t *testing.T) {
	t.Parallel()
	a, gw := testAPI(t, "tok", []stream.StreamEvent{
		{Type: stream.EventChunk, Text: "hi "},
		{Type: stream.EventChunk, Text: "there"},
		{Type: stream.EventUsage, Usage: &stream.Usage{InputTokens: 5, OutputTokens: 2}},
		{Type: stream.EventDone, Meta: &stream.Meta{Provider: "prov", Model: "mod", LedgerID: "led-1"}},
	})

	w := doChat(a, "Bearer tok", `{"session_id":"s-1","message":"hello","task_category":"mini"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
	}
	if gw.got.TaskCategory != "mini" || gw.got.SessionID != "s-1" {
		t.Fatalf("gateway request = %+v", gw.got)
	}
	if gw.got.System == "" || !strings.Contains(gw.got.System, "Timothy") {
		t.Fatal("system prompt missing from gateway request")
	}

	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n\n")
	last := strings.TrimPrefix(lines[len(lines)-1], "data: ")
	var m meta
	if err := json.Unmarshal([]byte(last), &m); err != nil {
		t.Fatalf("terminal event not meta JSON: %v (%s)", err, last)
	}
	if m.Type != "meta" || m.SessionID != "s-1" || m.Provider != "prov" ||
		m.Model != "mod" || m.LedgerID != "led-1" || m.Usage == nil || m.Usage.InputTokens != 5 {
		t.Fatalf("meta = %+v", m)
	}
}

func TestChatValidation(t *testing.T) {
	t.Parallel()
	a, _ := testAPI(t, "tok", nil)

	if w := doChat(a, "Bearer tok", `{"session_id":"s-1","message":"  "}`); w.Code != http.StatusBadGateway {
		t.Fatalf("blank message: code = %d", w.Code)
	}
	if w := doChat(a, "Bearer tok", `{not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: code = %d", w.Code)
	}
}
