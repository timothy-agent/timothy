package automations

import (
	"fmt"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

func TestRunEventStripsStarterKeys(t *testing.T) {
	ev := runEvent([]byte(`{"kind":"run.now","n":42,"start_error":"x","start_attempts":2,"start_attempt_at":"t"}`))
	if len(ev) != 2 || fmt.Sprint(ev["n"]) != "42" || ev["kind"] != "run.now" {
		t.Fatalf("runEvent = %v", ev)
	}
	if got := runEvent([]byte(`not json`)); len(got) != 0 {
		t.Fatalf("bad json = %v, want empty", got)
	}
}

func TestTriggerSource(t *testing.T) {
	if _, ok := triggerSource(map[string]any{}); ok {
		t.Fatal("empty event must produce no entry")
	}
	e, ok := triggerSource(map[string]any{"kind": "run.now", "url": "https://x/?a=1&b=<2>"})
	if !ok || e.Source != missions.SourceKindTrigger || e.Name != "Trigger" {
		t.Fatalf("entry = %+v", e)
	}
	if e.Digest != "{\n  \"kind\": \"run.now\",\n  \"url\": \"https://x/?a=1&b=<2>\"\n}" {
		t.Fatalf("digest = %q", e.Digest)
	}
	e, _ = triggerSource(map[string]any{"body": strings.Repeat("x", 5000)})
	if !strings.HasSuffix(e.Digest, "\n[truncated]") || len(e.Digest) != maxTriggerDigestBytes+len("\n[truncated]") {
		t.Fatalf("truncated digest len = %d, tail %q", len(e.Digest), e.Digest[len(e.Digest)-20:])
	}
}

func TestNotesSource(t *testing.T) {
	if _, ok := notesSource(nil); ok {
		t.Fatal("no notes must produce no entry")
	}
	e, ok := notesSource([]Note{{Name: "a", Content: "1"}, {Name: "b", Content: "two"}})
	if !ok || e.Source != missions.SourceKindNotes || e.Name != "Automation notes" || e.Digest != "a:\n1\n\nb:\ntwo" {
		t.Fatalf("entry = %+v", e)
	}
	full := strings.Repeat("x", MaxNoteBytes)
	var notes []Note
	for i := range MaxNotes {
		notes = append(notes, Note{Name: fmt.Sprintf("n%02d", i), Content: full})
	}
	e, _ = notesSource(notes)
	if len(e.Digest) > maxNotesDigestBytes+64 {
		t.Fatalf("digest len = %d, over the cap", len(e.Digest))
	}
	if !strings.HasPrefix(e.Digest, "n00:\n") || !strings.Contains(e.Digest, "n02:\n") || strings.Contains(e.Digest, "n03:\n") {
		t.Fatalf("digest must keep the first notes by name, got heads %q", noteHeads(e.Digest))
	}
	if !strings.HasSuffix(e.Digest, "\n\n[7 more notes not shown]") {
		t.Fatalf("digest tail = %q", e.Digest[len(e.Digest)-40:])
	}
	e, _ = notesSource([]Note{{Name: "huge", Content: strings.Repeat("y", maxNotesDigestBytes)}})
	if e.Digest != "[1 more notes not shown]" {
		t.Fatalf("oversized first note digest = %q", e.Digest)
	}
}

func noteHeads(digest string) []string {
	var out []string
	for _, line := range strings.Split(digest, "\n") {
		if strings.HasSuffix(line, ":") {
			out = append(out, line)
		}
	}
	return out
}
