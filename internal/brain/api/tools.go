package api

import (
	"maps"
	"net/http"
	"slices"
)

// deferredToolLister is connectors.Manager.DeferredTools: each indexed
// MCP connector's namespaced load_tool mapped to the namespaced names
// of the tools behind it (issue #729).
type deferredToolLister interface {
	DeferredTools() map[string][]string
}

// registerTools mounts a read-only listing of the live tool surface
// (builtins + connector tools) — feeds the agent editor's tools
// allowlist picker so a name is chosen from what actually exists,
// never typed blind. Tools a deferred MCP connector hides behind its
// load_tool are appended too (issue #729): allowlisting one of them is
// how an agent gains that connector's entry point, so they must be
// pickable even though no turn offers them eagerly. nil toolset leaves
// the surface unmounted; nil deferred appends nothing.
func (a *API) registerTools(handle func(pattern string, h http.Handler), toolset Toolset, deferred deferredToolLister) {
	if toolset == nil {
		return
	}
	handle("GET /v1/admin/tools", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defs := toolset.Tools()
		out := make([]toolSummary, 0, len(defs))
		for _, d := range defs {
			out = append(out, toolSummary{Name: d.Name, Description: d.Description})
		}
		if deferred != nil {
			out = append(out, deferredSummaries(deferred.DeferredTools())...)
		}
		writeJSON(w, http.StatusOK, map[string]any{"tools": out})
	})))
}

// deferredSummaries flattens the deferred map into picker entries,
// sorted by entry point then tool name so the listing is stable.
func deferredSummaries(deferred map[string][]string) []toolSummary {
	var out []toolSummary
	for _, loadTool := range slices.Sorted(maps.Keys(deferred)) {
		for _, name := range slices.Sorted(slices.Values(deferred[loadTool])) {
			out = append(out, toolSummary{
				Name:        name,
				Description: "Deferred: chat loads this tool through " + loadTool + ", which is offered automatically once this tool is allowlisted.",
			})
		}
	}
	return out
}

type toolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
