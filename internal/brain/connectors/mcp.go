package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/platform/sse"
)

// mcpProtocolVersion is the streamable-HTTP MCP revision this client
// speaks. Servers negotiate down; we accept whatever they answer.
const mcpProtocolVersion = "2025-06-18"

// mcpCallTimeout bounds one JSON-RPC round trip, including a tool
// call's remote work.
const mcpCallTimeout = 60 * time.Second

// mcpConfig is the connectors.config shape for kind='mcp'. Only
// streamable HTTP is supported: stdio would mean running arbitrary
// admin-configured subprocesses inside brain's container.
type mcpConfig struct {
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers"`
}

// MCPBuilder returns the Builder for kind='mcp'. The credential ref
// resolves to a bearer token; an empty or unresolvable ref builds
// without auth and lets the server's 401 surface at initialize.
func MCPBuilder(client *http.Client) Builder {
	if client == nil {
		client = &http.Client{}
	}
	return func(ctx context.Context, c Connector, resolve Resolve) (Source, error) {
		var cfg mcpConfig
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return nil, fmt.Errorf("mcp %s: config: %w", c.Name, err)
		}
		if cfg.Endpoint == "" {
			return nil, fmt.Errorf("mcp %s: config.endpoint is required", c.Name)
		}
		token := ""
		if c.CredentialRef != "" {
			if v, err := resolve(ctx, c.CredentialRef); err == nil {
				token = v
			}
		}
		src := &mcpSource{name: c.Name, cfg: cfg, token: token, client: client}
		if err := src.connect(ctx); err != nil {
			return nil, fmt.Errorf("mcp %s: %w", c.Name, err)
		}
		return src, nil
	}
}

// mcpSource is one connected MCP server: initialized session plus the
// tool list fetched at build time. Reloads rebuild sources, so the
// tool list is immutable for a source's lifetime.
type mcpSource struct {
	name   string
	cfg    mcpConfig
	token  string
	client *http.Client

	sessionID string // Mcp-Session-Id, captured at initialize
	nextID    atomic.Int64
	toolList  []*tools.Tool
}

// connect runs the MCP handshake and caches the tool list.
func (s *mcpSource) connect(ctx context.Context) error {
	var initRes struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	err := s.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "timothy", "version": "1"},
	}, &initRes)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if err := s.notify(ctx, "notifications/initialized"); err != nil {
		return fmt.Errorf("initialized notification: %w", err)
	}

	var listRes struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := s.rpc(ctx, "tools/list", map[string]any{}, &listRes); err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	for _, t := range listRes.Tools {
		remote := t.Name
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		s.toolList = append(s.toolList, &tools.Tool{
			Name:        remote,
			Description: t.Description,
			InputSchema: schema,
			Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
				return s.call(ctx, remote, args)
			},
		})
	}
	return nil
}

// Tools returns the server's tools, un-namespaced — the manager
// prefixes connector names when it aggregates.
func (s *mcpSource) Tools() []*tools.Tool { return s.toolList }

// Test re-lists tools: proves the session, auth, and endpoint are
// still good without side effects.
func (s *mcpSource) Test(ctx context.Context) error {
	var res json.RawMessage
	return s.rpc(ctx, "tools/list", map[string]any{}, &res)
}

// Close is a no-op: streamable HTTP holds no persistent connection.
// Kept on the interface for transports that will (stdio, websocket).
func (s *mcpSource) Close() error { return nil }

// call invokes one remote tool and flattens its content to text. An
// isError result comes back as a Go error so the loop reports it as
// tool feedback.
func (s *mcpSource) call(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var res struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	err := s.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args}, &res)
	if err != nil {
		return "", fmt.Errorf("mcp %s: %s: %w", s.name, tool, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	if res.IsError {
		return "", fmt.Errorf("mcp %s: %s: %s", s.name, tool, b.String())
	}
	return b.String(), nil
}

// jsonrpcEnvelope is the response shape for both plain-JSON and SSE
// replies.
type jsonrpcEnvelope struct {
	ID     json.Number     `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// rpc posts one JSON-RPC request and decodes the response, which a
// streamable-HTTP server may deliver as plain JSON or as an SSE
// stream carrying the response message.
func (s *mcpSource) rpc(ctx context.Context, method string, params, result any) error {
	id := s.nextID.Add(1)
	resp, err := s.post(ctx, map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("%s: status %d: %s", method, resp.StatusCode, snippet)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		s.sessionID = sid
	}

	var envelope jsonrpcEnvelope
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		found := false
		err := sse.Read(resp.Body, func(ev sse.Event) bool {
			var e jsonrpcEnvelope
			if json.Unmarshal([]byte(ev.Data), &e) != nil {
				return true // notification or noise; keep scanning
			}
			if e.ID.String() != fmt.Sprint(id) {
				return true
			}
			envelope, found = e, true
			return false
		})
		if err != nil && !found {
			return fmt.Errorf("%s: read sse: %w", method, err)
		}
		if !found {
			return fmt.Errorf("%s: stream ended without a response", method)
		}
	} else {
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return fmt.Errorf("%s: decode: %w", method, err)
		}
	}

	if envelope.Error != nil {
		return fmt.Errorf("%s: rpc error %d: %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("%s: result: %w", method, err)
		}
	}
	return nil
}

// notify posts a JSON-RPC notification (no id, no response body
// expected beyond the status).
func (s *mcpSource) notify(ctx context.Context, method string) error {
	resp, err := s.post(ctx, map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: status %d", method, resp.StatusCode)
	}
	return nil
}

func (s *mcpSource) post(ctx context.Context, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, s.cfg.Endpoint, bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	if s.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", s.sessionID)
	}
	for k, v := range s.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	// The cancel rides the body: rpc/notify close it promptly.
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelBody ties a request's timeout context to its body's lifetime.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelBody) Close() error {
	c.cancel()
	return c.ReadCloser.Close()
}
