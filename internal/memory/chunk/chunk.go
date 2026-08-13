// Package chunk splits a markdown document into embedding-sized
// chunks for the knowledge base (D-060). Splits happen on heading
// boundaries first, then on paragraph boundaries within an oversized
// section; a code fence is never split. Each chunk carries a
// breadcrumb (document title + heading path) prepended to the text
// actually embedded, so a chunk retrieved out of context still reads
// as "where it came from."
package chunk

import (
	"strings"
)

const (
	// targetTokens is the chunk size this package aims for.
	targetTokens = 500
	// maxTokens forces a section over this size to split further.
	maxTokens = 600
	// overlapRatio is how much of a split section's tail repeats at
	// the head of the next chunk, so a fact spanning the cut isn't
	// stranded in neither chunk.
	overlapRatio = 0.15
)

// estimateTokens is a cheap chars/4 approximation — good enough to
// size chunks, never used for billing.
func estimateTokens(s string) int {
	return len(s) / 4
}

// Chunk is one piece of a document, ready to embed and store.
// Breadcrumb is kept separate from Content: callers embed
// Breadcrumb+"\n\n"+Content but store them as distinct columns.
type Chunk struct {
	Seq        int
	Breadcrumb string
	Content    string
}

// heading is one parsed markdown heading line.
type heading struct {
	level int
	text  string
}

// parseHeading returns the heading level and text if line is an ATX
// heading ("# ", "## ", ...), or ok=false otherwise. Headings inside a
// fenced code block are handled by the caller, not here.
func parseHeading(line string) (heading, bool) {
	trimmed := strings.TrimRight(line, " \t")
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return heading{}, false
	}
	if level == len(trimmed) {
		return heading{level: level, text: ""}, true
	}
	if trimmed[level] != ' ' && trimmed[level] != '\t' {
		return heading{}, false
	}
	return heading{level: level, text: strings.TrimSpace(trimmed[level:])}, true
}

// section is one heading-delimited slice of the document, with the
// heading path (title excluded) that leads to it.
type section struct {
	path []string
	body string
}

// split walks the document line by line, tracking fence state so a
// "#" inside a code block is never mistaken for a heading, and the
// current heading path (one entry per active level).
func split(markdown string) []section {
	lines := strings.Split(markdown, "\n")
	var sections []section
	var path []string
	var body strings.Builder
	inFence := false

	flush := func() {
		if strings.TrimSpace(body.String()) != "" {
			sections = append(sections, section{path: append([]string(nil), path...), body: body.String()})
		}
		body.Reset()
	}

	for _, line := range lines {
		fenceLine := strings.HasPrefix(strings.TrimSpace(line), "```") || strings.HasPrefix(strings.TrimSpace(line), "~~~")
		if fenceLine {
			inFence = !inFence
			body.WriteString(line)
			body.WriteString("\n")
			continue
		}
		if !inFence {
			if h, ok := parseHeading(line); ok {
				flush()
				if h.level > len(path) {
					for len(path) < h.level-1 {
						path = append(path, "")
					}
					path = append(path, h.text)
				} else {
					path = append(path[:h.level-1], h.text)
				}
				continue
			}
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	flush()
	return sections
}

// breadcrumb renders "Title > H2 > H3", skipping empty path entries
// (a heading level jumped over, e.g. H1 then H3 directly).
func breadcrumb(title string, path []string) string {
	parts := []string{}
	if title != "" {
		parts = append(parts, title)
	}
	for _, p := range path {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " > ")
}

// Split chunks markdown into embedding-sized pieces. title names the
// document (the breadcrumb's first segment). A section within budget
// becomes one chunk; an oversized section splits at paragraph
// boundaries with overlap, never inside a code fence.
func Split(title, markdown string) []Chunk {
	var out []Chunk
	for _, sec := range split(markdown) {
		bc := breadcrumb(title, sec.path)
		body := strings.TrimSpace(sec.body)
		if body == "" {
			continue
		}
		for _, piece := range splitOversized(body) {
			out = append(out, Chunk{Breadcrumb: bc, Content: piece})
		}
	}
	for i := range out {
		out[i].Seq = i
	}
	return out
}

// paragraphs splits body on blank lines, keeping any fenced code block
// intact as a single paragraph regardless of blank lines inside it.
func paragraphs(body string) []string {
	lines := strings.Split(body, "\n")
	var out []string
	var cur strings.Builder
	inFence := false

	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}

	for _, line := range lines {
		fenceLine := strings.HasPrefix(strings.TrimSpace(line), "```") || strings.HasPrefix(strings.TrimSpace(line), "~~~")
		if fenceLine {
			inFence = !inFence
			cur.WriteString(line)
			cur.WriteString("\n")
			continue
		}
		if !inFence && strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	flush()
	return out
}

// splitOversized returns body as-is when it fits maxTokens, else packs
// its paragraphs into ~targetTokens pieces with a trailing-paragraph
// overlap between consecutive pieces. A single paragraph bigger than
// maxTokens (e.g. one huge code fence) is kept whole — never split
// inside a fence.
func splitOversized(body string) []string {
	if estimateTokens(body) <= maxTokens {
		return []string{body}
	}

	paras := paragraphs(body)
	var pieces []string
	var cur []string
	curTokens := 0

	pushPiece := func() {
		if len(cur) == 0 {
			return
		}
		pieces = append(pieces, strings.Join(cur, "\n\n"))
	}

	for _, p := range paras {
		pt := estimateTokens(p)
		if curTokens > 0 && curTokens+pt > targetTokens {
			pushPiece()
			// Overlap: carry the tail paragraphs of the just-closed
			// piece into the next one, up to ~overlapRatio of target.
			overlapBudget := int(float64(targetTokens) * overlapRatio)
			var carry []string
			carryTokens := 0
			for i := len(cur) - 1; i >= 0 && carryTokens < overlapBudget; i-- {
				carry = append([]string{cur[i]}, carry...)
				carryTokens += estimateTokens(cur[i])
			}
			cur = carry
			curTokens = carryTokens
		}
		cur = append(cur, p)
		curTokens += pt
	}
	pushPiece()
	return pieces
}
