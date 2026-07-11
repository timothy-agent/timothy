// Command brain is Timothy's public API service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/api"
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/session"
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
	compactor := session.NewCompactor(store, gwc, gwc, budget, app.Log,
		app.Metrics.NewCounter("session_compactions_total", "Sessions compacted to stay under the context budget."))
	distill := func(ctx context.Context, sessionID, turnText string) *session.TurnMemory {
		return loop.DistillTurn(ctx, gwc, sessionID, turnText)
	}

	workspace := os.Getenv("WORKSPACE_ROOT")
	if workspace == "" {
		workspace = "/workspace"
	}
	agent, broker, outputs, err := buildAgent(gwc, store, app.DB, workspace, app.Log)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go runOutputGC(ctx, outputs, app.Log)

	svc := chat.New(turnRouter{agent: agent, gw: gwc}, store, distill, compactor, budget, app.Log)
	api.Register(app.Server, svc, store, broker, token, app.Log)

	if err := app.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		app.Log.Error("server exited", "error", err)
		os.Exit(1)
	}
}

// turnRouter sends chat turns through the agent loop and everything
// else (titles, distills, compaction summaries) straight to the
// gateway.
type turnRouter struct {
	agent *loop.Agent
	gw    chat.Gateway
}

func (r turnRouter) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	if req.Purpose != "chat" {
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
func buildAgent(gwc *gwclient.Client, store *session.Store, db *pgpool.Pool, workspace string, log *slog.Logger) (*loop.Agent, *loop.PermBroker, *tools.Outputs, error) {
	outputs := tools.NewOutputs(db)
	reg := tools.NewRegistry()
	for _, t := range []*tools.Tool{
		builtin.CurrentTime(time.Now),
		builtin.ConvertTime(),
		builtin.Calculator(),
		builtin.WebFetch(),
		builtin.Shell(builtin.ShellConfig{WorkspaceRoot: workspace}),
		builtin.RetrieveOutput(outputs),
	} {
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
