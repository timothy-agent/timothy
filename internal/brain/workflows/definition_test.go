package workflows

import "testing"

func validDefinition() Definition {
	return Definition{
		Entry: "coder",
		Steps: map[string]Step{
			"coder": {Goal: "write code", Kind: "coding"},
			"qa":    {Goal: "check {{outcome}}", Kind: "general"},
		},
		Edges: []Edge{
			{From: "coder", On: "mission.done", To: "qa", MaxIterations: 1},
			{From: "qa", On: "mission.done", To: endStep, MaxIterations: 1},
		},
	}
}

func TestValidateOK(t *testing.T) {
	d := validDefinition()
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidateBadEntry(t *testing.T) {
	d := validDefinition()
	d.Entry = "nope"
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for unknown entry")
	}
}

func TestValidateEmptyEntry(t *testing.T) {
	d := validDefinition()
	d.Entry = ""
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for empty entry")
	}
}

func TestValidateDanglingEdgeFrom(t *testing.T) {
	d := validDefinition()
	d.Edges = append(d.Edges, Edge{From: "ghost", On: "mission.done", To: "qa", MaxIterations: 1})
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for dangling edge.From")
	}
}

func TestValidateDanglingEdgeTo(t *testing.T) {
	d := validDefinition()
	d.Edges = append(d.Edges, Edge{From: "coder", On: "mission.failed", To: "ghost", MaxIterations: 1})
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for dangling edge.To")
	}
}

func TestValidateEdgeToEndIsAllowed(t *testing.T) {
	d := validDefinition()
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (edge to %q must be allowed)", err, endStep)
	}
}

func TestValidateUnsupportedOn(t *testing.T) {
	d := validDefinition()
	d.Edges[0].On = "pr.approved"
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for unsupported On in slice 1")
	}
}

func TestValidateReservedStepNames(t *testing.T) {
	for _, name := range []string{"end", "done"} {
		d := validDefinition()
		d.Steps[name] = Step{Goal: "x", Kind: "general"}
		if err := d.Validate(); err == nil {
			t.Fatalf("Validate() = nil, want error for reserved step name %q", name)
		}
	}
}

func TestValidateBadStepKind(t *testing.T) {
	d := validDefinition()
	d.Steps["coder"] = Step{Goal: "write code", Kind: "bogus"}
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for unknown step kind")
	}
}

func TestValidateBadStepOnComplete(t *testing.T) {
	d := validDefinition()
	d.Steps["coder"] = Step{Goal: "write code", Kind: "coding", OnComplete: "bogus"}
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for unknown on_complete")
	}
}

func TestValidateAllowsKnownStepOnComplete(t *testing.T) {
	for _, oc := range []string{"", "push", "push_pr"} {
		d := validDefinition()
		step := d.Steps["coder"]
		step.OnComplete = oc
		d.Steps["coder"] = step
		if err := d.Validate(); err != nil {
			t.Fatalf("Validate() with on_complete=%q = %v, want nil", oc, err)
		}
	}
}

func TestValidateClampsMaxIterations(t *testing.T) {
	d := validDefinition()
	d.Edges[0].MaxIterations = 999
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if d.Edges[0].MaxIterations != maxIterations {
		t.Fatalf("MaxIterations = %d, want clamped to %d", d.Edges[0].MaxIterations, maxIterations)
	}
}

func TestValidateClampsZeroOrNegativeMaxIterations(t *testing.T) {
	for _, in := range []int{0, -1, -100} {
		d := validDefinition()
		d.Edges[0].MaxIterations = in
		if err := d.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
		if d.Edges[0].MaxIterations != maxIterations {
			t.Fatalf("MaxIterations(%d) = %d, want clamped to %d", in, d.Edges[0].MaxIterations, maxIterations)
		}
	}
}

func TestValidateKeepsLowerMaxIterations(t *testing.T) {
	d := validDefinition()
	d.Edges[0].MaxIterations = 3
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if d.Edges[0].MaxIterations != 3 {
		t.Fatalf("MaxIterations = %d, want unchanged 3 (below the ceiling)", d.Edges[0].MaxIterations)
	}
}

func TestParseDefinitionRejectsInvalidJSON(t *testing.T) {
	if _, err := ParseDefinition([]byte(`{not json`)); err == nil {
		t.Fatal("ParseDefinition() = nil, want error for invalid JSON")
	}
}

func TestParseDefinitionRejectsInvalidDefinition(t *testing.T) {
	if _, err := ParseDefinition([]byte(`{"entry":"nope","steps":{}}`)); err == nil {
		t.Fatal("ParseDefinition() = nil, want error for invalid definition")
	}
}
