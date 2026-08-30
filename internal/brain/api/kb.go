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

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
	"github.com/SumonMSelim/timothy/internal/platform/netguard"
)

// maxKBUploadBytes caps a single knowledge-base document upload.
const maxKBUploadBytes = 32 << 20 // 32MiB

// maxClipMarkdownBytes caps a browser-extension clip's markdown,
// mirroring markitdown's own output cap (markitdown.TruncateMarkdown).
const maxClipMarkdownBytes = 128 << 10 // 128KiB

// clipTitleInputRunes bounds how much markdown a clip with no title
// feeds to the titler; clipTitleMaxRunes bounds the generated title
// itself.
const (
	clipTitleInputRunes = 2000
	clipTitleMaxRunes   = 200
)

// kbAllowedExt names the file extensions kb document upload accepts.
// .md/.txt skip markitdown entirely (already markdown/plain text);
// everything else converts through the sidecar.
var kbAllowedExt = map[string]bool{
	".pdf": true, ".md": true, ".txt": true, ".docx": true, ".html": true,
}

// kbIngester runs memoryd's ingest pipeline for one document; nil
// disables background ingestion (memoryd unreachable is a runtime
// error, not a nil-gate, but MEMORYD_URL is always configured: this
// interface exists so tests can fake it).
type kbIngester interface {
	IngestDocument(ctx context.Context, documentID, title, markdown string) (int, error)
}

// kbClassifier picks (or proposes) a collection for a document at
// auto-ingest time: chat.ClassifyCollectionOverGateway in production,
// faked in tests. Never errors (same best-effort contract as the
// classifier itself): a document must always resolve to some choice.
type kbClassifier func(ctx context.Context, docTitle, docText string, collections []kb.Collection) chat.CollectionChoice

// kbTitler generates a document title from a markdown excerpt:
// chat.TitleOverGateway in production, faked in tests. Never errors;
// an empty return means the caller falls back to titleFromURL.
type kbTitler func(ctx context.Context, input string) string

// registerKB mounts the knowledge-base admin surface (D-060). Nil
// store leaves it unmounted, same nil-gate pattern as agents/skills.
func (a *API) registerKB(handle func(pattern string, h http.Handler), store *kb.Store, ingest kbIngester, markitdownURL string, classify kbClassifier, title kbTitler) {
	if store == nil {
		return
	}
	h := &kbAPI{
		store: store, ingest: ingest, markitdownURL: markitdownURL, classify: classify, title: title,
		markitdownHTTP: &http.Client{},
		fetchHTTP:      &http.Client{Timeout: kbURLFetchTimeout, Transport: kbFetchTransport},
		log:            a.log,
	}
	handle("GET /v1/admin/kb/collections", a.auth(http.HandlerFunc(h.listCollections)))
	handle("POST /v1/admin/kb/collections", a.auth(http.HandlerFunc(h.createCollection)))
	handle("GET /v1/admin/kb/collections/{id}", a.auth(http.HandlerFunc(h.getCollection)))
	handle("PATCH /v1/admin/kb/collections/{id}", a.auth(http.HandlerFunc(h.updateCollection)))
	handle("DELETE /v1/admin/kb/collections/{id}", a.auth(http.HandlerFunc(h.deleteCollection)))
	handle("GET /v1/admin/kb/collections/{id}/documents", a.auth(http.HandlerFunc(h.listDocuments)))
	handle("GET /v1/admin/kb/documents", a.auth(http.HandlerFunc(h.searchDocuments)))
	handle("POST /v1/admin/kb/collections/{id}/documents", a.auth(http.HandlerFunc(h.uploadDocument)))
	handle("POST /v1/admin/kb/collections/{id}/documents/url", a.auth(http.HandlerFunc(h.addDocumentFromURL)))
	handle("POST /v1/admin/kb/documents", a.auth(http.HandlerFunc(h.uploadDocumentAuto)))
	handle("POST /v1/admin/kb/documents/url", a.auth(http.HandlerFunc(h.addDocumentFromURLAuto)))
	handle("POST /v1/admin/kb/documents/clip", a.auth(http.HandlerFunc(h.clipDocument)))
	handle("DELETE /v1/admin/kb/documents/{id}", a.auth(http.HandlerFunc(h.deleteDocument)))
	handle("POST /v1/admin/kb/documents/{id}/reingest", a.auth(http.HandlerFunc(h.reingestDocument)))
}

type kbAPI struct {
	store          *kb.Store
	ingest         kbIngester
	classify       kbClassifier
	title          kbTitler
	markitdownURL  string
	markitdownHTTP *http.Client
	// fetchHTTP fetches user-supplied URLs; production wires it through
	// netguard.Dial so only public addresses are dialed (SSRF), tests
	// substitute an unguarded client.
	fetchHTTP *http.Client
	log       *slog.Logger
}

// resolveCollection runs the auto-classify path shared by
// uploadDocumentAuto/addDocumentFromURLAuto: lists existing collections,
// classifies the document against them, and creates a new collection
// when the classifier proposes one. Never fails outright: a
// CreateCollection error just logs and falls through to "Unsorted" via
// a second attempt, since ingest must not block on this step.
func (h *kbAPI) resolveCollection(ctx context.Context, title, markdownText string) (string, error) {
	collections, err := h.store.ListCollections(ctx)
	if err != nil {
		return "", fmt.Errorf("list collections: %w", err)
	}
	choice := h.classify(ctx, title, markdownText, collections)
	if choice.ExistingID != "" {
		return choice.ExistingID, nil
	}
	name := choice.NewName
	if name == "" {
		name = "Unsorted"
	}
	id, err := h.store.CreateCollection(ctx, name, choice.NewDesc)
	if err != nil {
		return "", fmt.Errorf("create collection %q: %w", name, err)
	}
	return id, nil
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
// writeJSON: the field stays populated in the struct (reingest reads
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

// updateCollection renames a collection and/or replaces its
// description: absent fields stay untouched (pointer-decoded PATCH,
// same shape the settings handlers use).
func (h *kbAPI) updateCollection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Name == nil && req.Description == nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "nothing to update")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "name must not be empty")
		return
	}
	if err := h.store.UpdateCollection(r.Context(), r.PathValue("id"), req.Name, req.Description); err != nil {
		failKB(w, err)
		return
	}
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

// searchDocuments serves GET /v1/admin/kb/documents?q=<text>: a
// cross-collection title search, the composer #-mention "type to
// find a kb document" search. An empty/absent q returns every
// document, same shape as listDocuments but not scoped to one
// collection.
func (h *kbAPI) searchDocuments(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.SearchDocuments(r.Context(), r.URL.Query().Get("q"))
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

// decodedUpload is one multipart file field, parsed and converted to
// markdown: shared by uploadDocument (collection given) and
// uploadDocumentAuto (collection classified after this step).
type decodedUpload struct {
	title, filename, markdown string
	rawBytes                  int64
}

// decodeUpload parses the multipart "file" field, caps it at
// maxKBUploadBytes, and converts it to markdown via markitdown (skipped
// for .md/.txt, already markdown/plain text).
func (h *kbAPI) decodeUpload(w http.ResponseWriter, r *http.Request) (decodedUpload, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxKBUploadBytes)
	if err := r.ParseMultipartForm(maxKBUploadBytes); err != nil { //nolint:gosec // G120: r.Body is already MaxBytesReader-capped above at the same limit
		jsonError(w, http.StatusBadRequest, "too_large", "file exceeds the 32MiB limit")
		return decodedUpload{}, false
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "multipart field \"file\" is required")
		return decodedUpload{}, false
	}
	defer func() { _ = file.Close() }()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !kbAllowedExt[ext] {
		jsonError(w, http.StatusBadRequest, "unsupported_type", "file must be .pdf, .md, .txt, .docx, or .html")
		return decodedUpload{}, false
	}

	raw, err := io.ReadAll(file)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "failed to read upload: "+err.Error())
		return decodedUpload{}, false
	}

	var markdownText string
	if ext == ".md" || ext == ".txt" {
		markdownText = string(raw)
	} else {
		if h.markitdownURL == "" {
			jsonError(w, http.StatusBadRequest, "bad_request", "document conversion requires the markitdown sidecar (MARKITDOWN_URL)")
			return decodedUpload{}, false
		}
		md, err := markitdown.Convert(r.Context(), h.markitdownHTTP, h.markitdownURL, header.Filename, "", raw)
		if err != nil {
			jsonError(w, http.StatusBadGateway, "conversion_failed", err.Error())
			return decodedUpload{}, false
		}
		markdownText = markitdown.TruncateMarkdown(md)
	}

	// Converted output can carry NUL bytes or invalid UTF-8 (seen in
	// real PDFs); Postgres text columns reject both (SQLSTATE 22021).
	markdownText = strings.ToValidUTF8(strings.ReplaceAll(markdownText, "\x00", ""), "�")

	return decodedUpload{
		title:    strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename)),
		filename: header.Filename,
		markdown: markdownText,
		rawBytes: int64(len(raw)),
	}, true
}

// finishIngest creates the pending document row, fires the background
// ingest goroutine, and writes the created document back: the shared
// tail of every ingest entry point (scoped and auto alike). Every
// caller is operator-driven (file upload or URL fetch), so provenance
// is always 'curated' (D-080); the browser-extension clip path
// bypasses finishIngest and sets 'web' directly.
func (h *kbAPI) finishIngest(w http.ResponseWriter, r *http.Request, collectionID, title, sourceType, sourceRef, markdownText string, size int64) {
	docID, err := h.store.CreateDocument(r.Context(), collectionID, title, sourceType, sourceRef, "curated", markdownText, size)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
		return
	}
	// Read the document back before kicking off ingestion so the
	// response deterministically reports the created "pending" state
	// instead of racing the background ingest goroutine.
	doc, err := h.store.GetDocument(r.Context(), docID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
		return
	}
	h.startIngest(docID, title)
	writeJSON(w, http.StatusCreated, sanitizeDocument(doc))
}

// uploadDocument accepts a single multipart file field "file" into a
// caller-chosen collection.
func (h *kbAPI) uploadDocument(w http.ResponseWriter, r *http.Request) {
	collectionID := r.PathValue("id")
	if _, err := h.store.GetCollection(r.Context(), collectionID); err != nil {
		failKB(w, err)
		return
	}
	up, ok := h.decodeUpload(w, r)
	if !ok {
		return
	}
	h.finishIngest(w, r, collectionID, up.title, "file", up.filename, up.markdown, up.rawBytes)
}

// uploadDocumentAuto accepts the same multipart upload as
// uploadDocument but with no collection chosen: the document is
// classified against existing collections (or files into a newly
// proposed one) before ingest.
func (h *kbAPI) uploadDocumentAuto(w http.ResponseWriter, r *http.Request) {
	up, ok := h.decodeUpload(w, r)
	if !ok {
		return
	}
	collectionID, err := h.resolveCollection(r.Context(), up.title, up.markdown)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
		return
	}
	h.finishIngest(w, r, collectionID, up.title, "file", up.filename, up.markdown, up.rawBytes)
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

// decodedURL is one fetched-and-converted URL: shared by
// addDocumentFromURL (collection given) and addDocumentFromURLAuto
// (collection classified after this step).
type decodedURL struct {
	title, url, markdown string
	rawBytes             int64
}

// decodeURL parses req.URL, fetches it (public addresses only: the
// client dials through netguard), and converts the response to
// markdown the same way upload does (HTML/PDF via markitdown, plain
// text and markdown taken as-is).
func (h *kbAPI) decodeURL(w http.ResponseWriter, r *http.Request, req kbURLRequest) (decodedURL, bool) {
	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "url must be a full http:// or https:// URL")
		return decodedURL{}, false
	}
	// Never forward embedded credentials (and never leak them on a
	// redirect to another host).
	u.User = nil

	body, contentType, err := h.fetchURL(r.Context(), u)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "fetch_failed", err.Error())
		return decodedURL{}, false
	}

	markdownText, err := h.convertFetched(r.Context(), u, body, contentType)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "conversion_failed", err.Error())
		return decodedURL{}, false
	}
	// Same sanitation as upload: converted output can carry NUL bytes
	// or invalid UTF-8, which Postgres text columns reject (22021).
	markdownText = strings.ToValidUTF8(strings.ReplaceAll(markdownText, "\x00", ""), "�")
	if strings.TrimSpace(markdownText) == "" {
		jsonError(w, http.StatusBadRequest, "conversion_failed", "page had no extractable text")
		return decodedURL{}, false
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = titleFromURL(u)
	}
	return decodedURL{title: title, url: normalizeSourceURL(u), markdown: markdownText, rawBytes: int64(len(body))}, true
}

// refreshOrCreate looks up an existing document by (sourceType,
// sourceRef). On a hit it refreshes the row in place (200 OK), moving
// it to moveTo ("" keeps the current collection). On a miss it calls
// resolveCollectionID to pick a collection (classifying or using the
// caller's choice) and creates a new document (201 Created).
func (h *kbAPI) refreshOrCreate(w http.ResponseWriter, r *http.Request, resolveCollectionID func() (string, error), moveTo, title, sourceType, sourceRef, markdownText string, size int64) {
	existing, err := h.store.FindDocumentBySource(r.Context(), sourceType, sourceRef)
	switch {
	case errors.Is(err, kb.ErrNotFound):
		collectionID, err := resolveCollectionID()
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
			return
		}
		h.finishIngest(w, r, collectionID, title, sourceType, sourceRef, markdownText, size)
	case err != nil:
		jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
	default:
		if err := h.store.ReplaceDocumentContent(r.Context(), existing.ID, title, markdownText, size, moveTo); err != nil {
			failKB(w, err)
			return
		}
		doc, err := h.store.GetDocument(r.Context(), existing.ID)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
			return
		}
		h.startIngest(existing.ID, title)
		writeJSON(w, http.StatusOK, sanitizeDocument(doc))
	}
}

// addDocumentFromURL fetches a user-supplied URL into a caller-chosen
// collection. Re-adding an already-known URL (by source_type=url,
// source_ref=normalized URL) refreshes the existing document in place,
// moving it to collectionID since the operator explicitly chose it.
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
	d, ok := h.decodeURL(w, r, req)
	if !ok {
		return
	}
	resolve := func() (string, error) { return collectionID, nil }
	h.refreshOrCreate(w, r, resolve, collectionID, d.title, "url", d.url, d.markdown, d.rawBytes)
}

// addDocumentFromURLAuto fetches a user-supplied URL with no collection
// chosen: the document is classified against existing collections (or
// files into a newly proposed one) before ingest. Re-adding an
// already-known URL refreshes it in place and keeps its current
// collection, skipping the classifier.
func (h *kbAPI) addDocumentFromURLAuto(w http.ResponseWriter, r *http.Request) {
	var req kbURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	d, ok := h.decodeURL(w, r, req)
	if !ok {
		return
	}
	resolve := func() (string, error) { return h.resolveCollection(r.Context(), d.title, d.markdown) }
	h.refreshOrCreate(w, r, resolve, "", d.title, "url", d.url, d.markdown, d.rawBytes)
}

// kbClipRequest is a browser-extension clip: the extension converts the
// page to markdown client-side, so this path never fetches or converts
// anything itself.
type kbClipRequest struct {
	URL          string `json:"url"`
	Title        string `json:"title"`
	Markdown     string `json:"markdown"`
	CollectionID string `json:"collection_id"`
}

// normalizeSourceURL is the url/clip dedup key: fragment dropped, and
// tracking query parameters (fbclid, gclid, utm_*) stripped. The clip
// extension already strips these client-side; this re-strips as
// defense so a stray tracking param never splits one page into two
// documents.
func normalizeSourceURL(u *url.URL) string {
	out := *u
	out.Fragment = ""
	q := out.Query()
	for key := range q {
		lower := strings.ToLower(key)
		if lower == "fbclid" || lower == "gclid" || strings.HasPrefix(lower, "utm_") {
			q.Del(key)
		}
	}
	out.RawQuery = q.Encode()
	return out.String()
}

// truncateRunes cuts s to at most n runes, not bytes (titles and
// markdown excerpts can carry multi-byte UTF-8).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// clipDocument ingests a browser-extension clip: the extension sends
// pre-converted markdown, so this handler validates and stores it
// directly, with no fetch and no markitdown conversion. Re-clipping an
// already-known URL (by source_type=clip, source_ref=normalized URL)
// refreshes the existing document in place rather than creating a
// duplicate.
func (h *kbAPI) clipDocument(w http.ResponseWriter, r *http.Request) {
	var req kbClipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "url must be a full http:// or https:// URL")
		return
	}
	u.User = nil

	markdownText := strings.TrimSpace(req.Markdown)
	if markdownText == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "markdown is required")
		return
	}
	if len(req.Markdown) > maxClipMarkdownBytes {
		jsonError(w, http.StatusRequestEntityTooLarge, "too_large", "markdown exceeds the 128KiB limit")
		return
	}

	collectionID := strings.TrimSpace(req.CollectionID)
	if collectionID != "" {
		if _, err := h.store.GetCollection(r.Context(), collectionID); err != nil {
			failKB(w, err)
			return
		}
	}

	// Sanitize the same as every other ingest path: converted (or here,
	// extension-supplied) markdown can carry NUL bytes or invalid UTF-8,
	// which Postgres text columns reject (22021).
	markdownText = strings.ToValidUTF8(strings.ReplaceAll(req.Markdown, "\x00", ""), "�")

	title := strings.TrimSpace(req.Title)
	if title == "" {
		if h.title != nil {
			title = truncateRunes(strings.TrimSpace(h.title(r.Context(), truncateRunes(markdownText, clipTitleInputRunes))), clipTitleMaxRunes)
		}
		if title == "" {
			title = titleFromURL(u)
		}
	}

	sourceRef := normalizeSourceURL(u)
	existing, err := h.store.FindDocumentBySource(r.Context(), "clip", sourceRef)
	switch {
	case errors.Is(err, kb.ErrNotFound):
		if collectionID == "" {
			collectionID, err = h.resolveCollection(r.Context(), title, markdownText)
			if err != nil {
				jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
				return
			}
		}
		docID, err := h.store.CreateDocument(r.Context(), collectionID, title, "clip", sourceRef, "web", markdownText, int64(len(markdownText)))
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
			return
		}
		doc, err := h.store.GetDocument(r.Context(), docID)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
			return
		}
		h.startIngest(docID, title)
		writeJSON(w, http.StatusAccepted, sanitizeDocument(doc))
	case err != nil:
		jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
	default:
		// Re-clip: refresh the existing document rather than duplicate
		// it. startIngest's memoryd call deletes the old chunks before
		// writing the new set (same as reingestDocument).
		if err := h.store.ReplaceDocumentContent(r.Context(), existing.ID, title, markdownText, int64(len(markdownText)), collectionID); err != nil {
			failKB(w, err)
			return
		}
		doc, err := h.store.GetDocument(r.Context(), existing.ID)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "kb_failed", err.Error())
			return
		}
		h.startIngest(existing.ID, title)
		writeJSON(w, http.StatusAccepted, sanitizeDocument(doc))
	}
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
		// responses: a raw status code reads like our bug when it's the
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
