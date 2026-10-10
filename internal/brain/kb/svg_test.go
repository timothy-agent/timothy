package kb

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const svgDiagram = `<svg xmlns="http://www.w3.org/2000/svg" width="400" height="200"><rect x="10" y="10" width="100" height="50"/><text x="20" y="40">Agent</text></svg>`

func TestIsSVG(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"namespaced", svgDiagram, true},
		{"no namespace", `<svg width="1" height="1"></svg>`, true},
		{"prefixed namespace", `<s:svg xmlns:s="http://www.w3.org/2000/svg"/>`, true},
		{"prolog doctype and comments", `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
			`<!-- generator: x --><!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">` + "\n" +
			`<!-- another --><svg xmlns="http://www.w3.org/2000/svg"/>`, true},
		{"bom", "\xef\xbb\xbf" + svgDiagram, true},
		{"leading whitespace", "\n  " + svgDiagram, true},
		{"wrong namespace", `<svg xmlns="http://example.com/x"/>`, false},
		{"non-svg xml", `<?xml version="1.0"?><rss><channel><svg/></channel></rss>`, false},
		{"html with inline svg", `<html><body><svg xmlns="http://www.w3.org/2000/svg"></svg></body></html>`, false},
		{"html doctype", `<!DOCTYPE html><html><svg/></html>`, false},
		{"text before root", `hello <svg/>`, false},
		{"png bytes", string(pngBytes()), false},
		{"jpeg bytes", "\xff\xd8\xff\xe0\x00\x10JFIF\x00", false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSVG([]byte(tt.in)); got != tt.want {
				t.Fatalf("isSVG = %v, want %v", got, tt.want)
			}
		})
	}
}

// fakeRasterizer records the SVG it got and returns png, or err.
func fakeRasterizer(png []byte, err error, got *[]byte) Rasterizer {
	return func(_ context.Context, svg []byte) ([]byte, error) {
		if got != nil {
			*got = svg
		}
		return png, err
	}
}

func serveBody(t *testing.T, contentType string, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnrichMarkdownCaptionsSVGServedAsText(t *testing.T) {
	for _, ctype := range []string{"text/plain", "text/xml", "image/svg+xml"} {
		t.Run(ctype, func(t *testing.T) {
			srv := serveBody(t, ctype, []byte(svgDiagram))
			var gotSVG, gotData []byte
			var gotType string
			e := &Enricher{
				Fetch: srv.Client(),
				Caption: func(_ context.Context, mt string, data []byte) string {
					gotType, gotData = mt, data
					return "an agent diagram"
				},
				Rasterize: fakeRasterizer(pngBytes(), nil, &gotSVG),
				Enabled:   alwaysEnabled,
				Log:       discardLog(),
			}
			link := "![d](" + srv.URL + "/diagram)"
			out, stats := e.EnrichMarkdown(context.Background(), link+"\n")
			if stats != (EnrichStats{Found: 1, Captioned: 1}) {
				t.Fatalf("stats = %+v, want Found=1 Captioned=1", stats)
			}
			if want := link + captionBlock(captionMarker, "an agent diagram") + "\n"; out != want {
				t.Fatalf("out = %q, want %q", out, want)
			}
			if string(gotSVG) != svgDiagram {
				t.Fatalf("rasterizer got %q, want the fetched svg", gotSVG)
			}
			if gotType != "image/png" || !bytes.Equal(gotData, pngBytes()) {
				t.Fatalf("captioner got %q %q, want the rasterized png", gotType, gotData)
			}
		})
	}
}

func TestEnrichMarkdownSVG(t *testing.T) {
	tests := []struct {
		name          string
		rasterize     Rasterizer
		visionBound   bool
		wantStats     EnrichStats
		wantMarker    string
		wantCaptioned string
	}{
		{"vision caption", fakeRasterizer(pngBytes(), nil, nil), true, EnrichStats{Found: 1, Captioned: 1}, captionMarker, "a diagram"},
		{"ocr fallback", fakeRasterizer(pngBytes(), nil, nil), false, EnrichStats{Found: 1, Captioned: 1}, ocrMarker, "Agent"},
		{"rasterizer error", fakeRasterizer(nil, errors.New("pdfgen returned http 422"), nil), true, EnrichStats{Found: 1, Failed: 1}, "", ""},
		{"rasterized png too large", fakeRasterizer(make([]byte, maxImageBytes+1), nil, nil), true, EnrichStats{Found: 1, Failed: 1}, "", ""},
		{"no rasterizer skips as before", nil, true, EnrichStats{Found: 1, Failed: 1}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := serveBody(t, "text/plain", []byte(svgDiagram))
			var captions int
			e := &Enricher{
				Fetch: srv.Client(),
				Caption: func(context.Context, string, []byte) string {
					captions++
					return "a diagram"
				},
				VisionAvailable: func(context.Context) bool { return tt.visionBound },
				OCR:             fakeRecognizer("Agent", false, nil),
				OCREnabled:      alwaysEnabled,
				Rasterize:       tt.rasterize,
				Enabled:         alwaysEnabled,
				Log:             discardLog(),
			}
			link := "![d](" + srv.URL + "/d.svg)"
			md := link + "\n"
			out, stats := e.EnrichMarkdown(context.Background(), md)
			if stats != tt.wantStats {
				t.Fatalf("stats = %+v, want %+v", stats, tt.wantStats)
			}
			if tt.wantMarker == "" {
				if out != md || captions != 0 {
					t.Fatalf("out = %q captions = %d, want text unchanged and no caption call", out, captions)
				}
				return
			}
			if want := link + captionBlock(tt.wantMarker, tt.wantCaptioned) + "\n"; out != want {
				t.Fatalf("out = %q, want %q", out, want)
			}
		})
	}
}

func TestEnrichMarkdownRasterImagesSkipRasterizer(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		wantType string
	}{
		{"png", pngBytes(), "image/png"},
		{"jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01"), "image/jpeg"},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp"},
		{"gif", []byte("GIF89a\x01\x00\x01\x00"), "image/gif"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := serveBody(t, "application/octet-stream", tt.body)
			var gotType string
			e := &Enricher{
				Fetch: srv.Client(),
				Caption: func(_ context.Context, mt string, _ []byte) string {
					gotType = mt
					return "a picture"
				},
				Rasterize: func(context.Context, []byte) ([]byte, error) {
					t.Error("rasterizer called for a raster image")
					return nil, errors.New("unexpected")
				},
				Enabled: alwaysEnabled,
				Log:     discardLog(),
			}
			md := "![p](" + srv.URL + "/p)\n"
			out, stats := e.EnrichMarkdown(context.Background(), md)
			if stats != (EnrichStats{Found: 1, Captioned: 1}) || gotType != tt.wantType {
				t.Fatalf("stats = %+v type = %q, want one caption of %s", stats, gotType, tt.wantType)
			}
			if !strings.Contains(out, captionMarker+" a picture") {
				t.Fatalf("out = %q, want a caption block", out)
			}
		})
	}
}

func TestEnrichMarkdownNonSVGTextNeverRasterized(t *testing.T) {
	for _, body := range []string{
		`<?xml version="1.0"?><rss><svg/></rss>`,
		`<html><body><svg xmlns="http://www.w3.org/2000/svg"></svg></body></html>`,
	} {
		srv := serveBody(t, "text/html", []byte(body))
		e := &Enricher{
			Fetch:   srv.Client(),
			Caption: fakeCaptioner("x", false),
			Rasterize: func(context.Context, []byte) ([]byte, error) {
				t.Error("rasterizer called for a non-svg document")
				return nil, errors.New("unexpected")
			},
			Enabled: alwaysEnabled,
			Log:     discardLog(),
		}
		md := "![p](" + srv.URL + "/p)\n"
		if out, stats := e.EnrichMarkdown(context.Background(), md); out != md || stats.Failed != 1 {
			t.Fatalf("body %q: out = %q stats = %+v, want unchanged and Failed=1", body, out, stats)
		}
	}
}
