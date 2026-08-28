package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
)

// registerAttachments mounts the attachment upload/download surface.
// Nil-gated: store is nil when ATTACHMENTS_DIR is unset, leaving the
// surface unmounted (404s), same pattern as missions/schedules/agents.
func (a *API) registerAttachments(handle func(pattern string, h http.Handler), store *attachments.Store) {
	if store == nil {
		return
	}
	h := &attachmentAPI{store: store}
	handle("POST /v1/attachments", a.auth(http.HandlerFunc(h.upload)))
	handle("GET /v1/attachments/{id}", a.auth(http.HandlerFunc(h.download)))
}

type attachmentAPI struct {
	store *attachments.Store
}

func failAttachment(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, attachments.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", "attachment not found")
	case errors.Is(err, attachments.ErrUnsupportedMIME):
		jsonError(w, http.StatusBadRequest, "unsupported_mime", err.Error())
	case errors.Is(err, attachments.ErrTooLarge):
		jsonError(w, http.StatusBadRequest, "too_large", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

type attachmentView struct {
	ID        string `json:"id"`
	Mime      string `json:"mime"`
	SizeBytes int64  `json:"size_bytes"`
}

// upload accepts a single multipart file field "file", caps its size
// at attachments.MaxSizeBytes before any bytes reach the store, and
// hands the body straight to Store.Save — the store itself sniffs the
// real MIME type rather than trusting the multipart part's header.
func (h *attachmentAPI) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, attachments.MaxSizeBytes)
	if err := r.ParseMultipartForm(attachments.MaxSizeBytes); err != nil { //nolint:gosec // G120: r.Body is already MaxBytesReader-capped above at the same limit
		jsonError(w, http.StatusBadRequest, "too_large", "file exceeds the maximum upload size")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "multipart field \"file\" is required")
		return
	}
	defer func() { _ = file.Close() }()

	att, err := h.store.Save(r.Context(), file)
	if err != nil {
		failAttachment(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attachmentView{ID: att.ID, Mime: att.Mime, SizeBytes: att.SizeBytes})
}

// download serves the raw bytes with Content-Type forced from the DB
// row (never sniffed at serve time — the row is the sole source of
// truth once Save already sniffed it once) and an immutable
// Cache-Control, since the id is the content hash.
func (h *attachmentAPI) download(w http.ResponseWriter, r *http.Request) {
	f, att, err := h.store.Open(r.Context(), r.PathValue("id"))
	if err != nil {
		failAttachment(w, err)
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", att.Mime)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}
