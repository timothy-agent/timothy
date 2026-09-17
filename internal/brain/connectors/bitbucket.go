package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// Bitbucket Cloud counterpart of the github kind (issue #656). Auth is a
// workspace or repository access token sent as a bearer.

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
		return &bitbucketSource{name: c.Name, credentialRef: c.CredentialRef, resolve: resolve, client: client}, nil
	}
}

type bitbucketSource struct {
	name          string
	credentialRef string
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
	_, err = fetchBitbucketIdentity(ctx, s.client, token)
	return err
}

// Identity is what the connector test endpoint reports, in the shared GitHubIdentity shape.
func (s *bitbucketSource) Identity(ctx context.Context) (GitHubIdentity, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return GitHubIdentity{}, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return fetchBitbucketIdentity(ctx, s.client, token)
}

func (s *bitbucketSource) Close() error { return nil }

// ListRepos returns every repo the token can see, most recently updated first.
func (s *bitbucketSource) ListRepos(ctx context.Context) ([]GitHubRepo, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return nil, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return fetchBitbucketRepos(ctx, s.client, token)
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
// no scopes header, so Scopes is a fixed label.
func fetchBitbucketIdentity(ctx context.Context, client *http.Client, token string) (GitHubIdentity, error) {
	user, err := bitbucketGetUser(ctx, client, token)
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

func bitbucketGetUser(ctx context.Context, client *http.Client, token string) (bitbucketUser, error) {
	resp, err := bitbucketRequest(ctx, client, token, "/user")
	if err != nil {
		return bitbucketUser{}, err
	}
	defer func() { _ = resp.Body.Close() }()
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

func fetchBitbucketRepos(ctx context.Context, client *http.Client, token string) ([]GitHubRepo, error) {
	var out []GitHubRepo
	next := fmt.Sprintf("%s/repositories?role=member&sort=-updated_on&pagelen=%d", bitbucketAPIBase, bitbucketRepoPageLen)
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
