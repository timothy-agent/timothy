package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/sandboxclient"
	"github.com/SumonMSelim/timothy/internal/mcpbridge"
)

// fakeSandboxd is sandboxd's service API in-process: each ensured
// service is a real mcpbridge in front of a fakeMCP over stdio, on its
// own listener.
type fakeSandboxd struct {
	mu          sync.Mutex
	unsupported bool
	// stoppedOnEnsure makes Ensure report a crashed service with this
	// last error.
	stoppedOnEnsure string
	services        map[string]*fakeService
	created         int
	removed         []string
	srv             *httptest.Server
}

type fakeService struct {
	spec     sandboxclient.ServiceSpec
	srv      *httptest.Server
	mu       sync.Mutex
	handler  http.Handler
	mcp      *fakeMCP
	state    string
	restarts int
	lastErr  string
	closers  []func()
}

func newFakeSandboxd(t *testing.T) *fakeSandboxd {
	t.Helper()
	f := &fakeSandboxd{services: map[string]*fakeService{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, s := range f.services {
			s.close()
		}
		f.srv.Close()
	})
	return f
}

// start runs a fresh bridge process: a new session id, a new server.
func (s *fakeService) start() {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	m := &fakeMCP{}
	go serveStdio(m, inR, outW)
	b := mcpbridge.New(s.spec.Env[bridgeTokenEnv], inW, outR, io.Discard)
	s.mu.Lock()
	s.handler, s.mcp = b, m
	s.closers = append(s.closers, func() { _ = inW.Close(); _ = outW.Close() })
	s.mu.Unlock()
}

func (s *fakeService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := s.handler
	s.mu.Unlock()
	h.ServeHTTP(w, r)
}

func (s *fakeService) close() {
	s.srv.Close()
	for _, c := range s.closers {
		c()
	}
}

func (s *fakeService) status(id string) sandboxclient.ServiceStatus {
	return sandboxclient.ServiceStatus{ID: id, Address: s.srv.URL, State: s.state, Restarts: s.restarts, LastError: s.lastErr}
}

// restart simulates Docker restarting a crashed service in place.
func (f *fakeSandboxd) restart(id string) *fakeMCP {
	f.mu.Lock()
	s := f.services[id]
	f.mu.Unlock()
	s.restarts++
	s.start()
	return s.mcp
}

// crash leaves service id stopped for good, its port closed.
func (f *fakeSandboxd) crash(id, lastErr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.services[id]
	s.srv.CloseClientConnections()
	_ = s.srv.Listener.Close()
	s.state, s.restarts, s.lastErr = "stopped", 3, lastErr
}

// seed registers a service sandboxd already runs, never connected to.
func (f *fakeSandboxd) seed(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &fakeService{state: "running"}
	s.srv = httptest.NewServer(http.NotFoundHandler())
	f.services[id] = s
}

func (f *fakeSandboxd) spec(id string) sandboxclient.ServiceSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.services[id].spec
}

func (f *fakeSandboxd) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unsupported {
		w.WriteHeader(http.StatusNotImplemented)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/services/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/services":
		list := []sandboxclient.ServiceStatus{}
		for sid, s := range f.services {
			list = append(list, s.status(sid))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"services": list})
	case r.Method == http.MethodPut:
		var spec sandboxclient.ServiceSpec
		_ = json.NewDecoder(r.Body).Decode(&spec)
		s, ok := f.services[id]
		if !ok || !reflect.DeepEqual(s.spec, spec) || s.state == "stopped" {
			if ok {
				s.close()
			}
			f.created++
			s = &fakeService{spec: spec, state: "running"}
			s.start()
			s.srv = httptest.NewServer(s)
			f.services[id] = s
		}
		if f.stoppedOnEnsure != "" {
			s.state, s.restarts, s.lastErr = "stopped", 3, f.stoppedOnEnsure
		}
		_ = json.NewEncoder(w).Encode(s.status(id))
	case r.Method == http.MethodDelete:
		if s, ok := f.services[id]; ok {
			s.close()
			delete(f.services, id)
		}
		f.removed = append(f.removed, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeSandboxd) removedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.removed)
	slices.Sort(out)
	return out
}

func newTestServices(f *fakeSandboxd, logw io.Writer) *MCPServices {
	s := NewMCPServices(sandboxclient.New(f.srv.URL), []byte("test-key"), slog.New(slog.NewTextHandler(logw, nil)))
	s.listenWait = 2 * time.Second
	return s
}

const cmdID = "5e7c1ce0-0000-4000-8000-000000000001"

func cmdConnector(id string, enabled bool, cfg string) Connector {
	return Connector{ID: id, Name: "toy-" + id[len(id)-1:], Kind: "mcp", Enabled: enabled, Config: json.RawMessage(cfg)}
}

const toyCmdConfig = `{"command":"npx","args":["-y","toy-mcp"],"env_refs":{"TOY_KEY":"TOY_KEY_REF"}}`

func toyResolve(_ context.Context, ref string) (string, error) {
	if ref == "TOY_KEY_REF" {
		return "toy-secret-value", nil
	}
	return "", fmt.Errorf("secret %s: not found", ref)
}

func TestCommandConnectorRunsThroughService(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxd(t)
	var logs bytes.Buffer
	svcs := newTestServices(f, &logs)
	row := cmdConnector(cmdID, true, toyCmdConfig)
	rowConfig := string(row.Config)
	build := mcpBuilder(nil, MCPDeferral{}, nil, svcs)

	src, err := build(t.Context(), row, toyResolve)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	list := src.Tools()
	if len(list) != 2 || list[0].Name != "create_issue" {
		t.Fatalf("tools = %+v", list)
	}
	if out, err := list[0].Execute(t.Context(), json.RawMessage(`{"title":"bug"}`)); err != nil || out != "issue #42 created" {
		t.Fatalf("call = %q, %v", out, err)
	}

	spec := f.spec(cmdID)
	if want := []string{"mcp-bridge", "--", "npx", "-y", "toy-mcp"}; !slices.Equal(spec.Command, want) {
		t.Fatalf("command = %v, want %v", spec.Command, want)
	}
	token := svcs.token(cmdID)
	if spec.Env["TOY_KEY"] != "toy-secret-value" || spec.Env[bridgeTokenEnv] != token || len(spec.Env) != 2 || spec.Internet {
		t.Fatalf("spec = %+v", spec)
	}

	if _, err := build(t.Context(), row, toyResolve); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if f.created != 1 {
		t.Fatalf("a rebuild with the same config replaced the service (%d created)", f.created)
	}
	if string(row.Config) != rowConfig || strings.Contains(rowConfig, token) {
		t.Fatal("the bridge token reached the connector config")
	}
	if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "toy-secret-value") {
		t.Fatalf("a secret reached the log: %s", logs.String())
	}
}

func TestCommandConnectorTokenPerConnector(t *testing.T) {
	t.Parallel()
	a := NewMCPServices(nil, []byte("k1"), nil)
	again := NewMCPServices(nil, []byte("k1"), nil)
	b := NewMCPServices(nil, []byte("k2"), nil)
	if a.token("x") != again.token("x") || a.token("x") == a.token("y") || a.token("x") == b.token("x") {
		t.Fatal("tokens must be stable per key and connector, and differ across either")
	}
}

// TestCommandConnectorRecoversRestartedServer covers a server restart
// between calls: the bridge's new session answers 404 and the client
// re-initializes and retries.
func TestCommandConnectorRecoversRestartedServer(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxd(t)
	svcs := newTestServices(f, io.Discard)
	src, err := mcpBuilder(nil, MCPDeferral{}, nil, svcs)(t.Context(), cmdConnector(cmdID, true, toyCmdConfig), toyResolve)
	if err != nil {
		t.Fatal(err)
	}
	fresh := f.restart(cmdID)
	if out, err := src.Tools()[0].Execute(t.Context(), json.RawMessage(`{"title":"x"}`)); err != nil || out != "issue #42 created" {
		t.Fatalf("call after restart = %q, %v", out, err)
	}
	if fresh.gotProto != mcpProtocolVersion || len(fresh.gotCalls) != 1 {
		t.Fatalf("restarted server: proto %q, calls %v", fresh.gotProto, fresh.gotCalls)
	}
}

func TestCommandConnectorExplainsStoppedServer(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxd(t)
	svcs := newTestServices(f, io.Discard)
	src, err := mcpBuilder(nil, MCPDeferral{}, nil, svcs)(t.Context(), cmdConnector(cmdID, true, toyCmdConfig), toyResolve)
	if err != nil {
		t.Fatal(err)
	}
	f.crash(cmdID, "exited with code 1")
	_, err = src.Tools()[0].Execute(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "local server stopped after 3 restarts: exited with code 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandConnectorBuildErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		cfg      string
		setup    func(*fakeSandboxd)
		noRunner bool
		wantIs   error
		want     string
		notWant  string
	}{
		{name: "kubernetes", cfg: toyCmdConfig, setup: func(f *fakeSandboxd) { f.unsupported = true }, wantIs: ErrLocalServersUnsupported},
		{name: "no runner", cfg: toyCmdConfig, noRunner: true, wantIs: ErrLocalServersUnsupported},
		{name: "crashes on start", cfg: toyCmdConfig, setup: func(f *fakeSandboxd) { f.stoppedOnEnsure = "exited with code 2" }, want: "local server stopped after 3 restarts: exited with code 2"},
		{name: "env ref missing", cfg: `{"command":"npx","env_refs":{"TOY_KEY":"MISSING_REF"}}`, want: "env TOY_KEY: secret MISSING_REF does not resolve"},
		{name: "oauth", cfg: `{"command":"npx","auth_mode":"oauth"}`, want: "cannot use oauth"},
		{name: "neither", cfg: `{}`, want: "config.endpoint or config.command is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeSandboxd(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			svcs := newTestServices(f, io.Discard)
			if tc.noRunner {
				svcs = nil
			}
			_, err := mcpBuilder(nil, MCPDeferral{}, nil, svcs)(t.Context(), cmdConnector(cmdID, true, tc.cfg), toyResolve)
			if err == nil {
				t.Fatal("build succeeded")
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("err = %v, want %v", err, tc.wantIs)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestReloadReconcilesServices covers stop on disable and delete: after
// a reload only enabled command connectors keep a service.
func TestReloadReconcilesServices(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxd(t)
	svcs := newTestServices(f, io.Discard)
	enabled := cmdConnector(cmdID, true, toyCmdConfig)
	disabled := cmdConnector("5e7c1ce0-0000-4000-8000-000000000002", false, toyCmdConfig)
	const deleted = "5e7c1ce0-0000-4000-8000-000000000003"
	f.seed(disabled.ID)
	f.seed(deleted)
	m := testManager(fakeRows{rows: []Connector{enabled, disabled}})
	m.SetServices(svcs)
	m.RegisterBuilder("mcp", mcpBuilder(nil, MCPDeferral{}, nil, svcs))

	if err := m.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, want := f.removedIDs(), []string{disabled.ID, deleted}; !slices.Equal(got, want) {
		t.Fatalf("removed = %v, want %v", got, want)
	}
	if st := svcs.status(t.Context(), cmdID); st == nil || st.State != "running" {
		t.Fatalf("enabled connector's service = %+v", st)
	}
	if len(m.Tools(nil)) == 0 {
		t.Fatal("enabled command connector contributed no tools")
	}
}

func TestReloadToleratesUnsupportedServices(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxd(t)
	f.unsupported = true
	m := testManager(fakeRows{rows: []Connector{{ID: "1", Name: "remote", Kind: "mcp", Enabled: true, Config: json.RawMessage(`{}`)}}})
	m.SetServices(newTestServices(f, io.Discard))
	if err := m.Reload(t.Context()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !errors.Is(m.LocalServersSupported(t.Context()), ErrLocalServersUnsupported) {
		t.Fatal("LocalServersSupported did not report the unsupported backend")
	}
}

func TestLocalServersSupported(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxd(t)
	m := testManager(fakeRows{})
	if !errors.Is(m.LocalServersSupported(t.Context()), ErrLocalServersUnsupported) {
		t.Fatal("no runner must be unsupported")
	}
	m.SetServices(newTestServices(f, io.Discard))
	if err := m.LocalServersSupported(t.Context()); err != nil {
		t.Fatalf("docker backend: %v", err)
	}
}

func TestTestReportCommandConnector(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxd(t)
	svcs := newTestServices(f, io.Discard)
	enabled := cmdConnector(cmdID, true, toyCmdConfig)
	disabled := cmdConnector("5e7c1ce0-0000-4000-8000-000000000002", false, toyCmdConfig)
	m := testManager(fakeRows{rows: []Connector{enabled, disabled}})
	m.resolve = toyResolve
	m.SetServices(svcs)
	m.RegisterBuilder("mcp", mcpBuilder(nil, MCPDeferral{}, nil, svcs))

	for _, c := range []Connector{enabled, disabled} {
		report, err := m.TestReport(t.Context(), c.ID)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if report.Service == nil || report.Service.State != "running" {
			t.Fatalf("%s: service = %+v", c.Name, report.Service)
		}
	}
	if got := f.removedIDs(); !slices.Equal(got, []string{disabled.ID}) {
		t.Fatalf("removed after tests = %v, want only the disabled connector's", got)
	}
}

func TestProbeCommand(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxd(t)
	m := testManager(fakeRows{})
	m.SetServices(newTestServices(f, io.Discard))

	res := m.ProbeCommand(t.Context(), "npx", []string{"-y", "toy-mcp"}, map[string]string{"TOY_KEY": "typed-secret"}, true)
	if res.Status != ProbeOK || res.ToolCount != 2 {
		t.Fatalf("probe = %+v", res)
	}
	removed := f.removedIDs()
	if len(removed) != 1 || !validServiceID(removed[0]) {
		t.Fatalf("probe service not removed: %v", removed)
	}

	f.stoppedOnEnsure = "server said typed-secret is invalid"
	res = m.ProbeCommand(t.Context(), "npx", nil, map[string]string{"TOY_KEY": "typed-secret"}, false)
	if res.Status != ProbeUnreachable || strings.Contains(res.Message, "typed-secret") || !strings.Contains(res.Message, "[redacted]") {
		t.Fatalf("failed probe = %+v", res)
	}
}

func validServiceID(s string) bool {
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[14] == '4' && s[18] == '-' && s[23] == '-'
}

func TestProbeStoredCommandConnector(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxd(t)
	svcs := newTestServices(f, io.Discard)
	m := testManager(fakeRows{})
	m.resolve = toyResolve
	m.SetServices(svcs)
	disabled := cmdConnector(cmdID, false, toyCmdConfig)
	res := m.ProbeStored(t.Context(), nil, disabled, nil)
	if res.Status != ProbeOK || res.ToolCount != 2 {
		t.Fatalf("probe = %+v", res)
	}
	if got := f.removedIDs(); !slices.Equal(got, []string{cmdID}) {
		t.Fatalf("a disabled connector's probe service must go: removed %v", got)
	}
}

func TestValidateMCPConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, cfg, want string
	}{
		{"endpoint only", `{"endpoint":"https://x.example/mcp"}`, ""},
		{"empty", ``, ""},
		{"command only", toyCmdConfig, ""},
		{"command with internet", `{"command":"uvx","args":["srv"],"internet":true}`, ""},
		{"both", `{"endpoint":"https://x.example/mcp","command":"npx"}`, "both endpoint and command"},
		{"args without command", `{"endpoint":"https://x.example/mcp","args":["x"]}`, "need config.command"},
		{"internet without command", `{"internet":true}`, "need config.command"},
		{"oauth command", `{"command":"npx","auth_mode":"oauth"}`, "oauth"},
		{"bad env name", `{"command":"npx","env_refs":{"1BAD":"REF"}}`, "not a usable variable name"},
		{"PATH", `{"command":"npx","env_refs":{"PATH":"REF"}}`, "not a usable variable name"},
		{"bridge token", `{"command":"npx","env_refs":{"MCP_BRIDGE_TOKEN":"REF"}}`, "not a usable variable name"},
		{"value not ref", `{"command":"npx","env_refs":{"KEY":"sk live 123"}}`, "never a secret value"},
		{"empty ref", `{"command":"npx","env_refs":{"KEY":""}}`, "never a secret value"},
		{"not an object", `[1]`, "config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateMCPConfig(json.RawMessage(tc.cfg))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestMCPSecretRefsListEnvRefs(t *testing.T) {
	t.Parallel()
	c := Connector{Kind: "mcp", Config: json.RawMessage(`{"command":"npx","env_refs":{"B_KEY":"REF_B","A_KEY":"REF_A"}}`)}
	got := c.SecretRefs()
	want := []SecretRefRole{{RefName: "", Role: "credential"}, {RefName: "REF_A", Role: "env:A_KEY"}, {RefName: "REF_B", Role: "env:B_KEY"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretRefs = %+v, want %+v", got, want)
	}
}

func TestIsCommandMCP(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		c    Connector
		want bool
	}{
		{Connector{Kind: "mcp", Config: json.RawMessage(`{"command":"npx"}`)}, true},
		{Connector{Kind: "mcp", Config: json.RawMessage(`{"endpoint":"https://x"}`)}, false},
		{Connector{Kind: "aws", Config: json.RawMessage(`{"command":"npx"}`)}, false},
		{Connector{Kind: "mcp", Config: json.RawMessage(`nope`)}, false},
	} {
		if got := IsCommandMCP(tc.c); got != tc.want {
			t.Errorf("IsCommandMCP(%s %s) = %v", tc.c.Kind, tc.c.Config, got)
		}
	}
}

// TestReloadHoldsCrashedService covers the crash-loop hold: a reload
// leaves a crashed, unchanged service stopped with its error for
// reviveAfter; a config change or a test starts it at once.
func TestReloadHoldsCrashedService(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxd(t)
	svcs := newTestServices(f, io.Discard)
	clock := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svcs.now = func() time.Time { return clock }
	row := cmdConnector(cmdID, true, toyCmdConfig)
	m := testManager(fakeRows{rows: []Connector{row}})
	m.resolve = toyResolve
	m.SetServices(svcs)
	build := mcpBuilder(nil, MCPDeferral{}, nil, svcs)
	m.RegisterBuilder("mcp", build)
	created := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.created
	}

	if _, err := build(t.Context(), row, toyResolve); err != nil {
		t.Fatal(err)
	}
	f.crash(cmdID, "exited with code 1")
	for _, after := range []time.Duration{0, 5 * time.Minute} {
		clock = clock.Add(after)
		_, err := build(t.Context(), row, toyResolve)
		if err == nil || !strings.Contains(err.Error(), "local server stopped after 3 restarts: exited with code 1") {
			t.Fatalf("+%v: err = %v", after, err)
		}
		if created() != 1 {
			t.Fatalf("+%v: a reload restarted the crashed service", after)
		}
	}
	clock = clock.Add(reviveAfter)
	if _, err := build(t.Context(), row, toyResolve); err != nil || created() != 2 {
		t.Fatalf("after reviveAfter: err %v, created %d", err, created())
	}

	f.crash(cmdID, "exited with code 1")
	if _, err := build(t.Context(), row, toyResolve); err == nil {
		t.Fatal("second crash was not held")
	}
	if _, err := m.TestReport(t.Context(), cmdID); err != nil || created() != 3 {
		t.Fatalf("a test must start the service at once: err %v, created %d", err, created())
	}

	f.crash(cmdID, "exited with code 1")
	if _, err := build(t.Context(), row, toyResolve); err == nil {
		t.Fatal("third crash was not held")
	}
	changed := cmdConnector(cmdID, true, `{"command":"npx","args":["-y","toy-mcp@2"],"env_refs":{"TOY_KEY":"TOY_KEY_REF"}}`)
	if _, err := build(t.Context(), changed, toyResolve); err != nil || created() != 4 {
		t.Fatalf("a config change must start the service at once: err %v, created %d", err, created())
	}
}

func TestServiceStatus(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxd(t)
	svcs := newTestServices(f, io.Discard)
	command := cmdConnector(cmdID, true, toyCmdConfig)
	remote := Connector{ID: "remote", Name: "remote", Kind: "mcp", Enabled: true, Config: json.RawMessage(`{"endpoint":"https://x.example/mcp"}`)}
	m := testManager(fakeRows{rows: []Connector{command, remote}})
	m.SetServices(svcs)
	if _, err := mcpBuilder(nil, MCPDeferral{}, nil, svcs)(t.Context(), command, toyResolve); err != nil {
		t.Fatal(err)
	}
	f.crash(cmdID, "exited with code 1")

	st, err := m.ServiceStatus(t.Context(), cmdID)
	if err != nil || st == nil || st.State != "stopped" || st.LastError != "exited with code 1" {
		t.Fatalf("command connector status = %+v, %v", st, err)
	}
	if st, err := m.ServiceStatus(t.Context(), "remote"); err != nil || st != nil {
		t.Fatalf("endpoint connector status = %+v, %v", st, err)
	}
	if _, err := m.ServiceStatus(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing connector err = %v", err)
	}
}
