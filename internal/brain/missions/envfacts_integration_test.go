//go:build integration

package missions

import (
	"reflect"
	"testing"
)

// TestSetEnvFactsPersists: env_facts is NULL until written, then
// round-trips through Get.
func TestSetEnvFactsPersists(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	id, err := s.Create(ctx, Mission{Goal: marker + "env facts", Kind: KindCoding, Route: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m, err := s.Get(ctx, id); err != nil || m.EnvFacts != nil {
		t.Fatalf("Get after Create = %+v, %v, want no facts", m.EnvFacts, err)
	}
	want := EnvFacts{
		BaseBranch:   "main",
		Destinations: []DestinationFact{{Kind: "github", Mode: "push_pr"}},
		Manifests:    []string{"composer.json", "package.json"},
		Tools:        []ToolFact{{Name: "docker"}, {Name: "git", Version: "git version 2.39.5"}},
		Gaps:         []string{"gap"},
	}
	if err := s.SetEnvFacts(ctx, id, want); err != nil {
		t.Fatalf("SetEnvFacts: %v", err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.EnvFacts == nil || !reflect.DeepEqual(*m.EnvFacts, want) {
		t.Fatalf("EnvFacts = %+v, want %+v", m.EnvFacts, want)
	}
}

// TestProvisionStoresEnvFacts: provisioning a Laravel + Vite clone
// stores its base branch, both manifests and the probed tools.
func TestProvisionStoresEnvFacts(t *testing.T) {
	m, _ := provisionWithRepo(t, map[string]string{"composer.json": `{"require":{"php":"^8.2"}}`, "package.json": "{}"})
	if m.EnvFacts == nil {
		t.Fatal("no env facts stored after provisioning")
	}
	if m.EnvFacts.BaseBranch != "main" || !reflect.DeepEqual(m.EnvFacts.Manifests, []string{"composer.json", "package.json"}) {
		t.Fatalf("EnvFacts = %+v, want base branch main and both manifests", m.EnvFacts)
	}
	if len(m.EnvFacts.Tools) != len(probedTools) {
		t.Fatalf("Tools = %+v, want one entry per probed tool", m.EnvFacts.Tools)
	}
}
