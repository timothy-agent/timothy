package connectors

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func eventReader(t *testing.T, handler http.HandlerFunc) *GitHubEventReader {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewGitHubEventReader(Connector{ID: "c1", Name: "gh", Kind: "github", CredentialRef: "GH_PAT"},
		func(context.Context, string) (string, error) { return "tok", nil }, srv.Client(), srv.URL)
}

func TestRepoEventsETagAndHeaders(t *testing.T) {
	reset := time.Now().Add(30 * time.Minute).Truncate(time.Second)
	r := eventReader(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/repos/o/r/events" || req.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("request %s auth %q", req.URL.Path, req.Header.Get("Authorization"))
		}
		w.Header().Set("X-Poll-Interval", "90")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		if req.Header.Get("If-None-Match") == `W/"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"v1"`)
		_, _ = w.Write([]byte(`[{"id":"2","type":"PullRequestEvent","actor":{"login":"alice"},"created_at":"2030-01-01T00:00:00Z","payload":{"action":"opened"}}]`))
	})
	evs, etag, poll, rate, err := r.RepoEvents(t.Context(), "o", "r", "")
	if err != nil {
		t.Fatalf("RepoEvents: %v", err)
	}
	if len(evs) != 1 || evs[0].ID != "2" || evs[0].Actor.Login != "alice" || etag != `W/"v1"` || poll != 90*time.Second {
		t.Fatalf("events %+v etag %q poll %v", evs, etag, poll)
	}
	if rate.Remaining != 4999 || !rate.Reset.Equal(reset) {
		t.Fatalf("rate = %+v, want 4999 until %v", rate, reset)
	}
	evs, etag, _, _, err = r.RepoEvents(t.Context(), "o", "r", `W/"v1"`)
	if err != nil || evs != nil || etag != `W/"v1"` {
		t.Fatalf("304: events %v etag %q err %v, want nil events and the same etag", evs, etag, err)
	}
}

func TestRepoEventsRateLimited(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		r := eventReader(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		})
		before := time.Now()
		_, _, _, rate, err := r.RepoEvents(t.Context(), "o", "r", "")
		if !errors.Is(err, ErrGitHubRateLimited) {
			t.Fatalf("status %d: err = %v, want ErrGitHubRateLimited", status, err)
		}
		if rate.Remaining != 0 || rate.Reset.Before(before.Add(119*time.Second)) {
			t.Fatalf("status %d: rate = %+v, want reset from Retry-After", status, rate)
		}
	}
}

func TestRepoEventsOtherErrorIsNotRateLimit(t *testing.T) {
	r := eventReader(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	_, _, _, rate, err := r.RepoEvents(t.Context(), "o", "r", "")
	if err == nil || errors.Is(err, ErrGitHubRateLimited) {
		t.Fatalf("err = %v, want a plain error", err)
	}
	if rate.Remaining != -1 {
		t.Fatalf("rate.Remaining = %d, want -1 without headers", rate.Remaining)
	}
}

func TestWorkflowRunsQuery(t *testing.T) {
	since := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	r := eventReader(t, func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if req.URL.Path != "/repos/o/r/actions/runs" || q.Get("event") != "pull_request" || q.Get("status") != "completed" ||
			q.Get("created") != ">=2030-01-02T03:04:05Z" || q.Get("per_page") != "50" {
			t.Errorf("request %s", req.URL.String())
		}
		_, _ = w.Write([]byte(`{"total_count":1,"workflow_runs":[{"id":7,"name":"ci","run_attempt":2,"status":"completed","conclusion":"failure","html_url":"u","created_at":"2030-01-02T03:05:00Z","actor":{"login":"bob"},"pull_requests":[{"number":12}]}]}`))
	})
	runs, _, err := r.WorkflowRuns(t.Context(), "o", "r", since)
	if err != nil {
		t.Fatalf("WorkflowRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != 7 || runs[0].RunAttempt != 2 || runs[0].Conclusion != "failure" || runs[0].PullRequests[0].Number != 12 {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestRepoEventsBodyCap(t *testing.T) {
	r := eventReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"1","payload":"` + strings.Repeat("a", githubEventsMaxBytes) + `"}]`))
	})
	if _, _, _, _, err := r.RepoEvents(t.Context(), "o", "r", ""); err == nil {
		t.Fatal("RepoEvents decoded a body past the cap")
	}
}

func TestManagerGitHubEventReader(t *testing.T) {
	m := testManager(fakeRows{rows: []Connector{
		{ID: "1", Name: "gh", Kind: "github", CredentialRef: "GH_PAT", Enabled: true},
		{ID: "2", Name: "gh-off", Kind: "github", CredentialRef: "GH_PAT"},
		{ID: "3", Name: "bb", Kind: "bitbucket", CredentialRef: "BB", Enabled: true},
	}})
	m.RegisterBuilder("github", GitHubBuilder(nil))
	if r, err := m.GitHubEventReader(t.Context(), "1"); err != nil || r == nil || r.baseURL != githubAPIBase {
		t.Fatalf("enabled github: reader %v err %v", r, err)
	}
	for _, id := range []string{"2", "3", "missing"} {
		if _, err := m.GitHubEventReader(t.Context(), id); err == nil {
			t.Errorf("connector %s: want an error", id)
		}
	}
}
