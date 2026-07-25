package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/logging"
	"github.com/SumonMSelim/timothy/internal/platform/metrics"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(health HealthFunc) *Server {
	return New(0, discard(), metrics.New(), health)
}

// newLoggingTestServer is newTestServer with its log output captured
// instead of discarded, for tests that assert on emitted lines.
func newLoggingTestServer(health HealthFunc) (*Server, *bytes.Buffer) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	return New(0, log, metrics.New(), health), &buf
}

func TestHealthEndpoint(t *testing.T) {
	t.Parallel()
	s := newTestServer(func() Health {
		return Health{Status: "degraded", Checks: map[string]Check{
			"postgres": {Status: "degraded", Detail: "connecting"},
		}}
	})

	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (liveness endpoint)", rec.Code)
	}
	var h Health
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if h.Status != "degraded" || h.Checks["postgres"].Detail != "connecting" {
		t.Fatalf("health = %+v, want degraded postgres detail", h)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	t.Parallel()
	s := newTestServer(func() Health { return Health{Status: "ok"} })

	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", rec.Code)
	}
}

func TestTraceIDInjected(t *testing.T) {
	t.Parallel()
	s := newTestServer(func() Health { return Health{Status: "ok"} })

	var got string
	s.Handle("GET /echo", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = logging.TraceID(r.Context())
	}))

	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/echo", nil))

	if got == "" {
		t.Fatal("handler context missing trace id")
	}
	if hdr := rec.Header().Get("X-Trace-Id"); hdr != got {
		t.Fatalf("X-Trace-Id header = %q, want %q", hdr, got)
	}
}

func TestInboundTraceIDPreserved(t *testing.T) {
	t.Parallel()
	s := newTestServer(func() Health { return Health{Status: "ok"} })

	var got string
	s.Handle("GET /echo", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = logging.TraceID(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/echo", nil)
	req.Header.Set("X-Trace-Id", "upstream-1")
	s.srv.Handler.ServeHTTP(httptest.NewRecorder(), req)

	if got != "upstream-1" {
		t.Fatalf("trace id = %q, want upstream-1", got)
	}
}

func TestAccessLogged(t *testing.T) {
	t.Parallel()
	s, buf := newLoggingTestServer(func() Health { return Health{Status: "ok"} })
	s.Handle("GET /echo", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	s.srv.Handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/echo", nil))

	line := buf.String()
	for _, want := range []string{`"msg":"request"`, `"method":"GET"`, `"route":"GET /echo"`, `"status":418`} {
		if !strings.Contains(line, want) {
			t.Fatalf("log = %s, want containing %q", line, want)
		}
	}
}

// TestHealthAndMetricsNotAccessLogged: both are polled every few
// seconds by compose healthchecks and the Prometheus scraper — logging
// them would recreate the exact noise this access log is meant to fix.
func TestHealthAndMetricsNotAccessLogged(t *testing.T) {
	t.Parallel()
	s, buf := newLoggingTestServer(func() Health { return Health{Status: "ok"} })

	s.srv.Handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	s.srv.Handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if buf.Len() != 0 {
		t.Fatalf("log = %s, want no access lines for /health or /metrics", buf.String())
	}
}

func TestRunShutsDownOnContextCancel(t *testing.T) {
	t.Parallel()
	s := newTestServer(func() Health { return Health{Status: "ok"} })
	s.srv.Addr = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	time.Sleep(100 * time.Millisecond) // let the listener start
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v, want nil on graceful shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after cancel")
	}
}

// TestRunCancelPropagatesToRequests pins the kill-test wiring:
// http.Server.Shutdown alone never cancels in-flight request contexts,
// so Run derives them from its own ctx — a SIGTERM must reach a
// long-lived streaming handler immediately.
func TestRunCancelPropagatesToRequests(t *testing.T) {
	t.Parallel()
	s := newTestServer(func() Health { return Health{Status: "ok"} })
	s.srv.Addr = "127.0.0.1:0"

	requestCanceled := make(chan struct{})
	inHandler := make(chan struct{})
	s.Handle("GET /hang", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(inHandler)
		select {
		case <-r.Context().Done():
			close(requestCanceled)
		case <-time.After(5 * time.Second):
		}
	}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.srv.Addr = ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	time.Sleep(100 * time.Millisecond) // let the listener start

	go func() {
		resp, err := http.Get("http://" + s.srv.Addr + "/hang")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-inHandler:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the handler")
	}
	cancel() // the SIGTERM path

	select {
	case <-requestCanceled:
		// in-flight request context observed the shutdown
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request context never canceled on shutdown")
	}
	<-done
}
