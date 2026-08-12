package api

import (
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/skills"
)

// registerSkills mounts a read-only listing of the loaded skill packs —
// feeds the agent editor's skills allowlist picker so a name is chosen
// from what actually exists, never typed blind. Nil/empty packs leave
// the surface unmounted.
func (a *API) registerSkills(handle func(pattern string, h http.Handler), packs []skills.Skill) {
	if len(packs) == 0 {
		return
	}
	handle("GET /v1/admin/skills", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := make([]skillSummary, len(packs))
		for i, s := range packs {
			out[i] = skillSummary{Name: s.Name, Description: s.Description}
		}
		writeJSON(w, http.StatusOK, map[string]any{"skills": out})
	})))
}

type skillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
