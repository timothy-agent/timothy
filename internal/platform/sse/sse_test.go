package sse

import (
	"strings"
	"testing"
)

func TestRead(t *testing.T) {
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
		"",
	}, "\n")

	var got []Event
	err := Read(strings.NewReader(input), func(ev Event) bool {
		got = append(got, ev)
		return true
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	want := []Event{
		{Name: "message_start", Data: `{"a":1}`},
		{Name: "", Data: "line1\nline2"},
		{Name: "", Data: "final"},
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

func TestReadStopsWhenYieldReturnsFalse(t *testing.T) {
	t.Parallel()
	input := "data: one\n\ndata: two\n\n"
	var got []string
	err := Read(strings.NewReader(input), func(ev Event) bool {
		got = append(got, ev.Data)
		return false
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0] != "one" {
		t.Fatalf("got %v, want [one]", got)
	}
}
