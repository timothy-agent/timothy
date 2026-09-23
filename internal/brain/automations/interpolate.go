package automations

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxInterpolatedBytes caps an interpolated goal or name.
const MaxInterpolatedBytes = 32 << 10

var (
	placeholderRe = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)
	eventPathRe   = regexp.MustCompile(`^event(\.[A-Za-z_][A-Za-z0-9_-]*)+$`)
	notesKeyRe    = regexp.MustCompile(`^notes\.([a-z0-9_-]{1,64})$`)
)

// Interpolate replaces {{event.a.b}} with the value at that path in
// event and {{notes.NAME}} with that note's content, in one pass so a
// substituted value is never expanded again. A missing key renders
// empty; any other placeholder stays literal. The result is capped at
// MaxInterpolatedBytes.
func Interpolate(s string, event map[string]any, notes map[string]string) string {
	out := placeholderRe.ReplaceAllStringFunc(s, func(match string) string {
		key := placeholderRe.FindStringSubmatch(match)[1]
		if m := notesKeyRe.FindStringSubmatch(key); m != nil {
			return notes[m[1]]
		}
		if eventPathRe.MatchString(key) {
			return renderValue(lookupPath(event, strings.Split(key, ".")[1:]))
		}
		return match
	})
	return truncateUTF8(out, MaxInterpolatedBytes)
}

// lookupPath walks nested objects along path; nil when any step is
// missing or not an object.
func lookupPath(v map[string]any, path []string) any {
	var cur any = v
	for _, k := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[k]
	}
	return cur
}

// renderValue renders a scalar with fmt.Sprint and an object or array
// as compact JSON; nil is empty.
func renderValue(v any) string {
	switch v.(type) {
	case nil:
		return ""
	case map[string]any, []any:
		raw, _ := json.Marshal(v)
		return string(raw)
	default:
		return fmt.Sprint(v)
	}
}

// truncateUTF8 cuts s to at most max bytes on a rune boundary.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
