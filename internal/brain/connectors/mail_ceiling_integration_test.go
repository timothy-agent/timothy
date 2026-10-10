//go:build integration

package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// TestMailCeilingDailyAcrossRestart drives send_mail through a fake
// provider past the daily ceiling with the pool and ceiling rebuilt in
// between (a brain restart): sends before the restart still count,
// rejections land in mail_sends, and a send succeeds again once the
// oldest admitted send is 24 hours old.
func TestMailCeilingDailyAcrossRestart(t *testing.T) {
	_, pool := testStore(t)
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	connector := fmt.Sprintf("%smail-%d", marker, time.Now().UnixNano())
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		_, _ = conn.Exec(cctx, `DELETE FROM mail_sends WHERE connector = $1`, connector)
	})

	now := time.Now()
	limits := func(context.Context) (int, int) { return 5, 3 }
	open := func(p *pgpool.Pool) *MailCeiling {
		c := NewMailCeiling(p, limits, log)
		c.now = func() time.Time { return now }
		return c
	}
	calls := 0
	provider := &tools.Tool{Name: "send_mail", Execute: func(context.Context, json.RawMessage) (string, error) {
		calls++
		return "sent", nil
	}}
	send := func(c *MailCeiling, args string) error {
		_, err := aggregateTool("send_mail", []toolAccount{{connector: connector, kind: "imap", tool: provider}}, c).
			Execute(ctx, json.RawMessage(args))
		return err
	}
	one := `{"to":"ada@x.com","subject":"s","body":"b"}`

	c := open(pool)
	for i := range 2 {
		if err := send(c, one); err != nil {
			t.Fatalf("send %d: %v", i+1, err)
		}
	}

	// Restart: a fresh pool and ceiling over the same database.
	pool2 := pgpool.New(ctx, os.Getenv("DATABASE_URL"), log)
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pool2.WaitHealthy(wctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	c = open(pool2)
	if err := send(c, one); err != nil {
		t.Fatalf("send 3 after restart: %v", err)
	}
	if err := send(c, one); !errors.Is(err, ErrMailCeiling) {
		t.Fatalf("send 4 = %v, want the daily ceiling", err)
	}
	six := `{"to":"a@x.com,b@x.com,c@x.com","cc":"d@x.com,e@x.com,f@x.com","subject":"s","body":"b"}`
	if err := send(c, six); !errors.Is(err, ErrMailCeiling) {
		t.Fatalf("six recipients = %v, want the per-send ceiling", err)
	}
	if calls != 3 {
		t.Fatalf("provider calls = %d, want 3", calls)
	}

	db, err := pool2.Get()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(ctx, `SELECT reason, recipient_count FROM mail_sends
		WHERE connector = $1 AND outcome = 'rejected' ORDER BY created_at, reason`, connector)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var reason string
		var n int
		if err := rows.Scan(&reason, &n); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s:%d", reason, n))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"daily:1", "recipients:6"}) {
		t.Fatalf("rejections = %v, want [daily:1 recipients:6]", got)
	}

	now = now.Add(24*time.Hour + time.Second)
	if err := send(c, one); err != nil {
		t.Fatalf("send after the oldest aged out: %v", err)
	}
	if calls != 4 {
		t.Fatalf("provider calls = %d, want 4", calls)
	}
}
