// Package sandboxd drives per-mission Docker containers that execute
// model-authored shell commands (mission worker/reviewer shell calls,
// verify_cmd) OUTSIDE brain's own process — so a command never inherits
// brain's environment (DATABASE_URL, TIMOTHY_MASTER_KEY, API tokens)
// or reaches brain's filesystem/binaries. Harness-authored git
// operations stay in brain; only model/plan-authored commands route
// here. The Docker socket lives only in this service — brain reaches
// it exclusively through sandboxclient's narrow HTTP API.
package sandboxd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// sandboxUser matches brain's own "nobody" (alpine runtime-shell stage)
// and the workspace volume's ownership — a sandbox container and brain
// write the shared volume as the same uid/gid.
const sandboxUser = "65534:65534"

const (
	workspaceMountPath  = "/workspace"
	containerNamePrefix = "timothy-sandbox-"
	missionLabel        = "timothy.mission"

	// stateVolumeMetaPath is where sandboxd itself mounts the executor
	// auth-state volume (deploy/docker-compose.yml) purely so it can
	// self-inspect and learn the volume's real name/spec — D-054,
	// mirroring how workspaceMountPath is resolved via resolveMount
	// against brain's own container. sandboxd never reads/writes
	// through this mount itself.
	stateVolumeMetaPath = "/statevols/claude"

	// executorStateMountPath is where the resolved state volume is
	// mounted, rw, in every mission container — the headless claude
	// CLI's subscription-auth state lives here, shared across a
	// mission's container restarts and across missions.
	executorStateMountPath = "/home/sandbox/.claude"

	// sandboxMemoryBytes / sandboxNanoCPUs / sandboxPidsLimit cap one
	// mission's blast radius: model-authored commands, unlike
	// harness-authored git ops, must never be able to exhaust the host.
	sandboxMemoryBytes = 2 << 30 // 2 GiB
	sandboxNanoCPUs    = 2_000_000_000
	sandboxPidsLimit   = 256

	// execGraceKill is how long `timeout` waits after SIGTERM before
	// SIGKILL — matches shell.go's cmd.WaitDelay intent (give a process a
	// moment to exit cleanly, then force it).
	execGraceKillSeconds = 5

	// timeoutExitCode is what GNU coreutils' timeout(1) exits with when
	// it had to kill the command — the same signal shell.go treats as a
	// timeout, not an ordinary non-zero exit.
	timeoutExitCode = 124

	// execPollInterval is how often Exec checks whether the exec process
	// has exited while its output stream is still open.
	execPollInterval = 200 * time.Millisecond

	// execStreamGrace is how long Exec keeps reading output after the
	// exec process itself has exited, before force-closing the attach —
	// the sandbox analog of shell.go's cmd.WaitDelay: a backgrounded
	// grandchild that inherited the shell's stdout keeps the stream open
	// past the exec's exit, and without this the call would hang until
	// that grandchild finishes.
	execStreamGrace = 2 * time.Second

	// execClientSlack pads the client-side deadline past the
	// in-container `timeout` enforcement (SIGTERM + kill grace) — the
	// backstop for a command the timeout binary cannot kill.
	execClientSlack = 35 * time.Second
)

// ErrTimeout wraps Exec's two timeout-return paths so the HTTP handler
// can classify a timeout (SSE event: error, code "timeout") separately
// from every other infrastructure failure via errors.Is, without
// string-matching the error text.
var ErrTimeout = errors.New("sandbox: command timed out")

// Manager creates, reuses, and tears down one Docker container per
// mission on the same Docker daemon brain itself runs under (driven via
// the mounted docker.sock). Safe for concurrent use.
type Manager struct {
	cli   *client.Client
	image string
	log   *slog.Logger

	// workspaceMount is resolved once at startup by inspecting brain's
	// OWN container for its /workspace mount and replicating that exact
	// spec — never hardcode a compose-prefixed volume name, since a
	// wrong name silently auto-creates an empty volume instead of
	// erroring (see NewManager).
	workspaceMount mount.Mount

	// stateMount is D-054's executor auth-state volume, resolved the
	// same self-inspection way as workspaceMount but optional: an
	// operator who never configured it (API-key-only auth) still gets
	// working mission containers, just without persistent executor
	// state. Zero value (Source == "") means absent.
	stateMount mount.Mount

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-mission ensureContainer lock
}

// NewManager connects to the Docker daemon and resolves the shared
// workspace mount by inspecting brain's own container — this is the
// only reliable way to learn the exact volume name (or bind source)
// brain is running under, since it may differ from any assumed
// compose-project prefix and a wrong name would silently create a
// fresh, empty volume instead of failing loudly.
func NewManager(ctx context.Context, image string, log *slog.Logger) (*Manager, error) {
	if image == "" {
		return nil, fmt.Errorf("sandbox: MISSION_SANDBOX_IMAGE not set")
	}
	// API-version negotiation is on by default in this client (unlike the
	// old github.com/docker/docker client, which required
	// WithAPIVersionNegotiation explicitly) — no negotiation option needed.
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("sandbox: connect to docker: %w", err)
	}
	wm, err := resolveWorkspaceMount(ctx, cli)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve workspace mount: %w", err)
	}
	// D-054: the state volume is optional — an operator running
	// API-key-only auth need not configure it. Absent is not an error;
	// createContainer simply omits the mount and mission containers keep
	// working without persistent executor state.
	sm, err := resolveMount(ctx, cli, stateVolumeMetaPath, executorStateMountPath)
	if err != nil {
		log.Info("sandbox: executor state volume not configured, mission containers will run without it", "error", err)
	}
	return &Manager{cli: cli, image: image, log: log, workspaceMount: wm, stateMount: sm, locks: map[string]*sync.Mutex{}}, nil
}

// resolveWorkspaceMount inspects the calling container (brain) for its
// /workspace mount and returns an equivalent mount.Mount for sandbox
// containers to use — same volume (or bind source), so paths recorded
// in the missions DB resolve identically on both sides with zero
// translation.
func resolveWorkspaceMount(ctx context.Context, cli *client.Client) (mount.Mount, error) {
	return resolveMount(ctx, cli, workspaceMountPath, workspaceMountPath)
}

// resolveMount inspects the calling container (sandboxd itself) for a
// mount at selfDestination and returns an equivalent mount.Mount
// targeting containerTarget for sandbox containers to use — same
// volume (or bind source), so it is never hardcoded to a
// compose-prefixed volume name (a wrong name would silently
// auto-create an empty volume instead of erroring).
func resolveMount(ctx context.Context, cli *client.Client, selfDestination, containerTarget string) (mount.Mount, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return mount.Mount{}, fmt.Errorf("read own hostname: %w", err)
	}
	self, err := cli.ContainerInspect(ctx, hostname, client.ContainerInspectOptions{})
	if err != nil {
		return mount.Mount{}, fmt.Errorf("inspect own container %s: %w", hostname, err)
	}
	for _, m := range self.Container.Mounts {
		if m.Destination != selfDestination {
			continue
		}
		if m.Type == mount.TypeBind {
			return mount.Mount{Type: mount.TypeBind, Source: m.Source, Target: containerTarget}, nil
		}
		return mount.Mount{Type: mount.TypeVolume, Source: m.Name, Target: containerTarget}, nil
	}
	return mount.Mount{}, fmt.Errorf("own container has no mount at %s", selfDestination)
}

// Ping reports whether the Docker daemon is reachable — the sandbox
// health check.
func (m *Manager) Ping(ctx context.Context) error {
	_, err := m.cli.Ping(ctx, client.PingOptions{})
	return err
}

// CheckImage confirms the configured sandbox image actually exists
// locally — an operator who never ran `make sandbox-image` would
// otherwise see every mission shell call fail opaquely instead of a
// clear boot-time health signal.
func (m *Manager) CheckImage(ctx context.Context) error {
	_, err := m.cli.ImageInspect(ctx, m.image)
	return err
}

// containerName is deterministic per mission so ensureContainer's
// get-or-create is idempotent across brain restarts.
func containerName(missionID string) string {
	return containerNamePrefix + missionID
}

// missionLock returns (creating if needed) the mutex serializing
// ensureContainer for one mission — a single mission turn can issue
// several concurrent shell calls (loop.Agent runs up to
// maxParallelTools at once), and without this two calls racing a
// container's first-ever creation would both attempt ContainerCreate.
func (m *Manager) missionLock(missionID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[missionID]
	if !ok {
		l = &sync.Mutex{}
		m.locks[missionID] = l
	}
	return l
}

// ensureContainer returns a running container id for missionID,
// creating (or restarting an exited one) as needed. Three prior states
// are handled explicitly: absent (create+start), Created/Exited
// (restart in place — preserves any packages the worker installed
// earlier in the mission, and recovers a container left stopped by a
// host reboot or an OOM-killed PID 1), Running (reuse as-is).
func (m *Manager) ensureContainer(ctx context.Context, missionID string) (string, error) {
	lock := m.missionLock(missionID)
	lock.Lock()
	defer lock.Unlock()

	name := containerName(missionID)
	insp, err := m.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	switch {
	case err == nil:
		if insp.Container.State != nil && insp.Container.State.Running {
			return insp.Container.ID, nil
		}
		if _, startErr := m.cli.ContainerStart(ctx, insp.Container.ID, client.ContainerStartOptions{}); startErr != nil {
			return "", fmt.Errorf("sandbox: restart container %s: %w", name, startErr)
		}
		return insp.Container.ID, nil
	case errdefs.IsNotFound(err):
		id, createErr := m.createContainer(ctx, missionID, name)
		if createErr == nil {
			return id, nil
		}
		if !errdefs.IsConflict(createErr) {
			return "", createErr
		}
		// Lost a race with a sibling call's create despite the mission
		// lock (e.g. a stale container from a crashed prior process) —
		// re-inspect and use whatever is there now rather than failing.
		insp, err = m.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
		if err != nil {
			return "", fmt.Errorf("sandbox: inspect after create conflict: %w", err)
		}
		if insp.Container.State != nil && insp.Container.State.Running {
			return insp.Container.ID, nil
		}
		if _, startErr := m.cli.ContainerStart(ctx, insp.Container.ID, client.ContainerStartOptions{}); startErr != nil {
			return "", fmt.Errorf("sandbox: start after create conflict: %w", startErr)
		}
		return insp.Container.ID, nil
	default:
		return "", fmt.Errorf("sandbox: inspect container %s: %w", name, err)
	}
}

func (m *Manager) createContainer(ctx context.Context, missionID, name string) (string, error) {
	memBytes := int64(sandboxMemoryBytes)
	nanoCPUs := int64(sandboxNanoCPUs)
	pids := int64(sandboxPidsLimit)
	initTrue := true

	cfg := &container.Config{
		Image: m.image,
		// PATH/HOME only — deliberately NOT os.Environ(): the whole point
		// of the sandbox is that a model-authored command never sees
		// brain's DATABASE_URL/TIMOTHY_MASTER_KEY/API tokens. Anything
		// beyond this (e.g. an executor's ANTHROPIC_* credentials) is
		// injected per-exec against the D-053 allowlist (api.go), never
		// baked into the container itself.
		Env:    []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/home/sandbox"},
		User:   sandboxUser,
		Cmd:    []string{"sleep", "infinity"},
		Labels: map[string]string{missionLabel: missionID},
	}
	mounts := []mount.Mount{m.workspaceMount}
	if m.stateMount.Source != "" {
		mounts = append(mounts, m.stateMount)
	}
	hostCfg := &container.HostConfig{
		Mounts: mounts,
		// Init (tini) reaps zombie processes: `sleep infinity` as PID 1
		// does not, and `timeout` killing a shell whose backgrounded
		// grandchildren reparent to PID 1 would otherwise accumulate
		// zombies against PidsLimit over a long mission.
		Init: &initTrue,
		Resources: container.Resources{
			Memory:    memBytes,
			NanoCPUs:  nanoCPUs,
			PidsLimit: &pids,
		},
		// Default bridge: internet access (a coding mission may need
		// `pip install`/`npm install`), but NOT the compose-internal
		// "timothy" network — no route to postgres/gateway/memoryd.
		NetworkMode:   "bridge",
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
	}
	resp, err := m.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hostCfg,
		Name:       name,
	})
	if err != nil {
		return "", fmt.Errorf("sandbox: create container %s: %w", name, err)
	}
	if _, err := m.cli.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("sandbox: start container %s: %w", name, err)
	}
	return resp.ID, nil
}

// Exec runs command in missionID's sandbox container, streaming
// combined stdout+stderr to out, and returns the exit code. Timeout is
// enforced by wrapping the command in the container's own `timeout`
// binary — Docker has no ExecKill API, so cancelling ctx only closes
// the attach stream, it does not stop the process running inside the
// container. The client-side deadline (timeout + execClientSlack) is a
// backstop for a command the timeout binary cannot kill.
//
// err is non-nil only for infrastructure failures (daemon unreachable,
// container gone, attach failed) or a timeout (mirroring
// builtin.Shell's contract: a command that ran and exited non-zero is
// reported via exitCode, not err).
func (m *Manager) Exec(ctx context.Context, missionID, workdir, command string, timeout time.Duration, out io.Writer) (exitCode int, err error) {
	return m.ExecEnv(ctx, missionID, workdir, command, nil, timeout, out)
}

// ExecEnv is Exec plus per-exec environment variables (D-053) — env
// must already be allowlist-validated by the caller (sandboxd's HTTP
// layer); this method trusts it and passes it straight to Docker's
// ExecCreate. Existing Exec callers are unaffected: they route through
// here with env == nil.
func (m *Manager) ExecEnv(ctx context.Context, missionID, workdir, command string, env map[string]string, timeout time.Duration, out io.Writer) (exitCode int, err error) {
	containerID, err := m.ensureContainer(ctx, missionID)
	if err != nil {
		return 0, err
	}

	secs := int(timeout / time.Second)
	if secs < 1 {
		secs = 1
	}
	cctx, cancel := context.WithTimeout(ctx, timeout+execClientSlack)
	defer cancel()

	execEnv := make([]string, 0, len(env))
	for k, v := range env {
		execEnv = append(execEnv, k+"="+v)
	}
	execCfg := client.ExecCreateOptions{
		Cmd:          []string{"timeout", "-k", strconv.Itoa(execGraceKillSeconds), strconv.Itoa(secs), "/bin/sh", "-c", command},
		Env:          execEnv,
		WorkingDir:   workdir,
		TTY:          false,
		AttachStdout: true,
		AttachStderr: true,
	}
	created, err := m.cli.ExecCreate(cctx, containerID, execCfg)
	if err != nil {
		return 0, fmt.Errorf("sandbox: exec create: %w", err)
	}
	attach, err := m.cli.ExecAttach(cctx, created.ID, client.ExecAttachOptions{TTY: false})
	if err != nil {
		return 0, fmt.Errorf("sandbox: exec attach: %w", err)
	}
	defer attach.Close()

	// StdCopy gets its own goroutine because output-stream EOF and
	// exec-process exit are independent events, and blocking on either
	// alone is wrong:
	//   - a backgrounded grandchild that inherited the shell's stdout
	//     keeps the stream open past the exec's exit (`sh -c "server &"`
	//     is exactly what an app-dev worker produces) — blocking on
	//     StdCopy alone would hang until that grandchild finishes; the
	//     local-exec path solves this with cmd.WaitDelay, this loop is
	//     the sandbox analog (force-close after execStreamGrace);
	//   - a process that closes its own stdout but keeps running yields
	//     EOF while Running is still true — trusting the inspect's exit
	//     code at that moment would report a FALSE success for a command
	//     that hasn't finished, so the loop also waits for exec exit.
	copied := make(chan error, 1)
	go func() {
		_, cerr := stdcopy.StdCopy(out, out, attach.Reader)
		copied <- cerr
	}()

	poll := time.NewTicker(execPollInterval)
	defer poll.Stop()
	var insp client.ExecInspectResult
	var graceUntil time.Time
	copyDone, execDone, forcedClose := false, false, false
	for !copyDone || !execDone {
		select {
		case cerr := <-copied:
			copyDone = true
			if cerr != nil && !forcedClose {
				return 0, fmt.Errorf("sandbox: read exec output: %w", cerr)
			}
		case <-cctx.Done():
			attach.Close()
			if !copyDone {
				<-copied
			}
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, fmt.Errorf("command timed out after %s: %w", timeout, ErrTimeout)
		case now := <-poll.C:
			if !execDone {
				cur, ierr := m.cli.ExecInspect(cctx, created.ID, client.ExecInspectOptions{})
				if ierr != nil {
					continue // transient; cctx or stream EOF ends the wait
				}
				if cur.Running {
					continue
				}
				insp, execDone = cur, true
				graceUntil = now.Add(execStreamGrace)
			}
			if !copyDone && now.After(graceUntil) {
				// Exec exited but something still holds the output stream
				// open — force EOF; the command's own result is already
				// settled, so the resulting read error is not the
				// command's failure.
				forcedClose = true
				attach.Close()
			}
		}
	}
	if insp.ExitCode == timeoutExitCode {
		return timeoutExitCode, fmt.Errorf("command timed out after %s: %w", timeout, ErrTimeout)
	}
	return insp.ExitCode, nil
}

// Remove force-removes missionID's sandbox container, if any. Callers
// treat this as best-effort — a slow or unreachable daemon must never
// block a mission's terminal state transition.
func (m *Manager) Remove(ctx context.Context, missionID string) error {
	name := containerName(missionID)
	_, err := m.cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("sandbox: remove container %s: %w", name, err)
	}
	m.mu.Lock()
	delete(m.locks, missionID)
	m.mu.Unlock()
	return nil
}

// List returns the mission id (the timothy.mission label's value) of
// every sandbox container that exists, running or not. The caller
// (brain's sandboxclient.Sweep) filters this against its own
// terminal-mission knowledge and deletes what it decides is safe to
// remove — this package holds no Postgres state to make that call
// itself.
func (m *Manager) List(ctx context.Context) ([]string, error) {
	result, err := m.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", missionLabel),
	})
	if err != nil {
		return nil, fmt.Errorf("sandbox: list: %w", err)
	}
	ids := make([]string, 0, len(result.Items))
	for _, c := range result.Items {
		if missionID := c.Labels[missionLabel]; missionID != "" {
			ids = append(ids, missionID)
		}
	}
	return ids, nil
}
