package events

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"
)

func TestRunNow(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 23, 10, 0, 0, 500, time.FixedZone("CEST", 2*3600))
	ev, err := RunNow("a1", at)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if ev.Source != SourceManual || ev.Kind != KindRunNow {
		t.Fatalf("event = %+v, want manual run.now", ev)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(ev.DedupKey) {
		t.Fatalf("dedup key %q is not a v4 uuid", ev.DedupKey)
	}
	var p map[string]string
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p["automation_id"] != "a1" || p["requested_at"] != "2026-09-23T08:00:00Z" {
		t.Fatalf("payload = %v", p)
	}
	other, _ := RunNow("a1", at)
	if other.DedupKey == ev.DedupKey {
		t.Fatal("two run-now requests share a dedup key")
	}
	if _, err := RunNow("", at); err == nil {
		t.Fatal("RunNow without an automation id succeeded")
	}
}
