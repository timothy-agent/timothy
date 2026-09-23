package gitevents

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/events"
)

var t0 = time.Date(2030, 1, 7, 10, 0, 0, 0, time.UTC)

func feedEvent(id, typ, actor, payload string) connectors.GitHubRepoEvent {
	return connectors.GitHubRepoEvent{ID: id, Type: typ, Actor: connectors.GitHubActor{Login: actor}, CreatedAt: t0, Payload: json.RawMessage(payload)}
}

const prObject = `{"number":5,"title":"Add x","html_url":"https://github.com/o/r/pull/5","body":"pr body","user":{"login":"alice"},"labels":[{"name":"timothy"},{"name":"bug"}]}`

func TestNormalizeEvent(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   connectors.GitHubRepoEvent
		ok   bool
		want events.ConnectorEventPayload
	}{
		{"pr opened", feedEvent("1", "PullRequestEvent", "alice", `{"action":"opened","number":5,"pull_request":`+prObject+`}`), true,
			events.ConnectorEventPayload{Kind: events.KindPROpened, Action: "opened", Number: 5, Title: "Add x", URL: "https://github.com/o/r/pull/5",
				Author: "alice", Labels: []string{"timothy", "bug"}, Body: "pr body", PullRequest: true}},
		{"pr labeled adds the label", feedEvent("2", "PullRequestEvent", "bob", `{"action":"labeled","number":5,"label":{"name":"urgent"},"pull_request":`+prObject+`}`), true,
			events.ConnectorEventPayload{Kind: events.KindPRLabeled, Action: "labeled", Number: 5, Title: "Add x", URL: "https://github.com/o/r/pull/5",
				Author: "bob", Labels: []string{"timothy", "bug", "urgent"}, Body: "pr body", PullRequest: true}},
		{"pr closed ignored", feedEvent("3", "PullRequestEvent", "alice", `{"action":"closed","number":5,"pull_request":`+prObject+`}`), false, events.ConnectorEventPayload{}},
		{"review", feedEvent("4", "PullRequestReviewEvent", "carol", `{"action":"created","review":{"body":"lgtm","html_url":"https://github.com/o/r/pull/5#pullrequestreview-1"},"pull_request":`+prObject+`}`), true,
			events.ConnectorEventPayload{Kind: events.KindPRReview, Action: "created", Number: 5, Title: "Add x", URL: "https://github.com/o/r/pull/5#pullrequestreview-1",
				Author: "carol", Labels: []string{"timothy", "bug"}, Body: "lgtm", PullRequest: true}},
		{"review comment", feedEvent("5", "PullRequestReviewCommentEvent", "carol", `{"action":"created","comment":{"body":"nit","html_url":"https://github.com/o/r/pull/5#discussion_r1"},"pull_request":`+prObject+`}`), true,
			events.ConnectorEventPayload{Kind: events.KindPRReviewComment, Action: "created", Number: 5, Title: "Add x", URL: "https://github.com/o/r/pull/5#discussion_r1",
				Author: "carol", Labels: []string{"timothy", "bug"}, Body: "nit", PullRequest: true}},
		{"issue comment on issue", feedEvent("6", "IssueCommentEvent", "dave", `{"action":"created","issue":{"number":9,"title":"Bug","labels":[{"name":"triage"}]},"comment":{"body":"same here","html_url":"https://github.com/o/r/issues/9#issuecomment-1"}}`), true,
			events.ConnectorEventPayload{Kind: events.KindIssueComment, Action: "created", Number: 9, Title: "Bug", URL: "https://github.com/o/r/issues/9#issuecomment-1",
				Author: "dave", Labels: []string{"triage"}, Body: "same here"}},
		{"issue comment on pr", feedEvent("7", "IssueCommentEvent", "dave", `{"action":"created","issue":{"number":5,"title":"Add x","pull_request":{"url":"x"}},"comment":{"body":"ping"}}`), true,
			events.ConnectorEventPayload{Kind: events.KindIssueComment, Action: "created", Number: 5, Title: "Add x", URL: "https://github.com/o/r/pull/5",
				Author: "dave", Body: "ping", PullRequest: true}},
		{"issue comment edited ignored", feedEvent("8", "IssueCommentEvent", "dave", `{"action":"edited","issue":{"number":9},"comment":{"body":"x"}}`), false, events.ConnectorEventPayload{}},
		{"push ignored", feedEvent("9", "PushEvent", "alice", `{"ref":"refs/heads/main"}`), false, events.ConnectorEventPayload{}},
		{"bad payload ignored", feedEvent("10", "PullRequestEvent", "alice", `[`), false, events.ConnectorEventPayload{}},
		{"trimmed pr payload", feedEvent("11", "PullRequestEvent", "alice", `{"action":"opened","number":6}`), true,
			events.ConnectorEventPayload{Kind: events.KindPROpened, Action: "opened", Number: 6, URL: "https://github.com/o/r/pull/6", Author: "alice", PullRequest: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeEvent("c1", "o/r", "timothy-bot", tc.ev)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			want := tc.want
			want.Provider, want.ConnectorID, want.Repo, want.ProviderEventID, want.OccurredAt = "github", "c1", "o/r", tc.ev.ID, t0
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("payload\n got %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestNormalizeEventSelf(t *testing.T) {
	ev := feedEvent("1", "IssueCommentEvent", "Timothy-Bot", `{"action":"created","issue":{"number":1},"comment":{"body":"done"}}`)
	if p, _ := normalizeEvent("c1", "o/r", "timothy-bot", ev); !p.Self {
		t.Fatal("self = false for the connector's own login in another case")
	}
	if p, _ := normalizeEvent("c1", "o/r", "someone-else", ev); p.Self {
		t.Fatal("self = true for another login")
	}
	if p, _ := normalizeEvent("c1", "o/r", "", ev); p.Self {
		t.Fatal("self = true with no known login")
	}
}

func TestNormalizedBodyIsCappedInTheEvent(t *testing.T) {
	body := strings.Repeat("x", 3*events.MaxConnectorBodyBytes)
	raw, _ := json.Marshal(map[string]any{"action": "created", "issue": map[string]any{"number": 1}, "comment": map[string]any{"body": body}})
	p, ok := normalizeEvent("c1", "o/r", "", feedEvent("1", "IssueCommentEvent", "a", string(raw)))
	if !ok {
		t.Fatal("not normalized")
	}
	ev, err := events.ConnectorEvent(p)
	if err != nil {
		t.Fatalf("ConnectorEvent: %v", err)
	}
	got, _ := events.DecodeConnectorEvent(ev)
	if len(got.Body) != events.MaxConnectorBodyBytes {
		t.Fatalf("body = %d bytes, want %d", len(got.Body), events.MaxConnectorBodyBytes)
	}
}

func TestNormalizeRun(t *testing.T) {
	run := func(mut func(*connectors.GitHubWorkflowRun)) connectors.GitHubWorkflowRun {
		r := connectors.GitHubWorkflowRun{ID: 77, Name: "ci", DisplayTitle: "Add x", RunAttempt: 2, Status: "completed", Conclusion: "failure",
			HTMLURL: "https://github.com/o/r/actions/runs/77", CreatedAt: t0, UpdatedAt: t0.Add(time.Minute), Actor: connectors.GitHubActor{Login: "alice"}}
		r.PullRequests = append(r.PullRequests, struct {
			Number int `json:"number"`
		}{Number: 5})
		if mut != nil {
			mut(&r)
		}
		return r
	}
	p, ok := normalizeRun("c1", "o/r", "timothy-bot", run(nil))
	if !ok || p.Kind != events.KindCheckCompleted || p.Conclusion != "failure" || p.CheckName != "ci" || p.Number != 5 ||
		p.ProviderEventID != "run:77:2" || p.URL != "https://github.com/o/r/actions/runs/77" || p.Self || !p.OccurredAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("normalizeRun = %+v, %v", p, ok)
	}
	if _, ok := normalizeRun("c1", "o/r", "", run(func(r *connectors.GitHubWorkflowRun) { r.PullRequests = nil })); ok {
		t.Fatal("run without a linked PR normalized")
	}
	if _, ok := normalizeRun("c1", "o/r", "", run(func(r *connectors.GitHubWorkflowRun) { r.Status = "in_progress" })); ok {
		t.Fatal("unfinished run normalized")
	}
	if p, _ := normalizeRun("c1", "o/r", "", run(func(r *connectors.GitHubWorkflowRun) { r.RunAttempt = 0; r.Conclusion = "success" })); p.ProviderEventID != "run:77:1" || p.Conclusion != "success" {
		t.Fatalf("attempt 0 payload = %+v", p)
	}
	if p, _ := normalizeRun("c1", "o/r", "ALICE", run(nil)); !p.Self {
		t.Fatal("run by the connector's login not self")
	}
}

func TestNormalizeFeed(t *testing.T) {
	opened := `{"action":"opened","number":1}`
	feed := []connectors.GitHubRepoEvent{
		feedEvent("105", "PullRequestEvent", "a", opened),
		feedEvent("104", "PushEvent", "a", `{}`),
		feedEvent("103", "PullRequestEvent", "a", opened),
		feedEvent("102", "PullRequestEvent", "a", opened),
	}
	feed[3].CreatedAt = t0.Add(-time.Hour)

	out, newest := normalizeFeed("c1", "o/r", "", feed, "103", time.Time{})
	if newest != "105" || len(out) != 1 || out[0].ProviderEventID != "105" {
		t.Fatalf("stop at last: newest %q out %v", newest, ids(out))
	}
	out, newest = normalizeFeed("c1", "o/r", "", feed, "", t0.Add(-time.Minute))
	if newest != "105" || !slices.Equal(ids(out), []string{"105", "103"}) {
		t.Fatalf("first sight: newest %q out %v, want older event skipped", newest, ids(out))
	}
	out, newest = normalizeFeed("c1", "o/r", "", feed, "105", time.Time{})
	if newest != "" || len(out) != 0 {
		t.Fatalf("nothing new: newest %q out %v", newest, ids(out))
	}
	out, newest = normalizeFeed("c1", "o/r", "", nil, "105", time.Time{})
	if newest != "" || len(out) != 0 {
		t.Fatalf("304: newest %q out %v", newest, ids(out))
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		id, last string
		want     bool
	}{
		{"10", "", true}, {"10", "9", true}, {"9", "10", false}, {"10", "10", false}, {"abc", "abd", true}, {"abc", "abc", false},
	} {
		if got := newer(tc.id, tc.last); got != tc.want {
			t.Errorf("newer(%q, %q) = %v, want %v", tc.id, tc.last, got, tc.want)
		}
	}
}

func ids(ps []events.ConnectorEventPayload) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.ProviderEventID)
	}
	return out
}
