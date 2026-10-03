package store

import "testing"

func TestInitialStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mem  Memory
		want Status
	}{
		{name: "explicit user memory activates", mem: Memory{Actor: ActorUser}, want: StatusActive},
		{name: "review-required user memory stays pending", mem: Memory{Actor: ActorUser, RequireReview: true}, want: StatusPending},
		{name: "agent memory stays pending", mem: Memory{Actor: "agent"}, want: StatusPending},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := initialStatus(tc.mem); got != tc.want {
				t.Fatalf("initialStatus(%+v) = %q, want %q", tc.mem, got, tc.want)
			}
		})
	}
}
