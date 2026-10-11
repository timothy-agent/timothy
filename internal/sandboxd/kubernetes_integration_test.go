//go:build integration

package sandboxd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// TestKubernetesLifecycle runs the Docker lifecycle suite's cases
// against a real cluster (make kind-up; KUBECONFIG and
// SANDBOXD_K8S_TEST_IMAGE set). The namespace it creates carries Pod
// Security enforce=restricted, so a pod spec that drifts from the
// restricted profile fails here before any operator sees it.
func TestKubernetesLifecycle(t *testing.T) {
	image := os.Getenv("SANDBOXD_K8S_TEST_IMAGE")
	if image == "" {
		t.Skip("SANDBOXD_K8S_TEST_IMAGE not set; skipping kubernetes integration test")
	}
	restCfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ns := "timothy-sbtest-" + rand.String(6)
	nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{
		"pod-security.kubernetes.io/enforce": "restricted",
		"pod-security.kubernetes.io/warn":    "restricted",
	}}}
	if _, err := cs.CoreV1().Namespaces().Create(ctx, nsObj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = cs.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: ns},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
		},
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pvc: %v", err)
	}

	missionID := "0b4a2d7c-9d8e-4f10-a4b7-" + rand.String(12)
	missionID = strings.ToLower(missionID[:36])
	missionDir := "/workspace/missions/coding/" + missionID
	provisionMissionDir(t, ctx, cs, ns, image, missionDir)

	k, err := newKubernetes(cs, restCfg, KubernetesConfig{
		Image: image, Namespace: ns, Owner: "sbtest", WorkspacePVC: "workspace", SkipImageCheck: true, ReadyTimeout: 3 * time.Minute,
	}, testLogger())
	if err != nil {
		t.Fatalf("newKubernetes: %v", err)
	}
	t.Cleanup(func() { _ = k.Remove(context.Background(), missionID) })

	if err := k.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	t.Run("exec runs and captures output", func(t *testing.T) {
		var out bytes.Buffer
		code, err := k.ExecEnv(ctx, missionID, missionDir, "echo hello; echo world >&2", nil, 30*time.Second, &out)
		if err != nil || code != 0 {
			t.Fatalf("exec: code=%d err=%v out=%q", code, err, out.String())
		}
		if !strings.Contains(out.String(), "hello") || !strings.Contains(out.String(), "world") {
			t.Fatalf("output = %q, want combined stdout+stderr", out.String())
		}
	})
	t.Run("non-zero exit is reported as a code, not an error", func(t *testing.T) {
		var out bytes.Buffer
		code, err := k.ExecEnv(ctx, missionID, missionDir, "exit 7", nil, 30*time.Second, &out)
		if err != nil || code != 7 {
			t.Fatalf("exec: code=%d err=%v", code, err)
		}
	})
	t.Run("timeout is reported as an error", func(t *testing.T) {
		var out bytes.Buffer
		_, err := k.ExecEnv(ctx, missionID, missionDir, "sleep 20", nil, 1*time.Second, &out)
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("exec(sleep 20, 1s) err = %v, want ErrTimeout", err)
		}
	})
	t.Run("runs as nobody, writes the workspace, no brain secrets leak", func(t *testing.T) {
		var out bytes.Buffer
		// The kubelet always injects KUBERNETES_SERVICE_HOST and friends
		// (enableServiceLinks only covers other services); they name the
		// API server, which the sandbox NetworkPolicy blocks and the
		// missing ServiceAccount token cannot authenticate to anyway.
		script := "id -u; touch probe && echo wrote; env; test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token && echo no-token; ls /workspace/missions/coding | wc -l"
		code, err := k.ExecEnv(ctx, missionID, missionDir, script, map[string]string{"NO_COLOR": "1"}, 30*time.Second, &out)
		if err != nil || code != 0 {
			t.Fatalf("exec: code=%d err=%v out=%q", code, err, out.String())
		}
		s := out.String()
		if !strings.HasPrefix(s, "65534\n") {
			t.Errorf("uid line = %q, want 65534", strings.SplitN(s, "\n", 2)[0])
		}
		for _, want := range []string{"wrote\n", "NO_COLOR=1\n", "no-token\n"} {
			if !strings.Contains(s, want) {
				t.Errorf("output lacks %q:\n%s", want, s)
			}
		}
		for _, leak := range []string{"DATABASE_URL", "TIMOTHY_MASTER_KEY", "TIMOTHY_API_TOKEN"} {
			if strings.Contains(s, leak) {
				t.Errorf("env leaks %s:\n%s", leak, s)
			}
		}
		if !strings.HasSuffix(strings.TrimSpace(s), "1") {
			t.Errorf("pod sees more than its own mission dir under /workspace/missions/coding:\n%s", s)
		}
	})
	t.Run("backgrounded grandchild does not hang the call", func(t *testing.T) {
		var out bytes.Buffer
		start := time.Now()
		code, err := k.ExecEnv(ctx, missionID, missionDir, "echo started; sleep 15 &", nil, 10*time.Second, &out)
		if err != nil || code != 0 {
			t.Fatalf("exec: code=%d err=%v out=%q", code, err, out.String())
		}
		if time.Since(start) > 8*time.Second {
			t.Fatalf("took %s; grandchild held the exec stream", time.Since(start))
		}
	})
	t.Run("pod persists between exec calls", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := k.ExecEnv(ctx, missionID, missionDir, "echo marker > /tmp/marker", nil, 30*time.Second, &out); err != nil {
			t.Fatalf("exec: %v", err)
		}
		out.Reset()
		code, err := k.ExecEnv(ctx, missionID, missionDir, "cat /tmp/marker", nil, 30*time.Second, &out)
		if err != nil || code != 0 || !strings.Contains(out.String(), "marker") {
			t.Fatalf("second exec: code=%d err=%v out=%q, want the same pod", code, err, out.String())
		}
	})
	t.Run("rootfs is read-only", func(t *testing.T) {
		var out bytes.Buffer
		code, _ := k.ExecEnv(ctx, missionID, missionDir, "touch /usr/bin/x", nil, 30*time.Second, &out)
		if code == 0 {
			t.Fatalf("wrote to /usr/bin; rootfs is not read-only")
		}
	})
	t.Run("List and Remove", func(t *testing.T) {
		ids, err := k.List(ctx)
		if err != nil || len(ids) != 1 || ids[0] != missionID {
			t.Fatalf("List = %v, %v", ids, err)
		}
		if err := k.Remove(ctx, missionID); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if err := k.waitGone(ctx, podName(missionID)); err != nil {
			t.Fatalf("pod still present: %v", err)
		}
	})
}

// provisionMissionDir does what brain's Provision does on the shared
// claim: creates the mission directory, owned by 65534, before any
// exec. It runs as a restricted pod so the test namespace's Pod
// Security label applies to it too.
func provisionMissionDir(t *testing.T, ctx context.Context, cs kubernetes.Interface, ns, image, missionDir string) {
	t.Helper()
	uid := int64(65534)
	yes, no := true, false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "provision", Namespace: ns},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: &no,
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: &uid, RunAsGroup: &uid, RunAsNonRoot: &yes, FSGroup: &uid,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Volumes: []corev1.Volume{{Name: "ws", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "workspace"}}}},
			Containers: []corev1.Container{{
				Name: "p", Image: image, Command: []string{"sh", "-c", fmt.Sprintf("mkdir -p %s/%s && id -u", missionDir, missionCacheDirName)},
				VolumeMounts: []corev1.VolumeMount{{Name: "ws", MountPath: "/workspace"}},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes,
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			}},
		},
	}
	if _, err := cs.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create provision pod: %v", err)
	}
	for {
		p, err := cs.CoreV1().Pods(ns).Get(ctx, "provision", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get provision pod: %v", err)
		}
		switch p.Status.Phase {
		case corev1.PodSucceeded:
			_ = cs.CoreV1().Pods(ns).Delete(ctx, "provision", metav1.DeleteOptions{})
			return
		case corev1.PodFailed:
			t.Fatalf("provision pod failed: %s %s", p.Status.Reason, p.Status.Message)
		}
		if _, msg, fatal := waitingFailure(p); fatal {
			t.Fatalf("provision pod cannot start: %s", msg)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("provision pod: %v", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
