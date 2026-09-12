package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// Pull request read tools on the github kind (issue #727). Every tool
// here is a pure GET against the REST API with the connector's PAT
// resolved at call time, and is marked ReadOnly so a mission turn can
// use it through missions' connector-reads resolver. Writes (comment,
// approve, merge) deliberately have no tool here: chat reaches them
// through the GitHub MCP connector, missions through destinations.
const (
	githubPRListDefault = 10
	githubPRListMax     = 50
	// githubDiffMaxBytes caps get_pull_request_diff's body before it
	// reaches the loop's own result cap, so a huge diff is cut with an
	// explicit marker instead of silently.
	githubDiffMaxBytes = 200 << 10
	// githubCommentsPerPage is the max GitHub allows; one page per
	// comment kind is all list_pull_request_comments reads.
	githubCommentsPerPage = 100

	githubDiffMediaType = "application/vnd.github.diff"
)

// githubRepoArg matches the "owner/name" form every PR tool takes.
var githubRepoArg = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$`)

// splitRepoArg validates an "owner/name" argument.
func splitRepoArg(repo string) (owner, name string, err error) {
	m := githubRepoArg.FindStringSubmatch(strings.TrimSpace(repo))
	if m == nil {
		return "", "", fmt.Errorf("repo must be \"owner/name\", got %q", repo)
	}
	return m[1], m[2], nil
}

// prTools is the github kind's read-only tool surface.
func (s *githubSource) prTools() []*tools.Tool {
	return []*tools.Tool{s.listPullRequests(), s.getPullRequest(), s.getPullRequestDiff(), s.listPullRequestComments()}
}

// githubPullRequest is the subset of a pull request object the read
// tools render.
type githubPullRequest struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	MergeableState string `json:"mergeable_state"`
	Body           string `json:"body"`
	HTMLURL        string `json:"html_url"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	Commits        int    `json:"commits"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
	ChangedFiles   int    `json:"changed_files"`
	User           struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// githubComment is one issue or review comment on a pull request;
// Path/Line are set only on review comments.
type githubComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (s *githubSource) listPullRequests() *tools.Tool {
	return &tools.Tool{
		Name:        "list_pull_requests",
		ReadOnly:    true,
		Description: "List pull requests in a GitHub repository, most recently updated first, with number, state, author, title, and head/base branches. Use this to find a PR by title or branch before reading it with get_pull_request. Do not use shell, git, or curl to reach the GitHub API; this tool carries the connected account's credentials.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"repo":{"type":"string","description":"owner/name"},
			"state":{"type":"string","enum":["open","closed","all"],"description":"defaults to open"},
			"max_results":{"type":"integer","minimum":1,"maximum":50}
		},"required":["repo"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Repo       string `json:"repo"`
				State      string `json:"state"`
				MaxResults int    `json:"max_results"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			owner, name, err := splitRepoArg(in.Repo)
			if err != nil {
				return "", err
			}
			if in.State == "" {
				in.State = "open"
			}
			if in.MaxResults <= 0 {
				in.MaxResults = githubPRListDefault
			}
			if in.MaxResults > githubPRListMax {
				in.MaxResults = githubPRListMax
			}
			var prs []githubPullRequest
			path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&sort=updated&direction=desc&per_page=%d", owner, name, in.State, in.MaxResults)
			if err := s.getJSON(ctx, path, "list pull requests", &prs); err != nil {
				return "", err
			}
			if len(prs) == 0 {
				return fmt.Sprintf("no %s pull requests in %s/%s", in.State, owner, name), nil
			}
			var b strings.Builder
			for _, pr := range prs {
				state := pr.State
				if pr.Draft {
					state = "draft"
				}
				fmt.Fprintf(&b, "#%d [%s] %s (%s) %s -> %s\n", pr.Number, state, pr.Title, pr.User.Login, pr.Head.Ref, pr.Base.Ref)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func (s *githubSource) getPullRequest() *tools.Tool {
	return &tools.Tool{
		Name:        "get_pull_request",
		ReadOnly:    true,
		Description: "Read one pull request's metadata: title, author, state, head/base branches and head commit, mergeable state, commit and changed-file counts, and the full description. Use get_pull_request_diff for the code changes and list_pull_request_comments for the discussion; this tool returns neither.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"repo":{"type":"string","description":"owner/name"},
			"number":{"type":"integer","minimum":1}
		},"required":["repo","number"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			owner, name, number, err := prArgs(args)
			if err != nil {
				return "", err
			}
			var pr githubPullRequest
			if err := s.getJSON(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, number), "get pull request", &pr); err != nil {
				return "", err
			}
			state := pr.State
			switch {
			case pr.Merged:
				state = "merged"
			case pr.Draft:
				state = "draft"
			}
			var b strings.Builder
			fmt.Fprintf(&b, "#%d %s\n", pr.Number, pr.Title)
			fmt.Fprintf(&b, "author: %s\nstate: %s\n", pr.User.Login, state)
			fmt.Fprintf(&b, "head: %s (%s)\nbase: %s\n", pr.Head.Ref, pr.Head.SHA, pr.Base.Ref)
			if pr.MergeableState != "" {
				fmt.Fprintf(&b, "mergeable_state: %s\n", pr.MergeableState)
			}
			fmt.Fprintf(&b, "commits: %d, files changed: %d, +%d/-%d\n", pr.Commits, pr.ChangedFiles, pr.Additions, pr.Deletions)
			fmt.Fprintf(&b, "created: %s, updated: %s\nurl: %s\n", pr.CreatedAt, pr.UpdatedAt, pr.HTMLURL)
			if strings.TrimSpace(pr.Body) != "" {
				fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(pr.Body))
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func (s *githubSource) getPullRequestDiff() *tools.Tool {
	return &tools.Tool{
		Name:        "get_pull_request_diff",
		ReadOnly:    true,
		Description: "Read a pull request's unified diff, the same text git diff base...head would print. Use this to review the actual code changes. Large diffs are cut at a fixed size with a marker saying how much was omitted; review what is returned rather than retrying.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"repo":{"type":"string","description":"owner/name"},
			"number":{"type":"integer","minimum":1}
		},"required":["repo","number"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			owner, name, number, err := prArgs(args)
			if err != nil {
				return "", err
			}
			token, err := s.resolve(ctx, s.credentialRef)
			if err != nil {
				return "", fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
			}
			resp, err := githubRequestAccept(ctx, s.client, token, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, number), githubDiffMediaType)
			if err != nil {
				return "", err
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("get pull request diff: %w", githubStatusError(resp))
			}
			// Read one byte past the cap so a diff exactly at the cap is
			// not reported as truncated.
			body, err := io.ReadAll(io.LimitReader(resp.Body, githubDiffMaxBytes+1))
			if err != nil {
				return "", fmt.Errorf("get pull request diff: read response: %w", err)
			}
			if len(body) <= githubDiffMaxBytes {
				if len(body) == 0 {
					return "empty diff", nil
				}
				return string(body), nil
			}
			omitted, _ := io.Copy(io.Discard, resp.Body)
			omitted++ // the one byte read past the cap
			return fmt.Sprintf("%s\n\n[diff truncated at %d bytes; %d more bytes omitted]", body[:githubDiffMaxBytes], githubDiffMaxBytes, omitted), nil
		},
	}
}

func (s *githubSource) listPullRequestComments() *tools.Tool {
	return &tools.Tool{
		Name:        "list_pull_request_comments",
		ReadOnly:    true,
		Description: "List the discussion on a pull request in chronological order: conversation comments and inline review comments (marked with their file and line). Use this to see reviewer feedback and whether it was addressed. This tool is read-only; it cannot post a comment.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"repo":{"type":"string","description":"owner/name"},
			"number":{"type":"integer","minimum":1}
		},"required":["repo","number"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			owner, name, number, err := prArgs(args)
			if err != nil {
				return "", err
			}
			var issue, review []githubComment
			if err := s.getJSON(ctx, fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=%d", owner, name, number, githubCommentsPerPage), "list pull request comments", &issue); err != nil {
				return "", err
			}
			if err := s.getJSON(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=%d", owner, name, number, githubCommentsPerPage), "list pull request comments", &review); err != nil {
				return "", err
			}
			type entry struct {
				at, text string
			}
			entries := make([]entry, 0, len(issue)+len(review))
			for _, c := range issue {
				entries = append(entries, entry{c.CreatedAt, fmt.Sprintf("[%s] %s (comment):\n%s", c.CreatedAt, c.User.Login, strings.TrimSpace(c.Body))})
			}
			for _, c := range review {
				where := c.Path
				if c.Line > 0 {
					where = fmt.Sprintf("%s:%d", c.Path, c.Line)
				}
				entries = append(entries, entry{c.CreatedAt, fmt.Sprintf("[%s] %s (review on %s):\n%s", c.CreatedAt, c.User.Login, where, strings.TrimSpace(c.Body))})
			}
			if len(entries) == 0 {
				return fmt.Sprintf("no comments on %s/%s#%d", owner, name, number), nil
			}
			sort.SliceStable(entries, func(i, j int) bool { return entries[i].at < entries[j].at })
			parts := make([]string, len(entries))
			for i, e := range entries {
				parts[i] = e.text
			}
			return strings.Join(parts, "\n\n"), nil
		},
	}
}

// prArgs decodes the shared {repo, number} argument shape.
func prArgs(args json.RawMessage) (owner, name string, number int, err error) {
	var in struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", "", 0, err
	}
	owner, name, err = splitRepoArg(in.Repo)
	if err != nil {
		return "", "", 0, err
	}
	if in.Number < 1 {
		return "", "", 0, fmt.Errorf("number must be a positive pull request number, got %d", in.Number)
	}
	return owner, name, in.Number, nil
}

// getJSON resolves the PAT, GETs path, and decodes a 200 body into
// out; op prefixes errors the way the other github helpers do.
func (s *githubSource) getJSON(ctx context.Context, path, op string, out any) error {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	resp, err := githubRequest(ctx, s.client, token, path)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %w", op, githubStatusError(resp))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode response: %w", op, err)
	}
	return nil
}
