package api

import (
	"testing"
	"time"
)

// TestKBRetryDelay pins the retry backoff ladder (issue #414): each
// scheduled retry uses the rung matching its retry_count, clamped to
// the ladder's last rung past kbRetryMaxAttempts.
func TestKBRetryDelay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		retryCount int
		want       time.Duration
	}{
		{0, 2 * time.Minute},
		{1, 2 * time.Minute},
		{2, 10 * time.Minute},
		{3, 30 * time.Minute},
		{4, 30 * time.Minute},
		{100, 30 * time.Minute},
	}
	for _, tc := range cases {
		if got := kbRetryDelay(tc.retryCount); got != tc.want {
			t.Fatalf("kbRetryDelay(%d) = %v, want %v", tc.retryCount, got, tc.want)
		}
	}
}

// TestValidKBRetrievalWeight pins the bounds (D-085, issue #443):
// > 0 and <= 2, matching the kb_collections CHECK constraint.
func TestValidKBRetrievalWeight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		weight float64
		want   bool
	}{
		{0, false},
		{-0.1, false},
		{0.1, true},
		{1.0, true},
		{2.0, true},
		{2.1, false},
	}
	for _, tc := range cases {
		if got := validKBRetrievalWeight(tc.weight); got != tc.want {
			t.Fatalf("validKBRetrievalWeight(%v) = %v, want %v", tc.weight, got, tc.want)
		}
	}
}
