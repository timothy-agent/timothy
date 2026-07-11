package builtin

import (
	"encoding/json"
	"testing"
)

func TestShellTimeoutClamp(t *testing.T) {
	t.Parallel()
	clamp := ShellTimeoutClamp()

	tests := []struct {
		name string
		in   string
		want int
	}{
		{name: "over cap clamps down", in: `{"command":"x","timeout_seconds":900}`, want: 120},
		{name: "under cap untouched", in: `{"command":"x","timeout_seconds":45}`, want: 45},
		{name: "negative resets to default", in: `{"command":"x","timeout_seconds":-5}`, want: 0},
		{name: "absent stays absent", in: `{"command":"x"}`, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := clamp(json.RawMessage(tc.in))
			if err != nil {
				t.Fatalf("clamp: %v", err)
			}
			var args shellArgs
			if err := json.Unmarshal(out, &args); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if args.TimeoutSeconds != tc.want {
				t.Fatalf("timeout_seconds = %d, want %d", args.TimeoutSeconds, tc.want)
			}
		})
	}
}

func TestShellHonorsRequestedTimeout(t *testing.T) {
	t.Parallel()
	tool, _ := shellTool(t, 0)
	args, _ := json.Marshal(map[string]any{"command": "sleep 5", "timeout_seconds": 1})
	_, err := tool.Execute(t.Context(), args)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
