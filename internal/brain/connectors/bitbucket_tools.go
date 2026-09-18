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

// Read-only PR tools on the bitbucket kind, the twin of github_tools.go: same
// names, schemas and descriptions so the two kinds aggregate under one raw
// name. All pure GETs, all ReadOnly so missions can use them.
const (
	bitbucketPRListDefault = 10
	bitbucketPRListMax     = 50
	// diff cap, cut with a marker instead of silently
	bitbucketDiffMaxBytes = 200 << 10
	// comments page size; one extra page is followed
	bitbucketCommentsPageLen = 100
	// diffstat page size and page ceiling; counts past it are marked truncated
	bitbucketDiffstatPageLen  = 100
	bitbucketDiffstatMaxPages = 10
)

// bitbucketRepoArg matches the "workspace/slug" form every PR tool takes.
var bitbucketRepoArg = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$`)

// same component class as bitbucketRepoArg, for a bare slug
var bitbucketSlug = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func splitBitbucketRepoArg(repo string) (workspace, slug string, err error) {
	m := bitbucketRepoArg.FindStringSubmatch(strings.TrimSpace(repo))
	if m == nil {
		return "", "", fmt.Errorf("repo must be \"workspace/slug\", got %q", repo)
	}
	return m[1], m[2], nil
}

func (s *bitbucketSource) prTools() []*tools.Tool {
	return []*tools.Tool{s.listPullRequests(), s.getPullRequest(), s.getPullRequestDiff(), s.listPullRequestComments()}
}

// States are upper-case (OPEN, MERGED, DECLINED, SUPERSEDED). No line counts
// on the object; get_pull_request reads those from diffstat.
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

type bitbucketDiffstat struct {
	LinesAdded   int `json:"lines_added"`
	LinesRemoved int `json:"lines_removed"`
}

// Inline is null for conversation comments. Deleted ones stay listed with a flag.
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

func bitbucketAuthor(nickname, displayName string) string {
	if nickname != "" {
		return nickname
	}
	return displayName
}

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
			// Bitbucket filters by repeating the state param.
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
			// counts come from diffstat; its failure degrades to a note
			var files, added, removed int
			var truncated bool
			var statErr error
			next := fmt.Sprintf("%s%s/diffstat?pagelen=%d", bitbucketAPIBase, base, bitbucketDiffstatPageLen)
			for pages := 0; next != ""; pages++ {
				if pages >= bitbucketDiffstatMaxPages {
					truncated = true
					break
				}
				var stat bitbucketPage[bitbucketDiffstat]
				if statErr = s.getJSONURL(ctx, next, "get pull request diffstat", &stat); statErr != nil {
					break
				}
				files += len(stat.Values)
				for _, f := range stat.Values {
					added += f.LinesAdded
					removed += f.LinesRemoved
				}
				next = stat.Next
			}
			switch {
			case statErr != nil:
				fmt.Fprintf(&b, "(diffstat unavailable: %v)\n", statErr)
			case truncated:
				fmt.Fprintf(&b, "files changed: %d, +%d/-%d [diffstat truncated at %d pages; more files omitted]\n",
					files, added, removed, bitbucketDiffstatMaxPages)
			default:
				fmt.Fprintf(&b, "files changed: %d, +%d/-%d\n", files, added, removed)
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
			// /diff answers 302 to a same-host text/plain diff; the client follows it.
			url := fmt.Sprintf("%s/repositories/%s/%s/pullrequests/%d/diff", bitbucketAPIBase, workspace, slug, number)
			resp, err := bitbucketRequestURL(ctx, s.client, token, url, "")
			if err != nil {
				return "", err
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("get pull request diff: %w", bitbucketStatusError(resp))
			}
			// one byte past the cap, so a diff exactly at the cap is not marked truncated
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
			// one endpoint serves both comment kinds; read at most two pages
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

func (s *bitbucketSource) getJSON(ctx context.Context, path, op string, out any) error {
	return s.getJSONURL(ctx, bitbucketAPIBase+path, op, out)
}

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
