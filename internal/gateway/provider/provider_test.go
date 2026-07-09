package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// collect drains a stream into a slice, failing the test if the
// channel does not close within the deadline.
func collect(t *testing.T, ch <-chan stream.StreamEvent) []stream.StreamEvent {
	t.Helper()
	var events []stream.StreamEvent
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-deadline:
			t.Fatalf("stream did not close; got %d events: %+v", len(events), events)
		}
	}
}

func eventsOfType(events []stream.StreamEvent, typ stream.EventType) []stream.StreamEvent {
	var out []stream.StreamEvent
	for _, ev := range events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func textOf(events []stream.StreamEvent, typ stream.EventType) string {
	var b strings.Builder
	for _, ev := range eventsOfType(events, typ) {
		b.WriteString(ev.Text)
	}
	return b.String()
}

func lastType(t *testing.T, events []stream.StreamEvent) stream.EventType {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	return events[len(events)-1].Type
}
