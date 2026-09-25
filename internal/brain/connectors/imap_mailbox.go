package connectors

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-imap/v2"
	"golang.org/x/net/html"

	"github.com/SumonMSelim/timothy/internal/platform/markitdown"
)

// MailAttachment names one attachment of a MailMessage.
type MailAttachment struct {
	Filename    string
	ContentType string
	Size        int
}

// MailMessage is one inbox message as the email channel reads it. Ids
// are without angle brackets.
type MailMessage struct {
	UID         uint32
	MessageID   string
	InReplyTo   string
	References  []string
	FromAddress string
	FromName    string
	Subject     string
	Date        string
	Text        string
	Attachments []MailAttachment
	// Raw Auto-Submitted, Precedence and List-Id header values.
	AutoSubmitted, Precedence, ListID string
}

// IMAPMailbox reads an imap connector's INBOX and replies over its
// SMTP server for the email channel (issue #830). Every call opens and
// closes its own IMAP session.
type IMAPMailbox struct {
	src *imapSource
}

// IMAPMailbox returns the mailbox of connector id, which must be an
// enabled imap connector with SMTP configured.
func (m *Manager) IMAPMailbox(ctx context.Context, id string) (*IMAPMailbox, error) {
	c, err := m.rows.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Kind != "imap" {
		return nil, fmt.Errorf("connector %s is kind %s, not imap: %w", c.Name, c.Kind, ErrUnsupported)
	}
	if !c.Enabled {
		return nil, fmt.Errorf("connector %s is disabled", c.Name)
	}
	b, ok := m.builders[c.Kind]
	if !ok {
		return nil, fmt.Errorf("connector kind %s has no builder yet: %w", c.Kind, ErrUnsupported)
	}
	src, err := b(ctx, c, m.resolve)
	if err != nil {
		return nil, err
	}
	is, ok := src.(*imapSource)
	if !ok {
		_ = src.Close()
		return nil, fmt.Errorf("connector %s: imap builder returned %T: %w", c.Name, src, ErrUnsupported)
	}
	if is.cfg.SMTPHost == "" {
		return nil, fmt.Errorf("connector %s has no smtp_host; the email channel needs it to reply", c.Name)
	}
	return &IMAPMailbox{src: is}, nil
}

// Address is the mailbox's own email address.
func (b *IMAPMailbox) Address() string { return b.src.cfg.email() }

// LatestUID returns the highest INBOX UID, the no-backfill cursor.
func (b *IMAPMailbox) LatestUID(ctx context.Context) (uint32, error) {
	sess, err := b.src.dial(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = sess.Close() }()
	uid, err := sess.LatestUID(ctx)
	return uint32(uid), err
}

// Newer returns up to max messages above sinceUID, oldest first. A
// message that fails to parse comes back with only its UID set so the
// cursor can pass it.
func (b *IMAPMailbox) Newer(ctx context.Context, sinceUID uint32, max int) ([]MailMessage, error) {
	sess, err := b.src.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	uids, err := sess.UIDsAfter(ctx, imap.UID(sinceUID))
	if err != nil {
		return nil, err
	}
	if len(uids) > max {
		uids = uids[:max]
	}
	out := make([]MailMessage, 0, len(uids))
	for _, uid := range uids {
		m, err := sess.FetchMessage(ctx, uid)
		if err != nil {
			out = append(out, MailMessage{UID: uint32(uid)})
			continue
		}
		out = append(out, b.mailMessage(ctx, uint32(uid), m))
	}
	return out, nil
}

func (b *IMAPMailbox) mailMessage(ctx context.Context, uid uint32, m imapMessage) MailMessage {
	text := m.Body
	if m.BodyHTML {
		text = b.htmlText(ctx, m.Body)
	}
	out := MailMessage{
		UID: uid, MessageID: m.MessageID, InReplyTo: m.InReplyTo, References: m.References,
		FromAddress: m.FromAddress, FromName: m.FromName, Subject: m.Subject, Date: m.Date, Text: text,
		AutoSubmitted: m.AutoSubmitted, Precedence: m.Precedence, ListID: m.ListID,
	}
	for _, a := range m.Attachments {
		out.Attachments = append(out.Attachments, MailAttachment(a))
	}
	return out
}

// htmlText converts an HTML body with markitdown when configured, else
// strips the tags.
func (b *IMAPMailbox) htmlText(ctx context.Context, body string) string {
	if b.src.markItDownURL != "" {
		if md, err := markitdown.Convert(ctx, b.src.client, b.src.markItDownURL, "body.html", "text/html", []byte(body)); err == nil {
			return md
		}
	}
	return stripHTML(body)
}

// stripHTML returns an HTML document's text, skipping script and style.
func stripHTML(body string) string {
	z := html.NewTokenizer(strings.NewReader(body))
	var b strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			if z.Err() != io.EOF {
				return body
			}
			return strings.TrimSpace(b.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style":
				skip++
			case "br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6":
				b.WriteString("\n")
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); (string(name) == "script" || string(name) == "style") && skip > 0 {
				skip--
			}
		case html.TextToken:
			if skip == 0 {
				b.Write(bytes.TrimLeft(z.Text(), "\r\n"))
			}
		}
	}
}

// Attachment returns one attachment's bytes and content type.
func (b *IMAPMailbox) Attachment(ctx context.Context, uid uint32, filename string) ([]byte, string, error) {
	sess, err := b.src.dial(ctx)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = sess.Close() }()
	return sess.FetchAttachment(ctx, imap.UID(uid), filename)
}

// Reply sends a plain-text mail with optional file attachments to one
// address, threaded under inReplyTo and references, and returns its
// generated Message-ID (without angle brackets). Ids that are not well
// formed are dropped; empty inReplyTo and references start a thread.
func (b *IMAPMailbox) Reply(ctx context.Context, to, subject, body, inReplyTo string, references []string, files []MailFile) (string, error) {
	if err := rejectHeaderInjection(append([]string{to, subject, inReplyTo}, references...)...); err != nil {
		return "", err
	}
	addrs, err := parseAddressList(to)
	if err != nil {
		return "", fmt.Errorf("to: %w", err)
	}
	if len(addrs) != 1 {
		return "", fmt.Errorf("to must be one address")
	}
	if !validMsgID(inReplyTo) {
		inReplyTo = ""
	}
	var refs []string
	for _, r := range references {
		if validMsgID(r) {
			refs = append(refs, r)
		}
	}
	id, err := newMessageID(b.Address())
	if err != nil {
		return "", err
	}
	pw, err := b.src.password(ctx)
	if err != nil {
		return "", err
	}
	msg := assembleRFC822(b.Address(), addrs, nil, subject, body, id, inReplyTo, refs, files)
	if err := b.src.send(ctx, b.src.cfg, pw, []string{addrs[0].Address}, msg); err != nil {
		return "", err
	}
	return id, nil
}

// newMessageID returns a random id-left@host Message-ID.
func newMessageID(from string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("message id: %w", err)
	}
	return hex.EncodeToString(raw[:]) + ".timothy@" + addressHost(from), nil
}

// validMsgID accepts a bracketless message id: non-empty, printable
// ASCII, no brackets or spaces, at most 250 bytes.
func validMsgID(id string) bool {
	if id == "" || len(id) > 250 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c <= ' ' || c > '~' || c == '<' || c == '>' {
			return false
		}
	}
	return true
}
