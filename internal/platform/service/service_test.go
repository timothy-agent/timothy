package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"testing"
	"testing/fstest"
	"time"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("PORT", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("LOG_LEVEL", "error")

	app, err := New(t.Context(), "testsvc", 8099, fstest.MapFS{})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return app
}

func TestHealthDegradedWithoutDatabase(t *testing.T) {
	app := newTestApp(t)

	h := app.health()
	if h.Status != "degraded" {
		t.Fatalf("status = %q, want degraded (no database)", h.Status)
	}
	if h.Checks["postgres"].Status != "degraded" {
		t.Fatalf("postgres check = %+v, want degraded", h.Checks["postgres"])
	}
	if h.Checks["migrations"].Status != "degraded" || h.Checks["migrations"].Detail != "pending" {
		t.Fatalf("migrations check = %+v, want pending", h.Checks["migrations"])
	}
}

func TestMigrationsStateTransitions(t *testing.T) {
	app := newTestApp(t)

	app.setMigrations(errors.New("boom"))
	if c := app.migrationsCheck(); c.Status != "degraded" || c.Detail != "boom" {
		t.Fatalf("after failure: %+v", c)
	}

	app.setMigrations(nil)
	if c := app.migrationsCheck(); c.Status != "ok" {
		t.Fatalf("after success: %+v", c)
	}
	if h := app.health(); h.Status != "degraded" {
		t.Fatalf("overall = %q, want degraded (postgres still down)", h.Status)
	}
}

func TestProbeHealth(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	healthy := true
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if !healthy {
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	port := ln.Addr().(*net.TCPAddr).Port
	t.Setenv("PORT", strconv.Itoa(port))

	if code := ProbeHealth(1); code != 0 {
		t.Fatalf("ProbeHealth(healthy) = %d, want 0", code)
	}

	healthy = false
	if code := ProbeHealth(1); code != 1 {
		t.Fatalf("ProbeHealth(500) = %d, want 1", code)
	}

	t.Setenv("PORT", "not-a-port")
	if code := ProbeHealth(1); code != 1 {
		t.Fatalf("ProbeHealth(bad PORT) = %d, want 1", code)
	}
}
