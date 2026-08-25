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
			if in.TimeMin == "" {
				in.TimeMin = time.Now().UTC().Format(caldavTimeFormat)
			} else {
				in.TimeMin = reformatCalDAVTime(in.TimeMin)
			}
			if in.TimeMax == "" {
				in.TimeMax = time.Now().UTC().AddDate(0, 0, 7).Format(caldavTimeFormat)
			} else {
				in.TimeMax = reformatCalDAVTime(in.TimeMax)
			}

			events, err := s.queryEvents(ctx, in.TimeMin, in.TimeMax)
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

// reformatCalDAVTime converts an RFC3339 UTC timestamp (the tool's
// input format) to the CalDAV wire format; an unparseable value is
// passed through unchanged rather than erroring the whole call.
func reformatCalDAVTime(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.UTC().Format(caldavTimeFormat)
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
// servers 400 on CALDAV:expand).
func (s *caldavSource) queryEvents(ctx context.Context, start, end string) ([]caldavEvent, error) {
	status, body, err := s.report(ctx, start, end, true)
	if err != nil {
		return nil, err
	}
	if status == http.StatusBadRequest {
		status, body, err = s.report(ctx, start, end, false)
		if err != nil {
			return nil, err
		}
	}
	if status != http.StatusMultiStatus {
		return nil, caldavStatusError(status, body)
	}
	return parseCalDAVMultistatus(body)
}

func (s *caldavSource) report(ctx context.Context, start, end string, expand bool) (int, []byte, error) {
	return s.request(ctx, "REPORT", "", map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml; charset=utf-8",
	}, []byte(caldavReportBody(start, end, expand)))
}

// parseCalDAVMultistatus decodes a REPORT response's XML envelope and
// parses each calendar-data payload's VEVENTs.
func parseCalDAVMultistatus(body []byte) ([]caldavEvent, error) {
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
			out = append(out, parseVEVENTs(ps.Prop.CalendarData)...)
		}
	}
	return out, nil
}

// parseVEVENTs parses one iCalendar payload's VEVENTs into caldavEvent,
// skipping any that fail to parse rather than failing the whole call.
func parseVEVENTs(data string) []caldavEvent {
	cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		return nil
	}
	var out []caldavEvent
	for _, ev := range cal.Events() {
		e := caldavEvent{}
		if p := ev.Props.Get(ical.PropSummary); p != nil {
			e.Summary = p.Value
		}
		if p := ev.Props.Get(ical.PropLocation); p != nil {
			e.Location = p.Value
		}
		if p := ev.Props.Get(ical.PropDateTimeStart); p != nil {
			e.Start = rfc3339FromICal(p.Value)
		}
		if p := ev.Props.Get(ical.PropDateTimeEnd); p != nil {
			e.End = rfc3339FromICal(p.Value)
		}
		out = append(out, e)
	}
	return out
}

// rfc3339FromICal converts an iCalendar date or date-time value to
// RFC3339, the format google/microsoft's calendar tools output, so the
// aggregate reads consistently across kinds. Unrecognized values pass
// through unchanged (e.g. floating times with a TZID param).
func rfc3339FromICal(v string) string {
	if t, err := time.Parse(caldavTimeFormat, v); err == nil {
		return t.Format(time.RFC3339)
	}
	if t, err := time.Parse("20060102", v); err == nil {
		return t.Format("2006-01-02")
	}
	return v
}

func (s *caldavSource) calendarCreateEvent() *tools.Tool {
	return &tools.Tool{
		Name:        "calendar_create_event",
		Description: "Create an event on the connected account's primary calendar. start and end are RFC3339 timestamps with offset, e.g. 2026-07-22T15:00:00+02:00. Use only when the user asked for an event; attendees receive invitations immediately.",
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
