package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/platform/netguard"
)

// Probe outcomes (D-152, issue #1166).
const (
	ProbeOK          = "ok"
	ProbeNeedsToken  = "needs_token"
	ProbeNeedsOAuth  = "needs_oauth"
	ProbeUnreachable = "unreachable"
	ProbeError       = "error"
)

const (
	// probeTimeout bounds the whole handshake; the operator is waiting
	// on a button.
	probeTimeout = 20 * time.Second
	// ProbeDescriptionMax caps one tool description, in runes.
	ProbeDescriptionMax = 300
	// ProbeToolCap caps the tools a probe returns; ToolCount stays the
	// server's full count.
	ProbeToolCap = 500
)

// ProbeServer is the server's initialize serverInfo.
type ProbeServer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ProbeTool is one tool as the add flow previews it. ReadOnlyHint is
// the server's own unverified annotation, nil when absent.
type ProbeTool struct {
	Name         string          `json:"name"`
	FinalName    string          `json:"final_name,omitempty"`
	Description  string          `json:"description"`
	ReadOnlyHint *bool           `json:"read_only_hint"`
	InputSchema  json.RawMessage `json:"input_schema"`
}

// ProbeResult is what POST /v1/admin/connectors/probe answers.
type ProbeResult struct {
	Status    string      `json:"status"`
	Server    ProbeServer `json:"server"`
	Tools     []ProbeTool `json:"tools"`
	ToolCount int         `json:"tool_count"`
	Message   string      `json:"message,omitempty"`
}

// statusRecorder keeps the last response's status and
// WWW-Authenticate, which rpc folds into a plain error string.
type statusRecorder struct {
	next          http.RoundTripper
	status        int
	authChallenge string
}

func (r *statusRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.status, r.authChallenge = 0, ""
	resp, err := r.next.RoundTrip(req)
	if resp != nil {
		r.status = resp.StatusCode
		r.authChallenge = resp.Header.Get("WWW-Authenticate")
	}
	return resp, err
}

// ProbeMCP runs initialize and tools/list against an unsaved MCP
// endpoint, once, with no retries. D-152: token is used for this
// probe only; it is never stored and is redacted from the returned
// message, since a server may echo it back in an error body.
func ProbeMCP(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, token string) ProbeResult {
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	rec := &statusRecorder{next: next}
	recClient := *client
	recClient.Transport = rec
	src := &mcpSource{name: "probe", cfg: mcpConfig{Endpoint: endpoint, Headers: headers}, token: token, client: &recClient}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	fail := func(err error) ProbeResult {
		msg := err.Error()
		if token != "" {
			msg = strings.ReplaceAll(msg, token, "[redacted]")
		}
		res := ProbeResult{Status: ProbeError, Tools: []ProbeTool{}, Message: msg}
		switch {
		case rec.status == http.StatusUnauthorized && strings.Contains(rec.authChallenge, "resource_metadata"):
			res.Status = ProbeNeedsOAuth
		case rec.status == http.StatusUnauthorized:
			res.Status = ProbeNeedsToken
		case rec.status == 0:
			res.Status = ProbeUnreachable
			if errors.Is(err, netguard.ErrBlocked) {
				res.Message += "; add the host to the outbound host allowlist in Settings to reach a private address"
			}
		}
		return res
	}

	var initRes struct {
		ServerInfo ProbeServer `json:"serverInfo"`
	}
	err := src.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "timothy", "version": "1"},
	}, &initRes)
	if err != nil {
		return fail(err)
	}
	if err := src.notify(ctx, "notifications/initialized"); err != nil {
		return fail(err)
	}
	var listRes struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
			Annotations struct {
				ReadOnlyHint *bool `json:"readOnlyHint"`
			} `json:"annotations"`
		} `json:"tools"`
	}
	if err := src.rpc(ctx, "tools/list", map[string]any{}, &listRes); err != nil {
		return fail(err)
	}

	res := ProbeResult{Status: ProbeOK, Server: initRes.ServerInfo, Tools: []ProbeTool{}}
	for _, t := range listRes.Tools {
		// Same reservation as connect: the connector would never serve it.
		if t.Name == LoadToolName {
			continue
		}
		res.ToolCount++
		if len(res.Tools) >= ProbeToolCap {
			continue
		}
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		res.Tools = append(res.Tools, ProbeTool{
			Name:         t.Name,
			Description:  truncateRunes(t.Description, ProbeDescriptionMax),
			ReadOnlyHint: t.Annotations.ReadOnlyHint,
			InputSchema:  schema,
		})
	}
	return res
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ProbeFinalNames predicts the allowlist name each probed tool would
// get if the connector were saved under name: the merge decision
// groupByRawName makes, over the live sources plus this one, or the
// always-namespaced form when the server's full tool count crosses
// threshold and the tools hide behind load_tool. surface is the
// agent's live tool names; the reserved set is recovered from it (see
// reservedFromSurface).
// Adding the connector can also split a same-named tool another MCP
// connector serves today; that rename is not reported here.
func (m *Manager) ProbeFinalNames(name string, res ProbeResult, surface []string, threshold int) map[string]string {
	probed := res.Tools
	out := make(map[string]string, len(probed))
	if threshold > 0 && res.ToolCount > threshold {
		for _, t := range probed {
			out[t.Name] = NamespacedName(name, t.Name)
		}
		return out
	}
	candidate := &mcpSource{name: name}
	for _, t := range probed {
		candidate.toolList = append(candidate.toolList, &tools.Tool{Name: t.Name, InputSchema: t.InputSchema})
	}

	m.mu.RLock()
	sources := make(map[string]Source, len(m.sources)+1)
	for k, v := range m.sources {
		sources[k] = v
	}
	reserved := reservedFromSurface(m.sources, surface)
	m.mu.RUnlock()

	sources[name] = candidate
	merged, _ := groupByRawName(sources, reserved)
	for _, t := range probed {
		out[t.Name] = NamespacedName(name, t.Name)
		for _, a := range merged[t.Name] {
			if a.connector == name {
				out[t.Name] = t.Name
				break
			}
		}
	}
	return out
}

// reservedFromSurface recovers the reserved set swapAgentTools passed
// to Tools: a surface name is reserved unless the connector merge
// produced it. A raw name some connector also serves is ambiguous; it
// was reserved exactly when its contributors were pushed to their
// namespaced form, which then sits on the surface too.
func reservedFromSurface(sources map[string]Source, surface []string) map[string]bool {
	on := make(map[string]bool, len(surface))
	for _, n := range surface {
		on[n] = true
	}
	merged, _ := groupByRawName(sources, nil)
	reserved := map[string]bool{}
	for _, n := range surface {
		accounts, isConnectorName := merged[n]
		if !isConnectorName {
			reserved[n] = true
			continue
		}
		for _, a := range accounts {
			if on[NamespacedName(a.connector, n)] {
				reserved[n] = true
				break
			}
		}
	}
	return reserved
}
