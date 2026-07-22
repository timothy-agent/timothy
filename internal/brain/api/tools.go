package api

import "net/http"

// registerTools mounts a read-only listing of the live tool surface
// (builtins + connector tools) — feeds the agent editor's tools
// allowlist picker so a name is chosen from what actually exists,
// never typed blind. nil toolset leaves the surface unmounted.
func (a *API) registerTools(handle func(pattern string, h http.Handler), toolset Toolset) {
	if toolset == nil {
		return
	}
	handle("GET /v1/admin/tools", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defs := toolset.Tools()
		out := make([]toolSummary, len(defs))
		for i, d := range defs {
			out[i] = toolSummary{Name: d.Name, Description: d.Description}
		}
		writeJSON(w, http.StatusOK, map[string]any{"tools": out})
	})))
}

type toolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
