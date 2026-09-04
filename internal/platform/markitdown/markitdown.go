// Package markitdown is the client for the markitdown sidecar
// (markitdown-svc/): one POST /convert per file, raw bytes in,
// markdown out. Compose-internal, no auth — the sidecar is only
// reachable inside the compose network.
package markitdown

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const convertTimeout = 60 * time.Second

// maxMarkdownBytes caps a converted document's persisted markdown
// (128KB ≈ ~32k tokens): the markdown lands unbounded in the caller's
// context (a chat turn's message, a mission's prompt), and neither
// trims it later — a huge PDF would otherwise blow the model's context
// on the very turn/prompt that carries it.
const maxMarkdownBytes = 128 << 10

// truncatedMarker is appended when a converted document's markdown is
// cut at maxMarkdownBytes, so the model (and a user re-reading the
// transcript) knows the document wasn't fully captured rather than
// silently ending mid-sentence.
const truncatedMarker = "\n\n[document truncated: markdown exceeded 128KB]"

// TruncateMarkdown caps md at maxMarkdownBytes, cutting at a valid rune
// boundary (strings.ToValidUTF8 drops any partial rune left dangling
// by the byte-level slice) and appending truncatedMarker. A no-op when
// md is already within budget.
func TruncateMarkdown(md string) string {
	if len(md) <= maxMarkdownBytes {
		return md
	}
	return strings.ToValidUTF8(md[:maxMarkdownBytes], "") + truncatedMarker
}

// Convert posts raw file bytes to the markitdown sidecar and returns
// the converted markdown. baseURL empty means the sidecar isn't
// configured — callers fall back to whatever they'd otherwise do.
// filename and mimeType are hints for the converter; either may be
// empty when unknown.
func Convert(ctx context.Context, client *http.Client, baseURL, filename, mimeType string, raw []byte) (string, error) {
	if baseURL == "" {
		return "", fmt.Errorf("markitdown is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/convert", bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Filename", filename)
	req.Header.Set("X-Mimetype", mimeType)
	req.Header.Set("Content-Type", "application/octet-stream")

	c := client
	if c == nil {
		c = http.DefaultClient
	}
	cctx, cancel := context.WithTimeout(ctx, convertTimeout)
	defer cancel()
	req = req.WithContext(cctx)

	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("markitdown request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("markitdown returned http %d: %s", resp.StatusCode, snippet)
	}
	var out struct {
		Markdown string `json:"markdown"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode markitdown response: %w", err)
	}
	return out.Markdown, nil
}

// pdfImagesTimeout bounds one /pdf/images call: page rendering is
// slower than a plain /convert, but the same document already went
// through markitdown.Convert under convertTimeout, so this stays in
// the same ballpark.
const pdfImagesTimeout = 60 * time.Second

// PDFImage is one embedded raster image extracted from a PDF page
// (issue #350): Index is the PDF xref, unique per document, used only
// for logging/debugging, never persisted.
type PDFImage struct {
	Index     int    `json:"index"`
	MediaType string `json:"media_type"`
	DataB64   string `json:"data_b64"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// PDFPage is one page's extraction result: Images holds embedded
// raster images (empty when the page has none worth captioning),
// RenderB64 holds a rendered page PNG when TextChars fell under the
// sidecar's scanned-page threshold, nil otherwise.
type PDFPage struct {
	Page      int        `json:"page"`
	TextChars int        `json:"text_chars"`
	Images    []PDFImage `json:"images"`
	RenderB64 *string    `json:"render_b64"`
}

// PDFImagesResult is PDFImages' whole-document result.
type PDFImagesResult struct {
	Pages []PDFPage `json:"pages"`
}

// PDFImages posts a raw PDF to the sidecar's /pdf/images endpoint and
// returns, per page, its embedded images and (for a text-sparse,
// likely scanned page) a rendered page PNG (issue #350). baseURL empty
// means the sidecar isn't configured. A wholly unparsable PDF returns
// an error; per-page/per-image extraction failures are the sidecar's
// own concern and never surface here (a corrupt image just yields
// fewer images, not an error).
func PDFImages(ctx context.Context, client *http.Client, baseURL string, raw []byte) (PDFImagesResult, error) {
	if baseURL == "" {
		return PDFImagesResult{}, fmt.Errorf("markitdown is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/pdf/images", bytes.NewReader(raw))
	if err != nil {
		return PDFImagesResult{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	c := client
	if c == nil {
		c = http.DefaultClient
	}
	cctx, cancel := context.WithTimeout(ctx, pdfImagesTimeout)
	defer cancel()
	req = req.WithContext(cctx)

	resp, err := c.Do(req)
	if err != nil {
		return PDFImagesResult{}, fmt.Errorf("markitdown pdf/images request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return PDFImagesResult{}, fmt.Errorf("markitdown pdf/images returned http %d: %s", resp.StatusCode, snippet)
	}
	var out PDFImagesResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return PDFImagesResult{}, fmt.Errorf("decode markitdown pdf/images response: %w", err)
	}
	return out, nil
}
