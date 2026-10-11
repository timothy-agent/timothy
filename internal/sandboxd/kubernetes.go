package sandboxd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
	"k8s.io/streaming/pkg/httpstream"
)

// Kubernetes backend (D-154): one pod per mission in a dedicated
// namespace, created through the API server with sandboxd's own
// ServiceAccount instead of a Docker socket. The hardening the Docker
// backend expresses as HostConfig fields (docker.go createContainer)
// maps onto the pod securityContext and resources below; the two
// ulimits a pod spec cannot express (pids, fsize) are covered by the
// kubelet's podPidsLimit and the ephemeral-storage limit instead.
// Mission files live on one ReadWriteMany PVC that brain mounts whole
// at /workspace and each sandbox pod mounts with a subPath narrowed to
// its own mission directory (D-107 boundary; D-155).

const (
	// defaultSandboxNamespace is where mission pods run when
	// SANDBOXD_K8S_NAMESPACE is unset. Separate from the platform
	// namespace so the sandbox NetworkPolicy, quota and Pod Security
	// labels never apply to brain or gateway.
	defaultSandboxNamespace = "timothy-sandbox"

	// sandboxContainerName is the single container in a mission pod.
	sandboxContainerName = "sandbox"

	// defaultSandboxTTL bounds a pod's life (activeDeadlineSeconds).
	// A pod past it goes Failed and the next exec recreates it, the
	// same recovery the Docker backend performs on an exited
	// container.
	defaultSandboxTTL = 24 * time.Hour

	// defaultReadyTimeout bounds the wait for a fresh pod to reach
	// Running: a first pull of the sandbox image on a cold node takes
	// minutes, scheduling on a full cluster may take longer.
	defaultReadyTimeout = 10 * time.Minute

	// readyPollInterval is how often ensurePod re-reads a pending pod.
	readyPollInterval = time.Second

	// podTerminationGraceSeconds: `sleep infinity` has nothing to flush.
	podTerminationGraceSeconds = int64(5)

	// sandboxEphemeralStorage caps a pod's writable layers and logs.
	// The rootfs is read-only and HOME and /tmp are memory-backed, so
	// this only bounds container logs and the kubelet's own scratch.
	sandboxEphemeralStorage = "4Gi"

	// saNamespacePath is the kubelet-mounted file naming sandboxd's
	// own namespace; it is the default owner id (D-132) on Kubernetes.
	saNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

	// workspaceVolumeName and friends name the pod's volumes.
	workspaceVolumeName  = "workspace"
	stateVolumeName      = "executor-state"
	toolchainsVolumeName = "toolchains"
	cachesVolumeName     = "caches"
	homeVolumeName       = "home"
	tmpVolumeName        = "tmp"
)

// execScript is the in-pod wrapper every exec runs through. pods/exec
// has no working-directory or environment parameters, so the script
// takes the workdir ($1), the timeout in seconds ($2) and the command
// ($3) as arguments (never interpolated, so no quoting bugs) and
// the per-exec env arrives through env(1) in front of it.
//
// The command's output goes to a scratch file that tail(1) streams
// back until the command's pid is gone. A backgrounded grandchild
// that inherited the shell's stdout would otherwise hold the exec
// stream open past the command's exit and hang the call: the Docker
// backend polls ExecInspect and force-closes the attach after a
// grace period, but pods/exec has no inspect, so the stream must
// close by itself when the command exits. tail --pid does exactly
// that. Exit codes pass through unchanged, including timeout(1)'s
// 124 that ExecEnv maps to ErrTimeout.
var execScript = `cd "$1" || exit 1
o=$(mktemp /tmp/.exec.XXXXXX) || exit 1
timeout -k ` + strconv.Itoa(execGraceKillSeconds) + ` "$2" /bin/sh -c "$3" >"$o" 2>&1 &
p=$!
tail -n +1 -f -s 0.1 --pid="$p" -- "$o"
wait "$p"; c=$?
rm -f -- "$o"
exit "$c"`

// KubernetesConfig is everything the Kubernetes backend reads from the
// environment; cmd/sandboxd fills it.
type KubernetesConfig struct {
	Image     string
	Namespace string // SANDBOXD_K8S_NAMESPACE
	Owner     string // SANDBOXD_K8S_OWNER; empty reads the ServiceAccount namespace

	// WorkspacePVC is the ReadWriteMany claim brain mounts at
	// /workspace. Required.
	WorkspacePVC string
	// Optional claims; empty means the same degraded behaviour as the
	// Docker backend without the matching volume.
	StatePVC      string
	ToolchainsPVC string
	CachesPVC     string

	RuntimeClass   string
	NodeSelector   map[string]string
	Tolerations    []corev1.Toleration
	SandboxTTL     time.Duration
	ReadyTimeout   time.Duration
	SkipImageCheck bool
}

// Kubernetes implements Backend on top of the Kubernetes API.
type Kubernetes struct {
	cs      kubernetes.Interface
	restCfg *rest.Config
	cfg     KubernetesConfig
	log     *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex

	// execFn runs cmd in pod and returns its exit code; the default
	// is remoteExec, tests substitute it.
	execFn func(ctx context.Context, pod string, cmd []string, out io.Writer) (int, error)

	imageCheck *imageCheckCache
}

// NewKubernetes connects to the API server (in-cluster, or KUBECONFIG
// for local runs) and validates the config. Like NewDocker it fails
// closed: a missing image or workspace claim stops the service.
func NewKubernetes(cfg KubernetesConfig, log *slog.Logger) (*Kubernetes, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		return nil, fmt.Errorf("sandbox: kubernetes client config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("sandbox: kubernetes client: %w", err)
	}
	return newKubernetes(cs, restCfg, cfg, log)
}

func newKubernetes(cs kubernetes.Interface, restCfg *rest.Config, cfg KubernetesConfig, log *slog.Logger) (*Kubernetes, error) {
	if cfg.Image == "" {
		return nil, fmt.Errorf("sandbox: MISSION_SANDBOX_IMAGE not set")
	}
	if cfg.WorkspacePVC == "" {
		return nil, fmt.Errorf("sandbox: SANDBOXD_K8S_WORKSPACE_PVC not set")
	}
	if cfg.Namespace == "" {
		cfg.Namespace = defaultSandboxNamespace
	}
	if cfg.Owner == "" {
		cfg.Owner = resolveK8sOwner(log)
	}
	if cfg.SandboxTTL <= 0 {
		cfg.SandboxTTL = defaultSandboxTTL
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = defaultReadyTimeout
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	k := &Kubernetes{cs: cs, restCfg: restCfg, cfg: cfg, log: log, locks: map[string]*sync.Mutex{}, imageCheck: &imageCheckCache{}}
	k.execFn = k.remoteExec
	log.Info("sandbox: kubernetes backend", "namespace", cfg.Namespace, "owner", cfg.Owner, "workspace_pvc", cfg.WorkspacePVC,
		"state_pvc", cfg.StatePVC, "toolchains_pvc", cfg.ToolchainsPVC, "caches_pvc", cfg.CachesPVC, "runtime_class", cfg.RuntimeClass)
	return k, nil
}

// resolveK8sOwner returns the owner id (D-132): sandboxd's own
// namespace, since one Timothy instance lives in one namespace and
// several instances may share a sandbox namespace.
func resolveK8sOwner(log *slog.Logger) string {
	b, err := os.ReadFile(saNamespacePath)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		if log != nil {
			log.Warn("sandbox: own namespace unknown, using default owner", "owner", defaultOwner, "error", err)
		}
		return defaultOwner
	}
	return strings.TrimSpace(string(b))
}

// Name implements Backend.
func (k *Kubernetes) Name() string { return BackendKubernetes }

// Ping is the health check: the namespace must exist and this
// ServiceAccount must be allowed to create pods, exec into them and
// delete them. A missing RBAC rule surfaces here at boot, not on the
// first mission.
func (k *Kubernetes) Ping(ctx context.Context) error {
	if _, err := k.cs.CoreV1().Namespaces().Get(ctx, k.cfg.Namespace, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("namespace %s: %w", k.cfg.Namespace, err)
	}
	for _, want := range []struct{ verb, resource, sub string }{
		{"create", "pods", ""},
		{"delete", "pods", ""},
		{"create", "pods", "exec"},
	} {
		review := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: k.cfg.Namespace, Verb: want.verb, Resource: want.resource, Subresource: want.sub},
		}}
		res, err := k.cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("access review: %w", err)
		}
		if !res.Status.Allowed {
			name := want.resource
			if want.sub != "" {
				name += "/" + want.sub
			}
			return fmt.Errorf("rbac: %s %s in %s denied: %s", want.verb, name, k.cfg.Namespace, res.Status.Reason)
		}
	}
	return nil
}

// CheckImage asks the registry whether the image manifest exists;
// there is no local image list on Kubernetes. Private registries the
// anonymous probe cannot read set SANDBOXD_K8S_SKIP_IMAGE_CHECK.
func (k *Kubernetes) CheckImage(ctx context.Context) error {
	if k.cfg.SkipImageCheck {
		return nil
	}
	return k.imageCheck.check(ctx, k.cfg.Image)
}

func podName(missionID string) string { return containerNamePrefix + missionID }

func (k *Kubernetes) missionLock(missionID string) *sync.Mutex {
	k.mu.Lock()
	defer k.mu.Unlock()
	l, ok := k.locks[missionID]
	if !ok {
		l = &sync.Mutex{}
		k.locks[missionID] = l
	}
	return l
}

// ownerSelector matches pods this instance owns (D-132).
func (k *Kubernetes) ownerSelector() string {
	return labels.Set{ownerLabel: k.cfg.Owner}.AsSelector().String()
}

func (k *Kubernetes) checkOwner(pod *corev1.Pod) error {
	if owner := pod.Labels[ownerLabel]; owner != "" && owner != k.cfg.Owner {
		return fmt.Errorf("%w: owner %q, this instance is %q", ErrForeignContainer, owner, k.cfg.Owner)
	}
	return nil
}

// ensurePod returns a Running pod for missionID, creating one or
// replacing a finished one. The workspace scope check runs on every
// path (D-116), as in the Docker backend.
func (k *Kubernetes) ensurePod(ctx context.Context, missionID, workdir string) (*corev1.Pod, error) {
	if _, err := missionWorkspaceDir(workdir, missionID); err != nil {
		return nil, err
	}
	lock := k.missionLock(missionID)
	lock.Lock()
	defer lock.Unlock()

	name := podName(missionID)
	pods := k.cs.CoreV1().Pods(k.cfg.Namespace)
	pod, err := pods.Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		if err := k.checkOwner(pod); err != nil {
			return nil, err
		}
		switch pod.Status.Phase {
		case corev1.PodRunning:
			if pod.DeletionTimestamp == nil {
				return pod, nil
			}
		case corev1.PodPending:
			return k.waitRunning(ctx, name)
		}
		// Succeeded, Failed, Unknown, or being deleted: a pod never
		// restarts (restartPolicy Never), so replace it.
		if err := k.deletePod(ctx, name); err != nil {
			return nil, err
		}
		if err := k.waitGone(ctx, name); err != nil {
			return nil, err
		}
	case !apierrors.IsNotFound(err):
		return nil, fmt.Errorf("sandbox: get pod %s: %w", name, err)
	}

	spec, err := k.podSpec(missionID, workdir)
	if err != nil {
		return nil, err
	}
	if _, err := pods.Create(ctx, spec, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("sandbox: create pod %s: %w", name, err)
		}
		// Lost a race despite the mission lock (a sibling sandboxd
		// process); use whatever is there now.
	}
	return k.waitRunning(ctx, name)
}

// waitRunning polls until the pod is Running, bounded by ReadyTimeout.
// Image pull failures and a pod that finished before it ran fail
// immediately with the kubelet's reason; a pod still Pending at the
// deadline fails with its last scheduling condition, so a quota or
// capacity problem reads as such in the mission's failure.
func (k *Kubernetes) waitRunning(ctx context.Context, name string) (*corev1.Pod, error) {
	deadline := time.NewTimer(k.cfg.ReadyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(readyPollInterval)
	defer tick.Stop()
	var last *corev1.Pod
	for {
		pod, err := k.cs.CoreV1().Pods(k.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("sandbox: get pod %s: %w", name, err)
		}
		last = pod
		switch pod.Status.Phase {
		case corev1.PodRunning:
			return pod, nil
		case corev1.PodSucceeded, corev1.PodFailed:
			return nil, fmt.Errorf("sandbox: pod %s is %s before running: %s %s", name, pod.Status.Phase, pod.Status.Reason, pod.Status.Message)
		}
		if reason, msg, fatal := waitingFailure(pod); fatal {
			return nil, fmt.Errorf("sandbox: pod %s cannot start: %s: %s", name, reason, msg)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("sandbox: pod %s not running after %s: %s", name, k.cfg.ReadyTimeout, pendingReason(last))
		case <-tick.C:
		}
	}
}

// waitingFailure reports a container waiting reason the kubelet will
// not recover from on its own.
func waitingFailure(pod *corev1.Pod) (reason, msg string, fatal bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		w := cs.State.Waiting
		if w == nil {
			continue
		}
		switch w.Reason {
		case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError", "ImageInspectError":
			return w.Reason, w.Message, true
		}
	}
	return "", "", false
}

func pendingReason(pod *corev1.Pod) string {
	if pod == nil {
		return "no status"
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue {
			return fmt.Sprintf("%s: %s", c.Reason, c.Message)
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil {
			return fmt.Sprintf("%s: %s", w.Reason, w.Message)
		}
	}
	return string(pod.Status.Phase)
}

func (k *Kubernetes) deletePod(ctx context.Context, name string) error {
	grace := int64(0)
	err := k.cs.CoreV1().Pods(k.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &grace})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("sandbox: delete pod %s: %w", name, err)
	}
	return nil
}

// waitGone waits for a deleted pod's name to free up before a
// replacement is created under it.
func (k *Kubernetes) waitGone(ctx context.Context, name string) error {
	deadline := time.NewTimer(k.cfg.ReadyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(readyPollInterval)
	defer tick.Stop()
	for {
		_, err := k.cs.CoreV1().Pods(k.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("sandbox: get pod %s: %w", name, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("sandbox: pod %s still terminating after %s", name, k.cfg.ReadyTimeout)
		case <-tick.C:
		}
	}
}

// podSpec builds the mission pod. Every field below has a counterpart
// in docker.go createContainer; see that function for the reasoning
// behind each limit.
func (k *Kubernetes) podSpec(missionID, workdir string) (*corev1.Pod, error) {
	dir, err := missionWorkspaceDir(workdir, missionID)
	if err != nil {
		return nil, err
	}
	subPath := strings.TrimPrefix(dir, workspaceMountPath+"/")

	uid := int64(65534)
	nonRoot := true
	readOnly := true
	noEscalation := false
	noSAToken := false
	noServiceLinks := false
	shareProcs := true // the pause container reaps zombies, as tini does under Docker
	ttl := int64(k.cfg.SandboxTTL / time.Second)
	grace := podTerminationGraceSeconds

	volumes := []corev1.Volume{
		{Name: workspaceVolumeName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: k.cfg.WorkspacePVC}}},
		{Name: tmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptrQuantity("512Mi")}}},
		{Name: homeVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptrQuantity("1Gi")}}},
	}
	mounts := []corev1.VolumeMount{
		{Name: workspaceVolumeName, MountPath: dir, SubPath: subPath},
		{Name: tmpVolumeName, MountPath: tmpMountPath},
		{Name: homeVolumeName, MountPath: sandboxHomePath},
	}
	if k.cfg.StatePVC != "" {
		volumes = append(volumes, corev1.Volume{Name: stateVolumeName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: k.cfg.StatePVC}}})
		mounts = append(mounts, corev1.VolumeMount{Name: stateVolumeName, MountPath: executorStateMountPath})
	}
	if k.cfg.ToolchainsPVC != "" {
		volumes = append(volumes, corev1.Volume{Name: toolchainsVolumeName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: k.cfg.ToolchainsPVC}}})
		mounts = append(mounts, corev1.VolumeMount{Name: toolchainsVolumeName, MountPath: toolchainsMountPath})
	}
	if k.cfg.CachesPVC != "" {
		volumes = append(volumes, corev1.Volume{Name: cachesVolumeName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: k.cfg.CachesPVC}}})
		mounts = append(mounts, corev1.VolumeMount{Name: cachesVolumeName, MountPath: cachesMountPath})
	} else {
		// D-131 fallback: the mission's own cache dir on the workspace claim.
		mounts = append(mounts, corev1.VolumeMount{Name: workspaceVolumeName, MountPath: cachesMountPath, SubPath: path.Join(subPath, missionCacheDirName)})
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName(missionID),
			Namespace: k.cfg.Namespace,
			Labels:    map[string]string{missionLabel: missionID, ownerLabel: k.cfg.Owner},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:         &ttl,
			TerminationGracePeriodSeconds: &grace,
			AutomountServiceAccountToken:  &noSAToken,
			EnableServiceLinks:            &noServiceLinks,
			ShareProcessNamespace:         &shareProcs,
			NodeSelector:                  k.cfg.NodeSelector,
			Tolerations:                   k.cfg.Tolerations,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:      &uid,
				RunAsGroup:     &uid,
				RunAsNonRoot:   &nonRoot,
				FSGroup:        &uid,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Volumes: volumes,
			Containers: []corev1.Container{{
				Name:            sandboxContainerName,
				Image:           k.cfg.Image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"sleep", "infinity"},
				Env: []corev1.EnvVar{
					{Name: "PATH", Value: strings.TrimPrefix(sandboxPath, "PATH=")},
					{Name: "HOME", Value: sandboxHomePath},
				},
				VolumeMounts: mounts,
				SecurityContext: &corev1.SecurityContext{
					ReadOnlyRootFilesystem:   &readOnly,
					AllowPrivilegeEscalation: &noEscalation,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:              resource.MustParse("250m"),
						corev1.ResourceMemory:           *resource.NewQuantity(sandboxMemoryReservationBytes, resource.BinarySI),
						corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:              *resource.NewMilliQuantity(sandboxNanoCPUs/1_000_000, resource.DecimalSI),
						corev1.ResourceMemory:           *resource.NewQuantity(sandboxMemoryBytes, resource.BinarySI),
						corev1.ResourceEphemeralStorage: resource.MustParse(sandboxEphemeralStorage),
					},
				},
			}},
		},
	}
	if k.cfg.RuntimeClass != "" {
		rc := k.cfg.RuntimeClass
		pod.Spec.RuntimeClassName = &rc
	}
	return pod, nil
}

func ptrQuantity(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

// execCommand composes the argv for one exec: env(1) carries the
// allowlisted per-exec variables, then execScript with its three
// positional arguments. Nothing from the caller is interpolated into
// shell syntax.
func execCommand(workdir, command string, env map[string]string, timeout time.Duration) []string {
	secs := int(timeout / time.Second)
	if secs < 1 {
		secs = 1
	}
	argv := make([]string, 0, len(env)+8)
	if len(env) > 0 {
		argv = append(argv, "env")
		for _, k := range slices.Sorted(maps.Keys(env)) {
			argv = append(argv, k+"="+env[k])
		}
	}
	return append(argv, "/bin/sh", "-c", execScript, "sh", workdir, strconv.Itoa(secs), command)
}

// ExecEnv implements Backend. Timeout is enforced in-pod by
// timeout(1) (execScript); the client-side deadline is the backstop.
func (k *Kubernetes) ExecEnv(ctx context.Context, missionID, workdir, command string, env map[string]string, timeout time.Duration, out io.Writer) (int, error) {
	pod, err := k.ensurePod(ctx, missionID, workdir)
	if err != nil {
		return 0, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout+execClientSlack)
	defer cancel()
	code, err := k.execFn(cctx, pod.Name, execCommand(workdir, command, env, timeout), out)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, fmt.Errorf("command timed out after %s: %w", timeout, ErrTimeout)
		}
		return 0, fmt.Errorf("sandbox: exec: %w", err)
	}
	if code == timeoutExitCode {
		return timeoutExitCode, fmt.Errorf("command timed out after %s: %w", timeout, ErrTimeout)
	}
	return code, nil
}

// remoteExec streams cmd through pods/exec: WebSocket first, SPDY
// when the API server refuses the upgrade (kubectl's own order since
// 1.29). stdout and stderr are both the combined writer, serialized
// because remotecommand writes them from separate goroutines.
func (k *Kubernetes) remoteExec(ctx context.Context, pod string, cmd []string, out io.Writer) (int, error) {
	req := k.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(k.cfg.Namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: sandboxContainerName,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	spdy, err := remotecommand.NewSPDYExecutor(k.restCfg, "POST", req.URL())
	if err != nil {
		return 0, fmt.Errorf("exec transport: %w", err)
	}
	ws, err := remotecommand.NewWebSocketExecutor(k.restCfg, "GET", req.URL().String())
	if err != nil {
		return 0, fmt.Errorf("exec transport: %w", err)
	}
	executor, err := remotecommand.NewFallbackExecutor(ws, spdy, httpstream.IsUpgradeFailure)
	if err != nil {
		return 0, fmt.Errorf("exec transport: %w", err)
	}
	w := &syncWriter{w: out}
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: w, Stderr: w})
	var exitErr k8sexec.CodeExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code, nil
	}
	return 0, err
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// Remove implements Backend. Only a pod carrying this instance's
// owner label is deleted (D-132).
func (k *Kubernetes) Remove(ctx context.Context, missionID string) error {
	name := podName(missionID)
	pod, err := k.cs.CoreV1().Pods(k.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("sandbox: get pod %s: %w", name, err)
	case pod.Labels[ownerLabel] != k.cfg.Owner:
		k.log.Info("sandbox: not removing pod of another or unknown owner", "mission", missionID, "owner", pod.Labels[ownerLabel])
		return nil
	default:
		if err := k.deletePod(ctx, name); err != nil {
			return err
		}
	}
	k.mu.Lock()
	delete(k.locks, missionID)
	k.mu.Unlock()
	return nil
}

// List implements Backend: mission ids of every pod this instance
// owns, in any phase, for brain's sweep.
func (k *Kubernetes) List(ctx context.Context) ([]string, error) {
	list, err := k.cs.CoreV1().Pods(k.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: k.ownerSelector()})
	if err != nil {
		return nil, fmt.Errorf("sandbox: list: %w", err)
	}
	ids := make([]string, 0, len(list.Items))
	for _, p := range list.Items {
		if p.Labels[ownerLabel] != k.cfg.Owner {
			continue
		}
		if id := p.Labels[missionLabel]; id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Capacity implements Backend. With a ResourceQuota on the namespace,
// headroom is the quota's unused memory limit and a new sandbox is
// admitted while one more 2 GiB limit fits. Without a quota the
// Docker rule applies to /proc/meminfo, which inside a pod is the
// node sandboxd itself runs on, a proxy at best; a quota is the
// honest signal and the Helm chart ships one.
func (k *Kubernetes) Capacity(ctx context.Context) (CapacityReport, error) {
	ids, err := k.List(ctx)
	if err != nil {
		return CapacityReport{}, fmt.Errorf("sandbox: capacity: %w", err)
	}
	report := CapacityReport{RunningSandboxes: len(ids)}

	if hard, used, ok, err := k.quotaMemory(ctx); err != nil {
		return CapacityReport{}, fmt.Errorf("sandbox: capacity: %w", err)
	} else if ok {
		freeMB := int((hard - used) >> 20)
		report.MemAvailableMB = freeMB
		if hard-used >= sandboxMemoryBytes {
			report.Admit = true
			return report, nil
		}
		report.Reason = fmt.Sprintf("quota limits.memory free %dMB < per-sandbox limit %dMB", freeMB, sandboxMemoryBytes>>20)
		return report, nil
	}

	f, err := openMeminfo(hostMeminfoPath, "/proc/meminfo")
	if err != nil {
		return CapacityReport{}, fmt.Errorf("sandbox: capacity: %w", err)
	}
	defer func() { _ = f.Close() }()
	availMB, err := parseMemAvailable(f)
	if err != nil {
		return CapacityReport{}, fmt.Errorf("sandbox: capacity: %w", err)
	}
	report.MemAvailableMB = availMB
	if availMB >= hostMemoryFloorMB+perSandboxEstimateMB {
		report.Admit = true
		return report, nil
	}
	report.Reason = fmt.Sprintf("mem_available %dMB < floor %d + per-sandbox %d (sandboxd's own node, no ResourceQuota)", availMB, hostMemoryFloorMB, perSandboxEstimateMB)
	return report, nil
}

// quotaMemory returns the namespace's hard and used limits.memory in
// bytes from the first ResourceQuota that sets it; ok is false when
// none does.
func (k *Kubernetes) quotaMemory(ctx context.Context) (hard, used int64, ok bool, err error) {
	quotas, err := k.cs.CoreV1().ResourceQuotas(k.cfg.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsForbidden(err) {
			k.log.Warn("sandbox: resourcequotas not readable, capacity falls back to meminfo", "error", err)
			return 0, 0, false, nil
		}
		return 0, 0, false, fmt.Errorf("list resourcequotas: %w", err)
	}
	for _, q := range quotas.Items {
		h, found := q.Status.Hard[corev1.ResourceLimitsMemory]
		if !found {
			continue
		}
		u := q.Status.Used[corev1.ResourceLimitsMemory]
		return h.Value(), u.Value(), true, nil
	}
	return 0, 0, false, nil
}
