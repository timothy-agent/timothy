package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// Bitbucket Cloud counterpart of the github kind (issue #656). Auth is a
// workspace or repository access token sent as a bearer.

// BitbucketConfig is the connectors.config shape for kind='bitbucket'.
// Workspace is required for a workspace or repository access token: such a
// token is not a user and cannot ask the API who it is or list repos
// account-wide, so the slug names where to look. A personal API token can
// leave it empty.
type BitbucketConfig struct {
	Workspace string `json:"workspace,omitempty"`
}

// var so tests can point it at a fake server.
var bitbucketAPIBase = "https://api.bitbucket.org/2.0"

const (
	bitbucketCallTimeout = 10 * time.Second

	// page size and cap for ListRepos, so a huge workspace can't hang the picker
	bitbucketRepoPageLen  = 100
	bitbucketRepoMaxRepos = 300
)

// BitbucketBuilder returns the Builder for kind='bitbucket'.
func BitbucketBuilder(client *http.Client) Builder {
	if client == nil {
		client = &http.Client{}
	}
	return func(_ context.Context, c Connector, resolve Resolve) (Source, error) {
		if c.CredentialRef == "" {
			return nil, fmt.Errorf("bitbucket %s: credential_ref is required (a Bitbucket access token)", c.Name)
		}
		var cfg BitbucketConfig
		if len(c.Config) > 0 {
			if err := json.Unmarshal(c.Config, &cfg); err != nil {
				return nil, fmt.Errorf("bitbucket %s: config: %w", c.Name, err)
			}
		}
		return &bitbucketSource{name: c.Name, credentialRef: c.CredentialRef, workspace: strings.TrimSpace(cfg.Workspace), resolve: resolve, client: client}, nil
	}
}

type bitbucketSource struct {
	name          string
	credentialRef string
	workspace     string
	resolve       Resolve
	client        *http.Client
}

// Tools is the read-only pull request surface (bitbucket_tools.go).
func (s *bitbucketSource) Tools() []*tools.Tool { return s.prTools() }

func (s *bitbucketSource) AccountInfo() (kind, email string) { return "bitbucket", "" }

func (s *bitbucketSource) Test(ctx context.Context) error {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	_, err = fetchBitbucketIdentity(ctx, s.client, token, s.workspace)
	return err
}

// Identity is what the connector test endpoint reports, in the shared GitHubIdentity shape.
func (s *bitbucketSource) Identity(ctx context.Context) (GitHubIdentity, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return GitHubIdentity{}, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return fetchBitbucketIdentity(ctx, s.client, token, s.workspace)
}

func (s *bitbucketSource) Close() error { return nil }

// ListRepos returns every repo the token can see, most recently updated first.
func (s *bitbucketSource) ListRepos(ctx context.Context) ([]GitHubRepo, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return nil, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return fetchBitbucketRepos(ctx, s.client, token, s.workspace)
}

// username can be absent (access tokens, users without one); nickname is the fallback.
type bitbucketUser struct {
	Username    string `json:"username"`
	Nickname    string `json:"nickname"`
	DisplayName string `json:"display_name"`
}

type bitbucketEmail struct {
	Email       string `json:"email"`
	IsPrimary   bool   `json:"is_primary"`
	IsConfirmed bool   `json:"is_confirmed"`
}

// bitbucketPage is Bitbucket's list envelope; next is absent on the last page.
type bitbucketPage[T any] struct {
	Values []T    `json:"values"`
	Next   string `json:"next"`
}

// fetchBitbucketIdentity resolves who the token authenticates as. Bitbucket has
// no scopes header, so Scopes is a fixed label. Workspace and repository
// access tokens are not users and get 403 from /user; they are identified
// by the configured workspace instead and carry no email.
func fetchBitbucketIdentity(ctx context.Context, client *http.Client, token, workspace string) (GitHubIdentity, error) {
	user, err := bitbucketGetUser(ctx, client, token)
	if errors.Is(err, errBitbucketNotAUser) {
		return fetchBitbucketTokenIdentity(ctx, client, token, workspace)
	}
	if err != nil {
		return GitHubIdentity{}, err
	}
	email, err := resolveBitbucketEmail(ctx, client, token)
	if err != nil {
		return GitHubIdentity{}, err
	}
	login := user.Username
	if login == "" {
		login = user.Nickname
	}
	return GitHubIdentity{Login: login, Name: user.DisplayName, Email: email, Scopes: "access token"}, nil
}

// errBitbucketNotAUser marks /user's 403 for an access-token principal.
var errBitbucketNotAUser = errors.New("bitbucket: token is not a user principal")

type bitbucketWorkspace struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// fetchBitbucketTokenIdentity proves an access token works against its
// configured workspace, the only thing such a token can be asked about.
func fetchBitbucketTokenIdentity(ctx context.Context, client *http.Client, token, workspace string) (GitHubIdentity, error) {
	if workspace == "" {
		return GitHubIdentity{}, fmt.Errorf("bitbucket: this token is a workspace or repository access token, not a user; set the connector's workspace so it can be verified")
	}
	resp, err := bitbucketRequest(ctx, client, token, "/workspaces/"+workspace)
	if err != nil {
		return GitHubIdentity{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return GitHubIdentity{}, fmt.Errorf("identify access token: %w", bitbucketStatusError(resp))
	}
	var ws bitbucketWorkspace
	if err := json.NewDecoder(resp.Body).Decode(&ws); err != nil {
		return GitHubIdentity{}, fmt.Errorf("decode /workspaces: %w", err)
	}
	return GitHubIdentity{Login: ws.Slug, Name: ws.Name + " (access token)", Scopes: "workspace or repository access token"}, nil
}

func bitbucketGetUser(ctx context.Context, client *http.Client, token string) (bitbucketUser, error) {
	resp, err := bitbucketRequest(ctx, client, token, "/user")
	if err != nil {
		return bitbucketUser{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusForbidden {
		return bitbucketUser{}, errBitbucketNotAUser
	}
	if resp.StatusCode != http.StatusOK {
		return bitbucketUser{}, bitbucketStatusError(resp)
	}
	var user bitbucketUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return bitbucketUser{}, fmt.Errorf("decode /user: %w", err)
	}
	return user, nil
}

// resolveBitbucketEmail returns the primary address, or "" when the token has
// no account email (workspace and repo tokens 403 here).
func resolveBitbucketEmail(ctx context.Context, client *http.Client, token string) (string, error) {
	resp, err := bitbucketRequest(ctx, client, token, "/user/emails")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		var page bitbucketPage[bitbucketEmail]
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			return "", fmt.Errorf("decode /user/emails: %w", err)
		}
		for _, e := range page.Values {
			if e.IsPrimary && e.IsConfirmed {
				return e.Email, nil
			}
		}
		for _, e := range page.Values {
			if e.IsPrimary {
				return e.Email, nil
			}
		}
		return "", nil
	case http.StatusForbidden, http.StatusNotFound:
		return "", nil
	default:
		return "", bitbucketStatusError(resp)
	}
}

type bitbucketRepo struct {
	FullName   string `json:"full_name"`
	IsPrivate  bool   `json:"is_private"`
	UpdatedOn  string `json:"updated_on"`
	MainBranch *struct {
		Name string `json:"name"`
	} `json:"mainbranch"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
		Clone []struct {
			Name string `json:"name"`
			Href string `json:"href"`
		} `json:"clone"`
	} `json:"links"`
}

// mainbranch is null on an empty repo; the https clone link sits beside ssh.
func (r bitbucketRepo) toGitHubRepo() GitHubRepo {
	out := GitHubRepo{FullName: r.FullName, Private: r.IsPrivate, HTMLURL: r.Links.HTML.Href, PushedAt: r.UpdatedOn}
	if r.MainBranch != nil {
		out.DefaultBranch = r.MainBranch.Name
	}
	for _, c := range r.Links.Clone {
		if c.Name == "https" {
			out.CloneURL = c.Href
			break
		}
	}
	return out
}

// fetchBitbucketRepos lists a configured workspace's repos, or everything the
// token can see when it is a user token and no workspace is set.
func fetchBitbucketRepos(ctx context.Context, client *http.Client, token, workspace string) ([]GitHubRepo, error) {
	var out []GitHubRepo
	next := fmt.Sprintf("%s/repositories?role=member&sort=-updated_on&pagelen=%d", bitbucketAPIBase, bitbucketRepoPageLen)
	if workspace != "" {
		next = fmt.Sprintf("%s/repositories/%s?sort=-updated_on&pagelen=%d", bitbucketAPIBase, workspace, bitbucketRepoPageLen)
	}
	for next != "" && len(out) < bitbucketRepoMaxRepos {
		resp, err := bitbucketRequestURL(ctx, client, token, next, "application/json")
		if err != nil {
			return nil, err
		}
		var page bitbucketPage[bitbucketRepo]
		err = func() error {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return bitbucketStatusError(resp)
			}
			return json.NewDecoder(resp.Body).Decode(&page)
		}()
		if err != nil {
			return nil, fmt.Errorf("list repos: %w", err)
		}
		for _, r := range page.Values {
			out = append(out, r.toGitHubRepo())
		}
		next = page.Next
	}
	if len(out) > bitbucketRepoMaxRepos {
		out = out[:bitbucketRepoMaxRepos]
	}
	return out, nil
}

func bitbucketRequest(ctx context.Context, client *http.Client, token, path string) (*http.Response, error) {
	return bitbucketRequestURL(ctx, client, token, bitbucketAPIBase+path, "application/json")
}

// bitbucketRequestURL takes an absolute URL (a page's next link). accept ""
// sends no Accept header, for the text/plain diff endpoint.
func bitbucketRequestURL(ctx context.Context, client *http.Client, token, url, accept string) (*http.Response, error) {
	cctx, cancel := context.WithTimeout(ctx, bitbucketCallTimeout)
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// Bitbucket's JSON error shape. Not every error is JSON: a 404 is the bare
// text "Not Found", so the decode is best-effort.
type bitbucketErrorBody struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// bitbucketStatusError never includes the token or a raw body.
func bitbucketStatusError(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("bitbucket: token invalid or expired — replace the access token")
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e bitbucketErrorBody
	msg := ""
	if json.Unmarshal(body, &e) == nil {
		msg = e.Error.Message
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if strings.HasPrefix(msg, "{") || strings.HasPrefix(msg, "<") {
			msg = ""
		}
	}
	if msg != "" {
		return fmt.Errorf("bitbucket: status %d: %s", resp.StatusCode, msg)
	}
	return fmt.Errorf("bitbucket: status %d", resp.StatusCode)
}

// GetRepo resolves workspace/slug; a 404 is ErrRepoNotFound so the
// destination's existence check can tell "absent" from "failed".
func (s *bitbucketSource) GetRepo(ctx context.Context, workspace, slug string) (GitHubRepo, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return GitHubRepo{}, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	resp, err := bitbucketRequest(ctx, s.client, token, fmt.Sprintf("/repositories/%s/%s", workspace, slug))
	if err != nil {
		return GitHubRepo{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return GitHubRepo{}, fmt.Errorf("get repo: %w", ErrRepoNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return GitHubRepo{}, fmt.Errorf("get repo: %w", bitbucketStatusError(resp))
	}
	var r bitbucketRepo
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return GitHubRepo{}, fmt.Errorf("get repo: decode response: %w", err)
	}
	return r.toGitHubRepo(), nil
}

// resolveRepoName splits name as workspace/slug, falling back to the
// connector's configured workspace for a bare slug.
func (s *bitbucketSource) resolveRepoName(name string) (workspace, slug string, err error) {
	if workspace, slug, err = splitBitbucketRepoArg(name); err == nil {
		return workspace, slug, nil
	}
	slug = strings.TrimSpace(name)
	if s.workspace == "" {
		return "", "", fmt.Errorf("create repo: bitbucket needs a workspace/slug name (or a workspace on connector %q), got %q", s.name, name)
	}
	if !bitbucketSlug.MatchString(slug) {
		return "", "", fmt.Errorf("create repo: bitbucket needs a workspace/slug name, got %q", name)
	}
	return s.workspace, slug, nil
}

// CreateRepo takes name as workspace/slug, or a bare slug when the
// connector has a workspace configured: Bitbucket repos live in a
// workspace and the token does not say which one.
func (s *bitbucketSource) CreateRepo(ctx context.Context, name string, private bool) (GitHubRepo, error) {
	workspace, slug, err := s.resolveRepoName(name)
	if err != nil {
		return GitHubRepo{}, err
	}
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return GitHubRepo{}, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	resp, err := bitbucketPost(ctx, s.client, token, fmt.Sprintf("/repositories/%s/%s", workspace, slug),
		map[string]any{"scm": "git", "is_private": private})
	if err != nil {
		return GitHubRepo{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return GitHubRepo{}, fmt.Errorf("create repo: %w", bitbucketStatusError(resp))
	}
	var r bitbucketRepo
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return GitHubRepo{}, fmt.Errorf("create repo: decode response: %w", err)
	}
	return r.toGitHubRepo(), nil
}

// bitbucketPRRef is the slice of a pull request CreatePR and PRMerged read.
type bitbucketPRRef struct {
	ID    int    `json:"id"`
	State string `json:"state"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

func (p bitbucketPRRef) toGitHubPR() GitHubPR {
	return GitHubPR{Number: p.ID, HTMLURL: p.Links.HTML.Href, State: strings.ToLower(p.State)}
}

// CreatePR opens a pull request from head to base, or returns the open
// one for head when Bitbucket refuses a duplicate.
func (s *bitbucketSource) CreatePR(ctx context.Context, workspace, slug, title, head, base, body string) (GitHubPR, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return GitHubPR{}, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	resp, err := bitbucketPost(ctx, s.client, token, fmt.Sprintf("/repositories/%s/%s/pullrequests", workspace, slug), map[string]any{
		"title":       title,
		"description": body,
		"source":      map[string]any{"branch": map[string]any{"name": head}},
		"destination": map[string]any{"branch": map[string]any{"name": base}},
	})
	if err != nil {
		return GitHubPR{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		var pr bitbucketPRRef
		if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
			return GitHubPR{}, fmt.Errorf("create pr: decode response: %w", err)
		}
		return pr.toGitHubPR(), nil
	}
	createErr := fmt.Errorf("create pr: %w", bitbucketStatusError(resp))
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusConflict {
		return GitHubPR{}, createErr
	}
	existing, findErr := findOpenBitbucketPR(ctx, s.client, token, workspace, slug, head)
	if findErr != nil {
		return GitHubPR{}, fmt.Errorf("%w (and could not fetch existing: %v)", createErr, findErr)
	}
	if existing == nil {
		return GitHubPR{}, createErr
	}
	return *existing, nil
}

func findOpenBitbucketPR(ctx context.Context, client *http.Client, token, workspace, slug, head string) (*GitHubPR, error) {
	q := url.Values{"q": {fmt.Sprintf(`source.branch.name="%s"`, head)}, "state": {"OPEN"}, "pagelen": {"1"}}
	resp, err := bitbucketRequest(ctx, client, token, fmt.Sprintf("/repositories/%s/%s/pullrequests?%s", workspace, slug, q.Encode()))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, bitbucketStatusError(resp)
	}
	var page bitbucketPage[bitbucketPRRef]
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(page.Values) == 0 {
		return nil, nil
	}
	pr := page.Values[0].toGitHubPR()
	return &pr, nil
}

func (s *bitbucketSource) PRMerged(ctx context.Context, workspace, slug string, number int) (bool, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return false, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	resp, err := bitbucketRequest(ctx, s.client, token, fmt.Sprintf("/repositories/%s/%s/pullrequests/%d", workspace, slug, number))
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("get pr: %w", bitbucketStatusError(resp))
	}
	var pr bitbucketPRRef
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return false, fmt.Errorf("get pr: decode response: %w", err)
	}
	return pr.State == "MERGED", nil
}

func bitbucketPost(ctx context.Context, client *http.Client, token, path string, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, bitbucketCallTimeout)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, bitbucketAPIBase+path, bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}
