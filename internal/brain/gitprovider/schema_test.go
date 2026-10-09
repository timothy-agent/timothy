package gitprovider

import (
	"regexp"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/migrations"
)

// TestSchemaAllowsEveryKind guards issue #1104: a registered provider
// kind must pass the kind CHECK on both connectors and destinations.
func TestSchemaAllowsEveryKind(t *testing.T) {
	raw, err := migrations.FS.ReadFile("0001_init.sql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	schema := string(raw)
	for _, table := range []string{"connectors", "destinations"} {
		re := regexp.MustCompile(`(?s)CREATE TABLE (?:IF NOT EXISTS )?` + table + ` \(.*?kind\s+text NOT NULL CHECK \(kind IN \(([^)]*)\)\)`)
		m := re.FindStringSubmatch(schema)
		if m == nil {
			t.Fatalf("%s: kind CHECK not found", table)
		}
		for _, k := range Kinds() {
			if !strings.Contains(m[1], "'"+string(k)+"'") {
				t.Errorf("%s kind CHECK (%s) is missing %q", table, m[1], k)
			}
		}
	}
}
