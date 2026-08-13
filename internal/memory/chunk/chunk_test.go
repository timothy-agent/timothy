package chunk

import (
	"strings"
	"testing"
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

func TestSplitKeepsCodeFenceIntact(t *testing.T) {
	t.Parallel()
	fence := "```go\nfunc main() {\n" + strings.Repeat("\tfmt.Println(\"line\")\n", 200) + "}\n```"
	md := "## Code\n" + fence + "\n"
	chunks := Split("Doc", md)
	for _, c := range chunks {
		openers := strings.Count(c.Content, "```")
		if openers%2 != 0 {
			t.Errorf("chunk has an unmatched code fence: %q", c.Content[:min(200, len(c.Content))])
		}
	}
	// The fence must appear whole in at least one chunk.
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "func main()") || !strings.Contains(joined, "}\n```") {
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
