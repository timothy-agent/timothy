package automations

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTemplatesValidate(t *testing.T) {
	t.Parallel()
	tpls := Templates()
	if len(tpls) != 5 {
		t.Fatalf("len(Templates()) = %d, want 5", len(tpls))
	}
	seen := map[string]bool{}
	for _, tpl := range tpls {
		t.Run(tpl.ID, func(t *testing.T) {
			if tpl.ID == "" || seen[tpl.ID] {
				t.Fatalf("id %q empty or duplicate", tpl.ID)
			}
			seen[tpl.ID] = true
			if tpl.Name == "" || tpl.Description == "" || tpl.Icon == "" {
				t.Fatalf("name, description and icon are required: %+v", tpl)
			}
			if err := ValidateAction(tpl.Action); err != nil {
				t.Fatalf("ValidateAction: %v", err)
			}
			a := Automation{
				Name: tpl.Name, AgentID: agentID, Action: tpl.Action,
				Concurrency: ConcurrencySkip, MaxConcurrent: 1, MaxRunsPerHour: 6,
			}
			for _, tr := range tpl.Triggers {
				a.Triggers = append(a.Triggers, Trigger{Kind: tr.Kind, Config: tr.Config, Enabled: true})
			}
			if err := Validate(&a); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			goal := tpl.Action.Mission.Goal
			if strings.ContainsAny(goal+tpl.Description+tpl.Name, "\u2014\u2013") {
				t.Fatalf("template %s contains an em or en dash", tpl.ID)
			}
			if tpl.Requires == nil {
				t.Fatalf("requires must be non-nil so it encodes as []")
			}
		})
	}
}

func TestTemplatesEncodeAsCreateFragments(t *testing.T) {
	t.Parallel()
	for _, tpl := range Templates() {
		raw, err := json.Marshal(tpl)
		if err != nil {
			t.Fatalf("marshal %s: %v", tpl.ID, err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", tpl.ID, err)
		}
		if string(got["requires"]) == "null" {
			t.Fatalf("%s requires encodes as null", tpl.ID)
		}
		var trs []map[string]json.RawMessage
		if err := json.Unmarshal(got["triggers"], &trs); err != nil {
			t.Fatalf("triggers %s: %v", tpl.ID, err)
		}
		for _, tr := range trs {
			for k := range tr {
				if k != "kind" && k != "config" {
					t.Fatalf("%s trigger carries non-request field %q", tpl.ID, k)
				}
			}
		}
		_, hasNotes := got["notes_enabled"]
		_, hasCont := got["continuity"]
		if want := tpl.ID == "coverage-watch"; hasNotes != want || hasCont != want {
			t.Fatalf("%s notes_enabled/continuity present = %v/%v, want %v", tpl.ID, hasNotes, hasCont, want)
		}
	}
}

func TestTemplatesFreshSlices(t *testing.T) {
	t.Parallel()
	a := Templates()
	a[0].Requires[0].Value = "changed"
	a[0].Action.Mission.Goal = "changed"
	b := Templates()
	if b[0].Requires[0].Value == "changed" || b[0].Action.Mission.Goal == "changed" {
		t.Fatal("Templates() shares state between calls")
	}
}

func TestMissing(t *testing.T) {
	t.Parallel()
	gh := Requirement{RequireConnector, "github"}
	email := Requirement{RequireDestination, "email"}
	mail := Requirement{RequireConnector, "mail"}
	digest := Template{Requires: []Requirement{gh, email}}
	triage := Template{Requires: []Requirement{mail}}
	for _, tc := range []struct {
		name        string
		tpl         Template
		conns, dsts []string
		want        []Requirement
	}{
		{"nothing configured", digest, nil, nil, []Requirement{gh, email}},
		{"connector only", digest, []string{"github"}, nil, []Requirement{email}},
		{"destination only", digest, nil, []string{"email"}, []Requirement{gh}},
		{"wrong kinds", digest, []string{"gitlab"}, []string{"webhook"}, []Requirement{gh, email}},
		{"all configured", digest, []string{"imap", "github"}, []string{"email"}, []Requirement{}},
		{"no requirements", Template{Requires: []Requirement{}}, nil, nil, []Requirement{}},
		{"mail via imap", triage, []string{"imap"}, nil, []Requirement{}},
		{"mail via google", triage, []string{"google"}, nil, []Requirement{}},
		{"mail via microsoft", triage, []string{"microsoft"}, nil, []Requirement{}},
		{"mail missing", triage, []string{"github", "caldav"}, []string{"email"}, []Requirement{mail}},
		{"destination kind is not a connector", digest, []string{"email"}, []string{"github"}, []Requirement{gh, email}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Missing(tc.tpl, tc.conns, tc.dsts)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Missing = %+v, want %+v", got, tc.want)
			}
		})
	}
}
