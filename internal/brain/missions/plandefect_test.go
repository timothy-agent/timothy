package missions

import "testing"

// TestNamesPlanDefect pins the classifier on the real BLOCKED notes
// from the five-harness eval (issue #718) and on questions that must
// still reach the operator.
func TestNamesPlanDefect(t *testing.T) {
	plan := Plan{Units: []PlanUnit{{Title: "u", Artifacts: []string{"internal/core/permute.go"}}}}
	defects := []string{
		"The benchmark acceptance check depends on `Permute`, so this unit cannot pass in isolation without adding `internal/core/permute.go`.",
		"The verify_cmd cannot pass as written: its first stage 'gofmt -l <files> | grep -q '^$'' exits 1 whenever gofmt output is empty.",
		"Slice 1 is fully implemented; the only remaining failure is the check_cmd's first stage.",
		"Criterion 3 asks for a benchmark the plan places in a later unit.",
	}
	for _, q := range defects {
		if !namesPlanDefect(q, plan) {
			t.Errorf("namesPlanDefect missed a plan diagnosis: %q", q)
		}
	}
	operator := []string{
		"Which OAuth scopes should the connector request?",
		"The repository requires a GitHub token I do not have.",
		"Should aliases be case sensitive? The design document does not say.",
		"Should aliases be case sensitive? The plan does not say.",
	}
	for _, q := range operator {
		if namesPlanDefect(q, plan) {
			t.Errorf("namesPlanDefect took an operator question for a plan defect: %q", q)
		}
	}
}

// TestRestorePassedUnitsKeepsHarnessEvidence (issue #718): a replan
// that renames a unit but produces the same artifacts keeps its
// harness verdict and loses reviewer approval; an unverified unit and a
// unit with new artifacts start clean; approval survives only the exact
// title plus check_cmd match.
func TestRestorePassedUnitsKeepsHarnessEvidence(t *testing.T) {
	prior := Plan{Units: []PlanUnit{
		{Title: "Implement Base62 encoding and decoding with table tests", Artifacts: []string{"internal/core/base62.go", "internal/core/base62_test.go"}, HarnessPassed: true, Passes: true},
		{Title: "Keyed permutation", CheckCmd: "go test ./internal/core/ -run TestPermute", Artifacts: []string{"internal/core/permute.go", "internal/core/permute_test.go"}, HarnessPassed: true, Passes: true},
		{Title: "Validation", Artifacts: []string{"internal/core/validate.go"}},
	}}
	next := Plan{Units: []PlanUnit{
		{Title: "Implement Base62 core and table tests", Artifacts: []string{"internal/core/base62_test.go", "internal/core/base62.go"}},
		{Title: "Keyed permutation", CheckCmd: "go test ./internal/core/ -run TestPermute", Artifacts: []string{"internal/core/permute.go", "internal/core/permute_test.go"}},
		{Title: "Validation", Artifacts: []string{"internal/core/validate.go"}},
		{Title: "Redirect", Artifacts: []string{"internal/core/redirect.go"}},
	}}
	restorePassedUnits(&next, prior)
	if !next.Units[0].HarnessPassed || next.Units[0].Passes {
		t.Fatalf("renamed base62 unit = %+v, want harness_passed carried by artifact match, passes dropped for a fresh review", next.Units[0])
	}
	if !next.Units[1].HarnessPassed || !next.Units[1].Passes {
		t.Fatalf("permutation unit = %+v, want both flags carried by title+check_cmd match", next.Units[1])
	}
	if next.Units[2].HarnessPassed || next.Units[3].HarnessPassed {
		t.Fatalf("unverified and new units must start clean: %+v %+v", next.Units[2], next.Units[3])
	}
}
