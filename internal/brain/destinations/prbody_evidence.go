package destinations

import (
	"fmt"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// prDependencyEvidence renders the harness lockfile evidence (D-140) as
// a before/after table of test and advisory counts, "" without one.
func prDependencyEvidence(m missions.Mission) string {
	if m.EnvFacts == nil || m.EnvFacts.Lockfile == nil {
		return ""
	}
	e := m.EnvFacts.Lockfile
	var b strings.Builder
	b.WriteString("## Dependency evidence\n\n")
	fmt.Fprintf(&b, "Lockfiles changed: %s. Measured by the harness, not reported by the model.\n\n", strings.Join(e.Lockfiles, ", "))
	b.WriteString("| | Before | After |\n|---|---|---|\n")
	label := "Tests"
	if e.TestCmd != "" {
		label = "Tests (`" + strings.ReplaceAll(e.TestCmd, "|", `\|`) + "`)"
	}
	fmt.Fprintf(&b, "| %s | %s | %s |\n", label, e.BeforeTests(), e.AfterTests())
	fmt.Fprintf(&b, "| Known advisories (osv-scanner) | %s | %s |\n", e.BeforeVulns(), e.AfterVulns())
	if len(e.Advisories) > 0 {
		fmt.Fprintf(&b, "\nRemaining advisories: %s\n", strings.Join(e.Advisories, ", "))
	}
	return b.String() + "\n"
}
