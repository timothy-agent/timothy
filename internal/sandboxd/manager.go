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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
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

// sandboxPath is the fixed PATH set on every sandbox container,
// overriding the image's own ENV PATH (see createContainer's Env
// comment). The three user-prefix dirs come first so a tool a worker
// installs with `npm install -g`, `pip install --user`, or `go install`
// under the sandbox HOME is on PATH for every later exec in the same
// container: sandbox-base.Dockerfile sets NPM_CONFIG_PREFIX to
// /home/sandbox/.npm-global and adds .local/bin, sandbox-go.Dockerfile
// adds go/bin.
const sandboxPath = "PATH=/home/sandbox/.local/bin:/home/sandbox/.npm-global/bin:/home/sandbox/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

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

	// sandboxMemoryReservationBytes is the soft limit (D-056):
	// MemorySwap == sandboxMemoryBytes below caps a container's total
	// memory+swap usage at the same 2 GiB ceiling, so a sandbox can
	// never push the host into swap; MemoryReservation is the kernel's
	// earlier, softer signal — under host memory pressure it reclaims a
	// sandbox's page cache before the 2 GiB hard cap is ever reached.
	sandboxMemoryReservationBytes = 256 << 20 // 256 MiB

	// sandboxOomScoreAdj (D-056) biases the kernel's OOM killer toward
	// sacrificing a sandbox container's processes before brain, gateway,
	// or sshd under host memory pressure.
	sandboxOomScoreAdj = 500

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

// environmentKeys is the D-05x allowlist of a mission's "environment"
// key — the ONLY way an image is ever chosen; a request carries the
// key, never a free-form image string (mirrors
// internal/brain/missions.Environments, which the API validates
// create/schedule requests against before this is ever reached). ""
// and "base" both resolve to the operator-configured base image
// (MISSION_SANDBOX_IMAGE) — "" is Manager's zero-value default for
// back-compat with a caller that predates the environment axis, "base"
// is the operator's explicit escape hatch out of auto-detection. Every
// other key derives a variant image ref from the base ref (imageFor):
// the base repository name gets "-<key>" appended, same tag —
// timothy-sandbox:latest -> timothy-sandbox-go:latest (local `make
// sandbox-image` convention) and
// ghcr.io/timothy-agent/timothy-sandbox:0.1.0-alpha.21 ->
// ghcr.io/timothy-agent/timothy-sandbox-go:0.1.0-alpha.21 (release
// convention: deploy/sandbox-<key>.Dockerfile + release.yml publish
// exactly these names).
var environmentKeys = map[string]bool{
	"go":     true,
	"node":   true,
	"python": true,
	"java":   true,
	"php":    true,
}

// ErrUnknownEnvironment reports an environment key outside
// environmentKeys (and not "" or "base") — never silently falls back
// to the base image, so a typo'd or stale key fails loudly instead of
// running a mission's coding work in the wrong toolchain.
var ErrUnknownEnvironment = errors.New("sandbox: unknown environment")

// imageFor derives environment's image ref from baseImage — see
// D-05x. baseImage is the operator-configured MISSION_SANDBOX_IMAGE
// (back-compat default, and "base"'s explicit target). Digest refs
// (baseImage containing "@") cannot be derived from and are rejected
// for any variant environment.
func imageFor(baseImage, environment string) (string, error) {
	switch environment {
	case "", "base":
		return baseImage, nil
	default:
		if !environmentKeys[environment] {
			return "", fmt.Errorf("%w: %q", ErrUnknownEnvironment, environment)
		}
		if strings.Contains(baseImage, "@") {
			return "", fmt.Errorf("sandbox: variant environments require a tagged image ref, got digest %q", baseImage)
		}
		repo, tag := splitImageRef(baseImage)
		variant := repo + "-" + environment
		if tag == "" {
			return variant, nil
		}
		return variant + ":" + tag, nil
	}
}

// splitImageRef splits an image ref into repository and tag. The tag
// separator is the last ":" occurring after the last "/", so a
// registry-with-port ref (e.g. localhost:5000/timothy-sandbox) is never
// split at the port colon. No tag present -> tag is "".
func splitImageRef(image string) (repo, tag string) {
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon <= slash {
		return image, ""
	}
	return image[:colon], image[colon+1:]
}

// Manager creates, reuses, and tears down one Docker container per
// mission on the same Docker daemon brain itself runs under (driven via
// the mounted docker.sock). Safe for concurrent use.
type Manager struct {
	cli *client.Client
	// baseImage is MISSION_SANDBOX_IMAGE — the image "", "base", and (via
	// CheckImage) the boot-time health check all resolve to.
	baseImage string
	log       *slog.Logger

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

	// pullMu (D-056) serializes ImagePull across missions — concurrent
	// layer decompression for two different variant images spikes
	// hundreds of MB each; serializing trades pull latency for bounded
	// memory during the pull.
	pullMu sync.Mutex
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
	return &Manager{cli: cli, baseImage: image, log: log, workspaceMount: wm, stateMount: sm, locks: map[string]*sync.Mutex{}}, nil
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

// CheckImage confirms the configured BASE sandbox image actually
// exists locally — an operator who never ran `make sandbox-image`
// would otherwise see every mission shell call fail opaquely instead
// of a clear boot-time health signal. Variant images (go/node/...) are
// not checked here: an operator who never runs a mission in that
// environment need not have it locally yet, and createContainer pulls
// a missing variant on demand instead (ErrUnknownEnvironment is for an
// unrecognized key, not a missing image).
func (m *Manager) CheckImage(ctx context.Context) error {
	_, err := m.cli.ImageInspect(ctx, m.baseImage)
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
// host reboot or an OOM-killed PID 1), Running (reuse as-is). environment
// (D-05x) only matters on the absent path — a container's image is
// fixed for its whole life, so ensureContainer never checks it against
// an already-running/existing container; the mission row's own
// Environment field is what's sticky, not anything read back from
// Docker.
func (m *Manager) ensureContainer(ctx context.Context, missionID, environment string) (string, error) {
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
		id, createErr := m.createContainer(ctx, missionID, name, environment)
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

func (m *Manager) createContainer(ctx context.Context, missionID, name, environment string) (string, error) {
	image, err := imageFor(m.baseImage, environment)
	if err != nil {
		return "", err
	}
	memBytes := int64(sandboxMemoryBytes)
	nanoCPUs := int64(sandboxNanoCPUs)
	pids := int64(sandboxPidsLimit)
	initTrue := true

	cfg := &container.Config{
		Image: image,
		// PATH/HOME only — deliberately NOT os.Environ(): the whole point
		// of the sandbox is that a model-authored command never sees
		// brain's DATABASE_URL/TIMOTHY_MASTER_KEY/API tokens. Anything
		// beyond this (e.g. an executor's ANTHROPIC_* credentials) is
		// injected per-exec against the D-053 allowlist (api.go), never
		// baked into the container itself.
		Env:    []string{sandboxPath, "HOME=/home/sandbox"},
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
			Memory: memBytes,
			// MemorySwap == Memory (D-056): swap is included in, not
			// added on top of, the 2 GiB cap — a sandbox can never push
			// the host into swap no matter how it misbehaves.
			MemorySwap:        memBytes,
			MemoryReservation: sandboxMemoryReservationBytes,
			NanoCPUs:          nanoCPUs,
			PidsLimit:         &pids,
		},
		// OomScoreAdj (D-056): the kernel sacrifices sandboxes before
		// brain/gateway/sshd when the host itself is under memory
		// pressure.
		OomScoreAdj: sandboxOomScoreAdj,
		// Capabilities only matter to root, and the container runs uid
		// 65534, but CapDrop closes NET_RAW on the bridge and
		// no-new-privileges blocks any setuid escalation path; missions
		// need neither (pip/npm/git run unprivileged).
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
		// Default bridge: internet access (a coding mission may need
		// `pip install`/`npm install`), but NOT the compose-internal
		// "timothy" network — no route to postgres/gateway/memoryd.
		NetworkMode:   "bridge",
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
	}
	createOpts := client.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hostCfg,
		Name:       name,
	}
	resp, err := m.cli.ContainerCreate(ctx, createOpts)
	if err != nil {
		if !errdefs.IsNotFound(err) {
			return "", fmt.Errorf("sandbox: create container %s: %w", name, err)
		}
		// Image not pulled locally yet (variant images especially are
		// built/pushed at release time, not baked into every deployment) —
		// pull it once and retry create exactly once. pullMu (D-056)
		// serializes this across concurrent missions: two different
		// variant images decompressing layers at once spikes hundreds of
		// MB each, and this trades pull latency for bounded memory.
		if err := m.pullImage(ctx, image, missionID); err != nil {
			return "", err
		}
		resp, err = m.cli.ContainerCreate(ctx, createOpts)
		if err != nil {
			return "", fmt.Errorf("sandbox: create container %s: %w", name, err)
		}
	}
	if _, err := m.cli.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("sandbox: start container %s: %w", name, err)
	}
	return resp.ID, nil
}

// pullImage pulls image, serialized against every other pull via pullMu
// (D-056). Re-checks with ImageInspect after acquiring the lock: the
// image may have arrived while this call waited on a sibling mission's
// pull, and re-pulling it would be a wasted decompression pass.
func (m *Manager) pullImage(ctx context.Context, image, missionID string) error {
	m.pullMu.Lock()
	defer m.pullMu.Unlock()

	if _, err := m.cli.ImageInspect(ctx, image); err == nil {
		return nil
	}

	if m.log != nil {
		m.log.Info("sandbox: pulling image", "image", image, "mission", missionID)
	}
	pull, pullErr := m.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if pullErr != nil {
		return fmt.Errorf("sandbox: pull image %s: %w", image, pullErr)
	}
	if waitErr := pull.Wait(ctx); waitErr != nil {
		return fmt.Errorf("sandbox: pull image %s: %w", image, waitErr)
	}
	return nil
}

// Exec runs command in missionID's sandbox container (environment
// selects its image, D-05x, on first creation only — see
// ensureContainer), streaming combined stdout+stderr to out, and
// returns the exit code. Timeout is enforced by wrapping the command in
// the container's own `timeout` binary — Docker has no ExecKill API,
// so cancelling ctx only closes the attach stream, it does not stop
// the process running inside the container. The client-side deadline
// (timeout + execClientSlack) is a backstop for a command the timeout
// binary cannot kill.
//
// err is non-nil only for infrastructure failures (daemon unreachable,
// container gone, attach failed) or a timeout (mirroring
// builtin.Shell's contract: a command that ran and exited non-zero is
// reported via exitCode, not err).
func (m *Manager) Exec(ctx context.Context, missionID, environment, workdir, command string, timeout time.Duration, out io.Writer) (exitCode int, err error) {
	return m.ExecEnv(ctx, missionID, environment, workdir, command, nil, timeout, out)
}

// ExecEnv is Exec plus per-exec environment variables (D-053) — env
// must already be allowlist-validated by the caller (sandboxd's HTTP
// layer); this method trusts it and passes it straight to Docker's
// ExecCreate. Existing Exec callers are unaffected: they route through
// here with env == nil.
func (m *Manager) ExecEnv(ctx context.Context, missionID, environment, workdir, command string, env map[string]string, timeout time.Duration, out io.Writer) (exitCode int, err error) {
	containerID, err := m.ensureContainer(ctx, missionID, environment)
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

// hostMemoryFloorMB / perSandboxEstimateMB (D-056) are the two halves of
// the admission rule: floor is the headroom the host and its own
// services (brain, gateway, sshd, ...) must keep; estimate is one
// active sandbox's typical working set INCLUDING a claude CLI run —
// deliberately below sandboxMemoryBytes's 2 GiB hard cap, which is a
// ceiling a runaway container may hit, not what a normal one actually
// uses. Admission compares against MemAvailable (not free), the kernel's
// own estimate of what can be handed to a new workload without swapping.
const (
	hostMemoryFloorMB    = 1024
	perSandboxEstimateMB = 768
)

// CapacityReport is sandboxd's answer to "can this host afford another
// working mission" — brain's admission gate (missions/sweep.go,
// driver.go) consults it before flipping a mission idle->working, so a
// host already tight on memory queues new work instead of swap-thrashing
// under one more sandbox plus its claude CLI process.
type CapacityReport struct {
	Admit            bool
	MemAvailableMB   int
	RunningSandboxes int
	Reason           string // empty when Admit is true
}

// hostMeminfoPath is bind-mounted from the LXC guest's own host-side
// /proc/meminfo (deploy/docker-compose.yml). Under LXC, a container's
// raw /proc/meminfo is the hypervisor's kernel procfs — lxcfs only
// masks /proc for processes running directly on the guest, not for
// something bind-mounted straight through into a container — so
// sandboxd's own /proc/meminfo reports the HYPERVISOR's memory (e.g.
// 32 GB on a 4 GB guest). The guest's real, lxcfs-served view is only
// reachable via this separately-mounted path; falls back to
// /proc/meminfo when absent (bare-metal/non-LXC hosts, or the mount
// missing).
const hostMeminfoPath = "/host/meminfo"

// Capacity reports whether the host can afford one more working
// mission. MemAvailableMB reads /host/meminfo when present, else
// /proc/meminfo (see hostMeminfoPath) — sandboxd's own container runs
// with no memory limit, so this is the HOST's view of available
// memory, not sandboxd's cgroup. RunningSandboxes reuses List, the same
// live-container count api.go's ensure-time cap uses.
func (m *Manager) Capacity(ctx context.Context) (CapacityReport, error) {
	f, err := openMeminfo(hostMeminfoPath, "/proc/meminfo")
	if err != nil {
		return CapacityReport{}, fmt.Errorf("sandbox: capacity: %w", err)
	}
	defer func() { _ = f.Close() }()
	availMB, err := parseMemAvailable(f)
	if err != nil {
		return CapacityReport{}, fmt.Errorf("sandbox: capacity: %w", err)
	}

	ids, err := m.List(ctx)
	if err != nil {
		return CapacityReport{}, fmt.Errorf("sandbox: capacity: %w", err)
	}

	report := CapacityReport{MemAvailableMB: availMB, RunningSandboxes: len(ids)}
	if availMB >= hostMemoryFloorMB+perSandboxEstimateMB {
		report.Admit = true
		return report, nil
	}
	report.Reason = fmt.Sprintf("mem_available %dMB < floor %d + per-sandbox %d", availMB, hostMemoryFloorMB, perSandboxEstimateMB)
	return report, nil
}

// openMeminfo opens primary, falling back to fallback when primary
// doesn't exist — split out from Capacity (params instead of the
// hostMeminfoPath/proc constants directly) so the preference order is
// table-testable against temp files without a real /host or /proc.
func openMeminfo(primary, fallback string) (*os.File, error) {
	if f, err := os.Open(primary); err == nil { //nolint:gosec // G304: fixed operator-controlled paths (hostMeminfoPath/proc), not user input.
		return f, nil
	}
	return os.Open(fallback) //nolint:gosec // G304: fixed operator-controlled paths (hostMeminfoPath/proc), not user input.
}

// parseMemAvailable reads the "MemAvailable:" line from a /proc/meminfo
// stream and returns it in MB (the file reports kB) — split out from
// Capacity so the parsing logic is table-testable without a real
// /proc/meminfo.
func parseMemAvailable(r io.Reader) (int, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("sandbox: malformed MemAvailable line %q", line)
		}
		kb, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, fmt.Errorf("sandbox: parse MemAvailable value %q: %w", fields[1], err)
		}
		return kb / 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("sandbox: read meminfo: %w", err)
	}
	return 0, fmt.Errorf("sandbox: no MemAvailable line in meminfo")
}
