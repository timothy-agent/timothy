package connectors

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	emmail "github.com/emersion/go-message/mail"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
)

// imapSearchDefault/imapSearchMax mirror microsoft/google's mail_search
// max_results default and cap.
const (
	imapSearchDefault = 10
	imapSearchMax     = 25
)

// imapMessageSummary is one mail_search result: envelope plus a best-
// effort snippet.
type imapMessageSummary struct {
	UID     imap.UID
	Date    string
	From    string
	Subject string
	Snippet string
}

// imapAttachment names one attachment found in a parsed message, ready
// to hand to mail_read_attachment.
type imapAttachment struct {
	Filename    string
	ContentType string
}

// imapMessage is one mail_read result: headers, body text, and
// attachment metadata.
type imapMessage struct {
	From, To, Date, Subject string
	Body                    string
	Attachments             []imapAttachment
}

// imapSession is the minimal IMAP session surface the tools need,
// shaped to imapConn's implementation and existing only so
// tests can substitute a fake, no real network. One session serves
// one tool call and is always closed afterward (see imapSource.dial).
type imapSession interface {
	// Search returns the newest max messages (by descending UID)
	// matching every word in words, ANDed.
	Search(ctx context.Context, words []string, max int) ([]imapMessageSummary, error)
	// FetchMessage returns one message's full parsed content by UID.
	FetchMessage(ctx context.Context, uid imap.UID) (imapMessage, error)
	// FetchAttachment returns one attachment's raw bytes and content
	// type by UID and filename.
	FetchAttachment(ctx context.Context, uid imap.UID, filename string) ([]byte, string, error)
	Close() error
}

// imapConn implements imapSession over a live imapclient.Client
// with INBOX already SELECTed read-only.
type imapConn struct {
	client *imapclient.Client
}

func (s *imapConn) Search(ctx context.Context, words []string, max int) ([]imapMessageSummary, error) {
	criteria := &imap.SearchCriteria{Text: words}
	data, err := s.client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap search: %w", err)
	}
	uids := data.AllUIDs()
	sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] })
	if len(uids) > max {
		uids = uids[:max]
	}
	if len(uids) == 0 {
		return nil, nil
	}

	fetchOptions := &imap.FetchOptions{Envelope: true, InternalDate: true, BodyStructure: &imap.FetchItemBodyStructure{}}
	msgs, err := s.client.Fetch(imap.UIDSetNum(uids...), fetchOptions).Collect()
	if err != nil {
		return nil, fmt.Errorf("imap fetch envelopes: %w", err)
	}
	byUID := make(map[imap.UID]*imapclient.FetchMessageBuffer, len(msgs))
	for _, m := range msgs {
		byUID[m.UID] = m
	}

	out := make([]imapMessageSummary, 0, len(uids))
	for _, uid := range uids {
		m, ok := byUID[uid]
		if !ok {
			continue
		}
		sum := buildSummary(uid, m.InternalDate, m.Envelope)
		sum.Snippet = s.snippet(ctx, uid, m.BodyStructure)
		out = append(out, sum)
	}
	return out, nil
}

// buildSummary builds one mail_search result's envelope fields from a
// fetched message's UID/INTERNALDATE/ENVELOPE, pure so it's testable
// without a live IMAP session. Date defaults to internalDate and is
// only overwritten by the envelope's own Date when that's non-zero: a
// server that omits or can't parse a message's Date header returns a
// zero envelope Date, and clobbering internalDate with that would
// regress to the zero time rather than keep the still-useful fallback.
func buildSummary(uid imap.UID, internalDate time.Time, envelope *imap.Envelope) imapMessageSummary {
	sum := imapMessageSummary{UID: uid, Date: internalDate.Format(time.RFC3339)}
	if envelope == nil {
		return sum
	}
	if !envelope.Date.IsZero() {
		sum.Date = envelope.Date.Format(time.RFC3339)
	}
	sum.Subject = envelope.Subject
	sum.From = formatEnvelopeAddresses(envelope.From)
	return sum
}

// findSnippetTextPart walks structure (DFS pre-order, same order the
// server numbers parts) for the first text/plain part, falling back to
// the first text/* part; ok is false when structure is nil or has no
// text part at all (e.g. an attachment-only or non-MIME message).
func findSnippetTextPart(structure imap.BodyStructure) (part *imap.BodyStructureSinglePart, path []int, ok bool) {
	if structure == nil {
		return nil, nil, false
	}
	var fallback *imap.BodyStructureSinglePart
	var fallbackPath []int
	structure.Walk(func(p []int, bs imap.BodyStructure) bool {
		sp, isSingle := bs.(*imap.BodyStructureSinglePart)
		if !isSingle || sp.Type != "text" {
			return true
		}
		if sp.Subtype == "plain" && part == nil {
			part, path = sp, p
		}
		if fallback == nil {
			fallback, fallbackPath = sp, p
		}
		return true
	})
	if part != nil {
		return part, path, true
	}
	if fallback != nil {
		return fallback, fallbackPath, true
	}
	return nil, nil, false
}

// imapSnippetPartialBytes bounds the partial fetch used for a
// snippet's first text part: larger than imapSnippetBytes so a base64-
// encoded part still yields a useful amount of decoded text (base64
// inflates size by ~4/3).
const imapSnippetPartialBytes = 512

// snippet opportunistically fetches and decodes a small partial of the
// message's first text part (see findSnippetTextPart), tolerating any
// failure with an empty snippet rather than failing the whole search:
// no text part, an unfetchable partial, or an undecodable encoding all
// return "".
func (s *imapConn) snippet(_ context.Context, uid imap.UID, structure imap.BodyStructure) string {
	textPart, path, ok := findSnippetTextPart(structure)
	if !ok {
		return ""
	}
	fetchOptions := &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{{
			Part:    path,
			Peek:    true,
			Partial: &imap.SectionPartial{Offset: 0, Size: imapSnippetPartialBytes},
		}},
	}
	msgs, err := s.client.Fetch(imap.UIDSetNum(uid), fetchOptions).Collect()
	if err != nil || len(msgs) == 0 || len(msgs[0].BodySection) == 0 {
		return ""
	}
	decoded := decodeSnippetBytes(msgs[0].BodySection[0].Bytes, textPart.Encoding)
	text := strings.TrimSpace(decoded)
	return strings.Join(strings.Fields(text), " ")
}

// decodeSnippetBytes decodes a partial text-part fetch per its
// Content-Transfer-Encoding: base64 (trimmed to a full 4-byte quantum
// before decoding, since a partial fetch can cut mid-group) or
// quoted-printable; any other encoding (7bit/8bit/binary/unknown) needs
// no decoding. A decode failure returns the raw bytes as text rather
// than an empty string: partial, possibly-truncated input is expected
// to sometimes fail to decode cleanly.
func decodeSnippetBytes(raw []byte, encoding string) string {
	switch strings.ToLower(encoding) {
	case "base64":
		raw = raw[:len(raw)-len(raw)%4]
		decoded, err := base64.StdEncoding.DecodeString(string(raw))
		if err != nil {
			return string(raw)
		}
		return string(decoded)
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
		if err != nil && len(decoded) == 0 {
			return string(raw)
		}
		return string(decoded)
	default:
		return string(raw)
	}
}

func (s *imapConn) FetchMessage(_ context.Context, uid imap.UID) (imapMessage, error) {
	fetchOptions := &imap.FetchOptions{
		Envelope: true,
		BodySection: []*imap.FetchItemBodySection{
			{Peek: true},
		},
	}
	msgs, err := s.client.Fetch(imap.UIDSetNum(uid), fetchOptions).Collect()
	if err != nil {
		return imapMessage{}, fmt.Errorf("imap fetch message: %w", err)
	}
	if len(msgs) == 0 || len(msgs[0].BodySection) == 0 {
		return imapMessage{}, fmt.Errorf("message %d not found", uid)
	}
	return parseIMAPMessage(msgs[0])
}

func (s *imapConn) FetchAttachment(_ context.Context, uid imap.UID, filename string) ([]byte, string, error) {
	fetchOptions := &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
	}
	msgs, err := s.client.Fetch(imap.UIDSetNum(uid), fetchOptions).Collect()
	if err != nil {
		return nil, "", fmt.Errorf("imap fetch message: %w", err)
	}
	if len(msgs) == 0 || len(msgs[0].BodySection) == 0 {
		return nil, "", fmt.Errorf("message %d not found", uid)
	}
	return findIMAPAttachment(msgs[0].BodySection[0].Bytes, filename)
}

func (s *imapConn) Close() error {
	if err := s.client.Logout().Wait(); err != nil {
		_ = s.client.Close()
		return fmt.Errorf("imap logout: %w", err)
	}
	return s.client.Close()
}

// formatEnvelopeAddresses renders envelope From addresses the same
// "Name <addr>" shape microsoft/google's mail_search uses.
func formatEnvelopeAddresses(addrs []imap.Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		addr := a.Addr()
		if a.Name != "" {
			parts = append(parts, fmt.Sprintf("%s <%s>", a.Name, addr))
		} else {
			parts = append(parts, addr)
		}
	}
	return strings.Join(parts, ", ")
}

// parseIMAPMessage parses a fetched message's raw RFC822 bytes: header
// fields plus a body preferring text/plain, falling back to stripped
// text/html, and every attachment's filename/content-type.
func parseIMAPMessage(buf *imapclient.FetchMessageBuffer) (imapMessage, error) {
	return parseIMAPMessageBytes(buf.BodySection[0].Bytes)
}

// parseIMAPMessageBytes is parseIMAPMessage's core, taking raw RFC822
// bytes directly so it can be tested without an imapclient buffer.
func parseIMAPMessageBytes(raw []byte) (imapMessage, error) {
	r, err := emmail.CreateReader(bytes.NewReader(raw))
	if err != nil && r == nil {
		return imapMessage{}, fmt.Errorf("parse message: %w", err)
	}
	defer func() { _ = r.Close() }()

	out := imapMessage{}
	if from, _ := r.Header.AddressList("From"); len(from) > 0 {
		out.From = joinAddresses(from)
	}
	if to, _ := r.Header.AddressList("To"); len(to) > 0 {
		out.To = joinAddresses(to)
	}
	if d, err := r.Header.Date(); err == nil && !d.IsZero() {
		out.Date = d.Format(time.RFC3339)
	}
	out.Subject, _ = r.Header.Subject()

	var htmlBody string
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch h := part.Header.(type) {
		case *emmail.AttachmentHeader:
			filename, _ := h.Filename()
			ct, _, _ := h.ContentType()
			if filename != "" {
				out.Attachments = append(out.Attachments, imapAttachment{Filename: filename, ContentType: ct})
			}
		case *emmail.InlineHeader:
			ct, _, _ := h.ContentType()
			body, _ := io.ReadAll(part.Body)
			if strings.HasPrefix(ct, "text/plain") && out.Body == "" {
				out.Body = string(body)
			} else if strings.HasPrefix(ct, "text/html") && htmlBody == "" {
				htmlBody = string(body)
			}
		}
	}
	if out.Body == "" {
		out.Body = htmlBody
	}
	return out, nil
}

// findIMAPAttachment parses raw and returns the named attachment's raw
// bytes and content type.
func findIMAPAttachment(raw []byte, filename string) ([]byte, string, error) {
	r, err := emmail.CreateReader(bytes.NewReader(raw))
	if err != nil && r == nil {
		return nil, "", fmt.Errorf("parse message: %w", err)
	}
	defer func() { _ = r.Close() }()

	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		h, ok := part.Header.(*emmail.AttachmentHeader)
		if !ok {
			continue
		}
		name, _ := h.Filename()
		if name != filename {
			continue
		}
		ct, _, _ := h.ContentType()
		body, err := io.ReadAll(part.Body)
		if err != nil {
			return nil, "", fmt.Errorf("read attachment: %w", err)
		}
		return body, ct, nil
	}
	return nil, "", fmt.Errorf("no attachment named %q on this message; check mail_read's attachments list", filename)
}

// joinAddresses renders a net/mail address list the same "Name <addr>"
// shape microsoft/google's mail_read uses.
func joinAddresses(addrs []*mail.Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}

func (s *imapSource) mailSearch() *tools.Tool {
	return &tools.Tool{
		Name:     "mail_search",
		ReadOnly: true,
		Description: `Search a connected mail account. Returns up to max_results
(default 10) messages as id, date, from, subject, snippet. Use
mail_read with an id for the full body. See the tool description's
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
				in.MaxResults = imapSearchDefault
			}
			if in.MaxResults > imapSearchMax {
				in.MaxResults = imapSearchMax
			}
			words := strings.Fields(in.Query)
			if len(words) == 0 {
				return "", fmt.Errorf("query must not be empty")
			}

			sess, err := s.dial(ctx)
			if err != nil {
				return "", err
			}
			defer func() { _ = sess.Close() }()

			results, err := sess.Search(ctx, words, in.MaxResults)
			if err != nil {
				return "", err
			}
			if len(results) == 0 {
				return "no messages matched", nil
			}
			var b strings.Builder
			for _, m := range results {
				fmt.Fprintf(&b, "id: %d\ndate: %s\nfrom: %s\nsubject: %s\nsnippet: %s\n\n",
					m.UID, m.Date, m.From, m.Subject, m.Snippet)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func (s *imapSource) mailRead() *tools.Tool {
	return &tools.Tool{
		Name:        "mail_read",
		ReadOnly:    true,
		Description: "Read one email's full content by message id (from mail_search). Returns headers and the body as readable text, plus a list of attachment filenames, if any. Use mail_read_attachment with the message id and a filename from that list to read an attachment's content.",
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
			uid, err := parseIMAPUID(in.ID)
			if err != nil {
				return "", err
			}

			sess, err := s.dial(ctx)
			if err != nil {
				return "", err
			}
			defer func() { _ = sess.Close() }()

			msg, err := sess.FetchMessage(ctx, uid)
			if err != nil {
				return "", err
			}
			out := fmt.Sprintf("from: %s\nto: %s\ndate: %s\nsubject: %s\n\n%s",
				msg.From, msg.To, msg.Date, msg.Subject, msg.Body)
			if len(msg.Attachments) > 0 {
				var b strings.Builder
				b.WriteString("\n\nattachments:\n")
				for _, a := range msg.Attachments {
					fmt.Fprintf(&b, "- %s\n", a.Filename)
				}
				out += strings.TrimRight(b.String(), "\n")
			}
			return out, nil
		},
	}
}

func (s *imapSource) mailReadAttachment() *tools.Tool {
	return &tools.Tool{
		Name:        "mail_read_attachment",
		ReadOnly:    true,
		Description: "Reads an attachment's content as markdown/text, given a message id and the attachment's filename (both from mail_read's attachments list). Handles PDFs, Office documents, and other common formats.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"message_id":{"type":"string","description":"message id"},
			"filename":{"type":"string","description":"Attachment filename from mail_read's attachments list"}
		},"required":["message_id","filename"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				MessageID string `json:"message_id"`
				Filename  string `json:"filename"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			uid, err := parseIMAPUID(in.MessageID)
			if err != nil {
				return "", err
			}

			sess, err := s.dial(ctx)
			if err != nil {
				return "", err
			}
			defer func() { _ = sess.Close() }()

			raw, contentType, err := sess.FetchAttachment(ctx, uid, in.Filename)
			if err != nil {
				return "", err
			}
			// Best-effort: the raw bytes exist here before markitdown
			// conversion discards them — emit them as media too, but a
			// failure (unconfigured, unsupported mime, oversize) must
			// never fail the tool; the markdown conversion below is the
			// call's actual contract.
			if collector := tools.CollectorFrom(ctx); collector != nil {
				_, _ = collector.Emit(ctx, in.Filename, bytes.NewReader(raw))
			}
			return markitdown.Convert(ctx, s.client, s.markItDownURL, in.Filename, contentType, raw)
		},
	}
}

func (s *imapSource) mailSend() *tools.Tool {
	return &tools.Tool{
		Name:        "mail_send",
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
			if err := rejectHeaderInjection(in.To, in.Subject, in.CC); err != nil {
				return "", err
			}

			pw, err := s.password(ctx)
			if err != nil {
				return "", err
			}

			toAddrs, err := parseAddressList(in.To)
			if err != nil {
				return "", fmt.Errorf("to: %w", err)
			}
			if len(toAddrs) == 0 {
				return "", fmt.Errorf("to must contain at least one address")
			}
			ccAddrs, err := parseAddressList(in.CC)
			if err != nil {
				return "", fmt.Errorf("cc: %w", err)
			}

			msg, err := buildRFC822Message(s.cfg.email(), toAddrs, ccAddrs, in.Subject, in.Body)
			if err != nil {
				return "", err
			}
			recipients := make([]string, 0, len(toAddrs)+len(ccAddrs))
			for _, a := range append(toAddrs, ccAddrs...) {
				recipients = append(recipients, a.Address)
			}
			if err := s.send(ctx, s.cfg, pw, recipients, msg); err != nil {
				return "", err
			}
			return "sent", nil
		},
	}
}

// parseIMAPUID parses a mail_search-returned id string back into a UID.
func parseIMAPUID(id string) (imap.UID, error) {
	n, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid message id %q", id)
	}
	return imap.UID(n), nil
}

// parseAddressList parses a comma-separated address list (optionally
// with display names, including a quoted display name containing a
// comma) via net/mail; empty input returns an empty, non-nil slice
// rather than erroring, so an omitted cc list is fine. Any unparseable
// entry errors clearly instead of silently sending to a mangled
// address.
func parseAddressList(addrs string) ([]*mail.Address, error) {
	if strings.TrimSpace(addrs) == "" {
		return nil, nil
	}
	list, err := mail.ParseAddressList(addrs)
	if err != nil {
		return nil, fmt.Errorf("invalid address list %q: %w", addrs, err)
	}
	return list, nil
}

// rejectHeaderInjection refuses any header-bound field carrying a raw
// newline: RFC822 headers are single-line, and a newline in a value
// otherwise let a caller inject extra headers or SMTP commands.
func rejectHeaderInjection(fields ...string) error {
	for _, f := range fields {
		if strings.ContainsAny(f, "\r\n") {
			return fmt.Errorf("header value must not contain newlines")
		}
	}
	return nil
}

// buildRFC822Message assembles a plain-text RFC822 message: From, To,
// Cc (when present), Subject, Date, Message-ID, then the body. To/Cc
// headers use each address's full "Name <addr>" form (mail.Address.
// String); the SMTP envelope's RCPT TO list is built separately from
// the bare addresses (see mailSend's Execute).
func buildRFC822Message(from string, to, cc []*mail.Address, subject, body string) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", joinAddresses(to))
	if len(cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\r\n", joinAddresses(cc))
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", encodeSubject(subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-Id: <%d.timothy@%s>\r\n", time.Now().UnixNano(), addressHost(from))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String()), nil
}

// encodeSubject RFC 2047-encodes subject when it carries any non-ASCII
// rune, so a header stays a single well-formed line; a pure-ASCII
// subject is left as-is.
func encodeSubject(subject string) string {
	for _, r := range subject {
		if r > unicode.MaxASCII {
			return mime.QEncoding.Encode("utf-8", subject)
		}
	}
	return subject
}

// addressHost extracts the domain part of an email address for the
// Message-Id header, falling back to "localhost" for an address with
// no "@".
func addressHost(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i+1 < len(addr) {
		return addr[i+1:]
	}
	return "localhost"
}
