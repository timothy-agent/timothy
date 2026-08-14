package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestKBReadExecute(t *testing.T) {
	t.Parallel()
	docs := map[string]KBDocument{
		"doc-1": {Title: "Runbook", SourceRef: "https://example.com/runbook", Markdown: "# Steps\n1. do the thing"},
	}
	tool := KBRead(func(_ context.Context, id string) (KBDocument, error) {
		d, ok := docs[id]
		if !ok {
			return KBDocument{}, fmt.Errorf("document %s not found", id)
		}
		return d, nil
	})

	tests := []struct {
		name    string
		args    string
		want    []string
		wantErr string
	}{
		{"kb ref", `{"ref":"kb://doc-1"}`, []string{"Runbook", "https://example.com/runbook", "do the thing"}, ""},
		{"bare id", `{"ref":"doc-1"}`, []string{"Runbook"}, ""},
		{"whitespace tolerated", `{"ref":"  kb://doc-1  "}`, []string{"Runbook"}, ""},
		{"unknown id", `{"ref":"kb://nope"}`, nil, "not found"},
		{"empty ref", `{"ref":"  "}`, nil, "ref must be"},
		{"bare scheme", `{"ref":"kb://"}`, nil, "ref must be"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := tool.Execute(context.Background(), json.RawMessage(tc.args))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Fatalf("output %q missing %q", out, w)
				}
			}
		})
	}
}
