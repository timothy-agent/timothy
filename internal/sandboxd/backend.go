package sandboxd

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Backend is what the HTTP API and cmd/sandboxd need from a sandbox
// runtime: one sandbox per mission, created lazily on first exec and
// torn down on Remove. Two implementations exist, Docker and
// Kubernetes (D-153); brain never learns which one is in use because
// the HTTP contract in api.go is the same for both.
type Backend interface {
	// Name is the runtime's identity for logs and the /health check
	// key ("docker", "kubernetes").
	Name() string
	// Ping reports whether the runtime's control plane is reachable.
	Ping(ctx context.Context) error
	// CheckImage reports whether MISSION_SANDBOX_IMAGE is usable.
	CheckImage(ctx context.Context) error
	// ExecEnv runs command inside the mission's sandbox, streaming
	// combined output to out, and returns the exit code. Timeouts
	// return ErrTimeout.
	ExecEnv(ctx context.Context, missionID, workdir, command string, env map[string]string, timeout time.Duration, out io.Writer) (int, error)
	// Remove tears down the mission's sandbox; a missing sandbox is
	// not an error.
	Remove(ctx context.Context, missionID string) error
	// List returns the mission IDs that currently have a sandbox
	// owned by this instance.
	List(ctx context.Context) ([]string, error)
	// Capacity reports running sandboxes and memory headroom.
	Capacity(ctx context.Context) (CapacityReport, error)
}

// Backend names accepted by SANDBOXD_BACKEND.
const (
	BackendDocker     = "docker"
	BackendKubernetes = "kubernetes"
)

// ResolveBackend picks the backend name from SANDBOXD_BACKEND
// (explicit) and KUBERNETES_SERVICE_HOST (k8sHost, set by every
// kubelet). Explicit wins; empty falls back to kubernetes when running
// inside a cluster, else docker; anything else fails closed (D-153).
func ResolveBackend(explicit, k8sHost string) (string, error) {
	switch explicit {
	case BackendDocker, BackendKubernetes:
		return explicit, nil
	case "":
		if k8sHost != "" {
			return BackendKubernetes, nil
		}
		return BackendDocker, nil
	}
	return "", fmt.Errorf("sandbox: SANDBOXD_BACKEND=%q: want %q or %q", explicit, BackendDocker, BackendKubernetes)
}
