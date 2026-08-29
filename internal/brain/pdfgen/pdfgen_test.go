package pdfgen

import (
	"testing"

	"github.com/SumonMSelim/timothy/internal/platform/pdfgen"
)

func TestInputHashStable(t *testing.T) {
	docs := []pdfgen.Document{{Title: "A", Content: "hello"}}
	opts := pdfgen.Options{CoverTitle: "Cover", TOC: true}

	h1 := inputHash(opts, docs)
	h2 := inputHash(opts, docs)
	if h1 != h2 {
		t.Fatal("same input produced different hashes")
	}
}

func TestInputHashDocBoundaryShift(t *testing.T) {
	// "AB"/"cd" split two ways must not collide: length-prefixing each
	// field is what prevents this.
	a := []pdfgen.Document{{Title: "A", Content: "Bcd"}}
	b := []pdfgen.Document{{Title: "AB", Content: "cd"}}
	opts := pdfgen.Options{}

	if inputHash(opts, a) == inputHash(opts, b) {
		t.Fatal("shifting the title/content boundary produced the same hash")
	}
}

func TestInputHashDocCountDiffers(t *testing.T) {
	one := []pdfgen.Document{{Title: "A", Content: "hello world"}}
	two := []pdfgen.Document{{Title: "A", Content: "hello"}, {Title: "", Content: " world"}}
	opts := pdfgen.Options{}

	if inputHash(opts, one) == inputHash(opts, two) {
		t.Fatal("one merged document hashed the same as two split documents")
	}
}

func TestInputHashOptionChangeDiffers(t *testing.T) {
	docs := []pdfgen.Document{{Title: "A", Content: "hello"}}

	h1 := inputHash(pdfgen.Options{TOC: true}, docs)
	h2 := inputHash(pdfgen.Options{TOC: false}, docs)
	if h1 == h2 {
		t.Fatal("TOC option change produced the same hash")
	}

	h3 := inputHash(pdfgen.Options{CoverTitle: "X"}, docs)
	h4 := inputHash(pdfgen.Options{CoverTitle: "Y"}, docs)
	if h3 == h4 {
		t.Fatal("CoverTitle option change produced the same hash")
	}
}

func TestRenderNilServiceNotEnabled(t *testing.T) {
	var s *Service
	if _, err := s.Render(t.Context(), nil, pdfgen.Options{}); err != ErrNotEnabled {
		t.Fatalf("err = %v, want ErrNotEnabled", err)
	}
}

func TestRenderNoClientNotEnabled(t *testing.T) {
	s := New(nil, nil, nil)
	if _, err := s.Render(t.Context(), nil, pdfgen.Options{}); err != ErrNotEnabled {
		t.Fatalf("err = %v, want ErrNotEnabled", err)
	}
}
