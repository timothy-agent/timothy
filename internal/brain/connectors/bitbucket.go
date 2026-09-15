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

// The bitbucket kind is the Bitbucket Cloud counterpart of the github
// kind (issue #656): a token-holding connector whose identity backs
// mission clone/push and whose read-only pull request tools are
// Timothy's own code, so missions may use them (the Atlassian MCP
// connector serves Bitbucket in chat, but missions strip every MCP
// source). Auth is a Bitbucket Cloud workspace or repository access
// token sent as a bearer; the connector's credential_ref is the whole
// configuration, so there is no BitbucketConfig.

// bitbucketAPIBase is a var, not a const, so tests can point it at a
// fake server; production never reassigns it.
var bitbucketAPIBase = "https://api.bitbucket.org/2.0"

const (
	bitbucketCallTimeout = 10 * time.Second

	// bitbucketRepoPageLen is the page size ListRepos asks for;
	// bitbucketRepoMaxRepos bounds how many `next` links it follows so a
	// token that sees an enormous workspace can't make mission creation's
	// repo list hang.
	bitbucketRepoPageLen  = 100
	bitbucketRepoMaxRepos = 300
)

// BitbucketBuilder returns the Builder for kind='bitbucket'. Read-only
// PR tools live in bitbucket_tools.go; identity and repo listing here.
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

// bitbucketSource is a built bitbucket-kind connector: read-only PR
// tools, and a Test that resolves the access token and confirms it
// authenticates against the Bitbucket Cloud API.
type bitbucketSource struct {
	name          string
	credentialRef string
	resolve       Resolve
	client        *http.Client
}

// Tools is the read-only pull request surface (bitbucket_tools.go).
func (s *bitbucketSource) Tools() []*tools.Tool { return s.prTools() }

// AccountInfo reports the kind and, since the token's account is only
// known after a network call, no email: the aggregated description
// then lists the connector by name alone.
func (s *bitbucketSource) AccountInfo() (kind, email string) { return "bitbucket", "" }

func (s *bitbucketSource) Test(ctx context.Context) error {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	_, err = fetchBitbucketIdentity(ctx, s.client, token)
	return err
}

// Identity resolves and returns the connector's Bitbucket identity in
// the shared GitHubIdentity shape — the richer counterpart to Test,
// called by the test endpoint to report login/name/email alongside the
// pass/fail.
func (s *bitbucketSource) Identity(ctx context.Context) (GitHubIdentity, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return GitHubIdentity{}, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return fetchBitbucketIdentity(ctx, s.client, token)
}

func (s *bitbucketSource) Close() error { return nil }

// ListRepos resolves the connector's token and returns every repo it
// can see, most recently updated first, following pagination up to
// bitbucketRepoMaxRepos.
func (s *bitbucketSource) ListRepos(ctx context.Context) ([]GitHubRepo, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return nil, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return fetchBitbucketRepos(ctx, s.client, token)
}

// bitbucketUser is the subset of GET /user this package reads.
// username is absent for some principals (an access token, a user who
// hasn't set one), in which case nickname is the account's handle.
type bitbucketUser struct {
	Username    string `json:"username"`
	Nickname    string `json:"nickname"`
	DisplayName string `json:"display_name"`
}

// bitbucketEmail is one entry of GET /user/emails' values.
type bitbucketEmail struct {
	Email       string `json:"email"`
	IsPrimary   bool   `json:"is_primary"`
	IsConfirmed bool   `json:"is_confirmed"`
}

// bitbucketPage is the envelope every Bitbucket list endpoint returns:
// values plus an absolute `next` URL, absent on the last page.
type bitbucketPage[T any] struct {
	Values []T    `json:"values"`
	Next   string `json:"next"`
}

// fetchBitbucketIdentity resolves the account a token authenticates as
// and its primary email. Never logs the token. Scopes is a fixed
// label: Bitbucket has no scopes header to echo, and an access token's
// permissions are visible only in the Bitbucket UI.
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

// bitbucketGetUser calls GET /user and returns the decoded body.
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

// resolveBitbucketEmail finds the primary confirmed address from
// /user/emails, tolerating a workspace or repository access token with
// no account email (403/404) by returning "": unlike GitHub there is
// no stable noreply address to fall back to, and the mission clone
// identity already handles an empty email.
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

// bitbucketRepo is the subset of a repository object the repo picker
// and the mission clone need, mapped onto GitHubRepo by toGitHubRepo.
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

// toGitHubRepo maps a Bitbucket repository onto the shared repo shape:
// mainbranch is null on an empty repository, and the https clone link
// is one entry of links.clone alongside ssh.
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

// fetchBitbucketRepos pages through GET /repositories?role=member by
// following each page's `next` link, capped at bitbucketRepoMaxRepos
// so a token with an enormous number of repos can't make this hang.
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

// bitbucketRequest issues one authenticated Bitbucket API GET for JSON
// at a path under bitbucketAPIBase. The token never appears in an
// error: only the status code and a short reason are surfaced.
func bitbucketRequest(ctx context.Context, client *http.Client, token, path string) (*http.Response, error) {
	return bitbucketRequestURL(ctx, client, token, bitbucketAPIBase+path, "application/json")
}

// bitbucketRequestURL is bitbucketRequest for an absolute URL — a
// page's `next` link — with an explicit Accept media type; "" sends
// none, for the endpoints that render a non-JSON body
// (get_pull_request_diff's text/plain). Redirects follow the client's
// default: the diff endpoint answers 302 to a same-host resource, and
// the bearer header survives a same-host redirect.
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

// bitbucketErrorBody is the shape of Bitbucket's JSON error replies:
// {"type":"error","error":{"message":"..."}}. Not every error is JSON
// — a 404 is the bare text "Not Found" — so the decode is best-effort.
type bitbucketErrorBody struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// bitbucketStatusError reports a non-200 Bitbucket response without
// ever including the request (and thus the token) or the raw response
// body: 401 gets a fixed reconnect-oriented message, other statuses
// keep the status code plus Bitbucket's parsed message, if any, else a
// trimmed text snippet.
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
