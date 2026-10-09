//go:build integration

package sandboxd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// brainUID is the uid/gid brain provisions mission directories as; a
// directory the tests create and leave behind must belong to it.
const brainUID = 65534

// newMissionDir creates /workspace/missions/coding/<missionID> with the
// .sandbox-cache dir brain provisions (D-131) and removes it when the
// test ends. Parent dirs the test had to create are removed too, or
// handed to brainUID when another mission dir still lives in them, so a
// run never leaves a root-owned missions/coding behind. Skips when the
// workspace volume is not writable here. Call it before registering the
// container cleanup so the container is removed first.
func newMissionDir(t *testing.T, missionID string) string {
	t.Helper()
	root := path.Join(workspaceMountPath, missionsDirName, "coding")
	missionDir := path.Join(root, missionID)
	var created []string // outermost first
	for _, d := range []string{path.Dir(root), root} {
		if _, err := os.Stat(d); errors.Is(err, fs.ErrNotExist) {
			created = append(created, d)
		}
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(missionDir); err != nil {
			t.Errorf("remove %s: %v", missionDir, err)
		}
		for _, d := range slices.Backward(created) {
			if err := os.Remove(d); err == nil || errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err := os.Chown(d, brainUID, brainUID); err != nil {
				t.Errorf("chown %s: %v", d, err)
			}
			if fi, err := os.Stat(d); err == nil {
				if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid == 0 {
					t.Errorf("%s is left root-owned", d)
				}
			}
		}
		if _, err := os.Stat(missionDir); err == nil {
			t.Errorf("%s still exists after cleanup", missionDir)
		}
	})
	if err := os.MkdirAll(path.Join(missionDir, missionCacheDirName), 0o777); err != nil { //nolint:gosec // test fixture inside the test container's own workspace mount
		t.Skipf("cannot create %s (workspace volume not mounted writable here): %v", missionDir, err)
	}
	return missionDir
}

// TestManagerLifecycle exercises the full path against a real Docker
// daemon: create, exec (including the timeout path and exit-code
// parity with builtin.Shell's contract), remove. Requires
// MISSION_SANDBOX_TEST_IMAGE (any small image with /bin/sh and
// coreutils' timeout — alpine works) and a reachable docker.sock.
func TestManagerLifecycle(t *testing.T) {
	image := os.Getenv("MISSION_SANDBOX_TEST_IMAGE")
	if image == "" {
		t.Skip("MISSION_SANDBOX_TEST_IMAGE not set; skipping sandbox integration test")
	}
	ctx := context.Background()
	mgr, err := NewManager(ctx, image, testLogger())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	missionID := "it-" + time.Now().UTC().Format("20060102-150405.000000000")
	// D-107 mounts the mission's own workspace subdirectory, which
	// Docker requires to exist; brain provisions it before any exec, so
	// the test stands in for that here.
	missionDir := newMissionDir(t, missionID)
	t.Cleanup(func() { _ = mgr.Remove(context.Background(), missionID) })

	t.Run("exec runs and captures output", func(t *testing.T) {
		var out bytes.Buffer
		code, err := mgr.Exec(ctx, missionID, missionDir, "echo hello", 5*time.Second, &out)
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if got := strings.TrimSpace(out.String()); got != "hello" {
			t.Errorf("output = %q, want hello", got)
		}
	})

	t.Run("non-zero exit is reported as a code, not an error", func(t *testing.T) {
		var out bytes.Buffer
		code, err := mgr.Exec(ctx, missionID, missionDir, "exit 7", 5*time.Second, &out)
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if code != 7 {
			t.Errorf("exit code = %d, want 7", code)
		}
	})

	t.Run("timeout is reported as an error", func(t *testing.T) {
		var out bytes.Buffer
		_, err := mgr.Exec(ctx, missionID, missionDir, "sleep 5", 1*time.Second, &out)
		if err == nil {
			t.Fatal("Exec: want timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("Exec error = %q, want a timeout message", err.Error())
		}
	})

	t.Run("runs as nobody, no brain secrets leak", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := mgr.Exec(ctx, missionID, missionDir, "id -u; env", 5*time.Second, &out); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		got := out.String()
		if !strings.Contains(got, "65534") {
			t.Errorf("id -u output = %q, want it to contain 65534", got)
		}
		for _, leaked := range []string{"DATABASE_URL", "TIMOTHY_MASTER_KEY", "TIMOTHY_API_TOKEN", "AWS_"} {
			if strings.Contains(got, leaked) {
				t.Errorf("sandbox env leaked %s:\n%s", leaked, got)
			}
		}
	})

	t.Run("backgrounded grandchild does not hang the call", func(t *testing.T) {
		// `sh -c "sleep 15 &"` exits immediately, but the backgrounded
		// sleep inherits the shell's stdout and holds the output stream
		// open — without the exec-exit poll + force-close, this call
		// would hang until the sleep finished. It must instead return
		// within the execStreamGrace window.
		var out bytes.Buffer
		start := time.Now()
		code, err := mgr.Exec(ctx, missionID, missionDir, "echo started; sleep 15 &", 10*time.Second, &out)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if !strings.Contains(out.String(), "started") {
			t.Errorf("output = %q, want it to contain the pre-background echo", out.String())
		}
		if elapsed > 8*time.Second {
			t.Errorf("Exec took %s — the grandchild's open stream hung the call", elapsed)
		}
	})

	t.Run("container persists between exec calls (reuse, not recreate)", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := mgr.Exec(ctx, missionID, missionDir, "echo one > /tmp/marker", 5*time.Second, &out); err != nil {
			t.Fatalf("Exec (write): %v", err)
		}
		out.Reset()
		if _, err := mgr.Exec(ctx, missionID, missionDir, "cat /tmp/marker", 5*time.Second, &out); err != nil {
			t.Fatalf("Exec (read): %v", err)
		}
		if got := strings.TrimSpace(out.String()); got != "one" {
			t.Errorf("second exec did not see first exec's write: got %q", got)
		}
	})

	t.Run("Remove tears the container down", func(t *testing.T) {
		if err := mgr.Remove(ctx, missionID); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		// Removing an already-gone container must not error.
		if err := mgr.Remove(ctx, missionID); err != nil {
			t.Fatalf("Remove (already gone): %v", err)
		}
	})
}

// TestCoreUlimitPreventsDump is the regression case for the core.234
// leak (issue #946): a process that dies on a dump-eligible signal,
// direct SIGSEGV or SIGXFSZ from a low fsize ulimit, must not leave a
// core file in the workdir.
func TestCoreUlimitPreventsDump(t *testing.T) {
	image := os.Getenv("MISSION_SANDBOX_TEST_IMAGE")
	if image == "" {
		t.Skip("MISSION_SANDBOX_TEST_IMAGE not set; skipping sandbox integration test")
	}
	ctx := context.Background()
	mgr, err := NewManager(ctx, image, testLogger())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	missionID := "it-core-" + time.Now().UTC().Format("20060102-150405.000000000")
	missionDir := newMissionDir(t, missionID)
	t.Cleanup(func() { _ = mgr.Remove(context.Background(), missionID) })

	assertNoCoreFiles := func(t *testing.T) {
		t.Helper()
		var out bytes.Buffer
		if _, err := mgr.Exec(ctx, missionID, missionDir, "ls -a", 5*time.Second, &out); err != nil {
			t.Fatalf("Exec (ls): %v", err)
		}
		for _, name := range strings.Fields(out.String()) {
			if strings.HasPrefix(name, "core") {
				t.Errorf("workdir contains %q after a dump-eligible death, want no core file", name)
			}
		}
	}

	t.Run("SIGSEGV does not dump", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := mgr.Exec(ctx, missionID, missionDir, "kill -SEGV $$", 5*time.Second, &out); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		assertNoCoreFiles(t)
	})

	t.Run("SIGXFSZ from a low ulimit -f does not dump", func(t *testing.T) {
		// A low per-exec fsize ulimit plus a write exceeding it raises
		// SIGXFSZ: the exact signal that produced the leaked core.234.
		var out bytes.Buffer
		if _, err := mgr.Exec(ctx, missionID, missionDir, "sh -c 'ulimit -f 1; yes > bigfile'", 5*time.Second, &out); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		assertNoCoreFiles(t)
	})
}

func TestPingAndCheckImage(t *testing.T) {
	image := os.Getenv("MISSION_SANDBOX_TEST_IMAGE")
	if image == "" {
		t.Skip("MISSION_SANDBOX_TEST_IMAGE not set; skipping sandbox integration test")
	}
	ctx := context.Background()
	mgr, err := NewManager(ctx, image, testLogger())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
	if err := mgr.CheckImage(ctx); err != nil {
		t.Errorf("CheckImage(%s): %v", image, err)
	}
}

func TestCheckImageMissingErrors(t *testing.T) {
	image := os.Getenv("MISSION_SANDBOX_TEST_IMAGE")
	if image == "" {
		t.Skip("MISSION_SANDBOX_TEST_IMAGE not set; skipping sandbox integration test")
	}
	ctx := context.Background()
	mgr, err := NewManager(ctx, "timothy-sandbox-test-image-that-does-not-exist:latest", testLogger())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.CheckImage(ctx); err == nil {
		t.Fatal("CheckImage: want an error for a nonexistent image, got nil")
	}
}

// TestTwoOwnersOnOneDaemon is the issue #1036 case on a real daemon
// (D-132): two Managers with different owners share it, and B can
// neither list, remove nor exec in A's container.
func TestTwoOwnersOnOneDaemon(t *testing.T) {
	image := os.Getenv("MISSION_SANDBOX_TEST_IMAGE")
	if image == "" {
		t.Skip("MISSION_SANDBOX_TEST_IMAGE not set; skipping sandbox integration test")
	}
	ctx := context.Background()
	run := time.Now().UTC().Format("20060102-150405.000000000")
	newMgr := func(owner string) *Manager {
		mgr, err := NewManager(ctx, image, testLogger())
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		mgr.owner = owner
		return mgr
	}
	a, b := newMgr("it-owner-a-"+run), newMgr("it-owner-b-"+run)
	missionID := "it-owner-" + run
	missionDir := newMissionDir(t, missionID)
	t.Cleanup(func() { _ = a.Remove(context.Background(), missionID) })
	var out bytes.Buffer
	if _, err := a.Exec(ctx, missionID, missionDir, "true", 5*time.Second, &out); err != nil {
		t.Fatalf("A Exec: %v", err)
	}

	if ids, err := b.List(ctx); err != nil || slices.Contains(ids, missionID) {
		t.Fatalf("B List = %v, %v; want no error and no A container", ids, err)
	}
	if err := b.Remove(ctx, missionID); err != nil {
		t.Fatalf("B Remove: %v", err)
	}
	if _, err := b.Exec(ctx, missionID, missionDir, "true", 5*time.Second, &out); !errors.Is(err, ErrForeignContainer) {
		t.Fatalf("B Exec err = %v, want ErrForeignContainer", err)
	}
	ids, err := a.List(ctx)
	if err != nil || !slices.Contains(ids, missionID) {
		t.Fatalf("A List = %v, %v; want A's container to survive B's Remove", ids, err)
	}
	if _, err := a.Exec(ctx, missionID, missionDir, "true", 5*time.Second, &out); err != nil {
		t.Fatalf("A Exec after B's Remove: %v", err)
	}
}
