package gitprovider

import (
	"go/build"
	"strings"
	"testing"
)

// The package is a leaf by contract (D-100): connectors, destinations
// and missions all depend on it, so an import back the other way would
// be a cycle the moment a call site uses it. Checked here rather than
// left to a build failure, because the direction must hold for indirect
// imports too, and the failure message should say why.
func TestNoUpwardImports(t *testing.T) {
	t.Parallel()
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("ImportDir: %v", err)
	}
	forbidden := []string{
		"internal/brain/connectors",
		"internal/brain/destinations",
		"internal/brain/missions",
	}
	for _, imp := range append(pkg.Imports, pkg.TestImports...) {
		for _, bad := range forbidden {
			if strings.Contains(imp, bad) {
				t.Fatalf("gitprovider must stay a leaf package, found import of %q", imp)
			}
		}
	}
}
