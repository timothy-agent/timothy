package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/channels"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
)

// registerChannels mounts the channels surface (issues #828, #830); a nil
// store leaves it unmounted and a nil service leaves the test endpoint
// unmounted. conns checks an email channel's connector; nil refuses
// email channels.
func (a *API) registerChannels(handle func(pattern string, h http.Handler), store *channels.Store, svc *channels.Service, conns connectorLookup) {
	if store == nil {
		return
	}
	h := &channelAPI{store: store, svc: svc, conns: conns, log: a.log}
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
	conns connectorLookup
	log   *slog.Logger
}

// checkEmailConnector rejects an email channel connector that is
// missing, disabled or not an imap connector.
func (h *channelAPI) checkEmailConnector(ctx context.Context, id string) error {
	if h.conns == nil {
		return fmt.Errorf("%w: connectors are not enabled", channels.ErrInvalid)
	}
	kind, enabled, err := h.conns(ctx, id)
	switch {
	case errors.Is(err, connectors.ErrNotFound):
		return fmt.Errorf("%w: unknown connector_id %s", channels.ErrInvalid, id)
	case err != nil:
		return fmt.Errorf("connector_id %s: %w", id, err)
	case kind != "imap":
		return fmt.Errorf("%w: connector %s is kind %s, not imap", channels.ErrInvalid, id, kind)
	case !enabled:
		return fmt.Errorf("%w: connector %s is disabled", channels.ErrInvalid, id)
	}
	return nil
}

func failChannel(w http.ResponseWriter, log *slog.Logger, err error) {
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
		failInternal(w, log, "channel", err)
	}
}

type channelConfigInput struct {
	Dispatch    *bool     `json:"dispatch"`
	AppTokenRef *string   `json:"app_token_ref"`
	ConnectorID *string   `json:"connector_id"`
	FromAllow   *[]string `json:"from_allow"`
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
		failChannel(w, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": rows})
}

func (h *channelAPI) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failChannel(w, h.log, err)
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
	if req.Config != nil && req.Config.AppTokenRef != nil {
		c.Config.AppTokenRef = *req.Config.AppTokenRef
	}
	if req.Config != nil && req.Config.ConnectorID != nil {
		c.Config.ConnectorID = *req.Config.ConnectorID
	}
	if req.Config != nil && req.Config.FromAllow != nil {
		c.Config.FromAllow = *req.Config.FromAllow
	}
	if c.Kind == channels.KindEmail {
		if err := channels.Validate(&c); err != nil {
			failChannel(w, h.log, err)
			return
		}
		if err := h.checkEmailConnector(r.Context(), c.Config.ConnectorID); err != nil {
			failChannel(w, h.log, err)
			return
		}
	}
	if req.Enabled != nil {
		c.Enabled = *req.Enabled
	}
	id, err := h.store.Create(r.Context(), c)
	if err != nil {
		failChannel(w, h.log, err)
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
		p.Dispatch, p.AppTokenRef = req.Config.Dispatch, req.Config.AppTokenRef
		p.ConnectorID, p.FromAllow = req.Config.ConnectorID, req.Config.FromAllow
		if p.ConnectorID != nil {
			if err := h.checkEmailConnector(r.Context(), *p.ConnectorID); err != nil {
				failChannel(w, h.log, err)
				return
			}
		}
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
		failChannel(w, h.log, err)
		return
	}
	h.get(w, r)
}

func (h *channelAPI) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), r.PathValue("id")); err != nil {
		failChannel(w, h.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// test checks the channel's credentials with its transport.
func (h *channelAPI) test(w http.ResponseWriter, r *http.Request) {
	username, err := h.svc.Test(r.Context(), r.PathValue("id"))
	if errors.Is(err, channels.ErrNotFound) {
		failChannel(w, h.log, err)
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
		failChannel(w, h.log, err)
		return
	}
	rows, err := h.store.ListPairings(r.Context(), id)
	if err != nil {
		failChannel(w, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairings": rows})
}

func (h *channelAPI) approve(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Approve(r.Context(), r.PathValue("id"), r.PathValue("external_user_id")); err != nil {
		failChannel(w, h.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *channelAPI) revoke(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Revoke(r.Context(), r.PathValue("id"), r.PathValue("external_user_id")); err != nil {
		failChannel(w, h.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
