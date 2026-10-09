// Package pagefetch GETs web pages for the fetch_url tool and Knowledge
// URL ingest (issue #1097): first as a desktop browser, then once more
// as a search crawler when the site refuses the request or serves an
// empty JavaScript shell. Many sites serve server-rendered HTML to
// crawlers, which is the content a client-rendered page hides.
package pagefetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"
	crawlerUA = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"

	// minShellText is the visible-text length below which an HTML page
	// counts as a shell; maxNoticeShellText bounds the "enable
	// JavaScript" notice rule so a long article with such a notice in a
	// comments widget is not retried.
	minShellText       = 500
	maxNoticeShellText = 2000
)

// Page is one fetched response. Body holds at most maxBytes+1 bytes so
// callers can tell an over-limit response from one exactly at it.
type Page struct {
	Status      int
	ContentType string
	Body        []byte
}

// Fetch GETs rawURL through client (callers pass their netguard-wrapped
// client). A transport error on the browser attempt is returned as is;
// the crawler attempt only replaces the first result when it comes back
// 2xx and not a shell.
func Fetch(ctx context.Context, client *http.Client, rawURL, accept string, maxBytes int64) (*Page, error) {
	page, err := get(ctx, client, rawURL, accept, maxBytes, browserHeaders)
	if err != nil {
		return nil, err
	}
	if !needsCrawler(page) {
		return page, nil
	}
	retry, err := get(ctx, client, rawURL, accept, maxBytes, crawlerHeaders)
	if err != nil || !ok(retry) || isShell(retry) {
		return page, nil
	}
	return retry, nil
}

func browserHeaders(h http.Header) {
	h.Set("User-Agent", browserUA)
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Upgrade-Insecure-Requests", "1")
	h.Set("Sec-Fetch-Dest", "document")
	h.Set("Sec-Fetch-Mode", "navigate")
	h.Set("Sec-Fetch-Site", "none")
	h.Set("Sec-Fetch-User", "?1")
}

func crawlerHeaders(h http.Header) {
	h.Set("User-Agent", crawlerUA)
}

func get(ctx context.Context, client *http.Client, rawURL, accept string, maxBytes int64, headers func(http.Header)) (*Page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	headers(req.Header)
	req.Header.Set("Accept", accept)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return &Page{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: body}, nil
}

func ok(p *Page) bool { return p.Status >= 200 && p.Status < 300 }

// needsCrawler: 403 and LinkedIn's 999 are bot blocks; a 2xx HTML
// shell has no content yet.
func needsCrawler(p *Page) bool {
	if p.Status == http.StatusForbidden || p.Status == 999 {
		return true
	}
	return ok(p) && isShell(p)
}

// isShell reports whether an HTML page has too little visible text to
// be the real content, or is short and asks for JavaScript.
func isShell(p *Page) bool {
	if !strings.Contains(p.ContentType, "text/html") {
		return false
	}
	n := utf8.RuneCountInString(visibleText(p.Body))
	if n < minShellText {
		return true
	}
	return n < maxNoticeShellText && bytes.Contains(bytes.ToLower(p.Body), []byte("enable javascript"))
}

// visibleText concatenates the page's body text, skipping elements a
// browser never renders as content.
func visibleText(body []byte) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	skip := map[string]bool{
		"script": true, "style": true, "noscript": true,
		"template": true, "svg": true, "head": true,
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skip[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(strings.Join(strings.Fields(n.Data), " "))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return b.String()
}
