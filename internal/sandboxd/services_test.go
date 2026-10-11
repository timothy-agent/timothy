package sandboxd

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
)

const svcID = "c0ffee00-0000-4000-8000-000000000001"

// svcRecord is one container in fakeServiceDaemon.
type svcRecord struct {
	id       string
	labels   map[string]string
	state    container.State
	restarts int
	logs     string
	cfg      container.Config
	host     container.HostConfig
}

// fakeServiceDaemon answers the Engine API calls services make:
// inspect, create, start, remove, list, logs and network connect.
type fakeServiceDaemon struct {
	t         *testing.T
	mu        sync.Mutex
	byName    map[string]*svcRecord
	created   []*svcRecord
	removed   []string
	connected []string
	nextID    int
}

func newFakeServiceDaemon(t *testing.T) *fakeServiceDaemon {
	return &fakeServiceDaemon{t: t, byName: map[string]*svcRecord{}}
}

func (d *fakeServiceDaemon) find(ref string) (string, *svcRecord) {
	for name, rec := range d.byName {
		if name == ref || rec.id == ref {
			return name, rec
		}
	}
	return "", nil
}

func (d *fakeServiceDaemon) handle(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1.51")
	switch {
	case r.Method == http.MethodPost && path == "/containers/create":
		var body struct {
			container.Config
			HostConfig container.HostConfig
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			d.t.Fatalf("decode create: %v", err)
		}
		d.nextID++
		rec := &svcRecord{id: "svc" + strconv.Itoa(d.nextID), labels: body.Labels, cfg: body.Config, host: body.HostConfig}
		d.byName[r.URL.Query().Get("name")] = rec
		d.created = append(d.created, rec)
		writeJSONNoT(w, container.CreateResponse{ID: rec.id})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
		_, rec := d.find(strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/start"))
		if rec == nil {
			http.Error(w, "no such container", http.StatusNotFound)
			return
		}
		rec.state = container.State{Running: true}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && path == "/networks/bridge/connect":
		var body struct{ Container string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		d.connected = append(d.connected, body.Container)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && path == "/containers/json":
		var filters map[string]map[string]bool
		_ = json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)
		items := []container.Summary{}
		for name, rec := range d.byName {
			if matchesLabels(rec.labels, filters["label"]) {
				items = append(items, container.Summary{ID: rec.id, Names: []string{"/" + name}, Labels: rec.labels})
			}
		}
		writeJSONNoT(w, items)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/logs"):
		_, rec := d.find(strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/logs"))
		if rec == nil {
			http.Error(w, "no such container", http.StatusNotFound)
			return
		}
		// One multiplexed frame: stream id, three zero bytes, big-endian
		// length, payload.
		var buf bytes.Buffer
		n := len(rec.logs)
		hdr := []byte{byte(stdcopy.Stderr), 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(hdr[4:], uint32(n)) //nolint:gosec // G115: a test log line fits in a frame
		buf.Write(hdr)
		buf.WriteString(rec.logs)
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		_, _ = w.Write(buf.Bytes())
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/json"):
		name, rec := d.find(strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json"))
		if rec == nil {
			http.Error(w, "no such container", http.StatusNotFound)
			return
		}
		state := rec.state
		writeJSONNoT(w, container.InspectResponse{
			ID: rec.id, Name: "/" + name, RestartCount: rec.restarts,
			Config: &container.Config{Labels: rec.labels},
			State:  &state,
		})
	case r.Method == http.MethodDelete:
		name, rec := d.find(strings.TrimPrefix(path, "/containers/"))
		if rec == nil {
			http.Error(w, "no such container", http.StatusNotFound)
			return
		}
		delete(d.byName, name)
		d.removed = append(d.removed, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		d.t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
	}
}

func (d *fakeServiceDaemon) docker(t *testing.T) *Docker {
	m := newTestDocker(newTestClient(t, d.handle))
	m.servicesNetwork = testOwner + "_" + servicesNetworkKey
	return m
}

func testSpec() ServiceSpec {
	return ServiceSpec{ID: svcID, Command: []string{"mcp-bridge", "--", "npx", "-y", "some-server"}, Env: map[string]string{"API_KEY": "s3cret-value", "REGION": "eu"}}
}

func TestServiceSpecValidate(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", serviceMaxEnvValue+1)
	cases := []struct {
		name   string
		mutate func(*ServiceSpec)
		want   string
	}{
		{name: "ok", mutate: func(*ServiceSpec) {}},
		{name: "bad id", mutate: func(s *ServiceSpec) { s.ID = "../etc" }, want: "uuid"},
		{name: "no command", mutate: func(s *ServiceSpec) { s.Command = nil }, want: "command is required"},
		{name: "blank command", mutate: func(s *ServiceSpec) { s.Command = []string{" "} }, want: "command is required"},
		{name: "too many args", mutate: func(s *ServiceSpec) { s.Command = make([]string, serviceMaxArgs+1); s.Command[0] = "x" }, want: "arguments"},
		{name: "long arg", mutate: func(s *ServiceSpec) { s.Command = []string{"x", strings.Repeat("a", serviceMaxArgLen+1)} }, want: "argument 1"},
		{name: "bad env name", mutate: func(s *ServiceSpec) { s.Env = map[string]string{"1BAD": "v"} }, want: "not a valid variable name"},
		{name: "PATH override", mutate: func(s *ServiceSpec) { s.Env = map[string]string{"PATH": "/evil"} }, want: "set by sandboxd"},
		{name: "HOME override", mutate: func(s *ServiceSpec) { s.Env = map[string]string{"HOME": "/evil"} }, want: "set by sandboxd"},
		{name: "long value", mutate: func(s *ServiceSpec) { s.Env = map[string]string{"TOKEN": long} }, want: "TOKEN is longer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := testSpec()
			tc.mutate(&spec)
			err := spec.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidService) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "s3cret-value") || strings.Contains(err.Error(), long) {
				t.Fatal("error echoes an env value")
			}
		})
	}
}

func TestServiceSpecHash(t *testing.T) {
	t.Parallel()
	base := testSpec().specHash("img")
	reordered := testSpec()
	reordered.Env = map[string]string{"REGION": "eu", "API_KEY": "s3cret-value"}
	if reordered.specHash("img") != base {
		t.Fatal("hash depends on env order")
	}
	for name, mutate := range map[string]func(*ServiceSpec){
		"command":  func(s *ServiceSpec) { s.Command = []string{"other"} },
		"env":      func(s *ServiceSpec) { s.Env["API_KEY"] = "rotated" },
		"internet": func(s *ServiceSpec) { s.Internet = true },
		"split":    func(s *ServiceSpec) { s.Command = []string{"mcp-bridge --", "npx", "-y", "some-server"} },
	} {
		s := testSpec()
		mutate(&s)
		if s.specHash("img") == base {
			t.Fatalf("a %s change must change the hash", name)
		}
	}
	if testSpec().specHash("img2") == base {
		t.Fatal("an image change must change the hash")
	}
}

func TestEnsureServiceCreatesHardenedContainer(t *testing.T) {
	t.Parallel()
	d := newFakeServiceDaemon(t)
	st, err := d.docker(t).EnsureService(t.Context(), testSpec())
	if err != nil {
		t.Fatalf("EnsureService: %v", err)
	}
	if st.State != ServiceRunning || st.Address != "http://timothy-service-"+svcID+":8090" || st.ID != svcID {
		t.Fatalf("status = %+v", st)
	}
	if len(d.created) != 1 || len(d.connected) != 0 {
		t.Fatalf("created %d, connected %v; want one container on the internal network only", len(d.created), d.connected)
	}
	rec := d.created[0]
	if rec.cfg.Image != "img" || rec.cfg.User != sandboxUser || strings.Join(rec.cfg.Cmd, " ") != "mcp-bridge -- npx -y some-server" {
		t.Fatalf("config = %+v", rec.cfg)
	}
	if strings.Join(rec.cfg.Env, "|") != sandboxPath+"|HOME=/home/sandbox|API_KEY=s3cret-value|REGION=eu" {
		t.Fatalf("env = %v", rec.cfg.Env)
	}
	if rec.labels[serviceLabel] != svcID || rec.labels[ownerLabel] != testOwner || rec.labels[serviceSpecLabel] != testSpec().specHash("img") {
		t.Fatalf("labels = %v", rec.labels)
	}
	if _, isMission := rec.labels[missionLabel]; isMission {
		t.Fatal("a service must not carry the mission label")
	}
	h := rec.host
	if string(h.NetworkMode) != testOwner+"_timothy-services" {
		t.Fatalf("network = %q", h.NetworkMode)
	}
	if h.RestartPolicy.Name != container.RestartPolicyOnFailure || h.RestartPolicy.MaximumRetryCount != serviceRestarts {
		t.Fatalf("restart policy = %+v", h.RestartPolicy)
	}
	if !h.ReadonlyRootfs || len(h.CapDrop) != 1 || h.CapDrop[0] != "ALL" || len(h.SecurityOpt) != 1 || h.SecurityOpt[0] != "no-new-privileges" {
		t.Fatalf("hardening = rootfs %v caps %v secopt %v", h.ReadonlyRootfs, h.CapDrop, h.SecurityOpt)
	}
	if len(h.Mounts) != 0 {
		t.Fatalf("mounts = %+v, a service gets no workspace", h.Mounts)
	}
	if h.Memory != serviceMemoryBytes || h.MemorySwap != serviceMemoryBytes || h.PidsLimit == nil || *h.PidsLimit != servicePidsLimit || h.Init == nil || !*h.Init {
		t.Fatalf("resources = mem %d swap %d pids %v init %v", h.Memory, h.MemorySwap, h.PidsLimit, h.Init)
	}
}

func TestEnsureServiceInternetJoinsBridge(t *testing.T) {
	t.Parallel()
	d := newFakeServiceDaemon(t)
	spec := testSpec()
	spec.Internet = true
	if _, err := d.docker(t).EnsureService(t.Context(), spec); err != nil {
		t.Fatalf("EnsureService: %v", err)
	}
	if len(d.connected) != 1 || d.connected[0] != d.created[0].id {
		t.Fatalf("connected = %v, want the new container on bridge", d.connected)
	}
}

func TestEnsureServiceReuseAndReplace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		state       container.State
		spec        func() ServiceSpec
		wantRemoved bool
	}{
		{name: "same spec running is kept", state: container.State{Running: true}, spec: testSpec},
		{name: "same spec restarting is kept", state: container.State{Restarting: true}, spec: testSpec},
		{name: "same spec stopped is recreated", state: container.State{ExitCode: 1}, spec: testSpec, wantRemoved: true},
		{name: "changed spec is replaced", state: container.State{Running: true}, spec: func() ServiceSpec {
			s := testSpec()
			s.Env["API_KEY"] = "rotated"
			return s
		}, wantRemoved: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeServiceDaemon(t)
			d.byName[serviceName(svcID)] = &svcRecord{id: "old", state: tc.state, labels: map[string]string{
				serviceLabel: svcID, ownerLabel: testOwner, serviceSpecLabel: testSpec().specHash("img"),
			}}
			st, err := d.docker(t).EnsureService(t.Context(), tc.spec())
			if err != nil {
				t.Fatalf("EnsureService: %v", err)
			}
			if got := len(d.removed) == 1; got != tc.wantRemoved {
				t.Fatalf("removed = %v, want removed %v", d.removed, tc.wantRemoved)
			}
			if tc.wantRemoved && (len(d.created) != 1 || st.State != ServiceRunning) {
				t.Fatalf("created %d, state %s; want a fresh running container", len(d.created), st.State)
			}
			if !tc.wantRemoved && len(d.created) != 0 {
				t.Fatal("a matching live service must not be recreated")
			}
		})
	}
}

func TestEnsureServiceRefusesForeignContainer(t *testing.T) {
	t.Parallel()
	d := newFakeServiceDaemon(t)
	d.byName[serviceName(svcID)] = &svcRecord{id: "theirs", state: container.State{Running: true}, labels: map[string]string{serviceLabel: svcID, ownerLabel: "timothy-b"}}
	if _, err := d.docker(t).EnsureService(t.Context(), testSpec()); !errors.Is(err, ErrForeignContainer) {
		t.Fatalf("err = %v, want ErrForeignContainer", err)
	}
	if len(d.removed) != 0 || len(d.created) != 0 {
		t.Fatal("another instance's service must not be touched")
	}
}

func TestServiceStatusStoppedCarriesExitAndTail(t *testing.T) {
	t.Parallel()
	d := newFakeServiceDaemon(t)
	d.byName[serviceName(svcID)] = &svcRecord{id: "dead", restarts: 3, state: container.State{ExitCode: 2}, logs: "boot\nfatal: no config\n",
		labels: map[string]string{serviceLabel: svcID, ownerLabel: testOwner, serviceSpecLabel: "x"}}
	list, err := d.docker(t).ListServices(t.Context())
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	st := list[0]
	if st.State != ServiceStopped || st.Restarts != 3 || st.LastError != "exited with code 2" || st.Tail != "boot\nfatal: no config" {
		t.Fatalf("status = %+v", st)
	}
}

func TestServicesStayOutOfMissionsAndOtherOwners(t *testing.T) {
	t.Parallel()
	d := newFakeServiceDaemon(t)
	other := "c0ffee00-0000-4000-8000-000000000002"
	d.byName[serviceName(svcID)] = &svcRecord{id: "mine", state: container.State{Running: true}, labels: map[string]string{serviceLabel: svcID, ownerLabel: testOwner}}
	d.byName[serviceName(other)] = &svcRecord{id: "theirs", state: container.State{Running: true}, labels: map[string]string{serviceLabel: other, ownerLabel: "timothy-b"}}
	d.byName[containerName(validUUID)] = &svcRecord{id: "mission", state: container.State{Running: true}, labels: map[string]string{missionLabel: validUUID, ownerLabel: testOwner}}
	m := d.docker(t)
	services, err := m.ListServices(t.Context())
	if err != nil || len(services) != 1 || services[0].ID != svcID {
		t.Fatalf("ListServices = %+v, %v; want this instance's service only", services, err)
	}
	missions, err := m.List(t.Context())
	if err != nil || len(missions) != 1 || missions[0] != validUUID {
		t.Fatalf("List = %v, %v; want the mission sandbox only", missions, err)
	}
}

func TestRemoveService(t *testing.T) {
	t.Parallel()
	d := newFakeServiceDaemon(t)
	other := "c0ffee00-0000-4000-8000-000000000002"
	d.byName[serviceName(svcID)] = &svcRecord{id: "mine", labels: map[string]string{serviceLabel: svcID, ownerLabel: testOwner}}
	d.byName[serviceName(other)] = &svcRecord{id: "theirs", labels: map[string]string{serviceLabel: other, ownerLabel: "timothy-b"}}
	m := d.docker(t)
	if err := m.RemoveService(t.Context(), svcID); err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	if err := m.RemoveService(t.Context(), other); err != nil {
		t.Fatalf("RemoveService(foreign): %v", err)
	}
	if err := m.RemoveService(t.Context(), "c0ffee00-0000-4000-8000-000000000009"); err != nil {
		t.Fatalf("RemoveService(missing): %v", err)
	}
	if len(d.removed) != 1 || d.removed[0] != serviceName(svcID) {
		t.Fatalf("removed = %v, want this instance's service only", d.removed)
	}
	if err := m.RemoveService(t.Context(), "nope"); !errors.Is(err, ErrInvalidService) {
		t.Fatalf("bad id err = %v", err)
	}
}

func TestKubernetesServicesUnsupported(t *testing.T) {
	t.Parallel()
	k := &Kubernetes{}
	if _, err := k.EnsureService(t.Context(), testSpec()); !errors.Is(err, ErrServicesUnsupported) {
		t.Fatalf("EnsureService err = %v", err)
	}
	if err := k.RemoveService(t.Context(), svcID); !errors.Is(err, ErrServicesUnsupported) {
		t.Fatalf("RemoveService err = %v", err)
	}
	if _, err := k.ListServices(t.Context()); !errors.Is(err, ErrServicesUnsupported) {
		t.Fatalf("ListServices err = %v", err)
	}
}

func TestServiceRoutes(t *testing.T) {
	t.Parallel()
	d := newFakeServiceDaemon(t)
	var logs bytes.Buffer
	a := testAPI(d.docker(t))
	a.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/services/{id}", a.handleEnsureService)
	mux.HandleFunc("DELETE /v1/services/{id}", a.handleRemoveService)
	mux.HandleFunc("GET /v1/services", a.handleListServices)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	for name, tc := range map[string]struct {
		path, body string
		code       int
	}{
		"bad id":        {"/v1/services/not-a-uuid", `{"command":["x"]}`, http.StatusBadRequest},
		"empty command": {"/v1/services/" + svcID, `{"command":[]}`, http.StatusBadRequest},
		"PATH override": {"/v1/services/" + svcID, `{"command":["x"],"env":{"PATH":"s3cret-value"}}`, http.StatusBadRequest},
		"unknown field": {"/v1/services/" + svcID, `{"command":["x"],"privileged":true}`, http.StatusBadRequest},
		"not json":      {"/v1/services/" + svcID, `nope`, http.StatusBadRequest},
	} {
		if rec := do(http.MethodPut, tc.path, tc.body); rec.Code != tc.code {
			t.Fatalf("%s: code = %d (%s), want %d", name, rec.Code, rec.Body, tc.code)
		}
	}

	rec := do(http.MethodPut, "/v1/services/"+svcID, `{"command":["mcp-bridge"],"env":{"API_KEY":"s3cret-value"},"internet":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ensure: %d %s", rec.Code, rec.Body)
	}
	var st ServiceStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st.State != ServiceRunning || st.ID != svcID {
		t.Fatalf("ensure body = %s (%v)", rec.Body, err)
	}

	rec = do(http.MethodGet, "/v1/services", "")
	var list struct{ Services []ServiceStatus }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Services) != 1 {
		t.Fatalf("list = %s (%v)", rec.Body, err)
	}

	if rec = do(http.MethodDelete, "/v1/services/"+svcID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(logs.String(), "s3cret-value") {
		t.Fatalf("logs carry an env value: %s", logs.String())
	}
}

func TestServiceRoutesUnsupportedBackend(t *testing.T) {
	t.Parallel()
	a := &API{mgr: &Kubernetes{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/services/"+svcID, strings.NewReader(`{"command":["x"]}`))
	req.SetPathValue("id", svcID)
	a.handleEnsureService(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("code = %d (%s), want 501", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	a.handleListServices(rec, httptest.NewRequest(http.MethodGet, "/v1/services", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("list code = %d, want 501", rec.Code)
	}
}
