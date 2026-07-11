package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultOffloadThreshold is the size above which a tool result is
// offloaded (D-019): roughly 2k tokens of text.
const DefaultOffloadThreshold = 8 << 10

const (
	digestHeadLines  = 30
	digestTailLines  = 10
	digestErrorLines = 5
	digestLineCap    = 200 // runes per quoted line
)

var errorLineRe = regexp.MustCompile(`(?i)\b(error|fail|failed|failure|panic|fatal|exception|denied)\b`)

// Digest builds the deterministic stand-in for an offloaded result:
// counts, a head/tail excerpt, error lines, and the retrieval ref.
// Pure string work — never an LLM call.
func Digest(content, tool, ref string) string {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "[%s output offloaded: %d bytes, %d lines. Full content: retrieve_output {\"ref\": %q}]\n",
		tool, len(content), len(lines), ref)

	head := lines
	truncated := false
	if len(lines) > digestHeadLines+digestTailLines {
		head = lines[:digestHeadLines]
		truncated = true
	}
	b.WriteString("\n--- first lines ---\n")
	for _, l := range head {
		b.WriteString(capLine(l))
		b.WriteByte('\n')
	}
	if truncated {
		tail := lines[len(lines)-digestTailLines:]
		fmt.Fprintf(&b, "\n--- ... %d lines omitted ... ---\n", len(lines)-digestHeadLines-digestTailLines)
		b.WriteString("--- last lines ---\n")
		for _, l := range tail {
			b.WriteString(capLine(l))
			b.WriteByte('\n')
		}
	}

	var errs []string
	for i, l := range lines {
		if i < digestHeadLines || i >= len(lines)-digestTailLines {
			continue // already shown
		}
		if errorLineRe.MatchString(l) {
			errs = append(errs, fmt.Sprintf("line %d: %s", i+1, capLine(l)))
			if len(errs) == digestErrorLines {
				break
			}
		}
	}
	if len(errs) > 0 {
		b.WriteString("\n--- error-looking lines ---\n")
		b.WriteString(strings.Join(errs, "\n"))
		b.WriteByte('\n')
	}
	return b.String()
}

func capLine(l string) string {
	runes := []rune(l)
	if len(runes) <= digestLineCap {
		return l
	}
	return string(runes[:digestLineCap]) + "…"
}
