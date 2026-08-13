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
// ids. route overrides memoryd's own side-call route when non-empty —
// set when the turn/session being extracted executed a sensitive tool
// and must not fall back to the default side-call route.
func (c *Client) Extract(ctx context.Context, sessionID string, sourceSeq int64, text, route string) ([]string, error) {
	body, err := json.Marshal(map[string]any{
		"session_id": sessionID, "source_seq": sourceSeq, "text": text, "route": route,
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

// IngestDocument runs memoryd's synchronous chunk/embed/store pipeline
// for one already-converted document and returns the chunk count.
// Re-ingest is safe to call again: memoryd deletes the document's
// existing chunks before writing the new set.
func (c *Client) IngestDocument(ctx context.Context, documentID, title, markdown string) (int, error) {
	body, err := json.Marshal(map[string]string{"document_id": documentID, "title": title, "markdown": markdown})
	if err != nil {
		return 0, fmt.Errorf("memclient: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/ingest-document", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("memclient: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("memclient: memoryd unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("memclient: memoryd http %d: %s", resp.StatusCode, string(msg))
	}
	var out struct {
		ChunkCount int `json:"chunk_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("memclient: decode: %w", err)
	}
	return out.ChunkCount, nil
}

// KBChunkHit is one hybrid-retrieval result from the knowledge base.
type KBChunkHit struct {
	ChunkID       string  `json:"chunk_id"`
	DocumentID    string  `json:"document_id"`
	DocumentTitle string  `json:"document_title"`
	Collection    string  `json:"collection"`
	Breadcrumb    string  `json:"breadcrumb"`
	Content       string  `json:"content"`
	Score         float64 `json:"score"`
	SourceRef     string  `json:"source_ref"`
}

// KBSearch asks memoryd for the top-k chunks matching query, scoped to
// collectionNames (required, non-empty — this is the ONLY place a
// caller can widen or narrow that scope; the tool that calls this must
// bind names at construction, never take them from model input).
func (c *Client) KBSearch(ctx context.Context, query string, collectionNames []string, mode string, k int) ([]KBChunkHit, error) {
	body, err := json.Marshal(map[string]any{
		"query": query, "collection_names": collectionNames, "mode": mode, "k": k,
	})
	if err != nil {
		return nil, fmt.Errorf("memclient: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/kb-search", bytes.NewReader(body))
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
		Results []KBChunkHit `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("memclient: decode: %w", err)
	}
	return out.Results, nil
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
