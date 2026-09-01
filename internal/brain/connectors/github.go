package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// GitHubConfig is the connectors.config shape for kind='github'. Auth
// is entirely the connector's credential_ref (a PAT in the secret
// store). SignCommits opts this connector into SSH commit signing
// (D-058): mission commits cloned through it are signed with the
// connector's own ed25519 key. SigningPublicKey is the authorized_keys
// line for that key — public, so it lives here (not the secret store)
// for the UI to re-display; the private half is stored in the secret
// store under a ref derived from CredentialRef (see signing.go).
type GitHubConfig struct {
	SignCommits      bool   `json:"sign_commits,omitempty"`
	SigningPublicKey string `json:"signing_public_key,omitempty"`
}

// githubAPIBase is a var, not a const, so tests can point it at a fake
// server; production never reassigns it.
var githubAPIBase = "https://api.github.com"

const (
	githubAPIVersion = "2022-11-28"

	githubCallTimeout = 10 * time.Second
)

// GitHubIdentity is what Test resolves and reports: enough to confirm
// which account a PAT authenticates as and what it can commit as.
type GitHubIdentity struct {
	Login  string `json:"login"`
	Name   string `json:"name"`
	Email  string `json:"email"`  // resolved commit email, see resolveEmail
	Scopes string `json:"scopes"` // "fine-grained token" when the classic scopes header is absent
}

// GitHubRepo is the wire shape of one repo a connector's PAT can see or
// create — the fields mission creation's repo picker/create-new flow
// needs, nothing more.
type GitHubRepo struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	PushedAt      string `json:"pushed_at"`
}

// githubRepoPerPage is the max GitHub allows per page; githubRepoMaxRepos
// bounds how many pages ListRepos follows so a PAT with an enormous
// number of repos can't make mission creation's repo list hang.
const (
	githubRepoPerPage  = 100
	githubRepoMaxRepos = 300
)

// GitHubBuilder returns the Builder for kind='github'. The source
// serves zero tools by design: identity/credential connector only,
// chat tools stay on the MCP-based GitHub connector.
func GitHubBuilder(client *http.Client) Builder {
	if client == nil {
		client = &http.Client{}
	}
	return func(_ context.Context, c Connector, resolve Resolve) (Source, error) {
		if c.CredentialRef == "" {
			return nil, fmt.Errorf("github %s: credential_ref is required (a GitHub PAT)", c.Name)
		}
		return &githubSource{name: c.Name, credentialRef: c.CredentialRef, resolve: resolve, client: client}, nil
	}
}

// githubSource is a built github-kind connector: no tools, Test
// resolves the PAT and confirms it authenticates against the GitHub API.
type githubSource struct {
	name          string
	credentialRef string
	resolve       Resolve
	client        *http.Client
}

// Tools is empty: identity/credential connector, no chat tools.
func (s *githubSource) Tools() []*tools.Tool { return nil }

func (s *githubSource) Test(ctx context.Context) error {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	_, err = fetchGitHubIdentity(ctx, s.client, token)
	return err
}

// Identity resolves and returns the connector's GitHub identity — the
// richer counterpart to Test, called by the test endpoint to report
// login/name/email/scopes alongside the pass/fail.
func (s *githubSource) Identity(ctx context.Context) (GitHubIdentity, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return GitHubIdentity{}, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return fetchGitHubIdentity(ctx, s.client, token)
}

func (s *githubSource) Close() error { return nil }

// ListRepos resolves the connector's PAT and returns every repo it can
// see (owner, collaborator, or org member), most recently pushed
// first, following pagination up to githubRepoMaxRepos.
func (s *githubSource) ListRepos(ctx context.Context) ([]GitHubRepo, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return nil, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return fetchGitHubRepos(ctx, s.client, token)
}

// CreateRepo resolves the connector's PAT and creates a new repo owned
// by whichever account the PAT authenticates as, with auto_init so it
// has a default branch to clone.
func (s *githubSource) CreateRepo(ctx context.Context, name string, private bool) (GitHubRepo, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return GitHubRepo{}, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return createGitHubRepo(ctx, s.client, token, name, private)
}

// fetchGitHubRepos pages through GET /user/repos, capped at
// githubRepoMaxRepos so a PAT with an enormous number of repos can't
// make this hang.
func fetchGitHubRepos(ctx context.Context, client *http.Client, token string) ([]GitHubRepo, error) {
	var out []GitHubRepo
	for page := 1; len(out) < githubRepoMaxRepos; page++ {
		path := fmt.Sprintf("/user/repos?affiliation=owner,collaborator,organization_member&sort=pushed&per_page=%d&page=%d", githubRepoPerPage, page)
		resp, err := githubRequest(ctx, client, token, path)
		if err != nil {
			return nil, err
		}
		var repos []GitHubRepo
		err = func() error {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return githubStatusError(resp)
			}
			return json.NewDecoder(resp.Body).Decode(&repos)
		}()
		if err != nil {
			return nil, fmt.Errorf("list repos: %w", err)
		}
		out = append(out, repos...)
		if len(repos) < githubRepoPerPage {
			break
		}
	}
	if len(out) > githubRepoMaxRepos {
		out = out[:githubRepoMaxRepos]
	}
	return out, nil
}

// createGitHubRepo issues POST /user/repos. auto_init is always true so
// the new repo has a default branch to clone into a mission workspace.
func createGitHubRepo(ctx context.Context, client *http.Client, token, name string, private bool) (GitHubRepo, error) {
	body, err := json.Marshal(map[string]any{"name": name, "private": private, "auto_init": true})
	if err != nil {
		return GitHubRepo{}, fmt.Errorf("create repo: encode request: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, githubAPIBase+"/user/repos", bytes.NewReader(body))
	if err != nil {
		cancel()
		return GitHubRepo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return GitHubRepo{}, err
	}
	defer cancel()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return GitHubRepo{}, fmt.Errorf("create repo: %w", githubStatusError(resp))
	}
	var repo GitHubRepo
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		return GitHubRepo{}, fmt.Errorf("create repo: decode response: %w", err)
	}
	return repo, nil
}

// GitHubPR is the wire shape of one pull request the PR-create flow
// needs: enough for POST /v1/missions/{id}/pr's {url, number} response
// and to detect/return an already-open PR for the same head.
type GitHubPR struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
}

// githubPRAlreadyExistsMarker is the distinctive substring GitHub's 422
// response carries when a PR for the given head/base already exists —
// the signal CreatePR uses to fetch and return the existing PR instead
// of erroring.
const githubPRAlreadyExistsMarker = "A pull request already exists for"

// ErrRepoNotFound marks GetRepo's 404: the repo doesn't exist (or the
// PAT can't see it), the create-if-missing delivery path's signal to
// create it rather than treat the lookup as a hard failure (issue
// #483). Distinct from connectors.ErrNotFound, which names a missing
// connectors table row, not a missing GitHub repo.
var ErrRepoNotFound = fmt.Errorf("github: repo not found")

// GetRepo resolves the connector's PAT and returns owner/repo's
// metadata (default_branch is all the PR flow needs from it).
func (s *githubSource) GetRepo(ctx context.Context, owner, repo string) (GitHubRepo, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return GitHubRepo{}, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return fetchGitHubRepo(ctx, s.client, token, owner, repo)
}

// CreatePR opens a pull request head -> base with title/body. If GitHub
// reports one already exists for this head (422,
// githubPRAlreadyExistsMarker), the existing open PR is fetched and
// returned instead of erroring — idempotent re-calls (e.g. a re-push
// through the same endpoint) never fail on this.
func (s *githubSource) CreatePR(ctx context.Context, owner, repo, title, head, base, body string) (GitHubPR, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return GitHubPR{}, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	pr, err := createGitHubPR(ctx, s.client, token, owner, repo, title, head, base, body)
	if err == nil {
		return pr, nil
	}
	if !strings.Contains(err.Error(), githubPRAlreadyExistsMarker) {
		return GitHubPR{}, err
	}
	existing, findErr := findOpenGitHubPR(ctx, s.client, token, owner, repo, head)
	if findErr != nil {
		return GitHubPR{}, fmt.Errorf("create pr: %w (and could not fetch existing: %v)", err, findErr)
	}
	if existing == nil {
		return GitHubPR{}, fmt.Errorf("create pr: %w (github reported one exists, but none was found open for head %s)", err, head)
	}
	return *existing, nil
}

// PRMerged resolves the connector's PAT and reports whether owner/repo
// pull request number has been merged.
func (s *githubSource) PRMerged(ctx context.Context, owner, repo string, number int) (bool, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return false, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return fetchGitHubPRMerged(ctx, s.client, token, owner, repo, number)
}

// fetchGitHubPRMerged calls GET /repos/{owner}/{repo}/pulls/{number} and
// returns its merged field.
func fetchGitHubPRMerged(ctx context.Context, client *http.Client, token, owner, repo string, number int) (bool, error) {
	resp, err := githubRequest(ctx, client, token, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number))
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("get pull request: %w", githubStatusError(resp))
	}
	var pr struct {
		Merged bool `json:"merged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return false, fmt.Errorf("get pull request: decode response: %w", err)
	}
	return pr.Merged, nil
}

// fetchGitHubRepo calls GET /repos/{owner}/{repo}.
func fetchGitHubRepo(ctx context.Context, client *http.Client, token, owner, repo string) (GitHubRepo, error) {
	resp, err := githubRequest(ctx, client, token, fmt.Sprintf("/repos/%s/%s", owner, repo))
	if err != nil {
		return GitHubRepo{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return GitHubRepo{}, fmt.Errorf("get repo: %w", ErrRepoNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return GitHubRepo{}, fmt.Errorf("get repo: %w", githubStatusError(resp))
	}
	var r GitHubRepo
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return GitHubRepo{}, fmt.Errorf("get repo: decode response: %w", err)
	}
	return r, nil
}

// createGitHubPR issues POST /repos/{owner}/{repo}/pulls. The error
// returned on a non-201 response embeds GitHub's parsed message(s) so
// CreatePR's own already-exists check (githubPRAlreadyExistsMarker) can
// match against it.
func createGitHubPR(ctx context.Context, client *http.Client, token, owner, repo, title, head, base, body string) (GitHubPR, error) {
	reqBody, err := json.Marshal(map[string]any{"title": title, "head": head, "base": base, "body": body})
	if err != nil {
		return GitHubPR{}, fmt.Errorf("create pr: encode request: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	path := fmt.Sprintf("/repos/%s/%s/pulls", owner, repo)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, githubAPIBase+path, bytes.NewReader(reqBody))
	if err != nil {
		cancel()
		return GitHubPR{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return GitHubPR{}, err
	}
	defer cancel()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return GitHubPR{}, fmt.Errorf("create pr: %w", githubStatusError(resp))
	}
	var pr GitHubPR
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return GitHubPR{}, fmt.Errorf("create pr: decode response: %w", err)
	}
	return pr, nil
}

// findOpenGitHubPR looks up the open PR for owner/repo whose head is
// "<owner>:<branch>" (GitHub's head filter form) — used when
// createGitHubPR reports one already exists, so CreatePR can return it
// instead of erroring. Returns (nil, nil) if none is found open.
func findOpenGitHubPR(ctx context.Context, client *http.Client, token, owner, repo, head string) (*GitHubPR, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&head=%s:%s", owner, repo, owner, head)
	resp, err := githubRequest(ctx, client, token, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, githubStatusError(resp)
	}
	var prs []GitHubPR
	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return nil, fmt.Errorf("decode pulls list: %w", err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

// githubUser is the subset of GET /user this package reads.
type githubUser struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

// githubEmail is one entry of GET /user/emails.
type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// fetchGitHubIdentity resolves the account a PAT authenticates as and
// its commit email, plus token scope metadata. Never logs the token.
func fetchGitHubIdentity(ctx context.Context, client *http.Client, token string) (GitHubIdentity, error) {
	user, scopesHeader, err := githubGetUser(ctx, client, token)
	if err != nil {
		return GitHubIdentity{}, err
	}
	email, err := resolveEmail(ctx, client, token, user)
	if err != nil {
		return GitHubIdentity{}, err
	}
	scopes := scopesHeader
	if scopes == "" {
		scopes = "fine-grained token"
	}
	return GitHubIdentity{Login: user.Login, Name: user.Name, Email: email, Scopes: scopes}, nil
}

// githubGetUser calls GET /user and returns the decoded body plus the
// classic PAT's X-OAuth-Scopes header verbatim (empty for fine-grained
// tokens, which don't set it).
func githubGetUser(ctx context.Context, client *http.Client, token string) (githubUser, string, error) {
	resp, err := githubRequest(ctx, client, token, "/user")
	if err != nil {
		return githubUser{}, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return githubUser{}, "", githubStatusError(resp)
	}
	var user githubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return githubUser{}, "", fmt.Errorf("decode /user: %w", err)
	}
	return user, resp.Header.Get("X-OAuth-Scopes"), nil
}

// resolveEmail finds the commit email for user: the primary+verified
// entry from /user/emails, tolerating a fine-grained PAT without the
// Email permission (403/404) by falling back to /user's public email,
// then GitHub's stable noreply address.
func resolveEmail(ctx context.Context, client *http.Client, token string, user githubUser) (string, error) {
	resp, err := githubRequest(ctx, client, token, "/user/emails")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		var emails []githubEmail
		if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
			return "", fmt.Errorf("decode /user/emails: %w", err)
		}
		for _, e := range emails {
			if e.Primary && e.Verified {
				return e.Email, nil
			}
		}
	} else if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
		return "", githubStatusError(resp)
	}

	if user.Email != "" {
		return user.Email, nil
	}
	return fmt.Sprintf("%d+%s@users.noreply.github.com", user.ID, user.Login), nil
}

// githubRequest issues one authenticated GitHub API GET. The token
// never appears in an error: only the status code and a short body
// snippet are surfaced.
func githubRequest(ctx context.Context, client *http.Client, token, path string) (*http.Response, error) {
	cctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, githubAPIBase+path, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// githubErrorBody is the subset of GitHub's error response this
// package reads: {"message": "..."} on virtually every non-2xx reply,
// plus the per-field "errors[].message" a 422 validation failure adds
// (e.g. CreatePR's already-exists detail, see
// githubPRAlreadyExistsMarker).
type githubErrorBody struct {
	Message string `json:"message"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// githubStatusError reports a non-200 GitHub response without ever
// including the request (and thus the token) or the raw response
// body: 401 gets a fixed reconnect-oriented message, other statuses
// keep the status code plus GitHub's parsed message field(s), if any.
func githubStatusError(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("GitHub token invalid or expired — replace the PAT")
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e githubErrorBody
	_ = json.Unmarshal(body, &e)
	msg := e.Message
	for _, sub := range e.Errors {
		if sub.Message != "" {
			msg += ": " + sub.Message
		}
	}
	if msg != "" {
		return fmt.Errorf("github: status %d: %s", resp.StatusCode, msg)
	}
	return fmt.Errorf("github: status %d", resp.StatusCode)
}
