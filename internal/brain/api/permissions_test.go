package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// TestPendingPermissionsEmptyWhenNothingActive confirms the endpoint
// returns an empty list, never null, when no turn is in flight.
func TestPendingPermissionsEmptyWhenNothingActive(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	w := doMux(a, http.MethodGet, "/v1/permissions/pending", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Pending []map[string]any `json:"pending"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pending == nil || len(body.Pending) != 0 {
		t.Fatalf("pending = %#v, want empty (non-nil) slice", body.Pending)
	}
}

// TestPendingPermissionsReturnsActiveUnresolvedRequest confirms a
// session with an in-flight turn parked on an unresolved permission
// ask shows up, with tool/rationale/session_title carried through.
func TestPendingPermissionsReturnsActiveUnresolvedRequest(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	events := []stream.StreamEvent{
		{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{
			ID: "perm-1", CallID: "call-1", Tool: "gmail_search", Danger: "destructive",
			Rationale: "read the inbox",
		}},
	}
	a, dir, gw := testAPI(t, "tok", events)
	gw.blockCh = block
	defer close(block)

	id, _ := dir.Create(t.Context(), "my session")

	done := make(chan struct{})
	go func() {
		doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages", `{"message":"hello"}`)
		close(done)
	}()
	waitForTest(t, func() bool { return a.svc.TurnActive(id) })
	// The permission_request event is appended synchronously by
	// notePermission before the fake gateway blocks on the next event,
	// but give the relay goroutine a moment to persist it.
	waitForTest(t, func() bool {
		evs, _ := dir.Events(t.Context(), id)
		for _, ev := range evs {
			if ev.Kind == "permission_request" {
				return true
			}
		}
		return false
	})

	w := doMux(a, http.MethodGet, "/v1/permissions/pending", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Pending []struct {
			SessionID    string `json:"session_id"`
			SessionTitle string `json:"session_title"`
			Tool         string `json:"tool"`
			Rationale    string `json:"rationale"`
		} `json:"pending"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Pending) != 1 {
		t.Fatalf("pending = %#v, want exactly one entry", body.Pending)
	}
	got := body.Pending[0]
	if got.SessionID != id || got.SessionTitle != "my session" ||
		got.Tool != "gmail_search" || got.Rationale != "read the inbox" {
		t.Fatalf("pending[0] = %#v, want session %s/my session, tool gmail_search", got, id)
	}
}

// TestPendingPermissionsExcludesResolvedRequest confirms a
// permission_request with a matching permission_resolved in the same
// session's log never shows as pending, even while the turn is still
// technically active (a second ask could still be parked, but this one
// isn't).
func TestPendingPermissionsExcludesResolvedRequest(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	// fakeGateway's blockCh sends events[0] immediately, then waits on
	// block before sending the rest — so the resolve (events[1]) and a
	// trailing chunk (events[2], keeping the turn active afterward)
	// both land only once the test unblocks below.
	events := []stream.StreamEvent{
		{Type: stream.EventPermissionRequest, Permission: &stream.PermissionRequestEvent{
			ID: "perm-1", CallID: "call-1", Tool: "shell", Rationale: "run tests",
		}},
		{Type: stream.EventPermissionResolved, Resolved: &stream.PermissionResolvedEvent{
			ID: "perm-1", Decision: "once",
		}},
		{Type: stream.EventChunk, Text: "still going"},
	}
	a, dir, gw := testAPI(t, "tok", events)
	gw.blockCh = block

	id, _ := dir.Create(t.Context(), "t")

	done := make(chan struct{})
	go func() {
		doMux(a, http.MethodPost, "/v1/sessions/"+id+"/messages", `{"message":"hello"}`)
		close(done)
	}()
	waitForTest(t, func() bool { return a.svc.TurnActive(id) })
	close(block)
	waitForTest(t, func() bool {
		evs, _ := dir.Events(t.Context(), id)
		for _, ev := range evs {
			if ev.Kind == "permission_resolved" {
				return true
			}
		}
		return false
	})

	w := doMux(a, http.MethodGet, "/v1/permissions/pending", "")
	var body struct {
		Pending []map[string]any `json:"pending"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Pending) != 0 {
		t.Fatalf("pending = %#v, want empty (request already resolved)", body.Pending)
	}
}

// TestPendingPermissionsExcludesInactiveSession confirms a session
// whose turn already ended (crashed/finished without resolving the
// permission) never shows as pending: the endpoint scopes its query to
// chat.Service.ActiveSessions(), so a permission_request left in an
// idle session's log is not a zombie pending flag.
func TestPendingPermissionsExcludesInactiveSession(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	id, _ := dir.Create(t.Context(), "t")
	// Directly seed an unresolved permission_request into a session
	// with no active turn — simulates a crash that stranded the ask.
	if _, err := dir.Append(t.Context(), id, "permission_request", map[string]string{
		"id": "perm-1", "tool": "shell",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	w := doMux(a, http.MethodGet, "/v1/permissions/pending", "")
	var body struct {
		Pending []map[string]any `json:"pending"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Pending) != 0 {
		t.Fatalf("pending = %#v, want empty (session has no active turn)", body.Pending)
	}
}
