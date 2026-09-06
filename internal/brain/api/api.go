// Package api is brain's public HTTP surface: bearer-authenticated
// session management and chat over SSE. /health and /metrics stay
// open (mounted by the platform server before auth exists).
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/destinations"
	"github.com/SumonMSelim/timothy/internal/brain/fxrates"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/pdfgen"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/settings"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/brain/workflows"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
	"github.com/SumonMSelim/timothy/internal/secretstore"
)

// Directory is the session-management slice of the store; tests fake
// it, *session.Store satisfies it.
type Directory interface {
	Create(ctx context.Context, title string) (string, error)
	List(ctx context.Context, query string, before time.Time, beforeID string) ([]session.Meta, error)
	Get(ctx context.Context, id string) (session.Meta, error)
	Events(ctx context.Context, id string) ([]session.Event, error)
	Update(ctx context.Context, id string, title *string, archived *bool) error
	Delete(ctx context.Context, id string) error
	PendingPermissions(ctx context.Context, sessionIDs []string) ([]session.PendingPermission, error)
	SetKnowledge(ctx context.Context, id string, names []string) error
}

// PermissionResolver answers parked permission prompts; the loop's
// broker satisfies it. Nil disables the endpoint (404).
type PermissionResolver interface {
	Resolve(id, decision string) bool
}

// Toolset lists the live tool surface (builtins + connector tools);
// loop.Agent satisfies it. Nil disables the endpoint (404) — an agent
// tools-allowlist picker degrades to free text.
type Toolset interface {
	Tools() []provider.ToolDef
}

// API serves brain's public routes.
type API struct {
	svc   *chat.Service
	dir   Directory
	perms PermissionResolver
	token string
	log   *slog.Logger

	// flags/rates drive display-currency conversion of chat cost (the
	// terminal SSE meta event and the transcript replay) — same
	// nil-able pattern as UsageDecorator: either being nil just turns
	// conversion off, it never blocks or errors the read/stream.
	flags *settings.Store
	rates *fxrates.Store

	// kbCollections lists the knowledge base's collections for
	// validating a knowledge PUT/chat request's names — nil when KB is
	// disabled (no kb.Store wired), rejecting any non-empty Knowledge.
	kbCollections func(ctx context.Context) ([]kb.Collection, error)

	// pdfService backs POST /v1/chat/export-pdf (single assistant
	// message export, #380) — nil-gated the same way missionAPI's
	// pdfService is (PDFGEN_URL unset or attachments disabled).
	pdfService *pdfgen.Service
}

// memoryRoutePatterns is the EXHAUSTIVE list of memoryd routes brain
// exposes; everything else on memoryd (extract, retrieve, internals)
// stays unreachable from outside. Tests pin this scope.
var memoryRoutePatterns = []string{
	"GET /v1/memories",
	"POST /v1/memories",
	"POST /v1/memories/{id}",
	"GET /v1/memories/{id}/chain",
	"POST /v1/memories/search",
	"GET /v1/entities/graph",
	"GET /v1/entities/{id}/memories",
}

// Register mounts the routes, each wrapped in bearer auth. memories
// is the reverse proxy to memoryd's management routes, admin the
// proxy to the gateway's internal control plane, conns the local
// connector control plane (nil leaves any of them unmounted).
// whisperURL empty leaves /v1/transcribe unmounted (WHISPER_URL unset).
// caption converts an image attachment's bytes into a plain-prose
// description (issue #359, chat.CaptionImageOverGateway) for mission
// create/schedule attachment resolution; nil (no gateway wiring) makes
// every image attachment fail with attachmentResolver's "could not be
// described" error.
func Register(srv *httpserver.Server, svc *chat.Service, dir Directory, perms PermissionResolver, memories, admin http.Handler, flags *settings.Store, rates *fxrates.Store, agentReg *agents.Store, conns *connectors.Manager, goog *connectors.Google, msft *connectors.Microsoft, secrets *secretstore.Store, toolset Toolset, packs []skills.Skill, missionStore *missions.Store, missionDriver *missions.Driver, missionNotifier *missions.Notifier, missionWorkspace *missions.Workspace, resolveSecret func(context.Context, string) (string, error), routeForRole func(context.Context, string) string, missionClassify agents.Classify, resolveRoute func(context.Context, string, string) (*gwclient.ResolvedRoute, error), nameMission func(context.Context, string) string, topModels func(context.Context, []string) (map[string]ledger.ModelUsed, error), hub *missions.Hub, attachmentStore *attachments.Store, whisperClient *http.Client, whisperURL string, markitdownURL string, token string, log *slog.Logger, gwSecrets GatewaySecrets, kbStore *kb.Store, kbIngest kbIngester, kbClassify kbClassifier, kbTitle kbTitler, kbEnrich *kb.Enricher, destinationStore *destinations.Store, destinationTest destinationTester, workflowStore *workflows.Store, workflowEngine *workflows.Engine, pdfService *pdfgen.Service, caption func(context.Context, string, []byte) string) {
	a := &API{svc: svc, dir: dir, perms: perms, token: token, log: log, flags: flags, rates: rates, pdfService: pdfService}
	if kbStore != nil {
		a.kbCollections = kbStore.ListCollections
	}
	if memories != nil {
		for _, pattern := range memoryRoutePatterns {
			srv.Handle(pattern, a.auth(memories))
		}
	}
	a.registerAdmin(srv.Handle, admin)
	// conns is a *connectors.Manager: boxing a nil pointer straight into
	// the connectorLister interface would make registerSecrets' nil
	// check fail (non-nil interface holding a nil pointer), so the
	// conversion happens here, guarded.
	var connLister connectorLister
	if conns != nil {
		connLister = conns.Store()
	}
	// Same nil-box guard as connLister above: a nil *destinations.Store
	// boxed straight into destLister would be a non-nil interface value,
	// breaking registerSecrets' nil dests gate.
	var destLister destinationLister
	if destinationStore != nil {
		destLister = destinationStore
	}
	a.registerSecrets(srv.Handle, gwSecrets, connLister, destLister)
	a.registerSettings(srv.Handle, flags, whisperURL, pdfService != nil)
	a.registerAgents(srv.Handle, agentReg)
	a.registerConnectors(srv.Handle, conns, goog, msft, secrets)
	a.registerTools(srv.Handle, toolset)
	a.registerSkills(srv.Handle, packs)
	a.registerKB(srv.Handle, kbStore, kbIngest, markitdownURL, kbClassify, kbTitle, kbEnrich)
	var codingExecutorDefault func(context.Context) string
	if flags != nil {
		codingExecutorDefault = flags.CodingExecutor
	}
	// Same nil-box guard as connLister above: a nil *attachments.Store
	// boxed straight into missionAttachmentStore would be a non-nil
	// interface value, breaking registerMissions' attachments == nil gate.
	var missionAttachments missionAttachmentStore
	if attachmentStore != nil {
		missionAttachments = attachmentStore
	}
	// destinationLookupStore is *destinations.Store itself (EnabledByID)
	// — nil-boxed the same way connLister is above so a nil
	// destinationStore keeps registerMissions' create-time validation
	// honest (rejects any non-empty destination_ids).
	var destLookup destinationLookup
	if destinationStore != nil {
		destLookup = destinationStore
	}
	a.registerMissions(srv.Handle, missionStore, missionDriver, missionNotifier, agentReg, missionWorkspace, resolveSecret, routeForRole, missionClassify, codingExecutorDefault, resolveRoute, nameMission, topModels, conns, missionAttachments, markitdownURL, pdfService, kbStore, kbIngest, kbEnrich, whisperURL, caption)
	resolver := &attachmentResolver{store: missionAttachments, markitdownURL: markitdownURL, markitdownHTTP: &http.Client{}, whisperURL: whisperURL, whisperHTTP: whisperClient, caption: caption}
	a.registerSchedules(srv.Handle, missionStore, destLookup, resolver)
	// destinationRefs/destinationScheduleRefs are *missions.Store itself
	// (ActiveMissionReferencesDestination / ScheduleReferencingDestinationID) —
	// nil-boxed the same way connLister is above so a nil missionStore
	// keeps registerDestinations' refs checks honest.
	var destRefs destinationRefs
	var destScheduleRefs destinationScheduleRefs
	if missionStore != nil {
		destRefs = missionStore
		destScheduleRefs = missionStore
	}
	a.registerDestinations(srv.Handle, destinationStore, destRefs, destScheduleRefs, destinationTest)
	// Same nil-box guard as connLister above: a nil *workflows.Engine
	// boxed straight into workflowStarter would be a non-nil interface
	// value, breaking registerWorkflows' engine == nil gate on
	// startRun.
	var workflowStarterIface workflowStarter
	if workflowEngine != nil {
		workflowStarterIface = workflowEngine
	}
	a.registerWorkflows(srv.Handle, workflowStore, workflowStarterIface)
	a.registerEvents(srv.Handle, hub)
	a.registerTranscribe(srv.Handle, whisperClient, whisperURL)
	a.registerAttachments(srv.Handle, attachmentStore)
	srv.Handle("GET /v1/sessions", a.auth(http.HandlerFunc(a.handleList)))
	srv.Handle("POST /v1/sessions", a.auth(http.HandlerFunc(a.handleCreate)))
	srv.Handle("GET /v1/sessions/{id}", a.auth(http.HandlerFunc(a.handleTranscript)))
	a.registerLive(srv.Handle)
	srv.Handle("PATCH /v1/sessions/{id}", a.auth(http.HandlerFunc(a.handleUpdate)))
	srv.Handle("PUT /v1/sessions/{id}/knowledge", a.auth(http.HandlerFunc(a.handleSetKnowledge)))
	srv.Handle("DELETE /v1/sessions/{id}", a.auth(http.HandlerFunc(a.handleDelete)))
	srv.Handle("POST /v1/sessions/{id}/messages", a.auth(http.HandlerFunc(a.handleMessages)))
	srv.Handle("POST /v1/sessions/{id}/messages/retry", a.auth(http.HandlerFunc(a.handleRetry)))
	srv.Handle("POST /v1/sessions/{id}/stop", a.auth(http.HandlerFunc(a.handleStop)))
	srv.Handle("POST /v1/permissions/{id}", a.auth(http.HandlerFunc(a.handlePermission)))
	srv.Handle("GET /v1/permissions/pending", a.auth(http.HandlerFunc(a.handlePendingPermissions)))
	// Deprecated shim: same behavior, session_id in the body.
	srv.Handle("POST /v1/chat", a.auth(http.HandlerFunc(a.handleChatShim)))
	srv.Handle("POST /v1/chat/export-pdf", a.auth(http.HandlerFunc(a.handleExportMessagePDF)))
}

// handlePermission answers a parked tool call: {decision: once|session|deny}.
func (a *API) handlePermission(w http.ResponseWriter, r *http.Request) {
	if a.perms == nil {
		jsonError(w, http.StatusNotFound, "not_found", "permissions are not enabled")
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
	if !a.perms.Resolve(r.PathValue("id"), body.Decision) {
		jsonError(w, http.StatusNotFound, "not_found", "unknown or already-answered permission request")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handlePendingPermissions answers "does anything anywhere need my
// approval right now" for the global badge/toast — read-only, no side
// effects. Scoped to chat.Service.ActiveSessions() (the in-memory
// live-turn registry, not a DB column) so a permission_request whose
// turn already died without resolving it (crash, abandoned) never
// shows as pending forever: only currently-active turns are queried.
func (a *API) handlePendingPermissions(w http.ResponseWriter, r *http.Request) {
	active := a.svc.ActiveSessions()
	pending, err := a.dir.PendingPermissions(r.Context(), active)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "pending_permissions_failed", err.Error())
		return
	}
	if pending == nil {
		pending = []session.PendingPermission{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": pending})
}

// auth enforces the single bearer token. An unconfigured token fails
// closed with 503 — never open.
func (a *API) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.token == "" {
			jsonError(w, http.StatusServiceUnavailable, "auth_not_configured", "TIMOTHY_API_TOKEN is not set")
			return
		}
		got, ok := bearerToken(r)
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
			jsonError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}

// validSessionID keeps malformed path ids from reaching the driver: a
// non-UUID is a 404, not a 500 with a raw pgx error on the wire.
func validSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			hex := c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
			if !hex {
				return false
			}
		}
	}
	return true
}

func jsonError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// --- session management ---

func (a *API) handleList(w http.ResponseWriter, r *http.Request) {
	// The page cursor is the last row of the previous page: both halves
	// travel together so ties on updated_at cannot drop or repeat rows.
	var before time.Time
	beforeID := r.URL.Query().Get("before_id")
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "bad_request", "before must be an RFC3339Nano timestamp")
			return
		}
		if beforeID == "" {
			jsonError(w, http.StatusBadRequest, "bad_request", "before requires before_id")
			return
		}
		before = t
	} else if beforeID != "" {
		jsonError(w, http.StatusBadRequest, "bad_request", "before_id requires before")
		return
	}
	sessions, err := a.dir.List(r.Context(), r.URL.Query().Get("query"), before, beforeID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if sessions == nil {
		sessions = []session.Meta{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (a *API) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := a.dir.Create(r.Context(), req.Title)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (a *API) handleTranscript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validSessionID(id) {
		jsonError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	meta, err := a.dir.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		jsonError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	events, err := a.dir.Events(r.Context(), id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "events_failed", err.Error())
		return
	}
	items, err := session.UITranscript(events)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "projection_failed", err.Error())
		return
	}
	if items == nil {
		items = []session.TranscriptItem{}
	}
	for i := range items {
		if converted, target, asOf, ok := a.convertedCost(r.Context(), items[i].Cost, items[i].Currency); ok {
			items[i].ConvertedCost, items[i].ConvertedCurrency, items[i].RateAsOf = &converted, target, asOf
		}
	}
	// turn_active is read straight off chat.Service's own broadcaster
	// registry (TurnActive) — the single source of truth for "is a turn
	// running right now", not a separate flag that could drift from it.
	// a.svc is never nil (Register always constructs one), and
	// TurnActive itself is nil-map-safe, so this needs no guard.
	writeJSON(w, http.StatusOK, map[string]any{
		"session": meta, "items": items, "turn_active": a.svc.TurnActive(id),
	})
}

func (a *API) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title    *string `json:"title"`
		Archived *bool   `json:"archived"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Title == nil && req.Archived == nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "nothing to update")
		return
	}
	if !validSessionID(r.PathValue("id")) {
		jsonError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	if err := a.dir.Update(r.Context(), r.PathValue("id"), req.Title, req.Archived); err != nil {
		if strings.Contains(err.Error(), "not found") {
			jsonError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		jsonError(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateKnowledge rejects any name that isn't a real kb collection.
// Nil/empty names is always fine; a.kbCollections == nil (KB disabled)
// rejects any non-empty list outright.
func (a *API) validateKnowledge(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if a.kbCollections == nil {
		return errors.New("knowledge base is not configured")
	}
	collections, err := a.kbCollections(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(collections))
	for _, c := range collections {
		known[c.Name] = true
	}
	for _, name := range names {
		if !known[name] {
			return fmt.Errorf("unknown knowledge collection %q", name)
		}
	}
	return nil
}

// handleSetKnowledge replaces a session's pinned kb_collection names
// outright (the composer's # mention state), validated against the
// knowledge base's real collections.
func (a *API) handleSetKnowledge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validSessionID(id) {
		jsonError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	var req struct {
		Collections []string `json:"collections"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := a.validateKnowledge(r.Context(), req.Collections); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := a.dir.SetKnowledge(r.Context(), id, req.Collections); err != nil {
		if strings.Contains(err.Error(), "not found") {
			jsonError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		jsonError(w, http.StatusInternalServerError, "set_knowledge_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDelete permanently removes a session and all its
// session-scoped records. Irreversible; the web UI gates it behind an
// explicit confirmation.
func (a *API) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validSessionID(id) {
		jsonError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	if err := a.dir.Delete(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, session.ErrMissionReferenced):
			jsonError(w, http.StatusConflict, "mission_referenced",
				"a mission references this session; its transcript must stay")
		case strings.Contains(err.Error(), "not found"):
			jsonError(w, http.StatusNotFound, "not_found", "no such session")
		default:
			jsonError(w, http.StatusInternalServerError, "delete_failed", err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- chat ---

func (a *API) handleMessages(w http.ResponseWriter, r *http.Request) {
	var req chat.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.SessionID = r.PathValue("id")
	if !validSessionID(req.SessionID) {
		jsonError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	if err := a.validateKnowledge(r.Context(), req.Knowledge); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if _, err := a.dir.Get(r.Context(), req.SessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		jsonError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	a.streamTurn(w, r, func(ctx context.Context) (string, <-chan stream.StreamEvent, error) {
		return a.svc.Chat(ctx, req)
	})
}

// handleChatShim keeps the original /v1/chat contract alive for one
// deprecation window: session_id travels in the body and a missing one
// creates a session.
func (a *API) handleChatShim(w http.ResponseWriter, r *http.Request) {
	var req chat.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := a.validateKnowledge(r.Context(), req.Knowledge); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Link", `</v1/sessions/{id}/messages>; rel="successor-version"`)
	a.streamTurn(w, r, func(ctx context.Context) (string, <-chan stream.StreamEvent, error) {
		return a.svc.Chat(ctx, req)
	})
}

// handleRetry re-runs the session's last turn without re-persisting
// the user message: Chat already leaves it durable even on failure
// (chat.Service.Retry's doc explains why), so a naive resend of
// /messages would double it. Same terminal SSE contract as /messages.
func (a *API) handleRetry(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if !validSessionID(sessionID) {
		jsonError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	if _, err := a.dir.Get(r.Context(), sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		jsonError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	a.streamTurn(w, r, func(ctx context.Context) (string, <-chan stream.StreamEvent, error) {
		return a.svc.Retry(ctx, sessionID)
	})
}

// handleStop cancels sessionID's in-flight turn server-side (the turn
// now runs detached from any one request, so a client-side abort alone
// no longer stops it — see chat.Service.StopTurn). 404 "no_active_turn"
// mirrors handleLive's framing for the identical "nothing here" case:
// no turn running, whether because none was ever started or because it
// already finished by the time this request lands.
func (a *API) handleStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validSessionID(id) {
		jsonError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	if !a.svc.StopTurn(id) {
		jsonError(w, http.StatusNotFound, "no_active_turn", "no turn is currently running for this session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// meta is brain's terminal SSE event: session identity plus whatever
// the gateway attributed on done.
//
// Wire contract (deliberately different from the internal channel
// contract): brain's chat SSE stream ALWAYS ends with exactly one
// meta event, emitted after the relayed gateway terminal (done or
// error). Clients must read until meta, not stop at done. Provider,
// model, usage, ledger_id, cost, and currency are best-effort — absent
// when no provider attempt succeeded. Cost is nil when the gateway had
// no price for the serving model — unknown price is never guessed
// (D-013); Currency is blank in that case too.
type meta struct {
	Type       string        `json:"type"` // always "meta"
	SessionID  string        `json:"session_id"`
	Provider   string        `json:"provider,omitempty"`
	Model      string        `json:"model,omitempty"`
	Usage      *stream.Usage `json:"usage,omitempty"`
	LedgerID   string        `json:"ledger_id,omitempty"`
	DurationMs int64         `json:"duration_ms,omitempty"`
	Cost       *float64      `json:"cost,omitempty"`
	Currency   string        `json:"currency,omitempty"`
	// ConvertedCost/ConvertedCurrency/RateAsOf are additive display
	// fields: Cost/Currency always stay exactly what the gateway
	// billed (D-013). Computed at emit time from the user's
	// default_currency setting and the latest stored fx rate — never
	// persisted, since rates drift and session_events must keep only
	// billed truth. Present only when the target differs from the
	// billed currency and a usable rate exists; absent otherwise (the
	// pill then just falls back to the billed amount).
	ConvertedCost     *float64 `json:"converted_cost,omitempty"`
	ConvertedCurrency string   `json:"converted_currency,omitempty"`
	RateAsOf          string   `json:"rate_as_of,omitempty"`
}

// convertedCost resolves a.flags' default_currency and a.rates' latest
// USD-base table, then delegates to convertCost — nil flags/rates
// degrade to "nothing converts" (ok=false), same contract as
// UsageDecorator: a read must never fail or block on this.
func (a *API) convertedCost(ctx context.Context, cost *float64, billed string) (converted float64, target, asOf string, ok bool) {
	if a.flags == nil || a.rates == nil {
		return 0, "", "", false
	}
	target = a.flags.DefaultCurrency(ctx)
	rates, err := a.rates.LatestUSDRates(ctx)
	if err != nil {
		rates = nil // degrade to "nothing converts," never guess
	}
	return convertCost(cost, billed, target, rates)
}

// convertCost is the pure core of convertedCost, split out so it's
// table-testable without a live settings/fxrates Store (mirrors
// DecorateUsageResponse's split from UsageDecorator.Decorate in
// usage.go). ok is false — never an error — whenever cost is nil,
// billed is blank, target already equals billed, or no usable rate
// covers the pair.
func convertCost(cost *float64, billed, target string, rates map[string]fxrates.Rate) (converted float64, gotTarget, asOf string, ok bool) {
	if cost == nil || billed == "" || target == "" || target == billed {
		return 0, "", "", false
	}
	c, rate, convertOK := fxrates.Convert(*cost, billed, target, rates)
	if !convertOK {
		return 0, "", "", false
	}
	if !rate.AsOf.IsZero() {
		asOf = rate.AsOf.Format("2006-01-02")
	}
	return c, target, asOf, true
}

// streamHeartbeat keeps a chat SSE connection alive through proxies
// that kill an idle one (Cloudflare's ~100s 524 fires on a gap with no
// bytes flowing, not on total duration) — a slow model can go well
// past that between tokens. A comment line carries no event, so it
// never reaches the client's SSE parser or reducer.
const streamHeartbeat = 20 * time.Second

// streamTurn runs run (chat.Service.Chat or Retry) and relays its
// terminal-contract channel to the client as SSE; both callers only
// differ in how the turn starts, not in how it streams or ends.
func (a *API) streamTurn(w http.ResponseWriter, r *http.Request, run func(context.Context) (string, <-chan stream.StreamEvent, error)) {
	sessionID, events, err := run(r.Context())
	if err != nil {
		if errors.Is(err, chat.ErrBadRequest) {
			jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if errors.Is(err, chat.ErrNoRetryableTurn) {
			jsonError(w, http.StatusConflict, "no_retryable_turn", err.Error())
			return
		}
		if errors.Is(err, chat.ErrTurnInFlight) {
			jsonError(w, http.StatusConflict, "turn_in_flight", err.Error())
			return
		}
		// session_id rides the error when a row was already created so
		// the client reuses it on retry.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "chat_failed", "message": err.Error(), "session_id": sessionID,
		})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer cannot flush")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// The session id also rides a header so a mid-stream transport cut
	// (client never reaches the terminal meta) still can't orphan the
	// session — headers arrive before the first byte of the body.
	w.Header().Set("X-Session-Id", sessionID)

	send := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	m := meta{Type: "meta", SessionID: sessionID}
	ticker := time.NewTicker(streamHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if converted, target, asOf, convOK := a.convertedCost(r.Context(), m.Cost, m.Currency); convOK {
					m.ConvertedCost, m.ConvertedCurrency, m.RateAsOf = &converted, target, asOf
				}
				send(m)
				return
			}
			if ev.Type == stream.EventUsage {
				m.Usage = ev.Usage
			}
			if ev.Meta != nil {
				m.Provider, m.Model, m.LedgerID = ev.Meta.Provider, ev.Meta.Model, ev.Meta.LedgerID
				m.DurationMs = ev.Meta.DurationMs
				m.Cost, m.Currency = ev.Meta.Cost, ev.Meta.Currency
			}
			send(ev)
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
