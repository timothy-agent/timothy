package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/SumonMSelim/timothy/internal/memory/chunk"
	"github.com/SumonMSelim/timothy/internal/memory/store"
)

// KBManager is the knowledge-base slice of the store; *store.KBStore
// satisfies it. Nil leaves the KB routes unmounted.
type KBManager interface {
	ReplaceChunks(ctx context.Context, documentID string, chunks []store.KBChunk) error
	KBSearch(ctx context.Context, query string, embedding store.Vector, collectionNames []string, mode store.KBSearchMode, k int) ([]store.KBSearchHit, error)
}

// DocumentStatusSetter reports ingestion outcomes back onto brain's
// kb_documents row — memoryd only ever reports ready/failed here; it
// does not own that table (internal/brain/kb does).
type DocumentStatusSetter interface {
	SetIngested(ctx context.Context, documentID string, chunkCount int) error
	SetFailed(ctx context.Context, documentID string, errMsg string) error
}

type ingestRequest struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title"`
	Markdown   string `json:"markdown"`
}

// handleIngest runs the synchronous ingestion pipeline: chunk, embed,
// replace the document's existing chunks, report the outcome. Always
// deletes-and-rewrites (ReplaceChunks) — a re-ingest is never additive.
func (a *API) handleIngest(w http.ResponseWriter, r *http.Request) {
	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(req.DocumentID) == "" || strings.TrimSpace(req.Markdown) == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "document_id and markdown are required")
		return
	}

	pieces := chunk.Split(req.Title, req.Markdown)
	if len(pieces) == 0 {
		a.reportKBFailed(r.Context(), req.DocumentID, "document produced no chunks")
		jsonError(w, http.StatusBadRequest, "empty_document", "document produced no chunks")
		return
	}

	texts := make([]string, len(pieces))
	for i, p := range pieces {
		texts[i] = p.Breadcrumb + "\n\n" + p.Content
	}
	const embedPurpose = "kb-ingest"
	vecs, err := a.embed.Embed(r.Context(), texts, embedPurpose)
	if err != nil {
		a.log.Warn("kb ingest embedding failed", "document_id", req.DocumentID, "error", err)
		a.reportKBFailed(r.Context(), req.DocumentID, fmt.Sprintf("embedding failed: %v", err))
		jsonError(w, http.StatusBadGateway, "embedding_failed", err.Error())
		return
	}

	chunks := make([]store.KBChunk, len(pieces))
	for i, p := range pieces {
		chunks[i] = store.KBChunk{
			Seq: p.Seq, Breadcrumb: p.Breadcrumb, Content: p.Content,
			Embedding: store.Vector(vecs[i]), EmbeddingModel: embedPurpose,
		}
	}
	if err := a.kb.ReplaceChunks(r.Context(), req.DocumentID, chunks); err != nil {
		a.log.Warn("kb ingest store failed", "document_id", req.DocumentID, "error", err)
		a.reportKBFailed(r.Context(), req.DocumentID, err.Error())
		jsonError(w, http.StatusInternalServerError, "ingest_failed", err.Error())
		return
	}
	if a.kbDocs != nil {
		if err := a.kbDocs.SetIngested(r.Context(), req.DocumentID, len(chunks)); err != nil {
			a.log.Warn("kb ingest status update failed", "document_id", req.DocumentID, "error", err)
		}
	}
	a.log.Info("kb document ingested", "document_id", req.DocumentID, "chunks", len(chunks))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"chunk_count": len(chunks)})
}

func (a *API) reportKBFailed(ctx context.Context, documentID, msg string) {
	if a.kbDocs == nil {
		return
	}
	if err := a.kbDocs.SetFailed(ctx, documentID, msg); err != nil {
		a.log.Warn("kb ingest failure report failed", "document_id", documentID, "error", err)
	}
}

type kbSearchRequest struct {
	Query           string   `json:"query"`
	CollectionNames []string `json:"collection_names"`
	Mode            string   `json:"mode"`
	K               int      `json:"k"`
}

type kbSearchHitJSON struct {
	ChunkID       string  `json:"chunk_id"`
	DocumentID    string  `json:"document_id"`
	DocumentTitle string  `json:"document_title"`
	Collection    string  `json:"collection"`
	Breadcrumb    string  `json:"breadcrumb"`
	Content       string  `json:"content"`
	Score         float64 `json:"score"`
	SourceRef     string  `json:"source_ref"`
}

const (
	kbSearchDefaultK = 8
	kbSearchMaxK     = 10
)

// handleKBSearch runs hybrid/semantic/keyword retrieval scoped to the
// caller-provided collection names — collection_names is required and
// non-empty; brain resolves it from the calling agent's Knowledge
// allowlist before this call, but memoryd enforces the non-empty
// requirement independently rather than trusting the caller.
func (a *API) handleKBSearch(w http.ResponseWriter, r *http.Request) {
	var req kbSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "query is required")
		return
	}
	if len(req.CollectionNames) == 0 {
		jsonError(w, http.StatusBadRequest, "bad_request", "collection_names must be non-empty")
		return
	}
	mode := store.KBSearchMode(req.Mode)
	switch mode {
	case "", store.KBSearchHybrid:
		mode = store.KBSearchHybrid
	case store.KBSearchSemantic, store.KBSearchKeyword:
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", "mode must be hybrid, semantic, or keyword")
		return
	}
	k := req.K
	if k <= 0 {
		k = kbSearchDefaultK
	}
	if k > kbSearchMaxK {
		k = kbSearchMaxK
	}

	var embedding store.Vector
	if mode != store.KBSearchKeyword {
		vecs, err := a.embed.Embed(r.Context(), []string{req.Query}, "kb-search")
		if err != nil {
			a.log.Warn("kb search embedding failed; degrading to keyword-only", "error", err)
			if mode == store.KBSearchSemantic {
				jsonError(w, http.StatusBadGateway, "embedding_failed", err.Error())
				return
			}
			mode = store.KBSearchKeyword
		} else {
			embedding = vecs[0]
		}
	}

	hits, err := a.kb.KBSearch(r.Context(), req.Query, embedding, req.CollectionNames, mode, k)
	if err != nil {
		a.log.Warn("kb search failed", "error", err)
		jsonError(w, http.StatusInternalServerError, "search_failed", err.Error())
		return
	}

	out := make([]kbSearchHitJSON, len(hits))
	var topScore float64
	for i, hit := range hits {
		out[i] = kbSearchHitJSON{
			ChunkID: hit.ChunkID, DocumentID: hit.DocumentID, DocumentTitle: hit.DocumentTitle,
			Collection: hit.Collection, Breadcrumb: hit.Breadcrumb, Content: hit.Content,
			Score: hit.Score, SourceRef: hit.SourceRef,
		}
		if i == 0 {
			topScore = hit.Score
		}
	}
	a.log.Info("kb search", "mode", string(mode), "k", k, "top_score", topScore, "rows", len(out))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"results": out})
}
