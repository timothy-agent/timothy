package missions

import "testing"

func TestComposeMessage(t *testing.T) {
	cases := []struct {
		name        string
		kind, title string
		reason      string
		want        string
	}{
		{"done", "done", "Fix the login bug", "", "Mission - Fix the login bug is done"},
		{"failed", "error", "Fix the login bug", "max_iterations", "Mission - Fix the login bug is failed"},
		{"failed no reason", "error", "Fix the login bug", "", "Mission - Fix the login bug is failed"},
		{"cancelled", "error", "Fix the login bug", "cancelled", "Mission - Fix the login bug is cancelled"},
		{"paused", "paused", "Fix the login bug", "", "Mission - Fix the login bug is paused, needs your intervention."},
		{"waiting_for_input", "waiting_for_input", "Fix the login bug", "", "Mission - Fix the login bug is waiting for your input."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := composeMessage(tc.kind, tc.title, tc.reason)
			if got != tc.want {
				t.Fatalf("composeMessage(%q, %q, %q) = %q, want %q", tc.kind, tc.title, tc.reason, got, tc.want)
			}
		})
	}
}

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
