package sandboxclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sandboxdStub(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestExecDecodesOutputChunksInOrder(t *testing.T) {
	t.Parallel()
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, chunk := range []string{"hello ", "world"} {
			_, _ = fmt.Fprintf(w, "event: output\ndata: %s\n\n", base64.StdEncoding.EncodeToString([]byte(chunk)))
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "event: exit\ndata: {\"exit_code\":0}\n\n")
		flusher.Flush()
	})

	var out bytes.Buffer
	code, err := c.Exec(t.Context(), "m1", "", "/workspace", "echo hi", 0, &out)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if out.String() != "hello world" {
		t.Errorf("output = %q, want %q", out.String(), "hello world")
	}
}

func TestExecNonZeroExitIsNotAnError(t *testing.T) {
	t.Parallel()
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "event: exit\ndata: {\"exit_code\":7}\n\n")
		w.(http.Flusher).Flush()
	})

	var out bytes.Buffer
	code, err := c.Exec(t.Context(), "m1", "", "/workspace", "exit 7", 0, &out)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

func TestExecOmitsEnvField(t *testing.T) {
	t.Parallel()
	var gotBody string
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "event: exit\ndata: {\"exit_code\":0}\n\n")
		w.(http.Flusher).Flush()
	})

	var out bytes.Buffer
	if _, err := c.Exec(t.Context(), "m1", "", "/workspace", "true", 0, &out); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if strings.Contains(gotBody, "\"env\"") {
		t.Errorf("request body = %q, want no env field when Exec is called without one", gotBody)
	}
}

func TestExecEnvMarshalsEnvField(t *testing.T) {
	t.Parallel()
	var gotBody string
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "event: exit\ndata: {\"exit_code\":0}\n\n")
		w.(http.Flusher).Flush()
	})

	var out bytes.Buffer
	env := map[string]string{"ANTHROPIC_API_KEY": "sk-test"}
	if _, err := c.ExecEnv(t.Context(), "m1", "", "/workspace", "true", env, 0, &out); err != nil {
		t.Fatalf("ExecEnv: %v", err)
	}
	var decoded struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(gotBody), &decoded); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if decoded.Env["ANTHROPIC_API_KEY"] != "sk-test" {
		t.Errorf("request env = %+v, want ANTHROPIC_API_KEY=sk-test", decoded.Env)
	}
}

func TestExecTimeoutErrorMessage(t *testing.T) {
	t.Parallel()
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "event: error\ndata: {\"code\":\"timeout\",\"exit_code\":124,\"message\":\"command timed out after 5s\"}\n\n")
		w.(http.Flusher).Flush()
	})

	var out bytes.Buffer
	code, err := c.Exec(t.Context(), "m1", "", "/workspace", "sleep 5", 0, &out)
	if err == nil {
		t.Fatal("Exec: want a timeout error, got nil")
	}
	if code != 124 {
		t.Errorf("exit code = %d, want 124", code)
	}
	const want = "command timed out after"
	if got := err.Error(); !strings.Contains(got, want) {
		t.Errorf("error = %q, want it to contain %q", got, want)
	}
}

func TestExecStreamCutWithoutTerminalIsInfraError(t *testing.T) {
	t.Parallel()
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "event: output\ndata: %s\n\n", base64.StdEncoding.EncodeToString([]byte("partial")))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})

	var out bytes.Buffer
	if _, err := c.Exec(t.Context(), "m1", "", "/workspace", "cmd", 0, &out); err == nil {
		t.Fatal("Exec: want an error when the stream ends without a terminal event, got nil")
	}
}

func TestExecNon200IsInfraError(t *testing.T) {
	t.Parallel()
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad_request","message":"invalid mission id"}`, http.StatusBadRequest)
	})

	var out bytes.Buffer
	if _, err := c.Exec(t.Context(), "not-a-uuid", "", "/workspace", "cmd", 0, &out); err == nil {
		t.Fatal("Exec: want an error for a non-200 sandboxd response, got nil")
	}
}

func TestSweepDeletesExactlyTerminalMissions(t *testing.T) {
	t.Parallel()
	var deleted []string
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"mission_ids":["m1","m2","m3"]}`))
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})

	terminal := map[string]bool{"m1": true, "m2": false, "m3": true}
	if err := c.Sweep(t.Context(), func(id string) bool { return terminal[id] }); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(deleted) != 2 || deleted[0] != "/v1/sandboxes/m1" || deleted[1] != "/v1/sandboxes/m3" {
		t.Fatalf("deleted = %v, want exactly [/v1/sandboxes/m1 /v1/sandboxes/m3]", deleted)
	}
}

func TestHealthPassthrough(t *testing.T) {
	t.Parallel()
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"degraded"}`))
	})

	if err := c.Health(t.Context()); err == nil {
		t.Fatal("Health: want an error for a degraded status, got nil")
	}
}

func TestHealthOK(t *testing.T) {
	t.Parallel()
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	if err := c.Health(t.Context()); err != nil {
		t.Fatalf("Health: %v", err)
	}
}

func TestCapacityAdmitted(t *testing.T) {
	t.Parallel()
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/capacity" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"admit":true,"mem_available_mb":4096,"running_sandboxes":1}`))
	})

	admit, reason, err := c.Capacity(t.Context())
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if !admit {
		t.Error("admit = false, want true")
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty when admitted", reason)
	}
}

func TestCapacityDenied(t *testing.T) {
	t.Parallel()
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"admit":false,"mem_available_mb":900,"running_sandboxes":3,"reason":"mem_available 900MB < floor 1024 + per-sandbox 768"}`))
	})

	admit, reason, err := c.Capacity(t.Context())
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if admit {
		t.Error("admit = true, want false")
	}
	if reason == "" {
		t.Error("reason = empty, want the denial reason surfaced")
	}
}

func TestCapacityNon200IsError(t *testing.T) {
	t.Parallel()
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"infra","message":"read meminfo failed"}`, http.StatusBadGateway)
	})

	if _, _, err := c.Capacity(t.Context()); err == nil {
		t.Fatal("Capacity: want an error for a non-200 sandboxd response, got nil")
	}
}
