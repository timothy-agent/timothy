// Package manifest renders the capability pages that describe what one
// build of Timothy ships: tools, connector, channel and destination
// kinds, feature switches, skills and web routes. Input comes from
// compiled-in data only, so generation needs no database or network.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/settings"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// ErrInvalid wraps every rejected Input or Page.
var ErrInvalid = errors.New("manifest: invalid input")

// Source tags every generated file so the docs site can tell them apart
// from hand-written pages.
const Source = "manifest"

// Input is everything one build ships, gathered by cmd/manifest.
type Input struct {
	Version          string
	Tools            []*tools.Tool
	Exempt           map[string]bool
	ConnectorKinds   []string
	ChannelKinds     []string
	DestinationKinds []string
	Switches         []settings.Switch
	Skills           []skills.Skill
	Routes           Routes
}

// Routes is the web UI's route export (web/routes.generated.json).
type Routes struct {
	Routes        []Route        `json:"routes"`
	SettingsAreas []SettingsArea `json:"settings_areas"`
	Labels        []string       `json:"labels"`
}

// Route is one web UI path, with its nav label when it has one.
type Route struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
}

// SettingsArea is one settings page.
type SettingsArea struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

// Page is one generated markdown file.
type Page struct {
	File        string
	Title       string
	Description string
	AppPath     string
	AppLabel    string
	Body        string
}

// services mirrors the service table in AGENTS.md.
var services = []struct{ name, role string }{
	{"brain", "the public API: chat, the agent loop and missions"},
	{"gateway", "the internal LLM gateway: provider routing and the cost ledger"},
	{"memoryd", "the internal memory service: pgvector recall"},
	{"sandboxd", "per-mission sandbox containers"},
	{"web", "the React UI"},
	{"searxng", "the metasearch backend for search_web"},
	{"markitdown", "file to markdown conversion"},
	{"ocr", "local image OCR"},
	{"whisper", "local speech to text, off unless enabled"},
	{"pdfgen", "markdown to PDF for mission export"},
}

// availableWhen notes tools brain registers only under a condition.
var availableWhen = map[string]string{
	"search_web":          "SEARXNG_URL is set.",
	"generate_pdf":        "PDFGEN_URL and ATTACHMENTS_DIR are set.",
	"share_file":          "ATTACHMENTS_DIR is set.",
	"deliver":             "WORKSPACES is set and destinations are configured.",
	"list_missions":       "WORKSPACES is set.",
	"get_mission":         "WORKSPACES is set.",
	"followup_mission":    "WORKSPACES is set.",
	"create_mission":      "WORKSPACES is set.",
	"push_mission_branch": "WORKSPACES is set and connectors are enabled.",
	"write_file":          "Inside mission workspaces.",
	"search_memory":       "Inside missions.",
}

// Generate renders every page, sorted by file name. It fails on a tool
// without a name or description, a duplicate tool name, or a settings
// area the routes export does not list.
func Generate(in Input) ([]Page, error) {
	toolsPage, err := toolsPage(in.Tools, in.Exempt)
	if err != nil {
		return nil, err
	}
	areas := map[string]SettingsArea{}
	for _, a := range in.Routes.SettingsAreas {
		areas[a.Key] = a
	}
	area := func(key string) (SettingsArea, error) {
		a, ok := areas[key]
		if !ok {
			return a, fmt.Errorf("%w: settings area %q missing from routes export", ErrInvalid, key)
		}
		return a, nil
	}
	pages := []Page{aboutPage(in.Version), toolsPage, settingsPage(in.Routes.SettingsAreas), skillsPage(in.Skills), routesPage(in.Routes.Routes)}
	for _, k := range []struct {
		file, title, area, intro string
		kinds                    []string
	}{
		{"connectors.md", "Connectors", "connectors", "Connector kinds this build can create.", in.ConnectorKinds},
		{"channels.md", "Channels", "channels", "Channel kinds this build runs.", in.ChannelKinds},
		{"destinations.md", "Destinations", "destinations", "Destination kinds a mission result can be delivered to.", in.DestinationKinds},
	} {
		a, err := area(k.area)
		if err != nil {
			return nil, err
		}
		pages = append(pages, kindsPage(k.file, k.title, k.intro, a, k.kinds))
	}
	features, err := area("features")
	if err != nil {
		return nil, err
	}
	pages = append(pages, featuresPage(features, in.Switches))
	slices.SortFunc(pages, func(a, b Page) int { return strings.Compare(a.File, b.File) })
	return pages, nil
}

func aboutPage(version string) Page {
	var b strings.Builder
	fmt.Fprintf(&b, "Timothy is a self-hosted personal AI assistant. This page describes version `%s`.\n\n", version)
	b.WriteString("It runs as Go services, one PostgreSQL database and a React web UI under Docker Compose. The services are ")
	for i, s := range services {
		switch {
		case i == len(services)-1:
			b.WriteString(", and ")
		case i > 0:
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "`%s` (%s)", s.name, s.role)
	}
	b.WriteString(".\n")
	return Page{File: "about.md", Title: "About Timothy", Description: "What this build of Timothy is and the services it runs.", Body: b.String()}
}

func toolsPage(in []*tools.Tool, exempt map[string]bool) (Page, error) {
	sorted := slices.Clone(in)
	seen := map[string]bool{}
	for _, t := range sorted {
		if t == nil || t.Name == "" {
			return Page{}, fmt.Errorf("%w: tool without a name", ErrInvalid)
		}
		if strings.TrimSpace(t.Description) == "" {
			return Page{}, fmt.Errorf("%w: tool %s has an empty description", ErrInvalid, t.Name)
		}
		if seen[t.Name] {
			return Page{}, fmt.Errorf("%w: duplicate tool %s", ErrInvalid, t.Name)
		}
		seen[t.Name] = true
	}
	slices.SortFunc(sorted, func(a, b *tools.Tool) int { return strings.Compare(a.Name, b.Name) })
	var b strings.Builder
	b.WriteString("Tools the agent can call in this build. Connector tools are added at runtime and are not listed here.\n")
	for _, t := range sorted {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n\n", t.Name, summary(t.Description))
		fmt.Fprintf(&b, "- Permission-exempt: %s\n", yesNo(exempt[t.Name]))
		if note, ok := availableWhen[t.Name]; ok {
			fmt.Fprintf(&b, "- Available when: %s\n", note)
		}
	}
	return Page{File: "tools.md", Title: "Tools", Description: "Every builtin tool this build ships.", Body: b.String()}, nil
}

func kindsPage(file, title, intro string, area SettingsArea, kinds []string) Page {
	var b strings.Builder
	b.WriteString(intro + "\n\n")
	for _, k := range sortedCopy(kinds) {
		fmt.Fprintf(&b, "- `%s`\n", k)
	}
	return Page{File: file, Title: title, Description: intro, AppPath: area.Path, AppLabel: area.Label, Body: b.String()}
}

func settingsPage(areas []SettingsArea) Page {
	sorted := slices.Clone(areas)
	slices.SortFunc(sorted, func(a, b SettingsArea) int { return strings.Compare(a.Key, b.Key) })
	var b strings.Builder
	b.WriteString("Settings pages in the web UI.\n\n")
	for _, a := range sorted {
		fmt.Fprintf(&b, "- %s: `%s`\n", a.Label, a.Path)
	}
	return Page{File: "settings.md", Title: "Settings", Description: "Settings pages in the web UI.", AppPath: "/settings", AppLabel: "Settings", Body: b.String()}
}

func featuresPage(area SettingsArea, switches []settings.Switch) Page {
	sorted := slices.Clone(switches)
	slices.SortFunc(sorted, func(a, b settings.Switch) int { return strings.Compare(a.Key, b.Key) })
	var b strings.Builder
	b.WriteString("Feature switches and their defaults. A change applies immediately, with no restart.\n\n")
	for _, s := range sorted {
		def := "off"
		if s.Default {
			def = "on"
		}
		fmt.Fprintf(&b, "- `%s`: %s by default\n", s.Key, def)
	}
	return Page{File: "features.md", Title: "Features", Description: "Feature switches and their defaults.", AppPath: area.Path, AppLabel: area.Label, Body: b.String()}
}

func skillsPage(packs []skills.Skill) Page {
	sorted := slices.Clone(packs)
	slices.SortFunc(sorted, func(a, b skills.Skill) int { return strings.Compare(a.Name, b.Name) })
	var b strings.Builder
	b.WriteString("Skill packs the agent can load for a kind of task.\n")
	for _, s := range sorted {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", s.Name, strings.TrimSpace(s.Description))
	}
	return Page{File: "skills.md", Title: "Skills", Description: "Skill packs this build ships.", Body: b.String()}
}

func routesPage(routes []Route) Page {
	sorted := slices.Clone(routes)
	slices.SortFunc(sorted, func(a, b Route) int { return strings.Compare(a.Path, b.Path) })
	var b strings.Builder
	b.WriteString("Paths the web UI serves.\n\n")
	for _, r := range sorted {
		if r.Label != "" {
			fmt.Fprintf(&b, "- `%s`: %s\n", r.Path, r.Label)
		} else {
			fmt.Fprintf(&b, "- `%s`\n", r.Path)
		}
	}
	return Page{File: "routes.md", Title: "Routes", Description: "Paths the web UI serves.", Body: b.String()}
}

// Render returns the page as markdown with YAML frontmatter; empty
// fields are omitted.
func Render(p Page) string {
	var b strings.Builder
	b.WriteString("---\n")
	for _, f := range []struct{ key, value string }{
		{"title", p.Title}, {"description", p.Description}, {"app_path", p.AppPath}, {"app_label", p.AppLabel}, {"source", Source},
	} {
		if f.value == "" {
			continue
		}
		// A JSON string is a valid YAML double-quoted scalar.
		q, _ := json.Marshal(f.value)
		fmt.Fprintf(&b, "%s: %s\n", f.key, q)
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(p.Body, "\n") + "\n")
	return b.String()
}

type index struct {
	Version string   `json:"version"`
	Source  string   `json:"source"`
	Files   []string `json:"files"`
	SHA256  string   `json:"sha256"`
}

// Write renders pages into dir plus manifest.json, whose sha256 covers
// the page contents concatenated in file-name order.
func Write(dir string, pages []Page, version string) error {
	sorted := slices.Clone(pages)
	slices.SortFunc(sorted, func(a, b Page) int { return strings.Compare(a.File, b.File) })
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // generated docs, read by the image build as another user
		return fmt.Errorf("manifest: create %s: %w", dir, err)
	}
	h := sha256.New()
	idx := index{Version: version, Source: Source, Files: make([]string, 0, len(sorted))}
	for _, p := range sorted {
		if p.File == "" || filepath.Base(p.File) != p.File || p.File == "manifest.json" {
			return fmt.Errorf("%w: page file name %q", ErrInvalid, p.File)
		}
		content := Render(p)
		if err := os.WriteFile(filepath.Join(dir, p.File), []byte(content), 0o644); err != nil { //nolint:gosec // generated docs, not secrets
			return fmt.Errorf("manifest: write %s: %w", p.File, err)
		}
		h.Write([]byte(content))
		idx.Files = append(idx.Files, p.File)
	}
	idx.SHA256 = hex.EncodeToString(h.Sum(nil))
	raw, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest: encode index: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(raw, '\n'), 0o644); err != nil { //nolint:gosec // generated index, not a secret
		return fmt.Errorf("manifest: write manifest.json: %w", err)
	}
	return nil
}

// summary is a tool description's first paragraph (what the tool does),
// with em dashes replaced to match the docs style.
func summary(desc string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(desc), "\n\n")
	s := strings.Join(strings.Fields(first), " ")
	return strings.ReplaceAll(strings.ReplaceAll(s, " \u2014 ", ", "), "\u2014", "-")
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}
