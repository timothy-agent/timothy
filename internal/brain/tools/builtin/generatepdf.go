package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	pdfgenclient "github.com/SumonMSelim/timothy/internal/platform/pdfgen"

	"github.com/SumonMSelim/timothy/internal/brain/pdfgen"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

type generatePDFDocument struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type generatePDFArgs struct {
	Documents  []generatePDFDocument `json:"documents"`
	CoverTitle string                `json:"cover_title"`
	TOC        bool                  `json:"toc"`
}

// GeneratePDF renders one or more markdown documents into a typeset PDF
// via the pdfgen sidecar and publishes it to the user as generated
// media, the same way share_file does. Pass one document for a plain
// PDF; pass several with toc:true and a cover_title for a book-style
// merged PDF (one chapter per document).
func GeneratePDF(svc *pdfgen.Service) *tools.Tool {
	return &tools.Tool{
		Name: "generate_pdf",
		Description: `Renders markdown document(s) into a typeset PDF and publishes it to the user as a downloadable file.

Arguments:
- documents (array, required): one or more {"title", "content"} pairs,
  content as markdown. Each document becomes one chapter.
- cover_title (string, optional): adds a cover page with this title.
- toc (boolean, optional): adds a table of contents. Meaningful with
  multiple documents.

Pass a single document for a plain PDF. Pass several with toc:true and
a cover_title for a book-style merged PDF.

Returns a confirmation naming the stored id. Identical input returns
the same cached PDF without regenerating it.

Example: {"documents": [{"title": "Report", "content": "# Report\n..."}]}`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"documents": {
					"type": "array",
					"minItems": 1,
					"items": {
						"type": "object",
						"properties": {
							"title": {"type": "string"},
							"content": {"type": "string"}
						},
						"required": ["title", "content"],
						"additionalProperties": false
					}
				},
				"cover_title": {
					"type": "string",
					"description": "Optional cover page title"
				},
				"toc": {
					"type": "boolean",
					"description": "Optional table of contents"
				}
			},
			"required": ["documents"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if svc == nil {
				return "", fmt.Errorf("generate_pdf is not configured")
			}
			var args generatePDFArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if len(args.Documents) == 0 {
				return "", fmt.Errorf("documents is empty")
			}
			docs := make([]pdfgenclient.Document, len(args.Documents))
			for i, d := range args.Documents {
				if d.Content == "" {
					return "", fmt.Errorf("document %d: content is empty", i)
				}
				docs[i] = pdfgenclient.Document{Title: d.Title, Content: d.Content}
			}
			opts := pdfgenclient.Options{CoverTitle: args.CoverTitle, TOC: args.TOC}

			result, err := svc.Render(ctx, docs, opts)
			if err != nil {
				return "", fmt.Errorf("render pdf: %w", err)
			}

			collector := tools.CollectorFrom(ctx)
			if collector == nil {
				return "", fmt.Errorf("media emission is not configured")
			}
			name := args.CoverTitle
			if name == "" {
				name = args.Documents[0].Title
			}
			ref, err := collector.Emit(ctx, name+".pdf", bytes.NewReader(result.PDF))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("generated %q as %s (%s)", name, ref.ID, ref.Mime), nil
		},
	}
}
