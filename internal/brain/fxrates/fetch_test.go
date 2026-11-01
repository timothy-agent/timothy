package fxrates

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestFetcherFetchFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		primaryStatus int
		fallbackOK    bool
		key           string
		wantErr       bool
		wantEUR       float64
		wantFallback  int32
	}{
		{name: "primary ok never contacts fallback", primaryStatus: http.StatusOK, fallbackOK: true, key: "k", wantEUR: 0.856},
		{name: "primary down without key stays keyless", primaryStatus: http.StatusServiceUnavailable, fallbackOK: true, key: "", wantErr: true},
		{name: "primary down with blank key stays keyless", primaryStatus: http.StatusServiceUnavailable, fallbackOK: true, key: "  ", wantErr: true},
		{name: "primary down with key uses fallback", primaryStatus: http.StatusServiceUnavailable, fallbackOK: true, key: "k", wantEUR: 0.8587, wantFallback: 1},
		{name: "both down returns error", primaryStatus: http.StatusServiceUnavailable, fallbackOK: false, key: "k", wantErr: true, wantFallback: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.primaryStatus)
				_, _ = w.Write([]byte(cannedResponse))
			}))
			defer primary.Close()

			var hits atomic.Int32
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if got := r.Header.Get("Authorization"); got != "Bearer k" {
					t.Errorf("Authorization = %q, want %q", got, "Bearer k")
				}
				if !tc.fallbackOK {
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				_, _ = w.Write([]byte(cannedFallback))
			}))
			defer fallback.Close()

			f := NewFetcher(nil, map[string]bool{"EUR": true, "BDT": true}, func(context.Context) string { return tc.key },
				slog.New(slog.NewTextHandler(io.Discard, nil)))
			f.primaryURL = primary.URL
			f.fallbackURL = fallback.URL

			rates, _, err := f.fetch(t.Context())
			if got := hits.Load(); got != tc.wantFallback {
				t.Errorf("fallback hits = %d, want %d", got, tc.wantFallback)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got rates %v", rates)
				}
				return
			}
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if rates["EUR"] != tc.wantEUR {
				t.Errorf("EUR = %v, want %v", rates["EUR"], tc.wantEUR)
			}
		})
	}
}
