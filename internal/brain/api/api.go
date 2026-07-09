// Package api is brain's public HTTP surface: bearer-authenticated
// chat over SSE. /health and /metrics stay open (mounted by the
// platform server before auth exists).
package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
)

// API serves brain's public routes.
type API struct {
	svc   *chat.Service
	token string
	log   *slog.Logger
}

// Register mounts the routes, each wrapped in bearer auth.
func Register(srv *httpserver.Server, svc *chat.Service, token string, log *slog.Logger) {
	a := &API{svc: svc, token: token, log: log}
	srv.Handle("POST /v1/chat", a.auth(http.HandlerFunc(a.handleChat)))
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

// meta is brain's terminal SSE event: session identity plus whatever
// the gateway attributed on done.
//
// Wire contract (deliberately different from the internal channel
// contract): brain's /v1/chat SSE stream ALWAYS ends with exactly one
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

func (a *API) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chat.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

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
