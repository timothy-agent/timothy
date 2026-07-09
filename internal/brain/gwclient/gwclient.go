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
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/sse"
)

// StreamRequest mirrors the gateway's /v1/stream request contract.
type StreamRequest struct {
	TaskCategory string             `json:"task_category"`
	ModelHint    string             `json:"model_hint,omitempty"`
	System       string             `json:"system,omitempty"`
	Messages     []provider.Message `json:"messages"`
	MaxTokens    int                `json:"max_tokens,omitempty"`
	SessionID    string             `json:"session_id,omitempty"`
}

// Client talks to one gateway base URL.
type Client struct {
	baseURL string
	http    *http.Client
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
