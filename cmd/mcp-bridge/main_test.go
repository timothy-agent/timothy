package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func TestRunUsage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		token string
	}{
		{"no command", nil, "t"},
		{"only separator", []string{"--"}, "t"},
		{"no token", []string{"--", "true"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr syncBuffer
			if code := run(tc.args, tc.token, listen(t), &stderr, nil); code != 2 {
				t.Fatalf("code = %d, stderr %q", code, stderr.String())
			}
		})
	}
}

func TestRunExitsWhenServerExits(t *testing.T) {
	var stderr syncBuffer
	start := time.Now()
	code := run([]string{"--", "sh", "-c", "echo bye >&2; exit 0"}, "t", listen(t), &stderr, nil)
	if code != 1 {
		t.Fatalf("code = %d", code)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v to notice the server exit", d)
	}
	if !strings.Contains(stderr.String(), "bye") {
		t.Fatalf("server stderr not forwarded: %q", stderr.String())
	}
}

func TestRunStartFailure(t *testing.T) {
	var stderr syncBuffer
	if code := run([]string{"/nonexistent/mcp-server"}, "t", listen(t), &stderr, nil); code != 1 {
		t.Fatalf("code = %d", code)
	}
}

// toyServer is a stdio MCP server in sh: it answers the bridge's
// first and third messages (bridge ids 1 and 2) and reports whether it
// saw the bridge token.
const toyServer = `echo "toy: token=${MCP_BRIDGE_TOKEN:-unset}" >&2
read -r l; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"sh-toy"}}}'
read -r l
read -r l; printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"echo"}]}}'
while read -r l; do :; done`

func TestRunServesStdioServer(t *testing.T) {
	t.Setenv(tokenEnv, "bridge-secret")
	ln := listen(t)
	url := "http://" + ln.Addr().String() + "/mcp"
	var stderr syncBuffer
	stop := make(chan os.Signal, 1)
	done := make(chan int, 1)
	go func() { done <- run([]string{"--", "sh", "-c", toyServer}, "bridge-secret", ln, &stderr, stop) }()

	call := func(token, body string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, _ := call("wrong", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", code)
	}
	if code, body := call("bridge-secret", `{"jsonrpc":"2.0","id":7,"method":"initialize","params":{}}`); code != http.StatusOK || !strings.Contains(body, `"id":7`) || !strings.Contains(body, "sh-toy") {
		t.Fatalf("initialize = %d %s", code, body)
	}
	if code, _ := call("bridge-secret", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Fatalf("initialized status = %d", code)
	}
	if code, body := call("bridge-secret", `{"jsonrpc":"2.0","id":8,"method":"tools/list","params":{}}`); code != http.StatusOK || !strings.Contains(body, `"echo"`) {
		t.Fatalf("tools/list = %d %s", code, body)
	}

	stop <- syscall.SIGTERM
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("code after stop = %d, stderr %q", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bridge did not stop")
	}
	if !strings.Contains(stderr.String(), "toy: token=unset") {
		t.Fatalf("the server saw the bridge token or its stderr was lost: %q", stderr.String())
	}
}

func TestChildEnv(t *testing.T) {
	got := childEnv([]string{"PATH=/bin", tokenEnv + "=s", tokenEnv + "_OTHER=keep", "HOME=/h"})
	want := []string{"PATH=/bin", tokenEnv + "_OTHER=keep", "HOME=/h"}
	if !slices.Equal(got, want) {
		t.Fatalf("childEnv = %v", got)
	}
}
