package automations

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// TestNoteToolsShape pins the two tools' names and flags: read_note is
// a ReadOnly read whose content is fenced, write_note a trusted write.
func TestNoteToolsShape(t *testing.T) {
	ts := NoteTools(&Store{}, "a1", "r1")
	if len(ts) != 2 || ts[0].Name != "read_note" || ts[1].Name != "write_note" {
		t.Fatalf("tools = %v", ts)
	}
	if !ts[0].ReadOnly || ts[0].Trusted {
		t.Fatalf("read_note ReadOnly=%v Trusted=%v, want true false", ts[0].ReadOnly, ts[0].Trusted)
	}
	if ts[1].ReadOnly || !ts[1].Trusted {
		t.Fatalf("write_note ReadOnly=%v Trusted=%v, want false true", ts[1].ReadOnly, ts[1].Trusted)
	}
	for _, tool := range ts {
		if !json.Valid(tool.InputSchema) {
			t.Fatalf("%s schema is not JSON", tool.Name)
		}
	}
	if !strings.Contains(ts[1].Description, "10 notes of 4096 bytes") {
		t.Fatalf("write_note description must name the caps: %q", ts[1].Description)
	}
}

// TestWriteNoteRejectsBeforeTheStore covers the argument and cap errors
// write_note returns without touching the database.
func TestWriteNoteRejectsBeforeTheStore(t *testing.T) {
	write := NoteTools(&Store{}, "a1", "r1")[1]
	cases := []struct {
		name, args, want string
	}{
		{"not json", `{`, "invalid write_note arguments"},
		{"missing content", `{"name":"x"}`, "needs both name and content"},
		{"missing name", `{"content":"x"}`, "needs both name and content"},
		{"bad name", `{"name":"Bad Name","content":"x"}`, "note name must match"},
		{"empty name", `{"name":"","content":"x"}`, "note name must match"},
		{"over 4 KB", `{"name":"big","content":"` + strings.Repeat("x", MaxNoteBytes+1) + `"}`, "at most 4096 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := write.Execute(context.Background(), json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadNoteRejectsBadArgs(t *testing.T) {
	read := NoteTools(&Store{}, "a1", "r1")[0]
	if _, err := read.Execute(context.Background(), json.RawMessage(`[`)); err == nil || !strings.Contains(err.Error(), "invalid read_note arguments") {
		t.Fatalf("err = %v", err)
	}
}

// TestWriteExemptByTriggerKind pins which runs write notes without the
// permission chain: operator-scheduled and operator-started ones only.
func TestWriteExemptByTriggerKind(t *testing.T) {
	for kind, want := range map[string]bool{
		TriggerCron: true, TriggerManual: true,
		TriggerWebhook: false, TriggerConnectorEvent: false, TriggerChannel: false, "": false,
	} {
		if got := writeExempt(kind); got != want {
			t.Errorf("writeExempt(%q) = %v, want %v", kind, got, want)
		}
	}
}

// TestMissionHooksSkipMissionsOutsideARun: a mission with no run never
// reaches the store.
func TestMissionHooksSkipMissionsOutsideARun(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := missions.Mission{ID: "m1"}
	if ts := MissionTools(&Store{}, log)(context.Background(), m); ts != nil {
		t.Fatalf("tools = %v, want nil", ts)
	}
	if gs := MissionGrants(&Store{}, log)(context.Background(), m); gs != nil {
		t.Fatalf("grants = %v, want nil", gs)
	}
}
