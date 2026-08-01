// Package whisper is the client for the whisper sidecar
// (whisper-svc/): one POST /transcribe per audio clip, raw bytes in,
// transcript text out. Compose-internal, no auth — the sidecar is
// only reachable inside the compose network.
package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// transcribeTimeout is generous: CPU transcription of a minute of
// audio can take a while on the "small" model.
const transcribeTimeout = 120 * time.Second

// Transcribe posts raw audio bytes to the whisper sidecar and returns
// the transcribed text. baseURL empty means the sidecar isn't
// configured — callers fall back to whatever they'd otherwise do.
func Transcribe(ctx context.Context, client *http.Client, baseURL string, raw []byte) (string, error) {
	if baseURL == "" {
		return "", fmt.Errorf("whisper is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/transcribe", bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	c := client
	if c == nil {
		c = http.DefaultClient
	}
	cctx, cancel := context.WithTimeout(ctx, transcribeTimeout)
	defer cancel()
	req = req.WithContext(cctx)

	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("whisper returned http %d: %s", resp.StatusCode, snippet)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode whisper response: %w", err)
	}
	return out.Text, nil
}
