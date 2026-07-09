package provider

import (
	"testing"
	"time"
)

func TestBackoffGrowsWithJitter(t *testing.T) {
	t.Parallel()
	for attempt := 1; attempt <= 3; attempt++ {
		base := baseBackoff << (attempt - 1)
		for range 20 {
			got := backoffFor(attempt)
			if got < base || got > base+base/2+time.Millisecond {
				t.Fatalf("attempt %d: backoff %v outside [%v, %v]", attempt, got, base, base+base/2)
			}
		}
	}
}

func TestRetryableStatus(t *testing.T) {
	t.Parallel()
	for code, want := range map[int]bool{
		429: true, 500: true, 502: true, 503: true,
		200: false, 400: false, 401: false, 404: false,
	} {
		if got := retryableStatus(code); got != want {
			t.Fatalf("retryableStatus(%d) = %v, want %v", code, got, want)
		}
	}
}
