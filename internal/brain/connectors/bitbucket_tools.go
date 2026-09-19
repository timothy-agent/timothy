package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
)

// bitbucketSource's rendering half of gitprovider.Client, the twin of
// github_tools.go: same four calls behind the shared tool builder, with
// Bitbucket's own API shapes. All pure GETs.
const (
	// diff cap, cut with a marker instead of silently
	bitbucketDiffMaxBytes = 200 << 10
	// comments page size; one extra page is followed
	bitbucketCommentsPageLen = 100
	// diffstat page size and page ceiling; counts past it are marked truncated
	bitbucketDiffstatPageLen  = 100
	bitbucketDiffstatMaxPages = 10
)

// same component class as gitRepoArg, for a bare slug
var bitbucketSlug = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func splitBitbucketRepoArg(repo string) (workspace, slug string, err error) {
	ref, err := parseRepoArg(repo)
	if err != nil {
		return "", "", fmt.Errorf("repo must be \"workspace/slug\", got %q", repo)
	}
	return ref.Owner, ref.Name, nil
}

// States are upper-case (OPEN, MERGED, DECLINED, SUPERSEDED). No line counts
// on the object; PRDescription reads those from diffstat.
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

func (s *bitbucketSource) ListPRs(ctx context.Context, ref gitprovider.RepoRef, state string, max int) (string, error) {
	// Bitbucket filters by repeating the state param.
	var states string
	switch state {
	case "open":
		states = "&state=OPEN"
	case "closed":
		states = "&state=MERGED&state=DECLINED&state=SUPERSEDED"
	}
	var page bitbucketPage[bitbucketPullRequest]
	path := fmt.Sprintf("/repositories/%s/%s/pullrequests?sort=-updated_on&pagelen=%d%s", ref.Owner, ref.Name, max, states)
	if err := s.getJSON(ctx, path, "list pull requests", &page); err != nil {
		return "", err
	}
	if len(page.Values) == 0 {
		return fmt.Sprintf("no %s pull requests in %s", state, ref.FullName()), nil
	}
	var b strings.Builder
	for _, pr := range page.Values {
		fmt.Fprintf(&b, "#%d [%s] %s (%s) %s -> %s\n", pr.ID, bitbucketPRState(pr), pr.Title,
			bitbucketAuthor(pr.Author.Nickname, pr.Author.DisplayName), pr.Source.Branch.Name, pr.Destination.Branch.Name)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (s *bitbucketSource) PRDescription(ctx context.Context, ref gitprovider.RepoRef, number int) (string, error) {
	var pr bitbucketPullRequest
	base := fmt.Sprintf("/repositories/%s/%s/pullrequests/%d", ref.Owner, ref.Name, number)
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
}

func (s *bitbucketSource) PRDiff(ctx context.Context, ref gitprovider.RepoRef, number int) (string, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return "", fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	// /diff answers 302 to a same-host text/plain diff; the client follows it.
	url := fmt.Sprintf("%s/repositories/%s/%s/pullrequests/%d/diff", bitbucketAPIBase, ref.Owner, ref.Name, number)
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
}

func (s *bitbucketSource) PRComments(ctx context.Context, ref gitprovider.RepoRef, number int) (string, error) {
	// one endpoint serves both comment kinds; read at most two pages
	var all []bitbucketComment
	next := fmt.Sprintf("%s/repositories/%s/%s/pullrequests/%d/comments?pagelen=%d", bitbucketAPIBase, ref.Owner, ref.Name, number, bitbucketCommentsPageLen)
	for pages := 0; next != "" && pages < 2; pages++ {
		var page bitbucketPage[bitbucketComment]
		if err := s.getJSONURL(ctx, next, "list pull request comments", &page); err != nil {
			return "", err
		}
		all = append(all, page.Values...)
		next = page.Next
	}
	entries := make([]commentEntry, 0, len(all))
	for _, c := range all {
		if c.Deleted {
			continue
		}
		who := bitbucketAuthor(c.User.Nickname, c.User.DisplayName)
		body := strings.TrimSpace(c.Content.Raw)
		if c.Inline == nil {
			entries = append(entries, commentEntry{c.CreatedOn, fmt.Sprintf("[%s] %s (comment):\n%s", c.CreatedOn, who, body)})
			continue
		}
		where := c.Inline.Path
		if c.Inline.To != nil && *c.Inline.To > 0 {
			where = fmt.Sprintf("%s:%d", c.Inline.Path, *c.Inline.To)
		}
		entries = append(entries, commentEntry{c.CreatedOn, fmt.Sprintf("[%s] %s (review on %s):\n%s", c.CreatedOn, who, where, body)})
	}
	if len(entries) == 0 {
		return fmt.Sprintf("no comments on %s#%d", ref.FullName(), number), nil
	}
	return renderComments(entries), nil
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
