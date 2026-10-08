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
	"strconv"
	"strings"
	"testing"
	"time"
)

// Test databases through mise daemons (D-142) in the real sandbox image,
// same env as the prepare integration tests. The container runs with
// sandboxd's flags (internal/sandboxd/manager.go): uid 65534, 2 GiB
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
