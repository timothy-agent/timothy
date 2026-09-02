//go:build integration

package missions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const marker = "itest-mission "

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sweep(ctx, db)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Logf("teardown sweep skipped: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		sweep(cctx, conn)
	})
	return NewStore(pool, log)
}

// execer is the shared Exec surface between a pool connection and a
// one-shot pgx.Conn: lets sweep run identically at setup (via the
// pool) and teardown (via a fresh connection, since the pool dies with
// t.Context()).
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// sweep clears every fixture row this package's tests create. Missions
// spawned by a test schedule carry the schedule's mission_template
// goal verbatim, not the marker prefix: delete them via the schedule
// join FIRST, before deleting schedules (whose FK would otherwise
// orphan them under ON DELETE CASCADE at an unpredictable order
// relative to a concurrently running test). Schedule NAMES can't carry
// marker verbatim (it has a trailing space; schedule names must be a
// slug: see schedules_integration_test.go's slugMarker), so schedule
// rows are also swept by the slug form.
func sweep(ctx context.Context, db execer) {
	slug := strings.TrimSpace(marker)
	_, _ = db.Exec(ctx, sweepMissionsSQL(`schedule_id IN (
		SELECT id FROM schedules WHERE name LIKE $1 || '%' OR name LIKE $2 || '%'
	)`), marker, slug)
	_, _ = db.Exec(ctx, "DELETE FROM schedules WHERE name LIKE $1 || '%' OR name LIKE $2 || '%'", marker, slug)
	_, _ = db.Exec(ctx, sweepMissionsSQL("goal LIKE $1 || '%'"), marker)
}

// sweepMissionsSQL deletes missions matching filter along with the
// hidden sessions they provisioned, which would otherwise linger as
// empty chats in the session list.
func sweepMissionsSQL(filter string) string {
	return `WITH gone AS (
		DELETE FROM missions WHERE ` + filter + ` RETURNING session_id
	), ids AS (SELECT session_id FROM gone WHERE session_id IS NOT NULL),
	g AS (DELETE FROM session_grants WHERE session_id IN (SELECT session_id FROM ids)),
	a AS (DELETE FROM tool_audit WHERE session_id IN (SELECT session_id FROM ids)),
	o AS (DELETE FROM tool_outputs WHERE session_id IN (SELECT session_id FROM ids)),
	e AS (DELETE FROM session_events WHERE session_id IN (SELECT session_id FROM ids))
	DELETE FROM sessions WHERE id IN (SELECT session_id FROM ids)`
}

func TestMissionCRUD(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "crud", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Phase != PhaseDiscover || m.Status != StatusIdle || m.MaxIterations != 3 {
		t.Fatalf("Get = %+v, want default discover/idle/3", m)
	}

	list, err := s.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, lm := range list {
		if lm.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("List did not include the created mission")
	}

	if _, err := s.Get(ctx, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("Get of a nonexistent id succeeded")
	}
}

// TestListFilterQueryMatchesNameOrGoal covers ListFilter.Query (the
// composer #-mention mission search, GET /v1/missions?q=): a
// case-insensitive substring match on name OR goal, applied at the SQL
// level (ILIKE), narrowing an unrelated third mission out.
func TestListFilterQueryMatchesNameOrGoal(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	byGoal, err := s.Create(ctx, Mission{Goal: marker + "fix the flaky QUERYTAG upload test", Kind: "general"})
	if err != nil {
		t.Fatalf("Create byGoal: %v", err)
	}
	byName, err := s.Create(ctx, Mission{Goal: marker + "unrelated goal", Kind: "general"})
	if err != nil {
		t.Fatalf("Create byName: %v", err)
	}
	if err := s.SetNameIfEmpty(ctx, byName, "QueryTag rename"); err != nil {
		t.Fatalf("SetNameIfEmpty: %v", err)
	}
	other, err := s.Create(ctx, Mission{Goal: marker + "something else entirely", Kind: "general"})
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}

	list, err := s.List(ctx, ListFilter{Query: "querytag"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]bool{}
	for _, m := range list {
		got[m.ID] = true
	}
	if !got[byGoal] {
		t.Fatal("List(q=querytag) did not include the mission matched by goal")
	}
	if !got[byName] {
		t.Fatal("List(q=querytag) did not include the mission matched by name")
	}
	if got[other] {
		t.Fatal("List(q=querytag) included an unrelated mission")
	}
}

// TestMissionReferencedContextRoundTrips covers the sources column's
// referenced-pick entries (composer #-mention references resolved at
// create time): round-trips through Create/Get unchanged, additive to
// (never replacing) the parent-lineage digest.
func TestMissionReferencedContextRoundTrips(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	parentID, err := s.Create(ctx, Mission{Goal: marker + "referenced-context-parent", Kind: "general"})
	if err != nil {
		t.Fatalf("Create parent: %v", err)
	}
	id, err := s.Create(ctx, Mission{
		Goal: marker + "referenced-context", Kind: "general", ParentMissionID: parentID,
		Sources: []SourceEntry{
			{Source: SourceKindMission, ID: ParentLineageID, MissionID: parentID, Digest: "prior mission fixed the signup bug"},
			{Source: SourceKindKB, DocID: "doc1", Name: "runbook", Digest: "kb doc: runbook contents"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.ReferencedContext() != "runbook:\nkb doc: runbook contents" {
		t.Fatalf("ReferencedContext() = %q, want it to round-trip unchanged", m.ReferencedContext())
	}
	if m.ParentContext() != "prior mission fixed the signup bug" {
		t.Fatalf("ParentContext() = %q, want ReferencedContext() to be additive, not a replacement", m.ParentContext())
	}
}

// TestMissionDelete covers Store.Delete's three outcomes: unknown id
// (ErrNotFound), a live (non-terminal) mission (ErrNotTerminal), and a
// terminal mission: which must actually remove the row, and its
// mission_events via the ON DELETE CASCADE migrations 0025/0027 rely
// on.
func TestMissionDelete(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	if _, err := s.Delete(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete of a nonexistent id = %v, want ErrNotFound", err)
	}

	id, err := s.Create(ctx, Mission{Goal: marker + "delete-live", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Delete(ctx, id); !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("Delete of a live (discover/idle) mission = %v, want ErrNotTerminal", err)
	}

	if err := s.AppendEvent(ctx, id, "mission.progress", map[string]any{"note": "hi"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next: StepState{Phase: PhaseDone, Status: StatusDone},
	}); err != nil {
		t.Fatalf("ApplyTransition to terminal: %v", err)
	}

	deleted, err := s.Delete(ctx, id)
	if err != nil {
		t.Fatalf("Delete of a terminal mission: %v", err)
	}
	if deleted.ID != id {
		t.Fatalf("Delete returned mission %q, want %q", deleted.ID, id)
	}

	if _, err := s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound (row gone)", err)
	}
	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events after Delete: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events after Delete = %+v, want none (mission_events cascades on mission delete)", events)
	}
}

// TestMissionParentLineageRoundTrips covers the follow-up columns:
// parent_mission_id/parent_context round-trip through Create/Get, and
// deleting the (terminal) parent SETs NULL rather than blocking or
// cascading: the child mission stays valid with an empty
// ParentMissionID afterward.
func TestMissionParentLineageRoundTrips(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	parentID, err := s.Create(ctx, Mission{Goal: marker + "parent", Kind: "general"})
	if err != nil {
		t.Fatalf("Create parent: %v", err)
	}
	if err := s.ApplyTransition(ctx, parentID, Transition{
		Next: StepState{Phase: PhaseDone, Status: StatusDone},
	}); err != nil {
		t.Fatalf("ApplyTransition parent to terminal: %v", err)
	}

	childID, err := s.Create(ctx, Mission{
		Goal: marker + "child", Kind: "general",
		ParentMissionID: parentID,
		Sources:         []SourceEntry{{Source: SourceKindMission, ID: ParentLineageID, MissionID: parentID, Digest: "mission goal: parent\nterminal state: done\n"}},
	})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}
	child, err := s.Get(ctx, childID)
	if err != nil {
		t.Fatalf("Get child: %v", err)
	}
	if child.ParentMissionID != parentID {
		t.Fatalf("ParentMissionID = %q, want %q", child.ParentMissionID, parentID)
	}
	if child.ParentContext() != "mission goal: parent\nterminal state: done\n" {
		t.Fatalf("ParentContext() = %q, want it to round-trip unchanged", child.ParentContext())
	}

	if _, err := s.Delete(ctx, parentID); err != nil {
		t.Fatalf("Delete parent: %v", err)
	}
	child, err = s.Get(ctx, childID)
	if err != nil {
		t.Fatalf("Get child after parent delete: %v", err)
	}
	if child.ParentMissionID != "" {
		t.Fatalf("ParentMissionID after parent delete = %q, want empty (ON DELETE SET NULL)", child.ParentMissionID)
	}
	if child.ParentContext() == "" {
		t.Fatal("ParentContext() after parent delete = empty, want the snapshot to survive (it's independent of the FK)")
	}
}

// TestMissionAttachmentsRoundTrip covers the sources column's "pdf"
// entries: a mission created with attachments round-trips them
// (including markdown) through Create/Get, and a mission created with
// a nil Sources slice still succeeds against the NOT NULL column.
func TestMissionAttachmentsRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{
		Goal: marker + "with attachments", Kind: "general",
		Sources: []SourceEntry{
			{Source: SourceKindPDF, ID: "att1", Mime: "application/pdf", Name: "spec.pdf", Markdown: "# Spec\ndo the thing"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(m.Attachments()) != 1 {
		t.Fatalf("Attachments() = %+v, want one", m.Attachments())
	}
	want := SourceEntry{Source: SourceKindPDF, ID: "att1", Mime: "application/pdf", Name: "spec.pdf", Markdown: "# Spec\ndo the thing"}
	if m.Attachments()[0] != want {
		t.Fatalf("Attachments()[0] = %+v, want %+v", m.Attachments()[0], want)
	}

	nilID, err := s.Create(ctx, Mission{Goal: marker + "no attachments", Kind: "general"})
	if err != nil {
		t.Fatalf("Create with nil Sources: %v", err)
	}
	nilM, err := s.Get(ctx, nilID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(nilM.Attachments()) != 0 {
		t.Fatalf("Attachments() = %+v, want empty", nilM.Attachments())
	}
}

func TestMissionPromptOverlayRoundTrips(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{
		Goal: marker + "overlay", Kind: "general", Route: "default",
		PromptOverlay: "You are a careful senior engineer.",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.PromptOverlay != "You are a careful senior engineer." {
		t.Fatalf("PromptOverlay = %q, want it to round-trip unchanged", m.PromptOverlay)
	}

	// Absent means "no agent overlay" (the general agent, e.g.): must
	// stay empty, not some driver default.
	id2, err := s.Create(ctx, Mission{Goal: marker + "no-overlay", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m2, err := s.Get(ctx, id2)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m2.PromptOverlay != "" {
		t.Fatalf("PromptOverlay = %q, want empty when not set", m2.PromptOverlay)
	}
}

// TestMissionHarnessRoundTrips covers D-051: harness is a first-class
// column snapshotted at create time, same shape as PromptOverlay above
// : "" (native) is the default when a mission omits it.
func TestMissionHarnessRoundTrips(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{
		Goal: marker + "harness", Kind: "coding", Route: "default", Harness: "claude-cli",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Harness != "claude-cli" {
		t.Fatalf("Harness = %q, want claude-cli", m.Harness)
	}

	id2, err := s.Create(ctx, Mission{Goal: marker + "no-harness", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m2, err := s.Get(ctx, id2)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m2.Harness != "" {
		t.Fatalf("Harness = %q, want empty (native) when not set", m2.Harness)
	}
}

// TestMissionModelPinsRoundTrip covers route_model/plan_route_model/
// review_route_model (D-078): same shape as TestMissionHarnessRoundTrips:
// set on create, read back verbatim; unset stays empty.
func TestMissionModelPinsRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{
		Goal: marker + "model-pins", Kind: "coding", Route: "default",
		RouteModel: "OpenAI/gpt-5-mini", PlanRouteModel: "GLM (Z.ai)/glm-5.3", ReviewRouteModel: "Anthropic/claude-sonnet-5",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.RouteModel != "OpenAI/gpt-5-mini" {
		t.Fatalf("RouteModel = %q, want OpenAI/gpt-5-mini", m.RouteModel)
	}
	if m.PlanRouteModel != "GLM (Z.ai)/glm-5.3" {
		t.Fatalf("PlanRouteModel = %q, want GLM (Z.ai)/glm-5.3", m.PlanRouteModel)
	}
	if m.ReviewRouteModel != "Anthropic/claude-sonnet-5" {
		t.Fatalf("ReviewRouteModel = %q, want Anthropic/claude-sonnet-5", m.ReviewRouteModel)
	}

	id2, err := s.Create(ctx, Mission{Goal: marker + "no-model-pins", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m2, err := s.Get(ctx, id2)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m2.RouteModel != "" || m2.PlanRouteModel != "" || m2.ReviewRouteModel != "" {
		t.Fatalf("model pins = %q/%q/%q, want all empty when not set", m2.RouteModel, m2.PlanRouteModel, m2.ReviewRouteModel)
	}
}

// TestMissionLightAndFinalOutputRoundTrip covers light (D-069, born
// phase=execute) and SetFinalOutput: same shape as
// TestMissionHarnessRoundTrips above, plus the setter mission state
// (not an event) is written and read back correctly.
func TestMissionLightAndFinalOutputRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "light", Kind: "general", Route: "default", Flow: FlowLight})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Flow != FlowLight {
		t.Fatalf("Flow = %q, want %q", m.Flow, FlowLight)
	}
	if m.Phase != PhaseGenerate {
		t.Fatalf("Phase = %q, want execute for a light mission at create", m.Phase)
	}
	if m.FinalOutput != "" {
		t.Fatalf("FinalOutput = %q, want empty before SetFinalOutput", m.FinalOutput)
	}

	if err := s.SetFinalOutput(ctx, id, "the complete deliverable"); err != nil {
		t.Fatalf("SetFinalOutput: %v", err)
	}
	m, err = s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after SetFinalOutput: %v", err)
	}
	if m.FinalOutput != "the complete deliverable" {
		t.Fatalf("FinalOutput = %q, want %q", m.FinalOutput, "the complete deliverable")
	}

	id2, err := s.Create(ctx, Mission{Goal: marker + "not-light", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m2, err := s.Get(ctx, id2)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m2.Flow == FlowLight {
		t.Fatal("Flow = light, want full when not set")
	}
	if m2.Phase != PhaseDiscover {
		t.Fatalf("Phase = %q, want discover for a non-light mission at create", m2.Phase)
	}
}

// TestMissionOnCompleteRoundTrips covers on_complete: a "github"
// destinations entry snapshotted at create time (issue #480), same
// shape as Harness above: no github entry (do nothing) is the default
// when a mission omits it.
func TestMissionOnCompleteRoundTrips(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{
		Goal: marker + "on-complete", Kind: "coding", Route: "default",
		Sources:      []SourceEntry{{Source: SourceKindGitHub, RepoURL: "https://github.com/octo/repo.git", ConnectorID: "conn1"}},
		Destinations: []DestinationEntry{{Destination: DestinationKindGitHub, Mode: "push_pr"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.OnComplete() != "push_pr" {
		t.Fatalf("OnComplete() = %q, want push_pr", m.OnComplete())
	}

	id2, err := s.Create(ctx, Mission{Goal: marker + "no-on-complete", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m2, err := s.Get(ctx, id2)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m2.OnComplete() != "" {
		t.Fatalf("OnComplete() = %q, want empty when not set", m2.OnComplete())
	}
}

// TestMissionGitStrategyRoundTrips covers branch_pattern/commit_style:
// fields of a "github" destinations entry snapshotted at create time
// (issue #480), same shape as Harness above: "" (use the settings
// default) is the default when a mission omits them.
func TestMissionGitStrategyRoundTrips(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{
		Goal: marker + "git-strategy", Kind: "coding", Route: "default",
		Destinations: []DestinationEntry{{Destination: DestinationKindGitHub, Mode: "push", BranchPattern: "{type}/{login}/{slug}", CommitStyle: "plain"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	e, ok := m.GitHubEntry()
	if !ok {
		t.Fatal("GitHubEntry() ok = false, want true")
	}
	if e.BranchPattern != "{type}/{login}/{slug}" {
		t.Fatalf("BranchPattern = %q, want {type}/{login}/{slug}", e.BranchPattern)
	}
	if e.CommitStyle != "plain" {
		t.Fatalf("CommitStyle = %q, want plain", e.CommitStyle)
	}

	id2, err := s.Create(ctx, Mission{Goal: marker + "no-git-strategy", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m2, err := s.Get(ctx, id2)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := m2.GitHubEntry(); ok {
		t.Fatal("GitHubEntry() ok = true, want false when not set")
	}
}

func TestMissionNameRoundTrips(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{
		Goal: marker + "named", Kind: "general", Route: "default", Name: "Marker Mission",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Name != "Marker Mission" {
		t.Fatalf("Name = %q, want Marker Mission", m.Name)
	}

	id2, err := s.Create(ctx, Mission{Goal: marker + "unnamed", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m2, err := s.Get(ctx, id2)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m2.Name != "" {
		t.Fatalf("Name = %q, want empty when not set", m2.Name)
	}
}

// TestSetNameIfEmpty mirrors session.Store.SetTitleIfEmpty's own
// integration coverage: a name lands once and a second call (the
// generation retrying, or racing a scheduler-set name) never clobbers
// it.
func TestSetNameIfEmpty(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "set-name", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.SetNameIfEmpty(ctx, id, "Generated Name"); err != nil {
		t.Fatalf("SetNameIfEmpty: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Name != "Generated Name" {
		t.Fatalf("Name = %q, want Generated Name", m.Name)
	}

	if err := s.SetNameIfEmpty(ctx, id, "Should Not Land"); err != nil {
		t.Fatalf("SetNameIfEmpty (second call): %v", err)
	}
	m2, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m2.Name != "Generated Name" {
		t.Fatalf("Name = %q, want unchanged Generated Name (guard against clobbering)", m2.Name)
	}
}

func TestSetAndClearPendingPermission(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "permission", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.SetPendingPermission(ctx, id, "perm-1", "shell", `{"command":"rm -rf x"}`, "destructive", "deletes files"); err != nil {
		t.Fatalf("SetPendingPermission: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.PendingPermission != "perm-1" || m.PendingPermissionTool != "shell" ||
		m.PendingPermissionDanger != "destructive" || m.PendingPermissionRationale != "deletes files" {
		t.Fatalf("Get after SetPendingPermission = %+v, want the parked detail persisted", m)
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	last := events[len(events)-1]
	if last.Kind != "mission.permission_requested" {
		t.Fatalf("last event kind = %q, want mission.permission_requested", last.Kind)
	}
	var payload struct {
		Tool, Args, Danger, Rationale string
	}
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}
	if payload.Tool != "shell" || payload.Args != `{"command":"rm -rf x"}` || payload.Danger != "destructive" {
		t.Fatalf("event payload = %+v, want the tool/args/danger the mission parked on", payload)
	}

	if err := s.ClearPendingPermission(ctx, id); err != nil {
		t.Fatalf("ClearPendingPermission: %v", err)
	}
	m, err = s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.PendingPermission != "" || m.PendingPermissionTool != "" || m.PendingPermissionDanger != "" || m.PendingPermissionRationale != "" {
		t.Fatalf("Get after ClearPendingPermission = %+v, want all permission fields empty", m)
	}
}

func TestAppendProgress(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "progress", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.AppendProgress(ctx, id, "first note"); err != nil {
		t.Fatalf("AppendProgress: %v", err)
	}
	if err := s.AppendProgress(ctx, id, "second note"); err != nil {
		t.Fatalf("AppendProgress: %v", err)
	}

	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(m.Progress) != 2 || m.Progress[0].Note != "first note" || m.Progress[1].Note != "second note" {
		t.Fatalf("Get.Progress = %+v, want both notes in order — this is what WorkPacket.Render reads for the next worker turn's memory", m.Progress)
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 mission.progress events alongside the column writes", len(events))
	}
	for _, e := range events {
		if e.Kind != "mission.progress" {
			t.Fatalf("event kind = %q, want mission.progress", e.Kind)
		}
	}
}

func TestApplyTransitionAndEvents(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "transition", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	next := StepState{Phase: PhasePlan, Status: StatusIdle, MaxIterations: 8}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   next,
		Events: []EventDraft{{Kind: "mission.phase_started", Payload: map[string]any{"phase": "plan"}}},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}

	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Phase != PhasePlan {
		t.Fatalf("Phase after transition = %q, want plan", m.Phase)
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "mission.phase_started" || events[0].Seq != 1 {
		t.Fatalf("Events = %+v, want one mission.phase_started at seq 1", events)
	}
}

// TestReviewFindingsRoundTrip confirms the D-092 ledger is real DB
// state: findings and the rework counter written by ApplyTransition
// come back from Get with ids, statuses and counters intact, and a
// fresh mission loads an empty (non-nil) ledger.
func TestReviewFindingsRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "findings", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fresh.ReviewFindings == nil || len(fresh.ReviewFindings) != 0 || fresh.ReworkRounds != 0 {
		t.Fatalf("fresh mission ledger = %+v rounds %d, want empty non-nil and 0", fresh.ReviewFindings, fresh.ReworkRounds)
	}

	findings := []Finding{
		{ID: "F1", Unit: 0, Title: "missing validation", File: "x.go", Detail: "no input check", Severity: SeverityBlocking, Status: FindingOpen, RoundOpened: 1, UntouchedRounds: 1},
		{ID: "F2", Unit: 0, Title: "typo", Severity: SeverityMinor, Status: FindingResolved, RoundOpened: 1},
	}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next: StepState{Phase: PhaseGenerate, Status: StatusIdle, MaxIterations: 3, ReviewFindings: findings, ReworkRounds: 2},
	}); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.ReworkRounds != 2 {
		t.Fatalf("ReworkRounds = %d, want 2", m.ReworkRounds)
	}
	if len(m.ReviewFindings) != 2 || m.ReviewFindings[0] != findings[0] || m.ReviewFindings[1] != findings[1] {
		t.Fatalf("ReviewFindings = %+v, want %+v", m.ReviewFindings, findings)
	}
}

// TestApplyTransitionPersistsUnitVerifyState confirms D-094's per-unit
// verify state is real DB state written through ApplyTransition: a unit
// passes, then regresses, and each step's Units land in the plan jsonb
// (the plan's other keys untouched) with the events appended in order.
// A transition with no units leaves a planless mission's plan alone.
func TestApplyTransitionPersistsUnitVerifyState(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "verify state", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{Next: StepState{Phase: PhasePlan, Status: StatusIdle, MaxIterations: 8}}); err != nil {
		t.Fatalf("ApplyTransition planless: %v", err)
	}
	planless, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(planless.Plan.Units) != 0 {
		t.Fatalf("planless plan = %+v, want no units written", planless.Plan)
	}

	plan := Plan{
		Units:       []PlanUnit{{Title: "write a.md", Artifacts: []string{"a.md"}, VerifyCmd: "test -s a.md"}, {Title: "write b.md"}},
		Assumptions: []PlanAssumption{{Assumption: "format", Default: "markdown"}},
	}
	if err := s.SetPlan(ctx, id, plan); err != nil {
		t.Fatalf("SetPlan: %v", err)
	}

	// Turn 1: unit 0 passes on harness evidence.
	passed := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, Units: plan.Units},
		StepInput{Input: InputReviewApprove, Verified: []UnitVerification{{Unit: 0, Passed: true, Check: "verify_cmd", Excerpt: "ok"}}},
		DefaultConfig,
	)
	if err := s.ApplyTransition(ctx, id, passed); err != nil {
		t.Fatalf("ApplyTransition pass: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if u := m.Plan.Units[0]; !u.Passes || !u.HarnessPassed || u.VerifyCheck != "verify_cmd" || u.VerifyExcerpt != "ok" {
		t.Fatalf("unit 0 after pass = %+v, want passed on harness evidence with the excerpt", u)
	}
	if len(m.Plan.Assumptions) != 1 || m.Plan.Units[1].Title != "write b.md" {
		t.Fatalf("plan after pass = %+v, want assumptions and the other unit untouched", m.Plan)
	}

	// Turn 2: unit 0 regresses while unit 1 passes.
	regressed := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, Units: m.Plan.Units},
		StepInput{Input: InputWorkerRetry, GapFingerprint: "regression:unit_0", Verified: []UnitVerification{
			{Unit: 0, Check: "artifacts", Excerpt: "a.md: not found"}, {Unit: 1, Passed: true, Check: "artifacts"},
		}},
		DefaultConfig,
	)
	if err := s.ApplyTransition(ctx, id, regressed); err != nil {
		t.Fatalf("ApplyTransition regression: %v", err)
	}
	m, err = s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if u := m.Plan.Units[0]; u.Passes || u.HarnessPassed || !u.Regressed || u.VerifyCheck != "artifacts" || u.VerifyExcerpt != "a.md: not found" {
		t.Fatalf("unit 0 after regression = %+v, want pending, regressed, with the failing output", u)
	}
	if u := m.Plan.Units[1]; !u.HarnessPassed || u.Passes {
		t.Fatalf("unit 1 after regression = %+v, want harness-passed awaiting approval", u)
	}
	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	want := []string{"mission.unit_verified", "mission.unit_regressed", "mission.retry"}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event kinds = %v, want %v", kinds, want)
		}
	}
}

// TestPlanApprovalParkRoundTrip confirms D-087 (issue #456) is real DB
// state, not in-memory only: a mission created with auto_approve_plan
// false, parked on PauseApproval via ApplyTransition, survives a fresh
// Get exactly as parked (same as a process restart would see), then
// the approve verb's own transition unparks it to generate.
func TestPlanApprovalParkRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "plan approval park", Kind: "general", AutoApprovePlan: false})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	created, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after create: %v", err)
	}
	if created.AutoApprovePlan {
		t.Fatal("AutoApprovePlan = true after create, want false (explicit request honored)")
	}

	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.plan_awaiting_approval", Payload: map[string]any{}}},
	}); err != nil {
		t.Fatalf("ApplyTransition (park): %v", err)
	}

	// A fresh Get is exactly what a restarted process sees: the park
	// is real DB state, not driver-in-memory.
	parked, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after park: %v", err)
	}
	if parked.Phase != PhasePlan || parked.Status != StatusPaused || parked.PauseReason != PauseApproval {
		t.Fatalf("mission after park = %s/%s/%s, want plan/paused/approval", parked.Phase, parked.Status, parked.PauseReason)
	}

	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseGenerate, Status: StatusIdle, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.plan_approved", Payload: map[string]any{}}},
	}); err != nil {
		t.Fatalf("ApplyTransition (approve): %v", err)
	}
	approved, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after approve: %v", err)
	}
	if approved.Phase != PhaseGenerate || approved.Status != StatusIdle || approved.PauseReason != "" {
		t.Fatalf("mission after approve = %s/%s/%s, want generate/idle/<none>", approved.Phase, approved.Status, approved.PauseReason)
	}
}

// TestApplyTransitionClearsPendingPermissionOnTerminal reproduces a
// real bug: cancelling (or otherwise terminating) a mission parked on
// a permission left the pending_permission_* columns populated:
// the mission was dead, but its "Allow" banner kept showing in the
// UI since nothing ever cleared them on the terminal transition.
func TestApplyTransitionClearsPendingPermissionOnTerminal(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "cancel-parked", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.SetPendingPermission(ctx, id, "perm-1", "shell", `{"command":"echo hi"}`, "safe", "no standing grant"); err != nil {
		t.Fatalf("SetPendingPermission: %v", err)
	}

	// Non-terminal transition must NOT clear it: only a terminal one.
	if err := s.ApplyTransition(ctx, id, Transition{Next: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8}}); err != nil {
		t.Fatalf("ApplyTransition (non-terminal): %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.PendingPermissionTool != "shell" {
		t.Fatalf("PendingPermissionTool after non-terminal transition = %q, want unchanged (shell)", m.PendingPermissionTool)
	}

	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseFailed, Status: StatusError, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.failed", Payload: map[string]any{"reason": "cancelled"}}},
	}); err != nil {
		t.Fatalf("ApplyTransition (terminal): %v", err)
	}
	m, err = s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.PendingPermission != "" || m.PendingPermissionTool != "" || m.PendingPermissionArgs != "" ||
		m.PendingPermissionDanger != "" || m.PendingPermissionRationale != "" {
		t.Fatalf("Get after terminal transition = %+v, want all pending_permission fields cleared", m)
	}
}

// TestApplyTransitionRejectsWriteOnTerminalMission reproduces a real
// bug: a mission cancelled (terminal phase persisted) while a Drive
// loop's turn was in flight: the turn's own ApplyTransition,
// operating on a stale pre-cancel snapshot, must not be allowed to
// overwrite the terminal row and resurrect the mission.
func TestApplyTransitionRejectsWriteOnTerminalMission(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "terminal-guard", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseFailed, Status: StatusError, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.failed", Payload: map[string]any{"reason": "cancelled"}}},
	}); err != nil {
		t.Fatalf("ApplyTransition (cancel): %v", err)
	}

	// A stale in-flight turn's transition arrives after cancel already
	// landed: must be rejected, not written over the terminal row.
	err = s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.turn", Payload: map[string]any{"phase": "generate"}}},
	})
	if !errors.Is(err, ErrTerminal) {
		t.Fatalf("ApplyTransition (stale turn) err = %v, want ErrTerminal", err)
	}

	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Phase != PhaseFailed || m.Status != StatusError {
		t.Fatalf("mission after rejected write: phase=%q status=%q, want failed/error unchanged", m.Phase, m.Status)
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "mission.failed" {
		t.Fatalf("Events = %+v, want only the cancel's mission.failed event, no mission.turn", events)
	}
}

// TestApplyTransitionRejectsWriteOnUnrecognizedPhase reproduces a
// corrupted row: a phase value no running binary recognizes (e.g. a
// column mangled out of band, or a future phase an older binary
// doesn't know). ApplyTransition must treat that as terminal and
// refuse the write, freezing the mission rather than letting stale
// code overwrite an unreadable row.
func TestApplyTransitionRejectsWriteOnUnrecognizedPhase(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "unknown-phase-guard", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET phase = 'quantum' WHERE id = $1`, id); err != nil {
		t.Fatalf("corrupt phase: %v", err)
	}

	err = s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.turn", Payload: map[string]any{"phase": "generate"}}},
	})
	if !errors.Is(err, ErrTerminal) {
		t.Fatalf("ApplyTransition (unrecognized phase) err = %v, want ErrTerminal", err)
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events = %+v, want none appended on rejected write", events)
	}
}

// TestApplyTransitionCancelThenDoubleCancel exercises the two ends of
// the cancel path around the new guard: a cancel applied to a live
// mission still writes normally, and a second cancel on the
// now-terminal mission is rejected rather than silently re-applying.
func TestApplyTransitionCancelThenDoubleCancel(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "double-cancel", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	cancelTransition := Transition{
		Next:   StepState{Phase: PhaseFailed, Status: StatusError, MaxIterations: 8},
		Events: []EventDraft{{Kind: "mission.failed", Payload: map[string]any{"reason": "cancelled"}}},
	}
	if err := s.ApplyTransition(ctx, id, cancelTransition); err != nil {
		t.Fatalf("ApplyTransition (first cancel): %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Phase != PhaseFailed {
		t.Fatalf("mission phase after first cancel = %q, want failed", m.Phase)
	}

	if err := s.ApplyTransition(ctx, id, cancelTransition); !errors.Is(err, ErrTerminal) {
		t.Fatalf("ApplyTransition (second cancel) err = %v, want ErrTerminal", err)
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Events = %+v, want exactly one mission.failed from the first cancel", events)
	}
}

// TestAppendEventSeqMonotonic drives concurrent appends to the SAME
// mission and asserts seq is a gap-free 1..N sequence: the FOR UPDATE
// lock on the mission row must serialize these, not merely avoid a
// crash.
func TestAppendEventSeqMonotonic(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "seq", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.AppendEvent(ctx, id, "mission.recovery", map[string]any{"i": i})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("AppendEvent[%d]: %v", i, err)
		}
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != n {
		t.Fatalf("len(events) = %d, want %d", len(events), n)
	}
	seen := map[int64]bool{}
	for _, e := range events {
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
	for i := int64(1); i <= n; i++ {
		if !seen[i] {
			t.Fatalf("seq %d missing: gap in the sequence", i)
		}
	}
}

func TestSetProvisionedBranchCollision(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id1, err := s.Create(ctx, Mission{Goal: marker + "provision-1", Kind: "coding"})
	if err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	id2, err := s.Create(ctx, Mission{Goal: marker + "provision-2", Kind: "coding"})
	if err != nil {
		t.Fatalf("Create 2: %v", err)
	}

	if err := s.SetProvisioned(ctx, id1, "/ws", "mission/shared-branch", "abc123"); err != nil {
		t.Fatalf("SetProvisioned 1: %v", err)
	}
	// Same workspace+branch, different mission, still active: refused.
	err = s.SetProvisioned(ctx, id2, "/ws", "mission/shared-branch", "abc123")
	if err == nil {
		t.Fatal("SetProvisioned allowed a branch collision with an active mission")
	}

	// A different branch on the same workspace is fine.
	if err := s.SetProvisioned(ctx, id2, "/ws", "mission/other-branch", "abc123"); err != nil {
		t.Fatalf("SetProvisioned distinct branch: %v", err)
	}

	// Once mission 1 is terminal, its branch is free for reuse.
	if err := s.ApplyTransition(ctx, id1, Transition{Next: StepState{Phase: PhaseDone, Status: StatusDone}}); err != nil {
		t.Fatalf("ApplyTransition terminal: %v", err)
	}
	id3, err := s.Create(ctx, Mission{Goal: marker + "provision-3", Kind: "coding"})
	if err != nil {
		t.Fatalf("Create 3: %v", err)
	}
	if err := s.SetProvisioned(ctx, id3, "/ws", "mission/shared-branch", "def456"); err != nil {
		t.Fatalf("SetProvisioned after mission 1 terminal: %v", err)
	}
}

// TestClaimWorkSlotConcurrencyCap fires more concurrent claimants than
// the slot cap and asserts the number actually flipped to working
// never exceeds max: the advisory lock must make the
// count-then-update atomic, not just individually safe.
func TestClaimWorkSlotConcurrencyCap(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	const total, max = 10, 3
	// The cap is global across the shared database: any real mission
	// already 'working' (the suite runs against a live stack) consumes
	// a slot, so the expected claim count is max minus that baseline:
	// asserting a bare max flakes whenever a live mission is running.
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	baseline := 0
	if err := db.QueryRow(ctx, "SELECT count(*) FROM missions WHERE status = 'working'").Scan(&baseline); err != nil {
		t.Fatalf("count working baseline: %v", err)
	}
	want := max - baseline
	if want < 0 {
		want = 0
	}
	ids := make([]string, total)
	for i := range ids {
		id, err := s.Create(ctx, Mission{Goal: marker + "slot", Kind: "general"})
		if err != nil {
			t.Fatalf("Create[%d]: %v", i, err)
		}
		ids[i] = id
	}

	var wg sync.WaitGroup
	claimed := make(chan string, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, ok, err := s.ClaimWorkSlot(ctx, max)
			if err != nil {
				t.Errorf("ClaimWorkSlot: %v", err)
				return
			}
			if ok {
				claimed <- id
			}
		}()
	}
	wg.Wait()
	close(claimed)

	var got []string
	for id := range claimed {
		got = append(got, id)
	}
	if len(got) != want {
		t.Fatalf("claimed %d slots, want exactly %d (cap %d minus %d already working)", len(got), want, max, baseline)
	}

	// ClaimWorkSlot takes the oldest idle mission GLOBALLY, so in a
	// shared database an older foreign idle row may be claimed instead
	// of this test's fixtures: the invariant under test is the global
	// cap, not which rows filled the slots.
	var working int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM missions WHERE status = 'working'`).Scan(&working); err != nil {
		t.Fatalf("count working: %v", err)
	}
	if working != baseline+want {
		t.Fatalf("working count = %d, want %d (baseline %d + claimed %d)", working, baseline+want, baseline, want)
	}
}

func TestReconcileTerminalIdempotency(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "reconcile", Kind: "general"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	donePayload := map[string]any{"units": 3}
	if err := s.ApplyTransition(ctx, id, Transition{
		Next:   StepState{Phase: PhaseDone, Status: StatusDone},
		Events: []EventDraft{{Kind: "mission.done", Payload: donePayload}},
	}); err != nil {
		t.Fatalf("ApplyTransition done: %v", err)
	}

	// A duplicate of the SAME outcome is a no-op: no mission.reconciled
	// event, mission stays done.
	if err := s.ReconcileTerminal(ctx, id, PhaseDone, donePayload); err != nil {
		t.Fatalf("ReconcileTerminal duplicate: %v", err)
	}
	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, e := range events {
		if e.Kind == "mission.reconciled" {
			t.Fatal("duplicate terminal wrote a mission.reconciled event")
		}
	}

	// A CONTRADICTORY second terminal (failed after done) writes
	// mission.reconciled naming the canonical (first-by-seq) outcome,
	// and does not flip the mission's persisted phase.
	if err := s.ReconcileTerminal(ctx, id, PhaseFailed, map[string]any{"reason": "race"}); err != nil {
		t.Fatalf("ReconcileTerminal contradiction: %v", err)
	}
	events, err = s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	reconciled := false
	for _, e := range events {
		if e.Kind == "mission.reconciled" {
			reconciled = true
		}
	}
	if !reconciled {
		t.Fatal("contradictory terminal did not write mission.reconciled")
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.Phase != PhaseDone {
		t.Fatalf("phase after reconciliation = %q, want done (canonical) unchanged", m.Phase)
	}
}

func TestRecoverWorking(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	idleID, err := s.Create(ctx, Mission{Goal: marker + "recover-idle", Kind: "general"})
	if err != nil {
		t.Fatalf("Create idle: %v", err)
	}
	workingID, err := s.Create(ctx, Mission{Goal: marker + "recover-working", Kind: "general"})
	if err != nil {
		t.Fatalf("Create working: %v", err)
	}
	if err := s.ApplyTransition(ctx, workingID, Transition{Next: StepState{Phase: PhaseGenerate, Status: StatusWorking}}); err != nil {
		t.Fatalf("ApplyTransition working: %v", err)
	}

	working, err := s.RecoverWorking(ctx)
	if err != nil {
		t.Fatalf("RecoverWorking: %v", err)
	}
	byID := map[string]bool{}
	for _, m := range working {
		byID[m.ID] = true
	}
	if !byID[workingID] {
		t.Fatal("RecoverWorking did not return the working mission")
	}
	if byID[idleID] {
		t.Fatal("RecoverWorking incorrectly returned an idle mission")
	}
}

func TestRecoverStaleWorking(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	freshID, err := s.Create(ctx, Mission{Goal: marker + "stale-fresh", Kind: "general"})
	if err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	if err := s.ApplyTransition(ctx, freshID, Transition{Next: StepState{Phase: PhaseGenerate, Status: StatusWorking}}); err != nil {
		t.Fatalf("ApplyTransition fresh: %v", err)
	}

	staleID, err := s.Create(ctx, Mission{Goal: marker + "stale-old", Kind: "general"})
	if err != nil {
		t.Fatalf("Create stale: %v", err)
	}
	if err := s.ApplyTransition(ctx, staleID, Transition{Next: StepState{Phase: PhaseGenerate, Status: StatusWorking}}); err != nil {
		t.Fatalf("ApplyTransition stale: %v", err)
	}
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("db.Get: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET updated_at = now() - interval '1 hour' WHERE id = $1`, staleID); err != nil {
		t.Fatalf("backdate updated_at: %v", err)
	}

	// A delegated run: updated_at is old (the CLI has run for an hour)
	// but the poll loop appended executor.progress a moment ago (#497).
	delegatedID := workingBackdated(t, s, marker+"stale-delegated-live")
	if err := s.AppendEvent(ctx, delegatedID, "executor.progress", map[string]any{"turns": 80}); err != nil {
		t.Fatalf("AppendEvent live progress: %v", err)
	}

	// A delegated run whose last progress is itself older than the
	// staleness bound: the CLI stopped without exiting, genuinely stale.
	delegatedStaleID := workingBackdated(t, s, marker+"stale-delegated-dead")
	if err := s.AppendEvent(ctx, delegatedStaleID, "executor.progress", map[string]any{"turns": 3}); err != nil {
		t.Fatalf("AppendEvent old progress: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE mission_events SET created_at = now() - interval '1 hour' WHERE mission_id = $1`, delegatedStaleID); err != nil {
		t.Fatalf("backdate progress event: %v", err)
	}

	stale, err := s.RecoverStaleWorking(ctx, 15*time.Minute)
	if err != nil {
		t.Fatalf("RecoverStaleWorking: %v", err)
	}
	byID := map[string]bool{}
	for _, m := range stale {
		byID[m.ID] = true
	}
	if !byID[staleID] {
		t.Fatal("RecoverStaleWorking did not return the stale mission")
	}
	if byID[freshID] {
		t.Fatal("RecoverStaleWorking incorrectly returned a fresh working mission")
	}
	if byID[delegatedID] {
		t.Fatal("RecoverStaleWorking returned a delegated run with recent executor.progress")
	}
	if !byID[delegatedStaleID] {
		t.Fatal("RecoverStaleWorking did not return a delegated run whose progress went stale")
	}
}

// workingBackdated creates a working mission whose updated_at is an hour
// old, the shape of a long delegated run's row.
func workingBackdated(t *testing.T, s *Store, goal string) string {
	t.Helper()
	ctx := t.Context()
	id, err := s.Create(ctx, Mission{Goal: goal, Kind: "coding"})
	if err != nil {
		t.Fatalf("Create %s: %v", goal, err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{Next: StepState{Phase: PhaseGenerate, Status: StatusWorking}}); err != nil {
		t.Fatalf("ApplyTransition %s: %v", goal, err)
	}
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("db.Get: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET updated_at = now() - interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatalf("backdate %s: %v", goal, err)
	}
	return id
}

func TestBackoffPausedAndCountBackoffPauses(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	backoffID, err := s.Create(ctx, Mission{Goal: marker + "backoff-paused", Kind: "general"})
	if err != nil {
		t.Fatalf("Create backoff: %v", err)
	}
	pauseBackoff := func() {
		if err := s.ApplyTransition(ctx, backoffID, Transition{
			Next:   StepState{Phase: PhaseGenerate, Status: StatusPaused, PauseReason: PauseBackoff},
			Events: []EventDraft{{Kind: "mission.paused", Payload: map[string]any{"reason": string(PauseBackoff)}}},
		}); err != nil {
			t.Fatalf("ApplyTransition pause backoff: %v", err)
		}
	}
	pauseBackoff()

	infraID, err := s.Create(ctx, Mission{Goal: marker + "infra-paused", Kind: "general"})
	if err != nil {
		t.Fatalf("Create infra: %v", err)
	}
	if err := s.ApplyTransition(ctx, infraID, Transition{
		Next:   StepState{Phase: PhaseGenerate, Status: StatusPaused, PauseReason: PauseInfra},
		Events: []EventDraft{{Kind: "mission.paused", Payload: map[string]any{"reason": string(PauseInfra)}}},
	}); err != nil {
		t.Fatalf("ApplyTransition pause infra: %v", err)
	}

	paused, err := s.BackoffPaused(ctx)
	if err != nil {
		t.Fatalf("BackoffPaused: %v", err)
	}
	byID := map[string]bool{}
	for _, m := range paused {
		byID[m.ID] = true
	}
	if !byID[backoffID] {
		t.Fatal("BackoffPaused did not return the backoff-paused mission")
	}
	if byID[infraID] {
		t.Fatal("BackoffPaused incorrectly returned an infra-paused mission")
	}

	n, err := s.CountBackoffPauses(ctx, backoffID)
	if err != nil {
		t.Fatalf("CountBackoffPauses: %v", err)
	}
	if n != 1 {
		t.Fatalf("CountBackoffPauses after 1 pause = %d, want 1", n)
	}

	// Resume, then pause for backoff again: count must accumulate.
	if err := s.ApplyTransition(ctx, backoffID, Transition{Next: StepState{Phase: PhaseGenerate, Status: StatusIdle}}); err != nil {
		t.Fatalf("ApplyTransition resume: %v", err)
	}
	pauseBackoff()

	n, err = s.CountBackoffPauses(ctx, backoffID)
	if err != nil {
		t.Fatalf("CountBackoffPauses after 2nd pause: %v", err)
	}
	if n != 2 {
		t.Fatalf("CountBackoffPauses after 2 pauses = %d, want 2", n)
	}

	n, err = s.CountBackoffPauses(ctx, infraID)
	if err != nil {
		t.Fatalf("CountBackoffPauses infra: %v", err)
	}
	if n != 0 {
		t.Fatalf("CountBackoffPauses for an infra-only mission = %d, want 0", n)
	}
}

func TestSpendExcludesUnbilledRows(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	id, err := s.Create(ctx, Mission{Goal: marker + "spend-unbilled", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	for _, row := range []struct {
		cost     float64
		unbilled bool
	}{{0.05, false}, {0.25, true}} {
		if _, err := db.Exec(ctx, `INSERT INTO cost_ledger
			(provider, model, route, latency_ms, status, cost, currency, purpose, mission_id, unbilled)
			VALUES ('itest-provider', 'itest-model', 'itest', 1, 'ok', $1, 'USD', 'executor', $2, $3)`,
			row.cost, id, row.unbilled); err != nil {
			t.Fatalf("insert ledger row: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM cost_ledger WHERE mission_id = $1`, id)
	})

	spend, err := s.Spend(ctx, id)
	if err != nil {
		t.Fatalf("Spend: %v", err)
	}
	if got := spend.ByCurrency["USD"]; got != 0.05 {
		t.Fatalf("Spend USD = %v, want 0.05 (unbilled row must be excluded from the brake)", got)
	}
}

// TestPendingPermissionsAndResolveTimeout covers issue #445's store
// layer: PendingPermissions lists a mission's parked request with its
// parked_at and per-mission override intact, and
// ResolvePendingPermissionTimeout clears the park and appends the SAME
// mission.permission_answered event kind an operator's manual deny
// produces, with reason=timed_out distinguishing the two.
func TestPendingPermissionsAndResolveTimeout(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	timeout := 30
	id, err := s.Create(ctx, Mission{Goal: marker + "permission-timeout-store", Kind: "general", Route: "default", PermissionTimeoutSeconds: &timeout})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{Next: StepState{Phase: PhaseGenerate, Status: StatusWorking}}); err != nil {
		t.Fatalf("ApplyTransition working: %v", err)
	}
	if err := s.SetPendingPermission(ctx, id, "perm-timeout-1", "shell", `{"command":"rm -rf x"}`, "destructive", "deletes files"); err != nil {
		t.Fatalf("SetPendingPermission: %v", err)
	}

	pending, err := s.PendingPermissions(ctx)
	if err != nil {
		t.Fatalf("PendingPermissions: %v", err)
	}
	var found *ParkedPermission
	for i := range pending {
		if pending[i].MissionID == id {
			found = &pending[i]
		}
	}
	if found == nil {
		t.Fatal("PendingPermissions did not return the parked mission")
	}
	if found.PermissionID != "perm-timeout-1" || found.Tool != "shell" {
		t.Fatalf("ParkedPermission = %+v, want id/tool from SetPendingPermission", found)
	}
	if found.TimeoutOverride == nil || *found.TimeoutOverride != timeout {
		t.Fatalf("TimeoutOverride = %v, want %d", found.TimeoutOverride, timeout)
	}
	if found.ParkedAt.IsZero() {
		t.Fatal("ParkedAt was not recorded")
	}

	if err := s.ResolvePendingPermissionTimeout(ctx, id, "shell"); err != nil {
		t.Fatalf("ResolvePendingPermissionTimeout: %v", err)
	}

	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.PendingPermission != "" {
		t.Fatalf("Get after timeout resolve = %+v, want pending_permission cleared", m)
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	last := events[len(events)-1]
	if last.Kind != "mission.permission_answered" {
		t.Fatalf("last event kind = %q, want mission.permission_answered", last.Kind)
	}
	var payload struct {
		Tool, Decision, Reason string
	}
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}
	if payload.Decision != "deny" || payload.Reason != "timed_out" {
		t.Fatalf("event payload = %+v, want decision=deny reason=timed_out", payload)
	}

	pending, err = s.PendingPermissions(ctx)
	if err != nil {
		t.Fatalf("PendingPermissions after resolve: %v", err)
	}
	for _, p := range pending {
		if p.MissionID == id {
			t.Fatal("resolved mission still reported as pending")
		}
	}
}

// TestAskUserParkAnswerRoundTripCarriesQAIntoNextTurn covers D-088's
// full path against a real store: ask_user's park (SetPendingInput),
// the operator's answer (AnswerAskUser via AppendProgress +
// AnswerPendingInput), and the durable channel that carries the Q+A
// into the NEXT turn of the same phase: WorkPacket.Render reads
// m.Progress verbatim, so no separate wiring is needed once the answer
// lands as a progress note.
func TestAskUserParkAnswerRoundTripCarriesQAIntoNextTurn(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Create(ctx, Mission{Goal: marker + "ask-user-round-trip", Kind: "general", Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{Next: StepState{Phase: PhaseGenerate, Status: StatusWorking}}); err != nil {
		t.Fatalf("ApplyTransition working: %v", err)
	}

	input := PendingInput{
		Question: "which runtime should this target?", Kind: "mcq",
		Options: []string{"node", "python"}, ProposedDefault: "node", Phase: PhaseGenerate,
	}
	if err := s.SetPendingInput(ctx, id, input); err != nil {
		t.Fatalf("SetPendingInput: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{Next: StepState{Phase: PhaseGenerate, Status: StatusWaitingForInput}}); err != nil {
		t.Fatalf("ApplyTransition waiting_for_input: %v", err)
	}

	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after park: %v", err)
	}
	if m.PendingInput == nil || m.PendingInput.Question != input.Question {
		t.Fatalf("PendingInput after park = %+v, want %+v", m.PendingInput, input)
	}
	if m.AsksUsed != 1 {
		t.Fatalf("AsksUsed after park = %d, want 1", m.AsksUsed)
	}

	// The API's answer handler validates the mcq option, appends the Q+A
	// progress note, then calls Store.AnswerPendingInput: mirrored here
	// directly against the store (Driver.AnswerAskUser wraps the same two
	// calls).
	if err := s.AppendProgress(ctx, id, `Operator answered your question "which runtime should this target?": python`); err != nil {
		t.Fatalf("AppendProgress: %v", err)
	}
	if err := s.AnswerPendingInput(ctx, id, "mission.input_answered", map[string]any{"answer": "python"}); err != nil {
		t.Fatalf("AnswerPendingInput: %v", err)
	}

	m, err = s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after answer: %v", err)
	}
	if m.PendingInput != nil {
		t.Fatalf("PendingInput after answer = %+v, want nil", m.PendingInput)
	}
	if m.Status != StatusIdle {
		t.Fatalf("Status after answer = %s, want idle", m.Status)
	}

	// The next worker turn's packet renders m.Progress verbatim
	// (WorkPacket.Render, packet.go): this is the "next-turn-prompt
	// carries Q+A round trip" the design promises, without any packet
	// changes: the Q+A rides the same durable channel a steering note
	// already uses.
	packet := WorkPacket{Progress: m.Progress}
	_, user := packet.Render()
	if !strings.Contains(user, input.Question) || !strings.Contains(user, "python") {
		t.Fatalf("next worker packet does not carry the Q+A verbatim: %s", user)
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawRequested, sawAnswered bool
	for _, e := range events {
		switch e.Kind {
		case "mission.input_requested":
			sawRequested = true
		case "mission.input_answered":
			sawAnswered = true
			var payload struct{ Answer string }
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("unmarshal input_answered payload: %v", err)
			}
			if payload.Answer != "python" {
				t.Fatalf("input_answered payload = %+v, want answer=python", payload)
			}
		}
	}
	if !sawRequested || !sawAnswered {
		t.Fatalf("events = %+v, want both mission.input_requested and mission.input_answered", events)
	}
}

// fakePermissionTimeoutDriverIT captures Drive calls from
// sweepPermissionTimeouts against a real store, synchronized so the
// test can wait for the sweep's background goroutine before asserting.
type fakePermissionTimeoutDriverIT struct {
	wg    sync.WaitGroup
	mu    sync.Mutex
	drove []string
}

func (f *fakePermissionTimeoutDriverIT) Drive(ctx context.Context, id string) error {
	defer f.wg.Done()
	f.mu.Lock()
	f.drove = append(f.drove, id)
	f.mu.Unlock()
	return nil
}

// TestSweepPermissionTimeoutsAutoDeniesAndResumes is the end-to-end
// path for issue #445 against a real store: a mission parked on
// permission with a very short (1s) effective timeout gets auto-denied
// once the sweep runs past that interval, the mission_event carries
// reason=timed_out, and the mission is handed back to Drive to resume.
func TestSweepPermissionTimeoutsAutoDeniesAndResumes(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	timeout := 1
	id, err := s.Create(ctx, Mission{Goal: marker + "permission-timeout-sweep", Kind: "general", Route: "default", PermissionTimeoutSeconds: &timeout})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ApplyTransition(ctx, id, Transition{Next: StepState{Phase: PhaseGenerate, Status: StatusWorking}}); err != nil {
		t.Fatalf("ApplyTransition working: %v", err)
	}
	if err := s.SetPendingPermission(ctx, id, "perm-sweep-1", "shell", `{"command":"rm -rf x"}`, "destructive", "deletes files"); err != nil {
		t.Fatalf("SetPendingPermission: %v", err)
	}

	time.Sleep(1200 * time.Millisecond) // past the 1s effective timeout

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	driver := &fakePermissionTimeoutDriverIT{}
	driver.wg.Add(1)
	sweepPermissionTimeouts(ctx, driver, s, func(context.Context) int { return 0 }, nil, nil, log)
	driver.wg.Wait()

	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.PendingPermission != "" {
		t.Fatalf("Get after sweep = %+v, want pending_permission auto-denied", m)
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	last := events[len(events)-1]
	if last.Kind != "mission.permission_answered" {
		t.Fatalf("last event kind = %q, want mission.permission_answered", last.Kind)
	}
	var payload struct {
		Decision, Reason string
	}
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}
	if payload.Decision != "deny" || payload.Reason != "timed_out" {
		t.Fatalf("event payload = %+v, want decision=deny reason=timed_out", payload)
	}

	driver.mu.Lock()
	drove := driver.drove
	driver.mu.Unlock()
	if len(drove) != 1 || drove[0] != id {
		t.Fatalf("Drive calls = %v, want exactly [%s] (mission resumed)", drove, id)
	}
}
