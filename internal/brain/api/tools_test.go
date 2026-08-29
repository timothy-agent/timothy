package api

import (
	"encoding/json"
	"net/http/httptest"
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
	}})

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
	a.registerTools(m.Handle, stubToolset{})

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
	a.registerTools(m.Handle, nil)

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
