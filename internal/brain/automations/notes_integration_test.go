//go:build integration

package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// createNotes inserts an automation named tag+name with a manual
// trigger, the given goal and the continuity/notes flags.
func (h *dispatchHarness) createNotes(name, goal string, continuity, notes bool) string {
	h.t.Helper()
	id, err := h.store.Create(h.t.Context(), Automation{
		Name: h.tag + name, AgentID: h.agentID,
		Action:      Action{Kind: ActionMission, Mission: &missions.MissionTemplate{Goal: h.tag + goal, Kind: missions.KindGeneral, Light: true}},
		Concurrency: ConcurrencySkip, MaxConcurrent: 1, MaxRunsPerHour: 60, Enabled: true,
		Continuity: continuity, NotesEnabled: notes,
		Triggers: []Trigger{{Kind: TriggerManual, Enabled: true}},
	})
	if err != nil {
		h.t.Fatalf("create automation: %v", err)
	}
	return id
}

// startRun fires automationID now, starts the run and returns it with
// its mission.
func (h *dispatchHarness) startRun(automationID string) (Run, missions.Mission) {
	h.t.Helper()
	h.fireNow(automationID)
	h.pass()
	rs := h.runs(automationID)
	r := rs[len(rs)-1]
	if r.Status != RunRunning || r.MissionID == "" {
		h.t.Fatalf("run = %s mission %q, want running with a mission (event %s)", r.Status, r.MissionID, r.Event)
	}
	m, err := h.missions.Get(h.t.Context(), r.MissionID)
	if err != nil {
		h.t.Fatalf("get mission: %v", err)
	}
	return r, m
}

func sourceOf(m missions.Mission, kind string) (missions.SourceEntry, bool) {
	for _, e := range m.Sources {
		if e.Source == kind {
			return e, true
		}
	}
	return missions.SourceEntry{}, false
}

func noteTool(t *testing.T, store *Store, automationID, runID, name string) func(args string) (string, error) {
	t.Helper()
	for _, tool := range NoteTools(store, automationID, runID) {
		if tool.Name == name {
			return func(args string) (string, error) { return tool.Execute(context.Background(), json.RawMessage(args)) }
		}
	}
	t.Fatalf("no %s tool", name)
	return nil
}

// TestNotesAcrossTwoRuns: run 1 writes a note through write_note; run 2
// sees it as a notes source, gets the trigger source, continues from
// run 1's mission and interpolates its goal with a missing key empty.
func TestNotesAcrossTwoRuns(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.createNotes("two-runs", "count {{event.kind}} [{{event.missing}}] last={{notes.run_count}}", true, true)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	run1, m1 := h.startRun(id)
	if m1.Goal != h.tag+"count run.now [] last=" {
		t.Fatalf("run 1 goal = %q", m1.Goal)
	}
	if m1.ParentMissionID != "" || m1.ParentContext() != "" {
		t.Fatalf("run 1 has a parent: %q", m1.ParentMissionID)
	}
	if _, ok := sourceOf(m1, missions.SourceKindNotes); ok {
		t.Fatal("run 1 has a notes source with no notes")
	}
	if e, ok := sourceOf(m1, missions.SourceKindTrigger); !ok || !strings.Contains(e.Digest, `"kind": "run.now"`) {
		t.Fatalf("run 1 trigger source = %+v", e)
	}
	tools1 := MissionTools(h.store, log)(t.Context(), m1)
	if len(tools1) != 2 {
		t.Fatalf("run 1 note tools = %d, want 2", len(tools1))
	}
	if got := MissionGrants(h.store, log)(t.Context(), m1); fmt.Sprint(got) != "[write_note]" {
		t.Fatalf("manual run grants = %v, want [write_note]", got)
	}
	out, err := noteTool(t, h.store, id, run1.ID, "write_note")(`{"name":"run_count","content":"1 {{event.kind}}"}`)
	if err != nil || out != "wrote run_count (16 bytes)" {
		t.Fatalf("write_note = %q, %v", out, err)
	}
	h.finish(m1.ID, false)

	run2, m2 := h.startRun(id)
	n, err := h.store.GetNote(t.Context(), id, "run_count")
	if err != nil || n.UpdatedByRunID != run1.ID {
		t.Fatalf("note = %+v, %v; want updated_by_run_id %s", n, err, run1.ID)
	}
	if m2.Goal != h.tag+"count run.now [] last=1 {{event.kind}}" {
		t.Fatalf("run 2 goal = %q, note content must stay literal", m2.Goal)
	}
	notes, ok := sourceOf(m2, missions.SourceKindNotes)
	if !ok || notes.Digest != "run_count:\n1 {{event.kind}}" {
		t.Fatalf("run 2 notes source = %+v", notes)
	}
	if _, ok := sourceOf(m2, missions.SourceKindTrigger); !ok {
		t.Fatal("run 2 has no trigger source")
	}
	if m2.ParentMissionID != m1.ID || m2.OriginKind != missions.OriginAutomation || !m2.Unattended || m2.AutomationRunID != run2.ID {
		t.Fatalf("run 2 parent=%q origin=%q unattended=%v run=%q", m2.ParentMissionID, m2.OriginKind, m2.Unattended, m2.AutomationRunID)
	}
	var lineage missions.SourceEntry
	for _, e := range m2.Sources {
		if e.Source == missions.SourceKindMission && e.ID == missions.ParentLineageID {
			lineage = e
		}
	}
	events, err := h.missions.Events(t.Context(), m1.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := h.missions.Get(t.Context(), m1.ID)
	if lineage.MissionID != m1.ID || lineage.Digest != missions.OutcomeDigest(parent, events, parent.Phase, parent.FailureReason) || lineage.Digest == "" {
		t.Fatalf("lineage = %+v", lineage)
	}
	if !strings.Contains(m2.ReferencedContext(), "Automation notes:\nrun_count:") {
		t.Fatalf("referenced context = %q", m2.ReferencedContext())
	}
}

// TestNoteToolsExecute covers read_note and write_note against the
// store: read by name, read missing, list, overwrite and the note cap.
func TestNoteToolsExecute(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.createNotes("exec", "g", false, true)
	run, _ := h.startRun(id)
	read := noteTool(t, h.store, id, run.ID, "read_note")
	write := noteTool(t, h.store, id, run.ID, "write_note")

	if out, err := read(`{}`); err != nil || out != "no notes yet" {
		t.Fatalf("empty list = %q, %v", out, err)
	}
	if _, err := read(`{"name":"nope"}`); err == nil || err.Error() != "no note named nope" {
		t.Fatalf("read missing err = %v", err)
	}
	for i := range MaxNotes {
		if _, err := write(fmt.Sprintf(`{"name":"n%02d","content":"v%d"}`, i, i)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if _, err := write(`{"name":"eleventh","content":"x"}`); err == nil || !errors.Is(err, ErrNoteLimit) || !strings.Contains(err.Error(), "at most 10 notes") {
		t.Fatalf("11th note err = %v", err)
	}
	if out, err := write(`{"name":"n03","content":"replaced"}`); err != nil || out != "wrote n03 (8 bytes)" {
		t.Fatalf("overwrite at the cap = %q, %v", out, err)
	}
	if out, err := read(`{"name":"n03"}`); err != nil || out != "replaced" {
		t.Fatalf("read n03 = %q, %v", out, err)
	}
	out, err := read(``)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != MaxNotes || !strings.HasPrefix(lines[0], "n00 (2 bytes, updated ") || !strings.HasPrefix(lines[3], "n03 (8 bytes, updated ") {
		t.Fatalf("list = %q", out)
	}
}

// TestNotesNeverCrossAutomations is the regression that A's notes never
// reach B: not through B's sources, B's read_note, or B's interpolation.
func TestNotesNeverCrossAutomations(t *testing.T) {
	h := newDispatchHarness(t)
	a := h.createNotes("iso-a", "a {{notes.secret}}", true, true)
	b := h.createNotes("iso-b", "b [{{notes.secret}}]", true, true)
	runA, _ := h.startRun(a)
	if _, err := noteTool(t, h.store, a, runA.ID, "write_note")(`{"name":"secret","content":"only-a"}`); err != nil {
		t.Fatal(err)
	}
	runB, mB := h.startRun(b)
	if _, ok := sourceOf(mB, missions.SourceKindNotes); ok {
		t.Fatalf("B's mission carries a notes source: %+v", mB.Sources)
	}
	if mB.Goal != h.tag+"b []" {
		t.Fatalf("B goal = %q", mB.Goal)
	}
	readB := noteTool(t, h.store, b, runB.ID, "read_note")
	if out, err := readB(`{}`); err != nil || out != "no notes yet" {
		t.Fatalf("B list = %q, %v", out, err)
	}
	if _, err := readB(`{"name":"secret"}`); err == nil {
		t.Fatal("B read A's note")
	}
	if _, err := noteTool(t, h.store, b, runB.ID, "write_note")(`{"name":"secret","content":"from-b"}`); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.store.GetNote(t.Context(), a, "secret"); n.Content != "only-a" || n.UpdatedByRunID != runA.ID {
		t.Fatalf("A's note changed through B: %+v", n)
	}
	// B's run-scoped tools resolve to B even when asked through the hook.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tool := range MissionTools(h.store, log)(t.Context(), mB) {
		if tool.Name == "read_note" {
			if out, _ := tool.Execute(t.Context(), json.RawMessage(`{"name":"secret"}`)); out != "from-b" {
				t.Fatalf("B hook read = %q", out)
			}
		}
	}
}

// TestNotesDisabledAndContinuityOff: notes off means no notes source,
// no tools and no grant; continuity off means no parent.
func TestNotesDisabledAndContinuityOff(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.createNotes("off", "g [{{notes.kept}}]", false, false)
	if _, _, err := h.store.PutNote(t.Context(), id, "kept", "x", ""); err != nil {
		t.Fatal(err)
	}
	_, m1 := h.startRun(id)
	h.finish(m1.ID, false)
	_, m2 := h.startRun(id)
	if _, ok := sourceOf(m2, missions.SourceKindNotes); ok {
		t.Fatal("notes source with notes_enabled=false")
	}
	if m2.Goal != h.tag+"g []" {
		t.Fatalf("goal = %q, notes must not interpolate when disabled", m2.Goal)
	}
	if m2.ParentMissionID != "" || m2.ParentContext() != "" {
		t.Fatalf("continuity off but parent = %q", m2.ParentMissionID)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if ts := MissionTools(h.store, log)(t.Context(), m2); ts != nil {
		t.Fatalf("tools with notes off = %d", len(ts))
	}
	if gs := MissionGrants(h.store, log)(t.Context(), m2); gs != nil {
		t.Fatalf("grants with notes off = %v", gs)
	}
}

// TestContinuitySkipsDeletedPreviousMission: a previous mission the
// lineage lookup cannot find starts the run without a parent.
func TestContinuitySkipsDeletedPreviousMission(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.createNotes("gone", "g", true, true)
	_, m1 := h.startRun(id)
	h.finish(m1.ID, false)
	h.starter.lineage = func(ctx context.Context, missionID string) (missions.SourceEntry, error) {
		return missions.SourceEntry{}, fmt.Errorf("mission %s: %w", missionID, missions.ErrNotFound)
	}
	_, m2 := h.startRun(id)
	if m2.ParentMissionID != "" {
		t.Fatalf("parent = %q, want none", m2.ParentMissionID)
	}
}

// TestNoteGrantsFollowTriggerKind: a cron run is pre-approved for
// write_note, a webhook run is not, and both still get the tools.
func TestNoteGrantsFollowTriggerKind(t *testing.T) {
	h := newDispatchHarness(t)
	id := h.createNotes("kinds", "g", false, true)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, _ := h.pool.Get()
	var webhookTrigger string
	if err := db.QueryRow(t.Context(), `INSERT INTO automation_triggers (automation_id, kind) VALUES ($1, 'webhook') RETURNING id`, id).Scan(&webhookTrigger); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, event string
		trigger     *string
		want        string
	}{
		{"cron", `{"kind":"cron.due"}`, nil, "[write_note]"},
		{"webhook", `{"kind":"webhook.received"}`, &webhookTrigger, "[]"},
	}
	for _, tc := range cases {
		runID, _, err := h.store.CreateRun(t.Context(), nil, Run{AutomationID: id, TriggerID: tc.trigger, DedupKey: h.tag + tc.name, Status: RunDone, Event: json.RawMessage(tc.event)})
		if err != nil {
			t.Fatal(err)
		}
		m := missions.Mission{ID: "m-" + tc.name, AutomationRunID: runID}
		if got := fmt.Sprint(MissionGrants(h.store, log)(t.Context(), m)); got != tc.want {
			t.Fatalf("%s grants = %s, want %s", tc.name, got, tc.want)
		}
		if ts := MissionTools(h.store, log)(t.Context(), m); len(ts) != 2 {
			t.Fatalf("%s tools = %d, want 2", tc.name, len(ts))
		}
	}
}
