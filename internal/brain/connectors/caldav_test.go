package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// caldavServerState records what the fixture server received, for
// assertions, and configures canned/error responses.
type caldavServerState struct {
	reports        []string // REPORT request bodies received, in order
	rejectExpand   bool     // REPORT with CALDAV:expand returns 400 once when true
	expandRejected bool     // set once the 400 has been served

	puts []struct {
		path        string
		body        string
		ifNoneMatch string
	}

	propfindStatus int // 0 defaults to 207
	reportEvents   string
	reportStatus   int // 0 defaults to 207; a non-400 override short-circuits REPORT handling
}

// caldavMultistatusEvents is a small fixed multistatus body: 3 VEVENTs,
// one carrying a LOCATION.
const caldavMultistatusEvents = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:response>
    <D:href>/dav/cal/ev1.ics</D:href>
    <D:propstat>
      <D:prop>
        <C:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:ev1
DTSTAMP:20260722T090000Z
DTSTART:20260722T100000Z
DTEND:20260722T103000Z
SUMMARY:standup
END:VEVENT
END:VCALENDAR
</C:calendar-data>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/dav/cal/ev2.ics</D:href>
    <D:propstat>
      <D:prop>
        <C:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:ev2
DTSTAMP:20260722T090000Z
DTSTART:20260723T140000Z
DTEND:20260723T150000Z
SUMMARY:1:1 with bob
LOCATION:zoom
END:VEVENT
END:VCALENDAR
</C:calendar-data>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/dav/cal/ev3.ics</D:href>
    <D:propstat>
      <D:prop>
        <C:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:ev3
DTSTAMP:20260722T090000Z
DTSTART:20260724T090000Z
DTEND:20260724T093000Z
SUMMARY:early
END:VEVENT
END:VCALENDAR
</C:calendar-data>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

// caldavTestServer starts an httptest server implementing the three
// requests the caldav connector issues: PROPFIND (test/identity),
// REPORT (calendar-query, with optional expand-unsupported behavior),
// and PUT (event creation). state may be nil for defaults.
func caldavTestServer(t *testing.T, state *caldavServerState) *httptest.Server {
	t.Helper()
	if state == nil {
		state = &caldavServerState{}
	}
	if state.reportEvents == "" {
		state.reportEvents = caldavMultistatusEvents
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/dav/cal/", func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "me@example.com" || pass != "secret-pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case "PROPFIND":
			status := state.propfindStatus
			if status == 0 {
				status = http.StatusMultiStatus
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:"></D:multistatus>`))
		case "REPORT":
			body, _ := io.ReadAll(r.Body)
			state.reports = append(state.reports, string(body))
			if state.reportStatus != 0 {
				w.WriteHeader(state.reportStatus)
				_, _ = w.Write([]byte("internal error"))
				return
			}
			if state.rejectExpand && strings.Contains(string(body), "C:expand") && !state.expandRejected {
				state.expandRejected = true
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(state.reportEvents))
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			state.puts = append(state.puts, struct {
				path        string
				body        string
				ifNoneMatch string
			}{path: strings.TrimPrefix(r.URL.Path, "/dav/cal/"), body: string(body), ifNoneMatch: r.Header.Get("If-None-Match")})
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// caldavRow builds a minimal caldav-kind connectors row for tests,
// pointing at baseURL's collection.
func caldavRow(baseURL string) Connector {
	cfg := map[string]any{
		"url":      baseURL + "/dav/cal/",
		"username": "me@example.com",
	}
	raw, _ := json.Marshal(cfg)
	//nolint:gosec // G101: ref NAME in a fake config, not a credential value.
	return Connector{ID: "c1", Name: "personalcal", Kind: "caldav", Config: raw, CredentialRef: "PERSONALCAL_CALDAV_PASSWORD"}
}

// testCalDAVSource builds a caldavSource against baseURL with a fixed
// resolved password, so tests never touch the secret store.
func testCalDAVSource(t *testing.T, baseURL string) *caldavSource {
	t.Helper()
	row := caldavRow(baseURL)
	cfg, err := caldavConfig(row)
	if err != nil {
		t.Fatalf("caldavConfig: %v", err)
	}
	return &caldavSource{
		name:          row.Name,
		cfg:           cfg,
		credentialRef: row.CredentialRef,
		resolve:       func(context.Context, string) (string, error) { return "secret-pw", nil },
		client:        &http.Client{},
	}
}

func caldavToolByName(t *testing.T, src *caldavSource, name string) *tools.Tool {
	t.Helper()
	for _, tl := range src.Tools() {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("tool %s missing", name)
	return nil
}

func TestCalDAVListEventsDefaultWindow(t *testing.T) {
	t.Parallel()
	srv := caldavTestServer(t, nil)
	src := testCalDAVSource(t, srv.URL)

	out, err := caldavToolByName(t, src, "calendar_list_events").Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"standup", "1:1 with bob", "(zoom)", "early"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, want it to contain %q", out, want)
		}
	}
}

func TestCalDAVListEventsExplicitTimeRangeInReportBody(t *testing.T) {
	t.Parallel()
	state := &caldavServerState{}
	srv := caldavTestServer(t, state)
	src := testCalDAVSource(t, srv.URL)

	_, err := caldavToolByName(t, src, "calendar_list_events").Execute(t.Context(),
		json.RawMessage(`{"time_min":"2026-08-01T00:00:00Z","time_max":"2026-08-02T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(state.reports) != 1 {
		t.Fatalf("REPORT calls = %d, want 1", len(state.reports))
	}
	body := state.reports[0]
	if !strings.Contains(body, `start="20260801T000000Z"`) || !strings.Contains(body, `end="20260802T000000Z"`) {
		t.Fatalf("REPORT body = %q, want the given time range", body)
	}
}

func TestCalDAVListEventsMaxResultsCap(t *testing.T) {
	t.Parallel()
	srv := caldavTestServer(t, nil)
	src := testCalDAVSource(t, srv.URL)

	out, err := caldavToolByName(t, src, "calendar_list_events").Execute(t.Context(), json.RawMessage(`{"max_results":2}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Count(out, "→"); got != 2 {
		t.Fatalf("event lines = %d, want 2 (capped)", got)
	}
}

func TestCalDAVListEventsFormatsLines(t *testing.T) {
	t.Parallel()
	srv := caldavTestServer(t, nil)
	src := testCalDAVSource(t, srv.URL)

	out, err := caldavToolByName(t, src, "calendar_list_events").Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "2026-07-23T14:00:00Z → 2026-07-23T15:00:00Z  1:1 with bob (zoom)") {
		t.Fatalf("out = %q, want the formatted event line", out)
	}
}

func TestCalDAVListEventsFallsBackWhenExpandUnsupported(t *testing.T) {
	t.Parallel()
	state := &caldavServerState{rejectExpand: true}
	srv := caldavTestServer(t, state)
	src := testCalDAVSource(t, srv.URL)

	// Explicit window matching caldavMultistatusEvents' fixed July 2026
	// dates: the fallback path now filters non-recurring events to the
	// window (finding 4), so relying on the default now-relative window
	// would make this test's fixture data go stale.
	out, err := caldavToolByName(t, src, "calendar_list_events").Execute(t.Context(),
		json.RawMessage(`{"time_min":"2026-07-20T00:00:00Z","time_max":"2026-07-26T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "standup") {
		t.Fatalf("out = %q, want events despite the expand rejection", out)
	}
	if len(state.reports) != 2 {
		t.Fatalf("REPORT calls = %d, want 2 (expand rejected, then retried without)", len(state.reports))
	}
	if !strings.Contains(state.reports[0], "C:expand") {
		t.Fatalf("first REPORT = %q, want it to request expand", state.reports[0])
	}
	if strings.Contains(state.reports[1], "C:expand") {
		t.Fatalf("second REPORT = %q, want no expand on retry", state.reports[1])
	}
}

func TestCalDAVCreateEventPutsExpectedBody(t *testing.T) {
	t.Parallel()
	state := &caldavServerState{}
	srv := caldavTestServer(t, state)
	src := testCalDAVSource(t, srv.URL)

	out, err := caldavToolByName(t, src, "calendar_create_event").Execute(t.Context(), json.RawMessage(
		`{"summary":"1:1","start":"2026-07-23T10:00:00Z","end":"2026-07-23T10:30:00Z","location":"zoom","attendees":["b@y.com"]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "event created:") {
		t.Fatalf("out = %q, want a confirmation string", out)
	}
	if len(state.puts) != 1 {
		t.Fatalf("PUT calls = %d, want 1", len(state.puts))
	}
	put := state.puts[0]
	if put.ifNoneMatch != "*" {
		t.Fatalf("If-None-Match = %q, want *", put.ifNoneMatch)
	}
	if !strings.HasSuffix(put.path, ".ics") {
		t.Fatalf("PUT path = %q, want a .ics suffix", put.path)
	}
	for _, want := range []string{"SUMMARY:1:1", "DTSTART", "DTEND", "LOCATION:zoom", "ATTENDEE", "mailto:b@y.com", "UID:"} {
		if !strings.Contains(put.body, want) {
			t.Fatalf("PUT body = %q, want it to contain %q", put.body, want)
		}
	}
}

func TestCalDAVCreateEventGeneratesUniqueUID(t *testing.T) {
	t.Parallel()
	state := &caldavServerState{}
	srv := caldavTestServer(t, state)
	src := testCalDAVSource(t, srv.URL)

	for i := 0; i < 2; i++ {
		_, err := caldavToolByName(t, src, "calendar_create_event").Execute(t.Context(), json.RawMessage(
			fmt.Sprintf(`{"summary":"e%d","start":"2026-07-23T10:00:00Z","end":"2026-07-23T10:30:00Z"}`, i)))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
	}
	if len(state.puts) != 2 || state.puts[0].path == state.puts[1].path {
		t.Fatalf("PUT paths not unique: %+v", state.puts)
	}
}

func TestCalDAVBuilderValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		row  Connector
	}{
		{"missing url", Connector{Name: "cal", Kind: "caldav", CredentialRef: "REF", Config: json.RawMessage(`{"username":"me@example.com"}`)}},
		{"missing username", Connector{Name: "cal", Kind: "caldav", CredentialRef: "REF", Config: json.RawMessage(`{"url":"https://x/dav/cal/"}`)}},
		{"missing credential_ref", Connector{Name: "cal", Kind: "caldav", Config: json.RawMessage(`{"url":"https://x/dav/cal/","username":"me@example.com"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := CalDAVBuilder(nil)
			if _, err := b(t.Context(), tc.row, func(context.Context, string) (string, error) { return "", nil }); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestCalDAVAccountInfoAndIdentity(t *testing.T) {
	t.Parallel()
	srv := caldavTestServer(t, nil)
	src := testCalDAVSource(t, srv.URL)

	kind, email := src.AccountInfo()
	if kind != "caldav" || email != "me@example.com" {
		t.Fatalf("AccountInfo = (%q, %q)", kind, email)
	}
	id, err := src.Identity(t.Context())
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if id.Login != "me@example.com" || id.Email != "me@example.com" || id.Scopes != "caldav" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestCalDAVTestFailsOn401(t *testing.T) {
	t.Parallel()
	srv := caldavTestServer(t, nil)
	src := testCalDAVSource(t, srv.URL)
	// Wrong password: the fixture server only accepts "secret-pw".
	src.resolve = func(context.Context, string) (string, error) { return "wrong", nil }

	if err := src.Test(t.Context()); err == nil {
		t.Fatal("expected an error for a rejected credential")
	}
}

func TestRfc3339FromICal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"utc wire form", "20260722T100000Z", "2026-07-22T10:00:00Z"},
		{"date only", "20260722", "2026-07-22"},
		{"floating local form", "20260826T090000", "2026-08-26T09:00:00"},
		{"unrecognized passes through", "not-a-date", "not-a-date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := rfc3339FromICal(tc.in); got != tc.want {
				t.Fatalf("rfc3339FromICal(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCalDAVStatusError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{
		{"401 unauthorized", http.StatusUnauthorized, "credentials invalid or expired"},
		{"403 forbidden", http.StatusForbidden, "credentials invalid or expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := caldavStatusError(tc.status, []byte("body"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}

	longBody := strings.Repeat("x", 1000)
	err := caldavStatusError(http.StatusInternalServerError, []byte(longBody))
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("err = %v, want it to name status 500", err)
	}
	if strings.Count(err.Error(), "x") != 512 {
		t.Fatalf("err = %v, want the body snippet truncated to 512 bytes", err)
	}
}

func TestReformatCalDAVTime(t *testing.T) {
	t.Parallel()
	_, wire, err := reformatCalDAVTime("2026-08-01T12:30:00Z")
	if err != nil || wire != "20260801T123000Z" {
		t.Fatalf("reformatCalDAVTime(valid) = (%q, %v), want (20260801T123000Z, nil)", wire, err)
	}
	if _, _, err := reformatCalDAVTime("not-a-timestamp"); err == nil {
		t.Fatal("reformatCalDAVTime(garbage): expected an error")
	}
}

func TestCalDAVConfigEmail(t *testing.T) {
	t.Parallel()
	if got := (CalDAVConfig{Username: "me@x.com", AccountEmail: "other@x.com"}).email(); got != "other@x.com" {
		t.Fatalf("email() with account_email set = %q, want other@x.com", got)
	}
	if got := (CalDAVConfig{Username: "me@x.com"}).email(); got != "me@x.com" {
		t.Fatalf("email() falls back to username = %q, want me@x.com", got)
	}
}

func TestCalDAVConfigInvalidJSON(t *testing.T) {
	t.Parallel()
	row := Connector{Name: "cal", Kind: "caldav", Config: json.RawMessage(`not json`)}
	if _, err := caldavConfig(row); err == nil {
		t.Fatal("expected an error for invalid config JSON")
	}
}

func TestParseCalDAVMultistatusMalformedXML(t *testing.T) {
	t.Parallel()
	if _, err := parseCalDAVMultistatus([]byte("not xml"), true, time.Time{}, time.Time{}); err == nil {
		t.Fatal("expected an error for malformed multistatus XML")
	}
}

func TestCalDAVQueryEventsServerError(t *testing.T) {
	t.Parallel()
	state := &caldavServerState{reportStatus: http.StatusInternalServerError}
	srv := caldavTestServer(t, state)
	src := testCalDAVSource(t, srv.URL)

	_, err := caldavToolByName(t, src, "calendar_list_events").Execute(t.Context(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("err = %v, want a caldavStatusError naming status 500", err)
	}
}

// weeklyRRuleICS is a weekly-recurring master VEVENT dated a year
// before windowStart/windowEnd's expected range (2025-08-01), used to
// prove the no-expand fallback path expands RRULE occurrences into the
// window rather than surfacing (or dropping) the stale master date.
const weeklyRRuleICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:recurring1
DTSTAMP:20250801T090000Z
DTSTART:20250801T090000Z
DTEND:20250801T093000Z
SUMMARY:weekly sync
RRULE:FREQ=WEEKLY;COUNT=200
END:VEVENT
END:VCALENDAR
`

func TestParseVEVENTsExpandsRRuleInFallbackWindow(t *testing.T) {
	t.Parallel()
	windowStart := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	windowEnd := windowStart.AddDate(0, 0, 7)

	events := parseVEVENTs(weeklyRRuleICS, false, windowStart, windowEnd)
	if len(events) == 0 {
		t.Fatal("expected at least one occurrence inside the window")
	}
	for _, e := range events {
		if e.Summary != "weekly sync" {
			t.Fatalf("occurrence summary = %q, want weekly sync", e.Summary)
		}
		if strings.HasPrefix(e.Start, "2025-08-01") {
			t.Fatalf("occurrence start = %q, want an occurrence inside the window, not the master's own date", e.Start)
		}
		start, err := time.Parse(time.RFC3339, e.Start)
		if err != nil {
			t.Fatalf("occurrence start %q not RFC3339: %v", e.Start, err)
		}
		if start.Before(windowStart) || start.After(windowEnd) {
			t.Fatalf("occurrence start %s outside window [%s, %s]", start, windowStart, windowEnd)
		}
		end, err := time.Parse(time.RFC3339, e.End)
		if err != nil {
			t.Fatalf("occurrence end %q not RFC3339: %v", e.End, err)
		}
		if end.Sub(start) != 30*time.Minute {
			t.Fatalf("occurrence duration = %s, want 30m (master's DTEND-DTSTART)", end.Sub(start))
		}
	}
}

func TestParseVEVENTsExpandedPathIgnoresRRule(t *testing.T) {
	t.Parallel()
	// The expand path (expanded=true) trusts the server to have already
	// expanded recurrence: a master VEVENT's own DTSTART is emitted as
	// one event, RRULE untouched, regardless of the window.
	windowStart := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	windowEnd := windowStart.AddDate(0, 0, 7)

	events := parseVEVENTs(weeklyRRuleICS, true, windowStart, windowEnd)
	if len(events) != 1 || !strings.HasPrefix(events[0].Start, "2025-08-01") {
		t.Fatalf("events = %+v, want the single master VEVENT unexpanded", events)
	}
}

func TestParseVEVENTsFallbackDropsNonRecurringOutsideWindow(t *testing.T) {
	t.Parallel()
	const ics = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:old
DTSTAMP:20200101T090000Z
DTSTART:20200101T090000Z
DTEND:20200101T093000Z
SUMMARY:ancient
END:VEVENT
END:VCALENDAR
`
	windowStart := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	windowEnd := windowStart.AddDate(0, 0, 7)
	if events := parseVEVENTs(ics, false, windowStart, windowEnd); len(events) != 0 {
		t.Fatalf("events = %+v, want the out-of-window non-recurring event dropped", events)
	}
}

func TestCalDAVListEventsTimeMinInvalidErrors(t *testing.T) {
	t.Parallel()
	srv := caldavTestServer(t, nil)
	src := testCalDAVSource(t, srv.URL)

	_, err := caldavToolByName(t, src, "calendar_list_events").Execute(t.Context(),
		json.RawMessage(`{"time_min":"not-a-timestamp"}`))
	if err == nil || !strings.Contains(err.Error(), "time_min must be RFC3339") {
		t.Fatalf("err = %v, want a time_min RFC3339 error", err)
	}
}

func TestCalDAVListEventsTimeMaxInvalidErrors(t *testing.T) {
	t.Parallel()
	srv := caldavTestServer(t, nil)
	src := testCalDAVSource(t, srv.URL)

	_, err := caldavToolByName(t, src, "calendar_list_events").Execute(t.Context(),
		json.RawMessage(`{"time_max":"not-a-timestamp"}`))
	if err == nil || !strings.Contains(err.Error(), "time_max must be RFC3339") {
		t.Fatalf("err = %v, want a time_max RFC3339 error", err)
	}
}

func TestIsSecureCalDAVURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		url  string
		want bool
	}{
		{"https", "https://cal.example.com/dav/cal/", true},
		{"http public host rejected", "http://cal.example.com/dav/cal/", false},
		{"http loopback IPv4 allowed", "http://127.0.0.1:8080/dav/cal/", true},
		{"http localhost allowed", "http://localhost:8080/dav/cal/", true},
		{"http loopback IPv6 allowed", "http://[::1]:8080/dav/cal/", true},
		{"unparseable url rejected", "://not a url", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isSecureCalDAVURL(tc.url); got != tc.want {
				t.Fatalf("isSecureCalDAVURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestCalDAVConfigRejectsCleartextURL(t *testing.T) {
	t.Parallel()
	row := Connector{
		Name:   "cal",
		Kind:   "caldav",
		Config: json.RawMessage(`{"url":"http://cal.example.com/dav/cal/","username":"me@example.com"}`),
	}
	_, err := caldavConfig(row)
	if err == nil || !strings.Contains(err.Error(), "must be https") {
		t.Fatalf("err = %v, want a https-required error", err)
	}
}

func TestCalDAVConfigAllowsLoopbackHTTP(t *testing.T) {
	t.Parallel()
	row := Connector{
		Name:   "cal",
		Kind:   "caldav",
		Config: json.RawMessage(`{"url":"http://127.0.0.1:9999/dav/cal/","username":"me@example.com"}`),
	}
	if _, err := caldavConfig(row); err != nil {
		t.Fatalf("caldavConfig with a loopback http url: %v", err)
	}
}
