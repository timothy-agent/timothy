package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// registerSchedules mounts the schedules surface: served locally,
// schedules is missions' domain (the table lives in
// internal/brain/missions), same nil-gating pattern as agents/
// connectors. store is *missions.Store — schedules and missions share
// one store since schedules is this package's own table.
func (a *API) registerSchedules(handle func(pattern string, h http.Handler), store *missions.Store) {
	if store == nil {
		return
	}
	h := &scheduleAPI{store: store}
	handle("GET /v1/schedules", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/schedules", a.auth(http.HandlerFunc(h.create)))
	handle("PATCH /v1/schedules/{id}", a.auth(http.HandlerFunc(h.patch)))
	handle("DELETE /v1/schedules/{id}", a.auth(http.HandlerFunc(h.delete)))
}

type scheduleAPI struct {
	store *missions.Store
}

func failSchedule(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, missions.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, missions.ErrScheduleNameConflict):
		jsonError(w, http.StatusConflict, "name_conflict", err.Error())
	case errors.Is(err, missions.ErrScheduleInUse):
		jsonError(w, http.StatusConflict, "in_use", err.Error())
	case errors.Is(err, missions.ErrBadCron):
		jsonError(w, http.StatusBadRequest, "bad_cron", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

// scheduleView is a Schedule decorated with the computed next_run the
// web UI needs but the row itself doesn't store.
type scheduleView struct {
	missions.Schedule
	NextRun *time.Time `json:"next_run,omitempty"`
}

// decorate computes next_run from the schedule's own cron and last_run
// (or created_at, mirroring scheduler.go's fireOne anchor rule) — a
// disabled schedule still gets a next_run so re-enabling it shows
// where it will pick back up.
func decorate(sc missions.Schedule) scheduleView {
	anchor := sc.CreatedAt
	if sc.LastRun != nil {
		anchor = *sc.LastRun
	}
	next := missions.NextRun(sc.Cron, anchor)
	view := scheduleView{Schedule: sc}
	if !next.IsZero() {
		view.NextRun = &next
	}
	return view
}

func (h *scheduleAPI) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListSchedules(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "schedules_failed", err.Error())
		return
	}
	views := make([]scheduleView, 0, len(rows))
	for _, sc := range rows {
		views = append(views, decorate(sc))
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": views})
}

type createScheduleRequest struct {
	Name            string                   `json:"name"`
	Cron            string                   `json:"cron"`
	MissionTemplate missions.MissionTemplate `json:"mission_template"`
	Enabled         *bool                    `json:"enabled"`
	ExpiresAt       *time.Time               `json:"expires_at"`
}

func (h *scheduleAPI) create(w http.ResponseWriter, r *http.Request) {
	var req createScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	id, err := h.store.CreateSchedule(r.Context(), missions.Schedule{
		Name: req.Name, Cron: req.Cron,
		MissionTemplate: req.MissionTemplate, Enabled: enabled, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		failSchedule(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

type patchScheduleRequest struct {
	Name            *string                   `json:"name"`
	Cron            *string                   `json:"cron"`
	MissionTemplate *missions.MissionTemplate `json:"mission_template"`
	Enabled         *bool                     `json:"enabled"`
	// ExpiresAt: nil (omitted OR explicit null — encoding/json can't
	// tell those apart through a single pointer) means "leave
	// unchanged," same convention as agents.Patch.BudgetUSD. Clearing an
	// expiry entirely is not reachable through this endpoint in v1 —
	// delete and recreate the schedule instead.
	ExpiresAt *time.Time `json:"expires_at"`
}

func (h *scheduleAPI) patch(w http.ResponseWriter, r *http.Request) {
	var req patchScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	p := missions.SchedulePatch{
		Name: req.Name, Cron: req.Cron,
		MissionTemplate: req.MissionTemplate, Enabled: req.Enabled,
	}
	if req.ExpiresAt != nil {
		p.ExpiresAt = &req.ExpiresAt
	}
	if err := h.store.PatchSchedule(r.Context(), r.PathValue("id"), p); err != nil {
		failSchedule(w, err)
		return
	}
	sc, err := h.store.GetSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		failSchedule(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decorate(sc))
}

func (h *scheduleAPI) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteSchedule(r.Context(), r.PathValue("id")); err != nil {
		failSchedule(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
