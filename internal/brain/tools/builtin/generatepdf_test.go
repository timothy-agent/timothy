package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

func TestGeneratePDFNilService(t *testing.T) {
	tool := GeneratePDF(nil)
	ctx := tools.WithCollector(context.Background(), tools.NewCollector(fakeSaver(0)))
	args, _ := json.Marshal(generatePDFArgs{Documents: []generatePDFDocument{{Title: "A", Content: "hello"}}})
	if _, err := tool.Execute(ctx, args); err == nil {
		t.Fatal("nil service accepted")
	}
}

func TestGeneratePDFValidation(t *testing.T) {
	tool := GeneratePDF(nil)
	ctx := tools.WithCollector(context.Background(), tools.NewCollector(fakeSaver(0)))

	t.Run("empty documents", func(t *testing.T) {
		args, _ := json.Marshal(generatePDFArgs{})
		if _, err := tool.Execute(ctx, args); err == nil {
			t.Fatal("empty documents accepted")
		}
	})

	t.Run("empty content", func(t *testing.T) {
		args, _ := json.Marshal(generatePDFArgs{Documents: []generatePDFDocument{{Title: "A", Content: ""}}})
		if _, err := tool.Execute(ctx, args); err == nil {
			t.Fatal("empty content accepted")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		if _, err := tool.Execute(ctx, json.RawMessage(`{`)); err == nil {
			t.Fatal("invalid json accepted")
		}
	})
}
