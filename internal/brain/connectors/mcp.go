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

// MCPDeferral is what an mcp source needs to defer tool schemas
// behind an index (issue #643): the threshold above which a server's
// tools stop being injected eagerly, and the sink a load_tool call
// reports the loaded tool to. A zero value (or a nil Threshold) keeps
// every connector eager, so tests and callers that don't wire it get
// today's behavior.
type MCPDeferral struct {
	// Threshold returns the tool count above which the index replaces
	// eager schemas; 0 disables deferral. Read per build, not cached,
	// so a settings change takes effect on the next connector reload.
	Threshold func(ctx context.Context) int
	// OnLoad records a tool the model loaded, under its final
	// namespaced name, for the rest of that session's turns. nil means
	// nothing records it, so deferral stays off.
	OnLoad func(sessionID string, t *tools.Tool)
}

// MCPBuilder returns the Builder for kind='mcp'. The credential ref
// resolves to a bearer token; an empty or unresolvable ref builds
// without auth and lets the server's 401 surface at initialize.
// deferral is optional: its zero value keeps every server's schemas
// eager.
func MCPBuilder(client *http.Client, deferral MCPDeferral) Builder {
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
		if deferral.Threshold != nil && deferral.OnLoad != nil {
			if n := deferral.Threshold(ctx); n > 0 && len(src.toolList) > n {
				src.indexed = true
				src.onLoad = deferral.OnLoad
			}
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

	// indexed defers this server's schemas behind load_tool (issue
	// #643): Tools() then returns that single entry point instead of
	// toolList, whose schemas the model pulls in one at a time. The
	// zero value is eager, today's behavior.
	indexed bool
	onLoad  func(sessionID string, t *tools.Tool)
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
// prefixes connector names when it aggregates. An indexed source
// returns exactly one synthetic load_tool whose description carries
// the index, so a large server costs one tool def per turn instead of
// one per remote tool; the manager namespaces it to
// "<connector>_load_tool", which is what we want, one entry point per
// connector.
func (s *mcpSource) Tools() []*tools.Tool {
	if !s.indexed {
		return s.toolList
	}
	return []*tools.Tool{s.loadTool()}
}

// IndexText renders the deferred tool index: one line per remote tool,
// name plus the first line of its description.
func (s *mcpSource) IndexText() string {
	var b strings.Builder
	for _, t := range s.toolList {
		summary := strings.TrimSpace(firstLine(t.Description))
		if len(summary) > mcpIndexSummaryMax {
			summary = summary[:mcpIndexSummaryMax] + "…"
		}
		if summary == "" {
			fmt.Fprintf(&b, "- %s\n", t.Name)
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", t.Name, summary)
	}
	return b.String()
}

// mcpIndexSummaryMax caps one index line's description so a server
// with verbose docs can't undo the saving the index exists for.
const mcpIndexSummaryMax = 160

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// loadTool is the deferred index's entry point: the model names a
// tool from the index, and the tool's full schema joins the session's
// surface from the next step on. The loaded tool carries the SAME
// namespaced name the eager path would have given it, so grants and
// the D-036 suffix rule behave identically; it takes no permission
// exemption from having been loaded this way.
func (s *mcpSource) loadTool() *tools.Tool {
	byName := make(map[string]*tools.Tool, len(s.toolList))
	names := make([]string, 0, len(s.toolList))
	for _, t := range s.toolList {
		byName[t.Name] = t
		names = append(names, t.Name)
	}
	return &tools.Tool{
		Name: "load_tool",
		Description: `Loads one of this connector's tools so you can call it.

The ` + s.name + ` connector serves too many tools to describe them all
up front, so their schemas are deferred. Pick one from the index below
by what you need to do, load it, then call it by its own name on your
next step.

Arguments:
- name (string, required): the tool's name exactly as listed below.

Returns the tool's description and argument schema; the tool stays
callable for the rest of this conversation. Loading a tool does not
grant permission to use it: the usual approval still applies when you
call it.

Tools available from ` + s.name + `:
` + s.IndexText(),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Tool name from the index in this description"
				}
			},
			"required": ["name"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			t, ok := byName[args.Name]
			if !ok {
				return "", fmt.Errorf("unknown tool %q on connector %s — available: %s", args.Name, s.name, strings.Join(names, ", "))
			}
			sessionID := tools.SessionIDFromContext(ctx)
			if sessionID == "" {
				return "", fmt.Errorf("load_tool: no session in context")
			}
			loaded := *t
			loaded.Name = NamespacedName(s.name, t.Name)
			s.onLoad(sessionID, &loaded)
			return fmt.Sprintf("Loaded %s. Call it as %q.\n\nDescription: %s\n\nInput schema: %s",
				t.Name, loaded.Name, t.Description, string(t.InputSchema)), nil
		},
	}
}

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
		return fmt.Errorf("%s: %w", method, mcpStatusError(resp))
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

// mcpErrorBody is the shape an MCP server's non-2xx body may carry;
// {"message"} and {"error"} both appear across servers in practice.
type mcpErrorBody struct {
	Message string `json:"message"`
	Error   string `json:"error"`
}

// mcpStatusError reports a non-2xx transport response: status code
// plus a short reason, never a raw JSON (or arbitrary HTML/text)
// body. A JSON error body's message/error field is used verbatim if
// present; otherwise a short truncated snippet stands in, since a
// server's error shape is not standardized like Google's or GitHub's.
func mcpStatusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e mcpErrorBody
	if json.Unmarshal(body, &e) == nil {
		if e.Message != "" {
			return fmt.Errorf("status %d: %s", resp.StatusCode, e.Message)
		}
		if e.Error != "" {
			return fmt.Errorf("status %d: %s", resp.StatusCode, e.Error)
		}
	}
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 120 {
		snippet = snippet[:120] + "…"
	}
	if snippet == "" {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return fmt.Errorf("status %d: %s", resp.StatusCode, snippet)
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
