package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/settings"
	"github.com/SumonMSelim/timothy/internal/platform/netguard"
)

// probeBodyMax caps the probe request body.
const probeBodyMax = 64 << 10

// registerConnectorProbe mounts POST /v1/admin/connectors/probe, the
// custom MCP add flow's check of an unsaved endpoint (D-152, issue
// #1166). nil mgr leaves it unmounted like the rest of the connector
// surface; nil toolset predicts final names with nothing reserved.
func (a *API) registerConnectorProbe(handle func(pattern string, h http.Handler), mgr *connectors.Manager, toolset Toolset) {
	if mgr == nil {
		return
	}
	guard := netguard.Guard{}
	if a.flags != nil {
		guard.Allowed = a.flags.OutboundHosts
	}
	h := &connectorProbeAPI{mgr: mgr, toolset: toolset, flags: a.flags, client: &http.Client{Transport: guard.Transport()}, log: a.log}
	handle("POST /v1/admin/connectors/probe", a.auth(http.HandlerFunc(h.probe)))
}

type connectorProbeAPI struct {
	mgr     *connectors.Manager
	toolset Toolset
	flags   *settings.Store
	client  *http.Client
	log     *slog.Logger
}

// probeRequest is the probe body. Token is a raw value for this call
// only: never stored, never logged.
type probeRequest struct {
	Name     string            `json:"name"`
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers"`
	Token    string            `json:"token"`
}

type probeResponse struct {
	connectors.ProbeResult
	IndexThreshold int `json:"index_threshold"`
}

func (h *connectorProbeAPI) probe(w http.ResponseWriter, r *http.Request) {
	var req probeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, probeBodyMax)).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON {endpoint, headers?, token?, name?}")
		return
	}
	u, err := url.Parse(req.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "endpoint must be an http or https URL")
		return
	}

	res := connectors.ProbeMCP(r.Context(), h.client, req.Endpoint, req.Headers, req.Token)
	threshold := settings.DefaultMCPToolIndexThreshold
	if h.flags != nil {
		threshold = h.flags.MCPToolIndexThreshold(r.Context())
	}
	res.Server.Name = missions.NeutralizeSlot(res.Server.Name)
	res.Server.Version = missions.NeutralizeSlot(res.Server.Version)
	for i := range res.Tools {
		res.Tools[i].Description = missions.NeutralizeSlot(res.Tools[i].Description)
	}
	if res.Status == connectors.ProbeOK && req.Name != "" {
		var surface []string
		if h.toolset != nil {
			for _, d := range h.toolset.Tools() {
				surface = append(surface, d.Name)
			}
		}
		names := h.mgr.ProbeFinalNames(req.Name, res, surface, threshold)
		for i := range res.Tools {
			res.Tools[i].FinalName = names[res.Tools[i].Name]
		}
	}
	h.log.Info("connector probe", "host", u.Host, "status", res.Status, "tools", res.ToolCount)
	writeJSON(w, http.StatusOK, probeResponse{ProbeResult: res, IndexThreshold: threshold})
}
