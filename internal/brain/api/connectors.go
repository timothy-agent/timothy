package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/secretstore"
)

// registerConnectors mounts brain's own integration control plane —
// served locally like the settings routes, not proxied: connectors
// are brain's domain (their tools execute in the agent loop). nil mgr
// leaves the surface unmounted (no master key, connectors disabled).
// secrets, when nil, just skips signing-key generation on a
// sign_commits create/patch — same as any other secret-store-gated
// feature elsewhere in brain.
func (a *API) registerConnectors(handle func(pattern string, h http.Handler), mgr *connectors.Manager, goog *connectors.Google, msft *connectors.Microsoft, secrets *secretstore.Store) {
	if mgr == nil {
		return
	}
	h := &connectorAPI{mgr: mgr, goog: goog, msft: msft, secrets: secrets}
	handle("GET /v1/admin/connectors", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/admin/connectors", a.auth(http.HandlerFunc(h.create)))
	handle("PATCH /v1/admin/connectors/{id}", a.auth(http.HandlerFunc(h.patch)))
	handle("DELETE /v1/admin/connectors/{id}", a.auth(http.HandlerFunc(h.delete)))
	handle("POST /v1/admin/connectors/{id}/test", a.auth(http.HandlerFunc(h.test)))
	handle("GET /v1/admin/connectors/{id}/repos", a.auth(http.HandlerFunc(h.listRepos)))
	handle("POST /v1/admin/connectors/{id}/repos", a.auth(http.HandlerFunc(h.createRepo)))
	if goog != nil || msft != nil {
		handle("POST /v1/admin/connectors/{id}/oauth/start", a.auth(http.HandlerFunc(h.oauthStart)))
		// The callback is Google/Microsoft redirecting the user's browser —
		// no bearer possible; the single-use expiring state token is the
		// auth. It writes nothing an attacker chooses: a forged call
		// without a live state is rejected.
		handle("GET /v1/connectors/oauth/callback", http.HandlerFunc(h.oauthCallback))
	}
}

type connectorAPI struct {
	mgr     *connectors.Manager
	goog    *connectors.Google
	msft    *connectors.Microsoft
	secrets *secretstore.Store
}

// oauthStart begins the OAuth dance and returns the URL the browser
// should visit, dispatching to Google or Microsoft by the connector's
// own kind.
func (h *connectorAPI) oauthStart(w http.ResponseWriter, r *http.Request) {
	c, err := h.mgr.Store().Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failConnector(w, err)
		return
	}
	var authURL string
	switch c.Kind {
	case "google":
		authURL, err = h.goog.StartAuth(r.Context(), c.ID)
	case "microsoft":
		authURL, err = h.msft.StartAuth(r.Context(), c.ID)
	default:
		err = fmt.Errorf("connector kind %s has no oauth dance: %w", c.Kind, connectors.ErrUnsupported)
	}
	if err != nil {
		failConnector(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": authURL})
}

// oauthCallback finishes the dance and bounces the browser back to
// Settings with the outcome in the query. Google and Microsoft share
// this one redirect route (both registered against the same publicURL
// at signup), so the live (unconsumed) state tells us which engine
// started it, via HasState.
func (h *connectorAPI) oauthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errCode := q.Get("error"); errCode != "" {
		http.Redirect(w, r, "/settings/connectors?oauth_error="+url.QueryEscape(errCode), http.StatusFound)
		return
	}
	state := q.Get("state")
	var name string
	var err error
	switch {
	case h.goog != nil && h.goog.HasState(state):
		name, err = h.goog.HandleCallback(r.Context(), state, q.Get("code"))
	case h.msft != nil && h.msft.HasState(state):
		name, err = h.msft.HandleCallback(r.Context(), state, q.Get("code"))
	default:
		err = fmt.Errorf("unknown or expired oauth state; restart the connection from Settings")
	}
	if err != nil {
		http.Redirect(w, r, "/settings/connectors?oauth_error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/settings/connectors?oauth_connected="+url.QueryEscape(name), http.StatusFound)
}

// failConnector maps the package's sentinel errors onto HTTP statuses;
// everything else is the caller's input.
func failConnector(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, connectors.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, connectors.ErrUnsupported):
		jsonError(w, http.StatusUnprocessableEntity, "unsupported", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

func (h *connectorAPI) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.mgr.Store().List(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "connectors_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": rows})
}

func (h *connectorAPI) create(w http.ResponseWriter, r *http.Request) {
	var c connectors.Connector
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if c.Kind == "github" {
		cfg, err := h.ensureGitHubSigningKey(r.Context(), c.CredentialRef, c.Config)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		c.Config = cfg
	}
	id, err := h.mgr.Store().Create(r.Context(), c)
	if err != nil {
		failConnector(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (h *connectorAPI) patch(w http.ResponseWriter, r *http.Request) {
	var patch connectors.Patch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if patch.Config != nil {
		existing, err := h.mgr.Store().Get(r.Context(), r.PathValue("id"))
		if err != nil {
			failConnector(w, err)
			return
		}
		if existing.Kind == "github" {
			credentialRef := existing.CredentialRef
			if patch.CredentialRef != nil {
				credentialRef = *patch.CredentialRef
			}
			cfg, err := h.ensureGitHubSigningKey(r.Context(), credentialRef, *patch.Config)
			if err != nil {
				jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
			patch.Config = &cfg
		}
	}
	if err := h.mgr.Store().Patch(r.Context(), r.PathValue("id"), patch); err != nil {
		failConnector(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ensureGitHubSigningKey decodes raw as a GitHubConfig, generates and
// persists an SSH signing keypair when sign_commits is newly true and
// no key exists yet (connectors.EnsureSigningKey is idempotent), and
// re-encodes the result. secrets nil (no master key configured) leaves
// sign_commits set but never generates a key — same degrade as any
// other secret-store-gated feature.
func (h *connectorAPI) ensureGitHubSigningKey(ctx context.Context, credentialRef string, raw json.RawMessage) (json.RawMessage, error) {
	if h.secrets == nil || len(raw) == 0 {
		return raw, nil
	}
	var cfg connectors.GitHubConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if !cfg.SignCommits || cfg.SigningPublicKey != "" {
		return raw, nil
	}
	next, err := connectors.EnsureSigningKey(ctx, h.secrets, credentialRef, cfg)
	if err != nil {
		return nil, err
	}
	return json.Marshal(next)
}

// listRepos serves GET /v1/admin/connectors/{id}/repos: every repo the
// connector's PAT can see, for the mission-create repo picker.
// Non-github-kind (or unbuildable) connectors 400/422 via failConnector
// (ErrUnsupported maps to 422 there — kept as 400 here since a picker
// asking a non-github connector for repos is caller error, not an
// infra condition).
func (h *connectorAPI) listRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := h.mgr.ListRepos(r.Context(), r.PathValue("id"))
	if err != nil {
		failConnector(w, err)
		return
	}
	if repos == nil {
		repos = []connectors.GitHubRepo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

// createRepo serves POST /v1/admin/connectors/{id}/repos body
// {"name","private"}: creates a new repo through the connector's PAT,
// auto-initialized so it has a default branch to clone.
func (h *connectorAPI) createRepo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Private bool   `json:"private"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	repo, err := h.mgr.CreateRepo(r.Context(), r.PathValue("id"), req.Name, req.Private)
	if err != nil {
		failConnector(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}

func (h *connectorAPI) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.mgr.Store().Delete(r.Context(), r.PathValue("id")); err != nil {
		failConnector(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// test mirrors the provider test endpoint's shape: 200 with {ok,
// error} for probe outcomes so the UI renders failures inline; HTTP
// errors only for unknown ids and unbuildable kinds. A successful
// github-kind test also carries the resolved identity (D-057): the
// connector has no tools to prove itself with, so identity is the
// evidence a working PAT was configured.
func (h *connectorAPI) test(w http.ResponseWriter, r *http.Request) {
	identity, err := h.mgr.TestIdentity(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		out := map[string]any{"ok": true}
		if identity != nil {
			out["identity"] = identity
		}
		writeJSON(w, http.StatusOK, out)
	case errors.Is(err, connectors.ErrNotFound), errors.Is(err, connectors.ErrUnsupported):
		failConnector(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
	}
}
