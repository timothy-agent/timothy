// Package api is memoryd's internal HTTP surface. Like the gateway's
// API it is reachable only on the compose network (brain is the sole
// caller), so routes carry no bearer auth; /health and /metrics are
// mounted by the platform server.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/SumonMSelim/timothy/internal/memory/extract"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
)

// Extractor runs one extraction job; *extract.Extractor satisfies it.
type Extractor interface {
	Extract(ctx context.Context, req extract.Request) ([]string, error)
}

// API serves memoryd's routes.
type API struct {
	ext Extractor
	log *slog.Logger
}

// Register mounts the routes.
func Register(srv *httpserver.Server, ext Extractor, log *slog.Logger) {
	a := &API{ext: ext, log: log}
	srv.Handle("POST /v1/extract", http.HandlerFunc(a.handleExtract))
}

// handleExtract runs extraction synchronously and returns the inserted
// memory ids. Callers choose their own coupling: turn-end posts from a
// goroutine and drops the response; pre-compaction waits for the ids.
func (a *API) handleExtract(w http.ResponseWriter, r *http.Request) {
	var req extract.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "text is required")
		return
	}

	ids, err := a.ext.Extract(r.Context(), req)
	if err != nil {
		a.log.Warn("extraction failed", "session_id", req.SessionID, "error", err)
		jsonError(w, http.StatusBadGateway, "extraction_failed", err.Error())
		return
	}
	if ids == nil {
		ids = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"memory_ids": ids})
}

func jsonError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}
