// Command sandboxd runs mission sandboxes on brain's behalf: it is
// the only service that talks to the container runtime (the Docker
// daemon, or the Kubernetes API, D-153), exposing a narrow,
// mission-scoped HTTP API (missionID in — never container names,
// images, mounts, env) that brain's sandboxclient calls instead of
// holding runtime credentials itself. Unlike every other Timothy
// service this has no database — there is nothing here to persist.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
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

	// Fail closed: sandboxed execution is mandatory, so a backend that
	// cannot initialize (image not set, runtime unreachable, workspace
	// mount unresolvable) must stop this service loudly rather than come
	// up in a state where every exec fails opaquely.
	mgr, err := newBackend(ctx, log)
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

// newBackend builds the sandbox backend SANDBOXD_BACKEND names (D-153).
func newBackend(ctx context.Context, log *slog.Logger) (sandboxd.Backend, error) {
	name, err := sandboxd.ResolveBackend(os.Getenv("SANDBOXD_BACKEND"), os.Getenv("KUBERNETES_SERVICE_HOST"))
	if err != nil {
		return nil, err
	}
	log.Info("sandbox: backend selected", "backend", name)
	image := os.Getenv("MISSION_SANDBOX_IMAGE")
	switch name {
	case sandboxd.BackendDocker:
		return sandboxd.NewDocker(ctx, image, log)
	default:
		return nil, fmt.Errorf("sandbox: backend %q is not available in this build", name)
	}
}

// health assembles /health's checks from the backend: runtime
// reachability (keyed by the backend's name) and whether the
// configured sandbox image is usable.
func health(ctx context.Context, mgr sandboxd.Backend) httpserver.Health {
	checks := map[string]httpserver.Check{}
	status := "ok"
	if err := mgr.Ping(ctx); err != nil {
		checks[mgr.Name()] = httpserver.Check{Status: "degraded", Detail: mgr.Name() + " unreachable: " + err.Error()}
		status = "degraded"
	} else {
		checks[mgr.Name()] = httpserver.Check{Status: "ok"}
	}
	if err := mgr.CheckImage(ctx); err != nil {
		checks["image"] = httpserver.Check{Status: "degraded", Detail: "sandbox image not found: " + err.Error()}
		status = "degraded"
	} else {
		checks["image"] = httpserver.Check{Status: "ok"}
	}
	return httpserver.Health{Status: status, Version: service.Version, Checks: checks}
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
