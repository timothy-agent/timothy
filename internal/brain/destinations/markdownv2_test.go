package destinations

import (
	"strings"
	"testing"
)

func TestRenderMarkdownV2Heading(t *testing.T) {
	got := RenderMarkdownV2("# Title\n\nSome text.")
	if !strings.Contains(got, "*Title*") {
		t.Fatalf("expected bold heading, got %q", got)
	}
	if !strings.Contains(got, "Some text\\.") {
		t.Fatalf("expected escaped paragraph text, got %q", got)
	}
}

func TestRenderMarkdownV2BoldItalic(t *testing.T) {
	got := RenderMarkdownV2("**bold** and *italic* and **bold with _nested italic_ inside**")
	if !strings.Contains(got, "*bold*") {
		t.Fatalf("expected bold span, got %q", got)
	}
	if !strings.Contains(got, "_italic_") {
		t.Fatalf("expected italic span, got %q", got)
	}
	if !strings.Contains(got, "*bold with _nested italic_ inside*") {
		t.Fatalf("expected nested italic-inside-bold preserved, got %q", got)
	}
}

func TestRenderMarkdownV2BulletList(t *testing.T) {
	got := RenderMarkdownV2("- first item\n- second item\n- third item")
	for _, want := range []string{"first item", "second item", "third item"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output, got %q", want, got)
		}
	}
	if strings.Count(got, `\-`) < 3 {
		t.Fatalf("expected an escaped bullet prefix per item, got %q", got)
	}
}

func TestRenderMarkdownV2NumberedList(t *testing.T) {
	got := RenderMarkdownV2("1. alpha\n2. beta\n3. gamma")
	if !strings.Contains(got, `1\.`) || !strings.Contains(got, `2\.`) || !strings.Contains(got, `3\.`) {
		t.Fatalf("expected escaped ordinal prefixes, got %q", got)
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output, got %q", want, got)
		}
	}
}

func TestRenderMarkdownV2Link(t *testing.T) {
	got := RenderMarkdownV2("See [the docs](https://example.com/path) for more.")
	if !strings.Contains(got, "[the docs](https://example.com/path)") {
		t.Fatalf("expected a rendered link, got %q", got)
	}
}

func TestRenderMarkdownV2LinkURLEscaping(t *testing.T) {
	// A literal ")" inside the URL must be escaped with a backslash to
	// avoid CommonMark itself, so goldmark parses the whole destination
	// as one segment ending at the matching unescaped ")".
	got := RenderMarkdownV2(`[x](https://example.com/a\)b)`)
	if !strings.Contains(got, `https://example.com/a\)b`) {
		t.Fatalf("expected the literal ) inside the url escaped, got %q", got)
	}
}

func TestRenderMarkdownV2InlineCode(t *testing.T) {
	got := RenderMarkdownV2("Run `go test ./...` to verify.")
	if !strings.Contains(got, "`go test ./...`") {
		t.Fatalf("expected an inline code span preserved verbatim, got %q", got)
	}
}

func TestRenderMarkdownV2FencedCodeBlock(t *testing.T) {
	got := RenderMarkdownV2("```go\nfmt.Println(\"hi\")\n```")
	if !strings.Contains(got, "```go\nfmt.Println(\"hi\")\n```") {
		t.Fatalf("expected a fenced code block with language tag preserved, got %q", got)
	}
}

func TestRenderMarkdownV2Blockquote(t *testing.T) {
	got := RenderMarkdownV2("> quoted line one\n> quoted line two")
	for _, line := range strings.Split(got, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, ">") {
			t.Fatalf("expected every blockquote line prefixed with '>', got %q in %q", line, got)
		}
	}
	if !strings.Contains(got, "quoted line one") || !strings.Contains(got, "quoted line two") {
		t.Fatalf("expected both quoted lines present, got %q", got)
	}
}

func TestRenderMarkdownV2EscapesLiteralSpecialChars(t *testing.T) {
	got := RenderMarkdownV2("Done! See report (final) - v1.0 results.")
	want := `Done\! See report \(final\) \- v1\.0 results\.`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderMarkdownV2NeverPanics(t *testing.T) {
	inputs := []string{
		"",
		"```unterminated fence\nno closing",
		"[broken link(",
		"**unterminated bold",
		strings.Repeat("*", 5000),
		"\x00\x01 control chars",
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("RenderMarkdownV2(%q) panicked: %v", in, r)
				}
			}()
			_ = RenderMarkdownV2(in)
		}()
	}
}

func TestChunkMarkdownV2RespectsLimit(t *testing.T) {
	// Many short paragraphs, well over the limit in total.
	var paras []string
	for i := 0; i < 300; i++ {
		paras = append(paras, "This is paragraph number "+itoa(i)+" with some filler text to add length.")
	}
	rendered := RenderMarkdownV2(strings.Join(paras, "\n\n"))
	const limit = 500
	chunks := ChunkMarkdownV2(rendered, limit)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for content well over the limit, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > limit {
			t.Fatalf("chunk %d is %d bytes, want <= %d", i, len(c), limit)
		}
	}
	// Lossless reconstruction: every paragraph's distinguishing number
	// must appear somewhere across the chunks.
	joined := strings.Join(chunks, "\n\n")
	for i := 0; i < 300; i++ {
		marker := "paragraph number " + itoa(i) + " "
		if !strings.Contains(joined, marker) {
			t.Fatalf("lost content for paragraph %d after chunking", i)
		}
	}
}

func TestChunkMarkdownV2KeepsEntitiesIntactAcrossBoundary(t *testing.T) {
	// Construct input where a bold span and a link would straddle a
	// naive byte-4096 cut point if chunking didn't respect block
	// boundaries: pad a first block right up to the edge, then a
	// second block containing an entity that would otherwise be split.
	const limit = 100
	first := strings.Repeat("x", limit-10) // leaves ~10 bytes of "room" before the limit
	rendered := RenderMarkdownV2(first + "\n\n**this bold span must stay intact** and [a link](https://example.com/x) too")

	chunks := ChunkMarkdownV2(rendered, limit)
	for _, c := range chunks {
		// An entity split mid-way would leave an unpaired '*' or a '['
		// with no matching '](' on the same chunk. Assert every '*' that
		// starts a bold span in this chunk has its closing partner in
		// the SAME chunk, and any '[' opening a link has its matching
		// '](...)' in the SAME chunk too.
		if n := strings.Count(c, "*"); n%2 != 0 {
			t.Fatalf("chunk has an unpaired bold marker (entity split across a boundary): %q", c)
		}
		if strings.Contains(c, "[") && !strings.Contains(c, "](") {
			t.Fatalf("chunk has an opened link with no closing '](' (entity split across a boundary): %q", c)
		}
	}
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "this bold span must stay intact") || !strings.Contains(joined, "a link") {
		t.Fatalf("lost entity content after chunking: %q", joined)
	}
}

func TestChunkMarkdownV2EmptyInput(t *testing.T) {
	if got := ChunkMarkdownV2("", 4096); got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
}

func TestChunkMarkdownV2SingleOversizeBlockSplitsAtNewline(t *testing.T) {
	// One block (no blank-line separator) that alone exceeds the limit,
	// but has internal newlines (soft line breaks) to split at safely.
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "line "+itoa(i)+" of filler content padded out a bit")
	}
	block := strings.Join(lines, "\n")
	const limit = 200
	chunks := ChunkMarkdownV2(block, limit)
	if len(chunks) < 2 {
		t.Fatalf("expected the oversize block split into multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > limit {
			t.Fatalf("chunk %d is %d bytes, want <= %d", i, len(c), limit)
		}
	}
}
