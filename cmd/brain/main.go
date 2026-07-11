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

	agent, broker, outputs, buildErr := buildAgent(gwc, store, app.DB, workspace, packs, mc.Add, app.Log)
	if buildErr != nil {
		fmt.Fprintln(os.Stderr, buildErr)
		os.Exit(1)
	}
	go runOutputGC(ctx, outputs, app.Log)

	svc := chat.New(turnRouter{agent: agent, gw: gwc, flags: flags}, store, distill,
		gatedCompactor{inner: compactor, flags: flags}, budget, skills.Index(packs), app.Log)
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
		memoryProxy(memorydURL, app.Log), adminProxy(gatewayURL, app.Log), flags, token, app.Log)

	if err := app.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		app.Log.Error("server exited", "error", err)
		os.Exit(1)
	}
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
// rails (D-009, D-010).
func buildAgent(gwc *gwclient.Client, store *session.Store, db *pgpool.Pool, workspace string, packs []skills.Skill, remember builtin.RememberFunc, log *slog.Logger) (*loop.Agent, *loop.PermBroker, *tools.Outputs, error) {
	outputs := tools.NewOutputs(db)
	reg := tools.NewRegistry()
	set := []*tools.Tool{
		builtin.CurrentTime(time.Now),
		builtin.ConvertTime(),
		builtin.Calculator(),
		builtin.WebFetch(),
		builtin.Shell(builtin.ShellConfig{WorkspaceRoot: workspace}),
		builtin.RetrieveOutput(outputs),
		builtin.Remember(remember),
	}
	if len(packs) > 0 {
		set = append(set, skills.LoadSkillTool(packs))
	}
	for _, t := range set {
		if err := reg.Register(t); err != nil {
			return nil, nil, nil, fmt.Errorf("register tools: %w", err)
		}
	}
	constrained, err := tools.NewConstrained(reg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("compile tool schemas: %w", err)
	}
	constrained.SetClamp("shell", builtin.ShellTimeoutClamp())

	defs := make([]provider.ToolDef, 0, len(reg.List()))
	for _, t := range reg.List() {
		defs = append(defs, provider.ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}

	perms := tools.NewPermissions(db, workspace)
	broker := loop.NewPermBroker()
	agent := loop.NewAgent(gwc, constrained, perms, outputs, tools.NewAudit(db), store, broker, defs, log)
	// Shell dumps grow fast; offload them sooner than the default so a
	// long command output never bloats the context (D-019).
	agent.SetOffloadThreshold("shell", 4<<10)
	return agent, broker, outputs, nil
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
