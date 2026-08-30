package chunk

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitHeadingNesting(t *testing.T) {
	t.Parallel()
	md := "Intro text.\n\n" +
		"## Section A\n" +
		"A body.\n\n" +
		"### Subsection A.1\n" +
		"A1 body.\n\n" +
		"## Section B\n" +
		"B body.\n"

	chunks := Split("Doc Title", md)
	if len(chunks) != 4 {
		t.Fatalf("got %d chunks, want 4: %+v", len(chunks), chunks)
	}
	want := []string{
		"Doc Title",
		"Doc Title > Section A",
		"Doc Title > Section A > Subsection A.1",
		"Doc Title > Section B",
	}
	for i, w := range want {
		if chunks[i].Breadcrumb != w {
			t.Errorf("chunk %d breadcrumb = %q, want %q", i, chunks[i].Breadcrumb, w)
		}
	}
}

func TestSplitSkipsHeadingLevelJump(t *testing.T) {
	t.Parallel()
	// H1 then H3 directly: the H2 slot must not leave a stray empty
	// segment in the breadcrumb.
	md := "# Title\nintro\n\n### Deep\nbody\n"
	chunks := Split("Doc", md)
	found := false
	for _, c := range chunks {
		if strings.Contains(c.Breadcrumb, "Deep") {
			found = true
			if strings.Contains(c.Breadcrumb, " >  > ") {
				t.Errorf("breadcrumb has an empty segment: %q", c.Breadcrumb)
			}
		}
	}
	if !found {
		t.Fatal("no chunk carried the Deep heading")
	}
}

func TestSplitLongSectionOverlaps(t *testing.T) {
	t.Parallel()
	// Build a section well past maxTokens (600 tokens ≈ 2400 chars) out
	// of many small paragraphs so it must split, and check consecutive
	// pieces share at least one paragraph (the overlap).
	var b strings.Builder
	b.WriteString("## Big Section\n")
	paras := make([]string, 40)
	for i := range paras {
		paras[i] = strings.Repeat("word", 20) + " para" + string(rune('a'+i%26))
		b.WriteString(paras[i])
		b.WriteString("\n\n")
	}
	chunks := Split("Doc", b.String())
	if len(chunks) < 2 {
		t.Fatalf("expected the oversized section to split into multiple chunks, got %d", len(chunks))
	}
	for i := 0; i < len(chunks)-1; i++ {
		if chunks[i].Breadcrumb != chunks[i+1].Breadcrumb {
			continue // different section, no overlap expected
		}
		tailOfCur := lastParagraph(chunks[i].Content)
		if !strings.Contains(chunks[i+1].Content, tailOfCur) {
			t.Errorf("chunk %d and %d (same section) share no overlap paragraph", i, i+1)
		}
	}
}

func lastParagraph(s string) string {
	parts := strings.Split(strings.TrimSpace(s), "\n\n")
	return parts[len(parts)-1]
}

func TestSplitSmallFenceStaysIntact(t *testing.T) {
	t.Parallel()
	fence := "```go\nfunc main() {\n\tfmt.Println(\"line\")\n}\n```"
	md := "## Code\n" + fence + "\n"
	chunks := Split("Doc", md)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "func main()") {
		t.Fatal("fence content lost")
	}
	if openers := strings.Count(chunks[0].Content, "```"); openers%2 != 0 {
		t.Errorf("chunk has an unmatched code fence")
	}
}

func TestSplitOversizedFenceIsBounded(t *testing.T) {
	t.Parallel()
	fence := "```go\nfunc main() {\n" + strings.Repeat("\tfmt.Println(\"line\")\n", 400) + "}\n```"
	md := "## Code\n" + fence + "\n"
	chunks := Split("Doc", md)
	if len(chunks) < 2 {
		t.Fatalf("expected the oversized fence to split, got %d chunks", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		if got := estimateTokens(c.Content); got > maxTokens {
			t.Errorf("chunk exceeds maxTokens: %d > %d", got, maxTokens)
		}
		openers := strings.Count(c.Content, "```")
		if openers%2 != 0 {
			t.Errorf("chunk has an unmatched code fence: %q", c.Content[:min(80, len(c.Content))])
		}
		if !strings.HasPrefix(strings.TrimSpace(c.Content), "```") {
			t.Errorf("chunk piece does not reopen the fence marker: %q", c.Content[:min(40, len(c.Content))])
		}
		joined += c.Content
	}
	if !strings.Contains(joined, "func main()") || !strings.Contains(joined, "}") {
		t.Fatal("fence content lost across the split")
	}
}

func TestSplitTinyDocument(t *testing.T) {
	t.Parallel()
	chunks := Split("Doc", "just one short line")
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	if chunks[0].Breadcrumb != "Doc" {
		t.Errorf("breadcrumb = %q, want %q", chunks[0].Breadcrumb, "Doc")
	}
	if chunks[0].Content != "just one short line" {
		t.Errorf("content = %q", chunks[0].Content)
	}
}

func TestSplitEmptyDocument(t *testing.T) {
	t.Parallel()
	chunks := Split("Doc", "")
	if len(chunks) != 0 {
		t.Fatalf("got %d chunks for empty doc, want 0", len(chunks))
	}
}

func TestSplitSeqIsSequential(t *testing.T) {
	t.Parallel()
	md := "## A\nbody a\n\n## B\nbody b\n\n## C\nbody c\n"
	chunks := Split("Doc", md)
	for i, c := range chunks {
		if c.Seq != i {
			t.Errorf("chunk %d has Seq %d", i, c.Seq)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestSplitGiantUnbrokenParagraph(t *testing.T) {
	t.Parallel()
	// A single paragraph, no blank lines, built from many sentences.
	// This is the arxiv-via-markitdown shape that broke ingest.
	var b strings.Builder
	for i := 0; i < 800; i++ {
		b.WriteString("This is sentence number ")
		b.WriteString(strings.Repeat("x", 5))
		b.WriteString(" in a very long unbroken block of prose. ")
	}
	md := "## Section\n" + b.String() + "\n"

	chunks := Split("Doc", md)
	if len(chunks) < 2 {
		t.Fatalf("expected the giant paragraph to split, got %d chunks", len(chunks))
	}
	for i, c := range chunks {
		if got := estimateTokens(c.Content); got > maxTokens {
			t.Errorf("chunk %d exceeds maxTokens: %d > %d", i, got, maxTokens)
		}
		if !strings.HasSuffix(strings.TrimSpace(c.Content), ".") {
			// Sentence-boundary splits should end on a terminator.
			t.Errorf("chunk %d does not end on a sentence boundary: %q", i, c.Content[max(0, len(c.Content)-40):])
		}
	}
	// Consecutive chunks in the same section should overlap.
	overlapFound := false
	for i := 0; i < len(chunks)-1; i++ {
		if chunks[i].Breadcrumb != chunks[i+1].Breadcrumb {
			continue
		}
		tail := lastSentence(chunks[i].Content)
		if strings.Contains(chunks[i+1].Content, tail) {
			overlapFound = true
		}
	}
	if !overlapFound {
		t.Error("expected at least one overlapping sentence between consecutive chunks")
	}
}

func lastSentence(s string) string {
	s = strings.TrimSpace(s)
	idx := strings.LastIndex(s, ". ")
	if idx == -1 || idx+2 >= len(s) {
		return s
	}
	return s[idx+2:]
}

func TestSplitGiantSentenceHardCutsAtRuneBoundary(t *testing.T) {
	t.Parallel()
	// One sentence, no spaces or punctuation to split on, well past
	// maxTokens, containing multi-byte runes throughout so a byte-index
	// cut would corrupt UTF-8 if it landed mid-rune.
	word := strings.Repeat("日本語ab", 1000) // mixes multi-byte and ASCII
	md := "## Section\n" + word + ".\n"

	chunks := Split("Doc", md)
	if len(chunks) < 2 {
		t.Fatalf("expected the giant sentence to hard-cut, got %d chunks", len(chunks))
	}
	for i, c := range chunks {
		if got := estimateTokens(c.Content); got > maxTokens {
			t.Errorf("chunk %d exceeds maxTokens: %d > %d", i, got, maxTokens)
		}
		if !utf8.ValidString(c.Content) {
			t.Errorf("chunk %d is not valid UTF-8 (mid-rune cut)", i)
		}
	}
	// Every rune of the original word must survive across the chunks.
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "日本語ab日本語ab") {
		t.Error("multi-byte content lost or reordered across the hard cut")
	}
}

func TestSplitInvariantAllChunksFitMaxTokens(t *testing.T) {
	t.Parallel()
	// Property-style check over adversarial shapes: no chunk Split
	// returns may ever exceed maxTokens, regardless of input shape.
	giantParagraph := strings.Repeat("word ", 8000) // no punctuation, no blank lines
	giantSentencePara := strings.Repeat("a", 40000) + "."
	giantFence := "```text\n" + strings.Repeat("line of code here\n", 3000) + "```"
	mixed := giantParagraph + "\n\n" + giantFence + "\n\n" + "short paragraph.\n\n" + giantSentencePara

	cases := map[string]string{
		"giant plain paragraph": "## S\n" + giantParagraph + "\n",
		"giant single sentence": "## S\n" + giantSentencePara + "\n",
		"giant fence":           "## S\n" + giantFence + "\n",
		"mixed":                 "## S\n" + mixed + "\n",
	}

	for name, md := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			chunks := Split("Doc", md)
			if len(chunks) == 0 {
				t.Fatal("got no chunks")
			}
			for i, c := range chunks {
				if got := estimateTokens(c.Content); got > maxTokens {
					t.Errorf("chunk %d exceeds maxTokens: %d > %d (len=%d)", i, got, maxTokens, len(c.Content))
				}
				if !utf8.ValidString(c.Content) {
					t.Errorf("chunk %d is not valid UTF-8", i)
				}
			}
		})
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
