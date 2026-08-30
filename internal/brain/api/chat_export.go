package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/pdfgen"
	pdfgenclient "github.com/SumonMSelim/timothy/internal/platform/pdfgen"
)

// handleExportMessagePDF serves POST /v1/chat/export-pdf: renders one
// already-rendered assistant message (title + markdown content, both
// supplied by the client — the message never left the transcript the
// client already holds) into a single-chapter PDF, no cover page or
// TOC. Returns the rendered attachment id; the client downloads it via
// GET /v1/attachments/{id}.
func (a *API) handleExportMessagePDF(w http.ResponseWriter, r *http.Request) {
	if a.pdfService == nil {
		jsonError(w, http.StatusServiceUnavailable, "not_enabled", "pdf generation is not enabled")
		return
	}
	var body struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON with a content field")
		return
	}
	if body.Content == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "content is empty")
		return
	}
	if len(body.Content) > maxExportMarkdownBytes {
		jsonError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("content exceeds %d bytes", maxExportMarkdownBytes))
		return
	}
	title := body.Title
	if title == "" {
		title = "Message"
	}

	docs := []pdfgenclient.Document{{Title: title, Content: body.Content}}
	result, err := a.pdfService.Render(r.Context(), docs, pdfgenclient.Options{})
	if err != nil {
		if errors.Is(err, pdfgen.ErrNotEnabled) {
			jsonError(w, http.StatusServiceUnavailable, "not_enabled", "pdf generation is not enabled")
			return
		}
		jsonError(w, http.StatusBadGateway, "export_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachment_id": result.AttachmentID, "cached": result.Cached})
}
