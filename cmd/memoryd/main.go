// Command memoryd is Timothy's long-term memory service.
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

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/memory/api"
	"github.com/SumonMSelim/timothy/internal/memory/extract"
	"github.com/SumonMSelim/timothy/internal/memory/retrieval"
	"github.com/SumonMSelim/timothy/internal/memory/store"
	"github.com/SumonMSelim/timothy/internal/platform/service"
	"github.com/SumonMSelim/timothy/migrations"
)

const (
	serviceName = "memoryd"
	defaultPort = 8082
	// consolidateEvery paces the lifecycle job (D-011): merge
	// near-dups, archive stale episodic, decay unconfirmed semantic.
	consolidateEvery = 24 * time.Hour
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

	gatewayURL := os.Getenv("GATEWAY_URL")
	if gatewayURL == "" {
		gatewayURL = "http://gateway:8081"
	}
	gwc := gwclient.New(gatewayURL)
	st := store.New(app.DB, app.Log)
	extractor := extract.New(gwc, st, app.Log)
	searcher := retrieval.NewSearcher(app.DB, app.Log)

	consolidator := extract.NewConsolidator(gwc, st, app.Log, extract.Metrics{
		Merges: app.Metrics.NewCounter("memory_merges_total", "Near-duplicate memory groups merged."),
		Rejects: app.Metrics.NewCounterVec("memory_merge_rejects_total",
			"Near-duplicate merges rejected by reason.", "reason"),
		Archived: app.Metrics.NewCounter("memory_archived_total", "Stale episodic memories archived."),
		Decayed:  app.Metrics.NewCounter("memory_decayed_total", "Stale semantic facts decayed and queued for reconfirmation."),
	})
	kbStore := store.NewKBStore(app.DB)
	api.Register(app.Server, extractor, searcher, gwc, st, consolidator, kbStore, kbStore, app.Log)
	go consolidator.RunLoop(ctx, consolidateEvery)

	if err := app.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		app.Log.Error("server exited", "error", err)
		os.Exit(1)
	}
}
