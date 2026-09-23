// Package automations stores automations: a trigger set, one action
// and the ceilings a run of it obeys (issue #821).
package automations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

const (
	ActionMission  = "mission"
	ActionWorkflow = "workflow"

	TriggerCron           = "cron"
	TriggerManual         = "manual"
	TriggerWebhook        = "webhook"
	TriggerConnectorEvent = "connector_event"
	TriggerChannel        = "channel"

	ConcurrencySkip     = "skip"
	ConcurrencyQueue    = "queue"
	ConcurrencyParallel = "parallel"

	RunQueued  = "queued"
	RunRunning = "running"
	RunDone    = "done"
	RunFailed  = "failed"
	RunSkipped = "skipped"
)

const (
	maxNameRunes        = 64
	maxDescriptionRunes = 1024
	maxTriggers         = 5
	maxAllowlist        = 64
	// MaxNotes caps notes per automation.
	MaxNotes = 10
	// MaxNoteBytes caps one note's content.
	MaxNoteBytes = 4096
)

var (
	ErrNotFound     = errors.New("automation not found")
	ErrNameConflict = errors.New("an automation with this name already exists")
	ErrNoteLimit    = fmt.Errorf("an automation holds at most %d notes", MaxNotes)
	ErrUnknownAgent = errors.New("unknown agent_id")
)

// Automation is one automations row with its triggers.
type Automation struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Description         string     `json:"description"`
	AgentID             string     `json:"agent_id"`
	Action              Action     `json:"action"`
	Concurrency         string     `json:"concurrency"`
	MaxConcurrent       int        `json:"max_concurrent"`
	MaxRunsPerHour      int        `json:"max_runs_per_hour"`
	Continuity          bool       `json:"continuity"`
	NotesEnabled        bool       `json:"notes_enabled"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	DisabledReason      string     `json:"disabled_reason,omitempty"`
	Enabled             bool       `json:"enabled"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	Triggers            []Trigger  `json:"triggers"`
}

// Action is what a run does: start a mission from Mission, or (later)
// a workflow run.
type Action struct {
	Kind       string                    `json:"kind"`
	Mission    *missions.MissionTemplate `json:"mission,omitempty"`
	WorkflowID string                    `json:"workflow_id,omitempty"`
}

// Trigger is one automation_triggers row.
type Trigger struct {
	ID            string          `json:"id"`
	AutomationID  string          `json:"automation_id"`
	Kind          string          `json:"kind"`
	Config        json.RawMessage `json:"config"`
	CredentialRef string          `json:"credential_ref,omitempty"`
	ToolAllowlist []string        `json:"tool_allowlist,omitempty"`
	State         json.RawMessage `json:"state"`
	Enabled       bool            `json:"enabled"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// Run is one automation_runs row.
type Run struct {
	ID            string          `json:"id"`
	AutomationID  string          `json:"automation_id"`
	TriggerID     *string         `json:"trigger_id,omitempty"`
	EventID       *int64          `json:"event_id,omitempty"`
	DedupKey      string          `json:"dedup_key"`
	Status        string          `json:"status"`
	SkipReason    string          `json:"skip_reason,omitempty"`
	Event         json.RawMessage `json:"event"`
	MissionID     string          `json:"mission_id,omitempty"`
	WorkflowRunID string          `json:"workflow_run_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	StartedAt     *time.Time      `json:"started_at,omitempty"`
	FinishedAt    *time.Time      `json:"finished_at,omitempty"`
}

// Note is one automation_notes row.
type Note struct {
	Name           string    `json:"name"`
	Content        string    `json:"content"`
	UpdatedAt      time.Time `json:"updated_at"`
	UpdatedByRunID string    `json:"updated_by_run_id,omitempty"`
}

// CronConfig is a cron trigger's config.
type CronConfig struct {
	Expr string `json:"expr"`
}

// validateName trims name and checks it: 1..64 runes, no control
// characters. Returns the trimmed name.
func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("name is required")
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", fmt.Errorf("name must be at most %d characters", maxNameRunes)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("name must not contain control characters")
		}
	}
	return name, nil
}

// validate checks a whole automation and normalizes it in place: the
// trimmed name and each trigger's canonical config.
func validate(a *Automation) error {
	name, err := validateName(a.Name)
	if err != nil {
		return err
	}
	a.Name = name
	if utf8.RuneCountInString(a.Description) > maxDescriptionRunes {
		return fmt.Errorf("description must be at most %d characters", maxDescriptionRunes)
	}
	if !isUUID(a.AgentID) {
		return errors.New("agent_id must be a UUID")
	}
	if err := validateAction(a.Action); err != nil {
		return err
	}
	if err := validateConcurrency(a.Concurrency, a.MaxConcurrent, a.MaxRunsPerHour); err != nil {
		return err
	}
	if len(a.Triggers) == 0 {
		return errors.New("at least one trigger is required")
	}
	if len(a.Triggers) > maxTriggers {
		return fmt.Errorf("at most %d triggers are allowed", maxTriggers)
	}
	for i := range a.Triggers {
		if err := validateTrigger(&a.Triggers[i]); err != nil {
			return fmt.Errorf("triggers[%d]: %w", i, err)
		}
	}
	return nil
}

// validateAction accepts a mission action with a valid template.
func validateAction(act Action) error {
	switch act.Kind {
	case ActionMission:
		if act.Mission == nil {
			return errors.New("action.mission is required for a mission action")
		}
		if act.WorkflowID != "" {
			return errors.New("action.workflow_id is only valid for a workflow action")
		}
		return missions.ValidateTemplate(*act.Mission)
	case ActionWorkflow:
		return errors.New("workflow actions are not available yet")
	default:
		return fmt.Errorf("action.kind must be %q", ActionMission)
	}
}

func validateConcurrency(mode string, maxConcurrent, maxRunsPerHour int) error {
	switch mode {
	case ConcurrencySkip, ConcurrencyQueue, ConcurrencyParallel:
	default:
		return errors.New(`concurrency must be "skip", "queue" or "parallel"`)
	}
	if maxConcurrent < 1 || maxConcurrent > 3 {
		return errors.New("max_concurrent must be between 1 and 3")
	}
	if maxConcurrent != 1 && mode != ConcurrencyParallel {
		return errors.New("max_concurrent above 1 needs concurrency parallel")
	}
	if maxRunsPerHour < 1 || maxRunsPerHour > 60 {
		return errors.New("max_runs_per_hour must be between 1 and 60")
	}
	return nil
}

// validateTrigger checks one trigger and rewrites its config to the
// canonical form.
func validateTrigger(t *Trigger) error {
	switch t.Kind {
	case TriggerCron:
		var c CronConfig
		dec := json.NewDecoder(bytes.NewReader(t.Config))
		dec.DisallowUnknownFields()
		if len(t.Config) == 0 || dec.Decode(&c) != nil {
			return errors.New(`cron trigger config must be {"expr": "<cron expression>"}`)
		}
		if err := ValidateCron(c.Expr); err != nil {
			return err
		}
		t.Config, _ = json.Marshal(c)
	case TriggerManual:
		trimmed := bytes.TrimSpace(t.Config)
		if len(trimmed) != 0 && string(trimmed) != "null" && string(trimmed) != "{}" {
			return errors.New("manual trigger takes no config")
		}
		t.Config = json.RawMessage(`{}`)
	case TriggerWebhook, TriggerConnectorEvent, TriggerChannel:
		return fmt.Errorf("trigger kind %q is not available yet", t.Kind)
	default:
		return fmt.Errorf("unknown trigger kind %q", t.Kind)
	}
	if t.CredentialRef != "" {
		return fmt.Errorf("credential_ref is not valid on a %s trigger", t.Kind)
	}
	if len(t.ToolAllowlist) > maxAllowlist {
		return fmt.Errorf("tool_allowlist holds at most %d entries", maxAllowlist)
	}
	for _, name := range t.ToolAllowlist {
		if strings.TrimSpace(name) == "" {
			return errors.New("tool_allowlist entries must be non-empty")
		}
	}
	return nil
}

var noteNameRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// validateNote checks a note's name and content size.
func validateNote(name, content string) error {
	if !noteNameRe.MatchString(name) {
		return errors.New("note name must match ^[a-z0-9_-]{1,64}$")
	}
	if len(content) > MaxNoteBytes {
		return fmt.Errorf("note content must be at most %d bytes", MaxNoteBytes)
	}
	return nil
}

// isUUID reports whether s is a canonical 36-character UUID.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
	}
	return true
}

// ValidationError marks input an automation, trigger or note rejects.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

func invalid(err error) error {
	if err == nil {
		return nil
	}
	return &ValidationError{Err: err}
}

// Validate checks a whole automation and normalizes it in place: the
// trimmed name and each trigger's canonical config.
func Validate(a *Automation) error { return invalid(validate(a)) }

// ValidateAction accepts a mission action with a valid template.
func ValidateAction(act Action) error { return invalid(validateAction(act)) }

// ValidateNote checks a note's name and content size.
func ValidateNote(name, content string) error { return invalid(validateNote(name, content)) }
