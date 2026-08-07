//go:build integration

package sandboxd

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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
	t.Cleanup(func() { _ = mgr.Remove(context.Background(), missionID) })

	t.Run("exec runs and captures output", func(t *testing.T) {
		var out bytes.Buffer
		code, err := mgr.Exec(ctx, missionID, "", "/workspace", "echo hello", 5*time.Second, &out)
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
		code, err := mgr.Exec(ctx, missionID, "", "/workspace", "exit 7", 5*time.Second, &out)
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if code != 7 {
			t.Errorf("exit code = %d, want 7", code)
		}
	})

	t.Run("timeout is reported as an error", func(t *testing.T) {
		var out bytes.Buffer
		_, err := mgr.Exec(ctx, missionID, "", "/workspace", "sleep 5", 1*time.Second, &out)
		if err == nil {
			t.Fatal("Exec: want timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("Exec error = %q, want a timeout message", err.Error())
		}
	})

	t.Run("runs as nobody, no brain secrets leak", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := mgr.Exec(ctx, missionID, "", "/workspace", "id -u; env", 5*time.Second, &out); err != nil {
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
		code, err := mgr.Exec(ctx, missionID, "", "/workspace", "echo started; sleep 15 &", 10*time.Second, &out)
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
		if _, err := mgr.Exec(ctx, missionID, "", "/workspace", "echo one > /tmp/marker", 5*time.Second, &out); err != nil {
			t.Fatalf("Exec (write): %v", err)
		}
		out.Reset()
		if _, err := mgr.Exec(ctx, missionID, "", "/workspace", "cat /tmp/marker", 5*time.Second, &out); err != nil {
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
