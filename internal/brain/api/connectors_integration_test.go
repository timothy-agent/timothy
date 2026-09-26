//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
)

// TestConnectorsRepoEndpoints drives GET/POST .../repos through
// Manager.GitClient into the connector's gitprovider.Client, and maps
// its errors through failConnector.
func TestConnectorsRepoEndpoints(t *testing.T) {
	a, _, _ := testAPI(t, "tok", nil)
	var gotName string
	var gotPrivate bool
	src := &fakeGitHubSource{
		listReposFn: func(context.Context) ([]gitprovider.Repo, error) {
			return []gitprovider.Repo{{FullName: "octocat/hello-world"}}, nil
		},
		createRepoFn: func(_ context.Context, name string, private bool) (gitprovider.Repo, error) {
			gotName, gotPrivate = name, private
			if name == "taken" {
				return gitprovider.Repo{}, errors.New("name already exists")
			}
			return gitprovider.Repo{FullName: "octocat/" + name}, nil
		},
	}
	mgr := testConnectorsManager(t, src)
	id := createGitHubConnectorRow(t, mgr)
	m := mux(a)
	a.registerConnectors(m.Handle, mgr, nil, nil, nil)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		return w
	}

	t.Run("list", func(t *testing.T) {
		w := do("GET", "/v1/admin/connectors/"+id+"/repos", "")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body)
		}
		var out struct {
			Repos []gitprovider.Repo `json:"repos"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out.Repos) != 1 || out.Repos[0].FullName != "octocat/hello-world" {
			t.Fatalf("repos = %+v", out.Repos)
		}
	})

	t.Run("list empty is an array", func(t *testing.T) {
		prev := src.listReposFn
		src.listReposFn = func(context.Context) ([]gitprovider.Repo, error) { return nil, nil }
		t.Cleanup(func() { src.listReposFn = prev })
		w := do("GET", "/v1/admin/connectors/"+id+"/repos", "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"repos":[]`) {
			t.Fatalf("status = %d body = %s", w.Code, w.Body)
		}
	})

	t.Run("list error", func(t *testing.T) {
		prev := src.listReposFn
		src.listReposFn = func(context.Context) ([]gitprovider.Repo, error) { return nil, errors.New("upstream 500") }
		t.Cleanup(func() { src.listReposFn = prev })
		if w := do("GET", "/v1/admin/connectors/"+id+"/repos", ""); w.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", w.Code, w.Body)
		}
	})

	t.Run("unknown connector", func(t *testing.T) {
		if w := do("GET", "/v1/admin/connectors/00000000-0000-0000-0000-000000000000/repos", ""); w.Code != http.StatusNotFound {
			t.Fatalf("list status = %d, want 404: %s", w.Code, w.Body)
		}
		if w := do("POST", "/v1/admin/connectors/00000000-0000-0000-0000-000000000000/repos", `{"name":"x"}`); w.Code != http.StatusNotFound {
			t.Fatalf("create status = %d, want 404: %s", w.Code, w.Body)
		}
	})

	t.Run("create", func(t *testing.T) {
		w := do("POST", "/v1/admin/connectors/"+id+"/repos", `{"name":"brand-new","private":true}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body)
		}
		var repo gitprovider.Repo
		if err := json.Unmarshal(w.Body.Bytes(), &repo); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if repo.FullName != "octocat/brand-new" || gotName != "brand-new" || !gotPrivate {
			t.Fatalf("repo = %+v, name = %q, private = %v", repo, gotName, gotPrivate)
		}
	})

	t.Run("create error", func(t *testing.T) {
		if w := do("POST", "/v1/admin/connectors/"+id+"/repos", `{"name":"taken"}`); w.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", w.Code, w.Body)
		}
	})
}
