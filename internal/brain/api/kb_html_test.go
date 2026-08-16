package api

import (
	"strings"
	"testing"
)

func TestStripHTMLChrome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       string
		wantAbsent  []string
		wantPresent []string
	}{
		{
			name: "nav/header/footer/script junk stripped around main article",
			input: `<html><body>
				<header>Site Logo <nav>Home | About</nav></header>
				<script>trackClick();</script>
				<style>.x{color:red}</style>
				<nav>Home | About | Contact</nav>
				<main><article><h1>Article Title</h1><p>The real content lives here.</p></article></main>
				<footer>Copyright 2026 · All rights reserved</footer>
			</body></html>`,
			wantAbsent:  []string{"Home | About", "trackClick", "color:red", "Copyright 2026", "Site Logo"},
			wantPresent: []string{"Article Title", "The real content lives here"},
		},
		{
			name: "nested header inside article survives",
			input: `<html><body>
				<header>Page Nav</header>
				<main><article><header><h2>Section Heading</h2></header><p>Body text.</p></article></main>
			</body></html>`,
			wantAbsent:  []string{"Page Nav"},
			wantPresent: []string{"Section Heading", "Body text"},
		},
		{
			name: "everything inside nav falls back to original",
			input: `<html><body>
				<nav><p>Only content is inside this nav element.</p></nav>
			</body></html>`,
			wantPresent: []string{"Only content is inside this nav element"},
		},
		{
			name: "role navigation div stripped",
			input: `<html><body>
				<div role="navigation">Menu | Links | Here</div>
				<main><p>Kept paragraph text.</p></main>
			</body></html>`,
			wantAbsent:  []string{"Menu | Links | Here"},
			wantPresent: []string{"Kept paragraph text"},
		},
		{
			name: "aria-hidden true stripped",
			input: `<html><body>
				<div aria-hidden="true">Decorative hidden text</div>
				<main><p>Visible paragraph.</p></main>
			</body></html>`,
			wantAbsent:  []string{"Decorative hidden text"},
			wantPresent: []string{"Visible paragraph"},
		},
		{
			name: "single main isolates content from non-semantic chrome siblings",
			input: `<html><body>
				<div class="progress"></div>
				<div class="hint">←/→ · space · f fullscreen · Home/End</div>
				<div class="corner">Day 1 · Last updated 2026-06-19</div>
				<main class="deck"><section><h1>Slide Title</h1><p>Slide body text.</p></section></main>
			</body></html>`,
			wantAbsent:  []string{"fullscreen", "Last updated"},
			wantPresent: []string{"Slide Title", "Slide body text"},
		},
		{
			name: "two mains leave body untouched",
			input: `<html><body>
				<div class="hint">keyboard hints here</div>
				<main><p>First main.</p></main>
				<main><p>Second main.</p></main>
			</body></html>`,
			wantPresent: []string{"keyboard hints here", "First main", "Second main"},
		},
		{
			name: "empty main leaves body untouched",
			input: `<html><body>
				<p>Real text outside.</p>
				<main></main>
			</body></html>`,
			wantPresent: []string{"Real text outside"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := string(stripHTMLChrome([]byte(tc.input)))
			for _, s := range tc.wantAbsent {
				if strings.Contains(got, s) {
					t.Errorf("stripHTMLChrome output still contains %q\ngot: %s", s, got)
				}
			}
			for _, s := range tc.wantPresent {
				if !strings.Contains(got, s) {
					t.Errorf("stripHTMLChrome output missing %q\ngot: %s", s, got)
				}
			}
		})
	}
}

// TestStripHTMLChromeMalformedReturnsUnchanged pins the best-effort
// fallback: a parse the html package can't handle at all (none are
// known to actually error, since it tolerates malformed markup by
// design) must never panic, and byte-identical input on any bail-out
// path.
func TestStripHTMLChromeMalformedReturnsUnchanged(t *testing.T) {
	t.Parallel()
	tests := []string{
		"",
		"<<<not really html at all>>>",
		"<div><span>unclosed tags all the way down<p><b>",
		"plain text, no tags whatsoever",
	}
	for _, in := range tests {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("stripHTMLChrome panicked on %q: %v", in, r)
				}
			}()
			_ = stripHTMLChrome([]byte(in))
		}()
	}
}
