package missions

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/platform/netguard"
)

// TestNotifierFanOutDialsThroughNetguard pins issue #431: a
// NOTIFY_WEBHOOK_URL on a loopback (compose-internal) address is
// refused, logged with the blocked-address reason, unless the operator
// allowlisted that host. fanOut is best-effort, so the log is the only
// place the refusal surfaces.
func TestNotifierFanOutDialsThroughNetguard(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tests := []struct {
		name    string
		allowed []string
		wantLog string
	}{
		{name: "loopback refused by default", wantLog: "blocked address"},
		{name: "allowlist naming another host still refuses", allowed: []string{"ntfy.example"}, wantLog: "blocked address"},
		{name: "allowlisted host permitted", allowed: []string{"127.0.0.1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits = 0
			g := netguard.Guard{}
			if tc.allowed != nil {
				g.Allowed = func(context.Context) []string { return tc.allowed }
			}
			var logBuf bytes.Buffer
			n := NewNotifier(nil, srv.URL, g.Transport(), slog.New(slog.NewTextHandler(&logBuf, nil)))
			n.fanOut(t.Context(), "m1", "done", "Mission - x is done")
			if tc.wantLog != "" {
				if !strings.Contains(logBuf.String(), tc.wantLog) {
					t.Fatalf("log = %q, want containing %q", logBuf.String(), tc.wantLog)
				}
				if hits != 0 {
					t.Fatalf("receiver hit %d times, want 0", hits)
				}
				return
			}
			if hits != 1 {
				t.Fatalf("receiver hit %d times, want 1 (log: %q)", hits, logBuf.String())
			}
		})
	}
}
