//go:build integration

package sandboxd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// newServiceDocker is a Docker backend for service tests against a real
// daemon: a unique owner and its own internal network standing in for
// compose's timothy-services. Requires MISSION_SANDBOX_TEST_IMAGE (an
// image with busybox sh, wget and nc: alpine works) and docker.sock.
func newServiceDocker(t *testing.T) *Docker {
	t.Helper()
	image := os.Getenv("MISSION_SANDBOX_TEST_IMAGE")
	if image == "" {
		t.Skip("MISSION_SANDBOX_TEST_IMAGE not set; skipping service integration test")
	}
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	owner := fmt.Sprintf("itest-svc-%d", time.Now().UnixNano())
	network := owner + "_" + servicesNetworkKey
	if _, err := cli.NetworkCreate(t.Context(), network, client.NetworkCreateOptions{Internal: true}); err != nil {
		t.Fatalf("create network: %v", err)
	}
	m := &Docker{cli: cli, baseImage: image, log: testLogger(), owner: owner, servicesNetwork: network, locks: map[string]*sync.Mutex{}}
	t.Cleanup(func() {
		ctx := context.Background()
		if list, err := m.ListServices(ctx); err == nil {
			for _, s := range list {
				_ = m.RemoveService(ctx, s.ID)
			}
		}
		_, _ = cli.NetworkRemove(ctx, network, client.NetworkRemoveOptions{})
	})
	return m
}

func itestServiceID(n int) string {
	return fmt.Sprintf("5e7c1ce0-0000-4000-8000-%012d", n)
}

// waitStopped polls until the service has stopped for good.
func waitStopped(t *testing.T, m *Docker, id string) ServiceStatus {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		list, err := m.ListServices(t.Context())
		if err != nil {
			t.Fatalf("ListServices: %v", err)
		}
		for _, s := range list {
			if s.ID == id && s.State == ServiceStopped {
				return s
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("service %s did not stop", id)
	return ServiceStatus{}
}

// runProbe runs command in a throwaway container on the services
// network and returns its exit code and output.
func runProbe(t *testing.T, m *Docker, command string) (int64, string) {
	t.Helper()
	ctx := t.Context()
	resp, err := m.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: m.baseImage, Cmd: []string{"sh", "-c", command}, Labels: map[string]string{ownerLabel: m.owner}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(m.servicesNetwork)},
	})
	if err != nil {
		t.Fatalf("create probe: %v", err)
	}
	t.Cleanup(func() {
		_, _ = m.cli.ContainerRemove(context.Background(), resp.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err := m.cli.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start probe: %v", err)
	}
	wait := m.cli.ContainerWait(ctx, resp.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	var code int64
	select {
	case r := <-wait.Result:
		code = r.StatusCode
	case err := <-wait.Error:
		t.Fatalf("wait probe: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("probe did not finish")
	}
	logs, err := m.cli.ContainerLogs(ctx, resp.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Fatalf("probe logs: %v", err)
	}
	defer func() { _ = logs.Close() }()
	var out bytes.Buffer
	_, _ = stdcopy.StdCopy(&out, &out, logs)
	return code, out.String()
}

// TestServiceLifecycle covers start, reach by name, keep on an equal
// spec, and remove.
func TestServiceLifecycle(t *testing.T) {
	m := newServiceDocker(t)
	id := itestServiceID(1)
	spec := ServiceSpec{ID: id, Command: []string{"sh", "-c", `while true; do printf "HTTP/1.0 200 OK\r\n\r\npong\n" | nc -l -p 8090; done`}}
	st, err := m.EnsureService(t.Context(), spec)
	if err != nil {
		t.Fatalf("EnsureService: %v", err)
	}
	if st.State != ServiceRunning {
		t.Fatalf("status = %+v", st)
	}

	addr := strings.TrimPrefix(st.Address, "http://")
	code, out := runProbe(t, m, "for i in 1 2 3 4 5; do wget -q -T 2 -O - http://"+addr+"/ && exit 0; sleep 1; done; exit 1")
	if code != 0 || !strings.Contains(out, "pong") {
		t.Fatalf("probe by name: code %d, output %q", code, out)
	}

	first, err := m.cli.ContainerInspect(t.Context(), serviceName(id), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsureService(t.Context(), spec); err != nil {
		t.Fatalf("EnsureService again: %v", err)
	}
	again, err := m.cli.ContainerInspect(t.Context(), serviceName(id), client.ContainerInspectOptions{})
	if err != nil || again.Container.ID != first.Container.ID {
		t.Fatalf("an equal spec must keep the container (%v)", err)
	}

	if err := m.RemoveService(t.Context(), id); err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	if list, err := m.ListServices(t.Context()); err != nil || len(list) != 0 {
		t.Fatalf("ListServices after remove = %+v, %v", list, err)
	}
}

// TestServiceCrashStopsAfterRestarts covers the restart policy: a
// crashing service is restarted, then left stopped with its exit and
// log tail, and the next Ensure starts it fresh.
func TestServiceCrashStopsAfterRestarts(t *testing.T) {
	m := newServiceDocker(t)
	id := itestServiceID(2)
	spec := ServiceSpec{ID: id, Command: []string{"sh", "-c", "echo boom >&2; exit 3"}}
	if _, err := m.EnsureService(t.Context(), spec); err != nil {
		t.Fatalf("EnsureService: %v", err)
	}
	st := waitStopped(t, m, id)
	if st.Restarts != serviceRestarts || st.LastError != "exited with code 3" || !strings.Contains(st.Tail, "boom") {
		t.Fatalf("stopped status = %+v", st)
	}
	dead, err := m.cli.ContainerInspect(t.Context(), serviceName(id), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsureService(t.Context(), spec); err != nil {
		t.Fatalf("EnsureService after stop: %v", err)
	}
	fresh, err := m.cli.ContainerInspect(t.Context(), serviceName(id), client.ContainerInspectOptions{})
	if err != nil || fresh.Container.ID == dead.Container.ID {
		t.Fatalf("a stopped service must be replaced by a fresh container, which resets its restart count (%v)", err)
	}
}

// TestServiceInternetIsOptIn covers the network split: without
// internet the service cannot reach out; with it, it can.
func TestServiceInternetIsOptIn(t *testing.T) {
	m := newServiceDocker(t)
	check := "if wget -q -T 5 -O /dev/null http://example.com/; then echo NET_OK; else echo NET_BLOCKED; fi"
	for i, tc := range []struct {
		internet bool
		want     string
	}{
		{internet: false, want: "NET_BLOCKED"},
		{internet: true, want: "NET_OK"},
	} {
		id := itestServiceID(10 + i)
		if _, err := m.EnsureService(t.Context(), ServiceSpec{ID: id, Command: []string{"sh", "-c", check}, Internet: tc.internet}); err != nil {
			t.Fatalf("EnsureService: %v", err)
		}
		st := waitStopped(t, m, id)
		if !strings.Contains(st.Tail, tc.want) {
			t.Fatalf("internet=%v: tail %q, want %s", tc.internet, st.Tail, tc.want)
		}
	}
}
