// Package chunk splits a markdown document into embedding-sized
// chunks for the knowledge base (D-060). Splits happen on heading
// boundaries first, then on paragraph boundaries within an oversized
// section. Every chunk this package returns is bounded to maxTokens:
// a paragraph that alone exceeds the budget is split further (at
// sentence boundaries for prose, at line boundaries for a code fence,
// hard character cut as a last resort) rather than emitted whole.
// Each chunk carries a breadcrumb (document title + heading path)
// prepended to the text actually embedded, so a chunk retrieved out
// of context still reads as "where it came from."
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
// boundaries with overlap. Every returned chunk fits maxTokens, even
// when that requires splitting inside a single oversized paragraph or
// code fence.
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
// overlap between consecutive pieces. A paragraph bigger than maxTokens
// on its own (a huge prose block or a huge code fence) is split further
// before packing, so every returned piece still fits maxTokens: that
// bound matters more than keeping a fence visually intact.
func splitOversized(body string) []string {
	if estimateTokens(body) <= maxTokens {
		return []string{body}
	}

	var paras []string
	for _, p := range paragraphs(body) {
		if estimateTokens(p) > maxTokens {
			paras = append(paras, splitOversizedParagraph(p)...)
		} else {
			paras = append(paras, p)
		}
	}

	return packPieces(paras, "\n\n")
}

// packPieces packs items into <= targetTokens pieces joined by sep,
// carrying a bounded tail-overlap into the next piece. It never lets a
// piece exceed maxTokens: an item is only carried as overlap when doing
// so still leaves room under maxTokens for at least the next item, and
// a single item already at or over maxTokens is pushed out on its own.
func packPieces(items []string, sep string) []string {
	var pieces []string
	var cur []string
	curTokens := 0

	pushPiece := func() {
		if len(cur) == 0 {
			return
		}
		pieces = append(pieces, strings.Join(cur, sep))
	}

	for _, p := range items {
		pt := estimateTokens(p)
		if curTokens > 0 && curTokens+pt > targetTokens {
			pushPiece()
			// Overlap: carry the tail items of the just-closed piece
			// into the next one, up to ~overlapRatio of target, and
			// never past what leaves room for pt under maxTokens.
			overlapBudget := int(float64(targetTokens) * overlapRatio)
			if room := maxTokens - pt; room < overlapBudget {
				overlapBudget = room
			}
			var carry []string
			carryTokens := 0
			for i := len(cur) - 1; i >= 0; i-- {
				it := estimateTokens(cur[i])
				if carryTokens+it > overlapBudget {
					break
				}
				carry = append([]string{cur[i]}, carry...)
				carryTokens += it
			}
			cur = carry
			curTokens = carryTokens
		}
		cur = append(cur, p)
		curTokens += pt
		if curTokens > maxTokens {
			// A lone oversized item (or overlap pushed past budget):
			// close this piece immediately rather than risk growing it
			// further on the next iteration.
			pushPiece()
			cur = nil
			curTokens = 0
		}
	}
	pushPiece()
	return pieces
}

// splitOversizedParagraph breaks a single paragraph bigger than
// maxTokens into pieces that each fit maxTokens, so the caller's
// packing loop never has to swallow one oversized paragraph whole.
func splitOversizedParagraph(p string) []string {
	if isFence(p) {
		return splitFence(p)
	}
	return splitSentences(p)
}

// isFence reports whether p is a fenced code block (paragraphs() keeps
// a fence as a single paragraph, opening and closing marker included).
func isFence(p string) bool {
	trimmed := strings.TrimSpace(p)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// splitFence breaks an oversized fenced code block into <= targetTokens
// pieces at line boundaries, re-opening the fence marker in each
// continuation piece so every stored chunk still renders as code. This
// is the simplest honest approach: it duplicates the marker rather than
// tracking the original language tag or reconstructing one true fence.
// A single line still over maxTokens on its own is hard-cut.
func splitFence(p string) []string {
	lines := strings.Split(strings.TrimRight(p, "\n"), "\n")
	if len(lines) < 2 {
		return splitByRunes(p) // degenerate: no room for a boundary line
	}
	marker := strings.TrimSpace(lines[0])
	inner := lines[1 : len(lines)-1]

	// The marker is re-added to every piece, so pack against a budget
	// that leaves room for it.
	markerTokens := estimateTokens(marker) * 2
	packed := packPieces(inner, "\n")

	var pieces []string
	for _, piece := range packed {
		if estimateTokens(piece)+markerTokens > maxTokens {
			// Packed under target but the marker overhead tips it over
			// maxTokens: fall back to a hard cut of the whole wrapped
			// piece rather than risk shipping an over-budget chunk.
			pieces = append(pieces, splitByRunes(marker+"\n"+piece+"\n"+marker)...)
			continue
		}
		pieces = append(pieces, marker+"\n"+piece+"\n"+marker)
	}
	return pieces
}

// sentenceEnds are byte sequences after which a sentence may be split:
// standard terminators followed by a space, or a bare newline.
var sentenceEnds = []string{". ", "! ", "? ", "; "}

// splitIntoSentences breaks s into pieces ending at a sentence boundary
// (a terminator + space, or a newline), keeping the terminator attached
// to the sentence it closes.
func splitIntoSentences(s string) []string {
	var sentences []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			sentences = append(sentences, s[start:i+1])
			start = i + 1
			continue
		}
		for _, end := range sentenceEnds {
			if strings.HasPrefix(s[i:], end) {
				cut := i + len(end)
				sentences = append(sentences, s[start:cut])
				start = cut
				i = cut - 1
				break
			}
		}
	}
	if start < len(s) {
		sentences = append(sentences, s[start:])
	}
	return sentences
}

// splitSentences packs an oversized plain paragraph's sentences into
// <= targetTokens pieces, with the same tail-overlap semantics as the
// paragraph packer. A single sentence still over maxTokens is hard-cut
// at a rune boundary as a last resort.
func splitSentences(p string) []string {
	var sentences []string
	for _, s := range splitIntoSentences(p) {
		if estimateTokens(s) > maxTokens {
			sentences = append(sentences, splitByRunes(s)...)
		} else {
			sentences = append(sentences, s)
		}
	}
	return packPieces(sentences, "")
}

// splitByRunes hard-cuts s into <= maxTokens pieces at rune boundaries.
// Last resort for a single sentence (or fence line) too big to split
// any other way; never cuts inside a multi-byte UTF-8 rune. Cuts by
// byte budget (estimateTokens is chars/4, i.e. bytes/4) so a run of
// multi-byte runes doesn't push a piece over maxTokens.
func splitByRunes(s string) []string {
	maxBytes := maxTokens * 4
	if maxBytes < 1 {
		maxBytes = 1
	}
	var pieces []string
	runes := []rune(s)
	for len(runes) > 0 {
		n := 0
		byteLen := 0
		for n < len(runes) {
			rl := len(string(runes[n]))
			if n > 0 && byteLen+rl > maxBytes {
				break
			}
			byteLen += rl
			n++
		}
		if n == 0 {
			n = 1 // a single rune wider than maxBytes still must advance
		}
		pieces = append(pieces, string(runes[:n]))
		runes = runes[n:]
	}
	return pieces
}
