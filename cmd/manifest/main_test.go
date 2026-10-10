package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const routesFixture = `{"routes":[{"path":"/missions","label":"Missions"}],"settings_areas":[` +
	`{"key":"connectors","label":"Connectors","path":"/settings/connectors"},` +
	`{"key":"channels","label":"Channels","path":"/settings/channels"},` +
	`{"key":"destinations","label":"Destinations","path":"/settings/destinations"},` +
	`{"key":"features","label":"Features","path":"/settings/features"}],"labels":["Missions"]}`

func TestRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	routes := filepath.Join(dir, "routes.json")
	if err := os.WriteFile(routes, []byte(routesFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	n, err := run(out, routes, "../../skills", "test")
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no pages written")
	}
	toolsMD, err := os.ReadFile(filepath.Join(out, "tools.md")) //nolint:gosec // reads back this test's own t.TempDir output
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## load_skill", "## shell", "## search_web"} {
		if !strings.Contains(string(toolsMD), want) {
			t.Errorf("tools.md missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRunErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, out, routes, want string
	}{
		{name: "missing out", routes: bad, want: "-out is required"},
		{name: "missing routes file", out: dir, routes: filepath.Join(dir, "none.json"), want: "read routes"},
		{name: "bad routes json", out: dir, routes: bad, want: "parse routes"},
	}
	for _, tc := range tests {
		if _, err := run(tc.out, tc.routes, "../../skills", "test"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}
