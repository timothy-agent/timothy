package channels

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
)

const (
	// emailMessageLimit is one reply mail's cap; replies are not
	// streamed, so it only guards against runaway text.
	emailMessageLimit = 100000
	// emailBatch caps the messages one poll reads; the rest wait for
	// the next poll.
	emailBatch = 20
	// emailPollFloor and emailPollDefault bound the poll interval.
	emailPollFloor   = 30 * time.Second
	emailPollDefault = 60 * time.Second
	// maxMailAttachments and maxMailAttachmentBytes cap what one mail
	// stores.
	maxMailAttachments     = 8
	maxMailAttachmentBytes = 20 << 20
	// mailPerHour caps outbound mail per channel.
	mailPerHour = 30
	// droppedLogEvery spaces the log line for one dropped sender.
	droppedLogEvery = time.Hour
	// maxThreads caps the in-memory thread map of one adapter.
	maxThreads = 256
	// maxReferences caps a reply's References header.
	maxReferences = 20
	// buttonsPrompt introduces buttons rendered as text.
	buttonsPrompt = "Reply with one of: "
)

// errMailBudget is a send refused by the outbound mail cap; callers
// drop the text and never retry.
var errMailBudget = errors.New("email: outbound mail budget for this hour is spent")

// mailbox is the connector surface the email adapter uses; production
// binds it to a *connectors.IMAPMailbox, tests to a fake.
type mailbox struct {
	Address    string
	Newer      func(ctx context.Context, sinceUID uint32, max int) ([]connectors.MailMessage, error)
	LatestUID  func(ctx context.Context) (uint32, error)
	Attachment func(ctx context.Context, uid uint32, filename string) ([]byte, string, error)
	Reply      func(ctx context.Context, to, subject, body, inReplyTo string, references []string) (string, error)
}

func mailboxOf(b *connectors.IMAPMailbox) mailbox {
	return mailbox{Address: b.Address(), Newer: b.Newer, LatestUID: b.LatestUID, Attachment: b.Attachment, Reply: b.Reply}
}

// emailEnv is what email adapters need beyond the channel row. A nil
// mailbox leaves the email kind unavailable; a nil save skips
// attachments.
type emailEnv struct {
	mailbox func(ctx context.Context, connectorID string) (mailbox, error)
	save    func(ctx context.Context, r io.Reader) (attachments.Attachment, error)
	poll    func(ctx context.Context) time.Duration
	store   *Store
	log     *slog.Logger
	now     func() time.Time
}

// openMailbox adapts Manager.IMAPMailbox to the adapter's mailbox.
func openMailbox(open func(ctx context.Context, id string) (*connectors.IMAPMailbox, error)) func(ctx context.Context, id string) (mailbox, error) {
	if open == nil {
		return nil
	}
	return func(ctx context.Context, id string) (mailbox, error) {
		b, err := open(ctx, id)
		if err != nil {
			return mailbox{}, err
		}
		return mailboxOf(b), nil
	}
}

// hourlyBudget is a per-key token bucket of mailPerHour tokens refilled
// evenly over an hour.
type hourlyBudget struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

func newHourlyBudget() *hourlyBudget { return &hourlyBudget{buckets: map[string]*bucket{}} }

func (h *hourlyBudget) allow(key string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.buckets[key]
	if !ok {
		b = &bucket{tokens: mailPerHour, last: now}
		h.buckets[key] = b
	}
	b.tokens = min(mailPerHour, b.tokens+now.Sub(b.last).Hours()*mailPerHour)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// mailBudget is shared by every adapter of a channel: the runner's,
// park pushes' and the outcomes consumer's.
var mailBudget = newHourlyBudget()

// emailAdapter is the email transport over an imap connector: an INBOX
// poll with the last seen UID as the cursor, SMTP replies threaded by
// Message-ID. Mail has no edits or buttons.
type emailAdapter struct {
	channelID   string
	connectorID string
	allow       []string
	env         emailEnv
	budget      *hourlyBudget

	// polled is the last poll time; receive's goroutine only.
	polled time.Time

	mu sync.Mutex
	// threads maps a thread root to where its next reply goes, from the
	// latest inbound mail.
	threads map[string]mailThread
	// dropped maps a hashed sender to its last drop log.
	dropped map[string]time.Time
}

func newEmailAdapter(c Channel, env emailEnv) *emailAdapter {
	if env.now == nil {
		env.now = time.Now
	}
	return &emailAdapter{channelID: c.ID, connectorID: c.Config.ConnectorID, allow: c.Config.FromAllow, env: env, budget: mailBudget,
		threads: map[string]mailThread{}, dropped: map[string]time.Time{}}
}

func (a *emailAdapter) open(ctx context.Context) (mailbox, error) {
	return a.env.mailbox(ctx, a.connectorID)
}

func (a *emailAdapter) connect(ctx context.Context) (identity, error) {
	box, err := a.open(ctx)
	if err != nil {
		return identity{}, err
	}
	if _, err := box.LatestUID(ctx); err != nil {
		return identity{}, redactAddress(err, box.Address)
	}
	return identity{ID: box.Address, Username: box.Address}, nil
}

// wait sleeps out the poll interval since the last poll; the first
// poll runs at once.
func (a *emailAdapter) wait(ctx context.Context) error {
	if !a.polled.IsZero() {
		every := emailPollDefault
		if a.env.poll != nil {
			every = a.env.poll(ctx)
		}
		if d := every - a.env.now().Sub(a.polled); d > 0 {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
			}
		}
	}
	a.polled = a.env.now()
	return nil
}

// receive polls INBOX for mail above the cursor UID. First sight (no
// cursor) stores the latest UID and returns nothing: no backfill.
// Mail from the mailbox itself or from senders off the allowlist is
// dropped before the pipeline.
func (a *emailAdapter) receive(ctx context.Context, cursor string) ([]inbound, string, error) {
	if err := a.wait(ctx); err != nil {
		return nil, cursor, err
	}
	box, err := a.open(ctx)
	if err != nil {
		return nil, cursor, err
	}
	if cursor == "" {
		latest, err := box.LatestUID(ctx)
		if err != nil {
			return nil, cursor, redactAddress(err, box.Address)
		}
		return nil, strconv.FormatUint(uint64(latest), 10), nil
	}
	since, err := strconv.ParseUint(cursor, 10, 32)
	if err != nil {
		return nil, cursor, fmt.Errorf("email cursor %q: %w", cursor, err)
	}
	msgs, err := box.Newer(ctx, uint32(since), emailBatch)
	if err != nil {
		return nil, cursor, redactAddress(err, box.Address)
	}
	next := uint32(since)
	own := strings.ToLower(box.Address)
	var items []inbound
	for _, m := range msgs {
		next = max(next, m.UID)
		from := strings.ToLower(strings.TrimSpace(m.FromAddress))
		if from == "" || from == own {
			continue
		}
		if !fromAllowed(a.allow, from) {
			a.logDropped(from)
			continue
		}
		in := mailInbound(m)
		var skipped []string
		in.Attachments, skipped = a.saveAttachments(ctx, box, m)
		if len(skipped) > 0 {
			in.Text = strings.TrimSpace(in.Text + "\n\nSkipped attachments: " + strings.Join(skipped, ", "))
		}
		a.remember(in.ChatID, mailThread{To: from, Subject: m.Subject, MessageID: in.MessageID, References: replyReferences(m, in.MessageID)})
		items = append(items, in)
	}
	return items, strconv.FormatUint(uint64(next), 10), nil
}

// saveAttachments stores up to maxMailAttachments attachments of at
// most maxMailAttachmentBytes; the rest, and any the store refuses,
// come back as skipped names.
func (a *emailAdapter) saveAttachments(ctx context.Context, box mailbox, m connectors.MailMessage) ([]chat.AttachmentRef, []string) {
	var refs []chat.AttachmentRef
	var skipped []string
	for _, att := range m.Attachments {
		if a.env.save == nil || len(refs) == maxMailAttachments || att.Size > maxMailAttachmentBytes {
			skipped = append(skipped, att.Filename)
			continue
		}
		raw, _, err := box.Attachment(ctx, m.UID, att.Filename)
		if err != nil || len(raw) > maxMailAttachmentBytes {
			skipped = append(skipped, att.Filename)
			continue
		}
		saved, err := a.env.save(ctx, bytes.NewReader(raw))
		if err != nil {
			skipped = append(skipped, att.Filename)
			continue
		}
		refs = append(refs, chat.AttachmentRef{ID: saved.ID, Name: att.Filename})
	}
	return refs, skipped
}

// logDropped logs a dropped sender at most once per droppedLogEvery,
// by hash only.
func (a *emailAdapter) logDropped(from string) {
	sum := sha256.Sum256([]byte(from))
	h := hex.EncodeToString(sum[:6])
	a.mu.Lock()
	now := a.env.now()
	last, seen := a.dropped[h]
	logIt := !seen || now.Sub(last) >= droppedLogEvery
	if logIt {
		if len(a.dropped) >= maxThreads {
			clear(a.dropped)
		}
		a.dropped[h] = now
	}
	a.mu.Unlock()
	if logIt && a.env.log != nil {
		a.env.log.Info("channels: email from a sender off the allowlist dropped", "channel_id", a.channelID, "sender_hash", h)
	}
}

func (a *emailAdapter) remember(chatID string, th mailThread) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.threads[chatID]; !ok && len(a.threads) >= maxThreads {
		for k := range a.threads {
			delete(a.threads, k)
			break
		}
	}
	a.threads[chatID] = th
}

func (a *emailAdapter) thread(chatID string) (mailThread, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	th, ok := a.threads[chatID]
	return th, ok
}

// send mails text as a reply in the thread of to.ChatID: the latest
// inbound mail this adapter saw there, else the conversation's stored
// thread. The outgoing Message-ID is the message id. Buttons arrive
// already rendered as text.
func (a *emailAdapter) send(ctx context.Context, to target, text string, _ [][]button, _ bool) (string, error) {
	conv, found, err := a.env.store.ConversationFor(ctx, a.channelID, to.ChatID, "")
	if err != nil {
		return "", err
	}
	th, ok := a.thread(to.ChatID)
	if !ok {
		if !found {
			return "", fmt.Errorf("email: no thread to reply to")
		}
		if th, err = a.env.store.emailThread(ctx, conv); err != nil {
			return "", err
		}
	}
	if !a.budget.allow(a.channelID, a.env.now()) {
		return "", errMailBudget
	}
	box, err := a.open(ctx)
	if err != nil {
		return "", err
	}
	id, err := box.Reply(ctx, th.To, replySubject(th.Subject), text, headerID(th.MessageID), th.References)
	if err != nil {
		return "", redactAddress(redactAddress(err, th.To), box.Address)
	}
	if found {
		if err := a.env.store.saveEmailThread(ctx, conv.ID, th, id); err != nil && a.env.log != nil {
			a.env.log.Warn("channels: save email thread failed", "channel_id", a.channelID, "conversation_id", conv.ID, "error", err)
		}
	}
	return id, nil
}

// edit is a no-op: sent mail cannot change.
func (*emailAdapter) edit(context.Context, target, string, string, [][]button) error { return nil }

func (*emailAdapter) answerPress(context.Context, string, string) error { return nil }

func (*emailAdapter) caps() capabilities {
	return capabilities{MessageLimit: emailMessageLimit}
}

// fromAllowed matches a lowercased sender against the allowlist: a
// full address exactly, an @domain entry by suffix.
func fromAllowed(allow []string, from string) bool {
	for _, e := range allow {
		if strings.HasPrefix(e, "@") {
			if strings.HasSuffix(from, e) {
				return true
			}
		} else if from == e {
			return true
		}
	}
	return false
}

// mailID is a mail's Message-ID, or uid:<n> for a mail without one.
func mailID(m connectors.MailMessage) string {
	return cmp.Or(m.MessageID, "uid:"+strconv.FormatUint(uint64(m.UID), 10))
}

// threadRoot is the conversation a mail belongs to: the first
// References entry, else In-Reply-To, else the mail itself.
func threadRoot(m connectors.MailMessage) string {
	if len(m.References) > 0 {
		return m.References[0]
	}
	return cmp.Or(m.InReplyTo, mailID(m))
}

// headerID drops the uid: fallback, which is no real Message-ID.
func headerID(id string) string {
	if strings.HasPrefix(id, "uid:") {
		return ""
	}
	return id
}

// replyReferences is a reply's References: the mail's own plus its
// Message-ID, the newest maxReferences kept.
func replyReferences(m connectors.MailMessage, id string) []string {
	refs := append([]string(nil), m.References...)
	if id = headerID(id); id != "" {
		refs = append(refs, id)
	}
	if len(refs) > maxReferences {
		refs = refs[len(refs)-maxReferences:]
	}
	return refs
}

var rePrefix = regexp.MustCompile(`(?i)^re:`)

func replySubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "Re: Timothy"
	}
	if rePrefix.MatchString(subject) {
		return subject
	}
	return "Re: " + subject
}

// mailInbound maps a mail onto an inbound private message.
func mailInbound(m connectors.MailMessage) inbound {
	id := mailID(m)
	text := replyText(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Subject)
	}
	from := strings.ToLower(strings.TrimSpace(m.FromAddress))
	return inbound{
		DedupID:     "email:" + id,
		MessageID:   id,
		UserID:      from,
		DisplayName: cmp.Or(strings.TrimSpace(m.FromName), from),
		ChatID:      threadRoot(m),
		Private:     true,
		Addressed:   true,
		Text:        text,
		ReplyToID:   m.InReplyTo,
	}
}

var wroteLine = regexp.MustCompile(`(?i)^on\s.+wrote:$`)

// replyText strips what a mail client adds to a reply: quoted lines,
// the "On ... wrote:" attribution, and everything from the signature
// or an "Original Message" marker on.
func replyText(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		if l == "-- " || strings.HasPrefix(t, "-----Original Message-----") {
			break
		}
		if strings.HasPrefix(t, ">") || wroteLine.MatchString(t) {
			continue
		}
		// Clients wrap a long attribution over two lines.
		if strings.HasPrefix(strings.ToLower(t), "on ") && i+1 < len(lines) && strings.HasSuffix(strings.TrimSpace(lines[i+1]), "wrote:") {
			i++
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// buttonsText renders buttons as a reply hint for transports without
// them.
func buttonsText(buttons [][]button) string {
	var names []string
	for _, row := range buttons {
		for _, b := range row {
			names = append(names, b.Text)
		}
	}
	return "\n\n" + buttonsPrompt + strings.Join(names, ", ")
}

// redactAddress removes addr, in any case, from err's text.
func redactAddress(err error, addr string) error {
	if err == nil || addr == "" {
		return err
	}
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(addr))
	if !re.MatchString(err.Error()) {
		return err
	}
	return errors.New(re.ReplaceAllString(err.Error(), "REDACTED"))
}
