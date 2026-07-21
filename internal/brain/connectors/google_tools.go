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
			src.toolList = append(src.toolList, src.gmailSearch(), src.gmailRead(), src.gmailSend())
		}
		if src.hasScope("calendar") {
			src.toolList = append(src.toolList, src.calendarListEvents(), src.calendarCreateEvent())
		}
		if len(src.toolList) == 0 {
			return nil, fmt.Errorf("google %s: no known scopes (want gmail and/or calendar)", c.Name)
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

// api performs one authenticated Google API call and decodes the JSON
// response into out (nil out discards the body).
func (s *googleSource) api(ctx context.Context, method, apiURL string, body, out any) error {
	token, err := s.g.token(ctx, s.cfg, s.ref)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, apiURL, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.g.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("google api status %d: %s", resp.StatusCode, snippet)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- Gmail ---

// gmailMessageMeta is the metadata slice both search and read render.
type gmailMessage struct {
	ID      string `json:"id"`
	Payload struct {
		Headers []struct {
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
	Body     gmailBody   `json:"body"`
	Parts    []gmailPart `json:"parts"`
}

type gmailBody struct {
	Data string `json:"data"`
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

func (s *googleSource) gmailSearch() *tools.Tool {
	return &tools.Tool{
		Name:        "gmail_search",
		Description: "Search the connected Gmail account. query uses Gmail search syntax (from:, subject:, is:unread, newer_than:7d, ...). Returns up to max_results (default 10) messages as id, date, from, subject, snippet. Use gmail_read with an id for the full body.",
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
				return "no messages matched", nil
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

func (s *googleSource) gmailRead() *tools.Tool {
	return &tools.Tool{
		Name:        "gmail_read",
		Description: "Read one email's full content by message id (from gmail_search). Returns headers and the plain-text body.",
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
			body := plainText(append(msg.Payload.Parts, gmailPart{MimeType: "text/plain", Body: msg.Payload.Body}))
			if body == "" {
				body = "(no text/plain body; snippet: " + msg.Snippet + ")"
			}
			return fmt.Sprintf("from: %s\nto: %s\ndate: %s\nsubject: %s\n\n%s",
				msg.header("From"), msg.header("To"), msg.header("Date"), msg.header("Subject"), body), nil
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
		Description: "List events from the connected Google Calendar (primary calendar). time_min/time_max are RFC3339 timestamps; both default to the next 7 days. Returns start, end, summary, and location per event.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"time_min":{"type":"string","description":"RFC3339, e.g. 2026-07-21T00:00:00Z"},
			"time_max":{"type":"string","description":"RFC3339"},
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
