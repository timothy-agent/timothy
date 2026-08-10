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
