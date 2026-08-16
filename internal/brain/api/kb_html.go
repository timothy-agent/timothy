package api

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// kbChromeTags are stripped unconditionally (never carry article
// content), except <header>/<footer> which need the sectioning-parent
// check in isPageChrome below.
var kbChromeTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"iframe": true, "nav": true, "aside": true,
}

// kbChromeRoles are ARIA landmark roles that mark page navigation/
// boilerplate rather than article content.
var kbChromeRoles = map[string]bool{
	"navigation": true, "banner": true, "contentinfo": true, "complementary": true,
}

// kbSectioningTags bound how far up the tree a <header>/<footer> looks
// for its nearest sectioning ancestor (HTML5 allows nested headers
// inside <article>/<section>, which are content, not page chrome).
var kbSectioningTags = map[string]bool{
	"article": true, "section": true, "aside": true, "body": true,
}

// stripHTMLChrome removes navigation/boilerplate elements from a page
// before it goes to markitdown: script/style/nav/header/footer/etc.,
// plus ARIA landmarks and aria-hidden nodes. Best-effort — a parse
// failure or an all-chrome page returns the input unchanged rather
// than starving the ingest of any text at all.
func stripHTMLChrome(body []byte) []byte {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return body
	}
	stripChromeNodes(doc)
	isolateMain(doc)

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return body
	}
	stripped := buf.Bytes()
	if strings.TrimSpace(textContent(doc)) == "" {
		return body
	}
	return stripped
}

// stripChromeNodes walks the tree removing chrome elements. Recurses
// depth-first so a removed node's children never get separately
// visited.
func stripChromeNodes(n *html.Node) {
	child := n.FirstChild
	for child != nil {
		next := child.NextSibling
		if child.Type == html.ElementNode && isChrome(child) {
			n.RemoveChild(child)
		} else {
			stripChromeNodes(child)
		}
		child = next
	}
}

func isChrome(n *html.Node) bool {
	if kbChromeTags[n.Data] {
		return true
	}
	if n.Data == "header" || n.Data == "footer" {
		return isPageChrome(n)
	}
	for _, attr := range n.Attr {
		switch attr.Key {
		case "role":
			if kbChromeRoles[strings.ToLower(strings.TrimSpace(attr.Val))] {
				return true
			}
		case "aria-hidden":
			if strings.EqualFold(strings.TrimSpace(attr.Val), "true") {
				return true
			}
		}
	}
	return false
}

// isolateMain replaces <body>'s children with the page's single
// <main> element when there is exactly one with text in it —
// non-semantic chrome siblings (progress bars, hint overlays) go with
// everything else outside it. Zero or multiple <main>s leaves the
// body untouched.
func isolateMain(doc *html.Node) {
	body := findElement(doc, "body")
	if body == nil {
		return
	}
	mains := collectElements(body, "main")
	if len(mains) != 1 || strings.TrimSpace(textContent(mains[0])) == "" {
		return
	}
	main := mains[0]
	main.Parent.RemoveChild(main)
	for body.FirstChild != nil {
		body.RemoveChild(body.FirstChild)
	}
	body.AppendChild(main)
}

func findElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func collectElements(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == tag {
			out = append(out, c)
			continue
		}
		out = append(out, collectElements(c, tag)...)
	}
	return out
}

// isPageChrome reports whether header/footer n's nearest sectioning
// ancestor is <body> (page-level chrome) rather than <article>/
// <section>/<aside> (in-content heading/footer, kept).
func isPageChrome(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && kbSectioningTags[p.Data] {
			return p.Data == "body"
		}
	}
	return true
}

// textContent concatenates all text nodes under n.
func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(textContent(c))
	}
	return sb.String()
}
