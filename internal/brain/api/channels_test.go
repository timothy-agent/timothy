package api

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/channels"
)

func TestChannelsEndpointsUnmountedWhenStoreNil(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerChannels(m.Handle, nil, nil, nil)
	for _, req := range []struct{ method, path string }{
		{"GET", "/v1/channels"},
		{"POST", "/v1/channels"},
		{"GET", "/v1/channels/abc"},
		{"PATCH", "/v1/channels/abc"},
		{"DELETE", "/v1/channels/abc"},
		{"POST", "/v1/channels/abc/test"},
		{"GET", "/v1/channels/abc/pairings"},
		{"POST", "/v1/channels/abc/pairings/1/approve"},
		{"POST", "/v1/channels/abc/pairings/1/revoke"},
	} {
		r := httptest.NewRequest(req.method, req.path, nil)
		r.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("%s %s with a nil store = %d, want 404", req.method, req.path, w.Code)
		}
	}
}

func TestChannelsRejectBadBodiesBeforeTheStore(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerChannels(m.Handle, channels.NewStore(nil), nil, nil)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/v1/channels", `{"name":"x","kind":"telegram","credential_ref":"T","token":"leak"}`},
		{"POST", "/v1/channels", `{"name":"x","kind":"telegram","credential_ref":"T","config":{"dispatch":true,"extra":1}}`},
		{"PATCH", "/v1/channels/abc", `{"agent_id":42}`},
		{"PATCH", "/v1/channels/abc", `{"kind":"slack"}`},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("%s %s %s = %d %s, want 400", tc.method, tc.path, tc.body, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "/v1/channels", nil)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("no bearer = %d, want 401", w.Code)
	}
}

func TestFailChannel(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		err  error
		want int
		code string
	}{
		{fmt.Errorf("x: %w", channels.ErrNotFound), 404, "not_found"},
		{channels.ErrNameConflict, 409, "name_conflict"},
		{fmt.Errorf("%w: %q", channels.ErrKindUnavailable, "slack"), 400, "not_available"},
		{fmt.Errorf("%w: name is required", channels.ErrInvalid), 400, "bad_request"},
		{channels.ErrUnknownAgent, 400, "bad_request"},
		{errors.New("db down"), 500, "channels_failed"},
	} {
		w := httptest.NewRecorder()
		failChannel(w, tt.err)
		if w.Code != tt.want || !strings.Contains(w.Body.String(), `"error":"`+tt.code+`"`) {
			t.Fatalf("failChannel(%v) = %d %s, want %d %s", tt.err, w.Code, w.Body.String(), tt.want, tt.code)
		}
	}
}
