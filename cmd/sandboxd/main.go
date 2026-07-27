// Command sandboxd holds the Docker socket on brain's behalf: it is
// the only service that talks to the Docker daemon, exposing a narrow,
// mission-scoped HTTP API (missionID in — never container names,
// images, mounts, env) that brain's sandboxclient calls instead of
// mounting docker.sock itself. Unlike every other Timothy service this
// has no database — there is nothing here to persist.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/SumonMSelim/timothy/internal/platform/config"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
	"github.com/SumonMSelim/timothy/internal/platform/logging"
	"github.com/SumonMSelim/timothy/internal/platform/metrics"
	"github.com/SumonMSelim/timothy/internal/platform/service"
	"github.com/SumonMSelim/timothy/internal/sandboxd"
)

const (
	serviceName = "sandboxd"
	defaultPort = 8083
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

	cfg, err := config.Load(serviceName, defaultPort)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log := logging.New(cfg.Name, cfg.LogLevel)
	m := metrics.New()

	// Fail closed: an operator who set MISSION_SANDBOX_IMAGE opted into
	// sandboxed execution, so a manager that cannot initialize (daemon
	// unreachable, workspace mount unresolvable) must stop this service
	// loudly rather than come up in a state where every exec fails
	// opaquely. A missing image is deliberately NOT fatal: the health
	// check below reports it, and building the image needs no restart.
	mgr, err := sandboxd.NewManager(ctx, os.Getenv("MISSION_SANDBOX_IMAGE"), log)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	srv := httpserver.New(cfg.Port, log, m, func() httpserver.Health {
		return health(ctx, mgr)
	})
	sandboxd.Register(srv, mgr, execConfig(), log)

	log.Info("starting", "port", cfg.Port)
	if err := srv.Run(ctx); err != nil {
		log.Error("server exited", "error", err)
		os.Exit(1)
	}
}

// health assembles /health's checks from the Manager: a nil manager
// (MISSION_SANDBOX_IMAGE unset) reports one degraded check rather than
// the usual docker+image pair, since there is nothing to Ping or
// CheckImage yet.
func health(ctx context.Context, mgr *sandboxd.Manager) httpserver.Health {
	if mgr == nil {
		return httpserver.Health{
			Status: "degraded",
			Checks: map[string]httpserver.Check{
				"sandbox": {Status: "degraded", Detail: "MISSION_SANDBOX_IMAGE not set"},
			},
		}
	}
	checks := map[string]httpserver.Check{}
	status := "ok"
	if err := mgr.Ping(ctx); err != nil {
		checks["docker"] = httpserver.Check{Status: "degraded", Detail: "docker daemon unreachable: " + err.Error()}
		status = "degraded"
	} else {
		checks["docker"] = httpserver.Check{Status: "ok"}
	}
	if err := mgr.CheckImage(ctx); err != nil {
		checks["image"] = httpserver.Check{Status: "degraded", Detail: "sandbox image not found: " + err.Error()}
		status = "degraded"
	} else {
		checks["image"] = httpserver.Check{Status: "ok"}
	}
	return httpserver.Health{Status: status, Checks: checks}
}

// execConfig reads the concurrency caps from the environment — bare
// os.Getenv + strconv, matching the rest of this repo's env parsing in
// main, not a config framework. Invalid or unset values fall back to
// sandboxd's package defaults (Register zero-checks them).
func execConfig() sandboxd.Config {
	return sandboxd.Config{
		MaxExecs:      envInt("SANDBOXD_MAX_EXECS"),
		MaxContainers: envInt("SANDBOXD_MAX_CONTAINERS"),
	}
}

func envInt(name string) int {
	v := os.Getenv(name)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}
