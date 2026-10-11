package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/settings"
	"github.com/SumonMSelim/timothy/internal/platform/netguard"
)

// probeBodyMax caps the probe request body.
const probeBodyMax = 64 << 10

// registerConnectorProbe mounts POST /v1/admin/connectors/probe, the
// custom MCP add flow's check of an unsaved endpoint (D-152, issue
// #1166) and the connector page's re-probe of a saved one (issue
// #1191). nil mgr leaves it unmounted like the rest of the connector
// surface; nil toolset predicts final names with nothing reserved;
// nil mcpAuth answers needs_oauth for an oauth-mode connector.
func (a *API) registerConnectorProbe(handle func(pattern string, h http.Handler), mgr *connectors.Manager, mcpAuth *connectors.MCPAuth, toolset Toolset) {
	if mgr == nil {
		return
	}
	guard := netguard.Guard{}
	if a.flags != nil {
		guard.Allowed = a.flags.OutboundHosts
	}
	h := &connectorProbeAPI{
		mgr:     mgr,
		rows:    mgr.Store(),
		auth:    mcpAuth,
		toolset: toolset,
		flags:   a.flags,
		client:  &http.Client{Transport: guard.Transport()},
		now:     time.Now,
		log:     a.log,
	}
	handle("POST /v1/admin/connectors/probe", a.auth(http.HandlerFunc(h.probe)))
}

// probeRows is the slice of the connector store a re-probe needs.
type probeRows interface {
	Get(ctx context.Context, id string) (connectors.Connector, error)
	SetLastProbe(ctx context.Context, id string, rec connectors.MCPLastProbe) error
}

type connectorProbeAPI struct {
	mgr     *connectors.Manager
	rows    probeRows
	auth    *connectors.MCPAuth
	toolset Toolset
	flags   *settings.Store
	client  *http.Client
	now     func() time.Time
	log     *slog.Logger
}

// probeRequest is the probe body. Token is a raw value for this call
// only: never stored, never logged. ConnectorID probes a saved
// connector with its stored credential instead; endpoint, headers and
// token are then ignored.
type probeRequest struct {
	Name        string            `json:"name"`
	Endpoint    string            `json:"endpoint"`
	Headers     map[string]string `json:"headers"`
	Token       string            `json:"token"`
	ConnectorID string            `json:"connector_id"`
}

// probeResponse adds the index threshold and, for a re-probe, the
// tool names that appeared or disappeared since the stored record.
type probeResponse struct {
	connectors.ProbeResult
	IndexThreshold int      `json:"index_threshold"`
	Added          []string `json:"added,omitempty"`
	Removed        []string `json:"removed,omitempty"`
}

func (h *connectorProbeAPI) probe(w http.ResponseWriter, r *http.Request) {
	var req probeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, probeBodyMax)).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON {endpoint, headers?, token?, name?} or {connector_id}")
		return
	}
	if req.ConnectorID != "" {
		h.probeStored(w, r, req.ConnectorID)
		return
	}
	u, err := url.Parse(req.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "endpoint must be an http or https URL")
		return
	}

	res := connectors.ProbeMCP(r.Context(), h.client, req.Endpoint, req.Headers, req.Token)
	threshold := h.finish(r.Context(), req.Name, &res)
	h.log.Info("connector probe", "host", u.Host, "status", res.Status, "tools", res.ToolCount)
	writeJSON(w, http.StatusOK, probeResponse{ProbeResult: res, IndexThreshold: threshold})
}

// probeStored re-probes a saved mcp connector and records the tool
// list on its row when the handshake succeeds.
func (h *connectorProbeAPI) probeStored(w http.ResponseWriter, r *http.Request, id string) {
	c, err := h.rows.Get(r.Context(), id)
	if err != nil {
		failConnector(w, h.log, err)
		return
	}
	if c.Kind != "mcp" {
		jsonError(w, http.StatusBadRequest, "bad_request", "only an mcp connector can be probed")
		return
	}
	res := h.mgr.ProbeStored(r.Context(), h.client, c, h.auth)
	threshold := h.finish(r.Context(), c.Name, &res)
	out := probeResponse{ProbeResult: res, IndexThreshold: threshold}
	if res.Status == connectors.ProbeOK {
		out.Added, out.Removed = connectors.DiffProbe(connectors.LastProbeOf(c), res)
		if err := h.rows.SetLastProbe(r.Context(), id, res.Record(h.now())); err != nil {
			h.log.Warn("connector probe record failed", "connector", c.Name, "error", err)
		}
	}
	h.log.Info("connector probe", "connector", c.Name, "status", res.Status, "tools", res.ToolCount,
		"added", len(out.Added), "removed", len(out.Removed))
	writeJSON(w, http.StatusOK, out)
}

// finish neutralizes the server's strings and fills the final names
// the tools would carry under name; it returns the index threshold.
func (h *connectorProbeAPI) finish(ctx context.Context, name string, res *connectors.ProbeResult) int {
	threshold := settings.DefaultMCPToolIndexThreshold
	if h.flags != nil {
		threshold = h.flags.MCPToolIndexThreshold(ctx)
	}
	res.Server.Name = missions.NeutralizeSlot(res.Server.Name)
	res.Server.Version = missions.NeutralizeSlot(res.Server.Version)
	for i := range res.Tools {
		res.Tools[i].Description = missions.NeutralizeSlot(res.Tools[i].Description)
	}
	if res.Status == connectors.ProbeOK && name != "" {
		var surface []string
		if h.toolset != nil {
			for _, d := range h.toolset.Tools() {
				surface = append(surface, d.Name)
			}
		}
		names := h.mgr.ProbeFinalNames(name, *res, surface, threshold)
		for i := range res.Tools {
			res.Tools[i].FinalName = names[res.Tools[i].Name]
		}
	}
	return threshold
}
