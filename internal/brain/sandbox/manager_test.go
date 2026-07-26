package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	return &Manager{cli: cli, image: "img", locks: map[string]*sync.Mutex{}}
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
	id, err := mgr.ensureContainer(context.Background(), "m1")
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
	id, err := mgr.ensureContainer(context.Background(), "m1")
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
	id, err := mgr.ensureContainer(context.Background(), "m1")
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
	id, err := mgr.ensureContainer(context.Background(), "m1")
	if err != nil {
		t.Fatalf("ensureContainer: %v", err)
	}
	if id != "sibling1" {
		t.Fatalf("id = %q, want sibling1 (the container the racing sibling created)", id)
	}
}

func TestManagerDisabledReturnsErrDisabled(t *testing.T) {
	var mgr *Manager // simulates MISSION_SANDBOX_IMAGE unset
	ctx := context.Background()
	if err := mgr.Ping(ctx); err != ErrDisabled {
		t.Errorf("Ping on nil manager = %v, want ErrDisabled", err)
	}
	if err := mgr.CheckImage(ctx); err != ErrDisabled {
		t.Errorf("CheckImage on nil manager = %v, want ErrDisabled", err)
	}
	if _, err := mgr.Exec(ctx, "m1", "/workspace", "true", time.Second, &bytes.Buffer{}); err != ErrDisabled {
		t.Errorf("Exec on nil manager = %v, want ErrDisabled", err)
	}
	if err := mgr.Remove(ctx, "m1"); err != ErrDisabled {
		t.Errorf("Remove on nil manager = %v, want ErrDisabled", err)
	}
	if err := mgr.Sweep(ctx, func(string) bool { return true }); err != ErrDisabled {
		t.Errorf("Sweep on nil manager = %v, want ErrDisabled", err)
	}
}

func TestNewManagerEmptyImageDisables(t *testing.T) {
	mgr, err := NewManager(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("NewManager(\"\"): %v", err)
	}
	if mgr != nil {
		t.Fatalf("NewManager(\"\") = %v, want nil (disabled)", mgr)
	}
}
