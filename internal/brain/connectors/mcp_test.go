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

	"github.com/SumonMSelim/timothy/internal/brain/tools"
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
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	//nolint:gosec // G101: CredentialRef is a ref NAME, not a credential value.
	src, err := MCPBuilder(srv.Client())(t.Context(), Connector{
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
	builder := MCPBuilder(nil)
	resolve := func(context.Context, string) (string, error) { return "", nil }

	// Endpoint is required.
	if _, err := builder(t.Context(), Connector{Name: "x", Config: json.RawMessage(`{}`)}, resolve); err == nil {
		t.Fatal("missing endpoint accepted")
	}
	// A 401 at initialize fails the build (bad token → skipped, logged).
	f := &fakeMCP{token: "right"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	_, err := MCPBuilder(srv.Client())(t.Context(), Connector{
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

// TestManagerNamespacesMCPTools pins that only MCP sources still get
// the "<connector>_<tool>" prefix (external names can't be unified);
// a real *mcpSource is used since Manager's MCP exclusion in
// aggregateTools type-asserts against it specifically.
func TestManagerNamespacesMCPTools(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"github": &mcpSource{name: "github", toolList: []*tools.Tool{
			{Name: "create_issue"},
			{Name: "search code"}, // space sanitized
		}},
	}
	names := map[string]bool{}
	for _, tl := range m.Tools() {
		names[tl.Name] = true
	}
	if !names["github_create_issue"] || !names["github_search_code"] {
		t.Fatalf("namespaced tools = %v", names)
	}
}
