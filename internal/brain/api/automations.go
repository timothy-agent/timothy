package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/automations"
	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// registerAutomations mounts the automations surface (issue #821); a
// nil store leaves it unmounted. destinations validates an action's
// destination_ids (nil rejects any), attachments resolves its
// attachments at save time, events backs run-now (nil leaves it
// unmounted) and loc is the operator timezone for stats and next runs.
func (a *API) registerAutomations(handle func(pattern string, h http.Handler), store *automations.Store, ev *events.Store, destinations destinationLookup, attachments *attachmentResolver, loc func(ctx context.Context) *time.Location) {
	if store == nil {
		return
	}
	h := &automationAPI{store: store, events: ev, destinations: destinations, attachments: attachments, loc: loc}
	handle("GET /v1/automations", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/automations", a.auth(http.HandlerFunc(h.create)))
	handle("GET /v1/automations/stats", a.auth(http.HandlerFunc(h.stats)))
	handle("GET /v1/automations/{id}", a.auth(http.HandlerFunc(h.get)))
	handle("PATCH /v1/automations/{id}", a.auth(http.HandlerFunc(h.patch)))
	handle("DELETE /v1/automations/{id}", a.auth(http.HandlerFunc(h.delete)))
	if ev != nil {
		handle("POST /v1/automations/{id}/run", a.auth(http.HandlerFunc(h.runNow)))
	}
	handle("GET /v1/automations/{id}/runs", a.auth(http.HandlerFunc(h.runs)))
	handle("GET /v1/automations/{id}/notes", a.auth(http.HandlerFunc(h.listNotes)))
	handle("GET /v1/automations/{id}/notes/{name}", a.auth(http.HandlerFunc(h.getNote)))
	handle("PUT /v1/automations/{id}/notes/{name}", a.auth(http.HandlerFunc(h.putNote)))
	handle("DELETE /v1/automations/{id}/notes/{name}", a.auth(http.HandlerFunc(h.deleteNote)))
}

type automationAPI struct {
	store        *automations.Store
	events       *events.Store
	destinations destinationLookup
	attachments  *attachmentResolver
	loc          func(ctx context.Context) *time.Location
}

const (
	defaultRunsLimit = 50
	maxRunsLimit     = 200
)

func (h *automationAPI) location(ctx context.Context) *time.Location {
	if h.loc != nil {
		if l := h.loc(ctx); l != nil {
			return l
		}
	}
	return time.UTC
}

// decodeStrict decodes r's body into v, rejecting unknown fields.
func decodeStrict(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func failAutomation(w http.ResponseWriter, err error) {
	var ae *attachmentError
	var ve *automations.ValidationError
	switch {
	case errors.Is(err, automations.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, automations.ErrNameConflict):
		jsonError(w, http.StatusConflict, "name_conflict", err.Error())
	case errors.Is(err, automations.ErrNoteLimit):
		jsonError(w, http.StatusConflict, "note_limit", err.Error())
	case errors.Is(err, automations.ErrBadCron):
		jsonError(w, http.StatusBadRequest, "bad_cron", err.Error())
	case errors.As(err, &ae):
		jsonError(w, attachmentErrorStatus(err), "bad_request", err.Error())
	case errors.Is(err, automations.ErrUnknownAgent), errors.As(err, &ve):
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		jsonError(w, http.StatusInternalServerError, "automations_failed", err.Error())
	}
}

// validateDestinationIDs rejects any id that does not name a real,
// enabled destination; nil destinations rejects any non-empty list.
func (h *automationAPI) validateDestinationIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if h.destinations == nil {
		return &automations.ValidationError{Err: errors.New("destinations are not enabled")}
	}
	var invalid []string
	for _, id := range ids {
		ok, err := h.destinations.EnabledByID(ctx, id)
		if err != nil {
			return fmt.Errorf("action.mission.destination_ids: %w", err)
		}
		if !ok {
			invalid = append(invalid, id)
		}
	}
	if len(invalid) > 0 {
		return &automations.ValidationError{Err: fmt.Errorf("unknown or disabled destination id(s): %s", strings.Join(invalid, ", "))}
	}
	return nil
}

// resolveTemplateAttachments converts a mission action's attachment
// refs (id and optional name) into converted entries, reusing an entry
// from existing whose id matches and whose markdown is already set.
func (h *automationAPI) resolveTemplateAttachments(ctx context.Context, want, existing []missions.SourceEntry) ([]missions.SourceEntry, error) {
	if len(want) == 0 {
		return nil, nil
	}
	if len(want) > maxMissionAttachments {
		return nil, attachErr(http.StatusBadRequest, fmt.Sprintf("too many attachments (max %d)", maxMissionAttachments))
	}
	byID := make(map[string]missions.SourceEntry, len(existing))
	for _, e := range existing {
		byID[e.ID] = e
	}
	var toConvert []missionAttachmentInput
	out := make([]missions.SourceEntry, len(want))
	for i, w := range want {
		if prior, ok := byID[w.ID]; ok && prior.Markdown != "" {
			out[i] = prior
			if w.Name != "" {
				out[i].Name = w.Name
			}
			continue
		}
		toConvert = append(toConvert, missionAttachmentInput{ID: w.ID, Name: w.Name})
	}
	if len(toConvert) == 0 {
		return out, nil
	}
	converted, err := h.attachments.Resolve(ctx, toConvert)
	if err != nil {
		return nil, err
	}
	convertedByID := make(map[string]missions.SourceEntry, len(converted))
	for _, c := range converted {
		convertedByID[c.ID] = c
	}
	for i, w := range want {
		if out[i].Source == "" {
			out[i] = convertedByID[w.ID]
		}
	}
	return out, nil
}

// prepareAction validates act and resolves its destinations and
// attachments; existing is the stored action's attachments (nil on
// create).
func (h *automationAPI) prepareAction(ctx context.Context, act *automations.Action, existing []missions.SourceEntry) error {
	if err := automations.ValidateAction(*act); err != nil {
		return err
	}
	if err := h.validateDestinationIDs(ctx, act.Mission.DestinationIDs); err != nil {
		return err
	}
	atts, err := h.resolveTemplateAttachments(ctx, act.Mission.Attachments, existing)
	if err != nil {
		return err
	}
	act.Mission.Attachments = atts
	return nil
}

// automationView is an automation plus its run stats, with attachment
// markdown stripped.
type automationView struct {
	automations.Automation
	Stats automations.AutomationStats `json:"stats"`
}

// stripActionAttachmentMarkdown clears converted attachment markdown
// before an automation goes out over the API.
func stripActionAttachmentMarkdown(a automations.Automation) automations.Automation {
	if a.Action.Mission == nil || len(a.Action.Mission.Attachments) == 0 {
		return a
	}
	m := *a.Action.Mission
	m.Attachments = make([]missions.SourceEntry, len(a.Action.Mission.Attachments))
	for i, e := range a.Action.Mission.Attachments {
		e.Markdown = ""
		m.Attachments[i] = e
	}
	a.Action.Mission = &m
	return a
}

func (h *automationAPI) view(ctx context.Context, a automations.Automation) (automationView, error) {
	st, err := h.store.AutomationStats(ctx, a, time.Now(), h.location(ctx))
	if err != nil {
		return automationView{}, err
	}
	return automationView{Automation: stripActionAttachmentMarkdown(a), Stats: st}, nil
}

func (h *automationAPI) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.List(r.Context())
	if err != nil {
		failAutomation(w, err)
		return
	}
	views := make([]automationView, 0, len(rows))
	for _, a := range rows {
		v, err := h.view(r.Context(), a)
		if err != nil {
			failAutomation(w, err)
			return
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"automations": views})
}

func (h *automationAPI) get(w http.ResponseWriter, r *http.Request) {
	a, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failAutomation(w, err)
		return
	}
	v, err := h.view(r.Context(), a)
	if err != nil {
		failAutomation(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *automationAPI) stats(w http.ResponseWriter, r *http.Request) {
	st, err := h.store.Stats(r.Context(), time.Now(), h.location(r.Context()))
	if err != nil {
		failAutomation(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// triggerInput is a trigger on the wire; id names an existing trigger
// to keep on patch.
type triggerInput struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	Config        json.RawMessage `json:"config"`
	CredentialRef string          `json:"credential_ref"`
	ToolAllowlist []string        `json:"tool_allowlist"`
	Enabled       *bool           `json:"enabled"`
}

func toTriggers(in []triggerInput) []automations.Trigger {
	out := make([]automations.Trigger, len(in))
	for i, t := range in {
		enabled := true
		if t.Enabled != nil {
			enabled = *t.Enabled
		}
		out[i] = automations.Trigger{ID: t.ID, Kind: t.Kind, Config: t.Config, CredentialRef: t.CredentialRef, ToolAllowlist: t.ToolAllowlist, Enabled: enabled}
	}
	return out
}

type createAutomationRequest struct {
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	AgentID        string             `json:"agent_id"`
	Action         automations.Action `json:"action"`
	Triggers       []triggerInput     `json:"triggers"`
	Concurrency    *string            `json:"concurrency"`
	MaxConcurrent  *int               `json:"max_concurrent"`
	MaxRunsPerHour *int               `json:"max_runs_per_hour"`
	Continuity     *bool              `json:"continuity"`
	NotesEnabled   *bool              `json:"notes_enabled"`
	Enabled        *bool              `json:"enabled"`
	ExpiresAt      *time.Time         `json:"expires_at"`
}

// automation builds the row a create request asks for, with defaults
// for every omitted optional field.
func (req createAutomationRequest) automation() automations.Automation {
	a := automations.Automation{
		Name: req.Name, Description: req.Description, AgentID: req.AgentID, Action: req.Action,
		Concurrency: automations.ConcurrencySkip, MaxConcurrent: 1, MaxRunsPerHour: 6,
		Continuity: true, NotesEnabled: true, Enabled: true, ExpiresAt: req.ExpiresAt,
		Triggers: toTriggers(req.Triggers),
	}
	if req.Concurrency != nil {
		a.Concurrency = *req.Concurrency
	}
	if req.MaxConcurrent != nil {
		a.MaxConcurrent = *req.MaxConcurrent
	}
	if req.MaxRunsPerHour != nil {
		a.MaxRunsPerHour = *req.MaxRunsPerHour
	}
	if req.Continuity != nil {
		a.Continuity = *req.Continuity
	}
	if req.NotesEnabled != nil {
		a.NotesEnabled = *req.NotesEnabled
	}
	if req.Enabled != nil {
		a.Enabled = *req.Enabled
	}
	return a
}

func (h *automationAPI) create(w http.ResponseWriter, r *http.Request) {
	var req createAutomationRequest
	if err := decodeStrict(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	a := req.automation()
	if err := automations.Validate(&a); err != nil {
		failAutomation(w, err)
		return
	}
	if err := h.prepareAction(r.Context(), &a.Action, nil); err != nil {
		failAutomation(w, err)
		return
	}
	id, err := h.store.Create(r.Context(), a)
	if err != nil {
		failAutomation(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

type patchAutomationRequest struct {
	Name           *string             `json:"name"`
	Description    *string             `json:"description"`
	AgentID        *string             `json:"agent_id"`
	Action         *automations.Action `json:"action"`
	Triggers       *[]triggerInput     `json:"triggers"`
	Concurrency    *string             `json:"concurrency"`
	MaxConcurrent  *int                `json:"max_concurrent"`
	MaxRunsPerHour *int                `json:"max_runs_per_hour"`
	Continuity     *bool               `json:"continuity"`
	NotesEnabled   *bool               `json:"notes_enabled"`
	Enabled        *bool               `json:"enabled"`
	// ExpiresAt stays raw so an explicit null (clear) differs from an
	// omitted field (unchanged).
	ExpiresAt json.RawMessage `json:"expires_at"`
}

// decodeExpiresAt maps a patch's raw expires_at: absent is nil
// (unchanged), null clears, a timestamp sets.
func decodeExpiresAt(raw json.RawMessage) (**time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	var t *time.Time
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var v time.Time
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("expires_at must be an RFC 3339 timestamp or null")
		}
		t = &v
	}
	return &t, nil
}

func (h *automationAPI) patch(w http.ResponseWriter, r *http.Request) {
	var req patchAutomationRequest
	if err := decodeStrict(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	expires, err := decodeExpiresAt(req.ExpiresAt)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id := r.PathValue("id")
	p := automations.Patch{
		Name: req.Name, Description: req.Description, AgentID: req.AgentID, Action: req.Action,
		Concurrency: req.Concurrency, MaxConcurrent: req.MaxConcurrent, MaxRunsPerHour: req.MaxRunsPerHour,
		Continuity: req.Continuity, NotesEnabled: req.NotesEnabled, Enabled: req.Enabled, ExpiresAt: expires,
	}
	if req.Triggers != nil {
		ts := toTriggers(*req.Triggers)
		p.Triggers = &ts
	}
	if req.Action != nil {
		if err := automations.ValidateAction(*req.Action); err != nil {
			failAutomation(w, err)
			return
		}
		before, err := h.store.Get(r.Context(), id)
		if err != nil {
			failAutomation(w, err)
			return
		}
		var existing []missions.SourceEntry
		if before.Action.Mission != nil {
			existing = before.Action.Mission.Attachments
		}
		if err := h.prepareAction(r.Context(), req.Action, existing); err != nil {
			failAutomation(w, err)
			return
		}
	}
	if err := h.store.Patch(r.Context(), id, p); err != nil {
		failAutomation(w, err)
		return
	}
	h.get(w, r)
}

func (h *automationAPI) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), r.PathValue("id")); err != nil {
		failAutomation(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runNow records a run.now event for an enabled, unexpired automation.
// Nothing consumes it until the dispatcher lands (issue #822).
func (h *automationAPI) runNow(w http.ResponseWriter, r *http.Request) {
	a, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failAutomation(w, err)
		return
	}
	now := time.Now()
	if !a.Enabled || (a.ExpiresAt != nil && !a.ExpiresAt.After(now)) {
		jsonError(w, http.StatusConflict, "automation_disabled", "automation is disabled or expired")
		return
	}
	ev, err := events.RunNow(a.ID, now)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "automations_failed", err.Error())
		return
	}
	eventID, err := h.events.Add(r.Context(), ev)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "automations_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int64{"event_id": eventID})
}

// parseRunsLimit reads ?limit=: default 50, 1..200.
func parseRunsLimit(v string) (int, error) {
	if v == "" {
		return defaultRunsLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxRunsLimit {
		return 0, fmt.Errorf("limit must be an integer between 1 and %d", maxRunsLimit)
	}
	return n, nil
}

func (h *automationAPI) runs(w http.ResponseWriter, r *http.Request) {
	limit, err := parseRunsLimit(r.URL.Query().Get("limit"))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id := r.PathValue("id")
	if _, err := h.store.Get(r.Context(), id); err != nil {
		failAutomation(w, err)
		return
	}
	runs, err := h.store.ListRuns(r.Context(), id, limit)
	if err != nil {
		failAutomation(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (h *automationAPI) listNotes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.store.Get(r.Context(), id); err != nil {
		failAutomation(w, err)
		return
	}
	notes, err := h.store.ListNotes(r.Context(), id)
	if err != nil {
		failAutomation(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes})
}

func (h *automationAPI) getNote(w http.ResponseWriter, r *http.Request) {
	n, err := h.store.GetNote(r.Context(), r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		failAutomation(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (h *automationAPI) putNote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content *string `json:"content"`
	}
	if err := decodeStrict(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Content == nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "content is required")
		return
	}
	n, created, err := h.store.PutNote(r.Context(), r.PathValue("id"), r.PathValue("name"), *req.Content, "")
	if err != nil {
		failAutomation(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, n)
}

func (h *automationAPI) deleteNote(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteNote(r.Context(), r.PathValue("id"), r.PathValue("name")); err != nil {
		failAutomation(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
