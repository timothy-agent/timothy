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

// Pull request read tools on the bitbucket kind (issue #656), the
// Bitbucket Cloud twin of github_tools.go: the same four names with
// byte-identical schemas and descriptions, so the manager aggregates
// the two kinds under one raw name with an account parameter (see
// TestSharedToolSchemasMatchAcrossKinds). Every tool is a pure GET with
// the connector's token resolved at call time, and is marked ReadOnly
// so a mission turn can use it. Writes (comment, approve, merge) have no
// tool here: chat reaches them through the Atlassian MCP connector,
// missions through destinations.
const (
	bitbucketPRListDefault = 10
	bitbucketPRListMax     = 50
	// bitbucketDiffMaxBytes caps get_pull_request_diff's body before it
	// reaches the loop's own result cap, so a huge diff is cut with an
	// explicit marker instead of silently.
	bitbucketDiffMaxBytes = 200 << 10
	// bitbucketCommentsPageLen is the page size for comments; one extra
	// `next` page is followed so a long review thread is not cut at one
	// page while an endless one cannot hang the call.
	bitbucketCommentsPageLen = 100
)

// bitbucketRepoArg matches the "workspace/slug" form every PR tool
// takes: the same character set as githubRepoArg, kept separate so the
// error names Bitbucket's terms.
var bitbucketRepoArg = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$`)

// splitBitbucketRepoArg validates a "workspace/slug" argument.
func splitBitbucketRepoArg(repo string) (workspace, slug string, err error) {
	m := bitbucketRepoArg.FindStringSubmatch(strings.TrimSpace(repo))
	if m == nil {
		return "", "", fmt.Errorf("repo must be \"workspace/slug\", got %q", repo)
	}
	return m[1], m[2], nil
}

// prTools is the bitbucket kind's read-only tool surface.
func (s *bitbucketSource) prTools() []*tools.Tool {
	return []*tools.Tool{s.listPullRequests(), s.getPullRequest(), s.getPullRequestDiff(), s.listPullRequestComments()}
}

// bitbucketPullRequest is the subset of a pull request object the read
// tools render. Bitbucket states are upper-case (OPEN, MERGED,
// DECLINED, SUPERSEDED) and the object carries no commit or line
// counts; get_pull_request reads those from the diffstat endpoint.
type bitbucketPullRequest struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Draft       bool   `json:"draft"`
	Description string `json:"description"`
	CreatedOn   string `json:"created_on"`
	UpdatedOn   string `json:"updated_on"`
	Author      struct {
		DisplayName string `json:"display_name"`
		Nickname    string `json:"nickname"`
	} `json:"author"`
	Source struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
	} `json:"source"`
	Destination struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"destination"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

// bitbucketDiffstat is one changed file from the diffstat endpoint.
type bitbucketDiffstat struct {
	LinesAdded   int `json:"lines_added"`
	LinesRemoved int `json:"lines_removed"`
}

// bitbucketComment is one comment on a pull request; Inline is null for
// conversation comments and carries the file and new-side line for
// review comments. Deleted comments stay in the listing with a flag.
type bitbucketComment struct {
	ID        int64  `json:"id"`
	CreatedOn string `json:"created_on"`
	Deleted   bool   `json:"deleted"`
	Content   struct {
		Raw string `json:"raw"`
	} `json:"content"`
	User struct {
		DisplayName string `json:"display_name"`
		Nickname    string `json:"nickname"`
	} `json:"user"`
	Inline *struct {
		Path string `json:"path"`
		To   *int   `json:"to"`
	} `json:"inline"`
}

// bitbucketAuthor renders an author the way github's tools render a
// login: the account handle, falling back to the display name.
func bitbucketAuthor(nickname, displayName string) string {
	if nickname != "" {
		return nickname
	}
	return displayName
}

// bitbucketPRState lower-cases Bitbucket's state for the model, with
// draft taking precedence the way github's renderer treats it.
func bitbucketPRState(pr bitbucketPullRequest) string {
	if pr.Draft && pr.State == "OPEN" {
		return "draft"
	}
	return strings.ToLower(pr.State)
}

func (s *bitbucketSource) listPullRequests() *tools.Tool {
	return &tools.Tool{
		Name:        "list_pull_requests",
		ReadOnly:    true,
		Description: "List pull requests in a repository, most recently updated first, with number, state, author, title, and head/base branches. Use this to find a PR by title or branch before reading it with get_pull_request. Do not use shell, git, or curl to reach the repository host's API; this tool carries the connected account's credentials.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"repo":{"type":"string","description":"owner/name on GitHub, workspace/slug on Bitbucket"},
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
			workspace, slug, err := splitBitbucketRepoArg(in.Repo)
			if err != nil {
				return "", err
			}
			if in.State == "" {
				in.State = "open"
			}
			if in.MaxResults <= 0 {
				in.MaxResults = bitbucketPRListDefault
			}
			if in.MaxResults > bitbucketPRListMax {
				in.MaxResults = bitbucketPRListMax
			}
			// Bitbucket filters by repeating the state parameter; "closed"
			// is everything that is no longer open.
			var states string
			switch in.State {
			case "open":
				states = "&state=OPEN"
			case "closed":
				states = "&state=MERGED&state=DECLINED&state=SUPERSEDED"
			}
			var page bitbucketPage[bitbucketPullRequest]
			path := fmt.Sprintf("/repositories/%s/%s/pullrequests?sort=-updated_on&pagelen=%d%s", workspace, slug, in.MaxResults, states)
			if err := s.getJSON(ctx, path, "list pull requests", &page); err != nil {
				return "", err
			}
			if len(page.Values) == 0 {
				return fmt.Sprintf("no %s pull requests in %s/%s", in.State, workspace, slug), nil
			}
			var b strings.Builder
			for _, pr := range page.Values {
				fmt.Fprintf(&b, "#%d [%s] %s (%s) %s -> %s\n", pr.ID, bitbucketPRState(pr), pr.Title,
					bitbucketAuthor(pr.Author.Nickname, pr.Author.DisplayName), pr.Source.Branch.Name, pr.Destination.Branch.Name)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func (s *bitbucketSource) getPullRequest() *tools.Tool {
	return &tools.Tool{
		Name:        "get_pull_request",
		ReadOnly:    true,
		Description: "Read one pull request's metadata: title, author, state, head/base branches and head commit, changed-file and line counts, and the full description. Use get_pull_request_diff for the code changes and list_pull_request_comments for the discussion; this tool returns neither.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"repo":{"type":"string","description":"owner/name on GitHub, workspace/slug on Bitbucket"},
			"number":{"type":"integer","minimum":1}
		},"required":["repo","number"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			workspace, slug, number, err := bitbucketPRArgs(args)
			if err != nil {
				return "", err
			}
			var pr bitbucketPullRequest
			base := fmt.Sprintf("/repositories/%s/%s/pullrequests/%d", workspace, slug, number)
			if err := s.getJSON(ctx, base, "get pull request", &pr); err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "#%d %s\n", pr.ID, pr.Title)
			fmt.Fprintf(&b, "author: %s\nstate: %s\n", bitbucketAuthor(pr.Author.Nickname, pr.Author.DisplayName), bitbucketPRState(pr))
			fmt.Fprintf(&b, "head: %s (%s)\nbase: %s\n", pr.Source.Branch.Name, pr.Source.Commit.Hash, pr.Destination.Branch.Name)
			// The PR object carries no counts; the diffstat endpoint does.
			// Its failure degrades to a note rather than failing a read that
			// already succeeded.
			var stat bitbucketPage[bitbucketDiffstat]
			if err := s.getJSON(ctx, base+"/diffstat", "get pull request diffstat", &stat); err != nil {
				fmt.Fprintf(&b, "(diffstat unavailable: %v)\n", err)
			} else {
				var added, removed int
				for _, f := range stat.Values {
					added += f.LinesAdded
					removed += f.LinesRemoved
				}
				fmt.Fprintf(&b, "files changed: %d, +%d/-%d\n", len(stat.Values), added, removed)
			}
			fmt.Fprintf(&b, "created: %s, updated: %s\nurl: %s\n", pr.CreatedOn, pr.UpdatedOn, pr.Links.HTML.Href)
			if strings.TrimSpace(pr.Description) != "" {
				fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(pr.Description))
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func (s *bitbucketSource) getPullRequestDiff() *tools.Tool {
	return &tools.Tool{
		Name:        "get_pull_request_diff",
		ReadOnly:    true,
		Description: "Read a pull request's unified diff, the same text git diff base...head would print. Use this to review the actual code changes. Large diffs are cut at a fixed size with a marker saying how much was omitted; review what is returned rather than retrying.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"repo":{"type":"string","description":"owner/name on GitHub, workspace/slug on Bitbucket"},
			"number":{"type":"integer","minimum":1}
		},"required":["repo","number"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			workspace, slug, number, err := bitbucketPRArgs(args)
			if err != nil {
				return "", err
			}
			token, err := s.resolve(ctx, s.credentialRef)
			if err != nil {
				return "", fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
			}
			// The diff endpoint answers 302 to the underlying commit-range
			// diff on the same host; the client follows it and the bearer
			// header survives a same-host redirect. The body is text/plain,
			// so no Accept is sent.
			url := fmt.Sprintf("%s/repositories/%s/%s/pullrequests/%d/diff", bitbucketAPIBase, workspace, slug, number)
			resp, err := bitbucketRequestURL(ctx, s.client, token, url, "")
			if err != nil {
				return "", err
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("get pull request diff: %w", bitbucketStatusError(resp))
			}
			// Read one byte past the cap so a diff exactly at the cap is
			// not reported as truncated.
			body, err := io.ReadAll(io.LimitReader(resp.Body, bitbucketDiffMaxBytes+1))
			if err != nil {
				return "", fmt.Errorf("get pull request diff: read response: %w", err)
			}
			if len(body) <= bitbucketDiffMaxBytes {
				if len(body) == 0 {
					return "empty diff", nil
				}
				return string(body), nil
			}
			omitted, _ := io.Copy(io.Discard, resp.Body)
			omitted++ // the one byte read past the cap
			return fmt.Sprintf("%s\n\n[diff truncated at %d bytes; %d more bytes omitted]", body[:bitbucketDiffMaxBytes], bitbucketDiffMaxBytes, omitted), nil
		},
	}
}

func (s *bitbucketSource) listPullRequestComments() *tools.Tool {
	return &tools.Tool{
		Name:        "list_pull_request_comments",
		ReadOnly:    true,
		Description: "List the discussion on a pull request in chronological order: conversation comments and inline review comments (marked with their file and line). Use this to see reviewer feedback and whether it was addressed. This tool is read-only; it cannot post a comment.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"repo":{"type":"string","description":"owner/name on GitHub, workspace/slug on Bitbucket"},
			"number":{"type":"integer","minimum":1}
		},"required":["repo","number"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			workspace, slug, number, err := bitbucketPRArgs(args)
			if err != nil {
				return "", err
			}
			// One endpoint serves both conversation and inline comments,
			// unlike GitHub's two; read the first page and at most one
			// more.
			var all []bitbucketComment
			next := fmt.Sprintf("%s/repositories/%s/%s/pullrequests/%d/comments?pagelen=%d", bitbucketAPIBase, workspace, slug, number, bitbucketCommentsPageLen)
			for pages := 0; next != "" && pages < 2; pages++ {
				var page bitbucketPage[bitbucketComment]
				if err := s.getJSONURL(ctx, next, "list pull request comments", &page); err != nil {
					return "", err
				}
				all = append(all, page.Values...)
				next = page.Next
			}
			type entry struct {
				at, text string
			}
			entries := make([]entry, 0, len(all))
			for _, c := range all {
				if c.Deleted {
					continue
				}
				who := bitbucketAuthor(c.User.Nickname, c.User.DisplayName)
				body := strings.TrimSpace(c.Content.Raw)
				if c.Inline == nil {
					entries = append(entries, entry{c.CreatedOn, fmt.Sprintf("[%s] %s (comment):\n%s", c.CreatedOn, who, body)})
					continue
				}
				where := c.Inline.Path
				if c.Inline.To != nil && *c.Inline.To > 0 {
					where = fmt.Sprintf("%s:%d", c.Inline.Path, *c.Inline.To)
				}
				entries = append(entries, entry{c.CreatedOn, fmt.Sprintf("[%s] %s (review on %s):\n%s", c.CreatedOn, who, where, body)})
			}
			if len(entries) == 0 {
				return fmt.Sprintf("no comments on %s/%s#%d", workspace, slug, number), nil
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

// bitbucketPRArgs decodes the shared {repo, number} argument shape.
func bitbucketPRArgs(args json.RawMessage) (workspace, slug string, number int, err error) {
	var in struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", "", 0, err
	}
	workspace, slug, err = splitBitbucketRepoArg(in.Repo)
	if err != nil {
		return "", "", 0, err
	}
	if in.Number < 1 {
		return "", "", 0, fmt.Errorf("number must be a positive pull request number, got %d", in.Number)
	}
	return workspace, slug, in.Number, nil
}

// getJSON resolves the token, GETs path under bitbucketAPIBase, and
// decodes a 200 body into out; op prefixes errors the way the other
// bitbucket helpers do.
func (s *bitbucketSource) getJSON(ctx context.Context, path, op string, out any) error {
	return s.getJSONURL(ctx, bitbucketAPIBase+path, op, out)
}

// getJSONURL is getJSON for an absolute URL — a page's `next` link.
func (s *bitbucketSource) getJSONURL(ctx context.Context, url, op string, out any) error {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	resp, err := bitbucketRequestURL(ctx, s.client, token, url, "application/json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %w", op, bitbucketStatusError(resp))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode response: %w", op, err)
	}
	return nil
}
