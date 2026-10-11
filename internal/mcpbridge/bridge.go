// Package mcpbridge serves one stdio MCP server over streamable HTTP
// (D-161, issue #1207): POST /mcp with JSON responses only (no SSE),
// one session shared by every client, a bearer token on every request.
// Requests are serialized onto the server's stdin; the bridge assigns
// its own JSON-RPC ids and maps responses back to the caller's id.
package mcpbridge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// MaxBody caps a request body.
const MaxBody = 1 << 20

// maxLine caps one newline-delimited message from the server; a longer
// line ends the session.
const maxLine = 16 << 20

// ErrClosed reports that the server's stdout ended.
var ErrClosed = errors.New("mcpbridge: server stdout closed")

// message is the subset of a JSON-RPC message the bridge routes on.
type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func (m message) hasID() bool { return len(m.ID) > 0 && string(m.ID) != "null" }

// Bridge is one stdio session behind an HTTP handler.
type Bridge struct {
	token     string
	sessionID string
	in        io.Writer
	logw      io.Writer

	writeMu sync.Mutex
	sem     chan struct{} // one request in flight on the stdio session
	nextID  atomic.Int64

	mu          sync.Mutex
	pending     map[int64]chan message
	initResult  json.RawMessage // first successful initialize, replayed after
	initialized bool            // notifications/initialized already forwarded

	done    chan struct{}
	err     error
	closeMu sync.Once
}

// New starts reading the server's stdout (out) and returns the bridge.
// in is the server's stdin; logw receives stdout lines that are not
// JSON.
func New(token string, in io.Writer, out io.Reader, logw io.Writer) *Bridge {
	b := &Bridge{
		token:     token,
		sessionID: rand.Text(),
		in:        in,
		logw:      logw,
		sem:       make(chan struct{}, 1),
		pending:   map[int64]chan message{},
		done:      make(chan struct{}),
	}
	go b.read(out)
	return b
}

// Done is closed when the server's stdout ends; Err says why.
func (b *Bridge) Done() <-chan struct{} { return b.done }

// Err is the reason Done closed.
func (b *Bridge) Err() error {
	<-b.done
	return b.err
}

func (b *Bridge) read(out io.Reader) {
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			_, _ = fmt.Fprintf(b.logw, "%s\n", line)
			continue
		}
		b.dispatch(m)
	}
	err := sc.Err()
	if err == nil {
		err = ErrClosed
	}
	b.closeMu.Do(func() {
		b.err = err
		close(b.done)
	})
}

// dispatch routes one server message: responses to their waiting call,
// server requests answered here (no client capabilities), notifications
// dropped.
func (b *Bridge) dispatch(m message) {
	switch {
	case m.Method != "" && m.hasID():
		reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}
		if m.Method == "ping" {
			reply["result"] = map[string]any{}
		} else {
			reply["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		raw, _ := json.Marshal(reply)
		_ = b.write(raw)
	case m.Method != "":
	default:
		var id int64
		if json.Unmarshal(m.ID, &id) != nil {
			return
		}
		b.mu.Lock()
		ch, ok := b.pending[id]
		delete(b.pending, id)
		b.mu.Unlock()
		if ok {
			ch <- m
		}
	}
}

func (b *Bridge) write(line []byte) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_, err := b.in.Write(append(line, '\n'))
	return err
}

// ServeHTTP implements the streamable HTTP transport's POST endpoint.
func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !b.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req map[string]json.RawMessage
	var m message
	if json.Unmarshal(body, &req) != nil || json.Unmarshal(body, &m) != nil || m.Method == "" {
		http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Mcp-Session-Id", b.sessionID)

	if !m.hasID() {
		if err := b.notify(m.Method, req); err != nil {
			http.Error(w, "server unavailable", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	res, err := b.call(r.Context(), m, req)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		http.Error(w, "server unavailable", http.StatusBadGateway)
		return
	}
	reply := map[string]json.RawMessage{"jsonrpc": json.RawMessage(`"2.0"`), "id": m.ID}
	if len(res.Error) > 0 {
		reply["error"] = res.Error
	} else {
		reply["result"] = res.Result
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		http.Error(w, "server sent an invalid response", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (b *Bridge) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(b.token)) == 1
}

// notify forwards a client notification. A repeat
// notifications/initialized (a client re-initializing against the
// replayed result) and notifications/cancelled (whose requestId is the
// client's, not the bridge's) are dropped.
func (b *Bridge) notify(method string, req map[string]json.RawMessage) error {
	switch method {
	case "notifications/cancelled":
		return nil
	case "notifications/initialized":
		b.mu.Lock()
		seen := b.initialized
		b.initialized = true
		b.mu.Unlock()
		if seen {
			return nil
		}
	}
	select {
	case <-b.done:
		return b.err
	default:
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return b.write(raw)
}

// call forwards one request under a bridge-assigned id and waits for
// its response. initialize runs against the server once; later
// initialize requests get the first result.
func (b *Bridge) call(ctx context.Context, m message, req map[string]json.RawMessage) (message, error) {
	if res, ok := b.cachedInit(m.Method); ok {
		return res, nil
	}
	select {
	case b.sem <- struct{}{}:
	case <-ctx.Done():
		return message{}, ctx.Err()
	case <-b.done:
		return message{}, b.err
	}
	defer func() { <-b.sem }()
	if res, ok := b.cachedInit(m.Method); ok {
		return res, nil
	}

	id := b.nextID.Add(1)
	ch := make(chan message, 1)
	b.mu.Lock()
	b.pending[id] = ch
	b.mu.Unlock()
	forget := func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}

	req["id"], _ = json.Marshal(id)
	raw, err := json.Marshal(req)
	if err != nil {
		forget()
		return message{}, err
	}
	if err := b.write(raw); err != nil {
		forget()
		return message{}, err
	}
	select {
	case res := <-ch:
		if m.Method == "initialize" && len(res.Error) == 0 {
			b.mu.Lock()
			b.initResult = res.Result
			b.mu.Unlock()
		}
		return res, nil
	case <-ctx.Done():
		forget()
		return message{}, ctx.Err()
	case <-b.done:
		forget()
		return message{}, b.err
	}
}

func (b *Bridge) cachedInit(method string) (message, bool) {
	if method != "initialize" {
		return message{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.initResult == nil {
		return message{}, false
	}
	return message{Result: b.initResult}, true
}
