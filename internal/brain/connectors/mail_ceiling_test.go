package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// fakeMailLedger keeps admitted send times in memory and counts them
// with the same windowStart bound the mail_sends query uses.
type fakeMailLedger struct {
	admitted []time.Time
	rejects  []string // reasons
	admits   int      // admit calls
	err      error
}

func (f *fakeMailLedger) admit(_ context.Context, _ string, _, perDay int, now time.Time) (bool, time.Time, error) {
	f.admits++
	if f.err != nil {
		return false, time.Time{}, f.err
	}
	var in []time.Time
	for _, at := range f.admitted {
		if at.After(windowStart(now)) {
			in = append(in, at)
		}
	}
	if perDay > 0 && len(in) >= perDay {
		f.rejects = append(f.rejects, mailReasonDaily)
		return false, in[0].Add(mailWindow), nil
	}
	f.admitted = append(f.admitted, now)
	return true, time.Time{}, nil
}

func (f *fakeMailLedger) reject(_ context.Context, _ string, _ int, reason string, _ time.Time) error {
	f.rejects = append(f.rejects, reason)
	return f.err
}

func testMailCeiling(ledger mailLedger, perSend, perDay int, now func() time.Time, log *slog.Logger) *MailCeiling {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if now == nil {
		now = time.Now
	}
	return &MailCeiling{ledger: ledger, log: log, now: now,
		limits: func(context.Context) (int, int) { return perSend, perDay }}
}

func TestCountRecipients(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		lists []string
		want  int
	}{
		{"empty", []string{"", "  "}, 0},
		{"one", []string{"a@x.com"}, 1},
		{"spaces", []string{"  a@x.com ,   b@x.com  "}, 2},
		{"trailing comma", []string{"a@x.com, b@x.com,"}, 2},
		{"empty entries", []string{",, a@x.com ,,"}, 1},
		{"across to and cc", []string{"a@x.com, b@x.com, c@x.com", "d@x.com, e@x.com, f@x.com"}, 6},
		{"to cc and bcc", []string{"a@x.com", "b@x.com", "c@x.com"}, 3},
		{"quoted display name with comma", []string{`"Doe, Jane" <jane@x.com>, bob@x.com`}, 2},
		{"display names", []string{"Jane <jane@x.com>, Bob <bob@x.com>"}, 2},
		{"duplicate in one list", []string{"a@x.com, a@x.com"}, 1},
		{"duplicate across lists, case-insensitive", []string{"a@x.com", "A@X.com"}, 1},
		{"semicolons in an unparseable list", []string{"a@x.com; b@x.com; c@x.com"}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := countRecipients(tc.lists...); got != tc.want {
				t.Fatalf("countRecipients(%q) = %d, want %d", tc.lists, got, tc.want)
			}
		})
	}
}

func TestCheckRecipients(t *testing.T) {
	t.Parallel()
	cases := []struct {
		n, limit int
		wantErr  bool
	}{
		{0, 5, false}, {1, 5, false}, {5, 5, false}, {6, 5, true}, {21, 20, true}, {20, 20, false},
		{1000, 0, false}, // 0 is no ceiling
		{1000, 999, true}, {999, 999, false},
	}
	for _, tc := range cases {
		err := checkRecipients(tc.n, tc.limit)
		if (err != nil) != tc.wantErr {
			t.Fatalf("checkRecipients(%d, %d) = %v, wantErr %v", tc.n, tc.limit, err, tc.wantErr)
		}
		if err != nil && (!errors.Is(err, ErrMailCeiling) || !strings.Contains(err.Error(), fmt.Sprintf("ceiling of %d recipients", tc.limit))) {
			t.Fatalf("checkRecipients(%d, %d) = %v, want ErrMailCeiling naming the ceiling", tc.n, tc.limit, err)
		}
	}
}

func TestWindowStart(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		sentAt time.Time
		counts bool
	}{
		{"just now", now, true},
		{"23h59m ago", now.Add(-23*time.Hour - 59*time.Minute), true},
		{"exactly 24h ago", now.Add(-24 * time.Hour), false},
		{"25h ago", now.Add(-25 * time.Hour), false},
	}
	for _, tc := range cases {
		if got := tc.sentAt.After(windowStart(now)); got != tc.counts {
			t.Errorf("%s: counts = %v, want %v", tc.name, got, tc.counts)
		}
	}
}

// TestMailCeilingRollingWindow: the daily ceiling refuses the send
// after perDay admitted sends and admits again once the oldest ages
// past 24 hours.
func TestMailCeilingRollingWindow(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	now := t0
	ledger := &fakeMailLedger{}
	c := testMailCeiling(ledger, 5, 2, func() time.Time { return now }, nil)
	ctx := t.Context()

	steps := []struct {
		at      time.Time
		wantErr bool
	}{
		{t0, false},
		{t0.Add(time.Hour), false},
		{t0.Add(2 * time.Hour), true},
		{t0.Add(24*time.Hour - time.Second), true},
		{t0.Add(24 * time.Hour), false},            // t0 aged out
		{t0.Add(24*time.Hour + time.Minute), true}, // t0+1h and t0+24h fill it
		{t0.Add(25 * time.Hour), false},            // t0+1h aged out
	}
	for i, s := range steps {
		now = s.at
		err := c.Admit(ctx, "mail", 1)
		if (err != nil) != s.wantErr {
			t.Fatalf("step %d at %s: err = %v, wantErr %v", i, s.at, err, s.wantErr)
		}
		if err != nil && (!errors.Is(err, ErrMailCeiling) || !strings.Contains(err.Error(), "ceiling of 2 sends per 24 hours")) {
			t.Fatalf("step %d: err = %v, want the daily ceiling named", i, err)
		}
	}
	if err := c.Admit(ctx, "mail", 1); err == nil {
		t.Fatal("want a rejection")
	} else if !strings.Contains(err.Error(), t0.Add(48*time.Hour).Format(time.RFC3339)) {
		t.Fatalf("err = %v, want the next slot at %s", err, t0.Add(48*time.Hour).Format(time.RFC3339))
	}
}

// TestMailCeilingRecipientRejection: over the per-send ceiling is
// refused without touching the daily count, recorded with its reason,
// and logged without addresses.
func TestMailCeilingRecipientRejection(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	ledger := &fakeMailLedger{}
	c := testMailCeiling(ledger, 5, 50, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	args := json.RawMessage(`{"to":"a@x.com, b@x.com, c@x.com","cc":"d@x.com, e@x.com, f@x.com","subject":"s","body":"b"}`)
	err := c.admitToolArgs(t.Context(), "work", args)
	if !errors.Is(err, ErrMailCeiling) || !strings.Contains(err.Error(), "6 recipients") || !strings.Contains(err.Error(), "ceiling of 5") {
		t.Fatalf("err = %v, want the per-send ceiling of 5 named", err)
	}
	if ledger.admits != 0 || len(ledger.rejects) != 1 || ledger.rejects[0] != mailReasonRecipients {
		t.Fatalf("ledger admits=%d rejects=%v, want 0 and [recipients]", ledger.admits, ledger.rejects)
	}
	if strings.Contains(logs.String(), "@") || !strings.Contains(logs.String(), "connector=work") || !strings.Contains(logs.String(), "recipients=6") {
		t.Fatalf("log = %q, want connector and count, no addresses", logs.String())
	}
}

// TestMailCeilingZeroDisables: a ceiling set to 0 is off, while the
// sends are still recorded.
func TestMailCeilingZeroDisables(t *testing.T) {
	t.Parallel()
	ledger := &fakeMailLedger{}
	c := testMailCeiling(ledger, 0, 0, nil, nil)
	for i := range 100 {
		if err := c.Admit(t.Context(), "work", 200); err != nil {
			t.Fatalf("send %d: %v", i+1, err)
		}
	}
	if len(ledger.admitted) != 100 || len(ledger.rejects) != 0 {
		t.Fatalf("admitted=%d rejects=%v, want 100 and none", len(ledger.admitted), ledger.rejects)
	}
	// Only the off ceiling is off: the other one still applies.
	if err := testMailCeiling(&fakeMailLedger{}, 5, 0, nil, nil).Admit(t.Context(), "work", 6); !errors.Is(err, ErrMailCeiling) {
		t.Fatalf("per-send ceiling with daily off: err = %v, want ErrMailCeiling", err)
	}
	if err := testMailCeiling(&fakeMailLedger{}, 0, 1, nil, nil).Admit(t.Context(), "work", 500); err != nil {
		t.Fatalf("recipients off, first send of the day: %v", err)
	}
}

func TestMailCeilingFailsClosed(t *testing.T) {
	t.Parallel()
	c := testMailCeiling(&fakeMailLedger{err: errors.New("db down")}, 5, 50, nil, nil)
	if err := c.Admit(t.Context(), "work", 1); err == nil || errors.Is(err, ErrMailCeiling) {
		t.Fatalf("err = %v, want the ledger error", err)
	}
	var nilCeiling *MailCeiling
	if err := nilCeiling.Admit(t.Context(), "work", 99); err != nil {
		t.Fatalf("nil ceiling: %v", err)
	}
}

// TestAggregateSendMailCeiling: the unified send_mail rejects six
// recipients across to and cc before the provider tool runs, admits
// five, and leaves other tools ungated.
func TestAggregateSendMailCeiling(t *testing.T) {
	t.Parallel()
	calls := 0
	provider := &tools.Tool{Name: "send_mail", Execute: func(context.Context, json.RawMessage) (string, error) {
		calls++
		return "sent", nil
	}}
	ledger := &fakeMailLedger{}
	c := testMailCeiling(ledger, 5, 50, nil, nil)
	send := aggregateTool("send_mail", []toolAccount{{connector: "work", kind: "imap", tool: provider}}, c)

	_, err := send.Execute(t.Context(), json.RawMessage(`{"to":"a@x.com,b@x.com,c@x.com","cc":"d@x.com,e@x.com,f@x.com","subject":"s","body":"b"}`))
	if !errors.Is(err, ErrMailCeiling) || !strings.Contains(err.Error(), "ceiling of 5") || calls != 0 {
		t.Fatalf("six recipients: err=%v provider calls=%d, want the ceiling of 5 and no provider call", err, calls)
	}
	if out, err := send.Execute(t.Context(), json.RawMessage(`{"to":"a@x.com,b@x.com,c@x.com","cc":"d@x.com,e@x.com","subject":"s","body":"b"}`)); err != nil || out != "sent" || calls != 1 {
		t.Fatalf("five recipients: out=%q err=%v calls=%d", out, err, calls)
	}
	if len(ledger.admitted) != 1 {
		t.Fatalf("admitted = %d, want 1", len(ledger.admitted))
	}

	other := aggregateTool("search_mail", []toolAccount{{connector: "work", kind: "imap", tool: &tools.Tool{Name: "search_mail",
		Execute: func(context.Context, json.RawMessage) (string, error) { return "found", nil }}}}, testMailCeiling(&fakeMailLedger{err: errors.New("db down")}, 0, 0, nil, nil))
	if out, err := other.Execute(t.Context(), json.RawMessage(`{"to":"a@x.com,b@x.com"}`)); err != nil || out != "found" {
		t.Fatalf("search_mail gated: out=%q err=%v", out, err)
	}
}

// TestGoogleDirectSendsCeiling: destinations' Gmail sends hit the
// ceiling before any token refresh or API call.
func TestGoogleDirectSendsCeiling(t *testing.T) {
	t.Parallel()
	sends := map[string]func(ctx context.Context, g *Google, id, to string) error{
		"SendMail": func(ctx context.Context, g *Google, id, to string) error { return g.SendMail(ctx, id, to, "s", "b") },
		"SendMailWithAttachments": func(ctx context.Context, g *Google, id, to string) error {
			return g.SendMailWithAttachments(ctx, id, to, "s", "b", []Attachment{{Name: "a.txt", Data: []byte("a")}})
		},
		"SendMailHTML": func(ctx context.Context, g *Google, id, to string) error {
			return g.SendMailHTML(ctx, id, to, "s", "b", "<p>b</p>", nil)
		},
	}
	for name, send := range sends {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := &fakeGoogle{}
			row := googleRow(bothScopes)
			g, secrets := testGoogle(t, f, row)
			//nolint:gosec // G117: fake token fixture.
			live, _ := json.Marshal(tokenBundle{AccessToken: "at-live", Expiry: time.Now().Add(time.Hour)})
			_ = secrets.Set(t.Context(), "PERSONAL_GOOGLE_OAUTH", string(live))
			g.MailCeiling = testMailCeiling(&fakeMailLedger{}, 1, 1, nil, nil)

			if err := send(t.Context(), g, row.ID, "a@x.com, b@x.com"); !errors.Is(err, ErrMailCeiling) {
				t.Fatalf("two recipients: err = %v, want ErrMailCeiling", err)
			}
			if err := send(t.Context(), g, row.ID, "a@x.com"); err != nil {
				t.Fatalf("one recipient: %v", err)
			}
			if err := send(t.Context(), g, row.ID, "a@x.com"); !errors.Is(err, ErrMailCeiling) {
				t.Fatalf("second send of the day: err = %v, want ErrMailCeiling", err)
			}
			if len(f.gmailSent) != 1 {
				t.Fatalf("gmail sends = %d, want 1", len(f.gmailSent))
			}
		})
	}
}

// TestIMAPMailboxReplyCeiling: channel replies count toward the daily
// ceiling and stop before SMTP once it is reached.
func TestIMAPMailboxReplyCeiling(t *testing.T) {
	t.Parallel()
	b, sent := testMailbox(t, &fakeIMAPSession{})
	b.connector, b.mail = "inbox", testMailCeiling(&fakeMailLedger{}, 5, 1, nil, nil)
	if _, err := b.Reply(t.Context(), "ada@x.com", "s", "b", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reply(t.Context(), "ada@x.com", "s", "b", "", nil, nil); !errors.Is(err, ErrMailCeiling) {
		t.Fatalf("second reply: err = %v, want ErrMailCeiling", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("SMTP sends = %d, want 1", len(*sent))
	}
}
