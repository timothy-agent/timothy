package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

type stubToolset struct{ defs []provider.ToolDef }

func (s stubToolset) Tools() []provider.ToolDef { return s.defs }

func TestToolsEndpointListsLiveSurface(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerTools(m.Handle, stubToolset{defs: []provider.ToolDef{
		{Name: "search_web", Description: "Search the web", InputSchema: []byte(`{}`)},
		{Name: "github_create_issue", Description: "Create a GitHub issue", InputSchema: []byte(`{}`)},
	}}, nil)

	req := httptest.NewRequest("GET", "/v1/admin/tools", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := decodeToolsBody(t, w.Body.Bytes())
	if len(body.Tools) != 2 || body.Tools[0].Name != "search_web" || body.Tools[1].Name != "github_create_issue" {
		t.Fatalf("tools = %+v", body.Tools)
	}
	// Only name/description are exposed — never the input schema
	// (an implementation detail an allowlist picker doesn't need).
	if body.Tools[0].Description != "Search the web" {
		t.Fatalf("description = %q, want Search the web", body.Tools[0].Description)
	}
}

func TestToolsEndpointRequiresAuth(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerTools(m.Handle, stubToolset{}, nil)

	req := httptest.NewRequest("GET", "/v1/admin/tools", nil)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Fatalf("unauthenticated status = %d, want 401", w.Code)
	}
}

func TestToolsEndpointUnmountedWhenToolsetNil(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerTools(m.Handle, nil, nil)

	req := httptest.NewRequest("GET", "/v1/admin/tools", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code == 200 {
		t.Fatal("nil toolset must leave /v1/admin/tools unmounted, got 200")
	}
}

type toolsBody struct {
	Tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"tools"`
}

func decodeToolsBody(t *testing.T, raw []byte) toolsBody {
	t.Helper()
	var body toolsBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

type stubDeferred map[string][]string

func (s stubDeferred) DeferredTools() map[string][]string { return s }

// Tools a deferred MCP connector hides behind load_tool join the
// listing after the live surface (issue #729), in a stable order, so
// the picker can offer them even though no turn injects them eagerly.
func TestToolsEndpointAppendsDeferredTools(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerTools(m.Handle, stubToolset{defs: []provider.ToolDef{
		{Name: "jira_load_tool", Description: "Loads one of this connector's tools", InputSchema: []byte(`{}`)},
	}}, stubDeferred{"jira_load_tool": {"jira_search_issues", "jira_get_issue"}})

	req := httptest.NewRequest("GET", "/v1/admin/tools", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := decodeToolsBody(t, w.Body.Bytes())
	if len(body.Tools) != 3 || body.Tools[0].Name != "jira_load_tool" || body.Tools[1].Name != "jira_get_issue" || body.Tools[2].Name != "jira_search_issues" {
		t.Fatalf("tools = %+v", body.Tools)
	}
	if !strings.Contains(body.Tools[1].Description, "jira_load_tool") {
		t.Fatalf("deferred description must name the entry point: %q", body.Tools[1].Description)
	}
}
