package kb

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
)

// Captioner describes one image's bytes in plain prose; the empty
// string means captioning failed (network, gateway, or a rejected
// reply) and the caller must keep the original content unchanged.
// chat.CaptionImageOverGateway is the production implementation.
type Captioner func(ctx context.Context, mediaType string, data []byte) string

// captionMarker prefixes every caption block Enricher inserts: it
// makes a captioned link recognizable so a reingest of an
// already-enriched document doesn't re-fetch and re-caption the same
// image (issue #349's reingest AC only requires captioning apply, not
// that it re-run for free).
const captionMarker = "Image description:"

// captionBlock formats one caption as a blockquote directly below the
// image markdown it describes: the original link stays intact
// (provenance, and a caption failure degrades to unchanged text).
func captionBlock(caption string) string {
	return fmt.Sprintf("\n> %s %s\n", captionMarker, caption)
}

// Enrich bounds (issue #349): a document with more images than this
// skips the rest rather than spending unbounded gateway calls per
// ingest; maxImageBytes caps one fetched image so a malicious or
// oversized link can't stall ingest or blow memory.
const (
	maxCaptionedImages = 20
	maxImageBytes      = 5 << 20 // 5MiB
	captionConcurrency = 4
)

// imageLinkPattern matches Markdown image syntax `![alt](url)`,
// optionally followed by a title in quotes; capture groups are alt
// text and URL. HTML <img> tags are out of scope for v1 (markitdown's
// own output and hand-written/clip markdown both use the Markdown
// form).
var imageLinkPattern = regexp.MustCompile(`!\[([^\]]*)\]\((\S+?)(?:\s+"[^"]*")?\)`)

// imageRef is one image link found in markdown, with the byte range of
// its full match (used to detect an existing caption block right after
// it).
type imageRef struct {
	url      string
	matchEnd int
	lineEnd  int // index just past the link's line, where a caption block would sit
}

// extractImageLinks finds every http(s) Markdown image link in md,
// deduplicated by URL, in document order. data: URLs and non-http(s)
// schemes are skipped: fetch only ever dials a real network address.
func extractImageLinks(md string) []imageRef {
	seen := map[string]bool{}
	var out []imageRef
	for _, m := range imageLinkPattern.FindAllStringSubmatchIndex(md, -1) {
		url := md[m[4]:m[5]]
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			continue
		}
		if seen[url] {
			continue
		}
		seen[url] = true
		lineEnd := strings.IndexByte(md[m[1]:], '\n')
		if lineEnd < 0 {
			lineEnd = len(md)
		} else {
			lineEnd += m[1]
		}
		out = append(out, imageRef{url: url, matchEnd: m[1], lineEnd: lineEnd})
	}
	return out
}

// alreadyCaptioned reports whether the text right after an image
// link's line already carries a caption block, so re-running
// EnrichMarkdown (reingest) doesn't re-fetch and re-spend on an image
// it already captioned.
func alreadyCaptioned(md string, lineEnd int) bool {
	rest := strings.TrimLeft(md[lineEnd:], "\n")
	return strings.HasPrefix(rest, "> "+captionMarker)
}

// EnrichStats reports what one EnrichMarkdown call did, for the
// ingest log line and for deciding whether the enriched markdown needs
// persisting.
type EnrichStats struct {
	Found     int // image links seen
	Captioned int // captions newly inserted
	Skipped   int // already captioned, over the image cap, or non-image content
	Failed    int // fetch or caption failures (original link kept)
}

// allowedImageTypes are the content types Enricher will fetch and
// caption; anything else (including a non-image response) is skipped
// rather than sent to the vision model.
var allowedImageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true,
}

// Enricher captions image links found in a document's markdown at KB
// ingest time (issue #349). Enabled gates real gateway spend: nil
// Enabled or a false result makes EnrichMarkdown a no-op returning the
// input unchanged, which callers rely on to skip the UpdateMarkdown
// write entirely.
type Enricher struct {
	// Fetch dials image URLs; production wires this through netguard
	// (SSRF guard), same transport api/kb.go's fetchHTTP uses.
	Fetch *http.Client
	// Caption describes one image's bytes; "" on any failure.
	Caption Captioner
	// Enabled reports whether captioning may spend gateway tokens right
	// now (settings.Store.Enabled(ctx, settings.KeyKBImageCaptioning)).
	Enabled func(ctx context.Context) bool
	Log     *slog.Logger
}

// EnrichMarkdown appends a plain-prose caption block under each
// distinct http(s) image link in md, up to maxCaptionedImages. Any
// per-image failure (fetch error, oversized body, non-image content
// type, or a caption failure) leaves that link untouched and counts as
// Failed; the document as a whole always ingests. A link that already
// carries a caption block (idempotent reingest) is Skipped without a
// network call.
func (e *Enricher) EnrichMarkdown(ctx context.Context, md string) (string, EnrichStats) {
	var stats EnrichStats
	if e == nil || e.Enabled == nil || !e.Enabled(ctx) {
		return md, stats
	}
	refs := extractImageLinks(md)
	stats.Found = len(refs)
	if len(refs) == 0 {
		return md, stats
	}

	type result struct {
		ref     imageRef
		caption string
		ok      bool
	}
	toFetch := make([]imageRef, 0, len(refs))
	for _, ref := range refs {
		if alreadyCaptioned(md, ref.lineEnd) {
			stats.Skipped++
			continue
		}
		if len(toFetch) >= maxCaptionedImages {
			stats.Skipped++
			continue
		}
		toFetch = append(toFetch, ref)
	}
	if len(toFetch) == 0 {
		return md, stats
	}

	results := make([]result, len(toFetch))
	sem := make(chan struct{}, captionConcurrency)
	done := make(chan int, len(toFetch))
	for i, ref := range toFetch {
		sem <- struct{}{}
		go func(i int, ref imageRef) {
			defer func() { <-sem; done <- i }()
			caption, ok := e.captionOne(ctx, ref.url)
			results[i] = result{ref: ref, caption: caption, ok: ok}
		}(i, ref)
	}
	for range toFetch {
		<-done
	}

	// Insert from the end so earlier offsets stay valid as the string
	// grows.
	out := md
	for i := len(results) - 1; i >= 0; i-- {
		r := results[i]
		if !r.ok {
			stats.Failed++
			continue
		}
		out = out[:r.ref.lineEnd] + captionBlock(r.caption) + out[r.ref.lineEnd:]
		stats.Captioned++
	}
	return out, stats
}

// maxPDFPages/maxPDFImagesPerDoc bound one document's PDF enrichment
// spend the same way maxCaptionedImages bounds EnrichMarkdown: a
// document with more pages/images than this skips the rest rather than
// captioning unboundedly.
const (
	maxPDFPages        = 50
	maxPDFImagesPerDoc = 30
)

// EnrichPDF captions each embedded image and each rendered scanned
// page markitdown-svc's /pdf/images extracted (issue #350), appending
// a per-page section to md: "## Page N images" for embedded images,
// "## Page N (scanned)" for a page whose text layer was too sparse to
// trust. A page/image with no caption (fetch never needed here, only
// the vision call can fail) is silently skipped, never blocking the
// rest of the document.
func (e *Enricher) EnrichPDF(ctx context.Context, md string, res markitdown.PDFImagesResult) (string, EnrichStats) {
	var stats EnrichStats
	if e == nil || e.Enabled == nil || !e.Enabled(ctx) {
		return md, stats
	}

	var out strings.Builder
	out.WriteString(md)
	imagesCaptioned := 0
	for pageIdx, page := range res.Pages {
		if pageIdx >= maxPDFPages {
			break
		}
		var section strings.Builder
		for _, img := range page.Images {
			if imagesCaptioned >= maxPDFImagesPerDoc {
				stats.Skipped++
				continue
			}
			stats.Found++
			data, err := base64.StdEncoding.DecodeString(img.DataB64)
			if err != nil {
				stats.Failed++
				continue
			}
			caption := e.Caption(ctx, img.MediaType, data)
			if caption == "" {
				stats.Failed++
				continue
			}
			fmt.Fprintf(&section, "\n> %s %s\n", captionMarker, caption)
			imagesCaptioned++
			stats.Captioned++
		}
		if section.Len() > 0 {
			fmt.Fprintf(&out, "\n\n## Page %d images\n%s", page.Page, section.String())
		}

		if page.RenderB64 != nil {
			if imagesCaptioned >= maxPDFImagesPerDoc {
				stats.Skipped++
				continue
			}
			stats.Found++
			data, err := base64.StdEncoding.DecodeString(*page.RenderB64)
			if err != nil {
				stats.Failed++
				continue
			}
			caption := e.Caption(ctx, "image/png", data)
			if caption == "" {
				stats.Failed++
				continue
			}
			fmt.Fprintf(&out, "\n\n## Page %d (scanned)\n> %s %s\n", page.Page, captionMarker, caption)
			imagesCaptioned++
			stats.Captioned++
		}
	}
	if stats.Captioned == 0 {
		return md, stats
	}
	return out.String(), stats
}

// captionOne fetches and captions a single image URL, logging (not
// erroring) any failure: EnrichMarkdown's contract is "never fails the
// document".
func (e *Enricher) captionOne(ctx context.Context, url string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		e.Log.Warn("kb caption: bad image url", "url", url, "error", err)
		return "", false
	}
	req.Header.Set("Accept", "image/*")
	resp, err := e.Fetch.Do(req)
	if err != nil {
		e.Log.Warn("kb caption: fetch failed", "url", url, "error", err)
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		e.Log.Warn("kb caption: fetch non-200", "url", url, "status", resp.StatusCode)
		return "", false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		e.Log.Warn("kb caption: read failed", "url", url, "error", err)
		return "", false
	}
	if len(data) > maxImageBytes {
		e.Log.Warn("kb caption: image too large", "url", url, "limit_bytes", maxImageBytes)
		return "", false
	}
	mediaType := http.DetectContentType(data)
	if !allowedImageTypes[mediaType] {
		e.Log.Warn("kb caption: unsupported content type", "url", url, "content_type", mediaType)
		return "", false
	}
	caption := e.Caption(ctx, mediaType, data)
	if caption == "" {
		return "", false
	}
	return caption, true
}
