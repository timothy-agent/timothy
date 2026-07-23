package missions

import "testing"

func TestIsActionableTransition(t *testing.T) {
	cases := []struct {
		name          string
		before, after Status
		wantOK        bool
		wantKind      string
	}{
		{"idle to working is not actionable", StatusIdle, StatusWorking, false, ""},
		{"working to idle is not actionable", StatusWorking, StatusIdle, false, ""},
		{"working to waiting_for_input is actionable", StatusWorking, StatusWaitingForInput, true, "waiting_for_input"},
		{"working to paused is actionable", StatusWorking, StatusPaused, true, "paused"},
		{"working to done is actionable", StatusWorking, StatusDone, true, "done"},
		{"working to error is actionable", StatusWorking, StatusError, true, "error"},
		{"paused to paused (repeat Advance) is NOT actionable", StatusPaused, StatusPaused, false, ""},
		{"waiting_for_input to waiting_for_input (repeat) is NOT actionable", StatusWaitingForInput, StatusWaitingForInput, false, ""},
		{"paused to idle (resume) is not itself actionable", StatusPaused, StatusIdle, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := isActionableTransition(tc.before, tc.after)
			if ok != tc.wantOK || kind != tc.wantKind {
				t.Fatalf("isActionableTransition(%q, %q) = (%q, %v), want (%q, %v)", tc.before, tc.after, kind, ok, tc.wantKind, tc.wantOK)
			}
		})
	}
}
