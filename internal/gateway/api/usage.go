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
func RegisterUsage(srv *httpserver.Server, agg *ledger.Aggregator) {
	u := &usageAPI{agg: agg}
	srv.Handle("GET /internal/usage/summary", http.HandlerFunc(u.handleSummary))
	srv.Handle("GET /internal/usage/series", http.HandlerFunc(u.handleSeries))
	srv.Handle("GET /internal/usage/sessions", http.HandlerFunc(u.handleSessions))
	srv.Handle("GET /internal/usage/latency", http.HandlerFunc(u.handleLatency))
	srv.Handle("GET /internal/usage/cache", http.HandlerFunc(u.handleCache))
}

type usageAPI struct {
	agg *ledger.Aggregator
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
	s, err := u.agg.Summary(r.Context(), from, to)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "usage_failed", err.Error())
		return
	}
	writeJSON(w, s)
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
