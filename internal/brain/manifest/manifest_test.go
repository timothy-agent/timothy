package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/settings"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

func fixture() Input {
	return Input{
		Version: "0.1.0-alpha.1",
		Tools: []*tools.Tool{
			{Name: "search_web", Description: "Searches the web.\n\nArguments: query."},
			{Name: "calculate", Description: "Evaluates   arithmetic\nexpressions."},
			{Name: "shell", Description: "Runs a command \u2014 in the workspace."},
		},
		Exempt:           map[string]bool{"calculate": true, "search_web": true},
		ConnectorKinds:   []string{"mcp", "google"},
		ChannelKinds:     []string{"telegram", "email"},
		DestinationKinds: []string{"webhook", "email"},
		Switches:         []settings.Switch{{Key: "tools_enabled", Default: true}, {Key: "kb_image_captioning_enabled"}},
		Skills:           []skills.Skill{{Name: "writing", Description: "Use when writing."}, {Name: "coding", Description: "Use when coding."}},
		Routes: Routes{
			Routes: []Route{{Path: "/missions", Label: "Missions"}, {Path: "/"}},
			SettingsAreas: []SettingsArea{
				{Key: "connectors", Label: "Connectors", Path: "/settings/connectors"},
				{Key: "channels", Label: "Channels", Path: "/settings/channels"},
				{Key: "destinations", Label: "Destinations", Path: "/settings/destinations"},
				{Key: "features", Label: "Features", Path: "/settings/features"},
			},
		},
	}
}

func pageByFile(t *testing.T, pages []Page, file string) Page {
	t.Helper()
	for _, p := range pages {
		if p.File == file {
			return p
		}
	}
	t.Fatalf("no page %s", file)
	return Page{}
}

func TestGenerate(t *testing.T) {
	t.Parallel()
	pages, err := Generate(fixture())
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, p := range pages {
		files = append(files, p.File)
	}
	wantFiles := []string{"about.md", "channels.md", "connectors.md", "destinations.md", "features.md", "routes.md", "settings.md", "skills.md", "tools.md"}
	if !reflect.DeepEqual(files, wantFiles) {
		t.Fatalf("files = %v, want %v", files, wantFiles)
	}

	tests := []struct {
		file      string
		appPath   string
		appLabel  string
		contains  []string
		notInBody []string
	}{
		{file: "about.md", contains: []string{"`0.1.0-alpha.1`", "`brain`", "`pdfgen`"}},
		{file: "tools.md", contains: []string{
			"## calculate\n\nEvaluates arithmetic expressions.\n\n- Permission-exempt: yes\n",
			"## search_web\n\nSearches the web.\n\n- Permission-exempt: yes\n- Available when: SEARXNG_URL is set.\n",
			"## shell\n\nRuns a command, in the workspace.\n\n- Permission-exempt: no\n",
		}, notInBody: []string{"Arguments"}},
		{file: "connectors.md", appPath: "/settings/connectors", appLabel: "Connectors", contains: []string{"- `google`\n- `mcp`\n"}},
		{file: "channels.md", appPath: "/settings/channels", appLabel: "Channels", contains: []string{"- `email`\n- `telegram`\n"}},
		{file: "destinations.md", appPath: "/settings/destinations", appLabel: "Destinations", contains: []string{"- `email`\n- `webhook`\n"}},
		{file: "features.md", appPath: "/settings/features", appLabel: "Features", contains: []string{
			"- `kb_image_captioning_enabled`: off by default\n- `tools_enabled`: on by default\n",
		}},
		{file: "settings.md", appPath: "/settings", appLabel: "Settings", contains: []string{"- Channels: `/settings/channels`\n- Connectors: `/settings/connectors`\n"}},
		{file: "skills.md", contains: []string{"## coding\n\nUse when coding.\n\n## writing\n"}},
		{file: "routes.md", contains: []string{"- `/`\n- `/missions`: Missions\n"}},
	}
	for _, tc := range tests {
		p := pageByFile(t, pages, tc.file)
		if p.AppPath != tc.appPath || p.AppLabel != tc.appLabel {
			t.Errorf("%s app = %q/%q, want %q/%q", tc.file, p.AppPath, p.AppLabel, tc.appPath, tc.appLabel)
		}
		if p.Title == "" || p.Description == "" {
			t.Errorf("%s missing title or description", tc.file)
		}
		for _, s := range tc.contains {
			if !strings.Contains(p.Body, s) {
				t.Errorf("%s body missing %q:\n%s", tc.file, s, p.Body)
			}
		}
		for _, s := range tc.notInBody {
			if strings.Contains(p.Body, s) {
				t.Errorf("%s body contains %q", tc.file, s)
			}
		}
	}
}

func TestGenerateRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Input)
		want   string
	}{
		{name: "empty description", mutate: func(in *Input) { in.Tools[0].Description = "  " }, want: "empty description"},
		{name: "empty name", mutate: func(in *Input) { in.Tools[0].Name = "" }, want: "without a name"},
		{name: "duplicate tool", mutate: func(in *Input) { in.Tools[1].Name = in.Tools[0].Name }, want: "duplicate tool"},
		{name: "missing settings area", mutate: func(in *Input) { in.Routes.SettingsAreas = in.Routes.SettingsAreas[:3] }, want: `"features"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := fixture()
			tc.mutate(&in)
			_, err := Generate(in)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}
}

func TestGenerateDeterministic(t *testing.T) {
	t.Parallel()
	a, err := Generate(fixture())
	if err != nil {
		t.Fatal(err)
	}
	in := fixture()
	// Reversed input order must not change the output.
	for i, j := 0, len(in.Tools)-1; i < j; i, j = i+1, j-1 {
		in.Tools[i], in.Tools[j] = in.Tools[j], in.Tools[i]
	}
	in.ConnectorKinds = []string{"google", "mcp"}
	b, err := Generate(in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("Generate output differs across runs")
	}
}

func TestRenderFrontmatter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		page Page
		want string
	}{
		{
			name: "all fields",
			page: Page{Title: "Features", Description: `Say "hi"`, AppPath: "/settings/features", AppLabel: "Features", Body: "Body.\n\n"},
			want: "---\ntitle: \"Features\"\ndescription: \"Say \\\"hi\\\"\"\napp_path: \"/settings/features\"\napp_label: \"Features\"\nsource: \"manifest\"\n---\n\nBody.\n",
		},
		{
			name: "empty keys omitted",
			page: Page{Title: "Tools", Body: "Body."},
			want: "---\ntitle: \"Tools\"\nsource: \"manifest\"\n---\n\nBody.\n",
		},
	}
	for _, tc := range tests {
		if got := Render(tc.page); got != tc.want {
			t.Errorf("%s: Render =\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}
}

func TestWrite(t *testing.T) {
	t.Parallel()
	pages, err := Generate(fixture())
	if err != nil {
		t.Fatal(err)
	}
	dirA, dirB := t.TempDir(), t.TempDir()
	if err := Write(dirA, pages, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := Write(dirB, pages, "v1"); err != nil {
		t.Fatal(err)
	}
	rawA, err := os.ReadFile(filepath.Join(dirA, "manifest.json")) //nolint:gosec // reads back this test's own t.TempDir output
	if err != nil {
		t.Fatal(err)
	}
	rawB, err := os.ReadFile(filepath.Join(dirB, "manifest.json")) //nolint:gosec // reads back this test's own t.TempDir output
	if err != nil {
		t.Fatal(err)
	}
	if string(rawA) != string(rawB) {
		t.Fatal("manifest.json differs across runs")
	}
	var idx struct {
		Version string   `json:"version"`
		Source  string   `json:"source"`
		Files   []string `json:"files"`
		SHA256  string   `json:"sha256"`
	}
	if err := json.Unmarshal(rawA, &idx); err != nil {
		t.Fatal(err)
	}
	if idx.Version != "v1" || idx.Source != "manifest" || len(idx.Files) != len(pages) {
		t.Fatalf("index = %+v", idx)
	}
	h := sha256.New()
	for i, f := range idx.Files {
		if f != pages[i].File {
			t.Fatalf("files[%d] = %s, want %s", i, f, pages[i].File)
		}
		content, err := os.ReadFile(filepath.Join(dirA, f)) //nolint:gosec // reads back this test's own t.TempDir output
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != Render(pages[i]) {
			t.Fatalf("%s content differs from Render", f)
		}
		h.Write(content)
	}
	if want := hex.EncodeToString(h.Sum(nil)); idx.SHA256 != want {
		t.Fatalf("sha256 = %s, want %s", idx.SHA256, want)
	}
}

func TestWriteRejectsUnsafeFileNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "../x.md", "sub/x.md", "manifest.json"} {
		err := Write(t.TempDir(), []Page{{File: name, Title: "x"}}, "v1")
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("Write(%q) err = %v, want ErrInvalid", name, err)
		}
	}
}
