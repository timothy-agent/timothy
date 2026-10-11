package sandboxd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
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

// testWorkdir is a mission workdir in the shape brain sends: an
// absolute path under the mission's own workspace directory, which
// missionMount (D-107) scopes the container's mount to.
const (
	testWorkdir    = "/workspace/missions/coding/m1/wt"
	testMissionDir = "/workspace/missions/coding/m1"
)

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// testOwner is newTestDocker's owner id (D-132).
const testOwner = "timothy-a"

func newTestDocker(cli *client.Client) *Docker {
	return &Docker{cli: cli, baseImage: "img", owner: testOwner, locks: map[string]*sync.Mutex{}}
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
	mgr := newTestDocker(cli)
	id, err := mgr.ensureContainer(context.Background(), "m1", testWorkdir)
	if err != nil {
		t.Fatalf("ensureContainer: %v", err)
	}
	if id != "c1" {
		t.Fatalf("id = %q, want c1", id)
	}
}

// TestEnsureContainerExitedRestarts confirms an Exited container is
// restarted in place (not recreated) — this is what keeps the mission
// on one container identity and recovers a container left stopped by a
// host reboot. Since D-106 the restart empties the HOME and /tmp tmpfs,
// so only the mounted volumes carry state across it.
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
	mgr := newTestDocker(cli)
	id, err := mgr.ensureContainer(context.Background(), "m1", testWorkdir)
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
			if cfg.Labels[ownerLabel] != testOwner || cfg.Labels[missionLabel] != "m1" {
				t.Errorf("create body: Labels = %v, want mission m1 and owner %s", cfg.Labels, testOwner)
			}
			if cfg.User != sandboxUser {
				t.Errorf("create body: User = %q, want %q", cfg.User, sandboxUser)
			}
			if len(cfg.HostConfig.Mounts) != 2 || cfg.HostConfig.Mounts[0].Target != testMissionDir || cfg.HostConfig.Mounts[1].Target != cachesMountPath {
				t.Errorf("create body: Mounts = %+v, want the mission dir at %s and its cache dir at %s", cfg.HostConfig.Mounts, testMissionDir, cachesMountPath)
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
	mgr := newTestDocker(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
	id, err := mgr.ensureContainer(context.Background(), "m1", testWorkdir)
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
	mgr := newTestDocker(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
	id, err := mgr.ensureContainer(context.Background(), "m1", testWorkdir)
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
	mgr := newTestDocker(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
	mgr.stateMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_executor-claude-state", Target: executorStateMountPath}

	if _, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", testWorkdir); err != nil {
		t.Fatalf("createContainer: %v", err)
	}
	if len(gotMounts) != 3 {
		t.Fatalf("create body: Mounts = %+v, want 3 (workspace + state + caches)", gotMounts)
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
	mgr := newTestDocker(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
	// mgr.stateMount left zero-value: not configured.

	if _, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", testWorkdir); err != nil {
		t.Fatalf("createContainer: %v", err)
	}
	if len(gotMounts) != 2 || gotMounts[0].Target != testMissionDir || gotMounts[1].Target != cachesMountPath {
		t.Fatalf("create body: Mounts = %+v, want exactly [mission workspace, mission cache dir]", gotMounts)
	}
}

// TestCreateContainerToolchainsMount covers D-125: the mise toolchain
// cache volume is replicated into mission containers when configured and
// omitted when not.
func TestCreateContainerToolchainsMount(t *testing.T) {
	tc := mount.Mount{Type: mount.TypeVolume, Source: "timothy_sandbox-toolchains", Target: toolchainsMountPath}
	tests := []struct {
		name string
		tm   mount.Mount
		want int
	}{
		{"configured", tc, 3},
		{"absent", mount.Mount{}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			mgr := newTestDocker(cli)
			mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
			mgr.toolchainsMount = tt.tm

			if _, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", testWorkdir); err != nil {
				t.Fatalf("createContainer: %v", err)
			}
			if len(gotMounts) != tt.want {
				t.Fatalf("Mounts = %+v, want %d", gotMounts, tt.want)
			}
			found := false
			for _, m := range gotMounts {
				if m.Target == toolchainsMountPath && m.Source == tc.Source {
					found = true
				}
			}
			if found != (tt.tm.Source != "") {
				t.Errorf("toolchains mount present = %v, Mounts = %+v", found, gotMounts)
			}
		})
	}
}

// TestResolveMountToolchainsVolume confirms resolveMount resolves the
// toolchains volume from toolchainsVolumeMetaPath (D-125).
func TestResolveMountToolchainsVolume(t *testing.T) {
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, container.InspectResponse{
			Mounts: []container.MountPoint{
				{Type: mount.TypeVolume, Name: "timothy_sandbox-toolchains", Destination: toolchainsVolumeMetaPath},
			},
		})
	})
	m, err := resolveMount(context.Background(), cli, toolchainsVolumeMetaPath, toolchainsMountPath)
	if err != nil {
		t.Fatalf("resolveMount: %v", err)
	}
	if m.Source != "timothy_sandbox-toolchains" || m.Target != toolchainsMountPath {
		t.Errorf("mount = %+v", m)
	}
}

// createMounts runs createContainer against a fake daemon and returns
// the Mounts it sent.
func createMounts(t *testing.T, mgr *Docker) []mount.Mount {
	t.Helper()
	var gotMounts []mount.Mount
	mgr.cli = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/create":
			var cfg struct {
				HostConfig container.HostConfig
			}
			if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			gotMounts = cfg.HostConfig.Mounts
			writeJSON(t, w, http.StatusCreated, container.CreateResponse{ID: "new1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/new1/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	if _, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", testWorkdir); err != nil {
		t.Fatalf("createContainer: %v", err)
	}
	return gotMounts
}

// TestCreateContainerCachesMount covers D-131: ~/.cache is the shared
// cache volume when configured, else the mission's own cache dir on the
// workspace volume (volume subpath or bind source), never the HOME tmpfs.
func TestCreateContainerCachesMount(t *testing.T) {
	shared := mount.Mount{Type: mount.TypeVolume, Source: "timothy_sandbox-caches", Target: cachesMountPath}
	tests := []struct {
		name      string
		workspace mount.Mount
		caches    mount.Mount
		want      mount.Mount
	}{
		{
			name:      "shared volume",
			workspace: mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath},
			caches:    shared,
			want:      shared,
		},
		{
			name:      "absent, workspace volume",
			workspace: mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath},
			want: mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: cachesMountPath,
				VolumeOptions: &mount.VolumeOptions{Subpath: "missions/coding/m1/.sandbox-cache"}},
		},
		{
			name:      "absent, workspace bind",
			workspace: mount.Mount{Type: mount.TypeBind, Source: "/srv/timothy/workspace", Target: workspaceMountPath},
			want:      mount.Mount{Type: mount.TypeBind, Source: "/srv/timothy/workspace/missions/coding/m1/.sandbox-cache", Target: cachesMountPath},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newTestDocker(nil)
			mgr.workspaceMount = tt.workspace
			mgr.cachesMount = tt.caches
			var got []mount.Mount
			for _, m := range createMounts(t, mgr) {
				if m.Target == cachesMountPath {
					got = append(got, m)
				}
			}
			if len(got) != 1 {
				t.Fatalf("mounts at %s = %+v, want exactly one", cachesMountPath, got)
			}
			g := got[0]
			if g.Type != tt.want.Type || g.Source != tt.want.Source {
				t.Errorf("cache mount = %+v, want %+v", g, tt.want)
			}
			wantSub, gotSub := "", ""
			if tt.want.VolumeOptions != nil {
				wantSub = tt.want.VolumeOptions.Subpath
			}
			if g.VolumeOptions != nil {
				gotSub = g.VolumeOptions.Subpath
			}
			if gotSub != wantSub {
				t.Errorf("cache mount subpath = %q, want %q", gotSub, wantSub)
			}
		})
	}
}

// TestResolveMountCachesVolume confirms resolveMount resolves the cache
// volume from cachesVolumeMetaPath (D-131).
func TestResolveMountCachesVolume(t *testing.T) {
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, container.InspectResponse{
			Mounts: []container.MountPoint{
				{Type: mount.TypeVolume, Name: "timothy_sandbox-caches", Destination: cachesVolumeMetaPath},
			},
		})
	})
	m, err := resolveMount(context.Background(), cli, cachesVolumeMetaPath, cachesMountPath)
	if err != nil {
		t.Fatalf("resolveMount: %v", err)
	}
	if m.Source != "timothy_sandbox-caches" || m.Target != cachesMountPath {
		t.Errorf("mount = %+v", m)
	}
}

// TestBaseImagePointsCachesAtCacheMount confirms every package cache
// env in the base image (D-131) sits under cachesMountPath, so caches
// follow the disk-backed mount instead of the HOME tmpfs.
func TestBaseImagePointsCachesAtCacheMount(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "sandbox.Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	df := strings.Join(strings.Fields(strings.ReplaceAll(string(raw), "\\\n", " ")), " ")
	for _, kv := range []string{
		"COMPOSER_CACHE_DIR=" + cachesMountPath + "/composer",
		"npm_config_cache=" + cachesMountPath + "/npm",
		"PIP_CACHE_DIR=" + cachesMountPath + "/pip",
		"UV_CACHE_DIR=" + cachesMountPath + "/uv",
		"GOMODCACHE=" + cachesMountPath + "/go-mod",
		"GOCACHE=" + cachesMountPath + "/go-build",
		"GOFLAGS=-modcacherw",
		"MAVEN_OPTS=-Dmaven.repo.local=" + cachesMountPath + "/m2/repository",
		"GRADLE_USER_HOME=" + cachesMountPath + "/gradle",
		"YARN_CACHE_FOLDER=" + cachesMountPath + "/yarn",
		"BUN_INSTALL_CACHE_DIR=" + cachesMountPath + "/bun",
		"MISE_CACHE_DIR=" + cachesMountPath + "/mise",
		"MISE_STATE_DIR=" + cachesMountPath + "/mise-state",
	} {
		if !strings.Contains(df, " "+kv+" ") {
			t.Errorf("sandbox.Dockerfile ENV is missing %s", kv)
		}
	}
}

func TestNewManagerEmptyImageErrors(t *testing.T) {
	mgr, err := NewDocker(context.Background(), "", nil)
	if err == nil {
		t.Fatal("NewDocker(\"\") = nil error, want an error (sandbox is mandatory)")
	}
	if mgr != nil {
		t.Fatalf("NewDocker(\"\") = %v, want nil manager alongside the error", mgr)
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
	mgr := newTestDocker(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}

	id, err := mgr.ensureContainer(context.Background(), "m1", testWorkdir)
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
	mgr := newTestDocker(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}

	_, err := mgr.ensureContainer(context.Background(), "m1", testWorkdir)
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
	mgr := newTestDocker(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", testWorkdir)
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
	mgr := newTestDocker(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}

	if _, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", testWorkdir); err != nil {
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

// TestCreateContainerHardensRootfs covers D-106: the rootfs is read-only
// with tmpfs standing in for the writable layer at the two paths mission
// workloads actually write outside their mounts, ulimits are set, and no
// SecurityOpt entry ever turns seccomp (or anything else) unconfined,
// since Docker's default seccomp profile applies precisely by NOT being
// overridden.
func TestCreateContainerHardensRootfs(t *testing.T) {
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
	mgr := newTestDocker(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}

	if _, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", testWorkdir); err != nil {
		t.Fatalf("createContainer: %v", err)
	}

	if !gotHostConfig.ReadonlyRootfs {
		t.Errorf("ReadonlyRootfs = false, want true")
	}

	// Each tmpfs must be writable by the sandbox uid and must say exec
	// explicitly, since Docker defaults tmpfs to noexec: `go test` runs
	// its binary from /tmp and HOME holds `npm install -g` binaries.
	tmpfsCases := []struct {
		path      string
		wantOpts  []string
		wantSized string
	}{
		{path: tmpMountPath, wantOpts: []string{"rw", "exec", "nosuid", "nodev"}, wantSized: sandboxTmpfsSize},
		{path: sandboxHomePath, wantOpts: []string{"rw", "exec", "nosuid", "nodev", "uid=65534", "gid=65534"}, wantSized: sandboxHomeTmpfsSize},
	}
	for _, tc := range tmpfsCases {
		opts, ok := gotHostConfig.Tmpfs[tc.path]
		if !ok {
			t.Errorf("Tmpfs has no entry for %s, want one (rootfs is read-only)", tc.path)
			continue
		}
		for _, want := range tc.wantOpts {
			if !slices.Contains(strings.Split(opts, ","), want) {
				t.Errorf("Tmpfs[%s] = %q, want option %q", tc.path, opts, want)
			}
		}
		if !strings.Contains(opts, tc.wantSized) {
			t.Errorf("Tmpfs[%s] = %q, want size option %q", tc.path, opts, tc.wantSized)
		}
		if slices.Contains(strings.Split(opts, ","), "noexec") {
			t.Errorf("Tmpfs[%s] = %q, must not be noexec", tc.path, opts)
		}
	}

	for _, p := range []string{executorStateMountPath, toolchainsMountPath, cachesMountPath} {
		if filepath.Dir(p) != sandboxHomePath {
			t.Errorf("volume mount %q is not a direct child of %q; Docker would create its parents root-owned on the HOME tmpfs", p, sandboxHomePath)
		}
	}

	// The executor state volume mounts under the HOME tmpfs; Docker
	// layers the volume over it, but only while the paths still nest.
	if !strings.HasPrefix(executorStateMountPath, sandboxHomePath+"/") {
		t.Errorf("executorStateMountPath %q no longer nests under %q; the HOME tmpfs would orphan the state volume", executorStateMountPath, sandboxHomePath)
	}
	if len(gotHostConfig.Tmpfs) != len(tmpfsCases) {
		t.Errorf("Tmpfs = %v, want exactly %d entries", gotHostConfig.Tmpfs, len(tmpfsCases))
	}

	wantUlimits := map[string]int64{
		"nofile": sandboxNofileLimit,
		"fsize":  sandboxFsizeLimit,
		"core":   sandboxCoreLimit,
	}
	gotUlimits := map[string]int64{}
	for _, u := range gotHostConfig.Ulimits {
		if u.Soft != u.Hard {
			t.Errorf("Ulimit %s soft %d != hard %d, want both pinned", u.Name, u.Soft, u.Hard)
		}
		gotUlimits[u.Name] = u.Soft
	}
	for name, want := range wantUlimits {
		got, ok := gotUlimits[name]
		if !ok {
			t.Errorf("Ulimits has no %s entry, want %d", name, want)
			continue
		}
		if got != want {
			t.Errorf("Ulimit %s = %d, want %d", name, got, want)
		}
	}

	// Omitting "seccomp=" IS the pin on Docker's default profile; an
	// explicit unconfined of any kind would silently undo it.
	for _, opt := range gotHostConfig.SecurityOpt {
		if strings.Contains(opt, "unconfined") {
			t.Errorf("SecurityOpt contains %q; mission containers must never run unconfined", opt)
		}
		if strings.HasPrefix(opt, "seccomp=") {
			t.Errorf("SecurityOpt sets %q; the default seccomp profile is pinned by omission, not by overriding it", opt)
		}
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
	mgr := newTestDocker(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}

	if _, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", testWorkdir); err != nil {
		t.Fatalf("createContainer: %v", err)
	}
	if !slices.Contains(gotEnv, sandboxPath) {
		t.Fatalf("Env = %v, want to contain %q", gotEnv, sandboxPath)
	}
	wantPrefix := "PATH=/home/sandbox/.mise/shims:/home/sandbox/.local/bin:/home/sandbox/.npm-global/bin:/home/sandbox/go/bin:"
	if !strings.HasPrefix(sandboxPath, wantPrefix) {
		t.Errorf("sandboxPath = %q, want prefix %q", sandboxPath, wantPrefix)
	}
	if len(gotEnv) != 2 {
		t.Errorf("Env = %v, want only PATH and HOME (the rest comes from the image ENV)", gotEnv)
	}
}

// TestBaseImageSetsNonInteractiveEnv confirms the base image carries the
// non-interactive, wide-output ENV (issue #1009) that createContainer's
// PATH/HOME-only Env merges with, that mise never auto-installs, and
// that mise reads the repo's idiomatic version files (D-139).
func TestBaseImageSetsNonInteractiveEnv(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "sandbox.Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	df := strings.Join(strings.Fields(strings.ReplaceAll(string(raw), "\\\n", " ")), " ")
	for _, kv := range []string{
		"COLUMNS=200", "TERM=dumb", "CI=1", "NO_COLOR=1", "FORCE_COLOR=0", "LANG=C.UTF-8",
		"GIT_TERMINAL_PROMPT=0", "COMPOSER_NO_INTERACTION=1", "PIP_NO_INPUT=1",
		"PIP_DISABLE_PIP_VERSION_CHECK=1", "PYTHONUNBUFFERED=1", "NPM_CONFIG_FUND=false",
		"NPM_CONFIG_UPDATE_NOTIFIER=false", "MISE_AUTO_INSTALL=false",
		"MISE_EXEC_AUTO_INSTALL=false", "MISE_NOT_FOUND_AUTO_INSTALL=false",
		"MISE_IDIOMATIC_VERSION_FILE_ENABLE_TOOLS=node,python,go,ruby,java,rust",
	} {
		if !strings.Contains(df, " "+kv) {
			t.Errorf("sandbox.Dockerfile ENV is missing %s", kv)
		}
	}
	if strings.Contains(df, "MISE_GITHUB_TOKEN") {
		t.Error("MISE_GITHUB_TOKEN must never be baked into the image")
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
	mgr := newTestDocker(cli)

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

// TestMissionWorkspaceDir covers D-107's derivation: only a workdir
// under /workspace/missions/<kind>/<mission id> resolves, and the
// mission id must be the path's own — a workdir naming a different
// mission is rejected, never widened to the workspace root.
func TestMissionWorkspaceDir(t *testing.T) {
	const other = "b2c3d4e5-0000-0000-0000-000000000002"
	tests := []struct {
		name      string
		workdir   string
		missionID string
		want      string
		wantErr   bool
	}{
		{name: "worktree under mission dir", workdir: "/workspace/missions/coding/m1/wt", missionID: "m1", want: "/workspace/missions/coding/m1"},
		{name: "mission dir itself", workdir: "/workspace/missions/general/m1", missionID: "m1", want: "/workspace/missions/general/m1"},
		{name: "deep run dir", workdir: "/workspace/missions/coding/m1/runs/r1/refs", missionID: "m1", want: "/workspace/missions/coding/m1"},
		{name: "another mission's dir", workdir: "/workspace/missions/coding/" + other + "/wt", missionID: "m1", wantErr: true},
		{name: "workspace root", workdir: "/workspace", missionID: "m1", wantErr: true},
		{name: "missions root", workdir: "/workspace/missions", missionID: "m1", wantErr: true},
		{name: "kind dir only", workdir: "/workspace/missions/coding", missionID: "m1", wantErr: true},
		{name: "mission id as the kind segment", workdir: "/workspace/missions/m1", missionID: "m1", wantErr: true},
		{name: "outside the workspace", workdir: "/etc", missionID: "m1", wantErr: true},
		// Traversal must fail in THIS function, not only in api.go's
		// validWorkdir: the mount gate has to hold standalone (D-116).
		{name: "traversal out of the mission dir", workdir: "/workspace/missions/coding/m1/../" + other, missionID: "m1", wantErr: true},
		{name: "traversal to the workspace root", workdir: "/workspace/missions/coding/m1/../..", missionID: "m1", wantErr: true},
		{name: "traversal back into the same mission", workdir: "/workspace/missions/coding/m1/wt/../wt", missionID: "m1", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := missionWorkspaceDir(tc.workdir, tc.missionID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("missionWorkspaceDir(%q, %q) = %q, want error", tc.workdir, tc.missionID, got)
				}
				if !errors.Is(err, ErrWorkspaceScope) {
					t.Fatalf("error = %v, want ErrWorkspaceScope", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("missionWorkspaceDir: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMissionMountScopesPerMission is the isolation claim in D-107:
// two missions sharing one workspace volume get two DIFFERENT mount
// specs, each narrowed by Subpath to its own directory, so neither
// container has a mount covering the other's files.
func TestMissionMountScopesPerMission(t *testing.T) {
	mgr := &Docker{workspaceMount: mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}}

	a, err := mgr.missionMount("/workspace/missions/coding/m1/wt", "m1")
	if err != nil {
		t.Fatalf("missionMount m1: %v", err)
	}
	b, err := mgr.missionMount("/workspace/missions/general/m2", "m2")
	if err != nil {
		t.Fatalf("missionMount m2: %v", err)
	}
	if a.VolumeOptions == nil || b.VolumeOptions == nil {
		t.Fatalf("mounts carry no VolumeOptions: a=%+v b=%+v", a, b)
	}
	if a.VolumeOptions.Subpath != "missions/coding/m1" {
		t.Errorf("m1 subpath = %q, want missions/coding/m1", a.VolumeOptions.Subpath)
	}
	if b.VolumeOptions.Subpath != "missions/general/m2" {
		t.Errorf("m2 subpath = %q, want missions/general/m2", b.VolumeOptions.Subpath)
	}
	if a.VolumeOptions.Subpath == b.VolumeOptions.Subpath {
		t.Fatal("both missions resolved to the same subpath: no isolation")
	}
	if a.Target != "/workspace/missions/coding/m1" || b.Target != "/workspace/missions/general/m2" {
		t.Fatalf("targets = %q / %q, want each mission's own dir (paths must match what brain records)", a.Target, b.Target)
	}
	if a.Target == workspaceMountPath || b.Target == workspaceMountPath {
		t.Fatal("a mission container still has a mount at the shared workspace root")
	}
}

// TestMissionMountBindSource covers the bind-backed deployment (an
// operator running a host bind instead of a named volume): the scoping
// moves to the source path, since a bind has no Subpath option.
func TestMissionMountBindSource(t *testing.T) {
	mgr := &Docker{workspaceMount: mount.Mount{Type: mount.TypeBind, Source: "/srv/timothy/workspace", Target: workspaceMountPath}}
	got, err := mgr.missionMount("/workspace/missions/coding/m1/wt", "m1")
	if err != nil {
		t.Fatalf("missionMount: %v", err)
	}
	if got.Source != "/srv/timothy/workspace/missions/coding/m1" {
		t.Errorf("source = %q, want the mission's own host subdirectory", got.Source)
	}
	if got.VolumeOptions != nil {
		t.Errorf("bind mount carries VolumeOptions = %+v, want nil", got.VolumeOptions)
	}
}

// TestCreateContainerRejectsUnscopedWorkdir confirms a workdir that
// resolves to no mission directory fails the create outright rather
// than falling back to the shared workspace root (D-107).
func TestCreateContainerRejectsUnscopedWorkdir(t *testing.T) {
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected docker call: %s %s", r.Method, r.URL.Path)
	})
	mgr := newTestDocker(cli)
	mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
	_, err := mgr.createContainer(context.Background(), "m1", "timothy-sandbox-m1", workspaceMountPath)
	if !errors.Is(err, ErrWorkspaceScope) {
		t.Fatalf("err = %v, want ErrWorkspaceScope", err)
	}
}

// TestEnsureContainerRejectsUnscopedWorkdirOnEveryPath pins D-116: the
// scope check gates reuse and restart-in-place too, not just create.
// Both branches hand workdir straight to Docker's WorkingDir, and they
// carry every exec after a mission's first, so a create-only check
// would leave the invariant unenforced where it matters most. Each
// case fails before any daemon call: an unscoped workdir must never
// reach Docker at all.
func TestEnsureContainerRejectsUnscopedWorkdirOnEveryPath(t *testing.T) {
	const other = "b2c3d4e5-0000-0000-0000-000000000002"
	tests := []struct {
		name    string
		workdir string
	}{
		{name: "workspace root", workdir: workspaceMountPath},
		{name: "missions root", workdir: "/workspace/missions"},
		{name: "another mission's workdir", workdir: "/workspace/missions/coding/" + other + "/wt"},
		{name: "traversal workdir", workdir: "/workspace/missions/coding/m1/../" + other},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("unexpected docker call for a workdir that must be rejected before inspect: %s %s", r.Method, r.URL.Path)
			})
			mgr := newTestDocker(cli)
			mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
			if _, err := mgr.ensureContainer(context.Background(), "m1", tc.workdir); !errors.Is(err, ErrWorkspaceScope) {
				t.Fatalf("err = %v, want ErrWorkspaceScope", err)
			}
		})
	}
}

// TestListFiltersOnOwner pins D-132: List asks the daemon for this
// instance's owner label and drops anything else the daemon returns.
func TestListFiltersOnOwner(t *testing.T) {
	var gotFilters map[string]map[string]bool
	cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &gotFilters); err != nil {
			t.Fatalf("decode filters: %v", err)
		}
		writeJSON(t, w, http.StatusOK, []container.Summary{
			{ID: "c1", Labels: map[string]string{missionLabel: "mine", ownerLabel: testOwner}},
			{ID: "c2", Labels: map[string]string{missionLabel: "theirs", ownerLabel: "timothy-b"}},
			{ID: "c3", Labels: map[string]string{missionLabel: "legacy"}},
		})
	})
	ids, err := newTestDocker(cli).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 1 || ids[0] != "mine" {
		t.Errorf("List = %v, want [mine]", ids)
	}
	if want := map[string]bool{missionLabel: true, ownerLabel + "=" + testOwner: true}; !maps.Equal(gotFilters["label"], want) {
		t.Errorf("label filters = %v, want %v", gotFilters["label"], want)
	}
}

// TestRemoveOnlyOwnContainer pins D-132: Remove deletes a container
// only when it carries this instance's owner label. Another instance's
// container and a pre-D-132 unlabelled one are left alone, without
// error, so brain's sweep never kills a sibling instance's mission.
func TestRemoveOnlyOwnContainer(t *testing.T) {
	tests := []struct {
		name       string
		labels     map[string]string // nil: no container
		wantRemove bool
	}{
		{name: "own container", labels: map[string]string{missionLabel: "m1", ownerLabel: testOwner}, wantRemove: true},
		{name: "another instance's container", labels: map[string]string{missionLabel: "m1", ownerLabel: "timothy-b"}},
		{name: "unlabelled legacy container", labels: map[string]string{missionLabel: "m1"}},
		{name: "no container"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			containers := map[string]map[string]string{}
			if tc.labels != nil {
				containers[containerName("m1")] = tc.labels
			}
			d := newFakeDaemon(containers)
			mgr := newTestDocker(newTestClient(t, d.handle))
			if err := mgr.Remove(context.Background(), "m1"); err != nil {
				t.Fatalf("Remove: %v", err)
			}
			removed := len(d.removedNames()) == 1
			if removed != tc.wantRemove {
				t.Errorf("removed = %v, want %v", removed, tc.wantRemove)
			}
		})
	}
}

// TestEnsureContainerRefusesForeignContainer pins D-132: an existing
// container owned by another instance is never reused, restarted or
// started after a create conflict; ensureContainer errors instead.
// An unlabelled pre-D-132 container stays reusable for its mission.
func TestEnsureContainerRefusesForeignContainer(t *testing.T) {
	foreign := &container.Config{Labels: map[string]string{missionLabel: "m1", ownerLabel: "timothy-b"}}
	tests := []struct {
		name        string
		config      *container.Config
		running     bool
		conflict    bool // first inspect 404, create 409, then inspect finds it
		wantForeign bool
	}{
		{name: "foreign running", config: foreign, running: true, wantForeign: true},
		{name: "foreign exited", config: foreign, wantForeign: true},
		{name: "foreign after create conflict", config: foreign, conflict: true, wantForeign: true},
		{name: "own running", config: &container.Config{Labels: map[string]string{missionLabel: "m1", ownerLabel: testOwner}}, running: true},
		{name: "legacy unlabelled running", config: &container.Config{Labels: map[string]string{missionLabel: "m1"}}, running: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inspects := 0
			cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1.51/containers/timothy-sandbox-m1/json":
					inspects++
					if tc.conflict && inspects == 1 {
						http.Error(w, "no such container", http.StatusNotFound)
						return
					}
					writeJSON(t, w, http.StatusOK, container.InspectResponse{
						ID: "c1", Config: tc.config, State: &container.State{Running: tc.running},
					})
				case r.Method == http.MethodPost && r.URL.Path == "/v1.51/containers/create" && tc.conflict:
					http.Error(w, "already exists", http.StatusConflict)
				default:
					t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
				}
			})
			mgr := newTestDocker(cli)
			mgr.workspaceMount = mount.Mount{Type: mount.TypeVolume, Source: "timothy_workspace", Target: workspaceMountPath}
			id, err := mgr.ensureContainer(context.Background(), "m1", testWorkdir)
			if tc.wantForeign {
				if !errors.Is(err, ErrForeignContainer) {
					t.Fatalf("err = %v, want ErrForeignContainer", err)
				}
				return
			}
			if err != nil || id != "c1" {
				t.Fatalf("ensureContainer = %q, %v; want c1, nil", id, err)
			}
		})
	}
}

// TestResolveOwner pins D-132's owner id: sandboxd's own compose
// project label, else defaultOwner.
func TestResolveOwner(t *testing.T) {
	tests := []struct {
		name   string
		status int
		labels map[string]string
		want   string
	}{
		{name: "compose project label", status: http.StatusOK, labels: map[string]string{composeProjectLabel: "timothy-demo1"}, want: "timothy-demo1"},
		{name: "no compose label", status: http.StatusOK, labels: map[string]string{}, want: defaultOwner},
		{name: "self inspect fails", status: http.StatusNotFound, want: defaultOwner},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cli := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.status != http.StatusOK {
					http.Error(w, "no such container", tc.status)
					return
				}
				writeJSON(t, w, http.StatusOK, container.InspectResponse{ID: "self", Config: &container.Config{Labels: tc.labels}})
			})
			if got := resolveOwner(context.Background(), cli, testLog()); got != tc.want {
				t.Errorf("resolveOwner = %q, want %q", got, tc.want)
			}
		})
	}
}
