package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
)

// githubSource's rendering half of gitprovider.Client: the read-only
// pull request surface (issue #727), reached through the shared tool
// builder in gittools.go. Every call here is a pure GET against the
// REST API with the connector's PAT resolved at call time.
const (
	// githubDiffMaxBytes caps PRDiff's body before it reaches the loop's
	// own result cap, so a huge diff is cut with an explicit marker
	// instead of silently.
	githubDiffMaxBytes = 200 << 10
	// githubCommentsPerPage is the max GitHub allows; one page per
	// comment kind is all PRComments reads.
	githubCommentsPerPage = 100

	githubDiffMediaType = "application/vnd.github.diff"
)

// splitRepoArg validates an "owner/name" argument.
func splitRepoArg(repo string) (owner, name string, err error) {
	ref, err := parseRepoArg(repo)
	if err != nil {
		return "", "", err
	}
	return ref.Owner, ref.Name, nil
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

func (s *githubSource) ListPRs(ctx context.Context, ref gitprovider.RepoRef, state string, max int) (string, error) {
	var prs []githubPullRequest
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&sort=updated&direction=desc&per_page=%d", ref.Owner, ref.Name, state, max)
	if err := s.getJSON(ctx, path, "list pull requests", &prs); err != nil {
		return "", err
	}
	if len(prs) == 0 {
		return fmt.Sprintf("no %s pull requests in %s", state, ref.FullName()), nil
	}
	var b strings.Builder
	for _, pr := range prs {
		st := pr.State
		if pr.Draft {
			st = "draft"
		}
		fmt.Fprintf(&b, "#%d [%s] %s (%s) %s -> %s\n", pr.Number, st, pr.Title, pr.User.Login, pr.Head.Ref, pr.Base.Ref)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (s *githubSource) PRDescription(ctx context.Context, ref gitprovider.RepoRef, number int) (string, error) {
	var pr githubPullRequest
	if err := s.getJSON(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d", ref.Owner, ref.Name, number), "get pull request", &pr); err != nil {
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
}

func (s *githubSource) PRDiff(ctx context.Context, ref gitprovider.RepoRef, number int) (string, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return "", fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	resp, err := githubRequestAccept(ctx, s.client, token, fmt.Sprintf("/repos/%s/%s/pulls/%d", ref.Owner, ref.Name, number), githubDiffMediaType)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get pull request diff: %w", githubStatusError(resp))
	}
	// Read one byte past the cap so a diff exactly at the cap is not
	// reported as truncated.
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
}

func (s *githubSource) PRComments(ctx context.Context, ref gitprovider.RepoRef, number int) (string, error) {
	var issue, review []githubComment
	if err := s.getJSON(ctx, fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=%d", ref.Owner, ref.Name, number, githubCommentsPerPage), "list pull request comments", &issue); err != nil {
		return "", err
	}
	if err := s.getJSON(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=%d", ref.Owner, ref.Name, number, githubCommentsPerPage), "list pull request comments", &review); err != nil {
		return "", err
	}
	entries := make([]commentEntry, 0, len(issue)+len(review))
	for _, c := range issue {
		entries = append(entries, commentEntry{c.CreatedAt, fmt.Sprintf("[%s] %s (comment):\n%s", c.CreatedAt, c.User.Login, strings.TrimSpace(c.Body))})
	}
	for _, c := range review {
		where := c.Path
		if c.Line > 0 {
			where = fmt.Sprintf("%s:%d", c.Path, c.Line)
		}
		entries = append(entries, commentEntry{c.CreatedAt, fmt.Sprintf("[%s] %s (review on %s):\n%s", c.CreatedAt, c.User.Login, where, strings.TrimSpace(c.Body))})
	}
	if len(entries) == 0 {
		return fmt.Sprintf("no comments on %s#%d", ref.FullName(), number), nil
	}
	return renderComments(entries), nil
}

// commentEntry is one rendered comment plus the timestamp it sorts by.
type commentEntry struct {
	at, text string
}

// renderComments sorts entries oldest first and joins them.
func renderComments(entries []commentEntry) string {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].at < entries[j].at })
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = e.text
	}
	return strings.Join(parts, "\n\n")
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
