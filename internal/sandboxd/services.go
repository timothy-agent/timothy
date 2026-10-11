package sandboxd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Service containers (D-159, issue #1206): long-lived helper processes
// sandboxd runs on brain's behalf, a stdio MCP server behind its bridge
// first. They are a second container class beside mission sandboxes:
// their own name prefix and label, no workspace mount, never counted
// against the mission cap or swept, and reachable from brain only over
// the internal services network, which sandboxd itself never joins, so
// a service cannot reach sandboxd's unauthenticated API.
const (
	serviceNamePrefix = "timothy-service-"
	serviceLabel      = "timothy.service"
	// serviceSpecLabel holds specHash, so an Ensure with a changed spec
	// replaces the container instead of reusing it.
	serviceSpecLabel = "timothy.service.spec"

	// ServicePort is where a service listens on the services network.
	ServicePort = 8090

	// servicesNetworkKey is the compose network services join; compose
	// names it "<project>_<key>", and the project is the owner id.
	servicesNetworkKey = "timothy-services"

	// serviceRestarts is how many times Docker restarts a crashed
	// service (its own backoff, doubling from 100ms) before leaving it
	// stopped until the next Ensure.
	serviceRestarts = 3

	serviceMemoryBytes = 1 << 30 // 1 GiB
	serviceNanoCPUs    = 1_000_000_000
	servicePidsLimit   = 128

	// serviceLogTail is how many log lines a stopped service reports.
	serviceLogTail = 20

	// Bounds on a service spec; values are never logged or echoed.
	serviceMaxArgs     = 64
	serviceMaxArgLen   = 4096
	serviceMaxEnv      = 64
	serviceMaxEnvValue = 8 << 10
)

// Service states as ServiceStatus reports them.
const (
	ServiceRunning    = "running"
	ServiceRestarting = "restarting"
	ServiceStopped    = "stopped"
)

// ErrServicesUnsupported is what a backend without service containers
// returns (Kubernetes, for now): fail closed rather than run elsewhere.
var ErrServicesUnsupported = errors.New("sandbox: service containers are not supported by this backend")

// ErrInvalidService marks a spec the API must answer 400 to.
var ErrInvalidService = errors.New("sandbox: invalid service spec")

// ServiceSpec is one service: the command run in the sandbox image, its
// environment, and whether it may reach the internet.
type ServiceSpec struct {
	ID       string            `json:"-"`
	Command  []string          `json:"command"`
	Env      map[string]string `json:"env,omitempty"`
	Internet bool              `json:"internet"`
}

// ServiceStatus is what Ensure and List report for one service.
type ServiceStatus struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	State    string `json:"state"`
	Restarts int    `json:"restarts"`
	// LastError and Tail explain a stopped service: the exit and the
	// last log lines.
	LastError string `json:"last_error,omitempty"`
	Tail      string `json:"tail,omitempty"`
}

var serviceEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks the spec's shape; a failure wraps ErrInvalidService
// and names a field, never a value.
func (s ServiceSpec) Validate() error {
	if !validMissionID(s.ID) {
		return fmt.Errorf("%w: id must be a uuid", ErrInvalidService)
	}
	if len(s.Command) == 0 || strings.TrimSpace(s.Command[0]) == "" {
		return fmt.Errorf("%w: command is required", ErrInvalidService)
	}
	if len(s.Command) > serviceMaxArgs {
		return fmt.Errorf("%w: command has more than %d arguments", ErrInvalidService, serviceMaxArgs)
	}
	for i, a := range s.Command {
		if len(a) > serviceMaxArgLen {
			return fmt.Errorf("%w: command argument %d is longer than %d bytes", ErrInvalidService, i, serviceMaxArgLen)
		}
	}
	if len(s.Env) > serviceMaxEnv {
		return fmt.Errorf("%w: more than %d env vars", ErrInvalidService, serviceMaxEnv)
	}
	for name, value := range s.Env {
		if !serviceEnvName.MatchString(name) {
			return fmt.Errorf("%w: env name %q is not a valid variable name", ErrInvalidService, name)
		}
		if name == "PATH" || name == "HOME" {
			return fmt.Errorf("%w: env %s is set by sandboxd", ErrInvalidService, name)
		}
		if len(value) > serviceMaxEnvValue {
			return fmt.Errorf("%w: env %s is longer than %d bytes", ErrInvalidService, name, serviceMaxEnvValue)
		}
	}
	return nil
}

// specHash fingerprints everything a running container was created
// from, values included; it sits in a label only Docker's operator
// can read, beside an env that reader can already see.
func (s ServiceSpec) specHash(image string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%t\x00", image, s.Internet)
	for _, a := range s.Command {
		_, _ = fmt.Fprintf(h, "%d:%s", len(a), a)
	}
	names := make([]string, 0, len(s.Env))
	for n := range s.Env {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		_, _ = fmt.Fprintf(h, "\x00%s=%d:%s", n, len(s.Env[n]), s.Env[n])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func serviceName(id string) string { return serviceNamePrefix + id }

func serviceAddress(id string) string {
	return "http://" + serviceName(id) + ":" + strconv.Itoa(ServicePort)
}

// EnsureService makes the service run with exactly spec: an existing
// container with the same spec is kept (restarting it if Docker gave
// up on it), any other is replaced.
func (m *Docker) EnsureService(ctx context.Context, spec ServiceSpec) (ServiceStatus, error) {
	if err := spec.Validate(); err != nil {
		return ServiceStatus{}, err
	}
	lock := m.missionLock(serviceName(spec.ID))
	lock.Lock()
	defer lock.Unlock()

	name := serviceName(spec.ID)
	hash := spec.specHash(m.baseImage)
	insp, err := m.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	switch {
	case err == nil:
		if err := m.checkOwner(containerLabels(insp)); err != nil {
			return ServiceStatus{}, err
		}
		if containerLabels(insp)[serviceSpecLabel] == hash {
			st := insp.Container.State
			if st != nil && (st.Running || st.Restarting) {
				return m.serviceStatus(ctx, spec.ID, insp), nil
			}
		}
		// Changed spec, or stopped after its restarts ran out: a fresh
		// container also resets Docker's restart count.
		if _, err := m.cli.ContainerRemove(ctx, insp.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return ServiceStatus{}, fmt.Errorf("sandbox: replace service %s: %w", name, err)
		}
	case !errdefs.IsNotFound(err):
		return ServiceStatus{}, fmt.Errorf("sandbox: inspect service %s: %w", name, err)
	}
	if err := m.createService(ctx, spec, name, hash); err != nil {
		return ServiceStatus{}, err
	}
	insp, err = m.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return ServiceStatus{}, fmt.Errorf("sandbox: inspect service %s: %w", name, err)
	}
	return m.serviceStatus(ctx, spec.ID, insp), nil
}

func (m *Docker) createService(ctx context.Context, spec ServiceSpec, name, hash string) error {
	env := []string{sandboxPath, "HOME=/home/sandbox"}
	names := make([]string, 0, len(spec.Env))
	for n := range spec.Env {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		env = append(env, n+"="+spec.Env[n])
	}
	memBytes := int64(serviceMemoryBytes)
	pids := int64(servicePidsLimit)
	initTrue := true
	cfg := &container.Config{
		Image:  m.baseImage,
		Env:    env,
		User:   sandboxUser,
		Cmd:    spec.Command,
		Labels: map[string]string{serviceLabel: spec.ID, serviceSpecLabel: hash, ownerLabel: m.owner},
	}
	hostCfg := &container.HostConfig{
		Init: &initTrue,
		Resources: container.Resources{
			Memory:     memBytes,
			MemorySwap: memBytes,
			NanoCPUs:   serviceNanoCPUs,
			PidsLimit:  &pids,
			Ulimits: []*container.Ulimit{
				{Name: "nofile", Soft: sandboxNofileLimit, Hard: sandboxNofileLimit},
				{Name: "core", Soft: sandboxCoreLimit, Hard: sandboxCoreLimit},
			},
		},
		OomScoreAdj:    sandboxOomScoreAdj,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		ReadonlyRootfs: true,
		Tmpfs: map[string]string{
			tmpMountPath:    "rw,exec,nosuid,nodev," + sandboxTmpfsSize,
			sandboxHomePath: "rw,exec,nosuid,nodev,uid=65534,gid=65534," + sandboxHomeTmpfsSize,
		},
		// Only the internal services network: brain reaches the service
		// by name there, and nothing on it routes to the internet or to
		// sandboxd. Internet egress is an explicit second network.
		NetworkMode:   container.NetworkMode(m.servicesNetwork),
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: serviceRestarts},
	}
	opts := client.ContainerCreateOptions{Config: cfg, HostConfig: hostCfg, Name: name}
	resp, err := m.cli.ContainerCreate(ctx, opts)
	if err != nil {
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("sandbox: create service %s: %w", name, err)
		}
		if err := m.pullImage(ctx, m.baseImage, spec.ID); err != nil {
			return err
		}
		if resp, err = m.cli.ContainerCreate(ctx, opts); err != nil {
			return fmt.Errorf("sandbox: create service %s: %w", name, err)
		}
	}
	if spec.Internet {
		if _, err := m.cli.NetworkConnect(ctx, "bridge", client.NetworkConnectOptions{Container: resp.ID}); err != nil {
			_, _ = m.cli.ContainerRemove(ctx, resp.ID, client.ContainerRemoveOptions{Force: true})
			return fmt.Errorf("sandbox: connect service %s to the internet: %w", name, err)
		}
	}
	if _, err := m.cli.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = m.cli.ContainerRemove(ctx, resp.ID, client.ContainerRemoveOptions{Force: true})
		return fmt.Errorf("sandbox: start service %s: %w", name, err)
	}
	return nil
}

// serviceStatus maps an inspect onto ServiceStatus; a stopped service
// carries its exit and log tail.
func (m *Docker) serviceStatus(ctx context.Context, id string, insp client.ContainerInspectResult) ServiceStatus {
	out := ServiceStatus{ID: id, Address: serviceAddress(id), State: ServiceStopped, Restarts: insp.Container.RestartCount}
	st := insp.Container.State
	switch {
	case st == nil:
	case st.Running:
		out.State = ServiceRunning
		return out
	case st.Restarting:
		out.State = ServiceRestarting
		return out
	default:
		out.LastError = fmt.Sprintf("exited with code %d", st.ExitCode)
		if st.Error != "" {
			out.LastError = st.Error
		}
	}
	out.Tail = m.serviceTail(ctx, insp.Container.ID)
	return out
}

// serviceTail reads a container's last log lines; best effort.
func (m *Docker) serviceTail(ctx context.Context, id string) string {
	logs, err := m.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(serviceLogTail)})
	if err != nil {
		return ""
	}
	defer func() { _ = logs.Close() }()
	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, logs); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

// RemoveService stops and deletes the service; a missing one, or one
// another instance owns, is not an error.
func (m *Docker) RemoveService(ctx context.Context, id string) error {
	if !validMissionID(id) {
		return fmt.Errorf("%w: id must be a uuid", ErrInvalidService)
	}
	name := serviceName(id)
	insp, err := m.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	switch {
	case errdefs.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("sandbox: inspect service %s: %w", name, err)
	case containerLabels(insp)[ownerLabel] != m.owner:
		return nil
	}
	if _, err := m.cli.ContainerRemove(ctx, insp.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("sandbox: remove service %s: %w", name, err)
	}
	return nil
}

// ListServices reports every service this instance owns.
func (m *Docker) ListServices(ctx context.Context) ([]ServiceStatus, error) {
	result, err := m.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", serviceLabel, ownerLabel+"="+m.owner),
	})
	if err != nil {
		return nil, fmt.Errorf("sandbox: list services: %w", err)
	}
	out := make([]ServiceStatus, 0, len(result.Items))
	for _, c := range result.Items {
		id := c.Labels[serviceLabel]
		if c.Labels[ownerLabel] != m.owner || id == "" {
			continue
		}
		insp, err := m.cli.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if err != nil {
			continue
		}
		out = append(out, m.serviceStatus(ctx, id, insp))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// EnsureService is not supported on Kubernetes yet.
func (k *Kubernetes) EnsureService(context.Context, ServiceSpec) (ServiceStatus, error) {
	return ServiceStatus{}, ErrServicesUnsupported
}

// RemoveService is not supported on Kubernetes yet.
func (k *Kubernetes) RemoveService(context.Context, string) error {
	return ErrServicesUnsupported
}

// ListServices is not supported on Kubernetes yet.
func (k *Kubernetes) ListServices(context.Context) ([]ServiceStatus, error) {
	return nil, ErrServicesUnsupported
}
