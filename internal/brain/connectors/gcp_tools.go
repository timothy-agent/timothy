package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// Cloud Storage and BigQuery read tools on the gcp kind (issue #736).
// Every tool here is marked ReadOnly: listing and reading objects are
// pure GETs, and run_bigquery_query rejects anything but a single
// SELECT/WITH before sending it (see checkReadOnlySQL). Writes
// (uploading objects, DML, job management) deliberately have no tool.

const (
	gcpListDefault = 50
	gcpListMax     = 200
)

// --- Cloud Storage ---

func (s *gcpSource) storageList() *tools.Tool {
	return &tools.Tool{
		Name:     "list_gcs_objects",
		ReadOnly: true,
		Description: `List objects in a Google Cloud Storage bucket, with name,
size, content type, and update time. prefix narrows to a path (e.g.
"reports/2026/"). Returns up to max_results (default 50) objects.
Use read_gcs_object with a name from this list to read one object's
content; this tool returns names only, never content.`,
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"bucket":{"type":"string","description":"bucket name, without the gs:// prefix"},
			"prefix":{"type":"string","description":"optional object name prefix to filter by"},
			"max_results":{"type":"integer","minimum":1,"maximum":200}
		},"required":["bucket"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Bucket     string `json:"bucket"`
				Prefix     string `json:"prefix"`
				MaxResults int    `json:"max_results"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			bucket, err := gcpBucketArg(in.Bucket)
			if err != nil {
				return "", err
			}
			if in.MaxResults <= 0 {
				in.MaxResults = gcpListDefault
			}
			if in.MaxResults > gcpListMax {
				in.MaxResults = gcpListMax
			}
			q := url.Values{"maxResults": {fmt.Sprint(in.MaxResults)}}
			if in.Prefix != "" {
				q.Set("prefix", in.Prefix)
			}
			var res struct {
				Items []struct {
					Name        string `json:"name"`
					Size        string `json:"size"`
					ContentType string `json:"contentType"`
					Updated     string `json:"updated"`
				} `json:"items"`
			}
			if err := s.api(ctx, http.MethodGet,
				s.storageBase+"/b/"+url.PathEscape(bucket)+"/o?"+q.Encode(),
				"list gcs objects", nil, &res); err != nil {
				return "", err
			}
			if len(res.Items) == 0 {
				if in.Prefix != "" {
					return fmt.Sprintf("no objects in gs://%s matching prefix %q", bucket, in.Prefix), nil
				}
				return fmt.Sprintf("no objects in gs://%s", bucket), nil
			}
			var b strings.Builder
			for _, o := range res.Items {
				fmt.Fprintf(&b, "%s\tsize: %s\tcontentType: %s\tupdated: %s\n",
					o.Name, o.Size, o.ContentType, o.Updated)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func (s *gcpSource) storageRead() *tools.Tool {
	return &tools.Tool{
		Name:     "read_gcs_object",
		ReadOnly: true,
		Description: `Read one Google Cloud Storage object's content as text, given
its bucket and full object name (from list_gcs_objects). Intended for
text formats (JSON, CSV, logs, plain text). Long content is
truncated. Binary objects come back as unreadable bytes; do not use
this to fetch images or archives.`,
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"bucket":{"type":"string","description":"bucket name, without the gs:// prefix"},
			"name":{"type":"string","description":"full object name from list_gcs_objects"}
		},"required":["bucket","name"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Bucket string `json:"bucket"`
				Name   string `json:"name"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			bucket, err := gcpBucketArg(in.Bucket)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(in.Name) == "" {
				return "", fmt.Errorf("name must be an object name from list_gcs_objects")
			}
			resp, err := s.rawAPI(ctx, http.MethodGet,
				s.storageBase+"/b/"+url.PathEscape(bucket)+"/o/"+url.PathEscape(in.Name)+"?alt=media",
				"read gcs object", nil)
			if err != nil {
				return "", err
			}
			defer func() { _ = resp.Body.Close() }()
			raw, err := io.ReadAll(io.LimitReader(resp.Body, gcpObjectReadMax+1))
			if err != nil {
				return "", fmt.Errorf("read gcs object: %w", err)
			}
			text := string(raw)
			if len(text) > gcpObjectReadMax {
				text = text[:gcpObjectReadMax] + "\n\n[truncated: object continues]"
			}
			return fmt.Sprintf("gs://%s/%s\n\n%s", bucket, in.Name, text), nil
		},
	}
}

// gcpBucketArg normalizes a bucket argument, accepting the gs:// form
// models reach for and rejecting anything carrying a path.
func gcpBucketArg(bucket string) (string, error) {
	name := strings.TrimPrefix(strings.TrimSpace(bucket), "gs://")
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("bucket must be a bucket name with no path, got %q", bucket)
	}
	return name, nil
}

// --- BigQuery ---

func (s *gcpSource) bigQueryQuery() *tools.Tool {
	return &tools.Tool{
		Name:     "run_bigquery_query",
		ReadOnly: true,
		Description: `Run a BigQuery SQL query against the connected project and
return the result rows. Standard SQL only; fully qualify tables as ` +
			"`project.dataset.table`" + ` unless they live in the connected project.
The query must be a single statement starting with SELECT or WITH;
anything that writes data or changes schema is rejected before it is
sent. Rows are capped, so add LIMIT and aggregate in SQL rather than
expecting a full table back. A query that has not finished within the
tool's wait reports so instead of returning rows; narrow it and
retry.`,
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"query":{"type":"string","description":"standard SQL SELECT"},
			"max_results":{"type":"integer","minimum":1,"maximum":200}
		},"required":["query"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Query      string `json:"query"`
				MaxResults int    `json:"max_results"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			if err := checkReadOnlySQL(in.Query); err != nil {
				return "", err
			}
			if in.MaxResults <= 0 || in.MaxResults > gcpQueryMaxRows {
				in.MaxResults = gcpQueryMaxRows
			}
			body := map[string]any{
				"query":        in.Query,
				"useLegacySql": false,
				"maxResults":   in.MaxResults,
				"timeoutMs":    gcpQueryTimeout.Milliseconds(),
			}
			if s.cfg.Location != "" {
				body["location"] = s.cfg.Location
			}
			var res bigQueryResponse
			if err := s.api(ctx, http.MethodPost,
				s.bigQueryBase+"/projects/"+url.PathEscape(s.project)+"/queries",
				"run bigquery query", body, &res); err != nil {
				return "", err
			}
			if !res.JobComplete {
				return "", fmt.Errorf("run bigquery query: the query is still running after %s; narrow it (add a WHERE clause, a LIMIT, or aggregate in SQL) and retry", gcpQueryTimeout)
			}
			return renderBigQueryRows(res, in.MaxResults), nil
		},
	}
}

// bigQueryWriteStatements are the leading keywords of every BigQuery
// statement that writes data, changes schema, or runs a script.
var bigQueryWriteStatements = []string{
	"insert", "update", "delete", "merge", "truncate", "drop", "alter",
	"create", "grant", "revoke", "call", "export", "load", "begin",
	"declare", "set", "execute",
}

// checkReadOnlySQL rejects anything but a leading SELECT or WITH. The
// tool is marked ReadOnly and missions' connector-reads resolver takes
// that marker at its word, so the guard is Go code here, not a line in
// the tool description (nor a trusted IAM grant, which an operator may
// well have made broader). Comment-prefixed queries are rejected
// rather than parsed: a query whose first keyword cannot be read
// cheaply is not one this tool should be sending.
func checkReadOnlySQL(query string) error {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return fmt.Errorf("query is required")
	}
	first := strings.ToLower(strings.TrimLeft(trimmed, "("))
	if idx := strings.IndexFunc(first, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '('
	}); idx > 0 {
		first = first[:idx]
	}
	if first == "select" || first == "with" {
		if strings.ContainsAny(trimmed, ";") && !isTrailingSemicolonOnly(trimmed) {
			return fmt.Errorf("query must be a single statement; remove the %q separating statements", ";")
		}
		return nil
	}
	for _, kw := range bigQueryWriteStatements {
		if first == kw {
			return fmt.Errorf("run_bigquery_query is read-only: %s statements are not allowed, use SELECT or WITH", strings.ToUpper(kw))
		}
	}
	return fmt.Errorf("run_bigquery_query is read-only: the query must start with SELECT or WITH, got %q", first)
}

// isTrailingSemicolonOnly reports whether the only semicolon in query
// terminates it: the harmless form, as opposed to a second statement
// smuggled in behind a SELECT.
func isTrailingSemicolonOnly(query string) bool {
	return strings.Count(query, ";") == 1 && strings.HasSuffix(strings.TrimSpace(query), ";")
}

// bigQueryResponse is the subset of jobs.query's reply the tool
// renders. Values arrive as {"v": ...} wrappers, one per field.
type bigQueryResponse struct {
	JobComplete bool `json:"jobComplete"`
	Schema      struct {
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
	} `json:"schema"`
	Rows []struct {
		F []struct {
			V json.RawMessage `json:"v"`
		} `json:"f"`
	} `json:"rows"`
	TotalRows string `json:"totalRows"`
}

// renderBigQueryRows prints the result as a tab-separated header plus
// rows, noting when more rows exist than the cap returned.
func renderBigQueryRows(res bigQueryResponse, maxResults int) string {
	if len(res.Rows) == 0 {
		return "the query returned no rows"
	}
	names := make([]string, 0, len(res.Schema.Fields))
	for _, f := range res.Schema.Fields {
		names = append(names, f.Name)
	}
	var b strings.Builder
	b.WriteString(strings.Join(names, "\t"))
	b.WriteString("\n")
	for _, row := range res.Rows {
		cells := make([]string, 0, len(row.F))
		for _, c := range row.F {
			cells = append(cells, bigQueryCell(c.V))
		}
		b.WriteString(strings.Join(cells, "\t"))
		b.WriteString("\n")
	}
	out := strings.TrimRight(b.String(), "\n")
	if len(res.Rows) >= maxResults && res.TotalRows != "" && res.TotalRows != fmt.Sprint(len(res.Rows)) {
		out += fmt.Sprintf("\n\n[truncated: %d of %s rows]", len(res.Rows), res.TotalRows)
	}
	return out
}

// bigQueryCell flattens one cell value. Scalars arrive as JSON strings
// (BigQuery stringifies every scalar type); structs and arrays arrive
// as nested JSON and are printed as-is.
func bigQueryCell(v json.RawMessage) string {
	if len(v) == 0 || string(v) == "null" {
		return "NULL"
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s
	}
	return string(v)
}
