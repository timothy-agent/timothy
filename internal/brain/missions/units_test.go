package missions

import (
	"context"
	"log/slog"
	"testing"
)

func TestIsLastUnit(t *testing.T) {
	cases := []struct {
		name  string
		units []PlanUnit
		want  bool
	}{
		{"empty plan", nil, true},
		{"all passed", []PlanUnit{{Passes: true}, {Passes: true}, {Passes: true}}, true},
		{"first unit still unverified", []PlanUnit{{Passes: false}, {Passes: false}, {Passes: false}}, false},
		{
			// The regression this guards: the middle unit passing must
			// NOT be mistaken for the plan being done — only unit 2
			// (the actual last unit) passing completes it.
			"middle unit passed, last unit not yet verified",
			[]PlanUnit{{Passes: true}, {Passes: true}, {Passes: false}},
			false,
		},
		{"only last unit unverified with one unit total", []PlanUnit{{Passes: false}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isLastUnit(Plan{Units: c.units})
			if got != c.want {
				t.Fatalf("isLastUnit(%+v) = %v, want %v", c.units, got, c.want)
			}
		})
	}
}

// TestMarkUnitPassedDoesNotAliasCaller guards the actual production
// bug: markUnitPassed must not mutate the caller's Plan.Units backing
// array. If it did, a caller that inspects m.Plan after calling it
// (e.g. Advance's toStepState(m)) would observe a unit as passed one
// full round before the harness ever verified it.
func TestMarkUnitPassedDoesNotAliasCaller(t *testing.T) {
	store := newFakeStore()
	d := NewDriver(store, nil, nil, nil, nil, nil, nil, nil, slog.Default())

	units := []PlanUnit{{Title: "a", Passes: true}, {Title: "b", Passes: false}, {Title: "c", Passes: false}}
	m := Mission{ID: "m1", Plan: Plan{Units: units}}
	store.put(m.ID, m)

	if err := d.markUnitPassed(context.Background(), m, 1); err != nil {
		t.Fatalf("markUnitPassed: %v", err)
	}

	if m.Plan.Units[1].Passes {
		t.Fatal("markUnitPassed mutated the caller's Mission value through a shared slice backing array")
	}

	persisted := store.missions[m.ID]
	if !persisted.Plan.Units[1].Passes {
		t.Fatal("markUnitPassed did not persist the update to the store")
	}
	if persisted.Plan.Units[2].Passes {
		t.Fatal("markUnitPassed must not affect unrelated units")
	}
}
