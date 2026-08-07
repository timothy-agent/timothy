package missions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoalEnvironmentKeyword(t *testing.T) {
	cases := []struct {
		name string
		goal string
		want string
	}{
		{"go cli", "write a Go CLI that parses logs", "go"},
		{"golang phrasing", "refactor this golang service", "go"},
		{"python algorithm", "implement a sorting algorithm in python", "python"},
		{"word go inside other words does not match", "design an algorithm for the routing logic", ""},
		{"ambiguous goal falls to base", "write some documentation about the project", ""},
		{"node keyword", "build a react dashboard with vite", "node"},
		{"java keyword", "set up a spring boot service with maven", "java"},
		{"php keyword", "add a laravel controller via composer", "php"},
		{"empty goal", "", ""},
		{"case insensitive", "PYTHON script for data cleaning", "python"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := goalEnvironmentKeyword(tc.goal); got != tc.want {
				t.Errorf("goalEnvironmentKeyword(%q) = %q, want %q", tc.goal, got, tc.want)
			}
		})
	}
}

func TestDetectEnvironmentFromMarkers(t *testing.T) {
	cases := []struct {
		name       string
		file       string
		wantEnv    string
		wantMarker string
	}{
		{"go.mod", "go.mod", "go", "go.mod"},
		{"package.json", "package.json", "node", "package.json"},
		{"composer.json", "composer.json", "php", "composer.json"},
		{"pom.xml", "pom.xml", "java", "pom.xml"},
		{"build.gradle", "build.gradle", "java", "build.gradle"},
		{"pyproject.toml", "pyproject.toml", "python", "pyproject.toml"},
		{"requirements.txt", "requirements.txt", "python", "requirements.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(""), 0o600); err != nil {
				t.Fatalf("write marker: %v", err)
			}
			env, marker := detectEnvironmentFromMarkers(dir)
			if env != tc.wantEnv || marker != tc.wantMarker {
				t.Errorf("detectEnvironmentFromMarkers = (%q, %q), want (%q, %q)", env, marker, tc.wantEnv, tc.wantMarker)
			}
		})
	}

	t.Run("empty worktree falls back to base", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env, marker := detectEnvironmentFromMarkers(dir)
		if env != "" || marker != "" {
			t.Errorf("detectEnvironmentFromMarkers(empty) = (%q, %q), want (\"\", \"\")", env, marker)
		}
	})

	t.Run("blank worktree path falls back to base", func(t *testing.T) {
		t.Parallel()
		env, marker := detectEnvironmentFromMarkers("")
		if env != "" || marker != "" {
			t.Errorf("detectEnvironmentFromMarkers(\"\") = (%q, %q), want (\"\", \"\")", env, marker)
		}
	})
}

func TestDetectEnvironmentChain(t *testing.T) {
	t.Run("marker wins over goal keyword", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(""), 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		env, marker := DetectEnvironment(dir, "build a python script")
		if env != "go" || marker != "go.mod" {
			t.Errorf("detectEnvironment = (%q, %q), want (\"go\", \"go.mod\")", env, marker)
		}
	})

	t.Run("no marker falls back to goal keyword", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env, marker := DetectEnvironment(dir, "write a Go CLI")
		if env != "go" || marker != "goal:go" {
			t.Errorf("detectEnvironment = (%q, %q), want (\"go\", \"goal:go\")", env, marker)
		}
	})

	t.Run("nothing matches falls back to base", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env, marker := DetectEnvironment(dir, "write some documentation")
		if env != "" || marker != "" {
			t.Errorf("detectEnvironment = (%q, %q), want (\"\", \"\")", env, marker)
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
