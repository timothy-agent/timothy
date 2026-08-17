package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/settings"
)

// adminRoutePatterns is the EXHAUSTIVE admin surface brain exposes;
// everything else on the gateway's internal API stays unreachable
// from outside. Tests pin this scope like the memory proxy's.
var adminRoutePatterns = []string{
	"GET /v1/admin/usage/{rest...}",
	"PATCH /v1/admin/usage/budget",
	"GET /v1/admin/providers",
	"POST /v1/admin/providers",
	"PATCH /v1/admin/providers/{id}",
	"DELETE /v1/admin/providers/{id}",
	"POST /v1/admin/providers/{id}/test",
	"GET /v1/admin/providers/{id}/models",
	"POST /v1/admin/providers/validate",
	"GET /v1/admin/providers/health",
	"POST /v1/admin/catalog/refresh",
	"GET /v1/admin/catalog/status",
	"GET /v1/admin/catalog/models",
	"POST /v1/admin/catalog/prices",
	"GET /v1/admin/providers/{id}/catalog-suggestions",
	"GET /v1/admin/providers/{id}/catalog-models",
	"GET /v1/admin/routes",
	"POST /v1/admin/routes",
	"PATCH /v1/admin/routes/{name}",
	"DELETE /v1/admin/routes/{name}",
	"PUT /v1/admin/routes/{name}/role",
	"PUT /v1/admin/secrets/{ref_name}",
	// DELETE /v1/admin/secrets/{ref_name} is deliberately NOT proxied:
	// registerSecrets serves it locally (connector-reference guard
	// before forwarding), and net/http's ServeMux panics on a duplicate
	// pattern — the two registrations must never coexist.
	"GET /v1/admin/secrets/{ref_name}",
	"GET /v1/admin/secret-backends",
	"PUT /v1/admin/secret-backends/default",
	"GET /v1/admin/secret-backends/{backend}",
	"PUT /v1/admin/secret-backends/{backend}",
	"DELETE /v1/admin/secret-backends/{backend}",
	"POST /v1/admin/secret-backends/{backend}/test",
}

// registerAdmin mounts the admin proxy behind bearer auth. nil leaves
// the surface unmounted (gateway URL misconfigured).
func (a *API) registerAdmin(handle func(pattern string, h http.Handler), admin http.Handler) {
	if admin == nil {
		return
	}
	for _, pattern := range adminRoutePatterns {
		handle(pattern, a.auth(admin))
	}
}

// registerSettings mounts brain's own feature switches — served
// locally, not proxied: the gateway has no business knowing them.
// whisperURL derives the read-only transcribe_enabled flag; it isn't a
// stored setting and PATCH rejects it like any other unknown key.
func (a *API) registerSettings(handle func(pattern string, h http.Handler), flags *settings.Store, whisperURL string) {
	if flags == nil {
		return
	}
	handle("GET /v1/admin/settings", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s := flags.All(r.Context())
		s["transcribe_enabled"] = whisperURL != ""
		_ = json.NewEncoder(w).Encode(map[string]any{
			"settings": s,
			"values":   flags.AllValues(r.Context()),
		})
	})))
	handle("PATCH /v1/admin/settings", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]bool
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) == 0 {
			jsonError(w, http.StatusBadRequest, "bad_request", "body must be a JSON object of setting keys to booleans")
			return
		}
		for key, value := range body {
			if err := flags.Set(r.Context(), key, value); err != nil {
				jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	// Typed runtime settings (strings; empty resets to the built-in
	// default) — separate from the boolean switches above so both
	// bodies stay flat maps.
	handle("PATCH /v1/admin/settings/values", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) == 0 {
			jsonError(w, http.StatusBadRequest, "bad_request", "body must be a JSON object of setting keys to string values")
			return
		}
		for key, value := range body {
			if err := flags.SetValue(r.Context(), key, value); err != nil {
				jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}

// GatewaySecrets is the slice of gwclient.Client the credentials
// directory needs — an interface at point of use so tests can fake the
// gateway round trip without a live one. *gwclient.Client satisfies it.
type GatewaySecrets interface {
	ListSecrets(ctx context.Context) ([]gwclient.SecretRef, error)
	DeleteSecret(ctx context.Context, refName string) (status int, err error)
}

// connectorLister is the slice of connectors.Store the directory
// needs — *connectors.Store satisfies it.
type connectorLister interface {
	List(ctx context.Context) ([]connectors.Connector, error)
}

// secretRefEntry is the credentials panel's per-ref shape: the
// gateway's directory metadata plus every referent (provider or
// connector) across both services, merged here because neither service
// alone can see both tables. Never a value.
type secretRefEntry struct {
	RefName      string          `json:"name"`
	CreatedAt    any             `json:"created_at,omitempty"`
	UpdatedAt    any             `json:"updated_at,omitempty"`
	ReferencedBy []referenceInfo `json:"referenced_by"`
}

type referenceInfo struct {
	Kind string `json:"kind"` // "provider" | "connector"
	Name string `json:"name"`
}

// registerSecrets mounts brain's own credentials-directory endpoints.
// GET assembles the gateway's per-ref provider referents with brain's
// own connector referents (two independent DBs, no new cross-service
// dependency — see admin_test.go for the design note). DELETE checks
// connector references itself before forwarding to the gateway, which
// independently refuses on provider references — each service stays
// authoritative for the referents it owns. nil gw leaves the surface
// unmounted; nil conns (connectors disabled — no master key) still
// mounts it, minus the connector-reference guard, because DELETE has
// no proxied fallback (see adminRoutePatterns) and the gateway's own
// provider guard still applies.
func (a *API) registerSecrets(handle func(pattern string, h http.Handler), gw GatewaySecrets, conns connectorLister) {
	if gw == nil {
		return
	}
	h := &secretsAPI{gw: gw, connectors: conns}
	handle("GET /v1/admin/secrets", a.auth(http.HandlerFunc(h.list)))
	handle("DELETE /v1/admin/secrets/{ref_name}", a.auth(http.HandlerFunc(h.delete)))
}

type secretsAPI struct {
	gw         GatewaySecrets
	connectors connectorLister
}

// connectorRefs maps every stored secret ref name to the connector
// names referencing it: each row's credential_ref, plus — for a github
// connector with commit signing enabled — the derived
// connectors.SigningKeyRefSuffix ref its private signing key lives
// under, so the signing key never looks orphaned in the panel or
// becomes deletable while the connector still signs with it.
func connectorRefs(ctx context.Context, store connectorLister) (map[string][]string, error) {
	if store == nil {
		return map[string][]string{}, nil
	}
	rows, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, c := range rows {
		if c.CredentialRef == "" {
			continue
		}
		out[c.CredentialRef] = append(out[c.CredentialRef], c.Name)
		if c.Kind == "github" {
			var cfg connectors.GitHubConfig
			if json.Unmarshal(c.Config, &cfg) == nil && (cfg.SignCommits || cfg.SigningPublicKey != "") {
				ref := connectors.SigningKeyRefSuffix(c.CredentialRef)
				out[ref] = append(out[ref], c.Name)
			}
		}
	}
	return out, nil
}

func (h *secretsAPI) list(w http.ResponseWriter, r *http.Request) {
	refs, err := h.gw.ListSecrets(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, "gateway_unreachable", err.Error())
		return
	}
	byConnector, err := connectorRefs(r.Context(), h.connectors)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "connectors_failed", err.Error())
		return
	}

	out := make([]secretRefEntry, len(refs))
	for i, ref := range refs {
		// Always a JSON array, never null: the frontend indexes
		// referenced_by unconditionally.
		referents := []referenceInfo{}
		for _, name := range ref.ReferencedBy {
			referents = append(referents, referenceInfo{Kind: "provider", Name: name})
		}
		for _, name := range byConnector[ref.RefName] {
			referents = append(referents, referenceInfo{Kind: "connector", Name: name})
		}
		out[i] = secretRefEntry{
			RefName: ref.RefName, CreatedAt: ref.CreatedAt, UpdatedAt: ref.UpdatedAt,
			ReferencedBy: referents,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": out})
}

// delete refuses (409) while any connector still names ref_name as its
// credential_ref, without ever asking the gateway — a connector-only
// reference is brain's own domain. Otherwise it forwards to the
// gateway, which independently refuses on provider references.
func (h *secretsAPI) delete(w http.ResponseWriter, r *http.Request) {
	refName := r.PathValue("ref_name")
	byConnector, err := connectorRefs(r.Context(), h.connectors)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "connectors_failed", err.Error())
		return
	}
	if names := byConnector[refName]; len(names) > 0 {
		jsonError(w, http.StatusConflict, "in_use",
			refName+" is referenced by connector(s) "+joinNames(names))
		return
	}
	status, err := h.gw.DeleteSecret(r.Context(), refName)
	if err != nil {
		if status == 0 {
			status = http.StatusBadGateway
		}
		jsonError(w, status, "delete_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func joinNames(names []string) string {
	out := names[0]
	for _, n := range names[1:] {
		out += ", " + n
	}
	return out
}
