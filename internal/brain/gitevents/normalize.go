package gitevents

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/events"
)

const provider = "github"

type ghLabel struct {
	Name string `json:"name"`
}

type ghUser struct {
	Login string `json:"login"`
}

// ghIssue is the subset of a pull request or issue object the
// normalizer reads. PullRequest is set on an issue that is a PR.
type ghIssue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	HTMLURL     string          `json:"html_url"`
	Body        string          `json:"body"`
	User        ghUser          `json:"user"`
	Labels      []ghLabel       `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request"`
}

// ghComment is a review or comment object.
type ghComment struct {
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
}

// ghPayload is the subset of an event payload the normalizer reads.
type ghPayload struct {
	Action      string     `json:"action"`
	Number      int        `json:"number"`
	PullRequest *ghIssue   `json:"pull_request"`
	Issue       *ghIssue   `json:"issue"`
	Label       *ghLabel   `json:"label"`
	Review      *ghComment `json:"review"`
	Comment     *ghComment `json:"comment"`
}

// normalizeEvent maps one feed event of repo to a payload; ok is false
// for event types and actions the poller ignores. self compares the
// actor with selfLogin, case-insensitively.
func normalizeEvent(connectorID, repo, selfLogin string, e connectors.GitHubRepoEvent) (events.ConnectorEventPayload, bool) {
	var p ghPayload
	if json.Unmarshal(e.Payload, &p) != nil {
		return events.ConnectorEventPayload{}, false
	}
	out := events.ConnectorEventPayload{
		Provider: provider, ConnectorID: connectorID, Repo: repo, Action: p.Action,
		Author: e.Actor.Login, Self: isSelf(e.Actor.Login, selfLogin), ProviderEventID: e.ID, OccurredAt: e.CreatedAt,
	}
	var subject *ghIssue
	var comment *ghComment
	switch {
	case e.Type == "PullRequestEvent" && p.Action == "opened":
		out.Kind, subject = events.KindPROpened, p.PullRequest
	case e.Type == "PullRequestEvent" && p.Action == "labeled":
		out.Kind, subject = events.KindPRLabeled, p.PullRequest
	case e.Type == "PullRequestReviewEvent" && (p.Action == "created" || p.Action == "submitted"):
		out.Kind, subject, comment = events.KindPRReview, p.PullRequest, p.Review
	case e.Type == "PullRequestReviewCommentEvent" && p.Action == "created":
		out.Kind, subject, comment = events.KindPRReviewComment, p.PullRequest, p.Comment
	case e.Type == "IssueCommentEvent" && p.Action == "created":
		out.Kind, subject, comment = events.KindIssueComment, p.Issue, p.Comment
	default:
		return events.ConnectorEventPayload{}, false
	}
	if subject == nil {
		subject = &ghIssue{}
	}
	out.Number = p.Number
	if out.Number == 0 {
		out.Number = subject.Number
	}
	out.Title, out.URL, out.Body = subject.Title, subject.HTMLURL, subject.Body
	out.PullRequest = out.Kind != events.KindIssueComment || (len(subject.PullRequest) > 0 && string(subject.PullRequest) != "null")
	for _, l := range subject.Labels {
		out.Labels = appendLabel(out.Labels, l.Name)
	}
	if out.Kind == events.KindPRLabeled && p.Label != nil {
		out.Labels = appendLabel(out.Labels, p.Label.Name)
	}
	if comment != nil {
		out.Body = comment.Body
		if comment.HTMLURL != "" {
			out.URL = comment.HTMLURL
		}
	}
	if out.URL == "" && out.Number > 0 {
		kind := "issues"
		if out.PullRequest {
			kind = "pull"
		}
		out.URL = fmt.Sprintf("https://github.com/%s/%s/%d", repo, kind, out.Number)
	}
	return out, true
}

// normalizeRun maps a completed workflow run linked to a pull request
// to a check.completed payload; ok is false for any other run.
func normalizeRun(connectorID, repo, selfLogin string, r connectors.GitHubWorkflowRun) (events.ConnectorEventPayload, bool) {
	if r.Status != "completed" || len(r.PullRequests) == 0 {
		return events.ConnectorEventPayload{}, false
	}
	attempt := max(r.RunAttempt, 1)
	occurred := r.UpdatedAt
	if occurred.IsZero() {
		occurred = r.CreatedAt
	}
	return events.ConnectorEventPayload{
		Provider: provider, ConnectorID: connectorID, Repo: repo, Kind: events.KindCheckCompleted, Action: "completed",
		Number: r.PullRequests[0].Number, Title: r.DisplayTitle, URL: r.HTMLURL, Author: r.Actor.Login,
		Conclusion: r.Conclusion, CheckName: r.Name, PullRequest: true, Self: isSelf(r.Actor.Login, selfLogin),
		ProviderEventID: fmt.Sprintf("run:%d:%d", r.ID, attempt), OccurredAt: occurred,
	}, true
}

// normalizeFeed walks a newest-first feed page, stopping at the first
// event not newer than lastID and skipping events created before
// notBefore (the first-sight floor; zero disables it). newest is the
// feed's newest id when it moved past lastID.
func normalizeFeed(connectorID, repo, selfLogin string, feed []connectors.GitHubRepoEvent, lastID string, notBefore time.Time) (out []events.ConnectorEventPayload, newest string) {
	for i, e := range feed {
		if !newer(e.ID, lastID) {
			break
		}
		if i == 0 {
			newest = e.ID
		}
		if !notBefore.IsZero() && e.CreatedAt.Before(notBefore) {
			continue
		}
		if p, ok := normalizeEvent(connectorID, repo, selfLogin, e); ok {
			out = append(out, p)
		}
	}
	return out, newest
}

// newer reports whether event id is after last: numerically when both
// parse, else by inequality.
func newer(id, last string) bool {
	if last == "" {
		return true
	}
	a, errA := strconv.ParseInt(id, 10, 64)
	b, errB := strconv.ParseInt(last, 10, 64)
	if errA == nil && errB == nil {
		return a > b
	}
	return id != last
}

func isSelf(login, selfLogin string) bool {
	return selfLogin != "" && strings.EqualFold(login, selfLogin)
}

func appendLabel(labels []string, name string) []string {
	if name == "" || slices.Contains(labels, name) {
		return labels
	}
	return append(labels, name)
}
