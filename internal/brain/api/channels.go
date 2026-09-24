package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/channels"
)

// registerChannels mounts the channels surface (issue #828); a nil
// store leaves it unmounted and a nil service leaves the test endpoint
// unmounted.
func (a *API) registerChannels(handle func(pattern string, h http.Handler), store *channels.Store, svc *channels.Service) {
	if store == nil {
		return
	}
	h := &channelAPI{store: store, svc: svc}
	handle("GET /v1/channels", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/channels", a.auth(http.HandlerFunc(h.create)))
	handle("GET /v1/channels/{id}", a.auth(http.HandlerFunc(h.get)))
	handle("PATCH /v1/channels/{id}", a.auth(http.HandlerFunc(h.patch)))
	handle("DELETE /v1/channels/{id}", a.auth(http.HandlerFunc(h.delete)))
	if svc != nil {
		handle("POST /v1/channels/{id}/test", a.auth(http.HandlerFunc(h.test)))
	}
	handle("GET /v1/channels/{id}/pairings", a.auth(http.HandlerFunc(h.pairings)))
	handle("POST /v1/channels/{id}/pairings/{external_user_id}/approve", a.auth(http.HandlerFunc(h.approve)))
	handle("POST /v1/channels/{id}/pairings/{external_user_id}/revoke", a.auth(http.HandlerFunc(h.revoke)))
}

type channelAPI struct {
	store *channels.Store
	svc   *channels.Service
}

func failChannel(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, channels.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, channels.ErrNameConflict):
		jsonError(w, http.StatusConflict, "name_conflict", err.Error())
	case errors.Is(err, channels.ErrKindUnavailable):
		jsonError(w, http.StatusBadRequest, "not_available", err.Error())
	case errors.Is(err, channels.ErrInvalid), errors.Is(err, channels.ErrUnknownAgent):
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		jsonError(w, http.StatusInternalServerError, "channels_failed", err.Error())
	}
}

type channelConfigInput struct {
	Dispatch *bool `json:"dispatch"`
}

type createChannelRequest struct {
	Name          string              `json:"name"`
	Kind          string              `json:"kind"`
	CredentialRef string              `json:"credential_ref"`
	AgentID       string              `json:"agent_id"`
	Config        *channelConfigInput `json:"config"`
	Enabled       *bool               `json:"enabled"`
}

type patchChannelRequest struct {
	Name          *string             `json:"name"`
	CredentialRef *string             `json:"credential_ref"`
	// AgentID stays raw so null (clear) differs from omitted.
	AgentID json.RawMessage     `json:"agent_id"`
	Enabled *bool               `json:"enabled"`
	Config  *channelConfigInput `json:"config"`
}

func (h *channelAPI) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.List(r.Context())
	if err != nil {
		failChannel(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": rows})
}

func (h *channelAPI) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failChannel(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *channelAPI) create(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if err := decodeStrict(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	c := channels.Channel{Name: req.Name, Kind: req.Kind, CredentialRef: req.CredentialRef, AgentID: req.AgentID, Enabled: true}
	if req.Config != nil && req.Config.Dispatch != nil {
		c.Config.Dispatch = *req.Config.Dispatch
	}
	if req.Enabled != nil {
		c.Enabled = *req.Enabled
	}
	id, err := h.store.Create(r.Context(), c)
	if err != nil {
		failChannel(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h *channelAPI) patch(w http.ResponseWriter, r *http.Request) {
	var req patchChannelRequest
	if err := decodeStrict(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	p := channels.Patch{Name: req.Name, CredentialRef: req.CredentialRef, Enabled: req.Enabled}
	if req.Config != nil {
		p.Dispatch = req.Config.Dispatch
	}
	if req.AgentID != nil {
		agent := ""
		if !bytes.Equal(bytes.TrimSpace(req.AgentID), []byte("null")) {
			if err := json.Unmarshal(req.AgentID, &agent); err != nil {
				jsonError(w, http.StatusBadRequest, "bad_request", "agent_id must be a string or null")
				return
			}
		}
		p.AgentID = &agent
	}
	if err := h.store.Patch(r.Context(), r.PathValue("id"), p); err != nil {
		failChannel(w, err)
		return
	}
	h.get(w, r)
}

func (h *channelAPI) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), r.PathValue("id")); err != nil {
		failChannel(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// test calls the Bot API's getMe with the channel's token.
func (h *channelAPI) test(w http.ResponseWriter, r *http.Request) {
	username, err := h.svc.Test(r.Context(), r.PathValue("id"))
	if errors.Is(err, channels.ErrNotFound) {
		failChannel(w, err)
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, "test_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bot_username": username})
}

func (h *channelAPI) pairings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.store.Get(r.Context(), id); err != nil {
		failChannel(w, err)
		return
	}
	rows, err := h.store.ListPairings(r.Context(), id)
	if err != nil {
		failChannel(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairings": rows})
}

func (h *channelAPI) approve(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Approve(r.Context(), r.PathValue("id"), r.PathValue("external_user_id")); err != nil {
		failChannel(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *channelAPI) revoke(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Revoke(r.Context(), r.PathValue("id"), r.PathValue("external_user_id")); err != nil {
		failChannel(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
