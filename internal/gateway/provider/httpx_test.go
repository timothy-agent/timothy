package provider

import (
	"strings"
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

func TestReadSSE(t *testing.T) {
	t.Parallel()
	input := strings.Join([]string{
		": comment to ignore",
		"event: message_start",
		"data: {\"a\":1}",
		"",
		"data: line1",
		"data: line2",
		"",
		"event: dangling-no-data",
		"",
		"data: final",
		"", // trailing blank
	}, "\n")

	var got []sseEvent
	err := readSSE(strings.NewReader(input), func(ev sseEvent) bool {
		got = append(got, ev)
		return true
	})
	if err != nil {
		t.Fatalf("readSSE: %v", err)
	}

	want := []sseEvent{
		{name: "message_start", data: `{"a":1}`},
		{name: "", data: "line1\nline2"},
		{name: "", data: "final"},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadSSEStopsWhenYieldReturnsFalse(t *testing.T) {
	t.Parallel()
	input := "data: one\n\ndata: two\n\n"
	var got []string
	err := readSSE(strings.NewReader(input), func(ev sseEvent) bool {
		got = append(got, ev.data)
		return false
	})
	if err != nil {
		t.Fatalf("readSSE: %v", err)
	}
	if len(got) != 1 || got[0] != "one" {
		t.Fatalf("got %v, want [one]", got)
	}
}
