package sandboxclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestEnsureServiceSendsSpec(t *testing.T) {
	var gotPath, gotMethod string
	var got ServiceSpec
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		_, _ = w.Write([]byte(`{"id":"svc","address":"http://timothy-service-svc:8090","state":"running","restarts":1}`))
	})
	spec := ServiceSpec{Command: []string{"mcp-bridge", "--", "srv"}, Env: map[string]string{"K": "v"}, Internet: true}
	st, err := c.EnsureService(t.Context(), "svc", spec)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/v1/services/svc" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if strings.Join(got.Command, " ") != "mcp-bridge -- srv" || got.Env["K"] != "v" || !got.Internet {
		t.Fatalf("spec sent = %+v", got)
	}
	if st.State != "running" || st.Restarts != 1 || st.Address != "http://timothy-service-svc:8090" {
		t.Fatalf("status = %+v", st)
	}
}

func TestServiceErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantIs  error
		wantMsg string
	}{
		{"kubernetes", http.StatusNotImplemented, `{"error":"unsupported","message":"x"}`, ErrServicesUnsupported, ""},
		{"invalid spec", http.StatusBadRequest, `{"error":"bad_request","message":"sandbox: invalid service spec: command is required"}`, nil, "command is required"},
		{"plain body", http.StatusInternalServerError, `boom`, nil, "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := sandboxdStub(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := c.EnsureService(t.Context(), "svc", ServiceSpec{})
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("err = %v, want %v", err, tc.wantIs)
			}
			if tc.wantMsg != "" && (err == nil || !strings.Contains(err.Error(), tc.wantMsg)) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}

func TestRemoveAndListServices(t *testing.T) {
	var removed string
	c := sandboxdStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			removed = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/services":
			_, _ = w.Write([]byte(`{"services":[{"id":"a","state":"running"},{"id":"b","state":"stopped","last_error":"exited with code 1"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if err := c.RemoveService(t.Context(), "a"); err != nil || removed != "/v1/services/a" {
		t.Fatalf("remove: %v, path %q", err, removed)
	}
	list, err := c.ListServices(t.Context())
	if err != nil || len(list) != 2 || list[1].LastError != "exited with code 1" {
		t.Fatalf("list = %+v, %v", list, err)
	}
}
