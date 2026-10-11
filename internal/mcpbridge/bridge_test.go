package mcpbridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "secret-token"

// harness is a bridge whose stdio is scripted by the test: recv reads
// what the bridge wrote to the server's stdin, send writes the server's
// stdout.
type harness struct {
	bridge *Bridge
	url    string
	lines  chan map[string]json.RawMessage
	out    *io.PipeWriter
	logs   *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
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

func newHarness(t *testing.T) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := &harness{lines: make(chan map[string]json.RawMessage, 16), out: outW, logs: &syncBuffer{}}
	h.bridge = New(testToken, inW, outR, h.logs)
	go func() {
		sc := bufio.NewScanner(inR)
		for sc.Scan() {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("bridge wrote invalid JSON %q: %v", sc.Text(), err)
				continue
			}
			h.lines <- m
		}
	}()
	srv := httptest.NewServer(h.bridge)
	h.url = srv.URL + "/mcp"
	t.Cleanup(func() {
		srv.Close()
		_ = outW.Close()
		_ = inR.Close()
	})
	return h
}

func (h *harness) recv(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	select {
	case m := <-h.lines:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("server received nothing")
		return nil
	}
}

func (h *harness) send(t *testing.T, s string) {
	t.Helper()
	if _, err := io.WriteString(h.out, s); err != nil {
		t.Errorf("write server stdout: %v", err)
	}
}

// reply answers req with result as the server would.
func (h *harness) reply(t *testing.T, req map[string]json.RawMessage, result string) {
	t.Helper()
	h.send(t, `{"jsonrpc":"2.0","id":`+string(req["id"])+`,"result":`+result+"}\n")
}

func post(t *testing.T, url, token, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func TestBridgeMapsIDsAndEchoesSession(t *testing.T) {
	h := newHarness(t)
	go func() {
		req := h.recv(t)
		if got := string(req["method"]); got != `"tools/list"` {
			t.Errorf("method = %s", got)
		}
		if got := string(req["params"]); got != `{"cursor":"x"}` {
			t.Errorf("params = %s", got)
		}
		h.reply(t, req, `{"tools":[]}`)
		h.reply(t, h.recv(t), `{"n":2}`)
	}()

	resp, body := post(t, h.url, testToken, `{"jsonrpc":"2.0","id":"abc","method":"tools/list","params":{"cursor":"x"}}`)
	if resp.StatusCode != http.StatusOK || body != `{"id":"abc","jsonrpc":"2.0","result":{"tools":[]}}` {
		t.Fatalf("first: %d %s", resp.StatusCode, body)
	}
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", resp.Header)
	}
	resp, body = post(t, h.url, testToken, `{"jsonrpc":"2.0","id":1,"method":"x"}`)
	if body != `{"id":1,"jsonrpc":"2.0","result":{"n":2}}` || resp.Header.Get("Mcp-Session-Id") != sid {
		t.Fatalf("second: %s, session %q want %q", body, resp.Header.Get("Mcp-Session-Id"), sid)
	}
}

func TestBridgeForwardsServerErrors(t *testing.T) {
	h := newHarness(t)
	go func() {
		req := h.recv(t)
		h.send(t, `{"jsonrpc":"2.0","id":`+string(req["id"])+`,"error":{"code":-32602,"message":"bad"}}`+"\n")
	}()
	_, body := post(t, h.url, testToken, `{"jsonrpc":"2.0","id":5,"method":"tools/call"}`)
	if body != `{"error":{"code":-32602,"message":"bad"},"id":5,"jsonrpc":"2.0"}` {
		t.Fatalf("body = %s", body)
	}
}

// TestBridgeFramingOddChunks covers newline framing across arbitrary
// write boundaries, blank lines, and non-JSON log lines on stdout.
func TestBridgeFramingOddChunks(t *testing.T) {
	h := newHarness(t)
	go func() {
		req := h.recv(t)
		msg := "server starting\n\n" + `{"jsonrpc":"2.0","id":` + string(req["id"]) + `,"result":{"text":"a\nb"}}` + "\r\n"
		for i := 0; i < len(msg); i += 3 {
			h.send(t, msg[i:min(i+3, len(msg))])
		}
	}()
	_, body := post(t, h.url, testToken, `{"jsonrpc":"2.0","id":1,"method":"m"}`)
	if body != `{"id":1,"jsonrpc":"2.0","result":{"text":"a\nb"}}` {
		t.Fatalf("body = %s", body)
	}
	if !strings.Contains(h.logs.String(), "server starting") {
		t.Fatalf("log line not forwarded: %q", h.logs.String())
	}
}

func TestBridgeSerializesConcurrentRequests(t *testing.T) {
	h := newHarness(t)
	const n = 5
	go func() {
		for range n {
			req := h.recv(t)
			select {
			case extra := <-h.lines:
				t.Errorf("request %s reached the server while %s was in flight", extra["id"], req["id"])
			case <-time.After(20 * time.Millisecond):
			}
			h.reply(t, req, `{"echo":`+string(req["params"])+`}`)
		}
	}()
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			id := strconv.Itoa(i)
			_, body := post(t, h.url, testToken, `{"jsonrpc":"2.0","id":"`+id+`","method":"m","params":"`+id+`"}`)
			if want := `{"id":"` + id + `","jsonrpc":"2.0","result":{"echo":"` + id + `"}}`; body != want {
				t.Errorf("body = %s, want %s", body, want)
			}
		})
	}
	wg.Wait()
}

func TestBridgeToken(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		name, header string
	}{
		{"missing", ""},
		{"wrong", "Bearer nope"},
		{"wrong scheme", "Basic " + testToken},
		{"prefix only", "Bearer " + testToken[:4]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, h.url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"m"}`))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d", resp.StatusCode)
			}
		})
	}
	select {
	case m := <-h.lines:
		t.Fatalf("an unauthorized request reached the server: %v", m)
	default:
	}
}

func TestBridgeRejects(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"get stream", http.MethodGet, "/mcp", "", http.StatusMethodNotAllowed},
		{"delete session", http.MethodDelete, "/mcp", "", http.StatusMethodNotAllowed},
		{"other path", http.MethodPost, "/", `{"jsonrpc":"2.0","id":1,"method":"m"}`, http.StatusNotFound},
		{"batch", http.MethodPost, "/mcp", `[{"jsonrpc":"2.0","id":1,"method":"m"}]`, http.StatusBadRequest},
		{"not json", http.MethodPost, "/mcp", `nope`, http.StatusBadRequest},
		{"response not request", http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"result":{}}`, http.StatusBadRequest},
		{"too large", http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"m","params":"` + strings.Repeat("x", MaxBody) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, strings.TrimSuffix(h.url, "/mcp")+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// TestBridgeReplaysInitialize covers a client reconnecting: the server
// sees one initialize and one initialized notification, and a later
// initialize gets the first result under the new caller's id.
func TestBridgeReplaysInitialize(t *testing.T) {
	h := newHarness(t)
	go func() {
		req := h.recv(t)
		if string(req["method"]) != `"initialize"` {
			t.Errorf("first message = %s", req["method"])
		}
		h.reply(t, req, `{"protocolVersion":"2025-06-18","serverInfo":{"name":"toy"}}`)
	}()
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	initialized := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	if _, body := post(t, h.url, testToken, init); !strings.Contains(body, `"toy"`) {
		t.Fatalf("initialize = %s", body)
	}
	resp, _ := post(t, h.url, testToken, initialized)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification status = %d", resp.StatusCode)
	}
	if m := h.recv(t); string(m["method"]) != `"notifications/initialized"` {
		t.Fatalf("server got %v", m)
	}

	_, body := post(t, h.url, testToken, `{"jsonrpc":"2.0","id":"again","method":"initialize","params":{}}`)
	if body != `{"id":"again","jsonrpc":"2.0","result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"toy"}}}` {
		t.Fatalf("replayed initialize = %s", body)
	}
	post(t, h.url, testToken, initialized)
	post(t, h.url, testToken, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	select {
	case m := <-h.lines:
		t.Fatalf("server got a second handshake message: %v", m)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestBridgeAnswersServerRequests covers requests the server sends
// while a call is pending: ping gets an empty result, anything else
// method-not-found, and the call still completes.
func TestBridgeAnswersServerRequests(t *testing.T) {
	h := newHarness(t)
	go func() {
		req := h.recv(t)
		h.send(t, `{"jsonrpc":"2.0","id":"p","method":"ping"}`+"\n")
		if got := h.recv(t); string(got["id"]) != `"p"` || string(got["result"]) != `{}` {
			t.Errorf("ping reply = %v", got)
		}
		h.send(t, `{"jsonrpc":"2.0","id":99,"method":"roots/list"}`+"\n")
		if got := h.recv(t); string(got["id"]) != `99` || !strings.Contains(string(got["error"]), `-32601`) {
			t.Errorf("roots/list reply = %v", got)
		}
		h.send(t, `{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}`+"\n")
		h.reply(t, req, `{"ok":true}`)
	}()
	if _, body := post(t, h.url, testToken, `{"jsonrpc":"2.0","id":1,"method":"m"}`); body != `{"id":1,"jsonrpc":"2.0","result":{"ok":true}}` {
		t.Fatalf("body = %s", body)
	}
}

func TestBridgeServerGone(t *testing.T) {
	h := newHarness(t)
	go func() {
		h.recv(t)
		_ = h.out.Close()
	}()
	resp, _ := post(t, h.url, testToken, `{"jsonrpc":"2.0","id":1,"method":"m"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	select {
	case <-h.bridge.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed")
	}
	if h.bridge.Err() != ErrClosed {
		t.Fatalf("Err = %v", h.bridge.Err())
	}
}
