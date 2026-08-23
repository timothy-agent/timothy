package connectors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
)

// Builder returns the Builder for kind='google'. The tool surface is
// scope-gated: only the services the user consented to appear.
func (g *Google) Builder() Builder {
	return func(ctx context.Context, c Connector, _ Resolve) (Source, error) {
		cfg, err := googleConfig(c)
		if err != nil {
			return nil, err
		}
		if c.CredentialRef == "" {
			return nil, fmt.Errorf("google %s: credential_ref is required (it names the stored OAuth tokens)", c.Name)
		}
		src := &googleSource{g: g, cfg: cfg, ref: c.CredentialRef}
		if src.hasScope("gmail") {
			src.toolList = append(src.toolList, src.gmailSearch(), src.gmailRead(), src.gmailReadAttachment(), src.gmailSend())
		}
		if src.hasScope("calendar") {
			src.toolList = append(src.toolList, src.calendarListEvents(), src.calendarCreateEvent())
		}
		if src.hasExactScope(driveReadonlyScope) {
			src.toolList = append(src.toolList, src.driveSearch(), src.driveRead())
		}
		if src.hasExactScope(documentsScope) {
			src.toolList = append(src.toolList, src.docsRead(), src.docsCreate(), src.docsAppend())
		}
		if len(src.toolList) == 0 {
			return nil, fmt.Errorf("google %s: no known scopes (want gmail, calendar, drive, and/or docs)", c.Name)
		}
		return src, nil
	}
}

// googleSource is one connected Google account. Unlike MCP, building
// it needs no network: tools are compiled from scopes, and auth
// problems surface on first use or Test.
type googleSource struct {
	g        *Google
	cfg      GoogleConfig
	ref      string
	toolList []*tools.Tool
}

func (s *googleSource) Tools() []*tools.Tool { return s.toolList }

// Test proves the stored tokens still refresh — the cheapest honest
// signal that the connection works.
func (s *googleSource) Test(ctx context.Context) error {
	_, err := s.g.token(ctx, s.cfg, s.ref)
	return err
}

func (s *googleSource) Close() error { return nil }

func (s *googleSource) hasScope(service string) bool {
	for _, sc := range s.cfg.Scopes {
		if strings.Contains(sc, service) {
			return true
		}
	}
	return false
}

// hasExactScope reports whether one of the connector's granted scopes
// is exactly want. Unlike hasScope's substring match (fine for
// "gmail"/"calendar", which never collide), drive.readonly and
// drive.file share the "drive" substring — an exact match keeps a
// docs-only connector (drive.file) from also enabling the broader
// Drive search/read tools it never consented to.
func (s *googleSource) hasExactScope(want string) bool {
	for _, sc := range s.cfg.Scopes {
		if sc == want {
			return true
		}
	}
	return false
}

// api performs one authenticated Google API call and decodes the JSON
// response into out (nil out discards the body).
func (s *googleSource) api(ctx context.Context, method, apiURL string, body, out any) error {
	resp, err := s.rawAPI(ctx, method, apiURL, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// rawAPI performs one authenticated Google API call and returns the
// raw response for callers that need non-JSON bytes (Drive export/
// download). Callers must close the response body.
func (s *googleSource) rawAPI(ctx context.Context, method, apiURL string, body any) (*http.Response, error) {
	token, err := s.g.token(ctx, s.cfg, s.ref)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, apiURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.g.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, googleAPIError(resp)
	}
	return resp, nil
}

// googleAPIError maps a non-2xx Drive/Docs/Gmail/Calendar API response
// to a human message: 401 (expired/revoked access token slipping past
// the refresh — e.g. a scope the stored grant no longer covers) gets a
// reconnect-oriented message, other statuses keep the status code plus
// a body snippet, same discipline as googleTokenError.
func googleAPIError(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("Google authorization expired or was revoked — reconnect to re-authorize")
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("google api status %d: %s", resp.StatusCode, snippet)
}

// --- Gmail ---

// gmailMessageMeta is the metadata slice both search and read render.
type gmailMessage struct {
	ID      string `json:"id"`
	Payload struct {
		MimeType string `json:"mimeType"`
		Headers  []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
		Body  gmailBody   `json:"body"`
		Parts []gmailPart `json:"parts"`
	} `json:"payload"`
	Snippet string `json:"snippet"`
}

type gmailPart struct {
	MimeType string      `json:"mimeType"`
	Filename string      `json:"filename"`
	Body     gmailBody   `json:"body"`
	Parts    []gmailPart `json:"parts"`
}

type gmailBody struct {
	Data         string `json:"data"`
	AttachmentID string `json:"attachmentId"`
	Size         int    `json:"size"`
}

func (m *gmailMessage) header(name string) string {
	for _, h := range m.Payload.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// plainText walks the MIME tree for the first text/plain part.
func plainText(parts []gmailPart) string {
	for _, p := range parts {
		if p.MimeType == "text/plain" && p.Body.Data != "" {
			raw, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(strings.TrimRight(p.Body.Data, "="))
			if err == nil {
				return string(raw)
			}
		}
		if inner := plainText(p.Parts); inner != "" {
			return inner
		}
	}
	return ""
}

// htmlPart returns the first text/html part's decoded bytes — the
// fallback source for HTML-only mail (common for booking confirmations
// and receipts), which has no text/plain alternative for plainText to
// find.
func htmlPart(parts []gmailPart) []byte {
	for _, p := range parts {
		if p.MimeType == "text/html" && p.Body.Data != "" {
			raw, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(strings.TrimRight(p.Body.Data, "="))
			if err == nil {
				return raw
			}
		}
		if inner := htmlPart(p.Parts); inner != nil {
			return inner
		}
	}
	return nil
}

func (s *googleSource) gmailSearch() *tools.Tool {
	return &tools.Tool{
		Name:     "gmail_search",
		ReadOnly: true,
		Description: `Search the connected Gmail account. query uses Gmail search syntax
(from:, subject:, is:unread, newer_than:7d, after:YYYY/MM/DD, ...).
Returns up to max_results (default 10) messages as id, date, from,
subject, snippet. Use gmail_read with an id for the full body.

A zero-result search does NOT mean the email doesn't exist — Gmail's
from: matching is stricter than it looks, and a query combining from:
with keyword/subject terms narrows twice, compounding a near-miss into
zero. If a targeted search returns nothing, retry BROADER before
concluding the email isn't there:
1. Drop keyword/subject filters, keep only from: and a date range.
2. If from: with a bare domain (e.g. from:example.com) misses, try
   the FULL sender address you're looking for, or a shorter substring
   of the domain, or just the company name as a plain keyword with no
   from: operator at all.
3. Widen the date range (after:/before:/newer_than:) — a mistaken
   assumption about when an email arrived is a common miss.
4. Try in:anywhere if you suspect it's archived or in another label.`,
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"query":{"type":"string","description":"Gmail search query"},
			"max_results":{"type":"integer","minimum":1,"maximum":25}
		},"required":["query"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Query      string `json:"query"`
				MaxResults int    `json:"max_results"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			if in.MaxResults <= 0 {
				in.MaxResults = 10
			}
			var list struct {
				Messages []struct {
					ID string `json:"id"`
				} `json:"messages"`
			}
			q := url.Values{"q": {in.Query}, "maxResults": {fmt.Sprint(in.MaxResults)}}
			if err := s.api(ctx, http.MethodGet,
				s.g.GmailBase+"/gmail/v1/users/me/messages?"+q.Encode(), nil, &list); err != nil {
				return "", err
			}
			if len(list.Messages) == 0 {
				return "no messages matched — before concluding the email doesn't exist, retry broader: " +
					"drop from:/subject: keyword combinations (they narrow twice), try the full sender " +
					"address or just the company name as a plain keyword, or widen the date range", nil
			}
			var b strings.Builder
			for _, m := range list.Messages {
				var msg gmailMessage
				if err := s.api(ctx, http.MethodGet,
					s.g.GmailBase+"/gmail/v1/users/me/messages/"+m.ID+"?format=metadata&metadataHeaders=From&metadataHeaders=Subject&metadataHeaders=Date",
					nil, &msg); err != nil {
					return "", err
				}
				fmt.Fprintf(&b, "id: %s\ndate: %s\nfrom: %s\nsubject: %s\nsnippet: %s\n\n",
					msg.ID, msg.header("Date"), msg.header("From"), msg.header("Subject"), msg.Snippet)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

// gmailAttachment names one attachment found while walking a message's
// MIME tree, ready to hand to gmail_read_attachment.
type gmailAttachment struct {
	Filename     string
	AttachmentID string
}

// attachments walks the MIME tree collecting every part that names a
// file AND carries an attachment id — inline images with no filename,
// and body parts with data inlined directly rather than referenced by
// id, are not attachments in the sense gmail_read_attachment needs.
func attachments(parts []gmailPart) []gmailAttachment {
	var out []gmailAttachment
	for _, p := range parts {
		if p.Filename != "" && p.Body.AttachmentID != "" {
			out = append(out, gmailAttachment{Filename: p.Filename, AttachmentID: p.Body.AttachmentID})
		}
		out = append(out, attachments(p.Parts)...)
	}
	return out
}

// findAttachment returns the attachment id for a given filename.
// gmail_read_attachment takes a filename, never the raw Gmail
// attachment id: that id is a 300-400+ char opaque token, and models
// reliably truncate it when copying it into a tool call — every
// real-world attempt failed with "Invalid attachment token" before
// this fix. Re-fetching the message and matching by filename (short,
// human-readable, copies correctly) trades one extra API call for a
// tool that actually works.
func findAttachment(parts []gmailPart, filename string) (string, bool) {
	for _, a := range attachments(parts) {
		if a.Filename == filename {
			return a.AttachmentID, true
		}
	}
	return "", false
}

func (s *googleSource) gmailRead() *tools.Tool {
	return &tools.Tool{
		Name:        "gmail_read",
		ReadOnly:    true,
		Description: "Read one email's full content by message id (from gmail_search). Returns headers and the body — plain text when available, otherwise the HTML body rendered to readable text (common for booking confirmations and receipts) — plus a list of attachment filenames, if any. Use gmail_read_attachment with the message id and a filename from that list to read an attachment's content.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"id":{"type":"string","description":"Gmail message id"}
		},"required":["id"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			var msg gmailMessage
			if err := s.api(ctx, http.MethodGet,
				s.g.GmailBase+"/gmail/v1/users/me/messages/"+url.PathEscape(in.ID)+"?format=full", nil, &msg); err != nil {
				return "", err
			}
			allParts := append(msg.Payload.Parts, gmailPart{MimeType: msg.Payload.MimeType, Body: msg.Payload.Body})
			body := plainText(allParts)
			if body == "" {
				if raw := htmlPart(allParts); raw != nil {
					if text, err := markitdown.Convert(ctx, s.g.Client, s.g.MarkItDownURL, "body.html", "text/html", raw); err == nil {
						body = text
					}
				}
			}
			if body == "" {
				body = "(no text/plain or text/html body; snippet: " + msg.Snippet + ")"
			}
			out := fmt.Sprintf("from: %s\nto: %s\ndate: %s\nsubject: %s\n\n%s",
				msg.header("From"), msg.header("To"), msg.header("Date"), msg.header("Subject"), body)
			if atts := attachments(msg.Payload.Parts); len(atts) > 0 {
				var b strings.Builder
				b.WriteString("\n\nattachments:\n")
				for _, a := range atts {
					fmt.Fprintf(&b, "- %s\n", a.Filename)
				}
				out += strings.TrimRight(b.String(), "\n")
			}
			return out, nil
		},
	}
}

func (s *googleSource) gmailReadAttachment() *tools.Tool {
	return &tools.Tool{
		Name:        "gmail_read_attachment",
		ReadOnly:    true,
		Description: "Reads an attachment's content as markdown/text, given a message id and the attachment's filename (both from gmail_read's attachments list). Handles PDFs, Office documents, and other common formats.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"message_id":{"type":"string","description":"Gmail message id"},
			"filename":{"type":"string","description":"Attachment filename from gmail_read's attachments list"}
		},"required":["message_id","filename"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				MessageID string `json:"message_id"`
				Filename  string `json:"filename"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			// The real Gmail attachment id is a 300-400+ char opaque
			// token — never asked of the caller, since models reliably
			// truncate it when copying into a tool call. Re-fetch the
			// message and match by filename instead.
			var msg gmailMessage
			if err := s.api(ctx, http.MethodGet,
				s.g.GmailBase+"/gmail/v1/users/me/messages/"+url.PathEscape(in.MessageID)+"?format=full", nil, &msg); err != nil {
				return "", err
			}
			attachmentID, ok := findAttachment(msg.Payload.Parts, in.Filename)
			if !ok {
				return "", fmt.Errorf("no attachment named %q on this message; check gmail_read's attachments list", in.Filename)
			}
			var att gmailBody
			if err := s.api(ctx, http.MethodGet,
				s.g.GmailBase+"/gmail/v1/users/me/messages/"+url.PathEscape(in.MessageID)+
					"/attachments/"+url.PathEscape(attachmentID), nil, &att); err != nil {
				return "", err
			}
			raw, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(strings.TrimRight(att.Data, "="))
			if err != nil {
				return "", fmt.Errorf("decode attachment: %w", err)
			}
			return markitdown.Convert(ctx, s.g.Client, s.g.MarkItDownURL, in.Filename, "", raw)
		},
	}
}

func (s *googleSource) gmailSend() *tools.Tool {
	return &tools.Tool{
		Name:        "gmail_send",
		Description: "Send an email from the connected Gmail account. Plain text only. Use only when the user asked for an email to be sent; the recipient sees it immediately.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"to":{"type":"string","description":"recipient address(es), comma-separated"},
			"subject":{"type":"string"},
			"body":{"type":"string","description":"plain-text message body"},
			"cc":{"type":"string","description":"optional cc address(es), comma-separated"}
		},"required":["to","subject","body"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				To, Subject, Body, CC string
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			var raw strings.Builder
			fmt.Fprintf(&raw, "To: %s\r\n", in.To)
			if in.CC != "" {
				fmt.Fprintf(&raw, "Cc: %s\r\n", in.CC)
			}
			fmt.Fprintf(&raw, "Subject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", in.Subject, in.Body)

			var res struct {
				ID string `json:"id"`
			}
			payload := map[string]string{"raw": base64.URLEncoding.EncodeToString([]byte(raw.String()))}
			if err := s.api(ctx, http.MethodPost,
				s.g.GmailBase+"/gmail/v1/users/me/messages/send", payload, &res); err != nil {
				return "", err
			}
			return "sent, message id " + res.ID, nil
		},
	}
}

// --- Calendar ---

func (s *googleSource) calendarListEvents() *tools.Tool {
	return &tools.Tool{
		Name:        "calendar_list_events",
		ReadOnly:    true,
		Description: "List events from the connected Google Calendar (primary calendar). Omit time_min/time_max for the default window — the next 7 days from now; set them (RFC3339 UTC) only when the goal needs a different window, computed from today's actual date. Returns start, end, summary, and location per event.",
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
				in.MaxResults = 20
			}
			q := url.Values{
				"singleEvents": {"true"},
				"orderBy":      {"startTime"},
				"maxResults":   {fmt.Sprint(in.MaxResults)},
			}
			if in.TimeMin != "" {
				q.Set("timeMin", in.TimeMin)
			}
			if in.TimeMax != "" {
				q.Set("timeMax", in.TimeMax)
			}
			var res struct {
				Items []struct {
					Summary  string `json:"summary"`
					Location string `json:"location"`
					Start    struct {
						DateTime string `json:"dateTime"`
						Date     string `json:"date"`
					} `json:"start"`
					End struct {
						DateTime string `json:"dateTime"`
						Date     string `json:"date"`
					} `json:"end"`
				} `json:"items"`
			}
			if err := s.api(ctx, http.MethodGet,
				s.g.CalendarBase+"/calendars/primary/events?"+q.Encode(), nil, &res); err != nil {
				return "", err
			}
			if len(res.Items) == 0 {
				return "no events in the window", nil
			}
			var b strings.Builder
			for _, e := range res.Items {
				start, end := e.Start.DateTime, e.End.DateTime
				if start == "" {
					start, end = e.Start.Date, e.End.Date // all-day
				}
				fmt.Fprintf(&b, "%s → %s  %s", start, end, e.Summary)
				if e.Location != "" {
					fmt.Fprintf(&b, " (%s)", e.Location)
				}
				b.WriteString("\n")
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func (s *googleSource) calendarCreateEvent() *tools.Tool {
	return &tools.Tool{
		Name:        "calendar_create_event",
		Description: "Create an event on the connected Google Calendar (primary calendar). start and end are RFC3339 timestamps with offset, e.g. 2026-07-22T15:00:00+02:00. Use only when the user asked for an event; attendees receive invitations immediately.",
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
			event := map[string]any{
				"summary": in.Summary,
				"start":   map[string]string{"dateTime": in.Start},
				"end":     map[string]string{"dateTime": in.End},
			}
			if in.Description != "" {
				event["description"] = in.Description
			}
			if in.Location != "" {
				event["location"] = in.Location
			}
			if len(in.Attendees) > 0 {
				list := make([]map[string]string, 0, len(in.Attendees))
				for _, a := range in.Attendees {
					list = append(list, map[string]string{"email": a})
				}
				event["attendees"] = list
			}
			var res struct {
				ID       string `json:"id"`
				HTMLLink string `json:"htmlLink"`
			}
			if err := s.api(ctx, http.MethodPost,
				s.g.CalendarBase+"/calendars/primary/events", event, &res); err != nil {
				return "", err
			}
			return "event created: " + res.HTMLLink, nil
		},
	}
}

// --- Drive ---

// driveReadMaxResult bounds drive_read's returned text — Drive
// documents and downloaded/converted files have no inherent size
// cap, and an unbounded file could blow the loop's context budget.
// Same convention as webFetchMaxResult.
const driveReadMaxResult = 64 << 10

// driveExportMimeTypes maps a Google-native file's mimeType to the
// export format requested from Drive's files.export endpoint — a
// text-friendly rendering for each editor type Drive can hold.
var driveExportMimeTypes = map[string]string{
	"application/vnd.google-apps.document":     "text/markdown",
	"application/vnd.google-apps.spreadsheet":  "text/csv",
	"application/vnd.google-apps.presentation": "text/plain",
}

func (s *googleSource) driveSearch() *tools.Tool {
	return &tools.Tool{
		Name: "drive_search",
		Description: `Search Google Drive by file name or content. query is matched
against file names and, for supported formats, document content
(Drive's "fullText contains" search). Returns up to max_results
(default 20) files as id, name, mimeType, modifiedTime, webViewLink.
Use drive_read with an id to read a file's content.`,
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"query":{"type":"string","description":"words to match against file name and content"},
			"max_results":{"type":"integer","minimum":1,"maximum":50}
		},"required":["query"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Query      string `json:"query"`
				MaxResults int    `json:"max_results"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			if in.MaxResults <= 0 {
				in.MaxResults = 20
			}
			escaped := strings.ReplaceAll(in.Query, `\`, `\\`)
			escaped = strings.ReplaceAll(escaped, `'`, `\'`)
			q := url.Values{
				"q":        {fmt.Sprintf("fullText contains '%s' or name contains '%s'", escaped, escaped)},
				"pageSize": {fmt.Sprint(in.MaxResults)},
				"fields":   {"files(id,name,mimeType,modifiedTime,webViewLink)"},
			}
			var res struct {
				Files []struct {
					ID           string `json:"id"`
					Name         string `json:"name"`
					MimeType     string `json:"mimeType"`
					ModifiedTime string `json:"modifiedTime"`
					WebViewLink  string `json:"webViewLink"`
				} `json:"files"`
			}
			if err := s.api(ctx, http.MethodGet,
				s.g.DriveBase+"/files?"+q.Encode(), nil, &res); err != nil {
				return "", err
			}
			if len(res.Files) == 0 {
				return "no files matched", nil
			}
			var b strings.Builder
			for _, f := range res.Files {
				fmt.Fprintf(&b, "id: %s\nname: %s\nmimeType: %s\nmodifiedTime: %s\nwebViewLink: %s\n\n",
					f.ID, f.Name, f.MimeType, f.ModifiedTime, f.WebViewLink)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func (s *googleSource) driveRead() *tools.Tool {
	return &tools.Tool{
		Name:        "drive_read",
		Description: "Read a Drive file's content by id (from drive_search). Google Docs/Sheets/Slides export to text/markdown/CSV; other formats (PDF, Office documents, ...) download and convert to markdown. Long content is truncated.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"id":{"type":"string","description":"Drive file id"}
		},"required":["id"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			var meta struct {
				Name     string `json:"name"`
				MimeType string `json:"mimeType"`
			}
			if err := s.api(ctx, http.MethodGet,
				s.g.DriveBase+"/files/"+url.PathEscape(in.ID)+"?fields=name,mimeType", nil, &meta); err != nil {
				return "", err
			}

			var text string
			if exportType, ok := driveExportMimeTypes[meta.MimeType]; ok {
				resp, err := s.rawAPI(ctx, http.MethodGet,
					s.g.DriveBase+"/files/"+url.PathEscape(in.ID)+"/export?mimeType="+url.QueryEscape(exportType), nil)
				if err != nil {
					return "", err
				}
				defer func() { _ = resp.Body.Close() }()
				raw, err := io.ReadAll(io.LimitReader(resp.Body, driveReadMaxResult+1))
				if err != nil {
					return "", fmt.Errorf("read export: %w", err)
				}
				text = string(raw)
			} else {
				resp, err := s.rawAPI(ctx, http.MethodGet,
					s.g.DriveBase+"/files/"+url.PathEscape(in.ID)+"?alt=media", nil)
				if err != nil {
					return "", err
				}
				defer func() { _ = resp.Body.Close() }()
				raw, err := io.ReadAll(resp.Body)
				if err != nil {
					return "", fmt.Errorf("download file: %w", err)
				}
				text, err = markitdown.Convert(ctx, s.g.Client, s.g.MarkItDownURL, meta.Name, meta.MimeType, raw)
				if err != nil {
					return "", err
				}
			}
			if len(text) > driveReadMaxResult {
				text = text[:driveReadMaxResult] + "\n\n[truncated: file continues]"
			}
			return fmt.Sprintf("name: %s\nmimeType: %s\n\n%s", meta.Name, meta.MimeType, text), nil
		},
	}
}

// --- Docs ---

// docsReadMaxResult bounds docs_read's returned text, same convention
// as driveReadMaxResult.
const docsReadMaxResult = 64 << 10

// docsParagraphText flattens one Docs StructuralElement's paragraph
// (if present) to its plain text run.
func docsParagraphText(el docsStructuralElement) string {
	if el.Paragraph == nil {
		return ""
	}
	var b strings.Builder
	for _, pe := range el.Paragraph.Elements {
		if pe.TextRun != nil {
			b.WriteString(pe.TextRun.Content)
		}
	}
	return b.String()
}

type docsStructuralElement struct {
	Paragraph *struct {
		Elements []struct {
			TextRun *struct {
				Content string `json:"content"`
			} `json:"textRun"`
		} `json:"elements"`
	} `json:"paragraph"`
}

func (s *googleSource) docsRead() *tools.Tool {
	return &tools.Tool{
		Name:        "docs_read",
		Description: "Read a Google Doc's content by id (from drive_search, or a doc's id from its URL/docs_create's result). Returns the document title and body as plain text. Long documents are truncated.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"id":{"type":"string","description":"Google Doc id"}
		},"required":["id"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			var doc struct {
				Title string `json:"title"`
				Body  struct {
					Content []docsStructuralElement `json:"content"`
				} `json:"body"`
			}
			if err := s.api(ctx, http.MethodGet,
				s.g.DocsBase+"/documents/"+url.PathEscape(in.ID), nil, &doc); err != nil {
				return "", err
			}
			var b strings.Builder
			for _, el := range doc.Body.Content {
				b.WriteString(docsParagraphText(el))
			}
			text := strings.TrimRight(b.String(), "\n")
			if len(text) > docsReadMaxResult {
				text = text[:docsReadMaxResult] + "\n\n[truncated: document continues]"
			}
			return fmt.Sprintf("title: %s\n\n%s", doc.Title, text), nil
		},
	}
}

func (s *googleSource) docsCreate() *tools.Tool {
	return &tools.Tool{
		Name:        "docs_create",
		Description: "Create a new Google Doc with a title and initial body text. Returns the new doc's id and webViewLink. Use only when the user asked for a new document.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"title":{"type":"string"},
			"body":{"type":"string","description":"initial plain-text body, empty for a blank doc"}
		},"required":["title"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Title, Body string
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			var doc struct {
				DocumentID string `json:"documentId"`
			}
			if err := s.api(ctx, http.MethodPost,
				s.g.DocsBase+"/documents", map[string]string{"title": in.Title}, &doc); err != nil {
				return "", err
			}
			if in.Body != "" {
				update := map[string]any{
					"requests": []map[string]any{
						{"insertText": map[string]any{
							"location": map[string]any{"index": 1},
							"text":     in.Body,
						}},
					},
				}
				if err := s.api(ctx, http.MethodPost,
					s.g.DocsBase+"/documents/"+url.PathEscape(doc.DocumentID)+":batchUpdate", update, nil); err != nil {
					return "", err
				}
			}
			return fmt.Sprintf("created doc id %s, webViewLink https://docs.google.com/document/d/%s/edit",
				doc.DocumentID, doc.DocumentID), nil
		},
	}
}

func (s *googleSource) docsAppend() *tools.Tool {
	return &tools.Tool{
		Name:        "docs_append",
		Description: "Append text to the end of an existing Google Doc. Use only when the user asked to add to a document; the change is immediate and visible to anyone viewing it.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"id":{"type":"string","description":"Google Doc id"},
			"text":{"type":"string","description":"text to append at the end of the document"}
		},"required":["id","text"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				ID, Text string
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			var doc struct {
				Body struct {
					Content []struct {
						EndIndex int `json:"endIndex"`
					} `json:"content"`
				} `json:"body"`
			}
			if err := s.api(ctx, http.MethodGet,
				s.g.DocsBase+"/documents/"+url.PathEscape(in.ID)+"?fields=body.content.endIndex", nil, &doc); err != nil {
				return "", err
			}
			// The document's last structural element's endIndex points
			// just past the final newline; inserting there is Docs'
			// documented "end of body" position. A blank/new document
			// still has one element (endIndex 1) so this always resolves.
			endIndex := 1
			if n := len(doc.Body.Content); n > 0 {
				endIndex = doc.Body.Content[n-1].EndIndex - 1
			}
			if endIndex < 1 {
				endIndex = 1
			}
			update := map[string]any{
				"requests": []map[string]any{
					{"insertText": map[string]any{
						"location": map[string]any{"index": endIndex},
						"text":     in.Text,
					}},
				},
			}
			if err := s.api(ctx, http.MethodPost,
				s.g.DocsBase+"/documents/"+url.PathEscape(in.ID)+":batchUpdate", update, nil); err != nil {
				return "", err
			}
			return "appended to doc " + in.ID, nil
		},
	}
}
