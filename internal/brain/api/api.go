// Package api is brain's public HTTP surface: bearer-authenticated
// session management and chat over SSE. /health and /metrics stay
// open (mounted by the platform server before auth exists).
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
)

// Directory is the session-management slice of the store; tests fake
// it, *session.Store satisfies it.
type Directory interface {
	Create(ctx context.Context, title string) (string, error)
	List(ctx context.Context, query string, before time.Time, beforeID string) ([]session.Meta, error)
	Get(ctx context.Context, id string) (session.Meta, error)
	Events(ctx context.Context, id string) ([]session.Event, error)
	Update(ctx context.Context, id string, title *string, archived *bool) error
}

// API serves brain's public routes.
type API struct {
	svc   *chat.Service
	dir   Directory
	token string
	log   *slog.Logger
}

// Register mounts the routes, each wrapped in bearer auth.
func Register(srv *httpserver.Server, svc *chat.Service, dir Directory, token string, log *slog.Logger) {
	a := &API{svc: svc, dir: dir, token: token, log: log}
	srv.Handle("GET /v1/sessions", a.auth(http.HandlerFunc(a.handleList)))
	srv.Handle("POST /v1/sessions", a.auth(http.HandlerFunc(a.handleCreate)))
	srv.Handle("GET /v1/sessions/{id}", a.auth(http.HandlerFunc(a.handleTranscript)))
	srv.Handle("PATCH /v1/sessions/{id}", a.auth(http.HandlerFunc(a.handleUpdate)))
	srv.Handle("POST /v1/sessions/{id}/messages", a.auth(http.HandlerFunc(a.handleMessages)))
	// Deprecated shim: same behavior, session_id in the body.
	srv.Handle("POST /v1/chat", a.auth(http.HandlerFunc(a.handleChatShim)))
}

// auth enforces the single bearer token. An unconfigured token fails
// closed with 503 — never open.
func (a *API) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.token == "" {
			jsonError(w, http.StatusServiceUnavailable, "auth_not_configured", "TIMOTHY_API_TOKEN is not set")
			return
		}
		got, ok := bearerToken(r)
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
			jsonError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}

func jsonError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// --- session management ---

func (a *API) handleList(w http.ResponseWriter, r *http.Request) {
	// The page cursor is the last row of the previous page: both halves
	// travel together so ties on updated_at cannot drop or repeat rows.
	var before time.Time
	beforeID := r.URL.Query().Get("before_id")
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", "before must be an RFC3339Nano timestamp")
			return
		}
		if beforeID == "" {
			jsonError(w, http.StatusBadRequest, "bad_request", "before requires before_id")
			return
		}
		before = t
	} else if beforeID != "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "before_id requires before")
		return
	}
	sessions, err := a.dir.List(r.Context(), r.URL.Query().Get("query"), before, beforeID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if sessions == nil {
		sessions = []session.Meta{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (a *API) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := a.dir.Create(r.Context(), req.Title)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (a *API) handleTranscript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	meta, err := a.dir.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		jsonError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	events, err := a.dir.Events(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "events_failed", err.Error())
		return
	}
	items, err := session.UITranscript(events)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "projection_failed", err.Error())
		return
	}
	if items == nil {
		items = []session.TranscriptItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": meta, "items": items})
}

func (a *API) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title    *string `json:"title"`
		Archived *bool   `json:"archived"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Title == nil && req.Archived == nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "nothing to update")
		return
	}
	if err := a.dir.Update(r.Context(), r.PathValue("id"), req.Title, req.Archived); err != nil {
		if strings.Contains(err.Error(), "not found") {
			jsonError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		jsonError(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- chat ---

func (a *API) handleMessages(w http.ResponseWriter, r *http.Request) {
	var req chat.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.SessionID = r.PathValue("id")
	if _, err := a.dir.Get(r.Context(), req.SessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		jsonError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	a.streamTurn(w, r, req)
}

// handleChatShim keeps the original /v1/chat contract alive for one
// deprecation window: session_id travels in the body and a missing one
// creates a session.
func (a *API) handleChatShim(w http.ResponseWriter, r *http.Request) {
	var req chat.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Link", `</v1/sessions/{id}/messages>; rel="successor-version"`)
	a.streamTurn(w, r, req)
}

// meta is brain's terminal SSE event: session identity plus whatever
// the gateway attributed on done.
//
// Wire contract (deliberately different from the internal channel
// contract): brain's chat SSE stream ALWAYS ends with exactly one
// meta event, emitted after the relayed gateway terminal (done or
// error). Clients must read until meta, not stop at done. Provider,
// model, usage, and ledger_id are best-effort — absent when no
// provider attempt succeeded.
type meta struct {
	Type      string        `json:"type"` // always "meta"
	SessionID string        `json:"session_id"`
	Provider  string        `json:"provider,omitempty"`
	Model     string        `json:"model,omitempty"`
	Usage     *stream.Usage `json:"usage,omitempty"`
	LedgerID  string        `json:"ledger_id,omitempty"`
}

func (a *API) streamTurn(w http.ResponseWriter, r *http.Request, req chat.Request) {
	sessionID, events, err := a.svc.Chat(r.Context(), req)
	if err != nil {
		// session_id rides the error when a row was already created so
		// the client reuses it on retry.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "chat_failed", "message": err.Error(), "session_id": sessionID,
		})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer cannot flush")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// The session id also rides a header so a mid-stream transport cut
	// (client never reaches the terminal meta) still can't orphan the
	// session — headers arrive before the first byte of the body.
	w.Header().Set("X-Session-Id", sessionID)

	send := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	m := meta{Type: "meta", SessionID: sessionID}
	for ev := range events {
		if ev.Type == stream.EventUsage {
			m.Usage = ev.Usage
		}
		if ev.Meta != nil {
			m.Provider, m.Model, m.LedgerID = ev.Meta.Provider, ev.Meta.Model, ev.Meta.LedgerID
		}
		send(ev)
	}
	send(m)
}
