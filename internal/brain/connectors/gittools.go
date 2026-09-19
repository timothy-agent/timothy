package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// The read-only pull request chat tools, built once for any
// gitprovider.Client (D-101, issue #795). github_tools.go and
// bitbucket_tools.go used to carry a copy each: same four names, same
// schemas, same descriptions, two sets of literals that had to be kept
// byte-identical by a test. The rendering stays per-provider (the
// Client's PRDescription/PRDiff/... methods); only the tool surface is
// shared.
const (
	gitPRListDefault = 10
	gitPRListMax     = 50
)

// gitRepoArg matches the "owner/name" (GitHub) or "workspace/slug"
// (Bitbucket) form every PR tool takes.
var gitRepoArg = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$`)

// parseRepoArg validates the shared repo argument into a RepoRef.
func parseRepoArg(repo string) (gitprovider.RepoRef, error) {
	m := gitRepoArg.FindStringSubmatch(strings.TrimSpace(repo))
	if m == nil {
		// One message for every kind: the tools already say "owner/name on
		// GitHub, workspace/slug on Bitbucket" in their schema, and a
		// per-kind message would put the duplication back (issue #795).
		return gitprovider.RepoRef{}, fmt.Errorf("repo must be \"owner/name\" (workspace/slug on Bitbucket), got %q", repo)
	}
	return gitprovider.RepoRef{Owner: m[1], Name: m[2]}, nil
}

// gitPRArgs decodes the shared {repo, number} argument shape.
func gitPRArgs(args json.RawMessage) (gitprovider.RepoRef, int, error) {
	var in struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return gitprovider.RepoRef{}, 0, err
	}
	ref, err := parseRepoArg(in.Repo)
	if err != nil {
		return gitprovider.RepoRef{}, 0, err
	}
	if in.Number < 1 {
		return gitprovider.RepoRef{}, 0, fmt.Errorf("number must be a positive pull request number, got %d", in.Number)
	}
	return ref, in.Number, nil
}

// prNumberSchema is the {repo, number} input schema the three
// single-PR tools share.
const prNumberSchema = `{"type":"object","properties":{
			"repo":{"type":"string","description":"owner/name on GitHub, workspace/slug on Bitbucket"},
			"number":{"type":"integer","minimum":1}
		},"required":["repo","number"],"additionalProperties":false}`

// gitPRTools is the read-only pull request surface every git provider
// kind serves. Every tool is a pure read with the connector's
// credential resolved at call time, marked ReadOnly so a mission turn
// can use it through missions' connector-reads resolver. Writes
// (comment, approve, merge) deliberately have no tool here: chat
// reaches them through the provider's MCP connector, missions through
// destinations.
func gitPRTools(c gitprovider.Client) []*tools.Tool {
	return []*tools.Tool{
		{
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
				ref, err := parseRepoArg(in.Repo)
				if err != nil {
					return "", err
				}
				if in.State == "" {
					in.State = "open"
				}
				if in.MaxResults <= 0 {
					in.MaxResults = gitPRListDefault
				}
				if in.MaxResults > gitPRListMax {
					in.MaxResults = gitPRListMax
				}
				return c.ListPRs(ctx, ref, in.State, in.MaxResults)
			},
		},
		{
			Name:        "get_pull_request",
			ReadOnly:    true,
			Description: "Read one pull request's metadata: title, author, state, head/base branches and head commit, changed-file and line counts, and the full description. Use get_pull_request_diff for the code changes and list_pull_request_comments for the discussion; this tool returns neither.",
			InputSchema: json.RawMessage(prNumberSchema),
			Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
				ref, number, err := gitPRArgs(args)
				if err != nil {
					return "", err
				}
				return c.PRDescription(ctx, ref, number)
			},
		},
		{
			Name:        "get_pull_request_diff",
			ReadOnly:    true,
			Description: "Read a pull request's unified diff, the same text git diff base...head would print. Use this to review the actual code changes. Large diffs are cut at a fixed size with a marker saying how much was omitted; review what is returned rather than retrying.",
			InputSchema: json.RawMessage(prNumberSchema),
			Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
				ref, number, err := gitPRArgs(args)
				if err != nil {
					return "", err
				}
				return c.PRDiff(ctx, ref, number)
			},
		},
		{
			Name:        "list_pull_request_comments",
			ReadOnly:    true,
			Description: "List the discussion on a pull request in chronological order: conversation comments and inline review comments (marked with their file and line). Use this to see reviewer feedback and whether it was addressed. This tool is read-only; it cannot post a comment.",
			InputSchema: json.RawMessage(prNumberSchema),
			Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
				ref, number, err := gitPRArgs(args)
				if err != nil {
					return "", err
				}
				return c.PRComments(ctx, ref, number)
			},
		},
	}
}
