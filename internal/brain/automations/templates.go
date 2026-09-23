package automations

import (
	"encoding/json"
	"slices"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// Requirement kinds a template can list.
const (
	RequireConnector   = "connector"
	RequireDestination = "destination"
)

// mailConnectorKinds satisfy a "mail" connector requirement.
var mailConnectorKinds = []string{"imap", "google", "microsoft"}

// Template is one built-in starting point for a new automation (issue
// #825). Action and Triggers are create-request fragments.
type Template struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Icon         string            `json:"icon"`
	Action       Action            `json:"action"`
	Triggers     []TemplateTrigger `json:"triggers"`
	Requires     []Requirement     `json:"requires"`
	NotesEnabled *bool             `json:"notes_enabled,omitempty"`
	Continuity   *bool             `json:"continuity,omitempty"`
}

// TemplateTrigger is a trigger as a create request takes it.
type TemplateTrigger struct {
	Kind   string          `json:"kind"`
	Config json.RawMessage `json:"config"`
}

// Requirement names a connector or destination kind a template needs.
// Connector value "mail" is met by any of imap, google or microsoft.
type Requirement struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

func templateCron(expr string) TemplateTrigger {
	cfg, _ := json.Marshal(CronConfig{Expr: expr})
	return TemplateTrigger{Kind: TriggerCron, Config: cfg}
}

func missionAction(kind string, light bool, goal string) Action {
	return Action{Kind: ActionMission, Mission: &missions.MissionTemplate{Goal: goal, Kind: kind, Light: light, AutoApproveTools: true}}
}

// Templates returns the built-in templates, freshly built on each call.
func Templates() []Template {
	on := true
	return []Template{
		{
			ID:          "daily-repo-digest",
			Name:        "Daily repo digest",
			Description: "Every weekday morning, summarize yesterday's commits, PRs and issues of one repository and email it.",
			Icon:        "git-branch",
			Action: missionAction(missions.KindGeneral, true,
				"Summarize yesterday's activity in the GitHub repository owner/name: commits on the default branch, "+
					"pull requests opened, merged or closed, and issues opened or closed. Put anything that needs my attention first, "+
					"then list the rest grouped by type, one line per item with its link. If nothing happened, say so in one line."),
			Triggers: []TemplateTrigger{templateCron("0 8 * * 1-5")},
			Requires: []Requirement{{RequireConnector, "github"}, {RequireDestination, "email"}},
		},
		{
			ID:          "pr-review-comment",
			Name:        "PR review comment",
			Description: "Review a pull request and post the findings as a review comment on it.",
			Icon:        "git-pull-request",
			Action: missionAction(missions.KindGeneral, false,
				"Review the GitHub pull request linked here: {{event.pr_url}}. If no link is shown, review the most recently "+
					"opened pull request in owner/name instead. Read the description and the diff, then post one review comment "+
					"on the pull request. List real problems first (bugs, missing tests, security issues), each with its file and "+
					"line, then smaller suggestions. Skip style points a linter would catch."),
			// Manual: a template cannot name a connector id, so the operator adds
			// a connector_event trigger on pr.opened (issue #826) after creating.
			Triggers: []TemplateTrigger{{Kind: TriggerManual, Config: json.RawMessage(`{}`)}},
			Requires: []Requirement{{RequireConnector, "github"}},
		},
		{
			ID:          "weekly-kb-freshness",
			Name:        "Weekly KB freshness check",
			Description: "Every Monday, list knowledge base documents that are old or contradict newer ones, with a refresh action for each.",
			Icon:        "book-open",
			Action: missionAction(missions.KindGeneral, true,
				"Go through the knowledge base and find documents older than 90 days, and documents that contradict a newer "+
					"document on the same topic. For each one, give its title, its age, what looks outdated or conflicting, and one "+
					"refresh action: update, merge or delete. Sort the list so the documents most likely to give a wrong answer come first. "+
					"If every document is fresh, say so in one line."),
			Triggers: []TemplateTrigger{templateCron("0 9 * * 1")},
			Requires: []Requirement{},
		},
		{
			ID:          "inbox-triage",
			Name:        "Inbox triage",
			Description: "Every two hours on weekdays, sort unread mail into needs reply, waiting and ignore.",
			Icon:        "inbox",
			Action: missionAction(missions.KindGeneral, true,
				"Check my unread email and put each message in one of three groups: needs reply, waiting on someone else, or ignore. "+
					"For needs reply, give the sender, the subject and one line on what they want. For waiting, say who owes what. "+
					"Give ignore as a count only. Do not reply to, archive or mark any message."),
			Triggers: []TemplateTrigger{templateCron("0 7-19/2 * * 1-5")},
			Requires: []Requirement{{RequireConnector, "mail"}},
		},
		{
			ID:          "coverage-watch",
			Name:        "Coverage watch",
			Description: "Every weekday evening, run the tests with coverage and report the change since the last run.",
			Icon:        "gauge",
			Action: missionAction(missions.KindCoding, false,
				"Run the test suite of the GitHub repository owner/name with coverage enabled and read the total coverage percentage. "+
					"Coverage from the last run (empty on the first run): {{notes.last_coverage}}. "+
					"Save the new percentage with write_note under the name last_coverage. Then report the new percentage, "+
					"the change since the last run, and the files whose coverage dropped the most."),
			Triggers:     []TemplateTrigger{templateCron("0 18 * * 1-5")},
			Requires:     []Requirement{{RequireConnector, "github"}},
			NotesEnabled: &on,
			Continuity:   &on,
		},
	}
}

// Missing returns the requirements of t that the enabled connector and
// destination kinds do not meet; never nil.
func Missing(t Template, connectorKinds, destinationKinds []string) []Requirement {
	out := []Requirement{}
	for _, r := range t.Requires {
		met := false
		switch {
		case r.Kind == RequireConnector && r.Value == "mail":
			met = slices.ContainsFunc(mailConnectorKinds, func(k string) bool { return slices.Contains(connectorKinds, k) })
		case r.Kind == RequireConnector:
			met = slices.Contains(connectorKinds, r.Value)
		case r.Kind == RequireDestination:
			met = slices.Contains(destinationKinds, r.Value)
		}
		if !met {
			out = append(out, r)
		}
	}
	return out
}
