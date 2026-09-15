package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/platform/netguard"
)

// TestMCPBuilderDialsThroughNetguard pins issue #431: an MCP endpoint
// on a loopback (compose-internal) address fails the build with a
// blocked-address error unless the operator allowlisted that host.
func TestMCPBuilderDialsThroughNetguard(t *testing.T) {
	f := &fakeMCP{}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	resolve := func(context.Context, string) (string, error) { return "", nil }

	tests := []struct {
		name    string
		allowed []string
		wantErr string
	}{
		{name: "loopback refused by default", wantErr: "blocked address"},
		{name: "allowlist naming another host still refuses", allowed: []string{"mcp.example"}, wantErr: "blocked address"},
		{name: "allowlisted host permitted", allowed: []string{"127.0.0.1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := netguard.Guard{}
			if tc.allowed != nil {
				g.Allowed = func(context.Context) []string { return tc.allowed }
			}
			client := &http.Client{Transport: g.Transport()}
			src, err := MCPBuilder(client, MCPDeferral{})(t.Context(), Connector{
				Name: "x", Kind: "mcp", Config: json.RawMessage(`{"endpoint":"` + srv.URL + `"}`),
			}, resolve)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("build err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if len(src.Tools()) == 0 {
				t.Fatal("allowlisted build returned no tools")
			}
		})
	}
}
