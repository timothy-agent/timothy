package missions

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

func TestDetectEnvironmentFromMarkers(t *testing.T) {
	manyFiles := func(dir, ext string, n int) []string {
		var out []string
		for i := range n {
			out = append(out, filepath.Join(dir, "f"+strconv.Itoa(i)+ext))
		}
		return out
	}
	cases := []struct {
		name           string
		files          []string
		wantEnv        string
		wantMarker     string
		wantCandidates []string
	}{
		{"go.mod", []string{"go.mod"}, "go", "go.mod", nil},
		{"package.json", []string{"package.json"}, "node", "package.json", nil},
		{"composer.json", []string{"composer.json"}, "php", "composer.json", nil},
		{"pom.xml", []string{"pom.xml"}, "java", "pom.xml", nil},
		{"build.gradle", []string{"build.gradle"}, "java", "build.gradle", nil},
		{"pyproject.toml", []string{"pyproject.toml"}, "python", "pyproject.toml", nil},
		{"requirements.txt", []string{"requirements.txt"}, "python", "requirements.txt", nil},
		{"composer outranks package.json", []string{"composer.json", "package.json"}, "php", "composer.json", []string{"package.json"}},
		{"go.mod outranks package.json", []string{"package.json", "go.mod"}, "go", "go.mod", []string{"package.json"}},
		{"requirements outranks package.json", []string{"package.json", "requirements.txt"}, "python", "requirements.txt", []string{"package.json"}},
		{
			"laravel-like",
			[]string{"composer.json", "composer.lock", "package.json", "vite.config.js", "app/Models/User.php", "resources/js/app.js"},
			"php", "composer.json", []string{"package.json"},
		},
		{
			"go.mod and pyproject.toml, more go files",
			append([]string{"go.mod", "pyproject.toml", "a.py"}, manyFiles("src", ".go", 3)...),
			"go", "go.mod", []string{"pyproject.toml"},
		},
		{
			"go.mod and pyproject.toml, more python files",
			append([]string{"go.mod", "pyproject.toml", "a.go"}, manyFiles("src", ".py", 3)...),
			"python", "pyproject.toml", []string{"go.mod"},
		},
		{"go.mod and pyproject.toml, no sources falls back to precedence", []string{"go.mod", "pyproject.toml"}, "go", "go.mod", []string{"pyproject.toml"}},
		{
			"equal counts fall back to precedence",
			[]string{"go.mod", "pyproject.toml", "a.go", "a.py"},
			"go", "go.mod", []string{"pyproject.toml"},
		},
		{
			"vendor and node_modules are not counted",
			append(append([]string{"composer.json", "go.mod", "a.php", "b.php"}, manyFiles("vendor", ".go", 50)...), manyFiles("node_modules/x", ".go", 50)...),
			"php", "composer.json", []string{"go.mod"},
		},
		{
			"dot directories are not counted",
			append([]string{"go.mod", "pyproject.toml", "a.py"}, manyFiles(".cache", ".go", 5)...),
			"python", "pyproject.toml", []string{"go.mod"},
		},
		{
			"tie-break ignores files below max depth",
			append([]string{"go.mod", "pyproject.toml", "a.py"}, manyFiles("a/b/c/d/e", ".go", 5)...),
			"python", "pyproject.toml", []string{"go.mod"},
		},
		{
			"same-env markers need no tie-break",
			append([]string{"pom.xml", "build.gradle"}, manyFiles("src", ".go", 3)...),
			"java", "pom.xml", []string{"build.gradle"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, f := range tc.files {
				path := filepath.Join(dir, f)
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
					t.Fatalf("write %s: %v", f, err)
				}
			}
			env, marker, candidates := detectEnvironmentFromMarkers(dir)
			if env != tc.wantEnv || marker != tc.wantMarker || !slices.Equal(candidates, tc.wantCandidates) {
				t.Errorf("detectEnvironmentFromMarkers = (%q, %q, %v), want (%q, %q, %v)",
					env, marker, candidates, tc.wantEnv, tc.wantMarker, tc.wantCandidates)
			}
		})
	}

	t.Run("symlinked directories are not followed", func(t *testing.T) {
		t.Parallel()
		outside := t.TempDir()
		for i := range 5 {
			if err := os.WriteFile(filepath.Join(outside, "f"+strconv.Itoa(i)+".go"), nil, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		dir := t.TempDir()
		for _, f := range []string{"go.mod", "pyproject.toml", "a.py"} {
			if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		env, marker, _ := detectEnvironmentFromMarkers(dir)
		if env != "python" || marker != "pyproject.toml" {
			t.Errorf("detectEnvironmentFromMarkers = (%q, %q), want (python, pyproject.toml)", env, marker)
		}
	})

	t.Run("empty worktree falls back to base", func(t *testing.T) {
		t.Parallel()
		env, marker, candidates := detectEnvironmentFromMarkers(t.TempDir())
		if env != "" || marker != "" || candidates != nil {
			t.Errorf("detectEnvironmentFromMarkers(empty) = (%q, %q, %v), want (\"\", \"\", nil)", env, marker, candidates)
		}
	})

	t.Run("blank worktree path falls back to base", func(t *testing.T) {
		t.Parallel()
		env, marker, candidates := detectEnvironmentFromMarkers("")
		if env != "" || marker != "" || candidates != nil {
			t.Errorf("detectEnvironmentFromMarkers(\"\") = (%q, %q, %v), want (\"\", \"\", nil)", env, marker, candidates)
		}
	})
}

func TestValidEnvironment(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"", true},
		{"go", true},
		{"node", true},
		{"python", true},
		{"java", true},
		{"php", true},
		{"base", true},
		{"ruby", false},
	}
	for _, tc := range cases {
		if got := ValidEnvironment(tc.v); got != tc.want {
			t.Errorf("ValidEnvironment(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
}
