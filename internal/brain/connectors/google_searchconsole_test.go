package connectors

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// scNow is 22:00 on 2026-10-09 in Pacific time but already 2026-10-10
// in UTC, so the default range shows which calendar it counts on.
var scNow = time.Date(2026, 10, 10, 5, 0, 0, 0, time.UTC)

// scRecorded is one searchAnalytics.query call fakeGoogle received.
type scRecorded struct {
	escapedPath string
	body        string
}

func connectedSearchConsoleSource(t *testing.T, f *fakeGoogle, expiry time.Time) (Source, *fakeSecrets) {
	t.Helper()
	row := googleRow(searchConsoleScopes)
	g, secrets := testGoogle(t, f, row)
	g.now = func() time.Time { return scNow }
	//nolint:gosec // G117: fake token fixture.
	bundle, _ := json.Marshal(tokenBundle{AccessToken: "at-live", RefreshToken: "rt-1", Expiry: expiry})
	_ = secrets.Set(t.Context(), "PERSONAL_GOOGLE_OAUTH", string(bundle))
	src, err := g.Builder()(t.Context(), row, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return src, secrets
}

func TestValidateSearchConsoleSite(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		site string
		ok   bool
	}{
		{"sc-domain:example.com", true},
		{"https://example.com/", true},
		{"http://example.com/blog/", true},
		{"example.com", false},
		{"www.example.com/", false},
		{"sc-domain:", false},
		{"sc-domain:https://example.com/", false},
		{"ftp://example.com/", false},
		{"https:example.com", false},
		{"", false},
	} {
		err := validateSearchConsoleSite(tc.site)
		if (err == nil) != tc.ok {
			t.Fatalf("validateSearchConsoleSite(%q) = %v, want ok=%v", tc.site, err, tc.ok)
		}
		if tc.site == "example.com" && (!strings.Contains(err.Error(), "sc-domain:example.com") ||
			!strings.Contains(err.Error(), "https://example.com/")) {
			t.Fatalf("bare host error %q does not show both valid forms", err)
		}
	}
}

func TestSearchConsoleRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		start, end string
		wantStart  string
		wantEnd    string
		wantErr    string
	}{
		{name: "defaults count back from the Pacific date", wantStart: "2026-09-09", wantEnd: "2026-10-06"},
		{name: "end only keeps a 28-day window", end: "2026-08-31", wantStart: "2026-08-04", wantEnd: "2026-08-31"},
		{name: "start only ends at the default end", start: "2026-10-01", wantStart: "2026-10-01", wantEnd: "2026-10-06"},
		{name: "both given", start: "2026-01-01", end: "2026-01-31", wantStart: "2026-01-01", wantEnd: "2026-01-31"},
		{name: "bad end format", end: "06/10/2026", wantErr: "end_date"},
		{name: "bad start format", start: "2026-1-1", wantErr: "start_date"},
		{name: "start after end", start: "2026-10-05", end: "2026-10-01", wantErr: "after end_date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start, end, err := searchConsoleRange(tc.start, tc.end, scNow)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if start != tc.wantStart || end != tc.wantEnd {
				t.Fatalf("range = %s..%s, want %s..%s", start, end, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func TestBuildSearchConsoleQueryValidation(t *testing.T) {
	t.Parallel()
	base := func() scQueryInput { return scQueryInput{Site: "sc-domain:example.com"} }
	for _, tc := range []struct {
		name    string
		mutate  func(*scQueryInput)
		wantErr string
	}{
		{"bare host", func(in *scQueryInput) { in.Site = "example.com" }, "not a Search Console property"},
		{"unknown dimension", func(in *scQueryInput) { in.Dimensions = []string{"query", "keyword"} }, `dimension "keyword"`},
		{"duplicate dimension", func(in *scQueryInput) { in.Dimensions = []string{"page", "page"} }, "listed twice"},
		{"unknown search type", func(in *scQueryInput) { in.SearchType = "shopping" }, `search_type "shopping"`},
		{"unknown data state", func(in *scQueryInput) { in.DataState = "fresh" }, `data_state "fresh"`},
		{"filter on date", func(in *scQueryInput) {
			in.Filters = []scFilter{{Dimension: "date", Operator: "equals", Expression: "2026-10-01"}}
		}, `filter dimension "date"`},
		{"unknown operator", func(in *scQueryInput) {
			in.Filters = []scFilter{{Dimension: "query", Operator: "startsWith", Expression: "cat"}}
		}, `filter operator "startsWith"`},
		{"empty expression", func(in *scQueryInput) {
			in.Filters = []scFilter{{Dimension: "page", Operator: "contains"}}
		}, "expression is empty"},
		{"negative start_row", func(in *scQueryInput) { in.StartRow = -1 }, "start_row -1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := base()
			tc.mutate(&in)
			_, err := buildSearchConsoleQuery(in, scNow)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestBuildSearchConsoleQueryDefaultsAndClamp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		rowLimit int
		want     int
	}{
		{0, 100},
		{1, 1},
		{250, 250},
		{1000, 1000},
		{5000, 1000},
	} {
		req, err := buildSearchConsoleQuery(scQueryInput{Site: "sc-domain:example.com", RowLimit: tc.rowLimit}, scNow)
		if err != nil {
			t.Fatal(err)
		}
		if req.RowLimit != tc.want {
			t.Fatalf("row_limit %d -> %d, want %d", tc.rowLimit, req.RowLimit, tc.want)
		}
		if strings.Join(req.Dimensions, ",") != "query" || req.Type != "web" || req.DataState != "final" ||
			req.DimensionFilterGroups != nil || req.StartRow != 0 {
			t.Fatalf("defaults = %+v", req)
		}
	}
}

func TestSearchConsoleQueryRequestAndRendering(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	src, _ := connectedSearchConsoleSource(t, f, time.Now().Add(time.Hour))

	out, err := toolByName(t, src, "search_console_query").Execute(t.Context(), json.RawMessage(`{
		"site":"https://example.com/","dimensions":["query","page"],"search_type":"discover",
		"data_state":"all","filters":[{"dimension":"query","operator":"notContains","expression":"timothy"},
		{"dimension":"page","operator":"includingRegex","expression":"/blog/"}],
		"row_limit":5000,"start_row":10}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(f.scQueries) != 1 {
		t.Fatalf("query calls = %d, want 1", len(f.scQueries))
	}
	got := f.scQueries[0]
	if want := "/sites/https%3A%2F%2Fexample.com%2F/searchAnalytics/query"; got.escapedPath != want {
		t.Fatalf("path = %s, want %s", got.escapedPath, want)
	}
	wantBody := `{"startDate":"2026-09-09","endDate":"2026-10-06","dimensions":["query","page"],` +
		`"type":"discover","dataState":"all","dimensionFilterGroups":[{"groupType":"and","filters":[` +
		`{"dimension":"query","operator":"notContains","expression":"timothy"},` +
		`{"dimension":"page","operator":"includingRegex","expression":"/blog/"}]}],` +
		`"rowLimit":1000,"startRow":10}`
	if got.body != wantBody {
		t.Fatalf("body =\n%s\nwant\n%s", got.body, wantBody)
	}

	wantOut := strings.Join([]string{
		"site https://example.com/, 2026-09-09 to 2026-10-06 (Pacific time), data_state all, search_type discover, rows 11 to 1010 requested, 2 returned",
		"query\tpage\tclicks\timpressions\tctr\tposition",
		"timothy cat\thttps://example.com/cat\t120\t2400\t5.0%\t3.5",
		"tab query\thttps://example.com/\t7\t1000\t0.7%\t12.0",
		"position is an impression-weighted average.",
		"query rows exclude anonymized queries, so page totals exceed the sum of query rows.",
	}, "\n")
	if out != wantOut {
		t.Fatalf("output =\n%s\nwant\n%s", out, wantOut)
	}
}

func TestRenderSearchConsoleRows(t *testing.T) {
	t.Parallel()
	req := scQueryRequest{
		StartDate: "2026-09-09", EndDate: "2026-10-06", Dimensions: []string{"page", "device"},
		Type: "web", DataState: "final", RowLimit: 100,
	}
	header := "site sc-domain:example.com, 2026-09-09 to 2026-10-06 (Pacific time), data_state final, search_type web, rows 1 to 100 requested, "
	for _, tc := range []struct {
		name string
		rows []scRow
		want string
	}{
		{
			name: "no rows",
			want: header + "0 returned\nno rows",
		},
		{
			name: "no query dimension omits the anonymized footer",
			rows: []scRow{{Keys: []string{"https://example.com/", "MOBILE"}, Clicks: 3, Impressions: 41, CTR: 0.073170, Position: 8.25}},
			want: header + "1 returned\npage\tdevice\tclicks\timpressions\tctr\tposition\n" +
				"https://example.com/\tMOBILE\t3\t41\t7.3%\t8.2\n" +
				"position is an impression-weighted average.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := renderSearchConsoleRows("sc-domain:example.com", req, tc.rows); got != tc.want {
				t.Fatalf("render =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestSearchConsoleQueryNoRows(t *testing.T) {
	t.Parallel()
	src, _ := connectedSearchConsoleSource(t, &fakeGoogle{}, time.Now().Add(time.Hour))
	out, err := toolByName(t, src, "search_console_query").Execute(t.Context(),
		json.RawMessage(`{"site":"sc-domain:empty.example"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "0 returned\nno rows") || strings.Contains(out, "position is") {
		t.Fatalf("output = %q", out)
	}
}

func TestListSearchConsoleSites(t *testing.T) {
	t.Parallel()
	src, _ := connectedSearchConsoleSource(t, &fakeGoogle{}, time.Now().Add(time.Hour))
	out, err := toolByName(t, src, "list_search_console_sites").Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "site\tpermission\nsc-domain:example.com\tsiteOwner\nhttps://blog.example.com/\tsiteFullUser"
	if out != want {
		t.Fatalf("output =\n%s\nwant\n%s", out, want)
	}
}

func TestSearchConsoleQueryErrorMapping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		site string
		want string
	}{
		{"sc-domain:forbidden.example", `the connected Google account has no access to Search Console site "sc-domain:forbidden.example" (Google: User does not have sufficient permission for site); list_search_console_sites shows the sites it can read`},
		{"sc-domain:quota.example", "search console quota was hit; wait a few minutes before calling again"},
		{"sc-domain:bad.example", "search console rejected the request: Invalid regex in filter"},
	} {
		t.Run(tc.site, func(t *testing.T) {
			t.Parallel()
			f := &fakeGoogle{}
			src, _ := connectedSearchConsoleSource(t, f, time.Now().Add(time.Hour))
			_, err := toolByName(t, src, "search_console_query").Execute(t.Context(),
				json.RawMessage(`{"site":"`+tc.site+`"}`))
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(f.scQueries) != 1 {
				t.Fatalf("query calls = %d, want exactly 1 (no retry)", len(f.scQueries))
			}
		})
	}
}

func TestSearchConsoleError(t *testing.T) {
	t.Parallel()
	other := errors.New("dial tcp: refused")
	for _, tc := range []struct {
		name string
		err  error
		site string
		want string
	}{
		{"404 names the site", &googleStatusError{Status: 404, Body: []byte(`{"error":{"message":"Not found"}}`)}, "sc-domain:x.example",
			`the connected Google account has no access to Search Console site "sc-domain:x.example" (Google: Not found); list_search_console_sites shows the sites it can read`},
		{"403 without a site keeps the generic message", &googleStatusError{Status: 403, Body: []byte(`denied`)}, "",
			"google api status 403: denied"},
		{"429 on the sites list", &googleStatusError{Status: 429}, "",
			"search console quota was hit; wait a few minutes before calling again"},
		{"400 with a non-JSON body", &googleStatusError{Status: 400, Body: []byte(" bad request \n")}, "sc-domain:x.example",
			"search console rejected the request: bad request"},
		{"500 passes through", &googleStatusError{Status: 500, Body: []byte(`oops`)}, "sc-domain:x.example",
			"google api status 500: oops"},
		{"non-status error passes through", other, "sc-domain:x.example", other.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := searchConsoleError(tc.err, tc.site).Error(); got != tc.want {
				t.Fatalf("searchConsoleError = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSearchConsoleToolsRefreshExpiredToken(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tool string
		args string
	}{
		{"list_search_console_sites", `{}`},
		{"search_console_query", `{"site":"sc-domain:example.com"}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			t.Parallel()
			f := &fakeGoogle{}
			src, _ := connectedSearchConsoleSource(t, f, time.Now().Add(-time.Hour))
			if _, err := toolByName(t, src, tc.tool).Execute(t.Context(), json.RawMessage(tc.args)); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if len(f.tokenForms) != 1 || f.tokenForms[0].Get("grant_type") != "refresh_token" {
				t.Fatalf("token calls = %v, want one refresh", f.tokenForms)
			}
			if last := f.authTokens[len(f.authTokens)-1]; last != "Bearer at-refresh_token" {
				t.Fatalf("API call used %q, want the refreshed token", last)
			}
		})
	}
}

// TestSearchConsoleAccountRouting runs the aggregated tool through the
// Manager with two google accounts and checks the account argument
// picks whose token the call carries.
func TestSearchConsoleAccountRouting(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	personal := googleRow(searchConsoleScopes)
	personal.Enabled = true
	work := googleRow(searchConsoleScopes)
	work.ID, work.Name, work.Enabled = "c2", "work", true
	work.CredentialRef = "WORK_GOOGLE_OAUTH"
	g, secrets := testGoogle(t, f, personal)
	for ref, token := range map[string]string{"PERSONAL_GOOGLE_OAUTH": "at-personal", "WORK_GOOGLE_OAUTH": "at-work"} {
		//nolint:gosec // G117: fake token fixture.
		bundle, _ := json.Marshal(tokenBundle{AccessToken: token, RefreshToken: "rt", Expiry: time.Now().Add(time.Hour)})
		_ = secrets.Set(t.Context(), ref, string(bundle))
	}

	m := testManager(fakeRows{rows: []Connector{personal, work}})
	m.RegisterBuilder("google", g.Builder())
	if err := m.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	tl := toolNamed(t, m.Tools(nil), "list_search_console_sites")
	if _, err := tl.Execute(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("two accounts and no account argument: want an error")
	}
	for _, account := range []string{"work", "personal"} {
		if _, err := tl.Execute(t.Context(), json.RawMessage(`{"account":"`+account+`"}`)); err != nil {
			t.Fatalf("Execute(account=%s): %v", account, err)
		}
		if last := f.authTokens[len(f.authTokens)-1]; last != "Bearer at-"+account {
			t.Fatalf("account %s: call used %q", account, last)
		}
	}
	// Missions see it through the read-only surface.
	toolNamed(t, m.ReadOnlyTools(), "search_console_query")
}

// TestSearchConsoleOnlyConnectorTestPasses pins that a connector with
// only the Search Console scope passes the connection test without an
// identity, since Search Console reports no account email.
func TestSearchConsoleOnlyConnectorTestPasses(t *testing.T) {
	t.Parallel()
	f := &fakeGoogle{}
	row := googleRow(searchConsoleScopes)
	g, secrets := testGoogle(t, f, row)
	//nolint:gosec // G117: fake token fixture.
	live, _ := json.Marshal(tokenBundle{AccessToken: "at-live", Expiry: time.Now().Add(time.Hour)})
	_ = secrets.Set(t.Context(), "PERSONAL_GOOGLE_OAUTH", string(live))

	m := testManager(fakeRows{rows: []Connector{row}})
	m.RegisterBuilder("google", g.Builder())
	report, err := m.TestReport(t.Context(), row.ID)
	if err != nil {
		t.Fatalf("TestReport: %v", err)
	}
	if report.Identity != nil {
		t.Fatalf("identity = %+v, want none", report.Identity)
	}
	if len(f.authTokens) != 1 {
		t.Fatalf("API calls = %d, want the one sites listing", len(f.authTokens))
	}
}
