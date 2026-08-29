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
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
)

// Builder returns the Builder for kind='microsoft'. The tool surface is
// scope-gated: only the services the user consented to appear.
func (m *Microsoft) Builder() Builder {
	return func(ctx context.Context, c Connector, _ Resolve) (Source, error) {
		cfg, err := microsoftConfig(c)
		if err != nil {
			return nil, err
		}
		if c.CredentialRef == "" {
			return nil, fmt.Errorf("microsoft %s: credential_ref is required (it names the stored OAuth tokens)", c.Name)
		}
		src := &microsoftSource{m: m, cfg: cfg, ref: c.CredentialRef}
		if src.hasScope(mailReadScope) {
			src.toolList = append(src.toolList, src.mailSearch(), src.mailRead(), src.mailReadAttachment())
		}
		if src.hasScope(mailSendScope) {
			src.toolList = append(src.toolList, src.mailSend())
		}
		if src.hasScope(calendarsReadScope) {
			src.toolList = append(src.toolList, src.calendarListEvents())
		}
		if len(src.toolList) == 0 {
			return nil, fmt.Errorf("microsoft %s: no known scopes (want Mail.Read, Mail.Send, and/or Calendars.Read)", c.Name)
		}
		return src, nil
	}
}

// microsoftSource is one connected Microsoft account. Like googleSource,
// building it needs no network: tools are compiled from scopes, and
// auth problems surface on first use or Test.
type microsoftSource struct {
	m        *Microsoft
	cfg      MicrosoftConfig
	ref      string
	toolList []*tools.Tool
}

func (s *microsoftSource) Tools() []*tools.Tool { return s.toolList }

// Test proves the stored tokens still refresh — the cheapest honest
// signal that the connection works.
func (s *microsoftSource) Test(ctx context.Context) error {
	_, err := s.m.token(ctx, s.cfg, s.ref)
	return err
}

func (s *microsoftSource) Close() error { return nil }

// AccountInfo reports this source's kind and connected account email,
// same role as googleSource.AccountInfo.
func (s *microsoftSource) AccountInfo() (kind, email string) {
	return "microsoft", s.cfg.AccountEmail
}

// hasScope reports whether one of the connector's granted scopes is
// exactly want. Graph scopes never share substrings the way Google's
// drive.readonly/drive.file do, so an exact match is enough (unlike
// googleSource's separate hasScope/hasExactScope).
func (s *microsoftSource) hasScope(want string) bool {
	return slicesContainsFold(s.cfg.Scopes, want)
}

func slicesContainsFold(scopes []string, want string) bool {
	for _, sc := range scopes {
		if strings.EqualFold(sc, want) {
			return true
		}
	}
	return false
}

// Identity resolves GET /me — the panel's evidence a working
// credential was configured, same role as githubSource.Identity.
// Reuses GitHubIdentity's shape (the identifier interface's only
// return type) rather than adding a parallel one: Login carries the
// userPrincipalName (the sign-in identifier), Email the mailbox
// address, Scopes the connector's granted Graph scopes.
func (s *microsoftSource) Identity(ctx context.Context) (GitHubIdentity, error) {
	var raw struct {
		DisplayName string `json:"displayName"`
		Mail        string `json:"mail"`
		UPN         string `json:"userPrincipalName"`
	}
	if err := s.api(ctx, http.MethodGet,
		s.m.GraphBase+"/me?$select=displayName,mail,userPrincipalName", nil, &raw); err != nil {
		return GitHubIdentity{}, err
	}
	email := raw.Mail
	if email == "" {
		email = raw.UPN
	}
	return GitHubIdentity{
		Login: raw.UPN, Name: raw.DisplayName, Email: email,
		Scopes: strings.Join(s.cfg.Scopes, ", "),
	}, nil
}

// api performs one authenticated Graph API call and decodes the JSON
// response into out (nil out discards the body).
func (s *microsoftSource) api(ctx context.Context, method, apiURL string, body, out any) error {
	resp, err := s.rawAPI(ctx, method, apiURL, body, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// rawAPI performs one authenticated Graph API call and returns the raw
// response. Callers must close the response body. extraHeaders lets a
// caller set a Graph-specific header (e.g. Prefer: outlook.body-
// content-type) without api's simpler signature growing one.
func (s *microsoftSource) rawAPI(ctx context.Context, method, apiURL string, body any, extraHeaders map[string]string) (*http.Response, error) {
	token, err := s.m.token(ctx, s.cfg, s.ref)
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
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := s.m.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, microsoftAPIError(resp)
	}
	return resp, nil
}

// microsoftAPIError maps a non-2xx Graph API response to a human
// message: 401 (expired/revoked access token slipping past the
// refresh) gets a reconnect-oriented message, other statuses keep the
// status code plus a body snippet, same discipline as googleAPIError.
func microsoftAPIError(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("Microsoft authorization expired or was revoked — reconnect to re-authorize")
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("microsoft graph api status %d: %s", resp.StatusCode, snippet)
}

// --- Mail ---

type outlookRecipient struct {
	EmailAddress struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
}

type outlookMessage struct {
	ID               string             `json:"id"`
	Subject          string             `json:"subject"`
	From             outlookRecipient   `json:"from"`
	ToRecipients     []outlookRecipient `json:"toRecipients"`
	ReceivedDateTime string             `json:"receivedDateTime"`
	BodyPreview      string             `json:"bodyPreview"`
	Body             struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	HasAttachments bool `json:"hasAttachments"`
}

func (r outlookRecipient) String() string {
	if r.EmailAddress.Name != "" {
		return fmt.Sprintf("%s <%s>", r.EmailAddress.Name, r.EmailAddress.Address)
	}
	return r.EmailAddress.Address
}

func (s *microsoftSource) mailSearch() *tools.Tool {
	return &tools.Tool{
		Name:     "search_mail",
		ReadOnly: true,
		Description: `Search a connected mail account. Returns up to max_results
(default 10) messages as id, date, from, subject, snippet. Use
read_mail with an id for the full body. See the tool description's
Connected accounts list for this account's query syntax.`,
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"query":{"type":"string","description":"search query"},
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
			q := url.Values{
				"$search": {`"` + strings.ReplaceAll(in.Query, `"`, `'`) + `"`},
				"$top":    {fmt.Sprint(in.MaxResults)},
				"$select": {"id,subject,from,receivedDateTime,bodyPreview"},
			}
			var list struct {
				Value []outlookMessage `json:"value"`
			}
			if err := s.api(ctx, http.MethodGet,
				s.m.GraphBase+"/me/messages?"+q.Encode(), nil, &list); err != nil {
				return "", err
			}
			if len(list.Value) == 0 {
				return "no messages matched", nil
			}
			var b strings.Builder
			for _, msg := range list.Value {
				fmt.Fprintf(&b, "id: %s\ndate: %s\nfrom: %s\nsubject: %s\nsnippet: %s\n\n",
					msg.ID, msg.ReceivedDateTime, msg.From.String(), msg.Subject, msg.BodyPreview)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func (s *microsoftSource) mailRead() *tools.Tool {
	return &tools.Tool{
		Name:        "read_mail",
		ReadOnly:    true,
		Description: "Read one email's full content by message id (from search_mail). Returns headers and the body as readable text, plus a list of attachment filenames, if any. Use read_mail_attachment with the message id and a filename from that list to read an attachment's content.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"id":{"type":"string","description":"message id"}
		},"required":["id"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			var msg outlookMessage
			q := url.Values{"$select": {"subject,from,toRecipients,receivedDateTime,body,hasAttachments"}}
			// Prefer text body content: Graph returns HTML by default.
			resp, err := s.rawAPI(ctx, http.MethodGet,
				s.m.GraphBase+"/me/messages/"+url.PathEscape(in.ID)+"?"+q.Encode(), nil,
				map[string]string{"Prefer": `outlook.body-content-type="text"`})
			if err != nil {
				return "", err
			}
			defer func() { _ = resp.Body.Close() }()
			if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
				return "", fmt.Errorf("decode message: %w", err)
			}
			to := make([]string, len(msg.ToRecipients))
			for i, r := range msg.ToRecipients {
				to[i] = r.String()
			}
			out := fmt.Sprintf("from: %s\nto: %s\ndate: %s\nsubject: %s\n\n%s",
				msg.From.String(), strings.Join(to, ", "), msg.ReceivedDateTime, msg.Subject, msg.Body.Content)
			if msg.HasAttachments {
				atts, err := s.listAttachments(ctx, in.ID)
				if err == nil && len(atts) > 0 {
					var b strings.Builder
					b.WriteString("\n\nattachments:\n")
					for _, a := range atts {
						fmt.Fprintf(&b, "- %s\n", a.Name)
					}
					out += strings.TrimRight(b.String(), "\n")
				}
			}
			return out, nil
		},
	}
}

type outlookAttachment struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ContentType  string `json:"contentType"`
	ContentBytes string `json:"contentBytes"`
}

// listAttachments fetches one message's attachment metadata (id, name,
// contentType), without the (potentially large) contentBytes field —
// read_mail_attachment fetches one attachment's bytes separately, by
// name, mirroring read_mail_attachment's filename-lookup shape.
func (s *microsoftSource) listAttachments(ctx context.Context, messageID string) ([]outlookAttachment, error) {
	var list struct {
		Value []outlookAttachment `json:"value"`
	}
	q := url.Values{"$select": {"id,name,contentType"}}
	if err := s.api(ctx, http.MethodGet,
		s.m.GraphBase+"/me/messages/"+url.PathEscape(messageID)+"/attachments?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	return list.Value, nil
}

func (s *microsoftSource) mailReadAttachment() *tools.Tool {
	return &tools.Tool{
		Name:        "read_mail_attachment",
		ReadOnly:    true,
		Description: "Reads an attachment's content as markdown/text, given a message id and the attachment's filename (both from read_mail's attachments list). Handles PDFs, Office documents, and other common formats.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"message_id":{"type":"string","description":"message id"},
			"filename":{"type":"string","description":"Attachment filename from read_mail's attachments list"}
		},"required":["message_id","filename"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				MessageID string `json:"message_id"`
				Filename  string `json:"filename"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			atts, err := s.listAttachments(ctx, in.MessageID)
			if err != nil {
				return "", err
			}
			var attachmentID, contentType string
			for _, a := range atts {
				if a.Name == in.Filename {
					attachmentID, contentType = a.ID, a.ContentType
					break
				}
			}
			if attachmentID == "" {
				return "", fmt.Errorf("no attachment named %q on this message; check read_mail's attachments list", in.Filename)
			}
			var full outlookAttachment
			if err := s.api(ctx, http.MethodGet,
				s.m.GraphBase+"/me/messages/"+url.PathEscape(in.MessageID)+"/attachments/"+url.PathEscape(attachmentID),
				nil, &full); err != nil {
				return "", err
			}
			raw, err := base64.StdEncoding.DecodeString(full.ContentBytes)
			if err != nil {
				return "", fmt.Errorf("decode attachment: %w", err)
			}
			return markitdown.Convert(ctx, s.m.Client, s.m.MarkItDownURL, in.Filename, contentType, raw)
		},
	}
}

func (s *microsoftSource) mailSend() *tools.Tool {
	return &tools.Tool{
		Name:        "send_mail",
		Description: "Send an email from a connected mail account. Plain text only. Use only when the user asked for an email to be sent; the recipient sees it immediately.",
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
			message := map[string]any{
				"subject": in.Subject,
				"body": map[string]string{
					"contentType": "Text",
					"content":     in.Body,
				},
				"toRecipients": outlookRecipientList(in.To),
			}
			if cc := outlookRecipientList(in.CC); cc != nil {
				message["ccRecipients"] = cc
			}
			payload := map[string]any{
				"message":         message,
				"saveToSentItems": true,
			}
			if err := s.api(ctx, http.MethodPost, s.m.GraphBase+"/me/sendMail", payload, nil); err != nil {
				return "", err
			}
			return "sent", nil
		},
	}
}

// outlookRecipientList splits a comma-separated address list into
// Graph's recipient shape; empty input yields nil so an omitted cc
// list is left off the payload's ccRecipients rather than sent empty.
func outlookRecipientList(addrs string) []map[string]any {
	var out []map[string]any
	for _, addr := range strings.Split(addrs, ",") {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		out = append(out, map[string]any{
			"emailAddress": map[string]string{"address": addr},
		})
	}
	return out
}

// --- Calendar ---

type outlookEvent struct {
	Subject string `json:"subject"`
	Start   struct {
		DateTime string `json:"dateTime"`
	} `json:"start"`
	End struct {
		DateTime string `json:"dateTime"`
	} `json:"end"`
	Location struct {
		DisplayName string `json:"displayName"`
	} `json:"location"`
	Organizer struct {
		EmailAddress struct {
			Name string `json:"name"`
		} `json:"emailAddress"`
	} `json:"organizer"`
}

func (s *microsoftSource) calendarListEvents() *tools.Tool {
	return &tools.Tool{
		Name:        "list_calendar_events",
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
			if in.TimeMin == "" {
				in.TimeMin = time.Now().Format("2006-01-02T15:04:05Z07:00")
			}
			if in.TimeMax == "" {
				in.TimeMax = time.Now().AddDate(0, 0, 7).Format("2006-01-02T15:04:05Z07:00")
			}
			if in.MaxResults <= 0 {
				in.MaxResults = 20
			}
			q := url.Values{
				"startDateTime": {in.TimeMin},
				"endDateTime":   {in.TimeMax},
				"$orderby":      {"start/dateTime"},
				"$select":       {"subject,start,end,location,organizer"},
				"$top":          {fmt.Sprint(in.MaxResults)},
			}
			var res struct {
				Value []outlookEvent `json:"value"`
			}
			if err := s.api(ctx, http.MethodGet,
				s.m.GraphBase+"/me/calendarView?"+q.Encode(), nil, &res); err != nil {
				return "", err
			}
			if len(res.Value) == 0 {
				return "no events in the window", nil
			}
			var b strings.Builder
			for _, e := range res.Value {
				fmt.Fprintf(&b, "%s → %s  %s", e.Start.DateTime, e.End.DateTime, e.Subject)
				if e.Location.DisplayName != "" {
					fmt.Fprintf(&b, " (%s)", e.Location.DisplayName)
				}
				if e.Organizer.EmailAddress.Name != "" {
					fmt.Fprintf(&b, " [organizer: %s]", e.Organizer.EmailAddress.Name)
				}
				b.WriteString("\n")
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}
