//go:build integration

package missions

import (
	"errors"
	"strings"
	"testing"
)

// slugMarker adapts the package's own marker (which has a trailing
// space — fine for a mission goal's LIKE-prefix sweep, not fine for a
// schedule name's namePattern slug) into something CreateSchedule's
// validation actually accepts.
var slugMarker = strings.TrimSpace(marker)

func TestScheduleCRUD(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	name := slugMarker + "-daily-brief"

	id, err := store.CreateSchedule(ctx, Schedule{
		Name: name, Cron: "0 7 * * *",
		MissionTemplate: MissionTemplate{Goal: marker + "check topics", Kind: "general"},
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	sc, err := store.GetSchedule(ctx, id)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if sc.Name != name || !sc.Enabled {
		t.Fatalf("GetSchedule = %+v", sc)
	}

	// Duplicate name conflicts.
	_, err = store.CreateSchedule(ctx, Schedule{Name: name, Cron: "0 8 * * *"})
	if !errors.Is(err, ErrScheduleNameConflict) {
		t.Fatalf("CreateSchedule duplicate name = %v, want ErrScheduleNameConflict", err)
	}

	// Bad cron rejected.
	_, err = store.CreateSchedule(ctx, Schedule{Name: slugMarker + "-bad-cron", Cron: "nonsense"})
	if !errors.Is(err, ErrBadCron) {
		t.Fatalf("CreateSchedule bad cron = %v, want ErrBadCron", err)
	}

	all, err := store.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	found := false
	for _, row := range all {
		if row.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("ListSchedules did not include the created schedule")
	}

	disabled := false
	if err := store.PatchSchedule(ctx, id, SchedulePatch{Enabled: &disabled}); err != nil {
		t.Fatalf("PatchSchedule: %v", err)
	}
	sc, err = store.GetSchedule(ctx, id)
	if err != nil {
		t.Fatalf("GetSchedule after patch: %v", err)
	}
	if sc.Enabled {
		t.Fatalf("GetSchedule after patch = %+v", sc)
	}

	badCron := "nonsense"
	if err := store.PatchSchedule(ctx, id, SchedulePatch{Cron: &badCron}); !errors.Is(err, ErrBadCron) {
		t.Fatalf("PatchSchedule bad cron = %v, want ErrBadCron", err)
	}

	if err := store.DeleteSchedule(ctx, id); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	if _, err := store.GetSchedule(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSchedule after delete = %v, want ErrNotFound", err)
	}
	if err := store.DeleteSchedule(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteSchedule = %v, want ErrNotFound", err)
	}
}

// TestScheduleDeleteBlockedByReferencingMission confirms the FK
// (missions_schedule_id_fkey, no ON DELETE) really does block deleting
// a schedule some mission still references, surfaced as
// ErrScheduleInUse rather than a raw pg error.
func TestScheduleDeleteBlockedByReferencingMission(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()

	id, err := store.CreateSchedule(ctx, Schedule{
		Name: slugMarker + "-in-use", Cron: "0 7 * * *",
		MissionTemplate: MissionTemplate{Goal: marker + "referenced", Kind: "general"},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	missionID, err := store.Create(ctx, Mission{Goal: marker + "referencing mission", Kind: "general"})
	if err != nil {
		t.Fatalf("Create mission: %v", err)
	}
	db, err := store.db.Get()
	if err != nil {
		t.Fatalf("Get pool: %v", err)
	}
	if _, err := db.Exec(ctx, "UPDATE missions SET schedule_id = $2 WHERE id = $1", missionID, id); err != nil {
		t.Fatalf("attach schedule: %v", err)
	}

	if err := store.DeleteSchedule(ctx, id); !errors.Is(err, ErrScheduleInUse) {
		t.Fatalf("DeleteSchedule with a referencing mission = %v, want ErrScheduleInUse", err)
	}
}

// TestScheduleReferencingDestinationID is the destinations delete
// guard's other half (ActiveMissionReferencesDestination covers
// missions; this covers schedules): an enabled schedule whose
// mission_template still names a destination id must be found and
// named, a disabled schedule's reference must never block, and no
// match must report ok=false cleanly.
func TestScheduleReferencingDestinationID(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()

	destID := "11111111-1111-1111-1111-111111111111"

	name, ok, err := store.ScheduleReferencingDestinationID(ctx, destID)
	if err != nil {
		t.Fatalf("ScheduleReferencingDestinationID with no schedules: %v", err)
	}
	if ok {
		t.Fatalf("ScheduleReferencingDestinationID = %q, true; want false with no schedules referencing it", name)
	}

	scID, err := store.CreateSchedule(ctx, Schedule{
		Name: slugMarker + "-dest-ref", Cron: "0 7 * * *", Enabled: true,
		MissionTemplate: MissionTemplate{Goal: marker + "digest", Kind: "general", DestinationIDs: []string{destID}},
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	name, ok, err = store.ScheduleReferencingDestinationID(ctx, destID)
	if err != nil {
		t.Fatalf("ScheduleReferencingDestinationID: %v", err)
	}
	if !ok || name != slugMarker+"-dest-ref" {
		t.Fatalf("ScheduleReferencingDestinationID = %q, %v; want %q, true", name, ok, slugMarker+"-dest-ref")
	}

	// Disabling the schedule stops it from blocking deletion — same
	// "active only" rule the mission-side check applies.
	disabled := false
	if err := store.PatchSchedule(ctx, scID, SchedulePatch{Enabled: &disabled}); err != nil {
		t.Fatalf("PatchSchedule disable: %v", err)
	}
	_, ok, err = store.ScheduleReferencingDestinationID(ctx, destID)
	if err != nil {
		t.Fatalf("ScheduleReferencingDestinationID after disable: %v", err)
	}
	if ok {
		t.Fatal("ScheduleReferencingDestinationID after disabling the schedule = true, want false")
	}
}
