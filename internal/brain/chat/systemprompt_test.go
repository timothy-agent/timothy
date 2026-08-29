package chat

import (
	"strings"
	"testing"
	"time"
)

func TestAssembleSystemDateLine(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 28, 15, 4, 5, 0, time.UTC)
	got := assembleSystem("", now, nil)

	want := "Today is Tuesday, 2026-07-28 (UTC)."
	if !strings.Contains(got, want) {
		t.Fatalf("date line missing or wrong:\n%s\nwant substring:\n%s", got, want)
	}
	if strings.Contains(got, "15:04") || strings.Contains(got, "15:04:05") {
		t.Fatalf("date line must not carry a clock time:\n%s", got)
	}
}

func TestAssembleSystemDateLineNilLocationIsUTC(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("UTC-5", -5*60*60)
	// 2026-07-28 23:30 UTC-5 == 2026-07-29 04:30 UTC.
	now := time.Date(2026, time.July, 28, 23, 30, 0, 0, loc)
	got := assembleSystem("", now, nil)

	if !strings.Contains(got, "Today is Wednesday, 2026-07-29 (UTC).") {
		t.Fatalf("date line not normalized to UTC:\n%s", got)
	}
}

func TestAssembleSystemDateLineOperatorLocation(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	// 2026-07-28 23:30 UTC == 2026-07-29 01:30 CEST.
	now := time.Date(2026, time.July, 28, 23, 30, 0, 0, time.UTC)
	got := assembleSystem("", now, loc)

	if !strings.Contains(got, "Today is Wednesday, 2026-07-29 (CEST).") {
		t.Fatalf("date line not rendered in operator location:\n%s", got)
	}
}

// TestAssembleSystemDateLineIncludesTimezoneSteer pins that the date
// line carries the timezone-presentation instruction (timezoneSteer)
// right after the date, both for an operator location and for the nil
// (UTC) default — a global harness instruction instead of per-prompt
// boilerplate.
func TestAssembleSystemDateLineIncludesTimezoneSteer(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 28, 15, 4, 5, 0, time.UTC)
	want := "Present all dates and times in this timezone"

	t.Run("nil location (UTC)", func(t *testing.T) {
		got := assembleSystem("", now, nil)
		if !strings.Contains(got, want) {
			t.Fatalf("timezone steer missing:\n%s\nwant substring:\n%s", got, want)
		}
	})

	t.Run("operator location", func(t *testing.T) {
		loc, err := time.LoadLocation("Europe/Amsterdam")
		if err != nil {
			t.Fatalf("load location: %v", err)
		}
		got := assembleSystem("", now, loc)
		if !strings.Contains(got, want) {
			t.Fatalf("timezone steer missing:\n%s\nwant substring:\n%s", got, want)
		}
	})
}

func TestAssembleSystemCloseStaysLast(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 28, 0, 0, 0, 0, time.UTC)

	for _, skillsIndex := range []string{"", "# Skills\n\n- foo: does foo"} {
		got := assembleSystem(skillsIndex, now, nil)
		if !strings.HasSuffix(got, systemPromptClose) {
			t.Fatalf("close steer not last line (skillsIndex=%q):\n%s", skillsIndex, got)
		}
	}
}

func TestAssembleSystemStablePrefixUnchangedByDate(t *testing.T) {
	t.Parallel()
	skillsIndex := "# Skills\n\n- foo: does foo"
	day1 := assembleSystem(skillsIndex, time.Date(2026, time.July, 28, 0, 0, 0, 0, time.UTC), nil)
	day2 := assembleSystem(skillsIndex, time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC), nil)

	prefix := systemPrompt + "\n\n" + skillsIndex
	if !strings.HasPrefix(day1, prefix) || !strings.HasPrefix(day2, prefix) {
		t.Fatalf("identity + skills index prefix changed across days")
	}
	if day1 == day2 {
		t.Fatalf("expected date line to differ across days, got identical output")
	}
}
