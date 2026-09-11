package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

func TestAssembleSystemDateLine(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 28, 15, 4, 5, 0, time.UTC)
	got := assembleSystem("", "", false, now, nil)

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
	got := assembleSystem("", "", false, now, nil)

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
	got := assembleSystem("", "", false, now, loc)

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
		got := assembleSystem("", "", false, now, nil)
		if !strings.Contains(got, want) {
			t.Fatalf("timezone steer missing:\n%s\nwant substring:\n%s", got, want)
		}
	})

	t.Run("operator location", func(t *testing.T) {
		loc, err := time.LoadLocation("Europe/Amsterdam")
		if err != nil {
			t.Fatalf("load location: %v", err)
		}
		got := assembleSystem("", "", false, now, loc)
		if !strings.Contains(got, want) {
			t.Fatalf("timezone steer missing:\n%s\nwant substring:\n%s", got, want)
		}
	})
}

// TestAssembleSystemIncludesKBNudge pins that the assembled prompt
// tells the model to consult search_kb for questions that plausibly
// overlap curated content (issue #429), matching the mission explore
// nudge (#405) in spirit but scoped to chat's own tool guidance.
func TestAssembleSystemIncludesKBNudge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 28, 0, 0, 0, 0, time.UTC)
	got := assembleSystem("", "", false, now, nil)

	want := "curated knowledge base of their own notes and reference material, reachable via search_kb"
	if !strings.Contains(got, want) {
		t.Fatalf("KB nudge missing:\n%s\nwant substring:\n%s", got, want)
	}
}

func TestAssembleSystemCloseStaysLast(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 28, 0, 0, 0, 0, time.UTC)

	for _, skillsIndex := range []string{"", "# Skills\n\n- foo: does foo"} {
		for _, style := range []string{"", "Short sentences."} {
			for _, samples := range []bool{false, true} {
				got := assembleSystem(skillsIndex, style, samples, now, nil)
				if !strings.HasSuffix(got, systemPromptClose) {
					t.Fatalf("close steer not last line (skillsIndex=%q style=%q samples=%v):\n%s", skillsIndex, style, samples, got)
				}
			}
		}
	}
}

func TestAssembleSystemStablePrefixUnchangedByDate(t *testing.T) {
	t.Parallel()
	skillsIndex := "# Skills\n\n- foo: does foo"
	day1 := assembleSystem(skillsIndex, "", false, time.Date(2026, time.July, 28, 0, 0, 0, 0, time.UTC), nil)
	day2 := assembleSystem(skillsIndex, "", false, time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC), nil)

	prefix := systemPrompt + "\n\n" + skillsIndex
	if !strings.HasPrefix(day1, prefix) || !strings.HasPrefix(day2, prefix) {
		t.Fatalf("identity + skills index prefix changed across days")
	}
	if day1 == day2 {
		t.Fatalf("expected date line to differ across days, got identical output")
	}

	// The writing-style block joins that same cacheable prefix.
	style := "Short sentences. No em dashes."
	styled1 := assembleSystem(skillsIndex, style, true, time.Date(2026, time.July, 28, 0, 0, 0, 0, time.UTC), nil)
	styled2 := assembleSystem(skillsIndex, style, true, time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC), nil)
	styledPrefix := systemPrompt + "\n\n" + missions.WritingStyleBlock(style, true) + "\n\n" + skillsIndex
	if !strings.HasPrefix(styled1, styledPrefix) || !strings.HasPrefix(styled2, styledPrefix) {
		t.Fatalf("writing-style prefix not stable across days:\n%s", styled1)
	}
}

// TestAssembleSystemWritingStyleBlock pins the operator writing-style
// block: style alone, samples alone, both, and absent when neither is
// configured.
func TestAssembleSystemWritingStyleBlock(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 28, 0, 0, 0, 0, time.UTC)
	style := "Short sentences. No em dashes."

	t.Run("style only", func(t *testing.T) {
		got := assembleSystem("", style, false, now, nil)
		if !strings.Contains(got, missions.WritingStyleHeading) || !strings.Contains(got, style) {
			t.Fatalf("writing-style block missing:\n%s", got)
		}
		if strings.Contains(got, missions.WritingSamplesNote) {
			t.Fatalf("samples note present with no samples collection:\n%s", got)
		}
	})

	t.Run("samples only", func(t *testing.T) {
		got := assembleSystem("", "", true, now, nil)
		if !strings.Contains(got, missions.WritingStyleHeading) || !strings.Contains(got, missions.WritingSamplesNote) {
			t.Fatalf("samples-only block missing:\n%s", got)
		}
	})

	t.Run("both", func(t *testing.T) {
		got := assembleSystem("", style, true, now, nil)
		if !strings.Contains(got, style) || !strings.Contains(got, missions.WritingSamplesNote) {
			t.Fatalf("combined block missing a half:\n%s", got)
		}
	})

	t.Run("neither", func(t *testing.T) {
		got := assembleSystem("", "", false, now, nil)
		if strings.Contains(got, missions.WritingStyleHeading) {
			t.Fatalf("writing-style block rendered with nothing configured:\n%s", got)
		}
	})
}
