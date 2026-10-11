//go:build integration

package missions

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Test databases through mise daemons (D-142) in the real sandbox image,
// same env as the prepare integration tests. The container runs with
// sandboxd's flags (internal/sandboxd/docker.go): uid 65534, 2 GiB
// memory, 2 CPUs, 256 pids, nofile 4096, fsize 256 MiB, caps dropped,
// read-only rootfs, HOME and /tmp on tmpfs, package caches on the
// mission's own .sandbox-cache.

// startSandboxContainer runs a long-lived mission container over ws and
// returns its name; the caller removes it.
func startSandboxContainer(t *testing.T, image, ws string) string {
	t.Helper()
	cache := filepath.Join(ws, ".sandbox-cache")
	if err := os.MkdirAll(cache, 0o777); err != nil { //nolint:gosec // the sandbox uid must write here
		t.Fatal(err)
	}
	if err := os.Chmod(cache, 0o777); err != nil { //nolint:gosec // see above
		t.Fatal(err)
	}
	name := "it-daemons-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	args := []string{"run", "-d", "--name", name, "--init", "--user", "65534:65534",
		"-e", "HOME=/home/sandbox", "-e", "MISE_TRUSTED_CONFIG_PATHS=" + ws,
		"--memory", "2g", "--memory-swap", "2g", "--memory-reservation", "256m", "--cpus", "2", "--pids-limit", "256",
		"--ulimit", "nofile=4096:4096", "--ulimit", "fsize=268435456:268435456", "--ulimit", "core=0:0",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=512m",
		"--tmpfs", "/home/sandbox:rw,exec,nosuid,nodev,uid=65534,gid=65534,size=1g",
		"-v", ws + ":" + ws, "-v", cache + ":/home/sandbox/.cache",
		image, "sleep", "infinity"}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil { //nolint:gosec // integration test over the sandbox image
		t.Fatalf("docker run: %v: %s", err, out)
	}
	return name
}

// dockerExecIn is a sandboxExec into a running container, under
// coreutils timeout as sandboxd execs it.
func dockerExecIn(container string) sandboxExec {
	return func(ctx context.Context, _, workdir, command string, timeout time.Duration, out io.Writer) (int, error) {
		secs := strconv.Itoa(int(timeout / time.Second))
		cmd := exec.CommandContext(ctx, "docker", "exec", "-w", workdir, container, //nolint:gosec // integration test over the sandbox image
			"timeout", "-k", "5", secs, "/bin/sh", "-c", command)
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode(), nil
			}
			return 0, err
		}
		return 0, nil
	}
}

// containerIP is the container's address on the default bridge.
func containerIP(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name).Output() //nolint:gosec // test-owned container
	ip := strings.TrimSpace(string(out))
	if err != nil || ip == "" {
		t.Fatalf("inspect %s: %v %q", name, err, out)
	}
	return ip
}

// reachableFromPeer reports whether a second container on the same
// bridge network opens a TCP connection to ip:port.
func reachableFromPeer(image, ip string, port int) bool {
	return exec.Command("docker", "run", "--rm", "--user", "65534:65534", image, //nolint:gosec // integration test over the sandbox image
		"bash", "-c", `timeout 3 bash -c 'echo > "/dev/tcp/$0/$1"' "$0" "$1"`, ip, strconv.Itoa(port)).Run() == nil
}

// TestPrepareSandboxDaemons: a repo whose CI needs PostgreSQL and Redis
// gets both started by prepare as uid 65534 under the sandbox limits;
// both accept a connection through the exported variables, the test
// task starts them again after a stop, and removing the container
// leaves no database data on disk.
func TestPrepareSandboxDaemons(t *testing.T) {
	image := os.Getenv("PREPARE_SANDBOX_IMAGE")
	if image == "" {
		t.Skip("PREPARE_SANDBOX_IMAGE not set")
	}
	m := prepareFixture(t, map[string]string{
		".github/workflows/ci.yml": "jobs:\n  test:\n    services:\n      postgres:\n        image: postgres:17\n      redis:\n        image: redis:8\n",
		"Makefile":                 "test:\n\tpsql -tAc 'select 1' && redis-cli -u \"$$REDIS_URL\" ping\n",
	})
	container := startSandboxContainer(t, image, m.Workspace)
	removed := false
	remove := func() {
		if !removed {
			_ = exec.Command("docker", "rm", "-f", container).Run() //nolint:gosec // test-owned container
			removed = true
		}
	}
	t.Cleanup(remove)
	run := dockerExecIn(container)
	wt := m.WorktreePath()
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: run}
	got := p.prepareWorkspace(context.Background(), m)
	f := got.EnvFacts.Prepare
	if f == nil {
		t.Fatal("no prepare facts")
	}
	t.Logf("prepare facts: %+v", f)
	t.Logf("facts block:%s", renderEnvFacts(got))
	if len(f.Failures) != 0 {
		t.Errorf("failures: %v", f.Failures)
	}
	if len(f.Services) != 2 || !f.Services[0].OK || !f.Services[1].OK || f.Services[0].Env["DATABASE_URL"] == "" || f.Services[1].Env["REDIS_URL"] == "" {
		t.Fatalf("Services = %+v", f.Services)
	}
	if f.TestCmd != "make test" || f.Tests == nil {
		t.Errorf("baseline = %q, want make test against both services", f.TestCmd)
	}
	for _, s := range f.Services {
		if len(s.Listen) == 0 || len(nonLoopback(s.Listen)) != 0 {
			t.Errorf("%s listens on %v, want loopback only", s.Name, s.Listen)
		}
	}
	ip := containerIP(t, container)
	for _, port := range []int{servicePort(f.Services[0]), servicePort(f.Services[1])} {
		if reachableFromPeer(image, ip, port) {
			t.Errorf("port %d reachable from a peer container at %s", port, ip)
		}
	}
	var out strings.Builder
	check := "mise exec -- sh -c 'psql -tAc \"select current_user\" && redis-cli -u \"$REDIS_URL\" ping && test \"$(id -u)\" = 65534 && test -f /tmp/timothy-daemons/postgres/PG_VERSION'"
	if code, err := run(context.Background(), m.ID, wt, check, time.Minute, &out); err != nil || code != 0 || !strings.Contains(out.String(), "postgres") || !strings.Contains(out.String(), "PONG") {
		t.Fatalf("connect: %d %v\n%s", code, err, out.String())
	}
	out.Reset()
	if code, err := run(context.Background(), m.ID, wt, "mise daemons stop postgres redis && mise run test", 3*time.Minute, &out); err != nil || code != 0 || !strings.Contains(out.String(), "PONG") {
		t.Fatalf("test task after stop: %d %v\n%s", code, err, out.String())
	}
	remove()
	_ = filepath.WalkDir(m.Workspace, func(p string, d fs.DirEntry, err error) error {
		if err == nil && (d.Name() == "PG_VERSION" || strings.HasPrefix(d.Name(), "appendonly.aof")) {
			t.Errorf("database data left on disk after removal: %s", p)
		}
		return nil
	})
}

// TestPrepareSandboxRepoDaemonExposed: the repo's own [daemons.redis]
// wins over the preset; when it binds 0.0.0.0 the harness records the
// observed address and the facts warn. The peer probe reaching it shows
// the probe used for the loopback test can see an exposed port.
func TestPrepareSandboxRepoDaemonExposed(t *testing.T) {
	image := os.Getenv("PREPARE_SANDBOX_IMAGE")
	if image == "" {
		t.Skip("PREPARE_SANDBOX_IMAGE not set")
	}
	m := prepareFixture(t, map[string]string{
		".github/workflows/ci.yml": "jobs:\n  test:\n    services:\n      redis:\n        image: redis:8\n",
		"mise.toml":                "[tools]\nredis = \"8\"\n\n[daemons.redis]\nrun = \"exec redis-server --bind 0.0.0.0 --port 6390 --protected-mode no --save ''\"\nport = 6390\nready_port = 6390\n",
	})
	container := startSandboxContainer(t, image, m.Workspace)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() }) //nolint:gosec // test-owned container
	store := newFakeStore()
	store.missions[m.ID] = m
	p := &provisioner{store: store, log: slog.Default(), sandboxExec: dockerExecIn(container)}
	got := p.prepareWorkspace(context.Background(), m)
	f := got.EnvFacts.Prepare
	if f == nil || len(f.Services) != 1 || !f.Services[0].OK {
		t.Fatalf("prepare facts: %+v", f)
	}
	local, _ := os.ReadFile(filepath.Join(m.WorktreePath(), miseLocalFile)) //nolint:gosec // test-owned fixture path
	if strings.Contains(string(local), "[daemons.redis]") {
		t.Errorf("harness overrode the repo daemon:\n%s", local)
	}
	if !reflect.DeepEqual(f.Services[0].Listen, []string{"0.0.0.0"}) {
		t.Errorf("Listen = %v, want 0.0.0.0", f.Services[0].Listen)
	}
	block := renderEnvFacts(got)
	if !strings.Contains(block, "Warning: test service redis listens on 0.0.0.0") {
		t.Errorf("facts block lacks the exposure warning:\n%s", block)
	}
	if !reachableFromPeer(image, containerIP(t, container), 6390) {
		t.Error("exposed port not reachable from a peer; the loopback probe proves nothing")
	}
}
