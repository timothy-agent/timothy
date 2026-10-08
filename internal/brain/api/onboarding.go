package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/onboarding"
	"github.com/SumonMSelim/timothy/internal/brain/settings"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// onboardingStore is the slice of settings.Store the onboarding
// endpoints need; *settings.Store satisfies it.
type onboardingStore interface {
	JSON(ctx context.Context, key string, dst any) (bool, error)
	SetJSON(ctx context.Context, key string, v any) error
}

// registerOnboarding mounts setup readiness and progress, served
// locally like the settings switches. nil flags leaves it unmounted.
func (a *API) registerOnboarding(handle func(pattern string, h http.Handler), flags *settings.Store, probes onboarding.Probes) {
	if flags == nil {
		return
	}
	a.mountOnboarding(handle, flags, probes)
}

func (a *API) mountOnboarding(handle func(pattern string, h http.Handler), store onboardingStore, probes onboarding.Probes) {
	handle("GET /v1/admin/onboarding", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p onboarding.Progress
		if _, err := store.JSON(r.Context(), settings.KeyOnboarding, &p); err != nil {
			a.log.Warn("onboarding: progress read degraded to empty", "error", err)
			p = onboarding.Progress{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"readiness": onboarding.Compute(r.Context(), probes, a.log),
			"progress":  p,
		})
	})))
	handle("PATCH /v1/admin/onboarding", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var patch onboarding.Progress
		if err := dec.Decode(&patch); err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if err := patch.Validate(); err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		var stored onboarding.Progress
		if _, err := store.JSON(r.Context(), settings.KeyOnboarding, &stored); err != nil {
			a.failOnboardingStore(w, err)
			return
		}
		merged := stored.Merge(patch)
		if err := store.SetJSON(r.Context(), settings.KeyOnboarding, merged); err != nil {
			a.failOnboardingStore(w, err)
			return
		}
		writeJSON(w, http.StatusOK, merged)
	})))
}

// failOnboardingStore answers 503 for a database outage, 500 otherwise.
func (a *API) failOnboardingStore(w http.ResponseWriter, err error) {
	if errors.Is(err, pgpool.ErrDegraded) {
		jsonError(w, http.StatusServiceUnavailable, "db_unavailable", "database unavailable")
		return
	}
	failInternal(w, a.log, "onboarding", err)
}
