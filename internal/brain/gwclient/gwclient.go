// Package gwclient is brain's client for the gateway's internal API.
// It relays the gateway's normalized SSE stream as typed events; all
// provider handling (retries, failover, accounting) already happened
// on the gateway side, so this client stays a thin reader.
package gwclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/sse"
)

// StreamRequest mirrors the gateway's /v1/stream request contract.
type StreamRequest struct {
	TaskCategory string             `json:"task_category"`
	Purpose      string             `json:"purpose,omitempty"` // ledger tag: why this call happened
	ModelHint    string             `json:"model_hint,omitempty"`
	System       string             `json:"system,omitempty"`
	Messages     []provider.Message `json:"messages"`
	MaxTokens    int                `json:"max_tokens,omitempty"`
	SessionID    string             `json:"session_id,omitempty"`
}

// windowsTTL matches the gateway's own config poll cadence: a fresher
// read couldn't observe anything newer.
const windowsTTL = 30 * time.Second

// Client talks to one gateway base URL.
type Client struct {
	baseURL string
	http    *http.Client

	mu         sync.Mutex
	windows    map[string]int
	windowsExp time.Time
}

func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{
		// No overall timeout (streams run long); bound the phases that
		// can hang before any byte arrives.
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}}
}

// ModelWindows fetches the gateway's provider listing and returns the
// context window per model id (entries without a window are omitted).
// The compactor uses it to size token budgets to the model in use.
// Results are memoized for the gateway's reload cadence; errors are
// never cached.
func (c *Client) ModelWindows(ctx context.Context) (map[string]int, error) {
	c.mu.Lock()
	if c.windows != nil && time.Now().Before(c.windowsExp) {
		w := c.windows
		c.mu.Unlock()
		return w, nil
	}
	c.mu.Unlock()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/providers", nil)
	if err != nil {
		return nil, fmt.Errorf("gwclient: request: %w", err)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gwclient: gateway unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("gwclient: gateway http %d: %s", resp.StatusCode, string(msg))
	}

	var listing struct {
		Providers []struct {
			Models []struct {
				ID            string `json:"id"`
				ContextWindow int    `json:"context_window"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return nil, fmt.Errorf("gwclient: decode providers: %w", err)
	}
	windows := make(map[string]int)
	for _, p := range listing.Providers {
		for _, m := range p.Models {
			if m.ContextWindow > 0 {
				windows[m.ID] = m.ContextWindow
			}
		}
	}
	c.mu.Lock()
	c.windows, c.windowsExp = windows, time.Now().Add(windowsTTL)
	c.mu.Unlock()
	return windows, nil
}

// Stream posts the request and yields the gateway's normalized events.
// The channel closes when the gateway stream ends; a transport failure
// mid-stream surfaces as a terminal error event.
func (c *Client) Stream(ctx context.Context, req StreamRequest) (<-chan stream.StreamEvent, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("gwclient: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/stream", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gwclient: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gwclient: gateway unreachable: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("gwclient: gateway http %d: %s", resp.StatusCode, string(msg))
	}

	ch := make(chan stream.StreamEvent)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()

		sawTerminal := false
		err := sse.Read(resp.Body, func(ev sse.Event) bool {
			var se stream.StreamEvent
			if err := json.Unmarshal([]byte(ev.Data), &se); err != nil {
				se = stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
					Code: "malformed_gateway_stream", Message: err.Error(),
				}}
			}
			if se.Type == stream.EventDone || se.Type == stream.EventError {
				sawTerminal = true
			}
			select {
			case ch <- se:
				return true
			case <-ctx.Done():
				return false
			}
		})
		// A read error AFTER the gateway's own terminal is just the
		// connection tearing down — emitting another terminal would
		// break the exactly-one-terminal contract.
		if err != nil && !sawTerminal && ctx.Err() == nil {
			select {
			case ch <- stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
				Code: "gateway_stream_cut", Message: err.Error(), Retryable: true,
			}}:
			case <-ctx.Done():
			}
		}
	}()
	return ch, nil
}
