package automations

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestInterpolate(t *testing.T) {
	var event map[string]any
	dec := json.NewDecoder(strings.NewReader(`{"kind":"run.now","pr_url":"https://x/pr/7","number":1234567,"pr":{"head":{"ref":"feat/x"},"labels":["a","b"]},"items":[{"id":1}],"flag":true,"none":null}`))
	dec.UseNumber()
	if err := dec.Decode(&event); err != nil {
		t.Fatal(err)
	}
	notes := map[string]string{"last_branch": "feat/y", "loop": "{{event.kind}}"}
	cases := []struct {
		name, in, want string
	}{
		{"scalar", "Review {{event.pr_url}}", "Review https://x/pr/7"},
		{"number renders as written", "#{{event.number}}", "#1234567"},
		{"bool", "{{event.flag}}", "true"},
		{"null is empty", "[{{event.none}}]", "[]"},
		{"nested path", "on {{event.pr.head.ref}}", "on feat/x"},
		{"object as compact json", "{{event.pr.head}}", `{"ref":"feat/x"}`},
		{"array as compact json", "{{event.pr.labels}}", `["a","b"]`},
		{"array index not supported", "{{event.items.0}}", "{{event.items.0}}"},
		{"path through a scalar is missing", "[{{event.kind.x}}]", "[]"},
		{"missing key empty", "[{{event.nope}}]", "[]"},
		{"notes key", "branch {{notes.last_branch}}", "branch feat/y"},
		{"missing note empty", "[{{notes.nope}}]", "[]"},
		{"whitespace tolerated", "{{  event.kind }} {{ notes.last_branch}}", "run.now feat/y"},
		{"unknown prefix literal", "{{env.HOME}} {{event}} {{notes.Bad}}", "{{env.HOME}} {{event}} {{notes.Bad}}"},
		{"note content is never expanded", "{{notes.loop}}", "{{event.kind}}"},
		{"no placeholders", "plain goal", "plain goal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Interpolate(tc.in, event, notes); got != tc.want {
				t.Fatalf("Interpolate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestInterpolateCap(t *testing.T) {
	big := strings.Repeat("é", MaxNoteBytes/2)
	notes := map[string]string{}
	var goal strings.Builder
	for i := range 10 {
		name := "n" + string(rune('0'+i))
		notes[name] = big
		goal.WriteString("{{notes." + name + "}}")
	}
	got := Interpolate(goal.String()+goal.String(), nil, notes)
	if len(got) > MaxInterpolatedBytes {
		t.Fatalf("len = %d, want at most %d", len(got), MaxInterpolatedBytes)
	}
	if len(got) < MaxInterpolatedBytes-1 || !utf8.ValidString(got) {
		t.Fatalf("cap must cut on a rune boundary just under the limit, len %d", len(got))
	}
	if Interpolate("{{event.x}}", nil, nil) != "" {
		t.Fatal("nil event must render a missing key empty")
	}
}
