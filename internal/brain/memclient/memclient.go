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
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/retrieval"
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

// Add stores a user-explicit memory (actor=user → active) and
// returns its id.
func (c *Client) Add(ctx context.Context, content, memoryType string) (string, error) {
	body, err := json.Marshal(map[string]string{"content": content, "type": memoryType})
	if err != nil {
		return "", fmt.Errorf("memclient: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/memories", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("memclient: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("memclient: memoryd unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("memclient: memoryd http %d: %s", resp.StatusCode, string(msg))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("memclient: decode: %w", err)
	}
	return out.ID, nil
}

// Memory is one retrieved long-term memory.
type Memory struct {
	ID      string  `json:"id"`
	Type    string  `json:"type"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// Retrieve asks memoryd what it remembers about a query. Zero
// memories is a normal answer.
func (c *Client) Retrieve(ctx context.Context, sessionID, query string) ([]Memory, error) {
	body, err := json.Marshal(map[string]any{"query": query, "session_id": sessionID})
	if err != nil {
		return nil, fmt.Errorf("memclient: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/retrieve", bytes.NewReader(body))
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
		Memories []Memory `json:"memories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("memclient: decode: %w", err)
	}
	return out.Memories, nil
}

// RenderBlock fences retrieved memories as tagged DATA for the system
// prompt tail. The preamble and the closing-tag escape are the
// memory-poisoning defense (D-011): whatever a memory's content says,
// it cannot close the fence or pose as instructions. Framing and
// escaping are single-sourced from the retrieval package — memoryd's
// token budget is enforced against exactly these strings.
func RenderBlock(memories []Memory) string {
	if len(memories) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(retrieval.BlockOpen)
	for _, m := range memories {
		b.WriteString(retrieval.RenderItem(m.Type, m.Content))
	}
	b.WriteString(retrieval.BlockClose)
	return b.String()
}
