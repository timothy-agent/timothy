// Package gwclient is brain's client for the gateway's internal API.
// It relays the gateway's normalized SSE stream as typed events; all
// provider handling (retries, failover, accounting) already happened
// on the gateway side, so this client stays a thin reader.
package gwclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/sse"
)

// StreamRequest mirrors the gateway's /v1/stream request contract.
type StreamRequest struct {
	Route string `json:"route"`
	// Agent attributes the call in the cost ledger. ToolAllow is
	// loop-internal (never serialized): the serving agent's tool
	// allowlist, empty = all tools.
	Agent     string   `json:"agent,omitempty"`
	ToolAllow []string `json:"-"`
	// ExtraTools are turn-only tool defs layered on top of the shared
	// base set (loop.Request.ExtraTools) — chat's per-agent kb_search
	// binding is the only caller today. Loop-internal, never
	// serialized, same as ToolAllow.
	ExtraTools []*tools.Tool      `json:"-"`
	Purpose    string             `json:"purpose,omitempty"` // ledger tag: why this call happened
	ModelHint  string             `json:"model_hint,omitempty"`
	System     string             `json:"system,omitempty"`
	Messages   []provider.Message `json:"messages"`
	Tools      []provider.ToolDef `json:"tools,omitempty"`
	MaxTokens  int                `json:"max_tokens,omitempty"`
	Effort     string             `json:"effort,omitempty"` // D-020: "low" | "" (normal)
	SessionID  string             `json:"session_id,omitempty"`
	MissionID  string             `json:"mission_id,omitempty"` // ledger tag: the mission this turn serves
}

// windowsTTL matches the gateway's own config poll cadence: a fresher
// read couldn't observe anything newer.
const windowsTTL = 30 * time.Second

// resolveTTL memoizes ResolveRoute per route name — shorter than
// windowsTTL because missions dispatch on this result (D-051): a
// stale native-vs-delegated read is a worse failure mode than the
// harness case's occasional extra gateway hit.
const resolveTTL = 15 * time.Second

// Client talks to one gateway base URL.
type Client struct {
	baseURL string
	http    *http.Client

	mu         sync.Mutex
	windows    map[string]int
	windowsExp time.Time
	roles      map[string]string
	rolesExp   time.Time
	resolved   map[string]resolveCacheEntry
}

// resolveCacheEntry is one route's memoized ResolveRoute result.
type resolveCacheEntry struct {
	route *ResolvedRoute
	exp   time.Time
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

// RouteForRole returns the name of the route currently serving role
// ("default", "embedding", "vision", or "summarize"), or false if no
// route is bound to it yet. Results are memoized for the gateway's
// reload cadence, same as ModelWindows.
func (c *Client) RouteForRole(ctx context.Context, role string) (string, bool, error) {
	c.mu.Lock()
	if c.roles != nil && time.Now().Before(c.rolesExp) {
		roles := c.roles
		c.mu.Unlock()
		name, ok := roles[role]
		return name, ok, nil
	}
	c.mu.Unlock()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/routes/roles", nil)
	if err != nil {
		return "", false, fmt.Errorf("gwclient: request: %w", err)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", false, fmt.Errorf("gwclient: gateway unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", false, fmt.Errorf("gwclient: gateway http %d: %s", resp.StatusCode, string(msg))
	}
	var out struct {
		Roles map[string]string `json:"roles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false, fmt.Errorf("gwclient: decode roles: %w", err)
	}
	c.mu.Lock()
	c.roles, c.rolesExp = out.Roles, time.Now().Add(windowsTTL)
	c.mu.Unlock()
	name, ok := out.Roles[role]
	return name, ok, nil
}

// ResolvedRouteEntry is one chain entry as reported by the gateway's
// resolve endpoint (D-051 rework: harness is now the caller's ResolveRoute
// arg, not a per-entry field). CredentialRef is a NAME, never a
// resolved secret value — missions resolves it itself when spawning
// the executor.
type ResolvedRouteEntry struct {
	ProviderID    string `json:"provider_id"`
	ProviderName  string `json:"provider_name"`
	Driver        string `json:"driver"`
	Kind          string `json:"kind"`
	Model         string `json:"model"`
	CredentialRef string `json:"credential_ref"`
	BaseURL       string `json:"base_url"`
	Usable        bool   `json:"usable"`
	SkipReason    string `json:"skip_reason"`
	// Prices is the entry's configured per-Mtok prices, when the
	// provider has a price row for Model — nil when unpriced (D-013).
	Prices *router.ModelPrices `json:"prices,omitempty"`
	// Wire is the wire format this entry exposes on the executor axis —
	// "anthropic" or "openai" — set only for a kind='api' row on a
	// harness resolve. A dual-wire harness (pi) picks its provider
	// config off this; empty for a kind='cli' row or the chat axis.
	Wire string `json:"wire,omitempty"`
}

// ResolvedRoute is a route's ordered chain, each entry annotated with
// the gate appropriate to the axis ResolveRoute was called with — the
// chat gate for harness == "", the executor rule otherwise.
type ResolvedRoute struct {
	Route   string               `json:"route"`
	Entries []ResolvedRouteEntry `json:"entries"`
}

// ResolveRoute fetches route's ordered chain with provider metadata so
// missions can dispatch native-vs-delegated and spawn a CLI executor
// (D-051): harness == "" resolves the chat-serving axis, a known
// harness name resolves the executor axis for every entry. Memoized per
// route+harness for resolveTTL, shorter than ModelWindows/RouteForRole's
// TTL since missions dispatch directly on this result.
func (c *Client) ResolveRoute(ctx context.Context, name, harness string) (*ResolvedRoute, error) {
	cacheKey := name + "\x00" + harness
	c.mu.Lock()
	if entry, ok := c.resolved[cacheKey]; ok && time.Now().Before(entry.exp) {
		c.mu.Unlock()
		return entry.route, nil
	}
	c.mu.Unlock()

	reqURL := c.baseURL + "/v1/routes/" + url.PathEscape(name) + "/resolve"
	if harness != "" {
		reqURL += "?harness=" + url.QueryEscape(harness)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
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
	var out ResolvedRoute
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("gwclient: decode resolve: %w", err)
	}

	c.mu.Lock()
	if c.resolved == nil {
		c.resolved = map[string]resolveCacheEntry{}
	}
	c.resolved[cacheKey] = resolveCacheEntry{route: &out, exp: time.Now().Add(resolveTTL)}
	c.mu.Unlock()
	return &out, nil
}

// SecretRef mirrors the gateway admin's SecretRef: a stored secret's
// directory metadata plus the provider names that reference it. Never
// a value.
type SecretRef struct {
	RefName      string    `json:"ref_name"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	ReferencedBy []string  `json:"referenced_by_providers"`
}

// ListSecrets fetches the gateway's secrets directory (names,
// timestamps, provider referents) — never a value. Brain's own
// /v1/admin/secrets handler merges connector referents into this
// before serving it to the UI.
func (c *Client) ListSecrets(ctx context.Context) ([]SecretRef, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/internal/admin/secrets", nil)
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
	var out struct {
		Secrets []SecretRef `json:"secrets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("gwclient: decode secrets: %w", err)
	}
	return out.Secrets, nil
}

// DeleteSecret proxies the delete to the gateway, which refuses (in
// use) while any provider still names refName as its credential_ref.
// Brain's own handler checks connector references first so a
// connector-only refusal never round-trips to the gateway at all. On
// failure it returns the gateway's own status code so the caller can
// preserve a 409 (in use) rather than flattening every failure to 500.
func (c *Client) DeleteSecret(ctx context.Context, refName string) (status int, err error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/internal/admin/secrets/"+url.PathEscape(refName), nil)
	if err != nil {
		return 0, fmt.Errorf("gwclient: request: %w", err)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("gwclient: gateway unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		var body struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(raw, &body); err == nil && body.Message != "" {
			return resp.StatusCode, errors.New(body.Message)
		}
		return resp.StatusCode, fmt.Errorf("gwclient: gateway http %d: %s", resp.StatusCode, string(raw))
	}
	return http.StatusNoContent, nil
}

// Embed returns one vector per input text via the gateway's
// embedding route, plus the model that produced them (the gateway
// reports the resolved provider model). purpose tags the ledger row.
func (c *Client) Embed(ctx context.Context, texts []string, purpose string) ([][]float32, string, error) {
	body, err := json.Marshal(map[string]any{"texts": texts, "purpose": purpose})
	if err != nil {
		return nil, "", fmt.Errorf("gwclient: marshal embed: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/embed", bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("gwclient: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("gwclient: gateway unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, "", fmt.Errorf("gwclient: gateway http %d: %s", resp.StatusCode, string(msg))
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
		Model      string      `json:"model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", fmt.Errorf("gwclient: decode embed: %w", err)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, "", fmt.Errorf("gwclient: embed returned %d vectors for %d texts", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, out.Model, nil
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
		// break the exactly-one-terminal contract. ctx already being
		// done does NOT skip this: turnCtx's own deadline racing gateway's
		// terminal write must still surface as a real failure (D-044)
		// rather than silently dropping it. Delivery deliberately does
		// NOT race ctx.Done(): with both select cases ready (deadline
		// fired, consumer mid-receive) Go picks randomly, and that coin
		// flip has eaten the only evidence of a real ~30min turn. Every
		// consumer of this channel drains until close, so the send
		// succeeds immediately in practice; the timeout only bounds a
		// hypothetically abandoned consumer.
		if err != nil && !sawTerminal {
			select {
			case ch <- stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
				Code: "gateway_stream_cut", Message: err.Error(), Retryable: true,
			}}:
			case <-time.After(5 * time.Second):
			}
		}
	}()
	return ch, nil
}
