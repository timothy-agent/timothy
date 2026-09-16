// Package trustfence wraps untrusted content in a data-trust fence.
//
// D-011 established the mechanism for retrieved memories: a tagged
// block declaring trust="data" plus an escape that keeps the content
// from spelling the closing tag. D-107 single-sources it here so every
// untrusted channel (memories, fetched pages, search results, KB
// passages, mail bodies, converted documents) shares one framing and
// one escape instead of each growing its own.
package trustfence

import (
	"regexp"
	"strings"
)

// Tags the fence is used with. A tag is call-site code, never content.
const (
	TagMemory  = "memory"
	TagUntrust = "untrusted_content"
)

// Open is the fence opener for tag: the trust="data" attribute, the
// source naming the channel, and the data-not-instructions preamble.
func Open(tag, source, preamble string) string {
	return `<` + tag + ` source="` + source + `" trust="data">` + "\n" + preamble
}

// Close ends a fence opened with the same tag.
func Close(tag string) string {
	return `</` + tag + `>`
}

// closePattern matches any spelling of a closing tag the content could
// smuggle in, case variants and embedded whitespace included
// ("</MEMORY>", "</ memory", "< / Memory"). A plain lowercase string
// replace is not a poisoning defense.
var closePattern = map[string]*regexp.Regexp{
	TagMemory:  regexp.MustCompile(`(?i)<\s*/\s*` + TagMemory),
	TagUntrust: regexp.MustCompile(`(?i)<\s*/\s*` + TagUntrust),
}

// Escape neutralizes closing-tag lookalikes so content can never
// terminate the fence. An unknown tag escapes nothing, which would be
// a fence that content can break out of, so it panics instead.
func Escape(tag, s string) string {
	re, ok := closePattern[tag]
	if !ok {
		panic("trustfence: unknown tag " + tag)
	}
	return re.ReplaceAllString(s, "&lt;/"+tag)
}

// Wrap fences content: opener, escaped content, closer.
func Wrap(tag, source, preamble, content string) string {
	var b strings.Builder
	b.WriteString(Open(tag, source, preamble))
	b.WriteString(Escape(tag, content))
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(Close(tag))
	return b.String()
}
