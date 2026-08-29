// Package pdfgen is the client for the pdfgen sidecar (pdfgen-svc/):
// one POST /render per document set, markdown in, PDF bytes out.
// Compose-internal, no auth — the sidecar is only reachable inside the
// compose network.
package pdfgen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// renderTimeout is generous: Typst compiles (mermaid diagrams, code
// highlighting) can run long on larger documents.
const renderTimeout = 120 * time.Second

// Document is one input document: a title and its markdown content.
// Each document becomes one chapter in the rendered PDF.
type Document struct {
	Title   string
	Content string
}

// Options controls the rendered PDF's optional cover page and table of
// contents.
type Options struct {
	CoverTitle string
	TOC        bool
}

// Client renders markdown documents to PDF via the pdfgen sidecar.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client targeting baseURL. baseURL empty means the
// sidecar isn't configured; callers nil-gate on PDFGEN_URL before
// constructing a Client.
func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: renderTimeout}}
}

type renderRequest struct {
	Documents []renderDocument `json:"documents"`
	Options   renderOptions    `json:"options"`
}

type renderDocument struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type renderOptions struct {
	CoverTitle string `json:"cover_title,omitempty"`
	TOC        bool   `json:"toc"`
}

// Render posts docs and opts to the sidecar and returns the rendered
// PDF bytes.
func (c *Client) Render(ctx context.Context, docs []Document, opts Options) ([]byte, error) {
	reqDocs := make([]renderDocument, len(docs))
	for i, d := range docs {
		reqDocs[i] = renderDocument(d)
	}
	body, err := json.Marshal(renderRequest{
		Documents: reqDocs,
		Options:   renderOptions(opts),
	})
	if err != nil {
		return nil, fmt.Errorf("pdfgen: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/render", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("pdfgen: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pdfgen: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var errBody struct {
			Error string `json:"error"`
		}
		if decErr := json.NewDecoder(resp.Body).Decode(&errBody); decErr == nil && errBody.Error != "" {
			return nil, fmt.Errorf("pdfgen returned http %d: %s", resp.StatusCode, errBody.Error)
		}
		return nil, fmt.Errorf("pdfgen returned http %d", resp.StatusCode)
	}

	pdf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("pdfgen: read response: %w", err)
	}
	return pdf, nil
}
