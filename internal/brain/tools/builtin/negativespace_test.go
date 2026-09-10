package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// TestDescriptionsNameTheirContrastedTool is a drift guard for issue
// #645: every overlapping tool must say which neighbouring tool to
// reach for instead, so the model routes without guessing.
func TestDescriptionsNameTheirContrastedTool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tool      *tools.Tool
		contrasts []string
	}{
		{SearchMemory(func(context.Context, string, int) ([]SearchMemoryHit, error) { return nil, nil }), []string{"search_kb"}},
		{KBSearch(func(context.Context, string, string, int) ([]KBSearchHit, error) { return nil, nil }), []string{"search_memory"}},
		{KBRead(func(context.Context, string) (KBDocument, error) { return KBDocument{}, nil }), []string{"search_kb"}},
		{WebSearch(""), []string{"fetch_url"}},
		{WebFetch(WebFetchConfig{}), []string{"search_web"}},
		{Remember(func(context.Context, string, string) (string, error) { return "", nil }), []string{"search_memory"}},
		{Shell(ShellConfig{}), []string{"write_file"}},
		{WriteFile(WriteFileConfig{}), []string{"shell"}},
		{RetrieveOutput(nil), []string{"shell", "search_web"}},
	}
	for _, tc := range tests {
		t.Run(tc.tool.Name, func(t *testing.T) {
			t.Parallel()
			lower := strings.ToLower(tc.tool.Description)
			if !strings.Contains(lower, "do not use this") {
				t.Fatalf("%s: description states no negative space", tc.tool.Name)
			}
			for _, want := range tc.contrasts {
				if !strings.Contains(tc.tool.Description, want) {
					t.Fatalf("%s: description does not name %s", tc.tool.Name, want)
				}
			}
		})
	}
}
