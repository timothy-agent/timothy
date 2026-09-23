package events

import (
	"testing"
	"time"
)

func TestCronDueRoundTrip(t *testing.T) {
	t.Parallel()
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	boundary := time.Date(2026, 9, 23, 9, 0, 0, 0, ams)
	ev, err := CronDue("a1", "t1", boundary)
	if err != nil {
		t.Fatalf("CronDue: %v", err)
	}
	if ev.Source != SourceCron || ev.Kind != KindCronDue || ev.DedupKey != "t1|2026-09-23T07:00:00Z" {
		t.Fatalf("event = %+v", ev)
	}
	if string(ev.Payload) != `{"automation_id":"a1","trigger_id":"t1","boundary":"2026-09-23T09:00:00+02:00"}` {
		t.Fatalf("payload = %s", ev.Payload)
	}
	p, err := DecodeCronDue(ev)
	if err != nil {
		t.Fatalf("DecodeCronDue: %v", err)
	}
	if p.AutomationID != "a1" || p.TriggerID != "t1" || !p.Boundary.Equal(boundary) || p.Boundary.Format(time.RFC3339) != "2026-09-23T09:00:00+02:00" {
		t.Fatalf("decoded = %+v", p)
	}
	if _, err := CronDue("", "t1", boundary); err == nil {
		t.Fatal("CronDue without an automation id succeeded")
	}
	if _, err := CronDue("a1", "", boundary); err == nil {
		t.Fatal("CronDue without a trigger id succeeded")
	}
	for _, payload := range []string{`{`, `{"trigger_id":"t1","boundary":"2026-09-23T09:00:00Z"}`, `{"automation_id":"a1","trigger_id":"t1"}`} {
		if _, err := DecodeCronDue(Event{Payload: []byte(payload)}); err == nil {
			t.Errorf("DecodeCronDue(%s) succeeded", payload)
		}
	}
}

func TestRunNowRoundTrip(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	ev, err := RunNow("a1", at)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	p, err := DecodeRunNow(ev)
	if err != nil {
		t.Fatalf("DecodeRunNow: %v", err)
	}
	if p.AutomationID != "a1" || !p.RequestedAt.Equal(at) {
		t.Fatalf("decoded = %+v", p)
	}
	for _, payload := range []string{`{`, `{"requested_at":"2026-09-23T10:00:00Z"}`} {
		if _, err := DecodeRunNow(Event{Payload: []byte(payload)}); err == nil {
			t.Errorf("DecodeRunNow(%s) succeeded", payload)
		}
	}
}
