package destinations

import (
	"bytes"
	"html"
	"strings"

	"github.com/yuin/goldmark"
)

// RenderMarkdownHTML converts source (CommonMark) into HTML for an
// email's text/html part — goldmark's own default HTML renderer,
// unlike RenderMarkdownV2 which needs a Telegram-specific format.
// Never panics: goldmark's own Convert doesn't panic on malformed
// input by design, so no fallback path is needed here (unlike
// RenderMarkdownV2, whose custom AST walk has more surface for a
// startling node shape).
func RenderMarkdownHTML(source string) string {
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(source), &buf); err != nil {
		return "<pre>" + html.EscapeString(source) + "</pre>"
	}
	return buf.String()
}

// RenderTextArtifactsHTML renders every .md/.txt artifact as one HTML
// body, each headed by its own name when there's more than one — the
// email adapter's inline-rendering counterpart to Telegram's
// sendTextArtifacts.
func RenderTextArtifactsHTML(artifacts []TextArtifact) string {
	var b strings.Builder
	for i, ta := range artifacts {
		if i > 0 {
			b.WriteString("<hr>\n")
		}
		if len(artifacts) > 1 {
			b.WriteString("<h2>")
			b.WriteString(html.EscapeString(ta.Name))
			b.WriteString("</h2>\n")
		}
		b.WriteString(RenderMarkdownHTML(ta.Content))
	}
	return b.String()
}
