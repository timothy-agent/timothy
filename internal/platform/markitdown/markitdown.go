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
	"time"
)

const convertTimeout = 60 * time.Second

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
