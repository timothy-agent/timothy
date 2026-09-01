package missions

import (
	"os"
	"path/filepath"
	"testing"
)

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
