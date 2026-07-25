// Package httpserver provides the HTTP server shared by every service:
// timeouts, graceful shutdown, trace-id injection, instrumented route
// registration, and the /health and /metrics endpoints.
package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/logging"
	"github.com/SumonMSelim/timothy/internal/platform/metrics"
)

const shutdownTimeout = 10 * time.Second

// Check is one dependency's health.
type Check struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Health is the /health response body. Status is "ok" or "degraded";
// the endpoint always answers 200 — it reports liveness, and a
// degraded service is alive by design.
type Health struct {
	Status string           `json:"status"`
	Checks map[string]Check `json:"checks,omitempty"`
}

// HealthFunc supplies the current health snapshot.
type HealthFunc func() Health

// Server wraps http.Server with the platform conventions.
type Server struct {
	mux *http.ServeMux
	srv *http.Server
	m   *metrics.Metrics
	log *slog.Logger
}

// New builds a server listening on port with /health and /metrics
// mounted. Register service routes with Handle before Run.
func New(port int, log *slog.Logger, m *metrics.Metrics, health HealthFunc) *Server {
	s := &Server{mux: http.NewServeMux(), m: m, log: log}

	s.Handle("GET /health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(health()); err != nil {
			log.ErrorContext(r.Context(), "encode health", "error", err)
		}
	}))
	s.Handle("GET /metrics", m.Handler())

	s.srv = &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           withTrace(s.mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		// WriteTimeout deliberately stays 0: SSE endpoints hold their
		// response open for the life of a stream. Per-request deadlines
		// come from handler contexts instead.
		IdleTimeout: 120 * time.Second,
	}
	return s
}

// Handle registers a handler under a method-qualified ServeMux pattern
// ("GET /health"), instrumented with the pattern as the route label and
// logged on completion.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, s.m.Instrument(pattern, s.logAccess(pattern, h)))
}

// noAccessLog are polled every few seconds by compose healthchecks and
// the Prometheus scraper — logging them would just recreate the
// wall-of-noise this is meant to fix.
var noAccessLog = map[string]bool{
	"GET /health":  true,
	"GET /metrics": true,
}

// logAccess emits one structured line per request: method, route,
// status, and duration, with the trace id withTrace already put on the
// context. This is the only place any service logs a request — no
// handler needs to log its own access line.
func (s *Server) logAccess(pattern string, next http.Handler) http.Handler {
	if noAccessLog[pattern] {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		s.log.InfoContext(r.Context(), "request",
			"method", r.Method,
			"route", pattern,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// statusWriter records the response status and keeps http.Flusher
// visible for streaming handlers.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withTrace gives every request a trace id (accepting an inbound
// X-Trace-Id) and carries it on the context for logging.
func withTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Trace-Id")
		if id == "" {
			id = newTraceID()
		}
		w.Header().Set("X-Trace-Id", id)
		next.ServeHTTP(w, r.WithContext(logging.WithTraceID(r.Context(), id)))
	})
}

func newTraceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// Run serves until ctx is canceled, then shuts down gracefully within
// shutdownTimeout. It returns nil on clean shutdown. In-flight request
// contexts descend from ctx, so long-lived streams learn about
// shutdown immediately and can persist state inside the grace window.
func (s *Server) Run(ctx context.Context) error {
	s.srv.BaseContext = func(net.Listener) context.Context { return ctx }
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	s.log.Info("shutting down")
	return s.srv.Shutdown(sctx)
}
