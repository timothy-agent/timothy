package missions

import "testing"

// TestScanPendingInput mirrors TestScanPendingPermission for
// pending_input's own jsonb boundary (D-088, issue #457).
func TestScanPendingInput(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want *PendingInput
	}{
		{name: "nil leaves PendingInput nil", raw: nil, want: nil},
		{
			name: "populated unmarshals",
			raw:  []byte(`{"question":"which runtime?","kind":"mcq","options":["node","python"],"proposed_default":"node","phase":"generate"}`),
			want: &PendingInput{Question: "which runtime?", Kind: "mcq", Options: []string{"node", "python"}, ProposedDefault: "node", Phase: PhaseGenerate},
		},
		{name: "invalid json leaves PendingInput nil", raw: []byte(`not json`), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Mission
			scanPendingInput(&m, tt.raw)
			if tt.want == nil {
				if m.PendingInput != nil {
					t.Fatalf("PendingInput = %+v, want nil", m.PendingInput)
				}
				return
			}
			if m.PendingInput == nil {
				t.Fatalf("PendingInput = nil, want %+v", tt.want)
			}
			if m.PendingInput.Question != tt.want.Question || m.PendingInput.Kind != tt.want.Kind ||
				m.PendingInput.ProposedDefault != tt.want.ProposedDefault || m.PendingInput.Phase != tt.want.Phase ||
				len(m.PendingInput.Options) != len(tt.want.Options) {
				t.Fatalf("PendingInput = %+v, want %+v", m.PendingInput, tt.want)
			}
		})
	}
}

// TestScanPendingPermission covers the jsonb-to-flat-fields boundary
// (issue #423's pending_permission bundle) without a database: nil
// means no pending request, a populated row unmarshals into Mission's
// flat PendingPermission* fields, and invalid JSON is ignored rather
// than erroring the scan.
func TestScanPendingPermission(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want Mission
	}{
		{
			name: "nil clears nothing",
			raw:  nil,
			want: Mission{},
		},
		{
			name: "populated unmarshals into flat fields",
			raw:  []byte(`{"id":"perm-1","tool":"shell","args":"{\"command\":\"echo hi\"}","danger":"safe","rationale":"no standing grant"}`),
			want: Mission{
				PendingPermission:          "perm-1",
				PendingPermissionTool:      "shell",
				PendingPermissionArgs:      `{"command":"echo hi"}`,
				PendingPermissionDanger:    "safe",
				PendingPermissionRationale: "no standing grant",
			},
		},
		{
			name: "invalid json leaves fields empty",
			raw:  []byte(`not json`),
			want: Mission{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Mission
			scanPendingPermission(&m, tt.raw)
			if m.PendingPermission != tt.want.PendingPermission ||
				m.PendingPermissionTool != tt.want.PendingPermissionTool ||
				m.PendingPermissionArgs != tt.want.PendingPermissionArgs ||
				m.PendingPermissionDanger != tt.want.PendingPermissionDanger ||
				m.PendingPermissionRationale != tt.want.PendingPermissionRationale {
				t.Fatalf("scanPendingPermission(%s) = %+v, want %+v", tt.name, m, tt.want)
			}
		})
	}
}
