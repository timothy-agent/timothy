package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
)

// maxKBUploadBytes caps a single knowledge-base document upload.
const maxKBUploadBytes = 32 << 20 // 32MiB

// kbAllowedExt names the file extensions kb document upload accepts.
// .md/.txt skip markitdown entirely (already markdown/plain text);
// everything else converts through the sidecar.
var kbAllowedExt = map[string]bool{
	".pdf": true, ".md": true, ".txt": true, ".docx": true, ".html": true,
}

// kbIngester runs memoryd's ingest pipeline for one document; nil
// disables background ingestion (memoryd unreachable is a runtime
// error, not a nil-gate, but MEMORYD_URL is always configured — this
// interface exists so tests can fake it).
type kbIngester interface {
	IngestDocument(ctx context.Context, documentID, title, markdown string) (int, error)
}

// registerKB mounts the knowledge-base admin surface (D-060). Nil
// store leaves it unmounted, same nil-gate pattern as agents/skills.
func (a *API) registerKB(handle func(pattern string, h http.Handler), store *kb.Store, ingest kbIngester, markitdownURL string) {
	if store == nil {
		return
	}
	h := &kbAPI{store: store, ingest: ingest, markitdownURL: markitdownURL, markitdownHTTP: &http.Client{}, log: a.log}
	handle("GET /v1/admin/kb/collections", a.auth(http.HandlerFunc(h.listCollections)))
	handle("POST /v1/admin/kb/collections", a.auth(http.HandlerFunc(h.createCollection)))
	handle("GET /v1/admin/kb/collections/{id}", a.auth(http.HandlerFunc(h.getCollection)))
	handle("DELETE /v1/admin/kb/collections/{id}", a.auth(http.HandlerFunc(h.deleteCollection)))
	handle("GET /v1/admin/kb/collections/{id}/documents", a.auth(http.HandlerFunc(h.listDocuments)))
	handle("POST /v1/admin/kb/collections/{id}/documents", a.auth(http.HandlerFunc(h.uploadDocument)))
	handle("DELETE /v1/admin/kb/documents/{id}", a.auth(http.HandlerFunc(h.deleteDocument)))
	handle("POST /v1/admin/kb/documents/{id}/reingest", a.auth(http.HandlerFunc(h.reingestDocument)))
}

type kbAPI struct {
	store          *kb.Store
	ingest         kbIngester
	markitdownURL  string
	markitdownHTTP *http.Client
	log            *slog.Logger
}

func failKB(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, kb.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, kb.ErrInUse):
		jsonError(w, http.StatusConflict, "in_use", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

// sanitizeDocument clears Markdown before a document ever reaches
// writeJSON — the field stays populated in the struct (reingest reads
// it back) but never rides the wire, mirroring sanitizeMission's
// attachment-markdown strip in missions.go.
func sanitizeDocument(d kb.Document) kb.Document {
	d.Markdown = ""
	return d
}

func (h *kbAPI) listCollections(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListCollections(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": rows})
}

func (h *kbAPI) createCollection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	id, err := h.store.CreateCollection(r.Context(), req.Name, req.Description)
	if err != nil {
		failKB(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h *kbAPI) getCollection(w http.ResponseWriter, r *http.Request) {
	c, err := h.store.GetCollection(r.Context(), r.PathValue("id"))
	if err != nil {
		failKB(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *kbAPI) deleteCollection(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteCollection(r.Context(), r.PathValue("id")); err != nil {
		failKB(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *kbAPI) listDocuments(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListDocuments(r.Context(), r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
		return
	}
	out := make([]kb.Document, len(rows))
	for i, d := range rows {
		out[i] = sanitizeDocument(d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"documents": out})
}

// uploadDocument accepts a single multipart file field "file", caps
// its size at maxKBUploadBytes, converts it to markdown via markitdown
// (skipped for .md/.txt, already markdown/plain text), creates a
// pending document row, then fires the background ingest goroutine.
func (h *kbAPI) uploadDocument(w http.ResponseWriter, r *http.Request) {
	collectionID := r.PathValue("id")
	if _, err := h.store.GetCollection(r.Context(), collectionID); err != nil {
		failKB(w, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxKBUploadBytes)
	if err := r.ParseMultipartForm(maxKBUploadBytes); err != nil { //nolint:gosec // G120: r.Body is already MaxBytesReader-capped above at the same limit
		jsonError(w, http.StatusBadRequest, "too_large", "file exceeds the 32MiB limit")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "multipart field \"file\" is required")
		return
	}
	defer func() { _ = file.Close() }()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !kbAllowedExt[ext] {
		jsonError(w, http.StatusBadRequest, "unsupported_type", "file must be .pdf, .md, .txt, .docx, or .html")
		return
	}

	raw, err := io.ReadAll(file)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "failed to read upload: "+err.Error())
		return
	}

	var markdownText string
	if ext == ".md" || ext == ".txt" {
		markdownText = string(raw)
	} else {
		if h.markitdownURL == "" {
			jsonError(w, http.StatusBadRequest, "bad_request", "document conversion requires the markitdown sidecar (MARKITDOWN_URL)")
			return
		}
		md, err := markitdown.Convert(r.Context(), h.markitdownHTTP, h.markitdownURL, header.Filename, "", raw)
		if err != nil {
			jsonError(w, http.StatusBadGateway, "conversion_failed", err.Error())
			return
		}
		markdownText = markitdown.TruncateMarkdown(md)
	}

	// Converted output can carry NUL bytes or invalid UTF-8 (seen in
	// real PDFs); Postgres text columns reject both (SQLSTATE 22021).
	markdownText = strings.ToValidUTF8(strings.ReplaceAll(markdownText, "\x00", ""), "�")

	title := strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
	docID, err := h.store.CreateDocument(r.Context(), collectionID, title, header.Filename, markdownText, int64(len(raw)))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
		return
	}
	h.startIngest(docID, title)

	doc, err := h.store.GetDocument(r.Context(), docID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sanitizeDocument(doc))
}

// startIngest fires the status->ingesting->memoryd->terminal sequence
// in the background: upload/reingest both return as soon as the
// document row exists, not once ingestion finishes.
func (h *kbAPI) startIngest(docID, title string) {
	go func() {
		ctx := context.Background()
		if err := h.store.SetIngesting(ctx, docID); err != nil {
			h.log.Warn("kb ingest status update failed", "document_id", docID, "error", err)
		}
		doc, err := h.store.GetDocument(ctx, docID)
		if err != nil {
			h.log.Warn("kb ingest: document vanished before ingest", "document_id", docID, "error", err)
			return
		}
		if h.ingest == nil {
			_ = h.store.SetFailed(ctx, docID, "memoryd is not configured")
			return
		}
		if _, err := h.ingest.IngestDocument(ctx, docID, title, doc.Markdown); err != nil {
			h.log.Warn("kb ingest failed", "document_id", docID, "error", err)
			_ = h.store.SetFailed(ctx, docID, err.Error())
		}
	}()
}

func (h *kbAPI) deleteDocument(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteDocument(r.Context(), r.PathValue("id")); err != nil {
		failKB(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reingestDocument reuses the document's already-stored markdown (no
// re-conversion) and re-runs the ingest pipeline, which deletes the
// document's existing chunks before writing the new set.
func (h *kbAPI) reingestDocument(w http.ResponseWriter, r *http.Request) {
	docID := r.PathValue("id")
	doc, err := h.store.GetDocument(r.Context(), docID)
	if err != nil {
		failKB(w, err)
		return
	}
	if strings.TrimSpace(doc.Markdown) == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "document has no stored markdown to reingest")
		return
	}
	h.startIngest(docID, doc.Title)
	w.WriteHeader(http.StatusNoContent)
}
