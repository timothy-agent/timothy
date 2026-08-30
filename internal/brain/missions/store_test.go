package missions

import "testing"

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
