// Package service bootstraps a Timothy service: configuration,
// logging, metrics, database pool, startup migrations, and the HTTP
// server — the wiring every main shares.
package service

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/config"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
	"github.com/SumonMSelim/timothy/internal/platform/logging"
	"github.com/SumonMSelim/timothy/internal/platform/metrics"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

const migrateRetryDelay = 5 * time.Second

// App is a bootstrapped service. Register routes on Server, then Run.
type App struct {
	Config  config.Service
	Log     *slog.Logger
	Metrics *metrics.Metrics
	DB      *pgpool.Pool
	Server  *httpserver.Server

	mu             sync.RWMutex
	migrationsDone bool
	migrationsErr  error
	extraChecks    map[string]func() httpserver.Check
}

// AddCheck registers a service-specific health check merged into
// /health beside the platform's postgres and migrations checks.
// Register before Run.
func (a *App) AddCheck(name string, fn func() httpserver.Check) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.extraChecks == nil {
		a.extraChecks = map[string]func() httpserver.Check{}
	}
	a.extraChecks[name] = fn
}

// New loads configuration and wires logging, metrics, the database
// pool, and the HTTP server. Migrations apply in the background once
// the database is reachable; their state (like the database's) shows
// in /health rather than blocking startup. Cancel ctx to stop
// background work.
func New(ctx context.Context, name string, defaultPort int, migrations fs.FS) (*App, error) {
	cfg, err := config.Load(name, defaultPort)
	if err != nil {
		return nil, err
	}

	a := &App{
		Config:  cfg,
		Log:     logging.New(cfg.Name, cfg.LogLevel),
		Metrics: metrics.New(),
	}
	a.DB = pgpool.New(ctx, cfg.DatabaseURL, a.Log)
	a.Server = httpserver.New(cfg.Port, a.Log, a.Metrics, a.health)

	go a.applyMigrations(ctx, migrations)
	return a, nil
}

// Run serves HTTP until ctx is canceled, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	a.Log.Info("starting", "port", a.Config.Port)
	return a.Server.Run(ctx)
}

func (a *App) health() httpserver.Health {
	dbStatus, dbDetail := a.DB.Status()
	checks := map[string]httpserver.Check{
		"postgres":   {Status: dbStatus, Detail: dbDetail},
		"migrations": a.migrationsCheck(),
	}
	a.mu.RLock()
	for name, fn := range a.extraChecks {
		checks[name] = fn()
	}
	a.mu.RUnlock()

	status := "ok"
	for _, c := range checks {
		if c.Status != "ok" {
			status = "degraded"
		}
	}
	return httpserver.Health{Status: status, Checks: checks}
}

func (a *App) migrationsCheck() httpserver.Check {
	a.mu.RLock()
	defer a.mu.RUnlock()
	switch {
	case a.migrationsDone:
		return httpserver.Check{Status: "ok"}
	case a.migrationsErr != nil:
		return httpserver.Check{Status: "degraded", Detail: a.migrationsErr.Error()}
	default:
		return httpserver.Check{Status: "degraded", Detail: "pending"}
	}
}

func (a *App) setMigrations(err error) {
	a.mu.Lock()
	a.migrationsDone, a.migrationsErr = err == nil, err
	a.mu.Unlock()
}

// applyMigrations waits for the database and applies migrations,
// retrying on failure. Failures surface through /health; startup never
// blocks on a reachable database.
func (a *App) applyMigrations(ctx context.Context, migrations fs.FS) {
	for {
		if err := a.DB.WaitHealthy(ctx); err != nil {
			return
		}
		db, err := a.DB.Get()
		if err != nil {
			continue
		}

		err = migrate.Run(ctx, db, migrations, a.Log)
		a.setMigrations(err)
		if err == nil {
			return
		}

		a.Log.Error("migrations failed", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(migrateRetryDelay):
		}
	}
}

// ProbeHealth performs the container health check without shell or
// curl in the (distroless) image: the binary probes its own /health.
// It returns a process exit code.
func ProbeHealth(defaultPort int) int {
	port := defaultPort
	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			fmt.Fprintf(os.Stderr, "invalid PORT %q\n", v)
			return 1
		}
		port = p
	}

	client := &http.Client{Timeout: 3 * time.Second}
	// Loopback self-probe with a range-validated integer port — not
	// reachable by external input.
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port)) // #nosec G704
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
