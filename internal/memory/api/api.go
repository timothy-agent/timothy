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
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/extract"
	"github.com/SumonMSelim/timothy/internal/memory/retrieval"
	"github.com/SumonMSelim/timothy/internal/memory/store"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
)

// Extractor runs one extraction job; *extract.Extractor satisfies it.
type Extractor interface {
	Extract(ctx context.Context, req extract.Request) ([]string, error)
}

// Searcher runs the retrieval legs; *retrieval.Searcher satisfies it.
type Searcher interface {
	Search(ctx context.Context, query string, embedding store.Vector, types []store.MemoryType) (map[string]*retrieval.Candidate, error)
	MarkRetrieved(ctx context.Context, ids []string)
}

// Embedder embeds the query text; *gwclient.Client satisfies it.
type Embedder interface {
	Embed(ctx context.Context, texts []string, purpose string) ([][]float32, string, error)
}

// API serves memoryd's routes.
type API struct {
	ext         Extractor
	search      Searcher
	embed       Embedder
	store       Manager
	consolidate ConsolidateRunner
	kb          KBManager
	kbDocs      DocumentStatusSetter
	log         *slog.Logger
}

// Register mounts the routes. kb/kbDocs nil leaves /ingest-document and
// /kb-search unmounted (WORKSPACES-style nil-gate, same as every other
// optional surface).
func Register(srv *httpserver.Server, ext Extractor, search Searcher, embed Embedder, st Manager, consolidate ConsolidateRunner, kb KBManager, kbDocs DocumentStatusSetter, log *slog.Logger) {
	a := &API{ext: ext, search: search, embed: embed, store: st, consolidate: consolidate, kb: kb, kbDocs: kbDocs, log: log}
	srv.Handle("POST /v1/extract", http.HandlerFunc(a.handleExtract))
	srv.Handle("POST /v1/retrieve", http.HandlerFunc(a.handleRetrieve))
	srv.Handle("GET /v1/memories", http.HandlerFunc(a.handleList))
	srv.Handle("POST /v1/memories", http.HandlerFunc(a.handleAdd))
	srv.Handle("POST /v1/memories/{id}", http.HandlerFunc(a.handleResolve))
	srv.Handle("GET /v1/memories/{id}/chain", http.HandlerFunc(a.handleChain))
	srv.Handle("GET /v1/entities/graph", http.HandlerFunc(a.handleEntityGraph))
	srv.Handle("GET /v1/entities/{id}/memories", http.HandlerFunc(a.handleEntityMemories))
	srv.Handle("POST /v1/consolidate", http.HandlerFunc(a.handleConsolidate))
	if kb != nil {
		srv.Handle("POST /v1/ingest-document", http.HandlerFunc(a.handleIngest))
		srv.Handle("POST /v1/kb-search", http.HandlerFunc(a.handleKBSearch))
	}
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

type retrieveRequest struct {
	Query        string   `json:"query"`
	SessionID    string   `json:"session_id,omitempty"`
	BudgetTokens int      `json:"budget_tokens,omitempty"`
	Types        []string `json:"types,omitempty"`
}

type retrievedMemory struct {
	ID      string  `json:"id"`
	Type    string  `json:"type"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// handleRetrieve answers one hybrid retrieval: embed the query, run
// the legs, fuse, pack to budget, stamp last_retrieved_at. A failed
// query embedding degrades to text+entity legs rather than failing —
// partial recall beats none.
func (a *API) handleRetrieve(w http.ResponseWriter, r *http.Request) {
	var req retrieveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "query is required")
		return
	}

	var embedding store.Vector
	if vecs, _, err := a.embed.Embed(r.Context(), []string{req.Query}, "memory-retrieve"); err != nil {
		a.log.Warn("query embedding failed; vector leg skipped", "error", err)
	} else {
		embedding = vecs[0]
	}

	types := make([]store.MemoryType, len(req.Types))
	for i, t := range req.Types {
		types[i] = store.MemoryType(t)
	}
	cands, err := a.search.Search(r.Context(), req.Query, embedding, types)
	if err != nil {
		a.log.Warn("retrieval failed", "error", err)
		jsonError(w, http.StatusInternalServerError, "retrieval_failed", err.Error())
		return
	}

	packed, tokens, err := retrieval.Pack(retrieval.Fuse(cands, time.Now()), req.BudgetTokens)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "retrieval_failed", err.Error())
		return
	}

	out := make([]retrievedMemory, len(packed))
	ids := make([]string, len(packed))
	for i, s := range packed {
		out[i] = retrievedMemory{ID: s.ID, Type: string(s.Type), Content: s.Content, Score: s.Score}
		ids[i] = s.ID
	}
	a.search.MarkRetrieved(r.Context(), ids)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"memories": out, "tokens": tokens})
}

func jsonError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}
