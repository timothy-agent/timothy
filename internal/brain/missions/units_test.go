package missions

import (
	"testing"
)

func TestAllPassed(t *testing.T) {
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
		{"harness-passed but not approved is not passed", []PlanUnit{{HarnessPassed: true}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := allPassed(c.units)
			if got != c.want {
				t.Fatalf("allPassed(%+v) = %v, want %v", c.units, got, c.want)
			}
		})
	}
}

// TestStepDoesNotAliasCallerUnits guards the aliasing bug D-094 inherits
// from the old markUnitPassed: Step receives Units sharing its backing
// array with the caller's Mission row, so applyVerification and
// stepReviewApprove must copy before writing. If they wrote through, a
// caller inspecting m.Plan after Step (Advance's own m) would observe a
// unit as passed before ApplyTransition ever persisted it.
func TestStepDoesNotAliasCallerUnits(t *testing.T) {
	units := []PlanUnit{{Title: "a", Passes: true, HarnessPassed: true}, {Title: "b", HarnessPassed: true}, {Title: "c"}}
	m := Mission{ID: "m1", Plan: Plan{Units: units}}

	got := Step(
		StepState{Phase: PhaseProve, Status: StatusWorking, Units: m.Plan.Units},
		StepInput{Input: InputReviewApprove, Verified: []UnitVerification{{Unit: 2, Passed: false, Check: "artifacts", Excerpt: "missing"}}},
		DefaultConfig,
	)

	if m.Plan.Units[1].Passes || m.Plan.Units[2].VerifyExcerpt != "" {
		t.Fatalf("Step mutated the caller's Mission value through a shared slice backing array: %+v", m.Plan.Units)
	}
	if !got.Next.Units[1].Passes {
		t.Fatal("approve did not flip the harness-passed unit in the returned state")
	}
	if got.Next.Units[2].Passes || got.Next.Units[2].VerifyExcerpt != "missing" {
		t.Fatalf("unit c = %+v, want unverified with the excerpt recorded", got.Next.Units[2])
	}
}
