package kb

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func alwaysEnabled(context.Context) bool { return true }

// fakeCaptioner returns text for any input, or "" when told to fail.
func fakeCaptioner(text string, fail bool) Captioner {
	return func(context.Context, string, []byte) string {
		if fail {
			return ""
		}
		return text
	}
}

func TestExtractImageLinksDedupsAndSkipsNonHTTP(t *testing.T) {
	md := "![a diagram](https://example.com/a.png)\n\n" +
		"text ![again same](https://example.com/a.png \"title\") more\n" +
		"![data url](data:image/png;base64,AAAA)\n" +
		"![relative](./local.png)\n" +
		"![b](https://example.com/b.jpg)\n"
	refs := extractImageLinks(md)
	if len(refs) != 2 {
		t.Fatalf("refs = %d, want 2 (deduped, http(s) only), got %+v", len(refs), refs)
	}
	if refs[0].url != "https://example.com/a.png" || refs[1].url != "https://example.com/b.jpg" {
		t.Fatalf("refs = %+v, want a.png then b.jpg in document order", refs)
	}
}

func TestEnrichMarkdownDisabledIsNoop(t *testing.T) {
	e := &Enricher{Enabled: func(context.Context) bool { return false }, Log: discardLog()}
	md := "![x](https://example.com/a.png)"
	out, stats := e.EnrichMarkdown(context.Background(), md)
	if out != md {
		t.Fatalf("out = %q, want unchanged when disabled", out)
	}
	if stats != (EnrichStats{}) {
		t.Fatalf("stats = %+v, want zero value when disabled", stats)
	}
}

func TestEnrichMarkdownNilEnricherIsNoop(t *testing.T) {
	var e *Enricher
	md := "![x](https://example.com/a.png)"
	out, stats := e.EnrichMarkdown(context.Background(), md)
	if out != md || stats != (EnrichStats{}) {
		t.Fatalf("nil enricher must be a safe no-op, got out=%q stats=%+v", out, stats)
	}
}

func TestEnrichMarkdownCaptionsImageAndKeepsLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes())
	}))
	defer srv.Close()

	e := &Enricher{
		Fetch:   srv.Client(),
		Caption: fakeCaptioner("a bar chart of revenue", false),
		Enabled: alwaysEnabled,
		Log:     discardLog(),
	}
	md := "before\n![chart](" + srv.URL + "/a.png)\nafter"
	out, stats := e.EnrichMarkdown(context.Background(), md)
	if stats.Found != 1 || stats.Captioned != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want Found=1 Captioned=1 Failed=0", stats)
	}
	if !strings.Contains(out, srv.URL+"/a.png") {
		t.Fatalf("out = %q, want original link kept", out)
	}
	if !strings.Contains(out, captionMarker) || !strings.Contains(out, "a bar chart of revenue") {
		t.Fatalf("out = %q, want a caption block inserted", out)
	}
}

func TestEnrichMarkdownFetch404KeepsLinkAndCountsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	e := &Enricher{Fetch: srv.Client(), Caption: fakeCaptioner("x", false), Enabled: alwaysEnabled, Log: discardLog()}
	md := "![x](" + srv.URL + "/missing.png)"
	out, stats := e.EnrichMarkdown(context.Background(), md)
	if out != md {
		t.Fatalf("out = %q, want unchanged on fetch failure", out)
	}
	if stats.Failed != 1 || stats.Captioned != 0 {
		t.Fatalf("stats = %+v, want Failed=1 Captioned=0", stats)
	}
}

func TestEnrichMarkdownCaptionerFailureKeepsLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes())
	}))
	defer srv.Close()

	e := &Enricher{Fetch: srv.Client(), Caption: fakeCaptioner("", true), Enabled: alwaysEnabled, Log: discardLog()}
	md := "![x](" + srv.URL + "/a.png)"
	out, stats := e.EnrichMarkdown(context.Background(), md)
	if out != md {
		t.Fatalf("out = %q, want unchanged when the captioner fails", out)
	}
	if stats.Failed != 1 {
		t.Fatalf("stats = %+v, want Failed=1", stats)
	}
}

func TestEnrichMarkdownOversizedImageSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(make([]byte, maxImageBytes+1))
	}))
	defer srv.Close()

	e := &Enricher{Fetch: srv.Client(), Caption: fakeCaptioner("x", false), Enabled: alwaysEnabled, Log: discardLog()}
	md := "![x](" + srv.URL + "/big.png)"
	out, stats := e.EnrichMarkdown(context.Background(), md)
	if out != md || stats.Failed != 1 {
		t.Fatalf("out=%q stats=%+v, want unchanged with Failed=1 for an oversized image", out, stats)
	}
}

func TestEnrichMarkdownNonImageContentTypeSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html></html>"))
	}))
	defer srv.Close()

	e := &Enricher{Fetch: srv.Client(), Caption: fakeCaptioner("x", false), Enabled: alwaysEnabled, Log: discardLog()}
	md := "![x](" + srv.URL + "/notreally.png)"
	out, stats := e.EnrichMarkdown(context.Background(), md)
	if out != md || stats.Failed != 1 {
		t.Fatalf("out=%q stats=%+v, want unchanged with Failed=1 for a non-image response", out, stats)
	}
}

func TestEnrichMarkdownAlreadyCaptionedIsIdempotent(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes())
	}))
	defer srv.Close()

	e := &Enricher{Fetch: srv.Client(), Caption: fakeCaptioner("first pass", false), Enabled: alwaysEnabled, Log: discardLog()}
	md := "![x](" + srv.URL + "/a.png)"
	out1, stats1 := e.EnrichMarkdown(context.Background(), md)
	if stats1.Captioned != 1 || calls != 1 {
		t.Fatalf("first pass: stats=%+v calls=%d, want Captioned=1 calls=1", stats1, calls)
	}

	out2, stats2 := e.EnrichMarkdown(context.Background(), out1)
	if stats2.Skipped != 1 || stats2.Captioned != 0 {
		t.Fatalf("second pass: stats=%+v, want Skipped=1 Captioned=0 (already captioned)", stats2)
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d after reingest, want still 1 (no re-fetch)", calls)
	}
	if out2 != out1 {
		t.Fatalf("second pass changed the markdown, want byte-identical output on an already-captioned doc")
	}
}

func TestEnrichPDFDisabledIsNoop(t *testing.T) {
	e := &Enricher{Enabled: func(context.Context) bool { return false }, Log: discardLog()}
	res := markitdown.PDFImagesResult{Pages: []markitdown.PDFPage{{Page: 1, Images: []markitdown.PDFImage{{MediaType: "image/png", DataB64: "AAAA"}}}}}
	out, stats := e.EnrichPDF(context.Background(), "# doc", res)
	if out != "# doc" || stats != (EnrichStats{}) {
		t.Fatalf("out=%q stats=%+v, want unchanged/zero when disabled", out, stats)
	}
}

func TestEnrichPDFCaptionsEmbeddedImage(t *testing.T) {
	e := &Enricher{Caption: fakeCaptioner("a flowchart", false), Enabled: alwaysEnabled, Log: discardLog()}
	res := markitdown.PDFImagesResult{Pages: []markitdown.PDFPage{
		{Page: 1, Images: []markitdown.PDFImage{{MediaType: "image/png", DataB64: "AAAA"}}},
	}}
	out, stats := e.EnrichPDF(context.Background(), "# doc", res)
	if stats.Found != 1 || stats.Captioned != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want Found=1 Captioned=1", stats)
	}
	if !strings.Contains(out, "## Page 1 images") || !strings.Contains(out, "a flowchart") {
		t.Fatalf("out = %q, want a Page 1 images section with the caption", out)
	}
}

func TestEnrichPDFCaptionsScannedPage(t *testing.T) {
	renderB64 := "QkJC"
	e := &Enricher{Caption: fakeCaptioner("transcribed text", false), Enabled: alwaysEnabled, Log: discardLog()}
	res := markitdown.PDFImagesResult{Pages: []markitdown.PDFPage{
		{Page: 3, TextChars: 5, RenderB64: &renderB64},
	}}
	out, stats := e.EnrichPDF(context.Background(), "# doc", res)
	if stats.Found != 1 || stats.Captioned != 1 {
		t.Fatalf("stats = %+v, want Found=1 Captioned=1", stats)
	}
	if !strings.Contains(out, "## Page 3 (scanned)") || !strings.Contains(out, "transcribed text") {
		t.Fatalf("out = %q, want a Page 3 (scanned) section with the transcription", out)
	}
}

func TestEnrichPDFCaptionerFailureSkipsPage(t *testing.T) {
	e := &Enricher{Caption: fakeCaptioner("", true), Enabled: alwaysEnabled, Log: discardLog()}
	res := markitdown.PDFImagesResult{Pages: []markitdown.PDFPage{
		{Page: 1, Images: []markitdown.PDFImage{{MediaType: "image/png", DataB64: "AAAA"}}},
	}}
	out, stats := e.EnrichPDF(context.Background(), "# doc", res)
	if out != "# doc" {
		t.Fatalf("out = %q, want unchanged when the captioner fails", out)
	}
	if stats.Failed != 1 || stats.Captioned != 0 {
		t.Fatalf("stats = %+v, want Failed=1 Captioned=0", stats)
	}
}

func TestEnrichPDFNoImagesOrScannedPagesIsNoop(t *testing.T) {
	e := &Enricher{Caption: fakeCaptioner("x", false), Enabled: alwaysEnabled, Log: discardLog()}
	res := markitdown.PDFImagesResult{Pages: []markitdown.PDFPage{{Page: 1, TextChars: 5000}}}
	out, stats := e.EnrichPDF(context.Background(), "# doc", res)
	if out != "# doc" || stats.Captioned != 0 {
		t.Fatalf("out=%q stats=%+v, want unchanged with a text-rich page and no images", out, stats)
	}
}

// pngBytes returns a minimal valid PNG signature + IHDR-less body: only
// http.DetectContentType's sniff of the PNG magic bytes matters here,
// not a decodable image.
func pngBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
}
