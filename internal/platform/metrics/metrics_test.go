package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestInstrumentCountsRequests(t *testing.T) {
	t.Parallel()
	m := New()
	h := m.Instrument("GET /health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	for range 3 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	}

	got := testutil.ToFloat64(m.reqTotal.WithLabelValues("GET", "GET /health", "418"))
	if got != 3 {
		t.Fatalf("requests_total = %v, want 3", got)
	}
}

func TestInstrumentDefaultsTo200(t *testing.T) {
	t.Parallel()
	m := New()
	h := m.Instrument("GET /ok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok")) // no explicit WriteHeader
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok", nil))

	got := testutil.ToFloat64(m.reqTotal.WithLabelValues("GET", "GET /ok", "200"))
	if got != 1 {
		t.Fatalf("requests_total = %v, want 1", got)
	}
}

func TestNewCounterVec(t *testing.T) {
	t.Parallel()
	m := New()
	c := m.NewCounterVec("widgets_total", "Widgets processed.", "kind")
	c.WithLabelValues("blue").Inc()
	c.WithLabelValues("blue").Inc()
	c.WithLabelValues("red").Inc()

	if got := testutil.ToFloat64(c.WithLabelValues("blue")); got != 2 {
		t.Fatalf("blue = %v, want 2", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues("red")); got != 1 {
		t.Fatalf("red = %v, want 1", got)
	}
}

func TestHandlerServesExposition(t *testing.T) {
	t.Parallel()
	m := New()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("empty exposition body")
	}
}
