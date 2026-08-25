package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// githubFakeServer serves /user and /user/emails with configurable
// bodies/status/headers, mirroring the seam google_test.go uses for
// its OAuth endpoints, and points githubAPIBase at it for the test's
// duration.
func githubFakeServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	prev := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = prev })
	return srv
}

// TestFetchGitHubIdentity and its siblings below mutate the shared
// githubAPIBase var, so none of them run t.Parallel().
func TestFetchGitHubIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    GitHubIdentity
		wantErr string
	}{
		{
			name: "public email on /user, classic PAT scopes",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					w.Header().Set("X-OAuth-Scopes", "repo, read:org")
					_, _ = w.Write([]byte(`{"login":"octocat","name":"The Octocat","id":1,"email":"octocat@github.com"}`))
				case "/user/emails":
					_, _ = w.Write([]byte(`[{"email":"octocat@github.com","primary":true,"verified":true}]`))
				}
			},
			want: GitHubIdentity{Login: "octocat", Name: "The Octocat", Email: "octocat@github.com", Scopes: "repo, read:org"},
		},
		{
			name: "primary verified email from /user/emails wins over /user's",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					_, _ = w.Write([]byte(`{"login":"octocat","name":"The Octocat","id":1,"email":""}`))
				case "/user/emails":
					_, _ = w.Write([]byte(`[
						{"email":"unverified@x.com","primary":true,"verified":false},
						{"email":"secondary@x.com","primary":false,"verified":true},
						{"email":"primary@x.com","primary":true,"verified":true}
					]`))
				}
			},
			want: GitHubIdentity{Login: "octocat", Name: "The Octocat", Email: "primary@x.com", Scopes: "fine-grained token"},
		},
		{
			name: "fine-grained PAT without Email permission falls back to noreply",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					_, _ = w.Write([]byte(`{"login":"octocat","name":"The Octocat","id":583231,"email":""}`))
				case "/user/emails":
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
				}
			},
			want: GitHubIdentity{Login: "octocat", Name: "The Octocat", Email: "583231+octocat@users.noreply.github.com", Scopes: "fine-grained token"},
		},
		{
			name: "emails 404 falls back the same way as 403",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					_, _ = w.Write([]byte(`{"login":"octocat","name":"","id":1,"email":""}`))
				case "/user/emails":
					w.WriteHeader(http.StatusNotFound)
				}
			},
			want: GitHubIdentity{Login: "octocat", Name: "", Email: "1+octocat@users.noreply.github.com", Scopes: "fine-grained token"},
		},
		{
			name: "bad token 401 on /user",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/user" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
				}
			},
			wantErr: "invalid or expired",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := githubFakeServer(t, tc.handler)

			got, err := fetchGitHubIdentity(t.Context(), srv.Client(), "test-token")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("fetchGitHubIdentity: %v", err)
			}
			if got != tc.want {
				t.Fatalf("identity = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestGitHubStatusErrorMapping pins the "status + short reason, no raw
// JSON body" discipline: 401 always gets the fixed reconnect message
// (never the raw body), other statuses keep GitHub's parsed message
// field(s), and no raw JSON structure ever leaks into the error text.
func TestGitHubStatusErrorMapping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "401 bad or expired token",
			status: http.StatusUnauthorized,
			body:   `{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest"}`,
			want:   "GitHub token invalid or expired — replace the PAT",
		},
		{
			name:   "403 forbidden keeps github's message",
			status: http.StatusForbidden,
			body:   `{"message":"Resource not accessible by personal access token","documentation_url":"https://docs.github.com/rest"}`,
			want:   "github: status 403: Resource not accessible by personal access token",
		},
		{
			name:   "404 not found keeps github's message",
			status: http.StatusNotFound,
			body:   `{"message":"Not Found","documentation_url":"https://docs.github.com/rest"}`,
			want:   "github: status 404: Not Found",
		},
		{
			name:   "500 with no parseable message",
			status: http.StatusInternalServerError,
			body:   `not json at all`,
			want:   "github: status 500",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := &http.Response{
				StatusCode: tc.status,
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			err := githubStatusError(resp)
			if err.Error() != tc.want {
				t.Fatalf("githubStatusError = %q, want %q", err.Error(), tc.want)
			}
			if strings.Contains(err.Error(), "documentation_url") {
				t.Fatalf("githubStatusError leaked raw JSON: %q", err.Error())
			}
		})
	}
}

func TestFetchGitHubIdentityNeverLogsToken(t *testing.T) {
	srv := githubFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer secret-pat" {
			t.Errorf("Authorization = %q", auth)
		}
		w.WriteHeader(http.StatusUnauthorized)
	})

	_, err := fetchGitHubIdentity(t.Context(), srv.Client(), "secret-pat")
	if err == nil || strings.Contains(err.Error(), "secret-pat") {
		t.Fatalf("err = %v, must never contain the token", err)
	}
}

func TestGitHubBuilderRequiresCredentialRef(t *testing.T) {
	t.Parallel()
	b := GitHubBuilder(nil)
	_, err := b(t.Context(), Connector{Name: "gh", Kind: "github"}, func(_ context.Context, _ string) (string, error) { return "", nil })
	if err == nil {
		t.Fatal("missing credential_ref accepted")
	}
}

func TestGitHubSourceServesNoTools(t *testing.T) {
	t.Parallel()
	b := GitHubBuilder(nil)
	src, err := b(t.Context(), Connector{Name: "gh", Kind: "github", CredentialRef: "GH_PAT"},
		func(_ context.Context, _ string) (string, error) { return "tok", nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := src.Tools(); len(got) != 0 {
		t.Fatalf("Tools() = %v, want none: github is identity-only in this slice", got)
	}
}

func TestValidateAcceptsGitHubKind(t *testing.T) {
	t.Parallel()
	c := Connector{Name: "personal-gh", Kind: "github", CredentialRef: "GH_PAT", Config: json.RawMessage(`{}`)}
	if err := validate(c); err != nil {
		t.Fatalf("valid github connector rejected: %v", err)
	}
}

// TestManagerTestIdentityReturnsGitHubIdentity exercises the seam the
// API's test endpoint uses: a github-kind Test resolving to an
// identity payload instead of just ok/error.
func TestManagerTestIdentityReturnsGitHubIdentity(t *testing.T) {
	srv := githubFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Header().Set("X-OAuth-Scopes", "repo")
			_, _ = w.Write([]byte(`{"login":"octocat","name":"The Octocat","id":1,"email":"octocat@github.com"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"octocat@github.com","primary":true,"verified":true}]`))
		}
	})

	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "personal-gh", Kind: "github", CredentialRef: "GH_PAT"},
	}})
	m.resolve = func(context.Context, string) (string, error) { return "tok", nil }
	m.RegisterBuilder("github", GitHubBuilder(srv.Client()))

	identity, err := m.TestIdentity(t.Context(), "1")
	if err != nil {
		t.Fatalf("TestIdentity: %v", err)
	}
	if identity == nil || identity.Login != "octocat" || identity.Email != "octocat@github.com" || identity.Scopes != "repo" {
		t.Fatalf("identity = %+v", identity)
	}
}

// TestManagerTestIdentityMCPHasNoIdentity pins that kinds without an
// identity concept keep the plain ok/error contract: identity is nil,
// not an error.
func TestManagerTestIdentityMCPHasNoIdentity(t *testing.T) {
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "grafana", Kind: "mcp"},
	}})
	m.RegisterBuilder("mcp", func(context.Context, Connector, Resolve) (Source, error) {
		return &fakeSource{}, nil
	})

	identity, err := m.TestIdentity(t.Context(), "1")
	if err != nil {
		t.Fatalf("TestIdentity: %v", err)
	}
	if identity != nil {
		t.Fatalf("identity = %+v, want nil for a non-identity kind", identity)
	}
}

// TestFetchGitHubReposPaginates proves ListRepos follows GET
// /user/repos pagination (a full page implies there may be more) and
// stops once a short page comes back.
func TestFetchGitHubReposPaginates(t *testing.T) {
	page1 := make([]GitHubRepo, githubRepoPerPage)
	for i := range page1 {
		page1[i] = GitHubRepo{FullName: "org/repo1"}
	}
	page2 := []GitHubRepo{{FullName: "org/repo2"}}

	var gotPages []string
	srv := githubFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/repos" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		gotPages = append(gotPages, r.URL.Query().Get("page"))
		switch r.URL.Query().Get("page") {
		case "1":
			_ = json.NewEncoder(w).Encode(page1)
		case "2":
			_ = json.NewEncoder(w).Encode(page2)
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	})

	repos, err := fetchGitHubRepos(t.Context(), srv.Client(), "test-token")
	if err != nil {
		t.Fatalf("fetchGitHubRepos: %v", err)
	}
	if len(repos) != githubRepoPerPage+1 {
		t.Fatalf("len(repos) = %d, want %d", len(repos), githubRepoPerPage+1)
	}
	if len(gotPages) != 2 {
		t.Fatalf("pages fetched = %v, want 2 pages", gotPages)
	}
}

// TestFetchGitHubReposCapsAtMax proves ListRepos stops paging once it
// has githubRepoMaxRepos, even if the remote claims more full pages —
// an enormous PAT-visible repo count must not hang the request.
func TestFetchGitHubReposCapsAtMax(t *testing.T) {
	fullPage := make([]GitHubRepo, githubRepoPerPage)
	srv := githubFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(fullPage)
	})

	repos, err := fetchGitHubRepos(t.Context(), srv.Client(), "test-token")
	if err != nil {
		t.Fatalf("fetchGitHubRepos: %v", err)
	}
	if len(repos) != githubRepoMaxRepos {
		t.Fatalf("len(repos) = %d, want capped at %d", len(repos), githubRepoMaxRepos)
	}
}

func TestCreateGitHubRepo(t *testing.T) {
	srv := githubFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/repos" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Name     string `json:"name"`
			Private  bool   `json:"private"`
			AutoInit bool   `json:"auto_init"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body.Name != "new-repo" || !body.Private || !body.AutoInit {
			t.Fatalf("unexpected request body: %+v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(GitHubRepo{FullName: "octocat/new-repo", Private: true, DefaultBranch: "main"})
	})

	repo, err := createGitHubRepo(t.Context(), srv.Client(), "test-token", "new-repo", true)
	if err != nil {
		t.Fatalf("createGitHubRepo: %v", err)
	}
	if repo.FullName != "octocat/new-repo" || repo.DefaultBranch != "main" {
		t.Fatalf("repo = %+v", repo)
	}
}

// TestManagerListReposNonGitHubKind pins that the ListRepos capability
// gate rejects a kind with no repo concept (mcp) with ErrUnsupported.
func TestManagerListReposNonGitHubKind(t *testing.T) {
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "grafana", Kind: "mcp"},
	}})
	m.RegisterBuilder("mcp", func(context.Context, Connector, Resolve) (Source, error) {
		return &fakeSource{}, nil
	})

	if _, err := m.ListRepos(t.Context(), "1"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ListRepos on mcp connector = %v, want ErrUnsupported", err)
	}
}

// TestManagerListReposAndCreateRepo exercises the full seam the API's
// repo endpoints use: a github-kind connector's ListRepos/CreateRepo
// both build fresh and close the ephemeral source.
func TestManagerListReposAndCreateRepo(t *testing.T) {
	srv := githubFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user/repos" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]GitHubRepo{{FullName: "octocat/hello-world"}})
		case r.URL.Path == "/user/repos" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(GitHubRepo{FullName: "octocat/brand-new"})
		}
	})

	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "personal-gh", Kind: "github", CredentialRef: "GH_PAT"},
	}})
	m.resolve = func(context.Context, string) (string, error) { return "tok", nil }
	m.RegisterBuilder("github", GitHubBuilder(srv.Client()))

	repos, err := m.ListRepos(t.Context(), "1")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].FullName != "octocat/hello-world" {
		t.Fatalf("repos = %+v", repos)
	}

	repo, err := m.CreateRepo(t.Context(), "1", "brand-new", false)
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if repo.FullName != "octocat/brand-new" {
		t.Fatalf("repo = %+v", repo)
	}
}

// TestFetchGitHubRepo proves GET /repos/{owner}/{repo} decodes into
// GitHubRepo — the PR flow's default_branch lookup.
func TestFetchGitHubRepo(t *testing.T) {
	srv := githubFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octocat/hello-world" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(GitHubRepo{FullName: "octocat/hello-world", DefaultBranch: "main"})
	})

	repo, err := fetchGitHubRepo(t.Context(), srv.Client(), "test-token", "octocat", "hello-world")
	if err != nil {
		t.Fatalf("fetchGitHubRepo: %v", err)
	}
	if repo.DefaultBranch != "main" {
		t.Fatalf("repo = %+v", repo)
	}
}

// TestFetchGitHubPRMerged proves GET /repos/{owner}/{repo}/pulls/{number}
// decodes the merged field, and a non-200 response surfaces as an
// error.
func TestFetchGitHubPRMerged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    bool
		wantErr string
	}{
		{
			name: "merged",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/octocat/hello-world/pulls/42" {
					t.Fatalf("unexpected path %q", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"merged": true})
			},
			want: true,
		},
		{
			name: "not merged",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"merged": false})
			},
			want: false,
		},
		{
			name: "error status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
			},
			wantErr: "status 404",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := githubFakeServer(t, tc.handler)
			got, err := fetchGitHubPRMerged(t.Context(), srv.Client(), "test-token", "octocat", "hello-world", 42)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("fetchGitHubPRMerged: %v", err)
			}
			if got != tc.want {
				t.Fatalf("merged = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCreateGitHubPR proves POST /repos/{owner}/{repo}/pulls sends the
// expected body and decodes the created PR.
func TestCreateGitHubPR(t *testing.T) {
	srv := githubFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octocat/hello-world/pulls" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Title string `json:"title"`
			Head  string `json:"head"`
			Base  string `json:"base"`
			Body  string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body.Title != "Fix bug" || body.Head != "mission/fix-bug" || body.Base != "main" {
			t.Fatalf("unexpected request body: %+v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(GitHubPR{Number: 42, HTMLURL: "https://github.com/octocat/hello-world/pull/42", State: "open"})
	})

	pr, err := createGitHubPR(t.Context(), srv.Client(), "test-token", "octocat", "hello-world", "Fix bug", "mission/fix-bug", "main", "body text")
	if err != nil {
		t.Fatalf("createGitHubPR: %v", err)
	}
	if pr.Number != 42 || pr.HTMLURL != "https://github.com/octocat/hello-world/pull/42" {
		t.Fatalf("pr = %+v", pr)
	}
}

// TestManagerCreatePRAlreadyExists proves CreatePR fetches and returns
// the existing open PR when GitHub's 422 reports one already exists for
// the head — the idempotent re-call path a re-push through the same
// endpoint takes.
func TestManagerCreatePRAlreadyExists(t *testing.T) {
	srv := githubFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/octocat/hello-world/pulls" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": "Validation Failed",
				"errors": []map[string]any{
					{"message": "A pull request already exists for octocat:mission/fix-bug."},
				},
			})
		case r.URL.Path == "/repos/octocat/hello-world/pulls" && r.Method == http.MethodGet:
			if r.URL.Query().Get("state") != "open" {
				t.Fatalf("expected state=open, got %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]GitHubPR{
				{Number: 7, HTMLURL: "https://github.com/octocat/hello-world/pull/7", State: "open"},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "personal-gh", Kind: "github", CredentialRef: "GH_PAT"},
	}})
	m.resolve = func(context.Context, string) (string, error) { return "tok", nil }
	m.RegisterBuilder("github", GitHubBuilder(srv.Client()))

	pr, err := m.CreatePR(t.Context(), "1", "octocat", "hello-world", "Fix bug", "mission/fix-bug", "main", "body")
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if pr.Number != 7 {
		t.Fatalf("pr = %+v, want the existing open PR #7", pr)
	}
}

// TestManagerGetRepoNonGitHubKind pins that GetRepo also gates on the
// repoSource capability, mirroring TestManagerListReposNonGitHubKind.
func TestManagerGetRepoNonGitHubKind(t *testing.T) {
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "grafana", Kind: "mcp"},
	}})
	m.RegisterBuilder("mcp", func(context.Context, Connector, Resolve) (Source, error) {
		return &fakeSource{}, nil
	})

	if _, err := m.GetRepo(t.Context(), "1", "o", "r"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("GetRepo on mcp connector = %v, want ErrUnsupported", err)
	}
}
