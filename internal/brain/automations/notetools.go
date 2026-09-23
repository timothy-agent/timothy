package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	readNoteToolName  = "read_note"
	writeNoteToolName = "write_note"
)

// NoteTools builds read_note and write_note for one run. The automation
// id lives only in the closure, so no argument can reach another
// automation's notes.
func NoteTools(store *Store, automationID, runID string) []*tools.Tool {
	return []*tools.Tool{readNoteTool(store, automationID), writeNoteTool(store, automationID, runID)}
}

func readNoteTool(store *Store, automationID string) *tools.Tool {
	return &tools.Tool{
		Name:        readNoteToolName,
		ReadOnly:    true,
		Description: "Read this automation's notes, facts kept between its runs. With a name, returns that note's content; without one, lists every note with its size and last update.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string", "description": "The note to read. Omit to list all notes."}
			}
		}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Name string `json:"name"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &a); err != nil {
					return "", fmt.Errorf("invalid read_note arguments: %w", err)
				}
			}
			if a.Name != "" {
				n, err := store.GetNote(ctx, automationID, a.Name)
				if errors.Is(err, ErrNotFound) {
					return "", fmt.Errorf("no note named %s", a.Name)
				}
				if err != nil {
					return "", err
				}
				return n.Content, nil
			}
			notes, err := store.ListNotes(ctx, automationID)
			if err != nil {
				return "", err
			}
			if len(notes) == 0 {
				return "no notes yet", nil
			}
			var b strings.Builder
			for _, n := range notes {
				fmt.Fprintf(&b, "%s (%d bytes, updated %s)\n", n.Name, len(n.Content), n.UpdatedAt.UTC().Format(time.RFC3339))
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func writeNoteTool(store *Store, automationID, runID string) *tools.Tool {
	return &tools.Tool{
		Name:    writeNoteToolName,
		Trusted: true,
		Description: fmt.Sprintf("Create or replace one of this automation's notes so a later run can read it. At most %d notes of %d bytes each; name must match ^[a-z0-9_-]{1,64}$.",
			MaxNotes, MaxNoteBytes),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string", "description": "Note name: lowercase letters, digits, _ and -."},
				"content": {"type": "string", "description": "The full new content."}
			},
			"required": ["name", "content"]
		}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Name    *string `json:"name"`
				Content *string `json:"content"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid write_note arguments: %w", err)
			}
			if a.Name == nil || a.Content == nil {
				return "", errors.New("write_note needs both name and content")
			}
			n, _, err := store.PutNote(ctx, automationID, *a.Name, *a.Content, runID)
			if errors.Is(err, ErrNoteLimit) {
				return "", fmt.Errorf("%w; overwrite an existing note instead", err)
			}
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %s (%d bytes)", n.Name, len(n.Content)), nil
		},
	}
}

// writeExempt reports whether runs of triggerKind write notes without
// asking: only operator-scheduled or operator-started runs do.
func writeExempt(triggerKind string) bool {
	return triggerKind == TriggerCron || triggerKind == TriggerManual
}

// noteScope is what a run's note tools need to know about it.
type noteScope struct {
	AutomationID string
	TriggerKind  string
	NotesEnabled bool
}

// runNoteScope reads runID's automation, notes flag and trigger kind.
// A run.now run is manual and a cron.due run is cron; any other run
// takes its trigger's kind.
func (s *Store) runNoteScope(ctx context.Context, runID string) (noteScope, bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return noteScope{}, false, fmt.Errorf("automations note scope: %w", err)
	}
	var sc noteScope
	var eventKind string
	err = db.QueryRow(ctx, `SELECT r.automation_id, a.notes_enabled, COALESCE(r.event->>'kind', ''), COALESCE(t.kind, '')
		FROM automation_runs r JOIN automations a ON a.id = r.automation_id
		LEFT JOIN automation_triggers t ON t.id = r.trigger_id
		WHERE r.id = $1`, runID).Scan(&sc.AutomationID, &sc.NotesEnabled, &eventKind, &sc.TriggerKind)
	if isNotFound(err) {
		return noteScope{}, false, nil
	}
	if err != nil {
		return noteScope{}, false, fmt.Errorf("automations note scope: %w", err)
	}
	switch eventKind {
	case events.KindRunNow:
		sc.TriggerKind = TriggerManual
	case events.KindCronDue:
		sc.TriggerKind = TriggerCron
	}
	return sc, true, nil
}

// MissionTools resolves an automation mission's note tools on every
// turn; nil when the mission has no run, the run is gone or its
// automation has notes off.
func MissionTools(store *Store, log *slog.Logger) func(ctx context.Context, m missions.Mission) []*tools.Tool {
	return func(ctx context.Context, m missions.Mission) []*tools.Tool {
		if m.AutomationRunID == "" {
			return nil
		}
		sc, ok, err := store.runNoteScope(ctx, m.AutomationRunID)
		if err != nil {
			log.Warn("automations: note tools lookup failed", "mission_id", m.ID, "run_id", m.AutomationRunID, "error", err)
			return nil
		}
		if !ok || !sc.NotesEnabled {
			return nil
		}
		return NoteTools(store, sc.AutomationID, m.AutomationRunID)
	}
}

// MissionGrants names the tools an automation mission's session is
// pre-approved for: write_note when its run came from a cron or manual
// trigger. Other triggers' writes go through the permission chain.
func MissionGrants(store *Store, log *slog.Logger) func(ctx context.Context, m missions.Mission) []string {
	return func(ctx context.Context, m missions.Mission) []string {
		if m.AutomationRunID == "" {
			return nil
		}
		sc, ok, err := store.runNoteScope(ctx, m.AutomationRunID)
		if err != nil {
			log.Warn("automations: note grants lookup failed", "mission_id", m.ID, "run_id", m.AutomationRunID, "error", err)
			return nil
		}
		if !ok || !sc.NotesEnabled || !writeExempt(sc.TriggerKind) {
			return nil
		}
		return []string{writeNoteToolName}
	}
}
