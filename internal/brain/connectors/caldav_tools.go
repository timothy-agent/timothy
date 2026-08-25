package connectors

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// caldavListDefault mirrors microsoft's calendar_list_events default
// max_results.
const caldavListDefault = 20

// caldavEvent is one parsed VEVENT: start/end rendered exactly as
// stored (offset preserved), title, and location.
type caldavEvent struct {
	Start, End string
	Summary    string
	Location   string
}

func (s *caldavSource) calendarListEvents() *tools.Tool {
	return &tools.Tool{
		Name:        "calendar_list_events",
		ReadOnly:    true,
		Description: "List events from the connected calendar in a time window. Omit time_min/time_max for the default window, the next 7 days from now; set them (RFC3339 UTC) only when the goal needs a different window, computed from today's actual date. Returns start, end, title, and location per event.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"time_min":{"type":"string","description":"RFC3339 UTC timestamp; omit for the default window"},
			"time_max":{"type":"string","description":"RFC3339 UTC timestamp; omit for the default window"},
			"max_results":{"type":"integer","minimum":1,"maximum":50}
		},"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				TimeMin    string `json:"time_min"`
				TimeMax    string `json:"time_max"`
				MaxResults int    `json:"max_results"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			if in.MaxResults <= 0 {
				in.MaxResults = caldavListDefault
			}
			var err error
			windowStart := time.Now().UTC()
			windowEnd := windowStart.AddDate(0, 0, 7)
			timeMin, timeMax := windowStart.Format(caldavTimeFormat), windowEnd.Format(caldavTimeFormat)
			if in.TimeMin != "" {
				windowStart, timeMin, err = reformatCalDAVTime(in.TimeMin)
				if err != nil {
					return "", fmt.Errorf("time_min must be RFC3339, e.g. 2026-07-22T15:00:00Z: %w", err)
				}
			}
			if in.TimeMax != "" {
				windowEnd, timeMax, err = reformatCalDAVTime(in.TimeMax)
				if err != nil {
					return "", fmt.Errorf("time_max must be RFC3339, e.g. 2026-07-22T15:00:00Z: %w", err)
				}
			}

			events, err := s.queryEvents(ctx, timeMin, timeMax, windowStart, windowEnd)
			if err != nil {
				return "", err
			}
			if len(events) == 0 {
				return "no events in the window", nil
			}
			sort.Slice(events, func(i, j int) bool { return events[i].Start < events[j].Start })
			if len(events) > in.MaxResults {
				events = events[:in.MaxResults]
			}
			var b strings.Builder
			for _, e := range events {
				fmt.Fprintf(&b, "%s → %s  %s", e.Start, e.End, e.Summary)
				if e.Location != "" {
					fmt.Fprintf(&b, " (%s)", e.Location)
				}
				b.WriteString("\n")
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

// caldavTimeFormat is the CalDAV REPORT time-range filter's wire
// format (RFC5545 UTC form).
const caldavTimeFormat = "20060102T150405Z"

// reformatCalDAVTime parses an RFC3339 timestamp (the tool's input
// format) and returns it as both a time.Time and the CalDAV REPORT
// wire format; an unparseable value errors rather than being
// interpolated into the REPORT's XML body unescaped.
func reformatCalDAVTime(rfc3339 string) (time.Time, string, error) {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return time.Time{}, "", err
	}
	t = t.UTC()
	return t, t.Format(caldavTimeFormat), nil
}

// caldavReportBody is a REPORT calendar-query for VEVENTs whose time
// span overlaps [start, end]. expand requests server-side recurrence
// expansion (CALDAV:expand); some servers 400 on it, in which case
// queryEvents retries without.
func caldavReportBody(start, end string, expand bool) string {
	var expandElem string
	if expand {
		expandElem = fmt.Sprintf(`<C:expand start="%s" end="%s"/>`, start, end)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8" ?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-data>%s</C:calendar-data>
  </D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:time-range start="%s" end="%s"/>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`, expandElem, start, end)
}

// caldavMultistatus is the subset of a REPORT/PROPFIND multistatus
// response this package reads: each response's calendar-data property.
type caldavMultistatus struct {
	XMLName   xml.Name `xml:"DAV: multistatus"`
	Responses []struct {
		Propstat []struct {
			Prop struct {
				CalendarData string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

// queryEvents runs the REPORT calendar-query with server-side expand,
// retrying without expand if the server rejects it (some CalDAV
// servers 400 on CALDAV:expand). Without server-side expand, a
// recurring event's master VEVENT carries its original DTSTART (often
// far outside the window) and an RRULE; parseCalDAVMultistatus expands
// that client-side when expanded is false, using windowStart/windowEnd.
func (s *caldavSource) queryEvents(ctx context.Context, start, end string, windowStart, windowEnd time.Time) ([]caldavEvent, error) {
	status, body, err := s.report(ctx, start, end, true)
	if err != nil {
		return nil, err
	}
	expanded := true
	if status == http.StatusBadRequest {
		expanded = false
		status, body, err = s.report(ctx, start, end, false)
		if err != nil {
			return nil, err
		}
	}
	if status != http.StatusMultiStatus {
		return nil, caldavStatusError(status, body)
	}
	return parseCalDAVMultistatus(body, expanded, windowStart, windowEnd)
}

func (s *caldavSource) report(ctx context.Context, start, end string, expand bool) (int, []byte, error) {
	return s.request(ctx, "REPORT", "", map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml; charset=utf-8",
	}, []byte(caldavReportBody(start, end, expand)))
}

// parseCalDAVMultistatus decodes a REPORT response's XML envelope and
// parses each calendar-data payload's VEVENTs. expanded/windowStart/
// windowEnd are parseVEVENTs' RRULE-expansion inputs, only used when
// expanded is false (see queryEvents).
func parseCalDAVMultistatus(body []byte, expanded bool, windowStart, windowEnd time.Time) ([]caldavEvent, error) {
	var ms caldavMultistatus
	if err := xml.Unmarshal(body, &ms); err != nil {
		return nil, fmt.Errorf("parse multistatus: %w", err)
	}
	var out []caldavEvent
	for _, r := range ms.Responses {
		for _, ps := range r.Propstat {
			if ps.Prop.CalendarData == "" {
				continue
			}
			out = append(out, parseVEVENTs(ps.Prop.CalendarData, expanded, windowStart, windowEnd)...)
		}
	}
	return out, nil
}

// caldavRRuleOccurrenceCap bounds how many occurrences one recurring
// master expands to, so a long-running or unbounded RRULE (FREQ=DAILY
// with no COUNT/UNTIL) can't make one search unbounded.
const caldavRRuleOccurrenceCap = 100

// parseVEVENTs parses one iCalendar payload's VEVENTs into caldavEvent,
// skipping any that fail to parse rather than failing the whole call.
// When expanded is true (server-side CALDAV:expand succeeded), each
// VEVENT is already one concrete occurrence and is emitted as-is. When
// false (the no-expand fallback), a VEVENT carrying an RRULE is a
// recurring master whose own DTSTART may be far outside
// [windowStart, windowEnd); its occurrences within that window are
// computed client-side instead (capped at caldavRRuleOccurrenceCap), and
// a non-recurring VEVENT entirely outside the window is dropped.
func parseVEVENTs(data string, expanded bool, windowStart, windowEnd time.Time) []caldavEvent {
	cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		return nil
	}
	var out []caldavEvent
	for _, ev := range cal.Events() {
		summary, location := "", ""
		if p := ev.Props.Get(ical.PropSummary); p != nil {
			summary = p.Value
		}
		if p := ev.Props.Get(ical.PropLocation); p != nil {
			location = p.Value
		}
		startProp := ev.Props.Get(ical.PropDateTimeStart)
		endProp := ev.Props.Get(ical.PropDateTimeEnd)

		if expanded {
			e := caldavEvent{Summary: summary, Location: location}
			if startProp != nil {
				e.Start = rfc3339FromICal(startProp.Value)
			}
			if endProp != nil {
				e.End = rfc3339FromICal(endProp.Value)
			}
			out = append(out, e)
			continue
		}

		roption, rerr := ev.Props.RecurrenceRule()
		if rerr != nil || roption == nil || startProp == nil {
			// Non-recurring: keep only if it overlaps the window.
			start, sok := parseICalTime(startProp)
			end, eok := parseICalTime(endProp)
			if sok && eok && (end.Before(windowStart) || start.After(windowEnd)) {
				continue
			}
			e := caldavEvent{Summary: summary, Location: location}
			if startProp != nil {
				e.Start = rfc3339FromICal(startProp.Value)
			}
			if endProp != nil {
				e.End = rfc3339FromICal(endProp.Value)
			}
			out = append(out, e)
			continue
		}

		masterStart, sok := parseICalTime(startProp)
		masterEnd, eok := parseICalTime(endProp)
		if !sok {
			continue
		}
		duration := time.Hour
		if eok {
			duration = masterEnd.Sub(masterStart)
		}
		roption.Dtstart = masterStart
		rule, err := rrule.NewRRule(*roption)
		if err != nil {
			continue
		}
		occurrences := rule.Between(windowStart, windowEnd, true)
		if len(occurrences) > caldavRRuleOccurrenceCap {
			occurrences = occurrences[:caldavRRuleOccurrenceCap]
		}
		for _, occStart := range occurrences {
			out = append(out, caldavEvent{
				Start:    occStart.Format(time.RFC3339),
				End:      occStart.Add(duration).Format(time.RFC3339),
				Summary:  summary,
				Location: location,
			})
		}
	}
	return out
}

// parseICalTime parses prop's iCalendar value (UTC wire form, floating
// local form, or date-only) into a time.Time; ok is false when prop is
// nil or its value doesn't match any recognized form.
func parseICalTime(prop *ical.Prop) (t time.Time, ok bool) {
	if prop == nil {
		return time.Time{}, false
	}
	if t, err := time.Parse(caldavTimeFormat, prop.Value); err == nil {
		return t, true
	}
	if t, err := time.Parse(caldavFloatingFormat, prop.Value); err == nil {
		return t, true
	}
	if t, err := time.Parse("20060102", prop.Value); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// caldavFloatingFormat is a DTSTART/DTEND value with no "Z" suffix and
// no TZID param: a floating local time, RFC5545 section 3.3.5.
const caldavFloatingFormat = "20060102T150405"

// rfc3339FromICal converts an iCalendar date, date-time, or floating
// date-time value to RFC3339 (or its no-offset prefix for a floating
// time), the format google/microsoft's calendar tools output, so the
// aggregate reads consistently across kinds and sorts lexicographically
// alongside real RFC3339 strings. Unrecognized values pass through
// unchanged (e.g. a floating time carrying a TZID param this package
// doesn't resolve).
func rfc3339FromICal(v string) string {
	if t, err := time.Parse(caldavTimeFormat, v); err == nil {
		return t.Format(time.RFC3339)
	}
	if t, err := time.Parse("20060102", v); err == nil {
		return t.Format("2006-01-02")
	}
	if t, err := time.Parse(caldavFloatingFormat, v); err == nil {
		return t.Format("2006-01-02T15:04:05")
	}
	return v
}

func (s *caldavSource) calendarCreateEvent() *tools.Tool {
	return &tools.Tool{
		Name:        "calendar_create_event",
		Description: "Create an event on the connected account's primary calendar. start and end are RFC3339 timestamps with offset, e.g. 2026-07-22T15:00:00+02:00. Use only when the user asked for an event; depending on the provider, attendees may be notified immediately.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"summary":{"type":"string","description":"event title"},
			"start":{"type":"string","description":"RFC3339 start"},
			"end":{"type":"string","description":"RFC3339 end"},
			"description":{"type":"string"},
			"location":{"type":"string"},
			"attendees":{"type":"array","items":{"type":"string"},"description":"attendee email addresses"}
		},"required":["summary","start","end"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Summary, Start, End, Description, Location string
				Attendees                                  []string
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			uid, err := newCalDAVUID()
			if err != nil {
				return "", err
			}
			raw, err := buildVEVENT(uid, in.Summary, in.Start, in.End, in.Description, in.Location, in.Attendees)
			if err != nil {
				return "", err
			}
			path := uid + ".ics"
			status, body, err := s.request(ctx, http.MethodPut, path, map[string]string{
				"Content-Type":  "text/calendar; charset=utf-8",
				"If-None-Match": "*",
			}, raw)
			if err != nil {
				return "", err
			}
			if status != http.StatusCreated && status != http.StatusNoContent {
				return "", caldavStatusError(status, body)
			}
			return "event created: " + strings.TrimRight(s.cfg.URL, "/") + "/" + path, nil
		},
	}
}

// newCalDAVUID generates a random UID for a new event's filename and
// UID property.
func newCalDAVUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate event uid: %w", err)
	}
	return hex.EncodeToString(buf) + "@timothy", nil
}

// buildVEVENT assembles a VCALENDAR/VEVENT with go-ical: UID, DTSTAMP,
// DTSTART/DTEND with their offsets preserved exactly as given, SUMMARY,
// DESCRIPTION, LOCATION, and ATTENDEE mailto: entries.
func buildVEVENT(uid, summary, start, end, description, location string, attendees []string) ([]byte, error) {
	startProp, err := caldavDateTimeProp(ical.PropDateTimeStart, start)
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	endProp, err := caldavDateTimeProp(ical.PropDateTimeEnd, end)
	if err != nil {
		return nil, fmt.Errorf("end: %w", err)
	}

	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, uid)
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	event.Props.Set(startProp)
	event.Props.Set(endProp)
	event.Props.SetText(ical.PropSummary, summary)
	if description != "" {
		event.Props.SetText(ical.PropDescription, description)
	}
	if location != "" {
		event.Props.SetText(ical.PropLocation, location)
	}
	for _, addr := range attendees {
		prop := ical.NewProp(ical.PropAttendee)
		prop.Value = "mailto:" + addr
		event.Props.Add(prop)
	}

	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Timothy//CalDAV connector//EN")
	cal.Children = append(cal.Children, event.Component)

	var b strings.Builder
	if err := ical.NewEncoder(&b).Encode(cal); err != nil {
		return nil, fmt.Errorf("encode event: %w", err)
	}
	return []byte(b.String()), nil
}

// caldavDateTimeProp builds a DTSTART/DTEND property in the RFC5545
// UTC form. A numeric offset in the input is converted to UTC rather
// than emitted as a TZID: an offset is not a valid TZID and carries no
// VTIMEZONE, and the UTC form denotes the same instant unambiguously.
func caldavDateTimeProp(name, rfc3339 string) (*ical.Prop, error) {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return nil, fmt.Errorf("invalid RFC3339 timestamp %q: %w", rfc3339, err)
	}
	prop := ical.NewProp(name)
	prop.SetDateTime(t.UTC())
	return prop, nil
}
