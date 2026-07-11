// Package memclient is brain's client for memoryd's internal API.
// Extraction is best-effort by contract: callers on the turn path
// invoke it from a goroutine and only log failures; the pre-compaction
// caller waits for the ids but proceeds empty-handed on error.
package memclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const requestTimeout = 60 * time.Second

// Client talks to one memoryd base URL.
type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: requestTimeout}}
}

// Extract posts one extraction job and returns the inserted memory
// ids.
func (c *Client) Extract(ctx context.Context, sessionID string, sourceSeq int64, text string) ([]string, error) {
	body, err := json.Marshal(map[string]any{
		"session_id": sessionID, "source_seq": sourceSeq, "text": text,
	})
	if err != nil {
		return nil, fmt.Errorf("memclient: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/extract", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("memclient: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("memclient: memoryd unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("memclient: memoryd http %d: %s", resp.StatusCode, string(msg))
	}
	var out struct {
		MemoryIDs []string `json:"memory_ids"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("memclient: decode: %w", err)
	}
	return out.MemoryIDs, nil
}
