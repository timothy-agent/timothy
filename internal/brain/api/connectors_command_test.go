package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/sandboxclient"
)

// sandboxdServices stubs sandboxd's GET /v1/services with status.
func sandboxdServices(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"services":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestLocalServersOK covers the save-time refusal (D-162): a command
// connector is refused with 400 where local servers cannot run, and
// nothing else is affected.
func TestLocalServersOK(t *testing.T) {
	t.Parallel()
	command := connectors.Connector{Kind: "mcp", Config: json.RawMessage(`{"command":"npx"}`)}
	for _, tc := range []struct {
		name    string
		c       connectors.Connector
		sandbox string // "" means no runner wired
		want    bool
	}{
		{"endpoint connector, no runner", connectors.Connector{Kind: "mcp", Config: json.RawMessage(`{"endpoint":"https://x.example/mcp"}`)}, "", true},
		{"other kind", connectors.Connector{Kind: "github", Config: json.RawMessage(`{"command":"npx"}`)}, "", true},
		{"command, no runner", command, "", false},
		{"command, kubernetes", command, sandboxdServices(t, http.StatusNotImplemented), false},
		{"command, docker", command, sandboxdServices(t, http.StatusOK), true},
		{"command, sandboxd unreachable", command, "http://127.0.0.1:1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr := connectors.NewManager(connectors.NewStore(nil, discard()), nil, discard())
			if tc.sandbox != "" {
				mgr.SetServices(connectors.NewMCPServices(sandboxclient.New(tc.sandbox), []byte("k"), discard()))
			}
			h := &connectorAPI{mgr: mgr, log: discard()}
			rec := httptest.NewRecorder()
			got := h.localServersOK(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/connectors", nil), tc.c)
			if got != tc.want {
				t.Fatalf("localServersOK = %v, want %v", got, tc.want)
			}
			if !got && (rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Docker sandbox backend")) {
				t.Fatalf("refusal = %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestCreateRefusesCommandWithoutRunner(t *testing.T) {
	t.Parallel()
	h := &connectorAPI{mgr: connectors.NewManager(connectors.NewStore(nil, discard()), nil, discard()), log: discard()}
	rec := httptest.NewRecorder()
	body := `{"name":"local","kind":"mcp","enabled":true,"config":{"command":"npx","args":["-y","srv"]}}`
	h.create(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/connectors", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
}

func TestConnectorProbeCommandRequests(t *testing.T) {
	t.Parallel()
	h := &connectorProbeAPI{
		mgr:     connectors.NewManager(connectors.NewStore(nil, discard()), nil, discard()),
		toolset: stubToolset{},
		log:     discard(),
	}
	rec := httptest.NewRecorder()
	h.probe(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/connectors/probe",
		strings.NewReader(`{"endpoint":"https://x.example/mcp","command":"npx"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("endpoint and command = %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.probe(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/connectors/probe",
		strings.NewReader(`{"name":"local","command":"npx","env":{"KEY":"typed-secret"}}`)))
	var got probeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("command probe = %d %s", rec.Code, rec.Body)
	}
	if got.Status != connectors.ProbeError || !strings.Contains(got.Message, "Docker sandbox backend") {
		t.Fatalf("command probe without a runner = %+v", got.ProbeResult)
	}
}
