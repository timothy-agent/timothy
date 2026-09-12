package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestAttachmentResolverResolve table-tests the shared attachment
// conversion path (issue #359): pdf/text unchanged from the pre-#359
// behavior, image captioned, audio transcribed, and each of their
// rejection paths.
func TestAttachmentResolverResolve(t *testing.T) {
	t.Parallel()

	fa := &fakeMissionAttachments{
		byID: map[string]attachments.Attachment{
			"doc1": {ID: "doc1", Mime: "application/pdf"},
			"txt1": {ID: "txt1", Mime: "text/plain"},
			"img1": {ID: "img1", Mime: "image/png"},
			"aud1": {ID: "aud1", Mime: "audio/mpeg"},
			"vid1": {ID: "vid1", Mime: "video/mp4"},
		},
		data: map[string][]byte{
			"doc1": []byte("%PDF-1.4"),
			"txt1": []byte("plain text notes"),
			"img1": []byte("fake image bytes"),
			"aud1": []byte("fake audio bytes"),
		},
	}
	md := fakeMarkitdownServer(t, "# converted")
	wh := fakeWhisperServer(t, "hello world")
	captionOK := func(context.Context, string, []byte) string { return "a description" }
	captionEmpty := func(context.Context, string, []byte) string { return "" }

	tests := []struct {
		name       string
		resolver   *attachmentResolver
		in         []missionAttachmentInput
		wantErr    string
		wantStatus int
		wantMime   string
		wantMD     string
	}{
		{
			name:     "pdf converted via markitdown",
			resolver: &attachmentResolver{store: fa, markitdownURL: md.URL, markitdownHTTP: md.Client()},
			in:       []missionAttachmentInput{{ID: "doc1", Name: "spec.pdf"}},
			wantMime: "application/pdf", wantMD: "# converted",
		},
		{
			name:     "text passed through unchanged",
			resolver: &attachmentResolver{store: fa},
			in:       []missionAttachmentInput{{ID: "txt1", Name: "notes.txt"}},
			wantMime: "text/plain", wantMD: "plain text notes",
		},
		{
			name:     "image captioned",
			resolver: &attachmentResolver{store: fa, caption: captionOK},
			in:       []missionAttachmentInput{{ID: "img1", Name: "photo.png"}},
			wantMime: "image/png", wantMD: "a description",
		},
		{
			name:       "image with empty caption rejected",
			resolver:   &attachmentResolver{store: fa, caption: captionEmpty},
			in:         []missionAttachmentInput{{ID: "img1", Name: "photo.png"}},
			wantErr:    "could not be described",
			wantStatus: 400,
		},
		{
			name:       "image with no caption func rejected",
			resolver:   &attachmentResolver{store: fa},
			in:         []missionAttachmentInput{{ID: "img1", Name: "photo.png"}},
			wantErr:    "could not be described",
			wantStatus: 400,
		},
		{
			name:     "audio transcribed via whisper",
			resolver: &attachmentResolver{store: fa, whisperURL: wh.URL, whisperHTTP: wh.Client()},
			in:       []missionAttachmentInput{{ID: "aud1", Name: "note.mp3"}},
			wantMime: "audio/mpeg", wantMD: "hello world",
		},
		{
			name:       "audio with empty whisperURL rejected",
			resolver:   &attachmentResolver{store: fa},
			in:         []missionAttachmentInput{{ID: "aud1", Name: "note.mp3"}},
			wantErr:    "whisper sidecar",
			wantStatus: 400,
		},
		{
			name:       "unsupported mime rejected",
			resolver:   &attachmentResolver{store: fa, markitdownURL: md.URL},
			in:         []missionAttachmentInput{{ID: "vid1", Name: "clip.mp4"}},
			wantErr:    "unsupported attachment type",
			wantStatus: 400,
		},
		{
			name:       "cap exceeded",
			resolver:   &attachmentResolver{store: fa, markitdownURL: md.URL},
			in:         make([]missionAttachmentInput, maxMissionAttachments+1),
			wantErr:    "too many attachments",
			wantStatus: 400,
		},
		{
			name:       "attachments not enabled",
			resolver:   &attachmentResolver{},
			in:         []missionAttachmentInput{{ID: "doc1"}},
			wantErr:    "attachments are not enabled",
			wantStatus: 400,
		},
		{
			name:       "pdf without markitdown configured rejected",
			resolver:   &attachmentResolver{store: fa},
			in:         []missionAttachmentInput{{ID: "doc1"}},
			wantErr:    "markitdown sidecar",
			wantStatus: 400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tt.resolver.Resolve(context.Background(), tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Resolve() err = %v, want containing %q", err, tt.wantErr)
				}
				if status := attachmentErrorStatus(err); status != tt.wantStatus {
					t.Fatalf("attachmentErrorStatus = %d, want %d", status, tt.wantStatus)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() unexpected error: %v", err)
			}
			if len(out) != 1 {
				t.Fatalf("Resolve() = %d entries, want 1", len(out))
			}
			if out[0].Mime != tt.wantMime {
				t.Fatalf("Mime = %q, want %q", out[0].Mime, tt.wantMime)
			}
			if out[0].Markdown != tt.wantMD {
				t.Fatalf("Markdown = %q, want %q", out[0].Markdown, tt.wantMD)
			}
		})
	}
}

// TestAttachmentResolverResolveEmptyInput confirms the zero-ids fast
// path never touches the store.
func TestAttachmentResolverResolveEmptyInput(t *testing.T) {
	t.Parallel()
	r := &attachmentResolver{}
	out, err := r.Resolve(context.Background(), nil)
	if err != nil || out != nil {
		t.Fatalf("Resolve(nil) = %v, %v, want nil, nil", out, err)
	}
}

// TestAttachmentResolverResolvePDFEnrichment covers issue #350: a PDF
// attachment with r.enrich set runs the sidecar's /pdf/images
// extraction and captions embedded images into the converted markdown.
func TestAttachmentResolverResolvePDFEnrichment(t *testing.T) {
	t.Parallel()

	fa := &fakeMissionAttachments{
		byID: map[string]attachments.Attachment{"doc1": {ID: "doc1", Mime: "application/pdf"}},
		data: map[string][]byte{"doc1": []byte("%PDF-1.4")},
	}
	sidecar := fakePDFMarkitdownServer(t, "# converted", markitdown.PDFImagesResult{
		Pages: []markitdown.PDFPage{{Page: 1, Images: []markitdown.PDFImage{{MediaType: "image/png", DataB64: "AAAA"}}}},
	})
	enrich := &kb.Enricher{
		Caption: func(context.Context, string, []byte) string { return "a flowchart" },
		Enabled: func(context.Context) bool { return true },
		Log:     discardLog(),
	}
	r := &attachmentResolver{store: fa, markitdownURL: sidecar.URL, markitdownHTTP: sidecar.Client(), enrich: enrich}

	out, err := r.Resolve(context.Background(), []missionAttachmentInput{{ID: "doc1", Name: "spec.pdf"}})
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("Resolve() = %d entries, want 1", len(out))
	}
	if !strings.Contains(out[0].Markdown, "a flowchart") {
		t.Fatalf("Markdown = %q, want it to contain the embedded image's caption", out[0].Markdown)
	}
}

// TestAttachmentResolverResolvePDFEnrichmentSidecarFailureKeepsMarkdown
// confirms a /pdf/images call that errors (sidecar unreachable) never
// fails the attachment resolve: the already-converted markdown is kept
// as-is.
func TestAttachmentResolverResolvePDFEnrichmentSidecarFailureKeepsMarkdown(t *testing.T) {
	t.Parallel()

	fa := &fakeMissionAttachments{
		byID: map[string]attachments.Attachment{"doc1": {ID: "doc1", Mime: "application/pdf"}},
		data: map[string][]byte{"doc1": []byte("%PDF-1.4")},
	}
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/pdf/images" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"markdown": "# converted"})
	}))
	t.Cleanup(sidecar.Close)
	enrich := &kb.Enricher{
		Caption: func(context.Context, string, []byte) string { return "a flowchart" },
		Enabled: func(context.Context) bool { return true },
		Log:     discardLog(),
	}
	r := &attachmentResolver{store: fa, markitdownURL: sidecar.URL, markitdownHTTP: sidecar.Client(), enrich: enrich, log: discardLog()}

	out, err := r.Resolve(context.Background(), []missionAttachmentInput{{ID: "doc1", Name: "spec.pdf"}})
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if out[0].Markdown != "# converted" {
		t.Fatalf("Markdown = %q, want unchanged when /pdf/images fails", out[0].Markdown)
	}
}

// fakePDFMarkitdownServer serves /convert with markdown and /pdf/images
// with res, JSON-encoded, matching the sidecar's two-endpoint shape.
func fakePDFMarkitdownServer(t *testing.T, markdown string, res markitdown.PDFImagesResult) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/pdf/images" {
			_ = json.NewEncoder(w).Encode(res)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"markdown": markdown})
	}))
	t.Cleanup(srv.Close)
	return srv
}
