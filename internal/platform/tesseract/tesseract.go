// Package tesseract is the client for the OCR sidecar (ocr-svc/): one
// POST /recognize per image, raw bytes in, recognized text out.
// Compose-internal, no auth — the sidecar is only reachable inside the
// compose network.
package tesseract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// recognizeTimeout is far shorter than whisper's: tesseract on one page
// image is a sub-second job, so a call still running after this is
// wedged, not slow.
const recognizeTimeout = 30 * time.Second

// Recognize posts raw image bytes to the OCR sidecar and returns the
// recognized text. baseURL empty means the sidecar isn't configured —
// callers fall back to whatever they'd otherwise do. mediaType is the
// image's content type (e.g. "image/png"); empty lets the sidecar sniff
// the bytes itself.
func Recognize(ctx context.Context, client *http.Client, baseURL string, raw []byte, mediaType string) (string, error) {
	if baseURL == "" {
		return "", fmt.Errorf("ocr is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/recognize", bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", mediaType)

	c := client
	if c == nil {
		c = http.DefaultClient
	}
	cctx, cancel := context.WithTimeout(ctx, recognizeTimeout)
	defer cancel()
	req = req.WithContext(cctx)

	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("ocr request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("ocr returned http %d: %s", resp.StatusCode, snippet)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode ocr response: %w", err)
	}
	return out.Text, nil
}
