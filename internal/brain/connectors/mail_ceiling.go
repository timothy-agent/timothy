package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// ErrMailCeiling wraps every send an outbound mail ceiling refused.
var ErrMailCeiling = errors.New("outbound mail ceiling")

// mailWindow is the rolling window of the daily send ceiling.
const mailWindow = 24 * time.Hour

// Rejection reasons recorded in mail_sends.reason.
const (
	mailReasonRecipients = "recipients"
	mailReasonDaily      = "daily"
)

// MailCeiling enforces the outbound mail ceilings (D-151): recipients
// per send and sends per account per rolling 24 hours. Every
// connector-kind send passes through Admit before any provider call:
// the unified send_mail tool, Google's destination sends and the imap
// mailbox's channel replies. A nil *MailCeiling admits everything.
// Mailbox sends are for personal and client correspondence: bulk mail
// goes through a dedicated email service connector, never by raising
// these numbers. A ceiling set to 0 is off.
type MailCeiling struct {
	ledger mailLedger
	limits func(ctx context.Context) (perSend, perDay int)
	log    *slog.Logger
	now    func() time.Time
}

// mailLedger is the append-only mail_sends record; a fake satisfies it
// in unit tests.
type mailLedger interface {
	// admit appends an admitted row when the account has fewer than
	// perDay admitted rows after windowStart(now) or perDay is 0, else
	// a daily rejection row; ok false carries when the next slot opens.
	admit(ctx context.Context, connector string, recipients, perDay int, now time.Time) (ok bool, next time.Time, err error)
	reject(ctx context.Context, connector string, recipients int, reason string, now time.Time) error
}

// NewMailCeiling returns the ceiling backed by the mail_sends table;
// limits reads the current ceilings from settings on every send.
func NewMailCeiling(db *pgpool.Pool, limits func(ctx context.Context) (perSend, perDay int), log *slog.Logger) *MailCeiling {
	return &MailCeiling{ledger: pgMailLedger{db: db}, limits: limits, log: log, now: time.Now}
}

// Admit admits one send of recipients addresses from connector, or
// returns an ErrMailCeiling error naming the ceiling it crossed. A
// ledger failure refuses the send too: the ceiling fails closed.
func (c *MailCeiling) Admit(ctx context.Context, connector string, recipients int) error {
	if c == nil {
		return nil
	}
	perSend, perDay := c.limits(ctx)
	now := c.now()
	if err := checkRecipients(recipients, perSend); err != nil {
		c.logRejection(connector, mailReasonRecipients, recipients, perSend)
		if rerr := c.ledger.reject(ctx, connector, recipients, mailReasonRecipients, now); rerr != nil {
			c.log.Warn("outbound mail rejection not recorded", "connector", connector, "error", rerr)
		}
		return err
	}
	ok, next, err := c.ledger.admit(ctx, connector, recipients, perDay, now)
	if err != nil {
		return fmt.Errorf("outbound mail ceiling: %w", err)
	}
	if !ok {
		c.logRejection(connector, mailReasonDaily, recipients, perDay)
		return fmt.Errorf("%w: this account reached its ceiling of %d sends per 24 hours, so the mail was not sent; the next send is possible after %s",
			ErrMailCeiling, perDay, next.UTC().Format(time.RFC3339))
	}
	return nil
}

// admitToolArgs admits one send_mail tool call from its arguments.
func (c *MailCeiling) admitToolArgs(ctx context.Context, connector string, args json.RawMessage) error {
	if c == nil {
		return nil
	}
	var in struct {
		To  string `json:"to"`
		CC  string `json:"cc"`
		BCC string `json:"bcc"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return err
	}
	return c.Admit(ctx, connector, countRecipients(in.To, in.CC, in.BCC))
}

// logRejection never logs addresses, only their count.
func (c *MailCeiling) logRejection(connector, reason string, recipients, ceiling int) {
	c.log.Warn("outbound mail rejected", "connector", connector, "reason", reason, "recipients", recipients, "ceiling", ceiling)
}

// checkRecipients refuses a send over the per-send ceiling; limit 0
// is no ceiling. A refused send is never split into smaller sends.
func checkRecipients(n, limit int) error {
	if limit > 0 && n > limit {
		return fmt.Errorf("%w: %d recipients is over the ceiling of %d recipients per send, so the mail was not sent", ErrMailCeiling, n, limit)
	}
	return nil
}

// countRecipients counts the distinct addresses across comma-separated
// lists, case-insensitively.
func countRecipients(lists ...string) int {
	seen := map[string]bool{}
	for _, list := range lists {
		for _, addr := range splitRecipients(list) {
			seen[strings.ToLower(addr)] = true
		}
	}
	return len(seen)
}

// splitRecipients returns list's addresses via net/mail, which keeps a
// quoted display name holding a comma whole. A list net/mail rejects
// falls back to splitting on commas and semicolons, which can count
// more entries than a provider sends to, never fewer.
func splitRecipients(list string) []string {
	if strings.TrimSpace(list) == "" {
		return nil
	}
	if parsed, err := mail.ParseAddressList(list); err == nil {
		out := make([]string, len(parsed))
		for i, a := range parsed {
			out[i] = a.Address
		}
		return out
	}
	var out []string
	for _, p := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// windowStart is the exclusive lower bound of the rolling window: a
// send exactly 24 hours old no longer counts.
func windowStart(now time.Time) time.Time { return now.Add(-mailWindow) }

type pgMailLedger struct {
	db *pgpool.Pool
}

func (l pgMailLedger) admit(ctx context.Context, connector string, recipients, perDay int, now time.Time) (bool, time.Time, error) {
	db, err := l.db.Get()
	if err != nil {
		return false, time.Time{}, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, time.Time{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serializes one account's sends so two cannot both take the last slot.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('mail_sends:' || $1))`, connector); err != nil {
		return false, time.Time{}, err
	}
	var (
		n      int
		oldest *time.Time
	)
	if err := tx.QueryRow(ctx, `SELECT count(*), min(created_at) FROM mail_sends
		WHERE connector = $1 AND outcome = 'admitted' AND created_at > $2`,
		connector, windowStart(now)).Scan(&n, &oldest); err != nil {
		return false, time.Time{}, err
	}
	ok := perDay == 0 || n < perDay
	outcome, reason := "admitted", ""
	if !ok {
		outcome, reason = "rejected", mailReasonDaily
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mail_sends (connector, outcome, reason, recipient_count, created_at)
		VALUES ($1, $2, $3, $4, $5)`, connector, outcome, reason, recipients, now); err != nil {
		return false, time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, time.Time{}, err
	}
	next := now
	if oldest != nil {
		next = oldest.Add(mailWindow)
	}
	return ok, next, nil
}

func (l pgMailLedger) reject(ctx context.Context, connector string, recipients int, reason string, now time.Time) error {
	db, err := l.db.Get()
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO mail_sends (connector, outcome, reason, recipient_count, created_at)
		VALUES ($1, 'rejected', $2, $3, $4)`, connector, reason, recipients, now)
	return err
}
