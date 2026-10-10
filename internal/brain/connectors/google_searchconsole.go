package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	scDefaultRowLimit = 100
	scMaxRowLimit     = 1000
	// scDataLagDays keeps the default range clear of days Search
	// Console has not finalized yet (about two days behind).
	scDataLagDays       = 3
	scDefaultWindowDays = 28
	scDateLayout        = "2006-01-02"
	// scTimeZone is the calendar Search Console reports dates in.
	scTimeZone = "America/Los_Angeles"
)

var (
	scDimensions = map[string]bool{
		"query": true, "page": true, "country": true, "device": true, "date": true, "searchAppearance": true,
	}
	// scFilterDimensions is scDimensions minus date, which the API
	// does not filter on.
	scFilterDimensions = map[string]bool{
		"query": true, "page": true, "country": true, "device": true, "searchAppearance": true,
	}
	scOperators = map[string]bool{
		"equals": true, "notEquals": true, "contains": true, "notContains": true,
		"includingRegex": true, "excludingRegex": true,
	}
	scSearchTypes = map[string]bool{
		"web": true, "image": true, "video": true, "news": true, "discover": true, "googleNews": true,
	}
	scDataStates = map[string]bool{"final": true, "all": true}
)

func (s *googleSource) searchConsoleSites() *tools.Tool {
	return &tools.Tool{
		Name:     "list_search_console_sites",
		ReadOnly: true,
		Description: `List the Google Search Console sites the connected account can
read, one per line as site identifier and permission level. The
identifier is exactly what search_console_query's site argument
takes: sc-domain:example.com for a domain property, or a full URL
with trailing slash (https://example.com/) for a URL-prefix property.`,
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
			var res struct {
				SiteEntry []struct {
					SiteURL         string `json:"siteUrl"`
					PermissionLevel string `json:"permissionLevel"`
				} `json:"siteEntry"`
			}
			if err := s.api(ctx, http.MethodGet, s.g.SearchConsoleBase+"/sites", nil, &res); err != nil {
				return "", searchConsoleError(err, "")
			}
			var b strings.Builder
			b.WriteString("site\tpermission\n")
			n := 0
			for _, e := range res.SiteEntry {
				// An unverified user sees the site listed but can read no data.
				if e.PermissionLevel == "siteUnverifiedUser" {
					continue
				}
				fmt.Fprintf(&b, "%s\t%s\n", scCell(e.SiteURL), scCell(e.PermissionLevel))
				n++
			}
			if n == 0 {
				return "no sites", nil
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func (s *googleSource) searchConsoleQuery() *tools.Tool {
	return &tools.Tool{
		Name:     "search_console_query",
		ReadOnly: true,
		Description: `Query Google Search Console search analytics for one site: clicks,
impressions, ctr and average position, grouped by the dimensions you
pick, sorted by clicks descending. Returns a tab-separated table.
site is an identifier from list_search_console_sites
(sc-domain:example.com or https://example.com/). Dates are
YYYY-MM-DD on Pacific time. Default range is the 28 days ending 3
days ago, because recent days are not final yet. To compare periods,
query equal-length windows. row_limit defaults to 100 and is capped
at 1000 per call; page with start_row. All filters must match (AND).`,
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"site":{"type":"string","description":"sc-domain:example.com or https://example.com/, as list_search_console_sites prints it"},
			"start_date":{"type":"string","description":"YYYY-MM-DD; default end_date minus 27 days"},
			"end_date":{"type":"string","description":"YYYY-MM-DD; default today minus 3 days (Pacific time)"},
			"dimensions":{"type":"array","items":{"type":"string","enum":["query","page","country","device","date","searchAppearance"]},"description":"table columns in this order; default [\"query\"]"},
			"search_type":{"type":"string","enum":["web","image","video","news","discover","googleNews"],"description":"default web"},
			"data_state":{"type":"string","enum":["final","all"],"description":"final (default) or all, which adds partial recent days"},
			"filters":{"type":"array","items":{"type":"object","properties":{
				"dimension":{"type":"string","enum":["query","page","country","device","searchAppearance"]},
				"operator":{"type":"string","enum":["equals","notEquals","contains","notContains","includingRegex","excludingRegex"]},
				"expression":{"type":"string"}
			},"required":["dimension","operator","expression"],"additionalProperties":false}},
			"row_limit":{"type":"integer","minimum":1,"description":"default 100; values above 1000 are clamped to 1000"},
			"start_row":{"type":"integer","minimum":0,"description":"zero-based row offset for paging; default 0"}
		},"required":["site"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in scQueryInput
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			req, err := buildSearchConsoleQuery(in, s.g.now())
			if err != nil {
				return "", err
			}
			var res struct {
				Rows []scRow `json:"rows"`
			}
			if err := s.api(ctx, http.MethodPost,
				s.g.SearchConsoleBase+"/sites/"+scSitePath(in.Site)+"/searchAnalytics/query", req, &res); err != nil {
				return "", searchConsoleError(err, in.Site)
			}
			return renderSearchConsoleRows(in.Site, req, res.Rows), nil
		},
	}
}

type scFilter struct {
	Dimension  string `json:"dimension"`
	Operator   string `json:"operator"`
	Expression string `json:"expression"`
}

type scQueryInput struct {
	Site       string     `json:"site"`
	StartDate  string     `json:"start_date"`
	EndDate    string     `json:"end_date"`
	Dimensions []string   `json:"dimensions"`
	SearchType string     `json:"search_type"`
	DataState  string     `json:"data_state"`
	Filters    []scFilter `json:"filters"`
	RowLimit   int        `json:"row_limit"`
	StartRow   int        `json:"start_row"`
}

type scFilterGroup struct {
	GroupType string     `json:"groupType"`
	Filters   []scFilter `json:"filters"`
}

// scQueryRequest is the searchAnalytics.query request body.
type scQueryRequest struct {
	StartDate             string          `json:"startDate"`
	EndDate               string          `json:"endDate"`
	Dimensions            []string        `json:"dimensions"`
	Type                  string          `json:"type"`
	DataState             string          `json:"dataState"`
	DimensionFilterGroups []scFilterGroup `json:"dimensionFilterGroups,omitempty"`
	RowLimit              int             `json:"rowLimit"`
	StartRow              int             `json:"startRow"`
}

type scRow struct {
	Keys        []string `json:"keys"`
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	CTR         float64  `json:"ctr"`
	Position    float64  `json:"position"`
}

// validateSearchConsoleSite accepts the two property identifier forms
// Search Console uses; a bare host matches neither.
func validateSearchConsoleSite(site string) error {
	if domain, ok := strings.CutPrefix(site, "sc-domain:"); ok {
		if domain == "" || strings.ContainsAny(domain, "/ ") {
			return fmt.Errorf("site %q: a domain property is sc-domain: followed by the domain, e.g. sc-domain:example.com", site)
		}
		return nil
	}
	u, err := url.Parse(site)
	if err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		return nil
	}
	return fmt.Errorf("site %q is not a Search Console property identifier; use sc-domain:example.com for a domain property or https://example.com/ for a URL-prefix property (list_search_console_sites prints the exact form)", site)
}

// searchConsoleRange resolves the query's date range. Defaults count
// back from today on Search Console's own calendar (Pacific time).
func searchConsoleRange(startArg, endArg string, now time.Time) (start, end string, err error) {
	loc, err := time.LoadLocation(scTimeZone)
	if err != nil {
		return "", "", fmt.Errorf("load %s: %w", scTimeZone, err)
	}
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)

	endDate := today.AddDate(0, 0, -scDataLagDays)
	if endArg != "" {
		if endDate, err = time.Parse(scDateLayout, endArg); err != nil {
			return "", "", fmt.Errorf("end_date %q: want YYYY-MM-DD", endArg)
		}
	}
	startDate := endDate.AddDate(0, 0, -(scDefaultWindowDays - 1))
	if startArg != "" {
		if startDate, err = time.Parse(scDateLayout, startArg); err != nil {
			return "", "", fmt.Errorf("start_date %q: want YYYY-MM-DD", startArg)
		}
	}
	if startDate.After(endDate) {
		return "", "", fmt.Errorf("start_date %s is after end_date %s", startDate.Format(scDateLayout), endDate.Format(scDateLayout))
	}
	return startDate.Format(scDateLayout), endDate.Format(scDateLayout), nil
}

// buildSearchConsoleQuery validates the tool input, fills defaults and
// clamps row_limit, returning the API request body.
func buildSearchConsoleQuery(in scQueryInput, now time.Time) (scQueryRequest, error) {
	if err := validateSearchConsoleSite(in.Site); err != nil {
		return scQueryRequest{}, err
	}
	start, end, err := searchConsoleRange(in.StartDate, in.EndDate, now)
	if err != nil {
		return scQueryRequest{}, err
	}

	dims := in.Dimensions
	if len(dims) == 0 {
		dims = []string{"query"}
	}
	seen := map[string]bool{}
	for _, d := range dims {
		if !scDimensions[d] {
			return scQueryRequest{}, fmt.Errorf("dimension %q: want one of query, page, country, device, date, searchAppearance", d)
		}
		if seen[d] {
			return scQueryRequest{}, fmt.Errorf("dimension %q is listed twice", d)
		}
		seen[d] = true
	}

	searchType := in.SearchType
	if searchType == "" {
		searchType = "web"
	}
	if !scSearchTypes[searchType] {
		return scQueryRequest{}, fmt.Errorf("search_type %q: want one of web, image, video, news, discover, googleNews", searchType)
	}
	dataState := in.DataState
	if dataState == "" {
		dataState = "final"
	}
	if !scDataStates[dataState] {
		return scQueryRequest{}, fmt.Errorf("data_state %q: want final or all", dataState)
	}

	for _, f := range in.Filters {
		if !scFilterDimensions[f.Dimension] {
			return scQueryRequest{}, fmt.Errorf("filter dimension %q: want one of query, page, country, device, searchAppearance", f.Dimension)
		}
		if !scOperators[f.Operator] {
			return scQueryRequest{}, fmt.Errorf("filter operator %q: want one of equals, notEquals, contains, notContains, includingRegex, excludingRegex", f.Operator)
		}
		if f.Expression == "" {
			return scQueryRequest{}, fmt.Errorf("filter on %s: expression is empty", f.Dimension)
		}
	}

	rowLimit := in.RowLimit
	if rowLimit <= 0 {
		rowLimit = scDefaultRowLimit
	}
	rowLimit = min(rowLimit, scMaxRowLimit)
	if in.StartRow < 0 {
		return scQueryRequest{}, fmt.Errorf("start_row %d: want 0 or more", in.StartRow)
	}

	req := scQueryRequest{
		StartDate: start, EndDate: end, Dimensions: dims,
		Type: searchType, DataState: dataState,
		RowLimit: rowLimit, StartRow: in.StartRow,
	}
	if len(in.Filters) > 0 {
		req.DimensionFilterGroups = []scFilterGroup{{GroupType: "and", Filters: in.Filters}}
	}
	return req, nil
}

// renderSearchConsoleRows prints the query result as a header line, a
// tab-separated table and the footnotes the numbers need.
func renderSearchConsoleRows(site string, req scQueryRequest, rows []scRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "site %s, %s to %s (Pacific time), data_state %s, search_type %s, rows %d to %d requested, %d returned\n",
		site, req.StartDate, req.EndDate, req.DataState, req.Type,
		req.StartRow+1, req.StartRow+req.RowLimit, len(rows))
	if len(rows) == 0 {
		b.WriteString("no rows")
		return b.String()
	}
	b.WriteString(strings.Join(req.Dimensions, "\t"))
	b.WriteString("\tclicks\timpressions\tctr\tposition\n")
	for _, r := range rows {
		for i := range req.Dimensions {
			key := ""
			if i < len(r.Keys) {
				key = scCell(r.Keys[i])
			}
			b.WriteString(key)
			b.WriteByte('\t')
		}
		fmt.Fprintf(&b, "%.0f\t%.0f\t%.1f%%\t%.1f\n", r.Clicks, r.Impressions, r.CTR*100, r.Position)
	}
	b.WriteString("position is an impression-weighted average.")
	for _, d := range req.Dimensions {
		if d == "query" {
			b.WriteString("\nquery rows exclude anonymized queries, so page totals exceed the sum of query rows.")
			break
		}
	}
	return b.String()
}

// scCell keeps an API string from breaking the tab-separated layout.
func scCell(v string) string {
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(v)
}

// searchConsoleError turns Search Console's access, quota and request
// errors into messages the agent can act on; site is empty for calls
// not about one site. No status here is retried.
func searchConsoleError(err error, site string) error {
	var se *googleStatusError
	if !errors.As(err, &se) {
		return err
	}
	switch {
	case (se.Status == http.StatusForbidden || se.Status == http.StatusNotFound) && site != "":
		return fmt.Errorf("the connected Google account has no access to Search Console site %q (Google: %s); list_search_console_sites shows the sites it can read", site, googleErrorMessage(se.Body))
	case se.Status == http.StatusTooManyRequests:
		return fmt.Errorf("search console quota was hit; wait a few minutes before calling again")
	case se.Status == http.StatusBadRequest:
		return fmt.Errorf("search console rejected the request: %s", googleErrorMessage(se.Body))
	}
	return err
}

// googleErrorMessage pulls error.message out of a Google API error
// body, falling back to the raw snippet.
func googleErrorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(body))
}

// scSitePath escapes a site identifier as one path segment, colon
// included, the way Google's own clients send it.
func scSitePath(site string) string {
	return strings.ReplaceAll(url.PathEscape(site), ":", "%3A")
}
