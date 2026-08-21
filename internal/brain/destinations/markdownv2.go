package destinations

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// RenderMarkdownV2 parses source as CommonMark and converts it into
// Telegram's MarkdownV2 wire format: headings become bold lines (no
// native heading syntax), list items become escaped-bullet/number
// prefixed lines, fenced/inline code stay fence-literal, and every
// other literal text run is escaped via escapeMarkdownV2. Never
// panics — a source goldmark can't parse cleanly, or an unrecognized
// node the renderer can't safely convert, falls back to treating the
// whole input as literal plain text.
func RenderMarkdownV2(source string) string {
	blocks, ok := renderBlocksSafe(source)
	if !ok {
		return escapeMarkdownV2(source)
	}
	return strings.Join(blocks, "\n\n")
}

// ChunkMarkdownV2 splits an already-rendered MarkdownV2 string into
// chunks each <= limit bytes, splitting only between the top-level
// blocks RenderMarkdownV2 joined with "\n\n" — never inside one, so a
// chunk boundary can never land inside an unescaped entity (a bold
// span, a link, a code fence). A single block that itself exceeds
// limit is a last-resort exception: it's split at the last safe
// newline, or failing that a rune boundary, within that block alone.
func ChunkMarkdownV2(rendered string, limit int) []string {
	if rendered == "" {
		return nil
	}
	blocks := strings.Split(rendered, "\n\n")
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
	}
	for _, b := range blocks {
		if len(b) > limit {
			flush()
			chunks = append(chunks, splitOversizeBlock(b, limit)...)
			continue
		}
		sep := 0
		if cur.Len() > 0 {
			sep = len("\n\n")
		}
		if cur.Len()+sep+len(b) > limit {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(b)
	}
	flush()
	return chunks
}

// splitOversizeBlock is ChunkMarkdownV2's last resort for a single
// block that alone exceeds limit: split at the last newline within
// budget, or a rune boundary if the block has no newline to use
// either — same rune-safe cutting technique as truncateMarkdownV2.
func splitOversizeBlock(b string, limit int) []string {
	var out []string
	for len(b) > limit {
		cut := strings.LastIndex(b[:limit+1], "\n")
		if cut <= 0 {
			cut = runeSafeCut(b, limit)
		}
		out = append(out, b[:cut])
		b = strings.TrimPrefix(b[cut:], "\n")
	}
	if b != "" {
		out = append(out, b)
	}
	return out
}

// runeSafeCut returns the largest byte offset <= limit that does not
// split a multi-byte rune — same technique as truncateMarkdownV2.
func runeSafeCut(s string, limit int) int {
	cut := 0
	for i, r := range s {
		if i+len(string(r)) > limit {
			break
		}
		cut = i + len(string(r))
	}
	if cut == 0 && len(s) > 0 {
		cut = len(s) // pathological: limit smaller than one rune; avoid an infinite loop upstream
	}
	return cut
}

// renderBlocksSafe walks source's parsed AST into one MarkdownV2
// string per top-level block node. ok is false if goldmark's parse
// (or the walk) hits anything the renderer can't safely handle —
// RenderMarkdownV2 falls back to plain-text escaping in that case.
func renderBlocksSafe(source string) (blocks []string, ok bool) {
	defer func() {
		if recover() != nil {
			blocks, ok = nil, false
		}
	}()
	src := []byte(source)
	doc := goldmark.DefaultParser().Parse(text.NewReader(src))
	if doc == nil {
		return nil, false
	}
	out := make([]string, 0, doc.ChildCount())
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		out = append(out, renderBlock(n, src))
	}
	return out, true
}

// renderBlock renders one top-level (or nested-container) block node
// to its MarkdownV2 string, without a trailing block separator — the
// caller joins siblings with "\n\n".
func renderBlock(n ast.Node, src []byte) string {
	switch node := n.(type) {
	case *ast.Paragraph:
		return renderInlineChildren(node, src)
	case *ast.Heading:
		return "*" + renderInlineChildren(node, src) + "*"
	case *ast.ThematicBreak:
		return escapeMarkdownV2("---")
	case *ast.Blockquote:
		return renderBlockquote(node, src)
	case *ast.CodeBlock:
		return renderCodeFence("", segmentsValue(node, src))
	case *ast.FencedCodeBlock:
		return renderCodeFence(string(node.Language(src)), segmentsValue(node, src))
	case *ast.List:
		return renderList(node, src)
	case *ast.HTMLBlock:
		return escapeMarkdownV2(strings.TrimRight(string(segmentsValue(node, src)), "\n"))
	default:
		// Any other/unknown block kind (e.g. an extension node this
		// renderer doesn't special-case): render its inline content as
		// plain text rather than dropping it silently.
		return renderInlineChildren(node, src)
	}
}

// segmentsValue reads a block node's raw source lines (CodeBlock/
// FencedCodeBlock/HTMLBlock content — these are IsRaw() nodes with no
// inline children, only Lines()).
func segmentsValue(n ast.Node, src []byte) []byte {
	return n.Lines().Value(src)
}

// renderCodeFence wraps content in a MarkdownV2 code fence: the
// content between fences is fence-literal (Telegram does not
// interpret it), but a literal "```" inside would still break out of
// the fence, so that sequence alone is neutralized.
func renderCodeFence(language string, content []byte) string {
	body := strings.ReplaceAll(string(content), "```", "``\u200b`")
	body = strings.TrimRight(body, "\n")
	return "```" + language + "\n" + body + "\n```"
}

// renderBlockquote prefixes every source line of the quote with ">",
// per MarkdownV2's per-line blockquote syntax — nested block children
// are rendered normally first, then each resulting line gets the
// prefix.
func renderBlockquote(n *ast.Blockquote, src []byte) string {
	var inner []string
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		inner = append(inner, renderBlock(c, src))
	}
	joined := strings.Join(inner, "\n\n")
	lines := strings.Split(joined, "\n")
	for i, l := range lines {
		lines[i] = ">" + l
	}
	return strings.Join(lines, "\n")
}

// renderList renders every item as one escaped-bullet ("•" for an
// unordered list) or escaped-number ("N\.") prefixed line — Telegram's
// MarkdownV2 has no native list syntax, unlike CommonMark.
func renderList(n *ast.List, src []byte) string {
	var lines []string
	ordered := n.IsOrdered()
	num := n.Start
	if num == 0 {
		num = 1
	}
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		li, is := item.(*ast.ListItem)
		if !is {
			continue
		}
		var parts []string
		for c := li.FirstChild(); c != nil; c = c.NextSibling() {
			parts = append(parts, renderBlock(c, src))
		}
		content := strings.Join(parts, "\n")
		var prefix string
		if ordered {
			prefix = escapeMarkdownV2(itoa(num) + ".") + " "
			num++
		} else {
			prefix = escapeMarkdownV2("-") + " "
		}
		// A multi-line item (nested block content) gets its continuation
		// lines aligned under the prefix's text, not re-prefixed.
		itemLines := strings.Split(content, "\n")
		itemLines[0] = prefix + itemLines[0]
		lines = append(lines, itemLines...)
	}
	return strings.Join(lines, "\n")
}

// itoa avoids importing strconv for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// renderInlineChildren walks n's inline children (the common shape for
// Paragraph/Heading/ListItem-wrapped-paragraph content) into one
// MarkdownV2 string.
func renderInlineChildren(n ast.Node, src []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		b.WriteString(renderInline(c, src))
	}
	return b.String()
}

// renderInline renders one inline node (and, for containers like
// Emphasis/Link, its own inline children) to MarkdownV2.
func renderInline(n ast.Node, src []byte) string {
	switch node := n.(type) {
	case *ast.Text:
		text := string(node.Segment.Value(src))
		out := escapeMarkdownV2(text)
		if node.SoftLineBreak() || node.HardLineBreak() {
			out += "\n"
		}
		return out
	case *ast.String:
		return escapeMarkdownV2(string(node.Value))
	case *ast.CodeSpan:
		return "`" + strings.ReplaceAll(collectRawInline(node, src), "`", "'") + "`"
	case *ast.Emphasis:
		marker := "_"
		if node.Level >= 2 {
			marker = "*"
		}
		return marker + renderInlineChildren(node, src) + marker
	case *ast.Link:
		return "[" + renderInlineChildren(node, src) + "](" + escapeLinkURL(string(node.Destination)) + ")"
	case *ast.AutoLink:
		u := string(node.URL(src))
		return "[" + escapeMarkdownV2(u) + "](" + escapeLinkURL(u) + ")"
	case *ast.Image:
		// No native image syntax in a text message: render as a link to
		// the image so the content is never silently dropped.
		return "[" + renderInlineChildren(node, src) + "](" + escapeLinkURL(string(node.Destination)) + ")"
	default:
		return renderInlineChildren(node, src)
	}
}

// collectRawInline reads a fence-literal inline node's (CodeSpan's)
// raw text from its Text children's segments, unescaped.
func collectRawInline(n ast.Node, src []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, is := c.(*ast.Text); is {
			b.Write(t.Segment.Value(src))
		}
	}
	return b.String()
}

// escapeLinkURL escapes only what MarkdownV2 requires inside a link's
// URL portion: ")" and "\" — the URL is not "literal text" and must
// not go through escapeMarkdownV2's full character set. u is goldmark's
// raw Destination bytes, which preserve the source's own CommonMark
// backslash-escapes verbatim (unlike Text nodes, a link destination is
// not unescaped by the parser) — unescapeCommonMarkBackslashes
// resolves those first so re-escaping for MarkdownV2 never doubles up
// an escape that was already there in the source.
func escapeLinkURL(u string) string {
	u = unescapeCommonMarkBackslashes(u)
	u = strings.ReplaceAll(u, `\`, `\\`)
	u = strings.ReplaceAll(u, `)`, `\)`)
	return u
}

// unescapeCommonMarkBackslashes resolves a backslash followed by an
// ASCII punctuation character back to the bare character — the same
// rule CommonMark's own backslash-escape spec applies, needed here
// because goldmark leaves a link destination's raw escapes untouched.
func unescapeCommonMarkBackslashes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]) {
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isASCIIPunct(c byte) bool {
	return strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", rune(c))
}
