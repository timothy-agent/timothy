package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// registerSchedules mounts the schedules surface: served locally,
// schedules is missions' domain (the table lives in
// internal/brain/missions), same nil-gating pattern as agents/
// connectors. store is *missions.Store — schedules and missions share
// one store since schedules is this package's own table. destinations
// validates a create/patch request's mission_template.destination_ids
// (D-061) the same way missionAPI.validateDestinationIDs does — nil
// (destinations disabled) rejects any non-empty destination_ids.
func (a *API) registerSchedules(handle func(pattern string, h http.Handler), store *missions.Store, destinations destinationLookup) {
	if store == nil {
		return
	}
	h := &scheduleAPI{store: store, destinations: destinations}
	handle("GET /v1/schedules", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/schedules", a.auth(http.HandlerFunc(h.create)))
	handle("PATCH /v1/schedules/{id}", a.auth(http.HandlerFunc(h.patch)))
	handle("DELETE /v1/schedules/{id}", a.auth(http.HandlerFunc(h.delete)))
}

type scheduleAPI struct {
	store        *missions.Store
	destinations destinationLookup
}

// validateDestinationIDs rejects any id that doesn't name a real,
// enabled destinations row — same rule and error shape as
// missionAPI.validateDestinationIDs (api/missions.go), applied to a
// schedule's mission_template instead of a one-off mission create.
func (h *scheduleAPI) validateDestinationIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if h.destinations == nil {
		return fmt.Errorf("destinations are not enabled")
	}
	var invalid []string
	for _, id := range ids {
		ok, err := h.destinations.EnabledByID(ctx, id)
		if err != nil {
			return fmt.Errorf("mission_template.destination_ids: %w", err)
		}
		if !ok {
			invalid = append(invalid, id)
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("unknown or disabled destination id(s): %s", strings.Join(invalid, ", "))
	}
	return nil
}

// validateLightTemplate rejects a template that pairs light with
// kind=coding — light (D-069) only makes sense for a kind=general
// mission, same rule create() enforces for a one-off mission.
func validateLightTemplate(t missions.MissionTemplate) error {
	if t.Light && t.Kind != missions.KindGeneral {
		return fmt.Errorf("mission_template.light is only valid for kind=general")
	}
	return nil
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
	if err := h.validateDestinationIDs(r.Context(), req.MissionTemplate.DestinationIDs); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := validateLightTemplate(req.MissionTemplate); err != nil {
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
	// unchanged," same convention as agents.Patch.ReviewRoute. Clearing an
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
	if req.MissionTemplate != nil {
		if err := h.validateDestinationIDs(r.Context(), req.MissionTemplate.DestinationIDs); err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if err := validateLightTemplate(*req.MissionTemplate); err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
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
