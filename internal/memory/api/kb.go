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
	KBSearch(ctx context.Context, query string, embedding store.Vector, collectionNames, boostCollections []string, mode store.KBSearchMode, k int) ([]store.KBSearchHit, error)
}

// DocumentStatusSetter reports ingestion outcomes back onto brain's
// kb_documents row: memoryd only ever reports ready/failed here; it
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
// deletes-and-rewrites (ReplaceChunks): a re-ingest is never additive.
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
	// Embed in bounded batches: a large document's chunk set in one
	// request can exceed a provider's per-call input limit.
	var vecs [][]float32
	var model string
	for start := 0; start < len(texts); start += kbEmbedBatchSize {
		end := min(start+kbEmbedBatchSize, len(texts))
		batch, batchModel, err := a.embed.Embed(r.Context(), texts[start:end], "kb-ingest")
		if err != nil {
			a.log.Warn("kb ingest embedding failed", "document_id", req.DocumentID, "error", err)
			a.reportKBFailed(r.Context(), req.DocumentID, fmt.Sprintf("embedding failed: %v", err))
			jsonError(w, http.StatusBadGateway, "embedding_failed", err.Error())
			return
		}
		vecs = append(vecs, batch...)
		model = batchModel
	}

	chunks := make([]store.KBChunk, len(pieces))
	for i, p := range pieces {
		chunks[i] = store.KBChunk{
			Seq: p.Seq, Breadcrumb: p.Breadcrumb, Content: p.Content,
			Embedding: store.Vector(vecs[i]), EmbeddingModel: model,
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
	Query            string   `json:"query"`
	CollectionNames  []string `json:"collection_names"`
	BoostCollections []string `json:"boost_collections"`
	Mode             string   `json:"mode"`
	K                int      `json:"k"`
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

	// kbEmbedBatchSize bounds how many chunk texts ride one gateway
	// embed call during ingestion.
	kbEmbedBatchSize = 16
)

// handleKBSearch runs hybrid/semantic/keyword retrieval over the
// knowledge base. collection_names is optional: empty searches every
// collection, non-empty scopes to it (an explicit narrowing).
// boost_collections never narrows the result set, it only reorders it
// (issue #368: collections are a ranking boost, not an access gate).
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
		vecs, _, err := a.embed.Embed(r.Context(), []string{req.Query}, "kb-search")
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

	hits, err := a.kb.KBSearch(r.Context(), req.Query, embedding, req.CollectionNames, req.BoostCollections, mode, k)
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
