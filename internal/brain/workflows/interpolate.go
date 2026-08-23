package workflows

import "strings"

// outcomeTruncateRunes bounds {{outcome}}'s expansion — the same
// reasoning as missions.OutcomeDigest's own truncation: an unbounded
// digest could blow out prompt size across every downstream step.
const outcomeTruncateRunes = 4000

// interpolate replaces {{outcome}} with outcome (truncated) and
// {{context.KEY}} with context[KEY] in goal. An unknown {{context.KEY}}
// renders as empty and is reported back via unknown, for the caller to
// log as a run event warning — slice-1 minimal, no other placeholder
// forms.
func interpolate(goal, outcome string, context map[string]string) (rendered string, unknown []string) {
	truncated := truncateRunes(outcome, outcomeTruncateRunes)
	rendered = strings.ReplaceAll(goal, "{{outcome}}", truncated)

	var b strings.Builder
	for {
		start := strings.Index(rendered, "{{context.")
		if start == -1 {
			b.WriteString(rendered)
			break
		}
		end := strings.Index(rendered[start:], "}}")
		if end == -1 {
			b.WriteString(rendered)
			break
		}
		end += start
		key := rendered[start+len("{{context.") : end]
		b.WriteString(rendered[:start])
		if v, ok := context[key]; ok {
			b.WriteString(v)
		} else {
			unknown = append(unknown, key)
		}
		rendered = rendered[end+len("}}"):]
	}
	return b.String(), unknown
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
