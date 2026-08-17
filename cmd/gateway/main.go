// Command gateway is Timothy's LLM provider gateway.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/admin"
	"github.com/SumonMSelim/timothy/internal/gateway/api"
	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/platform/service"
	"github.com/SumonMSelim/timothy/internal/secretstore"
	"github.com/SumonMSelim/timothy/migrations"
)

const (
	catalogSyncInterval  = 24 * time.Hour
	catalogRetryInterval = 15 * time.Minute
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

	masterKey, err := secretstore.DecodeMasterKey(os.Getenv(secretstore.MasterKeyEnv))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	secrets, err := secretstore.New(app.DB, masterKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	store := router.NewStore(app.DB, credentialLookup(secrets, app.Log), app.Log)
	go store.Run(ctx)
	led := ledger.New(app.DB, app.Log)
	agg := ledger.NewAggregator(app.DB)
	budgets := ledger.NewBudgetStore(app.DB)
	cat := catalog.New(app.Log)
	go runCatalogSweep(ctx, cat, app.Log)
	api.Register(app.Server, store, led, app.Log, app.Metrics)
	api.RegisterUsage(app.Server, agg, budgets)
	api.RegisterAdmin(app.Server, admin.New(app.DB, store, led, budgets, secrets, cat, app.Log))

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

// runCatalogSweep syncs the model catalog on its own goroutine. The
// cache starts empty in memory (no DB-backed persistence), so it always
// syncs once at boot; from then on it retries every 15 minutes while
// the last sync failed (or never succeeded), else settles into a
// jittered 24h interval — the jitter keeps a fleet of restarts from all
// hitting the upstream URL at once.
func runCatalogSweep(ctx context.Context, cat *catalog.Store, log *slog.Logger) {
	st := syncCatalogOnce(ctx, cat, log)
	for {
		interval := catalogSyncInterval
		if st.Error != "" {
			interval = catalogRetryInterval
		} else {
			interval += time.Duration(rand.Int64N(int64(10 * time.Minute))) // #nosec G404 -- jitter, not a secret
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			st = syncCatalogOnce(ctx, cat, log)
		}
	}
}

func syncCatalogOnce(ctx context.Context, cat *catalog.Store, log *slog.Logger) catalog.SyncStatus {
	st, err := cat.Sync(ctx)
	if err != nil {
		log.Warn("model catalog sync failed", "error", err)
		return st
	}
	log.Info("model catalog synced", "entry_count", st.EntryCount)
	return st
}

// credentialLookup resolves a credential_ref via the encrypted secret
// store. A missing or failed resolve returns "" — the provider then
// builds with an empty key and shows unhealthy until its credential is
// set in Settings, rather than falling back to any env var.
func credentialLookup(secrets *secretstore.Store, log *slog.Logger) func(string) string {
	return func(ref string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		val, err := secrets.Resolve(ctx, ref)
		if err != nil {
			if !errors.Is(err, secretstore.ErrNotFound) {
				log.Warn("secret resolve failed", "ref", ref, "error", err)
			}
			return ""
		}
		return val
	}
}
