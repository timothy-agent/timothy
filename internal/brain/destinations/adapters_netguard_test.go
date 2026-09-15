package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/platform/netguard"
)

// TestWebhookAdapterDialsThroughNetguard pins issue #431: a webhook
// destination URL on a loopback (compose-internal) address is refused
// unless the operator allowlisted that host, and the refusal is a
// retry-safe error, never errMaybeDelivered.
func TestWebhookAdapterDialsThroughNetguard(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tests := []struct {
		name    string
		allowed []string
		url     string
		wantErr string
	}{
		{name: "loopback refused by default", url: srv.URL, wantErr: "blocked address"},
		{name: "allowlist naming another host still refuses", allowed: []string{"hooks.example"}, url: srv.URL, wantErr: "blocked address"},
		{name: "allowlisted host permitted", allowed: []string{"127.0.0.1"}, url: srv.URL},
		{name: "non-http scheme refused", url: "ftp://" + strings.TrimPrefix(srv.URL, "http://"), wantErr: "unsupported protocol scheme"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits = 0
			g := netguard.Guard{}
			if tc.allowed != nil {
				g.Allowed = func(context.Context) []string { return tc.allowed }
			}
			a := &WebhookAdapter{HTTP: &http.Client{Transport: g.Transport()}}
			cfg, _ := json.Marshal(WebhookConfig{URL: tc.url, Format: "json"})
			err := a.Deliver(t.Context(), cfg, "", Payload{Body: "digest"})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Deliver err = %v, want containing %q", err, tc.wantErr)
				}
				if tc.wantErr == "blocked address" && errors.Is(err, errMaybeDelivered) {
					t.Fatalf("Deliver err = %v, want retry-safe (nothing left the machine)", err)
				}
				if hits != 0 {
					t.Fatalf("receiver hit %d times, want 0", hits)
				}
				return
			}
			if err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			if hits != 1 {
				t.Fatalf("receiver hit %d times, want 1", hits)
			}
		})
	}
}
