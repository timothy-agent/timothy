package missions

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

func TestParseModelFloor(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{" , ", nil},
		{"qwen2.5:7b,nova", []string{"qwen2.5:7b", "nova"}},
		{" nova-lite , ,llama3.2 ", []string{"nova-lite", "llama3.2"}},
	}
	for _, tc := range cases {
		if got := ParseModelFloor(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseModelFloor(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestBelowModelFloor(t *testing.T) {
	floor := []string{"qwen2.5:7b", "nova"}
	cases := []struct {
		model string
		want  bool
	}{
		{"qwen2.5:7b", true},
		{"QWEN2.5:7B-instruct", true},
		{"amazon.nova-lite-v1:0", true},
		{"qwen3:8b", false},
		{"gpt-5.3-codex", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := BelowModelFloor(floor, tc.model); got != tc.want {
			t.Errorf("BelowModelFloor(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
	if BelowModelFloor(nil, "qwen2.5:7b") {
		t.Error("an empty floor must match nothing")
	}
}

// TestDefaultFloorSparesOllamaPreset (issue #1090): the welcome
// wizard's Ollama model must clear the compose default floor, or the
// sample mission pauses on its first turn.
func TestDefaultFloorSparesOllamaPreset(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	presets, err := os.ReadFile(filepath.Join(root, "web", "src", "lib", "providerPresets.ts")) //nolint:gosec // G304: fixed repo path.
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`id: 'ollama',[\s\S]*?validateModel: '([^']*)'`).FindSubmatch(presets)
	if m == nil || len(m[1]) == 0 {
		t.Fatal("ollama preset validateModel not found in providerPresets.ts")
	}
	preset := string(m[1])
	floorRe := regexp.MustCompile(`MISSION_MODEL_FLOOR: \$\{MISSION_MODEL_FLOOR:-([^}]*)\}`)
	for _, compose := range []string{
		filepath.Join(root, "deploy", "docker-compose.yml"),
		filepath.Join(root, "deploy", "release", "docker-compose.yml"),
	} {
		b, err := os.ReadFile(compose) //nolint:gosec // G304: fixed repo path.
		if err != nil {
			t.Fatal(err)
		}
		fm := floorRe.FindSubmatch(b)
		if fm == nil {
			t.Fatalf("%s: no MISSION_MODEL_FLOOR default", compose)
		}
		floor := ParseModelFloor(string(fm[1]))
		cases := []struct {
			model string
			want  bool
		}{
			{preset, false},
			{"qwen2.5:7b", true},
		}
		for _, tc := range cases {
			if got := BelowModelFloor(floor, tc.model); got != tc.want {
				t.Errorf("%s: BelowModelFloor(%q, %q) = %v, want %v", compose, floor, tc.model, got, tc.want)
			}
		}
	}
}
