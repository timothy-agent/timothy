package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
)

// nestedWidgets is the nested-group repo the gitlab tests address: the
// shape that proves the API path is escaped whole rather than split at
// the first slash.
var nestedWidgets = gitprovider.RepoRef{Owner: "acme/platform", Name: "widgets"}

// gitlabFakeServer builds a source pointed at a test server. Unlike the
// bitbucket harness there is no package-level base to swap: the API
// base comes from the connector's own base_url, which is exactly the
// self-managed path this exercises.
func gitlabFakeServer(t *testing.T, handler http.HandlerFunc) (*gitlabSource, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return gitlabSourceAt(t, srv, "", tokenResolveValue("secret-token")), srv
}

// tokenResolveValue is a Resolve returning a fixed token.
func tokenResolveValue(token string) Resolve {
	return func(context.Context, string) (string, error) { return token, nil }
}

// gitlabSourceAt builds a gitlab source whose base_url is srv's, with
// the http scheme httptest serves; extra is merged into the config.
func gitlabSourceAt(t *testing.T, srv *httptest.Server, namespace string, resolve Resolve) *gitlabSource {
	t.Helper()
	cfg := map[string]any{"base_url": srv.URL}
	if namespace != "" {
		cfg["namespace"] = namespace
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	src, err := GitLabBuilder(srv.Client())(t.Context(),
		Connector{Name: "gl", Kind: "gitlab", CredentialRef: "GL_TOKEN", Config: raw}, resolve)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	gl, ok := src.(*gitlabSource)
	if !ok {
		t.Fatalf("builder returned %T", src)
	}
	return gl
}

// httptest serves http, so the descriptor must follow the base URL's
// scheme rather than assume https, or every call here would fail.
func TestGitLabSourceUsesConfiguredBase(t *testing.T) {
	var gotPath, gotToken string
	src, srv := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.Path, r.Header.Get("PRIVATE-TOKEN")
		_, _ = w.Write([]byte(`{"username":"taki","name":"Taki","email":"t@x.com","id":7}`))
	})
	id, err := src.Identity(t.Context())
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if gotPath != "/api/v4/user" {
		t.Fatalf("path = %q, want /api/v4/user", gotPath)
	}
	// GitLab authenticates by PRIVATE-TOKEN, never a bearer.
	if gotToken != "secret-token" {
		t.Fatalf("PRIVATE-TOKEN = %q", gotToken)
	}
	if id.Login != "taki" || id.Email != "t@x.com" || id.Scopes != "access token" {
		t.Fatalf("Identity = %+v", id)
	}
	if !strings.HasPrefix(src.APIBase(), srv.URL) {
		t.Fatalf("APIBase = %q, want a %s prefix", src.APIBase(), srv.URL)
	}
}

func TestGitLabIdentityNoEmailFallsBackToNoreply(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"username":"taki","name":"Taki","id":7}`))
	})
	id, err := src.Identity(t.Context())
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if !strings.HasPrefix(id.Email, "7-taki@users.noreply.") {
		t.Fatalf("Email = %q", id.Email)
	}
}

func TestGitLabTestResolvesCredential(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"username":"taki","id":1}`))
	})
	if err := src.Test(t.Context()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if kind, email := src.AccountInfo(); kind != "gitlab" || email != "" {
		t.Fatalf("AccountInfo = %q/%q", kind, email)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// A resolver failure must never reach the network.
func TestGitLabResolveFailureMakesNoRequest(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	t.Cleanup(srv.Close)
	src := gitlabSourceAt(t, srv, "", func(context.Context, string) (string, error) {
		return "", errors.New("vault sealed")
	})
	if _, err := src.Identity(t.Context()); err == nil || !strings.Contains(err.Error(), "vault sealed") {
		t.Fatalf("err = %v", err)
	}
	if hits != 0 {
		t.Fatalf("made %d requests", hits)
	}
}

// The nested group path is one escaped path parameter, so the API sees
// acme%2Fplatform%2Fwidgets and not a split at the first slash.
func TestGitLabGetRepoEscapesNestedPath(t *testing.T) {
	var gotRaw string
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotRaw = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"path_with_namespace":"acme/platform/widgets","visibility":"private",
			"default_branch":"main","web_url":"https://gitlab.com/acme/platform/widgets",
			"http_url_to_repo":"https://gitlab.com/acme/platform/widgets.git","last_activity_at":"2026-01-02T03:04:05Z"}`))
	})
	repo, err := src.GetRepo(t.Context(), nestedWidgets)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if want := "/api/v4/projects/acme%2Fplatform%2Fwidgets"; gotRaw != want {
		t.Fatalf("escaped path = %q, want %q", gotRaw, want)
	}
	if repo.FullName != "acme/platform/widgets" || !repo.Private || repo.DefaultBranch != "main" {
		t.Fatalf("repo = %+v", repo)
	}
	if repo.PushedAt != "2026-01-02T03:04:05Z" || repo.CloneURL == "" {
		t.Fatalf("repo = %+v", repo)
	}
}

func TestGitLabGetRepoNotFound(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"404 Project Not Found"}`))
	})
	_, err := src.GetRepo(t.Context(), nestedWidgets)
	if !errors.Is(err, ErrRepoNotFound) {
		t.Fatalf("err = %v, want ErrRepoNotFound", err)
	}
}

func TestGitLabGetRepoErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{
			name: "unauthorized never echoes the token",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			wantErr: "token invalid or expired",
		},
		{
			name: "string message is surfaced",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"insufficient scope"}`))
			},
			wantErr: "insufficient scope",
		},
		{
			name: "error field is surfaced",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"bad request"}`))
			},
			wantErr: "bad request",
		},
		{
			name: "object message is surfaced verbatim",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":{"path":["taken"]}}`))
			},
			wantErr: "taken",
		},
		{
			name: "plain body is surfaced",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("upstream down"))
			},
			wantErr: "upstream down",
		},
		{
			name: "html body is dropped, status kept",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("<html>nope</html>"))
			},
			wantErr: "status 502",
		},
		{
			name: "undecodable success body errors",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("not json"))
			},
			wantErr: "decode response",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, _ := gitlabFakeServer(t, tc.handler)
			_, err := src.GetRepo(t.Context(), nestedWidgets)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("error leaked the token: %v", err)
			}
		})
	}
}

func TestGitLabListRepos(t *testing.T) {
	page := 0
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			var b strings.Builder
			b.WriteString(`[`)
			for i := range gitlabRepoPageLen {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"path_with_namespace":"acme/p%d","visibility":"public"}`, i)
			}
			b.WriteString(`]`)
			_, _ = w.Write([]byte(b.String()))
			return
		}
		_, _ = w.Write([]byte(`[{"path_with_namespace":"acme/platform/last","visibility":"private"}]`))
	})
	repos, err := src.ListRepos(t.Context())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != gitlabRepoPageLen+1 {
		t.Fatalf("got %d repos, want %d", len(repos), gitlabRepoPageLen+1)
	}
	last := repos[len(repos)-1]
	if last.FullName != "acme/platform/last" || !last.Private {
		t.Fatalf("last = %+v", last)
	}
	if repos[0].Private {
		t.Fatal("a public project must not be reported private")
	}
}

func TestGitLabListReposError(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := src.ListRepos(t.Context()); err == nil {
		t.Fatal("want error")
	}
}

func TestGitLabResolveRepoName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, namespace, arg, wantGroup, wantSlug string
		wantErr                                   bool
	}{
		{name: "group and project", arg: "acme/widgets", wantGroup: "acme", wantSlug: "widgets"},
		{name: "nested group survives", arg: "acme/platform/widgets", wantGroup: "acme/platform", wantSlug: "widgets"},
		{name: "deeply nested group survives", arg: "a/b/c/d/widgets", wantGroup: "a/b/c/d", wantSlug: "widgets"},
		{name: "bare slug falls back to namespace", namespace: "cfg", arg: "widgets", wantGroup: "cfg", wantSlug: "widgets"},
		{name: "bare slug with no namespace errors", arg: "widgets", wantErr: true},
		{name: "trimmed and de-slashed", namespace: "cfg", arg: "  /acme/widgets/  ", wantGroup: "acme", wantSlug: "widgets"},
		{name: "empty errors", arg: "", wantErr: true},
		{name: "slashes only errors", arg: "///", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &gitlabSource{name: "gl", namespace: tc.namespace}
			group, slug, err := s.resolveRepoName(tc.arg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveRepoName(%q) = %q/%q, want error", tc.arg, group, slug)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRepoName(%q): %v", tc.arg, err)
			}
			if group != tc.wantGroup || slug != tc.wantSlug {
				t.Fatalf("resolveRepoName(%q) = %q/%q, want %q/%q", tc.arg, group, slug, tc.wantGroup, tc.wantSlug)
			}
		})
	}
}

// CreateRepo resolves the nested group to a namespace id first, so the
// project lands in the group rather than the token's user namespace.
func TestGitLabCreateRepo(t *testing.T) {
	var nsPath string
	var body map[string]any
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v4/namespaces/") {
			nsPath = r.URL.EscapedPath()
			_, _ = w.Write([]byte(`{"id":42}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"path_with_namespace":"acme/platform/widgets","visibility":"private","default_branch":"main"}`))
	})
	repo, err := src.CreateRepo(t.Context(), "acme/platform/widgets", true)
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if want := "/api/v4/namespaces/acme%2Fplatform"; nsPath != want {
		t.Fatalf("namespace path = %q, want %q", nsPath, want)
	}
	if body["namespace_id"] != float64(42) || body["path"] != "widgets" || body["visibility"] != "private" {
		t.Fatalf("body = %+v", body)
	}
	if body["initialize_with_readme"] != true {
		t.Fatal("a new repo needs a default branch to clone")
	}
	if repo.FullName != "acme/platform/widgets" {
		t.Fatalf("repo = %+v", repo)
	}
}

func TestGitLabCreateRepoPublic(t *testing.T) {
	var body map[string]any
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v4/namespaces/") {
			_, _ = w.Write([]byte(`{"id":1}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"path_with_namespace":"acme/widgets","visibility":"public"}`))
	})
	if _, err := src.CreateRepo(t.Context(), "acme/widgets", false); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if body["visibility"] != "public" {
		t.Fatalf("visibility = %v", body["visibility"])
	}
}

func TestGitLabCreateRepoErrors(t *testing.T) {
	t.Run("bad name never reaches the network", func(t *testing.T) {
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
		t.Cleanup(srv.Close)
		src := gitlabSourceAt(t, srv, "", tokenResolveValue("t"))
		if _, err := src.CreateRepo(t.Context(), "widgets", true); err == nil {
			t.Fatal("want error")
		}
		if hits != 0 {
			t.Fatalf("made %d requests", hits)
		}
	})
	t.Run("namespace lookup failure", func(t *testing.T) {
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"404 Namespace Not Found"}`))
		})
		if _, err := src.CreateRepo(t.Context(), "acme/platform/widgets", true); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("namespace without an id", func(t *testing.T) {
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		})
		if _, err := src.CreateRepo(t.Context(), "acme/widgets", true); err == nil ||
			!strings.Contains(err.Error(), "no id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("create rejected", func(t *testing.T) {
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/v4/namespaces/") {
				_, _ = w.Write([]byte(`{"id":1}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":{"path":["has already been taken"]}}`))
		})
		if _, err := src.CreateRepo(t.Context(), "acme/widgets", true); err == nil ||
			!strings.Contains(err.Error(), "already been taken") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("undecodable create response", func(t *testing.T) {
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/v4/namespaces/") {
				_, _ = w.Write([]byte(`{"id":1}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("not json"))
		})
		if _, err := src.CreateRepo(t.Context(), "acme/widgets", true); err == nil {
			t.Fatal("want error")
		}
	})
}

// CreatePR posts to the escaped nested path and maps GitLab's "opened"
// onto the shared "open".
func TestGitLabCreatePR(t *testing.T) {
	var gotRaw string
	var body map[string]any
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotRaw = r.URL.EscapedPath()
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"iid":12,"state":"opened","web_url":"https://gitlab.com/acme/platform/widgets/-/merge_requests/12"}`))
	})
	pr, err := src.CreatePR(t.Context(), gitprovider.PRSpec{
		Repo: nestedWidgets, Title: "Fix", Head: "feat/x", Base: "main", Body: "why",
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if want := "/api/v4/projects/acme%2Fplatform%2Fwidgets/merge_requests"; gotRaw != want {
		t.Fatalf("path = %q, want %q", gotRaw, want)
	}
	if body["source_branch"] != "feat/x" || body["target_branch"] != "main" || body["title"] != "Fix" {
		t.Fatalf("body = %+v", body)
	}
	if pr.Number != 12 || pr.State != "open" || pr.HTMLURL == "" {
		t.Fatalf("pr = %+v", pr)
	}
}

// A duplicate head returns the open merge request instead of erroring,
// so a re-push through the same endpoint is idempotent.
func TestGitLabCreatePRReturnsExisting(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":["Another open merge request already exists"]}`))
			return
		}
		_, _ = w.Write([]byte(`[{"iid":9,"state":"opened","web_url":"https://gitlab.com/x/-/merge_requests/9"}]`))
	})
	pr, err := src.CreatePR(t.Context(), gitprovider.PRSpec{Repo: nestedWidgets, Head: "feat/x", Base: "main"})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if pr.Number != 9 || pr.State != "open" {
		t.Fatalf("pr = %+v", pr)
	}
}

func TestGitLabCreatePRErrors(t *testing.T) {
	t.Run("hard failure is not retried as a lookup", func(t *testing.T) {
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if _, err := src.CreatePR(t.Context(), gitprovider.PRSpec{Repo: nestedWidgets}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("conflict with no existing mr keeps the create error", func(t *testing.T) {
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"conflict"}`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		})
		if _, err := src.CreatePR(t.Context(), gitprovider.PRSpec{Repo: nestedWidgets}); err == nil ||
			!strings.Contains(err.Error(), "conflict") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("conflict and a failing lookup reports both", func(t *testing.T) {
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		})
		if _, err := src.CreatePR(t.Context(), gitprovider.PRSpec{Repo: nestedWidgets}); err == nil ||
			!strings.Contains(err.Error(), "could not fetch existing") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("undecodable create response", func(t *testing.T) {
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("not json"))
		})
		if _, err := src.CreatePR(t.Context(), gitprovider.PRSpec{Repo: nestedWidgets}); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestGitLabGetPRStates(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"iid":3,"state":"opened"}`, "open"},
		{`{"iid":3,"state":"opened","draft":true}`, "draft"},
		{`{"iid":3,"state":"merged"}`, "merged"},
		{`{"iid":3,"state":"closed"}`, "closed"},
		// A draft that is no longer open reports its real state.
		{`{"iid":3,"state":"merged","draft":true}`, "merged"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			})
			pr, err := src.GetPR(t.Context(), nestedWidgets, 3)
			if err != nil {
				t.Fatalf("GetPR: %v", err)
			}
			if pr.State != tc.want || pr.Number != 3 {
				t.Fatalf("pr = %+v, want state %q", pr, tc.want)
			}
		})
	}
}

func TestGitLabGetPRError(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := src.GetPR(t.Context(), nestedWidgets, 3); err == nil {
		t.Fatal("want error")
	}
}

func TestGitLabListPRs(t *testing.T) {
	t.Run("open renders one line per mr", func(t *testing.T) {
		var gotQuery string
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.Query().Get("state")
			_, _ = w.Write([]byte(`[
				{"iid":2,"state":"opened","title":"Second","author":{"username":"taki"},"source_branch":"b","target_branch":"main"},
				{"iid":1,"state":"opened","draft":true,"title":"First","author":{"username":"ana"},"source_branch":"a","target_branch":"main"}
			]`))
		})
		out, err := src.ListPRs(t.Context(), nestedWidgets, "open", 10)
		if err != nil {
			t.Fatalf("ListPRs: %v", err)
		}
		if gotQuery != "opened" {
			t.Fatalf("state query = %q, want opened", gotQuery)
		}
		want := "#2 [open] Second (taki) b -> main\n#1 [draft] First (ana) a -> main"
		if out != want {
			t.Fatalf("ListPRs =\n%q\nwant\n%q", out, want)
		}
	})
	t.Run("closed filters out still-open mrs", func(t *testing.T) {
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("state"); got != "all" {
				t.Errorf("state query = %q, want all", got)
			}
			_, _ = w.Write([]byte(`[
				{"iid":3,"state":"opened","title":"Open","author":{"username":"a"}},
				{"iid":2,"state":"merged","title":"Merged","author":{"username":"b"}},
				{"iid":1,"state":"closed","title":"Closed","author":{"username":"c"}}
			]`))
		})
		out, err := src.ListPRs(t.Context(), nestedWidgets, "closed", 10)
		if err != nil {
			t.Fatalf("ListPRs: %v", err)
		}
		if strings.Contains(out, "Open") {
			t.Fatalf("closed listing kept an open mr:\n%s", out)
		}
		if !strings.Contains(out, "Merged") || !strings.Contains(out, "Closed") {
			t.Fatalf("closed listing dropped a closed mr:\n%s", out)
		}
	})
	t.Run("all sends no state filter", func(t *testing.T) {
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Has("state") {
				t.Error("state=all must not pin a state")
			}
			_, _ = w.Write([]byte(`[]`))
		})
		out, err := src.ListPRs(t.Context(), nestedWidgets, "all", 10)
		if err != nil {
			t.Fatalf("ListPRs: %v", err)
		}
		if !strings.Contains(out, "no all pull requests in acme/platform/widgets") {
			t.Fatalf("ListPRs = %q", out)
		}
	})
	t.Run("error", func(t *testing.T) {
		src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if _, err := src.ListPRs(t.Context(), nestedWidgets, "open", 10); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestGitLabPRDescription(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"iid":12,"title":"Fix the widget","state":"opened",
			"author":{"username":"taki"},"source_branch":"feat/x","target_branch":"main","sha":"abc123",
			"detailed_merge_status":"mergeable","changes_count":"3",
			"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z",
			"web_url":"https://gitlab.com/acme/platform/widgets/-/merge_requests/12",
			"description":"  the body  "}`))
	})
	out, err := src.PRDescription(t.Context(), nestedWidgets, 12)
	if err != nil {
		t.Fatalf("PRDescription: %v", err)
	}
	for _, want := range []string{
		"#12 Fix the widget", "author: taki", "state: open",
		"head: feat/x (abc123)", "base: main",
		"mergeable_state: mergeable", "files changed: 3",
		"url: https://gitlab.com/acme/platform/widgets/-/merge_requests/12", "the body",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("PRDescription missing %q:\n%s", want, out)
		}
	}
}

// The optional fields are omitted rather than rendered empty.
func TestGitLabPRDescriptionSparse(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"iid":1,"title":"T","state":"opened","author":{"username":"a"}}`))
	})
	out, err := src.PRDescription(t.Context(), nestedWidgets, 1)
	if err != nil {
		t.Fatalf("PRDescription: %v", err)
	}
	if strings.Contains(out, "mergeable_state:") || strings.Contains(out, "files changed:") {
		t.Fatalf("sparse mr rendered empty fields:\n%s", out)
	}
}

func TestGitLabPRDescriptionError(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := src.PRDescription(t.Context(), nestedWidgets, 1); err == nil {
		t.Fatal("want error")
	}
}

// GitLab serves per-file hunks, so the unified headers are reassembled.
func TestGitLabPRDiff(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"old_path":"a.go","new_path":"a.go","diff":"@@ -1 +1 @@\n-old\n+new\n"},
			{"old_path":"new.go","new_path":"new.go","new_file":true,"diff":"@@ -0,0 +1 @@\n+hi\n"},
			{"old_path":"gone.go","new_path":"gone.go","deleted_file":true,"diff":"@@ -1 +0,0 @@\n-bye"}
		]`))
	})
	out, err := src.PRDiff(t.Context(), nestedWidgets, 1)
	if err != nil {
		t.Fatalf("PRDiff: %v", err)
	}
	for _, want := range []string{
		"diff --git a/a.go b/a.go", "--- a/a.go\n+++ b/a.go",
		"--- /dev/null\n+++ b/new.go",
		"--- a/gone.go\n+++ /dev/null",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("PRDiff missing %q:\n%s", want, out)
		}
	}
	// The last hunk had no trailing newline; one is added so the next
	// file's header starts on its own line.
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("PRDiff does not end in a newline:\n%q", out)
	}
}

func TestGitLabPRDiffEmpty(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	out, err := src.PRDiff(t.Context(), nestedWidgets, 1)
	if err != nil {
		t.Fatalf("PRDiff: %v", err)
	}
	if out != "empty diff" {
		t.Fatalf("PRDiff = %q", out)
	}
}

// A huge diff is cut with an explicit marker, never silently.
func TestGitLabPRDiffTruncates(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		huge := strings.Repeat("+x\n", gitlabDiffMaxBytes)
		body, _ := json.Marshal([]map[string]any{{"old_path": "a", "new_path": "a", "diff": huge}})
		_, _ = w.Write(body)
	})
	out, err := src.PRDiff(t.Context(), nestedWidgets, 1)
	if err != nil {
		t.Fatalf("PRDiff: %v", err)
	}
	if !strings.Contains(out, "[diff truncated at") || !strings.Contains(out, "more bytes omitted]") {
		t.Fatalf("PRDiff did not mark truncation:\n%s", out[max(0, len(out)-200):])
	}
}

func TestGitLabPRDiffError(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := src.PRDiff(t.Context(), nestedWidgets, 1); err == nil {
		t.Fatal("want error")
	}
}

func TestGitLabPRComments(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"body":"looks good","created_at":"2026-01-01T00:00:00Z","author":{"username":"ana"}},
			{"body":"assigned to x","created_at":"2026-01-01T00:30:00Z","system":true,"author":{"username":"bot"}},
			{"body":"nit here","created_at":"2026-01-02T00:00:00Z","author":{"username":"taki"},
			 "position":{"new_path":"a.go","new_line":42}},
			{"body":"on a deleted line","created_at":"2026-01-03T00:00:00Z","author":{"username":"taki"},
			 "position":{"old_path":"b.go","new_path":""}}
		]`))
	})
	out, err := src.PRComments(t.Context(), nestedWidgets, 1)
	if err != nil {
		t.Fatalf("PRComments: %v", err)
	}
	if strings.Contains(out, "assigned to x") {
		t.Fatalf("system note rendered as discussion:\n%s", out)
	}
	for _, want := range []string{
		"ana (comment):\nlooks good",
		"taki (review on a.go:42):\nnit here",
		"taki (review on b.go):\non a deleted line",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("PRComments missing %q:\n%s", want, out)
		}
	}
}

func TestGitLabPRCommentsEmpty(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"body":"x","system":true,"author":{"username":"bot"}}]`))
	})
	out, err := src.PRComments(t.Context(), nestedWidgets, 1)
	if err != nil {
		t.Fatalf("PRComments: %v", err)
	}
	if out != "no comments on acme/platform/widgets#1" {
		t.Fatalf("PRComments = %q", out)
	}
}

func TestGitLabPRCommentsError(t *testing.T) {
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := src.PRComments(t.Context(), nestedWidgets, 1); err == nil {
		t.Fatal("want error")
	}
}

// The chat tools take a nested repo argument on gitlab and reject one
// on the two-segment kinds: the parser is picked from the Descriptor's
// CapNestedOwner, never from a kind literal.
func TestGitLabToolsAcceptNestedRepoArg(t *testing.T) {
	var gotPath string
	src, _ := gitlabFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`[]`))
	})
	tl := toolByName(t, src, "list_pull_requests")
	if _, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"acme/platform/widgets"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(gotPath, "acme%2Fplatform%2Fwidgets") {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestRepoArgParserFollowsCapability(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		client gitprovider.Client
		nested bool
	}{
		{"gitlab nests", &gitlabSource{}, true},
		{"github does not", &githubSource{}, false},
		{"bitbucket does not", &bitbucketSource{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := repoArgParserFor(tc.client)("a/b/c")
			if tc.nested != (err == nil) {
				t.Fatalf("nested %v, err = %v", tc.nested, err)
			}
		})
	}
}

func TestParseNestedRepoArg(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, owner, name string
		wantErr         bool
	}{
		{in: "acme/widgets", owner: "acme", name: "widgets"},
		{in: "acme/platform/widgets", owner: "acme/platform", name: "widgets"},
		{in: " a/b/c/d ", owner: "a/b/c", name: "d"},
		{in: "widgets", wantErr: true},
		{in: "", wantErr: true},
		{in: "a//b", wantErr: true},
		{in: "https://gitlab.com/a/b", wantErr: true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			ref, err := parseNestedRepoArg(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && (ref.Owner != tc.owner || ref.Name != tc.name) {
				t.Fatalf("= %q/%q, want %q/%q", ref.Owner, ref.Name, tc.owner, tc.name)
			}
		})
	}
}

func TestGitLabBuilderValidation(t *testing.T) {
	t.Parallel()
	t.Run("credential_ref is required", func(t *testing.T) {
		t.Parallel()
		_, err := GitLabBuilder(nil)(t.Context(), Connector{Name: "gl", Kind: "gitlab"}, tokenResolveValue("t"))
		if err == nil || !strings.Contains(err.Error(), "credential_ref is required") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad config", func(t *testing.T) {
		t.Parallel()
		_, err := GitLabBuilder(nil)(t.Context(),
			Connector{Name: "gl", Kind: "gitlab", CredentialRef: "GL", Config: json.RawMessage(`not json`)},
			tokenResolveValue("t"))
		if err == nil || !strings.Contains(err.Error(), "config") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no base_url defaults to gitlab.com", func(t *testing.T) {
		t.Parallel()
		src, err := GitLabBuilder(nil)(t.Context(),
			Connector{Name: "gl", Kind: "gitlab", CredentialRef: "GL"}, tokenResolveValue("t"))
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		gl, _ := src.(*gitlabSource)
		if gl.Host() != "gitlab.com" || gl.APIBase() != "https://gitlab.com/api/v4" {
			t.Fatalf("host = %q, api = %q", gl.Host(), gl.APIBase())
		}
		if gl.HTTPUsername() != "oauth2" {
			t.Fatalf("HTTPUsername = %q", gl.HTTPUsername())
		}
	})
	t.Run("self-managed known hosts reach the descriptor", func(t *testing.T) {
		t.Parallel()
		cfg := json.RawMessage(`{"base_url":"https://git.example.com","ssh_known_hosts":"# a comment\n\ngit.example.com ssh-ed25519 AAAA\n"}`)
		src, err := GitLabBuilder(nil)(t.Context(),
			Connector{Name: "gl", Kind: "gitlab", CredentialRef: "GL", Config: cfg}, tokenResolveValue("t"))
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		gl, _ := src.(*gitlabSource)
		got := gl.SSHKnownHosts()
		if len(got) != 1 || got[0] != "git.example.com ssh-ed25519 AAAA" {
			t.Fatalf("SSHKnownHosts = %v", got)
		}
	})
	t.Run("gitlab satisfies the shared tool surface", func(t *testing.T) {
		t.Parallel()
		src, err := GitLabBuilder(nil)(t.Context(),
			Connector{Name: "gl", Kind: "gitlab", CredentialRef: "GL"}, tokenResolveValue("t"))
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if got := len(src.Tools()); got != 4 {
			t.Fatalf("Tools() = %d, want 4", got)
		}
	})
}

func TestSplitKnownHosts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"   \n\n", 0},
		{"# only a comment", 0},
		{"host ssh-ed25519 AAAA", 1},
		{"a ssh-ed25519 A\n# c\n\nb ssh-ed25519 B\n", 2},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := splitKnownHosts(tc.in); len(got) != tc.want {
				t.Fatalf("splitKnownHosts(%q) = %v, want %d lines", tc.in, got, tc.want)
			}
		})
	}
}
