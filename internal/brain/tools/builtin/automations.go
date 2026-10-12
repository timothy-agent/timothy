package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// AutomationRecord is the automations tools' own view of an
// automations.Automation — kept as a local struct (not an import of
// internal/brain/automations) because automations imports missions,
// which imports this package (runner.go's builtin.Shell/WriteFile);
// automations.Automation would create an import cycle. cmd/brain/
// main.go adapts real values into this shape.
type AutomationRecord struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	AgentID        string    `json:"agent_id"`
	ActionKind     string    `json:"action"`
	MissionGoal    string    `json:"mission_goal"`
	MissionName    string    `json:"mission_name,omitempty"`
	MissionKind    string    `json:"mission_kind"`
	Light          bool      `json:"light,omitempty"`
	Concurrency    string    `json:"concurrency"`
	MaxConcurrent  int       `json:"max_concurrent"`
	MaxRunsPerHour int       `json:"max_runs_per_hour"`
	Enabled        bool      `json:"enabled"`
	DisabledReason string    `json:"disabled_reason,omitempty"`
	Triggers       []AutomationTriggerView `json:"triggers"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// AutomationTriggerView is one trigger of an AutomationRecord.
type AutomationTriggerView struct {
	Kind    string          `json:"kind"`
	Config  json.RawMessage `json:"config"`
	Enabled bool            `json:"enabled"`
}

// AutomationTriggerInput is a trigger create_automation and
// update_automation pass through to the adapter; the automations
// package validates the config per kind.
type AutomationTriggerInput struct {
	Kind    string          `json:"kind"`
	Config  json.RawMessage `json:"config"`
	Enabled bool            `json:"enabled"`
}

// AutomationCreateInput is everything create_automation needs on the
// store side. Trigger configs pass through unvalidated here; the
// automations package's own validation is the gate.
type AutomationCreateInput struct {
	Name             string
	Description      string
	AgentID          string
	MissionGoal      string
	MissionName      string
	MissionKind      string
	Light            bool
	AutoApproveTools bool
	Concurrency      string
	MaxConcurrent    int
	MaxRunsPerHour   int
	Enabled          bool
	Triggers         []AutomationTriggerInput
}

// AutomationPatchInput mirrors the patchable fields; nil means leave
// unchanged.
type AutomationPatchInput struct {
	Name           *string
	Description    *string
	AgentID        *string
	Enabled        *bool
	MaxRunsPerHour *int
	Triggers       *[]AutomationTriggerInput
}

// automationStore is the narrow slice of automations access the CRUD
// tools need, an interface so tests fake it without a real Postgres
// pool. Ids resolve by automation id or exact name.
type automationStore interface {
	ListAutomations(ctx context.Context) ([]AutomationRecord, error)
	GetAutomation(ctx context.Context, idOrName string) (AutomationRecord, error)
	CreateAutomation(ctx context.Context, in AutomationCreateInput) (string, error)
	PatchAutomation(ctx context.Context, idOrName string, p AutomationPatchInput) error
	DeleteAutomation(ctx context.Context, idOrName string) error
}

const (
	automationDefaultMaxRunsPerHour = 6
	automationTriggerHelp           = `trigger kinds and their config: cron {"expr": "0 8 * * 1-5"}; manual {}; channel {"channel_id": "<uuid>", "pattern": "<regex>"}; webhook {"scheme": "github"|"generic", "filters": [...]}; connector_event {"connector_id": "<uuid>", "repo": "owner/name", "events": [...]}`
)

// automationListItem is the compact list shape; the model reads this.
type automationListItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Action    string `json:"action"`
	Triggers  string `json:"triggers"`
	UpdatedAt string `json:"updated_at"`
}

func toAutomationListItem(a AutomationRecord) automationListItem {
	kinds := make([]string, 0, len(a.Triggers))
	for _, t := range a.Triggers {
		kinds = append(kinds, t.Kind)
	}
	return automationListItem{
		ID: a.ID, Name: a.Name, Enabled: a.Enabled,
		Action:    a.ActionKind,
		Triggers:  strings.Join(kinds, ","),
		UpdatedAt: a.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ListAutomations returns the create/read/list/update/delete
// automations tool set.
func ListAutomations(store automationStore) *tools.Tool {
	return &tools.Tool{
		Name:        "list_automations",
		ReadOnly:    true,
		Description: "List every automation with its id, name, enabled state, action and trigger kinds. Read one with read_automation.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
			list, err := store.ListAutomations(ctx)
			if err != nil {
				return "", fmt.Errorf("list automations: %w", err)
			}
			if len(list) == 0 {
				return "no automations", nil
			}
			items := make([]automationListItem, 0, len(list))
			for _, a := range list {
				items = append(items, toAutomationListItem(a))
			}
			b, err := json.Marshal(items)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	}
}

type readAutomationArgs struct {
	Automation string `json:"automation"`
}

// ReadAutomation returns one automation's full snapshot, triggers
// included, by id or exact name.
func ReadAutomation(store automationStore) *tools.Tool {
	return &tools.Tool{
		Name:        "read_automation",
		ReadOnly:    true,
		Description: "Read one automation in full: mission template, triggers with their config, ceilings and enabled state. automation is the id or the exact name.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"automation":{"type":"string","description":"automation id or exact name"}
		},"required":["automation"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args readAutomationArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.Automation) == "" {
				return "", fmt.Errorf("automation is required")
			}
			a, err := store.GetAutomation(ctx, strings.TrimSpace(args.Automation))
			if err != nil {
				return "", fmt.Errorf("read automation: %w", err)
			}
			b, err := json.Marshal(a)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	}
}

type createAutomationArgs struct {
	Name           string                     `json:"name"`
	Description    string                     `json:"description"`
	Agent          string                     `json:"agent"`
	MissionGoal    string                     `json:"mission_goal"`
	MissionName    string                     `json:"mission_name"`
	MissionKind    string                     `json:"mission_kind"`
	Light          bool                       `json:"light"`
	Triggers       []AutomationTriggerInput   `json:"triggers"`
	MaxRunsPerHour int                        `json:"max_runs_per_hour"`
	Enabled        bool                       `json:"enabled"`
}

// CreateAutomation creates an automation identical to one created from
// the UI: a mission action from a template plus its triggers.
func CreateAutomation(store automationStore) *tools.Tool {
	return &tools.Tool{
		Name: "create_automation",
		Description: "Create an automation that starts a mission from a template on each trigger fire. " +
			"Arguments: name; agent (agent name or id, e.g. \"general\"); mission_goal (may template event fields like {{event.sender}} for channel/webhook triggers); " +
			"mission_kind (general or coding, default general); light (single-turn general mission); triggers (at least one; " + automationTriggerHelp + "); " +
			"max_runs_per_hour (default 6, 1-60); enabled (default true).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"name":{"type":"string","description":"automation name, unique, max 64 chars"},
			"description":{"type":"string","description":"what this automation is for"},
			"agent":{"type":"string","description":"agent name or id the mission runs as"},
			"mission_goal":{"type":"string","description":"the mission template goal"},
			"mission_name":{"type":"string","description":"display name for started missions"},
			"mission_kind":{"type":"string","enum":["general","coding"]},
			"light":{"type":"boolean","description":"single-turn general mission without plan or review"},
			"triggers":{"type":"array","minItems":1,"items":{"type":"object","properties":{
				"kind":{"type":"string","enum":["cron","manual","channel","webhook","connector_event"]},
				"config":{"type":"object"},
				"enabled":{"type":"boolean"}
			},"required":["kind","config"],"additionalProperties":false}},
			"max_runs_per_hour":{"type":"integer","minimum":1,"maximum":60},
			"enabled":{"type":"boolean"}
		},"required":["name","agent","mission_goal","triggers"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args createAutomationArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			name := strings.TrimSpace(args.Name)
			if name == "" {
				return "", fmt.Errorf("name is required")
			}
			agent := strings.TrimSpace(args.Agent)
			if agent == "" {
				return "", fmt.Errorf("agent is required")
			}
			goal := strings.TrimSpace(args.MissionGoal)
			if goal == "" {
				return "", fmt.Errorf("mission_goal is required")
			}
			if len(args.Triggers) == 0 {
				return "", fmt.Errorf("at least one trigger is required")
			}
			maxRuns := args.MaxRunsPerHour
			if maxRuns == 0 {
				maxRuns = automationDefaultMaxRunsPerHour
			}
			in := AutomationCreateInput{
				Name: name, Description: args.Description, AgentID: agent,
				MissionGoal: goal, MissionName: strings.TrimSpace(args.MissionName),
				MissionKind: args.MissionKind, Light: args.Light,
				AutoApproveTools: true,
				Concurrency:      "skip", MaxConcurrent: 1,
				MaxRunsPerHour: maxRuns,
				Enabled:        args.Enabled,
				Triggers:       args.Triggers,
			}
			id, err := store.CreateAutomation(ctx, in)
			if err != nil {
				return "", fmt.Errorf("create automation: %w", err)
			}
			return fmt.Sprintf("created automation %s (%s)", id, name), nil
		},
	}
}

type updateAutomationArgs struct {
	Automation     string                   `json:"automation"`
	Name           *string                  `json:"name"`
	Description    *string                  `json:"description"`
	Agent          *string                  `json:"agent"`
	Enabled        *bool                    `json:"enabled"`
	MaxRunsPerHour *int                     `json:"max_runs_per_hour"`
	Triggers       *[]AutomationTriggerInput `json:"triggers"`
}

// UpdateAutomation patches an automation; only the fields present in
// the arguments change.
func UpdateAutomation(store automationStore) *tools.Tool {
	return &tools.Tool{
		Name: "update_automation",
		Description: "Update an automation by id or exact name. Only the fields present in the arguments change; triggers, when given, replace the whole set. " +
			"Trigger shape: " + automationTriggerHelp,
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"automation":{"type":"string","description":"automation id or exact name"},
			"name":{"type":"string"},
			"description":{"type":"string"},
			"agent":{"type":"string","description":"agent name or id the mission runs as"},
			"enabled":{"type":"boolean"},
			"max_runs_per_hour":{"type":"integer","minimum":1,"maximum":60},
			"triggers":{"type":"array","minItems":1,"items":{"type":"object","properties":{
				"kind":{"type":"string","enum":["cron","manual","channel","webhook","connector_event"]},
				"config":{"type":"object"},
				"enabled":{"type":"boolean"}
			},"required":["kind","config"],"additionalProperties":false}}
		},"required":["automation"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args updateAutomationArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			id := strings.TrimSpace(args.Automation)
			if id == "" {
				return "", fmt.Errorf("automation is required")
			}
			p := AutomationPatchInput{
				Name: args.Name, Description: args.Description,
				Enabled: args.Enabled, MaxRunsPerHour: args.MaxRunsPerHour,
				Triggers: args.Triggers,
			}
			if args.Agent != nil {
				v := strings.TrimSpace(*args.Agent)
				p.AgentID = &v
			}
			if err := store.PatchAutomation(ctx, id, p); err != nil {
				return "", fmt.Errorf("update automation: %w", err)
			}
			return "updated automation " + id, nil
		},
	}
}

type deleteAutomationArgs struct {
	Automation string `json:"automation"`
}

// DeleteAutomation removes an automation and its triggers; past runs
// stay.
func DeleteAutomation(store automationStore) *tools.Tool {
	return &tools.Tool{
		Name:        "delete_automation",
		Description: "Delete an automation by id or exact name. Its runs' history stays in the runs list.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"automation":{"type":"string","description":"automation id or exact name"}
		},"required":["automation"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args deleteAutomationArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			id := strings.TrimSpace(args.Automation)
			if id == "" {
				return "", fmt.Errorf("automation is required")
			}
			if err := store.DeleteAutomation(ctx, id); err != nil {
				return "", fmt.Errorf("delete automation: %w", err)
			}
			return "deleted automation " + id, nil
		},
	}
}
