package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
	"github.com/SumonMSelim/timothy/internal/platform/netguard"
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
	h := &kbAPI{
		store: store, ingest: ingest, markitdownURL: markitdownURL,
		markitdownHTTP: &http.Client{},
		fetchHTTP:      &http.Client{Timeout: kbURLFetchTimeout, Transport: kbFetchTransport},
		log:            a.log,
	}
	handle("GET /v1/admin/kb/collections", a.auth(http.HandlerFunc(h.listCollections)))
	handle("POST /v1/admin/kb/collections", a.auth(http.HandlerFunc(h.createCollection)))
	handle("GET /v1/admin/kb/collections/{id}", a.auth(http.HandlerFunc(h.getCollection)))
	handle("DELETE /v1/admin/kb/collections/{id}", a.auth(http.HandlerFunc(h.deleteCollection)))
	handle("GET /v1/admin/kb/collections/{id}/documents", a.auth(http.HandlerFunc(h.listDocuments)))
	handle("POST /v1/admin/kb/collections/{id}/documents", a.auth(http.HandlerFunc(h.uploadDocument)))
	handle("POST /v1/admin/kb/collections/{id}/documents/url", a.auth(http.HandlerFunc(h.addDocumentFromURL)))
	handle("DELETE /v1/admin/kb/documents/{id}", a.auth(http.HandlerFunc(h.deleteDocument)))
	handle("POST /v1/admin/kb/documents/{id}/reingest", a.auth(http.HandlerFunc(h.reingestDocument)))
}

type kbAPI struct {
	store          *kb.Store
	ingest         kbIngester
	markitdownURL  string
	markitdownHTTP *http.Client
	// fetchHTTP fetches user-supplied URLs; production wires it through
	// netguard.Dial so only public addresses are dialed (SSRF), tests
	// substitute an unguarded client.
	fetchHTTP *http.Client
	log       *slog.Logger
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
	docID, err := h.store.CreateDocument(r.Context(), collectionID, title, "file", header.Filename, markdownText, int64(len(raw)))
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

// kbURLFetchTimeout bounds one whole URL fetch (dial through read).
const kbURLFetchTimeout = 30 * time.Second

// kbFetchTransport dials user-supplied URLs through netguard so only
// public addresses are reached (SSRF). A var, not inline, so tests can
// point it at an unguarded transport; production never reassigns it.
var kbFetchTransport http.RoundTripper = &http.Transport{DialContext: netguard.Dial, ForceAttemptHTTP2: true}

type kbURLRequest struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// addDocumentFromURL fetches a user-supplied URL (public addresses
// only — the client dials through netguard), converts the response to
// markdown the same way upload does (HTML/PDF via markitdown, plain
// text and markdown taken as-is), creates a pending document row with
// the URL as source_ref, then fires the same background ingest.
func (h *kbAPI) addDocumentFromURL(w http.ResponseWriter, r *http.Request) {
	collectionID := r.PathValue("id")
	if _, err := h.store.GetCollection(r.Context(), collectionID); err != nil {
		failKB(w, err)
		return
	}

	var req kbURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "url must be a full http:// or https:// URL")
		return
	}
	// Never forward embedded credentials (and never leak them on a
	// redirect to another host).
	u.User = nil

	body, contentType, err := h.fetchURL(r.Context(), u)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "fetch_failed", err.Error())
		return
	}

	markdownText, err := h.convertFetched(r.Context(), u, body, contentType)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "conversion_failed", err.Error())
		return
	}
	// Same sanitation as upload: converted output can carry NUL bytes
	// or invalid UTF-8, which Postgres text columns reject (22021).
	markdownText = strings.ToValidUTF8(strings.ReplaceAll(markdownText, "\x00", ""), "�")
	if strings.TrimSpace(markdownText) == "" {
		jsonError(w, http.StatusBadRequest, "conversion_failed", "page had no extractable text")
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = titleFromURL(u)
	}
	docID, err := h.store.CreateDocument(r.Context(), collectionID, title, "url", u.String(), markdownText, int64(len(body)))
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

// fetchURL GETs u, capping the body at maxKBUploadBytes, and returns
// the bytes plus the response Content-Type.
func (h *kbAPI) fetchURL(ctx context.Context, u *url.URL) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "timothy/1.0 (+self-hosted assistant)")
	req.Header.Set("Accept", "text/html, application/pdf;q=0.9, text/plain;q=0.9, text/markdown;q=0.9, */*;q=0.1")

	resp, err := h.fetchHTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Bot-block statuses: LinkedIn's 999, plus the usual challenge
		// responses — a raw status code reads like our bug when it's the
		// site refusing automated clients.
		switch resp.StatusCode {
		case 999, http.StatusForbidden, http.StatusTooManyRequests:
			return nil, "", fmt.Errorf("http %d fetching %s: the site blocks automated access — save the page as a PDF and upload it instead", resp.StatusCode, u.Host)
		}
		return nil, "", fmt.Errorf("http %d fetching %s", resp.StatusCode, u.Host)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKBUploadBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read body: %w", err)
	}
	if len(body) > maxKBUploadBytes {
		return nil, "", fmt.Errorf("response exceeds the 32MiB limit")
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// convertFetched turns a fetched body into markdown by content type:
// HTML and PDF through markitdown (required, same as upload), plain
// text and markdown as-is. Anything else is unsupported.
func (h *kbAPI) convertFetched(ctx context.Context, u *url.URL, body []byte, contentType string) (string, error) {
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}
	switch {
	case strings.Contains(contentType, "text/html"), strings.Contains(contentType, "application/pdf"):
		if h.markitdownURL == "" {
			return "", fmt.Errorf("document conversion requires the markitdown sidecar (MARKITDOWN_URL)")
		}
		name := "page.html"
		if strings.Contains(contentType, "application/pdf") {
			name = "page.pdf"
		} else {
			body = stripHTMLChrome(body)
		}
		md, err := markitdown.Convert(ctx, h.markitdownHTTP, h.markitdownURL, name, contentType, body)
		if err != nil {
			return "", err
		}
		return markitdown.TruncateMarkdown(md), nil
	case strings.Contains(contentType, "text/markdown"), strings.Contains(contentType, "text/plain"):
		return string(body), nil
	default:
		return "", fmt.Errorf("unsupported content type %q at %s: only html, pdf, markdown, and plain text", contentType, u.Host)
	}
}

// titleFromURL derives a document title from the URL: the last
// non-empty path segment without its extension, else the host.
func titleFromURL(u *url.URL) string {
	seg := path.Base(strings.TrimRight(u.Path, "/"))
	seg = strings.TrimSuffix(seg, path.Ext(seg))
	if seg != "" && seg != "." && seg != "/" {
		return seg
	}
	return u.Host
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
