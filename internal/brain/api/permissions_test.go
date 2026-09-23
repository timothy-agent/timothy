package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/loop"
)

// apiPermStore is a loop.PermStore over an in-memory row set: enough
// for the pending listing and resolve paths the API exercises.
type apiPermStore struct {
	mu       sync.Mutex
	rows     []loop.PendingPermission
	resolved map[string]string
	carried  map[string]bool
}

func newAPIPermStore(rows ...loop.PendingPermission) *apiPermStore {
	return &apiPermStore{rows: rows, resolved: map[string]string{}, carried: map[string]bool{}}
}

func (s *apiPermStore) Insert(_ context.Context, p loop.PendingPermission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, p)
	return nil
}

func (s *apiPermStore) Redeem(context.Context, loop.PendingPermission, time.Duration, time.Duration) (string, string, error) {
	return "", "", nil
}

func (s *apiPermStore) RedeemID(context.Context, string) (string, bool, error) { return "", true, nil }

func (s *apiPermStore) Adopt(context.Context, loop.PendingPermission, []string) (string, error) {
	return "", nil
}

func (s *apiPermStore) Resolve(_ context.Context, id, decision string, carry bool) (loop.PendingPermission, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.rows {
		if p.ID == id {
			if _, done := s.resolved[id]; done {
				return loop.PendingPermission{}, false, nil
			}
			s.resolved[id] = decision
			s.carried[id] = carry
			return p, true, nil
		}
	}
	return loop.PendingPermission{}, false, nil
}

func (s *apiPermStore) Expire(context.Context, []string, time.Duration) (int64, error) { return 0, nil }

func (s *apiPermStore) Prune(context.Context, time.Duration) (int64, error) { return 0, nil }

func (s *apiPermStore) Pending(context.Context) ([]loop.PendingPermission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []loop.PendingPermission
	for _, p := range s.rows {
		if _, done := s.resolved[p.ID]; !done {
			out = append(out, p)
		}
	}
	return out, nil
}

// recordingMissionPerms records ResolvePendingPermission calls.
type recordingMissionPerms struct{ calls []string }

func (r *recordingMissionPerms) ResolvePendingPermission(_ context.Context, id, decision string) (string, bool, error) {
	r.calls = append(r.calls, id+"|"+decision)
	return "m1", true, nil
}

func brokerOver(store loop.PermStore) *loop.PermBroker {
	b := loop.NewPermBroker()
	b.SetStore(store, nil, nil)
	return b
}

// TestPendingPermissionsEmptyWhenNothingPending confirms the endpoint
// returns an empty list, never null, with no store rows and with no
// resolver wired at all.
func TestPendingPermissionsEmptyWhenNothingPending(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	for _, perms := range []PermissionResolver{brokerOver(newAPIPermStore()), nil} {
		a.perms = perms
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
}

// TestPendingPermissionsListingShape pins the D-118 response: every
// persisted pending row with id, args as an object and danger, chat
// and mission rows alike; resolved rows are left out.
func TestPendingPermissionsListingShape(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	store := newAPIPermStore(
		loop.PendingPermission{ID: "p-chat", SessionID: "s1", SessionTitle: "my session", Tool: "shell",
			Args: json.RawMessage(`{"command":"rm -rf build"}`), Danger: "destructive", Rationale: "danger classifier",
			OriginKind: loop.PermOriginChat, RequestedAt: at},
		loop.PendingPermission{ID: "p-mission", SessionID: "s2", MissionID: "m1", Tool: "write_file",
			Args: json.RawMessage(`{"path":"a.md"}`), Danger: "safe", OriginKind: loop.PermOriginMission, RequestedAt: at.Add(time.Second)},
		loop.PendingPermission{ID: "p-done", SessionID: "s1", Tool: "shell", Args: json.RawMessage(`{}`), OriginKind: loop.PermOriginChat},
	)
	store.resolved["p-done"] = loop.DecideDeny
	a, _, _ := testAPI(t, "tok", nil)
	a.perms = brokerOver(store)

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
	if len(body.Pending) != 2 {
		t.Fatalf("pending = %#v, want the two unresolved rows", body.Pending)
	}
	got := body.Pending[0]
	want := map[string]any{
		"id": "p-chat", "session_id": "s1", "session_title": "my session", "mission_id": "",
		"tool": "shell", "danger": "destructive", "rationale": "danger classifier",
		"origin_kind": "chat", "requested_at": "2026-09-23T10:00:00Z",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("pending[0][%s] = %#v, want %#v (row %#v)", k, got[k], v, got)
		}
	}
	args, ok := got["args"].(map[string]any)
	if !ok || args["command"] != "rm -rf build" {
		t.Fatalf("pending[0].args = %#v, want a parsed object", got["args"])
	}
	if m := body.Pending[1]; m["mission_id"] != "m1" || m["origin_kind"] != "mission" {
		t.Fatalf("pending[1] = %#v, want the mission row", m)
	}
}

// TestPermissionResolveAfterRestart covers D-118's API path: a broker
// with no live channel for the id (a fresh process) still answers a
// pending persisted row, records the decision with carry-over, and
// hands it to the mission recorder; a second answer and an unknown id
// are 404.
func TestPermissionResolveAfterRestart(t *testing.T) {
	t.Parallel()
	store := newAPIPermStore(loop.PendingPermission{ID: "p1", SessionID: "s1", MissionID: "m1", Tool: "shell", OriginKind: loop.PermOriginMission})
	a, _, _ := testAPI(t, "tok", nil)
	a.perms = brokerOver(store)
	rec := &recordingMissionPerms{}
	a.missionPerms = rec

	if w := doMux(a, http.MethodPost, "/v1/permissions/p1", `{"decision":"once"}`); w.Code != http.StatusOK {
		t.Fatalf("resolve after restart: %d %s", w.Code, w.Body)
	}
	if store.resolved["p1"] != loop.DecideOnce || !store.carried["p1"] {
		t.Fatalf("row decision=%q carried=%v, want once carried over", store.resolved["p1"], store.carried["p1"])
	}
	if len(rec.calls) != 1 || rec.calls[0] != "p1|once" {
		t.Fatalf("mission recorder calls = %v, want [p1|once]", rec.calls)
	}
	if w := doMux(a, http.MethodPost, "/v1/permissions/p1", `{"decision":"deny"}`); w.Code != http.StatusNotFound {
		t.Fatalf("second answer: %d, want 404", w.Code)
	}
	if store.resolved["p1"] != loop.DecideOnce {
		t.Fatalf("second answer changed the decision to %q", store.resolved["p1"])
	}
	if w := doMux(a, http.MethodPost, "/v1/permissions/nope", `{"decision":"once"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d, want 404", w.Code)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("mission recorder called on a 404: %v", rec.calls)
	}
}
