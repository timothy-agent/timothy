package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
)

const (
	webFetchMaxBody   = 2 << 20 // bytes read off the wire
	webFetchMaxResult = 64 << 10 // bytes returned to the loop
	webFetchTimeout   = 30 * time.Second
)

type webFetchArgs struct {
	URL string `json:"url"`
}

// WebFetchConfig carries optional infrastructure for the tool.
type WebFetchConfig struct {
	// MarkitdownURL is the markitdown sidecar's base address (compose-
	// internal). When set, HTML pages convert to structured markdown
	// (headings, tables, links survive) and PDF responses become
	// readable; empty falls back to the built-in DOM text extractor
	// and PDFs stay unsupported.
	MarkitdownURL string
}

// WebFetch fetches a public URL and returns a readable text extract.
// Every connection — including redirect hops — dials only vetted
// public IPs: the guard resolves the host itself and refuses private,
// loopback, link-local (cloud metadata), CGNAT, and unspecified
// ranges, then dials the vetted address directly so a DNS answer
// can't change between check and connect. The markitdown sidecar is
// deliberately NOT behind that guard: its address is operator config
// (compose-internal), never model input.
func WebFetch(cfg WebFetchConfig) *tools.Tool {
	client := &http.Client{
		Timeout: webFetchTimeout,
		Transport: &http.Transport{
			DialContext:       guardedDial,
			ForceAttemptHTTP2: true,
		},
	}
	return &tools.Tool{
		Name: "web_fetch",
		Description: `Fetches a public web page and returns its readable text.

Use to read the content of a specific URL the user gave you or one you
already know. It is a plain GET — no search, no JavaScript rendering,
no login. Private and internal addresses are refused.

Arguments:
- url (string, required): full http:// or https:// URL including
  scheme, e.g. "https://example.com/pricing".

Returns readable markdown for HTML and PDF responses when the
converter is available (falling back to a plain-text extract), or the
raw body for plain-text responses. Long pages are truncated (a note
marks the cut). Errors state the reason: blocked address, unsupported
content, HTTP status, timeout.

Example: {"url": "https://go.dev/doc/devel/release"} → "Release
History — Go 1.26 (released 2026-02-10) ..."`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {
					"type": "string",
					"description": "Full URL with scheme, e.g. https://example.com/page"
				}
			},
			"required": ["url"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args webFetchArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			return fetchReadable(ctx, client, cfg.MarkitdownURL, args.URL)
		},
	}
}

func fetchReadable(ctx context.Context, client *http.Client, markitdownURL, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q: only http and https", u.Scheme)
	}
	// Drop any embedded credentials: never send userinfo as a Basic
	// Authorization header (no auth passthrough), and never leak it on
	// a redirect to another host.
	u.User = nil
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "timothy/1.0 (+self-hosted assistant)")
	accept := "text/html, text/plain;q=0.9, application/json;q=0.8, */*;q=0.1"
	if markitdownURL != "" {
		accept = "text/html, application/pdf;q=0.9, text/plain;q=0.9, application/json;q=0.8, */*;q=0.1"
	}
	req.Header.Set("Accept", accept)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http %d fetching %s", resp.StatusCode, u.Host)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBody+1))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	bodyTruncated := len(body) > webFetchMaxBody
	if bodyTruncated {
		body = body[:webFetchMaxBody]
	}

	ct := resp.Header.Get("Content-Type")
	var text string
	switch {
	case strings.Contains(ct, "text/html"):
		// Markitdown keeps structure (headings, tables, links) the DOM
		// text walk flattens away; any sidecar failure degrades to the
		// walk rather than failing the fetch.
		text = ""
		if markitdownURL != "" {
			text, _ = markitdown.Convert(ctx, nil, markitdownURL, "page.html", "text/html", body)
		}
		if strings.TrimSpace(text) == "" {
			text = extractHTMLText(body)
		}
	case strings.Contains(ct, "application/pdf"):
		if markitdownURL == "" {
			return "", fmt.Errorf("unsupported content type %q: PDF conversion needs the markitdown sidecar, which is not configured", ct)
		}
		md, err := markitdown.Convert(ctx, nil, markitdownURL, "page.pdf", "application/pdf", body)
		if err != nil {
			return "", fmt.Errorf("pdf conversion failed: %w", err)
		}
		text = md
	case strings.HasPrefix(ct, "text/"),
		strings.Contains(ct, "json"),
		strings.Contains(ct, "xml"):
		text = string(body)
	default:
		return "", fmt.Errorf("unsupported content type %q: only text responses are fetched", ct)
	}

	text = strings.TrimSpace(text)
	if len(text) > webFetchMaxResult {
		text = text[:webFetchMaxResult] + "\n\n[truncated: page continues]"
	} else if bodyTruncated {
		text += "\n\n[truncated: response exceeded the fetch size cap]"
	}
	if text == "" {
		return "", fmt.Errorf("page had no extractable text")
	}
	return text, nil
}

// guardedDial resolves the host, refuses non-public addresses, and
// dials the vetted IP directly.
func guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split host: %w", err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var lastErr error
	for _, ip := range ips {
		if reason := blockedIP(ip.IP); reason != "" {
			lastErr = fmt.Errorf("blocked address: %s resolves to %s (%s)", host, ip.IP, reason)
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no addresses for %s", host)
	}
	return nil, lastErr
}

var cgnatNet = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// blockedIP returns a non-empty reason when the address must not be
// dialed: anything that isn't plain public unicast. Covers loopback,
// RFC 1918 + IPv6 ULA, link-local (169.254.0.0/16 — cloud metadata
// endpoints live there), CGNAT, unspecified, and multicast.
func blockedIP(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return "loopback"
	case ip.IsPrivate():
		return "private range"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "link-local"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsMulticast():
		return "multicast"
	case cgnatNet.Contains(ip):
		return "carrier-grade NAT range"
	case !ip.IsGlobalUnicast():
		return "not global unicast"
	}
	return ""
}

// extractHTMLText walks the DOM collecting text nodes, skipping
// non-content elements, and collapses whitespace. The <title> leads.
func extractHTMLText(body []byte) string {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return ""
	}
	var title string
	var b strings.Builder
	skip := map[string]bool{
		"script": true, "style": true, "noscript": true,
		"template": true, "svg": true, "head": true,
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "title" && title == "" {
				if c := n.FirstChild; c != nil && c.Type == html.TextNode {
					title = strings.TrimSpace(c.Data)
				}
				return
			}
			if skip[n.Data] {
				return
			}
		}
		if n.Type == html.TextNode {
			if t := strings.TrimSpace(n.Data); t != "" {
				b.WriteString(t)
				b.WriteByte('\n')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	// The title lives under <head>, which the content walk skips —
	// pull it out first with its own pass.
	var findTitle func(*html.Node)
	findTitle = func(n *html.Node) {
		if title != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "title" {
			if c := n.FirstChild; c != nil && c.Type == html.TextNode {
				title = strings.TrimSpace(c.Data)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findTitle(c)
		}
	}
	findTitle(doc)
	walk(doc)

	text := b.String()
	if title != "" {
		return title + "\n\n" + text
	}
	return text
}
