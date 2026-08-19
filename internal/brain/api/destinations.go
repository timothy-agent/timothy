package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/destinations"
)

// destinationRefs is the narrow slice of *missions.Store the delete
// guard needs — an interface so this file doesn't force a hard
// missions import dependency shape beyond what's already used
// elsewhere in this package.
type destinationRefs interface {
	ActiveMissionReferencesDestination(ctx context.Context, destinationID string) (bool, error)
}

// destinationTester sends a canned test payload through a
// destination's real adapter — destinations.Deliverer's own
// deliverOne machinery isn't reused here since a test send must report
// success/failure synchronously to the caller, not fire-and-forget;
// see (a *destinationAPI).test.
type destinationTester interface {
	Test(ctx context.Context, id string) error
}

// registerDestinations mounts the destinations CRUD + test-send
// surface — served locally like connectors, nil-gated on store (no
// WORKSPACES/missions disables destinations too, since delivery has no
// meaning without missions).
func (a *API) registerDestinations(handle func(pattern string, h http.Handler), store *destinations.Store, refs destinationRefs, tester destinationTester) {
	if store == nil {
		return
	}
	h := &destinationAPI{store: store, refs: refs, tester: tester}
	handle("GET /v1/admin/destinations", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/admin/destinations", a.auth(http.HandlerFunc(h.create)))
	handle("PATCH /v1/admin/destinations/{id}", a.auth(http.HandlerFunc(h.patch)))
	handle("DELETE /v1/admin/destinations/{id}", a.auth(http.HandlerFunc(h.delete)))
	handle("POST /v1/admin/destinations/{id}/test", a.auth(http.HandlerFunc(h.test)))
}

type destinationAPI struct {
	store  *destinations.Store
	refs   destinationRefs
	tester destinationTester
}

func failDestination(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, destinations.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, destinations.ErrReferenced):
		jsonError(w, http.StatusConflict, "referenced", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

func (h *destinationAPI) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.List(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "destinations_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"destinations": rows})
}

func (h *destinationAPI) create(w http.ResponseWriter, r *http.Request) {
	var d destinations.Destination
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := h.store.Create(r.Context(), d)
	if err != nil {
		failDestination(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (h *destinationAPI) patch(w http.ResponseWriter, r *http.Request) {
	var patch destinations.Patch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.store.Patch(r.Context(), r.PathValue("id"), patch); err != nil {
		failDestination(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// delete refuses with 409 while any non-terminal mission references
// this destination — historical (terminal) mission references never
// block deletion.
func (h *destinationAPI) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), r.PathValue("id"), h.refs); err != nil {
		failDestination(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// test sends a canned "Timothy test delivery" payload through the REAL
// adapter and reports success/failure synchronously — mirrors the
// connector/provider test endpoints' 200-with-{ok,error} shape so the
// UI renders failures inline.
func (h *destinationAPI) test(w http.ResponseWriter, r *http.Request) {
	if h.tester == nil {
		jsonError(w, http.StatusNotFound, "not_found", "destination test-send is not enabled")
		return
	}
	if err := h.tester.Test(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, destinations.ErrNotFound) {
			failDestination(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
