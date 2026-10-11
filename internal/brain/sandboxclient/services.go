package sandboxclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrServicesUnsupported is sandboxd's 501 for service calls: its
// backend (Kubernetes, for now) runs no service containers (D-159).
var ErrServicesUnsupported = errors.New("sandboxclient: the sandbox backend does not run service containers")

// ServiceSpec mirrors sandboxd's PUT /v1/services/{id} body.
type ServiceSpec struct {
	Command  []string          `json:"command"`
	Env      map[string]string `json:"env,omitempty"`
	Internet bool              `json:"internet"`
}

// ServiceStatus mirrors sandboxd's report for one service.
type ServiceStatus struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	State     string `json:"state"`
	Restarts  int    `json:"restarts"`
	LastError string `json:"last_error,omitempty"`
	Tail      string `json:"tail,omitempty"`
}

// EnsureService starts or keeps service id with spec.
func (c *Client) EnsureService(ctx context.Context, id string, spec ServiceSpec) (ServiceStatus, error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return ServiceStatus{}, fmt.Errorf("sandboxclient: encode service: %w", err)
	}
	var st ServiceStatus
	err = c.serviceCall(ctx, http.MethodPut, "/v1/services/"+id, bytes.NewReader(body), http.StatusOK, &st)
	return st, err
}

// RemoveService removes service id; removing an absent one succeeds.
func (c *Client) RemoveService(ctx context.Context, id string) error {
	return c.serviceCall(ctx, http.MethodDelete, "/v1/services/"+id, nil, http.StatusNoContent, nil)
}

// ListServices reports every service sandboxd runs for this instance.
func (c *Client) ListServices(ctx context.Context) ([]ServiceStatus, error) {
	var out struct {
		Services []ServiceStatus `json:"services"`
	}
	err := c.serviceCall(ctx, http.MethodGet, "/v1/services", nil, http.StatusOK, &out)
	return out.Services, err
}

func (c *Client) serviceCall(ctx context.Context, method, path string, body io.Reader, want int, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("sandboxclient: request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sandboxclient: sandboxd unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotImplemented {
		return ErrServicesUnsupported
	}
	if resp.StatusCode != want {
		var e struct {
			Message string `json:"message"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			return fmt.Errorf("sandboxclient: sandboxd http %d: %s", resp.StatusCode, e.Message)
		}
		return fmt.Errorf("sandboxclient: sandboxd http %d: %s", resp.StatusCode, string(raw))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("sandboxclient: decode: %w", err)
	}
	return nil
}
