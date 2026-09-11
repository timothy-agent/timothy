package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/kb"
)

const (
	bnText = "আমি আজ সকালে অফিসে গিয়েছিলাম। কাজের চাপ ছিল অনেক বেশি।"
	enText = "I went to the office this morning. The workload was heavier than usual."
	mixed  = "আমি GitHub এ একটা PR খুলেছি। email পাঠিয়ে দিয়েছি সকালেই।"
)

func TestDetectLanguage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bangla", bnText, "bn"},
		{"english", enText, "en"},
		{"bangla with english tech words", mixed, "bn"},
		{"empty", "", "en"},
		{"digits and punctuation only", "123 ... !!!", "en"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := detectLanguage(tc.in); got != tc.want {
				t.Fatalf("detectLanguage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

type fakeSamples struct {
	docs     []kb.DocumentText
	err      error
	sawName  string
	sawLimit int
}

func (f *fakeSamples) fetch(_ context.Context, name string, limit int) ([]kb.DocumentText, error) {
	f.sawName, f.sawLimit = name, limit
	return f.docs, f.err
}

func fixedCollection(name string) WritingSamplesCollectionFunc {
	return func(context.Context) string { return name }
}

func sampleDocs() []kb.DocumentText {
	day := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	return []kb.DocumentText{
		{Title: "Bangla note", CreatedAt: day, Text: bnText},
		{Title: "English post", CreatedAt: day.AddDate(0, 0, -1), Text: enText},
		{Title: "Mixed note", CreatedAt: day.AddDate(0, 0, -2), Text: mixed},
	}
}

func execTool(t *testing.T, coll string, fk *fakeSamples, args map[string]any) (string, error) {
	t.Helper()
	tool := WritingSamples(fixedCollection(coll), fk.fetch)
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Execute(context.Background(), raw)
}

func TestWritingSamplesUnsetCollection(t *testing.T) {
	t.Parallel()
	fk := &fakeSamples{docs: sampleDocs()}
	out, err := execTool(t, "", fk, map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "No writing samples collection is configured." {
		t.Fatalf("unexpected output: %q", out)
	}
	if fk.sawName != "" {
		t.Fatal("fetch must not be called without a configured collection")
	}
}

func TestWritingSamplesFilters(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    map[string]any
		want    []string
		notWant []string
	}{
		{"bn filter", map[string]any{"language": "bn"}, []string{"1. Bangla note", "2. Mixed note", "2026-09-11"}, []string{"English post"}},
		{"en filter", map[string]any{"language": "en"}, []string{"1. English post", "2026-09-10"}, []string{"Bangla note", "Mixed note"}},
		{"no filter", map[string]any{}, []string{"1. Bangla note", "2. English post", "3. Mixed note"}, nil},
		{"k bound", map[string]any{"k": 1}, []string{"1. Bangla note"}, []string{"English post"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fk := &fakeSamples{docs: sampleDocs()}
			out, err := execTool(t, "samples", fk, tc.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Fatalf("output missing %q:\n%s", w, out)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(out, w) {
					t.Fatalf("output has unwanted %q:\n%s", w, out)
				}
			}
			if fk.sawName != "samples" {
				t.Fatalf("collection name = %q, want samples", fk.sawName)
			}
		})
	}
}

func TestWritingSamplesDefaultK(t *testing.T) {
	t.Parallel()
	fk := &fakeSamples{docs: sampleDocs()}
	if _, err := execTool(t, "samples", fk, map[string]any{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fk.sawLimit != writingSamplesDefaultK {
		t.Fatalf("limit = %d, want %d", fk.sawLimit, writingSamplesDefaultK)
	}
}

func TestWritingSamplesNoMatch(t *testing.T) {
	t.Parallel()
	fk := &fakeSamples{docs: []kb.DocumentText{{Title: "English post", Text: enText}}}
	out, err := execTool(t, "samples", fk, map[string]any{"language": "bn"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "No bn samples found in samples." {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestWritingSamplesEmptyCollection(t *testing.T) {
	t.Parallel()
	fk := &fakeSamples{}
	out, err := execTool(t, "samples", fk, map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "No samples found in samples." {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestWritingSamplesTruncates(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("অ", writingSamplesTextCap+500)
	fk := &fakeSamples{docs: []kb.DocumentText{{Title: "Long", Text: long}}}
	out, err := execTool(t, "samples", fk, map[string]any{"language": "bn"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), " ...") {
		t.Fatalf("truncated sample must end with the cut marker:\n%s", out[len(out)-40:])
	}
	if n := strings.Count(out, "অ"); n != writingSamplesTextCap {
		t.Fatalf("kept %d runes, want %d", n, writingSamplesTextCap)
	}
}

func TestWritingSamplesBadArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{"malformed json", `{"language":`},
		{"unknown language", `{"language":"fr"}`},
		{"k too low", `{"k":0}`},
		{"k too high", `{"k":6}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fk := &fakeSamples{docs: sampleDocs()}
			tool := WritingSamples(fixedCollection("samples"), fk.fetch)
			if _, err := tool.Execute(context.Background(), json.RawMessage(tc.raw)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestWritingSamplesFetchError(t *testing.T) {
	t.Parallel()
	fk := &fakeSamples{err: errors.New("boom")}
	if _, err := execTool(t, "samples", fk, map[string]any{}); err == nil {
		t.Fatal("expected the fetch error to surface")
	}
}
