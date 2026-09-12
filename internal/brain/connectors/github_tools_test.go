package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// githubToolsSource builds a github source against the fake server
// with a fixed token, returning its tools by name.
func githubToolsSource(t *testing.T, handler http.HandlerFunc) map[string]*tools.Tool {
	t.Helper()
	githubFakeServer(t, handler)
	src, err := GitHubBuilder(nil)(t.Context(), Connector{Name: "gh", Kind: "github", CredentialRef: "GH_PAT"},
		func(_ context.Context, _ string) (string, error) { return "secret-token", nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	out := map[string]*tools.Tool{}
	for _, tl := range src.Tools() {
		out[tl.Name] = tl
	}
	return out
}

// requireAuth fails the request unless the fake token is present, so
// every tool test also proves the PAT is sent.
func requireAuth(t *testing.T, r *http.Request) bool {
	t.Helper()
	if r.Header.Get("Authorization") != "Bearer secret-token" {
		t.Errorf("%s: Authorization = %q", r.URL.Path, r.Header.Get("Authorization"))
		return false
	}
	return true
}

func TestSplitRepoArg(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, owner, name string
		wantErr         bool
	}{
		{in: "octo/hello.world", owner: "octo", name: "hello.world"},
		{in: " octo/repo-1 ", owner: "octo", name: "repo-1"},
		{in: "octo", wantErr: true},
		{in: "octo/a/b", wantErr: true},
		{in: "https://github.com/octo/repo", wantErr: true},
		{in: "", wantErr: true},
	} {
		owner, name, err := splitRepoArg(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("%q: err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if owner != tc.owner || name != tc.name {
			t.Errorf("%q: got %s/%s, want %s/%s", tc.in, owner, name, tc.owner, tc.name)
		}
	}
}

func TestListPullRequests(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     string
		handler  http.HandlerFunc
		want     []string
		wantErr  string
		wantPath string
	}{
		{
			name: "renders number, state, author, branches",
			args: `{"repo":"octo/repo"}`,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !requireAuth(t, r) {
					return
				}
				if r.URL.Query().Get("state") != "open" || r.URL.Query().Get("per_page") != "10" {
					t.Errorf("query = %s", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`[
					{"number":7,"title":"Add thing","state":"open","draft":false,"user":{"login":"alice"},"head":{"ref":"feat/x"},"base":{"ref":"main"}},
					{"number":5,"title":"WIP","state":"open","draft":true,"user":{"login":"bob"},"head":{"ref":"wip"},"base":{"ref":"main"}}
				]`))
			},
			want: []string{"#7 [open] Add thing (alice) feat/x -> main", "#5 [draft] WIP (bob) wip -> main"},
		},
		{
			name: "state and max_results pass through, capped",
			args: `{"repo":"octo/repo","state":"closed","max_results":500}`,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("state") != "closed" || r.URL.Query().Get("per_page") != "50" {
					t.Errorf("query = %s", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`[]`))
			},
			want: []string{"no closed pull requests in octo/repo"},
		},
		{
			name:    "malformed repo",
			args:    `{"repo":"not-a-repo"}`,
			wantErr: `repo must be "owner/name"`,
		},
		{
			name:    "malformed json",
			args:    `{"repo":`,
			wantErr: "unexpected end of JSON",
		},
		{
			name: "401 never leaks token or body",
			args: `{"repo":"octo/repo"}`,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			},
			wantErr: "GitHub token invalid or expired",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := tc.handler
			if handler == nil {
				handler = func(_ http.ResponseWriter, r *http.Request) { t.Errorf("unexpected request %s", r.URL.Path) }
			}
			tl := githubToolsSource(t, handler)["list_pull_requests"]
			got, err := tl.Execute(t.Context(), json.RawMessage(tc.args))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "Bad credentials") {
					t.Fatalf("error leaks token or body: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if want := strings.Join(tc.want, "\n"); got != want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestGetPullRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    string
		handler http.HandlerFunc
		want    []string
		wantErr string
	}{
		{
			name: "renders metadata and body",
			args: `{"repo":"octo/repo","number":7}`,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !requireAuth(t, r) {
					return
				}
				if r.URL.Path != "/repos/octo/repo/pulls/7" {
					t.Errorf("path = %s", r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"number":7,"title":"Add thing","state":"open","merged":false,"mergeable_state":"clean",
					"body":"Does the thing.\n","html_url":"https://github.com/octo/repo/pull/7","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z",
					"commits":2,"additions":10,"deletions":3,"changed_files":4,"user":{"login":"alice"},
					"head":{"ref":"feat/x","sha":"abc123"},"base":{"ref":"main"}}`))
			},
			want: []string{
				"#7 Add thing", "author: alice", "state: open", "head: feat/x (abc123)", "base: main", "mergeable_state: clean",
				"commits: 2, files changed: 4, +10/-3", "created: 2026-09-01T00:00:00Z, updated: 2026-09-02T00:00:00Z", "url: https://github.com/octo/repo/pull/7",
				"", "Does the thing.",
			},
		},
		{
			name: "merged wins over state, empty body omitted",
			args: `{"repo":"octo/repo","number":8}`,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"number":8,"title":"Done","state":"closed","merged":true,"user":{"login":"alice"},"head":{"ref":"h","sha":"s"},"base":{"ref":"main"}}`))
			},
			want: []string{"#8 Done", "author: alice", "state: merged", "head: h (s)", "base: main", "commits: 0, files changed: 0, +0/-0", "created: , updated: ", "url: "},
		},
		{
			name: "404 surfaces status and message",
			args: `{"repo":"octo/repo","number":404}`,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			},
			wantErr: "get pull request: github: status 404: Not Found",
		},
		{
			name:    "number required",
			args:    `{"repo":"octo/repo"}`,
			wantErr: "number must be a positive pull request number",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := tc.handler
			if handler == nil {
				handler = func(_ http.ResponseWriter, r *http.Request) { t.Errorf("unexpected request %s", r.URL.Path) }
			}
			tl := githubToolsSource(t, handler)["get_pull_request"]
			got, err := tl.Execute(t.Context(), json.RawMessage(tc.args))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if want := strings.Join(tc.want, "\n"); got != want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestGetPullRequestDiff(t *testing.T) {
	small := "diff --git a/x b/x\n+hello\n"
	big := strings.Repeat("x", githubDiffMaxBytes+500)
	for _, tc := range []struct {
		name    string
		body    string
		status  int
		want    string
		wantErr string
	}{
		{name: "returns diff verbatim", body: small, want: small},
		{name: "exactly at cap is not truncated", body: strings.Repeat("y", githubDiffMaxBytes), want: strings.Repeat("y", githubDiffMaxBytes)},
		{name: "over cap is cut with marker", body: big, want: big[:githubDiffMaxBytes] + "\n\n[diff truncated at 204800 bytes; 500 more bytes omitted]"},
		{name: "empty diff", body: "", want: "empty diff"},
		{name: "404", status: http.StatusNotFound, body: `{"message":"Not Found"}`, wantErr: "get pull request diff: github: status 404: Not Found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tl := githubToolsSource(t, func(w http.ResponseWriter, r *http.Request) {
				if !requireAuth(t, r) {
					return
				}
				if r.Header.Get("Accept") != githubDiffMediaType {
					t.Errorf("Accept = %q, want %q", r.Header.Get("Accept"), githubDiffMediaType)
				}
				if r.URL.Path != "/repos/octo/repo/pulls/7" {
					t.Errorf("path = %s", r.URL.Path)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			})["get_pull_request_diff"]
			got, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"octo/repo","number":7}`))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d bytes ending %q, want %d bytes ending %q", len(got), tail(got), len(tc.want), tail(tc.want))
			}
		})
	}
}

func tail(s string) string {
	if len(s) > 60 {
		return s[len(s)-60:]
	}
	return s
}

func TestListPullRequestComments(t *testing.T) {
	t.Run("merges issue and review comments chronologically", func(t *testing.T) {
		tl := githubToolsSource(t, func(w http.ResponseWriter, r *http.Request) {
			if !requireAuth(t, r) {
				return
			}
			switch r.URL.Path {
			case "/repos/octo/repo/issues/7/comments":
				_, _ = w.Write([]byte(`[{"id":1,"body":"Looks good overall.","created_at":"2026-09-02T10:00:00Z","user":{"login":"bob"}}]`))
			case "/repos/octo/repo/pulls/7/comments":
				_, _ = w.Write([]byte(`[
					{"id":2,"body":"Nit: rename.","created_at":"2026-09-01T09:00:00Z","path":"main.go","line":12,"user":{"login":"carol"}},
					{"id":3,"body":"Whole-file note.","created_at":"2026-09-03T09:00:00Z","path":"README.md","user":{"login":"carol"}}
				]`))
			default:
				t.Errorf("unexpected path %s", r.URL.Path)
			}
		})["list_pull_request_comments"]
		got, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"octo/repo","number":7}`))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		want := strings.Join([]string{
			"[2026-09-01T09:00:00Z] carol (review on main.go:12):\nNit: rename.",
			"[2026-09-02T10:00:00Z] bob (comment):\nLooks good overall.",
			"[2026-09-03T09:00:00Z] carol (review on README.md):\nWhole-file note.",
		}, "\n\n")
		if got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})
	t.Run("no comments", func(t *testing.T) {
		tl := githubToolsSource(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })["list_pull_request_comments"]
		got, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"octo/repo","number":7}`))
		if err != nil || got != "no comments on octo/repo#7" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("error on second call propagates", func(t *testing.T) {
		tl := githubToolsSource(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/pulls/") {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		})["list_pull_request_comments"]
		_, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"octo/repo","number":7}`))
		if err == nil || !strings.Contains(err.Error(), "list pull request comments: github: status 403: Resource not accessible") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestGitHubPRToolsReachMissions pins the whole reason for this
// surface: through the manager, every github tool lands in
// ReadOnlyTools under its raw name, so missions' connector-reads
// resolver can offer it.
func TestGitHubPRToolsReachMissions(t *testing.T) {
	t.Parallel()
	src, err := GitHubBuilder(nil)(t.Context(), Connector{Name: "gh", Kind: "github", CredentialRef: "GH_PAT"},
		func(_ context.Context, _ string) (string, error) { return "tok", nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	m := testManager(fakeRows{})
	m.sources = map[string]Source{"gh": src}
	got := map[string]bool{}
	for _, tl := range m.ReadOnlyTools() {
		got[tl.Name] = true
	}
	for _, name := range []string{"list_pull_requests", "get_pull_request", "get_pull_request_diff", "list_pull_request_comments"} {
		if !got[name] {
			t.Errorf("ReadOnlyTools lacks %s: got %v", name, got)
		}
	}
}
