// Command gateway is Timothy's LLM provider gateway.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/admin"
	"github.com/SumonMSelim/timothy/internal/gateway/api"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/platform/service"
	"github.com/SumonMSelim/timothy/migrations"
)

const (
	serviceName = "gateway"
	defaultPort = 8081
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

	store := router.NewStore(app.DB, os.Getenv, app.Log)
	go store.Run(ctx)
	led := ledger.New(app.DB, app.Log)
	agg := ledger.NewAggregator(app.DB)
	budgets := ledger.NewBudgetStore(app.DB)
	api.Register(app.Server, store, led, app.Log)
	api.RegisterUsage(app.Server, agg, budgets)
	api.RegisterAdmin(app.Server, admin.New(app.DB, store, led, budgets, app.Log))

	spendGauge := app.Metrics.NewGaugeVec("spend_usd",
		"USD spent in the current UTC calendar window.", "window")
	budgetGauge := app.Metrics.NewGaugeVec("spend_budget_usd",
		"Configured USD budget per window; 0 when unset.", "window")
	go ledger.RunSpendGauges(ctx, time.Minute, agg, budgets, spendGauge, budgetGauge, app.Log)

	if err := app.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		app.Log.Error("server exited", "error", err)
		os.Exit(1)
	}
}
