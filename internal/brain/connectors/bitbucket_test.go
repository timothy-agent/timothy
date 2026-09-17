package connectors

import (
	"context"
	"encoding/json"
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

			got, err := fetchBitbucketIdentity(t.Context(), srv.Client(), "test-token")
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

	_, err := fetchBitbucketIdentity(t.Context(), srv.Client(), "secret-token")
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

	repos, err := fetchBitbucketRepos(t.Context(), srv.Client(), "test-token")
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

	repos, err := fetchBitbucketRepos(t.Context(), srv.Client(), "test-token")
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
	_, err := fetchBitbucketRepos(t.Context(), srv.Client(), "test-token")
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
			call:    func(c *http.Client) error { _, err := fetchBitbucketIdentity(t.Context(), c, "tok"); return err },
			wantErr: "decode /user:",
		},
		{
			name:    "/user/emails",
			bad:     "/user/emails",
			call:    func(c *http.Client) error { _, err := fetchBitbucketIdentity(t.Context(), c, "tok"); return err },
			wantErr: "decode /user/emails:",
		},
		{
			name:    "/repositories",
			bad:     "/repositories",
			call:    func(c *http.Client) error { _, err := fetchBitbucketRepos(t.Context(), c, "tok"); return err },
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
		_, err := fetchBitbucketIdentity(t.Context(), &http.Client{}, "secret-token")
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("connection refused", func(t *testing.T) {
		srv := bitbucketFakeServer(t, func(http.ResponseWriter, *http.Request) {})
		srv.Close()
		_, err := fetchBitbucketRepos(t.Context(), &http.Client{}, "secret-token")
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
	_, err := fetchBitbucketIdentity(t.Context(), srv.Client(), "secret-token")
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
	repos, err := fetchBitbucketRepos(t.Context(), srv.Client(), "tok")
	if err != nil {
		t.Fatalf("fetchBitbucketRepos: %v", err)
	}
	if len(repos) != bitbucketRepoMaxRepos {
		t.Fatalf("len(repos) = %d, want exactly %d", len(repos), bitbucketRepoMaxRepos)
	}
}
