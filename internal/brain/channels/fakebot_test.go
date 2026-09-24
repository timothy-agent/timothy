package channels

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeToken = "123456:SECRET-TOKEN"

// botCall is one recorded Bot API request.
type botCall struct {
	Method string
	Body   map[string]any
}

// fakeBot is an httptest Bot API: getUpdates serves scripted batches
// once each, then blocks until the request ends.
type fakeBot struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	calls   []botCall
	batches [][]map[string]any
	offsets []int64
	nextID  int64
	// pollsSinceServe counts polls after the last scripted batch went
	// out; the poll loop only polls again once a batch is handled.
	pollsSinceServe int
	// editErr, when set, fails editMessageText with this description.
	editErr string
}

func newFakeBot(t *testing.T) *fakeBot {
	t.Helper()
	f := &fakeBot{t: t, nextID: 100}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBot) serve(w http.ResponseWriter, r *http.Request) {
	prefix := "/bot" + fakeToken + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, prefix)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.calls = append(f.calls, botCall{Method: method, Body: body})
	f.mu.Unlock()
	switch method {
	case "getMe":
		writeOK(w, map[string]any{"id": 999, "is_bot": true, "username": "timothy_test_bot"})
	case "getUpdates":
		f.mu.Lock()
		off, _ := body["offset"].(float64)
		f.offsets = append(f.offsets, int64(off))
		var batch []map[string]any
		if len(f.batches) > 0 {
			batch, f.batches = f.batches[0], f.batches[1:]
			f.pollsSinceServe = 0
		} else {
			f.pollsSinceServe++
		}
		f.mu.Unlock()
		if batch != nil {
			writeOK(w, batch)
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
			writeOK(w, []any{})
		}
	case "sendMessage":
		f.mu.Lock()
		f.nextID++
		id := f.nextID
		f.mu.Unlock()
		writeOK(w, map[string]any{"message_id": id})
	case "editMessageText":
		f.mu.Lock()
		e := f.editErr
		f.mu.Unlock()
		if e != "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": e})
			return
		}
		writeOK(w, true)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
	}
}

func writeOK(w http.ResponseWriter, result any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// callsOf returns the recorded calls of one method.
func (f *fakeBot) callsOf(method string) []botCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []botCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeBot) api() *botAPI {
	return &botAPI{http: f.srv.Client(), base: f.srv.URL, resolve: fakeResolve, ref: "TELEGRAM_BOT"}
}

func fakeResolve(_ context.Context, ref string) (string, error) {
	if ref == "TELEGRAM_BOT" {
		return fakeToken, nil
	}
	return "", io.EOF
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
