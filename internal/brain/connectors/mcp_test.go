package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

// fakeMCP is a minimal streamable-HTTP MCP server: initialize,
// initialized notification, tools/list, tools/call. sseMode answers
// RPCs as one-event SSE streams instead of plain JSON.
type fakeMCP struct {
	token     string
	sseMode   bool
	callErr   bool
	gotAuth   string
	gotProto  string
	gotCalls  []string
	sessionID string
	// toolsJSON overrides the default two-tool list (the "tools" array
	// body only), for the deferred-index tests that need a server over
	// the threshold.
	toolsJSON string
}

func (f *fakeMCP) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.gotAuth = r.Header.Get("Authorization")
		if f.token != "" && f.gotAuth != "Bearer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     json.Number     `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		respond := func(result string) {
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
			if f.sseMode {
				w.Header().Set("Content-Type", "text/event-stream")
				// A notification frame first: the client must skip
				// non-response frames while scanning for its id.
				_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/ping\"}\n\n")
				_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}

		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			f.gotProto = p.ProtocolVersion
			f.sessionID = "sess-1"
			w.Header().Set("Mcp-Session-Id", f.sessionID)
			respond(`{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"fake"}}`)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if f.sessionID != "" && r.Header.Get("Mcp-Session-Id") != f.sessionID {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			// One line: SSE data frames must not contain raw newlines.
			if f.toolsJSON != "" {
				respond(`{"tools":` + f.toolsJSON + `}`)
				return
			}
			respond(`{"tools":[{"name":"create_issue","description":"Create a GitHub issue","inputSchema":{"type":"object","properties":{"title":{"type":"string"}}}},{"name":"search code","description":"Search"}]}`)
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			f.gotCalls = append(f.gotCalls, p.Name+" "+string(p.Arguments))
			if f.callErr {
				respond(`{"isError":true,"content":[{"type":"text","text":"boom: rate limited"}]}`)
				return
			}
			respond(`{"content":[{"type":"text","text":"issue "},{"type":"text","text":"#42 created"}]}`)
		default:
			respond(`{}`)
		}
	}
}

func buildMCP(t *testing.T, f *fakeMCP, token string) Source {
	t.Helper()
	return buildMCPWith(t, f, token, MCPDeferral{})
}

func buildMCPWith(t *testing.T, f *fakeMCP, token string, deferral MCPDeferral) Source {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	//nolint:gosec // G101: CredentialRef is a ref NAME, not a credential value.
	src, err := MCPBuilder(srv.Client(), deferral)(t.Context(), Connector{
		Name: "github", Kind: "mcp",
		Config:        json.RawMessage(`{"endpoint":"` + srv.URL + `"}`),
		CredentialRef: "GITHUB_MCP_TOKEN",
	}, func(context.Context, string) (string, error) { return token, nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return src
}

func TestMCPHandshakeAndToolList(t *testing.T) {
	t.Parallel()
	f := &fakeMCP{token: "tok-1"}
	src := buildMCP(t, f, "tok-1")

	if f.gotAuth != "Bearer tok-1" || f.gotProto != mcpProtocolVersion {
		t.Fatalf("handshake auth=%q proto=%q", f.gotAuth, f.gotProto)
	}
	list := src.Tools()
	if len(list) != 2 || list[0].Name != "create_issue" || list[0].Description == "" {
		t.Fatalf("tools = %+v", list)
	}
	// Missing schema gets the permissive object default.
	if string(list[1].InputSchema) != `{"type":"object"}` {
		t.Fatalf("default schema = %s", list[1].InputSchema)
	}
	if err := src.Test(t.Context()); err != nil {
		t.Fatalf("Test: %v", err)
	}
}

func TestMCPCallRoundTrip(t *testing.T) {
	t.Parallel()
	f := &fakeMCP{}
	src := buildMCP(t, f, "")

	out, err := src.Tools()[0].Execute(t.Context(), json.RawMessage(`{"title":"bug"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "issue #42 created" {
		t.Fatalf("Execute = %q", out)
	}
	if len(f.gotCalls) != 1 || f.gotCalls[0] != `create_issue {"title":"bug"}` {
		t.Fatalf("server saw %v", f.gotCalls)
	}
}

func TestMCPCallErrorSurfacesAsToolError(t *testing.T) {
	t.Parallel()
	f := &fakeMCP{callErr: true}
	src := buildMCP(t, f, "")

	_, err := src.Tools()[0].Execute(t.Context(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("Execute err = %v, want the isError content", err)
	}
}

func TestMCPSSEResponses(t *testing.T) {
	t.Parallel()
	f := &fakeMCP{sseMode: true}
	src := buildMCP(t, f, "")

	if got := len(src.Tools()); got != 2 {
		t.Fatalf("tools over sse = %d, want 2", got)
	}
	out, err := src.Tools()[0].Execute(t.Context(), nil)
	if err != nil || out != "issue #42 created" {
		t.Fatalf("Execute over sse = (%q, %v)", out, err)
	}
}

func TestMCPBuildFailures(t *testing.T) {
	t.Parallel()
	builder := MCPBuilder(nil, MCPDeferral{})
	resolve := func(context.Context, string) (string, error) { return "", nil }

	// Endpoint is required.
	if _, err := builder(t.Context(), Connector{Name: "x", Config: json.RawMessage(`{}`)}, resolve); err == nil {
		t.Fatal("missing endpoint accepted")
	}
	// A 401 at initialize fails the build (bad token → skipped, logged).
	f := &fakeMCP{token: "right"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	_, err := MCPBuilder(srv.Client(), MCPDeferral{})(t.Context(), Connector{
		Name: "x", Config: json.RawMessage(`{"endpoint":"` + srv.URL + `"}`),
	}, func(context.Context, string) (string, error) { return "wrong", nil })
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("bad token build = %v, want 401", err)
	}
}

// TestMCPStatusErrorNeverLeaksRawJSON pins the "status + short reason,
// no raw body" discipline shared with the Google/GitHub error mapping:
// a JSON error body's message/error field is used verbatim, and a
// non-JSON body is truncated rather than dumped whole.
func TestMCPStatusErrorNeverLeaksRawJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "message field",
			status: http.StatusForbidden,
			body:   `{"message":"insufficient scope","extra":{"deeply":{"nested":"junk"}}}`,
			want:   "status 403: insufficient scope",
		},
		{
			name:   "error field",
			status: http.StatusInternalServerError,
			body:   `{"error":"internal failure","trace":"huge stack trace blob"}`,
			want:   "status 500: internal failure",
		},
		{
			name:   "non-json body truncated",
			status: http.StatusBadGateway,
			body:   strings.Repeat("x", 500),
			want:   "status 502: " + strings.Repeat("x", 120) + "…",
		},
		{
			name:   "empty body",
			status: http.StatusServiceUnavailable,
			body:   "",
			want:   "status 503",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := &http.Response{
				StatusCode: tc.status,
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			err := mcpStatusError(resp)
			if err.Error() != tc.want {
				t.Fatalf("mcpStatusError = %q, want %q", err.Error(), tc.want)
			}
			if strings.Contains(err.Error(), "extra") || strings.Contains(err.Error(), "trace") {
				t.Fatalf("mcpStatusError leaked raw JSON fields: %q", err.Error())
			}
		})
	}
}

// TestManagerMergesLoneMCPToolUnderRawName pins the raw-name refactor's
// default case: a single MCP connector with no reserved-name collision
// and nothing else sharing its tools' raw names joins the unified
// surface exactly like any other source (see groupByRawName) — no
// prefix, "account" left off the schema since there's only one
// contributor.
func TestManagerMergesLoneMCPToolUnderRawName(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"github": &mcpSource{name: "github", toolList: []*tools.Tool{
			{Name: "create_issue", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}},
	}
	names := map[string]bool{}
	for _, tl := range m.Tools(nil) {
		names[tl.Name] = true
	}
	if !names["create_issue"] {
		t.Fatalf("tools = %v, want create_issue un-namespaced", names)
	}
	if names["github_create_issue"] {
		t.Fatalf("tools = %v, must not also appear namespaced", names)
	}
}

// TestManagerNamespacesMCPToolReservedByBuiltin pins Guard A: a raw
// name in the reserved set (the agent's non-connector tools) never
// merges, MCP included — an MCP tool sharing that name falls back to
// "<connector>_<tool>" (namespacedTools' sanitizer intact, hence the
// space in "search code") instead of shadowing the reserved tool.
func TestManagerNamespacesMCPToolReservedByBuiltin(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"github": &mcpSource{name: "github", toolList: []*tools.Tool{
			{Name: "create_issue", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "search code", InputSchema: json.RawMessage(`{"type":"object"}`)}, // space sanitized
		}},
	}
	names := map[string]bool{}
	for _, tl := range m.Tools(map[string]bool{"search code": true}) {
		names[tl.Name] = true
	}
	if !names["create_issue"] {
		t.Fatalf("tools = %v, want unreserved create_issue un-namespaced", names)
	}
	if !names["github_search_code"] {
		t.Fatalf("tools = %v, want the reserved name namespaced", names)
	}
	if names["search code"] {
		t.Fatalf("tools = %v, reserved raw name must never be served directly", names)
	}
}

// bigToolsJSON builds a tools/list body with n tools named tool_0..n-1.
func bigToolsJSON(n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":"tool_%d","description":"Does thing %d","inputSchema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"],"additionalProperties":false}}`, i, i)
	}
	b.WriteString("]")
	return b.String()
}

func indexDeferral(threshold int, loaded *[]*tools.Tool, sessions *[]string) MCPDeferral {
	return MCPDeferral{
		Threshold: func(context.Context) int { return threshold },
		OnLoad: func(sessionID string, t *tools.Tool) {
			*sessions = append(*sessions, sessionID)
			*loaded = append(*loaded, t)
		},
	}
}

// TestMCPDefersLargeToolSetBehindIndex pins issue #643's headline
// behavior: over the threshold, the turn sees one load_tool whose
// description carries every remote tool's name, not every schema.
func TestMCPDefersLargeToolSetBehindIndex(t *testing.T) {
	t.Parallel()
	f := &fakeMCP{toolsJSON: bigToolsJSON(9)}
	var loaded []*tools.Tool
	var sessions []string
	src := buildMCPWith(t, f, "", indexDeferral(8, &loaded, &sessions))

	list := src.Tools()
	if len(list) != 1 || list[0].Name != "load_tool" {
		t.Fatalf("tools = %+v, want a single load_tool", list)
	}
	index := src.(*mcpSource).IndexText()
	for i := range 9 {
		name := fmt.Sprintf("tool_%d", i)
		if !strings.Contains(index, "- "+name+": Does thing") {
			t.Fatalf("index missing %s:\n%s", name, index)
		}
		if !strings.Contains(list[0].Description, name) {
			t.Fatalf("load_tool description missing %s", name)
		}
	}
	// The index carries names and one-line summaries, never schemas.
	if strings.Contains(list[0].Description, "additionalProperties") {
		t.Fatalf("index leaked a schema:\n%s", list[0].Description)
	}
}

// TestMCPUnderThresholdStaysEager pins the unchanged case: a small
// server injects every schema exactly as before, and never grows a
// load_tool.
func TestMCPUnderThresholdStaysEager(t *testing.T) {
	t.Parallel()
	f := &fakeMCP{toolsJSON: bigToolsJSON(8)}
	var loaded []*tools.Tool
	var sessions []string
	src := buildMCPWith(t, f, "", indexDeferral(8, &loaded, &sessions))

	list := src.Tools()
	if len(list) != 8 {
		t.Fatalf("tools = %d, want all 8 eager", len(list))
	}
	for _, tl := range list {
		if tl.Name == "load_tool" {
			t.Fatalf("under-threshold server grew a load_tool")
		}
	}
}

// TestMCPThresholdZeroDisablesDeferral pins the off switch: 0 keeps a
// large server fully eager, and so does an unwired deferral.
func TestMCPThresholdZeroDisablesDeferral(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		deferral MCPDeferral
	}{
		{"threshold zero", MCPDeferral{Threshold: func(context.Context) int { return 0 }, OnLoad: func(string, *tools.Tool) {}}},
		{"not wired", MCPDeferral{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeMCP{toolsJSON: bigToolsJSON(20)}
			src := buildMCPWith(t, f, "", tc.deferral)
			if len(src.Tools()) != 20 {
				t.Fatalf("tools = %d, want 20 eager", len(src.Tools()))
			}
		})
	}
}

// TestMCPLoadToolRoundTrip proves the full path: load a tool by name,
// get it back under the namespaced name the eager path would have
// given it, then call it through the source's own RPC.
func TestMCPLoadToolRoundTrip(t *testing.T) {
	t.Parallel()
	f := &fakeMCP{toolsJSON: bigToolsJSON(9)}
	var loaded []*tools.Tool
	var sessions []string
	src := buildMCPWith(t, f, "", indexDeferral(8, &loaded, &sessions))

	load := src.Tools()[0]
	ctx := tools.WithSessionID(t.Context(), "sess-abc")
	out, err := load.Execute(ctx, json.RawMessage(`{"name":"tool_3"}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !strings.Contains(out, "github_tool_3") {
		t.Fatalf("load result = %q, want the namespaced name", out)
	}
	if len(loaded) != 1 || loaded[0].Name != "github_tool_3" {
		t.Fatalf("recorded = %+v, want github_tool_3", loaded)
	}
	if len(sessions) != 1 || sessions[0] != "sess-abc" {
		t.Fatalf("sessions = %v, want sess-abc", sessions)
	}
	// The recorded tool keeps the remote schema and actually calls.
	if !strings.Contains(string(loaded[0].InputSchema), `"q"`) {
		t.Fatalf("schema = %s", loaded[0].InputSchema)
	}
	res, err := loaded[0].Execute(t.Context(), json.RawMessage(`{"q":"x"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res != "issue #42 created" {
		t.Fatalf("call result = %q", res)
	}
	// The remote sees the RAW name, not the namespaced one.
	if len(f.gotCalls) != 1 || !strings.HasPrefix(f.gotCalls[0], "tool_3 ") {
		t.Fatalf("remote calls = %v", f.gotCalls)
	}
}

func TestMCPLoadToolRejectsUnknownAndSessionless(t *testing.T) {
	t.Parallel()
	f := &fakeMCP{toolsJSON: bigToolsJSON(9)}
	var loaded []*tools.Tool
	var sessions []string
	src := buildMCPWith(t, f, "", indexDeferral(8, &loaded, &sessions))
	load := src.Tools()[0]

	_, err := load.Execute(tools.WithSessionID(t.Context(), "s1"), json.RawMessage(`{"name":"nope"}`))
	if err == nil || !strings.Contains(err.Error(), "tool_0") {
		t.Fatalf("unknown name error = %v, want the available list", err)
	}
	if _, err := load.Execute(t.Context(), json.RawMessage(`{"name":"tool_1"}`)); err == nil {
		t.Fatalf("load with no session in context must fail")
	}
	if len(loaded) != 0 {
		t.Fatalf("nothing should have been recorded, got %+v", loaded)
	}
}

// TestMCPIndexTokenCost is the before/after measurement issue #643
// asks for: the tool-definition tokens a turn pays for a large MCP
// server, eager versus indexed.
func TestMCPIndexTokenCost(t *testing.T) {
	t.Parallel()
	f := &fakeMCP{toolsJSON: bigToolsJSON(30)}
	var loaded []*tools.Tool
	var sessions []string
	eager := buildMCPWith(t, f, "", MCPDeferral{})
	indexed := buildMCPWith(t, &fakeMCP{toolsJSON: bigToolsJSON(30)}, "", indexDeferral(8, &loaded, &sessions))

	before, err := session.EstimateToolTokens(toolDefs(eager.Tools()))
	if err != nil {
		t.Fatalf("estimate eager: %v", err)
	}
	after, err := session.EstimateToolTokens(toolDefs(indexed.Tools()))
	if err != nil {
		t.Fatalf("estimate indexed: %v", err)
	}
	t.Logf("tool-definition tokens for a 30-tool MCP server: eager=%d indexed=%d saved=%d (%.0f%%)",
		before, after, before-after, 100*float64(before-after)/float64(before))
	if after >= before {
		t.Fatalf("indexed surface (%d) must cost fewer tokens than eager (%d)", after, before)
	}
}

func toolDefs(ts []*tools.Tool) []provider.ToolDef {
	out := make([]provider.ToolDef, 0, len(ts))
	for _, t := range ts {
		out = append(out, provider.ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}
