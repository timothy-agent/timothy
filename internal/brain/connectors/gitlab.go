package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// GitLab counterpart of the github and bitbucket kinds (issue #797).
// Auth is a personal or project access token sent as PRIVATE-TOKEN
// against GitLab API v4; merge requests are what the shared Client
// interface calls pull requests.

// GitLabConfig is the connectors.config shape for kind='gitlab'.
// BaseURL points at a self-managed instance; empty means gitlab.com.
// SSHKnownHosts are the operator's pasted host-key lines for such an
// instance, whose key nobody can pin at build time (D-102). Namespace
// is the group path new projects are created under, the counterpart of
// bitbucket's Workspace: a token does not say which group to use.
type GitLabConfig struct {
	GitKeyConfig
	BaseURL       string `json:"base_url,omitempty"`
	SSHKnownHosts string `json:"ssh_known_hosts,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
}

const (
	gitlabCallTimeout = 10 * time.Second

	// page size and cap for ListRepos, so a huge instance can't hang the picker
	gitlabRepoPageLen  = 100
	gitlabRepoMaxRepos = 300

	// diff cap, cut with a marker instead of silently
	gitlabDiffMaxBytes = 200 << 10
	// notes page size; one page is all PRComments reads
	gitlabNotesPageLen = 100
)

// GitLabBuilder returns the Builder for kind='gitlab'.
func GitLabBuilder(client *http.Client) Builder {
	if client == nil {
		client = &http.Client{}
	}
	return func(_ context.Context, c Connector, resolve Resolve) (Source, error) {
		if c.CredentialRef == "" {
			return nil, fmt.Errorf("gitlab %s: credential_ref is required (a GitLab access token)", c.Name)
		}
		var cfg GitLabConfig
		if len(c.Config) > 0 {
			if err := json.Unmarshal(c.Config, &cfg); err != nil {
				return nil, fmt.Errorf("gitlab %s: config: %w", c.Name, err)
			}
		}
		return &gitlabSource{
			GitLab:        gitprovider.NewGitLab(cfg.BaseURL, splitKnownHosts(cfg.SSHKnownHosts)),
			name:          c.Name,
			credentialRef: c.CredentialRef,
			namespace:     strings.TrimSpace(cfg.Namespace),
			resolve:       resolve,
			client:        client,
		}, nil
	}
}

// splitKnownHosts turns the operator's pasted block into one line per
// host key, dropping blanks and comments the way ssh itself does.
func splitKnownHosts(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// The embedded Descriptor supplies the static half of
// gitprovider.Client, exactly as githubSource and bitbucketSource do.
// Unlike theirs it carries the instance host, so a self-managed GitLab
// parses and clones against its own host.
type gitlabSource struct {
	gitprovider.GitLab

	name          string
	credentialRef string
	namespace     string
	resolve       Resolve
	client        *http.Client
}

var _ gitprovider.Client = (*gitlabSource)(nil)

// Tools is the read-only pull request surface, from the shared builder.
func (s *gitlabSource) Tools() []*tools.Tool { return gitPRTools(s) }

func (s *gitlabSource) AccountInfo() (kind, email string) { return "gitlab", "" }

func (s *gitlabSource) Test(ctx context.Context) error {
	_, err := s.Identity(ctx)
	return err
}

func (s *gitlabSource) Close() error { return nil }

// gitlabUser is the subset of GET /user this package reads. A project
// access token authenticates as a bot user, so this answers for both
// token kinds and there is no bitbucket-style not-a-user fallback.
type gitlabUser struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	ID       int64  `json:"id"`
}

// Identity reports who the token authenticates as. GitLab has no scopes
// header, so Scopes is a fixed label, same as bitbucket's.
func (s *gitlabSource) Identity(ctx context.Context) (GitHubIdentity, error) {
	var u gitlabUser
	if err := s.getJSON(ctx, "/user", "identify token", &u); err != nil {
		return GitHubIdentity{}, err
	}
	email := u.Email
	if email == "" {
		// GitLab hides a private commit email behind its own noreply
		// form, which is what a commit would be attributed to anyway.
		email = fmt.Sprintf("%d-%s@users.noreply.%s", u.ID, u.Username, s.Host())
	}
	return GitHubIdentity{Login: u.Username, Name: u.Name, Email: email, Scopes: "access token"}, nil
}

// gitlabProject is the subset of a project object the repo flows read.
// PathWithNamespace is the full nested group path.
type gitlabProject struct {
	PathWithNamespace string `json:"path_with_namespace"`
	Visibility        string `json:"visibility"`
	DefaultBranch     string `json:"default_branch"`
	WebURL            string `json:"web_url"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	LastActivityAt    string `json:"last_activity_at"`
}

func (p gitlabProject) toGitHubRepo() GitHubRepo {
	return GitHubRepo{
		FullName:      p.PathWithNamespace,
		Private:       p.Visibility != "public",
		DefaultBranch: p.DefaultBranch,
		HTMLURL:       p.WebURL,
		CloneURL:      p.HTTPURLToRepo,
		PushedAt:      p.LastActivityAt,
	}
}

// ListRepos returns every project the token can see, most recently
// active first.
func (s *gitlabSource) ListRepos(ctx context.Context) ([]GitHubRepo, error) {
	var out []GitHubRepo
	for page := 1; len(out) < gitlabRepoMaxRepos; page++ {
		var projects []gitlabProject
		path := fmt.Sprintf("/projects?membership=true&order_by=last_activity_at&sort=desc&per_page=%d&page=%d", gitlabRepoPageLen, page)
		if err := s.getJSON(ctx, path, "list repos", &projects); err != nil {
			return nil, err
		}
		for _, p := range projects {
			out = append(out, p.toGitHubRepo())
		}
		if len(projects) < gitlabRepoPageLen {
			break
		}
	}
	if len(out) > gitlabRepoMaxRepos {
		out = out[:gitlabRepoMaxRepos]
	}
	return out, nil
}

// projectPath is GitLab's URL-encoded project identifier. The whole
// nested group path is one path parameter, so every slash in it has to
// be escaped: group/subgroup/project becomes group%2Fsubgroup%2Fproject.
func projectPath(ref gitprovider.RepoRef) string {
	return url.PathEscape(ref.FullName())
}

// GetRepo resolves ref; a 404 is ErrRepoNotFound so the destination's
// existence check can tell "absent" from "failed".
func (s *gitlabSource) GetRepo(ctx context.Context, ref gitprovider.RepoRef) (GitHubRepo, error) {
	resp, err := s.request(ctx, http.MethodGet, "/projects/"+projectPath(ref), nil)
	if err != nil {
		return GitHubRepo{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return GitHubRepo{}, fmt.Errorf("get repo: %w", ErrRepoNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return GitHubRepo{}, fmt.Errorf("get repo: %w", gitlabStatusError(resp))
	}
	var p gitlabProject
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return GitHubRepo{}, fmt.Errorf("get repo: decode response: %w", err)
	}
	return p.toGitHubRepo(), nil
}

// CreateRepo takes name as a full group/project path (nested to any
// depth), or a bare slug when the connector has a namespace configured.
// GitLab creates under the token's own user namespace by default, which
// is rarely where a group project belongs, so the path is resolved
// explicitly and turned into the numeric namespace id the API wants.
func (s *gitlabSource) CreateRepo(ctx context.Context, name string, private bool) (GitHubRepo, error) {
	group, slug, err := s.resolveRepoName(name)
	if err != nil {
		return GitHubRepo{}, err
	}
	body := map[string]any{"path": slug, "name": slug, "initialize_with_readme": true}
	if private {
		body["visibility"] = "private"
	} else {
		body["visibility"] = "public"
	}
	if group != "" {
		id, err := s.namespaceID(ctx, group)
		if err != nil {
			return GitHubRepo{}, err
		}
		body["namespace_id"] = id
	}
	resp, err := s.request(ctx, http.MethodPost, "/projects", body)
	if err != nil {
		return GitHubRepo{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return GitHubRepo{}, fmt.Errorf("create repo: %w", gitlabStatusError(resp))
	}
	var p gitlabProject
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return GitHubRepo{}, fmt.Errorf("create repo: decode response: %w", err)
	}
	return p.toGitHubRepo(), nil
}

// resolveRepoName splits name into its group path and project slug.
// Everything before the last segment is the group, so a nested path
// survives; a bare slug falls back to the connector's namespace, and
// with neither the project would land in the token's user namespace
// silently, so that is an error instead.
func (s *gitlabSource) resolveRepoName(name string) (group, slug string, err error) {
	trimmed := strings.Trim(strings.TrimSpace(name), "/")
	if trimmed == "" {
		return "", "", fmt.Errorf("create repo: gitlab needs a group/project name, got %q", name)
	}
	if i := strings.LastIndex(trimmed, "/"); i > 0 {
		return trimmed[:i], trimmed[i+1:], nil
	}
	if s.namespace == "" {
		return "", "", fmt.Errorf("create repo: gitlab needs a group/project name (or a namespace on connector %q), got %q", s.name, name)
	}
	return s.namespace, trimmed, nil
}

// namespaceID resolves a group path to the numeric id POST /projects
// wants. The path is URL-encoded whole, nesting included.
func (s *gitlabSource) namespaceID(ctx context.Context, group string) (int64, error) {
	var ns struct {
		ID int64 `json:"id"`
	}
	if err := s.getJSON(ctx, "/namespaces/"+url.PathEscape(group), "resolve namespace", &ns); err != nil {
		return 0, err
	}
	if ns.ID == 0 {
		return 0, fmt.Errorf("resolve namespace %q: gitlab returned no id", group)
	}
	return ns.ID, nil
}

// gitlabMergeRequest is the subset of a merge request object the PR
// flows read. IID is the per-project number users and URLs see; the
// object's own ID is instance-global and never what a caller means.
type gitlabMergeRequest struct {
	IID          int    `json:"iid"`
	Title        string `json:"title"`
	State        string `json:"state"`
	Draft        bool   `json:"draft"`
	Description  string `json:"description"`
	WebURL       string `json:"web_url"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	SHA          string `json:"sha"`
	DetailedName string `json:"detailed_merge_status"`
	Author       struct {
		Username string `json:"username"`
	} `json:"author"`
	Changes struct {
		Count string `json:"changes_count"`
	} `json:"-"`
	ChangesCount string `json:"changes_count"`
}

// gitlabPRState maps GitLab's state vocabulary onto the shared one:
// "opened" is "open", and a draft is reported as such the way the other
// kinds report it.
func gitlabPRState(mr gitlabMergeRequest) string {
	switch {
	case mr.State == "merged":
		return "merged"
	case mr.Draft && mr.State == "opened":
		return "draft"
	case mr.State == "opened":
		return "open"
	default:
		return mr.State
	}
}

func (mr gitlabMergeRequest) toGitHubPR() GitHubPR {
	return GitHubPR{Number: mr.IID, HTMLURL: mr.WebURL, State: gitlabPRState(mr)}
}

// CreatePR opens a merge request, or returns the open one already
// serving spec.Head when GitLab refuses a duplicate.
func (s *gitlabSource) CreatePR(ctx context.Context, spec gitprovider.PRSpec) (GitHubPR, error) {
	path := "/projects/" + projectPath(spec.Repo) + "/merge_requests"
	resp, err := s.request(ctx, http.MethodPost, path, map[string]any{
		"title":         spec.Title,
		"description":   spec.Body,
		"source_branch": spec.Head,
		"target_branch": spec.Base,
	})
	if err != nil {
		return GitHubPR{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		var mr gitlabMergeRequest
		if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
			return GitHubPR{}, fmt.Errorf("create pr: decode response: %w", err)
		}
		return mr.toGitHubPR(), nil
	}
	createErr := fmt.Errorf("create pr: %w", gitlabStatusError(resp))
	if resp.StatusCode != http.StatusConflict && resp.StatusCode != http.StatusBadRequest {
		return GitHubPR{}, createErr
	}
	existing, findErr := s.findOpenMergeRequest(ctx, spec.Repo, spec.Head)
	if findErr != nil {
		return GitHubPR{}, fmt.Errorf("%w (and could not fetch existing: %v)", createErr, findErr)
	}
	if existing == nil {
		return GitHubPR{}, createErr
	}
	return *existing, nil
}

func (s *gitlabSource) findOpenMergeRequest(ctx context.Context, ref gitprovider.RepoRef, head string) (*GitHubPR, error) {
	q := url.Values{"state": {"opened"}, "source_branch": {head}, "per_page": {"1"}}
	var mrs []gitlabMergeRequest
	path := "/projects/" + projectPath(ref) + "/merge_requests?" + q.Encode()
	if err := s.getJSON(ctx, path, "list merge requests", &mrs); err != nil {
		return nil, err
	}
	if len(mrs) == 0 {
		return nil, nil
	}
	pr := mrs[0].toGitHubPR()
	return &pr, nil
}

// GetPR reads ref's merge request by its per-project iid.
func (s *gitlabSource) GetPR(ctx context.Context, ref gitprovider.RepoRef, number int) (GitHubPR, error) {
	var mr gitlabMergeRequest
	path := fmt.Sprintf("/projects/%s/merge_requests/%d", projectPath(ref), number)
	if err := s.getJSON(ctx, path, "get pr", &mr); err != nil {
		return GitHubPR{}, err
	}
	return mr.toGitHubPR(), nil
}

func (s *gitlabSource) ListPRs(ctx context.Context, ref gitprovider.RepoRef, state string, max int) (string, error) {
	q := url.Values{"order_by": {"updated_at"}, "sort": {"desc"}, "per_page": {fmt.Sprint(max)}}
	switch state {
	case "open":
		q.Set("state", "opened")
	case "closed":
		// GitLab splits what the shared vocabulary calls closed into
		// merged and closed, and its API takes one state at a time, so
		// "all" plus a local filter is the only faithful reading.
		q.Set("state", "all")
	}
	var mrs []gitlabMergeRequest
	path := "/projects/" + projectPath(ref) + "/merge_requests?" + q.Encode()
	if err := s.getJSON(ctx, path, "list pull requests", &mrs); err != nil {
		return "", err
	}
	if state == "closed" {
		kept := mrs[:0]
		for _, mr := range mrs {
			if mr.State == "merged" || mr.State == "closed" {
				kept = append(kept, mr)
			}
		}
		mrs = kept
	}
	if len(mrs) == 0 {
		return fmt.Sprintf("no %s pull requests in %s", state, ref.FullName()), nil
	}
	var b strings.Builder
	for _, mr := range mrs {
		fmt.Fprintf(&b, "#%d [%s] %s (%s) %s -> %s\n", mr.IID, gitlabPRState(mr), mr.Title,
			mr.Author.Username, mr.SourceBranch, mr.TargetBranch)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (s *gitlabSource) PRDescription(ctx context.Context, ref gitprovider.RepoRef, number int) (string, error) {
	var mr gitlabMergeRequest
	path := fmt.Sprintf("/projects/%s/merge_requests/%d", projectPath(ref), number)
	if err := s.getJSON(ctx, path, "get pull request", &mr); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s\n", mr.IID, mr.Title)
	fmt.Fprintf(&b, "author: %s\nstate: %s\n", mr.Author.Username, gitlabPRState(mr))
	fmt.Fprintf(&b, "head: %s (%s)\nbase: %s\n", mr.SourceBranch, mr.SHA, mr.TargetBranch)
	if mr.DetailedName != "" {
		fmt.Fprintf(&b, "mergeable_state: %s\n", mr.DetailedName)
	}
	// GitLab reports a changed-file count only as a string, and gives no
	// line counts on the object at all; reporting what it does say beats
	// a second paged call for numbers no other kind promises.
	if mr.ChangesCount != "" {
		fmt.Fprintf(&b, "files changed: %s\n", mr.ChangesCount)
	}
	fmt.Fprintf(&b, "created: %s, updated: %s\nurl: %s\n", mr.CreatedAt, mr.UpdatedAt, mr.WebURL)
	if strings.TrimSpace(mr.Description) != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(mr.Description))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// PRDiff renders the merge request's changes. GitLab serves no unified
// diff for a merge request, only per-file diff hunks, so the unified
// form is reassembled from them with the ---/+++ headers git would
// print.
func (s *gitlabSource) PRDiff(ctx context.Context, ref gitprovider.RepoRef, number int) (string, error) {
	var diffs []struct {
		OldPath     string `json:"old_path"`
		NewPath     string `json:"new_path"`
		Diff        string `json:"diff"`
		NewFile     bool   `json:"new_file"`
		DeletedFile bool   `json:"deleted_file"`
	}
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/diffs?per_page=%d", projectPath(ref), number, gitlabNotesPageLen)
	if err := s.getJSON(ctx, path, "get pull request diff", &diffs); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, d := range diffs {
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", d.OldPath, d.NewPath)
		switch {
		case d.NewFile:
			fmt.Fprintf(&b, "--- /dev/null\n+++ b/%s\n", d.NewPath)
		case d.DeletedFile:
			fmt.Fprintf(&b, "--- a/%s\n+++ /dev/null\n", d.OldPath)
		default:
			fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", d.OldPath, d.NewPath)
		}
		b.WriteString(d.Diff)
		if !strings.HasSuffix(d.Diff, "\n") {
			b.WriteString("\n")
		}
	}
	out := b.String()
	if out == "" {
		return "empty diff", nil
	}
	if len(out) > gitlabDiffMaxBytes {
		return fmt.Sprintf("%s\n\n[diff truncated at %d bytes; %d more bytes omitted]",
			out[:gitlabDiffMaxBytes], gitlabDiffMaxBytes, len(out)-gitlabDiffMaxBytes), nil
	}
	return out, nil
}

// gitlabNote is one discussion note on a merge request. Position is set
// only on an inline review note; System marks GitLab's own activity
// entries ("assigned to ...") which are not discussion.
type gitlabNote struct {
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	System    bool   `json:"system"`
	Author    struct {
		Username string `json:"username"`
	} `json:"author"`
	Position *struct {
		NewPath string `json:"new_path"`
		OldPath string `json:"old_path"`
		NewLine int    `json:"new_line"`
	} `json:"position"`
}

func (s *gitlabSource) PRComments(ctx context.Context, ref gitprovider.RepoRef, number int) (string, error) {
	var notes []gitlabNote
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/notes?sort=asc&order_by=created_at&per_page=%d",
		projectPath(ref), number, gitlabNotesPageLen)
	if err := s.getJSON(ctx, path, "list pull request comments", &notes); err != nil {
		return "", err
	}
	entries := make([]commentEntry, 0, len(notes))
	for _, n := range notes {
		if n.System {
			continue
		}
		body := strings.TrimSpace(n.Body)
		if n.Position == nil {
			entries = append(entries, commentEntry{n.CreatedAt, fmt.Sprintf("[%s] %s (comment):\n%s", n.CreatedAt, n.Author.Username, body)})
			continue
		}
		where := n.Position.NewPath
		if where == "" {
			where = n.Position.OldPath
		}
		if n.Position.NewLine > 0 {
			where = fmt.Sprintf("%s:%d", where, n.Position.NewLine)
		}
		entries = append(entries, commentEntry{n.CreatedAt, fmt.Sprintf("[%s] %s (review on %s):\n%s", n.CreatedAt, n.Author.Username, where, body)})
	}
	if len(entries) == 0 {
		return fmt.Sprintf("no comments on %s#%d", ref.FullName(), number), nil
	}
	return renderComments(entries), nil
}

// request issues one authenticated GitLab API v4 call against this
// connector's instance. body nil means GET-shaped with no payload.
func (s *gitlabSource) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	token, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return nil, fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(encoded)
	}
	cctx, cancel := context.WithTimeout(ctx, gitlabCallTimeout)
	req, err := http.NewRequestWithContext(cctx, method, s.APIBase()+path, payload)
	if err != nil {
		cancel()
		return nil, err
	}
	// GitLab authenticates a personal or project access token by this
	// header, not a bearer and not basic auth.
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

func (s *gitlabSource) getJSON(ctx context.Context, path, op string, out any) error {
	resp, err := s.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %w", op, gitlabStatusError(resp))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode response: %w", op, err)
	}
	return nil
}

// GitLab reports an error as either {"message": ...} or {"error": ...},
// and message is sometimes an object keyed by field rather than a
// string, so both are decoded loosely.
type gitlabErrorBody struct {
	Message json.RawMessage `json:"message"`
	Error   string          `json:"error"`
}

// gitlabStatusError never includes the token or a raw body.
func gitlabStatusError(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("gitlab: token invalid or expired — replace the access token")
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e gitlabErrorBody
	msg := ""
	if json.Unmarshal(body, &e) == nil {
		msg = e.Error
		if msg == "" && len(e.Message) > 0 {
			var text string
			if json.Unmarshal(e.Message, &text) == nil {
				msg = text
			} else {
				msg = string(e.Message)
			}
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if strings.HasPrefix(msg, "{") || strings.HasPrefix(msg, "<") {
			msg = ""
		}
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if msg != "" {
		return fmt.Errorf("gitlab: status %d: %s", resp.StatusCode, msg)
	}
	return fmt.Errorf("gitlab: status %d", resp.StatusCode)
}
