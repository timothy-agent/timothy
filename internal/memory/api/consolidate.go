package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/memory/extract"
)

// ConsolidateRunner runs one consolidation pass; *extract.Consolidator
// satisfies it.
type ConsolidateRunner interface {
	Run(ctx context.Context) (extract.Summary, error)
}

// handleConsolidate triggers one consolidation pass synchronously and
// returns its Summary — the manual trigger the daily ticker lacked,
// letting a pass be inspected live rather than waiting up to 24h.
// The request body is ignored; there is nothing to configure.
func (a *API) handleConsolidate(w http.ResponseWriter, r *http.Request) {
	summary, err := a.consolidate.Run(r.Context())
	out := map[string]any{
		"merged":   summary.Merged,
		"rejected": summary.Rejected,
		"archived": summary.Archived,
		"decayed":  summary.Decayed,
		"demoted":  summary.Demoted,
	}
	if err != nil {
		a.log.Warn("consolidation pass failed", "error", err)
		out["errors"] = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
