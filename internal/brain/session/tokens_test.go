package session

import (
	"encoding/json"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

func TestEstimateToolTokens(t *testing.T) {
	t.Parallel()

	if got, err := EstimateToolTokens(nil); err != nil || got != 0 {
		t.Fatalf("EstimateToolTokens(nil) = %d, %v; want 0, nil", got, err)
	}

	one := []provider.ToolDef{{
		Name:        "read_file",
		Description: "Read a file from the workspace.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}}
	single, err := EstimateToolTokens(one)
	if err != nil {
		t.Fatal(err)
	}
	if single <= 0 {
		t.Fatalf("EstimateToolTokens(one def) = %d, want positive", single)
	}

	two, err := EstimateToolTokens(append(one, provider.ToolDef{
		Name:        "write_file",
		Description: "Write a file into the workspace.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"body":{"type":"string"}}}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if two <= single {
		t.Fatalf("two defs = %d tokens, want more than one def's %d", two, single)
	}
}
