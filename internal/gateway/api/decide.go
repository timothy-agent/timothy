package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/decide"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// decideRequest is the POST /v1/decide body (D-163). Route is
// optional: empty resolves the decide role.
type decideRequest struct {
	Route     string   `json:"route,omitempty"`
	Kind      string   `json:"kind"`
	Question  string   `json:"question"`
	Options   []string `json:"options,omitempty"`
	Purpose   string   `json:"purpose,omitempty"` // ledger tag; defaults to "decide"
	SessionID string   `json:"session_id,omitempty"`
	MissionID string   `json:"mission_id,omitempty"`
}

// decideResponse is the typed answer plus the entry that served it.
type decideResponse struct {
	provider.DecideResult
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Native   bool          `json:"native"`
	LedgerID string        `json:"ledger_id"`
	Usage    *stream.Usage `json:"usage,omitempty"`
}

// handleDecide answers one fixed-choice question on a decide route.
// Each chain entry is tried in order: a native decision model through
// provider.Decider, any other entry through the llm fallback at low
// effort. A provider error or a malformed reply advances the chain;
// the reply is never guessed. Every attempt books one ledger row.
func (a *API) handleDecide(w http.ResponseWriter, r *http.Request) {
	var req decideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	dreq := provider.DecideRequest{Kind: req.Kind, Question: req.Question, Options: req.Options}
	if err := decide.Validate(&dreq); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Purpose == "" {
		req.Purpose = "decide"
	}

	snap := a.store.Snapshot()
	if snap == nil {
		jsonError(w, http.StatusServiceUnavailable, "config_unavailable", "routing configuration not loaded yet")
		return
	}
	route, attempts, err := resolveDecide(snap, req.Route)
	if err != nil {
		var nre *router.NoRouteError
		if errors.As(err, &nre) {
			jsonError(w, http.StatusBadGateway, "no_route", nre.Error())
			return
		}
		jsonError(w, http.StatusInternalServerError, "resolve_failed", err.Error())
		return
	}

	var failed []string
	for i, att := range attempts {
		local := false
		if row, ok := snap.ProviderRow(att.ProviderName); ok {
			local = isLocalBaseURL(row.BaseURL)
		}
		entry := ledger.Entry{
			ID:       ledger.NewID(),
			Provider: att.ProviderName, Model: att.Model,
			Route: route, Purpose: req.Purpose,
			SessionID: req.SessionID, MissionID: req.MissionID, Local: local,
		}
		start := time.Now()
		res, usage, effort, err := decideAttempt(r.Context(), att, dreq, i == len(attempts)-1)
		entry.LatencyMS = time.Since(start).Milliseconds()
		entry.Usage, entry.Effort = usage, effort
		prices := snap.Prices(att.ProviderName, att.Model)
		entry.Cost = ledger.Cost(prices, usage)
		if prices != nil {
			entry.Currency = prices.Currency
		}
		// D-135: the caller hung up; book it and stop, no provider is
		// asked for an answer nobody reads.
		if err != nil && r.Context().Err() != nil {
			entry.Status, entry.ErrorCode = "cancelled", "client_cancelled"
			a.recordAttempt(r.Context(), entry)
			return
		}
		if err != nil {
			entry.Status, entry.ErrorCode = "error", "provider_error"
			if errors.Is(err, decide.ErrMalformed) {
				entry.ErrorCode = "malformed_reply"
			}
			a.log.Warn("decide attempt failed",
				"provider", att.ProviderName, "model", att.Model,
				"route", route, "code", entry.ErrorCode, "reason", err.Error())
			a.recordAttempt(r.Context(), entry)
			failed = append(failed, att.ProviderName+"/"+att.Model+": "+entry.ErrorCode)
			continue
		}
		entry.Status = "ok"
		a.recordAttempt(r.Context(), entry)
		writeJSON(w, decideResponse{DecideResult: res, Provider: att.ProviderName, Model: att.Model, Native: effort == "", LedgerID: entry.ID, Usage: usage})
		return
	}
	jsonError(w, http.StatusBadGateway, "chain_exhausted",
		"every decide attempt failed: "+strings.Join(failed, "; "))
}

// decideAttempt asks one chain entry: a decision model directly, any
// other provider through the llm fallback. The returned effort is the
// ledger's D-020 tag: "low" for the fallback, empty for a native
// answer that spent no reasoning budget.
func decideAttempt(ctx context.Context, att router.Attempt, req provider.DecideRequest, final bool) (provider.DecideResult, *stream.Usage, string, error) {
	if d, ok := att.Provider.(provider.Decider); ok {
		res, usage, err := d.Decide(ctx, att.Model, req)
		return res, usage, "", err
	}
	res, usage, err := decide.ViaChat(ctx, att.Provider, att.Model, req, final, att.ReasoningEffort)
	return res, usage, "low", err
}

// resolveDecide picks the route and attempts for a decide call. An
// explicit route is used as given. Otherwise the decide role's route
// serves; when that role is unbound, or its route is disabled or
// empty, the summarize role's route serves instead, so an install that
// never chained a decide route answers exactly as the old classify
// side-call did (D-163). A decide route whose every entry lacks the
// capability still errors: that is a configuration to fix, not to
// route around.
func resolveDecide(snap *router.Snapshot, route string) (string, []router.Attempt, error) {
	if route != "" {
		attempts, err := snap.Resolve(route, "", router.Sticky{})
		return route, attempts, err
	}
	if name, ok := snap.RouteForRole("decide"); ok {
		attempts, err := snap.Resolve(name, "", router.Sticky{})
		var nre *router.NoRouteError
		if err == nil || !errors.As(err, &nre) || len(nre.Skipped) > 0 {
			return name, attempts, err
		}
	}
	name, ok := snap.RouteForRole("summarize")
	if !ok {
		return "", nil, &router.NoRouteError{Route: "decide"}
	}
	attempts, err := snap.Resolve(name, "", router.Sticky{})
	return name, attempts, err
}
