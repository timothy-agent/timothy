package sandboxd

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/common"
	"github.com/moby/moby/api/types/container"

	"github.com/SumonMSelim/timothy/internal/brain/sandboxclient"
)

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testAPI(mgr *Manager) *API {
	return &API{mgr: mgr, log: testLog(), execSem: make(chan struct{}, defaultMaxExecs), maxContainers: defaultMaxContainers}
}

// validUUID is a mission id shaped exactly like gen_random_uuid()'s
// output — every "valid missionID" test uses this so a failure clearly
// means the handler rejected a well-formed id, not a coincidentally
// malformed fixture.
const validUUID = "a1b2c3d4-e5f6-4789-a012-b34c56d78e9f"

// validWorkdirFor is validUUID's own workspace directory, the shape
// brain actually sends. Tests whose exec must reach the daemon use it
// rather than the bare workspace root: since D-116 the scope check runs
// on every ensureContainer path, so an unscoped workdir stops before
// ExecCreate instead of only failing at container create.
const validWorkdirFor = "/workspace/missions/coding/" + validUUID

func execReq(missionID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+missionID+"/exec", strings.NewReader(body))
	req.SetPathValue("missionID", missionID)
	return req
}

func removeReq(missionID string) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, "/v1/sandboxes/"+missionID, nil)
	req.SetPathValue("missionID", missionID)
	return req
}

func TestHandleExecInvalidMissionID(t *testing.T) {
	t.Parallel()
	cases := []string{
		"not-a-uuid",
		"../../etc/passwd",
		"alpine:latest",
		"a1b2c3d4-e5f6-4789-a012-b34c56d78e9",  // one char short
		"A1B2C3D4-E5F6-4789-A012-B34C56D78E9F", // uppercase not accepted
	}
	for _, id := range cases {
		rec := httptest.NewRecorder()
		testAPI(nil).handleExec(rec, execReq(id, `{}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("id=%q: status = %d, want 400", id, rec.Code)
		}
	}
}

func TestHandleRemoveInvalidMissionID(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testAPI(nil).handleRemove(rec, removeReq("../escape"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleExecWorkdirEscape(t *testing.T) {
	t.Parallel()
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected daemon call for a workdir that should 400 before ensureContainer: %s %s", r.Method, r.URL.Path)
	})
	mgr := newTestManager(cli)

	cases := []string{
		"/workspace/../etc",
		"workspace",
		"/workspace/",
		"/other",
	}
	for _, wd := range cases {
		body, _ := json.Marshal(map[string]any{"workdir": wd, "command": "true", "timeout_seconds": 5})
		rec := httptest.NewRecorder()
		testAPI(mgr).handleExec(rec, execReq(validUUID, string(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("workdir=%q: status = %d, want 400", wd, rec.Code)
		}
	}
}

// TestValidExecEnv covers D-053's allowlist: accept known names within
// the size cap, reject an unknown name or an oversized value.
func TestValidExecEnv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		env     map[string]string
		wantOK  bool
		badName string
	}{
		{name: "nil env", env: nil, wantOK: true},
		{name: "empty env", env: map[string]string{}, wantOK: true},
		{
			name: "all allowlisted",
			env: map[string]string{ //nolint:gosec // G101: fixture values, not real credentials.
				"ANTHROPIC_API_KEY":       "sk-test",
				"ANTHROPIC_BASE_URL":      "https://api.anthropic.com",
				"ANTHROPIC_MODEL":         "claude-x",
				"ANTHROPIC_AUTH_TOKEN":    "tok",
				"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat-test",
				"NO_COLOR":                "1",
				"TERM":                    "xterm",
				"NODE_OPTIONS":            "--max-old-space-size=768",
			},
			wantOK: true,
		},
		{
			name: "codex adapter env allowlisted",
			env: map[string]string{ //nolint:gosec // G101: fixture values, not real credentials.
				"CODEX_API_KEY": "sk-test",
				"CODEX_HOME":    "/workspace/missions/x/runs/y/codex-home",
				"NO_COLOR":      "1",
			},
			wantOK: true,
		},
		{
			name: "opencode adapter env allowlisted",
			env: map[string]string{ //nolint:gosec // G101: fixture values, not real credentials.
				"OPENCODE_API_KEY": "sk-test",
				"OPENCODE_CONFIG":  "/workspace/missions/x/runs/y/opencode/opencode.json",
				"NO_COLOR":         "1",
			},
			wantOK: true,
		},
		{
			name:    "unknown name rejected",
			env:     map[string]string{"AWS_SECRET_ACCESS_KEY": "x"},
			wantOK:  false,
			badName: "AWS_SECRET_ACCESS_KEY",
		},
		{
			name:    "oversized value rejected",
			env:     map[string]string{"ANTHROPIC_API_KEY": strings.Repeat("a", execEnvMaxValueLen+1)},
			wantOK:  false,
			badName: "ANTHROPIC_API_KEY",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			badName, ok := validExecEnv(tc.env)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok && badName != tc.badName {
				t.Errorf("badName = %q, want %q", badName, tc.badName)
			}
		})
	}
}

// TestHandleExecUnknownEnvNameRejected confirms the HTTP layer rejects
// a disallowed env name with 400 before ever reaching the daemon, and
// that the offending name (never the value) appears in the response.
func TestHandleExecUnknownEnvNameRejected(t *testing.T) {
	t.Parallel()
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected daemon call for env that should 400 before ensureContainer: %s %s", r.Method, r.URL.Path)
	})
	mgr := newTestManager(cli)

	body := `{"workdir":"/workspace","command":"true","timeout_seconds":5,"env":{"AWS_SECRET_ACCESS_KEY":"leaked-value"}}`
	rec := httptest.NewRecorder()
	testAPI(mgr).handleExec(rec, execReq(validUUID, body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "leaked-value") {
		t.Errorf("response body = %q, must never echo the rejected env value", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "AWS_SECRET_ACCESS_KEY") {
		t.Errorf("response body = %q, want it to name the offending env var", rec.Body.String())
	}
}

// TestHandleExecUnknownEnvironmentRejected confirms an environment key
// outside manager.go's environmentKeys allowlist (D-05x) 400s before
// ever reaching the daemon — mirrors D-053's env-var allowlist gate.
func TestHandleExecUnknownEnvironmentRejected(t *testing.T) {
	t.Parallel()
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected daemon call for an unknown environment that should 400 before ensureContainer: %s %s", r.Method, r.URL.Path)
	})
	mgr := newTestManager(cli)

	body := `{"workdir":"/workspace","command":"true","timeout_seconds":5,"environment":"ruby"}`
	rec := httptest.NewRecorder()
	testAPI(mgr).handleExec(rec, execReq(validUUID, body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ruby") {
		t.Errorf("response body = %q, want it to name the offending environment", rec.Body.String())
	}
}

// TestHandleExecKnownEnvironmentAccepted confirms "", "base", and every
// registered key pass validation (the daemon call itself is exercised
// by TestEnsureContainerNotFoundCreates/TestCreateContainer*).
func TestHandleExecKnownEnvironmentAccepted(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"", "base", "go", "node", "python", "java", "php"} {
		if !validExecEnvironment(env) {
			t.Errorf("validExecEnvironment(%q) = false, want true", env)
		}
	}
}

func TestHandleExecUnknownFieldRejected(t *testing.T) {
	t.Parallel()
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected daemon call: %s %s", r.Method, r.URL.Path)
	})
	mgr := newTestManager(cli)

	body := `{"workdir":"/workspace","command":"true","timeout_seconds":5,"image":"escape:latest"}`
	rec := httptest.NewRecorder()
	testAPI(mgr).handleExec(rec, execReq(validUUID, body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandleExecTimeoutClampVisibleInArgv confirms an out-of-range
// timeout_seconds is clamped server-side before ever reaching the
// daemon: the fake records ExecCreate's Cmd argv (the "timeout N ..."
// wrapper) and asserts the clamped value, not the caller's raw input.
// ExecAttach is left to fail (this fake speaks plain JSON-over-HTTP,
// not the raw hijacked upgrade ExecAttach needs — that path is
// exercised by the integration test against a real daemon); what
// matters here is what argv the daemon received before that point.
func TestHandleExecTimeoutClampVisibleInArgv(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotCmd []string
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			// liveContainers' List call, ahead of ensureContainer: no
			// sandboxes running yet, so the container cap never trips.
			writeJSON(t, w, http.StatusOK, []container.Summary{})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "timothy-sandbox-") && strings.HasSuffix(r.URL.Path, "/json"):
			writeJSON(t, w, http.StatusOK, container.InspectResponse{
				ID: "c1", State: &container.State{Running: true},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read exec create body: %v", err)
			}
			var req container.ExecCreateRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("unmarshal exec create body: %v", err)
			}
			mu.Lock()
			gotCmd = req.Cmd
			mu.Unlock()
			writeJSON(t, w, http.StatusCreated, common.IDResponse{ID: "exec1"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			// No hijack support: report a plain error status so ExecAttach
			// fails cleanly instead of hanging on a protocol upgrade this
			// fake doesn't implement.
			http.Error(w, "attach not supported by fake daemon", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)

	// 999999 seconds is far past execMaxTimeout (15m); the handler must
	// clamp it before it ever reaches the daemon.
	body := `{"workdir":"` + validWorkdirFor + `","command":"true","timeout_seconds":999999}`
	rec := httptest.NewRecorder()
	testAPI(mgr).handleExec(rec, execReq(validUUID, body))

	mu.Lock()
	defer mu.Unlock()
	if len(gotCmd) < 4 {
		t.Fatalf("ExecCreate argv = %v, want at least [timeout -k N secs ...]", gotCmd)
	}
	if gotCmd[0] != "timeout" {
		t.Fatalf("ExecCreate argv[0] = %q, want %q", gotCmd[0], "timeout")
	}
	secs := gotCmd[3]
	if secs != "900" {
		t.Errorf("clamped timeout seconds = %q, want 900 (execMaxTimeout)", secs)
	}
}

// TestHandleExecEnvReachesExecCreate confirms an allowlisted env
// request field arrives at Docker's ExecCreate as "K=V" entries — the
// D-053 path from HTTP request to the daemon call.
func TestHandleExecEnvReachesExecCreate(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotEnv []string
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			writeJSON(t, w, http.StatusOK, []container.Summary{})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "timothy-sandbox-") && strings.HasSuffix(r.URL.Path, "/json"):
			writeJSON(t, w, http.StatusOK, container.InspectResponse{
				ID: "c1", State: &container.State{Running: true},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read exec create body: %v", err)
			}
			var req container.ExecCreateRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("unmarshal exec create body: %v", err)
			}
			mu.Lock()
			gotEnv = req.Env
			mu.Unlock()
			writeJSON(t, w, http.StatusCreated, common.IDResponse{ID: "exec1"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			http.Error(w, "attach not supported by fake daemon", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)

	body := `{"workdir":"` + validWorkdirFor + `","command":"true","timeout_seconds":5,"env":{"ANTHROPIC_API_KEY":"sk-test"}}`
	rec := httptest.NewRecorder()
	testAPI(mgr).handleExec(rec, execReq(validUUID, body))

	mu.Lock()
	defer mu.Unlock()
	want := "ANTHROPIC_API_KEY=sk-test"
	found := false
	for _, e := range gotEnv {
		if e == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("ExecCreate env = %v, want it to contain %q", gotEnv, want)
	}
}

// TestHandleExecNoEnvOmitsExecEnv confirms existing exec behavior is
// unchanged when the request omits env entirely — no env entries reach
// ExecCreate.
func TestHandleExecNoEnvOmitsExecEnv(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotEnv []string
	envSet := false
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			writeJSON(t, w, http.StatusOK, []container.Summary{})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "timothy-sandbox-") && strings.HasSuffix(r.URL.Path, "/json"):
			writeJSON(t, w, http.StatusOK, container.InspectResponse{
				ID: "c1", State: &container.State{Running: true},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read exec create body: %v", err)
			}
			var req container.ExecCreateRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("unmarshal exec create body: %v", err)
			}
			mu.Lock()
			gotEnv, envSet = req.Env, true
			mu.Unlock()
			writeJSON(t, w, http.StatusCreated, common.IDResponse{ID: "exec1"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			http.Error(w, "attach not supported by fake daemon", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)

	body := `{"workdir":"` + validWorkdirFor + `","command":"true","timeout_seconds":5}`
	rec := httptest.NewRecorder()
	testAPI(mgr).handleExec(rec, execReq(validUUID, body))

	mu.Lock()
	defer mu.Unlock()
	if !envSet {
		t.Fatal("ExecCreate was never called")
	}
	if len(gotEnv) != 0 {
		t.Errorf("ExecCreate env = %v, want empty when the request omits env", gotEnv)
	}
}

func TestHandleRemoveIdempotent(t *testing.T) {
	t.Parallel()
	d := newFakeDaemon(map[string]map[string]string{
		containerName(validUUID): {missionLabel: validUUID, ownerLabel: testOwner},
	})
	api := testAPI(newTestManager(newTestClient(t, d.handle)))

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		api.handleRemove(rec, removeReq(validUUID))
		if rec.Code != http.StatusNoContent {
			t.Errorf("call %d: status = %d, want 204", i, rec.Code)
		}
	}
	if got := d.removedNames(); len(got) != 1 || got[0] != containerName(validUUID) {
		t.Fatalf("daemon removed %v, want exactly [%s]", got, containerName(validUUID))
	}
}

// otherUUID / legacyUUID are mission ids of containers this instance
// does not own: one labelled by another instance, one from before D-132
// with no owner label.
const (
	otherUUID  = "b2c3d4e5-0000-4000-8000-000000000002"
	legacyUUID = "b2c3d4e5-0000-4000-8000-000000000003"
)

// fakeDaemon is a Docker daemon stand-in that several Managers can
// share, like instances on one host. It honors label filters on list,
// inspect by name, and remove by id.
type fakeDaemon struct {
	mu         sync.Mutex
	containers map[string]map[string]string // name -> labels
	removed    []string
}

func newFakeDaemon(containers map[string]map[string]string) *fakeDaemon {
	return &fakeDaemon{containers: containers}
}

func (d *fakeDaemon) removedNames() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.removed)
}

func (d *fakeDaemon) exists(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.containers[name]
	return ok
}

func (d *fakeDaemon) handle(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rest := r.URL.Path[strings.Index(r.URL.Path, "/containers/")+len("/containers/"):]
	switch {
	case r.Method == http.MethodGet && rest == "json":
		var filters map[string]map[string]bool
		if f := r.URL.Query().Get("filters"); f != "" {
			if err := json.Unmarshal([]byte(f), &filters); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		items := []container.Summary{}
		for name, labels := range d.containers {
			if matchesLabels(labels, filters["label"]) {
				items = append(items, container.Summary{ID: name, Names: []string{"/" + name}, Labels: labels})
			}
		}
		writeJSONNoT(w, items)
	case r.Method == http.MethodGet && strings.HasSuffix(rest, "/json"):
		name := strings.TrimSuffix(rest, "/json")
		labels, ok := d.containers[name]
		if !ok {
			http.Error(w, "no such container", http.StatusNotFound)
			return
		}
		writeJSONNoT(w, container.InspectResponse{
			ID: name, Name: "/" + name,
			Config: &container.Config{Labels: labels},
			State:  &container.State{Running: true},
		})
	case r.Method == http.MethodDelete:
		if _, ok := d.containers[rest]; !ok {
			http.Error(w, "no such container", http.StatusNotFound)
			return
		}
		delete(d.containers, rest)
		d.removed = append(d.removed, rest)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected call "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
	}
}

// matchesLabels applies Docker's label filter: every "key" or
// "key=value" entry must match.
func matchesLabels(labels map[string]string, want map[string]bool) bool {
	for f := range want {
		k, v, hasValue := strings.Cut(f, "=")
		got, ok := labels[k]
		if !ok || (hasValue && got != v) {
			return false
		}
	}
	return true
}

func writeJSONNoT(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// TestSweepLeavesOtherInstancesContainers is the issue #1036 regression:
// instance A's mission is missing from instance B's store, so B's sweep
// treats it as terminal. B must still leave A's container, and any
// pre-D-132 container, untouched, and remove only its own orphan.
func TestSweepLeavesOtherInstancesContainers(t *testing.T) {
	t.Parallel()
	const ownerA, ownerB = "timothy-timothy", "timothy-demo1"
	d := newFakeDaemon(map[string]map[string]string{
		containerName(validUUID):  {missionLabel: validUUID, ownerLabel: ownerA},
		containerName(otherUUID):  {missionLabel: otherUUID, ownerLabel: ownerB},
		containerName(legacyUUID): {missionLabel: legacyUUID},
	})
	cli := newTestClient(t, d.handle)
	mgrB := newTestManager(cli)
	mgrB.owner = ownerB
	apiB := testAPI(mgrB)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sandboxes", apiB.handleList)
	mux.HandleFunc("DELETE /v1/sandboxes/{missionID}", apiB.handleRemove)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var seen []string
	err := sandboxclient.New(srv.URL).Sweep(t.Context(), func(id string) bool {
		seen = append(seen, id)
		return true // B's store knows none of these missions
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(seen) != 1 || seen[0] != otherUUID {
		t.Errorf("B's sweep saw %v, want only its own [%s]", seen, otherUUID)
	}
	if got := d.removedNames(); len(got) != 1 || got[0] != containerName(otherUUID) {
		t.Errorf("daemon removed %v, want only B's own %s", got, containerName(otherUUID))
	}
	for _, id := range []string{validUUID, legacyUUID} {
		if !d.exists(containerName(id)) {
			t.Errorf("container %s was removed by another instance's sweep", containerName(id))
		}
	}

	// A direct DELETE of A's mission through B is equally a no-op.
	rec := httptest.NewRecorder()
	apiB.handleRemove(rec, removeReq(validUUID))
	if rec.Code != http.StatusNoContent || !d.exists(containerName(validUUID)) {
		t.Errorf("DELETE via B: status %d, A's container exists=%v; want 204 and untouched", rec.Code, d.exists(containerName(validUUID)))
	}
}

func TestHandleListLabelFiltering(t *testing.T) {
	t.Parallel()
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json") {
			writeJSON(t, w, http.StatusOK, []container.Summary{
				{ID: "c1", Labels: map[string]string{missionLabel: validUUID, ownerLabel: testOwner}},
				{ID: "c2", Labels: map[string]string{missionLabel: "", ownerLabel: testOwner}},
				{ID: "c3", Labels: map[string]string{}},
				{ID: "c4", Labels: map[string]string{missionLabel: otherUUID, ownerLabel: "timothy-b"}},
				{ID: "c5", Labels: map[string]string{missionLabel: legacyUUID}},
			})
			return
		}
		t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
	})
	mgr := newTestManager(cli)

	rec := httptest.NewRecorder()
	testAPI(mgr).handleList(rec, httptest.NewRequest(http.MethodGet, "/v1/sandboxes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out struct {
		MissionIDs []string `json:"mission_ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.MissionIDs) != 1 || out.MissionIDs[0] != validUUID {
		t.Fatalf("mission_ids = %v, want [%s] (empty/missing labels and other owners skipped)", out.MissionIDs, validUUID)
	}
}

// TestHandleCapacity confirms the /capacity route reports Manager's
// Capacity result as JSON (D-056) — brain's admission gate reads this.
func TestHandleCapacity(t *testing.T) {
	t.Parallel()
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json") {
			writeJSON(t, w, http.StatusOK, []container.Summary{
				{ID: "c1", Labels: map[string]string{missionLabel: validUUID, ownerLabel: testOwner}},
			})
			return
		}
		t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
	})
	mgr := newTestManager(cli)

	rec := httptest.NewRecorder()
	testAPI(mgr).handleCapacity(rec, httptest.NewRequest(http.MethodGet, "/capacity", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out capacityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.RunningSandboxes != 1 {
		t.Fatalf("running_sandboxes = %d, want 1", out.RunningSandboxes)
	}
	if out.MemAvailableMB <= 0 {
		t.Fatalf("mem_available_mb = %d, want > 0 from a real /proc/meminfo", out.MemAvailableMB)
	}
}
