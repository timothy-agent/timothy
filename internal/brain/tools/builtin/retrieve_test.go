package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

type fakeOutputs map[string]tools.Output

func (f fakeOutputs) Get(_ context.Context, id string) (tools.Output, error) {
	out, ok := f[id]
	if !ok {
		return tools.Output{}, tools.ErrOutputNotFound
	}
	return out, nil
}

func TestRetrieveOutput(t *testing.T) {
	t.Parallel()
	store := fakeOutputs{
		"9be4c1d2-04a7-47a1-a1a9-3f6d2c9f1e10": {Tool: "shell", Content: "full build log"},
	}
	tool := RetrieveOutput(store)

	args, _ := json.Marshal(map[string]string{"ref": "9be4c1d2-04a7-47a1-a1a9-3f6d2c9f1e10"})
	got, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "full build log" {
		t.Fatalf("= %q", got)
	}

	args, _ = json.Marshal(map[string]string{"ref": "00000000-0000-0000-0000-000000000000"})
	_, err = tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "unknown or expired ref") {
		t.Fatalf("err = %v, want unknown ref", err)
	}
}
