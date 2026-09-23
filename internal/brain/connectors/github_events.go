package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
)

// githubEventsMaxBytes caps an events or workflow runs response body.
const githubEventsMaxBytes = 2 << 20

// ErrGitHubRateLimited marks a 403 or 429 reply; the GitHubRate
// returned with it says when to retry.
var ErrGitHubRateLimited = errors.New("github rate limited")

// GitHubRate is the rate limit state of one reply. Remaining is -1
// when the reply carried no rate headers.
type GitHubRate struct {
	Remaining int
	Reset     time.Time
}

// GitHubActor is the user behind an event or workflow run.
type GitHubActor struct {
	Login string `json:"login"`
}

// GitHubRepoEvent is one entry of GET /repos/{o}/{r}/events.
type GitHubRepoEvent struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Actor     GitHubActor     `json:"actor"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}

// GitHubWorkflowRun is one entry of GET /repos/{o}/{r}/actions/runs.
type GitHubWorkflowRun struct {
	ID           int64       `json:"id"`
	Name         string      `json:"name"`
	DisplayTitle string      `json:"display_title"`
	RunAttempt   int         `json:"run_attempt"`
	Status       string      `json:"status"`
	Conclusion   string      `json:"conclusion"`
	HTMLURL      string      `json:"html_url"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	Actor        GitHubActor `json:"actor"`
	PullRequests []struct {
		Number int `json:"number"`
	} `json:"pull_requests"`
}

// GitHubEventReader reads a github connector's repo event feed and
// workflow runs for the event poller (issue #826).
type GitHubEventReader struct {
	credentialRef string
	resolve       Resolve
	client        *http.Client
	baseURL       string
}

// NewGitHubEventReader builds a reader for connector c against baseURL
// (the GitHub REST API root).
func NewGitHubEventReader(c Connector, resolve Resolve, client *http.Client, baseURL string) *GitHubEventReader {
	if client == nil {
		client = &http.Client{}
	}
	return &GitHubEventReader{credentialRef: c.CredentialRef, resolve: resolve, client: client, baseURL: baseURL}
}

// GitHubEventReader returns a reader for connector id, which must be an
// enabled github connector. It reuses the github builder's client.
func (m *Manager) GitHubEventReader(ctx context.Context, id string) (*GitHubEventReader, error) {
	c, err := m.rows.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Kind != string(gitprovider.KindGitHub) {
		return nil, fmt.Errorf("connector %s is kind %s, not github: %w", c.Name, c.Kind, ErrUnsupported)
	}
	if !c.Enabled {
		return nil, fmt.Errorf("connector %s is disabled", c.Name)
	}
	b, ok := m.builders[c.Kind]
	if !ok {
		return nil, fmt.Errorf("connector kind %s has no builder yet: %w", c.Kind, ErrUnsupported)
	}
	src, err := b(ctx, c, m.resolve)
	if err != nil {
		return nil, err
	}
	gs, ok := src.(*githubSource)
	if !ok {
		_ = src.Close()
		return nil, fmt.Errorf("connector %s: github builder returned %T: %w", c.Name, src, ErrUnsupported)
	}
	return NewGitHubEventReader(c, gs.resolve, gs.client, githubAPIBase), nil
}

// Identity returns the account the connector's PAT authenticates as.
func (r *GitHubEventReader) Identity(ctx context.Context) (gitprovider.Identity, error) {
	token, err := r.token(ctx)
	if err != nil {
		return gitprovider.Identity{}, err
	}
	var u githubUser
	if _, err := r.get(ctx, token, "/user", "", &u); err != nil {
		return gitprovider.Identity{}, fmt.Errorf("github identity: %w", err)
	}
	return gitprovider.Identity{Login: u.Login, Name: u.Name, Email: u.Email}, nil
}

// RepoEvents reads the newest page of owner/repo's event feed with
// If-None-Match etag. A 304 returns nil events and the same etag.
// pollInterval is GitHub's X-Poll-Interval, zero when absent.
func (r *GitHubEventReader) RepoEvents(ctx context.Context, owner, repo, etag string) (events []GitHubRepoEvent, newETag string, pollInterval time.Duration, rate GitHubRate, err error) {
	token, err := r.token(ctx)
	if err != nil {
		return nil, etag, 0, GitHubRate{Remaining: -1}, err
	}
	path := fmt.Sprintf("/repos/%s/%s/events?per_page=100", url.PathEscape(owner), url.PathEscape(repo))
	var out []GitHubRepoEvent
	resp, err := r.get(ctx, token, path, etag, &out)
	if resp == nil {
		return nil, etag, 0, GitHubRate{Remaining: -1}, fmt.Errorf("github events: %w", err)
	}
	rate = parseGitHubRate(resp.Header, time.Now())
	pollInterval = parsePollInterval(resp.Header)
	if err != nil {
		return nil, etag, pollInterval, rate, fmt.Errorf("github events: %w", err)
	}
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, pollInterval, rate, nil
	}
	newETag = resp.Header.Get("ETag")
	return out, newETag, pollInterval, rate, nil
}

// WorkflowRuns lists owner/repo's completed pull_request workflow runs
// created at or after since, newest first, one page of 50.
func (r *GitHubEventReader) WorkflowRuns(ctx context.Context, owner, repo string, since time.Time) ([]GitHubWorkflowRun, GitHubRate, error) {
	token, err := r.token(ctx)
	if err != nil {
		return nil, GitHubRate{Remaining: -1}, err
	}
	q := url.Values{}
	q.Set("event", "pull_request")
	q.Set("status", "completed")
	q.Set("created", ">="+since.UTC().Format(time.RFC3339))
	q.Set("per_page", "50")
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?%s", url.PathEscape(owner), url.PathEscape(repo), q.Encode())
	var out struct {
		WorkflowRuns []GitHubWorkflowRun `json:"workflow_runs"`
	}
	resp, err := r.get(ctx, token, path, "", &out)
	if resp == nil {
		return nil, GitHubRate{Remaining: -1}, fmt.Errorf("github workflow runs: %w", err)
	}
	rate := parseGitHubRate(resp.Header, time.Now())
	if err != nil {
		return nil, rate, fmt.Errorf("github workflow runs: %w", err)
	}
	return out.WorkflowRuns, rate, nil
}

func (r *GitHubEventReader) token(ctx context.Context) (string, error) {
	token, err := r.resolve(ctx, r.credentialRef)
	if err != nil {
		return "", fmt.Errorf("resolve credential_ref %q: %w", r.credentialRef, err)
	}
	return token, nil
}

// get issues one GET and decodes a 200 body (capped at
// githubEventsMaxBytes) into out. resp is nil only when no reply
// arrived; its body is closed. A 304 is not an error; a 403 or 429
// wraps ErrGitHubRateLimited.
func (r *GitHubEventReader) get(ctx context.Context, token, path, etag string, out any) (*http.Response, error) {
	cctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, r.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.NewDecoder(io.LimitReader(resp.Body, githubEventsMaxBytes)).Decode(out); err != nil {
			return resp, fmt.Errorf("decode response: %w", err)
		}
		return resp, nil
	case http.StatusNotModified:
		return resp, nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		return resp, fmt.Errorf("%w: %w", ErrGitHubRateLimited, githubStatusError(resp))
	default:
		return resp, githubStatusError(resp)
	}
}

// parseGitHubRate reads X-RateLimit-Remaining and X-RateLimit-Reset,
// falling back to Retry-After seconds from now for the reset.
func parseGitHubRate(h http.Header, now time.Time) GitHubRate {
	rate := GitHubRate{Remaining: -1}
	if n, err := strconv.Atoi(h.Get("X-RateLimit-Remaining")); err == nil {
		rate.Remaining = n
	}
	if n, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && n > 0 {
		rate.Reset = time.Unix(n, 0)
	}
	if n, err := strconv.Atoi(h.Get("Retry-After")); err == nil && n > 0 {
		if at := now.Add(time.Duration(n) * time.Second); at.After(rate.Reset) {
			rate.Reset = at
		}
	}
	return rate
}

// parsePollInterval reads X-Poll-Interval seconds; zero when absent.
func parsePollInterval(h http.Header) time.Duration {
	n, err := strconv.Atoi(h.Get("X-Poll-Interval"))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}
