// Command manifest writes the capability pages for this build (see
// internal/brain/manifest) from compiled-in data, the skill packs and
// the web routes export. It needs no database or network.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/SumonMSelim/timothy/internal/brain/channels"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/destinations"
	"github.com/SumonMSelim/timothy/internal/brain/manifest"
	"github.com/SumonMSelim/timothy/internal/brain/settings"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
	"github.com/SumonMSelim/timothy/internal/platform/service"
)

func main() {
	out := flag.String("out", "", "output directory (required)")
	routesPath := flag.String("routes", "web/routes.generated.json", "web routes export")
	skillsDir := flag.String("skills", "skills", "skills directory")
	version := flag.String("version", defaultVersion(), "version to record")
	flag.Parse()

	n, err := run(*out, *routesPath, *skillsDir, *version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Printf("ok: %d pages\n", n)
}

func defaultVersion() string {
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	if service.Version != "" {
		return service.Version
	}
	return "dev"
}

func run(out, routesPath, skillsDir, version string) (int, error) {
	if out == "" {
		return 0, fmt.Errorf("-out is required")
	}
	raw, err := os.ReadFile(routesPath) //nolint:gosec // operator-supplied build input path
	if err != nil {
		return 0, fmt.Errorf("read routes: %w", err)
	}
	var routes manifest.Routes
	if err := json.Unmarshal(raw, &routes); err != nil {
		return 0, fmt.Errorf("parse routes %s: %w", routesPath, err)
	}
	packs, err := skills.Load(skillsDir)
	if err != nil {
		return 0, err
	}
	all := append(builtin.Catalog(), skills.LoadSkillTool(packs, nil))
	exempt := map[string]bool{}
	for _, name := range tools.ExemptNames() {
		exempt[name] = true
	}
	pages, err := manifest.Generate(manifest.Input{
		Version:          version,
		Tools:            all,
		Exempt:           exempt,
		ConnectorKinds:   connectors.Kinds(),
		ChannelKinds:     channels.Kinds(),
		DestinationKinds: destinations.Kinds(),
		Switches:         settings.Switches(),
		Skills:           packs,
		Routes:           routes,
	})
	if err != nil {
		return 0, err
	}
	if err := manifest.Write(out, pages, version); err != nil {
		return 0, err
	}
	return len(pages), nil
}
