// Command brain is Timothy's public API service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/api"
	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/memclient"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/sandboxclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/settings"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/internal/platform/service"
	"github.com/SumonMSelim/timothy/internal/secretstore"
	"github.com/SumonMSelim/timothy/migrations"
)

// defaultTokenBudget bounds the projected context; provider-window
// driven budgets arrive when model rows carry context windows.
const defaultTokenBudget = 60_000

// sensitiveToolSuffixes is the single source of truth for which tools'
// output must never leave the sensitive route floor once called: the
// in-turn pin (loop.Agent.SetForceRoute) and the side-call route
// (session.SensitiveTools, chat/memoryd/compactor) both key off this
// same list, so a tool added here is covered everywhere at once.
var sensitiveToolSuffixes = []string{"gmail_read", "gmail_read_attachment"}

const (
	serviceName = "brain"
	defaultPort = 8080
	// extractBudget bounds the fire-and-forget turn-end extraction:
	// one memoryd round trip (LLM + embed) plus slack.
	extractBudget = 90 * time.Second
	// retrieveBudget sits ON the turn's critical path — one embed +
	// three SQL legs, so tight.
	retrieveBudget = 10 * time.Second
	// preCompactExtractBudget bounds the extraction INSIDE the
	// compaction pass so a slow extraction degrades to empty facts
	// instead of starving the summarize.
	preCompactExtractBudget = 45 * time.Second
	// connectorReadyWait bounds how long a turn waits at entry for the
	// first connector load (D-043) — long enough to absorb the DB pool
	// warming at boot, short enough that a turn never meaningfully hangs
	// on it.
	connectorReadyWait = 15 * time.Second
)

func main() {
	healthcheck := flag.Bool("healthcheck", false,
		"probe /health and exit; used as the container health check")
	flag.Parse()
	if *healthcheck {
		os.Exit(service.ProbeHealth(defaultPort))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := service.New(ctx, serviceName, defaultPort, migrations.FS)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	token := os.Getenv("TIMOTHY_API_TOKEN")
	if token == "" {
		app.Log.Warn("TIMOTHY_API_TOKEN not set; API requests will be rejected")
	}
	app.AddCheck("auth", func() httpserver.Check {
		if token == "" {
			return httpserver.Check{Status: "degraded", Detail: "TIMOTHY_API_TOKEN not set"}
		}
		return httpserver.Check{Status: "ok"}
	})

	gatewayURL := os.Getenv("GATEWAY_URL")
	if gatewayURL == "" {
		gatewayURL = "http://gateway:8081"
	}
	gwc := gwclient.New(gatewayURL)
	store := session.NewStore(app.DB, app.Log)
	flags := settings.New(app.DB, app.Log)
	// Runtime settings, editable in the UI without a restart: the
	// projected-context budget fallback and the skill-pack allowlist.
	budgetFn := func(ctx context.Context) int { return flags.TokenBudget(ctx, defaultTokenBudget) }
	compactor := session.NewCompactor(store, gwc, gwc, budgetFn, app.Log,
		app.Metrics.NewCounter("session_compactions_total", "Sessions compacted to stay under the context budget."))
	distill := func(ctx context.Context, sessionID, turnText, route string) *session.TurnMemory {
		return loop.DistillTurn(ctx, gwc, sessionID, turnText, route)
	}

	workspace := os.Getenv("WORKSPACE_ROOT")
	if workspace == "" {
		workspace = "/workspace"
	}
	skillsDir := os.Getenv("SKILLS_DIR")
	if skillsDir == "" {
		skillsDir = "/skills"
	}
	// Broken packs degrade health rather than crash the service.
	// All loaded packs stay in memory; the skills_allowlist runtime
	// setting gates which ones the agent may reach per turn.
	packs, err := skills.Load(skillsDir)
	if err != nil {
		app.Log.Error("skills failed to load; continuing without them", "error", err)
	}
	app.AddCheck("skills", func() httpserver.Check {
		if err != nil {
			return httpserver.Check{Status: "degraded", Detail: err.Error()}
		}
		return httpserver.Check{Status: "ok"}
	})

	memorydURL := os.Getenv("MEMORYD_URL")
	if memorydURL == "" {
		memorydURL = "http://memoryd:8082"
	}
	mc := memclient.New(memorydURL)

	searxngURL := os.Getenv("SEARXNG_URL")
	markitdownURL := os.Getenv("MARKITDOWN_URL")
	if markitdownURL == "" {
		app.Log.Warn("MARKITDOWN_URL not set; PDF attachments in chat are unavailable")
	}
	whisperURL := os.Getenv("WHISPER_URL")
	if whisperURL == "" {
		app.Log.Warn("WHISPER_URL not set; the web mic button's /v1/transcribe endpoint is unavailable")
	}

	toolCalls := app.Metrics.NewCounterVec("tool_calls_total",
		"Tool executions by tool name and outcome.", "tool", "outcome")
	// sensitiveRoute resolves the route sensitive-tool turns and their
	// side-calls (extraction, compaction summarize) pin to: the runtime
	// setting, else "" (feature off). A func, not a value snapshotted at
	// startup, so a settings change from the web UI applies to the next
	// turn without a restart.
	sensitiveRoute := func(ctx context.Context) string {
		return flags.Value(ctx, settings.ValueSensitiveToolRoute)
	}
	agent, broker, outputs, builtinSet, chatPerms, buildErr := buildAgent(gwc, store, app.DB, workspace, searxngURL, markitdownURL, packs, flags.SkillAllowed, mc.Add, app.Log, toolCalls, sensitiveRoute)
	if buildErr != nil {
		fmt.Fprintln(os.Stderr, buildErr)
		os.Exit(1)
	}
	go runOutputGC(ctx, outputs, app.Log)

	secrets, err := buildSecretStore(app.DB, app.Log)
	if err != nil {
		app.Log.Warn("secret store disabled", "error", err)
	}
	var resolveSecret func(context.Context, string) (string, error)
	if secrets != nil {
		resolveSecret = secrets.Resolve
	}

	conns, goog := buildConnectors(app.DB, secrets, app.Log)
	if conns != nil {
		conns.RegisterBuilder("mcp", connectors.MCPBuilder(nil))
		if goog != nil {
			conns.RegisterBuilder("google", goog.Builder())
		}
		conns.SetOnReload(func(context.Context) {
			swapAgentTools(agent, builtinSet, conns, app.Log, toolCalls)
		})
		go runConnectorReload(ctx, conns, app.Log)
		app.AddCheck("connectors", func() httpserver.Check {
			select {
			case <-conns.Ready():
				return httpserver.Check{Status: "ok"}
			default:
				return httpserver.Check{Status: "degraded", Detail: "initial connector load not yet complete"}
			}
		})
		// Only the first turn after boot pays this: once conns.Ready()
		// closes, WaitReady returns instantly on every later call. Bounded
		// so a turn NEVER blocks indefinitely — a slow/never-ready load
		// degrades to builtins-only instead of hanging the turn.
		agent.SetWaitToolsReady(func(ctx context.Context) {
			wctx, cancel := context.WithTimeout(ctx, connectorReadyWait)
			defer cancel()
			if err := conns.WaitReady(wctx); err != nil {
				app.Log.Warn("connector tools not yet loaded; turn proceeds with builtin tools only")
			}
		})
	}

	// SANDBOXD_URL empty (MISSION_SANDBOX_IMAGE unset in compose) keeps a
	// nil client and the original in-process exec fallback — same
	// nil-able-dependency convention as every other optional brain
	// dependency. Unlike the old direct-socket path, brain itself can no
	// longer fail closed on a Docker problem: that fail-closed behavior
	// moved to sandboxd's own boot (it holds the socket now), and a
	// sandboxd that's unreachable or degraded surfaces here as a
	// degraded health check, with missions failing as infra at exec
	// time instead of brain refusing to start.
	var missionSandbox *sandboxclient.Client
	if sandboxdURL := os.Getenv("SANDBOXD_URL"); sandboxdURL != "" {
		missionSandbox = sandboxclient.New(sandboxdURL)
	}

	// Built here, above buildMissions, so both the scheduler (fire-time
	// route/review_route/budget/prompt_overlay resolution) and the
	// driver (ApprovalAllowlist grants at provisioning time) can close
	// over the same registry.
	agentReg := agents.NewStore(app.DB, app.Log)

	routeForRole := func(ctx context.Context, role string) string {
		name, ok, err := gwc.RouteForRole(ctx, role)
		if err != nil || !ok {
			return ""
		}
		return name
	}
	missionStore, missionDriver, missionNotifier, missionWorkspace, missionHub := buildMissions(ctx, app.DB, agent, store, workspace, flags, missionSandbox, agentReg, routeForRole, app.Log)
	if missionDriver != nil {
		var sandboxSweeper interface {
			Sweep(ctx context.Context, isTerminal func(missionID string) bool) error
		}
		if missionSandbox != nil {
			sandboxSweeper = missionSandbox
		}
		go missions.RecoverAndSweep(ctx, missionDriver, missionStore, missionWorkSlotMax, sandboxSweeper, app.Log)
	}
	if missionSandbox != nil {
		app.AddCheck("sandbox", func() httpserver.Check {
			if err := missionSandbox.Health(ctx); err != nil {
				return httpserver.Check{Status: "degraded", Detail: err.Error()}
			}
			return httpserver.Check{Status: "ok"}
		})
	}

	// sensitiveConnectorNames is the connector-level input to
	// SensitiveTools: every enabled connector marked sensitive in
	// settings, matched as a namespace PREFIX (Matches' ConnectorNames
	// rule) since "<connector-name>_<tool-name>" puts the connector's own
	// name in front of every tool it serves. Computed fresh each call,
	// not cached, so toggling a connector's sensitive flag takes effect
	// on the next side-call without a restart — same reasoning as
	// sensitiveRoute above. conns is nil when the connector surface
	// itself is disabled (no secret store).
	sensitiveConnectorNames := func(ctx context.Context) []string {
		if conns == nil {
			return nil
		}
		names, err := conns.SensitiveNames(ctx)
		if err != nil {
			app.Log.Warn("sensitive connector lookup failed; connector-level sensitivity off this call", "error", err)
			return nil
		}
		return names
	}

	// Single source of truth for "this turn/session executed a sensitive
	// tool": the loop's in-turn SetForceRoute pin above and this
	// SensitiveTools value share sensitiveToolSuffixes and the same
	// sensitiveRoute resolver, so side-calls (extraction, compaction
	// summarize) honor the same route floor the tool loop already
	// pinned the turn to. Wired unconditionally — sensitiveRoute
	// resolving to "" at call time means the feature is currently off,
	// same as before, but now editable at runtime from the settings UI.
	sensitiveTools := &session.SensitiveTools{
		Suffixes:       func(context.Context) []string { return sensitiveToolSuffixes },
		ConnectorNames: sensitiveConnectorNames,
		Route:          sensitiveRoute,
	}
	// Connector-level sensitivity's in-turn counterpart: a whole
	// connector flagged sensitive must pin the SAME turn that calls its
	// tool, not just every turn after (chat.pinSensitiveRoute) — without
	// this, a search/read tool's own results ride the turn's ORIGINAL
	// route (e.g. a cloud default) until the NEXT turn notices the
	// session is sensitive, one turn too late for whatever content that
	// tool just pulled into context. Wired here, after conns exists,
	// rather than inside buildAgent (which runs before conns is built).
	agent.SetForceRouteByConnector(sensitiveConnectorNames, sensitiveRoute)

	svc := chat.New(turnRouter{agent: agent, gw: gwc, flags: flags}, store, distill,
		gatedCompactor{inner: compactor, flags: flags}, budgetFn, packs, flags.SkillAllowed,
		agentReg.Resolve, app.Log)
	svc.SetAutoDispatch(agentReg.Enabled, chat.ClassifyOverGateway(gwc))
	svc.SetSensitiveTools(sensitiveTools)
	// Nil-safe: missionHub is nil when WORKSPACES is unset (see
	// buildMissions), in which case SetSessionHub's publish func is
	// never called (chat.go only invokes it when non-nil), leaving
	// today's behavior (no push) unchanged.
	if missionHub != nil {
		svc.SetSessionHub(func(sessionID string) {
			missionHub.Publish(missions.Signal{Kind: "session", ID: sessionID})
		})
		svc.SetPermissionHub(func(sessionID string) {
			missionHub.Publish(missions.Signal{Kind: "permission", ID: sessionID})
		})
	}
	// Same Permissions instance (and session_grants table) the chat
	// agent loop's own Resolve chain reads from — seeding here is
	// visible to that exact chain, not a parallel grant store.
	svc.SetApprovalGrants(chatPerms)
	compactor.SetSensitiveTools(sensitiveTools)
	svc.SetMemoryExtract(func(ctx context.Context, sessionID string, seq int64, text, route string) {
		if !flags.Enabled(ctx, settings.KeyMemoryExtraction) {
			return
		}
		ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), extractBudget)
		defer cancel()
		if _, err := mc.Extract(ectx, sessionID, seq, text, route); err != nil {
			app.Log.Warn("turn memory extraction failed", "session_id", sessionID, "error", err)
		}
	})
	compactor.SetMemoryExtract(func(ctx context.Context, sessionID string, seq int64, text, route string) []string {
		if !flags.Enabled(ctx, settings.KeyMemoryExtraction) {
			return nil
		}
		// Own deadline WITHIN the compaction budget: extraction must
		// never starve the summarize that follows it.
		ectx, cancel := context.WithTimeout(ctx, preCompactExtractBudget)
		defer cancel()
		ids, err := mc.Extract(ectx, sessionID, seq, text, route)
		if err != nil {
			app.Log.Warn("pre-compaction extraction failed", "session_id", sessionID, "error", err)
			return nil
		}
		return ids
	})
	svc.SetMemoryRetrieve(func(ctx context.Context, sessionID, query string) string {
		rctx, cancel := context.WithTimeout(ctx, retrieveBudget)
		defer cancel()
		memories, err := mc.Retrieve(rctx, sessionID, query)
		if err != nil {
			app.Log.Warn("memory retrieval failed; turn continues without", "session_id", sessionID, "error", err)
			return ""
		}
		return memclient.RenderBlock(memories)
	})

	attachmentStore := buildAttachments(app.DB, app.Log)
	// Nil-safe: a nil *attachments.Store boxed into the AttachmentStore
	// interface would be a non-nil interface value, breaking chat.go's
	// `s.attachments == nil` gate — so this only wires when non-nil,
	// same guard shape as the missionHub check above.
	if attachmentStore != nil {
		svc.SetAttachments(attachmentStore)
	}
	if markitdownURL != "" {
		svc.SetMarkitdown(markitdownURL)
	}

	api.Register(app.Server, svc, store, broker,
		memoryProxy(memorydURL, app.Log), adminProxy(gatewayURL, app.Log), flags,
		agentReg, conns, goog, agent, missionStore, missionDriver, missionNotifier,
		missionWorkspace, resolveSecret, routeForRole, missionHub, attachmentStore, &http.Client{}, whisperURL, token, app.Log)

	if err := app.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		app.Log.Error("server exited", "error", err)
		os.Exit(1)
	}
}

// buildSecretStore builds brain's secret-store handle (same DB, same
// master key as the gateway) — shared by connectors and mission push,
// so it's built once here rather than each caller decoding the master
// key independently. A nil return (with an error to log) means an
// unusable master key or init failure; callers nil-gate on it.
func buildSecretStore(db *pgpool.Pool, log *slog.Logger) (*secretstore.Store, error) {
	masterKey, err := secretstore.DecodeMasterKey(os.Getenv(secretstore.MasterKeyEnv))
	if err != nil {
		return nil, fmt.Errorf("no usable master key: %w", err)
	}
	secrets, err := secretstore.New(db, masterKey)
	if err != nil {
		return nil, fmt.Errorf("secret store init failed: %w", err)
	}
	return secrets, nil
}

// buildAttachments wires the image-attachment store (D-045).
// ATTACHMENTS_DIR unset means the feature is off: no store, chat
// rejects attachment refs, and the API surface stays unmounted.
func buildAttachments(db *pgpool.Pool, log *slog.Logger) *attachments.Store {
	dir := os.Getenv("ATTACHMENTS_DIR")
	if dir == "" {
		log.Info("ATTACHMENTS_DIR not set; image attachments disabled")
		return nil
	}
	return attachments.New(dir, db)
}

// buildConnectors wires the integration control plane. secrets is
// brain's already-built secret-store handle (nil when unavailable,
// e.g. no valid master key) — a nil store still nil-gates the
// connector surface exactly as before, it's just built once in main()
// instead of here. The Google half additionally needs
// TIMOTHY_PUBLIC_URL for the OAuth redirect; without it google
// connectors are configured but cannot connect.
func buildConnectors(db *pgpool.Pool, secrets *secretstore.Store, log *slog.Logger) (*connectors.Manager, *connectors.Google) {
	if secrets == nil {
		log.Warn("connectors disabled: no secret store")
		return nil, nil
	}
	resolve := func(ctx context.Context, ref string) (string, error) {
		return secrets.Resolve(ctx, ref)
	}
	store := connectors.NewStore(db, log)
	mgr := connectors.NewManager(store, resolve, log)

	publicURL := os.Getenv("TIMOTHY_PUBLIC_URL")
	if publicURL == "" {
		log.Warn("TIMOTHY_PUBLIC_URL not set; google connectors cannot run their OAuth flow")
	}
	goog := connectors.NewGoogle(secrets, store, publicURL, log)
	if markItDownURL := os.Getenv("MARKITDOWN_URL"); markItDownURL != "" {
		goog.MarkItDownURL = markItDownURL
	} else {
		log.Warn("MARKITDOWN_URL not set; gmail_read falls back to a snippet for HTML-only mail, and gmail_read_attachment is unavailable")
	}
	return mgr, goog
}

// missionWorkSlotMax bounds how many missions may be status='working'
// at once — a conservative default until this needs to be a runtime
// setting.
const missionWorkSlotMax = 4

// missionAgentResolver adapts agentReg.ResolveByID to scheduler.go's
// AgentResolver / driver.go's SetAgentResolver shape — both need the
// SAME resolution (route/review_route/budget/prompt_overlay/
// approval_allowlist from an agents row), just at different moments
// (schedule fire time vs mission provisioning time), so one adapter
// serves both call sites.
func missionAgentResolver(agentReg *agents.Store) missions.AgentResolver {
	return func(ctx context.Context, agentID string) (missions.AgentDefaults, bool) {
		a, ok := agentReg.ResolveByID(ctx, agentID)
		if !ok {
			return missions.AgentDefaults{}, false
		}
		return missions.AgentDefaults{
			Route: a.Route, ReviewRoute: a.ReviewRoute, PromptOverlay: a.PromptOverlay,
			BudgetUSD: a.BudgetUSD, ApprovalAllowlist: a.ApprovalAllowlist,
		}, true
	}
}

// buildMissions wires the mission engine (Store, Driver, Notifier,
// Scheduler, Hub). Gated on WORKSPACES: no workspace root configured
// means missions stay entirely inert — no goroutines started, nothing
// scheduled, the API surface unmounted (registerMissions 404s on a nil
// store). agentReg is D-034's agent registry, resolved at scheduler
// fire time and mission provisioning time (never schedule-create
// time) so an agent edited after the fact still applies. The hub
// lives inside the same gate as everything else here — no missions,
// no push events either.
func buildMissions(ctx context.Context, db *pgpool.Pool, agent *loop.Agent, sessions *session.Store, toolWorkspaceRoot string, flags *settings.Store, sandboxMgr *sandboxclient.Client, agentReg *agents.Store, routeForRole func(context.Context, string) string, log *slog.Logger) (*missions.Store, *missions.Driver, *missions.Notifier, *missions.Workspace, *missions.Hub) {
	root := os.Getenv("WORKSPACES")
	if root == "" {
		log.Info("WORKSPACES not set; missions disabled")
		return nil, nil, nil, nil, nil
	}
	hub := missions.NewHub()
	store := missions.NewStore(db, log)
	store.SetHub(hub)
	identity := func(ctx context.Context) (string, string) {
		return flags.Value(ctx, settings.ValueGitAuthorName), flags.Value(ctx, settings.ValueGitAuthorEmail)
	}
	workspace := missions.NewWorkspace(root, identity, log)
	parker := missions.NewStorePermissionParker(store, log)
	// MISSION_MODEL_FLOOR lists model-name substrings (comma-separated)
	// too weak to drive tool-using mission turns; a turn served by one
	// pauses the mission immediately instead of burning its iteration
	// budget. Unset = floor disabled.
	var floorDeny []string
	for _, s := range strings.Split(os.Getenv("MISSION_MODEL_FLOOR"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			floorDeny = append(floorDeny, s)
		}
	}
	// sandboxMgr non-nil routes model-authored command execution (the
	// worker/reviewer shell, verify_cmd) OUT of brain's own process,
	// through sandboxd, into a per-mission Docker container; nil
	// (SANDBOXD_URL unset) keeps the original in-process
	// exec.CommandContext behavior.
	var sandboxExec func(context.Context, string, string, string, time.Duration, io.Writer) (int, error)
	if sandboxMgr != nil {
		sandboxExec = sandboxMgr.Exec
	}
	runner := missions.NewNativeRunnerWithFloor(agent, parker, floorDeny, sandboxExec, log)
	webhookURL := os.Getenv("NOTIFY_WEBHOOK_URL")
	notifier := missions.NewNotifier(db, webhookURL, log)
	notifier.SetHub(hub)
	// A second tools.Permissions instance, not the one buildAgent built —
	// it's stateless besides the shared db/root (Grant/Resolve hit
	// Postgres directly), so a fresh instance behaves identically. Used
	// only to pre-authorize a mission's hidden session at creation.
	perms := tools.NewPermissions(db, toolWorkspaceRoot)
	driver := missions.NewDriver(store, runner, workspace, notifier, sessions, perms, log)
	if sandboxMgr != nil {
		driver.SetSandbox(sandboxExec, sandboxMgr)
	}
	resolveAgent := missionAgentResolver(agentReg)
	driver.SetAgentResolver(resolveAgent)

	schedulerEnabled := func(ctx context.Context) bool { return flags.Enabled(ctx, settings.KeyScheduler) }
	scheduler := missions.NewScheduler(db, store, resolveAgent, schedulerEnabled, routeForRole, log)
	go scheduler.Run(ctx)
	return store, driver, notifier, workspace, hub
}

// memoryProxy forwards the web's memory-management routes to memoryd
// verbatim — brain adds only the bearer gate. The search route maps
// onto memoryd's retrieve endpoint.
func memoryProxy(memorydURL string, log *slog.Logger) http.Handler {
	target, err := url.Parse(memorydURL)
	if err != nil {
		log.Error("invalid MEMORYD_URL; memory routes disabled", "error", err)
		return nil
	}
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			if r.In.URL.Path == "/v1/memories/search" {
				r.Out.URL.Path = "/v1/retrieve"
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("memoryd proxy error", "path", r.URL.Path, "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"code":"memoryd_unreachable","message":"memory service unavailable"}}`))
		},
	}
}

// adminProxy forwards the browser's control-plane reads to the
// gateway's internal API: /v1/admin/usage/* maps onto
// /internal/usage/*. Brain adds only the bearer gate.
func adminProxy(gatewayURL string, log *slog.Logger) http.Handler {
	target, err := url.Parse(gatewayURL)
	if err != nil {
		log.Error("invalid GATEWAY_URL; admin routes disabled", "error", err)
		return nil
	}
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = "/internal/admin/" + strings.TrimPrefix(r.In.URL.Path, "/v1/admin/")
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("gateway admin proxy error", "path", r.URL.Path, "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"code":"gateway_unreachable","message":"gateway unavailable"}}`))
		},
	}
}

// gatedCompactor honors the compaction feature switch: off means
// sessions simply grow until it is flipped back on.
type gatedCompactor struct {
	inner chat.Compactor
	flags *settings.Store
}

func (g gatedCompactor) MaybeCompact(ctx context.Context, sessionID string) error {
	if !g.flags.Enabled(ctx, settings.KeyCompaction) {
		return nil
	}
	return g.inner.MaybeCompact(ctx, sessionID)
}

// turnRouter sends chat turns through the agent loop and everything
// else (titles, distills, compaction summaries) straight to the
// gateway. With the tools switch off, chat turns bypass the agent
// loop entirely — plain pass-through completion.
type turnRouter struct {
	agent *loop.Agent
	gw    chat.Gateway
	flags *settings.Store
}

func (r turnRouter) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	if req.Purpose != "chat" || !r.flags.Enabled(ctx, settings.KeyTools) {
		return r.gw.Stream(ctx, req)
	}
	return r.agent.Start(ctx, loop.Request{
		SessionID: req.SessionID,
		Route:     req.Route,
		Agent:     req.Agent,
		ToolAllow: req.ToolAllow,
		ModelHint: req.ModelHint,
		System:    req.System,
		Messages:  req.Messages,
	})
}

func (r turnRouter) RouteForRole(ctx context.Context, role string) (string, bool, error) {
	return r.gw.RouteForRole(ctx, role)
}

// buildAgent assembles the compiled-in tool registry and its guard
// rails (D-009, D-010). The returned builtin set is the fixed half of
// the tool surface; connector tools join it via swapAgentTools.
func buildAgent(gwc *gwclient.Client, store *session.Store, db *pgpool.Pool, workspace, searxngURL, markitdownURL string, packs []skills.Skill, skillAllow func(context.Context, string) bool, remember builtin.RememberFunc, log *slog.Logger, toolCalls *prometheus.CounterVec, sensitiveRoute func(context.Context) string) (*loop.Agent, *loop.PermBroker, *tools.Outputs, []*tools.Tool, *tools.Permissions, error) {
	outputs := tools.NewOutputs(db)
	set := []*tools.Tool{
		builtin.CurrentTime(time.Now),
		builtin.ConvertTime(),
		builtin.Calculator(),
		builtin.WebFetch(builtin.WebFetchConfig{MarkitdownURL: markitdownURL}),
		builtin.Shell(builtin.ShellConfig{WorkspaceRoot: workspace}),
		builtin.RetrieveOutput(outputs),
		builtin.Remember(remember),
		builtin.ConvertCurrency(),
	}
	// Search is optional infra: only registered when a backend is
	// configured, so an environment without SearXNG still runs clean.
	if searxngURL != "" {
		set = append(set, builtin.WebSearch(searxngURL))
	}
	if len(packs) > 0 {
		set = append(set, skills.LoadSkillTool(packs, skillAllow))
	}
	constrained, defs, err := compileToolset(set, nil, log, toolCalls)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	perms := tools.NewPermissions(db, workspace)
	broker := loop.NewPermBroker()
	agent := loop.NewAgent(gwc, constrained, perms, outputs, tools.NewAudit(db), store, broker, defs, log)
	// Shell dumps grow fast; offload them sooner than the default so a
	// long command output never bloats the context (D-019).
	agent.SetOffloadThreshold("shell", 4<<10)
	// Reading real email content is sensitive: once a Gmail read tool
	// fires, pin the rest of the turn to a trusted route (e.g. one
	// chained only to a local Ollama provider) instead of whatever
	// route the turn started on — optional, since not everyone runs a
	// local model. sensitiveRoute resolving to "" at flip time means
	// the feature is currently off; wired unconditionally since the
	// route is now runtime-configurable from the settings UI, not just
	// boot-time env.
	for _, suffix := range sensitiveToolSuffixes {
		agent.SetForceRoute(suffix, sensitiveRoute)
	}
	return agent, broker, outputs, set, perms, nil
}

// compileToolset registers builtin + connector tools into a fresh
// constrained registry. A connector tool whose name collides with an
// existing tool is skipped with a log — a remote server must never
// shadow a builtin.
func compileToolset(builtins, connectorTools []*tools.Tool, log *slog.Logger, toolCalls *prometheus.CounterVec) (*tools.Constrained, []provider.ToolDef, error) {
	reg := tools.NewRegistry()
	for _, t := range builtins {
		if err := reg.Register(t); err != nil {
			return nil, nil, fmt.Errorf("register tools: %w", err)
		}
	}
	for _, t := range connectorTools {
		if err := reg.Register(t); err != nil {
			log.Warn("connector tool skipped", "tool", t.Name, "error", err)
		}
	}
	constrained, err := tools.NewConstrained(reg, toolCalls)
	if err != nil {
		return nil, nil, fmt.Errorf("compile tool schemas: %w", err)
	}
	constrained.SetClamp("shell", builtin.ShellTimeoutClamp())

	defs := make([]provider.ToolDef, 0, len(reg.List()))
	for _, t := range reg.List() {
		defs = append(defs, provider.ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return constrained, defs, nil
}

// swapAgentTools recompiles builtin + connector tools and swaps them
// into the agent. A compile failure keeps the previous surface — a
// broken connector schema must not take down the builtins.
func swapAgentTools(agent *loop.Agent, builtins []*tools.Tool, conns *connectors.Manager, log *slog.Logger, toolCalls *prometheus.CounterVec) {
	constrained, defs, err := compileToolset(builtins, conns.Tools(), log, toolCalls)
	if err != nil {
		log.Warn("connector toolset compile failed; keeping previous tools", "error", err)
		return
	}
	agent.SwapTools(constrained, defs)
	log.Info("agent tool surface updated", "tools", len(defs))
}

// initialReloadRetry bounds the wait between initial-load retries: the
// DB pool is often still warming right at boot (ErrDegraded), and
// falling back to the full-minute ticker for that first success would
// leave chat turns running builtins-only tools for up to a minute.
const initialReloadRetry = 5 * time.Second

// runConnectorReload loads connector sources at startup and keeps
// retrying/refreshing on a slow tick, mirroring the gateway's snapshot
// poll: a DB that wasn't ready at boot, or a row edited outside the
// admin API, converges within a minute. The first load retries on the
// short initialReloadRetry cadence until it succeeds once, THEN falls
// into the normal minute ticker — later reloads don't need the fast
// path since the agent already has a tool surface.
func runConnectorReload(ctx context.Context, conns *connectors.Manager, log *slog.Logger) {
	for {
		if err := conns.Reload(ctx); err != nil {
			log.Warn("initial connector load failed; will retry", "error", err)
		} else {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialReloadRetry):
		}
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conns.Reload(ctx); err != nil {
				log.Warn("connector reload failed; keeping previous sources", "error", err)
			}
		}
	}
}

// runOutputGC sweeps expired offloaded outputs (D-019 retention).
func runOutputGC(ctx context.Context, outputs *tools.Outputs, log *slog.Logger) {
	const retention = 7 * 24 * time.Hour
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gctx, cancel := context.WithTimeout(ctx, time.Minute)
			removed, err := outputs.GC(gctx, retention)
			cancel()
			if err != nil {
				log.Warn("tool output gc", "error", err)
			} else if removed > 0 {
				log.Info("tool output gc", "removed", removed)
			}
		}
	}
}
