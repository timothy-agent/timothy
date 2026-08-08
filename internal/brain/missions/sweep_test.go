package missions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

// fakeCapacityChecker scripts one Capacity call per test case.
type fakeCapacityChecker struct {
	admit  bool
	reason string
	err    error
}

func (f fakeCapacityChecker) Capacity(ctx context.Context) (bool, string, error) {
	return f.admit, f.reason, f.err
}

// TestAdmitWork covers D-056's three gate outcomes: nil gate always
// admits (tests, sandbox-less setups), a gate error also admits (fails
// open so a dead sandboxd never freezes the mission queue), and a live
// gate's own admit/reason pass through unchanged.
func TestAdmitWork(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cases := []struct {
		name       string
		gate       capacityChecker
		wantAdmit  bool
		wantReason string
	}{
		{name: "nil gate admits", gate: nil, wantAdmit: true},
		{
			name:      "gate error admits open",
			gate:      fakeCapacityChecker{err: errors.New("sandboxd unreachable")},
			wantAdmit: true,
		},
		{
			name:       "gate denies",
			gate:       fakeCapacityChecker{admit: false, reason: "mem_available 900MB < floor 1024 + per-sandbox 768"},
			wantAdmit:  false,
			wantReason: "mem_available 900MB < floor 1024 + per-sandbox 768",
		},
		{
			name:      "gate admits",
			gate:      fakeCapacityChecker{admit: true},
			wantAdmit: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			admit, reason := admitWork(context.Background(), tc.gate, log)
			if admit != tc.wantAdmit {
				t.Errorf("admit = %v, want %v", admit, tc.wantAdmit)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}
