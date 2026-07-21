// Command brain is Timothy's public API service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/api"
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/memclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/settings"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/secretstore"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/internal/platform/service"
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
	budget := defaultTokenBudget
	if v := os.Getenv("SESSION_TOKEN_BUDGET"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			budget = n
		}
	}

	gwc := gwclient.New(gatewayURL)
	store := session.NewStore(app.DB)
	flags := settings.New(app.DB, app.Log)
	compactor := session.NewCompactor(store, gwc, gwc, budget, app.Log,
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
	packs, err := skills.Load(skillsDir)
	if err != nil {
		app.Log.Error("skills failed to load; continuing without them", "error", err)
	}
	// SKILLS_ALLOWLIST restricts which loaded packs the agent may reach
	// for, without deleting the others from disk — comma-separated
	// names, empty means no restriction.
	if allow := os.Getenv("SKILLS_ALLOWLIST"); allow != "" {
		allowed := make(map[string]bool)
		for _, name := range strings.Split(allow, ",") {
			allowed[strings.TrimSpace(name)] = true
		}
		filtered := packs[:0]
		for _, p := range packs {
			if allowed[p.Name] {
				filtered = append(filtered, p)
			}
		}
		packs = filtered
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

	agent, broker, outputs, builtinSet, buildErr := buildAgent(gwc, store, app.DB, workspace, searxngURL, packs, mc.Add, app.Log)
	if buildErr != nil {
		fmt.Fprintln(os.Stderr, buildErr)
		os.Exit(1)
	}
	go runOutputGC(ctx, outputs, app.Log)

	conns, goog := buildConnectors(app.DB, app.Log)
	if conns != nil {
		conns.RegisterBuilder("mcp", connectors.MCPBuilder(nil))
		if goog != nil {
			conns.RegisterBuilder("google", goog.Builder())
		}
		conns.SetOnReload(func(context.Context) {
			swapAgentTools(agent, builtinSet, conns, app.Log)
		})
		go runConnectorReload(ctx, conns, app.Log)
	}

	svc := chat.New(turnRouter{agent: agent, gw: gwc, flags: flags}, store, distill,
		gatedCompactor{inner: compactor, flags: flags}, budget, packs, app.Log)
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
		conns, goog, token, app.Log)

	if err := app.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		app.Log.Error("server exited", "error", err)
		os.Exit(1)
	}
}

// buildConnectors wires the integration control plane. Brain resolves
// connector credentials through its own secret-store handle (same DB,
// same master key as the gateway); without a valid master key the
// connector surface stays unmounted and the rest of brain runs. The
// Google half additionally needs TIMOTHY_PUBLIC_URL for the OAuth
// redirect; without it google connectors are configured but cannot
// connect.
func buildConnectors(db *pgpool.Pool, log *slog.Logger) (*connectors.Manager, *connectors.Google) {
	masterKey, err := secretstore.DecodeMasterKey(os.Getenv(secretstore.MasterKeyEnv))
	if err != nil {
		log.Warn("connectors disabled: no usable master key", "error", err)
		return nil, nil
	}
	secrets, err := secretstore.New(db, masterKey)
	if err != nil {
		log.Warn("connectors disabled: secret store init failed", "error", err)
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
	return mgr, goog
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
		SessionID:    req.SessionID,
		TaskCategory: req.TaskCategory,
		ModelHint:    req.ModelHint,
		System:       req.System,
		Messages:     req.Messages,
	})
}

// buildAgent assembles the compiled-in tool registry and its guard
// rails (D-009, D-010). The returned builtin set is the fixed half of
// the tool surface; connector tools join it via swapAgentTools.
func buildAgent(gwc *gwclient.Client, store *session.Store, db *pgpool.Pool, workspace, searxngURL string, packs []skills.Skill, remember builtin.RememberFunc, log *slog.Logger) (*loop.Agent, *loop.PermBroker, *tools.Outputs, []*tools.Tool, error) {
	outputs := tools.NewOutputs(db)
	set := []*tools.Tool{
		builtin.CurrentTime(time.Now),
		builtin.ConvertTime(),
		builtin.Calculator(),
		builtin.WebFetch(),
		builtin.Shell(builtin.ShellConfig{WorkspaceRoot: workspace}),
		builtin.RetrieveOutput(outputs),
		builtin.Remember(remember),
	}
	// Search is optional infra: only registered when a backend is
	// configured, so an environment without SearXNG still runs clean.
	if searxngURL != "" {
		set = append(set, builtin.WebSearch(searxngURL))
	}
	if len(packs) > 0 {
		set = append(set, skills.LoadSkillTool(packs))
	}
	constrained, defs, err := compileToolset(set, nil, log)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	perms := tools.NewPermissions(db, workspace)
	broker := loop.NewPermBroker()
	agent := loop.NewAgent(gwc, constrained, perms, outputs, tools.NewAudit(db), store, broker, defs, log)
	// Shell dumps grow fast; offload them sooner than the default so a
	// long command output never bloats the context (D-019).
	agent.SetOffloadThreshold("shell", 4<<10)
	return agent, broker, outputs, set, nil
}

// compileToolset registers builtin + connector tools into a fresh
// constrained registry. A connector tool whose name collides with an
// existing tool is skipped with a log — a remote server must never
// shadow a builtin.
func compileToolset(builtins, connectorTools []*tools.Tool, log *slog.Logger) (*tools.Constrained, []provider.ToolDef, error) {
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
	constrained, err := tools.NewConstrained(reg)
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
func swapAgentTools(agent *loop.Agent, builtins []*tools.Tool, conns *connectors.Manager, log *slog.Logger) {
	constrained, defs, err := compileToolset(builtins, conns.Tools(), log)
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
