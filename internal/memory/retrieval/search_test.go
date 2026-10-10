package retrieval

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func testMetrics() Metrics {
	return Metrics{
		LegDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "leg_seconds"}, []string{"leg"}),
		LegErrors:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "leg_errors"}, []string{"leg"}),
	}
}

func TestObserveRecordsDurationAndErrors(t *testing.T) {
	t.Parallel()
	m := testMetrics()
	s := &Searcher{metrics: m}
	s.observe("vector", time.Now(), nil)
	s.observe("text", time.Now(), errors.New("boom"))
	s.observe("text", time.Now(), nil)

	if n := testutil.CollectAndCount(m.LegDuration); n != 2 {
		t.Fatalf("duration series = %d, want 2 (vector, text)", n)
	}
	if got := testutil.ToFloat64(m.LegErrors.WithLabelValues("text")); got != 1 {
		t.Fatalf("text errors = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.LegErrors.WithLabelValues("vector")); got != 0 {
		t.Fatalf("vector errors = %v, want 0", got)
	}
}

func TestObserveNilMetrics(t *testing.T) {
	t.Parallel()
	(&Searcher{}).observe("entity", time.Now(), errors.New("boom")) // must not panic
}
