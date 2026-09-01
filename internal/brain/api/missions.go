package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/destinations"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/missions/executor"
	pdfgenservice "github.com/SumonMSelim/timothy/internal/brain/pdfgen"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
	"github.com/SumonMSelim/timothy/internal/platform/pdfgen"
	"github.com/SumonMSelim/timothy/internal/secretstore"
)

// maxMissionAttachments caps how many PDFs a single mission create
// request may attach — bounds worst-case prompt size across every
// discover/plan/work turn (each attachment's markdown is rendered every
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
// mission whose request omits one (D-051). resolveRoute is
// gwclient.Client.ResolveRoute itself — backs GET
// /v1/missions/executor-options (preview provider/model pairing per
// harness before create) and GET /v1/missions/execution-plan (the
// full per-phase resolution preview) so the web UI never duplicates
// gateway resolve logic.
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
func (a *API) registerMissions(handle func(pattern string, h http.Handler), store *missions.Store, driver *missions.Driver, notifier *missions.Notifier, agentReg *agents.Store, workspace *missions.Workspace, resolveSecret func(context.Context, string) (string, error), routeForRole func(context.Context, string) string, classify agents.Classify, codingExecutorDefault func(context.Context) string, resolveRoute func(context.Context, string, string) (*gwclient.ResolvedRoute, error), nameMission func(context.Context, string) string, topModels func(context.Context, []string) (map[string]ledger.ModelUsed, error), conns *connectors.Manager, attachmentStore missionAttachmentStore, markitdownURL string, pdfService *pdfgenservice.Service, kbStore *kb.Store, kbIngest kbIngester, extractGitHubDestination func(context.Context, string) chat.GitHubDestinationProposal) {
	if store == nil {
		return
	}
	var resolveAgentRoute func(context.Context, string) (string, bool)
	var resolveAgentHarness func(context.Context, string) (string, bool)
	if agentReg != nil {
		resolveAgentRoute = func(ctx context.Context, id string) (string, bool) {
			if id == "" {
				return "", false
			}
			a, ok := agentReg.ResolveByID(ctx, id)
			if !ok || a.Route == "" {
				return "", false
			}
			return a.Route, true
		}
		resolveAgentHarness = func(ctx context.Context, id string) (string, bool) {
			if id == "" {
				return "", false
			}
			a, ok := agentReg.ResolveByID(ctx, id)
			if !ok || a.Harness == "" {
				return "", false
			}
			return a.Harness, true
		}
	}
	h := &missionAPI{store: store, driver: driver, notifier: notifier, agentReg: agentReg, resolveAgentRoute: resolveAgentRoute, resolveAgentHarness: resolveAgentHarness, workspace: workspace, resolveSecret: resolveSecret, routeForRole: routeForRole, classify: classify, codingExecutorDefault: codingExecutorDefault, resolveRoute: resolveRoute, nameMission: nameMission, topModels: topModels, conns: conns, perms: a.perms, dir: a.dir, log: a.log, attachments: attachmentStore, markitdownURL: markitdownURL, markitdownHTTP: &http.Client{}, pdfService: pdfService, resolveReferences: a.svc.ResolveReferences, kbStore: kbStore, kbIngest: kbIngest, extractGitHubDestination: extractGitHubDestination}
	handle("GET /v1/missions", a.auth(http.HandlerFunc(h.list)))
	handle("POST /v1/missions", a.auth(http.HandlerFunc(h.create)))
	handle("POST /v1/missions/classify", a.auth(http.HandlerFunc(h.classifyGoal)))
	handle("POST /v1/missions/detect-destination", a.auth(http.HandlerFunc(h.detectDestination)))
	handle("GET /v1/missions/executor-options", a.auth(http.HandlerFunc(h.executorOptions)))
	handle("GET /v1/missions/execution-plan", a.auth(http.HandlerFunc(h.executionPlan)))
	handle("GET /v1/missions/{id}", a.auth(http.HandlerFunc(h.get)))
	handle("DELETE /v1/missions/{id}", a.auth(http.HandlerFunc(h.delete)))
	handle("GET /v1/missions/{id}/events", a.auth(http.HandlerFunc(h.events)))
	handle("POST /v1/missions/{id}/resume", a.auth(http.HandlerFunc(h.resume)))
	handle("POST /v1/missions/{id}/note", a.auth(http.HandlerFunc(h.note)))
	handle("POST /v1/missions/{id}/cancel", a.auth(http.HandlerFunc(h.cancel)))
	handle("POST /v1/missions/{id}/permission", a.auth(http.HandlerFunc(h.permission)))
	handle("POST /v1/missions/{id}/approve-plan", a.auth(http.HandlerFunc(h.approvePlan)))
	handle("POST /v1/missions/{id}/replan", a.auth(http.HandlerFunc(h.replan)))
	handle("POST /v1/missions/{id}/rediscover", a.auth(http.HandlerFunc(h.rediscover)))
	handle("POST /v1/missions/{id}/answer", a.auth(http.HandlerFunc(h.answer)))
	handle("GET /v1/missions/{id}/files", a.auth(http.HandlerFunc(h.files)))
	handle("GET /v1/missions/{id}/files/{path...}", a.auth(http.HandlerFunc(h.download)))
	handle("GET /v1/missions/{id}/archive", a.auth(http.HandlerFunc(h.archive)))
	handle("POST /v1/missions/{id}/export-pdf", a.auth(http.HandlerFunc(h.exportPDF)))
	handle("POST /v1/missions/{id}/promote-kb", a.auth(http.HandlerFunc(h.promoteKB)))
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
	// resolveAgentRoute resolves an agent id to its own Route field
	// (agentReg.ResolveByID's Route, ok — ok=false on an unknown id or
	// no agentReg wired), the executionPlan handler's seam for the base
	// route's "agent" provenance. Kept separate from agentReg itself so
	// executionPlan is unit-testable without a live agents table.
	resolveAgentRoute func(context.Context, string) (string, bool)
	// resolveAgentHarness is resolveAgentRoute's counterpart for an
	// agent's Harness field - the "agent" provenance step in
	// missions.ResolveHarness (mission.harness -> agent.harness ->
	// settings.coding_executor -> native).
	resolveAgentHarness func(context.Context, string) (string, bool)
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
	// resolveRoute is gwclient.Client.ResolveRoute: backs GET
	// /v1/missions/executor-options and GET /v1/missions/execution-plan;
	// nil (no gateway wiring) makes either endpoint 404.
	resolveRoute func(context.Context, string, string) (*gwclient.ResolvedRoute, error)
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
	// pdfService renders export-pdf requests via the pdfgen sidecar; nil
	// (PDFGEN_URL unset or attachments disabled) 503s the endpoint.
	pdfService *pdfgenservice.Service
	// kbStore/kbIngest back POST .../promote-kb (D-081, issue #370): nil
	// kbStore (kb disabled) or nil kbIngest (memoryd unreachable is a
	// runtime error, not expected here since mc always resolves) 503s
	// the endpoint the same way pdfService's absence does export-pdf.
	kbStore  *kb.Store
	kbIngest kbIngester
	// resolveReferences resolves a create request's picked #-mention
	// references (mission/session/kb doc) into documents: chat.Service's
	// own resolver (chat.go's ResolveReferences), reused here so a
	// mission-create reference can never diverge from how a chat turn
	// resolves the exact same kinds. Never nil (a.svc is never nil,
	// Register always constructs one).
	resolveReferences func(context.Context, []chat.Reference) ([]session.DocumentRef, error)
	// destinations resolves a create request's destination_ids against
	// the operator-owned destinations table (D-061's exfiltration
	// guard: an id must exist AND be enabled) — nil (destinations
	// disabled) rejects any non-empty destination_ids.
	// extractGitHubDestination backs POST /v1/missions/detect-destination
	// (issue #483, chat.ExtractGitHubDestinationOverGateway): nil (no
	// gateway wiring) makes the endpoint always report found=false, same
	// never-errors degrade as the function itself.
	extractGitHubDestination func(context.Context, string) chat.GitHubDestinationProposal
}

// destinationLookup is the narrow slice of *destinations.Store the
// schedule handlers need to validate destination_ids — EnabledByID
// reports whether id names a real, enabled row (ok=false covers both
// "unknown id" and "disabled", both rejected identically). Mission
// create validation moved into missions.ValidateCreate (D-071).
type destinationLookup interface {
	EnabledByID(ctx context.Context, id string) (ok bool, err error)
}

// routeExists reports whether name resolves to a real route via
// resolveRoute (gwclient.ResolveRoute's own existence check —
// a 404/not-found error means no such route) — the seam
// DefaultCodingRoute uses to prefer the operator's "coding" route over
// "default" only when it's actually configured. false, never an error,
// on any failure (no gateway wiring, gateway unreachable, unknown
// route): a missing preferred route must degrade silently, never 500
// mission creation.
func (h *missionAPI) routeExists(ctx context.Context, name string) bool {
	if h.resolveRoute == nil {
		return false
	}
	_, err := h.resolveRoute(ctx, name, "")
	return err == nil
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
	case errors.Is(err, missions.ErrNotAwaitingApproval):
		jsonError(w, http.StatusConflict, "not_awaiting_approval", err.Error())
	case errors.Is(err, missions.ErrInvalidMission):
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}

// list serves GET /v1/missions, optionally narrowed by ?schedule_id=
// (a recurring schedule's fire history: every mission it spawned),
// ?q= (case-insensitive substring match on name or goal, the
// composer #-mention mission search), and/or ?limit= (a positive
// result cap). All are ignored when empty/absent, the original
// "every mission" behavior, and a malformed value is a 400 rather
// than a silently-empty filter.
func (h *missionAPI) list(w http.ResponseWriter, r *http.Request) {
	var filter missions.ListFilter
	if v := r.URL.Query().Get("schedule_id"); v != "" {
		if !validSessionID(v) {
			jsonError(w, http.StatusBadRequest, "bad_request", "schedule_id must be a UUID")
			return
		}
		filter.ScheduleID = v
	}
	filter.Query = r.URL.Query().Get("q")
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

// responseAttachment is the wire shape of one "pdf" Sources entry:
// id/mime/name only, mirroring the pre-#481 attachments column's own
// response shape (markdown is never sent -- sanitizeMission's original
// rationale, a response carrying up to maxMissionAttachments*128KB of
// markdown on every list/get call would be wasteful and the client
// never reads it).
type responseAttachment struct {
	ID   string `json:"id"`
	Mime string `json:"mime"`
	Name string `json:"name,omitempty"`
}

// sanitizeMission clears each "pdf" Sources entry's Markdown before a
// mission row goes out over the API: the json tags exist for jsonb
// persistence, but a response carrying up to maxMissionAttachments*
// 128KB of markdown on every list/get call would be wasteful and the
// client never reads it. Digest (parent/referenced-pick entries) is
// left alone -- those were plain text columns pre-#481, always sent.
func sanitizeMission(m missions.Mission) missions.Mission {
	if len(m.Sources) == 0 {
		return m
	}
	sources := make([]missions.SourceEntry, len(m.Sources))
	for i, e := range m.Sources {
		if e.Source == missions.SourceKindPDF {
			e.Markdown = ""
		}
		sources[i] = e
	}
	m.Sources = sources
	return m
}

// missionResponse is missions.Mission plus fields the store itself
// has no business computing: top_model/top_model_provider come from
// the cost ledger (a different service's data), decorated on here at
// serve time. Light/Worktree (issue #479) and RepoURL/ConnectorID/
// Attachments (issue #481) are likewise computed here rather than
// stored: dropping their columns means Mission itself no longer
// carries them, but the web client still reads mission.light,
// mission.worktree, mission.repo_url, mission.connector_id, and
// mission.attachments unchanged, so the API response derives all of
// them from Flow/WorktreePath()/GitHubSource()/Attachments() on the
// way out.
type missionResponse struct {
	missions.Mission
	// Light is derived from Flow == FlowLight (issue #479): the web
	// client's own light checks (e.g. MissionDetail.tsx's runsPlanless)
	// read this exactly as before the light column was dropped.
	Light    bool   `json:"light"`
	Worktree string `json:"worktree,omitempty"`
	// OnComplete is derived from the mission's github Destinations entry
	// (issue #480 dropped the on_complete column): the web client's own
	// auto-push/auto-PR badge (MissionDetail.tsx) reads this exactly as
	// before.
	OnComplete string `json:"on_complete,omitempty"`
	// RepoURL/ConnectorID are derived from the mission's "github" Sources
	// entry (issue #481 dropped the repo_url/connector_id columns): the
	// web client's own github-connection checks (MissionDetail.tsx,
	// MissionForm.tsx) read these exactly as before.
	RepoURL     string `json:"repo_url,omitempty"`
	ConnectorID string `json:"connector_id,omitempty"`
	// Attachments are the mission's "pdf" Sources entries in the
	// pre-#481 wire shape (id/mime/name, never markdown).
	Attachments      []responseAttachment `json:"attachments,omitempty"`
	TopModel         string               `json:"top_model,omitempty"`
	TopModelProvider string               `json:"top_model_provider,omitempty"`
}

// decorateTopModels adds top_model/top_model_provider to a page of
// missions in one ledger call — nil topModels (no ledger wiring) or a
// ledger error both degrade to every mission simply omitting the
// field, never a failed list/get.
func (h *missionAPI) decorateTopModels(ctx context.Context, rows []missions.Mission) []missionResponse {
	out := make([]missionResponse, len(rows))
	for i, m := range rows {
		github, _ := m.GitHubSource()
		var atts []responseAttachment
		for _, a := range m.Attachments() {
			atts = append(atts, responseAttachment{ID: a.ID, Mime: a.Mime, Name: a.Name})
		}
		out[i] = missionResponse{
			Mission: m, Light: m.Flow == missions.FlowLight, Worktree: m.WorktreePath(), OnComplete: m.OnComplete(),
			RepoURL: github.RepoURL, ConnectorID: github.ConnectorID, Attachments: atts,
		}
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
	// PlanRoute, when set, is the route discover/plan/replan/prove run
	// on instead of Route (see missions.Mission.PlanRoute for full
	// precedence). "" is the default: Route covers everything, exact
	// prior behavior. Not defaulted from an agent's route/review_route —
	// there is no agent-level plan_route equivalent.
	PlanRoute string `json:"plan_route"`
	// EscalationRoute, when set, is where worker turns move after a
	// failure or rework. Empty keeps escalation off — never defaulted,
	// so a route change is always an explicit choice.
	EscalationRoute string `json:"escalation_route"`
	// RouteModel/PlanRouteModel/ReviewRouteModel (D-078) pin one phase
	// axis to one exact chain entry ("provider name/model") in the route
	// it would otherwise resolve — "" (the default) keeps the
	// first-usable walk. Precedence mirrors the route fields exactly:
	// see missions.Mission.RouteModel. Never validated against the live
	// chain here — a chain can change after create and the runtime
	// already falls back to first-usable when a pin doesn't match.
	RouteModel       string   `json:"route_model"`
	PlanRouteModel   string   `json:"plan_route_model"`
	ReviewRouteModel string   `json:"review_route_model"`
	MaxIterations    int      `json:"max_iterations"`
	BudgetAmount     *float64 `json:"budget_amount"`
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
	// AutoApproveTools defaults true (a pointer so an omitted field is
	// distinguishable from an explicit false) — missions run for hours
	// unattended, so auto-approving DangerSafe shell calls is the
	// sensible default; destructive commands always ask regardless.
	AutoApproveTools *bool `json:"auto_approve_tools"`
	// AutoApprovePlan defaults true (pointer, same "omitted vs explicit
	// false" reasoning as AutoApproveTools above): false parks the
	// mission for operator approve/replan/rediscover once the plan
	// phase lands (D-087, issue #456). Ignored (forced true) for
	// scheduler-fired and workflow-spawned missions.
	AutoApprovePlan *bool `json:"auto_approve_plan"`
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
	// human choosing it here. Normalized into a "github" Destinations
	// entry by destinationEntries below (issue #480).
	OnComplete string `json:"on_complete"`
	// BranchPattern/CommitStyle override the settings-configured git
	// strategy defaults for this mission alone; "" (the default) applies
	// the settings default at provisioning/commit time. Validated the
	// same way settings.Store.SetValue validates the global default —
	// only known placeholders/styles, never model-decided. Folded into
	// the same "github" Destinations entry as OnComplete.
	BranchPattern string `json:"branch_pattern"`
	CommitStyle   string `json:"commit_style"`
	// CreateIfMissing (issue #483) opts the github destination entry
	// into create-if-missing delivery (missions.DestinationEntry's own
	// field): false (the default) never creates a repo, matching every
	// pre-#483 request. Only meaningful alongside OnComplete; folded
	// into the same "github" Destinations entry destinationEntries
	// below builds. No flat-field precedent to mirror (issue #480's
	// columns predate create-if-missing entirely), so this is new
	// surface, not a compat shim.
	CreateIfMissing bool `json:"create_if_missing"`
	// DestinationConnectorID/DestinationRepoURL (issue #483) name the
	// github destination entry's OWN push target, distinct from
	// RepoURL/ConnectorID above (the mission's clone SOURCE): a scratch
	// mission (RepoURL empty, self-init'd worktree) can still push
	// somewhere via CreateIfMissing, and even a cloned mission can push
	// to a different repo than it cloned from. DestinationRepoURL empty
	// with CreateIfMissing=true derives a repo name from the mission's
	// goal at delivery time (missions.Completer.ensureRepo);
	// DestinationConnectorID empty falls back to ConnectorID (the same
	// connector authenticates both clone and push, the common case).
	DestinationConnectorID string `json:"destination_connector_id"`
	DestinationRepoURL     string `json:"destination_repo_url"`
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
	// References names composer #-mention picks (missions/sessions/kb
	// docs) to resolve at create time into ReferencedContext, additive
	// to and distinct from ParentMissionID's own single-parent lineage.
	References []chat.Reference `json:"references"`
	// DestinationIDs names operator-created destinations (email,
	// webhook) to deliver this mission's outcome digest to on the
	// terminal done transition — every id is validated to exist AND be
	// enabled at create time (missions.ValidateCreate); the model never
	// supplies or addresses a destination (D-061). Normalized into bare
	// Destinations entries by destinationEntries below (issue #480).
	DestinationIDs []string `json:"destination_ids"`
	// PromoteKBCollectionID (D-081, issue #370) names a kb collection to
	// promote this mission's markdown artifacts into on the terminal
	// done transition: "" (default) promotes nothing automatically;
	// validated to exist at create time (missions.ValidateCreate), the
	// model never supplies or addresses it, same invariant as
	// DestinationIDs. Normalized into a "kb" Destinations entry by
	// destinationEntries below.
	PromoteKBCollectionID string `json:"promote_kb_collection_id"`
	// Light requests a mission that skips discover/plan/prove (D-069):
	// kind=general only, rejected outright on kind=coding (explicit or
	// classified). Always the operator's explicit choice — the classify
	// preview only suggests a value, never sets it. Kept working
	// unchanged alongside Flow (D-090, issue #459): Light=true maps to
	// Flow=light below when Flow is omitted, and must never contradict
	// an explicit Flow.
	Light bool `json:"light"`
	// Flow selects the phase set this mission runs (D-090, issue #459):
	// "" (omitted, the default) maps to "light" when Light=true, else
	// "full", today's exact pre-#459 behavior. Rejected outright on
	// kind=coding (must stay "full"). Snapshotted once at create time,
	// never model-mutable afterward.
	Flow string `json:"flow"`
	// PermissionTimeoutSeconds overrides the global
	// settings.ValuePermissionTimeoutSeconds for this mission alone
	// (issue #445): nil (omitted) inherits the global setting; 0 or a
	// positive integer sets this mission's own park-forever/auto-deny
	// bound. Never negative.
	PermissionTimeoutSeconds *int `json:"permission_timeout_seconds"`
}

// destinationEntries normalizes the request's separate DestinationIDs/
// PromoteKBCollectionID/OnComplete/BranchPattern/CommitStyle/
// CreateIfMissing fields into missions.Mission's single Destinations
// slice (issue #480, extended #483): one bare entry per destination
// id, one "kb" entry when PromoteKBCollectionID is set, one "github"
// entry when OnComplete, BranchPattern, CommitStyle, or
// CreateIfMissing is set. The wire request shape is unchanged; only
// the internal Mission representation moved.
func (r createMissionRequest) destinationEntries() []missions.DestinationEntry {
	var entries []missions.DestinationEntry
	for _, id := range r.DestinationIDs {
		entries = append(entries, missions.DestinationEntry{DestinationID: id})
	}
	if r.PromoteKBCollectionID != "" {
		entries = append(entries, missions.DestinationEntry{Destination: missions.DestinationKindKB, CollectionID: r.PromoteKBCollectionID})
	}
	if r.OnComplete != "" || r.BranchPattern != "" || r.CommitStyle != "" || r.CreateIfMissing {
		entries = append(entries, missions.DestinationEntry{
			Destination: missions.DestinationKindGitHub, Mode: r.OnComplete,
			BranchPattern: r.BranchPattern, CommitStyle: r.CommitStyle,
			CreateIfMissing: r.CreateIfMissing,
			ConnectorID:     r.DestinationConnectorID,
			RepoURL:         r.DestinationRepoURL,
		})
	}
	return entries
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
	if req.Kind == "" {
		req.Kind = classifyKind(r.Context(), h.classify, req.Goal)
	}
	if req.Harness == "native" {
		req.Harness = ""
	}
	// codingExecutorDefault/environment auto-detect are HTTP-request-time
	// resolution seams (settings lookup, goal-keyword heuristic) with no
	// place in ValidateCreate's pure struct-shape rules; the resulting
	// values still pass through ValidateCreate's kind/harness/environment
	// checks below via Driver.Create. ResolveHarness applies the full
	// mission.harness -> agent.harness -> settings.coding_executor ->
	// native precedence (missions.ResolveHarness).
	var agentHarness string
	if h.resolveAgentHarness != nil {
		agentHarness, _ = h.resolveAgentHarness(r.Context(), req.AgentID)
	}
	req.Harness, _ = missions.ResolveHarness(r.Context(), req.Kind, req.Harness, agentHarness, h.codingExecutorDefault)
	if req.Kind == missions.KindCoding && req.Environment == "" {
		// Auto-detect (D-05x), resolved server-side at create time so
		// the environment is fixed before the sandbox container is ever
		// created: no worktree exists yet (it's provisioned after this
		// handler returns), so only the goal-keyword heuristic can fire
		// here — repo-marker detection has nothing to check against a
		// mission that hasn't been provisioned. Never wins over an
		// explicit request; "" stays "" (base) when nothing matches.
		req.Environment, _ = missions.DetectEnvironment("", req.Goal)
	}
	// connector_id existence + kind check is a store lookup ValidateCreate
	// can't perform (it takes no connectors dependency); repo_url's other
	// shape rules (coding-only, requires connector_id) are ValidateCreate's.
	if req.RepoURL != "" {
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
	var parentMissionID string
	var parentSource *missions.SourceEntry
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
		parentSource = &missions.SourceEntry{
			Source: missions.SourceKindMission, ID: missions.ParentLineageID, MissionID: parent.ID,
			Digest: missions.OutcomeDigest(parent, events, parent.Phase, parent.FailureReason),
		}
	}
	pdfSources, ok := h.resolveAttachments(w, r.Context(), req.Attachments)
	if !ok {
		return
	}
	refSources, err := h.resolveReferenceSources(r.Context(), req.References)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// Resolve even with an empty AgentID: ResolveByID("") falls back to
	// the default agent, same as chat sessions that don't pick one — a
	// mission created without an explicit agent still needs a real
	// route, not an empty string the gateway will reject. By id, not
	// name: req.AgentID is the mission row's agent_id FK value (the
	// picker sends a.id), unlike chat's session.agent which is a name.
	var promptOverlay string
	if h.agentReg != nil {
		if a, ok := h.agentReg.ResolveByID(r.Context(), req.AgentID); ok {
			if req.Route == "" {
				req.Route = a.Route
			}
			if req.ReviewRoute == "" {
				req.ReviewRoute = a.ReviewRoute
			}
			promptOverlay = a.PromptOverlay
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
		if req.Kind == missions.KindCoding {
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

	autoApproveTools := true
	if req.AutoApproveTools != nil {
		autoApproveTools = *req.AutoApproveTools
	}
	autoApprovePlan := true
	if req.AutoApprovePlan != nil {
		autoApprovePlan = *req.AutoApprovePlan
	}
	if req.PermissionTimeoutSeconds != nil && *req.PermissionTimeoutSeconds < 0 {
		jsonError(w, http.StatusBadRequest, "bad_request", "permission_timeout_seconds must not be negative")
		return
	}
	budgetCurrency := req.BudgetCurrency
	if budgetCurrency == "" {
		budgetCurrency = "USD"
	}
	// flow/light normalization (D-090, issue #459): flow omitted maps to
	// today's exact pre-#459 behavior (light=true -> "light", else
	// "full"). Any other mismatch between an explicit flow and light (or
	// kind=coding requesting a non-full flow) is left for
	// missions.ValidateCreate to reject with the specific reason, not
	// silently resolved here. issue #479 dropped the mission.light
	// column: flow alone drives every downstream code path now.
	flow := req.Flow
	if flow == "" {
		if req.Light {
			flow = string(missions.FlowLight)
		} else {
			flow = string(missions.FlowFull)
		}
	}
	// Sources order matches packet.go's renderSources exactly (issue
	// #481): parent-mission digest, then referenced picks, then
	// attached PDFs, then the github clone source (order-independent,
	// rendered nowhere).
	var sources []missions.SourceEntry
	if parentSource != nil {
		sources = append(sources, *parentSource)
	}
	sources = append(sources, refSources...)
	sources = append(sources, pdfSources...)
	if req.RepoURL != "" {
		sources = append(sources, missions.SourceEntry{Source: missions.SourceKindGitHub, ConnectorID: req.ConnectorID, RepoURL: req.RepoURL})
	}
	m := missions.Mission{
		Goal: req.Goal, Kind: req.Kind, AgentID: req.AgentID,
		Route: req.Route, ReviewRoute: req.ReviewRoute, PlanRoute: req.PlanRoute, EscalationRoute: req.EscalationRoute,
		RouteModel: req.RouteModel, PlanRouteModel: req.PlanRouteModel, ReviewRouteModel: req.ReviewRouteModel,
		MaxIterations: req.MaxIterations, BudgetAmount: req.BudgetAmount, BudgetCurrency: budgetCurrency,
		AutoApproveTools: autoApproveTools, AutoApprovePlan: autoApprovePlan, PromptOverlay: promptOverlay, Harness: req.Harness, Environment: req.Environment,
		ParentMissionID:          parentMissionID,
		Sources:                  sources,
		Destinations:             req.destinationEntries(),
		Flow:                     missions.Flow(flow),
		PermissionTimeoutSeconds: req.PermissionTimeoutSeconds,
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
	writeJSON(w, http.StatusCreated, h.decorateTopModels(r.Context(), []missions.Mission{sanitizeMission(created)})[0])
}

// referenceSourceKind maps a composer #-mention Reference.Kind to its
// SourceEntry.Source value: "session" -> "chat" (Sources' kind name
// for a referenced chat transcript, distinguishing it from a mission's
// own hidden session), "mission"/"kb_doc" carry straight through.
func referenceSourceKind(refKind string) string {
	switch refKind {
	case chat.ReferenceKindSession:
		return missions.SourceKindChat
	case chat.ReferenceKindMission:
		return missions.SourceKindMission
	case chat.ReferenceKindKBDoc:
		return missions.SourceKindKB
	default:
		return ""
	}
}

// resolveReferenceSources resolves a create request's picked
// #-mention references into one SourceEntry per pick (issue #481,
// replacing the single concatenated ReferencedContext string) --
// resolved one at a time (not the batch h.resolveReferences call
// pre-#481 used) so each entry can carry the reference's own Kind,
// reusing chat.Service's own resolver so a mission reference can never
// resolve differently than the identical chat reference kind. Empty
// input returns nil, nil without calling the resolver, same zero-refs
// fast path as resolveAttachments. The resolver itself already skips
// unresolvable picks (missing id, unknown kind) rather than failing;
// the only error surfaced here is the shared over-cap rejection,
// still checked against the full batch up front.
func (h *missionAPI) resolveReferenceSources(ctx context.Context, refs []chat.Reference) ([]missions.SourceEntry, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if _, err := h.resolveReferences(ctx, refs); err != nil {
		return nil, err // surfaces only the over-cap rejection
	}
	var out []missions.SourceEntry
	for _, ref := range refs {
		kind := referenceSourceKind(ref.Kind)
		if kind == "" {
			continue
		}
		docs, err := h.resolveReferences(ctx, []chat.Reference{ref})
		if err != nil || len(docs) == 0 || docs[0].Markdown == "" {
			continue
		}
		d := docs[0]
		e := missions.SourceEntry{Source: kind, Name: d.Name, Digest: d.Markdown}
		switch kind {
		case missions.SourceKindChat:
			e.SessionID = ref.ID
		case missions.SourceKindMission:
			e.MissionID = ref.ID
		case missions.SourceKindKB:
			e.DocID = ref.ID
		}
		out = append(out, e)
	}
	return out, nil
}

// resolveAttachments validates and converts a create request's PDF and
// text refs into "pdf" SourceEntry values -- writes any error response
// itself and returns ok=false, mirroring chat.validateAttachments'
// shape but as a method so it can write directly (create()'s other
// validation blocks use jsonError+return rather than a returned
// error). Empty input returns nil, true without touching the store,
// same as chat's own zero-ids fast path.
func (h *missionAPI) resolveAttachments(w http.ResponseWriter, ctx context.Context, in []missionAttachmentInput) ([]missions.SourceEntry, bool) {
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
	// Fetch and validate mime up front so the markitdown-sidecar check
	// below only fires when a PDF actually needs conversion — text
	// attachments must succeed with no sidecar configured.
	atts := make([]attachments.Attachment, len(in))
	needsMarkitdown := false
	for i, input := range in {
		att, err := h.attachments.Get(ctx, input.ID)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("attachment %q not found", input.ID))
			return nil, false
		}
		if att.Mime != "application/pdf" && att.Mime != "text/plain" {
			jsonError(w, http.StatusBadRequest, "bad_request", "only document attachments are supported for missions")
			return nil, false
		}
		if att.Mime == "application/pdf" {
			needsMarkitdown = true
		}
		atts[i] = att
	}
	if needsMarkitdown && h.markitdownURL == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "pdf attachments require the markitdown sidecar (MARKITDOWN_URL)")
		return nil, false
	}
	out := make([]missions.SourceEntry, 0, len(in))
	for i, input := range in {
		att := atts[i]
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
		var md string
		if att.Mime == "application/pdf" {
			md, err = markitdown.Convert(ctx, h.markitdownHTTP, h.markitdownURL, att.ID+".pdf", att.Mime, raw)
			if err != nil {
				jsonError(w, http.StatusInternalServerError, "internal_error", err.Error())
				return nil, false
			}
		} else {
			md = string(raw)
		}
		out = append(out, missions.SourceEntry{
			Source: missions.SourceKindPDF, ID: att.ID, Mime: att.Mime, Name: input.Name, Markdown: markitdown.TruncateMarkdown(md),
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

// classifyKindAndLight answers both classifyKind and classifyLight's
// questions in one model call — used only by the preview endpoint
// (classifyGoal), which needs both on every debounced keystroke and
// would otherwise pay for two full LLM turns per preview. create()'s
// fallback path keeps using classifyKind/classifyLight separately,
// since it only ever needs light after kind is already known to be
// general. Parsing is defensive: any reply shape other than exactly
// "coding"/"general" followed by "light"/"full" (case-insensitive,
// separated by whitespace) falls back to kind=coding, light=false —
// the same safe-side bias classifyKind/classifyLight each apply alone.
func classifyKindAndLight(ctx context.Context, classify agents.Classify, goal string) (kind string, light bool) {
	if classify == nil {
		return "coding", false
	}
	prompt := "Classify this mission goal along two independent axes.\n" +
		"Axis 1 — coding or general: coding means creating or modifying code, scripts, or configuration in a project/repository; general means everything else (documents, analysis, data gathering, operations).\n" +
		"Axis 2 — light or full: light means the goal is a single-pass task deliverable in one response (a read, summary, lookup, or short write-up with no multi-step plan or file artifacts); full means it needs a plan and artifacts.\n" +
		"Answer with exactly two words separated by a space: first coding or general, then light or full.\n\n" +
		"Goal: " + goal
	reply, err := classify(ctx, prompt)
	if err != nil {
		return "coding", false
	}
	fields := strings.Fields(strings.ToLower(reply))
	if len(fields) != 2 {
		return "coding", false
	}
	switch fields[0] {
	case "general":
		kind = "general"
	case "coding":
		kind = "coding"
	default:
		return "coding", false
	}
	switch fields[1] {
	case "light":
		light = true
	case "full":
		light = false
	default:
		return "coding", false
	}
	return kind, kind == "general" && light
}

// classifyGoal serves POST /v1/missions/classify: the same
// classification create() falls back to when kind is omitted, exposed
// standalone so the web UI's chip preview can show a mission's inferred
// kind (and, for a general goal, a light suggestion) before submit
// without actually creating anything. Uses classifyKindAndLight's
// single-call form since every debounced keystroke hits this endpoint.
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
	kind, light := classifyKindAndLight(r.Context(), h.classify, req.Goal)
	writeJSON(w, http.StatusOK, map[string]any{"kind": kind, "light": light})
}

// detectDestination serves POST /v1/missions/detect-destination
// (issue #483): an on-demand "Detect from goal" action the create
// form calls explicitly, never fired automatically, so a proposal is
// always something the operator saw before it could ever end up on
// the create request. chat.ExtractGitHubDestinationOverGateway's
// never-errors contract means this 200s with found=false rather than
// erroring on any gateway failure/ambiguity -- there is nothing the
// caller needs to distinguish from "genuinely nothing detected."
func (h *missionAPI) detectDestination(w http.ResponseWriter, r *http.Request) {
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
	if h.extractGitHubDestination == nil {
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	p := h.extractGitHubDestination(r.Context(), req.Goal)
	if !p.Found {
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"found": true,
		"owner": p.Owner,
		"repo":  p.Repo,
		"mode":  p.Mode,
	})
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
	if h.resolveRoute == nil {
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
		resolved, err := h.resolveRoute(r.Context(), route, harness)
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

// executionPlanEntry is one resolved chain entry for a phase's route,
// mirroring gwclient.ResolvedRouteEntry but trimmed to what the phase
// table needs — Selected marks the one entry the runner/dispatcher
// would actually pick (the first Usable entry; none set when no entry
// is usable).
type executionPlanEntry struct {
	ProviderName string              `json:"provider_name"`
	Driver       string              `json:"driver"`
	Kind         string              `json:"kind"`
	BaseURL      string              `json:"base_url"`
	Model        string              `json:"model"`
	Usable       bool                `json:"usable"`
	SkipReason   string              `json:"skip_reason"`
	Selected     bool                `json:"selected"`
	Prices       *router.ModelPrices `json:"prices,omitempty"`
}

// executionPlanPhase is one phase's resolved route/axis/entries for
// GET /v1/missions/execution-plan.
type executionPlanPhase struct {
	Phase         string               `json:"phase"`
	Route         string               `json:"route"`
	RouteSource   string               `json:"route_source"`
	Axis          string               `json:"axis"`
	Harness       string               `json:"harness"`
	HarnessSource string               `json:"harness_source"`
	Skipped       bool                 `json:"skipped"`
	SkipReason    string               `json:"skip_reason"`
	Entries       []executionPlanEntry `json:"entries"`
}

// baseRoute resolves the route explicit/agent/named-coding/default-role
// precedence chain a mission's Route field itself follows at create
// time (create()'s own req.Route resolution, missions.DefaultCodingRoute)
// — mirrored here rather than reused because create()'s version is
// entangled with the request/agent-defaulting flow, not a standalone
// function. Returns "" with source "none" when nothing resolves.
func (h *missionAPI) baseRoute(ctx context.Context, kind, explicitRoute, agentID string) (route, source string) {
	if explicitRoute != "" {
		return explicitRoute, "explicit"
	}
	if h.resolveAgentRoute != nil {
		if r, ok := h.resolveAgentRoute(ctx, agentID); ok {
			return r, "agent"
		}
	}
	if kind == missions.KindCoding && h.routeExists(ctx, "coding") {
		return "coding", "named-coding"
	}
	if h.routeForRole != nil {
		if r := h.routeForRole(ctx, "default"); r != "" {
			return r, "default-role"
		}
	}
	return "", "none"
}

// resolveHarness resolves the generate phase's harness via
// missions.ResolveHarness, the same precedence chain create() and the
// scheduler's fire path use: explicit -> agent -> settings -> native.
// Only kind=coding can ever delegate (mirrors policy.go's
// canDelegate) - any other kind always stays native regardless of
// what resolves.
func (h *missionAPI) resolveHarness(ctx context.Context, kind, explicitHarness, agentID string) (harness, source string) {
	var agentHarness string
	if h.resolveAgentHarness != nil {
		agentHarness, _ = h.resolveAgentHarness(ctx, agentID)
	}
	return missions.ResolveHarness(ctx, kind, explicitHarness, agentHarness, h.codingExecutorDefault)
}

// resolveEntries calls resolveRoute for route on the given axis
// (harness == "" is the chat/native axis) and shapes the result into
// the phase table's entry list. A resolve error or an empty route
// yields no entries, with the failure reason returned for the phase's
// skip_reason — never a failed request, matching executorOptions' own
// degrade-on-resolve-error behavior. modelPin ("provider name/model",
// D-078), when it names a USABLE entry, marks that entry selected
// instead of the first-usable one; when the pin names no entry, or an
// unusable one, the first-usable entry keeps Selected and the pin's own
// entry (if present) keeps its Usable/SkipReason as-is so the UI can
// show why the pin will not apply — resolveEntries never fails a
// request over a pin that doesn't currently resolve.
func (h *missionAPI) resolveEntries(ctx context.Context, route, harness, modelPin string) ([]executionPlanEntry, string) {
	if route == "" {
		return nil, ""
	}
	if h.resolveRoute == nil {
		return nil, ""
	}
	resolved, err := h.resolveRoute(ctx, route, harness)
	if err != nil {
		return nil, err.Error()
	}
	if resolved == nil {
		return nil, ""
	}
	entries := make([]executionPlanEntry, len(resolved.Entries))
	pinIdx := -1
	firstUsable := -1
	for i, e := range resolved.Entries {
		entries[i] = executionPlanEntry{
			ProviderName: e.ProviderName,
			Driver:       e.Driver,
			Kind:         e.Kind,
			BaseURL:      e.BaseURL,
			Model:        e.Model,
			Usable:       e.Usable,
			SkipReason:   e.SkipReason,
			Prices:       e.Prices,
		}
		if firstUsable == -1 && e.Usable {
			firstUsable = i
		}
		if modelPin != "" && pinIdx == -1 && modelPin == e.ProviderName+"/"+e.Model {
			pinIdx = i
		}
	}
	switch {
	case pinIdx != -1 && entries[pinIdx].Usable:
		entries[pinIdx].Selected = true
	case firstUsable != -1:
		entries[firstUsable].Selected = true
	}
	return entries, ""
}

const (
	lightSkipReason   = "light missions run generate only"
	escalateOffReason = "no escalation route set; failures retry on the generate route"
)

// executionPlan serves GET /v1/missions/execution-plan, resolving
// every phase (discover, plan, generate, prove, escalate) server-side
// so the web UI never recomputes route/harness precedence itself
// (docs/2026-08-26-mission-execution-plan.md, slice 1). Query params
// mirror createMissionRequest's own route fields; all are optional.
func (h *missionAPI) executionPlan(w http.ResponseWriter, r *http.Request) {
	if h.resolveRoute == nil {
		jsonError(w, http.StatusNotFound, "not_found", "execution plan preview is not enabled")
		return
	}
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind == "" {
		kind = missions.KindGeneral
	}
	agentID := q.Get("agent")
	explicitHarness := q.Get("harness")
	route := q.Get("route")
	planRoute := q.Get("plan_route")
	reviewRoute := q.Get("review_route")
	escalationRoute := q.Get("escalation_route")
	light := q.Get("light") == "true"
	// Model pins (D-078) mirror runner.go's own precedence: routeModel
	// backs generate, planRouteModel backs discover/plan, reviewModel
	// falls back reviewRouteModel > planRouteModel > routeModel, see
	// oversightModel/reviewModel.
	routeModel := q.Get("route_model")
	planRouteModel := q.Get("plan_route_model")
	reviewRouteModel := q.Get("review_route_model")
	oversightModel := routeModel
	if planRouteModel != "" {
		oversightModel = planRouteModel
	}
	reviewModel := oversightModel
	if reviewRouteModel != "" {
		reviewModel = reviewRouteModel
	}

	ctx := r.Context()
	base, baseSource := h.baseRoute(ctx, kind, route, agentID)

	// oversightRoute mirrors runner.go's own helper: plan_route when
	// set, else the base route.
	oversight, oversightSource := base, baseSource
	if planRoute != "" {
		oversight, oversightSource = planRoute, "explicit"
	}

	harness, harnessSource := h.resolveHarness(ctx, kind, explicitHarness, agentID)
	executeAxis := "native"
	if harness != "" {
		executeAxis = "harness"
	}

	// reviewRoute mirrors runner.go's own helper: review_route, else
	// plan_route (as "inherited-from-plan"), else the base route (as
	// "inherited-from-generate"). oversight already equals planRoute
	// when planRoute is set (see above), so that's the discriminator,
	// oversightSource itself may also read "explicit" when it fell
	// through to an explicit base route, which must not be confused
	// with plan_route actually being set.
	review, reviewSource := oversight, "inherited-from-generate"
	if planRoute != "" {
		reviewSource = "inherited-from-plan"
	}
	if reviewRoute != "" {
		review, reviewSource = reviewRoute, "explicit"
	}

	phases := make([]executionPlanPhase, 0, 5)

	discoverEntries, discoverErr := h.resolveEntries(ctx, oversight, "", oversightModel)
	phases = append(phases, executionPlanPhase{
		Phase: "discover", Route: oversight, RouteSource: oversightSource, Axis: "native",
		Skipped: light, SkipReason: skipReasonIf(light, lightSkipReason, discoverErr),
		Entries: emptyEntries(discoverEntries),
	})

	planEntries, planErr := h.resolveEntries(ctx, oversight, "", oversightModel)
	phases = append(phases, executionPlanPhase{
		Phase: "plan", Route: oversight, RouteSource: oversightSource, Axis: "native",
		Skipped: light, SkipReason: skipReasonIf(light, lightSkipReason, planErr),
		Entries: emptyEntries(planEntries),
	})

	generateEntries, generateErr := h.resolveEntries(ctx, base, harness, routeModel)
	phases = append(phases, executionPlanPhase{
		Phase: "generate", Route: base, RouteSource: baseSource, Axis: executeAxis,
		Harness: harness, HarnessSource: harnessSource,
		SkipReason: generateErr,
		Entries:    emptyEntries(generateEntries),
	})

	proveEntries, proveErr := h.resolveEntries(ctx, review, "", reviewModel)
	phases = append(phases, executionPlanPhase{
		Phase: "prove", Route: review, RouteSource: reviewSource, Axis: "native",
		Skipped: light, SkipReason: skipReasonIf(light, lightSkipReason, proveErr),
		Entries: emptyEntries(proveEntries),
	})

	if escalationRoute != "" {
		escalateEntries, escalateErr := h.resolveEntries(ctx, escalationRoute, "", "")
		phases = append(phases, executionPlanPhase{
			Phase: "escalate", Route: escalationRoute, RouteSource: "explicit", Axis: "native",
			SkipReason: escalateErr,
			Entries:    emptyEntries(escalateEntries),
		})
	} else {
		phases = append(phases, executionPlanPhase{
			Phase: "escalate", Route: "", RouteSource: "off", Axis: "native",
			Skipped: true, SkipReason: escalateOffReason,
			Entries: []executionPlanEntry{},
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"phases": phases})
}

// skipReasonIf returns lightReason when skipped is true, else
// resolveErr (a resolve failure's own message) — a skipped phase's
// reason always wins since the resolve was never load-bearing for it.
func skipReasonIf(skipped bool, lightReason, resolveErr string) string {
	if skipped {
		return lightReason
	}
	return resolveErr
}

// emptyEntries normalizes a nil entry slice to an empty one so the
// JSON response always carries "entries": [] rather than null.
func emptyEntries(entries []executionPlanEntry) []executionPlanEntry {
	if entries == nil {
		return []executionPlanEntry{}
	}
	return entries
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
		if err := h.workspace.Teardown(r.Context(), m.Workspace, m.WorktreePath(), m.Kind); err != nil {
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
// state transition, no driver signal. Accepted on any non-terminal
// phase (D-089, issue #458): the mission.steered event carries the
// phase it landed in, and every phase's packet renders the note like
// any other progress note (Render, packet.go), so steering takes
// effect on its own without waking the mission early.
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
	if err := h.store.AppendEvent(r.Context(), id, "mission.steered", map[string]any{"note": note, "phase": string(m.Phase)}); err != nil {
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

// approvePlan handles POST /v1/missions/{id}/approve-plan: the operator
// accepts the plan as landed, advancing straight to generate (D-087,
// issue #456). Plain HTTP handler only, never a tool: the model
// cannot call this.
func (h *missionAPI) approvePlan(w http.ResponseWriter, r *http.Request) {
	if err := h.driver.DecidePlan(r.Context(), r.PathValue("id"), missions.InputPlanApprove, ""); err != nil {
		failMission(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// replan handles POST /v1/missions/{id}/replan: re-runs the plan phase
// with optional operator feedback folded into the next planning
// prompt. Feedback is recorded as a progress note (same channel the
// note endpoint's steering notes use, and the same precedent for
// storing the operator's own text verbatim in an event payload) BEFORE
// DecidePlan runs, so runPlan's replanNotes picks it up on the very
// next planning turn. Does not consume the mission's automatic
// stall-replan budget (ReplanUsed): an operator-requested replan is a
// free, unlimited iteration.
func (h *missionAPI) replan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Feedback string `json:"feedback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON with an optional feedback field")
		return
	}
	id := r.PathValue("id")
	feedback := missions.NeutralizeSlot(truncateAnswer(body.Feedback, resumeAnswerCap))
	if feedback != "" {
		if err := h.store.AppendProgress(r.Context(), id, "Operator replan feedback: "+feedback); err != nil {
			h.log.Warn("mission: record replan feedback progress note failed", "mission_id", id, "error", err)
		}
	}
	if err := h.driver.DecidePlan(r.Context(), id, missions.InputPlanReplan, feedback); err != nil {
		failMission(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rediscover handles POST /v1/missions/{id}/rediscover: sends the
// mission back to the discover phase for a fresh exploration pass, no
// feedback text taken (unlike replan).
func (h *missionAPI) rediscover(w http.ResponseWriter, r *http.Request) {
	if err := h.driver.DecidePlan(r.Context(), r.PathValue("id"), missions.InputPlanRediscover, ""); err != nil {
		failMission(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// answer handles POST /v1/missions/{id}/answer: the operator's reply to
// a mission's parked ask_user question (D-088, issue #457). Valid only
// while the mission is actually parked on one, 409 otherwise, same
// convention as approve-plan's DecidePlan error mapping. mcq answers
// must be one of the question's own options; yes_no answers must be
// "yes" or "no"; open accepts any non-empty text.
func (h *missionAPI) answer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Answer == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON with a non-empty answer field")
		return
	}
	id := r.PathValue("id")
	m, err := h.store.Get(r.Context(), id)
	if err != nil {
		failMission(w, err)
		return
	}
	if m.PendingInput == nil {
		jsonError(w, http.StatusConflict, "not_awaiting_answer", "this mission is not awaiting an answer")
		return
	}
	switch m.PendingInput.Kind {
	case "mcq":
		valid := false
		for _, opt := range m.PendingInput.Options {
			if opt == body.Answer {
				valid = true
				break
			}
		}
		if !valid {
			jsonError(w, http.StatusBadRequest, "bad_request", "answer must be one of the question's options")
			return
		}
	case "yes_no":
		if body.Answer != "yes" && body.Answer != "no" {
			jsonError(w, http.StatusBadRequest, "bad_request", `answer must be "yes" or "no"`)
			return
		}
	}
	answer := truncateAnswer(body.Answer, resumeAnswerCap)
	if err := h.driver.AnswerAskUser(r.Context(), id, answer); err != nil {
		failMission(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// declaredArtifacts builds the workspace-relative, filepath.Clean'ed
// set of paths the mission's plan units declare — files.go itself
// never imports or knows about Plan, keeping the two decoupled.
func declaredArtifacts(plan missions.Plan) map[string]bool {
	declared := map[string]bool{}
	for _, u := range plan.Units {
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
	entries, truncated, err := missions.ListFiles(m.WorkRoot(), declaredArtifacts(m.Plan))
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

// maxExportMarkdownBytes caps the total size of markdown selected for
// a PDF export (single file or merged) — bounds worst-case sidecar
// compile work.
const maxExportMarkdownBytes = 10 << 20 // 10 MiB

// exportPDF serves POST .../export-pdf: a single workspace-relative
// markdown file, or (empty body/path) every markdown file merged into
// one PDF with a cover page and TOC. Returns the rendered attachment
// id; the client downloads it via GET /v1/attachments/{id}.
func (h *missionAPI) exportPDF(w http.ResponseWriter, r *http.Request) {
	if h.pdfService == nil {
		jsonError(w, http.StatusServiceUnavailable, "not_enabled", "pdf generation is not enabled")
		return
	}
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.Workspace == "" {
		jsonError(w, http.StatusNotFound, "no_workspace", "this mission has no workspace")
		return
	}

	var body struct {
		Path string `json:"path"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON with an optional path field")
			return
		}
	}

	var docs []pdfgen.Document
	var opts pdfgen.Options
	if body.Path != "" {
		docs, opts, err = h.singleFileExportDocs(m, body.Path)
	} else {
		docs, opts, err = h.mergedExportDocs(r.Context(), m)
	}
	if err != nil {
		var status int
		var code string
		switch {
		case errors.As(err, new(*tools.Violation)), errors.Is(err, os.ErrNotExist):
			status, code = http.StatusNotFound, "not_found"
		case errors.Is(err, errNoMarkdownFiles), errors.Is(err, errNotMarkdown):
			status, code = http.StatusBadRequest, "bad_request"
		case errors.Is(err, errExportTooLarge):
			status, code = http.StatusRequestEntityTooLarge, "too_large"
		default:
			status, code = http.StatusInternalServerError, "export_failed"
		}
		jsonError(w, status, code, err.Error())
		return
	}

	result, err := h.pdfService.Render(r.Context(), docs, opts)
	if err != nil {
		if errors.Is(err, pdfgenservice.ErrNotEnabled) {
			jsonError(w, http.StatusServiceUnavailable, "not_enabled", "pdf generation is not enabled")
			return
		}
		jsonError(w, http.StatusBadGateway, "export_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachment_id": result.AttachmentID, "cached": result.Cached})
}

// promoteKB serves POST .../promote-kb: promotes a done mission's
// markdown artifact refs into collection_id as kb documents with
// provenance='mission' (D-081, issue #370): destinations.PromoteMission
// does the actual work, the SAME code path the terminal-done auto-fire
// hook (promote_kb_collection_id) uses, so a manual promote and an
// auto-fired one can never diverge. Done-only: a failed mission's
// artifacts are unverified (CheckArtifacts never ran to completion),
// so promoting them would put unverified content into search results.
// Idempotent: see PromoteMission's own doc comment.
func (h *missionAPI) promoteKB(w http.ResponseWriter, r *http.Request) {
	if h.kbStore == nil {
		jsonError(w, http.StatusServiceUnavailable, "not_enabled", "knowledge base is not enabled")
		return
	}
	m, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failMission(w, err)
		return
	}
	if m.Phase != missions.PhaseDone {
		jsonError(w, http.StatusBadRequest, "not_done", "only a mission in phase=done can be promoted")
		return
	}
	var body struct {
		CollectionID string `json:"collection_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "body must be JSON with a collection_id field")
		return
	}
	if body.CollectionID == "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "collection_id is required")
		return
	}
	if _, err := h.kbStore.GetCollection(r.Context(), body.CollectionID); err != nil {
		if errors.Is(err, kb.ErrNotFound) {
			jsonError(w, http.StatusBadRequest, "bad_request", "unknown collection_id")
			return
		}
		jsonError(w, http.StatusInternalServerError, "promote_failed", err.Error())
		return
	}
	if len(m.ArtifactRefs) == 0 {
		jsonError(w, http.StatusBadRequest, "no_artifacts", "mission has no artifacts to promote")
		return
	}
	promoted, errs := destinations.PromoteMission(r.Context(), h.attachments, h.kbStore, h.kbIngest, m, body.CollectionID)
	if promoted == 0 {
		msg := "no markdown artifacts were promoted"
		if len(errs) > 0 {
			msg = errs[0].Error()
		}
		jsonError(w, http.StatusBadGateway, "promote_failed", msg)
		return
	}
	resp := map[string]any{"promoted": promoted}
	if len(errs) > 0 {
		failed := make([]string, len(errs))
		for i, e := range errs {
			failed[i] = e.Error()
		}
		resp["failed"] = failed
	}
	writeJSON(w, http.StatusOK, resp)
}

var (
	errNoMarkdownFiles = errors.New("workspace has no markdown files")
	errNotMarkdown     = errors.New("path is not a markdown file")
	errExportTooLarge  = fmt.Errorf("selected markdown exceeds %d bytes", maxExportMarkdownBytes)
)

// singleFileExportDocs builds the one-document Render input for a
// single markdown file: chapter title is the file's base name without
// extension, no cover page, no TOC.
func (h *missionAPI) singleFileExportDocs(m missions.Mission, rel string) ([]pdfgen.Document, pdfgen.Options, error) {
	if !markdownExt.MatchString(rel) {
		return nil, pdfgen.Options{}, errNotMarkdown
	}
	f, fi, err := missions.OpenFile(m.WorkRoot(), rel)
	if err != nil {
		return nil, pdfgen.Options{}, err
	}
	defer func() { _ = f.Close() }()
	if fi.Size() > maxExportMarkdownBytes {
		return nil, pdfgen.Options{}, errExportTooLarge
	}
	content, err := io.ReadAll(f)
	if err != nil {
		return nil, pdfgen.Options{}, err
	}
	title := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	return []pdfgen.Document{{Title: title, Content: string(content)}}, pdfgen.Options{}, nil
}

// markdownExt matches a markdown file by extension, case-insensitive —
// mirrors missions.OrderedMarkdownPaths' own pattern for the
// single-file path (which never goes through ListFiles).
var markdownExt = regexp.MustCompile(`(?i)\.(md|markdown)$`)

// h1Line matches a level-1 ATX heading: "#" then required whitespace
// then text, trailing "#"s and whitespace trimmed off separately.
// Setext-style headings (a line of "===" under the title) are not
// recognized here — merged exports only need to catch the common ATX
// case models actually produce.
var h1Line = regexp.MustCompile(`^#\s+(.+?)$`)

// chapterTitle picks a merged-export chapter's title and returns the
// content with any duplicated heading line removed. If content's first
// non-blank line is a level-1 heading, that heading's text becomes the
// title and the line is dropped (the template injects the chapter
// heading, so keeping it would render the title twice). Otherwise the
// title falls back to path with its markdown extension stripped.
func chapterTitle(path, content string) (title, remaining string) {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if m := h1Line.FindStringSubmatch(trimmed); m != nil {
			heading := strings.TrimRight(m[1], "# \t")
			remaining = strings.Join(append(lines[:i:i], lines[i+1:]...), "\n")
			return heading, remaining
		}
		break
	}
	return markdownExt.ReplaceAllString(path, ""), content
}

// mergedExportDocs builds the merged Render input: every markdown file
// in the workspace, ordered by missions.OrderedMarkdownPaths, chapter
// titled via chapterTitle. Cover title is the mission's display name,
// falling back to its goal's first line or a short id.
func (h *missionAPI) mergedExportDocs(ctx context.Context, m missions.Mission) ([]pdfgen.Document, pdfgen.Options, error) {
	entries, _, err := missions.ListFiles(m.WorkRoot(), nil)
	if err != nil {
		return nil, pdfgen.Options{}, err
	}
	paths := missions.OrderedMarkdownPaths(entries)
	if len(paths) == 0 {
		return nil, pdfgen.Options{}, errNoMarkdownFiles
	}

	sizes := make(map[string]int64, len(entries))
	for _, e := range entries {
		sizes[e.Path] = e.Size
	}
	var total int64
	for _, p := range paths {
		total += sizes[p]
	}
	if total > maxExportMarkdownBytes {
		return nil, pdfgen.Options{}, errExportTooLarge
	}

	docs := make([]pdfgen.Document, 0, len(paths))
	for _, p := range paths {
		f, _, err := missions.OpenFile(m.WorkRoot(), p)
		if err != nil {
			return nil, pdfgen.Options{}, err
		}
		content, readErr := io.ReadAll(f)
		closeErr := f.Close()
		if readErr != nil {
			return nil, pdfgen.Options{}, readErr
		}
		if closeErr != nil {
			return nil, pdfgen.Options{}, closeErr
		}
		title, remaining := chapterTitle(p, string(content))
		docs = append(docs, pdfgen.Document{Title: title, Content: remaining})
	}

	return docs, pdfgen.Options{CoverTitle: missionDisplayName(m), TOC: true}, nil
}

// missionDisplayName resolves a mission's cover title: its generated
// Name, or (before naming lands) the goal's first line, or a short-id
// fallback when even the goal is empty.
func missionDisplayName(m missions.Mission) string {
	if m.Name != "" {
		return m.Name
	}
	if goal := strings.TrimSpace(m.Goal); goal != "" {
		if line, _, _ := strings.Cut(goal, "\n"); strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	id := m.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return "Mission " + id
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
	connectorID := m.ConnectorID()
	if connectorID == "" {
		return "", &pushTokenError{http.StatusBadRequest, "bad_request", "credential_ref is required and must match ^[A-Za-z0-9_./-]{1,128}$"}
	}
	if h.conns == nil {
		return "", &pushTokenError{http.StatusBadRequest, "bad_request", "connectors are not enabled"}
	}
	c, err := h.conns.Store().Get(ctx, connectorID)
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

// RepoExists/CreateRepo satisfy missions.PRSource's create-if-missing
// methods (issue #483); see cmd/brain/main.go's connsPRSource for the
// full doc comments, identical adapter logic duplicated here since
// this handler builds its own short-lived Completer per call (see
// completer() above) rather than sharing the driver's.
func (c connsPRSource) RepoExists(ctx context.Context, connectorID, owner, repo string) (bool, error) {
	_, err := c.conns.GetRepo(ctx, connectorID, owner, repo)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, connectors.ErrRepoNotFound) {
		return false, nil
	}
	return false, err
}

func (c connsPRSource) CreateRepo(ctx context.Context, connectorID, name string, private bool) (string, error) {
	repo, err := c.conns.CreateRepo(ctx, connectorID, name, private)
	if err != nil {
		return "", err
	}
	return repo.CloneURL, nil
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
	repoURL := m.RepoURL()
	if m.ConnectorID() == "" || repoURL == "" {
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
	if _, _, ok := missions.ParseGitHubRepoURL(repoURL); !ok {
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
