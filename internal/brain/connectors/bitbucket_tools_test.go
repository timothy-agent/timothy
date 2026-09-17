package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// bitbucketToolsSource builds a source against the fake server and returns its tools by name.
func bitbucketToolsSource(t *testing.T, handler http.HandlerFunc) map[string]*tools.Tool {
	t.Helper()
	bitbucketFakeServer(t, handler)
	src, err := BitbucketBuilder(nil)(t.Context(), Connector{Name: "bb", Kind: "bitbucket", CredentialRef: "BB_TOKEN"},
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

func TestSplitBitbucketRepoArg(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, workspace, slug string
		wantErr             bool
	}{
		{in: "acme-team/widget-service", workspace: "acme-team", slug: "widget-service"},
		{in: " ws/repo.1 ", workspace: "ws", slug: "repo.1"},
		{in: "ws", wantErr: true},
		{in: "ws/a/b", wantErr: true},
		{in: "https://bitbucket.org/ws/repo", wantErr: true},
		{in: "", wantErr: true},
	} {
		workspace, slug, err := splitBitbucketRepoArg(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("%q: err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if workspace != tc.workspace || slug != tc.slug {
			t.Errorf("%q: got %s/%s, want %s/%s", tc.in, workspace, slug, tc.workspace, tc.slug)
		}
	}
}

func TestBitbucketSourceServesReadOnlyPRTools(t *testing.T) {
	t.Parallel()
	src, err := BitbucketBuilder(nil)(t.Context(), Connector{Name: "bb", Kind: "bitbucket", CredentialRef: "BB_TOKEN"},
		func(_ context.Context, _ string) (string, error) { return "tok", nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []string{"list_pull_requests", "get_pull_request", "get_pull_request_diff", "list_pull_request_comments"}
	got := src.Tools()
	if len(got) != len(want) {
		t.Fatalf("Tools() has %d tools, want %d", len(got), len(want))
	}
	for i, tl := range got {
		if tl.Name != want[i] {
			t.Errorf("Tools()[%d] = %s, want %s", i, tl.Name, want[i])
		}
		if !tl.ReadOnly {
			t.Errorf("%s must be ReadOnly: it is a pure GET and missions rely on the marker", tl.Name)
		}
	}
}

func TestBitbucketListPullRequests(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    string
		handler http.HandlerFunc
		want    []string
		wantErr string
	}{
		{
			name: "renders id, state, author, branches",
			args: `{"repo":"ws/repo"}`,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !requireAuth(t, r) {
					return
				}
				q := r.URL.Query()
				if q["state"] == nil || q["state"][0] != "OPEN" || q.Get("pagelen") != "10" || q.Get("sort") != "-updated_on" {
					t.Errorf("query = %s", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`{"values":[
					{"id":7,"title":"Add thing","state":"OPEN","draft":false,"author":{"nickname":"alice","display_name":"Alice A"},"source":{"branch":{"name":"feat/x"}},"destination":{"branch":{"name":"main"}}},
					{"id":5,"title":"WIP","state":"OPEN","draft":true,"author":{"nickname":"","display_name":"Bob B"},"source":{"branch":{"name":"wip"}},"destination":{"branch":{"name":"main"}}}
				]}`))
			},
			want: []string{"#7 [open] Add thing (alice) feat/x -> main", "#5 [draft] WIP (Bob B) wip -> main"},
		},
		{
			name: "closed unions the non-open states and max_results is capped",
			args: `{"repo":"ws/repo","state":"closed","max_results":500}`,
			handler: func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if strings.Join(q["state"], ",") != "MERGED,DECLINED,SUPERSEDED" || q.Get("pagelen") != "50" {
					t.Errorf("query = %s", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`{"values":[]}`))
			},
			want: []string{"no closed pull requests in ws/repo"},
		},
		{
			name: "all sends no state filter and lower-cases merged",
			args: `{"repo":"ws/repo","state":"all"}`,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query()["state"] != nil {
					t.Errorf("query = %s", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`{"values":[{"id":1,"title":"Old","state":"MERGED","author":{"nickname":"c"},"source":{"branch":{"name":"a"}},"destination":{"branch":{"name":"main"}}}]}`))
			},
			want: []string{"#1 [merged] Old (c) a -> main"},
		},
		{
			name:    "malformed repo",
			args:    `{"repo":"not-a-repo"}`,
			wantErr: `repo must be "workspace/slug"`,
		},
		{
			name:    "malformed json",
			args:    `{"repo":`,
			wantErr: "unexpected end of JSON",
		},
		{
			name: "401 never leaks token or body",
			args: `{"repo":"ws/repo"}`,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"type":"error","error":{"message":"Bad credentials"}}`))
			},
			wantErr: "bitbucket: token invalid or expired",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := tc.handler
			if handler == nil {
				handler = func(_ http.ResponseWriter, r *http.Request) { t.Errorf("unexpected request %s", r.URL.Path) }
			}
			tl := bitbucketToolsSource(t, handler)["list_pull_requests"]
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

func TestBitbucketGetPullRequest(t *testing.T) {
	const pr = `{"id":7,"title":"Add thing","state":"OPEN","draft":false,"description":"  Does the thing.  ",
		"author":{"nickname":"alice","display_name":"Alice A"},
		"source":{"branch":{"name":"feat/x"},"commit":{"hash":"abc123"}},"destination":{"branch":{"name":"main"}},
		"created_on":"2026-09-01T00:00:00+00:00","updated_on":"2026-09-02T00:00:00+00:00",
		"links":{"html":{"href":"https://bitbucket.org/ws/repo/pull-requests/7"}}}`
	for _, tc := range []struct {
		name     string
		args     string
		diffstat func(w http.ResponseWriter, r *http.Request)
		prStatus int
		want     []string
		wantErr  string
	}{
		{
			name: "renders metadata with diffstat totals",
			args: `{"repo":"ws/repo","number":7}`,
			diffstat: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"values":[{"lines_added":4,"lines_removed":6,"status":"modified"},{"lines_added":10,"lines_removed":0,"status":"added"}],"size":2}`))
			},
			want: []string{
				"#7 Add thing",
				"author: alice", "state: open",
				"head: feat/x (abc123)", "base: main",
				"files changed: 2, +14/-6",
				"created: 2026-09-01T00:00:00+00:00, updated: 2026-09-02T00:00:00+00:00",
				"url: https://bitbucket.org/ws/repo/pull-requests/7",
				"", "Does the thing.",
			},
		},
		{
			name: "diffstat follows next across pages",
			args: `{"repo":"ws/repo","number":7}`,
			diffstat: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("page") == "2" {
					_, _ = w.Write([]byte(`{"values":[{"lines_added":1,"lines_removed":2}]}`))
					return
				}
				_, _ = fmt.Fprintf(w, `{"values":[{"lines_added":4,"lines_removed":6},{"lines_added":10,"lines_removed":0}],"next":"%s/repositories/ws/repo/pullrequests/7/diffstat?page=2"}`, bitbucketAPIBase)
			},
			want: []string{
				"#7 Add thing",
				"author: alice", "state: open",
				"head: feat/x (abc123)", "base: main",
				"files changed: 3, +15/-8",
				"created: 2026-09-01T00:00:00+00:00, updated: 2026-09-02T00:00:00+00:00",
				"url: https://bitbucket.org/ws/repo/pull-requests/7",
				"", "Does the thing.",
			},
		},
		{
			name: "diffstat past the page ceiling is marked truncated",
			args: `{"repo":"ws/repo","number":7}`,
			diffstat: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"values":[{"lines_added":1,"lines_removed":1}],"next":"%s/repositories/ws/repo/pullrequests/7/diffstat"}`, bitbucketAPIBase)
			},
			want: []string{
				"#7 Add thing",
				"author: alice", "state: open",
				"head: feat/x (abc123)", "base: main",
				fmt.Sprintf("files changed: %d, +%d/-%d [diffstat truncated at %d pages; more files omitted]",
					bitbucketDiffstatMaxPages, bitbucketDiffstatMaxPages, bitbucketDiffstatMaxPages, bitbucketDiffstatMaxPages),
				"created: 2026-09-01T00:00:00+00:00, updated: 2026-09-02T00:00:00+00:00",
				"url: https://bitbucket.org/ws/repo/pull-requests/7",
				"", "Does the thing.",
			},
		},
		{
			name: "diffstat failure degrades to a note",
			args: `{"repo":"ws/repo","number":7}`,
			diffstat: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"type":"error","error":{"message":"boom"}}`))
			},
			want: []string{
				"#7 Add thing",
				"author: alice", "state: open",
				"head: feat/x (abc123)", "base: main",
				"(diffstat unavailable: get pull request diffstat: bitbucket: status 500: boom)",
				"created: 2026-09-01T00:00:00+00:00, updated: 2026-09-02T00:00:00+00:00",
				"url: https://bitbucket.org/ws/repo/pull-requests/7",
				"", "Does the thing.",
			},
		},
		{
			name:     "404 is plain text on Bitbucket",
			args:     `{"repo":"ws/repo","number":999}`,
			prStatus: http.StatusNotFound,
			wantErr:  "get pull request: bitbucket: status 404: Not Found",
		},
		{name: "number must be positive", args: `{"repo":"ws/repo","number":0}`, wantErr: "positive pull request number"},
		{name: "malformed args", args: `{"repo":5}`, wantErr: "cannot unmarshal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tl := bitbucketToolsSource(t, func(w http.ResponseWriter, r *http.Request) {
				if !requireAuth(t, r) {
					return
				}
				switch r.URL.Path {
				case "/repositories/ws/repo/pullrequests/7":
					_, _ = w.Write([]byte(pr))
				case "/repositories/ws/repo/pullrequests/7/diffstat":
					tc.diffstat(w, r)
				case "/repositories/ws/repo/pullrequests/999":
					w.WriteHeader(tc.prStatus)
					_, _ = w.Write([]byte("Not Found"))
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			})["get_pull_request"]
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

// /diff answers 302 to a same-host text/plain diff; the bearer must survive the redirect.
func TestBitbucketGetPullRequestDiff(t *testing.T) {
	small := "diff --git a/x b/x\n+hello\n"
	big := strings.Repeat("x", bitbucketDiffMaxBytes+500)
	for _, tc := range []struct {
		name    string
		body    string
		status  int
		want    string
		wantErr string
	}{
		{name: "returns diff verbatim through the redirect", body: small, want: small},
		{name: "exactly at cap is not truncated", body: strings.Repeat("y", bitbucketDiffMaxBytes), want: strings.Repeat("y", bitbucketDiffMaxBytes)},
		{name: "over cap is cut with marker", body: big, want: big[:bitbucketDiffMaxBytes] + "\n\n[diff truncated at 204800 bytes; 500 more bytes omitted]"},
		{name: "empty diff", body: "", want: "empty diff"},
		{name: "404 on the pull request", status: http.StatusNotFound, body: "Not Found", wantErr: "get pull request diff: bitbucket: status 404: Not Found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tl := bitbucketToolsSource(t, func(w http.ResponseWriter, r *http.Request) {
				if !requireAuth(t, r) {
					return
				}
				if r.Header.Get("Accept") != "" {
					t.Errorf("Accept = %q, want none on the text endpoint", r.Header.Get("Accept"))
				}
				switch r.URL.Path {
				case "/repositories/ws/repo/pullrequests/7/diff":
					if tc.status != 0 {
						w.WriteHeader(tc.status)
						_, _ = w.Write([]byte(tc.body))
						return
					}
					http.Redirect(w, r, "/repositories/ws/repo/diff/ws/repo:abc%0Ddef?from_pullrequest_id=7&topic=true", http.StatusFound)
				case "/repositories/ws/repo/diff/ws/repo:abc\rdef":
					w.Header().Set("Content-Type", "text/plain")
					_, _ = w.Write([]byte(tc.body))
				default:
					t.Errorf("unexpected path %q", r.URL.Path)
				}
			})["get_pull_request_diff"]
			got, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"ws/repo","number":7}`))
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

func TestBitbucketListPullRequestComments(t *testing.T) {
	t.Run("orders conversation and inline comments chronologically, skipping deleted", func(t *testing.T) {
		tl := bitbucketToolsSource(t, func(w http.ResponseWriter, r *http.Request) {
			if !requireAuth(t, r) {
				return
			}
			if r.URL.Path != "/repositories/ws/repo/pullrequests/7/comments" || r.URL.Query().Get("pagelen") != "100" {
				t.Errorf("request = %s", r.URL.RequestURI())
			}
			_, _ = w.Write([]byte(`{"values":[
				{"id":3,"created_on":"2026-09-03T00:00:00+00:00","deleted":false,"content":{"raw":" later "},"user":{"nickname":"bob"},"inline":null},
				{"id":1,"created_on":"2026-09-01T00:00:00+00:00","deleted":false,"content":{"raw":"line note"},"user":{"nickname":"alice"},"inline":{"path":"src/a.go","to":12,"from":null}},
				{"id":2,"created_on":"2026-09-02T00:00:00+00:00","deleted":true,"content":{"raw":"gone"},"user":{"nickname":"eve"},"inline":null},
				{"id":4,"created_on":"2026-09-02T12:00:00+00:00","deleted":false,"content":{"raw":"file-level"},"user":{"nickname":"","display_name":"Carol C"},"inline":{"path":"README.md","to":null,"from":null}}
			]}`))
		})["list_pull_request_comments"]
		got, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"ws/repo","number":7}`))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		want := strings.Join([]string{
			"[2026-09-01T00:00:00+00:00] alice (review on src/a.go:12):\nline note",
			"[2026-09-02T12:00:00+00:00] Carol C (review on README.md):\nfile-level",
			"[2026-09-03T00:00:00+00:00] bob (comment):\nlater",
		}, "\n\n")
		if got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("follows next once and stops", func(t *testing.T) {
		var calls int
		tl := bitbucketToolsSource(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			switch r.URL.Query().Get("page") {
			case "":
				_, _ = w.Write([]byte(`{"values":[{"id":1,"created_on":"a","content":{"raw":"one"},"user":{"nickname":"x"}}],"next":"` + bitbucketAPIBase + `/repositories/ws/repo/pullrequests/7/comments?page=2"}`))
			case "2":
				_, _ = w.Write([]byte(`{"values":[{"id":2,"created_on":"b","content":{"raw":"two"},"user":{"nickname":"x"}}],"next":"` + bitbucketAPIBase + `/repositories/ws/repo/pullrequests/7/comments?page=3"}`))
			default:
				t.Errorf("page 3 must not be fetched")
			}
		})["list_pull_request_comments"]
		got, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"ws/repo","number":7}`))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if calls != 2 || !strings.Contains(got, "one") || !strings.Contains(got, "two") {
			t.Fatalf("calls = %d, got:\n%s", calls, got)
		}
	})

	t.Run("no comments", func(t *testing.T) {
		tl := bitbucketToolsSource(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"values":[]}`))
		})["list_pull_request_comments"]
		got, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"ws/repo","number":7}`))
		if err != nil || got != "no comments on ws/repo#7" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})

	t.Run("error propagates", func(t *testing.T) {
		tl := bitbucketToolsSource(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"type":"error","error":{"message":"no access"}}`))
		})["list_pull_request_comments"]
		_, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"ws/repo","number":7}`))
		if err == nil || !strings.Contains(err.Error(), "list pull request comments: bitbucket: status 403: no access") {
			t.Fatalf("err = %v", err)
		}
	})
}

// The tools survive the manager's mission filter, which strips every MCP source.
func TestBitbucketPRToolsReachMissions(t *testing.T) {
	t.Parallel()
	src, err := BitbucketBuilder(nil)(t.Context(), Connector{Name: "bb", Kind: "bitbucket", CredentialRef: "BB_TOKEN"},
		func(_ context.Context, _ string) (string, error) { return "tok", nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	m := testManager(fakeRows{})
	m.sources = map[string]Source{"bb": src}
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

// Every tool reports a resolve failure by ref name and makes no request.
func TestBitbucketPRToolsResolveFailure(t *testing.T) {
	var hits int
	srv := bitbucketFakeServer(t, func(http.ResponseWriter, *http.Request) { hits++ })
	src := bitbucketSourceWith(t, srv.Client(), "", fmt.Errorf("vault sealed"))
	for _, tl := range src.Tools() {
		_, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"ws/repo","number":7}`))
		if err == nil || !strings.Contains(err.Error(), `resolve credential_ref "BB_TOKEN": vault sealed`) {
			t.Errorf("%s: err = %v", tl.Name, err)
		}
	}
	if hits != 0 {
		t.Fatalf("resolver failure still made %d requests", hits)
	}
}

// The shared {repo, number} decoder rejects a bad repo form.
func TestBitbucketPRArgsRepoShape(t *testing.T) {
	t.Parallel()
	for _, args := range []string{`{"repo":"nope","number":7}`, `{"repo":"a/b/c","number":7}`} {
		if _, _, _, err := bitbucketPRArgs(json.RawMessage(args)); err == nil || !strings.Contains(err.Error(), `repo must be "workspace/slug"`) {
			t.Errorf("%s: err = %v", args, err)
		}
	}
}

func TestBitbucketGetPullRequestDiffRequestErrors(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(http.ResponseWriter, *http.Request) {})
		srv.Close()
		tl := bitbucketSourceWith(t, &http.Client{}, "secret-token", nil).getPullRequestDiff()
		_, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"ws/repo","number":7}`))
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("body cut mid-read", func(t *testing.T) {
		tl := bitbucketToolsSource(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("diff --git"))
		})["get_pull_request_diff"]
		_, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"ws/repo","number":7}`))
		if err == nil || !strings.Contains(err.Error(), "get pull request diff: read response:") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestBitbucketGetJSONDecodeError(t *testing.T) {
	tl := bitbucketToolsSource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	})["list_pull_requests"]
	_, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"ws/repo"}`))
	if err == nil || !strings.Contains(err.Error(), "list pull requests: decode response:") {
		t.Fatalf("err = %v", err)
	}
}

func TestBitbucketCommentsInlineWithoutLine(t *testing.T) {
	tl := bitbucketToolsSource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"id":1,"created_on":"a","content":{"raw":"whole file"},"user":{"nickname":"x"},"inline":{"path":"a.go","to":0,"from":null}}]}`))
	})["list_pull_request_comments"]
	got, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"ws/repo","number":7}`))
	if err != nil || got != "[a] x (review on a.go):\nwhole file" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

// The diff and comments tools reject a bad repo form too.
func TestBitbucketPRToolsRejectBadRepoArg(t *testing.T) {
	tls := bitbucketToolsSource(t, func(_ http.ResponseWriter, r *http.Request) { t.Errorf("unexpected request %s", r.URL.Path) })
	for _, name := range []string{"get_pull_request_diff", "list_pull_request_comments"} {
		_, err := tls[name].Execute(t.Context(), json.RawMessage(`{"repo":"nope","number":7}`))
		if err == nil || !strings.Contains(err.Error(), `repo must be "workspace/slug"`) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestBitbucketGetJSONRequestError(t *testing.T) {
	srv := bitbucketFakeServer(t, func(http.ResponseWriter, *http.Request) {})
	srv.Close()
	tl := bitbucketSourceWith(t, &http.Client{}, "secret-token", nil).listPullRequests()
	_, err := tl.Execute(t.Context(), json.RawMessage(`{"repo":"ws/repo"}`))
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v", err)
	}
}
