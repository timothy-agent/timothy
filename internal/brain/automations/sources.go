package automations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

const (
	// maxTriggerDigestBytes caps the trigger source's digest.
	maxTriggerDigestBytes = 4 << 10
	// maxNotesDigestBytes caps the notes source's digest.
	maxNotesDigestBytes = 16 << 10
)

// starterKeys are the starter's own bookkeeping in a run's event, kept
// out of the trigger source and interpolation.
var starterKeys = []string{"start_error", "start_attempts", "start_attempt_at"}

// runEvent decodes a run's event without the starter's bookkeeping.
// Numbers stay json.Number so an id renders as written.
func runEvent(raw []byte) map[string]any {
	var ev map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&ev) != nil || ev == nil {
		return map[string]any{}
	}
	for _, k := range starterKeys {
		delete(ev, k)
	}
	return ev
}

// triggerSource renders event as the run's "trigger" source entry;
// false when event is empty.
func triggerSource(event map[string]any) (missions.SourceEntry, bool) {
	if len(event) == 0 {
		return missions.SourceEntry{}, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if enc.Encode(event) != nil {
		return missions.SourceEntry{}, false
	}
	digest := strings.TrimRight(buf.String(), "\n")
	if len(digest) > maxTriggerDigestBytes {
		digest = truncateUTF8(digest, maxTriggerDigestBytes) + "\n[truncated]"
	}
	return missions.SourceEntry{Source: missions.SourceKindTrigger, Name: "Trigger", Digest: digest}, true
}

// notesSource renders notes, already ordered by name, as the run's
// "notes" source entry. Notes past the 16 KB cap are counted, not
// shown. false when there are no notes.
func notesSource(notes []Note) (missions.SourceEntry, bool) {
	if len(notes) == 0 {
		return missions.SourceEntry{}, false
	}
	var b strings.Builder
	shown := 0
	for _, n := range notes {
		entry := n.Name + ":\n" + n.Content + "\n\n"
		if b.Len()+len(entry) > maxNotesDigestBytes {
			break
		}
		b.WriteString(entry)
		shown++
	}
	digest := strings.TrimRight(b.String(), "\n")
	if rest := len(notes) - shown; rest > 0 {
		if digest != "" {
			digest += "\n\n"
		}
		digest += fmt.Sprintf("[%d more notes not shown]", rest)
	}
	return missions.SourceEntry{Source: missions.SourceKindNotes, Name: "Automation notes", Digest: digest}, true
}

// noteMap indexes notes by name for interpolation.
func noteMap(notes []Note) map[string]string {
	out := make(map[string]string, len(notes))
	for _, n := range notes {
		out[n.Name] = n.Content
	}
	return out
}
