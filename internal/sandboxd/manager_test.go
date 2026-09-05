package sandboxd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// newTestClient points a real *client.Client at a fake HTTP server
// standing in for the Docker daemon — the Engine API is plain
// JSON-over-HTTP for every call this package makes except exec attach
// (a raw hijacked connection), so this covers everything but Exec
// itself; Exec's attach path is covered by the integration test
// against a real daemon.
func newTestClient(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cli, err := client.New(client.WithHost(srv.URL), client.WithAPIVersion("1.51"))
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	return cli
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func newTestManager(cli *client.Client) *Manager {
	return &Manager{cli: cli, baseImage: "img", locks: map[string]*sync.Mutex{}}
}

func TestResolveWorkspaceMountVolume(t *testing.T) {
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, container.InspectResponse{
			ID: "self",
			Mounts: []container.MountPoint{
				{Type: mount.TypeVolume, Name: "timothy_workspace", Destination: "/workspace"},
				{Type: mount.TypeVolume, Name: "unrelated", Destination: "/other"},
			},
		})
	})

	m, err := resolveWorkspaceMount(context.Background(), cli)
	if err != nil {
		t.Fatalf("resolveWorkspaceMount: %v", err)
	}
	if m.Source != "timothy_workspace" || m.Target != workspaceMountPath {
		t.Fatalf("mount = %+v, want source=timothy_workspace target=%s", m, workspaceMountPath)
	}
}

func TestResolveWorkspaceMountBind(t *testing.T) {
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, container.InspectResponse{
			ID: "self",
			Mounts: []container.MountPoint{
				{Type: mount.TypeBind, Source: "/host/dev/workspace", Destination: "/workspace"},
			},
		})
	})

	m, err := resolveWorkspaceMount(context.Background(), cli)
	if err != nil {
		t.Fatalf("resolveWorkspaceMount: %v", err)
	}
	if m.Type != mount.TypeBind || m.Source != "/host/dev/workspace" {
		t.Fatalf("mount = %+v, want a bind mount at /host/dev/workspace", m)
	}
}

func TestResolveWorkspaceMountMissing(t *testing.T) {
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, container.InspectResponse{
			ID:     "self",
			Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "x", Destination: "/data"}},
		})
	})

	// Fail closed: no mount at /workspace must error, never guess.
	if _, err := resolveWorkspaceMount(context.Background(), cli); err == nil {
		t.Fatal("resolveWorkspaceMount: want error when no /workspace mount is present, got nil")
	}
}

// TestEnsureContainerRunningReusesInPlace confirms a container already
// Running is returned as-is with no create/start call.
func TestEnsureContainerRunningReusesInPlace(t *testing.T) {
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.51/containers/timothy-sandbox-m1/json":
			writeJSON(t, w, http.StatusOK, container.InspectResponse{
				ID: "c1", State: &container.State{Running: true},
			})
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)
	id, err := mgr.ensureContainer(context.Background(), "m1", "")
	if err != nil {
		t.Fatalf("ensureContainer: %v", err)
	}
	if id != "c1" {
		t.Fatalf("id = %q, want c1", id)
	}
}

// TestEnsureContainerExitedRestarts confirms an Exited container is
// restarted in place (not recreated) — this is what preserves any
// packages a worker installed earlier in the mission and recovers a
// container left stopped by a host reboot.
func TestEnsureContainerExitedRestarts(t *testing.T) {
	started := false
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.51/containers/timothy-sandbox-m1/json":
			writeJSON(t, w, http.StatusOK, container.InspectResponse{
				ID: "c1", State: &container.State{Running: false},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/c1/start":
			started = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)
	id, err := mgr.ensureContainer(context.Background(), "m1", "")
	if err != nil {
		t.Fatalf("ensureContainer: %v", err)
	}
	if id != "c1" || !started {
		t.Fatalf("id=%q started=%v, want c1/true", id, started)
	}
}

// TestEnsureContainerNotFoundCreates confirms a missing container is
// created (with the safety-relevant HostConfig/Config fields set) and
// started.
func TestEnsureContainerNotFoundCreates(t *testing.T) {
	var created bool
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.51/containers/timothy-sandbox-m1/json":
			http.Error(w, "no such container", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/create":
			created = true
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read create body: %v", err)
			}
			var cfg struct {
				container.Config
				HostConfig container.HostConfig
			}
			if err := json.Unmarshal(body, &cfg); err != nil {
				t.Fatalf("unmarshal create body: %v", err)
			}
			if cfg.HostConfig.Init == nil || !*cfg.HostConfig.Init {
				t.Errorf("create body: HostConfig.Init not set true")
			}
			if cfg.User != sandboxUser {
				t.Errorf("create body: User = %q, want %q", cfg.User, sandboxUser)
			}
			if len(cfg.HostConfig.Mounts) != 1 || cfg.HostConfig.Mounts[0].Target != workspaceMountPath {
				t.Errorf("create body: Mounts = %+v, want one mount at %s", cfg.HostConfig.Mounts, workspaceMountPath)
			}
			for _, e := range cfg.Env {
				if len(e) >= len("DATABASE_URL") && e[:len("DATABASE_URL")] == "DATABASE_URL" {
					t.Errorf("create body: Env leaked DATABASE_URL")
				}
			}
			writeJSON(t, w, http.StatusCreated, container.CreateResponse{ID: "new1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/new1/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
	id, err := mgr.ensureContainer(context.Background(), "m1", "")
	if err != nil {
		t.Fatalf("ensureContainer: %v", err)
	}
	if id != "new1" || !created {
		t.Fatalf("id=%q created=%v, want new1/true", id, created)
	}
}

// TestEnsureContainerCreateConflictReinspects covers the race where a
// concurrent tool call within the same mission turn (loop.Agent runs
// up to maxParallelTools concurrently) both attempt creation despite
// the mission lock — e.g. after a crash left a stale container the
// mutex map doesn't yet know about. A 409 on create must re-inspect
// and use whatever now exists rather than erroring.
func TestEnsureContainerCreateConflictReinspects(t *testing.T) {
	inspectCalls := 0
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.51/containers/timothy-sandbox-m1/json":
			inspectCalls++
			if inspectCalls == 1 {
				http.Error(w, "no such container", http.StatusNotFound)
				return
			}
			writeJSON(t, w, http.StatusOK, container.InspectResponse{
				ID: "sibling1", State: &container.State{Running: true},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/create":
			http.Error(w, "already exists", http.StatusConflict)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
	id, err := mgr.ensureContainer(context.Background(), "m1", "")
	if err != nil {
		t.Fatalf("ensureContainer: %v", err)
	}
	if id != "sibling1" {
		t.Fatalf("id = %q, want sibling1 (the container the racing sibling created)", id)
	}
}

// TestResolveMountStateVolume confirms resolveMount generalizes to
// D-054's state-volume lookup (keyed on stateVolumeMetaPath) the same
// way it already resolves the workspace mount.
func TestResolveMountStateVolume(t *testing.T) {
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, container.InspectResponse{
			ID: "self",
			Mounts: []container.MountPoint{
				{Type: mount.TypeVolume, Name: "timothy_executor-claude-state", Destination: stateVolumeMetaPath},
			},
		})
	})

	m, err := resolveMount(context.Background(), cli, stateVolumeMetaPath, executorStateMountPath)
	if err != nil {
		t.Fatalf("resolveMount: %v", err)
	}
	if m.Source != "timothy_executor-claude-state" || m.Target != executorStateMountPath {
		t.Fatalf("mount = %+v, want source=timothy_executor-claude-state target=%s", m, executorStateMountPath)
	}
}

// TestCreateContainerIncludesStateMountWhenPresent confirms a
// configured state mount is added (rw) to every mission container.
func TestCreateContainerIncludesStateMountWhenPresent(t *testing.T) {
	var gotMounts []mount.Mount
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/create":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read create body: %v", err)
			}
			var cfg struct {
				HostConfig container.HostConfig
			}
			if err := json.Unmarshal(body, &cfg); err != nil {
				t.Fatalf("unmarshal create body: %v", err)
			}
			gotMounts = cfg.HostConfig.Mounts
			writeJSON(t, w, http.StatusCreated, container.CreateResponse{ID: "new1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/new1/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
	mgr.stateMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_executor-claude-state", Target: executorStateMountPath}

	if _, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", ""); err != nil {
		t.Fatalf("createContainer: %v", err)
	}
	if len(gotMounts) != 2 {
		t.Fatalf("create body: Mounts = %+v, want 2 (workspace + state)", gotMounts)
	}
	found := false
	for _, m := range gotMounts {
		if m.Target == executorStateMountPath && m.Source == "timothy_executor-claude-state" {
			found = true
		}
	}
	if !found {
		t.Errorf("create body: Mounts = %+v, want one at %s", gotMounts, executorStateMountPath)
	}
}

// TestCreateContainerOmitsStateMountWhenAbsent confirms a container is
// still created successfully (no state mount) when the operator hasn't
// configured the volume — missions on API-key auth must keep working.
func TestCreateContainerOmitsStateMountWhenAbsent(t *testing.T) {
	var gotMounts []mount.Mount
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/create":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read create body: %v", err)
			}
			var cfg struct {
				HostConfig container.HostConfig
			}
			if err := json.Unmarshal(body, &cfg); err != nil {
				t.Fatalf("unmarshal create body: %v", err)
			}
			gotMounts = cfg.HostConfig.Mounts
			writeJSON(t, w, http.StatusCreated, container.CreateResponse{ID: "new1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/new1/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
	// mgr.stateMount left zero-value: not configured.

	if _, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", ""); err != nil {
		t.Fatalf("createContainer: %v", err)
	}
	if len(gotMounts) != 1 || gotMounts[0].Target != workspaceMountPath {
		t.Fatalf("create body: Mounts = %+v, want exactly [workspace]", gotMounts)
	}
}

// TestImageFor covers D-05x's environment->image derivation: "" and
// "base" both resolve to the operator-configured base image; every
// other allowlisted key derives a variant ref from the base ref
// (repository + "-<key>", same tag); a digest base or an unrecognized
// key is a loud error, never a silent fallback.
func TestImageFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		baseImage   string
		environment string
		want        string
		wantErr     bool
	}{
		{name: "empty", baseImage: "timothy-sandbox:latest", environment: "", want: "timothy-sandbox:latest"},
		{name: "base", baseImage: "timothy-sandbox:latest", environment: "base", want: "timothy-sandbox:latest"},
		{name: "go", baseImage: "timothy-sandbox:latest", environment: "go", want: "timothy-sandbox-go:latest"},
		{name: "node", baseImage: "timothy-sandbox:latest", environment: "node", want: "timothy-sandbox-node:latest"},
		{name: "python", baseImage: "timothy-sandbox:latest", environment: "python", want: "timothy-sandbox-python:latest"},
		{name: "java", baseImage: "timothy-sandbox:latest", environment: "java", want: "timothy-sandbox-java:latest"},
		{name: "php", baseImage: "timothy-sandbox:latest", environment: "php", want: "timothy-sandbox-php:latest"},
		{
			name:        "ghcr versioned",
			baseImage:   "ghcr.io/timothy-agent/timothy-sandbox:0.1.0-alpha.21",
			environment: "go",
			want:        "ghcr.io/timothy-agent/timothy-sandbox-go:0.1.0-alpha.21",
		},
		{
			name:        "registry with port",
			baseImage:   "localhost:5000/timothy-sandbox:v1",
			environment: "go",
			want:        "localhost:5000/timothy-sandbox-go:v1",
		},
		{
			name:        "untagged base",
			baseImage:   "timothy-sandbox",
			environment: "go",
			want:        "timothy-sandbox-go",
		},
		{
			name:        "digest base rejected",
			baseImage:   "x@sha256:abc",
			environment: "go",
			wantErr:     true,
		},
		{name: "unknown env", baseImage: "timothy-sandbox:latest", environment: "ruby", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := imageFor(tc.baseImage, tc.environment)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("imageFor(%q, %q) = %q, nil, want an error", tc.baseImage, tc.environment, got)
				}
				if tc.environment == "ruby" && !errors.Is(err, ErrUnknownEnvironment) {
					t.Errorf("imageFor(%q, %q) error = %v, want ErrUnknownEnvironment", tc.baseImage, tc.environment, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("imageFor(%q, %q): %v", tc.baseImage, tc.environment, err)
			}
			if got != tc.want {
				t.Errorf("imageFor(%q, %q) = %q, want %q", tc.baseImage, tc.environment, got, tc.want)
			}
		})
	}
}

func TestNewManagerEmptyImageErrors(t *testing.T) {
	mgr, err := NewManager(context.Background(), "", nil)
	if err == nil {
		t.Fatal("NewManager(\"\") = nil error, want an error (sandbox is mandatory)")
	}
	if mgr != nil {
		t.Fatalf("NewManager(\"\") = %v, want nil manager alongside the error", mgr)
	}
}

// TestEnsureContainerPullsMissingImage confirms a create that 404s for
// a missing image triggers exactly one ImagePull, then a retried create
// that succeeds.
func TestEnsureContainerPullsMissingImage(t *testing.T) {
	var createCalls, pullCalls int
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.51/containers/timothy-sandbox-m1/json":
			http.Error(w, "no such container", http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.51/images/"):
			// pullImage's ImageInspect re-check (D-056): not present yet.
			http.Error(w, "no such image", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/create":
			createCalls++
			if createCalls == 1 {
				http.Error(w, "No such image: img:latest", http.StatusNotFound)
				return
			}
			writeJSON(t, w, http.StatusCreated, container.CreateResponse{ID: "new1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/images/create":
			pullCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"Pull complete"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/new1/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}

	id, err := mgr.ensureContainer(context.Background(), "m1", "")
	if err != nil {
		t.Fatalf("ensureContainer: %v", err)
	}
	if id != "new1" {
		t.Fatalf("id = %q, want new1", id)
	}
	if pullCalls != 1 {
		t.Fatalf("pullCalls = %d, want exactly 1", pullCalls)
	}
	if createCalls != 2 {
		t.Fatalf("createCalls = %d, want exactly 2 (initial 404 + retry)", createCalls)
	}
}

// TestEnsureContainerPullFailureNamesImage confirms a pull that itself
// fails surfaces an error naming the image, not a generic infra error.
func TestEnsureContainerPullFailureNamesImage(t *testing.T) {
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.51/containers/timothy-sandbox-m1/json":
			http.Error(w, "no such container", http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.51/images/"):
			http.Error(w, "no such image", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/create":
			http.Error(w, "No such image: img:latest", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/images/create":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"errorDetail":{"message":"manifest unknown"}}`))
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}

	_, err := mgr.ensureContainer(context.Background(), "m1", "")
	if err == nil {
		t.Fatal("ensureContainer: want error when pull fails, got nil")
	}
	if !strings.Contains(err.Error(), "img") {
		t.Errorf("error = %q, want it to name the image", err.Error())
	}
}

// TestEnsureContainerConcurrentPullsDoNotOverlap covers D-056's pullMu:
// two missions racing a create for the SAME missing image must not
// trigger two overlapping ImagePull calls — the second caller's
// ImageInspect (after acquiring pullMu) must find the image the first
// caller already pulled.
func TestEnsureContainerConcurrentPullsDoNotOverlap(t *testing.T) {
	var mu sync.Mutex
	pullCalls := 0
	pullInFlight := false
	imagePresent := false

	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.51/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			http.Error(w, "no such container", http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.51/images/"):
			mu.Lock()
			present := imagePresent
			mu.Unlock()
			if present {
				writeJSON(t, w, http.StatusOK, container.InspectResponse{})
				return
			}
			http.Error(w, "no such image", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/images/create":
			mu.Lock()
			if pullInFlight {
				mu.Unlock()
				t.Errorf("overlapping ImagePull calls: pullMu did not serialize")
				return
			}
			pullInFlight = true
			pullCalls++
			mu.Unlock()

			time.Sleep(20 * time.Millisecond) // widen the window a missing lock would race in

			mu.Lock()
			imagePresent = true
			pullInFlight = false
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"Pull complete"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/create":
			mu.Lock()
			present := imagePresent
			mu.Unlock()
			if !present {
				http.Error(w, "No such image: img:latest", http.StatusNotFound)
				return
			}
			writeJSON(t, w, http.StatusCreated, container.CreateResponse{ID: "new-" + r.RemoteAddr})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", "")
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("createContainer[%d]: %v", i, err)
		}
	}
	if pullCalls != 1 {
		t.Fatalf("pullCalls = %d, want exactly 1 (second caller's ImageInspect after pullMu should find it already pulled)", pullCalls)
	}
}

// TestCreateContainerHardensResources covers D-056: MemorySwap caps at
// the same 2 GiB the memory limit does (so swap can never let a sandbox
// exceed it), MemoryReservation is the soft-limit floor, and
// OomScoreAdj biases the kernel to sacrifice a sandbox before brain.
func TestCreateContainerHardensResources(t *testing.T) {
	var gotHostConfig container.HostConfig
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/create":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read create body: %v", err)
			}
			var cfg struct {
				HostConfig container.HostConfig
			}
			if err := json.Unmarshal(body, &cfg); err != nil {
				t.Fatalf("unmarshal create body: %v", err)
			}
			gotHostConfig = cfg.HostConfig
			writeJSON(t, w, http.StatusCreated, container.CreateResponse{ID: "new1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/new1/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}

	if _, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", ""); err != nil {
		t.Fatalf("createContainer: %v", err)
	}
	if gotHostConfig.MemorySwap != sandboxMemoryBytes {
		t.Errorf("MemorySwap = %d, want %d (== Memory, swap included not additive)", gotHostConfig.MemorySwap, sandboxMemoryBytes)
	}
	if gotHostConfig.MemoryReservation != sandboxMemoryReservationBytes {
		t.Errorf("MemoryReservation = %d, want %d", gotHostConfig.MemoryReservation, sandboxMemoryReservationBytes)
	}
	if gotHostConfig.OomScoreAdj != sandboxOomScoreAdj {
		t.Errorf("OomScoreAdj = %d, want %d", gotHostConfig.OomScoreAdj, sandboxOomScoreAdj)
	}
	if !slices.Equal(gotHostConfig.CapDrop, []string{"ALL"}) {
		t.Errorf("CapDrop = %v, want [ALL]", gotHostConfig.CapDrop)
	}
	if !slices.Contains(gotHostConfig.SecurityOpt, "no-new-privileges") {
		t.Errorf("SecurityOpt = %v, want to contain no-new-privileges", gotHostConfig.SecurityOpt)
	}
}

// TestCreateContainerSetsUserPrefixPath confirms the container's PATH
// (issue #568) leads with the sandbox HOME's user-install dirs, so a
// tool a worker installs with `npm install -g` or `pip install --user`
// is reachable by a later exec in the same container.
func TestCreateContainerSetsUserPrefixPath(t *testing.T) {
	var gotEnv []string
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/create":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read create body: %v", err)
			}
			var cfg struct {
				Env []string
			}
			if err := json.Unmarshal(body, &cfg); err != nil {
				t.Fatalf("unmarshal create body: %v", err)
			}
			gotEnv = cfg.Env
			writeJSON(t, w, http.StatusCreated, container.CreateResponse{ID: "new1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/new1/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	mgr := newTestManager(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}

	if _, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", ""); err != nil {
		t.Fatalf("createContainer: %v", err)
	}
	if !slices.Contains(gotEnv, sandboxPath) {
		t.Fatalf("Env = %v, want to contain %q", gotEnv, sandboxPath)
	}
	wantPrefix := "PATH=/home/sandbox/.local/bin:/home/sandbox/.npm-global/bin:/home/sandbox/go/bin:"
	if !strings.HasPrefix(sandboxPath, wantPrefix) {
		t.Errorf("sandboxPath = %q, want prefix %q", sandboxPath, wantPrefix)
	}
}

// TestManagerCapacityReadsRealMeminfo confirms Capacity reads the
// process's real /proc/meminfo (the host view sandboxd's own
// memory-unlimited container sees) and folds in List's live container
// count — it can't script MemAvailable itself, so it only asserts the
// report is internally consistent with whatever this test process's
// actual /proc/meminfo and container list report.
func TestManagerCapacityReadsRealMeminfo(t *testing.T) {
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []container.Summary{})
	})
	mgr := newTestManager(cli)

	report, err := mgr.Capacity(context.Background())
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if report.MemAvailableMB <= 0 {
		t.Errorf("MemAvailableMB = %d, want > 0 from a real /proc/meminfo", report.MemAvailableMB)
	}
	if report.RunningSandboxes != 0 {
		t.Errorf("RunningSandboxes = %d, want 0 (fake daemon reported none)", report.RunningSandboxes)
	}
	wantAdmit := report.MemAvailableMB >= hostMemoryFloorMB+perSandboxEstimateMB
	if report.Admit != wantAdmit {
		t.Errorf("Admit = %v, want %v for MemAvailableMB=%d", report.Admit, wantAdmit, report.MemAvailableMB)
	}
	if !report.Admit && report.Reason == "" {
		t.Error("Admit = false, want a non-empty Reason")
	}
}

// TestOpenMeminfo covers the LXC/lxcfs path preference: primary
// (hostMeminfoPath's bind mount) wins when present, and a missing
// primary falls back to the plain /proc/meminfo path — reproduces the
// bug where sandboxd read the hypervisor's /proc/meminfo instead of
// the guest's lxcfs-served one.
func TestOpenMeminfo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	primary := filepath.Join(dir, "host-meminfo")
	fallback := filepath.Join(dir, "proc-meminfo")
	if err := os.WriteFile(primary, []byte("MemAvailable: 1000 kB\n"), 0o600); err != nil {
		t.Fatalf("write primary: %v", err)
	}
	if err := os.WriteFile(fallback, []byte("MemAvailable: 2000 kB\n"), 0o600); err != nil {
		t.Fatalf("write fallback: %v", err)
	}
	missing := filepath.Join(dir, "does-not-exist")

	t.Run("primary present", func(t *testing.T) {
		f, err := openMeminfo(primary, fallback)
		if err != nil {
			t.Fatalf("openMeminfo: %v", err)
		}
		defer func() { _ = f.Close() }()
		if f.Name() != primary {
			t.Errorf("opened %q, want primary %q", f.Name(), primary)
		}
	})

	t.Run("primary missing falls back", func(t *testing.T) {
		f, err := openMeminfo(missing, fallback)
		if err != nil {
			t.Fatalf("openMeminfo: %v", err)
		}
		defer func() { _ = f.Close() }()
		if f.Name() != fallback {
			t.Errorf("opened %q, want fallback %q", f.Name(), fallback)
		}
	})

	t.Run("both missing errors", func(t *testing.T) {
		if _, err := openMeminfo(missing, filepath.Join(dir, "also-missing")); err == nil {
			t.Error("openMeminfo with both paths missing = nil error, want an error")
		}
	})
}

// TestParseMemAvailable covers normal /proc/meminfo shape, a missing
// MemAvailable line (error, never a guessed 0), and outright garbage.
func TestParseMemAvailable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{
			name: "normal",
			input: "MemTotal:        8000000 kB\n" +
				"MemFree:         1000000 kB\n" +
				"MemAvailable:    3145728 kB\n" +
				"Buffers:          200000 kB\n",
			want: 3072, // 3145728 kB / 1024
		},
		{
			name:    "missing line",
			input:   "MemTotal:        8000000 kB\nMemFree:         1000000 kB\n",
			wantErr: true,
		},
		{
			name:    "garbage value",
			input:   "MemAvailable:    notanumber kB\n",
			wantErr: true,
		},
		{
			name:    "empty",
			input:   "",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseMemAvailable(strings.NewReader(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseMemAvailable(%q) = %d, nil, want an error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMemAvailable(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("parseMemAvailable(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}
