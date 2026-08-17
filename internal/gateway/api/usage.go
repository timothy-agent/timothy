package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
)

// RegisterUsage mounts the internal usage-aggregation routes. Like the
// rest of the gateway surface they carry no auth — brain proxies them
// behind its bearer as /v1/admin/usage/*.
func RegisterUsage(srv *httpserver.Server, agg *ledger.Aggregator, budgets *ledger.BudgetStore) {
	u := &usageAPI{agg: agg, budgets: budgets}
	srv.Handle("GET /internal/admin/usage/summary", http.HandlerFunc(u.handleSummary))
	srv.Handle("GET /internal/admin/usage/series", http.HandlerFunc(u.handleSeries))
	srv.Handle("GET /internal/admin/usage/totals", http.HandlerFunc(u.handleTotals))
	srv.Handle("GET /internal/admin/usage/unpriced", http.HandlerFunc(u.handleUnpriced))
	srv.Handle("GET /internal/admin/usage/sessions", http.HandlerFunc(u.handleSessions))
	srv.Handle("GET /internal/admin/usage/latency", http.HandlerFunc(u.handleLatency))
	srv.Handle("GET /internal/admin/usage/cache", http.HandlerFunc(u.handleCache))
	srv.Handle("GET /internal/admin/usage/budget", http.HandlerFunc(u.handleBudget))
	srv.Handle("GET /internal/admin/usage/mission", http.HandlerFunc(u.handleMission))
}

type usageAPI struct {
	agg     *ledger.Aggregator
	budgets *ledger.BudgetStore
}

// timeRange parses from/to with sane defaults: the last 30 days, and
// to is exclusive. Bad input is a 400, never a silent full scan.
func timeRange(r *http.Request) (from, to time.Time, err error) {
	now := time.Now().UTC()
	from, to = now.AddDate(0, 0, -30), now
	if v := r.URL.Query().Get("from"); v != "" {
		if from, err = time.Parse(time.RFC3339, v); err != nil {
			return from, to, err
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if to, err = time.Parse(time.RFC3339, v); err != nil {
			return from, to, err
		}
	}
	return from, to, nil
}

func (u *usageAPI) handleSummary(w http.ResponseWriter, r *http.Request) {
	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	s, err := u.agg.SummaryByCurrency(r.Context(), from, to)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "usage_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"summaries": s})
}

func (u *usageAPI) handleSeries(w http.ResponseWriter, r *http.Request) {
	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "day"
	}
	group := r.URL.Query().Get("group")
	if group == "" {
		group = "provider"
	}
	points, err := u.agg.Series(r.Context(), from, to, bucket, group)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, map[string]any{"points": points})
}

func (u *usageAPI) handleTotals(w http.ResponseWriter, r *http.Request) {
	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	group := r.URL.Query().Get("group")
	if group == "" {
		group = "provider"
	}
	totals, err := u.agg.Totals(r.Context(), from, to, group)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, map[string]any{"totals": totals})
}

// handleUnpriced serves the (provider, model) pairs with unpriced usage
// in range — the dashboard's advisory catalog estimate needs the
// provider alongside the model so CatalogPrices resolves each pair
// against that provider's own candidate pool only.
func (u *usageAPI) handleUnpriced(w http.ResponseWriter, r *http.Request) {
	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	groups, err := u.agg.UnpricedByProviderModel(r.Context(), from, to)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "usage_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"groups": groups})
}

func (u *usageAPI) handleSessions(w http.ResponseWriter, r *http.Request) {
	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	sessions, err := u.agg.TopSessions(r.Context(), from, to, limit)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "usage_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"sessions": sessions})
}

func (u *usageAPI) handleLatency(w http.ResponseWriter, r *http.Request) {
	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	rows, err := u.agg.Latency(r.Context(), from, to)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "usage_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"providers": rows})
}

func (u *usageAPI) handleCache(w http.ResponseWriter, r *http.Request) {
	from, to, err := timeRange(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	rows, err := u.agg.Cache(r.Context(), from, to)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "usage_failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"providers": rows})
}

func (u *usageAPI) handleMission(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}
	m, err := u.agg.Mission(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "usage_failed", err.Error())
		return
	}
	writeJSON(w, m)
}

func (u *usageAPI) handleBudget(w http.ResponseWriter, r *http.Request) {
	limits, err := u.budgets.Limits(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "usage_failed", err.Error())
		return
	}
	status, err := u.agg.BudgetStatus(r.Context(), limits, time.Now())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "usage_failed", err.Error())
		return
	}
	writeJSON(w, status)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
