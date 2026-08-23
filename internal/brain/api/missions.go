package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/missions/executor"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
	"github.com/SumonMSelim/timothy/internal/secretstore"
)

// maxMissionAttachments caps how many PDFs a single mission create
// request may attach — bounds worst-case prompt size across every
// explore/plan/work turn (each attachment's markdown is rendered every
// turn, unlike chat where it's per-message).
const maxMissionAttachments = 8

// missionAttachmentStore is the slice of *attachments.Store missions
// need — mirrors chat.AttachmentStore exactly (chat.go's AttachmentStore).
type missionAttachmentStore interface {
	Get(ctx context.Context, id string) (attachments.Attachment, error)
	Open(ctx context.Context, id string) (io.ReadCloser, attachments.Attachment, error)
}

// registerMissions mounts the mission surface: served locally,
// missions are brain's domain like agents and connectors. nil store
// leaves the surface unmounted (404s), matching the memories/admin/
// agents gating pattern. agentReg resolves a mission's route/
// review_route/budget/approval_allowlist defaults from the chosen
// agent when the create request omits them. routeForRole resolves the
// route bound to the "default" system role (D-049) — an agent's empty
// route means "the default chain," but the gateway's /v1/stream
// requires a real, non-empty route name. codingExecutorDefault
// resolves the settings-configured harness default for a coding
// mission whose request omits one (D-051). resolveExecutorOptions
// backs GET /v1/missions/executor-options — a thin proxy over
// gwclient.ResolveRoute so the web UI can preview provider/model
// pairing before create without duplicating gateway resolve logic.
// nameMission generates a mission's short display name from its goal
// (chat.TitleOverGateway, the same mechanism a chat session's title
// uses) — nil (no gateway wiring) leaves every mission unnamed, same
// as any generation failure. topModels resolves the top-served
// provider/model per mission id from the cost ledger (D-05x's
// ledger.Aggregator.TopModelByMission) for the list response's
// top_model decoration — nil (no ledger wiring) omits the field.
// attachmentStore/markitdownURL back create-time PDF attachment
// conversion (see resolveAttachments) — a nil store or empty URL
// disables attachments. attachmentStore takes the narrow interface
// (not *attachments.Store) so the caller's own nil-box guard (a nil
// *attachments.Store boxed here would be a non-nil interface value)
// happens once, at the call site — same shape as chat.Service.SetAttachments.
func (a *API) registerMissions(handle func(pattern string, h http.Handler), store *missions.Store, driver *missions.Driver, notifier *missions.Notifier, agentReg *agents.Store, workspace *missions.Workspace, resolveSecret func(context.Context, string) (string, error), routeForRole func(context.Context, string) string, classify agents.Classify, codingExecutorDefault func(context.Context) string, resolveExecutorOptions func(context.Context, string, string) (*gwclient.ResolvedRoute, error), nameMission func(context.Context, string) string, topModels func(context.Context, []string) (map[string]ledger.ModelUsed, error), conns *connectors.Manager, attachmentStore missionAttachmentStore, markitdownURL string, destinationLookupStore destinationLookup) {
	if store == nil {
		return
	}
	h := &missionAPI{store: store, driver: driver, notifier: notifier, agentReg: agentReg, workspace: workspace, resolveSecret: resolveSecret, routeForRole: routeForRole, classify: classify, codingExecutorDefault: codingExecutorDefault, resolveExecutorOptions: resolveExecutorOptions, nameMission: nameMission, topModels: topModels, conns: conns, perms: a.perms, dir: a.dir, log: a.log, attachments: attachmentStore, markitdownURL: markitdownURL, markitdownHTTP: &http.Client{}, destinations: destinationLookupStore}
	handle("GET /v1/missions", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/missions", a.auth(http.HandlerFunc(h.create)))
	handle("POST /v1/missions/classify", a.auth(http.HandlerFunc(h.classifyGoal)))
	handle("GET /v1/missions/executor-options", a.auth(http.HandlerFunc(h.executorOptions)))
	handle("GET /v1/missions/{id}", a.auth(http.HandlerFunc(h.get)))
	handle("DELETE /v1/missions/{id}", a.auth(http.HandlerFunc(h.delete)))
	handle("GET /v1/missions/{id}/events", a.auth(http.HandlerFunc(h.events)))
	handle("POST /v1/missions/{id}/resume", a.auth(http.HandlerFunc(h.resume)))
	handle("POST /v1/missions/{id}/note", a.auth(http.HandlerFunc(h.note)))
	handle("POST /v1/missions/{id}/cancel", a.auth(http.HandlerFunc(h.cancel)))
	handle("POST /v1/missions/{id}/permission", a.auth(http.HandlerFunc(h.permission)))
	handle("GET /v1/missions/{id}/files", a.auth(http.HandlerFunc(h.files)))
	handle("GET /v1/missions/{id}/files/{path...}", a.auth(http.HandlerFunc(h.download)))
	handle("GET /v1/missions/{id}/archive", a.auth(http.HandlerFunc(h.archive)))
	handle("POST /v1/missions/{id}/push", a.auth(http.HandlerFunc(h.push)))
	handle("POST /v1/missions/{id}/pr", a.auth(http.HandlerFunc(h.pr)))
	handle("GET /v1/notifications", a.auth(http.HandlerFunc(h.notifications)))
	handle("POST /v1/notifications/{id}/read", a.auth(http.HandlerFunc(h.markRead)))
}

type missionAPI struct {
	store    *missions.Store
	driver   *missions.Driver
	notifier *missions.Notifier
	agentReg *agents.Store
	// workspace performs mission-directory git operations (Push,
	// Teardown for delete) outside the normal Drive loop.
	workspace *missions.Workspace
	// resolveSecret resolves a credential_ref to its plaintext value for
	// push; nil means no secret store is configured.
	resolveSecret func(context.Context, string) (string, error)
	// routeForRole resolves the route bound to a system role ("default"
	// for missions); nil or an unbound role fall back to "" and the
	// gateway's own no_route error, same as an unconfigured route.
	routeForRole func(context.Context, string) string
	// classify resolves an omitted create request's kind from its goal
	// (classifyKind below) and backs the /v1/missions/classify preview
	// endpoint; nil (no gateway wiring) makes every omitted kind default
	// straight to "coding", same as any classify error.
	classify agents.Classify
	// codingExecutorDefault resolves settings.ValueCodingExecutor for a
	// coding mission's create request that omits harness; nil (no
	// settings wiring) leaves it native.
	codingExecutorDefault func(context.Context) string
	// resolveExecutorOptions backs GET /v1/missions/executor-options;
	// nil (no gateway wiring) makes the endpoint 404.
	resolveExecutorOptions func(context.Context, string, string) (*gwclient.ResolvedRoute, error)
	// nameMission generates a mission's short display name from its
	// goal, fired async after create; nil (no gateway wiring) leaves
	// every mission unnamed, same as any generation failure.
	nameMission func(context.Context, string) string
	// topModels resolves the top-served provider/model per mission id
	// from the cost ledger; nil (no ledger wiring) omits top_model/
	// top_model_provider from list/get responses entirely.
	topModels func(context.Context, []string) (map[string]ledger.ModelUsed, error)
	// conns is the connector control plane: create() validates
	// connector_id names an enabled github-kind connector before
	// accepting repo_url. nil (no secret store, connectors disabled)
	// makes repo_url mission creation unavailable, same 400 shape as any
	// other rejected connector_id.
	conns *connectors.Manager
	// perms answers a mission's pending_permission — the same
	// PermissionResolver chat sessions use (A.perms), never a
	// mission-specific broker.
	perms PermissionResolver
	// dir deletes a mission's hidden session on mission delete — the
	// same Directory the top-level session routes use (a.dir), not a
	// mission-specific store.
	dir Directory
	log *slog.Logger
	// attachments resolves create-time PDF refs; nil disables mission
	// attachments entirely (same as chat's own attachments field).
	attachments missionAttachmentStore
	// markitdownURL is the sidecar's base URL for PDF conversion; ""
	// rejects any attachment with a 400 naming the missing sidecar.
	markitdownURL  string
	markitdownHTTP *http.Client
	// destinations resolves a create request's destination_ids against
	// the operator-owned destinations table (D-061's exfiltration
	// guard: an id must exist AND be enabled) — nil (destinations
	// disabled) rejects any non-empty destination_ids.
	destinations destinationLookup
}

// destinationLookup is the narrow slice of *destinations.Store the
// mission create handler needs to validate destination_ids —
// EnabledByID reports whether id names a real, enabled row (ok=false
// covers both "unknown id" and "disabled", both rejected identically
// by validateDestinationIDs below).
type destinationLookup interface {
	EnabledByID(ctx context.Context, id string) (ok bool, err error)
}

// routeExists reports whether name resolves to a real route via
// resolveExecutorOptions (gwclient.ResolveRoute's own existence check —
// a 404/not-found error means no such route) — the seam
// DefaultCodingRoute uses to prefer the operator's "coding" route over
// "default" only when it's actually configured. false, never an error,
// on any failure (no gateway wiring, gateway unreachable, unknown
// route): a missing preferred route must degrade silently, never 500
// mission creation.
func (h *missionAPI) routeExists(ctx context.Context, name string) bool {
	if h.resolveExecutorOptions == nil {
		return false
	}
	_, err := h.resolveExecutorOptions(ctx, name, "")
	return err == nil
}

// validateDestinationIDs rejects any id that doesn't name a real,
// enabled destinations row — the exfiltration guard (D-061): a mission
// create request only ever attaches operator-owned destinations, never
// an arbitrary string the model might have supplied. Lists every
// invalid id in one error, same spirit as an unknown-tool rejection
// naming what's actually valid.
func (h *missionAPI) validateDestinationIDs(ctx context.Context, ids []string) error {
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
			return fmt.Errorf("destination_ids: %w", err)
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

func failMission(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, missions.ErrNotFound):
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, missions.ErrBranchConflict):
		jsonError(w, http.StatusConflict, "branch_conflict", err.Error())
	case errors.Is(err, missions.ErrTerminal):
		jsonError(w, http.StatusConflict, "already_finished", err.Error())
	case errors.Is(err, missions.ErrNotTerminal):
		jsonError(w, http.StatusConflict, "not_terminal", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

// list serves GET /v1/missions, optionally narrowed by ?schedule_id=
// (a recurring schedule's fire history: every mission it spawned)
// and/or ?limit= (a positive result cap). Both are ignored when
// empty/absent — the original "every mission" behavior — and a
// malformed value is a 400 rather than a silently-empty filter.
func (h *missionAPI) list(w http.ResponseWriter, r *http.Request) {
	var filter missions.ListFilter
	if v := r.URL.Query().Get("schedule_id"); v != "" {
		if !validSessionID(v) {
			jsonError(w, http.StatusBadRequest, "bad_request", "schedule_id must be a UUID")
			return
		}
		filter.ScheduleID = v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			jsonError(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
		filter.Limit = n
	}
	rows, err := h.store.List(r.Context(), filter)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "missions_failed", err.Error())
		return
	}
	for i := range rows {
		rows[i] = sanitizeMission(rows[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"missions": h.decorateTopModels(r.Context(), rows)})
}

// sanitizeMission clears each attachment's Markdown before a mission
// row goes out over the API: the json tags exist for jsonb persistence,
// but a response carrying up to maxMissionAttachments*128KB of markdown
// on every list/get call would be wasteful and the client never reads it.
func sanitizeMission(m missions.Mission) missions.Mission {
	if len(m.Attachments) == 0 {
		return m
	}
	atts := make([]missions.MissionAttachment, len(m.Attachments))
	for i, a := range m.Attachments {
		a.Markdown = ""
		atts[i] = a
	}
	m.Attachments = atts
	return m
}

// missionResponse is missions.Mission plus fields the store itself
// has no business computing: top_model/top_model_provider come from
// the cost ledger (a different service's data), decorated on here at
// serve time rather than added to missions.Mission itself.
type missionResponse struct {
	missions.Mission
	TopModel         string `json:"top_model,omitempty"`
	TopModelProvider string `json:"top_model_provider,omitempty"`
}

// decorateTopModels adds top_model/top_model_provider to a page of
// missions in one ledger call — nil topModels (no ledger wiring) or a
// ledger error both degrade to every mission simply omitting the
// field, never a failed list/get.
func (h *missionAPI) decorateTopModels(ctx context.Context, rows []missions.Mission) []missionResponse {
	out := make([]missionResponse, len(rows))
	for i, m := range rows {
		out[i] = missionResponse{Mission: m}
	}
	if h.topModels == nil || len(rows) == 0 {
		return out
	}
	ids := make([]string, len(rows))
	for i, m := range rows {
		ids[i] = m.ID
	}
	top, err := h.topModels(ctx, ids)
	if err != nil {
		h.log.Warn("missions: top model lookup failed", "error", err)
		return out
	}
	for i := range out {
		if mu, ok := top[out[i].ID]; ok {
			out[i].TopModel, out[i].TopModelProvider = mu.Model, mu.Provider
		}
	}
	return out
}

type createMissionRequest struct {
	Goal string `json:"goal"`
	// Kind is optional: an empty value is classified from Goal (see
	// classifyKind) rather than rejected — the web UI's chip preview
	// resolves it before submit, but any other caller (a script, a
	// future integration) can still just send a goal and let the
	// server decide.
	Kind        string `json:"kind"`
	AgentID     string `json:"agent_id"`
	Route       string `json:"route"`
	ReviewRoute string `json:"review_route"`
	// PlanRoute, when set, is the route explore/plan/replan/review run
	// on instead of Route (see missions.Mission.PlanRoute for full
	// precedence). "" is the default: Route covers everything, exact
	// prior behavior. Not defaulted from an agent's route/review_route —
	// there is no agent-level plan_route equivalent.
	PlanRoute string `json:"plan_route"`
	// EscalationRoute, when set, is where worker turns move after a
	// failure or rework. Empty keeps escalation off — never defaulted,
	// so a route change is always an explicit choice.
	EscalationRoute string   `json:"escalation_route"`
	MaxIterations   int      `json:"max_iterations"`
	BudgetAmount    *float64 `json:"budget_amount"`
	// BudgetCurrency is optional; an omitted value defaults to "USD"
	// directly in create() below. The web UI, not this handler, is
	// responsible for sending the settings page's configured default
	// currency when the user hasn't explicitly overridden it — this
	// keeps the handler simple and stateless w.r.t. settings, UNLIKE
	// Harness below: the coding-executor default must apply on every
	// creation path (including the scheduler, which never goes through
	// the web UI), so create() reads settings for that one field.
	BudgetCurrency string `json:"budget_currency"`
	// Harness selects the execution strategy for a coding mission's
	// worker turns (D-051): "" applies the settings default,
	// "native" forces native (stored as ""), anything else must name a
	// registered harness. Rejected outright on kind=general.
	Harness string `json:"harness"`
	// Environment selects the per-language sandbox image (D-05x) a
	// coding mission's container runs: "" auto-detects from the repo at
	// provisioning (falling back to base), a registered key forces that
	// image. Unlike Harness there is no settings default. Rejected
	// outright on kind=general.
	Environment string `json:"environment"`
	// AutoApproveSafe defaults true (a pointer so an omitted field is
	// distinguishable from an explicit false) — missions run for hours
	// unattended, so auto-approving DangerSafe shell calls is the
	// sensible default; destructive commands always ask regardless.
	AutoApproveSafe *bool `json:"auto_approve_safe"`
	// RepoURL is a GitHub repo's https clone URL: when set, the mission
	// clones it instead of self-initializing an empty repo. Mutually
	// exclusive with a future repo_path option and coding-only, same as
	// Harness/Environment. Requires ConnectorID — v1 has no anonymous
	// clone path.
	RepoURL string `json:"repo_url"`
	// ConnectorID names a github-kind connectors row whose PAT
	// authenticates the clone; only meaningful alongside RepoURL.
	ConnectorID string `json:"connector_id"`
	// OnComplete is the operator's consent-at-create choice for what
	// happens when this mission reaches done: "" (default), "push", or
	// "push_pr". Requires RepoURL+ConnectorID (a github-connection
	// mission) and kind=coding — a model never decides this, only the
	// human choosing it here.
	OnComplete string `json:"on_complete"`
	// BranchPattern/CommitStyle override the settings-configured git
	// strategy defaults for this mission alone; "" (the default) applies
	// the settings default at provisioning/commit time. Validated the
	// same way settings.Store.SetValue validates the global default —
	// only known placeholders/styles, never model-decided.
	BranchPattern string `json:"branch_pattern"`
	CommitStyle   string `json:"commit_style"`
	// ParentMissionID, when set, makes this a follow-up mission: the
	// named mission must already be terminal (done/failed). Its
	// outcome digest (missions.OutcomeDigest) is snapshotted onto this
	// mission's ParentContext at create time, and its branch, when
	// reachable, becomes this mission's worktree base.
	ParentMissionID string `json:"parent_mission_id"`
	// Attachments name already-uploaded PDF documents (POST
	// /v1/attachments) to attach at create time — PDF-only, converted
	// to markdown once here (see resolveAttachments); images/audio are
	// unsupported for missions.
	Attachments []missionAttachmentInput `json:"attachments"`
	// DestinationIDs names operator-created destinations (email,
	// webhook) to deliver this mission's outcome digest to on the
	// terminal done transition — every id is validated to exist AND be
	// enabled at create time (see create() below); the model never
	// supplies or addresses a destination (D-061).
	DestinationIDs []string `json:"destination_ids"`
	// Light requests a mission that skips explore/plan/review (D-069):
	// kind=general only, rejected outright on kind=coding (explicit or
	// classified). Always the operator's explicit choice — the classify
	// preview only suggests a value, never sets it.
	Light bool `json:"light"`
}

// missionAttachmentInput names one already-uploaded attachment to
// resolve at create time; Name is display-only (the store itself
// doesn't carry a filename).
type missionAttachmentInput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// create validates the request, resolves route/review_route/budget
// defaults from the chosen agent when the request omits them, and
// hands off to Driver.Create — provisioning and the mission's first
// turn happen in the background; this returns {id} immediately.
func (h *missionAPI) create(w http.ResponseWriter, r *http.Request) {
	var req createMissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Goal == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "goal is required")
		return
	}
	switch req.Kind {
	case "":
		req.Kind = classifyKind(r.Context(), h.classify, req.Goal)
	case "coding", "general":
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", `kind must be "coding" or "general"`)
		return
	}
	if req.Light && req.Kind != "general" {
		jsonError(w, http.StatusBadRequest, "bad_request", "light is only valid for kind=general missions")
		return
	}
	if req.Harness == "native" {
		req.Harness = ""
	}
	switch {
	case req.Kind != "coding" && req.Harness != "":
		jsonError(w, http.StatusBadRequest, "bad_request", "harness is only valid for kind=coding missions")
		return
	case req.Kind == "coding" && req.Harness == "":
		if h.codingExecutorDefault != nil {
			req.Harness = h.codingExecutorDefault(r.Context())
		}
	case req.Harness != "":
		if _, ok := executor.Lookup(req.Harness); !ok {
			jsonError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("unknown harness %q", req.Harness))
			return
		}
	}
	switch {
	case req.Kind != "coding" && req.Environment != "":
		jsonError(w, http.StatusBadRequest, "bad_request", "environment is only valid for kind=coding missions")
		return
	case !missions.ValidEnvironment(req.Environment):
		jsonError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("unknown environment %q", req.Environment))
		return
	case req.Kind == "coding" && req.Environment == "":
		// Auto-detect (D-05x), resolved server-side at create time so
		// the environment is fixed before the sandbox container is ever
		// created: no worktree exists yet (it's provisioned after this
		// handler returns), so only the goal-keyword heuristic can fire
		// here — repo-marker detection has nothing to check against a
		// mission that hasn't been provisioned. Never wins over an
		// explicit request; "" stays "" (base) when nothing matches.
		req.Environment, _ = missions.DetectEnvironment("", req.Goal)
	}
	switch {
	case req.RepoURL != "" && req.Kind != "coding":
		jsonError(w, http.StatusBadRequest, "bad_request", "repo_url is only valid for kind=coding missions")
		return
	case req.RepoURL != "" && req.ConnectorID == "":
		jsonError(w, http.StatusBadRequest, "bad_request", "connector_id is required with repo_url")
		return
	case req.RepoURL == "" && req.ConnectorID != "":
		jsonError(w, http.StatusBadRequest, "bad_request", "connector_id is only valid alongside repo_url")
		return
	case req.RepoURL != "":
		if h.conns == nil {
			jsonError(w, http.StatusBadRequest, "bad_request", "connectors are not enabled")
			return
		}
		c, err := h.conns.Store().Get(r.Context(), req.ConnectorID)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", "unknown connector_id")
			return
		}
		if c.Kind != "github" {
			jsonError(w, http.StatusBadRequest, "bad_request", "connector_id must name a github-kind connector")
			return
		}
	}
	switch req.OnComplete {
	case "":
	case "push", "push_pr":
		if req.Kind != "coding" || req.RepoURL == "" || req.ConnectorID == "" {
			jsonError(w, http.StatusBadRequest, "bad_request", "on_complete requires repo_url and connector_id on a kind=coding mission")
			return
		}
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", `on_complete must be "", "push", or "push_pr"`)
		return
	}
	if req.BranchPattern != "" {
		if err := missions.ValidateBranchPattern(req.BranchPattern); err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}
	if err := missions.ValidateCommitStyle(req.CommitStyle); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.validateDestinationIDs(r.Context(), req.DestinationIDs); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var parentMissionID, parentContext string
	if req.ParentMissionID != "" {
		parent, err := h.store.Get(r.Context(), req.ParentMissionID)
		if err != nil {
			// Store.Get wraps any row-level failure (bad uuid, missing
			// row) as ErrNotFound — same "can't tell degraded-store
			// from truly-unknown-id" shape as the connector_id lookup
			// above, so every miss lands on the same 400.
			jsonError(w, http.StatusBadRequest, "bad_request", "parent mission not found")
			return
		}
		if !parent.Phase.Terminal() {
			jsonError(w, http.StatusConflict, "parent_not_terminal", "parent mission is not finished")
			return
		}
		events, err := h.store.Events(r.Context(), req.ParentMissionID)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		parentMissionID = parent.ID
		parentContext = missions.OutcomeDigest(parent, events, parent.Phase, parent.FailureReason)
	}
	missionAtts, ok := h.resolveAttachments(w, r.Context(), req.Attachments)
	if !ok {
		return
	}
	// Resolve even with an empty AgentID: ResolveByID("") falls back to
	// the default agent, same as chat sessions that don't pick one — a
	// mission created without an explicit agent still needs a real
	// route, not an empty string the gateway will reject. By id, not
	// name: req.AgentID is the mission row's agent_id FK value (the
	// picker sends a.id), unlike chat's session.agent which is a name.
	var promptOverlay string
	var knowledge []string
	if h.agentReg != nil {
		if a, ok := h.agentReg.ResolveByID(r.Context(), req.AgentID); ok {
			if req.Route == "" {
				req.Route = a.Route
			}
			if req.ReviewRoute == "" {
				req.ReviewRoute = a.ReviewRoute
			}
			if req.BudgetAmount == nil {
				req.BudgetAmount = a.BudgetUSD
			}
			promptOverlay = a.PromptOverlay
			knowledge = a.Knowledge
		}
	}
	// An agent's route (and this handler's own fallback) can still be
	// "" — that's each agent's shorthand for "the default chain," same
	// as chat's own default-role fallback — but the gateway requires a
	// real route name, so the substitution has to happen somewhere
	// concrete.
	defaultRoute := ""
	if h.routeForRole != nil {
		defaultRoute = h.routeForRole(r.Context(), "default")
	}
	if req.Route == "" {
		if req.Kind == "coding" {
			req.Route = missions.DefaultCodingRoute(r.Context(), h.routeExists, defaultRoute)
		} else {
			req.Route = defaultRoute
		}
	}
	if req.ReviewRoute == "" {
		// Review is an oversight phase: an explicit plan_route covers it
		// unless review_route itself was set. Defaulting to defaultRoute
		// here would bake a masking value into the row and plan_route
		// would never reach review (runner precedence: review_route >
		// plan_route > route).
		if req.PlanRoute != "" {
			req.ReviewRoute = req.PlanRoute
		} else {
			req.ReviewRoute = defaultRoute
		}
	}

	autoApproveSafe := true
	if req.AutoApproveSafe != nil {
		autoApproveSafe = *req.AutoApproveSafe
	}
	budgetCurrency := req.BudgetCurrency
	if budgetCurrency == "" {
		budgetCurrency = "USD"
	}
	m := missions.Mission{
		Goal: req.Goal, Kind: req.Kind, AgentID: req.AgentID,
		Route: req.Route, ReviewRoute: req.ReviewRoute, PlanRoute: req.PlanRoute, EscalationRoute: req.EscalationRoute,
		MaxIterations: req.MaxIterations, BudgetAmount: req.BudgetAmount, BudgetCurrency: budgetCurrency,
		AutoApproveSafe: autoApproveSafe, PromptOverlay: promptOverlay, Knowledge: knowledge, Harness: req.Harness, Environment: req.Environment,
		RepoURL: req.RepoURL, ConnectorID: req.ConnectorID, OnComplete: req.OnComplete,
		BranchPattern: req.BranchPattern, CommitStyle: req.CommitStyle,
		ParentMissionID: parentMissionID, ParentContext: parentContext,
		Attachments: missionAtts, DestinationIDs: req.DestinationIDs, Light: req.Light,
	}
	id, err := h.driver.Create(r.Context(), m)
	if err != nil {
		failMission(w, err)
		return
	}
	h.generateName(id, req.Goal)
	// Re-read rather than echo req/m back: Driver.Create's own
	// provisioning (ensureProvisioned) can mutate the row before this
	// handler ever sees it again — environment auto-detection in
	// particular resolves before Create is called here, but the row is
	// still the source of truth, and a future provisioning-time write
	// must not silently go missing from the create response the way
	// environment did before this fix. Best-effort: the mission was
	// just created successfully, so a read failure here is surprising
	// enough to log, but must not turn a successful create into an
	// error response — id is still valid and GET /v1/missions/{id}
	// works regardless.
	created, err := h.store.Get(r.Context(), id)
	if err != nil {
		h.log.Warn("mission: re-read after create failed", "mission_id", id, "error", err)
		writeJSON(w, http.StatusCreated, map[string]string{"id": id})
		return
	}
	writeJSON(w, http.StatusCreated, sanitizeMission(created))
}

// resolveAttachments validates and converts a create request's PDF
// refs into MissionAttachments — writes any error response itself and
// returns ok=false, mirroring chat.validateAttachments' shape but as a
// method so it can write directly (create()'s other validation blocks
// use jsonError+return rather than a returned error). Empty input
// returns nil, true without touching the store, same as chat's own
// zero-ids fast path.
func (h *missionAPI) resolveAttachments(w http.ResponseWriter, ctx context.Context, in []missionAttachmentInput) ([]missions.MissionAttachment, bool) {
	if len(in) == 0 {
		return nil, true
	}
	if h.attachments == nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "attachments are not enabled")
		return nil, false
	}
	if len(in) > maxMissionAttachments {
		jsonError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("too many attachments (max %d)", maxMissionAttachments))
		return nil, false
	}
	if h.markitdownURL == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "pdf attachments require the markitdown sidecar (MARKITDOWN_URL)")
		return nil, false
	}
	out := make([]missions.MissionAttachment, 0, len(in))
	for _, input := range in {
		att, err := h.attachments.Get(ctx, input.ID)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("attachment %q not found", input.ID))
			return nil, false
		}
		if att.Mime != "application/pdf" {
			jsonError(w, http.StatusBadRequest, "bad_request", "only document attachments are supported for missions")
			return nil, false
		}
		r, _, err := h.attachments.Open(ctx, att.ID)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return nil, false
		}
		raw, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return nil, false
		}
		md, err := markitdown.Convert(ctx, h.markitdownHTTP, h.markitdownURL, att.ID+".pdf", att.Mime, raw)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return nil, false
		}
		out = append(out, missions.MissionAttachment{
			ID: att.ID, Mime: att.Mime, Name: input.Name, Markdown: markitdown.TruncateMarkdown(md),
		})
	}
	return out, true
}

// generateName fires the mission's one-shot naming call in the
// background — never blocks or fails create, mirrors chat.autoTitle's
// fire-and-forget shape exactly. Detached from the request context
// (context.Background()) so a client disconnect right after create
// doesn't cancel it; nameMission carries its own short timeout
// (chat.TitleOverGateway). SetNameIfEmpty's own guard is what makes
// this safe even if called twice for the same id.
func (h *missionAPI) generateName(id, goal string) {
	if h.nameMission == nil {
		return
	}
	go func() {
		name := h.nameMission(context.Background(), goal)
		if name == "" {
			h.log.Warn("mission: name generation returned empty", "mission_id", id)
			return
		}
		if err := h.store.SetNameIfEmpty(context.Background(), id, name); err != nil {
			h.log.Warn("mission: set name failed", "mission_id", id, "error", err)
		}
	}()
}

// classifyKind decides how a mission's work happens when the create
// request omits kind. Biased hard toward "coding" on anything short of
// an unambiguous "general" reply: a coding goal misread as general
// loses branch/diff/rollback safety (the worse failure), while a
// general goal misread as coding only wastes an empty worktree — so
// every ambiguous or failed classification lands on the cheap side of
// the error. nil classify (no gateway wiring) takes the same default.
func classifyKind(ctx context.Context, classify agents.Classify, goal string) string {
	if classify == nil {
		return "coding"
	}
	prompt := "Decide how this mission's work happens. Answer with exactly one word.\n" +
		"coding — the goal requires creating or modifying code, scripts, or configuration in a project/repository.\n" +
		"general — everything else (documents, analysis, data gathering, operations).\n\n" +
		"Goal: " + goal
	reply, err := classify(ctx, prompt)
	if err != nil {
		return "coding"
	}
	reply = strings.ToLower(reply)
	// "general" wins only when "coding" is absent from the reply.
	if strings.Contains(reply, "general") && !strings.Contains(reply, "coding") {
		return "general"
	}
	return "coding"
}

// classifyLight decides whether a general-kind goal is single-pass
// (deliverable in one worker turn, no plan or artifacts needed) — only
// ever a suggestion for the web UI's toggle default; create() still
// requires the operator's explicit light flag. Biased false on any
// ambiguity or classify failure: a light suggestion is only useful when
// confident, and defaulting the toggle on for a multi-step goal would
// silently drop review/artifact checks the operator likely wants.
func classifyLight(ctx context.Context, classify agents.Classify, goal string) bool {
	if classify == nil {
		return false
	}
	prompt := "Is this goal a single-pass task deliverable in one response — a read, summary, lookup, or short write-up with no multi-step plan or file artifacts? Answer with exactly one word, yes or no.\n\n" +
		"Goal: " + goal
	reply, err := classify(ctx, prompt)
	if err != nil {
		return false
	}
	reply = strings.ToLower(reply)
	return strings.Contains(reply, "yes") && !strings.Contains(reply, "no")
}

// classifyGoal serves POST /v1/missions/classify: the same
// classification create() falls back to when kind is omitted, exposed
// standalone so the web UI's chip preview can show a mission's inferred
// kind (and, for a general goal, a light suggestion) before submit
// without actually creating anything.
func (h *missionAPI) classifyGoal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Goal string `json:"goal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Goal == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "goal is required")
		return
	}
	kind := classifyKind(r.Context(), h.classify, req.Goal)
	light := kind == "general" && classifyLight(r.Context(), h.classify, req.Goal)
	writeJSON(w, http.StatusOK, map[string]any{"kind": kind, "light": light})
}

// executorOption is one registered harness's live pairing preview for
// MissionForm's Executor select — the first usable chain entry
// gwclient.ResolveRoute(route, harness) reports, or Reason explaining
// why none is.
type executorOption struct {
	Harness      string `json:"harness"`
	Usable       bool   `json:"usable"`
	ProviderName string `json:"provider_name,omitempty"`
	Model        string `json:"model,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// executorOptions serves GET /v1/missions/executor-options?route=<name>,
// a thin proxy over the gateway's resolve endpoint (D-051): for every
// registered harness it reports the first usable chain entry (or why
// none is) so the web UI can show "runs via provider/model" or disable
// an incompatible choice before create. route defaults to the
// "default" system role's route, same fallback create() itself applies.
func (h *missionAPI) executorOptions(w http.ResponseWriter, r *http.Request) {
	if h.resolveExecutorOptions == nil {
		jsonError(w, http.StatusNotFound, "not_found", "executor options are not enabled")
		return
	}
	route := r.URL.Query().Get("route")
	if route == "" && h.routeForRole != nil {
		route = h.routeForRole(r.Context(), "default")
	}
	harnesses := executor.Registered()
	options := make([]executorOption, 0, len(harnesses))
	for _, harness := range harnesses {
		opt := executorOption{Harness: harness}
		resolved, err := h.resolveExecutorOptions(r.Context(), route, harness)
		switch {
		case err != nil:
			opt.Reason = err.Error()
		case resolved == nil || len(resolved.Entries) == 0:
			opt.Reason = "route has no chain entries"
		default:
			// No entry usable: surface the first entry's own skip_reason
			// (e.g. the responses-probe gate's "endpoint does not serve
			// /v1/responses…") so the MissionForm tooltip stays
			// actionable instead of falling back to a generic string —
			// only when that reason is itself empty does the generic
			// string apply.
			opt.Reason = "no usable provider for this route"
			if resolved.Entries[0].SkipReason != "" {
				opt.Reason = resolved.Entries[0].SkipReason
			}
			for _, e := range resolved.Entries {
				if e.Usable {
					opt.Usable, opt.ProviderName, opt.Model, opt.Reason = true, e.ProviderName, e.Model, ""
					break
				}
			}
		}
		options = append(options, opt)
	}
	writeJSON(w, http.StatusOK, map[string]any{"options": options})
}

func (h *missionAPI) get(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.decorateTopModels(r.Context(), []missions.Mission{sanitizeMission(m)})[0])
}

// delete permanently removes a terminal mission: its row (Store.Delete
// cascades mission_events/notifications), its hidden session, and its
// on-disk workspace. Only a terminal mission (done/error, which
// includes cancelled) may be deleted — 409 not_terminal otherwise — so
// a live mission's row can never vanish out from under a running
// Driver.Advance. Session and workspace cleanup are best-effort past
// that point: the mission row is already gone, so a failure here is
// logged, never turned into an error response.
func (h *missionAPI) delete(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Delete(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.SessionID != "" && h.dir != nil {
		if err := h.dir.Delete(r.Context(), m.SessionID); err != nil {
			h.log.Warn("mission delete: hidden session cleanup failed", "mission_id", m.ID, "session_id", m.SessionID, "error", err)
		}
	}
	if m.Workspace != "" && h.workspace != nil {
		if err := h.workspace.Teardown(r.Context(), m.Workspace, m.Worktree, m.Kind); err != nil {
			h.log.Warn("mission delete: workspace cleanup failed", "mission_id", m.ID, "workspace", m.Workspace, "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *missionAPI) events(w http.ResponseWriter, r *http.Request) {
	events, err := h.store.Events(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// resumeAnswerCap bounds the answer text carried into the progress note
// and the mission.answered event payload — same cap as other bounded
// event-payload fields in this package (e.g. push_failed's reason).
const resumeAnswerCap = 500

// resume handles POST /v1/missions/{id}/resume. An optional JSON body
// {"answer": "..."} carries the human's reply to a worker's blocked
// question: it's appended as a progress note (so the next worker
// session's packet renders it, same as any other progress note) and as
// a mission.answered event, BEFORE the resume signal — so the worker
// that runs next already has the answer in its packet. An empty or
// absent body (or one with an empty/missing answer) resumes exactly as
// before this existed; only malformed JSON is rejected.
func (h *missionAPI) resume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON with an optional answer field")
		return
	}
	id := r.PathValue("id")
	if body.Answer != "" {
		answer := missions.NeutralizeSlot(truncateAnswer(body.Answer, resumeAnswerCap))
		if err := h.store.AppendProgress(r.Context(), id, "Answer to your question: "+answer); err != nil {
			h.log.Warn("mission: record answer progress note failed", "mission_id", id, "error", err)
		}
		if err := h.store.AppendEvent(r.Context(), id, "mission.answered", map[string]any{"answer": answer}); err != nil {
			h.log.Warn("mission: record mission.answered event failed", "mission_id", id, "error", err)
		}
	}
	if err := h.driver.Signal(r.Context(), id, missions.InputResume); err != nil {
		failMission(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// truncateAnswer caps an answer at n runes-as-bytes, matching the
// truncate helper's shape elsewhere in this codebase (missions.truncate
// is unexported to this package).
func truncateAnswer(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (h *missionAPI) cancel(w http.ResponseWriter, r *http.Request) {
	if err := h.driver.Signal(r.Context(), r.PathValue("id"), missions.InputCancel); err != nil {
		failMission(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// note handles POST /v1/missions/{id}/note: operator guidance injected
// into a running mission via the existing progress-note pipeline — no
// state transition, no driver signal. The next worker turn's packet
// renders it like any other progress note (Render, packet.go), so
// steering takes effect on its own without waking the mission early.
func (h *missionAPI) note(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON with a non-empty text field")
		return
	}
	id := r.PathValue("id")
	m, err := h.store.Get(r.Context(), id)
	if err != nil {
		failMission(w, err)
		return
	}
	if m.Phase.Terminal() {
		jsonError(w, http.StatusConflict, "already_finished", missions.ErrTerminal.Error())
		return
	}
	note := missions.NeutralizeSlot(truncateAnswer(body.Text, resumeAnswerCap))
	if err := h.store.AppendEvent(r.Context(), id, "mission.steered", map[string]any{"note": note}); err != nil {
		h.log.Warn("mission: record mission.steered event failed", "mission_id", id, "error", err)
	}
	if err := h.store.AppendProgress(r.Context(), id, "Operator note: "+note); err != nil {
		h.log.Warn("mission: record operator note progress failed", "mission_id", id, "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// permission answers a mission's pending_permission — the SAME
// permission broker chat sessions use (a.perms), keyed by the
// broker-issued id stored on the mission row rather than a session id.
// No mission-specific broker exists; a mission's pending_permission is
// only ever set by the same interactive-prompt code path chat already
// goes through.
func (h *missionAPI) permission(w http.ResponseWriter, r *http.Request) {
	if h.perms == nil {
		jsonError(w, http.StatusNotFound, "not_found", "permissions are not enabled")
		return
	}
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.PendingPermission == "" {
		jsonError(w, http.StatusNotFound, "not_found", "this mission has no pending permission request")
		return
	}
	var body struct {
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON with a decision field")
		return
	}
	switch body.Decision {
	case "once", "session", "deny":
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", `decision must be "once", "session", or "deny"`)
		return
	}
	if !h.perms.Resolve(m.PendingPermission, body.Decision) {
		jsonError(w, http.StatusNotFound, "not_found", "unknown or already-answered permission request")
		return
	}
	// Best-effort: the decision already took effect (Resolve above
	// unparked the turn); a failure to log it is a missing Timeline
	// entry, not a wrong mission state, so it must not fail this request.
	if err := h.store.AppendEvent(r.Context(), m.ID, "mission.permission_answered",
		map[string]any{"tool": m.PendingPermissionTool, "decision": body.Decision}); err != nil {
		h.log.Warn("mission: record permission_answered event failed", "mission_id", m.ID, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// declaredArtifacts builds the workspace-relative, filepath.Clean'ed
// set of paths the mission's plan units declare — files.go itself
// never imports or knows about Spec, keeping the two decoupled.
func declaredArtifacts(spec missions.Spec) map[string]bool {
	declared := map[string]bool{}
	for _, u := range spec.Units {
		for _, a := range u.Artifacts {
			declared[filepath.Clean(a)] = true
		}
	}
	return declared
}

func (h *missionAPI) files(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.Workspace == "" {
		jsonError(w, http.StatusNotFound, "no_workspace", "this mission has no workspace")
		return
	}
	entries, truncated, err := missions.ListFiles(m.WorkRoot(), declaredArtifacts(m.Spec))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "files_failed", err.Error())
		return
	}
	if entries == nil {
		entries = []missions.FileEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": entries, "truncated": truncated})
}

// download streams one workspace file. Content-Type is always forced
// to application/octet-stream + nosniff — this is same-origin SPA
// traffic, and a worker-authored file (which could be arbitrary HTML)
// must never be rendered by the browser.
func (h *missionAPI) download(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.Workspace == "" {
		jsonError(w, http.StatusNotFound, "no_workspace", "this mission has no workspace")
		return
	}
	rel := r.PathValue("path")
	f, fi, err := missions.OpenFile(m.WorkRoot(), rel)
	if err != nil {
		// A containment violation and a plain not-found both read as 404
		// — the reason must never leak through a different status.
		jsonError(w, http.StatusNotFound, "not_found", "file not found")
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(rel)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// archive streams the mission's whole workspace as a zip. Headers are
// sent before WriteArchive begins, so a mid-stream failure can only be
// logged — the HTTP status is already committed.
func (h *missionAPI) archive(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.Workspace == "" {
		jsonError(w, http.StatusNotFound, "no_workspace", "this mission has no workspace")
		return
	}
	id := m.ID
	if len(id) > 8 {
		id = id[:8]
	}
	name := fmt.Sprintf("mission-%s.zip", id)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Content-Type", "application/zip")
	if err := missions.WriteArchive(m.WorkRoot(), w); err != nil {
		h.log.Warn("mission archive stream failed", "mission_id", m.ID, "error", err)
	}
}

// pushRefPattern bounds credential_ref the same way any other operator
// identifier is bounded elsewhere in this API — a defensive shape
// check, not a secret-store lookup.
var pushRefPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,128}$`)

// pushTokenError is the sentinel resolvePushToken returns on any
// resolution failure, carrying the exact HTTP status/code/message the
// caller should surface — push and pr share this one resolution path,
// so both endpoints report identical errors for identical causes.
type pushTokenError struct {
	status  int
	code    string
	message string
}

func (e *pushTokenError) Error() string { return e.message }

// resolvePushToken resolves the token that authenticates a push:
// credentialRef (from the request body) when non-empty always wins —
// an explicit override — otherwise, a github-connection mission (m has
// a connector_id) resolves the connector's own PAT; a mission with
// neither is a 400, not a fall-through to "no credential configured".
func (h *missionAPI) resolvePushToken(ctx context.Context, m missions.Mission, credentialRef string) (string, error) {
	if credentialRef != "" {
		if !pushRefPattern.MatchString(credentialRef) {
			return "", &pushTokenError{http.StatusBadRequest, "bad_request", "credential_ref must match ^[A-Za-z0-9_./-]{1,128}$"}
		}
		if h.resolveSecret == nil {
			return "", &pushTokenError{http.StatusNotFound, "not_found", "secret store not configured"}
		}
		token, err := h.resolveSecret(ctx, credentialRef)
		if err != nil {
			if errors.Is(err, secretstore.ErrNotFound) {
				return "", &pushTokenError{http.StatusNotFound, "secret_not_found", "no secret configured for that credential_ref"}
			}
			return "", &pushTokenError{http.StatusBadGateway, "push_failed", "failed to resolve credential"}
		}
		return token, nil
	}
	if m.ConnectorID == "" {
		return "", &pushTokenError{http.StatusBadRequest, "bad_request", "credential_ref is required and must match ^[A-Za-z0-9_./-]{1,128}$"}
	}
	if h.conns == nil {
		return "", &pushTokenError{http.StatusBadRequest, "bad_request", "connectors are not enabled"}
	}
	c, err := h.conns.Store().Get(ctx, m.ConnectorID)
	if err != nil {
		return "", &pushTokenError{http.StatusBadRequest, "bad_request", "unknown connector_id"}
	}
	if c.Kind != "github" {
		return "", &pushTokenError{http.StatusBadRequest, "bad_request", "connector_id must name a github-kind connector"}
	}
	if !c.Enabled {
		return "", &pushTokenError{http.StatusBadRequest, "bad_request", "connector is disabled"}
	}
	if h.resolveSecret == nil {
		return "", &pushTokenError{http.StatusNotFound, "not_found", "secret store not configured"}
	}
	token, err := h.resolveSecret(ctx, c.CredentialRef)
	if err != nil {
		return "", &pushTokenError{http.StatusBadGateway, "push_failed", "failed to resolve connector credential"}
	}
	return token, nil
}

// pushMissionBranch pushes m's branch to its worktree's origin remote
// with token, recording mission.pushed/mission.push_failed either way —
// missions.Completer.PushBranch does the actual push and event
// recording, the SAME code the driver's auto-fire-on-done hook calls,
// so a manual push and an auto-fired push can never diverge in what
// they do or which events land on the Timeline. Event-record failures
// here are logged, never turned into a failed response — the push
// itself already succeeded or failed on its own terms.
func (h *missionAPI) pushMissionBranch(ctx context.Context, m missions.Mission, token string) (host string, err error) {
	host, err = h.completer().PushBranch(ctx, m, token)
	if err != nil && !errors.Is(err, missions.ErrRemoteUnsupported) && !errors.Is(err, missions.ErrPushRejected) {
		h.log.Warn("mission: push failed", "mission_id", m.ID, "error", err)
	}
	return host, err
}

// completer builds a missions.Completer wired to this handler's own
// workspace/store — cheap, stateless besides those two pointers, so a
// fresh one per call is simplest; the driver holds its own long-lived
// Completer for the auto-fire path.
func (h *missionAPI) completer() *missions.Completer {
	return missions.NewCompleter(h.workspace, h.store, nil, h.prSource())
}

// prSource adapts h.conns (nil-safe) to missions.PRSource.
func (h *missionAPI) prSource() missions.PRSource {
	if h.conns == nil {
		return nil
	}
	return connsPRSource{h.conns}
}

// connsPRSource adapts *connectors.Manager to missions.PRSource — the
// PR endpoint's own adapter; the driver's SetPRSource wiring in
// cmd/brain/main.go builds an equivalent one.
type connsPRSource struct {
	conns *connectors.Manager
}

func (c connsPRSource) DefaultBranch(ctx context.Context, connectorID, owner, repo string) (string, error) {
	repoInfo, err := c.conns.GetRepo(ctx, connectorID, owner, repo)
	if err != nil {
		return "", err
	}
	return repoInfo.DefaultBranch, nil
}

func (c connsPRSource) CreatePR(ctx context.Context, connectorID, owner, repo, title, head, base, body string) (string, int, error) {
	created, err := c.conns.CreatePR(ctx, connectorID, owner, repo, title, head, base, body)
	if err != nil {
		return "", 0, err
	}
	return created.HTMLURL, created.Number, nil
}

// pushStatusCode maps a Push error to the HTTP status/code pair the
// push/pr endpoints both report — shared so a rejected/unsupported
// remote reads identically from either entry point.
func pushStatusCode(err error) (status int, code string) {
	switch {
	case errors.Is(err, missions.ErrRemoteUnsupported):
		return http.StatusBadRequest, "remote_unsupported"
	case errors.Is(err, missions.ErrPushRejected):
		return http.StatusConflict, "push_rejected"
	default:
		return http.StatusBadGateway, "push_failed"
	}
}

// push pushes the mission's branch to its worktree's origin remote.
// credential_ref in the body is optional for a github-connection
// mission (absent resolves the connector's own PAT); required
// otherwise. Guards (kind, branch, worktree presence) are Go code,
// never a prompt — only coding missions with a live worktree are ever
// pushable.
func (h *missionAPI) push(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if reason := missions.NotPushable(m); reason != "" {
		jsonError(w, http.StatusBadRequest, "not_pushable", reason)
		return
	}
	var req struct {
		CredentialRef string `json:"credential_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	token, err := h.resolvePushToken(r.Context(), m, req.CredentialRef)
	if err != nil {
		var te *pushTokenError
		if errors.As(err, &te) {
			jsonError(w, te.status, te.code, te.message)
			return
		}
		jsonError(w, http.StatusBadGateway, "push_failed", err.Error())
		return
	}
	host, pushErr := h.pushMissionBranch(r.Context(), m, token)
	if pushErr != nil {
		status, code := pushStatusCode(pushErr)
		jsonError(w, status, code, pushErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"branch": m.Branch, "remote_host": host})
}

// pr handles POST /v1/missions/{id}/pr: github-connection missions
// only (400 otherwise). missions.Completer.OpenPR does the actual push,
// default-branch lookup, PR create, and mission.pr_opened event
// recording — the SAME code the driver's auto-fire-on-done hook calls
// for on_complete="push_pr", so a manual PR and an auto-fired one can
// never diverge. A re-call that finds the existing PR (CreatePR's
// already-exists path) still appends a SECOND mission.pr_opened event,
// tolerated as a harmless duplicate (the Timeline shows one extra
// identical row) rather than adding a "did we already record this PR"
// lookup.
func (h *missionAPI) pr(w http.ResponseWriter, r *http.Request) {
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.ConnectorID == "" || m.RepoURL == "" {
		jsonError(w, http.StatusBadRequest, "not_pr_able", "only github-connection missions can open a pull request")
		return
	}
	if reason := missions.NotPushable(m); reason != "" {
		jsonError(w, http.StatusBadRequest, "not_pushable", reason)
		return
	}
	if h.conns == nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "connectors are not enabled")
		return
	}
	if _, _, ok := missions.ParseGitHubRepoURL(m.RepoURL); !ok {
		jsonError(w, http.StatusBadRequest, "bad_request", "mission repo_url is not a recognizable github https clone URL")
		return
	}

	token, err := h.resolvePushToken(r.Context(), m, "")
	if err != nil {
		var te *pushTokenError
		if errors.As(err, &te) {
			jsonError(w, te.status, te.code, te.message)
			return
		}
		jsonError(w, http.StatusBadGateway, "push_failed", err.Error())
		return
	}
	url, number, err := h.completer().OpenPR(r.Context(), m, token)
	if err != nil {
		if errors.Is(err, missions.ErrRemoteUnsupported) || errors.Is(err, missions.ErrPushRejected) {
			status, code := pushStatusCode(err)
			jsonError(w, status, code, err.Error())
			return
		}
		jsonError(w, http.StatusBadGateway, "pr_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "number": number})
}

func (h *missionAPI) notifications(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		jsonError(w, http.StatusNotFound, "not_found", "notifications are not enabled")
		return
	}
	rows, err := h.notifier.List(r.Context())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "notifications_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": rows})
}

func (h *missionAPI) markRead(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		jsonError(w, http.StatusNotFound, "not_found", "notifications are not enabled")
		return
	}
	if err := h.notifier.MarkRead(r.Context(), r.PathValue("id")); err != nil {
		jsonError(w, http.StatusInternalServerError, "mark_read_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
