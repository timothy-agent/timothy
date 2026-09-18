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
)

// bitbucketFakeServer swaps bitbucketAPIBase for the test's lifetime; callers must not run in parallel.
func bitbucketFakeServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	prev := bitbucketAPIBase
	bitbucketAPIBase = srv.URL
	t.Cleanup(func() { bitbucketAPIBase = prev })
	return srv
}

func TestFetchBitbucketIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    GitHubIdentity
		wantErr string
	}{
		{
			name: "username and primary confirmed email",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					_, _ = w.Write([]byte(`{"username":"taki","nickname":"Taki","display_name":"Taki Elias","account_id":"1"}`))
				case "/user/emails":
					_, _ = w.Write([]byte(`{"values":[
						{"email":"old@x.com","is_primary":false,"is_confirmed":true},
						{"email":"taki@x.com","is_primary":true,"is_confirmed":true}
					]}`))
				}
			},
			want: GitHubIdentity{Login: "taki", Name: "Taki Elias", Email: "taki@x.com", Scopes: "access token"},
		},
		{
			name: "nickname stands in when username is absent",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					_, _ = w.Write([]byte(`{"nickname":"taki","display_name":"Taki Elias"}`))
				case "/user/emails":
					_, _ = w.Write([]byte(`{"values":[]}`))
				}
			},
			want: GitHubIdentity{Login: "taki", Name: "Taki Elias", Email: "", Scopes: "access token"},
		},
		{
			name: "unconfirmed primary is still preferred over nothing",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					_, _ = w.Write([]byte(`{"username":"taki","display_name":"Taki"}`))
				case "/user/emails":
					_, _ = w.Write([]byte(`{"values":[{"email":"p@x.com","is_primary":true,"is_confirmed":false}]}`))
				}
			},
			want: GitHubIdentity{Login: "taki", Name: "Taki", Email: "p@x.com", Scopes: "access token"},
		},
		{
			name: "access token without an account email (403) yields empty email",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					_, _ = w.Write([]byte(`{"username":"ws-token","display_name":"workspace token"}`))
				case "/user/emails":
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"type":"error","error":{"message":"Access token has insufficient scope"}}`))
				}
			},
			want: GitHubIdentity{Login: "ws-token", Name: "workspace token", Email: "", Scopes: "access token"},
		},
		{
			name: "emails 404 falls back the same way as 403",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					_, _ = w.Write([]byte(`{"username":"taki","display_name":""}`))
				case "/user/emails":
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`Not Found`))
				}
			},
			want: GitHubIdentity{Login: "taki", Name: "", Email: "", Scopes: "access token"},
		},
		{
			name: "bad token 401 on /user",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/user" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"type":"error","error":{"message":"Token is invalid or not supported for this endpoint."}}`))
				}
			},
			wantErr: "invalid or expired",
		},
		{
			name: "emails 500 is an error, not an empty email",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					_, _ = w.Write([]byte(`{"username":"taki"}`))
				case "/user/emails":
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"type":"error","error":{"message":"boom"}}`))
				}
			},
			wantErr: "status 500: boom",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := bitbucketFakeServer(t, tc.handler)

			got, err := fetchBitbucketIdentity(t.Context(), srv.Client(), "test-token", "")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("fetchBitbucketIdentity: %v", err)
			}
			if got != tc.want {
				t.Fatalf("identity = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// 401 gets a fixed message, JSON keeps its message, plain text is kept, HTML is dropped.
func TestBitbucketStatusErrorMapping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"401 fixed message", http.StatusUnauthorized, `{"type":"error","error":{"message":"Bad token"}}`, "bitbucket: token invalid or expired — replace the access token"},
		{"json message", http.StatusForbidden, `{"type":"error","error":{"message":"insufficient scope"}}`, "bitbucket: status 403: insufficient scope"},
		{"plain text 404", http.StatusNotFound, "Not Found", "bitbucket: status 404: Not Found"},
		{"html body", http.StatusBadGateway, "<html><body>gateway</body></html>", "bitbucket: status 502"},
		{"empty body", http.StatusTooManyRequests, "", "bitbucket: status 429"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			rec.WriteHeader(tc.status)
			_, _ = rec.WriteString(tc.body)
			err := bitbucketStatusError(rec.Result())
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "<html>") {
				t.Fatalf("raw html leaked into error: %v", err)
			}
		})
	}
}

func TestFetchBitbucketIdentityNeverLogsToken(t *testing.T) {
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer secret-token" {
			t.Errorf("Authorization = %q", auth)
		}
		w.WriteHeader(http.StatusUnauthorized)
	})

	_, err := fetchBitbucketIdentity(t.Context(), srv.Client(), "secret-token", "")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v, must never contain the token", err)
	}
}

func TestBitbucketBuilderRequiresCredentialRef(t *testing.T) {
	t.Parallel()
	b := BitbucketBuilder(nil)
	_, err := b(t.Context(), Connector{Name: "bb", Kind: "bitbucket"}, func(_ context.Context, _ string) (string, error) { return "", nil })
	if err == nil {
		t.Fatal("missing credential_ref accepted")
	}
}

func TestValidateAcceptsBitbucketKind(t *testing.T) {
	t.Parallel()
	c := Connector{Name: "work-bb", Kind: "bitbucket", CredentialRef: "BB_TOKEN", Config: json.RawMessage(`{}`)}
	if err := validate(c); err != nil {
		t.Fatalf("valid bitbucket connector rejected: %v", err)
	}
}

// The manager's identifier assertion picks up the kind with no manager change.
func TestManagerTestIdentityReturnsBitbucketIdentity(t *testing.T) {
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"username":"taki","display_name":"Taki Elias"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`{"values":[{"email":"taki@x.com","is_primary":true,"is_confirmed":true}]}`))
		}
	})

	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "work-bb", Kind: "bitbucket", CredentialRef: "BB_TOKEN"},
	}})
	m.resolve = func(context.Context, string) (string, error) { return "tok", nil }
	m.RegisterBuilder("bitbucket", BitbucketBuilder(srv.Client()))

	identity, err := m.TestIdentity(t.Context(), "1")
	if err != nil {
		t.Fatalf("TestIdentity: %v", err)
	}
	if identity == nil || identity.Login != "taki" || identity.Email != "taki@x.com" || identity.Scopes != "access token" {
		t.Fatalf("identity = %+v", identity)
	}
}

// bitbucketRepoJSON is one repositories entry in the live wire shape.
func bitbucketRepoJSON(fullName, mainBranch string) map[string]any {
	entry := map[string]any{
		"full_name":  fullName,
		"is_private": true,
		"updated_on": "2026-09-13T00:00:00+00:00",
		"links": map[string]any{
			"html": map[string]any{"href": "https://bitbucket.org/" + fullName},
			"clone": []map[string]any{
				{"name": "ssh", "href": "git@bitbucket.org:" + fullName + ".git"},
				{"name": "https", "href": "https://bitbucket.org/" + fullName + ".git"},
			},
		},
	}
	if mainBranch != "" {
		entry["mainbranch"] = map[string]any{"name": mainBranch, "type": "branch"}
	} else {
		entry["mainbranch"] = nil
	}
	return entry
}

func TestFetchBitbucketReposFollowsNext(t *testing.T) {
	var gotPaths []string
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		gotPaths = append(gotPaths, r.URL.RequestURI())
		switch r.URL.Query().Get("page") {
		case "":
			if r.URL.Query().Get("role") != "member" || r.URL.Query().Get("pagelen") != fmt.Sprint(bitbucketRepoPageLen) {
				t.Errorf("first page query = %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"values": []map[string]any{bitbucketRepoJSON("ws/one", "main"), bitbucketRepoJSON("ws/empty", "")},
				"next":   bitbucketAPIBase + "/repositories?role=member&page=2",
			})
		case "2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"values": []map[string]any{bitbucketRepoJSON("ws/two", "master")},
			})
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	})

	repos, err := fetchBitbucketRepos(t.Context(), srv.Client(), "test-token", "")
	if err != nil {
		t.Fatalf("fetchBitbucketRepos: %v", err)
	}
	if len(gotPaths) != 2 {
		t.Fatalf("requests = %v, want 2 pages", gotPaths)
	}
	want := []GitHubRepo{
		{FullName: "ws/one", Private: true, DefaultBranch: "main", HTMLURL: "https://bitbucket.org/ws/one", CloneURL: "https://bitbucket.org/ws/one.git", PushedAt: "2026-09-13T00:00:00+00:00"},
		{FullName: "ws/empty", Private: true, DefaultBranch: "", HTMLURL: "https://bitbucket.org/ws/empty", CloneURL: "https://bitbucket.org/ws/empty.git", PushedAt: "2026-09-13T00:00:00+00:00"},
		{FullName: "ws/two", Private: true, DefaultBranch: "master", HTMLURL: "https://bitbucket.org/ws/two", CloneURL: "https://bitbucket.org/ws/two.git", PushedAt: "2026-09-13T00:00:00+00:00"},
	}
	if len(repos) != len(want) {
		t.Fatalf("len(repos) = %d, want %d: %+v", len(repos), len(want), repos)
	}
	for i := range want {
		if repos[i] != want[i] {
			t.Errorf("repos[%d] = %+v, want %+v", i, repos[i], want[i])
		}
	}
}

// ListRepos stops following next at the cap.
func TestFetchBitbucketReposCapsAtMax(t *testing.T) {
	full := make([]map[string]any, bitbucketRepoPageLen)
	for i := range full {
		full[i] = bitbucketRepoJSON(fmt.Sprintf("ws/r%d", i), "main")
	}
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"values": full, "next": bitbucketAPIBase + "/repositories?page=more"})
	})

	repos, err := fetchBitbucketRepos(t.Context(), srv.Client(), "test-token", "")
	if err != nil {
		t.Fatalf("fetchBitbucketRepos: %v", err)
	}
	if len(repos) != bitbucketRepoMaxRepos {
		t.Fatalf("len(repos) = %d, want capped at %d", len(repos), bitbucketRepoMaxRepos)
	}
}

func TestFetchBitbucketReposStatusError(t *testing.T) {
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"message":"Access token has insufficient scope"}}`))
	})
	_, err := fetchBitbucketRepos(t.Context(), srv.Client(), "test-token", "")
	if err == nil || !strings.Contains(err.Error(), "list repos: bitbucket: status 403: Access token has insufficient scope") {
		t.Fatalf("err = %v", err)
	}
}

// bitbucketSourceWith builds a source with a fixed resolver result.
func bitbucketSourceWith(t *testing.T, client *http.Client, token string, resolveErr error) *bitbucketSource {
	t.Helper()
	src, err := BitbucketBuilder(client)(t.Context(), Connector{Name: "bb", Kind: "bitbucket", CredentialRef: "BB_TOKEN"},
		func(_ context.Context, _ string) (string, error) { return token, resolveErr })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return src.(*bitbucketSource)
}

// Test, Identity and ListRepos through the Source; a failing resolver must not reach the network.
func TestBitbucketSourceMethods(t *testing.T) {
	var hits int
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"username":"taki","display_name":"Taki"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`{"values":[]}`))
		case "/repositories":
			_ = json.NewEncoder(w).Encode(map[string]any{"values": []map[string]any{bitbucketRepoJSON("ws/one", "main")}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})

	ok := bitbucketSourceWith(t, srv.Client(), "tok", nil)
	if err := ok.Test(t.Context()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	id, err := ok.Identity(t.Context())
	if err != nil || id.Login != "taki" {
		t.Fatalf("Identity = %+v, %v", id, err)
	}
	repos, err := ok.ListRepos(t.Context())
	if err != nil || len(repos) != 1 || repos[0].FullName != "ws/one" {
		t.Fatalf("ListRepos = %+v, %v", repos, err)
	}

	hits = 0
	broken := bitbucketSourceWith(t, srv.Client(), "", fmt.Errorf("vault sealed"))
	for name, call := range map[string]func() error{
		"Test":      func() error { return broken.Test(t.Context()) },
		"Identity":  func() error { _, err := broken.Identity(t.Context()); return err },
		"ListRepos": func() error { _, err := broken.ListRepos(t.Context()); return err },
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), `resolve credential_ref "BB_TOKEN": vault sealed`) {
			t.Errorf("%s with failing resolver: err = %v", name, err)
		}
	}
	if hits != 0 {
		t.Fatalf("resolver failure still made %d requests", hits)
	}
}

// A 200 with a bad body is a decode error naming the endpoint, not an empty result.
func TestBitbucketDecodeErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bad     string // path that returns garbage
		call    func(client *http.Client) error
		wantErr string
	}{
		{
			name:    "/user",
			bad:     "/user",
			call:    func(c *http.Client) error { _, err := fetchBitbucketIdentity(t.Context(), c, "tok", ""); return err },
			wantErr: "decode /user:",
		},
		{
			name:    "/user/emails",
			bad:     "/user/emails",
			call:    func(c *http.Client) error { _, err := fetchBitbucketIdentity(t.Context(), c, "tok", ""); return err },
			wantErr: "decode /user/emails:",
		},
		{
			name:    "/repositories",
			bad:     "/repositories",
			call:    func(c *http.Client) error { _, err := fetchBitbucketRepos(t.Context(), c, "tok", ""); return err },
			wantErr: "list repos:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.bad {
					_, _ = w.Write([]byte(`{not json`))
					return
				}
				_, _ = w.Write([]byte(`{"username":"taki","values":[]}`))
			})
			err := tc.call(srv.Client())
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

// Request failures before any status code, with the token never echoed.
func TestBitbucketRequestErrors(t *testing.T) {
	t.Run("unbuildable URL", func(t *testing.T) {
		prev := bitbucketAPIBase
		bitbucketAPIBase = "http://[::1]:namedport"
		t.Cleanup(func() { bitbucketAPIBase = prev })
		_, err := fetchBitbucketIdentity(t.Context(), &http.Client{}, "secret-token", "")
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("connection refused", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(http.ResponseWriter, *http.Request) {})
		srv.Close()
		_, err := fetchBitbucketRepos(t.Context(), &http.Client{}, "secret-token", "")
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestBitbucketStatusErrorTruncatesLongText(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusBadGateway)
	_, _ = rec.WriteString(strings.Repeat("x", 250))
	err := bitbucketStatusError(rec.Result())
	want := "bitbucket: status 502: " + strings.Repeat("x", 200)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v (len %d)", err, len(err.Error()))
	}
}

// The connection dying on /user/emails surfaces without the token.
func TestBitbucketEmailRequestError(t *testing.T) {
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			_, _ = w.Write([]byte(`{"username":"taki"}`))
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = conn.Close()
	})
	_, err := fetchBitbucketIdentity(t.Context(), srv.Client(), "secret-token", "")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v", err)
	}
}

// A page larger than pagelen can overshoot the cap; the result is trimmed.
func TestFetchBitbucketReposTrimsOversizedLastPage(t *testing.T) {
	big := make([]map[string]any, 2*bitbucketRepoPageLen)
	for i := range big {
		big[i] = bitbucketRepoJSON(fmt.Sprintf("ws/r%d", i), "main")
	}
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"values": big, "next": bitbucketAPIBase + "/repositories?page=more"})
	})
	repos, err := fetchBitbucketRepos(t.Context(), srv.Client(), "tok", "")
	if err != nil {
		t.Fatalf("fetchBitbucketRepos: %v", err)
	}
	if len(repos) != bitbucketRepoMaxRepos {
		t.Fatalf("len(repos) = %d, want exactly %d", len(repos), bitbucketRepoMaxRepos)
	}
}

func TestBitbucketGetRepo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		want    string
		wantErr error
	}{
		{"found", http.StatusOK, `{"full_name":"acme/widgets","mainbranch":{"name":"develop"},"links":{"clone":[{"name":"https","href":"https://bitbucket.org/acme/widgets.git"}]}}`, "develop", nil},
		{"404 is the not-found sentinel", http.StatusNotFound, "Not Found", "", ErrRepoNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repositories/acme/widgets" {
					t.Errorf("path = %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			repo, err := bitbucketSourceWith(t, srv.Client(), "tok", nil).GetRepo(t.Context(), "acme", "widgets")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || repo.DefaultBranch != tc.want || repo.CloneURL != "https://bitbucket.org/acme/widgets.git" {
				t.Fatalf("repo = %+v, err %v", repo, err)
			}
		})
	}
}

func TestBitbucketCreateRepo(t *testing.T) {
	t.Run("posts scm and visibility to workspace/slug", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/repositories/acme/widgets" {
				t.Errorf("%s %s", r.Method, r.URL.Path)
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["scm"] != "git" || body["is_private"] != true {
				t.Errorf("body = %v", body)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"full_name":"acme/widgets","links":{"clone":[{"name":"https","href":"https://bitbucket.org/acme/widgets.git"}]}}`))
		})
		repo, err := bitbucketSourceWith(t, srv.Client(), "tok", nil).CreateRepo(t.Context(), "acme/widgets", true)
		if err != nil || repo.CloneURL != "https://bitbucket.org/acme/widgets.git" {
			t.Fatalf("repo = %+v, err %v", repo, err)
		}
	})
	t.Run("a bare name has no workspace", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(http.ResponseWriter, *http.Request) { t.Error("must not call the API") })
		_, err := bitbucketSourceWith(t, srv.Client(), "tok", nil).CreateRepo(t.Context(), "widgets", true)
		if err == nil || !strings.Contains(err.Error(), "workspace/slug") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("api error", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"message":"project key is required"}}`))
		})
		_, err := bitbucketSourceWith(t, srv.Client(), "tok", nil).CreateRepo(t.Context(), "acme/widgets", true)
		if err == nil || !strings.Contains(err.Error(), "create repo: bitbucket: status 400: project key is required") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestBitbucketCreatePR(t *testing.T) {
	const created = `{"id":12,"state":"OPEN","links":{"html":{"href":"https://bitbucket.org/acme/widgets/pull-requests/12"}}}`
	t.Run("created", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			src := body["source"].(map[string]any)["branch"].(map[string]any)["name"]
			dst := body["destination"].(map[string]any)["branch"].(map[string]any)["name"]
			if body["title"] != "feat: thing" || src != "feat/x" || dst != "main" {
				t.Errorf("body = %v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(created))
		})
		pr, err := bitbucketSourceWith(t, srv.Client(), "tok", nil).CreatePR(t.Context(), "acme", "widgets", "feat: thing", "feat/x", "main", "body")
		if err != nil || pr.Number != 12 || pr.HTMLURL != "https://bitbucket.org/acme/widgets/pull-requests/12" || pr.State != "open" {
			t.Fatalf("pr = %+v, err %v", pr, err)
		}
	})
	t.Run("duplicate returns the existing open pr", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"type":"error","error":{"message":"There is already a pull request open"}}`))
			case http.MethodGet:
				if r.URL.Query().Get("q") != `source.branch.name="feat/x"` || r.URL.Query().Get("state") != "OPEN" {
					t.Errorf("query = %s", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`{"values":[` + created + `]}`))
			}
		})
		pr, err := bitbucketSourceWith(t, srv.Client(), "tok", nil).CreatePR(t.Context(), "acme", "widgets", "t", "feat/x", "main", "")
		if err != nil || pr.Number != 12 {
			t.Fatalf("pr = %+v, err %v", pr, err)
		}
	})
	t.Run("400 with no existing pr keeps the original error", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"type":"error","error":{"message":"destination branch missing"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"values":[]}`))
		})
		_, err := bitbucketSourceWith(t, srv.Client(), "tok", nil).CreatePR(t.Context(), "acme", "widgets", "t", "feat/x", "main", "")
		if err == nil || !strings.Contains(err.Error(), "create pr: bitbucket: status 400: destination branch missing") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("403 does not look for an existing pr", func(t *testing.T) {
		var gets int
		srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				gets++
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"type":"error","error":{"message":"no"}}`))
		})
		_, err := bitbucketSourceWith(t, srv.Client(), "tok", nil).CreatePR(t.Context(), "acme", "widgets", "t", "feat/x", "main", "")
		if err == nil || gets != 0 {
			t.Fatalf("err = %v, gets = %d", err, gets)
		}
	})
}

func TestBitbucketPRMerged(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  bool
	}{{"MERGED", true}, {"OPEN", false}, {"DECLINED", false}} {
		t.Run(tc.state, func(t *testing.T) {
			srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repositories/acme/widgets/pullrequests/12" {
					t.Errorf("path = %s", r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"id":12,"state":"` + tc.state + `"}`))
			})
			merged, err := bitbucketSourceWith(t, srv.Client(), "tok", nil).PRMerged(t.Context(), "acme", "widgets", 12)
			if err != nil || merged != tc.want {
				t.Fatalf("merged = %v, err %v", merged, err)
			}
		})
	}
}

// With all five methods present the manager's repoSource assertion admits
// the kind: this is what the mission repo picker and the PR flow go through.
func TestManagerRepoSourceAdmitsBitbucket(t *testing.T) {
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repositories" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"values": []map[string]any{bitbucketRepoJSON("acme/widgets", "main")}})
		case r.URL.Path == "/repositories/acme/widgets/pullrequests/3":
			_, _ = w.Write([]byte(`{"id":3,"state":"MERGED"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	m := testManager(fakeRows{rows: []Connector{{ID: "1", Name: "work-bb", Kind: "bitbucket", CredentialRef: "BB_TOKEN"}}})
	m.RegisterBuilder("bitbucket", BitbucketBuilder(srv.Client()))

	repos, err := m.ListRepos(t.Context(), "1")
	if err != nil || len(repos) != 1 || repos[0].FullName != "acme/widgets" {
		t.Fatalf("ListRepos = %+v, %v", repos, err)
	}
	merged, err := m.PRMerged(t.Context(), "1", "acme", "widgets", 3)
	if err != nil || !merged {
		t.Fatalf("PRMerged = %v, %v", merged, err)
	}
}

// Error paths shared by the repo and PR methods: a failing resolver, a
// non-200 status, a bad body, and a request that cannot be built.
func TestBitbucketRepoMethodErrors(t *testing.T) {
	calls := func(s *bitbucketSource) map[string]func() error {
		return map[string]func() error{
			"GetRepo":    func() error { _, err := s.GetRepo(t.Context(), "acme", "widgets"); return err },
			"CreateRepo": func() error { _, err := s.CreateRepo(t.Context(), "acme/widgets", true); return err },
			"CreatePR":   func() error { _, err := s.CreatePR(t.Context(), "acme", "widgets", "t", "h", "b", ""); return err },
			"PRMerged":   func() error { _, err := s.PRMerged(t.Context(), "acme", "widgets", 1); return err },
		}
	}

	t.Run("resolver failure makes no request", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(http.ResponseWriter, *http.Request) { t.Error("must not call the API") })
		for name, call := range calls(bitbucketSourceWith(t, srv.Client(), "", fmt.Errorf("vault sealed"))) {
			if err := call(); err == nil || !strings.Contains(err.Error(), "vault sealed") {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
	t.Run("server error", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		})
		for name, call := range calls(bitbucketSourceWith(t, srv.Client(), "tok", nil)) {
			if err := call(); err == nil || !strings.Contains(err.Error(), "status 500: boom") {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
	t.Run("bad body", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{nope`)) })
		for name, call := range calls(bitbucketSourceWith(t, srv.Client(), "tok", nil)) {
			if err := call(); err == nil || !strings.Contains(err.Error(), "decode response") {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
	t.Run("unbuildable url", func(t *testing.T) {
		prev := bitbucketAPIBase
		bitbucketAPIBase = "http://[::1]:namedport"
		t.Cleanup(func() { bitbucketAPIBase = prev })
		for name, call := range calls(bitbucketSourceWith(t, &http.Client{}, "secret-token", nil)) {
			if err := call(); err == nil || strings.Contains(err.Error(), "secret-token") {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
	t.Run("duplicate pr but the lookup fails", func(t *testing.T) {
		for _, lookup := range []struct {
			name, body string
			status     int
		}{
			{"status", `{"type":"error","error":{"message":"denied"}}`, http.StatusForbidden},
			{"decode", `{nope`, http.StatusOK},
		} {
			t.Run(lookup.name, func(t *testing.T) {
				srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write([]byte(`{"type":"error","error":{"message":"exists"}}`))
						return
					}
					w.WriteHeader(lookup.status)
					_, _ = w.Write([]byte(lookup.body))
				})
				_, err := bitbucketSourceWith(t, srv.Client(), "tok", nil).CreatePR(t.Context(), "acme", "widgets", "t", "h", "b", "")
				if err == nil || !strings.Contains(err.Error(), "status 409: exists") || !strings.Contains(err.Error(), "could not fetch existing") {
					t.Fatalf("err = %v", err)
				}
			})
		}
	})
}

func TestBitbucketPostConnectionRefused(t *testing.T) {
	srv := bitbucketFakeServer(t, func(http.ResponseWriter, *http.Request) {})
	srv.Close()
	_, err := bitbucketSourceWith(t, &http.Client{}, "secret-token", nil).CreatePR(t.Context(), "acme", "widgets", "t", "h", "b", "")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v", err)
	}
}

func TestBitbucketFindExistingPRConnectionDrops(t *testing.T) {
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"type":"error","error":{"message":"exists"}}`))
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = conn.Close()
	})
	_, err := bitbucketSourceWith(t, srv.Client(), "tok", nil).CreatePR(t.Context(), "acme", "widgets", "t", "h", "b", "")
	if err == nil || !strings.Contains(err.Error(), "could not fetch existing") {
		t.Fatalf("err = %v", err)
	}
}

// A workspace or repository access token is not a user: /user answers 403
// and the token is verified against its configured workspace instead.
func TestFetchBitbucketIdentityAccessToken(t *testing.T) {
	notAUser := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"message":"This API is not accessible by this authentication mechanism"}}`))
	}
	for _, tc := range []struct {
		name      string
		workspace string
		handler   func(w http.ResponseWriter)
		want      GitHubIdentity
		wantErr   string
	}{
		{
			name:      "named by its workspace",
			workspace: "acme-team",
			handler: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"slug":"acme-team","name":"Acme Team"}`))
			},
			want: GitHubIdentity{Login: "acme-team", Name: "Acme Team (access token)", Email: "", Scopes: "workspace or repository access token"},
		},
		{
			name:    "no workspace configured is a clear error",
			wantErr: "set the connector's workspace",
		},
		{
			name:      "workspace not visible to the token",
			workspace: "other",
			handler: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"type":"error","error":{"message":"No workspace with identifier 'other'."}}`))
			},
			wantErr: "identify access token: bitbucket: status 404: No workspace with identifier 'other'.",
		},
		{
			name:      "workspace bad body",
			workspace: "acme-team",
			handler:   func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{nope`)) },
			wantErr:   "decode /workspaces",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var emailCalls int
			srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user":
					notAUser(w)
				case "/workspaces/" + tc.workspace:
					tc.handler(w)
				case "/user/emails":
					emailCalls++
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			})
			got, err := fetchBitbucketIdentity(t.Context(), srv.Client(), "tok", tc.workspace)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("identity = %+v, err %v, want %+v", got, err, tc.want)
			}
			if emailCalls != 0 {
				t.Fatalf("/user/emails called %d times for a non-user token", emailCalls)
			}
		})
	}
}

func TestFetchBitbucketIdentityAccessTokenConnectionDrops(t *testing.T) {
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = conn.Close()
	})
	_, err := fetchBitbucketIdentity(t.Context(), srv.Client(), "secret-token", "acme-team")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v", err)
	}
}

func TestBitbucketBuilderConfig(t *testing.T) {
	t.Parallel()
	b := BitbucketBuilder(nil)
	resolve := func(_ context.Context, _ string) (string, error) { return "tok", nil }
	src, err := b(t.Context(), Connector{Name: "bb", Kind: "bitbucket", CredentialRef: "R", Config: json.RawMessage(`{"workspace":" acme-team "}`)}, resolve)
	if err != nil || src.(*bitbucketSource).workspace != "acme-team" {
		t.Fatalf("workspace = %q, err %v", src.(*bitbucketSource).workspace, err)
	}
	if _, err := b(t.Context(), Connector{Name: "bb", Kind: "bitbucket", CredentialRef: "R", Config: json.RawMessage(`{nope`)}, resolve); err == nil {
		t.Fatal("bad config accepted")
	}
}

func TestFetchBitbucketReposScopedToWorkspace(t *testing.T) {
	srv := bitbucketFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repositories/acme-team" || r.URL.Query().Get("role") != "" {
			t.Errorf("request = %s", r.URL.RequestURI())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"values": []map[string]any{bitbucketRepoJSON("acme-team/widgets", "main")}})
	})
	repos, err := fetchBitbucketRepos(t.Context(), srv.Client(), "tok", "acme-team")
	if err != nil || len(repos) != 1 || repos[0].FullName != "acme-team/widgets" {
		t.Fatalf("repos = %+v, err %v", repos, err)
	}
}

// resolveRepoName is what stands between a bare mission slug and the
// workspace/slug the API needs (issue #786).
func TestBitbucketResolveRepoName(t *testing.T) {
	tests := []struct {
		name          string
		workspace     string
		arg           string
		wantWorkspace string
		wantSlug      string
		wantErr       bool
	}{
		{name: "explicit workspace/slug wins", workspace: "cfgws", arg: "argws/repo", wantWorkspace: "argws", wantSlug: "repo"},
		{name: "explicit workspace/slug with no config", arg: "argws/repo", wantWorkspace: "argws", wantSlug: "repo"},
		{name: "bare slug falls back to config", workspace: "cfgws", arg: "fix-widgets-abc", wantWorkspace: "cfgws", wantSlug: "fix-widgets-abc"},
		{name: "bare slug with blank workspace errors", arg: "fix-widgets-abc", wantErr: true},
		{name: "bare slug is trimmed", workspace: "cfgws", arg: "  repo  ", wantWorkspace: "cfgws", wantSlug: "repo"},
		{name: "nested path is not a slug", workspace: "cfgws", arg: "a/b/c", wantErr: true},
		{name: "slug with a space is rejected", workspace: "cfgws", arg: "not a slug", wantErr: true},
		{name: "empty name is rejected", workspace: "cfgws", arg: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &bitbucketSource{name: "bb", workspace: tc.workspace}
			ws, slug, err := s.resolveRepoName(tc.arg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveRepoName(%q) = %q/%q, want error", tc.arg, ws, slug)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRepoName(%q): %v", tc.arg, err)
			}
			if ws != tc.wantWorkspace || slug != tc.wantSlug {
				t.Fatalf("resolveRepoName(%q) = %q/%q, want %q/%q", tc.arg, ws, slug, tc.wantWorkspace, tc.wantSlug)
			}
		})
	}
}

// A blank workspace must say so: the operator has to know which knob
// to turn, not just that the slug looked wrong.
func TestBitbucketResolveRepoNameBlankWorkspaceError(t *testing.T) {
	s := &bitbucketSource{name: "my-bb", workspace: ""}
	_, _, err := s.resolveRepoName("fix-widgets-abc")
	if err == nil {
		t.Fatal("want error for bare slug with no configured workspace")
	}
	if !strings.Contains(err.Error(), "workspace") || !strings.Contains(err.Error(), "my-bb") {
		t.Fatalf("error %q should name the workspace and the connector", err)
	}
}
