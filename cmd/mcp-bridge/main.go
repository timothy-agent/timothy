// Command mcp-bridge runs a stdio MCP server and serves it over
// streamable HTTP on :8090 (D-161, issue #1207). It exits non-zero when
// the server exits, so the service container's restart policy restarts
// both.
//
//	MCP_BRIDGE_TOKEN=... mcp-bridge -- <command> [args...]
package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/SumonMSelim/timothy/internal/mcpbridge"
)

const (
	tokenEnv = "MCP_BRIDGE_TOKEN" //nolint:gosec // G101: an env var name, not a credential.
	addr     = ":8090"
	// stopGrace is how long a signalled server gets before it is killed.
	stopGrace = 5 * time.Second
)

func main() {
	ln, err := net.Listen("tcp", addr) //nolint:gosec // G102: brain reaches the service container over its network.
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mcp-bridge: listen: %v\n", err)
		os.Exit(1)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	os.Exit(run(os.Args[1:], os.Getenv(tokenEnv), ln, os.Stderr, stop))
}

// run starts the server, serves it on ln and returns the exit code: 1
// when the server exits or its stdout ends, 0 after a stop signal, 2
// on bad usage.
func run(args []string, token string, ln net.Listener, stderr io.Writer, stop <-chan os.Signal) int {
	defer func() { _ = ln.Close() }()
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mcp-bridge -- <command> [args...]")
		return 2
	}
	if token == "" {
		_, _ = fmt.Fprintf(stderr, "mcp-bridge: %s is required\n", tokenEnv)
		return 2
	}

	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // G204: running the configured server is the bridge's job.
	cmd.Env = childEnv(os.Environ())
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp-bridge: %v\n", err)
		return 1
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp-bridge: %v\n", err)
		return 1
	}
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp-bridge: start %s: %v\n", args[0], err)
		return 1
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	b := mcpbridge.New(token, stdin, stdout, stderr)
	srv := &http.Server{Handler: b, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_, _ = fmt.Fprintf(stderr, "mcp-bridge: serve: %v\n", err)
		}
	}()
	defer func() { _ = srv.Close() }()

	select {
	case err := <-exited:
		_, _ = fmt.Fprintf(stderr, "mcp-bridge: server exited: %v\n", err)
		return 1
	case <-b.Done():
		_, _ = fmt.Fprintf(stderr, "mcp-bridge: %v\n", b.Err())
		_ = cmd.Process.Kill()
		<-exited
		return 1
	case sig := <-stop:
		_ = cmd.Process.Signal(sig)
		select {
		case <-exited:
		case <-time.After(stopGrace):
			_ = cmd.Process.Kill()
			<-exited
		}
		return 0
	}
}

// childEnv is the bridge's environment minus the bridge token, which
// the MCP server has no use for.
func childEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, tokenEnv+"=") {
			out = append(out, kv)
		}
	}
	return out
}
