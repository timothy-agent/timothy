package sandboxd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	k8sTestMission = "0b4a2d7c-9d8e-4f10-a4b7-5c3f2e1d0a9b"
	k8sTestWorkdir = "/workspace/missions/coding/" + k8sTestMission + "/repo"
)

func newTestK8s(t *testing.T, cfg KubernetesConfig, objs ...runtime.Object) (*Kubernetes, *fake.Clientset) {
	t.Helper()
	cs := fake.NewClientset(objs...)
	if cfg.Image == "" {
		cfg.Image = "img"
	}
	if cfg.WorkspacePVC == "" {
		cfg.WorkspacePVC = "workspace"
	}
	if cfg.Owner == "" {
		cfg.Owner = testOwner
	}
	cfg.ReadyTimeout = 2 * time.Second
	k, err := newKubernetes(cs, nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newKubernetes: %v", err)
	}
	return k, cs
}

// runOnCreate makes every created pod Running at once, the way a
// scheduled and pulled pod would be.
func runOnCreate(cs *fake.Clientset) {
	cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Status.Phase = corev1.PodRunning
		return false, nil, nil
	})
}

func findMount(mounts []corev1.VolumeMount, mountPath string) (corev1.VolumeMount, bool) {
	for _, m := range mounts {
		if m.MountPath == mountPath {
			return m, true
		}
	}
	return corev1.VolumeMount{}, false
}

func TestNewKubernetesRequiresImageAndWorkspace(t *testing.T) {
	for _, tc := range []struct{ name, image, pvc string }{
		{"no image", "", "ws"},
		{"no workspace pvc", "img", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newKubernetes(fake.NewClientset(), nil, KubernetesConfig{Image: tc.image, WorkspacePVC: tc.pvc}, nil); err == nil {
				t.Fatal("newKubernetes = nil error, want error")
			}
		})
	}
}

func TestPodSpecHardening(t *testing.T) {
	k, _ := newTestK8s(t, KubernetesConfig{})
	pod, err := k.podSpec(k8sTestMission, k8sTestWorkdir)
	if err != nil {
		t.Fatalf("podSpec: %v", err)
	}
	if pod.Name != "timothy-sandbox-"+k8sTestMission || pod.Namespace != defaultSandboxNamespace {
		t.Fatalf("name/namespace = %s/%s", pod.Name, pod.Namespace)
	}
	if pod.Labels[missionLabel] != k8sTestMission || pod.Labels[ownerLabel] != testOwner {
		t.Fatalf("labels = %v", pod.Labels)
	}
	ps := pod.Spec
	sc := ps.SecurityContext
	checks := []struct {
		name string
		ok   bool
	}{
		{"restartPolicy Never", ps.RestartPolicy == corev1.RestartPolicyNever},
		{"activeDeadlineSeconds 24h", ps.ActiveDeadlineSeconds != nil && *ps.ActiveDeadlineSeconds == int64(defaultSandboxTTL/time.Second)},
		{"terminationGracePeriodSeconds small", ps.TerminationGracePeriodSeconds != nil && *ps.TerminationGracePeriodSeconds == podTerminationGraceSeconds},
		{"automountServiceAccountToken false", ps.AutomountServiceAccountToken != nil && !*ps.AutomountServiceAccountToken},
		{"enableServiceLinks false", ps.EnableServiceLinks != nil && !*ps.EnableServiceLinks},
		{"shareProcessNamespace true", ps.ShareProcessNamespace != nil && *ps.ShareProcessNamespace},
		{"runAsUser 65534", sc.RunAsUser != nil && *sc.RunAsUser == 65534},
		{"runAsGroup 65534", sc.RunAsGroup != nil && *sc.RunAsGroup == 65534},
		{"fsGroup 65534", sc.FSGroup != nil && *sc.FSGroup == 65534},
		{"runAsNonRoot", sc.RunAsNonRoot != nil && *sc.RunAsNonRoot},
		{"seccomp RuntimeDefault", sc.SeccompProfile != nil && sc.SeccompProfile.Type == corev1.SeccompProfileTypeRuntimeDefault},
		{"no runtimeClass by default", ps.RuntimeClassName == nil},
		{"one container", len(ps.Containers) == 1},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("pod spec: %s not set", c.name)
		}
	}
	ctr := ps.Containers[0]
	csc := ctr.SecurityContext
	ctrChecks := []struct {
		name string
		ok   bool
	}{
		{"image", ctr.Image == "img"},
		{"sleep infinity", slices.Equal(ctr.Command, []string{"sleep", "infinity"})},
		{"readOnlyRootFilesystem", csc.ReadOnlyRootFilesystem != nil && *csc.ReadOnlyRootFilesystem},
		{"allowPrivilegeEscalation false", csc.AllowPrivilegeEscalation != nil && !*csc.AllowPrivilegeEscalation},
		{"drop ALL", csc.Capabilities != nil && slices.Equal(csc.Capabilities.Drop, []corev1.Capability{"ALL"})},
		{"cpu limit 2", ctr.Resources.Limits.Cpu().Cmp(resource.MustParse("2")) == 0},
		{"memory limit 2Gi", ctr.Resources.Limits.Memory().Cmp(resource.MustParse("2Gi")) == 0},
		{"ephemeral-storage limit", !ctr.Resources.Limits.StorageEphemeral().IsZero()},
		{"memory request 256Mi", ctr.Resources.Requests.Memory().Cmp(resource.MustParse("256Mi")) == 0},
	}
	for _, c := range ctrChecks {
		if !c.ok {
			t.Errorf("container: %s wrong", c.name)
		}
	}
	for _, e := range ctr.Env {
		if e.Name != "PATH" && e.Name != "HOME" {
			t.Errorf("container env carries %s; only PATH and HOME are allowed", e.Name)
		}
	}

	ws, ok := findMount(ctr.VolumeMounts, "/workspace/missions/coding/"+k8sTestMission)
	if !ok || ws.SubPath != "missions/coding/"+k8sTestMission || ws.Name != workspaceVolumeName {
		t.Fatalf("workspace mount = %+v, want subPath scoped to the mission (D-107)", ws)
	}
	if _, ok := findMount(ctr.VolumeMounts, "/workspace"); ok {
		t.Fatal("pod mounts the whole workspace; D-107 forbids it")
	}
	cache, ok := findMount(ctr.VolumeMounts, cachesMountPath)
	if !ok || cache.Name != workspaceVolumeName || cache.SubPath != "missions/coding/"+k8sTestMission+"/"+missionCacheDirName {
		t.Fatalf("cache fallback mount = %+v, want the mission's own .sandbox-cache (D-131)", cache)
	}
	for _, vol := range ps.Volumes {
		switch vol.Name {
		case tmpVolumeName, homeVolumeName:
			if vol.EmptyDir == nil || vol.EmptyDir.Medium != corev1.StorageMediumMemory || vol.EmptyDir.SizeLimit == nil {
				t.Errorf("%s volume = %+v, want sized memory emptyDir", vol.Name, vol.VolumeSource)
			}
		case workspaceVolumeName:
			if vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != "workspace" {
				t.Errorf("workspace volume = %+v", vol.VolumeSource)
			}
		default:
			t.Errorf("unexpected volume %s", vol.Name)
		}
	}
	if _, ok := findMount(ctr.VolumeMounts, executorStateMountPath); ok {
		t.Error("state mount present without a state PVC")
	}
	if _, ok := findMount(ctr.VolumeMounts, toolchainsMountPath); ok {
		t.Error("toolchains mount present without a toolchains PVC")
	}
}

func TestPodSpecOptionalVolumesAndScheduling(t *testing.T) {
	k, _ := newTestK8s(t, KubernetesConfig{
		StatePVC: "state", ToolchainsPVC: "tc", CachesPVC: "caches",
		RuntimeClass: "gvisor",
		NodeSelector: map[string]string{"pool": "sandbox"},
		Tolerations:  []corev1.Toleration{{Key: "sandbox", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}},
		SandboxTTL:   time.Hour,
	})
	pod, err := k.podSpec(k8sTestMission, k8sTestWorkdir)
	if err != nil {
		t.Fatalf("podSpec: %v", err)
	}
	ctr := pod.Spec.Containers[0]
	for _, want := range []struct{ path, claim string }{
		{executorStateMountPath, "state"}, {toolchainsMountPath, "tc"}, {cachesMountPath, "caches"},
	} {
		m, ok := findMount(ctr.VolumeMounts, want.path)
		if !ok {
			t.Errorf("no mount at %s", want.path)
			continue
		}
		if m.SubPath != "" {
			t.Errorf("%s mount has subPath %q, want the whole claim", want.path, m.SubPath)
		}
		var claim string
		for _, v := range pod.Spec.Volumes {
			if v.Name == m.Name && v.PersistentVolumeClaim != nil {
				claim = v.PersistentVolumeClaim.ClaimName
			}
		}
		if claim != want.claim {
			t.Errorf("%s backed by claim %q, want %q", want.path, claim, want.claim)
		}
	}
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "gvisor" {
		t.Error("runtimeClassName not set")
	}
	if pod.Spec.NodeSelector["pool"] != "sandbox" || len(pod.Spec.Tolerations) != 1 {
		t.Error("nodeSelector/tolerations not carried")
	}
	if *pod.Spec.ActiveDeadlineSeconds != 3600 {
		t.Errorf("activeDeadlineSeconds = %d, want 3600", *pod.Spec.ActiveDeadlineSeconds)
	}
}

func TestPodSpecRejectsForeignWorkdir(t *testing.T) {
	k, _ := newTestK8s(t, KubernetesConfig{})
	other := "/workspace/missions/coding/11111111-2222-4333-8444-555555555555/repo"
	if _, err := k.podSpec(k8sTestMission, other); !errors.Is(err, ErrWorkspaceScope) {
		t.Fatalf("podSpec(other mission's dir) = %v, want ErrWorkspaceScope", err)
	}
	if _, err := k.ensurePod(context.Background(), k8sTestMission, "/workspace"); !errors.Is(err, ErrWorkspaceScope) {
		t.Fatalf("ensurePod(/workspace) = %v, want ErrWorkspaceScope", err)
	}
}

func TestEnsurePodCreatesThenReuses(t *testing.T) {
	k, cs := newTestK8s(t, KubernetesConfig{})
	runOnCreate(cs)
	ctx := context.Background()
	first, err := k.ensurePod(ctx, k8sTestMission, k8sTestWorkdir)
	if err != nil {
		t.Fatalf("ensurePod: %v", err)
	}
	creates := 0
	for _, a := range cs.Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == "pods" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("creates = %d, want 1", creates)
	}
	second, err := k.ensurePod(ctx, k8sTestMission, k8sTestWorkdir)
	if err != nil {
		t.Fatalf("ensurePod again: %v", err)
	}
	if second.Name != first.Name {
		t.Fatalf("second pod %s, want reuse of %s", second.Name, first.Name)
	}
	for _, a := range cs.Actions()[len(cs.Actions())-1:] {
		if a.GetVerb() == "create" {
			t.Fatal("second ensurePod created a pod; want reuse")
		}
	}
}

func TestEnsurePodReplacesFinishedPod(t *testing.T) {
	dead := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName(k8sTestMission), Namespace: defaultSandboxNamespace, Labels: map[string]string{missionLabel: k8sTestMission, ownerLabel: testOwner}},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed, Reason: "DeadlineExceeded"},
	}
	k, cs := newTestK8s(t, KubernetesConfig{}, dead)
	runOnCreate(cs)
	pod, err := k.ensurePod(context.Background(), k8sTestMission, k8sTestWorkdir)
	if err != nil {
		t.Fatalf("ensurePod: %v", err)
	}
	if pod.Status.Phase != corev1.PodRunning {
		t.Fatalf("phase = %s, want Running replacement", pod.Status.Phase)
	}
	var verbs []string
	for _, a := range cs.Actions() {
		if a.GetResource().Resource == "pods" {
			verbs = append(verbs, a.GetVerb())
		}
	}
	if !slices.Contains(verbs, "delete") || !slices.Contains(verbs, "create") {
		t.Fatalf("verbs = %v, want delete then create", verbs)
	}
}

func TestEnsurePodRefusesForeignOwner(t *testing.T) {
	foreign := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName(k8sTestMission), Namespace: defaultSandboxNamespace, Labels: map[string]string{missionLabel: k8sTestMission, ownerLabel: "other"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	k, _ := newTestK8s(t, KubernetesConfig{}, foreign)
	if _, err := k.ensurePod(context.Background(), k8sTestMission, k8sTestWorkdir); !errors.Is(err, ErrForeignContainer) {
		t.Fatalf("ensurePod = %v, want ErrForeignContainer", err)
	}
}

func TestEnsurePodSurfacesImagePullFailure(t *testing.T) {
	k, cs := newTestK8s(t, KubernetesConfig{})
	cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Status.Phase = corev1.PodPending
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: sandboxContainerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "manifest unknown"}}}}
		return false, nil, nil
	})
	_, err := k.ensurePod(context.Background(), k8sTestMission, k8sTestWorkdir)
	if err == nil || !strings.Contains(err.Error(), "ImagePullBackOff") || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("ensurePod = %v, want ImagePullBackOff with the kubelet message", err)
	}
}

func TestEnsurePodPendingTimesOutWithSchedulingReason(t *testing.T) {
	k, cs := newTestK8s(t, KubernetesConfig{})
	k.cfg.ReadyTimeout = 1500 * time.Millisecond
	cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Status.Phase = corev1.PodPending
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "exceeded quota"}}
		return false, nil, nil
	})
	_, err := k.ensurePod(context.Background(), k8sTestMission, k8sTestWorkdir)
	if err == nil || !strings.Contains(err.Error(), "Unschedulable") || !strings.Contains(err.Error(), "exceeded quota") {
		t.Fatalf("ensurePod = %v, want timeout naming the scheduling condition", err)
	}
}

func TestEnsurePodConcurrentCallsCreateOnce(t *testing.T) {
	k, cs := newTestK8s(t, KubernetesConfig{})
	runOnCreate(cs)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := k.ensurePod(context.Background(), k8sTestMission, k8sTestWorkdir); err != nil {
				t.Errorf("ensurePod: %v", err)
			}
		}()
	}
	wg.Wait()
	creates := 0
	for _, a := range cs.Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == "pods" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("creates = %d, want 1 under the mission lock", creates)
	}
}

func TestExecCommandShape(t *testing.T) {
	argv := execCommand(k8sTestWorkdir, "echo hi", map[string]string{"B": "2", "A": "1"}, 30*time.Second)
	want := []string{"env", "A=1", "B=2", "/bin/sh", "-c", execScript, "sh", k8sTestWorkdir, "30", "echo hi"}
	if !slices.Equal(argv, want) {
		t.Fatalf("execCommand = %q, want %q", argv, want)
	}
	noEnv := execCommand(k8sTestWorkdir, "true", nil, 500*time.Millisecond)
	if noEnv[0] != "/bin/sh" || noEnv[len(noEnv)-2] != "1" {
		t.Fatalf("execCommand without env = %q, want no env prefix and a 1s floor", noEnv)
	}
}

// TestExecScriptRoundTrip runs execScript through a real /bin/sh with
// GNU coreutils, the way the pod does: fakes forgive quoting bugs.
func TestExecScriptRoundTrip(t *testing.T) {
	for _, bin := range []string{"timeout", "tail", "mktemp"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	if out, err := exec.Command("tail", "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), "GNU") {
		t.Skip("GNU tail required for --pid")
	}
	dir := t.TempDir()
	run := func(t *testing.T, command string, env map[string]string, timeout time.Duration) (string, int) {
		t.Helper()
		argv := execCommand(dir, command, env, timeout)
		cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // test runs its own composed argv
		cmd.Env = append(os.Environ(), "HOME="+dir)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			return out.String(), 0
		case errors.As(err, &exitErr):
			return out.String(), exitErr.ExitCode()
		default:
			t.Fatalf("run: %v", err)
			return "", 0
		}
	}
	t.Run("cwd, env, combined output, exit code", func(t *testing.T) {
		out, code := run(t, `pwd; echo "$TOKEN_X"; echo err >&2; echo 'it''s "quoted" $HOME'; exit 7`, map[string]string{"TOKEN_X": "a b=c"}, 5*time.Second)
		if code != 7 {
			t.Fatalf("exit = %d, want 7; output: %s", code, out)
		}
		for _, want := range []string{dir + "\n", "a b=c\n", "err\n", `its "quoted" $HOME` + "\n"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("timeout exits 124", func(t *testing.T) {
		start := time.Now()
		out, code := run(t, "echo before; sleep 10; echo after", nil, time.Second)
		if code != timeoutExitCode {
			t.Fatalf("exit = %d, want %d; output: %s", code, timeoutExitCode, out)
		}
		if elapsed := time.Since(start); elapsed > 8*time.Second {
			t.Fatalf("took %s, timeout did not fire", elapsed)
		}
		if !strings.Contains(out, "before") || strings.Contains(out, "after") {
			t.Fatalf("output = %q", out)
		}
	})
	t.Run("backgrounded grandchild does not hold the stream", func(t *testing.T) {
		start := time.Now()
		out, code := run(t, "echo started; sleep 6 &", nil, 10*time.Second)
		if code != 0 || !strings.Contains(out, "started") {
			t.Fatalf("exit = %d, output = %q", code, out)
		}
		if elapsed := time.Since(start); elapsed > 4*time.Second {
			t.Fatalf("took %s; the grandchild held the call open", elapsed)
		}
	})
	t.Run("missing workdir fails", func(t *testing.T) {
		if _, code := run(t, "true", nil, time.Second); code != 0 {
			t.Fatalf("sanity: exit %d", code)
		}
		argv := execCommand(dir+"/nope", "true", nil, time.Second)
		if err := exec.Command(argv[0], argv[1:]...).Run(); err == nil { //nolint:gosec // test
			t.Fatal("missing workdir ran successfully")
		}
	})
}

func TestExecEnvMapsTimeoutAndErrors(t *testing.T) {
	type stub struct {
		code int
		err  error
	}
	tests := []struct {
		name     string
		stub     stub
		wantCode int
		wantErr  error
	}{
		{"exit code passes through", stub{code: 3}, 3, nil},
		{"124 is a timeout", stub{code: timeoutExitCode}, timeoutExitCode, ErrTimeout},
		{"client deadline is a timeout", stub{err: context.DeadlineExceeded}, 0, ErrTimeout},
		{"transport error is infrastructure", stub{err: errors.New("spdy: boom")}, 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, cs := newTestK8s(t, KubernetesConfig{})
			runOnCreate(cs)
			var gotCmd []string
			k.execFn = func(_ context.Context, pod string, cmd []string, out io.Writer) (int, error) {
				gotCmd = cmd
				_, _ = io.WriteString(out, "x")
				return tc.stub.code, tc.stub.err
			}
			var out bytes.Buffer
			code, err := k.ExecEnv(context.Background(), k8sTestMission, k8sTestWorkdir, "true", map[string]string{"NO_COLOR": "1"}, time.Second, &out)
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d", code, tc.wantCode)
			}
			switch {
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			case tc.wantErr == nil && tc.stub.err == nil && err != nil:
				t.Fatalf("err = %v, want nil", err)
			case tc.stub.err != nil && tc.wantErr == nil && (err == nil || errors.Is(err, ErrTimeout)):
				t.Fatalf("err = %v, want a non-timeout infrastructure error", err)
			}
			if !slices.Contains(gotCmd, "NO_COLOR=1") {
				t.Fatalf("exec argv %q lacks the per-exec env", gotCmd)
			}
		})
	}
}

func TestRemoveHonoursOwner(t *testing.T) {
	mine := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName(k8sTestMission), Namespace: defaultSandboxNamespace, Labels: map[string]string{missionLabel: k8sTestMission, ownerLabel: testOwner}}}
	otherID := "22222222-3333-4444-8555-666666666666"
	theirs := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName(otherID), Namespace: defaultSandboxNamespace, Labels: map[string]string{missionLabel: otherID, ownerLabel: "other"}}}
	k, cs := newTestK8s(t, KubernetesConfig{}, mine, theirs)
	ctx := context.Background()
	if err := k.Remove(ctx, k8sTestMission); err != nil {
		t.Fatalf("Remove(mine): %v", err)
	}
	if err := k.Remove(ctx, otherID); err != nil {
		t.Fatalf("Remove(theirs): %v, want silent success", err)
	}
	if err := k.Remove(ctx, "33333333-3333-4444-8555-666666666666"); err != nil {
		t.Fatalf("Remove(absent): %v, want nil", err)
	}
	left, _ := cs.CoreV1().Pods(defaultSandboxNamespace).List(ctx, metav1.ListOptions{})
	if len(left.Items) != 1 || left.Items[0].Name != theirs.Name {
		t.Fatalf("pods left = %v, want only the foreign pod", left.Items)
	}
}

func TestListFiltersByOwner(t *testing.T) {
	ids := []string{"aaaaaaaa-1111-4222-8333-444444444444", "bbbbbbbb-1111-4222-8333-444444444444"}
	objs := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName(ids[0]), Namespace: defaultSandboxNamespace, Labels: map[string]string{missionLabel: ids[0], ownerLabel: testOwner}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName(ids[1]), Namespace: defaultSandboxNamespace, Labels: map[string]string{missionLabel: ids[1], ownerLabel: testOwner}, DeletionTimestamp: &metav1.Time{Time: time.Now()}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: defaultSandboxNamespace, Labels: map[string]string{missionLabel: "cccccccc-1111-4222-8333-444444444444", ownerLabel: "other"}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: defaultSandboxNamespace}},
	}
	k, _ := newTestK8s(t, KubernetesConfig{}, objs...)
	got, err := k.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	slices.Sort(got)
	if !slices.Equal(got, ids) {
		t.Fatalf("List = %v, want %v (own pods in every phase, nothing foreign)", got, ids)
	}
}

func TestCapacityFromQuota(t *testing.T) {
	quota := func(hard, used string) *corev1.ResourceQuota {
		return &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: "sandbox", Namespace: defaultSandboxNamespace},
			Status: corev1.ResourceQuotaStatus{
				Hard: corev1.ResourceList{corev1.ResourceLimitsMemory: resource.MustParse(hard)},
				Used: corev1.ResourceList{corev1.ResourceLimitsMemory: resource.MustParse(used)},
			},
		}
	}
	tests := []struct {
		name      string
		hard      string
		used      string
		wantAdmit bool
		wantMB    int
	}{
		{"room for one more", "8Gi", "4Gi", true, 4096},
		{"exactly one more", "8Gi", "6Gi", true, 2048},
		{"full", "8Gi", "7Gi", false, 1024},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, _ := newTestK8s(t, KubernetesConfig{}, quota(tc.hard, tc.used))
			rep, err := k.Capacity(context.Background())
			if err != nil {
				t.Fatalf("Capacity: %v", err)
			}
			if rep.Admit != tc.wantAdmit || rep.MemAvailableMB != tc.wantMB {
				t.Fatalf("Capacity = %+v, want admit=%v mem=%d", rep, tc.wantAdmit, tc.wantMB)
			}
			if !rep.Admit && rep.Reason == "" {
				t.Fatal("refusal without a reason")
			}
		})
	}
	t.Run("quota without limits.memory falls back to meminfo", func(t *testing.T) {
		if _, err := os.Stat("/proc/meminfo"); err != nil {
			t.Skip("no /proc/meminfo")
		}
		q := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "pods-only", Namespace: defaultSandboxNamespace},
			Status: corev1.ResourceQuotaStatus{Hard: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("10")}}}
		k, _ := newTestK8s(t, KubernetesConfig{}, q)
		rep, err := k.Capacity(context.Background())
		if err != nil {
			t.Fatalf("Capacity: %v", err)
		}
		if rep.MemAvailableMB <= 0 {
			t.Fatalf("Capacity = %+v, want meminfo-derived memory", rep)
		}
	})
}

func TestPingChecksNamespaceAndRBAC(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: defaultSandboxNamespace}}
	allow := func(cs *fake.Clientset, deny string) {
		cs.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			r := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
			attr := r.Spec.ResourceAttributes
			key := attr.Verb + " " + attr.Resource
			if attr.Subresource != "" {
				key += "/" + attr.Subresource
			}
			r.Status.Allowed = key != deny
			if !r.Status.Allowed {
				r.Status.Reason = "no role binding"
			}
			return true, r, nil
		})
	}
	t.Run("ok", func(t *testing.T) {
		k, cs := newTestK8s(t, KubernetesConfig{}, ns)
		allow(cs, "")
		if err := k.Ping(context.Background()); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	})
	t.Run("missing namespace", func(t *testing.T) {
		k, cs := newTestK8s(t, KubernetesConfig{})
		allow(cs, "")
		if err := k.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), defaultSandboxNamespace) {
			t.Fatalf("Ping = %v, want namespace error", err)
		}
	})
	t.Run("exec denied", func(t *testing.T) {
		k, cs := newTestK8s(t, KubernetesConfig{}, ns)
		allow(cs, "create pods/exec")
		if err := k.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "pods/exec") {
			t.Fatalf("Ping = %v, want pods/exec denial", err)
		}
	})
}

func TestParseImageRef(t *testing.T) {
	tests := []struct {
		in   string
		want imageRef
		url  string
	}{
		{"ghcr.io/timothy-agent/timothy-sandbox:0.1.0", imageRef{"ghcr.io", "timothy-agent/timothy-sandbox", "0.1.0"}, "https://ghcr.io/v2/timothy-agent/timothy-sandbox/manifests/0.1.0"},
		{"ghcr.io/a/b@sha256:abc", imageRef{"ghcr.io", "a/b", "sha256:abc"}, "https://ghcr.io/v2/a/b/manifests/sha256:abc"},
		{"timothy-sandbox", imageRef{"registry-1.docker.io", "library/timothy-sandbox", "latest"}, "https://registry-1.docker.io/v2/library/timothy-sandbox/manifests/latest"},
		{"user/img:tag", imageRef{"registry-1.docker.io", "user/img", "tag"}, "https://registry-1.docker.io/v2/user/img/manifests/tag"},
		{"localhost:5001/timothy-sandbox:dev", imageRef{"localhost:5001", "timothy-sandbox", "dev"}, "http://localhost:5001/v2/timothy-sandbox/manifests/dev"},
		{"registry.example.com:5000/team/img", imageRef{"registry.example.com:5000", "team/img", "latest"}, "https://registry.example.com:5000/v2/team/img/manifests/latest"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseImageRef(tc.in)
			if err != nil {
				t.Fatalf("parseImageRef: %v", err)
			}
			if got != tc.want {
				t.Fatalf("parseImageRef = %+v, want %+v", got, tc.want)
			}
			if got.manifestURL() != tc.url {
				t.Fatalf("manifestURL = %s, want %s", got.manifestURL(), tc.url)
			}
		})
	}
	if _, err := parseImageRef(""); err == nil {
		t.Fatal("empty reference accepted")
	}
}

func TestProbeManifestTokenFlow(t *testing.T) {
	var tokenHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			tokenHits++
			if r.URL.Query().Get("scope") != "repository:a/b:pull" {
				t.Errorf("scope = %q", r.URL.Query().Get("scope"))
			}
			_, _ = io.WriteString(w, `{"token":"anon"}`)
		case r.URL.Path == "/v2/a/b/manifests/ok" && r.Method == http.MethodHead:
			if r.Header.Get("Authorization") != "Bearer anon" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="http://%s/token",service="reg",scope="repository:a/b:pull"`, r.Host))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v2/a/b/manifests/missing":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/v2/a/b/manifests/private":
			w.Header().Set("WWW-Authenticate", `Basic realm="private"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://") // 127.0.0.1:port, so the probe speaks http
	cache := &imageCheckCache{client: srv.Client()}
	ctx := context.Background()
	if err := cache.check(ctx, host+"/a/b:ok"); err != nil {
		t.Fatalf("check(ok): %v", err)
	}
	if err := cache.check(ctx, host+"/a/b:ok"); err != nil || tokenHits != 1 {
		t.Fatalf("second check: err=%v tokenHits=%d, want cached success", err, tokenHits)
	}
	for _, tag := range []string{"missing", "private"} {
		if err := (&imageCheckCache{client: srv.Client()}).check(ctx, host+"/a/b:"+tag); err == nil {
			t.Errorf("check(%s) = nil, want error", tag)
		}
	}
}

func TestKubernetesSkipImageCheck(t *testing.T) {
	k, _ := newTestK8s(t, KubernetesConfig{SkipImageCheck: true, Image: "nowhere.invalid/x:y"})
	if err := k.CheckImage(context.Background()); err != nil {
		t.Fatalf("CheckImage with skip = %v", err)
	}
}

var _ Backend = (*Kubernetes)(nil)
