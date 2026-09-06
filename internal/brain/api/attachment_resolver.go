package api

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
	"github.com/SumonMSelim/timothy/internal/platform/whisper"
)

// attachmentError is a resolver failure carrying the HTTP status the
// caller (mission create, schedule create/patch) should map it to via
// jsonError; every existing resolveAttachments message is preserved
// verbatim so callers/tests don't need to change.
type attachmentError struct {
	status int
	msg    string
}

func (e *attachmentError) Error() string { return e.msg }

func attachErr(status int, msg string) error {
	return &attachmentError{status: status, msg: msg}
}

// attachmentErrorStatus returns err's HTTP status if it's an
// *attachmentError, else 500 -- callers map a resolver error to
// jsonError with this and err.Error().
func attachmentErrorStatus(err error) int {
	if ae, ok := err.(*attachmentError); ok {
		return ae.status
	}
	return http.StatusInternalServerError
}

// attachmentResolver converts already-uploaded attachment refs into
// missions.SourceEntry values, shared by mission create and schedule
// create/patch (issue #359: images and audio, extending the original
// PDF/text-only path) so both surfaces convert attachments identically.
type attachmentResolver struct {
	store          missionAttachmentStore
	markitdownURL  string
	markitdownHTTP *http.Client
	whisperURL     string
	whisperHTTP    *http.Client
	// caption converts an image's bytes into a plain-prose description;
	// nil (no gateway wiring) makes every image attachment fail with the
	// same "could not be described" error an empty caption would.
	caption func(ctx context.Context, mediaType string, data []byte) string
}

// imageMimes/audioMimes are the mission-attachable subsets of
// attachments.Store's own allowlist (internal/brain/attachments/
// attachments.go) -- documents (pdf/text) are handled inline below.
var (
	imageMimes = map[string]bool{
		"image/png":  true,
		"image/jpeg": true,
		"image/webp": true,
		"image/gif":  true,
	}
	audioMimes = map[string]bool{
		"audio/mpeg": true,
		"audio/wav":  true,
		"audio/ogg":  true,
	}
)

// Resolve validates and converts in into SourceEntry values: pdf/text
// unchanged from the pre-#359 behavior, an image captioned once via
// caption (empty caption is rejected, never silently attached with no
// content), an audio clip transcribed once via the whisper sidecar.
// Empty input returns nil, nil without touching the store, same
// zero-ids fast path as the original resolveAttachments.
func (r *attachmentResolver) Resolve(ctx context.Context, in []missionAttachmentInput) ([]missions.SourceEntry, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if r.store == nil {
		return nil, attachErr(http.StatusBadRequest, "attachments are not enabled")
	}
	if len(in) > maxMissionAttachments {
		return nil, attachErr(http.StatusBadRequest, fmt.Sprintf("too many attachments (max %d)", maxMissionAttachments))
	}
	// Fetch and validate mime up front so the sidecar checks below only
	// fire when a conversion that needs one is actually requested.
	atts := make([]attachments.Attachment, len(in))
	needsMarkitdown := false
	needsWhisper := false
	for i, input := range in {
		att, err := r.store.Get(ctx, input.ID)
		if err != nil {
			return nil, attachErr(http.StatusBadRequest, fmt.Sprintf("attachment %q not found", input.ID))
		}
		switch {
		case att.Mime == "application/pdf", att.Mime == "text/plain":
			if att.Mime == "application/pdf" {
				needsMarkitdown = true
			}
		case imageMimes[att.Mime]:
			// captioned below, no sidecar precheck.
		case audioMimes[att.Mime]:
			needsWhisper = true
		default:
			return nil, attachErr(http.StatusBadRequest, "unsupported attachment type for missions")
		}
		atts[i] = att
	}
	if needsMarkitdown && r.markitdownURL == "" {
		return nil, attachErr(http.StatusBadRequest, "pdf attachments require the markitdown sidecar (MARKITDOWN_URL)")
	}
	if needsWhisper && r.whisperURL == "" {
		return nil, attachErr(http.StatusBadRequest, "audio attachments require the whisper sidecar (WHISPER_URL)")
	}
	out := make([]missions.SourceEntry, 0, len(in))
	for i, input := range in {
		att := atts[i]
		raw, err := r.readAttachment(ctx, att.ID)
		if err != nil {
			return nil, attachErr(http.StatusInternalServerError, err.Error())
		}
		var md string
		switch {
		case att.Mime == "application/pdf":
			md, err = markitdown.Convert(ctx, r.markitdownHTTP, r.markitdownURL, att.ID+".pdf", att.Mime, raw)
			if err != nil {
				return nil, attachErr(http.StatusInternalServerError, err.Error())
			}
			md = markitdown.TruncateMarkdown(md)
		case att.Mime == "text/plain":
			md = markitdown.TruncateMarkdown(string(raw))
		case imageMimes[att.Mime]:
			var caption string
			if r.caption != nil {
				caption = r.caption(ctx, att.Mime, raw)
			}
			if caption == "" {
				return nil, attachErr(http.StatusBadRequest, "image attachment could not be described (no vision route available)")
			}
			md = caption
		case audioMimes[att.Mime]:
			text, err := whisper.Transcribe(ctx, r.whisperHTTP, r.whisperURL, raw, "")
			if err != nil {
				return nil, attachErr(http.StatusInternalServerError, err.Error())
			}
			md = markitdown.TruncateMarkdown(text)
		}
		out = append(out, missions.SourceEntry{
			Source: missions.SourceKindPDF, ID: att.ID, Mime: att.Mime, Name: input.Name, Markdown: md,
		})
	}
	return out, nil
}

// readAttachment opens and fully reads one stored attachment's bytes.
func (r *attachmentResolver) readAttachment(ctx context.Context, id string) ([]byte, error) {
	rc, _, err := r.store.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}
