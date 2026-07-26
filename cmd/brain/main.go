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
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/memclient"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/sandbox"
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
	store := session.NewStore(app.DB)
	flags := settings.New(app.DB, app.Log)
	// Runtime settings, editable in the UI without a restart: the
	// projected-context budget fallback and the skill-pack allowlist.
	budgetFn := func(ctx context.Context) int { return flags.TokenBudget(ctx, defaultTokenBudget) }
	compactor := session.NewCompactor(store, gwc, gwc, budgetFn, app.Log,
		app.Metrics.NewCounter("session_compactions_total", "Sessions compacted to stay under the context budget."))
	distill := func(ctx context.Context, sessionID, turnText string) *session.TurnMemory {
		return loop.DistillTurn(ctx, gwc, sessionID, turnText)
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

	toolCalls := app.Metrics.NewCounterVec("tool_calls_total",
		"Tool executions by tool name and outcome.", "tool", "outcome")
	agent, broker, outputs, builtinSet, buildErr := buildAgent(gwc, store, app.DB, workspace, searxngURL, markitdownURL, packs, flags.SkillAllowed, mc.Add, app.Log, toolCalls)
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
	}

	// Fail closed: an operator who set MISSION_SANDBOX_IMAGE opted into
	// sandboxed execution, so a manager that cannot initialize (daemon
	// unreachable, workspace mount unresolvable) must stop the service
	// loudly — silently falling back to executing model-authored
	// commands inside brain's own process would defeat the whole point.
	// A missing image is deliberately NOT fatal: the health check below
	// reports it, and building it needs no brain restart.
	missionSandbox, sberr := sandbox.NewManager(ctx, os.Getenv("MISSION_SANDBOX_IMAGE"), app.Log)
	if sberr != nil {
		fmt.Fprintln(os.Stderr, sberr)
		os.Exit(1)
	}

	missionStore, missionDriver, missionNotifier, missionWorkspace := buildMissions(ctx, app.DB, agent, store, workspace, flags, missionSandbox, app.Log)
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
			if err := missionSandbox.Ping(ctx); err != nil {
				return httpserver.Check{Status: "degraded", Detail: "docker daemon unreachable: " + err.Error()}
			}
			if err := missionSandbox.CheckImage(ctx); err != nil {
				return httpserver.Check{Status: "degraded", Detail: "sandbox image not found: " + err.Error()}
			}
			return httpserver.Check{Status: "ok"}
		})
	}

	agentReg := agents.NewStore(app.DB, app.Log)
	svc := chat.New(turnRouter{agent: agent, gw: gwc, flags: flags}, store, distill,
		gatedCompactor{inner: compactor, flags: flags}, budgetFn, packs, flags.SkillAllowed,
		agentReg.Resolve, app.Log)
	svc.SetAutoDispatch(agentReg.Enabled, chat.ClassifyOverGateway(gwc))
	svc.SetMemoryExtract(func(ctx context.Context, sessionID string, seq int64, text string) {
		if !flags.Enabled(ctx, settings.KeyMemoryExtraction) {
			return
		}
		ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), extractBudget)
		defer cancel()
		if _, err := mc.Extract(ectx, sessionID, seq, text); err != nil {
			app.Log.Warn("turn memory extraction failed", "session_id", sessionID, "error", err)
		}
	})
	compactor.SetMemoryExtract(func(ctx context.Context, sessionID string, seq int64, text string) []string {
		if !flags.Enabled(ctx, settings.KeyMemoryExtraction) {
			return nil
		}
		// Own deadline WITHIN the compaction budget: extraction must
		// never starve the summarize that follows it.
		ectx, cancel := context.WithTimeout(ctx, preCompactExtractBudget)
		defer cancel()
		ids, err := mc.Extract(ectx, sessionID, seq, text)
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

	api.Register(app.Server, svc, store, broker,
		memoryProxy(memorydURL, app.Log), adminProxy(gatewayURL, app.Log), flags,
		agentReg, conns, goog, agent, missionStore, missionDriver, missionNotifier,
		missionWorkspace, resolveSecret, token, app.Log)

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

// buildMissions wires the mission engine (Store, Driver, Notifier,
// Scheduler). Gated on WORKSPACES: no workspace root configured means
// missions stay entirely inert — no goroutines started, nothing
// scheduled, the API surface unmounted (registerMissions 404s on a nil
// store).
func buildMissions(ctx context.Context, db *pgpool.Pool, agent *loop.Agent, sessions *session.Store, toolWorkspaceRoot string, flags *settings.Store, sandboxMgr *sandbox.Manager, log *slog.Logger) (*missions.Store, *missions.Driver, *missions.Notifier, *missions.Workspace) {
	root := os.Getenv("WORKSPACES")
	if root == "" {
		log.Info("WORKSPACES not set; missions disabled")
		return nil, nil, nil, nil
	}
	store := missions.NewStore(db, log)
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
	// worker/reviewer shell, verify_cmd) OUT of brain's own process into
	// a per-mission Docker container; nil (MISSION_SANDBOX_IMAGE unset)
	// keeps the original in-process exec.CommandContext behavior.
	var sandboxExec func(context.Context, string, string, string, time.Duration, io.Writer) (int, error)
	if sandboxMgr != nil {
		sandboxExec = sandboxMgr.Exec
	}
	runner := missions.NewNativeRunnerWithFloor(agent, parker, floorDeny, sandboxExec, log)
	webhookURL := os.Getenv("NOTIFY_WEBHOOK_URL")
	notifier := missions.NewNotifier(db, webhookURL, log)
	// A second tools.Permissions instance, not the one buildAgent built —
	// it's stateless besides the shared db/root (Grant/Resolve hit
	// Postgres directly), so a fresh instance behaves identically. Used
	// only to pre-authorize a mission's hidden session at creation.
	perms := tools.NewPermissions(db, toolWorkspaceRoot)
	driver := missions.NewDriver(store, runner, workspace, notifier, sessions, perms, log)
	if sandboxMgr != nil {
		driver.SetSandbox(sandboxExec, sandboxMgr)
	}

	scheduler := missions.NewScheduler(db, store, log)
	go scheduler.Run(ctx)
	return store, driver, notifier, workspace
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

// buildAgent assembles the compiled-in tool registry and its guard
// rails (D-009, D-010). The returned builtin set is the fixed half of
// the tool surface; connector tools join it via swapAgentTools.
func buildAgent(gwc *gwclient.Client, store *session.Store, db *pgpool.Pool, workspace, searxngURL, markitdownURL string, packs []skills.Skill, skillAllow func(context.Context, string) bool, remember builtin.RememberFunc, log *slog.Logger, toolCalls *prometheus.CounterVec) (*loop.Agent, *loop.PermBroker, *tools.Outputs, []*tools.Tool, error) {
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
		return nil, nil, nil, nil, err
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
	// local model.
	if sensitiveRoute := os.Getenv("SENSITIVE_TOOL_ROUTE"); sensitiveRoute != "" {
		agent.SetForceRoute("gmail_read", sensitiveRoute)
		agent.SetForceRoute("gmail_read_attachment", sensitiveRoute)
	} else {
		log.Warn("SENSITIVE_TOOL_ROUTE not set; gmail_read/gmail_read_attachment output is processed on the turn's normal route, same as everything else")
	}
	return agent, broker, outputs, set, nil
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

// runConnectorReload loads connector sources at startup and keeps
// retrying/refreshing on a slow tick, mirroring the gateway's snapshot
// poll: a DB that wasn't ready at boot, or a row edited outside the
// admin API, converges within a minute.
func runConnectorReload(ctx context.Context, conns *connectors.Manager, log *slog.Logger) {
	if err := conns.Reload(ctx); err != nil {
		log.Warn("initial connector load failed; will retry", "error", err)
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
